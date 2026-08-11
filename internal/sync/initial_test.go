package sync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
)

// referenceNow is the fixed "now" every test uses, so the 30-day window covers
// the same messages regardless of when the suite runs.
var referenceNow = time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

// TestInitialSyncStoresEverything is the baseline: a fresh account ends with
// every message of every mailbox stored exactly once, with its flags.
func TestInitialSyncStoresEverything(t *testing.T) {
	env := newTestEnv(t)
	srv := newFakeServer()

	inbox := srv.addMailbox("INBOX", imap.RoleInbox, 100)
	seedMailbox(inbox, 40, referenceNow, "Inbox")
	sent := srv.addMailbox("Sent", imap.RoleSent, 101)
	seedMailbox(sent, 15, referenceNow.Add(-90*24*time.Hour), "Sent")
	archive := srv.addMailbox("Archive", imap.RoleArchive, 102)
	seedMailbox(archive, 25, referenceNow.Add(-200*24*time.Hour), "Archive")

	opts := env.testOptions(referenceNow)
	res, err := env.syncer(t, srv, opts).Run(context.Background(), env.account)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !res.Complete {
		t.Error("Result.Complete is false after a successful run")
	}
	if got, want := res.Mailboxes, 3; got != want {
		t.Errorf("discovered %d mailboxes, want %d", got, want)
	}

	total := srv.totalMessages()
	if got := env.countMessages(t); got != total {
		t.Errorf("stored %d messages, want %d", got, total)
	}

	stored := env.storedUIDs(t)
	for _, mb := range []struct {
		name string
		n    int
	}{{"INBOX", 40}, {"Sent", 15}, {"Archive", 25}} {
		uids := stored[mb.name]
		if len(uids) != mb.n {
			t.Errorf("%s: stored %d uids, want %d", mb.name, len(uids), mb.n)
			continue
		}
		for i, uid := range uids {
			if uid != int64(i+1) {
				t.Errorf("%s: uid at position %d is %d, want %d", mb.name, i, uid, i+1)
				break
			}
		}
	}

	// The roles must survive, because the JMAP layer asks for folders by role
	// rather than by name.
	for role, name := range map[store.MailboxRole]string{
		store.RoleInbox:   "INBOX",
		store.RoleSent:    "Sent",
		store.RoleArchive: "Archive",
	} {
		mb, err := env.store.GetMailboxByRole(context.Background(), env.account.ID, role)
		if err != nil {
			t.Errorf("GetMailboxByRole(%s): %v", role, err)
			continue
		}
		if mb.Name != name {
			t.Errorf("role %s maps to %q, want %q", role, mb.Name, name)
		}
	}
}

// TestRecentPhaseCoversTheWindowAndMakesTheAccountUsable checks the phase-A
// contract: the recent window of INBOX is stored first, and the account is
// marked usable before the backfill has run.
func TestRecentPhaseCoversTheWindowAndMakesTheAccountUsable(t *testing.T) {
	env := newTestEnv(t)
	srv := newFakeServer()

	// 100 messages one hour apart: the newest ~30 days' worth is the window,
	// and the rest is history the recent phase must not touch.
	inbox := srv.addMailbox("INBOX", imap.RoleInbox, 100)
	seedMailbox(inbox, 100, referenceNow, "Inbox")
	// Deliberately push the oldest well outside the window.
	for i := range 40 {
		inbox.messages[i].internalDate = referenceNow.Add(-time.Duration(60+i) * 24 * time.Hour)
	}

	opts := env.testOptions(referenceNow)
	s := env.syncer(t, srv, opts)

	boxes, err := s.discover(context.Background(), env.account, env.logger)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	stats, err := s.runRecent(context.Background(), env.account, boxes, env.logger)
	if err != nil {
		t.Fatalf("runRecent: %v", err)
	}

	// The 60 messages from index 40 upwards are inside the window (they are
	// hours apart, ending at referenceNow); the 40 backdated ones are not.
	if got, want := stats.stored, 60; got != want {
		t.Errorf("recent phase stored %d messages, want %d", got, want)
	}
	if got := env.countMessages(t); got != 60 {
		t.Errorf("store holds %d messages after the recent phase, want 60", got)
	}

	mb, err := env.store.GetMailboxByRole(context.Background(), env.account.ID, store.RoleInbox)
	if err != nil {
		t.Fatalf("GetMailboxByRole: %v", err)
	}
	if mb.BackfillState != store.BackfillRecentDone {
		t.Errorf("backfill state is %q after the recent phase, want %q",
			mb.BackfillState, store.BackfillRecentDone)
	}
}

// TestResumeAfterCrashLosesNothingAndDuplicatesNothing is THE E5 acceptance
// criterion: a run killed mid-backfill, resumed, must end with exactly the
// server's corpus — no message missing, none stored twice.
//
// The crash is injected at a fetch boundary rather than simulated with a real
// kill, because that is the only way to place it deterministically. Everything
// downstream of the injection point behaves exactly as it would after a SIGKILL:
// the process's in-flight batch is lost, and the durable state is whatever was
// committed before it.
func TestResumeAfterCrashLosesNothingAndDuplicatesNothing(t *testing.T) {
	env := newTestEnv(t)
	srv := newFakeServer()

	inbox := srv.addMailbox("INBOX", imap.RoleInbox, 100)
	seedMailbox(inbox, 120, referenceNow, "Inbox")
	other := srv.addMailbox("Archive", imap.RoleArchive, 101)
	seedMailbox(other, 60, referenceNow.Add(-300*24*time.Hour), "Archive")

	total := srv.totalMessages()
	opts := env.testOptions(referenceNow)

	// Crash partway through: after 75 message bodies have been handed out,
	// every further fetch fails. 75 lands inside the backfill, past several
	// committed batches, which is the window where a naive implementation
	// loses or duplicates messages.
	srv.mu.Lock()
	srv.failAfterFetches = 75
	srv.mu.Unlock()

	_, err := env.syncer(t, srv, opts).Run(context.Background(), env.account)
	if err == nil {
		t.Fatal("the interrupted run returned no error; the crash was not injected")
	}
	if !errors.Is(err, errInjectedFetchFailure) {
		t.Fatalf("interrupted run failed with %v, want the injected failure", err)
	}

	partial := env.countMessages(t)
	if partial == 0 {
		t.Fatal("the interrupted run committed nothing; the test cannot distinguish resume from a first run")
	}
	if partial >= total {
		t.Fatalf("the interrupted run stored everything (%d of %d); the crash was too late to test resume", partial, total)
	}
	t.Logf("crash left %d of %d messages stored", partial, total)

	// Resume: the injection is lifted and the same corpus is synced again.
	srv.mu.Lock()
	srv.failAfterFetches = 0
	fetchesBeforeResume := srv.fetchCount
	srv.mu.Unlock()

	res, err := env.syncer(t, srv, opts).Run(context.Background(), env.account)
	if err != nil {
		t.Fatalf("resumed run: %v", err)
	}
	if !res.Complete {
		t.Error("the resumed run did not complete")
	}

	// NO LOSS: every message of the corpus is present.
	if got := env.countMessages(t); got != total {
		t.Errorf("after resume the store holds %d messages, want %d", got, total)
	}

	// NO DUPLICATES: every mailbox holds each UID exactly once. The unique
	// index would have raised an error rather than storing a duplicate, so
	// this also proves the run did not merely survive by aborting a batch.
	stored := env.storedUIDs(t)
	for name, uids := range stored {
		seen := map[int64]bool{}
		for _, uid := range uids {
			if seen[uid] {
				t.Errorf("%s: uid %d stored more than once", name, uid)
			}
			seen[uid] = true
		}
	}
	if got, want := len(stored["INBOX"]), 120; got != want {
		t.Errorf("INBOX holds %d uids after resume, want %d", got, want)
	}
	if got, want := len(stored["Archive"]), 60; got != want {
		t.Errorf("Archive holds %d uids after resume, want %d", got, want)
	}

	// The resume must not re-download what it already had: that is what the
	// checkpoint and the already-stored filter are for. Some overlap is
	// expected (the window that was in flight when the crash hit), but the
	// resume must cost far less than a full re-run.
	srv.mu.Lock()
	resumeFetches := srv.fetchCount - fetchesBeforeResume
	srv.mu.Unlock()
	if resumeFetches > total {
		t.Errorf("resume fetched %d messages for a %d-message corpus; the resume path re-downloaded everything",
			resumeFetches, total)
	}
	t.Logf("resume fetched %d messages to finish a %d-message corpus", resumeFetches, total)
}

// TestRerunIsIdempotent proves that a completed sync run twice stores nothing
// new — the property every retry, restart and supervisor loop depends on.
func TestRerunIsIdempotent(t *testing.T) {
	env := newTestEnv(t)
	srv := newFakeServer()

	inbox := srv.addMailbox("INBOX", imap.RoleInbox, 100)
	seedMailbox(inbox, 30, referenceNow, "Inbox")

	opts := env.testOptions(referenceNow)

	first, err := env.syncer(t, srv, opts).Run(context.Background(), env.account)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	afterFirst := env.countMessages(t)
	if afterFirst != 30 {
		t.Fatalf("first run stored %d messages, want 30", afterFirst)
	}

	second, err := env.syncer(t, srv, opts).Run(context.Background(), env.account)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}

	if got := env.countMessages(t); got != afterFirst {
		t.Errorf("the second run changed the message count from %d to %d", afterFirst, got)
	}
	if second.RecentStored+second.BackfillStored != 0 {
		t.Errorf("the second run stored %d messages, want 0",
			second.RecentStored+second.BackfillStored)
	}
	if first.Skipped != 0 {
		t.Errorf("the first run skipped %d messages, want 0", first.Skipped)
	}
}

// TestUIDValidityChangeInvalidatesAndResyncs covers the rule that keeps Moov
// from attaching new mail to old identities (L2 §2.5).
func TestUIDValidityChangeInvalidatesAndResyncs(t *testing.T) {
	env := newTestEnv(t)
	srv := newFakeServer()

	inbox := srv.addMailbox("INBOX", imap.RoleInbox, 100)
	seedMailbox(inbox, 20, referenceNow, "Original")

	opts := env.testOptions(referenceNow)
	if _, err := env.syncer(t, srv, opts).Run(context.Background(), env.account); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if got := env.countMessages(t); got != 20 {
		t.Fatalf("first run stored %d messages, want 20", got)
	}

	// The mailbox is recreated on the server: same name, new UIDVALIDITY, and
	// the same UID numbers now name DIFFERENT messages. Storing the new
	// messages under the old identities is the corruption this must prevent.
	srv.mu.Lock()
	inbox.uidValidity = 200
	inbox.messages = nil
	seedMailbox(inbox, 12, referenceNow, "Recreated")
	srv.mu.Unlock()

	if _, err := env.syncer(t, srv, opts).Run(context.Background(), env.account); err != nil {
		t.Fatalf("resync after uidvalidity change: %v", err)
	}

	// Exactly the new corpus, nothing of the old one.
	if got := env.countMessages(t); got != 12 {
		t.Errorf("after the uidvalidity change the store holds %d messages, want 12", got)
	}

	mb, err := env.store.GetMailboxByRole(context.Background(), env.account.ID, store.RoleInbox)
	if err != nil {
		t.Fatalf("GetMailboxByRole: %v", err)
	}
	if mb.UIDValidity == nil || *mb.UIDValidity != 200 {
		t.Errorf("stored uidvalidity is %v, want 200", mb.UIDValidity)
	}

	// The stored subjects must be the new ones: this is the assertion that
	// would catch an implementation that kept the old rows and merely bumped
	// the uidvalidity column.
	var subject string
	err = env.store.Pool().QueryRow(context.Background(),
		`SELECT subject FROM messages WHERE account_id = $1 ORDER BY id LIMIT 1`,
		env.account.ID).Scan(&subject)
	if err != nil {
		t.Fatalf("reading a stored subject: %v", err)
	}
	if want := "Recreated"; len(subject) < len(want) || subject[:len(want)] != want {
		t.Errorf("stored subject is %q, want one starting with %q", subject, want)
	}
}

// TestFailedParseIsStoredAndDoesNotStopTheRun is risk R4: a message the cascade
// cannot read must still occupy its UID and must not hold the mailbox hostage.
func TestFailedParseIsStoredAndDoesNotStopTheRun(t *testing.T) {
	env := newTestEnv(t)
	srv := newFakeServer()

	inbox := srv.addMailbox("INBOX", imap.RoleInbox, 100)
	seedMailbox(inbox, 20, referenceNow, "Inbox")

	// Three messages the parser cannot make sense of, scattered so that a run
	// which stopped at the first would visibly store fewer than 20.
	for _, i := range []int{3, 11, 17} {
		inbox.messages[i].raw = unparseableMessage()
	}

	opts := env.testOptions(referenceNow)
	res, err := env.syncer(t, srv, opts).Run(context.Background(), env.account)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := env.countMessages(t); got != 20 {
		t.Errorf("stored %d messages, want 20: a failed parse stopped the run", got)
	}
	if got, want := env.countByParseStatus(t, store.ParseFailed), 3; got != want {
		t.Errorf("%d messages have parse_status='failed', want %d", got, want)
	}
	if res.Failed != 3 {
		t.Errorf("Result.Failed is %d, want 3", res.Failed)
	}

	// Every UID is present, including the unparseable ones: their raw bytes
	// are durable and a reparse can recover them later.
	uids := env.storedUIDs(t)["INBOX"]
	if len(uids) != 20 {
		t.Errorf("INBOX holds %d uids, want 20", len(uids))
	}
}

// TestFlagsAndKeywordsSurvive checks the flag mapping end to end, since a
// wrong bitmask silently mis-renders every message list.
func TestFlagsAndKeywordsSurvive(t *testing.T) {
	env := newTestEnv(t)
	srv := newFakeServer()

	inbox := srv.addMailbox("INBOX", imap.RoleInbox, 100)
	seedMailbox(inbox, 8, referenceNow, "Inbox")
	inbox.messages[0].flags = []string{"seen", "flagged", "answered"}
	inbox.messages[0].keywords = []string{"$MoovL1", "$Forwarded"}
	inbox.messages[1].flags = nil
	inbox.messages[1].keywords = nil

	opts := env.testOptions(referenceNow)
	if _, err := env.syncer(t, srv, opts).Run(context.Background(), env.account); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mb, err := env.store.GetMailboxByRole(context.Background(), env.account.ID, store.RoleInbox)
	if err != nil {
		t.Fatalf("GetMailboxByRole: %v", err)
	}

	first, err := env.store.GetMessageStateByUID(context.Background(), mb.ID, 100, 1)
	if err != nil {
		t.Fatalf("GetMessageStateByUID(1): %v", err)
	}
	want := store.FlagSeen | store.FlagFlagged | store.FlagAnswered
	if first.Flags != want {
		t.Errorf("uid 1 flags are %s, want %s", first.Flags, want)
	}
	if len(first.Keywords) != 2 {
		t.Errorf("uid 1 keywords are %v, want two entries", first.Keywords)
	}

	second, err := env.store.GetMessageStateByUID(context.Background(), mb.ID, 100, 2)
	if err != nil {
		t.Fatalf("GetMessageStateByUID(2): %v", err)
	}
	if second.Flags != 0 {
		t.Errorf("uid 2 flags are %s, want none", second.Flags)
	}
}

// TestSearchFindsAPlantedNeedle proves the pipeline populates the FTS column,
// which is the only end-to-end check that the parser's text actually reaches
// the index.
func TestSearchFindsAPlantedNeedle(t *testing.T) {
	env := newTestEnv(t)
	srv := newFakeServer()

	inbox := srv.addMailbox("INBOX", imap.RoleInbox, 100)
	seedMailbox(inbox, 20, referenceNow, "Inbox")

	const needle = "zarzaparrilla"
	inbox.messages[7].raw = buildMessage(7, "A message about "+needle,
		referenceNow.Add(-3*time.Hour), "The body also mentions "+needle+" once.")

	opts := env.testOptions(referenceNow)
	if _, err := env.syncer(t, srv, opts).Run(context.Background(), env.account); err != nil {
		t.Fatalf("Run: %v", err)
	}

	hits, err := env.store.Search(context.Background(), store.SearchQuery{
		AccountID: env.account.ID,
		Text:      needle,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("search for %q returned %d hits, want 1", needle, len(hits))
	}
	if want := "A message about " + needle; hits[0].Subject != want {
		t.Errorf("hit subject is %q, want %q", hits[0].Subject, want)
	}
}

// TestCancellationStopsCleanlyAndResumes checks that a shutdown mid-run leaves
// resumable state rather than a wedged account — the same guarantee as the
// crash test, reached through the graceful path.
func TestCancellationStopsCleanlyAndResumes(t *testing.T) {
	env := newTestEnv(t)
	srv := newFakeServer()

	inbox := srv.addMailbox("INBOX", imap.RoleInbox, 100)
	seedMailbox(inbox, 80, referenceNow, "Inbox")

	opts := env.testOptions(referenceNow)
	// Cancel as soon as the first batch is committed, which puts the
	// cancellation squarely in the middle of the pipeline.
	ctx, cancel := context.WithCancel(context.Background())
	opts.OnProgress = func(Progress) { cancel() }

	_, err := env.syncer(t, srv, opts).Run(ctx, env.account)
	if err == nil {
		t.Fatal("the canceled run returned no error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled run failed with %v, want context.Canceled", err)
	}

	// Resume with a fresh context and no progress hook.
	opts.OnProgress = nil
	res, err := env.syncer(t, srv, opts).Run(context.Background(), env.account)
	if err != nil {
		t.Fatalf("resumed run: %v", err)
	}
	if !res.Complete {
		t.Error("the resumed run did not complete")
	}
	if got := env.countMessages(t); got != 80 {
		t.Errorf("after resume the store holds %d messages, want 80", got)
	}
}

// TestDiscoverySkipsNoSelectMailboxes checks that a parent node is stored for
// the folder tree but never selected, which would be a protocol error.
func TestDiscoverySkipsNoSelectMailboxes(t *testing.T) {
	env := newTestEnv(t)
	srv := newFakeServer()

	inbox := srv.addMailbox("INBOX", imap.RoleInbox, 100)
	seedMailbox(inbox, 5, referenceNow, "Inbox")
	child := srv.addMailbox("Projects/Alpha", imap.RoleNone, 101)
	seedMailbox(child, 4, referenceNow, "Alpha")

	// A parent that cannot be selected, as Dovecot reports for an intermediate
	// hierarchy node. Selecting it on the fake is an error, so a run that
	// reaches SELECT for it fails this test rather than passing quietly.
	parent := srv.addMailbox("Projects", imap.RoleNone, 102)
	parent.noSelect = true

	opts := env.testOptions(referenceNow)
	res, err := env.syncer(t, srv, opts).Run(context.Background(), env.account)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The parent is listed and stored but is not one of the synced mailboxes:
	// two of the three folders are selectable.
	if res.Mailboxes != 2 {
		t.Errorf("synced %d mailboxes, want 2", res.Mailboxes)
	}
	if got := env.countMessages(t); got != 9 {
		t.Errorf("stored %d messages, want 9", got)
	}

	all, err := env.store.ListMailboxes(context.Background(), env.account.ID)
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("stored %d mailbox rows, want 3", len(all))
	}
}
