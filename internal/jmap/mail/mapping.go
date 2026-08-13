package mail

import (
	"sort"
	"strings"

	"github.com/GrupoNU/moov/internal/store"
)

// Vocabulary mapping: store roles -> JMAP roles, IMAP flags -> JMAP keywords.
//
// Both mappings are written out as explicit tables rather than derived by
// string manipulation. The two vocabularies happen to coincide for most
// values, and a cast would work today — but they are versioned by different
// standards bodies, and the day one of them adds a value the cast silently
// invents a role or a keyword this server does not implement.

// jmapRoles maps the store's normalized SPECIAL-USE role onto the JMAP role
// name of RFC 8621 §2.
//
// RFC 8621 §2 defines role as "the IANA-registered name" from the "IMAP
// Mailbox Name Attributes" registry, "with the leading '\' removed and the
// name lowercased" — which is exactly the normalization internal/imap already
// applies (convert.go roleFromAttrs) and the store persists. So the mapping is
// an identity on paper; it is a table anyway, for the reason above, and
// because it is the single place to look when a client reports a folder with
// the wrong icon.
//
// This is the mapping spike S1 validated end to end against our real Mailcow
// (S1 §"Alcance validado" point 3: inbox, sent, drafts, trash, junk, archive
// all mapped correctly through jmap-perl), and the mapping our own store
// records — the two agreeing is what makes the parity check in the tests
// meaningful rather than circular.
var jmapRoles = map[string]string{
	"inbox":   "inbox",
	"archive": "archive",
	"drafts":  "drafts",
	"sent":    "sent",
	"junk":    "junk",
	"trash":   "trash",
	"all":     "all",
	"flagged": "flagged",
}

// jmapRole returns the JMAP role for a stored role, and whether there is one.
// An unmapped or empty role yields ok=false, which the Mailbox object renders
// as JSON null — the RFC 8621 §2 value for "no role".
func jmapRole(storeRole string) (string, bool) {
	if storeRole == "" {
		return "", false
	}
	r, ok := jmapRoles[strings.ToLower(storeRole)]
	return r, ok
}

// The JMAP keywords of RFC 8621 §4.1.1, which defines them by reference to the
// IMAP keyword registry (RFC 5788) and requires the "$" prefix, lowercase.
const (
	KeywordSeen      = "$seen"
	KeywordFlagged   = "$flagged"
	KeywordAnswered  = "$answered"
	KeywordDraft     = "$draft"
	KeywordForwarded = "$forwarded"
)

// systemFlagKeywords maps the store's IMAP system-flag bits onto JMAP
// keywords. The bit values are store.Flags, restated here as untyped
// constants so this package does not import the store (contracts.go's whole
// point) — the adapter passes the raw bitmask.
//
// RFC 8621 §4.1.1 is explicit that the IMAP system flags map to keywords:
// "\Seen" -> "$seen", "\Flagged" -> "$flagged", "\Answered" -> "$answered",
// "\Draft" -> "$draft".
//
// Two IMAP flags are deliberately NOT mapped:
//
//   - \Deleted has no JMAP keyword. RFC 8621 §4.1.1 does not define one, and a
//     message marked \Deleted but not yet expunged is, in JMAP's model, simply
//     still there. The store tombstones expunged messages separately
//     (deleted_at), which is what Email/changes reports as destroyed.
//   - \Recent is session state that cannot be cached (store types.go says as
//     much) and JMAP has no concept of it at all.
var systemFlagKeywords = []struct {
	bit     uint64
	keyword string
}{
	{1 << 0, KeywordSeen},     // \Seen — bit 0, fixed by the store's schema
	{1 << 1, KeywordAnswered}, // \Answered
	{1 << 2, KeywordFlagged},  // \Flagged
	{1 << 4, KeywordDraft},    // \Draft
}

// systemFlagForKeyword is the reverse of systemFlagKeywords: it maps a JMAP
// keyword back to the store flag bit that holds it, if any.
//
// ok is false for every keyword that lives in the keywords ARRAY rather than in
// the bitmask — which is most of them, including all of A6's labels. Callers use
// it to decide which of the two places to look in (see hasKeyword).
//
// Comparison is case-insensitive per RFC 8621 §4.1.1.
func systemFlagForKeyword(keyword string) (store.Flags, bool) {
	for _, f := range systemFlagKeywords {
		if strings.EqualFold(f.keyword, keyword) {
			return store.Flags(f.bit), true
		}
	}
	return 0, false
}

// jmapKeywords converts a stored flag bitmask plus the stored keyword array
// into the JMAP keyword set of RFC 8621 §4.1.1.
//
// The result is sorted and deduplicated. Sorted because the keywords property
// is a Set (a JSON object used as one) and a stable rendering makes responses
// byte-comparable in golden tests; deduplicated because a mailbox may carry
// both the \Seen flag and a literal "$seen" keyword, and reporting it twice
// would be a malformed set.
//
// Custom keywords pass through with their case preserved except for the
// standard "$"-prefixed ones, which RFC 8621 §4.1.1 requires lowercase
// ("Keywords are case-insensitive... servers MUST preserve the case of the
// first use, but the standard keywords are always lowercase"). Lowercasing
// only the "$" family is the conservative reading: it normalizes what the
// standard fixes and preserves what belongs to the user, including the
// "$MoovL7" label shape A6 stores.
func jmapKeywords(flags uint64, stored []string) []string {
	set := make(map[string]bool, len(stored)+len(systemFlagKeywords))

	for _, f := range systemFlagKeywords {
		if flags&f.bit != 0 {
			set[f.keyword] = true
		}
	}

	for _, k := range stored {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		// IMAP spells the system flags with a backslash; if one arrives in the
		// keywords column (a server that reported it as a keyword rather than a
		// flag), normalize it into its JMAP form instead of leaking "\Seen" —
		// which is not a legal JMAP keyword — onto the wire.
		if norm, ok := imapFlagKeyword(k); ok {
			if norm != "" {
				set[norm] = true
			}
			continue
		}
		if strings.HasPrefix(k, "$") {
			k = strings.ToLower(k)
		}
		set[k] = true
	}

	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// imapFlagKeyword normalizes a backslash-prefixed IMAP system flag to its JMAP
// keyword. ok reports whether the input was such a flag at all; an empty
// return with ok=true means the flag has no JMAP equivalent and must be
// dropped (\Deleted, \Recent — see systemFlagKeywords).
func imapFlagKeyword(k string) (string, bool) {
	if !strings.HasPrefix(k, `\`) {
		return "", false
	}
	switch strings.ToLower(k) {
	case `\seen`:
		return KeywordSeen, true
	case `\answered`:
		return KeywordAnswered, true
	case `\flagged`:
		return KeywordFlagged, true
	case `\draft`:
		return KeywordDraft, true
	default:
		return "", true
	}
}

// imapNameForKeyword maps a JMAP keyword to the flag vocabulary the sync
// engine writes to Dovecot: the four system keywords of RFC 8621 §4.1.1
// become the bare flag names internal/imap re-attaches the backslash to
// ("$seen" -> "seen"), and everything else travels verbatim as an IMAP user
// keyword — which is exactly how it will come back through splitFlags and be
// re-rendered by jmapKeywords, closing the round trip.
//
// "$"-prefixed keywords are lowercased, mirroring what jmapKeywords does on
// the way out (RFC 8621 §4.1.1 keeps the standard keywords lowercase);
// user-cased keywords are preserved, since IMAP matches flags
// case-insensitively anyway (RFC 3501 §2.3.2).
func imapNameForKeyword(k string) string {
	switch strings.ToLower(k) {
	case KeywordSeen:
		return "seen"
	case KeywordAnswered:
		return "answered"
	case KeywordFlagged:
		return "flagged"
	case KeywordDraft:
		return "draft"
	}
	if strings.HasPrefix(k, "$") {
		return strings.ToLower(k)
	}
	return k
}

// validKeyword enforces the RFC 8621 §4.1.1 keyword grammar: "The IANA
// 'IMAP and JMAP Keywords' registry... keywords... MUST be at least 1
// character in length and MUST NOT be larger than 255 octets", and "MUST NOT
// contain any of the following characters: ( ) { ] % * \" \\" nor control
// characters — the IMAP atom-specials a flag can never carry (RFC 3501
// §9). A keyword this refuses could never reach Dovecot as a flag, so
// refusing it here with invalidProperties beats a protocol error later.
func validKeyword(k string) bool {
	if len(k) == 0 || len(k) > 255 {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		if c <= 0x1f || c == 0x7f || c == ' ' {
			return false
		}
		switch c {
		case '(', ')', '{', ']', '%', '*', '"', '\\':
			return false
		}
	}
	return true
}

// keywordSet renders a keyword list as the JSON object RFC 8621 §4.1.1
// requires: "a set of keywords... A set is represented as an object with the
// keys as the set's members and true as the value for each".
func keywordSet(keywords []string) map[string]bool {
	out := make(map[string]bool, len(keywords))
	for _, k := range keywords {
		out[k] = true
	}
	return out
}
