package parser

// HTML sanitization: declared here, implemented elsewhere.
//
// L2 §1 puts HTML sanitization out of scope for the sync engine phase, and E4's
// acceptance criteria ask for the hook to be DECLARED but not implemented. This
// file is that declaration, and it exists so the seam is visible in the type
// system rather than living in someone's memory until the PWA phase.
//
// The reason it does not belong in this package: sanitization is a rendering
// decision with a threat model attached (ADR-001 §7 specifies three layers —
// bluemonday server-side, DOMPurify client-side, and an iframe sandbox without
// allow-scripts, under a CSP). Doing any of it during parsing would mean the
// store held sanitized HTML, and the sanitized form would then be the only copy
// the engine had. That is the wrong invariant: a sanitizer bug discovered later
// could not be fixed by re-rendering, only by re-fetching every message.
//
// So the parser stores what the sender sent, and sanitization happens on the way
// OUT, per render, where the policy can change without touching stored data.

// SanitizeHook is the seam the rendering layer fills.
//
// It takes the raw HTML of a text/html part and returns markup safe to embed.
// The parser never calls it: ParsedMessage carries the original bytes, and the
// JMAP/PWA layer applies the hook when it serves a body.
//
// The signature takes and returns a string rather than []byte because the
// sanitizer implementations this will wrap (bluemonday) are string-oriented,
// and because by the time a body reaches rendering it has already been
// transcoded to UTF-8 by this package.
type SanitizeHook func(html string) string

// NoSanitize is the explicit no-op, for tests and for the pipeline stages that
// legitimately handle untrusted HTML themselves (the FTS text extraction in
// assembleBodyText strips tags rather than sanitizing them).
//
// It is named to be conspicuous: a call site holding NoSanitize is one grep away
// from review, which an untyped nil would not be.
func NoSanitize(html string) string { return html }
