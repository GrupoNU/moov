package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/GrupoNU/moov/internal/blob"
	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
)

// Snooze, in the engine (L3 epic E4, arbitration GC-10).
//
// ===========================================================================
// THE CANON, AND THE TWO SENTENCES THAT DECIDE THE DESIGN
// ===========================================================================
//
// docs/research/06-gmail-canon.md §2.2, quoting support.google.com/mail/answer
// /7622010 verbatim:
//
//	"Removed from your inbox temporarily"; returns "to the TOP of your inbox".
//
// The first sentence is what GC-10 turns into a MOVE: the message leaves INBOX
// in Dovecot, so every IMAP client — Thunderbird, a phone, the user's Outlook —
// agrees with Moov about where the mail is, and a cache rebuild rediscovers
// the snoozed set by listing one folder. Postgres-only state would have failed
// both.
//
// The SECOND sentence is the one that has no obvious IMAP answer, and it is
// the arbitration this file records.
//
// ===========================================================================
// THE RETURN-TO-TOP DECISION: re-APPEND, not MOVE back
// ===========================================================================
//
// Our inbox sorts by receivedAt (RFC 8621 §4.4.2's default and what Bulwark
// asks for on every folder open — the sort pair `hasKeyword`+`receivedAt`
// documented in the J4 report). receivedAt is the message's INTERNALDATE.
//
// IMAP's MOVE (RFC 6851 §3.3) is defined as COPY + \Deleted + EXPUNGE, and RFC
// 3501 §6.4.7 requires COPY to preserve the internal date: "the copied message
// SHOULD have [...] the internal date of the source message". Dovecot honors
// that. So MOVING a message back from Snoozed returns it at its ORIGINAL
// position — buried under everything that arrived while it slept. A message
// snoozed for a week comes back on page four. That is not "the top of your
// inbox"; it is the opposite of the feature.
//
// The two candidate designs:
//
//	(a) RE-APPEND with a fresh INTERNALDATE, then expunge the sleeping copy.
//	    The message reappears at the top because its receivedAt IS now. Same
//	    Message-ID, same bytes, new UID.
//
//	(b) MOVE back in place, plus a `$snoozed-wake` keyword the UI sorts on.
//
// (a) is chosen. The reasoning, against GC-10 and the canon:
//
//   - It is the only one that satisfies the canon's own words for EVERY
//     client. (b) puts the message back at its old position for Thunderbird
//     and every other IMAP client, and relies on Moov's UI to fake the
//     ordering — which is the "state only Moov can see" pattern GC-10 exists
//     to forbid, wearing a keyword as a disguise.
//   - (b) also spends one of the 26 durable Maildir keywords (GC-5's rationed
//     resource) on a flag whose only consumer is our own sort.
//   - Gmail's behavior is (a)-shaped: a woken message shows the SNOOZE time as
//     its date, not the original delivery time, which is only expressible by
//     changing what the client reads as the date.
//
// # The side effects of (a), stated rather than buried
//
//  1. NEW UID, so the JMAP Email id changes. A client holding the old id gets
//     notFound; Email/changes reports the old id destroyed and the new one
//     created. This is the same contract a MOVE without COPYUID already has
//     (write.go), and RFC 8621 §4.1.1 makes an Email id opaque and per-mailbox
//     anyway.
//  2. OTHER IMAP CLIENTS SEE IT AS NEW MAIL. Thunderbird will show an unread
//     badge, a phone may notify. That is a real consequence and it is also
//     what the user asked for: "bring this back to my attention on Tuesday"
//     means exactly "make it new again on Tuesday". Gmail's own snooze
//     notifies on wake.
//  3. The message's \Seen state is PRESERVED (the flags are re-appended with
//     it), so a message read before snoozing does not come back unread. Only
//     its position changes. This is the one place we deliberately do NOT copy
//     "new mail" semantics: resetting \Seen would be inventing a fact about
//     the user's own reading.
//  4. INTERNALDATE is not the Date: header. The original Date: header is
//     untouched, so the message still says when it was actually sent, and
//     "show original" is unaffected. Only the delivery timestamp — which is
//     genuinely "when this arrived in your inbox", and it did arrive again —
//     moves.
//  5. It costs one download of the message's raw bytes. They are already in
//     our content-addressed blob store, so the cost is a local read, not a
//     re-fetch from Dovecot.
//
// # The failure ordering
//
// APPEND first, expunge second. The reverse would risk losing the mail: an
// expunge that succeeds followed by an append that fails deletes the user's
// message. In this order the worst failure is a DUPLICATE — the woken copy in
// INBOX and the sleeping one still in Snoozed — which is visible, harmless,
// and repaired by the user or by a retry (the second attempt finds the source
// gone and only has to expunge). "Duplicate beats lost" is the same rule the
// outbox applies to \Sent copies (ADR §4).

// SnoozeMailboxName is the folder snoozed mail lives in.
//
// # Why this exact name, and why it has no SPECIAL-USE role
//
// RFC 6154 defines the special-use attributes — \All \Archive \Drafts \Flagged
// \Junk \Sent \Trash — and there is NO registered attribute for snoozed mail.
// The IANA registry has not grown one; Gmail's own IMAP gateway does not
// expose Snoozed as a folder at all. So there is nothing standard to claim,
// and inventing an attribute (\Snoozed) would put a non-registered string in
// the LIST response of a server we do not own.
//
// The choice is therefore a NAME, and "Snoozed" is picked because it is the
// word Gmail's own UI uses (canon §2.2: `in:snoozed`, `g b` goes to Snoozed),
// so a user who opens Thunderbird finds a folder whose purpose is legible
// without a manual.
//
// Role-ish treatment, without a role: the folder is created on first use,
// SUBSCRIBED (so other clients show it), and recognized by NAME in this
// engine and in the JMAP layer. Recognition by name is weaker than by role —
// a user who renames it breaks the association — and that weakness is accepted
// over the alternative of writing a fake SPECIAL-USE attribute. If IANA ever
// registers one, this constant becomes a role lookup and the folder is
// re-detected; nothing else changes.
const SnoozeMailboxName = "Snoozed"

// Errors the snooze paths return.
var (
	// ErrSnoozeUnavailable means the Snoozed folder could not be created or
	// found, so the message was NOT moved and nothing was recorded.
	ErrSnoozeUnavailable = errors.New("sync: the Snoozed mailbox is unavailable")

	// ErrNotSnoozed means an unsnooze named a message that is not in the
	// Snoozed folder.
	ErrNotSnoozed = errors.New("sync: the message is not snoozed")
)

// SnoozeResult reports what a snooze did.
type SnoozeResult struct {
	// MailboxID is the Snoozed folder the message now lives in.
	MailboxID int64
	// UID is where it landed.
	UID int64
	// WakeAt is the recorded wake time.
	WakeAt time.Time
	// OriginMailbox is the folder it came from, recorded for the wake.
	OriginMailbox string
}

// WakeResult reports what a wake did.
type WakeResult struct {
	// MessageID is the store id of the WOKEN message — a NEW id, because the
	// wake re-appends (see the file header).
	MessageID int64
	// MailboxID and UID are where it landed.
	MailboxID int64
	UID       int64
}

// ApplySnooze moves one message to the Snoozed folder and records its wake.
//
// The ordering is the W-A1 one every write here follows — Dovecot first, store
// second — with one addition specific to this operation: the snooze ROW is
// written after the MOVE, so a crash between them leaves a message in Snoozed
// with no wake time. That state is recoverable and visible (the message is in
// a folder the user can open), whereas the reverse — a wake scheduled for a
// message still sitting in INBOX — would fire a MOVE-back of a message that
// never left, which the executor would answer with a confusing not-found.
func (w *WriteExecutor) ApplySnooze(ctx context.Context, accountID, messageID int64, wakeAt time.Time) (SnoozeResult, error) {
	var out SnoozeResult

	_, srcMb, err := w.target(ctx, accountID, messageID)
	if err != nil {
		return out, err
	}
	msg, err := w.ownedMessage(ctx, accountID, messageID)
	if err != nil {
		return out, err
	}
	if msg.MessageID == "" {
		// The durable key IS the Message-ID (migration 0009). A message
		// without one cannot be snoozed, because the wake could not find it
		// again after a rebuild — and silently snoozing it into a folder it
		// would never leave is the worst of the options.
		return out, fmt.Errorf("%w: the message has no Message-ID, so its snooze could not be recorded durably",
			ErrSnoozeUnavailable)
	}

	snoozeMb, err := w.ensureSnoozeMailbox(ctx, accountID)
	if err != nil {
		return out, err
	}
	if snoozeMb.ID == srcMb.ID {
		return out, fmt.Errorf("%w: the message is already snoozed", ErrSnoozeUnavailable)
	}

	if _, err := w.ApplyMove(ctx, accountID, messageID, snoozeMb.ID); err != nil {
		return out, err
	}

	origin := srcMb.Name
	if srcMb.Role == store.RoleInbox || strings.EqualFold(origin, "INBOX") {
		// The empty string means INBOX (migration 0009), which keeps the row
		// correct across a rename of the inbox — which cannot happen — and,
		// more usefully, makes the common case one obvious value in the table.
		origin = ""
	}
	row, err := w.store.PutSnooze(ctx, store.Snooze{
		AccountID:     accountID,
		MessageRFCID:  msg.MessageID,
		WakeAt:        wakeAt,
		OriginMailbox: origin,
	})
	if err != nil {
		// The MOVE already happened. Reporting the error is right — the caller
		// must not believe a wake is scheduled — and the message stays in
		// Snoozed, which the user can see and move back by hand.
		return out, fmt.Errorf("recording the snooze (the message IS in %s): %w", SnoozeMailboxName, err)
	}

	if stAfter, serr := w.store.GetMessageState(ctx, messageID); serr == nil {
		out.UID = stAfter.UID
	}
	out.MailboxID = snoozeMb.ID
	out.WakeAt = row.WakeAt
	out.OriginMailbox = row.OriginMailbox
	return out, nil
}

// ApplyUnsnooze cancels a pending snooze and returns the message to its origin
// NOW, using the same re-append the timed wake uses.
//
// It is the same operation as a wake whose time has come; the only difference
// is who asked. Sharing the implementation is what keeps "un-snooze" and "the
// wake fired" from drifting into two behaviors — a class of bug that would
// only show up as "the message came back in the wrong place, but only when I
// clicked the button".
func (w *WriteExecutor) ApplyUnsnooze(ctx context.Context, accountID, messageID int64) (WakeResult, error) {
	var out WakeResult

	msg, err := w.ownedMessage(ctx, accountID, messageID)
	if err != nil {
		return out, err
	}

	// The origin comes from the snooze row when there is one. When there is
	// not — a message somebody dropped into Snoozed with another client, or one
	// whose row was lost — INBOX is the honest default: it is where the canon
	// says a snooze returns to, and it is where the user is looking.
	origin := ""
	snoozes, err := w.store.SnoozesByMessageIDs(ctx, accountID, []string{msg.MessageID})
	if err != nil {
		return out, fmt.Errorf("reading the snooze of message %d: %w", messageID, err)
	}
	if sn, ok := snoozes[msg.MessageID]; ok {
		origin = sn.OriginMailbox
	}

	res, err := w.wakeMessage(ctx, accountID, messageID, origin)
	if err != nil {
		return out, err
	}
	if _, err := w.store.CancelSnooze(ctx, accountID, msg.MessageID); err != nil {
		// The message is back where it belongs; a stale pending row would only
		// cause a second, idempotent wake later. Logged, not fatal.
		w.log.Warn("the message was un-snoozed but its snooze row could not be cleared",
			"account_id", accountID, "message_id", messageID, "error", err)
	}
	return res, nil
}

// wakeMessage performs the re-append that returns a snoozed message to the top
// of its origin folder. It is the implementation of decision (a) in the file
// header.
func (w *WriteExecutor) wakeMessage(ctx context.Context, accountID, messageID int64, originMailbox string) (WakeResult, error) {
	var out WakeResult

	if w.blobs == nil {
		return out, errors.New("sync: the write executor has no blob store; snooze wakes are not wired")
	}

	st, srcMb, err := w.target(ctx, accountID, messageID)
	if err != nil {
		return out, err
	}
	if !strings.EqualFold(srcMb.Name, SnoozeMailboxName) {
		return out, ErrNotSnoozed
	}

	target, err := w.originMailbox(ctx, accountID, originMailbox)
	if err != nil {
		return out, err
	}

	raw, err := w.rawBytes(ctx, accountID, messageID)
	if err != nil {
		return out, err
	}

	// The flags travel with the message, so a snoozed-after-reading message
	// does not come back unread (side effect 3 in the header). \Recent is not
	// settable and is not in this vocabulary anyway.
	flags := imapFlagsOf(st)

	// THE APPEND, with NOW as the internal date. This is the whole feature:
	// receivedAt becomes the wake time, so the message sorts to the top.
	appended, err := w.ApplyAppendAt(ctx, accountID, target.ID, raw, flags, time.Now())
	if err != nil {
		return out, fmt.Errorf("re-appending the woken message to %q: %w", target.Name, err)
	}

	// Only now is the sleeping copy removed. See "the failure ordering" in the
	// header: a failure here leaves a duplicate, never a lost message.
	if err := w.expungeFromSnoozed(ctx, accountID, srcMb, st); err != nil {
		w.log.Warn("the woken copy was delivered but the sleeping copy could not be removed",
			"account_id", accountID, "message_id", messageID, "error", err)
	}

	out.MessageID = appended.MessageID
	out.MailboxID = target.ID
	out.UID = appended.UID
	w.broker.Notify(accountID)
	return out, nil
}

// expungeFromSnoozed removes the sleeping copy, scoped to its one UID.
//
// It is the same Expunge primitive ApplyDestroy uses inside Trash (which the
// imap package documents as a UID EXPUNGE, RFC 4315 — so a concurrent client's
// \Deleted marks in the same folder are not swept along), applied to a
// narrower target: a single UID, in a folder this engine created, holding a
// message this engine put there, whose bytes it has ALREADY re-delivered to
// the inbox. There is no state in which this expunge can be the only copy.
func (w *WriteExecutor) expungeFromSnoozed(ctx context.Context, accountID int64, mb store.Mailbox, st store.MessageState) error {
	account, err := w.store.GetAccount(ctx, accountID)
	if err != nil {
		return fmt.Errorf("loading account %d: %w", accountID, err)
	}
	return w.withMailbox(ctx, account, mb.Name, func(c imap.Client, sel imap.SelectResult) error {
		if err := checkUIDValidity(sel, st); err != nil {
			return err
		}
		if err := c.Expunge(ctx, []imap.UID{uidFromDB(st.UID)}); err != nil {
			return fmt.Errorf("expunging the sleeping copy: %w", err)
		}
		if err := w.store.MarkDeleted(ctx, mb.ID, st.UIDValidity, []int64{st.UID}); err != nil {
			return fmt.Errorf("tombstoning the sleeping copy: %w", err)
		}
		return nil
	})
}

// RetireDraft implements submit.DraftRetirer: it destroys the draft a
// SCHEDULED submission held until its send moment (L3 epic E4).
//
// It goes through ApplyDestroy, which means the draft is MOVED TO TRASH rather
// than expunged (W-A2's rule: never an expunge from anywhere but Trash). That
// is the right behavior here and not merely the convenient one — a scheduled
// message the user spent effort composing should be recoverable for the 30
// days Trash retains it, exactly as a draft they deleted by hand would be.
//
// A draft that is already gone (the user deleted it while it was scheduled)
// answers ErrWriteNotFound, which is translated to nil: the postcondition
// "there is no leftover draft" already holds, and reporting a failure would
// put a warning in the log for a state that is correct.
func (w *WriteExecutor) RetireDraft(ctx context.Context, accountID, messageID int64) error {
	_, err := w.ApplyDestroy(ctx, accountID, messageID)
	if errors.Is(err, ErrWriteNotFound) {
		return nil
	}
	return err
}

// ownedMessage reads a message after checking the account owns it — the same
// no-oracle rule target() applies to the state row, applied to the immutable
// half.
func (w *WriteExecutor) ownedMessage(ctx context.Context, accountID, messageID int64) (store.Message, error) {
	msg, err := w.store.GetMessage(ctx, messageID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Message{}, ErrWriteNotFound
		}
		return store.Message{}, fmt.Errorf("loading message %d: %w", messageID, err)
	}
	if msg.AccountID != accountID {
		return store.Message{}, ErrWriteNotFound
	}
	return msg, nil
}

// originMailbox resolves a snooze row's origin to a live mailbox, falling back
// to the inbox.
func (w *WriteExecutor) originMailbox(ctx context.Context, accountID int64, name string) (store.Mailbox, error) {
	if name != "" {
		mb, err := w.store.GetMailboxByName(ctx, accountID, name)
		if err == nil && mb.Selectable {
			return mb, nil
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return store.Mailbox{}, fmt.Errorf("resolving the snooze origin %q: %w", name, err)
		}
		// The origin folder is gone (the user deleted it while the message
		// slept). INBOX is the fallback rather than an error: the canon's
		// default destination, and the alternative would strand the message in
		// Snoozed forever because of a folder it no longer needs.
		w.log.Warn("the snooze origin no longer exists; waking into the inbox instead",
			"account_id", accountID, "origin", name)
	}
	mb, err := w.store.GetMailboxByRole(ctx, accountID, store.RoleInbox)
	if err == nil {
		return mb, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.Mailbox{}, fmt.Errorf("resolving the inbox: %w", err)
	}
	// Some servers report no \Inbox SPECIAL-USE; the name is mandated by RFC
	// 3501 §5.1 to be case-insensitively "INBOX" on every server that exists.
	mb, err = w.store.GetMailboxByName(ctx, accountID, "INBOX")
	if err != nil {
		return store.Mailbox{}, fmt.Errorf("resolving the inbox: %w", err)
	}
	return mb, nil
}

// ensureSnoozeMailbox returns the account's Snoozed folder, creating it on
// first use.
//
// Creation goes through ApplyMailboxCreate, so the folder is made on Dovecot,
// subscribed, and reflected in the store by exactly the code path a user's own
// "new folder" click uses — there is no second folder-creation implementation
// to keep in step.
func (w *WriteExecutor) ensureSnoozeMailbox(ctx context.Context, accountID int64) (store.Mailbox, error) {
	mb, err := w.store.GetMailboxByName(ctx, accountID, SnoozeMailboxName)
	if err == nil {
		if !mb.Selectable {
			return store.Mailbox{}, fmt.Errorf("%w: %q exists but is not selectable",
				ErrSnoozeUnavailable, SnoozeMailboxName)
		}
		return mb, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.Mailbox{}, fmt.Errorf("looking up %q: %w", SnoozeMailboxName, err)
	}

	res, cerr := w.ApplyMailboxCreate(ctx, accountID, SnoozeMailboxName, true)
	if cerr != nil {
		if errors.Is(cerr, ErrMailboxNameTaken) {
			// Another client (or our own discovery) created it between the
			// lookup and the CREATE. Re-read rather than fail: the folder the
			// caller needs now exists.
			if mb, rerr := w.store.GetMailboxByName(ctx, accountID, SnoozeMailboxName); rerr == nil {
				return mb, nil
			}
		}
		return store.Mailbox{}, fmt.Errorf("%w: %w", ErrSnoozeUnavailable, cerr)
	}
	mb, err = w.store.GetMailbox(ctx, res.MailboxID)
	if err != nil {
		return store.Mailbox{}, fmt.Errorf("%w: %w", ErrSnoozeUnavailable, err)
	}
	return mb, nil
}

// rawBytes reads a message's raw RFC 5322 bytes out of the blob store.
//
// The blob is content-addressed by the sha256 on the message row, so this is
// the same bytes Dovecot delivered — which is what makes the wake's re-append
// byte-identical to the original rather than a re-serialization of our parse.
func (w *WriteExecutor) rawBytes(ctx context.Context, accountID, messageID int64) ([]byte, error) {
	msg, err := w.ownedMessage(ctx, accountID, messageID)
	if err != nil {
		return nil, err
	}
	h, err := blob.HashFromBytes(msg.RawSHA256)
	if err != nil {
		return nil, fmt.Errorf("reading the raw message %d: %w", messageID, err)
	}
	rc, err := w.blobs.Open(h)
	if err != nil {
		return nil, fmt.Errorf("reading the raw message %d: %w", messageID, err)
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("reading the raw message %d: %w", messageID, err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("reading the raw message %d: the blob is empty", messageID)
	}
	return raw, nil
}

// imapFlagsOf renders a stored state as the imap package's normalized flag
// vocabulary, so a re-append preserves what the user already did to the
// message.
func imapFlagsOf(st store.MessageState) []string {
	var flags []string
	if st.Flags&store.FlagSeen != 0 {
		flags = append(flags, "seen")
	}
	if st.Flags&store.FlagAnswered != 0 {
		flags = append(flags, "answered")
	}
	if st.Flags&store.FlagFlagged != 0 {
		flags = append(flags, "flagged")
	}
	if st.Flags&store.FlagDraft != 0 {
		flags = append(flags, "draft")
	}
	flags = append(flags, st.Keywords...)
	return flags
}
