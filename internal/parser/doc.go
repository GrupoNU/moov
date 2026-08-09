// Package parser turns raw RFC 5322 bytes into Moov's canonical message form.
//
// # Contract
//
// docs/specs/L2-sync-engine.md §4.2:
//
//	func Parse(raw io.Reader, limits Limits) ParsedMessage
//
// ParsedMessage carries a Status (ok | partial | failed), the name of the layer
// that produced it, decoded canonical headers, a flattened part tree, the text
// destined for the full-text index, and the defects observed along the way.
//
// # Cascade
//
// Spike S4 settled the pipeline (L2 §2.4): go-message first, enmime as the
// recovery layer, and the raw blob as the floor. The raw blob is ALWAYS
// persisted before parsing, so parsing is a retryable derivation — a parser
// version bump re-derives, it never re-downloads.
//
// Mandatory mitigations, each with its corpus case:
//
//   - Resource caps of our own: depth <= 100, parts <= 1000, configurable total
//     size. Exceeding a cap is parse_status='failed', which is a bounded
//     refusal and therefore correct behavior (corpus convention C4, S4 H8).
//   - Never discard partial bytes on a decode error; mark the part partial
//     (S4 H5).
//   - Post-process RFC 2047: a residual "=?…?=" triggers a retry with raw
//     encodings (S4 H4).
//   - Charset cascade: declared, then chardet, then windows-1252, recording
//     charset_guessed (S4 H6).
//   - Recursive descent into message/rfc822 under the global caps, so forwarded
//     mail is indexed (S4 H7).
//   - Both layers failing means parse_status='failed' plus a plain-text salvage
//     of the body when it is legible at all (S4 H3). The rate of 'failed' is a
//     metric with an alert (risk R4).
//   - CR-only input is normalized before the cascade, as an explicit decision
//     rather than an accident of the library (S4 H9).
//
// # Dependencies
//
// This package depends on neither internal/imap nor internal/store. It is pure:
// bytes in, ParsedMessage out. That is what makes the corpus suite meaningful.
//
// # Test suite
//
// testdata/mime-corpus/ (110 cases + manifest.yaml) is the regression suite and
// it exists before this package does, by design. Every case must produce the
// result its manifest entry declares; a disagreement is a finding to examine,
// never an expectation to quietly edit. Basic fuzzing over the corpus
// generators must not panic.
//
// The engine's operating rule, which this package exists to honor: a message
// that fails to parse must never break a folder's sync.
//
// HTML sanitization is declared as a hook here but implemented elsewhere; it is
// explicitly out of scope for this phase (L2 §1).
//
// Implementation lands in epic E4.
package parser
