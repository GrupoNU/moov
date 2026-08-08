package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// notifyEvent is one observation made by the single watcher connection.
type notifyEvent struct {
	At      time.Duration
	Kind    string // STATUS | FETCH | EXPUNGE | MAILBOX | LIST | OVERFLOW
	Mailbox string
	Detail  string
}

// testNotify is the core of spike S2.
//
// Question: can ONE connection watch MANY mailboxes, so the Moov sync engine
// avoids one IMAP connection per folder per user (the fan-out problem)?
//
// Method: create S2/folder1..5, issue a single NOTIFY SET covering the whole
// personal namespace (falling back to an explicit mailbox list if the server
// rejects PERSONAL), then mutate several DIFFERENT folders from a second
// connection and record everything the watcher receives.
//
// Findings recorded: which of MessageNew / MessageExpunge / FlagChange Dovecot
// actually delivers, whether NOTIFY-induced STATUS carries HIGHESTMODSEQ (the
// suspected RFC 5465 violation), and what STATUS actually contains.
func testNotify(cfg *Config) *Result {
	res := newResult("notify")

	folders := []string{"S2/folder1", "S2/folder2", "S2/folder3", "S2/folder4", "S2/folder5"}

	// --- setup: make sure the folders exist ---------------------------------
	setup, err := dialLib(cfg, nil)
	if err != nil {
		return res.fail("setup conn: %v", err)
	}
	for _, f := range folders {
		if err := setup.Create(f, nil).Wait(); err != nil {
			// Already-exists is fine; anything else is not.
			if !strings.Contains(strings.ToLower(err.Error()), "exist") {
				res.note("Create(%s): %v", f, err)
			}
		}
		_ = setup.Subscribe(f).Wait()
	}
	setup.Close()
	res.note("watch set: INBOX + %s", strings.Join(folders, " "))

	// --- watcher connection --------------------------------------------------
	var mu sync.Mutex
	var events []notifyEvent
	t0 := time.Now()

	add := func(e notifyEvent) {
		mu.Lock()
		e.At = time.Since(t0)
		events = append(events, e)
		mu.Unlock()
	}

	// statusDetail renders a NOTIFY-induced STATUS so we can inspect exactly
	// which attributes Dovecot chose to include. HIGHESTMODSEQ is the one that
	// decides whether Moov can resync from the notification alone.
	statusDetail := func(d *imap.StatusData) string {
		var parts []string
		if d.NumMessages != nil {
			parts = append(parts, fmt.Sprintf("MESSAGES=%d", *d.NumMessages))
		}
		if d.UIDNext != 0 {
			parts = append(parts, fmt.Sprintf("UIDNEXT=%d", d.UIDNext))
		}
		if d.UIDValidity != 0 {
			parts = append(parts, fmt.Sprintf("UIDVALIDITY=%d", d.UIDValidity))
		}
		if d.NumUnseen != nil {
			parts = append(parts, fmt.Sprintf("UNSEEN=%d", *d.NumUnseen))
		}
		if d.HighestModSeq != 0 {
			parts = append(parts, fmt.Sprintf("HIGHESTMODSEQ=%d", d.HighestModSeq))
		} else {
			parts = append(parts, "HIGHESTMODSEQ=<absent>")
		}
		return strings.Join(parts, " ")
	}

	opts := &imapclient.Options{
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Status: func(d *imap.StatusData) {
				add(notifyEvent{Kind: "STATUS", Mailbox: d.Mailbox, Detail: statusDetail(d)})
			},
			Mailbox: func(d *imapclient.UnilateralDataMailbox) {
				detail := "-"
				if d.NumMessages != nil {
					detail = fmt.Sprintf("EXISTS=%d", *d.NumMessages)
				}
				add(notifyEvent{Kind: "MAILBOX", Mailbox: "<selected>", Detail: detail})
			},
			Expunge: func(seqNum uint32) {
				add(notifyEvent{Kind: "EXPUNGE", Mailbox: "<selected>", Detail: fmt.Sprintf("seq=%d", seqNum)})
			},
			Fetch: func(m *imapclient.FetchMessageData) {
				buf, err := m.Collect()
				detail := fmt.Sprintf("seq=%d", m.SeqNum)
				if err == nil && buf != nil {
					detail = fmt.Sprintf("seq=%d uid=%d flags=%v modseq=%d",
						buf.SeqNum, buf.UID, buf.Flags, buf.ModSeq)
				}
				add(notifyEvent{Kind: "FETCH", Mailbox: "<selected>", Detail: detail})
			},
			List: func(d *imap.ListData) {
				add(notifyEvent{Kind: "LIST", Mailbox: d.Mailbox, Detail: fmt.Sprintf("%v", d.Attrs)})
			},
			NotificationOverflow: func() {
				add(notifyEvent{Kind: "OVERFLOW", Detail: "server disabled notifications"})
			},
		},
	}

	watcher, err := dialLib(cfg, opts)
	if err != nil {
		return res.fail("watcher conn: %v", err)
	}
	defer watcher.Close()

	// Does the library let NOTIFY and QRESYNC coexist? (Expected: no, because
	// Enable() refuses QRESYNC entirely - recorded as a finding.)
	if _, err := watcher.Enable(imap.CapQResync).Wait(); err != nil {
		res.note("FINDING: cannot ENABLE QRESYNC alongside NOTIFY via the library: %v", err)
	} else {
		res.note("QRESYNC enabled alongside NOTIFY")
	}

	events2 := []imap.NotifyEvent{
		imap.NotifyEventMessageNew,
		imap.NotifyEventMessageExpunge,
		imap.NotifyEventFlagChange,
	}

	// Try the whole personal namespace first: that is what a real sync engine
	// wants (one command, every folder, including ones created later).
	sendNotify := func(opts *imap.NotifyOptions) error {
		cmd, err := watcher.Notify(opts)
		if err != nil {
			return err
		}
		return cmd.Wait()
	}

	// --- library encoding bugs, isolated against raw RFC 5465 syntax --------
	//
	// Two forms the library emits are rejected by Dovecot. Both were confirmed
	// to be CLIENT-side encoding bugs, not server limitations: the equivalent
	// hand-written commands are accepted (see RESULTS.md, T2-NOTIFY-BUGS).
	//
	//  1. Status:true   -> library sends "SET (STATUS) (PERSONAL (...))"
	//                      RFC 5465 wants "SET STATUS (PERSONAL (...))"
	//                      (bare atom, not a parenthesised list)
	//  2. Mailboxes[]   -> library sends "SET ((INBOX) (...))"
	//                      RFC 5465 wants "SET (MAILBOXES (INBOX) (...))"
	//                      (the MAILBOXES keyword is omitted entirely)
	//
	// We probe both so the failure is recorded as evidence, then fall back to
	// the one form that does work: a bare PERSONAL spec without Status.
	if err := sendNotify(&imap.NotifyOptions{
		Status: true,
		Items:  []imap.NotifyItem{{MailboxSpec: imap.NotifyMailboxSpecPersonal, Events: events2}},
	}); err != nil {
		res.note("FINDING (library bug #1): NOTIFY with Status:true rejected: %v", err)
		res.note("  library emits `SET (STATUS) (PERSONAL ...)`; RFC 5465 requires `SET STATUS (PERSONAL ...)`")
	} else {
		res.note("UNEXPECTED: Status:true accepted")
	}

	// Reconnect: the failed command left the connection usable, but start clean.
	mboxes := append([]string{"INBOX"}, folders...)
	if err := sendNotify(&imap.NotifyOptions{
		Items: []imap.NotifyItem{{Mailboxes: mboxes, Events: events2}},
	}); err != nil {
		res.note("FINDING (library bug #2): NOTIFY with explicit Mailboxes rejected: %v", err)
		res.note("  library emits `SET ((INBOX ...) (...))`; RFC 5465 requires `SET (MAILBOXES (INBOX ...) (...))`")
	} else {
		res.note("UNEXPECTED: explicit Mailboxes accepted")
	}

	// The form that actually works on branch-v2 tip.
	notifyMode := "PERSONAL (no STATUS keyword - library cannot emit it)"
	if err := sendNotify(&imap.NotifyOptions{
		Items: []imap.NotifyItem{{MailboxSpec: imap.NotifyMailboxSpecPersonal, Events: events2}},
	}); err != nil {
		return res.fail("NOTIFY SET (PERSONAL ...) rejected: %v", err)
	}
	res.note("NOTIFY SET accepted using mode: %s", notifyMode)

	// Drain any initial burst the server sends when NOTIFY SET is activated,
	// so it does not pollute the change observations.
	time.Sleep(1500 * time.Millisecond)
	mu.Lock()
	initial := len(events)
	initialSnapshot := append([]notifyEvent{}, events...)
	events = nil
	mu.Unlock()
	res.note("initial STATUS burst on NOTIFY SET: %d event(s)", initial)
	for _, e := range initialSnapshot {
		res.note("  [init] %s %s %s", e.Kind, e.Mailbox, e.Detail)
	}
	// Whether the initial burst carries HIGHESTMODSEQ is itself the key
	// question for the sync engine's cold-start path.
	initialWithModSeq := 0
	for _, e := range initialSnapshot {
		if e.Kind == "STATUS" && !strings.Contains(e.Detail, "HIGHESTMODSEQ=<absent>") {
			initialWithModSeq++
		}
	}
	res.note("initial STATUS events carrying HIGHESTMODSEQ: %d/%d", initialWithModSeq, initial)

	// Keep the watcher's protocol pump running. The library only reads the
	// connection while a command is in flight or during IDLE, so a NOTIFY
	// watcher must sit in IDLE to actually receive the notifications.
	idle, err := watcher.Idle()
	if err != nil {
		res.note("FINDING: could not enter IDLE after NOTIFY SET: %v", err)
		res.note("=> notifications may only arrive when another command is issued")
	} else {
		res.note("watcher entered IDLE after NOTIFY SET (required to pump notifications)")
	}

	// --- mutator connection --------------------------------------------------
	mut, err := dialLib(cfg, nil)
	if err != nil {
		return res.fail("mutator conn: %v", err)
	}
	defer mut.Close()

	type change struct {
		mailbox string
		uid     imap.UID
	}
	var changes []change
	stamp := time.Now().Format("150405")

	// 1. MessageNew in three different, non-selected folders.
	for _, f := range []string{"S2/folder1", "S2/folder3", "S2/folder5"} {
		uid, err := appendLib(mut, f, fmt.Sprintf("moov-s2 notify %s %s", f, stamp))
		if err != nil {
			res.fail("append to %s: %v", f, err)
			continue
		}
		changes = append(changes, change{f, uid})
		res.note("mutator: appended UID %d to %s", uid, f)
		time.Sleep(400 * time.Millisecond)
	}

	// 2. FlagChange in folder1.
	if len(changes) > 0 {
		if _, err := mut.Select(changes[0].mailbox, &imap.SelectOptions{CondStore: true}).Wait(); err != nil {
			res.fail("mutator SELECT %s: %v", changes[0].mailbox, err)
		} else {
			if err := mut.Store(imap.UIDSetNum(changes[0].uid), &imap.StoreFlags{
				Op:    imap.StoreFlagsAdd,
				Flags: []imap.Flag{imap.FlagFlagged},
			}, nil).Close(); err != nil {
				res.fail("mutator STORE: %v", err)
			} else {
				res.note("mutator: flagged UID %d in %s", changes[0].uid, changes[0].mailbox)
			}
			time.Sleep(800 * time.Millisecond)
		}
	}

	// 3. MessageExpunge in folder3.
	var expungedIn string
	for _, ch := range changes {
		if ch.mailbox == "S2/folder3" {
			if _, err := mut.Select(ch.mailbox, nil).Wait(); err == nil {
				cleanupUIDs(mut, imap.UIDSetNum(ch.uid))
				expungedIn = ch.mailbox
				res.note("mutator: expunged UID %d from %s", ch.uid, ch.mailbox)
			}
		}
	}
	time.Sleep(2500 * time.Millisecond)

	if idle != nil {
		if err := idle.Close(); err != nil {
			res.note("idle.Close(): %v", err)
		}
	}

	mu.Lock()
	observed := append([]notifyEvent{}, events...)
	mu.Unlock()

	res.note("---- observed events after mutations: %d ----", len(observed))
	for _, e := range observed {
		res.note("  [%6dms] %-8s %-14s %s", e.At.Milliseconds(), e.Kind, e.Mailbox, e.Detail)
	}

	// --- analysis ------------------------------------------------------------
	notifiedMailboxes := map[string]bool{}
	statusWithModSeq, statusTotal := 0, 0
	for _, e := range observed {
		if e.Kind == "STATUS" {
			statusTotal++
			notifiedMailboxes[e.Mailbox] = true
			if !strings.Contains(e.Detail, "HIGHESTMODSEQ=<absent>") {
				statusWithModSeq++
			}
		}
	}
	var names []string
	for m := range notifiedMailboxes {
		names = append(names, m)
	}
	sort.Strings(names)
	res.note("mailboxes that produced notifications: %v", names)

	// The decisive question: did ONE connection see events from MULTIPLE,
	// non-selected mailboxes?
	if len(notifiedMailboxes) >= 2 {
		res.note("PASS: a single connection received events for %d distinct mailboxes "+
			"=> NOTIFY collapses the per-folder connection fan-out", len(notifiedMailboxes))
	} else {
		res.fail("only %d mailbox(es) produced notifications; multi-mailbox watching NOT demonstrated",
			len(notifiedMailboxes))
	}

	// HIGHESTMODSEQ in NOTIFY-induced STATUS.
	//
	// IMPORTANT: the absence observed here is NOT a Dovecot RFC 5465 violation.
	// A raw-protocol control experiment (RESULTS.md, T2-NOTIFY-RAW) proved that
	// the presence of HIGHESTMODSEQ depends on whether the client asked for the
	// STATUS *keyword* in NOTIFY SET:
	//
	//   NOTIFY SET STATUS (PERSONAL ...)  -> STATUS carries HIGHESTMODSEQ, and
	//                                        FlagChange IS delivered
	//   NOTIFY SET (PERSONAL ...)         -> STATUS omits HIGHESTMODSEQ, and
	//                                        FlagChange is NOT delivered
	//
	// go-imap can only emit the second form today (library bug #1), so this
	// test necessarily observes the degraded behaviour. Fixing the encoder
	// unlocks the good path.
	if statusTotal == 0 {
		res.note("no NOTIFY-induced STATUS observed after the mutations")
	} else if statusWithModSeq == 0 {
		res.note("EXPECTED under library bug #1: %d/%d STATUS responses omit HIGHESTMODSEQ.", statusTotal, statusTotal)
		res.note("  Raw-protocol control shows Dovecot DOES send HIGHESTMODSEQ when the client " +
			"issues `NOTIFY SET STATUS (...)`. Dovecot is compliant; the library is the limit.")
	} else if statusWithModSeq < statusTotal {
		res.note("HIGHESTMODSEQ present in only %d/%d STATUS responses (inconsistent)",
			statusWithModSeq, statusTotal)
	} else {
		res.note("NOTIFY-induced STATUS includes HIGHESTMODSEQ in all %d responses", statusTotal)
	}

	// FlagChange coverage. Under the library's degraded NOTIFY form, a pure
	// flag change on a non-selected mailbox produces NO notification at all.
	flagChangeSeen := false
	for _, e := range observed {
		if e.Kind == "STATUS" && len(changes) > 0 && e.Mailbox == changes[0].mailbox &&
			strings.Contains(e.Detail, "HIGHESTMODSEQ=") && !strings.Contains(e.Detail, "<absent>") {
			flagChangeSeen = true
		}
	}
	res.note("FlagChange notification observable through the library: %v", flagChangeSeen)
	if !flagChangeSeen {
		res.note("  => with `NOTIFY SET (PERSONAL ...)` a flag change that does not alter " +
			"MESSAGES/UNSEEN is INVISIBLE to the watcher. Moov MUST fix the encoder to emit " +
			"the STATUS keyword, otherwise flag sync silently breaks.")
	}

	// Which event classes actually produced something observable.
	sawNew := len(changes) > 0 && len(notifiedMailboxes) > 0
	res.note("event coverage: MessageNew observable=%v, MessageExpunge folder=%q, "+
		"FlagChange folder=%q", sawNew, expungedIn, func() string {
		if len(changes) > 0 {
			return changes[0].mailbox
		}
		return ""
	}())
	res.note("NOTE: for non-selected mailboxes Dovecot signals ALL event classes as a single " +
		"STATUS response; the event TYPE is not distinguishable from the notification itself.")

	// --- cleanup: remove the messages this test created ---------------------
	for _, ch := range changes {
		if ch.mailbox == expungedIn {
			continue
		}
		if _, err := mut.Select(ch.mailbox, nil).Wait(); err == nil {
			cleanupUIDs(mut, imap.UIDSetNum(ch.uid))
		}
	}
	fmt.Fprintln(os.Stderr, "notify test finished")
	return res
}
