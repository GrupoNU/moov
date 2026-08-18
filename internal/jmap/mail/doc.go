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
// # Rights are truthful, phase by phase
//
// A myRights member is true exactly when a registered method honors it
// (RFC 8621 §2 — rights are what a client builds its UI from). Phase 1 was
// read-only and said so; W1 made the message-level rights (mayAddItems,
// mayRemoveItems, maySetSeen, maySetKeywords) real; W2 makes mayCreateChild,
// mayRename and mayDelete real — the last two PER MAILBOX, since Mailbox/set
// refuses to rename or destroy a protected role folder and myRights says so
// rather than letting a client offer an action that will be refused
// (mailbox.go rightsFor). maySubmit stays false until W3.
//
// The write side follows the same layering as the read side: handlers speak
// to the EmailWriter interface (write.go), and the only file that knows the
// sync engine's write executor implements it is write_adapter.go — the JMAP
// layer never touches internal/imap (L2-jmap-write §4).
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
