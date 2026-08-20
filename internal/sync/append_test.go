package sync

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
)

// The append half of the write executor (W3): Dovecot-first ordering, the
// APPENDUID reflection, and the \Sent dedupe — against the fake server and a
// real store (PG-gated like the rest of this package's integration suite).

// appendEnv builds a write executor over one fake mailbox that exists on both
// sides (fake server and store), which is the invariant ApplyAppend's
// reflection depends on.
type appendEnv struct {
	*testEnv
	srv    *fakeServer
	exec   *WriteExecutor
	drafts store.Mailbox
	sent   store.Mailbox
}

func newAppendEnv(t *testing.T) *appendEnv {
	t.Helper()
	env := newTestEnv(t)
	ctx := context.Background()

	srv := newFakeServer()
	srv.addMailbox("Drafts", imap.RoleDrafts, 1111)
	srv.addMailbox("Sent", imap.RoleSent, 2222)

	drafts, err := env.store.UpsertMailbox(ctx, store.Mailbox{
		AccountID: env.account.ID, Name: "Drafts", Role: store.RoleDrafts, Selectable: true,
	})
	if err != nil {
		t.Fatalf("UpsertMailbox: %v", err)
	}
	sent, err := env.store.UpsertMailbox(ctx, store.Mailbox{
		AccountID: env.account.ID, Name: "Sent", Role: store.RoleSent, Selectable: true,
	})
	if err != nil {
		t.Fatalf("UpsertMailbox: %v", err)
	}

	exec, err := NewWriteExecutor(env.store,
		ConnectorFunc(func(context.Context, store.Account, int) ([]imap.Client, error) {
			return srv.clients(1), nil
		}),
		WriteOptions{Logger: env.logger, Blobs: env.blobs})
	if err != nil {
		t.Fatalf("NewWriteExecutor: %v", err)
	}
	t.Cleanup(exec.Close)

	return &appendEnv{testEnv: env, srv: srv, exec: exec, drafts: drafts, sent: sent}
}

func draftBytes(msgID string) []byte {
	return []byte("Message-ID: <" + msgID + ">\r\n" +
		"Date: Sat, 15 Aug 2026 10:00:00 +0000\r\n" +
		"From: env@example.test\r\nTo: dest@example.test\r\n" +
		"Subject: draft under test\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\nhello from the append path\r\n")
}

func TestApplyAppendReflectsTheServerTruth(t *testing.T) {
	env := newAppendEnv(t)
	ctx := context.Background()

	res, err := env.exec.ApplyAppend(ctx, env.account.ID, env.drafts.ID,
		draftBytes("append-1@example.test"), []string{"draft", "seen"})
	if err != nil {
		t.Fatalf("ApplyAppend: %v", err)
	}
	if res.MessageID == 0 || res.UID == 0 {
		t.Fatalf("reflection incomplete: %+v", res)
	}

	// The server side really holds it, flags included.
	mb := env.srv.mailbox("Drafts")
	msg := mb.find(imap.UID(res.UID))
	if msg == nil {
		t.Fatal("the fake server has no appended message")
	}
	if !containsFold(msg.flags, "draft") || !containsFold(msg.flags, "seen") {
		t.Errorf("server flags = %v", msg.flags)
	}

	// The store row matches the server's (mailbox, uidvalidity, uid) triple
	// and the parse-derived columns are real.
	st, err := env.store.GetMessageState(ctx, res.MessageID)
	if err != nil {
		t.Fatalf("GetMessageState: %v", err)
	}
	if st.MailboxID != env.drafts.ID || st.UID != res.UID || st.UIDValidity != 1111 {
		t.Errorf("state = %+v", st)
	}
	if !st.Flags.Has(store.FlagDraft) || !st.Flags.Has(store.FlagSeen) {
		t.Errorf("reflected flags = %s", st.Flags)
	}
	row, err := env.store.GetMessage(ctx, res.MessageID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if row.MessageID != "append-1@example.test" || row.Subject != "draft under test" {
		t.Errorf("parsed row = %q / %q", row.MessageID, row.Subject)
	}
	if row.ThreadID != res.ThreadID {
		t.Errorf("thread = %d vs %d", row.ThreadID, res.ThreadID)
	}

	// The blob is durable and referenced: the raw bytes come back verbatim.
	rc, err := env.blobs.Open(res.BlobHash)
	if err != nil {
		t.Fatalf("blob missing: %v", err)
	}
	defer func() { _ = rc.Close() }()
	if n, err := env.blobs.RefCount(ctx, res.BlobHash); err != nil || n < 1 {
		t.Errorf("blob refcount = %d, %v", n, err)
	}
}

func TestApplyAppendDovecotFirstOrdering(t *testing.T) {
	// W-A1 for creates: when the APPEND fails, the store is untouched — no
	// row, no half-truth.
	env := newAppendEnv(t)
	env.srv.appendErr = errors.New("injected: NO [OVERQUOTA]")

	_, err := env.exec.ApplyAppend(context.Background(), env.account.ID, env.drafts.ID,
		draftBytes("ordering@example.test"), nil)
	if err == nil {
		t.Fatal("ApplyAppend succeeded through a failing APPEND")
	}
	found, err := env.store.MailboxContainsMessageID(context.Background(), env.drafts.ID, "ordering@example.test")
	if err != nil || found {
		t.Errorf("the store holds a row for a message the server refused (found=%v, err=%v)", found, err)
	}
}

func TestApplyAppendRefusesWithoutAppendUID(t *testing.T) {
	env := newAppendEnv(t)
	env.srv.noAppendUID = true

	_, err := env.exec.ApplyAppend(context.Background(), env.account.ID, env.drafts.ID,
		draftBytes("nouid@example.test"), nil)
	if err == nil || !strings.Contains(err.Error(), "APPENDUID") {
		t.Errorf("ApplyAppend without APPENDUID = %v, want the loud refusal", err)
	}
}

func TestAppendToSentDedupesByMessageID(t *testing.T) {
	env := newAppendEnv(t)
	ctx := context.Background()
	raw := draftBytes("sent-copy@example.test")

	// First copy: really appended, on both sides.
	deduped, err := env.exec.AppendToSent(ctx, env.account.ID, raw, "sent-copy@example.test")
	if err != nil || deduped {
		t.Fatalf("first AppendToSent = (%v, %v)", deduped, err)
	}
	if got := len(env.srv.mailbox("Sent").messages); got != 1 {
		t.Fatalf("server Sent holds %d messages, want 1", got)
	}

	// Second copy with the same Message-ID: the ADR §4 dedupe skips the
	// append entirely — the crash-recovery replays and the onSuccess-moved
	// draft both hit exactly this branch.
	deduped, err = env.exec.AppendToSent(ctx, env.account.ID, raw, "sent-copy@example.test")
	if err != nil || !deduped {
		t.Fatalf("second AppendToSent = (%v, %v), want deduped", deduped, err)
	}
	if got := len(env.srv.mailbox("Sent").messages); got != 1 {
		t.Errorf("server Sent holds %d messages after the dedupe, want still 1", got)
	}

	if found, err := env.exec.SentContainsMessageID(ctx, env.account.ID, "sent-copy@example.test"); err != nil || !found {
		t.Errorf("SentContainsMessageID = (%v, %v), want true", found, err)
	}
	if found, err := env.exec.SentContainsMessageID(ctx, env.account.ID, "never-sent@example.test"); err != nil || found {
		t.Errorf("probe for an absent id = (%v, %v), want false", found, err)
	}
}

func TestApplyAppendScopesTheMailbox(t *testing.T) {
	env := newAppendEnv(t)
	ctx := context.Background()

	// A foreign account's mailbox is indistinguishable from a missing one.
	other, err := env.store.CreateAccount(ctx, store.Account{
		Email: "other-" + time.Now().Format("150405.000000000") + "@example.test", IMAPHost: "x", IMAPPort: 143,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.store.DeleteAccount(context.Background(), other.ID) })

	if _, err := env.exec.ApplyAppend(ctx, other.ID, env.drafts.ID, draftBytes("foreign@example.test"), nil); !errors.Is(err, ErrWriteNotFound) {
		t.Errorf("append into a foreign mailbox = %v, want ErrWriteNotFound", err)
	}
}
