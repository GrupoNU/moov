// Package mail implements the JMAP Mail data types of RFC 8621 — Mailbox,
// Thread and Email — over Moov's store, blob and parser packages.
//
// # Why this package exists separately from internal/jmap
//
// internal/jmap is the protocol contract and may not import storage (L2
// §4, enforced by depguard and by TestJMAPCoreImportsNothingBelowTheContract).
// This package is the other side of that arrow: it defines the reader
// interfaces its handlers consume (contracts.go), implements them over
// internal/store + internal/blob + internal/parser (adapter.go), and registers
// the handlers into a jmap.Registry (register.go). Handlers depend on the
// interfaces, never on *store.Store, which is what makes them testable with
// fakes and what keeps a future store change from reaching into protocol code.
//
// # Phase 1 is read-only and truthful about it
//
// Every Mailbox reports myRights with mayReadItems true and every mutation
// right false (RFC 8621 §2). That is not a placeholder: this server genuinely
// cannot mutate anything yet, and advertising a right it does not honor would
// make a client offer its user an action that silently fails. The rights
// become dynamic when Email/set lands, not before.
//
// # The load-bearing decisions, each cited where it is implemented
//
//   - Ids are opaque, canonical and reversible: see id.go. A JMAP Id must not
//     be a bare integer, because clients cache them and a bare integer invites
//     both guessing and accidental arithmetic.
//   - blobId is the sha256 hex of the raw message, which the store already
//     addresses content by (L2 §4) — so a blobId is free rather than a second
//     identifier to maintain.
//   - bodyValues are derived on demand by re-parsing the raw blob (email.go),
//     never stored. L2 §5 risk 2 accepts the re-parse cost for phase 1 and
//     names the cache by (blobId, parser-version) as the mitigation if real
//     usage shows it matters.
//   - Threading is derived here rather than read from the store, because the
//     store has no thread column yet. See thread.go, which documents the gap
//     and the interface seam that closes it without touching handlers.
package mail
