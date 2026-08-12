package mail

import (
	"fmt"
	"strconv"
	"strings"
)

// Object ids on the wire.
//
// RFC 8620 §1.2 constrains an Id to 1-255 octets of [A-Za-z0-9_-], and adds:
// "it MUST NOT start with a dash or a digit". It also states the client "MUST
// treat this as an opaque value" and warns servers against ids that "could be
// confused with a number".
//
// So a mailbox is "m<base36>" and a thread is "t<base36>", following the same
// scheme internal/jmap/accountid.go established for accounts ("a<base36>").
// Three properties come out of that choice:
//
//   - the prefix satisfies "must not start with a digit" for free, and makes a
//     mis-wired id (a mailbox id passed where an email id is expected) a
//     decode error rather than a silent read of the wrong object;
//   - base36 keeps ids short, which matters because Email/get sends hundreds
//     of them per request;
//   - the encoding is canonical — DecodeX rejects any spelling its own EncodeX
//     would not produce — so two distinct strings can never name one object,
//     which is what would otherwise let a cache be poisoned by an alias.
//
// Email ids deliberately do NOT get their own prefix scheme here: they use the
// same "m" space as… no. They use "e". Spelled out because the reader will
// look for it: e = Email, m = Mailbox, t = Thread, a = Account.

const (
	mailboxIDPrefix = "m"
	emailIDPrefix   = "e"
	threadIDPrefix  = "t"
)

// EncodeMailboxID renders a store mailbox id as a JMAP Id.
func EncodeMailboxID(id int64) string { return encodeID(mailboxIDPrefix, id) }

// DecodeMailboxID parses a JMAP mailbox Id back to the store id.
func DecodeMailboxID(s string) (int64, error) { return decodeID(mailboxIDPrefix, "mailbox", s) }

// EncodeEmailID renders a store message id as a JMAP Id.
func EncodeEmailID(id int64) string { return encodeID(emailIDPrefix, id) }

// DecodeEmailID parses a JMAP email Id back to the store message id.
func DecodeEmailID(s string) (int64, error) { return decodeID(emailIDPrefix, "email", s) }

// EncodeThreadID renders a thread key as a JMAP Id.
//
// Unlike mailboxes and emails, a thread has no row of its own in the store
// (see thread.go), so its key is the store id of the thread's ROOT message.
// That makes the id stable for as long as the root exists, which is the same
// guarantee a thread table would give while the thread column does not exist.
func EncodeThreadID(rootMessageID int64) string { return encodeID(threadIDPrefix, rootMessageID) }

// DecodeThreadID parses a JMAP thread Id back to its root message id.
func DecodeThreadID(s string) (int64, error) { return decodeID(threadIDPrefix, "thread", s) }

func encodeID(prefix string, id int64) string {
	if id <= 0 {
		// Unreachable for a persisted row (the identity columns start at 1).
		// Returning an empty string rather than "m0" keeps an impossible id
		// from ever looking like a valid one on the wire.
		return ""
	}
	return prefix + strconv.FormatInt(id, 36)
}

func decodeID(prefix, kind, s string) (int64, error) {
	rest, ok := strings.CutPrefix(s, prefix)
	if !ok || rest == "" {
		return 0, fmt.Errorf("mail: invalid %s id %q", kind, s)
	}
	id, err := strconv.ParseInt(rest, 36, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("mail: invalid %s id %q", kind, s)
	}
	// Canonicality: reject "m01", "M1", padding — anything Encode would not
	// have produced for this same value.
	if encodeID(prefix, id) != s {
		return 0, fmt.Errorf("mail: non-canonical %s id %q", kind, s)
	}
	return id, nil
}

// decodeIDList decodes a list of wire ids, reporting which ones were
// unparseable.
//
// An id that does not decode is NOT an error for the request: RFC 8620 §5.1
// says a /get returns "a list of ids for records that could not be found" in
// notFound, and an id this server could never have issued is precisely a
// record that cannot be found. Failing the whole call instead would let one
// stale id in a client's cache break a batch of 200 legitimate ones.
func decodeIDList(ids []string, decode func(string) (int64, error)) (decoded []int64, byID map[int64]string, unknown []string) {
	decoded = make([]int64, 0, len(ids))
	byID = make(map[int64]string, len(ids))
	seen := make(map[int64]bool, len(ids))

	for _, raw := range ids {
		id, err := decode(raw)
		if err != nil {
			unknown = append(unknown, raw)
			continue
		}
		// A duplicate id in the request must not produce a duplicate object in
		// the response; the wire spelling is canonical, so the first wins.
		if seen[id] {
			continue
		}
		seen[id] = true
		decoded = append(decoded, id)
		byID[id] = raw
	}
	return decoded, byID, unknown
}
