package imap

import (
	"crypto/x509"
	"errors"
	"sort"
	"strings"

	goimap "github.com/emersion/go-imap/v2"
)

// This file is the only place where go-imap types are translated into Moov
// types and back. Keeping the conversion in one file rather than scattering it
// through the implementation is what makes an upstream API change a
// single-file diff.

// normalizeCap folds a capability name to the lowercase form Capabilities uses.
func normalizeCap(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// capsFromGoIMAP converts go-imap's capability set to Moov's.
func capsFromGoIMAP(set goimap.CapSet) Capabilities {
	out := make(Capabilities, len(set))
	for c := range set {
		out[normalizeCap(string(c))] = struct{}{}
	}
	return out
}

// sortedKeys returns a set's keys in sorted order, for stable logging.
func sortedKeys(set Capabilities) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// rootPoolFromPEM builds a certificate pool from PEM bytes.
func rootPoolFromPEM(pem []byte) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("imap: TLSRootCAsPEM contains no usable certificate")
	}
	return pool, nil
}

// roleFromAttrs maps SPECIAL-USE mailbox attributes (RFC 6154) to Moov roles.
//
// \Important (RFC 8457) is deliberately not mapped: it is a per-message hint,
// not a folder Moov syncs differently, and giving it a role would make the
// engine treat a virtual folder as a real one.
func roleFromAttrs(name string, attrs []goimap.MailboxAttr) MailboxRole {
	// A map rather than a switch: the SPECIAL-USE attributes are a small open
	// set, and most MailboxAttr values (\HasChildren, \Subscribed, \Noselect …)
	// carry no role at all. Enumerating only the ones that map to a role keeps
	// the intent visible instead of burying it among a dozen no-op cases.
	roles := map[goimap.MailboxAttr]MailboxRole{
		goimap.MailboxAttrAll:     RoleAll,
		goimap.MailboxAttrArchive: RoleArchive,
		goimap.MailboxAttrDrafts:  RoleDrafts,
		goimap.MailboxAttrFlagged: RoleFlagged,
		goimap.MailboxAttrJunk:    RoleJunk,
		goimap.MailboxAttrSent:    RoleSent,
		goimap.MailboxAttrTrash:   RoleTrash,
	}
	for _, a := range attrs {
		if role, ok := roles[a]; ok {
			return role
		}
	}
	// INBOX has no SPECIAL-USE attribute: RFC 3501 makes the name itself
	// reserved and case-insensitive, so the name is the role.
	if strings.EqualFold(name, "INBOX") {
		return RoleInbox
	}
	return RoleNone
}

// hasAttr reports whether the attribute list contains attr.
func hasAttr(attrs []goimap.MailboxAttr, attr goimap.MailboxAttr) bool {
	for _, a := range attrs {
		if a == attr {
			return true
		}
	}
	return false
}

// mailboxFromListData converts a LIST response, folding in the LIST-STATUS
// data when the server returned it in the same round trip.
func mailboxFromListData(d *goimap.ListData) MailboxInfo {
	info := MailboxInfo{
		Name:       d.Mailbox,
		Role:       roleFromAttrs(d.Mailbox, d.Attrs),
		Subscribed: hasAttr(d.Attrs, goimap.MailboxAttrSubscribed),
		NoSelect: hasAttr(d.Attrs, goimap.MailboxAttrNoSelect) ||
			hasAttr(d.Attrs, goimap.MailboxAttrNonExistent),
	}
	if d.Delim != 0 {
		info.Delimiter = string(d.Delim)
	}
	if d.Status != nil {
		applyStatus(&info, d.Status)
	}
	return info
}

// applyStatus folds a STATUS response into a MailboxInfo.
//
// go-imap uses pointers for the counters precisely because zero is a valid
// value the server may or may not have sent, so each one is checked rather
// than dereferenced blindly.
func applyStatus(info *MailboxInfo, s *goimap.StatusData) {
	info.HasStatus = true
	if s.NumMessages != nil {
		info.NumMessages = *s.NumMessages
	}
	if s.NumUnseen != nil {
		info.NumUnseen = *s.NumUnseen
	}
	if s.Size != nil {
		info.SizeBytes = *s.Size
	}
	info.UIDNext = UID(s.UIDNext)
	info.UIDValidity = s.UIDValidity
	info.HighestModSeq = ModSeq(s.HighestModSeq)
}

// systemFlags maps Moov's normalized flag names to the IMAP system flags.
//
// Moov stores flags without the backslash and in lowercase because that is
// what JMAP keywords look like ($seen, $flagged) and what ends up in the
// store; the backslash form is a wire detail that belongs on this side of the
// boundary.
var systemFlags = map[string]goimap.Flag{
	"seen":     goimap.FlagSeen,
	"answered": goimap.FlagAnswered,
	"flagged":  goimap.FlagFlagged,
	"deleted":  goimap.FlagDeleted,
	"draft":    goimap.FlagDraft,
}

// flagToGoIMAP converts one Moov flag name to its wire form. A name that is
// not a known system flag is passed through as a user keyword, which is how
// label keywords ($MoovL7) reach the server.
func flagToGoIMAP(name string) goimap.Flag {
	if f, ok := systemFlags[strings.ToLower(name)]; ok {
		return f
	}
	return goimap.Flag(name)
}

// flagsToGoIMAP converts a slice of Moov flag names.
func flagsToGoIMAP(names []string) []goimap.Flag {
	out := make([]goimap.Flag, 0, len(names))
	for _, n := range names {
		out = append(out, flagToGoIMAP(n))
	}
	return out
}

// splitFlags separates a message's wire flags into Moov's system flags and its
// user keywords.
//
// \Recent is dropped: RFC 3501 makes it session-scoped and non-persistent, so
// storing it would record a fact that is false by the time anything reads it.
func splitFlags(flags []goimap.Flag) (system, keywords []string) {
	for _, f := range flags {
		s := string(f)
		if !strings.HasPrefix(s, "\\") {
			keywords = append(keywords, s)
			continue
		}
		lower := strings.ToLower(strings.TrimPrefix(s, "\\"))
		if lower == "recent" {
			continue
		}
		if _, ok := systemFlags[lower]; ok {
			system = append(system, lower)
			continue
		}
		// An unknown system flag (\Junk on some servers, \Forwarded) is kept
		// verbatim as a keyword rather than dropped: it is real state another
		// client set, and losing it would make Moov's copy lossy.
		keywords = append(keywords, s)
	}
	return system, keywords
}

// uidSetFromUIDs builds a go-imap UID set from Moov UIDs.
func uidSetFromUIDs(uids []UID) goimap.UIDSet {
	var set goimap.UIDSet
	for _, u := range uids {
		set.AddNum(goimap.UID(u))
	}
	return set
}

// uidsFromUIDSet expands a go-imap UID set into a slice.
//
// The wire form is a set of ranges, so "1:4294967295" is four billion entries
// waiting to be allocated and "n:*" cannot be expanded at all. Both are the
// server's way of saying "everything", which the caller of this function
// (VANISHED handling) answers with a full resync rather than a list — so the
// expansion is capped and truncation is reported instead of hidden.
func uidsFromUIDSet(set goimap.UIDSet, limit int) (uids []UID, truncated bool) {
	// Nums reports ok=false for a dynamic set (one containing "*"), which is
	// unbounded by construction.
	nums, ok := set.Nums()
	if !ok {
		return nil, true
	}
	if len(nums) > limit {
		nums = nums[:limit]
		truncated = true
	}
	uids = make([]UID, len(nums))
	for i, n := range nums {
		uids[i] = UID(n)
	}
	return uids, truncated
}

// maxVanishedUIDs caps the expansion of a VANISHED set. A mailbox where more
// than this many messages vanished at once is better handled by a full resync
// than by a list, and the cap keeps a hostile or buggy server from making Moov
// allocate without bound.
const maxVanishedUIDs = 1 << 20

// eventStatusFromGoIMAP converts a NOTIFY-induced STATUS response.
func eventStatusFromGoIMAP(s *goimap.StatusData) EventStatus {
	var out EventStatus
	if s == nil {
		return out
	}
	if s.NumMessages != nil {
		out.NumMessages, out.HasNumMessages = *s.NumMessages, true
	}
	if s.NumUnseen != nil {
		out.NumUnseen, out.HasNumUnseen = *s.NumUnseen, true
	}
	if s.UIDNext != 0 {
		out.UIDNext, out.HasUIDNext = UID(s.UIDNext), true
	}
	if s.HighestModSeq != 0 {
		out.HighestModSeq, out.HasHighestModSeq = ModSeq(s.HighestModSeq), true
	}
	return out
}

// storeOpToGoIMAP converts a flag operation.
func storeOpToGoIMAP(op FlagOp) (goimap.StoreFlagsOp, error) {
	switch op {
	case FlagsAdd:
		return goimap.StoreFlagsAdd, nil
	case FlagsRemove:
		return goimap.StoreFlagsDel, nil
	case FlagsSet:
		return goimap.StoreFlagsSet, nil
	default:
		return 0, errors.New("imap: unknown flag operation")
	}
}
