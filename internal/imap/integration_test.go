package imap

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Integration tests against a real Dovecot.
//
// They are skipped unless MOOV_IMAP_TEST_HOST and friends are set, so
// `go test ./...` stays hermetic. They exist because spike S2's central lesson
// was that go-imap's own test suite is green against bytes a real server
// rejects: every bug in patches/ was invisible to unit tests and obvious to
// Dovecot. These are the S2 scenarios promoted to Go tests, so the next
// dependency bump re-runs them instead of re-deriving them.
//
// Environment:
//
//	MOOV_IMAP_TEST_HOST      required — e.g. "dovecot" inside the Mailcow network
//	MOOV_IMAP_TEST_PORT      optional, default 143
//	MOOV_IMAP_TEST_USER      required — a DEDICATED test mailbox, never a real one
//	MOOV_IMAP_TEST_PASSWORD  required — passed by environment only, never a file
//	MOOV_IMAP_TEST_SERVERNAME optional — the name the certificate is issued for
//	MOOV_IMAP_TEST_INSECURE  optional — "1" to skip verification (dev/CI only)
//
// The tests write: they create a scratch folder, append messages, and expunge
// them. They must never point at a mailbox with real mail.

const (
	// testFolderPrefix namespaces everything these tests create, so a leftover
	// folder is obviously theirs and safe to delete.
	testFolderPrefix = "MoovE2Test"

	// eventWait bounds how long a test waits for a push notification. S2
	// measured ~0.5 s container-to-container; 15 s is generous enough that a
	// loaded server does not produce a flake, and short enough that a genuine
	// failure is not a hang.
	eventWait = 15 * time.Second
)

// testConfig builds a Config from the environment, or skips the test.
func testConfig(t *testing.T) Config {
	t.Helper()

	host := os.Getenv("MOOV_IMAP_TEST_HOST")
	user := os.Getenv("MOOV_IMAP_TEST_USER")
	pass := os.Getenv("MOOV_IMAP_TEST_PASSWORD")
	if host == "" || user == "" || pass == "" {
		t.Skip("integration test: set MOOV_IMAP_TEST_HOST, MOOV_IMAP_TEST_USER and " +
			"MOOV_IMAP_TEST_PASSWORD to run (see internal/imap/integration_test.go)")
	}

	cfg := Config{
		Host:          host,
		Username:      user,
		Password:      pass,
		TLSServerName: os.Getenv("MOOV_IMAP_TEST_SERVERNAME"),
	}
	if p := os.Getenv("MOOV_IMAP_TEST_PORT"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			t.Fatalf("MOOV_IMAP_TEST_PORT=%q is not a number: %v", p, err)
		}
		cfg.Port = n
	}
	if os.Getenv("MOOV_IMAP_TEST_INSECURE") == "1" {
		// The Mailcow-internal certificate is issued for the public hostname,
		// not for the "dovecot" network alias (S1 H2). The right fix is
		// MOOV_IMAP_TEST_SERVERNAME; this is the escape hatch for a test rig
		// where even that is not available.
		cfg.InsecureSkipVerify = true
	}
	return cfg
}

// connectForTest returns a connected client, closed at the end of the test.
func connectForTest(t *testing.T) *client {
	t.Helper()

	cfg := testConfig(t)
	cl, ok := New(slog.New(slog.NewTextHandler(io.Discard, nil))).(*client)
	if !ok {
		t.Fatal("New did not return the package's own implementation")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := cl.Connect(ctx, cfg); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() {
		if err := cl.Close(); err != nil {
			t.Logf("Close: %v", err)
		}
	})
	return cl
}

// scratchFolder creates a uniquely named folder and deletes it afterwards.
func scratchFolder(t *testing.T, cl *client, suffix string) string {
	t.Helper()

	name := fmt.Sprintf("%s/%s-%d", testFolderPrefix, suffix, time.Now().UnixNano())
	gc, err := cl.conn()
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	if err := gc.Create(name, nil).Wait(); err != nil {
		t.Fatalf("creating scratch folder %q: %v", name, err)
	}
	t.Cleanup(func() {
		// Leaving the mailbox selected would make DELETE fail on some servers.
		if err := gc.Unselect().Wait(); err != nil {
			t.Logf("unselect before cleanup: %v", err)
		}
		if err := gc.Delete(name).Wait(); err != nil {
			t.Logf("deleting scratch folder %q: %v (delete it by hand if it lingers)", name, err)
		}
	})
	return name
}

// appendTestMessage puts one small message into a mailbox and returns its UID.
func appendTestMessage(t *testing.T, cl *client, mailbox, subject string) UID {
	t.Helper()

	body := "From: moov-test@example.com\r\n" +
		"To: moov-test@example.com\r\n" +
		"Subject: " + subject + "\r\n" +
		"Message-ID: <" + subject + "." + strconv.FormatInt(time.Now().UnixNano(), 36) + "@moov.test>\r\n" +
		"Date: " + time.Now().Format(time.RFC1123Z) + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Cuerpo de prueba de Moov E2.\r\n"

	gc, err := cl.conn()
	if err != nil {
		t.Fatalf("conn: %v", err)
	}

	cmd := gc.Append(mailbox, int64(len(body)), nil)
	if _, err := io.WriteString(cmd, body); err != nil {
		t.Fatalf("writing APPEND literal: %v", err)
	}
	if err := cmd.Close(); err != nil {
		t.Fatalf("closing the APPEND literal for %q: %v", mailbox, err)
	}
	data, err := cmd.Wait()
	if err != nil {
		t.Fatalf("APPEND to %q: %v", mailbox, err)
	}
	return UID(data.UID)
}

// TestIntegrationConnectAndCapabilities is the S2 T2a scenario: the extensions
// the engine depends on must be advertised, and only after login.
func TestIntegrationConnectAndCapabilities(t *testing.T) {
	cl := connectForTest(t)
	caps := cl.Capabilities()

	for _, want := range []string{CapCondStore, CapQResync, CapIdle, CapNotify, CapMetadata} {
		if !caps.Has(want) {
			t.Errorf("server does not advertise %s (post-login capabilities: %v)",
				want, caps.Names())
		}
	}

	// S2 T2a recorded the absence of OBJECTID, which is why Moov derives its
	// own message identity. If a server bump ever adds it, that design choice
	// is worth revisiting — so the test says so rather than asserting absence.
	if caps.Has("objectid") {
		t.Log("NOTE: this server now advertises OBJECTID; " +
			"the derived-identity design of L2 §2.3 could be simplified")
	}
}

// TestIntegrationListMailboxes covers roles and LIST-STATUS in one round trip.
func TestIntegrationListMailboxes(t *testing.T) {
	cl := connectForTest(t)

	boxes, err := cl.ListMailboxes(context.Background())
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	if len(boxes) == 0 {
		t.Fatal("no mailboxes returned")
	}

	var inbox *MailboxInfo
	for i := range boxes {
		if boxes[i].Role == RoleInbox {
			inbox = &boxes[i]
		}
	}
	if inbox == nil {
		t.Fatal("no mailbox came back with RoleInbox")
	}
	if !inbox.HasStatus {
		t.Error("INBOX has no STATUS data; LIST-STATUS should have filled it in one round trip")
	}
	if inbox.UIDValidity == 0 {
		t.Error("INBOX UIDVALIDITY is 0; it is the anchor of every UID Moov stores")
	}

	roles := map[MailboxRole]int{}
	for _, b := range boxes {
		roles[b.Role]++
	}
	t.Logf("%d mailboxes; roles: %v", len(boxes), roles)
}

// TestIntegrationQResyncDelta is the S2 T1 scenario end to end, through Moov's
// own API: go "offline" at a known modseq, mutate from a second connection,
// and check the reconnect replays exactly the delta.
func TestIntegrationQResyncDelta(t *testing.T) {
	ctx := context.Background()
	cl := connectForTest(t)
	folder := scratchFolder(t, cl, "qresync")

	// Seed three messages and remember where we are.
	for i := range 3 {
		appendTestMessage(t, cl, folder, fmt.Sprintf("qresync-seed-%d", i))
	}

	sel, err := cl.SelectQResync(ctx, folder, 0, 0)
	if err != nil {
		t.Fatalf("initial SELECT: %v", err)
	}
	if sel.NumMessages != 3 {
		t.Fatalf("seeded 3 messages, mailbox reports %d", sel.NumMessages)
	}
	uidValidity, syncPoint := sel.UIDValidity, sel.HighestModSeq

	msgs := collectUIDs(ctx, t, cl)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 UIDs, got %v", msgs)
	}
	toFlag, toExpunge := msgs[0], msgs[1]

	// A second connection plays the role of another client: it flags one
	// message and expunges another while "we" are offline.
	other := connectForTest(t)
	if _, err := other.SelectQResync(ctx, folder, 0, 0); err != nil {
		t.Fatalf("second connection SELECT: %v", err)
	}
	if _, err := other.StoreFlags(ctx, []UID{toFlag}, FlagDelta{Op: FlagsAdd, Flags: []string{"flagged"}}, 0); err != nil {
		t.Fatalf("second connection STORE: %v", err)
	}
	expungeUID(ctx, t, other, toExpunge)

	// Reconnect with QRESYNC from the remembered point.
	back := connectForTest(t)
	res, err := back.SelectQResync(ctx, folder, uidValidity, syncPoint)
	if err != nil {
		t.Fatalf("QRESYNC SELECT: %v", err)
	}

	if res.UIDValidityChanged {
		t.Fatal("UIDVALIDITY changed unexpectedly during the test")
	}
	if res.HighestModSeq <= syncPoint {
		t.Errorf("HighestModSeq did not advance: was %d, now %d", syncPoint, res.HighestModSeq)
	}
	if !containsUID(res.VanishedUIDs, toExpunge) {
		t.Errorf("VANISHED (EARLIER) did not report UID %d; got %v", toExpunge, res.VanishedUIDs)
	}
	t.Logf("QRESYNC replayed: vanished=%v highestmodseq %d -> %d",
		res.VanishedUIDs, syncPoint, res.HighestModSeq)
}

// TestIntegrationFetchChangesCondStore is the S2 T2b scenario: CHANGEDSINCE
// must return the changed message and nothing else.
func TestIntegrationFetchChangesCondStore(t *testing.T) {
	ctx := context.Background()
	cl := connectForTest(t)
	folder := scratchFolder(t, cl, "condstore")

	for i := range 3 {
		appendTestMessage(t, cl, folder, fmt.Sprintf("condstore-seed-%d", i))
	}
	sel, err := cl.SelectQResync(ctx, folder, 0, 0)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	baseline := sel.HighestModSeq

	uids := collectUIDs(ctx, t, cl)
	if len(uids) < 2 {
		t.Fatalf("expected at least 2 UIDs, got %v", uids)
	}
	target := uids[1]

	if _, err := cl.StoreFlags(ctx, []UID{target}, FlagDelta{Op: FlagsAdd, Flags: []string{"seen"}}, 0); err != nil {
		t.Fatalf("STORE: %v", err)
	}

	it, err := cl.FetchChanges(ctx, baseline)
	if err != nil {
		t.Fatalf("FetchChanges: %v", err)
	}
	var changed []UID
	for {
		msg, err := it.Next()
		if err != nil {
			t.Fatalf("iterating changes: %v", err)
		}
		if msg == nil {
			break
		}
		changed = append(changed, msg.UID)
		if msg.ModSeq <= baseline {
			t.Errorf("UID %d came back with modseq %d, not above the baseline %d",
				msg.UID, msg.ModSeq, baseline)
		}
	}
	if err := it.Close(); err != nil {
		t.Fatalf("closing the change iterator: %v", err)
	}

	if len(changed) != 1 || changed[0] != target {
		t.Errorf("CHANGEDSINCE returned %v, want exactly [%d]", changed, target)
	}
}

// TestIntegrationStoreFlagsConflictIsSurfaced is the S2 H6 scenario, and the
// most important test in this file.
//
// A conditional STORE the server refuses completes with a tagged OK and no
// error. Stock go-imap reports that as success. If this test ever fails, Moov
// is silently losing flag writes under concurrent modification.
func TestIntegrationStoreFlagsConflictIsSurfaced(t *testing.T) {
	ctx := context.Background()
	cl := connectForTest(t)
	folder := scratchFolder(t, cl, "conflict")

	appendTestMessage(t, cl, folder, "conflict-subject")
	if _, err := cl.SelectQResync(ctx, folder, 0, 0); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	uids := collectUIDs(ctx, t, cl)
	if len(uids) != 1 {
		t.Fatalf("expected 1 UID, got %v", uids)
	}
	target := uids[0]

	// Remember a modseq, then let somebody else change the message so that our
	// remembered value is stale.
	sel, err := cl.SelectQResync(ctx, folder, 0, 0)
	if err != nil {
		t.Fatalf("re-SELECT: %v", err)
	}
	stale := sel.HighestModSeq

	other := connectForTest(t)
	if _, err := other.SelectQResync(ctx, folder, 0, 0); err != nil {
		t.Fatalf("second connection SELECT: %v", err)
	}
	if _, err := other.StoreFlags(ctx, []UID{target}, FlagDelta{Op: FlagsAdd, Flags: []string{"flagged"}}, 0); err != nil {
		t.Fatalf("second connection STORE: %v", err)
	}

	// Now write against the stale modseq. The server must refuse.
	res, err := cl.StoreFlags(ctx, []UID{target},
		FlagDelta{Op: FlagsAdd, Flags: []string{"answered"}}, stale)
	if err != nil {
		t.Fatalf("conditional STORE returned an error: %v", err)
	}

	if !res.Conflicted() {
		t.Fatalf("the conditional STORE was refused by the server but reported as success "+
			"(updated=%v rejected=%v read_back=%v).\n"+
			"This is the silent-write hazard of S2 H6: Moov's flag state is now diverging "+
			"from Dovecot's. Check that patch 0003 is applied and that the read-back "+
			"fallback in store.go still runs.",
			res.Updated, res.Rejected, res.VerifiedByReadBack)
	}
	if !containsUID(res.Rejected, target) {
		t.Errorf("rejected = %v, want it to contain UID %d", res.Rejected, target)
	}
	t.Logf("conflict correctly surfaced: rejected=%v via_read_back=%v",
		res.Rejected, res.VerifiedByReadBack)

	// And the proof that the server really did refuse: \Answered must be absent.
	flags := flagsOf(ctx, t, cl, target)
	for _, f := range flags {
		if f == "answered" {
			t.Error("the server applied \\Answered despite the stale UNCHANGEDSINCE; " +
				"the conflict detection is reporting a conflict that did not happen")
		}
	}
}

// TestIntegrationStoreFlagsAppliesWhenUnconflicted is the other half: a
// conditional write against a current modseq must go through and must NOT be
// reported as a conflict. Without it, a detector that always says "conflict"
// would pass the test above.
func TestIntegrationStoreFlagsAppliesWhenUnconflicted(t *testing.T) {
	ctx := context.Background()
	cl := connectForTest(t)
	folder := scratchFolder(t, cl, "noconflict")

	appendTestMessage(t, cl, folder, "noconflict-subject")
	sel, err := cl.SelectQResync(ctx, folder, 0, 0)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	uids := collectUIDs(ctx, t, cl)
	if len(uids) != 1 {
		t.Fatalf("expected 1 UID, got %v", uids)
	}

	res, err := cl.StoreFlags(ctx, uids,
		FlagDelta{Op: FlagsAdd, Flags: []string{"seen"}}, sel.HighestModSeq)
	if err != nil {
		t.Fatalf("conditional STORE: %v", err)
	}
	if res.Conflicted() {
		t.Errorf("a write against a current modseq was reported as conflicted: %+v", res)
	}
	if !containsUID(res.Updated, uids[0]) {
		t.Errorf("updated = %v, want it to contain UID %d", res.Updated, uids[0])
	}

	if flags := flagsOf(ctx, t, cl, uids[0]); !containsString(flags, "seen") {
		t.Errorf("flags after the write = %v, want \\Seen among them", flags)
	}
}

// TestIntegrationWatchNotifyMultiMailbox is the S2 T2d scenario, and the one
// the whole watcher architecture rests on: ONE connection must receive events
// for mailboxes it has not selected. If it does not, the engine needs a
// connection per folder and fail2ban becomes a problem (ADR §4).
//
// It also covers S2 T4: a pure flag change in a non-selected folder must be
// visible, which is only true with the patched STATUS keyword.
func TestIntegrationWatchNotifyMultiMailbox(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cl := connectForTest(t)
	if !cl.Capabilities().Has(CapNotify) {
		t.Skip("server does not advertise NOTIFY")
	}

	folderA := scratchFolder(t, cl, "watch-a")
	folderB := scratchFolder(t, cl, "watch-b")

	// Seed folder B so there is something to flag later, and prepare the
	// second connection BEFORE the watch starts, so none of that setup shows
	// up as an event and gets mistaken for the mutations under test.
	seedUID := appendTestMessage(t, cl, folderB, "watch-seed")

	other := connectForTest(t)
	if _, err := other.SelectQResync(ctx, folderB, 0, 0); err != nil {
		t.Fatalf("second connection SELECT: %v", err)
	}

	// The watcher selects INBOX and then watches the whole personal namespace,
	// so neither scratch folder is the selected one.
	if _, err := cl.SelectQResync(ctx, "INBOX", 0, 0); err != nil {
		t.Fatalf("SELECT INBOX: %v", err)
	}

	events, err := cl.Watch(ctx, WatchSpec{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// Let the watcher settle into IDLE, then drain whatever the setup above
	// already queued. Only events after this point are attributable to the
	// mutations below.
	time.Sleep(1500 * time.Millisecond)
drain:
	for {
		select {
		case ev := <-events:
			t.Logf("(drained setup event for %q)", ev.Mailbox)
		default:
			break drain
		}
	}

	// Mutation 1: a new message in folder A. This moves MESSAGES, so it is
	// visible even with the unpatched encoder — it is the control.
	appendTestMessage(t, other, folderA, "watch-new")

	// Mutation 2: a PURE flag change in folder B, on a message that is already
	// seen so that UNSEEN does not move either. Neither MESSAGES nor UNSEEN
	// changes, so HIGHESTMODSEQ is the only possible signal — and Dovecot only
	// sends it when the STATUS keyword is present (S2 T4). With the stock
	// encoder this mutation produces NO event at all.
	if _, err := other.StoreFlags(ctx, []UID{seedUID},
		FlagDelta{Op: FlagsAdd, Flags: []string{"seen"}}, 0); err != nil {
		t.Fatalf("second connection STORE (seen): %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	flagOnly := make(chan struct{})
	go func() {
		defer close(flagOnly)
		time.Sleep(500 * time.Millisecond)
		if _, err := other.StoreFlags(ctx, []UID{seedUID},
			FlagDelta{Op: FlagsAdd, Flags: []string{"flagged"}}, 0); err != nil {
			t.Errorf("second connection STORE (flagged): %v", err)
		}
	}()

	seenIn := make(map[string]bool)
	modSeqOnly := make(map[string]bool)
	deadline := time.After(eventWait)

collect:
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				break collect
			}
			if ev.Kind == EventOverflow {
				t.Log("NOTIFICATIONOVERFLOW received; the engine would resync the account")
				continue
			}
			t.Logf("event: mailbox=%q modseq=%d(has=%v) messages=%d(has=%v) unseen=%d(has=%v)",
				ev.Mailbox, ev.Status.HighestModSeq, ev.Status.HasHighestModSeq,
				ev.Status.NumMessages, ev.Status.HasNumMessages,
				ev.Status.NumUnseen, ev.Status.HasNumUnseen)
			seenIn[ev.Mailbox] = true
			// A STATUS carrying only HIGHESTMODSEQ is the fingerprint of the
			// flag-only notification that the patched encoder unlocks.
			if ev.Status.HasHighestModSeq && !ev.Status.HasNumMessages {
				modSeqOnly[ev.Mailbox] = true
			}
			if seenIn[folderA] && modSeqOnly[folderB] {
				break collect
			}
		case <-deadline:
			break collect
		case <-ctx.Done():
			break collect
		}
	}
	<-flagOnly

	if !seenIn[folderA] {
		t.Errorf("no event for %q after an APPEND; NOTIFY is not delivering events for "+
			"non-selected mailboxes, which the one-connection-per-account design depends on",
			folderA)
	}
	if !seenIn[folderB] {
		t.Errorf("no event for %q after a pure flag change.\n"+
			"This is the S2 T4 hazard: without the STATUS keyword in NOTIFY SET, a flag "+
			"change in a non-selected folder produces NO notification at all, and another "+
			"client marking mail read stays invisible to Moov. Check patch 0002.", folderB)
	}
	if !modSeqOnly[folderB] {
		t.Errorf("folder %q never produced a STATUS carrying HIGHESTMODSEQ without "+
			"MESSAGES. That response is the only way a flag-only change can be signaled, "+
			"so its absence means patch 0002 is not in effect.", folderB)
	}
}

// TestIntegrationMetadataRoundTrip covers the METADATA operations the label
// definitions of arbitrage A6 rely on. The measured limits are in
// docs/spikes/V1-metadata-dovecot.md; this is the regression test.
func TestIntegrationMetadataRoundTrip(t *testing.T) {
	ctx := context.Background()
	cl := connectForTest(t)
	if !cl.Capabilities().Has(CapMetadata) {
		t.Skip("server does not advertise METADATA")
	}

	ops := cl.Metadata()
	entry := EntryLabels
	value := []byte(`{"labels":[{"keyword":"$MoovL1","name":"Facturación","color":"#c62828"}]}`)

	if err := ops.Set(ctx, "INBOX", []Annotation{{Name: entry, Value: value}}); err != nil {
		t.Fatalf("SETMETADATA: %v", err)
	}
	t.Cleanup(func() {
		if err := ops.Set(context.Background(), "INBOX", []Annotation{{Name: entry, Value: nil}}); err != nil {
			t.Logf("cleaning up the annotation: %v", err)
		}
	})

	got, err := ops.Get(ctx, "INBOX", []string{entry})
	if err != nil {
		t.Fatalf("GETMETADATA: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d annotations, want 1", len(got))
	}
	if string(got[0].Value) != string(value) {
		t.Errorf("annotation round trip changed the value.\n got: %q\nwant: %q", got[0].Value, value)
	}

	// An entry that does not exist must come back with a nil Value rather than
	// being omitted: "absent" and "empty" are different, and a caller that
	// cannot tell them apart cannot implement "create the label set once".
	missing, err := ops.Get(ctx, "INBOX", []string{"/private/vendor/moov/does-not-exist"})
	if err != nil {
		t.Fatalf("GETMETADATA for an absent entry: %v", err)
	}
	if len(missing) != 1 {
		t.Fatalf("got %d annotations for an absent entry, want 1 with a nil value", len(missing))
	}
	if missing[0].Value != nil {
		t.Errorf("an absent entry came back with value %q, want nil", missing[0].Value)
	}
}

// TestIntegrationFetchMessagesStreamsBodies checks the body really is a
// streamed reader and that headers are usable.
func TestIntegrationFetchMessagesStreamsBodies(t *testing.T) {
	ctx := context.Background()
	cl := connectForTest(t)
	folder := scratchFolder(t, cl, "fetch")

	const subject = "streaming-body-subject"
	appendTestMessage(t, cl, folder, subject)
	if _, err := cl.SelectQResync(ctx, folder, 0, 0); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	uids := collectUIDs(ctx, t, cl)
	if len(uids) != 1 {
		t.Fatalf("expected 1 UID, got %v", uids)
	}

	t.Run("headers only", func(t *testing.T) {
		it, err := cl.FetchMessages(ctx, uids, FetchSpec{Headers: true, Flags: true, Size: true})
		if err != nil {
			t.Fatalf("FetchMessages: %v", err)
		}
		defer func() { _ = it.Close() }()

		msg, err := it.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if msg == nil {
			t.Fatal("no message returned")
		}
		if !strings.Contains(string(msg.Header), subject) {
			t.Errorf("header block does not contain the subject; got %q", msg.Header)
		}
		if msg.Body != nil {
			t.Error("a headers-only fetch must not carry a body reader")
		}
		if msg.Size == 0 {
			t.Error("RFC822.SIZE was requested but came back 0")
		}
		if err := it.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	t.Run("full body streams", func(t *testing.T) {
		it, err := cl.FetchMessages(ctx, uids, FetchSpec{Body: true, Flags: true})
		if err != nil {
			t.Fatalf("FetchMessages: %v", err)
		}
		defer func() { _ = it.Close() }()

		msg, err := it.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if msg == nil || msg.Body == nil {
			t.Fatal("no body reader returned")
		}
		raw, err := io.ReadAll(msg.Body)
		if err != nil {
			t.Fatalf("reading the body: %v", err)
		}
		if !strings.Contains(string(raw), subject) {
			t.Errorf("body does not contain the subject; got %d bytes", len(raw))
		}

		// The reader must be dead once the iterator advances, so a consumer
		// that forgot to copy fails loudly instead of reading whatever the
		// connection moved on to.
		if _, err := it.Next(); err != nil {
			t.Fatalf("advancing: %v", err)
		}
		if _, err := msg.Body.Read(make([]byte, 1)); err == nil {
			t.Error("reading a body after the iterator advanced must fail")
		}
		if err := it.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
}

// TestIntegrationFetchPeekDoesNotMarkSeen guards a bug with a very unpleasant
// user-visible symptom: syncing a mailbox must not mark it all read.
func TestIntegrationFetchPeekDoesNotMarkSeen(t *testing.T) {
	ctx := context.Background()
	cl := connectForTest(t)
	folder := scratchFolder(t, cl, "peek")

	appendTestMessage(t, cl, folder, "peek-subject")
	if _, err := cl.SelectQResync(ctx, folder, 0, 0); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	uids := collectUIDs(ctx, t, cl)
	if len(uids) != 1 {
		t.Fatalf("expected 1 UID, got %v", uids)
	}

	it, err := cl.FetchMessages(ctx, uids, FetchSpec{Body: true})
	if err != nil {
		t.Fatalf("FetchMessages: %v", err)
	}
	msg, err := it.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if msg != nil && msg.Body != nil {
		_, _ = io.Copy(io.Discard, msg.Body)
	}
	if err := it.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if flags := flagsOf(ctx, t, cl, uids[0]); containsString(flags, "seen") {
		t.Error("fetching a body set \\Seen; BODY.PEEK is missing and a first sync would " +
			"mark the user's whole mailbox as read")
	}
}

// --- helpers -------------------------------------------------------------

// collectUIDs returns every UID in the selected mailbox.
func collectUIDs(ctx context.Context, t *testing.T, cl *client) []UID {
	t.Helper()

	it, err := cl.FetchChanges(ctx, 0)
	if err != nil {
		t.Fatalf("FetchChanges(0): %v", err)
	}
	defer func() { _ = it.Close() }()

	var uids []UID
	for {
		msg, err := it.Next()
		if err != nil {
			t.Fatalf("iterating: %v", err)
		}
		if msg == nil {
			break
		}
		uids = append(uids, msg.UID)
	}
	if err := it.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
	sortUIDs(uids)
	return uids
}

// flagsOf returns the flags and keywords of one message.
func flagsOf(ctx context.Context, t *testing.T, cl *client, uid UID) []string {
	t.Helper()

	it, err := cl.FetchMessages(ctx, []UID{uid}, FetchSpec{Flags: true})
	if err != nil {
		t.Fatalf("FetchMessages: %v", err)
	}
	defer func() { _ = it.Close() }()

	msg, err := it.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if msg == nil {
		t.Fatalf("UID %d not found", uid)
	}
	out := append(append([]string{}, msg.Flags...), msg.Keywords...)
	if err := it.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return out
}

// expungeUID marks a message deleted and expunges it.
func expungeUID(ctx context.Context, t *testing.T, cl *client, uid UID) {
	t.Helper()

	if _, err := cl.StoreFlags(ctx, []UID{uid}, FlagDelta{Op: FlagsAdd, Flags: []string{"deleted"}}, 0); err != nil {
		t.Fatalf("marking UID %d deleted: %v", uid, err)
	}
	gc, err := cl.conn()
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	if err := gc.Expunge().Close(); err != nil {
		t.Fatalf("EXPUNGE: %v", err)
	}
}

func containsUID(uids []UID, want UID) bool {
	for _, u := range uids {
		if u == want {
			return true
		}
	}
	return false
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
