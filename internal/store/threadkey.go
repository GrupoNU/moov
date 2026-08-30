package store

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// The durable thread key (L3 epic E4, migration 0009).
//
// # What problem this solves
//
// `messages.thread_id` is the id of the thread's oldest member — a surrogate
// key minted by a sequence. It is stable while the store lives and it is
// GONE the moment the store is rebuilt, because a rebuild re-inserts every
// message and the sequence hands out different numbers. ADR-001 makes that
// rebuild an ordinary operation ("Dovecot es la fuente de verdad; Moov es
// cache reconstruible"), so anything keyed on thread_id is state a rebuild
// silently destroys.
//
// Mute is exactly that kind of state, and GC-10 forbids losing it. So the
// thread needs an identity derived from the MAIL rather than from our
// numbering — one that Dovecot's own bytes reproduce.
//
// # The derivation, and why it is the ROOT rather than a graph fingerprint
//
// The key is the thread's root: the oldest member's Message-ID, or the first
// ancestor it names, or a digest of its normalized subject. Migration 0009's
// header argues the choice at length; the short form is that a fingerprint
// over the whole member set would CHANGE on every reply, and an identity that
// changes when the conversation grows is not an identity — a mute keyed on it
// would evaporate on the first reply, which is the exact message the mute
// exists to suppress.
//
// # The prefixes
//
// Every key carries a two-letter scheme prefix ("mid:", "ref:", "sub:"). Three
// reasons, in order of weight:
//
//  1. The three namespaces must not collide. A Message-ID and a subject digest
//     are both opaque strings; without a prefix a (pathological) Message-ID
//     equal to some subject's digest would fuse two unrelated conversations.
//  2. An operator reading the table can tell a well-formed thread from one
//     held together by a subject guess, which is precisely the population to
//     look at when threading complaints arrive.
//  3. It leaves room for a fourth scheme without a data migration.

// Thread key schemes. The prefix is part of the stored key.
const (
	threadKeyMessageID = "mid:" // the root's own Message-ID (the good case)
	threadKeyReference = "ref:" // an ancestor we never received
	threadKeySubject   = "sub:" // neither header usable: a subject digest
)

// maxThreadKeyLength bounds a stored key.
//
// The key is half of a unique btree index, and PostgreSQL cannot index a value
// past roughly a third of a page. A Message-ID longer than this is malformed
// (RFC 5322 §3.6.4 gives no length limit, but 998 is the hard line-length cap
// and real ones are under 100 bytes), so truncation here only ever affects
// mail that is already broken — and truncation is safe for those: two
// different 512-byte Message-IDs sharing a 512-byte prefix would merge two
// threads, which is the same outcome the subject fallback risks and strictly
// better than refusing to key the thread at all.
const maxThreadKeyLength = 512

// ThreadKey derives the durable key of the thread rooted at this message.
//
// The candidate is the OLDEST member of the thread — the caller resolves that;
// this function does not query. accountID is not part of the returned string:
// the key is scoped by the account_id COLUMN of the threads table, so the same
// conversation delivered to two accounts yields the same key text under two
// different account scopes, which is what makes the pair a natural key.
//
// It never returns an empty string. A message with no Message-ID, no
// References and no usable subject still gets a key — the digest of an empty
// normalized subject — which is a degenerate but STABLE answer: every such
// message in one account collapses into one thread key, and that is a
// documented, bounded wrongness rather than a nil identity the caller would
// have to branch on. In practice threads.go never reaches it: a message with
// none of the three has no way to join anything either, so it is its own
// thread and the degenerate key is only ever consulted for itself.
func ThreadKey(c ThreadCandidate) string {
	if id := strings.TrimSpace(c.MessageID); id != "" {
		return truncateThreadKey(threadKeyMessageID + id)
	}
	// The FIRST reference, not the last: References is ordered oldest-first
	// (RFC 5322 §3.6.4 — "the contents of the parent's References field,
	// followed by the parent's Message-ID"), so entry zero is the closest
	// thing to the conversation's root this message knows about. Using the
	// last would name the immediate parent, which is not stable: a different
	// member of the same thread would name a different parent and derive a
	// different key for one conversation.
	for _, ref := range c.References {
		if ref = strings.TrimSpace(ref); ref != "" {
			return truncateThreadKey(threadKeyReference + ref)
		}
	}
	// Neither header. The subject digest uses the SAME normalization the JWZ
	// subject fallback uses (NormalizeSubject), so a thread held together by
	// subject in threads.go is keyed by that same subject here — the two
	// mechanisms agree by construction instead of by coincidence.
	key, _ := NormalizeSubject(c.Subject)
	sum := sha256.Sum256([]byte(key))
	return threadKeySubject + hex.EncodeToString(sum[:16])
}

// truncateThreadKey bounds a key on a rune boundary (a Message-ID is ASCII in
// any well-formed mail, but this store has met mail that is not).
func truncateThreadKey(key string) string {
	if len(key) <= maxThreadKeyLength {
		return key
	}
	return truncateRunes(key, maxThreadKeyLength)
}

// ThreadKeyScheme reports which of the three derivations produced a key, for
// diagnostics and for the operator question migration 0009 names ("which of my
// threads are held together by a subject guess?").
func ThreadKeyScheme(key string) string {
	switch {
	case strings.HasPrefix(key, threadKeyMessageID):
		return "message-id"
	case strings.HasPrefix(key, threadKeyReference):
		return "reference"
	case strings.HasPrefix(key, threadKeySubject):
		return "subject"
	default:
		return "unknown"
	}
}
