package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/GrupoNU/moov/internal/store"
)

// Mute, in the engine (L3 epic E4, arbitration GC-10).
//
// ===========================================================================
// THE CANON
// ===========================================================================
//
// docs/research/06-gmail-canon.md §2.2, quoting support.google.com/mail/answer
// /16594169: replies to a muted conversation "skip your inbox and go directly
// to your archive", with THREE escape hatches, verbatim:
//
//	1. "The message is only sent to you."
//	2. "The message is sent to a Google Group you're in."
//	3. "Someone adds you to the 'To' or 'Cc' fields."
//
// GC-10 fixes the mechanism: the state is Moov-side with a DURABLE key (the
// thread's root Message-ID, migration 0009), and the EFFECT is executed in
// Dovecot — the engine archives the arriving reply — so every other IMAP
// client sees the same mailbox contents Moov does.
//
// ===========================================================================
// THE THREE HATCHES, AS IMPLEMENTED
// ===========================================================================
//
// # Hatch 1 — "The message is only sent to you"
//
// Implemented exactly. The message is delivered to the inbox (not archived)
// when the account's own address — or one of its identity addresses, which is
// how an alias reaches the same mailbox — is the ONLY recipient across To and
// Cc combined. Bcc is not consulted because a Bcc is not in the message a
// recipient receives (RFC 5322 §3.6.3: the Bcc field is removed on delivery),
// so a message that LOOKS sole-recipient may have been blind-copied to a crowd
// and we cannot know. Erring toward "let it through" is the right direction:
// the cost is one message in the inbox the user muted, and the cost of the
// other error is a personal reply to the user disappearing into the archive.
//
// # Hatch 2 — "The message is sent to a Google Group you're in"
//
// NOT IMPLEMENTED, and unimplementable without a group directory. This is
// named, not silently dropped, because P4 of the plan forbids silent holes.
//
// Gmail's hatch works because Google Workspace knows the user's group
// memberships: it can see that `team@company.com` is a group and that this
// user is in it, and therefore that a message addressed to the group is
// addressed to them personally. Moov has no such directory. Dovecot knows one
// mailbox; Mailcow knows aliases, but a mailing list the user subscribed to
// externally (the actual common case — a project list, a vendor's
// announcements) is invisible to both.
//
// The three alternatives considered and rejected:
//
//   - Treat any List-Id message as group mail and let it through. That
//     INVERTS the feature: mailing-list threads are the single most common
//     thing a user mutes, and this would make mute do nothing for them.
//   - Ask the user to declare their groups. That is a settings surface with no
//     Gmail equivalent, for a hatch whose purpose is to be invisible.
//   - Infer membership from sent mail. A heuristic guess about whether a reply
//     should reach the inbox — the exact class of thing GC-8 puts behind the
//     AI consent toggle, and not something to smuggle in as plumbing.
//
// So the hatch is absent, and the consequence is precise and bounded: mail
// arriving on a muted thread via a list the user belongs to IS archived, where
// Gmail would have let it through. Hatch 3 still catches the case that matters
// most — someone naming the user explicitly — which is the one Gmail's own
// help text describes as "someone adds you", and which is how a person
// actually pulls you back into a conversation.
//
// # Hatch 3 — "Someone adds you to the 'To' or 'Cc' fields"
//
// Implemented as it reads: the account's address (or an identity address)
// appearing anywhere in To or Cc means the message is NOT archived.
//
// Note that hatch 3 SUBSUMES hatch 1 as written — a message sent only to you
// necessarily has you in To. Both are implemented anyway, separately, because
// the canon lists them separately and because they will diverge if hatch 1
// ever grows the "and nobody else" reading a stricter product decision might
// want. Keeping them apart costs one boolean and makes the code readable
// against the source it cites.

// MuteDecision is why a message on a muted thread was or was not archived.
type MuteDecision struct {
	// Archive is the outcome: true means the engine should move it out of the
	// inbox.
	Archive bool

	// Reason names the hatch that let it through, for the log line. Empty when
	// Archive is true.
	Reason string
}

// The hatch names, as they appear in logs and tests.
const (
	// MuteHatchSoleRecipient is hatch 1.
	MuteHatchSoleRecipient = "sole-recipient"
	// MuteHatchExplicitRecipient is hatch 3.
	MuteHatchExplicitRecipient = "explicit-to-or-cc"
	// MuteHatchGroup is hatch 2 — NOT implemented (see the file header). The
	// constant exists so the gap has a name in the code, as P4 requires, and
	// so a future implementation has an obvious place to attach.
	MuteHatchGroup = "google-group-membership-unavailable"
)

// evaluateMuteHatches decides whether an arriving message on a muted thread
// should be archived.
//
// addresses is the message's stored address JSON (store.Message.Addresses);
// own is the set of addresses that reach this account, lowercased.
func evaluateMuteHatches(addresses []byte, own map[string]bool) MuteDecision {
	to := addressEmails(addresses, "to")
	cc := addressEmails(addresses, "cc")

	// Hatch 3 first, because it is the broader test and a message that passes
	// it needs no further examination.
	explicit := false
	for _, addr := range append(append([]string{}, to...), cc...) {
		if own[strings.ToLower(addr)] {
			explicit = true
			break
		}
	}
	if !explicit {
		return MuteDecision{Archive: true}
	}

	// Hatch 1: sole recipient. Reported separately when it holds, because the
	// canon lists it separately and the log line is more useful for it.
	if len(to)+len(cc) == 1 {
		return MuteDecision{Reason: MuteHatchSoleRecipient}
	}
	return MuteDecision{Reason: MuteHatchExplicitRecipient}
}

// addressEmails pulls one header's addresses out of the stored JSON.
//
// The shape is the one the parser writes and the JMAP adapter reads:
// {"to":[{"name":...,"email":...}], ...}. A malformed or absent field yields
// no addresses, which makes the hatch evaluation fall through to "archive" —
// the conservative direction for a MUTED thread, since the user asked for
// silence and an unparseable recipient list is not evidence they were named.
func addressEmails(raw []byte, field string) []string {
	if len(raw) == 0 {
		return nil
	}
	var doc map[string][]struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	list := doc[field]
	out := make([]string, 0, len(list))
	for _, a := range list {
		if a.Email != "" {
			out = append(out, a.Email)
		}
	}
	return out
}

// MuteArchiver applies the mute effect: it archives inbox messages that joined
// a muted conversation.
//
// It runs from the sync pipeline's commit step (pipeline.go), right after
// threading — which is the earliest moment the answer is knowable, because
// "which thread did this join" is what threading decides.
type MuteArchiver struct {
	store *store.Store
	exec  *WriteExecutor

	// Observer, when set, counts applied mutes (E8-lite).
	Observer MuteObserver
}

// MuteObserver counts mute archivals. Same seam shape as SnoozeObserver.
type MuteObserver interface {
	MuteApplied()
}

// NewMuteArchiver builds the archiver. exec may be nil, in which case the
// archiver is inert — which is what a read-only deployment (no write executor)
// gets, and what keeps every existing Syncer construction valid.
func NewMuteArchiver(st *store.Store, exec *WriteExecutor) *MuteArchiver {
	if st == nil {
		return nil
	}
	return &MuteArchiver{store: st, exec: exec}
}

// Apply archives the messages of this batch that landed in the INBOX on a
// muted thread and that no escape hatch saved.
//
// # Why it archives AFTER the message is stored rather than before it lands
//
// There is no "before": Dovecot delivered the message, our watcher noticed,
// and the pipeline has already downloaded and stored it. Archiving is
// therefore a MOVE of a message that is briefly in the inbox — for the
// hundreds of milliseconds between delivery and this call.
//
// The alternative — a Sieve rule that files muted threads at delivery time —
// would be genuinely better (the message never touches the inbox) and is not
// available: Sieve cannot evaluate "is this thread muted" without the thread
// graph, which lives here. E6 brings ManageSieve for the rules Sieve CAN
// express; mute is not one of them. The brief window is documented rather than
// hidden, and it is the same window every other IMAP client's own filters have.
//
// # Failure policy
//
// Per message, and never fatal. A mute that could not be applied leaves the
// reply in the inbox — visible, and the state the user would have had without
// the feature — which is strictly better than failing a sync batch that has
// already committed the mail.
func (m *MuteArchiver) Apply(ctx context.Context, accountID int64, mailbox store.Mailbox, messageIDs []int64, threadIDs []int64) {
	if m == nil || m.exec == nil || len(messageIDs) == 0 {
		return
	}
	// Only the inbox. A reply that Dovecot filed elsewhere (a user's own Sieve
	// rule, a shared folder) is already not in the inbox, and moving it would
	// be the engine overriding a decision somebody else made.
	if mailbox.Role != store.RoleInbox && !strings.EqualFold(mailbox.Name, "INBOX") {
		return
	}
	if len(messageIDs) != len(threadIDs) {
		return
	}

	muted, err := m.mutedAmong(ctx, accountID, threadIDs)
	if err != nil {
		m.exec.log.Warn("could not check mutes for a batch; nothing was archived",
			"account_id", accountID, "error", err)
		return
	}
	if len(muted) == 0 {
		return
	}

	own, err := m.ownAddresses(ctx, accountID)
	if err != nil {
		m.exec.log.Warn("could not resolve the account's own addresses; mute hatches cannot be evaluated",
			"account_id", accountID, "error", err)
		return
	}

	archive, err := m.store.GetMailboxByRole(ctx, accountID, store.RoleArchive)
	if err != nil {
		// No Archive folder: the canon says muted replies "go directly to your
		// archive", and without one there is nowhere honest to put them.
		// Leaving them in the inbox is the visible failure.
		m.exec.log.Warn("a muted thread received mail but the account has no Archive mailbox",
			"account_id", accountID)
		return
	}

	for i, messageID := range messageIDs {
		if !muted[threadIDs[i]] {
			continue
		}
		msg, err := m.store.GetMessage(ctx, messageID)
		if err != nil {
			continue
		}
		decision := evaluateMuteHatches(msg.Addresses, own)
		if !decision.Archive {
			m.exec.log.Info("a muted thread received mail that an escape hatch let through",
				"account_id", accountID, "message_id", messageID, "hatch", decision.Reason)
			continue
		}
		if _, err := m.exec.ApplyMove(ctx, accountID, messageID, archive.ID); err != nil {
			m.exec.log.Warn("archiving a muted thread's reply failed; it stays in the inbox",
				"account_id", accountID, "message_id", messageID, "error", err)
			continue
		}
		m.exec.log.Info("a muted thread's reply was archived",
			"account_id", accountID, "message_id", messageID, "thread_id", threadIDs[i])
		if m.Observer != nil {
			m.Observer.MuteApplied()
		}
	}
}

// mutedAmong returns the subset of the given volatile thread ids whose
// conversations are muted, in one round trip.
func (m *MuteArchiver) mutedAmong(ctx context.Context, accountID int64, threadIDs []int64) (map[int64]bool, error) {
	seen := map[int64]bool{}
	unique := make([]int64, 0, len(threadIDs))
	for _, id := range threadIDs {
		if id != 0 && !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	if len(unique) == 0 {
		return nil, nil
	}
	rows, err := m.store.ThreadRowsByThreadIDs(ctx, accountID, unique)
	if err != nil {
		return nil, err
	}
	rowIDs := make([]int64, 0, len(rows))
	byRow := make(map[int64]int64, len(rows))
	for threadID, t := range rows {
		rowIDs = append(rowIDs, t.ID)
		byRow[t.ID] = threadID
	}
	mutedRows, err := m.store.MutedThreadRows(ctx, accountID, rowIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]bool, len(mutedRows))
	for rowID := range mutedRows {
		out[byRow[rowID]] = true
	}
	return out, nil
}

// ownAddresses is every address that reaches this account: the mailbox address
// plus every identity address (which is how an alias is expressed here — E7's
// Identity rows, migration 0006).
//
// Lowercased, because the local part of an address is technically
// case-sensitive (RFC 5321 §2.4) and treating it as such here would make
// `Diego@` fail a hatch that `diego@` passes — a distinction no real mail
// system honors and one that would make the feature look broken.
func (m *MuteArchiver) ownAddresses(ctx context.Context, accountID int64) (map[string]bool, error) {
	account, err := m.store.GetAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("loading account %d: %w", accountID, err)
	}
	own := map[string]bool{strings.ToLower(account.Email): true}

	identities, err := m.store.ListIdentities(ctx, accountID)
	if err != nil {
		// The mailbox address alone is a correct-but-narrower answer: an alias
		// would fail hatch 3 and its reply would be archived. Degrading rather
		// than refusing keeps mute working; the log says what was missing.
		if !errors.Is(err, store.ErrNotFound) {
			m.exec.log.Warn("could not read identities; mute hatches use the mailbox address only",
				"account_id", accountID, "error", err)
		}
		return own, nil
	}
	for _, id := range identities {
		if id.Email != "" {
			own[strings.ToLower(id.Email)] = true
		}
	}
	return own, nil
}
