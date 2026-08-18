package sync

import (
	"context"
	"errors"
	"testing"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
)

// The folder half of the write executor (W2). Same split as W1's suite — fake
// server, real store — because the properties under test straddle the
// boundary: the W-A1 ordering (Dovecot FIRST, store second), the id stability
// across a rename, the tombstone-before-drop on destroy, and the convergence
// with the discovery pass that will see every one of these changes again.

// mailboxByName reads a stored mailbox row, failing the test if absent.
func (e *writeEnv) mailboxByName(t *testing.T, name string) store.Mailbox {
	t.Helper()
	mb, err := e.store.GetMailboxByName(context.Background(), e.account.ID, name)
	if err != nil {
		t.Fatalf("GetMailboxByName(%q): %v", name, err)
	}
	return mb
}

// mailboxMissing asserts a name is absent from the store.
func (e *writeEnv) mailboxMissing(t *testing.T, name string) {
	t.Helper()
	_, err := e.store.GetMailboxByName(context.Background(), e.account.ID, name)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("mailbox %q is still stored (err=%v)", name, err)
	}
}

// serverHasMailbox reports whether the fake server has a folder.
func (e *writeEnv) serverHasMailbox(name string) bool {
	e.srv.mu.Lock()
	defer e.srv.mu.Unlock()
	return e.srv.mailbox(name) != nil
}

// ---------------------------------------------------------------------------
// create
// ---------------------------------------------------------------------------

func TestApplyMailboxCreateLandsOnBothSides(t *testing.T) {
	env := newWriteEnv(t, 3)
	ctx := context.Background()

	res, err := env.exec.ApplyMailboxCreate(ctx, env.account.ID, "Projects", true)
	if err != nil {
		t.Fatalf("ApplyMailboxCreate: %v", err)
	}
	if res.MailboxID == 0 || res.Name != "Projects" {
		t.Fatalf("result = %+v", res)
	}

	if !env.serverHasMailbox("Projects") {
		t.Error("the folder was not created on the server")
	}
	row := env.mailboxByName(t, "Projects")
	if row.ID != res.MailboxID {
		t.Errorf("the reflected row id %d does not match the reported %d", row.ID, res.MailboxID)
	}
	if !row.Subscribed {
		t.Error("a folder created with subscribe=true is not subscribed in the store")
	}

	// The fresh UIDVALIDITY the server assigned was read back through STATUS
	// and recorded, so the first incremental pass resumes rather than treating
	// the folder as never-synced.
	if res.UIDValidity == 0 {
		t.Error("no UIDVALIDITY was read back after the create")
	}
	if row.UIDValidityOrZero() != res.UIDValidity {
		t.Errorf("stored uidvalidity %d != reported %d", row.UIDValidityOrZero(), res.UIDValidity)
	}
}

func TestApplyMailboxCreateGivesEachFolderItsOwnUIDValidity(t *testing.T) {
	env := newWriteEnv(t, 1)
	ctx := context.Background()

	a, err := env.exec.ApplyMailboxCreate(ctx, env.account.ID, "A", true)
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	b, err := env.exec.ApplyMailboxCreate(ctx, env.account.ID, "B", true)
	if err != nil {
		t.Fatalf("create B: %v", err)
	}
	if a.UIDValidity == b.UIDValidity {
		t.Errorf("two folders share UIDVALIDITY %d; a UID would then name two different messages", a.UIDValidity)
	}
}

func TestApplyMailboxCreateRefusesADuplicate(t *testing.T) {
	env := newWriteEnv(t, 1)
	ctx := context.Background()

	if _, err := env.exec.ApplyMailboxCreate(ctx, env.account.ID, "Archive", true); !errors.Is(err, ErrMailboxNameTaken) {
		t.Fatalf("got %v, want ErrMailboxNameTaken", err)
	}
}

func TestApplyMailboxCreateLeavesTheStoreUntouchedWhenDovecotRefuses(t *testing.T) {
	// The W-A1 ordering, restated for folders: if IMAP fails, the store must
	// not have invented a mailbox that does not exist on the server.
	env := newWriteEnv(t, 1)
	ctx := context.Background()

	env.srv.mu.Lock()
	env.srv.createErr = errors.New("fake: CREATE refused")
	env.srv.mu.Unlock()

	if _, err := env.exec.ApplyMailboxCreate(ctx, env.account.ID, "Ghost", true); err == nil {
		t.Fatal("a refused CREATE was reported as success")
	}
	env.mailboxMissing(t, "Ghost")
}

func TestApplyMailboxCreateSurvivesAFailedStatus(t *testing.T) {
	// A folder that exists but whose STATUS could not be read is still a
	// success: the mailbox is what the caller asked for, and NULL uidvalidity
	// is the schema's own honest "never synced" state.
	env := newWriteEnv(t, 1)
	ctx := context.Background()

	env.srv.mu.Lock()
	env.srv.statusErr = errors.New("fake: STATUS refused")
	env.srv.mu.Unlock()

	res, err := env.exec.ApplyMailboxCreate(ctx, env.account.ID, "Partial", true)
	if err != nil {
		t.Fatalf("a failed STATUS aborted a successful create: %v", err)
	}
	if res.UIDValidity != 0 {
		t.Errorf("UIDValidity = %d, want 0 when STATUS failed", res.UIDValidity)
	}
	row := env.mailboxByName(t, "Partial")
	if row.UIDValidity != nil {
		t.Errorf("uidvalidity = %v, want NULL (never synced)", *row.UIDValidity)
	}
}

func TestApplyMailboxCreateConvergesWithDiscovery(t *testing.T) {
	// The echo: discovery WILL list this folder again. UpsertMailbox keys on
	// (account_id, name), so the second sighting updates the same row and the
	// id survives — which is what keeps a JMAP client's id valid.
	env := newWriteEnv(t, 1)
	ctx := context.Background()

	res, err := env.exec.ApplyMailboxCreate(ctx, env.account.ID, "Echo", true)
	if err != nil {
		t.Fatalf("ApplyMailboxCreate: %v", err)
	}
	if _, err := env.syncer.Run(ctx, env.account); err != nil {
		t.Fatalf("re-sync: %v", err)
	}

	row := env.mailboxByName(t, "Echo")
	if row.ID != res.MailboxID {
		t.Errorf("discovery minted a new id (%d) for a folder the executor created (%d)", row.ID, res.MailboxID)
	}
}

// ---------------------------------------------------------------------------
// rename
// ---------------------------------------------------------------------------

func TestApplyMailboxRenameKeepsTheIdOnBothSides(t *testing.T) {
	// THE W2 property. A rename must update the object, not replace it
	// (RFC 8621 §2.5), and the store row's id IS the JMAP Mailbox id.
	env := newWriteEnv(t, 1)
	ctx := context.Background()

	before := env.mailboxByName(t, "Archive")

	res, err := env.exec.ApplyMailboxRename(ctx, env.account.ID, before.ID, "Archivo")
	if err != nil {
		t.Fatalf("ApplyMailboxRename: %v", err)
	}
	if res.MailboxID != before.ID {
		t.Errorf("the result reports id %d, want the unchanged %d", res.MailboxID, before.ID)
	}

	after := env.mailboxByName(t, "Archivo")
	if after.ID != before.ID {
		t.Fatalf("the mailbox id changed across a rename: %d -> %d", before.ID, after.ID)
	}
	env.mailboxMissing(t, "Archive")
	if !env.serverHasMailbox("Archivo") || env.serverHasMailbox("Archive") {
		t.Error("the server-side rename did not happen")
	}
}

func TestApplyMailboxRenameCarriesChildrenOnBothSides(t *testing.T) {
	env := newWriteEnv(t, 1)
	ctx := context.Background()

	// A small tree: Work, Work/2026, Work/2026/Q1, plus a decoy sibling whose
	// name SHARES the prefix but is not a child.
	for _, name := range []string{"Work", "Work/2026", "Work/2026/Q1", "Workshop"} {
		if _, err := env.exec.ApplyMailboxCreate(ctx, env.account.ID, name, true); err != nil {
			t.Fatalf("create %q: %v", name, err)
		}
	}
	work := env.mailboxByName(t, "Work")
	child := env.mailboxByName(t, "Work/2026")
	grand := env.mailboxByName(t, "Work/2026/Q1")
	decoy := env.mailboxByName(t, "Workshop")

	res, err := env.exec.ApplyMailboxRename(ctx, env.account.ID, work.ID, "Projects")
	if err != nil {
		t.Fatalf("ApplyMailboxRename: %v", err)
	}
	if res.ChildrenRenamed != 2 {
		t.Errorf("ChildrenRenamed = %d, want 2", res.ChildrenRenamed)
	}

	// Every descendant kept its id and moved to the new path — on both sides.
	if got := env.mailboxByName(t, "Projects/2026"); got.ID != child.ID {
		t.Errorf("the child's id changed: %d -> %d", child.ID, got.ID)
	}
	if got := env.mailboxByName(t, "Projects/2026/Q1"); got.ID != grand.ID {
		t.Errorf("the grandchild's id changed: %d -> %d", grand.ID, got.ID)
	}
	if !env.serverHasMailbox("Projects/2026/Q1") {
		t.Error("the server did not carry the grandchild along")
	}

	// The prefix-sharing sibling must NOT have moved. "Work" + delimiter is
	// the prefix, never "Work" alone.
	if got := env.mailboxByName(t, "Workshop"); got.ID != decoy.ID {
		t.Errorf("Workshop was caught by a rename of Work: %+v", got)
	}
	if !env.serverHasMailbox("Workshop") {
		t.Error("Workshop disappeared from the server")
	}
}

func TestApplyMailboxRenameRefusesInbox(t *testing.T) {
	// RFC 3501 §6.3.5 makes RENAME INBOX a bulk move that empties the inbox.
	env := newWriteEnv(t, 3)
	ctx := context.Background()

	inbox := env.mailboxByName(t, "INBOX")
	if _, err := env.exec.ApplyMailboxRename(ctx, env.account.ID, inbox.ID, "OldMail"); !errors.Is(err, ErrMailboxProtected) {
		t.Fatalf("got %v, want ErrMailboxProtected", err)
	}
	// And nothing happened on either side.
	if !env.serverHasMailbox("INBOX") || env.serverHasMailbox("OldMail") {
		t.Error("the refused rename still touched the server")
	}
	env.mailboxByName(t, "INBOX")
}

func TestApplyMailboxRenameRefusesATakenName(t *testing.T) {
	env := newWriteEnv(t, 1)
	ctx := context.Background()

	archive := env.mailboxByName(t, "Archive")
	if _, err := env.exec.ApplyMailboxRename(ctx, env.account.ID, archive.ID, "Trash"); !errors.Is(err, ErrMailboxNameTaken) {
		t.Fatalf("got %v, want ErrMailboxNameTaken", err)
	}
	if !env.serverHasMailbox("Archive") {
		t.Error("the refused rename still touched the server")
	}
}

func TestApplyMailboxRenameToTheSameNameIsANoop(t *testing.T) {
	env := newWriteEnv(t, 1)
	ctx := context.Background()

	archive := env.mailboxByName(t, "Archive")
	res, err := env.exec.ApplyMailboxRename(ctx, env.account.ID, archive.ID, "Archive")
	if err != nil {
		t.Fatalf("a self-rename must be a no-op, got %v", err)
	}
	if res.MailboxID != archive.ID {
		t.Errorf("result = %+v", res)
	}
}

func TestApplyMailboxRenameLeavesTheStoreUntouchedWhenDovecotRefuses(t *testing.T) {
	env := newWriteEnv(t, 1)
	ctx := context.Background()

	env.srv.mu.Lock()
	env.srv.renameErr = errors.New("fake: RENAME refused")
	env.srv.mu.Unlock()

	archive := env.mailboxByName(t, "Archive")
	if _, err := env.exec.ApplyMailboxRename(ctx, env.account.ID, archive.ID, "Archivo"); err == nil {
		t.Fatal("a refused RENAME was reported as success")
	}
	env.mailboxByName(t, "Archive")
	env.mailboxMissing(t, "Archivo")
}

func TestApplyMailboxRenameKeepsMessagesAttachedToTheSameRow(t *testing.T) {
	// A rename must not disturb the messages: message_state points at the
	// mailbox ID, and the ID does not change, so nothing about the messages
	// needs to move.
	env := newWriteEnv(t, 3)
	ctx := context.Background()

	inbox := env.mailboxByName(t, "INBOX")
	total, _, err := env.store.CountMailboxMessages(ctx, inbox.ID)
	if err != nil {
		t.Fatalf("CountMailboxMessages: %v", err)
	}

	// Rename Archive (not INBOX, which is protected) after moving a message
	// into it, so there is something to preserve.
	st := env.stateByUID(t, "INBOX", 1)
	archive := env.mailboxByName(t, "Archive")
	if _, err := env.exec.ApplyMove(ctx, env.account.ID, st.MessageID, archive.ID); err != nil {
		t.Fatalf("ApplyMove: %v", err)
	}
	if _, err := env.exec.ApplyMailboxRename(ctx, env.account.ID, archive.ID, "Archivo"); err != nil {
		t.Fatalf("ApplyMailboxRename: %v", err)
	}

	after := env.state(t, st.MessageID)
	if after.MailboxID != archive.ID {
		t.Errorf("the message's mailbox_id changed across a rename: %d -> %d", archive.ID, after.MailboxID)
	}
	if after.DeletedAt != nil {
		t.Error("a rename tombstoned a message")
	}
	// The inbox is down one, which is the move's doing, not the rename's.
	newTotal, _, err := env.store.CountMailboxMessages(ctx, inbox.ID)
	if err != nil {
		t.Fatalf("CountMailboxMessages: %v", err)
	}
	if newTotal != total-1 {
		t.Errorf("inbox total = %d, want %d", newTotal, total-1)
	}
}

func TestApplyMailboxRenameConvergesWithDiscovery(t *testing.T) {
	// The echo that would break without the in-place UPDATE: discovery lists
	// the folder under its NEW name, upserts by (account_id, name), and lands
	// on the same row. The old name is nowhere — no phantom row, no second id.
	env := newWriteEnv(t, 1)
	ctx := context.Background()

	archive := env.mailboxByName(t, "Archive")
	if _, err := env.exec.ApplyMailboxRename(ctx, env.account.ID, archive.ID, "Archivo"); err != nil {
		t.Fatalf("ApplyMailboxRename: %v", err)
	}
	if _, err := env.syncer.Run(ctx, env.account); err != nil {
		t.Fatalf("re-sync: %v", err)
	}

	after := env.mailboxByName(t, "Archivo")
	if after.ID != archive.ID {
		t.Errorf("discovery minted a new id (%d) after a rename of %d", after.ID, archive.ID)
	}
	env.mailboxMissing(t, "Archive")

	// And exactly one row names this folder — a delete-then-create reflection
	// would have left two.
	rows, err := env.store.ListMailboxes(ctx, env.account.ID)
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	seen := 0
	for _, r := range rows {
		if r.Name == "Archivo" || r.Name == "Archive" {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("%d rows name the renamed folder, want 1: %+v", seen, rows)
	}
}

// ---------------------------------------------------------------------------
// destroy
// ---------------------------------------------------------------------------

func TestApplyMailboxDestroyRemovesFromBothSides(t *testing.T) {
	env := newWriteEnv(t, 1)
	ctx := context.Background()

	if _, err := env.exec.ApplyMailboxCreate(ctx, env.account.ID, "Temp", true); err != nil {
		t.Fatalf("create: %v", err)
	}
	temp := env.mailboxByName(t, "Temp")

	if _, err := env.exec.ApplyMailboxDestroy(ctx, env.account.ID, temp.ID); err != nil {
		t.Fatalf("ApplyMailboxDestroy: %v", err)
	}
	if env.serverHasMailbox("Temp") {
		t.Error("the folder survived on the server")
	}
	env.mailboxMissing(t, "Temp")
}

func TestApplyMailboxDestroyTombstonesTheMessagesBeforeDroppingTheRow(t *testing.T) {
	// message_state cascades on the mailbox delete, so the tombstones are the
	// LAST chance Email/changes has to report those messages as destroyed.
	// Writing them first is what makes that reportable.
	env := newWriteEnv(t, 1)
	ctx := context.Background()

	trash := env.mailboxByName(t, "Trash")
	before, _, err := env.store.CountMailboxMessages(ctx, trash.ID)
	if err != nil {
		t.Fatalf("CountMailboxMessages: %v", err)
	}
	if before == 0 {
		t.Fatal("the fixture's Trash is empty; the test needs a message in it")
	}

	// Trash has a role but the EXECUTOR does not protect roles — that is the
	// JMAP layer's decision (mailbox_set.go). Here we prove the mechanics.
	res, err := env.exec.ApplyMailboxDestroy(ctx, env.account.ID, trash.ID)
	if err != nil {
		t.Fatalf("ApplyMailboxDestroy: %v", err)
	}
	if int64(res.MessagesTombstoned) != before {
		t.Errorf("tombstoned %d, want %d", res.MessagesTombstoned, before)
	}
	env.mailboxMissing(t, "Trash")
}

func TestApplyMailboxDestroyRefusesAParentWithChildren(t *testing.T) {
	env := newWriteEnv(t, 1)
	ctx := context.Background()

	for _, name := range []string{"Parent", "Parent/Child"} {
		if _, err := env.exec.ApplyMailboxCreate(ctx, env.account.ID, name, true); err != nil {
			t.Fatalf("create %q: %v", name, err)
		}
	}
	parent := env.mailboxByName(t, "Parent")

	if _, err := env.exec.ApplyMailboxDestroy(ctx, env.account.ID, parent.ID); !errors.Is(err, ErrMailboxHasChildren) {
		t.Fatalf("got %v, want ErrMailboxHasChildren", err)
	}
	if !env.serverHasMailbox("Parent") || !env.serverHasMailbox("Parent/Child") {
		t.Error("a refused destroy still touched the server")
	}
}

func TestApplyMailboxDestroyRefusesInbox(t *testing.T) {
	env := newWriteEnv(t, 2)
	ctx := context.Background()

	inbox := env.mailboxByName(t, "INBOX")
	if _, err := env.exec.ApplyMailboxDestroy(ctx, env.account.ID, inbox.ID); !errors.Is(err, ErrMailboxProtected) {
		t.Fatalf("got %v, want ErrMailboxProtected", err)
	}
	if !env.serverHasMailbox("INBOX") {
		t.Fatal("INBOX was deleted")
	}
}

func TestApplyMailboxDestroyLeavesTheStoreUntouchedWhenDovecotRefuses(t *testing.T) {
	env := newWriteEnv(t, 1)
	ctx := context.Background()

	env.srv.mu.Lock()
	env.srv.deleteErr = errors.New("fake: DELETE refused")
	env.srv.mu.Unlock()

	archive := env.mailboxByName(t, "Archive")
	if _, err := env.exec.ApplyMailboxDestroy(ctx, env.account.ID, archive.ID); err == nil {
		t.Fatal("a refused DELETE was reported as success")
	}
	env.mailboxByName(t, "Archive") // still there
}

func TestApplyMailboxDestroyOfAnAlreadyGoneFolderStillReflects(t *testing.T) {
	// Another client deleted it first, or this is a replay. The store has to
	// catch up either way, so an absent server folder is success.
	env := newWriteEnv(t, 1)
	ctx := context.Background()

	archive := env.mailboxByName(t, "Archive")
	env.srv.mu.Lock()
	for i, mb := range env.srv.mailboxes {
		if mb.name == "Archive" {
			env.srv.mailboxes = append(env.srv.mailboxes[:i], env.srv.mailboxes[i+1:]...)
			break
		}
	}
	env.srv.mu.Unlock()

	if _, err := env.exec.ApplyMailboxDestroy(ctx, env.account.ID, archive.ID); err != nil {
		t.Fatalf("destroying an already-absent folder failed: %v", err)
	}
	env.mailboxMissing(t, "Archive")
}

// ---------------------------------------------------------------------------
// authorization
// ---------------------------------------------------------------------------

func TestFolderWritesRefuseAnotherAccountsMailbox(t *testing.T) {
	env := newWriteEnv(t, 1)
	ctx := context.Background()

	archive := env.mailboxByName(t, "Archive")
	const foreign int64 = 999999

	if _, err := env.exec.ApplyMailboxRename(ctx, foreign, archive.ID, "Nope"); !errors.Is(err, ErrMailboxNotFound) {
		t.Errorf("rename: got %v, want ErrMailboxNotFound", err)
	}
	if _, err := env.exec.ApplyMailboxDestroy(ctx, foreign, archive.ID); !errors.Is(err, ErrMailboxNotFound) {
		t.Errorf("destroy: got %v, want ErrMailboxNotFound", err)
	}
	if _, err := env.exec.KeywordBudgetFor(ctx, foreign, archive.ID); !errors.Is(err, ErrMailboxNotFound) {
		t.Errorf("budget: got %v, want ErrMailboxNotFound", err)
	}
	// And nothing moved.
	env.mailboxByName(t, "Archive")
}

func TestFolderWritesRefuseAnUnknownMailbox(t *testing.T) {
	env := newWriteEnv(t, 1)
	ctx := context.Background()

	if _, err := env.exec.ApplyMailboxRename(ctx, env.account.ID, 424242, "X"); !errors.Is(err, ErrMailboxNotFound) {
		t.Errorf("rename: got %v, want ErrMailboxNotFound", err)
	}
	if _, err := env.exec.ApplyMailboxDestroy(ctx, env.account.ID, 424242); !errors.Is(err, ErrMailboxNotFound) {
		t.Errorf("destroy: got %v, want ErrMailboxNotFound", err)
	}
}

func TestApplyMailboxCreateRefusesAnEmptyName(t *testing.T) {
	env := newWriteEnv(t, 1)
	ctx := context.Background()

	for _, name := range []string{"", "   "} {
		if _, err := env.exec.ApplyMailboxCreate(ctx, env.account.ID, name, true); !errors.Is(err, ErrMailboxNameInvalid) {
			t.Errorf("%q: got %v, want ErrMailboxNameInvalid", name, err)
		}
	}
}

// ---------------------------------------------------------------------------
// the keyword budget
// ---------------------------------------------------------------------------

func TestKeywordBudgetCountsDistinctLiveKeywords(t *testing.T) {
	env := newWriteEnv(t, 3)
	ctx := context.Background()

	inbox := env.mailboxByName(t, "INBOX")
	budget, err := env.exec.KeywordBudgetFor(ctx, env.account.ID, inbox.ID)
	if err != nil {
		t.Fatalf("KeywordBudgetFor: %v", err)
	}
	if budget.Limit != imap.MaxDurableKeywordsPerMailbox {
		t.Errorf("Limit = %d, want %d", budget.Limit, imap.MaxDurableKeywordsPerMailbox)
	}
	if len(budget.InUse) != 0 {
		t.Fatalf("a freshly seeded inbox reports keywords in use: %v", budget.InUse)
	}

	// Two messages, one keyword each, plus a repeat and a case variant. The
	// budget must count THREE distinct case-folded names, not five.
	a := env.stateByUID(t, "INBOX", 1)
	b := env.stateByUID(t, "INBOX", 2)
	c := env.stateByUID(t, "INBOX", 3)
	for _, w := range []struct {
		id    int64
		flags []string
	}{
		{a.MessageID, []string{"Work", "Urgent"}},
		{b.MessageID, []string{"work"}}, // same slot as "Work"
		{c.MessageID, []string{"Personal"}},
	} {
		if _, err := env.exec.ApplyFlagChange(ctx, env.account.ID, w.id, FlagChange{Add: w.flags}); err != nil {
			t.Fatalf("ApplyFlagChange: %v", err)
		}
	}

	budget, err = env.exec.KeywordBudgetFor(ctx, env.account.ID, inbox.ID)
	if err != nil {
		t.Fatalf("KeywordBudgetFor: %v", err)
	}
	if len(budget.InUse) != 3 {
		t.Fatalf("InUse = %v, want three distinct case-folded names", budget.InUse)
	}
	if budget.Remaining() != imap.MaxDurableKeywordsPerMailbox-3 {
		t.Errorf("Remaining = %d, want %d", budget.Remaining(), imap.MaxDurableKeywordsPerMailbox-3)
	}
	if !budget.Has("WORK") || !budget.Has("work") {
		t.Error("Has must fold case, since dovecot-keywords allocates one slot per folded name")
	}
	if budget.Has("Nothing") {
		t.Error("Has invented a keyword")
	}
}

func TestKeywordBudgetExcludesTombstonedMessages(t *testing.T) {
	// A keyword on an expunged message is not in the live folder. The
	// under-count is documented on the store method; what matters here is that
	// it is deliberate and consistent.
	env := newWriteEnv(t, 2)
	ctx := context.Background()

	inbox := env.mailboxByName(t, "INBOX")
	st := env.stateByUID(t, "INBOX", 1)
	if _, err := env.exec.ApplyFlagChange(ctx, env.account.ID, st.MessageID, FlagChange{Add: []string{"Doomed"}}); err != nil {
		t.Fatalf("ApplyFlagChange: %v", err)
	}
	budget, err := env.exec.KeywordBudgetFor(ctx, env.account.ID, inbox.ID)
	if err != nil {
		t.Fatalf("KeywordBudgetFor: %v", err)
	}
	if !budget.Has("Doomed") {
		t.Fatalf("the keyword was not counted while live: %v", budget.InUse)
	}

	// Destroy moves it to Trash (W-A2), so the INBOX row is re-pointed rather
	// than tombstoned — the keyword leaves the inbox's budget either way.
	if _, err := env.exec.ApplyDestroy(ctx, env.account.ID, st.MessageID); err != nil {
		t.Fatalf("ApplyDestroy: %v", err)
	}
	budget, err = env.exec.KeywordBudgetFor(ctx, env.account.ID, inbox.ID)
	if err != nil {
		t.Fatalf("KeywordBudgetFor: %v", err)
	}
	if budget.Has("Doomed") {
		t.Errorf("a keyword that left the folder still occupies its budget: %v", budget.InUse)
	}
}

func TestKeywordBudgetIsPerMailbox(t *testing.T) {
	// dovecot-keywords is a PER-FOLDER registry, so a keyword in the inbox
	// costs the archive nothing.
	env := newWriteEnv(t, 2)
	ctx := context.Background()

	st := env.stateByUID(t, "INBOX", 1)
	if _, err := env.exec.ApplyFlagChange(ctx, env.account.ID, st.MessageID, FlagChange{Add: []string{"InboxOnly"}}); err != nil {
		t.Fatalf("ApplyFlagChange: %v", err)
	}

	archive := env.mailboxByName(t, "Archive")
	budget, err := env.exec.KeywordBudgetFor(ctx, env.account.ID, archive.ID)
	if err != nil {
		t.Fatalf("KeywordBudgetFor: %v", err)
	}
	if budget.Has("InboxOnly") {
		t.Errorf("the archive's budget counted the inbox's keyword: %v", budget.InUse)
	}
	if budget.Remaining() != imap.MaxDurableKeywordsPerMailbox {
		t.Errorf("Remaining = %d, want the full ceiling", budget.Remaining())
	}
}
