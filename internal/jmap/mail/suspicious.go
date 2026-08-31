package mail

import (
	"strings"

	"github.com/GrupoNU/moov/internal/parser"
	"github.com/GrupoNU/moov/internal/sieve"
)

// Suspicious-mail surfacing (L3 epic E10; canon §4.1.15).
//
// Gmail's rule: a message the scanner judged spam-ish gets its affordances
// DEGRADED — a warning banner, remote images withheld — without the mail
// being hidden. For a message filed into Junk the client already does this
// (E2). The gap this file closes is the message that carries a spam verdict
// but was NOT filed to Junk: a never-spam rule rescued it, or the deployment
// files on a different threshold than it tags on. The user is reading it in
// the Inbox with no signal that the scanner flagged it.
//
// # Where the verdict comes from — the Rspamd headers Mailcow actually stamps
//
// Findings (E6 recon against the live Mailcow, plus the Rspamd milter_headers
// documentation those settings come from):
//
//   - `X-Spam-Flag: YES` — the verdict on tagged-but-accepted spam, and the
//     exact header Mailcow's own global_sieve_after files into Junk on. This
//     is the SAME constant the Sieve generator guards on
//     (sieve.DefaultSpamHeader): one definition of "the scanner said spam",
//     two consumers.
//   - `X-Spam: Yes` — Rspamd's milter_headers `spam-header` routine under its
//     default name, for deployments configured that way.
//   - `X-Spamd-Result: default: True [score / threshold]; SYMBOLS…` — the
//     extended_spam_headers form, stamped on every scanned message; the
//     boolean after the settings-id colon is the verdict.
//
// What is deliberately NOT invented: a borderline-score heuristic. The
// scanner's own boolean verdict is the only signal surfaced; parsing scores
// out of X-Spamd-Result and drawing our own line would make Moov a second
// spam filter with an unauditable threshold. If the headers are absent (mail
// that never crossed Rspamd — e.g. Sent copies appended by Moov itself), the
// message is simply not suspicious.
//
// # Trust boundary, stated honestly
//
// These headers live in the message and a sender can write them. Rspamd's
// milter_headers routines remove pre-existing copies of the headers they
// manage before stamping their own, so on a Mailcow deployment the delivery
// pipeline owns them; but if a forged header ever survived, the failure
// direction is a warning banner plus blocked images on a legitimate mail —
// affordances degraded, nothing hidden, nothing fetched. The inverse failure
// (trusting a sender's "not spam") does not exist: absence of the header
// grants nothing that the default posture didn't already grant.
//
// # Why a server-computed vendor property, not a client-side header parse
//
// The client COULD request `headers` and grep them (it already fetches
// headers for List-Unsubscribe). It does not, for two reasons. One place:
// the verdict grammar (which headers, which values, the settings-id prefix
// of X-Spamd-Result) is deployment knowledge that belongs next to
// sieve.DefaultSpamHeader on the server, not duplicated into a TypeScript
// parser that drifts. Honesty at the contract: the property is computed from
// the same memoized raw-blob parse Email/get already performs for the
// message-open request (bodyValues/headers), so it costs no extra I/O
// exactly where it is used, and any other JMAP client can read the same bit
// instead of re-deriving it.
//
// It is served ONLY when requested by name: it is not in the §4.6 default
// property list, so a client that has never heard of it (Bulwark) sees a
// byte-identical server.

// PropSuspicious is the vendor Email property carrying the scanner's spam
// verdict. Vendor-prefixed so it can never collide with a future RFC 8621
// property or with the `header:*` request grammar.
const PropSuspicious = "moov:suspicious"

// suspiciousVerdict reports whether the scanner declared this message spam.
//
// nil (blob unavailable) and hard-failed parses have no headers to read —
// CanonHeaders is empty by construction on StatusFailed — so they are not
// suspicious: no verdict is not a verdict.
func suspiciousVerdict(p *parser.ParsedMessage) bool {
	if p == nil {
		return false
	}
	h := p.Headers

	// X-Spam-Flag: YES — the Mailcow/Rspamd verdict the Sieve layer keys on.
	for _, v := range h.Values(sieve.DefaultSpamHeader) {
		if strings.EqualFold(strings.TrimSpace(v), sieve.DefaultSpamValue) {
			return true
		}
	}

	// X-Spam: Yes — Rspamd milter_headers `spam-header` routine, default name.
	for _, v := range h.Values("X-Spam") {
		if strings.EqualFold(strings.TrimSpace(v), "yes") {
			return true
		}
	}

	// X-Spamd-Result: default: True [12.10 / 15.00]; … (extended form).
	for _, v := range h.Values("X-Spamd-Result") {
		if spamdResultIsSpam(v) {
			return true
		}
	}
	return false
}

// spamdResultIsSpam reads the boolean out of an X-Spamd-Result value.
//
// The shape is `<settings-id>: <True|False> [<score> / <threshold>]; …` —
// "default" is the settings-id on a stock deployment, but it is a NAME, so
// the parse anchors on the first colon rather than the word. A value with no
// colon is read from its start (defensive: some rspamd versions omit the
// settings-id entirely).
func spamdResultIsSpam(v string) bool {
	rest := v
	if i := strings.IndexByte(v, ':'); i >= 0 {
		rest = v[i+1:]
	}
	rest = strings.TrimSpace(rest)
	if len(rest) < 4 || !strings.EqualFold(rest[:4], "true") {
		return false
	}
	// Token boundary: "True" or "True [score / threshold]…", never a prefix
	// of some longer word.
	return len(rest) == 4 || rest[4] == ' ' || rest[4] == '\t' || rest[4] == '['
}
