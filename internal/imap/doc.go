// Package imap is the only package in Moov that may import go-imap.
//
// # Architecture rule
//
// ADR-001 and L2 §2.1 make this an invariant, not a preference: go-imap/v2 is
// beta, vendored, and carried with a local patch set (L2 §2.2), so its API can
// move under us at any bump. Confining it here means a breaking upstream change
// costs one package, not the engine. The rule is enforced mechanically by the
// depguard linter in .golangci.yml — an import of go-imap from anywhere else
// fails CI, and TestGoIMAPIsConfinedToInternalIMAP in this package proves the
// rule is live.
//
// Everything outside this package talks to Dovecot through the Client
// interface, expressed in Moov's own types. No go-imap type may appear in an
// exported signature here.
//
// # Contract
//
// The interface this package must provide is docs/specs/L2-sync-engine.md §4.1:
//
//	Connect            STARTTLS on dovecot:143, LOGIN, ENABLE QRESYNC,
//	                   capabilities probed POST-login (S2: NOTIFY is not
//	                   advertised before authentication)
//	ListMailboxes      LIST + STATUS, with SPECIAL-USE roles (S1)
//	SelectQResync      SELECT (QRESYNC (uidvalidity modseq)), returning
//	                   VANISHED (EARLIER) UIDs
//	FetchChanges       UID FETCH CHANGEDSINCE … VANISHED
//	FetchMessages      headers or full bodies, streamed, never buffered whole
//	Watch              NOTIFY SET STATUS (PERSONAL …) plus a maintenance IDLE
//	                   loop; emits MailboxChanged and Overflow events
//	StoreFlags         conditional STORE with read-back verification while
//	                   [MODIFIED] is not exposed by the client (S2 H6)
//	Metadata           IMAP METADATA get/set for the label definitions (A6)
//
// # Design constraints carried from the spikes
//
//   - Certificate verification is always on; ServerName is configurable because
//     the internal certificate belongs to the public hostname (S1 H2). There is
//     no global "skip verify" switch, ever.
//   - One watcher connection per active account plus a small bounded worker
//     pool (default 2), with backoff+jitter and a per-account circuit breaker,
//     to stay clear of fail2ban (ADR §4).
//   - NOTIFY encoding needs the patched encoder (S2 H4); NOTIFICATIONOVERFLOW
//     means a full account resync.
//   - A mailbox holds at most 26 keywords durably — see
//     MaxDurableKeywordsPerMailbox. The server does not enforce it and does
//     not report it; validation V1 measured it on disk.
//
// # The vendored patch set
//
// go-imap is carried with three local patches (patches/README.md): QRESYNC
// support (upstream PR #757), two RFC 5465 fixes in the NOTIFY encoder, and
// the CONDSTORE [MODIFIED] response code. Without them this package cannot
// resync, cannot see flag changes in non-selected folders, and cannot tell a
// rejected conditional write from an applied one.
//
// `go mod vendor` silently reverts all three. `make vendor-patches` puts them
// back, and TestVendoredPatchSetIsApplied fails the build if any is missing.
package imap
