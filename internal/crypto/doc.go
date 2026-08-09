// Package crypto encrypts the per-account credentials Moov must keep at rest.
//
// # What is stored, and what is not
//
// ADR-001 §4 defines the provisioning flow: the user's password is used ONCE,
// for an IMAP LOGIN that validates it, and is then discarded. What Moov keeps
// is an application password minted through the Mailcow API with a scope of
// imap+smtp+sieve only. The user's own password is never written to the
// database, never logged, and never held longer than the validation call — E7
// carries a test that demonstrates exactly that, because "we don't store it" is
// a claim that has to be provable in a public repository.
//
// # Contract
//
// docs/specs/L2-sync-engine.md §2.1, E7:
//
//   - AES-256-GCM, authenticated encryption. A unique random nonce per
//     encryption, never reused. Additional authenticated data binds a ciphertext
//     to the account it belongs to, so a row cannot be moved between accounts.
//   - The master key lives OUTSIDE the database — environment variable or
//     mounted file — so a database dump alone yields nothing usable.
//   - Key rotation is a documented, testable procedure: ciphertexts record the
//     key version they were sealed with, and re-encryption is an online
//     operation.
//   - No home-made primitives. crypto/aes and crypto/cipher from the standard
//     library, crypto/rand for every nonce and key.
//
// # Boundary
//
// This package knows nothing about IMAP, Mailcow or the store. It seals and
// opens bytes. The provisioning flow that calls it belongs to E7.
//
// Implementation lands in epic E7.
package crypto
