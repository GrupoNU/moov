package store_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// Tests for the store API, against a real PostgreSQL 17.
//
// They share the database with the migration tests, so every test creates its
// own account and scopes its assertions to it. That mirrors production — the
// store is multi-tenant and account scoping is the property most of these
// methods exist to guarantee — and it means the suite does not need to
// serialize on a truncate between tests.

func testStore(t *testing.T) *store.Store {
	t.Helper()

	dsn := os.Getenv(testDBEnv)
	if dsn == "" {
		t.Skipf("%s is not set; start a dev database with `make db-up` to run the store tests", testDBEnv)
	}

	// Migrations first: the schema must exist before the pools are used.
	db := migrated(t)
	_ = db

	ctx := context.Background()
	s, err := store.Open(ctx, store.Config{
		DSN:              dsn,
		MaxConns:         8,
		AnalyticMaxConns: 2,
	})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// newAccount creates an isolated account for one test and removes it
// afterwards, taking its mailboxes, messages and state with it.
func newAccount(t *testing.T, s *store.Store) store.Account {
	t.Helper()
	ctx := context.Background()

	email := fmt.Sprintf("t-%s-%d@example.test", sanitizeName(t.Name()), time.Now().UnixNano())
	acct, err := s.CreateAccount(ctx, store.Account{
		Email:    email,
		IMAPHost: "dovecot.internal",
		IMAPPort: 143,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	t.Cleanup(func() {
		if err := s.DeleteAccount(context.Background(), acct.ID); err != nil {
			t.Logf("cleanup: deleting account %d: %v", acct.ID, err)
		}
	})
	return acct
}

func sanitizeName(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}

// seedBlob registers a blob row so messages can reference it. The bytes
// themselves are internal/blob's business; here only the foreign key matters.
func seedBlob(t *testing.T, s *store.Store, content string) []byte {
	t.Helper()
	sum := sha256.Sum256([]byte(content))

	_, err := s.Pool().Exec(context.Background(), `
		INSERT INTO blobs (sha256, size, refcount, zero_ref_since)
		VALUES ($1, $2, 1, NULL)
		ON CONFLICT (sha256) DO NOTHING`, sum[:], len(content))
	if err != nil {
		t.Fatalf("seeding blob: %v", err)
	}
	return sum[:]
}

// ---------------------------------------------------------------------------
// accounts
// ---------------------------------------------------------------------------

func TestAccountCRUD(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)

	if acct.ID == 0 {
		t.Fatal("CreateAccount returned an account with no id")
	}
	if acct.CredentialState != store.CredentialPending {
		t.Errorf("new account credential state = %q, want pending", acct.CredentialState)
	}
	if acct.State != store.AccountActive {
		t.Errorf("new account state = %q, want active", acct.State)
	}

	got, err := s.GetAccount(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if got.Email != acct.Email {
		t.Errorf("GetAccount email = %q, want %q", got.Email, acct.Email)
	}

	byEmail, err := s.GetAccountByEmail(ctx, acct.Email)
	if err != nil {
		t.Fatalf("GetAccountByEmail: %v", err)
	}
	if byEmail.ID != acct.ID {
		t.Errorf("GetAccountByEmail id = %d, want %d", byEmail.ID, acct.ID)
	}

	if _, err := s.GetAccount(ctx, 999_999_999); err == nil {
		t.Error("GetAccount on a missing id returned no error")
	} else if !isNotFound(err) {
		t.Errorf("GetAccount on a missing id = %v, want ErrNotFound", err)
	}
}

// The app password is stored as opaque bytes and the store never sees a
// plaintext user password. E7 owns the encryption; this asserts the store's
// half of that contract — that what goes in comes back byte-identical and
// untransformed.
func TestAccountCredentialsAreOpaqueBytes(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)

	// Deliberately not valid UTF-8: real AES-256-GCM ciphertext is not, and a
	// column or driver that mangles it would corrupt every credential.
	ciphertext := []byte{0x00, 0xff, 0xfe, 0x01, 0x80, 0x7f, 0xc3, 0x28}

	if err := s.SetAccountCredentials(ctx, acct.ID, "user@example.test", ciphertext); err != nil {
		t.Fatalf("SetAccountCredentials: %v", err)
	}

	got, err := s.GetAccount(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if string(got.IMAPAppPassword) != string(ciphertext) {
		t.Errorf("app password round-trip = %v, want %v", got.IMAPAppPassword, ciphertext)
	}
	if got.CredentialState != store.CredentialActive {
		t.Errorf("credential state = %q, want active", got.CredentialState)
	}

	if err := s.SetAccountCredentialState(ctx, acct.ID, store.CredentialRevoked); err != nil {
		t.Fatalf("SetAccountCredentialState: %v", err)
	}
	got, err = s.GetAccount(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if got.CredentialState != store.CredentialRevoked {
		t.Errorf("credential state = %q, want revoked", got.CredentialState)
	}
}

// ---------------------------------------------------------------------------
// mailboxes
// ---------------------------------------------------------------------------

func TestMailboxCRUDAndRoles(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)

	inbox, err := s.UpsertMailbox(ctx, store.Mailbox{
		AccountID: acct.ID, Name: "INBOX", Role: store.RoleInbox,
		Subscribed: true, Selectable: true,
	})
	if err != nil {
		t.Fatalf("UpsertMailbox: %v", err)
	}
	if inbox.Role != store.RoleInbox {
		t.Errorf("role = %q, want inbox", inbox.Role)
	}
	if inbox.BackfillState != store.BackfillPending {
		t.Errorf("backfill state = %q, want pending", inbox.BackfillState)
	}

	// A plain folder has no role, which must be NULL rather than "" so the
	// partial unique index on (account_id, role) admits many of them.
	for _, name := range []string{"Projects", "Projects/2026"} {
		if _, err := s.UpsertMailbox(ctx, store.Mailbox{
			AccountID: acct.ID, Name: name, Subscribed: true, Selectable: true,
		}); err != nil {
			t.Fatalf("UpsertMailbox(%s): %v", name, err)
		}
	}

	byRole, err := s.GetMailboxByRole(ctx, acct.ID, store.RoleInbox)
	if err != nil {
		t.Fatalf("GetMailboxByRole: %v", err)
	}
	if byRole.ID != inbox.ID {
		t.Errorf("GetMailboxByRole id = %d, want %d", byRole.ID, inbox.ID)
	}

	// Upsert is idempotent on (account_id, name): a LIST refresh must not
	// create duplicates.
	again, err := s.UpsertMailbox(ctx, store.Mailbox{
		AccountID: acct.ID, Name: "INBOX", Role: store.RoleInbox,
		Subscribed: true, Selectable: true,
	})
	if err != nil {
		t.Fatalf("re-upserting INBOX: %v", err)
	}
	if again.ID != inbox.ID {
		t.Errorf("re-upsert created a new mailbox %d, want %d", again.ID, inbox.ID)
	}

	boxes, err := s.ListMailboxes(ctx, acct.ID)
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	if len(boxes) != 3 {
		t.Errorf("ListMailboxes returned %d mailboxes, want 3", len(boxes))
	}
}

// A LIST refresh happens on every reconnect and must never reset the QRESYNC
// resume point of a mailbox it merely re-listed. Losing highestmodseq would
// turn an incremental sync into a full resync of the folder.
func TestUpsertMailboxPreservesSyncState(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)

	mbox, err := s.UpsertMailbox(ctx, store.Mailbox{
		AccountID: acct.ID, Name: "INBOX", Role: store.RoleInbox,
		Subscribed: true, Selectable: true,
	})
	if err != nil {
		t.Fatalf("UpsertMailbox: %v", err)
	}
	if err := s.SetMailboxSyncState(ctx, mbox.ID, 42, 1000, 987654); err != nil {
		t.Fatalf("SetMailboxSyncState: %v", err)
	}
	uidLow := int64(500)
	if err := s.SetBackfillProgress(ctx, mbox.ID, store.BackfillInProgress, &uidLow); err != nil {
		t.Fatalf("SetBackfillProgress: %v", err)
	}

	// Re-list, as a reconnect would.
	if _, err := s.UpsertMailbox(ctx, store.Mailbox{
		AccountID: acct.ID, Name: "INBOX", Role: store.RoleInbox,
		Subscribed: true, Selectable: true,
	}); err != nil {
		t.Fatalf("re-upserting: %v", err)
	}

	got, err := s.GetMailbox(ctx, mbox.ID)
	if err != nil {
		t.Fatalf("GetMailbox: %v", err)
	}
	if got.UIDValidity == nil || *got.UIDValidity != 42 {
		t.Errorf("uidvalidity = %v, want 42 — an upsert must not reset the QRESYNC resume point", got.UIDValidity)
	}
	if got.HighestModSeq == nil || *got.HighestModSeq != 987654 {
		t.Errorf("highestmodseq = %v, want 987654", got.HighestModSeq)
	}
	if got.BackfillState != store.BackfillInProgress {
		t.Errorf("backfill state = %q, want in_progress", got.BackfillState)
	}
	if got.BackfillUIDLow == nil || *got.BackfillUIDLow != 500 {
		t.Errorf("backfill_uid_low = %v, want 500", got.BackfillUIDLow)
	}
}

// ---------------------------------------------------------------------------
// messages
// ---------------------------------------------------------------------------

func TestInsertMessagesAndReadBack(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	mbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)

	now := time.Now().UTC().Truncate(time.Second)
	msgs := []store.NewMessage{
		{
			Message: store.Message{
				AccountID: acct.ID,
				RawSHA256: seedBlob(t, s, "raw-message-one"),
				RawSize:   1024,
				MessageID: "one@example.test",
				Subject:   "Primera factura",
				FromAddr:  "ana@example.test",
				ToAddrs:   "bob@example.test",
				BodyText:  "cuerpo del mensaje uno",
				Date:      now,
			},
			State: store.MessageState{
				AccountID: acct.ID, MailboxID: mbox.ID,
				UID: 1, UIDValidity: 1, Flags: store.FlagSeen,
				Keywords: []string{"$MoovL1"}, ModSeqSeen: 100,
			},
		},
		{
			Message: store.Message{
				AccountID: acct.ID,
				RawSHA256: seedBlob(t, s, "raw-message-two"),
				RawSize:   2048,
				MessageID: "two@example.test",
				Subject:   "Segunda factura",
				FromAddr:  "carlos@example.test",
				BodyText:  "cuerpo del mensaje dos",
				Date:      now.Add(-time.Hour),
			},
			State: store.MessageState{
				AccountID: acct.ID, MailboxID: mbox.ID,
				UID: 2, UIDValidity: 1, ModSeqSeen: 101,
			},
		},
	}

	ids, err := s.InsertMessages(ctx, msgs)
	if err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("InsertMessages returned %d ids, want 2", len(ids))
	}

	got, err := s.GetMessage(ctx, ids[0])
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.Subject != "Primera factura" {
		t.Errorf("subject = %q", got.Subject)
	}
	if got.ParseStatus != store.ParseOK {
		t.Errorf("parse status = %q, want ok (the default)", got.ParseStatus)
	}

	st, err := s.GetMessageState(ctx, ids[0])
	if err != nil {
		t.Fatalf("GetMessageState: %v", err)
	}
	if !st.Flags.Has(store.FlagSeen) {
		t.Errorf("flags = %s, want \\Seen", st.Flags)
	}
	if len(st.Keywords) != 1 || st.Keywords[0] != "$MoovL1" {
		t.Errorf("keywords = %v, want [$MoovL1]", st.Keywords)
	}

	byUID, err := s.GetMessageStateByUID(ctx, mbox.ID, 1, 2)
	if err != nil {
		t.Fatalf("GetMessageStateByUID: %v", err)
	}
	if byUID.MessageID != ids[1] {
		t.Errorf("GetMessageStateByUID returned message %d, want %d", byUID.MessageID, ids[1])
	}

	existing, err := s.ExistingUIDs(ctx, mbox.ID, 1, []int64{1, 2, 3, 4})
	if err != nil {
		t.Fatalf("ExistingUIDs: %v", err)
	}
	if !existing[1] || !existing[2] || existing[3] || existing[4] {
		t.Errorf("ExistingUIDs = %v, want {1:true, 2:true}", existing)
	}

	total, unread, err := s.CountMailboxMessages(ctx, mbox.ID)
	if err != nil {
		t.Fatalf("CountMailboxMessages: %v", err)
	}
	if total != 2 || unread != 1 {
		t.Errorf("counts = (%d total, %d unread), want (2, 1)", total, unread)
	}
}

// THE A5 TEST.
//
// A flag update must touch message_state and leave the messages row — and
// therefore the ~2.2 KB generated tsv and its GIN index entries — completely
// alone. S3 §4.5 measured what the alternative costs; this test is what stops
// the design from quietly regressing to it.
//
// It compares the tsv itself and the row's xmin (its last-writing transaction
// id). An unchanged xmin proves the heap tuple was never rewritten, which is
// a stronger statement than the tsv merely holding the same value.
func TestUpdateFlagsDoesNotRewriteMessageRow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	mbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)

	ids, err := s.InsertMessages(ctx, []store.NewMessage{{
		Message: store.Message{
			AccountID: acct.ID,
			RawSHA256: seedBlob(t, s, "a5-test-message"),
			RawSize:   512,
			Subject:   "Mensaje inmutable",
			BodyText:  "el cuerpo no cambia nunca",
			Date:      time.Now().UTC(),
		},
		State: store.MessageState{
			AccountID: acct.ID, MailboxID: mbox.ID, UID: 1, UIDValidity: 1,
		},
	}})
	if err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}
	id := ids[0]

	var tsvBefore string
	var xminBefore int64
	if err := s.Pool().QueryRow(ctx,
		`SELECT tsv::text, xmin::text::bigint FROM messages WHERE id = $1`, id,
	).Scan(&tsvBefore, &xminBefore); err != nil {
		t.Fatalf("reading tsv before: %v", err)
	}

	for i := range 5 {
		if err := s.UpdateFlags(ctx, []store.FlagUpdate{{
			MessageID:  id,
			Flags:      store.FlagSeen | store.FlagFlagged,
			Keywords:   []string{"$MoovL2"},
			ModSeqSeen: int64(200 + i),
		}}); err != nil {
			t.Fatalf("UpdateFlags: %v", err)
		}
	}

	var tsvAfter string
	var xminAfter int64
	if err := s.Pool().QueryRow(ctx,
		`SELECT tsv::text, xmin::text::bigint FROM messages WHERE id = $1`, id,
	).Scan(&tsvAfter, &xminAfter); err != nil {
		t.Fatalf("reading tsv after: %v", err)
	}

	if tsvAfter != tsvBefore {
		t.Errorf("the tsv changed across a flag update:\nbefore: %s\nafter:  %s", tsvBefore, tsvAfter)
	}
	if xminAfter != xminBefore {
		t.Errorf("the messages row was rewritten by a flag update (xmin %d -> %d).\n"+
			"This is arbitration A5: flag churn must touch message_state ONLY, or every "+
			"read/unread toggle rewrites the ~2.2 KB tsv into the GIN index (S3 §4.5).",
			xminBefore, xminAfter)
	}

	// The update did land where it belongs.
	st, err := s.GetMessageState(ctx, id)
	if err != nil {
		t.Fatalf("GetMessageState: %v", err)
	}
	if !st.Flags.Has(store.FlagSeen | store.FlagFlagged) {
		t.Errorf("flags = %s, want \\Seen \\Flagged", st.Flags)
	}
	if st.ModSeqSeen != 204 {
		t.Errorf("modseq_seen = %d, want 204", st.ModSeqSeen)
	}
}

// A move is an UPDATE of message_state; the content is never touched, which is
// what lets a message survive a folder change without re-download (L2 §2.3).
func TestMoveMessagesTouchesOnlyState(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	inbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)
	archive := seedMailbox(t, s, acct.ID, "Archive", store.RoleArchive)

	ids, err := s.InsertMessages(ctx, []store.NewMessage{{
		Message: store.Message{
			AccountID: acct.ID, RawSHA256: seedBlob(t, s, "move-me"),
			RawSize: 256, Subject: "Para archivar", Date: time.Now().UTC(),
		},
		State: store.MessageState{
			AccountID: acct.ID, MailboxID: inbox.ID, UID: 7, UIDValidity: 1,
		},
	}})
	if err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	var xminBefore int64
	if err := s.Pool().QueryRow(ctx,
		`SELECT xmin::text::bigint FROM messages WHERE id = $1`, ids[0]).Scan(&xminBefore); err != nil {
		t.Fatalf("reading xmin: %v", err)
	}

	if err := s.MoveMessages(ctx, ids, archive.ID, 1, []int64{99}); err != nil {
		t.Fatalf("MoveMessages: %v", err)
	}

	st, err := s.GetMessageState(ctx, ids[0])
	if err != nil {
		t.Fatalf("GetMessageState: %v", err)
	}
	if st.MailboxID != archive.ID {
		t.Errorf("mailbox = %d, want %d", st.MailboxID, archive.ID)
	}
	if st.UID != 99 {
		t.Errorf("uid = %d, want 99", st.UID)
	}

	var xminAfter int64
	if err := s.Pool().QueryRow(ctx,
		`SELECT xmin::text::bigint FROM messages WHERE id = $1`, ids[0]).Scan(&xminAfter); err != nil {
		t.Fatalf("reading xmin: %v", err)
	}
	if xminAfter != xminBefore {
		t.Errorf("a move rewrote the messages row (xmin %d -> %d); it must touch message_state only",
			xminBefore, xminAfter)
	}
}

// Email/changes: everything an account touched since a cursor, in order.
func TestChangedSinceFeedsEmailChanges(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	mbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)

	ids := insertN(t, s, acct.ID, mbox.ID, 5, "changes")

	cursor := time.Now().UTC()
	// Give the clock room: updated_at has microsecond resolution, and a
	// strictly-greater cursor comparison needs the update to land after it.
	time.Sleep(10 * time.Millisecond)

	if err := s.UpdateFlags(ctx, []store.FlagUpdate{
		{MessageID: ids[1], Flags: store.FlagSeen, ModSeqSeen: 10},
		{MessageID: ids[3], Flags: store.FlagFlagged, ModSeqSeen: 11},
	}); err != nil {
		t.Fatalf("UpdateFlags: %v", err)
	}

	changed, err := s.ChangedSince(ctx, acct.ID, cursor, 100)
	if err != nil {
		t.Fatalf("ChangedSince: %v", err)
	}
	if len(changed) != 2 {
		t.Fatalf("ChangedSince returned %d rows, want 2", len(changed))
	}

	seen := map[int64]bool{}
	for _, c := range changed {
		seen[c.MessageID] = true
	}
	if !seen[ids[1]] || !seen[ids[3]] {
		t.Errorf("ChangedSince returned %v, want messages %d and %d", seen, ids[1], ids[3])
	}
}

// Content identity survives everything IMAP does to a message (S2 H8): the
// sha256 is how a resync finds bytes it already holds.
func TestFindMessageByHash(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	mbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)

	hash := seedBlob(t, s, "content-addressed-identity")
	if _, err := s.InsertMessages(ctx, []store.NewMessage{{
		Message: store.Message{
			AccountID: acct.ID, RawSHA256: hash, RawSize: 128,
			Subject: "Identidad por contenido", Date: time.Now().UTC(),
		},
		State: store.MessageState{
			AccountID: acct.ID, MailboxID: mbox.ID, UID: 1, UIDValidity: 1,
		},
	}}); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	found, err := s.FindMessageByHash(ctx, acct.ID, hash)
	if err != nil {
		t.Fatalf("FindMessageByHash: %v", err)
	}
	if found.Subject != "Identidad por contenido" {
		t.Errorf("subject = %q", found.Subject)
	}

	// Another account's identical content must not be visible: account
	// scoping is a security property, not an optimization.
	other := newAccount(t, s)
	if _, err := s.FindMessageByHash(ctx, other.ID, hash); !isNotFound(err) {
		t.Errorf("FindMessageByHash across accounts = %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// sync_log and intents
// ---------------------------------------------------------------------------

func TestCheckpointLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)

	// A never-synced account reads as a zero checkpoint, not an error.
	cp, err := s.GetCheckpoint(ctx, acct.ID, store.AccountScope)
	if err != nil {
		t.Fatalf("GetCheckpoint on a fresh account: %v", err)
	}
	if cp.StateCounter != 0 {
		t.Errorf("fresh checkpoint state counter = %d, want 0", cp.StateCounter)
	}
	if cp.BreakerState != store.BreakerClosed {
		t.Errorf("fresh breaker state = %q, want closed", cp.BreakerState)
	}

	if err := s.SaveCheckpoint(ctx, acct.ID, store.AccountScope, []byte(`{"modseq":42}`)); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	cp, err = s.GetCheckpoint(ctx, acct.ID, store.AccountScope)
	if err != nil {
		t.Fatalf("GetCheckpoint: %v", err)
	}
	if string(cp.Checkpoint) != `{"modseq": 42}` && string(cp.Checkpoint) != `{"modseq":42}` {
		t.Errorf("checkpoint = %s", cp.Checkpoint)
	}
	first := cp.StateCounter

	// The state counter is what JMAP hands clients as Email/changes state. It
	// must advance monotonically and never reset, or clients silently miss
	// changes.
	if err := s.SaveCheckpoint(ctx, acct.ID, store.AccountScope, []byte(`{"modseq":43}`)); err != nil {
		t.Fatalf("second SaveCheckpoint: %v", err)
	}
	cp, err = s.GetCheckpoint(ctx, acct.ID, store.AccountScope)
	if err != nil {
		t.Fatalf("GetCheckpoint: %v", err)
	}
	if cp.StateCounter <= first {
		t.Errorf("state counter went from %d to %d; it must increase monotonically", first, cp.StateCounter)
	}
}

func TestSyncErrorsAndBreaker(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)

	for want := 1; want <= 3; want++ {
		got, err := s.RecordSyncError(ctx, acct.ID, store.AccountScope, "connection refused")
		if err != nil {
			t.Fatalf("RecordSyncError: %v", err)
		}
		if got != want {
			t.Errorf("consecutive errors = %d, want %d", got, want)
		}
	}

	until := time.Now().Add(5 * time.Minute)
	if err := s.SetBreakerState(ctx, acct.ID, store.AccountScope, store.BreakerOpen, &until); err != nil {
		t.Fatalf("SetBreakerState: %v", err)
	}
	cp, err := s.GetCheckpoint(ctx, acct.ID, store.AccountScope)
	if err != nil {
		t.Fatalf("GetCheckpoint: %v", err)
	}
	if cp.BreakerState != store.BreakerOpen {
		t.Errorf("breaker = %q, want open", cp.BreakerState)
	}
	if cp.LastError != "connection refused" {
		t.Errorf("last error = %q", cp.LastError)
	}

	// A successful pass clears the error history and closes the breaker:
	// leaving a stale error behind would be a lie the next pass works around.
	if err := s.SaveCheckpoint(ctx, acct.ID, store.AccountScope, []byte(`{}`)); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	cp, err = s.GetCheckpoint(ctx, acct.ID, store.AccountScope)
	if err != nil {
		t.Fatalf("GetCheckpoint: %v", err)
	}
	if cp.ConsecutiveErrors != 0 {
		t.Errorf("consecutive errors after success = %d, want 0", cp.ConsecutiveErrors)
	}
	if cp.BreakerState != store.BreakerClosed {
		t.Errorf("breaker after success = %q, want closed", cp.BreakerState)
	}
	if cp.LastError != "" {
		t.Errorf("last error after success = %q, want empty", cp.LastError)
	}
}

func TestIntentQueue(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)

	in, err := s.EnqueueIntent(ctx, acct.ID, store.IntentFlag,
		[]byte(`{"messageIds":[1,2],"add":["\\Seen"]}`), time.Time{})
	if err != nil {
		t.Fatalf("EnqueueIntent: %v", err)
	}
	if in.State != store.IntentQueued {
		t.Errorf("state = %q, want queued", in.State)
	}

	// An intent scheduled in the future must not be claimable yet: that delay
	// is what undo-send is built on.
	future, err := s.EnqueueIntent(ctx, acct.ID, store.IntentSend,
		[]byte(`{}`), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("EnqueueIntent (future): %v", err)
	}

	claimed, err := s.ClaimIntents(ctx, acct.ID, 10)
	if err != nil {
		t.Fatalf("ClaimIntents: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d intents, want 1 (the future one must not be claimable)", len(claimed))
	}
	if claimed[0].ID != in.ID {
		t.Errorf("claimed intent %d, want %d", claimed[0].ID, in.ID)
	}
	if claimed[0].State != store.IntentInFlight {
		t.Errorf("claimed state = %q, want in_flight", claimed[0].State)
	}
	if claimed[0].Attempts != 1 {
		t.Errorf("attempts = %d, want 1", claimed[0].Attempts)
	}

	// A claimed intent is not claimable again.
	again, err := s.ClaimIntents(ctx, acct.ID, 10)
	if err != nil {
		t.Fatalf("second ClaimIntents: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("claimed %d already-claimed intents, want 0", len(again))
	}

	if err := s.CompleteIntent(ctx, in.ID); err != nil {
		t.Fatalf("CompleteIntent: %v", err)
	}
	done, err := s.GetIntent(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetIntent: %v", err)
	}
	if done.State != store.IntentDone {
		t.Errorf("state = %q, want done", done.State)
	}

	// A failure with a retry time returns the intent to the queue.
	retryAt := time.Now().Add(-time.Minute) // already due
	if err := s.FailIntent(ctx, future.ID, "smtp timeout", &retryAt); err != nil {
		t.Fatalf("FailIntent: %v", err)
	}
	retried, err := s.ClaimIntents(ctx, acct.ID, 10)
	if err != nil {
		t.Fatalf("ClaimIntents after retry: %v", err)
	}
	if len(retried) != 1 || retried[0].ID != future.ID {
		t.Errorf("retried intents = %v, want the failed one requeued", retried)
	}
	if retried[0].LastError != "smtp timeout" {
		t.Errorf("last error = %q", retried[0].LastError)
	}
}

// ---------------------------------------------------------------------------
// helpers shared with search_test.go
// ---------------------------------------------------------------------------

func seedMailbox(t *testing.T, s *store.Store, accountID int64, name string, role store.MailboxRole) store.Mailbox {
	t.Helper()
	mbox, err := s.UpsertMailbox(context.Background(), store.Mailbox{
		AccountID: accountID, Name: name, Role: role,
		Subscribed: true, Selectable: true,
	})
	if err != nil {
		t.Fatalf("seeding mailbox %s: %v", name, err)
	}
	return mbox
}

// insertN inserts n trivial messages and returns their ids.
func insertN(t *testing.T, s *store.Store, accountID, mailboxID int64, n int, tag string) []int64 {
	t.Helper()

	msgs := make([]store.NewMessage, n)
	base := time.Now().UTC().Add(-time.Duration(n) * time.Hour)
	for i := range n {
		msgs[i] = store.NewMessage{
			Message: store.Message{
				AccountID: accountID,
				RawSHA256: seedBlob(t, s, fmt.Sprintf("%s-%d-%d", tag, accountID, i)),
				RawSize:   int64(100 + i),
				Subject:   fmt.Sprintf("%s mensaje %d", tag, i),
				FromAddr:  fmt.Sprintf("sender%d@example.test", i),
				BodyText:  fmt.Sprintf("cuerpo de prueba numero %d", i),
				Date:      base.Add(time.Duration(i) * time.Hour),
			},
			State: store.MessageState{
				AccountID: accountID, MailboxID: mailboxID,
				UID: int64(i + 1), UIDValidity: 1, ModSeqSeen: int64(i),
			},
		}
	}

	ids, err := s.InsertMessages(context.Background(), msgs)
	if err != nil {
		t.Fatalf("inserting %d messages: %v", n, err)
	}
	return ids
}

func isNotFound(err error) bool {
	return errors.Is(err, store.ErrNotFound)
}
