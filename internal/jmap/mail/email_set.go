package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/GrupoNU/moov/internal/jmap"
)

// Email/set — RFC 8620 §5.3 over the RFC 8621 §4.6 mutable properties.
//
// # What is mutable, and why exactly that
//
// RFC 8621 §4.6: "Only the 'keywords' and 'mailboxIds' properties may be set
// by the client; all other properties are immutable" (create aside). W1
// implements precisely those two, each in both of §5.3's spellings — the
// whole-property form and the PatchObject form — because real clients use
// both (Bulwark patches single keywords; a move is usually a whole-set
// write).
//
// # The one-mailbox constraint (phase 2)
//
// A message lives in exactly one mailbox: Moov mirrors IMAP, where a message
// IS a (mailbox, UID) pair, and the A6 label model puts Gmail-style multi
// membership on keywords, not on folders (ADR-001 mapping decisions; the
// session advertises maxMailboxesPerEmail accordingly). An update whose net
// mailboxIds is not exactly one mailbox is therefore notUpdated with
// invalidProperties and a description that says so — never a silent partial
// application.
//
// # Per-record granularity
//
// One bad id never fails the batch: every update and destroy answers for
// itself through notUpdated/notDestroyed (§5.3), and the state strings
// bracket the whole call — a client that runs set→changes with the returned
// oldState sees exactly the records this call touched.

// handleEmailSet implements Email/set.
func (d *Deps) handleEmailSet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseSet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}

	oldState, err := d.State.EmailState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading email state", err)
	}

	// §5.3: "If supplied, the string must match the current state; otherwise,
	// the method will be aborted and a stateMismatch error returned."
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, jmap.NewMethodError(jmap.CodeStateMismatch).
			WithDescription("ifInState does not match the current Email state; fetch changes and retry")
	}

	resp := &setResponse{AccountID: req.AccountID, OldState: oldState}

	// create: reserved for W3 (drafts + submission), refused loudly rather
	// than silently absent — a client that tries learns why, with the §3.6.2
	// serverUnavailable meaning ("temporarily unavailable") which is the
	// truthful reading of "the next epic implements this".
	for cid := range req.Create {
		if resp.NotCreated == nil {
			resp.NotCreated = map[string]setError{}
		}
		resp.NotCreated[cid] = setError{
			Type:        setErrServerUnavailable,
			Description: "Email/set create is not available yet; drafts and submission arrive with the outbox epic (W3)",
		}
	}

	destroySet := make(map[string]bool, len(req.Destroy))
	for _, id := range req.Destroy {
		destroySet[id] = true
	}

	// Updates, in sorted id order so a response is reproducible — the RFC
	// imposes no order and an unstable one would make golden tests flap.
	updateIDs := make([]string, 0, len(req.Update))
	for id := range req.Update {
		updateIDs = append(updateIDs, id)
	}
	sort.Strings(updateIDs)

	for _, wire := range updateIDs {
		if destroySet[wire] {
			// §5.3 willDestroy: "The client requested that an object be both
			// updated and destroyed in the same /set request, and the server
			// has decided to therefore ignore the update."
			d.noteNotUpdated(resp, wire, setError{Type: setErrWillDestroy,
				Description: "the same id is also in destroy; the update was ignored"})
			continue
		}
		if serr := d.applyEmailUpdate(ctx, caller.AccountID, wire, req.Update[wire]); serr != nil {
			d.noteNotUpdated(resp, wire, *serr)
			continue
		}
		if resp.Updated == nil {
			resp.Updated = map[string]any{}
		}
		// null: the server changed nothing beyond what the client asked for
		// (§5.3: "the value of each entry in the map is... null if the server
		// made no changes... in addition to those explicitly requested").
		resp.Updated[wire] = nil
	}

	// Destroys, in request order, deduplicated: a repeated id must produce
	// one outcome, not one success and one notFound.
	seen := make(map[string]bool, len(req.Destroy))
	for _, wire := range req.Destroy {
		if seen[wire] {
			continue
		}
		seen[wire] = true
		if serr := d.applyEmailDestroy(ctx, caller.AccountID, wire); serr != nil {
			if resp.NotDestroyed == nil {
				resp.NotDestroyed = map[string]setError{}
			}
			resp.NotDestroyed[wire] = *serr
			continue
		}
		resp.Destroyed = append(resp.Destroyed, wire)
	}

	// newState AFTER every write: set→changes with (oldState, newState) must
	// bracket exactly this call's effects, which the executor guarantees by
	// reflecting each write into message_state before returning.
	newState, err := d.State.EmailState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading email state", err)
	}
	resp.NewState = newState
	return resp, nil
}

// noteNotUpdated records a per-record update failure.
func (d *Deps) noteNotUpdated(resp *setResponse, wire string, serr setError) {
	if resp.NotUpdated == nil {
		resp.NotUpdated = map[string]setError{}
	}
	resp.NotUpdated[wire] = serr
}

// applyEmailDestroy destroys one id, per W-A2 semantics inside the writer.
func (d *Deps) applyEmailDestroy(ctx context.Context, accountID int64, wire string) *setError {
	msgID, err := DecodeEmailID(wire)
	if err != nil {
		// An id this server never issued names nothing (§5.3 notFound).
		return &setError{Type: setErrNotFound}
	}
	return writeSetError(d.Writer.Destroy(ctx, accountID, msgID), "destroy")
}

// applyEmailUpdate applies one update object to one message.
func (d *Deps) applyEmailUpdate(ctx context.Context, accountID int64, wire string, raw json.RawMessage) *setError {
	msgID, err := DecodeEmailID(wire)
	if err != nil {
		return &setError{Type: setErrNotFound}
	}

	var patch map[string]json.RawMessage
	if err := json.Unmarshal(raw, &patch); err != nil || patch == nil {
		return &setError{Type: setErrInvalidProperties,
			Description: "an update must be a PatchObject (RFC 8620 §5.3)"}
	}

	upd, serr := interpretEmailPatch(patch)
	if serr != nil {
		return serr
	}

	// The current row: existence (notFound before any write) and the current
	// mailbox, which a mailboxIds PATCH is evaluated against. The writer
	// re-checks ownership under its own lock; this read only shapes the
	// request.
	rows, err := d.Emails.EmailsByID(ctx, accountID, []int64{msgID})
	if err != nil {
		return &setError{Type: setErrServerFail, Description: "reading the message failed"}
	}
	if len(rows) == 0 || len(rows[0].MailboxIDs) == 0 {
		return &setError{Type: setErrNotFound}
	}
	row := rows[0]

	targetMailbox, serr := upd.resolveMailbox(row.MailboxIDs)
	if serr != nil {
		return serr
	}

	// Keywords first, then the move. The order is chosen so the validation
	// above has already rejected everything rejectable: what remains can only
	// fail at the server, and a keywords-then-move split leaves the smaller
	// inconsistency window (flags survive a MOVE verbatim; a move-then-flags
	// order would race the destination's watcher echo).
	appliedKeywords := false
	if upd.touchesKeywords() {
		// The durable-keyword ceiling (A6 / validation V1) is checked HERE,
		// before the write, because it is the only place it can be checked at
		// all — see checkKeywordCeiling.
		//
		// The mailbox counted is the one the message is in NOW, not the move
		// target, and that is correct: the keywords are written by a STORE
		// against the current folder, so they occupy a slot in ITS
		// dovecot-keywords. A subsequent move carries them into the
		// destination, which may then be over its own ceiling — an outcome no
		// pre-check can prevent without refusing legitimate moves, and one the
		// destination's next keyword write will report honestly.
		if serr := d.checkKeywordCeiling(ctx, accountID, row.MailboxIDs[0], upd); serr != nil {
			return serr
		}
		if err := d.Writer.SetFlags(ctx, accountID, msgID, upd.flagsChange()); err != nil {
			return writeSetError(err, "keywords")
		}
		appliedKeywords = true
	}

	if targetMailbox != 0 && targetMailbox != row.MailboxIDs[0] {
		if err := d.Writer.Move(ctx, accountID, msgID, targetMailbox); err != nil {
			serr := writeSetError(err, "mailboxIds")
			if appliedKeywords {
				// §5.3 wants record updates atomic; two IMAP commands cannot
				// be. The honest answer is a loud partial-failure note — the
				// client re-reads and sees exactly what state the message is
				// in — never a silent half.
				serr.Description = "the keywords change was applied but the move failed: " + serr.Description
			}
			return serr
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// the durable keyword ceiling (A6 / validation V1) — a W2 acceptance criterion
// ---------------------------------------------------------------------------

// checkKeywordCeiling refuses a keyword write that would push the mailbox past
// the 26 distinct keywords a Maildir folder holds durably.
//
// # Why the check lives here and not at the server
//
// Dovecot does not enforce this and cannot report it. Validation V1 put 500
// keywords on one message: Dovecot accepted every one, persisted them in its
// index, and served all 500 back on the next FETCH. On disk, after a
// force-resync, only 26 remained — `dovecot-keywords` encodes each keyword as
// one letter a-z in the Maildir filename and stops at index 25. So keywords
// past the 26th live only in a warm in-memory index and vanish, silently and
// all at once, the next time it is rebuilt: possibly weeks later, with no
// error anywhere.
//
// That is also why W1's read-back verification cannot catch it — at the moment
// of the read-back the keyword really is there. The engine has to count the
// folder's keywords itself and refuse the 27th, which is what L2 §2.3 means by
// "no labels that exist only in the DB, silently".
//
// # What counts against the budget
//
// Every DISTINCT case-folded keyword name in the folder, because that is what
// dovecot-keywords allocates a letter to. The four system flags of RFC 8621
// §4.1.1 ($seen, $flagged, $answered, $draft) do NOT: they are IMAP system
// flags, stored in the Maildir filename's own flag field, not in the keyword
// registry — which is why imapNameForKeyword translates them to bare flag
// names before they ever reach the writer. A $forwarded or a NonJunk from
// another client DOES count, and the error says so, because those really do
// occupy a slot.
func (d *Deps) checkKeywordCeiling(ctx context.Context, accountID, mailboxID int64, upd *emailUpdate) *setError {
	// The keywords this update would ADD to the folder, in the writer's
	// vocabulary. A full-set replace adds everything in the set (whatever it
	// removes from THIS message may still be on others, so a replace can only
	// grow the folder's distinct set, never shrink it within one call).
	var wanted []string
	if upd.kwReplace {
		wanted = upd.kwSet
	} else {
		wanted = upd.kwAdd
	}

	candidates := make([]string, 0, len(wanted))
	for _, name := range wanted {
		if isSystemFlagName(name) {
			continue
		}
		candidates = append(candidates, name)
	}
	if len(candidates) == 0 {
		// Nothing that occupies a Maildir keyword slot. Skipping the read is
		// not just an optimization: marking a message read is the single most
		// frequent write this server takes, and it must not cost a query.
		return nil
	}

	budget, err := d.Writer.KeywordBudget(ctx, accountID, mailboxID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return &setError{Type: setErrNotFound}
		}
		return &setError{Type: setErrServerFail,
			Description: "checking the mailbox's keyword budget failed"}
	}
	if budget.Limit <= 0 {
		// A writer that does not implement the budget (a test double, a future
		// backend on a store without the Maildir constraint) must not silently
		// block every keyword write.
		return nil
	}

	// The NEW names only: a keyword already in the folder occupies a slot it
	// already has, so re-applying it to another message is always free. This
	// is what makes "tag 500 messages with an existing label" cost nothing.
	seen := make(map[string]bool, len(candidates))
	var fresh []string
	for _, name := range candidates {
		key := strings.ToLower(name)
		if seen[key] || budget.Has(name) {
			continue
		}
		seen[key] = true
		fresh = append(fresh, name)
	}
	if len(fresh) == 0 {
		return nil
	}

	remaining := budget.Remaining()
	if len(fresh) <= remaining {
		return nil
	}

	sort.Strings(fresh)
	// The error is deliberately verbose. It is the one refusal in this server
	// that a user cannot possibly guess the reason for — the folder looks
	// empty of labels, the server accepted 25 of them, and the 26th is refused
	// by a rule that lives in a filename format from 1998. Naming the format,
	// the number and the remaining slots is what makes it actionable instead
	// of mysterious.
	return &setError{
		Type:       setErrInvalidProperties,
		Properties: []string{"keywords"},
		Description: fmt.Sprintf(
			"this mailbox is at the Maildir durable-keyword ceiling: a folder stores at most %d distinct "+
				"keywords permanently (dovecot-keywords encodes each as one letter a-z in the message "+
				"filename), %d are already in use here and %d slot(s) remain, but this change needs %d new "+
				"one(s): %s. Dovecot would ACCEPT this write and lose those keywords silently the next time "+
				"its index is rebuilt, so Moov refuses it instead. Standard keywords other clients set "+
				"($forwarded, $mdnsent, NonJunk) consume from the same budget; the four system keywords "+
				"($seen, $flagged, $answered, $draft) do not. Reuse an existing keyword, or remove one from "+
				"every message in this mailbox to free a slot.",
			budget.Limit, len(budget.InUse), remaining, len(fresh), strings.Join(fresh, ", ")),
	}
}

// isSystemFlagName reports the bare IMAP system-flag names imapNameForKeyword
// produces for the four RFC 8621 §4.1.1 system keywords.
//
// They are excluded from the keyword budget because Maildir stores them in the
// filename's flag field ("S" for \Seen, "F" for \Flagged, …), not in
// dovecot-keywords — so they occupy no letter of the a-z registry.
func isSystemFlagName(name string) bool {
	switch strings.ToLower(name) {
	case "seen", "answered", "flagged", "draft", "deleted":
		return true
	}
	return false
}

// writeSetError maps an EmailWriter error onto the §5.3 SetError vocabulary
// (see set.go for the stateMismatch judgment call). Internal error text stays
// out of the wire for the same reason serverFail() keeps it out.
func writeSetError(err error, what string) *setError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound):
		return &setError{Type: setErrNotFound}
	case errors.Is(err, ErrWriteConflict):
		return &setError{Type: setErrStateMismatch,
			Description: "the message changed on the server after the state this change was based on; re-fetch and retry"}
	case errors.Is(err, ErrNoTrash):
		return &setError{Type: setErrServerFail,
			Description: "destroy maps to a move into the Trash mailbox (W-A2), and this account has none"}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &setError{Type: setErrServerUnavailable,
			Description: "applying " + what + ": request canceled or timed out; retry"}
	default:
		return &setError{Type: setErrServerFail, Description: "applying " + what + " failed"}
	}
}

// ---------------------------------------------------------------------------
// PatchObject interpretation
// ---------------------------------------------------------------------------

// emailUpdate is one update object after interpretation: what to do to
// keywords, and what to do to mailbox membership.
type emailUpdate struct {
	kwReplace           bool
	kwSet               []string // translated to the writer's flag vocabulary
	kwAdd, kwRemove     []string
	mbReplace           bool
	mbSet               []int64
	mbAdd, mbRemove     []int64
	sawKeywords, sawMbs bool
}

func (u *emailUpdate) touchesKeywords() bool {
	return u.kwReplace || len(u.kwAdd) > 0 || len(u.kwRemove) > 0
}

func (u *emailUpdate) flagsChange() FlagsChange {
	return FlagsChange{Replace: u.kwReplace, Flags: u.kwSet, Add: u.kwAdd, Remove: u.kwRemove}
}

// resolveMailbox evaluates the mailboxIds half against the message's current
// membership and returns the single mailbox the message must end up in, or 0
// when the update does not touch membership.
func (u *emailUpdate) resolveMailbox(current []int64) (int64, *setError) {
	if !u.sawMbs {
		return 0, nil
	}

	final := map[int64]bool{}
	if u.mbReplace {
		for _, id := range u.mbSet {
			final[id] = true
		}
	} else {
		for _, id := range current {
			final[id] = true
		}
		for _, id := range u.mbAdd {
			final[id] = true
		}
		for _, id := range u.mbRemove {
			delete(final, id)
		}
	}

	switch len(final) {
	case 1:
		for id := range final {
			return id, nil
		}
		panic("unreachable")
	case 0:
		return 0, &setError{Type: setErrInvalidProperties, Properties: []string{"mailboxIds"},
			Description: "a message must belong to exactly one mailbox; to remove it entirely, use destroy"}
	default:
		return 0, &setError{Type: setErrInvalidProperties, Properties: []string{"mailboxIds"},
			Description: "a message lives in exactly one mailbox in this phase (IMAP folder semantics, ADR-001 mapping); " +
				"labels are keywords, not extra mailboxes"}
	}
}

// interpretEmailPatch parses one §5.3 PatchObject into an emailUpdate,
// enforcing the grammar strictly enough that nothing invalid reaches IMAP.
func interpretEmailPatch(patch map[string]json.RawMessage) (*emailUpdate, *setError) {
	upd := &emailUpdate{}
	var badProps []string

	for key, raw := range patch {
		property, sub, hasSub, ok := splitPatchPointer(key)
		if !ok {
			// A pointer deeper than the properties allow (§5.3 invalidPatch:
			// "The patch could not be applied... a path that does not exist").
			return nil, &setError{Type: setErrInvalidPatch,
				Description: "the pointer " + key + " is deeper than any settable Email property"}
		}

		switch property {
		case "keywords":
			if !hasSub {
				// §5.3 prefix rule: "There MUST NOT be two patches in the
				// PatchObject where the pointer of one is the prefix of the
				// pointer of the other".
				if upd.sawKeywords {
					return nil, invalidPrefixPatch("keywords")
				}
				upd.sawKeywords, upd.kwReplace = true, true
				set, serr := parseKeywordSet(raw)
				if serr != nil {
					return nil, serr
				}
				upd.kwSet = set
			} else {
				if upd.kwReplace {
					return nil, invalidPrefixPatch("keywords")
				}
				upd.sawKeywords = true
				if !validKeyword(sub) {
					badProps = append(badProps, key)
					continue
				}
				add, remove, valid := boolPatchValue(raw)
				if !valid {
					badProps = append(badProps, key)
					continue
				}
				name := imapNameForKeyword(sub)
				if add {
					upd.kwAdd = append(upd.kwAdd, name)
				} else if remove {
					upd.kwRemove = append(upd.kwRemove, name)
				}
			}

		case "mailboxIds":
			if !hasSub {
				if upd.sawMbs {
					return nil, invalidPrefixPatch("mailboxIds")
				}
				upd.sawMbs, upd.mbReplace = true, true
				ids, serr := parseMailboxIDSet(raw)
				if serr != nil {
					return nil, serr
				}
				upd.mbSet = ids
			} else {
				if upd.mbReplace {
					return nil, invalidPrefixPatch("mailboxIds")
				}
				upd.sawMbs = true
				id, err := DecodeMailboxID(sub)
				if err != nil {
					badProps = append(badProps, key)
					continue
				}
				add, remove, valid := boolPatchValue(raw)
				if !valid {
					badProps = append(badProps, key)
					continue
				}
				if add {
					upd.mbAdd = append(upd.mbAdd, id)
				} else if remove {
					upd.mbRemove = append(upd.mbRemove, id)
				}
			}

		default:
			// RFC 8621 §4.6: everything except keywords and mailboxIds is
			// immutable; §5.3 answers invalidProperties naming the property.
			badProps = append(badProps, property)
		}
	}

	if len(badProps) > 0 {
		sort.Strings(badProps)
		return nil, &setError{Type: setErrInvalidProperties, Properties: badProps,
			Description: "only keywords and mailboxIds may be set on an Email (RFC 8621 §4.6)"}
	}
	return upd, nil
}

func invalidPrefixPatch(property string) *setError {
	return &setError{Type: setErrInvalidPatch,
		Description: "the update sets both " + property + " and a " + property +
			"/... patch; a pointer must not be the prefix of another (RFC 8620 §5.3)"}
}

// parseKeywordSet parses the full-set "keywords" value: a JSON object whose
// every value MUST be true (RFC 8621 §4.1.1), keys valid keywords.
func parseKeywordSet(raw json.RawMessage) ([]string, *setError) {
	var set map[string]json.RawMessage
	if err := json.Unmarshal(raw, &set); err != nil || set == nil {
		return nil, &setError{Type: setErrInvalidProperties, Properties: []string{"keywords"},
			Description: "keywords must be an object of keyword: true entries (RFC 8621 §4.1.1)"}
	}
	out := make([]string, 0, len(set))
	var bad []string
	for k, v := range set {
		add, _, valid := boolPatchValue(v)
		if !valid || !add || !validKeyword(k) {
			bad = append(bad, "keywords/"+k)
			continue
		}
		out = append(out, imapNameForKeyword(k))
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return nil, &setError{Type: setErrInvalidProperties, Properties: bad,
			Description: "every keyword value must be true and every keyword must satisfy the RFC 8621 §4.1.1 grammar"}
	}
	sort.Strings(out)
	return out, nil
}

// parseMailboxIDSet parses the full-set "mailboxIds" value.
func parseMailboxIDSet(raw json.RawMessage) ([]int64, *setError) {
	var set map[string]json.RawMessage
	if err := json.Unmarshal(raw, &set); err != nil || set == nil {
		return nil, &setError{Type: setErrInvalidProperties, Properties: []string{"mailboxIds"},
			Description: "mailboxIds must be an object of mailboxId: true entries (RFC 8621 §4.1.1)"}
	}
	out := make([]int64, 0, len(set))
	seen := map[int64]bool{}
	var bad []string
	for k, v := range set {
		add, _, valid := boolPatchValue(v)
		id, err := DecodeMailboxID(k)
		if !valid || !add || err != nil {
			bad = append(bad, "mailboxIds/"+k)
			continue
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return nil, &setError{Type: setErrInvalidProperties, Properties: bad,
			Description: "every mailboxIds value must be true and every key must be a mailbox id this server issued"}
	}
	return out, nil
}
