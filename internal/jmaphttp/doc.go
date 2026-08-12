// Package jmaphttp is the HTTP transport of Moov's JMAP server: routing,
// authentication, CORS, request limits and the Session endpoint
// (L2-jmap-server §2.1).
//
// # Division of labor
//
// internal/jmap owns the protocol — Request/Response, dispatch,
// back-references, the error taxonomy. This package owns everything HTTP:
//
//   - routes.go declares every route the server exposes, in one place
//     (L2 §2.4, a lesson from spike S1 H7: clients get confused by
//     undocumented route layouts and by /.well-known/jmap redirecting).
//   - auth.go implements arbitration J-A1: HTTP Basic validated against
//     Dovecot by a real IMAP LOGIN, a positive-result cache, and a
//     failed-attempt limiter that keeps Moov from triggering Mailcow's
//     fail2ban (ADR §4).
//   - cors.go implements the configurable allow-list CORS policy with
//     correct preflight on every route.
//   - session.go builds the RFC 8620 §2 Session object, advertising ONLY
//     what the server enforces (limits come from the same jmap.Limits the
//     engine applies).
//
// The package reads accounts through the small AccountDirectory interface —
// satisfied by *store.Store — and never issues SQL or IMAP commands of its
// own except the LOGIN validation confined to imapvalidator.go.
package jmaphttp
