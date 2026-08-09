// Package blob stores raw message bytes, content-addressed by sha256.
//
// # Contract
//
// docs/specs/L2-sync-engine.md §2.3 and §2.4. Three properties define this
// package:
//
//   - Write-once. A blob's name IS the sha256 of its content, so a write is
//     either a no-op or the creation of new content. Blobs are never mutated.
//   - Refcounted. The same bytes are referenced by every message that carries
//     them; content identity survives IMAP moves and mailbox reconstruction
//     after a UIDVALIDITY change, which is what lets a resync avoid
//     re-downloading anything already held.
//   - Garbage collected. When the last reference drops the bytes become
//     collectable. The GC must be safe against concurrent writers — a blob
//     being written while its last old reference is dropped must not be
//     collected. E3's acceptance criteria require a concurrency test for
//     exactly this.
//
// # Position in the pipeline
//
// The raw blob is persisted BEFORE parsing (L2 §2.4). Parsing is a retryable
// derivation of a blob that is already durable; a parser version bump
// re-derives from local bytes.
//
// # Hard constraint from the ADR
//
// This package never reads or writes Dovecot's vmail filesystem. Not once, not
// read-only. Mailcow's storage is reached exclusively over IMAP; touching it
// directly corrupts mailboxes under maildir_very_dirty_syncs. Moov's blob store
// is its own storage, and it is reconstructible from Dovecot by definition.
//
// Implementation lands in epic E3.
package blob
