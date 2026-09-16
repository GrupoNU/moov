// Package accounts implements the per-domain accounts API of epic M1
// (docs/specs/L2-accounts-api-contract.md §2): the service that creates,
// updates, suspends, locks, exports and deletes ONE domain's mailboxes on
// behalf of an external system, without that system ever touching Mailcow.
//
// # What lives here and what does not
//
// This package is the DOMAIN layer: it owns the state machine (§2.4), the
// field rules (§2.5), the ordering of the Mailcow and Moov writes, the
// rollback on a half-done create, and the audit line every write leaves. It
// owns no HTTP: statuses, bodies and the no-oracle 404 are
// internal/jmaphttp's (admin_accounts.go), which translates the typed errors
// below. That split is what lets the whole state machine be tested without a
// server, and the whole wire contract be tested without a Mailcow.
//
// # The domain check is the security boundary
//
// Mailcow does not scope an API key by domain (contract §4): the write key
// Moov holds can create a mailbox on ANY domain of the installation. The only
// thing standing between a service account and someone else's domain is the
// check in Service.resolve, which runs BEFORE any Mailcow call and is pinned
// by TestDomainIsCheckedBeforeAnyMailcowCall. Every route funnels through it;
// no method here takes an address that has not passed it.
//
// # Nothing infers "does not exist" from a silence
//
// Mailcow reports almost every failure inside an HTTP 200 and answers `{}`
// both for an absent object and for a silently rejected key (F0 §3). The
// client validates its key at startup and refuses to read `{}` as "not found"
// until it has; this package relies on that, because an idempotent create
// that mistook a rejected key for "no such mailbox" would mint duplicates.
package accounts
