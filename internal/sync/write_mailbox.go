package sync

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
)

// The folder half of the write executor (W2): create, rename and destroy a
// mailbox, under the same W-A1 ordering the message writes follow — Dovecot
// first, store second, answer last.
//
// # Why a mailbox write needs its own reasoning, and does not just reuse the
// message path's
//
// A flag change touches one row that the incremental pass can re-derive from
// the server at any time. A folder change touches the SHAPE of the account: the
// mailbox row's id is the JMAP Mailbox id, every message's message_state points
// at it, and the JMAP parent/child hierarchy is derived from its name. Getting
// the reflection wrong here does not lose a flag; it makes a client believe a
// folder was destroyed and a different one created in its place.
//
// # Echo safety: what the watcher does with our own folder writes
//
// The watcher's NOTIFY covers the personal namespace, and the reconciler sweeps
// the tree with LIST-STATUS. Both will observe every change made here. The
// executor is designed so each observation is a no-op by content:
//
//   - CREATE. The reconciler sees a server mailbox that is "not stored
//     locally" and triggers a discovery sweep. discover() upserts by
//     (account_id, name) — which is precisely the row this executor already
//     inserted under that exact name — so the upsert updates it in place and
//     the id survives. The freshly created folder is empty, so the backfill
//     the sweep runs finds nothing to download.
//
//   - RENAME. This is the one that would break without care. The store row is
//     renamed IN PLACE (store.RenameMailbox), keeping its id, and the children
//     are re-pathed with it. The next discovery lists the mailbox under its NEW
//     name, and UpsertMailbox's ON CONFLICT (account_id, name) therefore
//     matches THAT SAME ROW and updates it — no insert, no new id. The old name
//     is simply absent from the server's LIST, and the reconciler's
//     "a stored mailbox is gone from the server" arm cannot fire for it because
//     no row carries the old name any more: the rename already moved it.
//     A rename reflected as delete-then-create, or left for discovery to pick
//     up, would have produced exactly that stale row — a phantom mailbox whose
//     JMAP id points at a folder Dovecot does not have, plus a second id for
//     the same folder under its new name. Renaming the row in place is what
//     makes the JMAP id survive, which is a W2 acceptance criterion.
//
//   - DESTROY. The messages are tombstoned and the mailbox row deleted, in that
//     order. message_state cascades on the mailbox delete (migration 0002), so
//     the tombstones are the last chance Email/changes has to report those
//     messages as destroyed — which is why they are written BEFORE the row goes
//     away rather than relying on the cascade. The reconciler then sees neither
//     a server mailbox missing locally nor a stored mailbox missing on the
//     server: both sides already agree.
//
// The mailbox sync cursor is not advanced from here, for the same reason the
// message path does not advance it: the incremental pass owns it.

// Errors the folder writes return, as sentinels for the JMAP layer to map onto
// RFC 8620 §5.3 SetErrors without matching strings.
var (
	// ErrMailboxNotFound means no such mailbox for this account. Like
	// ErrWriteNotFound, "does not exist" and "belongs to someone else" are
	// deliberately one error.
	ErrMailboxNotFound = errors.New("sync: no such mailbox for this account")

	// ErrMailboxNameTaken means the target name is already in use on the
	// server (or, for a create, in the local tree).
	ErrMailboxNameTaken = errors.New("sync: a mailbox with that name already exists")

	// ErrMailboxNameInvalid means the requested name cannot be an IMAP mailbox
	// name at all.
	ErrMailboxNameInvalid = errors.New("sync: invalid mailbox name")

	// ErrMailboxProtected means the operation targets a mailbox whose IMAP
	// semantics forbid it — INBOX, which can be neither renamed (RFC 3501
	// §6.3.5 makes that a bulk move) nor deleted (§6.3.4 calls it an error).
	ErrMailboxProtected = errors.New("sync: this mailbox cannot be renamed or deleted")

	// ErrMailboxHasChildren means a destroy was refused because the mailbox
	// has descendants, which would be destroyed with it.
	ErrMailboxHasChildren = errors.New("sync: the mailbox has child mailboxes")

	// ErrKeywordCeiling means a keyword write would push a mailbox past the 26
	// distinct keywords a Maildir folder holds durably (A6 / validation V1).
	ErrKeywordCeiling = errors.New("sync: the mailbox is at its durable keyword ceiling")
)

// MailboxWriteResult is the state of a mailbox after a successful folder write,
// as the SERVER reported it.
type MailboxWriteResult struct {
	// MailboxID is the store row's id. It is STABLE across a rename: that is
	// the property the JMAP layer's Mailbox id depends on.
	MailboxID int64

	// Name is the full IMAP name after the operation.
	Name string

	// UIDValidity is what the server assigned, read back after a create.
	UIDValidity int64

	// ChildrenRenamed counts the descendants a rename carried along.
	ChildrenRenamed int

	// MessagesTombstoned counts the messages a destroy marked deleted.
	MessagesTombstoned int
}

// ApplyMailboxCreate creates a mailbox on Dovecot and reflects it in the store.
//
// name is the FULL IMAP path ("INBOX/Work/2026"), already composed by the
// caller from the JMAP name and parentId — this layer does not know JMAP's
// leaf-plus-parent model.
func (w *WriteExecutor) ApplyMailboxCreate(ctx context.Context, accountID int64, name string, subscribe bool) (MailboxWriteResult, error) {
	var out MailboxWriteResult

	name = strings.TrimRight(name, " ")
	if name == "" {
		return out, fmt.Errorf("%w: the name is empty", ErrMailboxNameInvalid)
	}

	account, err := w.store.GetAccount(ctx, accountID)
	if err != nil {
		return out, fmt.Errorf("loading account %d: %w", accountID, err)
	}

	// A local pre-check, purely so the common collision answers fast and
	// legibly. It is NOT the authority — the server is, and the CREATE below
	// still maps [ALREADYEXISTS] — because another client may have created the
	// folder since our last LIST.
	if existing, err := w.store.GetMailboxByName(ctx, accountID, name); err == nil {
		_ = existing
		return out, fmt.Errorf("%w: %q", ErrMailboxNameTaken, name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return out, fmt.Errorf("checking for an existing mailbox %q: %w", name, err)
	}

	var info imap.MailboxInfo
	err = w.withConn(ctx, account, func(c imap.Client) error {
		if err := c.CreateMailbox(ctx, name); err != nil {
			return mapFolderErr(err)
		}
		if subscribe {
			// A folder nobody is subscribed to is invisible in most clients.
			// The failure is NOT fatal: the mailbox exists, which is what the
			// caller asked for, and a missing subscription is a cosmetic
			// divergence the next LIST reports honestly.
			if serr := c.SetSubscribed(ctx, name, true); serr != nil {
				w.log.Warn("the mailbox was created but SUBSCRIBE failed",
					"account_id", accountID, "mailbox", name, "error", serr)
			}
		}

		// The UIDVALIDITY the server just assigned. STATUS rather than SELECT:
		// selecting would disturb the connection's selected mailbox, which the
		// next message write on this same cached connection would then have to
		// re-establish anyway.
		st, serr := c.StatusMailbox(ctx, name)
		if serr != nil {
			// Not fatal either: the mailbox is created, and the first
			// incremental pass records the real UIDVALIDITY from its own
			// SELECT. Leaving the column NULL is the honest "never synced"
			// state the schema already defines, and it is what the mailbox row
			// would have had if discovery had found it first.
			w.log.Warn("the mailbox was created but its STATUS could not be read",
				"account_id", accountID, "mailbox", name, "error", serr)
			return nil
		}
		info = st
		return nil
	})
	if err != nil {
		return out, err
	}

	// Reflection. UpsertMailbox is keyed on (account_id, name), so if the
	// watcher's discovery raced us to this exact folder the upsert lands on the
	// row it already made — the id is whichever of the two won, and both name
	// the same Dovecot folder, which is the whole point of keying on the name.
	row, err := w.store.UpsertMailbox(ctx, store.Mailbox{
		AccountID:  accountID,
		Name:       name,
		Delimiter:  w.delimiterFor(ctx, accountID),
		Role:       store.RoleNone,
		Subscribed: subscribe,
		Selectable: true,
	})
	if err != nil {
		return out, fmt.Errorf("reflecting the new mailbox %q in the store: %w", name, err)
	}
	out.MailboxID, out.Name = row.ID, row.Name

	if info.HasStatus && info.UIDValidity != 0 {
		// A brand-new mailbox is empty, so recording the resume point here
		// costs nothing and saves the first incremental pass from treating it
		// as never-synced. The same three conversion helpers the incremental
		// pass uses, for the same reasons (convert.go).
		out.UIDValidity = uidValidityToDB(info.UIDValidity)
		if err := w.store.SetMailboxSyncState(ctx, row.ID,
			uidValidityToDB(info.UIDValidity), uidNextToDB(info.UIDNext),
			modSeqToDB(info.HighestModSeq)); err != nil {
			return out, fmt.Errorf("recording the sync state of %q: %w", name, err)
		}
	}
	// W4a: a new folder changes Mailbox/get's state for the account.
	w.broker.Notify(accountID)
	return out, nil
}

// ApplyMailboxRename renames a mailbox on Dovecot and reflects it in the store,
// keeping the row — and therefore the JMAP Mailbox id — alive.
func (w *WriteExecutor) ApplyMailboxRename(ctx context.Context, accountID, mailboxID int64, newName string) (MailboxWriteResult, error) {
	var out MailboxWriteResult

	newName = strings.TrimRight(newName, " ")
	if newName == "" {
		return out, fmt.Errorf("%w: the name is empty", ErrMailboxNameInvalid)
	}

	mb, err := w.ownedMailbox(ctx, accountID, mailboxID)
	if err != nil {
		return out, err
	}
	out.MailboxID, out.Name = mb.ID, mb.Name

	if mb.Name == newName {
		// Idempotent replay: nothing to do on either side.
		return out, nil
	}
	if equalFoldASCII(mb.Name, "INBOX") {
		return out, fmt.Errorf("%w: INBOX (RFC 3501 §6.3.5)", ErrMailboxProtected)
	}

	if _, err := w.store.GetMailboxByName(ctx, accountID, newName); err == nil {
		return out, fmt.Errorf("%w: %q", ErrMailboxNameTaken, newName)
	} else if !errors.Is(err, store.ErrNotFound) {
		return out, fmt.Errorf("checking for an existing mailbox %q: %w", newName, err)
	}

	account, err := w.store.GetAccount(ctx, accountID)
	if err != nil {
		return out, fmt.Errorf("loading account %d: %w", accountID, err)
	}

	if err := w.withConn(ctx, account, func(c imap.Client) error {
		return mapFolderErr(c.RenameMailbox(ctx, mb.Name, newName))
	}); err != nil {
		return out, err
	}

	// Dovecot has renamed the folder and its children. Reflecting it as an
	// in-place UPDATE is what keeps the JMAP id stable (store.RenameMailbox
	// documents the full argument).
	renamed, err := w.store.RenameMailbox(ctx, accountID, mailboxID, newName)
	if err != nil {
		// The server-side rename ALREADY happened. This is the one window in
		// the folder path where the two sides can disagree, and the honest
		// answer is a loud error plus the knowledge that the reconciler
		// repairs it: its next sweep sees a server mailbox missing locally,
		// runs discovery, and picks the folder up under its new name. The cost
		// of that repair is a new JMAP id for the folder — bad, but strictly
		// better than a store that claims a folder exists under a name Dovecot
		// no longer has.
		w.log.Error("the mailbox was renamed on the server but the store could not be updated; "+
			"the reconciler will repair the divergence",
			"account_id", accountID, "mailbox_id", mailboxID, "from", mb.Name, "to", newName, "error", err)
		return out, fmt.Errorf("reflecting the rename of %q in the store: %w", mb.Name, err)
	}

	out.Name = newName
	// The row itself is one of the rows RenameMailbox counted.
	if renamed > 0 {
		out.ChildrenRenamed = renamed - 1
	}
	w.broker.Notify(accountID)
	return out, nil
}

// ApplyMailboxDestroy deletes a mailbox on Dovecot and reflects it in the
// store.
//
// It does NOT move anything: whatever is inside is destroyed with the folder,
// which is what RFC 3501 §6.3.4's DELETE means. The decision about whether that
// is acceptable — and the Trash detour Moov performs first when it is not —
// belongs to the JMAP layer, which is the one that knows the client said
// onDestroyRemoveEmails.
func (w *WriteExecutor) ApplyMailboxDestroy(ctx context.Context, accountID, mailboxID int64) (MailboxWriteResult, error) {
	var out MailboxWriteResult

	mb, err := w.ownedMailbox(ctx, accountID, mailboxID)
	if err != nil {
		return out, err
	}
	out.MailboxID, out.Name = mb.ID, mb.Name

	if equalFoldASCII(mb.Name, "INBOX") {
		return out, fmt.Errorf("%w: INBOX (RFC 3501 §6.3.4)", ErrMailboxProtected)
	}

	children, err := w.store.ListChildMailboxes(ctx, accountID, mailboxID)
	if err != nil {
		return out, fmt.Errorf("checking the children of mailbox %d: %w", mailboxID, err)
	}
	if len(children) > 0 {
		// Refused rather than cascaded. RFC 3501 §6.3.4 lets a server delete a
		// parent and keep its children, or refuse; either way, silently
		// destroying a subtree the client named ONE mailbox of is not something
		// this executor will do to a real mailbox.
		return out, fmt.Errorf("%w: %d under %q", ErrMailboxHasChildren, len(children), mb.Name)
	}

	account, err := w.store.GetAccount(ctx, accountID)
	if err != nil {
		return out, fmt.Errorf("loading account %d: %w", accountID, err)
	}

	if err := w.withConn(ctx, account, func(c imap.Client) error {
		err := c.DeleteMailbox(ctx, mb.Name)
		if errors.Is(err, imap.ErrMailboxMissing) {
			// Already gone on the server — another client deleted it, or this
			// is a replay. The store still has to catch up, so this is success,
			// not failure.
			w.log.Info("the mailbox was already absent from the server; reflecting the delete anyway",
				"account_id", accountID, "mailbox", mb.Name)
			return nil
		}
		return mapFolderErr(err)
	}); err != nil {
		return out, err
	}

	// Tombstone BEFORE dropping the row: message_state cascades on the mailbox
	// delete, so this is the last moment Email/changes can learn those messages
	// are destroyed.
	tombstoned, err := w.store.TombstoneMailbox(ctx, mailboxID)
	if err != nil {
		return out, fmt.Errorf("tombstoning the messages of %q: %w", mb.Name, err)
	}
	out.MessagesTombstoned = tombstoned

	if err := w.store.DeleteMailbox(ctx, mailboxID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return out, fmt.Errorf("removing the mailbox row for %q: %w", mb.Name, err)
	}
	// A destroy changes both the mailbox list and, when it tombstoned
	// messages, the Email state — one notification covers both, since the
	// payload is a snapshot of every type's state.
	w.broker.Notify(accountID)
	return out, nil
}

// ---------------------------------------------------------------------------
// the keyword ceiling (A6 / validation V1)
// ---------------------------------------------------------------------------

// KeywordBudget reports how many distinct keywords a mailbox already carries
// against the durable Maildir ceiling.
//
// It is the number the JMAP layer needs to refuse a 27th keyword BEFORE the
// write reaches Dovecot — which is the only place the refusal can happen,
// because Dovecot accepts the write and loses it silently later
// (imap.MaxDurableKeywordsPerMailbox).
type KeywordBudget struct {
	// InUse are the distinct keywords currently on live messages of the
	// mailbox, lowercased.
	InUse []string

	// Limit is imap.MaxDurableKeywordsPerMailbox, restated so a caller
	// rendering an error message does not have to import internal/imap.
	Limit int
}

// Remaining is how many NEW distinct keywords still fit.
func (b KeywordBudget) Remaining() int {
	if n := b.Limit - len(b.InUse); n > 0 {
		return n
	}
	return 0
}

// Has reports whether a keyword is already among those in use, matched
// case-insensitively as IMAP matches flags (RFC 3501 §2.3.2) — and, more to
// the point, as dovecot-keywords allocates slots: one slot per name,
// case-folded.
func (b KeywordBudget) Has(keyword string) bool {
	want := strings.ToLower(strings.TrimSpace(keyword))
	for _, k := range b.InUse {
		if k == want {
			return true
		}
	}
	return false
}

// KeywordBudgetFor reads a mailbox's keyword budget.
func (w *WriteExecutor) KeywordBudgetFor(ctx context.Context, accountID, mailboxID int64) (KeywordBudget, error) {
	budget := KeywordBudget{Limit: imap.MaxDurableKeywordsPerMailbox}

	if _, err := w.ownedMailbox(ctx, accountID, mailboxID); err != nil {
		return budget, err
	}
	inUse, err := w.store.DistinctKeywordsInMailbox(ctx, mailboxID)
	if err != nil {
		return budget, fmt.Errorf("reading the keyword budget of mailbox %d: %w", mailboxID, err)
	}
	budget.InUse = inUse
	return budget, nil
}

// ---------------------------------------------------------------------------
// internals
// ---------------------------------------------------------------------------

// ownedMailbox resolves a mailbox id, enforcing that the account owns it. THE
// authorization check of the folder write path, mirroring target()'s role on
// the message path.
func (w *WriteExecutor) ownedMailbox(ctx context.Context, accountID, mailboxID int64) (store.Mailbox, error) {
	mb, err := w.store.GetMailbox(ctx, mailboxID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Mailbox{}, ErrMailboxNotFound
		}
		return store.Mailbox{}, fmt.Errorf("loading mailbox %d: %w", mailboxID, err)
	}
	if mb.AccountID != accountID {
		return store.Mailbox{}, ErrMailboxNotFound
	}
	return mb, nil
}

// delimiterFor returns the hierarchy delimiter this account's server uses,
// taken from a mailbox it already has.
//
// The account always has at least INBOX by the time a client can create a
// folder, so this is a lookup rather than a guess. "/" is the fallback for the
// impossible case of an account with no mailboxes at all, and it is what the
// store defaults to anyway.
func (w *WriteExecutor) delimiterFor(ctx context.Context, accountID int64) string {
	if inbox, err := w.store.GetMailboxByRole(ctx, accountID, store.RoleInbox); err == nil && inbox.Delimiter != "" {
		return inbox.Delimiter
	}
	boxes, err := w.store.ListMailboxes(ctx, accountID)
	if err == nil {
		for _, m := range boxes {
			if m.Delimiter != "" {
				return m.Delimiter
			}
		}
	}
	return "/"
}

// withConn runs fn on the account's cached write connection WITHOUT selecting
// a mailbox — folder commands operate on the connection, not on a selection.
//
// It keeps withMailbox's self-healing shape: a connection that died while idle
// fails, is discarded, and one fresh dial is attempted. Unlike withMailbox
// there is no SELECT to probe liveness with, so the probe IS fn's own failure —
// which means fn CAN run twice. That is safe for exactly these commands and no
// others: CREATE of an existing name, RENAME to a name already taken, and
// DELETE of a missing mailbox are each mapped to a definite answer rather than
// retried blindly, and a folder command that reached the server does not
// change a second message when repeated. A message write never gets this
// treatment (see the package comment on why an unknown MOVE is never retried).
func (w *WriteExecutor) withConn(ctx context.Context, account store.Account, fn func(imap.Client) error) error {
	ac, err := w.forAccount(account.ID)
	if err != nil {
		return err
	}
	ac.mu.Lock()
	defer ac.mu.Unlock()

	var lastErr error
	for attempt := range 2 {
		if err := ctx.Err(); err != nil {
			return err
		}
		c, err := ac.ensure(ctx, w.connector, account)
		if err != nil {
			lastErr = err
			if attempt == 0 {
				continue
			}
			return fmt.Errorf("connecting for a folder write: %w", err)
		}
		err = fn(c)
		if err != nil && isConnectionDead(err) && attempt == 0 {
			ac.discard()
			lastErr = err
			continue
		}
		return err
	}
	return fmt.Errorf("running a folder write: %w", lastErr)
}

// isConnectionDead reports the errors that mean "this socket is no longer
// usable", as opposed to "the server refused the command".
//
// The distinction is what makes withConn's single retry safe: a refusal is
// returned to the caller untouched, and only a dead connection is retried.
func isConnectionDead(err error) bool {
	return errors.Is(err, imap.ErrNotConnected) || errors.Is(err, imap.ErrMailboxStale)
}

// mapFolderErr translates the imap package's folder sentinels into this
// package's, so the JMAP layer branches on one vocabulary.
func mapFolderErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, imap.ErrMailboxExists):
		return fmt.Errorf("%w: %w", ErrMailboxNameTaken, err)
	case errors.Is(err, imap.ErrMailboxMissing):
		return fmt.Errorf("%w: %w", ErrMailboxNotFound, err)
	case errors.Is(err, imap.ErrInvalidMailboxName):
		return fmt.Errorf("%w: %w", ErrMailboxNameInvalid, err)
	case errors.Is(err, imap.ErrRenameInbox), errors.Is(err, imap.ErrDeleteInbox):
		return fmt.Errorf("%w: %w", ErrMailboxProtected, err)
	default:
		return err
	}
}
