// Package jmap implements the JMAP core protocol (RFC 8620): the
// Request/Response model, the Invocation data type, method dispatch,
// back-reference resolution, the error taxonomy, and the request limits.
//
// # What this package is, and is not
//
// This is the wire-protocol layer of Moov's JMAP server (L2-jmap-server §2.1).
// It knows how to parse a Request, run its method calls in order, resolve
// "#"-prefixed result references, and produce a Response. It does NOT know
// about mail, HTTP, authentication, or storage:
//
//   - HTTP transport, auth and CORS live in internal/jmaphttp.
//   - The mail methods (Mailbox/*, Email/*, Thread/*) arrive with J2/J3 in
//     internal/jmap/mail, registered into this package's Registry.
//   - This package MUST NOT import internal/store or any database driver
//     (L2 §4): it defines contracts, and the architecture test in this
//     package enforces that the dependency arrow points the right way.
//
// # Conformance stance
//
// Where RFC 8620 gives the server a choice, this package takes the strictest
// interpretation that keeps real clients working, and each such decision is
// documented at the point it is made with the RFC section cited. Wire behavior
// follows the RFC even where internal documents describe it loosely.
package jmap
