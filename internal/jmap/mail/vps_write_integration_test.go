package mail_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/jmap/mail"
	"github.com/GrupoNU/moov/internal/store"
	syncengine "github.com/GrupoNU/moov/internal/sync"
)

// The W1 end-to-end suite: Email/set operating a REAL Dovecot mailbox through
// the whole stack — handler, write executor, IMAP, store — with the real
// watcher running, so the acceptance criteria of L2-jmap-write §3 are proven
// where they matter:
//
//   - a JMAP /set becomes visible to raw IMAP, and a raw IMAP change becomes
//     visible to JMAP (through the watcher);
//   - an UNCHANGEDSINCE conflict surfaces as a per-record SetError;
//   - both W-A2 destroy semantics round-trip (move-to-Trash, then the final
//     expunge from inside Trash);
//   - a replayed /set is idempotent;
//   - the watcher's echo of our own writes converges without duplication or
//     flapping.
//
// Environment: the same MOOV_IMAP_TEST_* / MOOV_TEST_DATABASE_URL variables
// as every other integration suite (vps_integration_test.go documents them).
// The test account MUST be the dedicated moov-test mailbox — it appends,
// flags, moves and expunges real messages (its own; everything it creates is
// removed on the way out).

// w1Message renders a minimal unique test message.
func w1Message(subject string) []byte {
	return []byte(fmt.Sprintf(
		"Message-ID: <%d@moov-w1.test>\r\n"+
			"Date: %s\r\n"+
			"From: W1 Suite <w1@moov-w1.test>\r\n"+
			"To: Moov Test <moov-test@atmosfera.cloud>\r\n"+
			"Subject: %s\r\n"+
			"MIME-Version: 1.0\r\n"+
			"Content-Type: text/plain; charset=utf-8\r\n"+
			"\r\n"+
			"Write-core integration fixture. Safe to delete.\r\n",
		time.Now().UnixNano(), time.Now().Format(time.RFC1123Z), subject))
}

// rawMessageFlags reads a message's flags and keywords over a raw IMAP
// connection, bypassing Moov entirely — the ground truth every "visible via
// raw IMAP" assertion compares against. ok is false when the UID is gone.
func rawMessageFlags(ctx context.Context, t *testing.T, c imap.Client, mailbox string, uid imap.UID) (flags, keywords []string, ok bool) {
	t.Helper()
	if _, err := c.SelectQResync(ctx, mailbox, 0, 0); err != nil {
		t.Fatalf("raw SELECT %q: %v", mailbox, err)
	}
	it, err := c.FetchMessages(ctx, []imap.UID{uid}, imap.FetchSpec{Flags: true})
	if err != nil {
		t.Fatalf("raw FETCH %d in %q: %v", uid, mailbox, err)
	}
	defer func() { _ = it.Close() }()
	for {
		msg, err := it.Next()
		if err != nil {
			t.Fatalf("raw FETCH %d in %q: %v", uid, mailbox, err)
		}
		if msg == nil {
			break
		}
		if msg.UID == uid {
			return append([]string(nil), msg.Flags...), append([]string(nil), msg.Keywords...), true
		}
	}
	return nil, nil, false
}

func hasName(list []string, name string) bool {
	for _, n := range list {
		if n == name {
			return true
		}
	}
	return false
}

// waitFor polls until cond is true or the deadline passes.
func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

func TestVPSIntegrationEmailSetWriteCore(t *testing.T) {
	cfg := vpsIMAPConfig(t)
	f := newFixture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if testing.Verbose() {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}

	if err := f.store.SetAccountCredentials(ctx, f.account.ID, cfg.Username, []byte("x")); err != nil {
		t.Fatalf("SetAccountCredentials: %v", err)
	}
	account, err := f.store.GetAccount(ctx, f.account.ID)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}

	// One connector serves the syncer, the watcher and the write executor —
	// the same topology cmd/moovd wires, minus the keyring (the test holds
	// the password directly, environment-only).
	connector := syncengine.ConnectorFunc(
		func(cctx context.Context, _ store.Account, n int) ([]imap.Client, error) {
			clients := make([]imap.Client, 0, n)
			for range n {
				c := imap.New(logger)
				if err := c.Connect(cctx, cfg); err != nil {
					for _, open := range clients {
						_ = open.Close()
					}
					return nil, err
				}
				clients = append(clients, c)
			}
			return clients, nil
		})

	// The raw connection: appends the fixture and verifies ground truth.
	rawClient := imap.New(logger)
	if err := rawClient.Connect(ctx, cfg); err != nil {
		t.Fatalf("connecting the raw client: %v", err)
	}
	defer func() { _ = rawClient.Close() }()
	mut, err := imap.Mutator(rawClient)
	if err != nil {
		t.Fatalf("Mutator: %v", err)
	}

	// The fixture message, appended before the sync so one Run ingests it.
	subject := fmt.Sprintf("moov-w1 fixture %d", time.Now().UnixNano())
	inboxUID, err := mut.Append(ctx, "INBOX", w1Message(subject), nil, time.Now())
	if err != nil {
		t.Fatalf("APPEND fixture: %v", err)
	}
	if inboxUID == 0 {
		t.Fatal("APPEND returned no UID; the test needs UIDPLUS")
	}
	// Best-effort cleanup if the test dies before its own destroy path runs.
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		if err := mut.Select(cctx, "INBOX"); err == nil {
			_ = mut.Expunge(cctx, []imap.UID{inboxUID})
		}
	})

	// Initial sync over the real mailbox.
	syncClient := imap.New(logger)
	if err := syncClient.Connect(ctx, cfg); err != nil {
		t.Fatalf("connecting the sync client: %v", err)
	}
	defer func() { _ = syncClient.Close() }()
	syncer, err := syncengine.New(f.store, f.blobs, []imap.Client{syncClient}, syncengine.Options{
		Logger: logger, Connections: 1,
	})
	if err != nil {
		t.Fatalf("sync.New: %v", err)
	}
	if _, err := syncer.Run(ctx, account); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	// The write path, wired exactly as cmd/moovd does.
	exec, err := syncengine.NewWriteExecutor(f.store, connector, syncengine.WriteOptions{Logger: logger})
	if err != nil {
		t.Fatalf("NewWriteExecutor: %v", err)
	}
	defer exec.Close()
	writer, err := mail.NewWriterAdapter(exec)
	if err != nil {
		t.Fatalf("NewWriterAdapter: %v", err)
	}
	f.deps.Writer = writer

	// Resolve the fixture's identities.
	inbox, err := f.store.GetMailboxByName(ctx, account.ID, "INBOX")
	if err != nil {
		t.Fatalf("GetMailboxByName(INBOX): %v", err)
	}
	st, err := f.store.GetMessageStateByUID(ctx, inbox.ID, inbox.UIDValidityOrZero(), int64(inboxUID))
	if err != nil {
		t.Fatalf("the fixture message was not ingested: %v", err)
	}
	wireID := mail.EncodeEmailID(st.MessageID)
	trash, err := f.store.GetMailboxByRole(ctx, account.ID, store.RoleTrash)
	if err != nil {
		t.Fatalf("the test account has no Trash mailbox: %v", err)
	}

	// ---- AC: UNCHANGEDSINCE conflict surfaced as a per-record SetError -----
	//
	// A raw client changes the message AFTER Moov's snapshot and BEFORE any
	// sync pass, so the store's modseq is stale and the content genuinely
	// differs — the exact race the conditional STORE exists for.
	if _, err := rawClient.SelectQResync(ctx, "INBOX", 0, 0); err != nil {
		t.Fatalf("raw SELECT: %v", err)
	}
	if _, err := rawClient.StoreFlags(ctx, []imap.UID{inboxUID},
		imap.FlagDelta{Op: imap.FlagsAdd, Flags: []string{"w1conflict"}}, 0); err != nil {
		t.Fatalf("raw STORE: %v", err)
	}

	conflictResp := f.callQuery(t, "Email/set", fmt.Sprintf(
		`{"accountId":%q,"update":{%q:{"keywords":{"$seen":true}}}}`, f.accountID(), wireID))
	notUpdated, _ := conflictResp["notUpdated"].(map[string]any)
	serr, _ := notUpdated[wireID].(map[string]any)
	if serr == nil || serr["type"] != "stateMismatch" {
		t.Fatalf("concurrent change did not surface as the per-record conflict: %v", conflictResp)
	}

	// The refusal refreshed the store with the server's state, so the SAME
	// request, resubmitted, succeeds — and being a full-set, it also erases
	// the conflicting keyword, which raw IMAP must confirm.
	retryResp := f.callQuery(t, "Email/set", fmt.Sprintf(
		`{"accountId":%q,"update":{%q:{"keywords":{"$seen":true}}}}`, f.accountID(), wireID))
	if updated, _ := retryResp["updated"].(map[string]any); updated == nil {
		t.Fatalf("retry after conflict did not succeed: %v", retryResp)
	} else if _, ok := updated[wireID]; !ok {
		t.Fatalf("retry after conflict did not update the id: %v", retryResp)
	}
	flags, keywords, ok := rawMessageFlags(ctx, t, rawClient, "INBOX", inboxUID)
	if !ok {
		t.Fatal("the fixture message vanished")
	}
	if !hasName(flags, "seen") || hasName(keywords, "w1conflict") {
		t.Fatalf("raw state after full-set = %v %v, want seen and no w1conflict", flags, keywords)
	}

	// ---- AC: JMAP /set -> visible via raw IMAP (patch syntax) --------------
	patchResp := f.callQuery(t, "Email/set", fmt.Sprintf(
		`{"accountId":%q,"update":{%q:{"keywords/$flagged":true}}}`, f.accountID(), wireID))
	if _, ok := object(t, patchResp, "updated")[wireID]; !ok {
		t.Fatalf("patch update failed: %v", patchResp)
	}
	flags, _, ok = rawMessageFlags(ctx, t, rawClient, "INBOX", inboxUID)
	if !ok || !hasName(flags, "flagged") {
		t.Fatalf("raw flags = %v, want \\Flagged set by JMAP", flags)
	}

	// ---- AC: replay idempotency --------------------------------------------
	beforeReplay, err := f.store.GetMessageState(ctx, st.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	replayResp := f.callQuery(t, "Email/set", fmt.Sprintf(
		`{"accountId":%q,"update":{%q:{"keywords/$flagged":true}}}`, f.accountID(), wireID))
	if _, ok := object(t, replayResp, "updated")[wireID]; !ok {
		t.Fatalf("replayed patch refused: %v", replayResp)
	}
	afterReplay, err := f.store.GetMessageState(ctx, st.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if afterReplay.ModSeqSeen != beforeReplay.ModSeqSeen || !afterReplay.UpdatedAt.Equal(beforeReplay.UpdatedAt) {
		t.Errorf("replay moved the row (modseq %d->%d): a no-op must cost nothing",
			beforeReplay.ModSeqSeen, afterReplay.ModSeqSeen)
	}

	// ---- the real watcher joins --------------------------------------------
	watcher, err := syncengine.NewPushWatcher(f.store, f.blobs, syncengine.WatcherOptions{
		Options:           syncengine.Options{Logger: logger, Connections: 1},
		Connector:         connector,
		ReconcileInterval: -1, // determinism: only NOTIFY drives passes here
	})
	if err != nil {
		t.Fatalf("NewPushWatcher: %v", err)
	}
	wctx, wcancel := context.WithCancel(ctx)
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		_ = watcher.Watch(wctx, account)
	}()
	stopWatcher := func() {
		wcancel()
		select {
		case <-watcherDone:
		case <-time.After(30 * time.Second):
			t.Error("the watcher did not stop")
		}
	}
	defer stopWatcher()

	// ---- AC: raw IMAP change -> JMAP sees it (via the watcher) -------------
	if _, err := rawClient.SelectQResync(ctx, "INBOX", 0, 0); err != nil {
		t.Fatalf("raw SELECT: %v", err)
	}
	if _, err := rawClient.StoreFlags(ctx, []imap.UID{inboxUID},
		imap.FlagDelta{Op: imap.FlagsAdd, Flags: []string{"answered"}}, 0); err != nil {
		t.Fatalf("raw STORE answered: %v", err)
	}
	waitFor(t, "the watcher to ingest the raw \\Answered", 30*time.Second, func() bool {
		got := f.callQuery(t, "Email/get", fmt.Sprintf(
			`{"accountId":%q,"ids":[%q],"properties":["keywords"]}`, f.accountID(), wireID))
		list, _ := got["list"].([]any)
		if len(list) != 1 {
			return false
		}
		kw, _ := list[0].(map[string]any)["keywords"].(map[string]any)
		_, has := kw["$answered"]
		return has
	})

	// ---- AC: our own write's echo converges --------------------------------
	echoResp := f.callQuery(t, "Email/set", fmt.Sprintf(
		`{"accountId":%q,"update":{%q:{"keywords/$flagged":null}}}`, f.accountID(), wireID))
	if _, ok := object(t, echoResp, "updated")[wireID]; !ok {
		t.Fatalf("echo-phase update failed: %v", echoResp)
	}
	reflected, err := f.store.GetMessageState(ctx, st.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	// Give the NOTIFY echo ample time to arrive and be processed, then prove
	// it changed nothing: same row, same cursor position, no tombstone, no
	// duplicate.
	time.Sleep(3 * time.Second)
	afterEcho, err := f.store.GetMessageState(ctx, st.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if !afterEcho.UpdatedAt.Equal(reflected.UpdatedAt) || afterEcho.ModSeqSeen != reflected.ModSeqSeen {
		t.Errorf("the watcher echo re-touched our own write (updated_at %v -> %v, modseq %d -> %d): not convergent",
			reflected.UpdatedAt, afterEcho.UpdatedAt, reflected.ModSeqSeen, afterEcho.ModSeqSeen)
	}
	if afterEcho.DeletedAt != nil || afterEcho.MailboxID != inbox.ID {
		t.Errorf("the echo corrupted the row: %+v", afterEcho)
	}

	// ---- AC: destroy outside Trash = MOVE to Trash (W-A2) ------------------
	destroyResp := f.callQuery(t, "Email/set", fmt.Sprintf(
		`{"accountId":%q,"destroy":[%q]}`, f.accountID(), wireID))
	destroyed, _ := destroyResp["destroyed"].([]any)
	if len(destroyed) != 1 || destroyed[0] != wireID {
		t.Fatalf("destroy response = %v", destroyResp)
	}

	afterDestroy, err := f.store.GetMessageState(ctx, st.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if afterDestroy.MailboxID != trash.ID || afterDestroy.DeletedAt != nil {
		t.Fatalf("destroy outside Trash must move, not kill: %+v", afterDestroy)
	}
	trashUID := imap.UID(afterDestroy.UID) // #nosec G115 -- store UIDs fit uint32 by construction
	if _, _, ok := rawMessageFlags(ctx, t, rawClient, trash.Name, trashUID); !ok {
		t.Fatalf("the message is not in the server's %q at uid %d", trash.Name, trashUID)
	}
	if _, _, ok := rawMessageFlags(ctx, t, rawClient, "INBOX", inboxUID); ok {
		t.Fatal("the message is still in the server's INBOX after destroy")
	}

	// ---- AC: destroy inside Trash = \Deleted + UID EXPUNGE (W-A2) ----------
	finalResp := f.callQuery(t, "Email/set", fmt.Sprintf(
		`{"accountId":%q,"destroy":[%q]}`, f.accountID(), wireID))
	finalDestroyed, _ := finalResp["destroyed"].([]any)
	if len(finalDestroyed) != 1 {
		t.Fatalf("second destroy response = %v", finalResp)
	}
	if _, _, ok := rawMessageFlags(ctx, t, rawClient, trash.Name, trashUID); ok {
		t.Fatal("the message survived the in-Trash destroy on the server")
	}
	tombstone, err := f.store.GetMessageState(ctx, st.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if tombstone.DeletedAt == nil {
		t.Error("the store row was not tombstoned after the final destroy")
	}

	// A destroyed id is notFound on replay (§5.3) — and stays destroyed.
	replayDestroy := f.callQuery(t, "Email/set", fmt.Sprintf(
		`{"accountId":%q,"destroy":[%q]}`, f.accountID(), wireID))
	nd, _ := replayDestroy["notDestroyed"].(map[string]any)
	if e, _ := nd[wireID].(map[string]any); e == nil || e["type"] != "notFound" {
		t.Errorf("replayed destroy = %v, want notFound", replayDestroy)
	}

	// Let the expunge echoes settle, then confirm the tombstone did not flap.
	time.Sleep(2 * time.Second)
	settled, err := f.store.GetMessageState(ctx, st.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.DeletedAt == nil {
		t.Error("the destroy echo resurrected the tombstone")
	}

	stopWatcher()
	t.Logf("W1 write core round-tripped against the real Dovecot: conflict, patch, replay, watcher echo, both W-A2 destroys")
}
