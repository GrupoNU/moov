// Package provision implements the account provisioning flow of ADR-001 §4.
//
// # The flow
//
// A user hands Moov the password to their own mailbox exactly once. What
// happens next, in order:
//
//  1. VALIDATE. A real IMAP LOGIN against Dovecot, using the user's password.
//     Not an API call that reports whether the password "looks right" — an
//     actual authenticated session against the server Moov will be syncing.
//     There is no other way to be sure the credential works, and a
//     provisioning step that succeeds against a mailbox Moov cannot then read
//     is worse than one that fails.
//
//  2. MINT. A fresh app password is generated locally from crypto/rand and
//     registered with Mailcow, scoped to imap+smtp+sieve and nothing else.
//     Mailcow keeps a bcrypt hash; the plaintext exists only in this process.
//
//  3. SEAL. The app password is encrypted with AES-256-GCM (internal/crypto),
//     bound to the account row it belongs to.
//
//  4. PERSIST. The sealed bytes go to the store. The plaintext is discarded.
//
//  5. DISCARD. The user's password is discarded. It was never written
//     anywhere, never logged, and never held beyond step 1.
//
// # No app password, no account
//
// If step 2 fails, provisioning fails. Moov does not fall back to storing the
// user's own password — that fallback is exactly the design decision that made
// the Nylas breach as bad as it was (ADR §5), and it is not available here at
// any level of the API. The store's credential column holds one thing: an
// AES-256-GCM envelope around a scoped app password.
//
// # Proving the claim
//
// "We never store your password" is a claim a public repository has to be able
// to demonstrate, not merely assert. [Provisioner.Provision] is exercised in
// provision_test.go against fakes that record every byte written to the store
// and every byte logged, and the test fails if the user's password appears in
// either. That test is the load-bearing artifact of this package; the rest is
// plumbing around it.
//
// # Rollback
//
// Steps 3 and 4 can fail after step 2 has already created a live credential on
// the Mailcow side. Leaving it there would be a credential nobody tracks and
// nobody can revoke, so [Provisioner.Provision] deletes it before returning the
// error. When that cleanup ALSO fails, the returned error names the app
// password by name and id so an operator can remove it by hand: the one
// outcome that must never happen is a silent orphan.
//
// # Boundary
//
// This package orchestrates. It owns no protocol: IMAP belongs to
// internal/imap, the Mailcow API to internal/mailcow, encryption to
// internal/crypto and persistence to internal/store. Every one of them is
// reached through a narrow interface declared here, which is what makes the
// flow testable without a network — and what keeps this package from becoming
// a second place where any of those protocols is understood.
package provision
