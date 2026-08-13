package sync

import (
	"context"
	"strings"

	"github.com/GrupoNU/moov/internal/imap"
)

// W1's extensions to the fake server: the write primitives Move and Expunge,
// and the flag-delta semantics StoreFlags gained. Same rationale as the E5/E6
// extensions above them — the properties under test (Dovecot-first ordering,
// conflict refusal, echo convergence) are ordering properties, and only a
// deterministic server makes them exact.

// applyDelta applies one STORE operation to one message, returning whether
// anything observably changed. The modseq bumps ONLY on a real change,
// matching Dovecot: a no-op +FLAGS leaves the modseq alone, which is the
// behavior the executor's echo and replay reasoning depend on.
// The caller holds the server lock.
func (m *fakeMailbox) applyDelta(msg *fakeMessage, delta imap.FlagDelta) bool {
	system, keywords := splitFakeFlags(delta.Flags)

	var newFlags, newKeywords []string
	switch delta.Op {
	case imap.FlagsSet:
		newFlags, newKeywords = system, keywords
	case imap.FlagsAdd:
		newFlags = addNames(msg.flags, system)
		newKeywords = addNames(msg.keywords, keywords)
	case imap.FlagsRemove:
		newFlags = removeNames(msg.flags, system)
		newKeywords = removeNames(msg.keywords, keywords)
	default:
		return false
	}

	if sameNames(msg.flags, newFlags) && sameNames(msg.keywords, newKeywords) {
		return false
	}
	msg.flags, msg.keywords = newFlags, newKeywords
	msg.modSeq = m.nextModSeq()
	return true
}

// splitFakeFlags mirrors internal/imap's normalization (convert.go
// flagToGoIMAP/splitFlags): a known system flag — with or without its
// backslash — travels lowercase and bare, everything else is a user keyword,
// including backslash-prefixed flags this engine does not model (\Junk),
// which the real client keeps verbatim as keywords.
func splitFakeFlags(names []string) (system, keywords []string) {
	for _, n := range names {
		if bare := strings.TrimPrefix(n, `\`); isSystemName(bare) {
			system = append(system, strings.ToLower(bare))
			continue
		}
		keywords = append(keywords, n)
	}
	return system, keywords
}

func isSystemName(n string) bool {
	switch strings.ToLower(n) {
	case "seen", "answered", "flagged", "deleted", "draft":
		return true
	}
	return false
}

func addNames(have, add []string) []string {
	out := append([]string(nil), have...)
	for _, n := range add {
		if !containsFold(out, n) {
			out = append(out, n)
		}
	}
	return out
}

func removeNames(have, remove []string) []string {
	var out []string
	for _, n := range have {
		if !containsFold(remove, n) {
			out = append(out, n)
		}
	}
	return out
}

// containsFold matches flag names the way IMAP does: case-insensitively
// (RFC 3501 §2.3.2).
func containsFold(list []string, name string) bool {
	for _, n := range list {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

// sameNames compares two flag lists as case-insensitive sets.
func sameNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, n := range a {
		if !containsFold(b, n) {
			return false
		}
	}
	return true
}

// Move implements imap.Client for the fake: the destination copy gets a new
// UID and a fresh modseq, the source copy leaves through the vanished trail
// (a MOVE is an expunge from the source's point of view — RFC 6851 §3.3),
// and both mailboxes notify, which is the double echo the executor must
// converge under.
func (c *fakeClient) Move(_ context.Context, uids []imap.UID, dest string) (imap.MoveResult, error) {
	c.srv.mu.Lock()

	var out imap.MoveResult
	if err := c.srv.moveErr; err != nil {
		c.srv.mu.Unlock()
		return out, err
	}
	if c.selected == nil {
		c.srv.mu.Unlock()
		return out, imap.ErrNoMailboxSelected
	}
	target := c.srv.mailbox(dest)
	if target == nil || target.noSelect {
		c.srv.mu.Unlock()
		return out, imap.ErrNoMailboxSelected
	}

	mapping := map[imap.UID]imap.UID{}
	moved := false
	for _, u := range uids {
		msg := c.selected.find(u)
		if msg == nil {
			continue
		}
		newUID := target.uidNext()
		target.messages = append(target.messages, fakeMessage{
			uid:          newUID,
			raw:          msg.raw,
			flags:        append([]string(nil), msg.flags...),
			keywords:     append([]string(nil), msg.keywords...),
			internalDate: msg.internalDate,
			modSeq:       target.nextModSeq(),
		})
		c.selected.expunge(u)
		mapping[u] = newUID
		moved = true
	}

	if moved {
		out.DestUIDValidity = target.uidValidity
		if !c.srv.noCopyUID {
			out.DestUIDs = mapping
		}
	}
	srcName, srcStatus := c.selected.name, c.selected.statusFor()
	dstStatus := target.statusFor()
	c.srv.mu.Unlock()

	if moved {
		c.srv.notify(srcName, srcStatus)
		c.srv.notify(dest, dstStatus)
	}
	return out, nil
}

// Expunge implements imap.Client for the fake: \Deleted + UID EXPUNGE
// collapsed into the removal plus the vanished-trail record the real
// sequence produces.
func (c *fakeClient) Expunge(_ context.Context, uids []imap.UID) error {
	c.srv.mu.Lock()

	if err := c.srv.expungeErr; err != nil {
		c.srv.mu.Unlock()
		return err
	}
	if c.selected == nil {
		c.srv.mu.Unlock()
		return imap.ErrNoMailboxSelected
	}

	removed := false
	for _, u := range uids {
		if c.selected.expunge(u) {
			removed = true
		}
	}
	name, status := c.selected.name, c.selected.statusFor()
	c.srv.mu.Unlock()

	if removed {
		c.srv.notify(name, status)
	}
	return nil
}
