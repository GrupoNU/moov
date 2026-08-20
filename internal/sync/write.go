package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
)

// The write executor (W1, L2-jmap-write §4): the ONE path through which a
// client-initiated change reaches Dovecot.
//
// # The W-A1 ordering, stated once
//
// Dovecot first, store second, answer last. Dovecot is the source of truth
// (ADR-001) and Moov's store is a reconstructible cache; a write reflected
// locally before the server accepted it would be the cache inventing state.
// So every Apply* here (1) applies the change over IMAP, (2) reflects the
// server's OWN answer into message_state — never the caller's intent — and
// (3) only then returns. If IMAP fails, the store is not touched and the
// caller gets a loud error: there is deliberately no path on which a change
// is half-true.
//
// # Echo-safety: how a write and the watcher coexist
//
// Every write here also raises a NOTIFY event, because the watcher's
// connection observes the same mailbox this executor just changed. That echo
// triggers an incremental pass, and the design makes the pass converge on the
// already-reflected state instead of duplicating or flapping:
//
//   - A FLAG echo arrives as a CHANGEDSINCE row. applyFlagChanges compares it
//     against message_state and skips when flags, keywords — and, since W1,
//     nothing else — already match (incremental.go: "No observable change").
//     The executor reflects the READ-BACK state with the server's own modseq,
//     so the echo is a no-op by content, and updated_at does not move again.
//   - A MOVE echo arrives twice: VANISHED in the source and a new UID in the
//     destination. The executor has already re-pointed the message_state row
//     at (destination, new UID) using COPYUID, so MarkDeleted's WHERE clause
//     (old mailbox, old UID, deleted_at IS NULL) matches nothing, and the
//     destination pass finds the UID in ExistingUIDs and never re-fetches the
//     body. Convergent from both directions.
//   - A DESTROY echo is a VANISHED for a row that is already tombstoned;
//     MarkDeleted is idempotent by its deleted_at IS NULL guard.
//
// The mailbox-level sync cursor is NOT advanced here — that stays the
// incremental pass's job (incremental.go: "the cursor moves only now"). The
// echo pass therefore still runs, sees its work already done, and advances
// the cursor itself; skipping the cursor forward from here would race the
// very pass that owns it.
//
// # Connections
//
// One cached connection per account, dialed on demand through the same
// Connector the watcher uses, serialized by a per-account lock (imap.Client
// is a single command stream). A connection that died while idle self-heals:
// the SELECT that opens every operation fails, the connection is discarded,
// and one fresh dial is attempted. The write command itself is NEVER
// auto-retried — a command whose connection died mid-flight has an UNKNOWN
// outcome, and retrying an unknown MOVE is how a message moves twice. The
// caller (a human clicking retry, or a client resubmitting a /set) retries
// against fresh state instead.

// Errors the write executor returns. Sentinels, so the JMAP layer can map
// each condition to its RFC 8620 §5.3 SetError without matching strings.
var (
	// ErrWriteNotFound means the message does not exist, is tombstoned, or
	// belongs to another account. The three are deliberately one error: a
	// write path that distinguished them would be an existence oracle, same
	// rule as the readers (jmap/mail contracts.go).
	ErrWriteNotFound = errors.New("sync: no such message for this account")

	// ErrWriteConflict means the message changed on the server after the
	// state this write was computed from (RFC 7162 UNCHANGEDSINCE, S2 H6),
	// or the mailbox's UIDVALIDITY changed under it. The store has been
	// refreshed with the server's current state where possible; the caller
	// should re-read and retry.
	ErrWriteConflict = errors.New("sync: the message changed on the server; re-read and retry")

	// ErrNoTrashMailbox means a destroy-outside-Trash (W-A2) could not find
	// the \Trash role mailbox to move into.
	ErrNoTrashMailbox = errors.New("sync: the account has no Trash mailbox")
)

// FlagChange is one flag/keyword mutation, in the imap package's normalized
// vocabulary: system flags as "seen"/"answered"/"flagged"/"draft", user
// keywords verbatim. The JMAP layer translates from $-keywords before calling.
type FlagChange struct {
	// Replace, when true, makes Flags the message's exact final flag set —
	// RFC 8621 §4.6's "keywords: set to exactly this" semantics. Applied as a
	// conditional STORE FLAGS with UNCHANGEDSINCE, because a full-set replace
	// computed from stale state is the textbook lost update (RFC 7162 §1).
	Replace bool

	// Flags is the full final set when Replace is true.
	Flags []string

	// Add and Remove are the patch halves when Replace is false. They are
	// applied as +FLAGS/-FLAGS WITHOUT UNCHANGEDSINCE — deliberately: a
	// per-flag delta is commutative with concurrent changes (it touches only
	// the named flags), so a conditional write would surface conflicts that
	// cannot corrupt anything, and every false conflict is a user's click
	// bouncing for no reason.
	Add    []string
	Remove []string
}

// isNoop reports a change that asks for nothing.
func (c FlagChange) isNoop() bool {
	return !c.Replace && len(c.Add) == 0 && len(c.Remove) == 0
}

// WriteResult is the state of the message after a successful write, as the
// SERVER reported it — the read-back, not the request.
type WriteResult struct {
	// MailboxID and UID are where the message lives now (they change on a
	// move).
	MailboxID int64
	UID       int64

	// Flags and Keywords are the message's post-write state.
	Flags    store.Flags
	Keywords []string

	// Expunged reports a W-A2 destroy-inside-Trash: the message is gone from
	// the server, not moved. The row is tombstoned.
	Expunged bool
}

// WriteOptions configures a WriteExecutor.
type WriteOptions struct {
	// Logger receives structured diagnostics. Default slog.Default().
	Logger *slog.Logger

	// Broker, when set, is notified after every write that changed something
	// (W4a). It is what makes a change made through Moov's own JMAP API reach
	// the account's OTHER connected clients promptly, instead of waiting for
	// the watcher's NOTIFY echo to complete a round trip through Dovecot.
	//
	// The echo still arrives and is still handled — the write path's
	// echo-safety rules (see the package comment) make the resulting pass a
	// no-op by content, and a no-op pass does not notify. So the fast local
	// notification and the slower authoritative echo coalesce into at most a
	// second harmless wake-up, never into divergent state.
	Broker *Broker

	// Blobs, when set, enables the append path (W3: Email/set create and the
	// outbox's \Sent copy — append.go). An executor without it refuses appends
	// loudly and serves the W1/W2 operations unchanged, which is what keeps
	// every pre-W3 construction site valid.
	Blobs AppendBlobStore
}

// WriteExecutor applies client writes per W-A1. One instance serves every
// account; construction is cheap and connections are dialed on first use.
type WriteExecutor struct {
	store     *store.Store
	connector Connector
	log       *slog.Logger
	broker    *Broker
	blobs     AppendBlobStore

	mu       sync.Mutex
	accounts map[int64]*accountConn
	closed   bool
}

// accountConn is one account's cached write connection, serialized by its own
// lock so two concurrent /set calls for the same account cannot interleave
// commands on one socket.
type accountConn struct {
	mu     sync.Mutex
	client imap.Client
}

// NewWriteExecutor builds the executor.
func NewWriteExecutor(st *store.Store, connector Connector, opts WriteOptions) (*WriteExecutor, error) {
	if st == nil {
		return nil, errors.New("sync: a store is required")
	}
	if connector == nil {
		return nil, errors.New("sync: a Connector is required")
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &WriteExecutor{
		store:     st,
		connector: connector,
		log:       log.With("component", "write-executor"),
		broker:    opts.Broker,
		blobs:     opts.Blobs,
		accounts:  map[int64]*accountConn{},
	}, nil
}

// Close releases every cached connection. The executor must not be used after.
func (w *WriteExecutor) Close() {
	w.mu.Lock()
	conns := make([]*accountConn, 0, len(w.accounts))
	for _, ac := range w.accounts {
		conns = append(conns, ac)
	}
	w.accounts = map[int64]*accountConn{}
	w.closed = true
	w.mu.Unlock()

	for _, ac := range conns {
		ac.mu.Lock()
		if ac.client != nil {
			_ = ac.client.Close()
			ac.client = nil
		}
		ac.mu.Unlock()
	}
}

// ---------------------------------------------------------------------------
// The three write operations of L2-jmap-write §4
// ---------------------------------------------------------------------------

// ApplyFlagChange applies a flag/keyword change to one message: Dovecot
// first, then the read-back reflected into message_state.
func (w *WriteExecutor) ApplyFlagChange(ctx context.Context, accountID, messageID int64, change FlagChange) (WriteResult, error) {
	var out WriteResult

	st, mb, err := w.target(ctx, accountID, messageID)
	if err != nil {
		return out, err
	}
	out.MailboxID, out.UID = st.MailboxID, st.UID

	if change.isNoop() {
		// Nothing to ask the server; the current state is the answer.
		out.Flags, out.Keywords = st.Flags, st.Keywords
		return out, nil
	}

	account, err := w.store.GetAccount(ctx, accountID)
	if err != nil {
		return out, fmt.Errorf("loading account %d: %w", accountID, err)
	}

	err = w.withMailbox(ctx, account, mb.Name, func(c imap.Client, sel imap.SelectResult) error {
		if err := checkUIDValidity(sel, st); err != nil {
			return err
		}
		uid := uidFromDB(st.UID)

		if change.Replace {
			if err := w.storeReplace(ctx, c, st, uid, change.Flags); err != nil {
				return err
			}
		} else {
			// +FLAGS then -FLAGS, unconditional (see FlagChange). Two
			// commands, but each touches only its named flags, so no
			// interleaving with a concurrent writer can lose a bit that was
			// not explicitly in this change.
			if len(change.Add) > 0 {
				if _, err := c.StoreFlags(ctx, []imap.UID{uid},
					imap.FlagDelta{Op: imap.FlagsAdd, Flags: change.Add}, 0); err != nil {
					return fmt.Errorf("adding flags: %w", err)
				}
			}
			if len(change.Remove) > 0 {
				if _, err := c.StoreFlags(ctx, []imap.UID{uid},
					imap.FlagDelta{Op: imap.FlagsRemove, Flags: change.Remove}, 0); err != nil {
					return fmt.Errorf("removing flags: %w", err)
				}
			}
		}

		// The read-back: what the store learns is the server's answer, never
		// the request. This is the applies-and-reflects half of W-A1 — the
		// alternative (reflecting the intent) diverges the moment the server
		// normalized, refused or raced anything.
		msg, err := fetchOne(ctx, c, uid)
		if err != nil {
			return err
		}
		if msg == nil {
			// Expunged by another client between our STORE and the read-back.
			// The flags write may or may not have landed; either way the
			// message is gone and the incremental pass will tombstone it.
			return ErrWriteNotFound
		}
		flags, keywords, err := w.reflectFlags(ctx, st, msg)
		if err != nil {
			return err
		}
		out.Flags, out.Keywords = flags, keywords
		return nil
	})
	if err == nil {
		// W4a: the store now agrees with Dovecot, so the state string a
		// pushed StateChange resolves to already describes this write. The
		// no-op branch above returned before reaching here, so an idempotent
		// replay wakes nobody.
		w.broker.Notify(accountID)
	}
	return out, err
}

// ApplyMove moves one message to another mailbox of the same account.
func (w *WriteExecutor) ApplyMove(ctx context.Context, accountID, messageID, targetMailboxID int64) (WriteResult, error) {
	var out WriteResult

	st, srcMb, err := w.target(ctx, accountID, messageID)
	if err != nil {
		return out, err
	}
	if st.MailboxID == targetMailboxID {
		// Already there. Answering success without a round trip is the
		// correct reading of an idempotent move, and it is what a replayed
		// /set hits.
		return WriteResult{MailboxID: st.MailboxID, UID: st.UID, Flags: st.Flags, Keywords: st.Keywords}, nil
	}

	tgtMb, err := w.store.GetMailbox(ctx, targetMailboxID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return out, ErrWriteNotFound
		}
		return out, fmt.Errorf("loading target mailbox %d: %w", targetMailboxID, err)
	}
	// The target must be the caller's own and must be a real, selectable
	// folder. A foreign mailbox is indistinguishable from a missing one.
	if tgtMb.AccountID != accountID || !tgtMb.Selectable {
		return out, ErrWriteNotFound
	}

	account, err := w.store.GetAccount(ctx, accountID)
	if err != nil {
		return out, fmt.Errorf("loading account %d: %w", accountID, err)
	}

	err = w.withMailbox(ctx, account, srcMb.Name, func(c imap.Client, sel imap.SelectResult) error {
		if err := checkUIDValidity(sel, st); err != nil {
			return err
		}
		uid := uidFromDB(st.UID)

		res, err := c.Move(ctx, []imap.UID{uid}, tgtMb.Name)
		if err != nil {
			return fmt.Errorf("moving to %q: %w", tgtMb.Name, err)
		}

		destUID, mapped := res.DestUIDs[uid]
		if !mapped {
			// No COPYUID (no UIDPLUS, or an unpairable set). The move
			// happened on the server; without the mapping the local row
			// cannot be re-pointed, so the honest reflection is a tombstone —
			// the destination copy arrives through the ordinary sync as a new
			// message. JMAP-wise the id dies and a new one is born, which is
			// the documented degradation, never the default (Dovecot has
			// UIDPLUS).
			w.log.Warn("move completed without a usable COPYUID; tombstoning the source row",
				"account_id", accountID, "message_id", messageID, "target", tgtMb.Name)
			if err := w.store.MarkDeleted(ctx, srcMb.ID, st.UIDValidity, []int64{st.UID}); err != nil {
				return fmt.Errorf("tombstoning after unmapped move: %w", err)
			}
			out.MailboxID, out.UID = targetMailboxID, 0
			out.Flags, out.Keywords = st.Flags, st.Keywords
			return nil
		}

		// Select the destination to learn the message's fresh modseq (a move
		// assigns a new one) and to confirm the UIDVALIDITY the mapping is
		// valid under. One extra round trip; it is what keeps the row's
		// modseq_seen truthful, and a stale modseq_seen is a future false
		// conflict on every conditional write.
		dsel, err := c.SelectQResync(ctx, tgtMb.Name, 0, 0)
		if err != nil {
			return fmt.Errorf("selecting %q after move: %w", tgtMb.Name, err)
		}
		destValidity := int64(dsel.UIDValidity)
		if res.DestUIDValidity != 0 && res.DestUIDValidity != dsel.UIDValidity {
			// The destination was recreated between the MOVE and the SELECT.
			// The mapping's UIDs belong to the old incarnation; re-pointing
			// the row at them would bind it to a message that no longer
			// exists. Tombstone instead and let the sync rediscover.
			if err := w.store.MarkDeleted(ctx, srcMb.ID, st.UIDValidity, []int64{st.UID}); err != nil {
				return fmt.Errorf("tombstoning after uidvalidity race: %w", err)
			}
			out.MailboxID, out.UID = targetMailboxID, 0
			out.Flags, out.Keywords = st.Flags, st.Keywords
			return nil
		}

		if err := w.store.MoveMessages(ctx, []int64{messageID}, targetMailboxID, destValidity, []int64{int64(destUID)}); err != nil {
			return fmt.Errorf("reflecting move in the store: %w", err)
		}

		out.MailboxID, out.UID = targetMailboxID, int64(destUID)
		out.Flags, out.Keywords = st.Flags, st.Keywords

		// Read back the moved message for its new modseq (flags survive a
		// MOVE, but the modseq is the destination mailbox's). Best-effort in
		// one narrow sense: if the message was ALREADY expunged or moved on
		// by another client, the row keeps a stale modseq_seen of 0-risk —
		// the incremental pass owns whatever happened next.
		msg, err := fetchOne(ctx, c, destUID)
		if err != nil {
			return err
		}
		if msg != nil {
			moved := st
			moved.MailboxID, moved.UID, moved.UIDValidity = targetMailboxID, int64(destUID), destValidity
			flags, keywords, err := w.reflectFlags(ctx, moved, msg)
			if err != nil {
				return err
			}
			out.Flags, out.Keywords = flags, keywords
		}
		return nil
	})
	if err == nil {
		// A move changes two mailboxes' counts and the message's own row; the
		// already-there short circuit above returned before reaching here.
		w.broker.Notify(accountID)
	}
	return out, err
}

// ApplyDestroy destroys one message per W-A2: outside Trash it is a MOVE to
// the \Trash role mailbox (reversible, Gmail semantics); already inside Trash
// it is \Deleted + UID EXPUNGE (final). Never an expunge from anywhere else —
// the pilot mailbox is the product owner's real mail.
func (w *WriteExecutor) ApplyDestroy(ctx context.Context, accountID, messageID int64) (WriteResult, error) {
	var out WriteResult

	st, mb, err := w.target(ctx, accountID, messageID)
	if err != nil {
		return out, err
	}

	if mb.Role != store.RoleTrash {
		trash, err := w.store.GetMailboxByRole(ctx, accountID, store.RoleTrash)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return out, ErrNoTrashMailbox
			}
			return out, fmt.Errorf("resolving the Trash mailbox: %w", err)
		}
		return w.ApplyMove(ctx, accountID, messageID, trash.ID)
	}

	account, err := w.store.GetAccount(ctx, accountID)
	if err != nil {
		return out, fmt.Errorf("loading account %d: %w", accountID, err)
	}

	err = w.withMailbox(ctx, account, mb.Name, func(c imap.Client, sel imap.SelectResult) error {
		if err := checkUIDValidity(sel, st); err != nil {
			return err
		}
		if err := c.Expunge(ctx, []imap.UID{uidFromDB(st.UID)}); err != nil {
			return fmt.Errorf("expunging from %q: %w", mb.Name, err)
		}
		// Tombstone, not DELETE: Email/changes must report the destroy until
		// every client caught up (store.MarkDeleted's contract). The watcher's
		// VANISHED echo re-runs this exact statement and matches zero rows.
		if err := w.store.MarkDeleted(ctx, mb.ID, st.UIDValidity, []int64{st.UID}); err != nil {
			return fmt.Errorf("tombstoning after expunge: %w", err)
		}
		out = WriteResult{MailboxID: mb.ID, UID: st.UID, Flags: st.Flags, Keywords: st.Keywords, Expunged: true}
		return nil
	})
	if err == nil {
		// Only the expunge-inside-Trash branch reaches here; the
		// destroy-outside-Trash branch returned through ApplyMove above,
		// which notified for itself. One notification per destroy either way.
		w.broker.Notify(accountID)
	}
	return out, err
}

// ---------------------------------------------------------------------------
// internals
// ---------------------------------------------------------------------------

// target resolves a message id to its state and mailbox, enforcing that the
// authenticated account owns it. THE authorization check of the write path:
// no write proceeds past this line for a message the account does not hold.
func (w *WriteExecutor) target(ctx context.Context, accountID, messageID int64) (store.MessageState, store.Mailbox, error) {
	st, err := w.store.GetMessageState(ctx, messageID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.MessageState{}, store.Mailbox{}, ErrWriteNotFound
		}
		return store.MessageState{}, store.Mailbox{}, fmt.Errorf("loading message %d: %w", messageID, err)
	}
	if st.AccountID != accountID || st.DeletedAt != nil {
		return store.MessageState{}, store.Mailbox{}, ErrWriteNotFound
	}

	mb, err := w.store.GetMailbox(ctx, st.MailboxID)
	if err != nil {
		return store.MessageState{}, store.Mailbox{}, fmt.Errorf("loading mailbox %d: %w", st.MailboxID, err)
	}
	return st, mb, nil
}

// checkUIDValidity refuses to write when the selected mailbox's UIDVALIDITY
// no longer matches the row's: every local UID then names a different message
// (RFC 3501 §2.3.1.1), and a write against it would hit an arbitrary one. The
// incremental pass repairs the divergence; the caller retries after.
func checkUIDValidity(sel imap.SelectResult, st store.MessageState) error {
	if int64(sel.UIDValidity) != st.UIDValidity {
		return fmt.Errorf("%w: uidvalidity moved from %d to %d", ErrWriteConflict, st.UIDValidity, sel.UIDValidity)
	}
	return nil
}

// storeReplace performs the conditional full-set STORE, with the conflict
// protocol that keeps optimistic concurrency both safe and self-healing.
func (w *WriteExecutor) storeReplace(ctx context.Context, c imap.Client, st store.MessageState, uid imap.UID, flags []string) error {
	// A JMAP full-set replaces "the keywords", and JMAP's keyword model
	// cannot express two pieces of IMAP flag state: \Deleted (RFC 8621
	// §4.1.1 defines no keyword for it) and backslash-prefixed flags other
	// servers report (\Junk on some), which the read path stores verbatim
	// but hides from clients. A bare STORE FLAGS would erase both — state
	// the client never saw and never asked to change. They are re-added to
	// the replace set instead; safe to take from the store's row because
	// UNCHANGEDSINCE below guarantees the row still matches the server.
	flags = append(append([]string(nil), flags...), invisibleFlags(st)...)
	delta := imap.FlagDelta{Op: imap.FlagsSet, Flags: flags}

	unchanged := modSeqFromDB(st.ModSeqSeen)
	if unchanged == 0 {
		// No recorded modseq (a row from before W1, or a post-move gap). An
		// unconditional replace would clobber whatever a concurrent writer
		// did, so the current modseq is read first and the write stays
		// conditional — the TOCTOU window collapses to one round trip and
		// the server's own UNCHANGEDSINCE closes it.
		msg, err := fetchOne(ctx, c, uid)
		if err != nil {
			return err
		}
		if msg == nil {
			return ErrWriteNotFound
		}
		unchanged = msg.ModSeq
	}

	res, err := c.StoreFlags(ctx, []imap.UID{uid}, delta, unchanged)
	if err != nil {
		return fmt.Errorf("replacing flags: %w", err)
	}
	if !res.Conflicted() {
		return nil
	}

	// The server refused: the message's modseq moved past what the store
	// knew. Two very different situations produce that, and one read-back
	// tells them apart:
	//
	//  1. The modseq moved but the CONTENT still matches the store's belief
	//     (a flag toggled and toggled back, an echo the incremental pass
	//     skipped as a no-op). The premise the client acted on still holds,
	//     so the write is retried once against the fresh modseq. Without
	//     this branch, a stale modseq_seen would bounce every conditional
	//     write forever — the store never learns a modseq from a no-op.
	//  2. The content genuinely changed. That is the real conflict: the
	//     fresh state is reflected into the store (so the client's re-read
	//     sees the truth, not the stale row that caused this) and
	//     ErrWriteConflict surfaces.
	fresh, err := fetchOne(ctx, c, uid)
	if err != nil {
		return err
	}
	if fresh == nil {
		return ErrWriteNotFound
	}

	if storeFlags(fresh.Flags) == st.Flags && sameKeywords(sanitizeAll(fresh.Keywords), st.Keywords) {
		res, err = c.StoreFlags(ctx, []imap.UID{uid}, delta, fresh.ModSeq)
		if err != nil {
			return fmt.Errorf("replacing flags (retry after modseq refresh): %w", err)
		}
		if !res.Conflicted() {
			return nil
		}
		// A second refusal inside one round trip means a live concurrent
		// writer; fall through to the honest conflict.
		fresh, err = fetchOne(ctx, c, uid)
		if err != nil {
			return err
		}
		if fresh == nil {
			return ErrWriteNotFound
		}
	}

	if _, _, err := w.reflectFlags(ctx, st, fresh); err != nil {
		return err
	}
	return fmt.Errorf("%w: flags changed concurrently", ErrWriteConflict)
}

// reflectFlags writes the server's read-back state into message_state (A5:
// message_state and nothing else), skipping the write when the row already
// matches — which is also what makes a replayed no-op cost nothing.
func (w *WriteExecutor) reflectFlags(ctx context.Context, st store.MessageState, msg *imap.Message) (store.Flags, []string, error) {
	flags := storeFlags(msg.Flags)
	keywords := sanitizeAll(msg.Keywords)
	newMod := modSeqToDB(msg.ModSeq)

	if st.Flags == flags && sameKeywords(st.Keywords, keywords) && st.ModSeqSeen == newMod {
		return flags, keywords, nil
	}
	if err := w.store.UpdateFlags(ctx, []store.FlagUpdate{{
		MessageID:  st.MessageID,
		Flags:      flags,
		Keywords:   keywords,
		ModSeqSeen: newMod,
	}}); err != nil {
		return flags, keywords, fmt.Errorf("reflecting flags in the store: %w", err)
	}
	return flags, keywords, nil
}

// withMailbox runs fn with the account's write connection holding the given
// mailbox selected.
//
// The SELECT is the liveness probe: a cached connection that died while idle
// fails it, gets discarded, and ONE fresh dial is attempted — that retry is
// safe because selecting is read-only. fn itself runs exactly once; see the
// package comment for why a write command is never auto-retried.
func (w *WriteExecutor) withMailbox(ctx context.Context, account store.Account, mailbox string, fn func(imap.Client, imap.SelectResult) error) error {
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
			return fmt.Errorf("connecting for a write: %w", err)
		}
		sel, err := c.SelectQResync(ctx, mailbox, 0, 0)
		if err != nil {
			// Covers the dead-idle connection AND the stale-session view
			// (imap.ErrMailboxStale): both are cured by a fresh connection
			// and by nothing else.
			ac.discard()
			lastErr = err
			if attempt == 0 {
				continue
			}
			return fmt.Errorf("selecting %q for a write: %w", mailbox, lastErr)
		}
		return fn(c, sel)
	}
	return fmt.Errorf("selecting %q for a write: %w", mailbox, lastErr)
}

// forAccount returns the account's connection slot, creating it on first use.
func (w *WriteExecutor) forAccount(accountID int64) (*accountConn, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil, errors.New("sync: the write executor is closed")
	}
	ac, ok := w.accounts[accountID]
	if !ok {
		ac = &accountConn{}
		w.accounts[accountID] = ac
	}
	return ac, nil
}

// ensure returns the cached connection, dialing when there is none. Caller
// holds ac.mu.
func (ac *accountConn) ensure(ctx context.Context, connector Connector, account store.Account) (imap.Client, error) {
	if ac.client != nil {
		return ac.client, nil
	}
	clients, err := connector.Connect(ctx, account, 1)
	if err != nil {
		return nil, err
	}
	if len(clients) == 0 {
		return nil, errors.New("sync: the connector returned no connections")
	}
	// A connector handing out more than asked is unexpected but must not leak
	// sockets.
	for _, extra := range clients[1:] {
		_ = extra.Close()
	}
	ac.client = clients[0]
	return ac.client, nil
}

// discard closes and forgets the cached connection. Caller holds ac.mu.
func (ac *accountConn) discard() {
	if ac.client != nil {
		_ = ac.client.Close()
		ac.client = nil
	}
}

// invisibleFlags returns the flag state a JMAP client cannot see and a
// full-set replace must therefore carry over (see storeReplace).
func invisibleFlags(st store.MessageState) []string {
	var out []string
	if st.Flags.Has(store.FlagDeleted) {
		out = append(out, "deleted")
	}
	for _, k := range st.Keywords {
		if len(k) > 0 && k[0] == '\\' {
			out = append(out, k)
		}
	}
	return out
}

// fetchOne reads one message's flags, keywords and modseq from the selected
// mailbox. nil means the UID no longer exists there.
func fetchOne(ctx context.Context, c imap.Client, uid imap.UID) (*imap.Message, error) {
	it, err := c.FetchMessages(ctx, []imap.UID{uid}, imap.FetchSpec{Flags: true})
	if err != nil {
		return nil, fmt.Errorf("reading back uid %d: %w", uid, err)
	}
	defer func() { _ = it.Close() }()

	var out *imap.Message
	for {
		msg, err := it.Next()
		if err != nil {
			return nil, fmt.Errorf("reading back uid %d: %w", uid, err)
		}
		if msg == nil {
			break
		}
		if msg.UID == uid {
			// Copy the scalar state; the iterator owns nothing we keep.
			m := *msg
			m.Body = nil
			out = &m
		}
	}
	if err := it.Close(); err != nil {
		return nil, fmt.Errorf("reading back uid %d: %w", uid, err)
	}
	return out, nil
}
