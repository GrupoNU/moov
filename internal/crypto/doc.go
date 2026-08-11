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
// # Envelope format
//
// A sealed value is a self-describing byte string, stored verbatim in
// accounts.imap_app_password (bytea):
//
//	┌────────┬────────┬──────────────┬───────────────────────────┐
//	│ byte 0 │ byte 1 │  bytes 2..13 │        bytes 14..         │
//	│ version│ key id │  nonce (12B) │  ciphertext ‖ GCM tag     │
//	└────────┴────────┴──────────────┴───────────────────────────┘
//
// Version is the envelope format version (currently [EnvelopeV1]); it exists so
// a future change of primitive — a different AEAD, a longer nonce — is a
// decodable migration rather than an ambiguous blob. Key id names WHICH master
// key sealed this value, which is what makes rotation possible: during a
// rotation both the old and the new key are loaded, every ciphertext still
// opens, and re-sealing happens row by row without downtime.
//
// The two header bytes are authenticated: they are prepended to the caller's
// additional data, so an attacker cannot relabel a ciphertext as belonging to a
// different key or a different envelope version and have it still open.
//
// # Additional authenticated data
//
// Every seal binds its ciphertext to a context string — for accounts, the value
// returned by [AccountAAD]. Without it, an attacker with write access to the
// database (but not the master key) could copy account A's encrypted app
// password into account B's row and make Moov authenticate to Dovecot as A
// while believing it is B. With it, the open fails: the tag covers the account
// id, so the ciphertext is only valid in the row it was created for.
//
// # Key rotation procedure
//
// Rotation never requires downtime and never requires the plaintext to leave
// the process. Concretely:
//
//  1. Generate a new key: `moovctl key generate` (or any 32 bytes from a CSPRNG,
//     base64-encoded).
//
//  2. Publish it alongside the current one. [Keyring] accepts several keys; the
//     configuration form is a comma-separated list of `id:base64key` entries in
//     MOOV_MASTER_KEY, with the FIRST entry being the primary — the one new
//     seals use. For example, rotating from key 1 to key 2:
//
//     MOOV_MASTER_KEY="2:<new-base64>,1:<old-base64>"
//
//     Restart moovd. From this moment new seals carry key id 2 and old
//     ciphertexts sealed under key id 1 still open, because key 1 is still
//     loaded.
//
//  3. Re-seal the existing rows: `moovctl key rotate` reads each account's
//     ciphertext, opens it with whichever key it names, re-seals it under the
//     primary, and writes it back. The operation is idempotent and interruptible
//     — a row already carrying the primary key id is skipped — so it may be run
//     repeatedly and killed at any point.
//
//  4. Once no row references the old key ([Keyring.InUse] over the store
//     reports only the primary), drop it from MOOV_MASTER_KEY and restart. The
//     old key material can then be destroyed.
//
// Step 4 is the only irreversible one, and it is deliberately separated from
// step 3 by an operator decision rather than automated: a key deleted while a
// row still needs it is unrecoverable data loss, and the price of that mistake
// is higher than the cost of one extra manual step.
//
// # Boundary
//
// This package knows nothing about IMAP, Mailcow or the store. It seals and
// opens bytes. The provisioning flow that calls it lives in internal/provision.
package crypto
