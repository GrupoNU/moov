package blob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a blob is not present in the store.
var ErrNotFound = errors.New("blob: not found")

// HashSize is the length in bytes of a blob name (sha256).
const HashSize = sha256.Size

// Hash is the content address of a blob: the sha256 of its bytes.
type Hash [HashSize]byte

// String returns the lowercase hex form.
func (h Hash) String() string { return hex.EncodeToString(h[:]) }

// Bytes returns the raw 32 bytes, which is the form the database stores.
func (h Hash) Bytes() []byte { return h[:] }

// ParseHash converts a hex string back into a Hash.
func ParseHash(s string) (Hash, error) {
	var h Hash
	b, err := hex.DecodeString(s)
	if err != nil {
		return h, fmt.Errorf("blob: parsing hash %q: %w", s, err)
	}
	if len(b) != HashSize {
		return h, fmt.Errorf("blob: hash %q is %d bytes, want %d", s, len(b), HashSize)
	}
	copy(h[:], b)
	return h, nil
}

// HashFromBytes converts a 32-byte slice — the form read back from the
// database — into a Hash.
func HashFromBytes(b []byte) (Hash, error) {
	var h Hash
	if len(b) != HashSize {
		return h, fmt.Errorf("blob: hash is %d bytes, want %d", len(b), HashSize)
	}
	copy(h[:], b)
	return h, nil
}

// OwnerKind is what holds a reference to a blob. It matches the CHECK
// constraint on blob_refs in migration 0002.
type OwnerKind string

// The reference owner kinds.
const (
	// OwnerMessage is a stored message's raw bytes.
	OwnerMessage OwnerKind = "message"
	// OwnerPart is a MIME part stored separately from its message.
	OwnerPart OwnerKind = "part"
	// OwnerDraft is a draft being composed, whose message row does not exist
	// yet.
	OwnerDraft OwnerKind = "draft"
	// OwnerPin is a deliberate hold with no owner id, used to keep freshly
	// written bytes alive until their real reference is committed.
	OwnerPin OwnerKind = "pin"
)

// DefaultGCGracePeriod is how long a blob must have been unreferenced before
// the GC will collect it.
//
// The grace period is the safety margin that makes mark-and-sweep correct in
// the presence of concurrent writers: a blob written by a transaction that has
// not yet committed its reference is momentarily unreferenced, and collecting
// it in that window would delete live data. An hour is far longer than any
// sync transaction and short enough that garbage does not accumulate.
const DefaultGCGracePeriod = time.Hour

// Store is a content-addressed blob store: bytes on the filesystem, reference
// counts in PostgreSQL.
//
// The split is deliberate. Message bodies do not belong in PostgreSQL — they
// would bloat TOAST and the WAL for data that is never queried, only fetched
// by name. But reference counting must be transactional with the message rows
// that do the referencing, which a filesystem cannot provide. So the bytes go
// to disk, keyed by their own hash, and the bookkeeping goes to the database
// in the same transaction as the message insert.
type Store struct {
	root  string
	pool  *pgxpool.Pool
	grace time.Duration
}

// Config configures a blob store.
type Config struct {
	// Root is the directory holding the sharded blob tree. Required.
	Root string
	// Pool is the PostgreSQL pool holding blobs and blob_refs. Required.
	Pool *pgxpool.Pool
	// GCGracePeriod overrides DefaultGCGracePeriod.
	GCGracePeriod time.Duration
}

// New creates a blob store, creating the root directory if needed.
func New(cfg Config) (*Store, error) {
	if cfg.Root == "" {
		return nil, errors.New("blob: Root is required")
	}
	if cfg.Pool == nil {
		return nil, errors.New("blob: Pool is required")
	}
	grace := cfg.GCGracePeriod
	if grace <= 0 {
		grace = DefaultGCGracePeriod
	}
	// 0o700: blobs are other people's mail. Nothing outside this process's
	// user has any business reading them.
	if err := os.MkdirAll(cfg.Root, 0o700); err != nil {
		return nil, fmt.Errorf("blob: creating root %s: %w", cfg.Root, err)
	}
	return &Store{root: cfg.Root, pool: cfg.Pool, grace: grace}, nil
}

// Root returns the directory the blobs live in.
func (s *Store) Root() string { return s.root }

// path returns the sharded filesystem path of a hash: ab/cd/abcd…
//
// Two levels of 256 gives 65,536 directories, so a million blobs average ~15
// files per directory. A flat directory with a million entries makes every
// lookup and every readdir slow on most filesystems; this keeps both cheap.
func (s *Store) path(h Hash) string {
	name := h.String()
	return filepath.Join(s.root, name[0:2], name[2:4], name)
}

// Put stores the bytes read from r and returns their hash and size.
//
// It is write-once and idempotent: the blob's name IS the hash of its content,
// so writing the same bytes twice is a no-op rather than a conflict. Two
// concurrent Puts of identical content both succeed and agree on the result.
//
// Durability sequence, which matters and is easy to get subtly wrong:
//  1. stream into a temp file in the same directory as the destination, so the
//     rename is on one filesystem and therefore atomic;
//  2. fsync the temp file, so its bytes are on disk before anything points at
//     it;
//  3. rename into place — atomic, so a reader sees either no file or the whole
//     file, never a partial one;
//  4. fsync the containing directory, so the rename itself survives a crash.
//
// Skipping step 4 is the classic mistake: the file's contents are durable but
// the directory entry naming it may not be, so a crash can leave the blob
// unreachable while the database row referencing it is committed.
//
// Put also records the blob row. It does NOT add a reference: the caller does
// that in the transaction that creates the referencing message, via AddRef.
// Until then the blob is unreferenced and protected only by the GC grace
// period, which is why that period must exceed the longest sync transaction.
func (s *Store) Put(ctx context.Context, r io.Reader) (Hash, int64, error) {
	var zero Hash

	dir := filepath.Join(s.root, "tmp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return zero, 0, fmt.Errorf("blob: creating temp dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, "put-*")
	if err != nil {
		return zero, 0, fmt.Errorf("blob: creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup for every path that does not rename the file away.
	// Once the rename succeeds this remove finds nothing, which is fine.
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, hasher), r)
	if err != nil {
		return zero, 0, fmt.Errorf("blob: writing temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return zero, 0, fmt.Errorf("blob: syncing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return zero, 0, fmt.Errorf("blob: closing temp file: %w", err)
	}

	var h Hash
	copy(h[:], hasher.Sum(nil))

	dest := s.path(h)
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return zero, 0, fmt.Errorf("blob: creating shard dir: %w", err)
	}

	// If the destination already exists the content is byte-identical by
	// definition, so the existing file is kept and the temp one discarded.
	// This is what makes concurrent Puts of the same content safe: whichever
	// rename lands first wins, and the loser's bytes were identical anyway.
	if _, err := os.Stat(dest); err != nil {
		if !os.IsNotExist(err) {
			return zero, 0, fmt.Errorf("blob: stat %s: %w", dest, err)
		}
		if err := os.Rename(tmpName, dest); err != nil {
			// A concurrent writer may have created it between the Stat and
			// the Rename. On Unix rename(2) would have overwritten it
			// harmlessly; on Windows it fails, and an existing destination is
			// still the correct outcome.
			if _, statErr := os.Stat(dest); statErr != nil {
				return zero, 0, fmt.Errorf("blob: renaming into place: %w", err)
			}
		}
		if err := syncDir(filepath.Dir(dest)); err != nil {
			return zero, 0, fmt.Errorf("blob: syncing shard dir: %w", err)
		}
	}

	if err := s.recordBlob(ctx, h, size); err != nil {
		return zero, 0, err
	}
	return h, size, nil
}

// recordBlob inserts the blobs row, or leaves an existing one alone.
//
// A blob that already exists with refcount 0 keeps its zero_ref_since, so a
// re-Put does not extend the life of garbage indefinitely. The row is created
// with refcount 0 and zero_ref_since now(): it becomes collectable after the
// grace period unless somebody references it, which is the correct default for
// bytes nobody has claimed yet.
func (s *Store) recordBlob(ctx context.Context, h Hash, size int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO blobs (sha256, size, refcount, zero_ref_since)
		VALUES ($1, $2, 0, now())
		ON CONFLICT (sha256) DO NOTHING`, h.Bytes(), size)
	if err != nil {
		return fmt.Errorf("blob: recording %s: %w", h, err)
	}
	return nil
}

// Open returns a reader for a blob's bytes. The caller closes it.
func (s *Store) Open(h Hash) (io.ReadCloser, error) {
	f, err := os.Open(s.path(h))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("blob %s: %w", h, ErrNotFound)
		}
		return nil, fmt.Errorf("blob: opening %s: %w", h, err)
	}
	return f, nil
}

// Exists reports whether a blob's bytes are present on disk.
func (s *Store) Exists(h Hash) bool {
	_, err := os.Stat(s.path(h))
	return err == nil
}

// AddRef records a reference and increments the refcount, in tx.
//
// It takes a transaction rather than using the pool because the whole point is
// that the reference commits atomically with whatever created it: a message
// insert and its blob reference must both happen or neither, or a crash
// between them leaves either a message pointing at collectable bytes or bytes
// nobody will ever collect.
//
// It is idempotent per (blob, owner): the unique index on blob_refs means a
// retried sync of the same message cannot inflate the count.
//
// # Why the blob row is locked first
//
// The obvious implementation — insert the reference, then `refcount =
// refcount + 1` — drifts under concurrency, and a test caught it doing so.
// Under READ COMMITTED two transactions can interleave the insert and the
// increment so that the stored count no longer matches the number of reference
// rows, and the same window lets an increment race the GC's delete.
//
// Taking the blob row's lock BEFORE touching blob_refs serializes every
// mutation of a given blob's reference set, which makes the pair atomic. The
// count is then RECOMPUTED from the reference rows rather than incremented, so
// it is a fact derived from the data instead of a running total that can drift
// away from it. Both changes matter: the lock removes the race, and the
// recomputation means even an unforeseen path cannot leave the count wrong.
//
// SELECT ... FOR UPDATE also makes the GC's delete wait, and the GC re-checks
// the refcount under its own lock, so the two can never both win.
func AddRef(ctx context.Context, tx pgx.Tx, h Hash, accountID int64, kind OwnerKind, ownerID int64) error {
	var existing int64
	err := tx.QueryRow(ctx, `SELECT refcount FROM blobs WHERE sha256 = $1 FOR UPDATE`, h.Bytes()).Scan(&existing)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Collected, or never recorded. The caller must Put the bytes
			// again; silently creating a row here would point a reference at
			// bytes that are not on disk.
			return fmt.Errorf("blob %s: %w", h, ErrNotFound)
		}
		return fmt.Errorf("blob: locking %s: %w", h, err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO blob_refs (sha256, account_id, owner_kind, owner_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (sha256, owner_kind, owner_id) WHERE owner_id IS NOT NULL
		DO NOTHING`, h.Bytes(), accountID, kind, ownerID); err != nil {
		return fmt.Errorf("blob: adding reference to %s: %w", h, err)
	}

	return syncRefcount(ctx, tx, h)
}

// Ref names one reference to add: which blob, on behalf of which account, held
// by which owner.
type Ref struct {
	Hash      Hash
	AccountID int64
	Kind      OwnerKind
	OwnerID   int64
}

// AddRefs records a batch of references in tx, taking every blob's lock exactly
// once and in a deterministic order.
//
// # Why a batch variant exists at all
//
// AddRef called in a loop does three round trips per reference (lock, insert,
// recompute) and — the part that matters — takes the same blob's lock once per
// reference to it. A sync batch of a hundred messages routinely contains
// several copies of one blob: a mailing list post that landed in two folders, a
// company-wide announcement held by two accounts being migrated together. Each
// of those took the lock, released it, and took it again.
//
// This variant groups by hash first, so each blob is locked once, all of its
// reference rows are inserted together, and its count is recomputed once. The
// lock order is the sorted hash order — the same global order E5's pipeline
// established after a bulk migration deadlocked on arrival-order locking
// (SQLSTATE 40P01) — so it composes with any other caller that respects it.
//
// # What it does NOT change
//
// The invariant is exactly AddRef's: the blob row is locked BEFORE blob_refs is
// touched, and the refcount is RECOMPUTED from the reference rows rather than
// incremented. Both properties are what make the count correct under
// concurrency instead of merely usually right, and batching does not weaken
// either — it only amortizes them.
//
// It is idempotent per (blob, owner), and a duplicate inside one call is
// harmless for the same reason: the count comes from the rows that exist, not
// from how many inserts were attempted.
func AddRefs(ctx context.Context, tx pgx.Tx, refs []Ref) error {
	if len(refs) == 0 {
		return nil
	}

	// Group by blob, preserving one entry per distinct hash. The groups are
	// then visited in sorted hash order, which is the lock order.
	byHash := make(map[Hash][]Ref, len(refs))
	order := make([]Hash, 0, len(refs))
	for _, r := range refs {
		if _, seen := byHash[r.Hash]; !seen {
			order = append(order, r.Hash)
		}
		byHash[r.Hash] = append(byHash[r.Hash], r)
	}
	sort.Slice(order, func(i, j int) bool {
		return bytes.Compare(order[i][:], order[j][:]) < 0
	})

	for _, h := range order {
		var existing int64
		err := tx.QueryRow(ctx, `SELECT refcount FROM blobs WHERE sha256 = $1 FOR UPDATE`, h.Bytes()).Scan(&existing)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Collected, or never recorded. Same rule as AddRef: never
				// create the row here, because that would point a reference at
				// bytes that are not on disk.
				return fmt.Errorf("blob %s: %w", h, ErrNotFound)
			}
			return fmt.Errorf("blob: locking %s: %w", h, err)
		}

		group := byHash[h]
		batch := &pgx.Batch{}
		for _, r := range group {
			batch.Queue(`
				INSERT INTO blob_refs (sha256, account_id, owner_kind, owner_id)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT (sha256, owner_kind, owner_id) WHERE owner_id IS NOT NULL
				DO NOTHING`, r.Hash.Bytes(), r.AccountID, r.Kind, r.OwnerID)
		}
		results := tx.SendBatch(ctx, batch)
		for i := range group {
			if _, err := results.Exec(); err != nil {
				_ = results.Close()
				return fmt.Errorf("blob: adding reference %d of %d to %s: %w",
					i+1, len(group), h, err)
			}
		}
		if err := results.Close(); err != nil {
			return fmt.Errorf("blob: adding references to %s: %w", h, err)
		}

		// Once per blob, not once per reference: the count is derived from the
		// rows, so recomputing it after the whole group is the same answer for
		// a fraction of the work.
		if err := syncRefcount(ctx, tx, h); err != nil {
			return err
		}
	}
	return nil
}

// RemoveRef drops a reference and decrements the refcount, in tx.
//
// When the count reaches zero it stamps zero_ref_since, which starts the GC
// grace period. The blob is not deleted here: deleting bytes inside a
// transaction that might roll back would be unrecoverable, so collection is
// always a separate, later, idempotent sweep.
func RemoveRef(ctx context.Context, tx pgx.Tx, h Hash, kind OwnerKind, ownerID int64) error {
	// Same lock-first discipline as AddRef, for the same reason.
	var existing int64
	err := tx.QueryRow(ctx, `SELECT refcount FROM blobs WHERE sha256 = $1 FOR UPDATE`, h.Bytes()).Scan(&existing)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // already collected; nothing to decrement
		}
		return fmt.Errorf("blob: locking %s: %w", h, err)
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM blob_refs
		 WHERE sha256 = $1 AND owner_kind = $2 AND owner_id = $3`,
		h.Bytes(), kind, ownerID); err != nil {
		return fmt.Errorf("blob: removing reference to %s: %w", h, err)
	}

	return syncRefcount(ctx, tx, h)
}

// syncRefcount recomputes a blob's refcount from its reference rows and
// maintains zero_ref_since, with the blob row already locked by the caller.
//
// Deriving the count instead of adjusting it is what makes the invariant
// "refcount equals the number of blob_refs rows" true by construction rather
// than by everyone remembering to keep it true. zero_ref_since is stamped only
// on the transition to zero, so a blob that has been garbage for an hour does
// not have its grace period restarted by an unrelated reference churn.
func syncRefcount(ctx context.Context, tx pgx.Tx, h Hash) error {
	if _, err := tx.Exec(ctx, `
		UPDATE blobs b
		   SET refcount = c.n,
		       zero_ref_since = CASE
		           WHEN c.n > 0 THEN NULL
		           WHEN b.zero_ref_since IS NOT NULL THEN b.zero_ref_since
		           ELSE now()
		       END
		  FROM (
		      SELECT count(*) AS n FROM blob_refs WHERE sha256 = $1
		  ) c
		 WHERE b.sha256 = $1`, h.Bytes()); err != nil {
		return fmt.Errorf("blob: updating refcount of %s: %w", h, err)
	}
	return nil
}

// AddRefTx is AddRef in its own transaction, for callers with nothing else to
// commit alongside.
func (s *Store) AddRefTx(ctx context.Context, h Hash, accountID int64, kind OwnerKind, ownerID int64) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		return AddRef(ctx, tx, h, accountID, kind, ownerID)
	})
}

// RemoveRefTx is RemoveRef in its own transaction.
func (s *Store) RemoveRefTx(ctx context.Context, h Hash, kind OwnerKind, ownerID int64) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		return RemoveRef(ctx, tx, h, kind, ownerID)
	})
}

// RefCount returns a blob's current reference count.
func (s *Store) RefCount(ctx context.Context, h Hash) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT refcount FROM blobs WHERE sha256 = $1`, h.Bytes()).Scan(&n)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("blob %s: %w", h, ErrNotFound)
		}
		return 0, fmt.Errorf("blob: reading refcount of %s: %w", h, err)
	}
	return n, nil
}

// Size returns a blob's recorded size.
func (s *Store) Size(ctx context.Context, h Hash) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT size FROM blobs WHERE sha256 = $1`, h.Bytes()).Scan(&n)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("blob %s: %w", h, ErrNotFound)
		}
		return 0, fmt.Errorf("blob: reading size of %s: %w", h, err)
	}
	return n, nil
}

// GCStats reports what a collection pass did.
type GCStats struct {
	Scanned   int
	Collected int
	Bytes     int64
	// Skipped counts blobs that were claimed between the scan and the delete,
	// i.e. the race the grace period and the re-check exist to handle.
	Skipped int
}

// GC collects blobs that have been unreferenced for longer than the grace
// period. It is a mark-and-sweep, and it is safe to run concurrently with
// writers.
//
// The safety argument, which is the whole design of this method:
//
//   - A candidate must have refcount = 0 AND zero_ref_since older than the
//     grace period. A blob freshly written by Put is unreferenced but recent,
//     so it cannot be collected while its writer is still working.
//
//   - Each candidate is deleted under a row lock, and its refcount is
//     RE-CHECKED inside that lock. If a writer referenced the blob between the
//     scan and the delete, the re-check sees refcount > 0 and skips it. This
//     is what closes the window that a naive "select candidates, then delete
//     them" would leave open.
//
//   - The database row is deleted BEFORE the file. If the process dies in
//     between, the result is an orphaned file — wasted space, found by the next
//     full sweep — rather than a database row pointing at bytes that no longer
//     exist, which would be data loss. The ordering is chosen so every crash
//     leaves a recoverable state.
//
// limit bounds one pass so the GC never holds locks for long; the caller runs
// it periodically.
func (s *Store) GC(ctx context.Context, limit int) (GCStats, error) {
	var stats GCStats
	if limit <= 0 {
		limit = 1000
	}
	cutoff := time.Now().Add(-s.grace)

	rows, err := s.pool.Query(ctx, `
		SELECT sha256, size FROM blobs
		 WHERE refcount = 0 AND zero_ref_since IS NOT NULL AND zero_ref_since < $1
		 ORDER BY zero_ref_since
		 LIMIT $2`, cutoff, limit)
	if err != nil {
		return stats, fmt.Errorf("blob: scanning gc candidates: %w", err)
	}

	type candidate struct {
		hash Hash
		size int64
	}
	var candidates []candidate
	for rows.Next() {
		var raw []byte
		var size int64
		if err := rows.Scan(&raw, &size); err != nil {
			rows.Close()
			return stats, fmt.Errorf("blob: scanning gc candidates: %w", err)
		}
		h, err := HashFromBytes(raw)
		if err != nil {
			rows.Close()
			return stats, err
		}
		candidates = append(candidates, candidate{hash: h, size: size})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return stats, fmt.Errorf("blob: scanning gc candidates: %w", err)
	}
	stats.Scanned = len(candidates)

	for _, c := range candidates {
		collected, err := s.collectOne(ctx, c.hash, cutoff)
		if err != nil {
			return stats, err
		}
		if !collected {
			stats.Skipped++
			continue
		}
		// The row is gone, so the bytes are unreachable. Removing the file is
		// now safe, and a failure here only leaks disk space.
		if err := os.Remove(s.path(c.hash)); err != nil && !os.IsNotExist(err) {
			return stats, fmt.Errorf("blob: removing %s: %w", c.hash, err)
		}
		stats.Collected++
		stats.Bytes += c.size
	}
	return stats, nil
}

// collectOne deletes one blob row if it is still collectable under a row lock.
// It reports whether the row was actually deleted.
func (s *Store) collectOne(ctx context.Context, h Hash, cutoff time.Time) (bool, error) {
	var collected bool
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		// SELECT ... FOR UPDATE takes the row lock; the conditions are
		// re-evaluated under it, so a concurrent AddRef either happened before
		// (and the refcount is now > 0, so no row is returned) or blocks until
		// this transaction finishes and then finds the row gone.
		var refcount int64
		err := tx.QueryRow(ctx, `
			SELECT refcount FROM blobs
			 WHERE sha256 = $1 AND refcount = 0
			   AND zero_ref_since IS NOT NULL AND zero_ref_since < $2
			 FOR UPDATE`, h.Bytes(), cutoff).Scan(&refcount)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil // claimed, or already collected
			}
			return fmt.Errorf("blob: locking %s for gc: %w", h, err)
		}

		tag, err := tx.Exec(ctx, `DELETE FROM blobs WHERE sha256 = $1 AND refcount = 0`, h.Bytes())
		if err != nil {
			return fmt.Errorf("blob: deleting %s: %w", h, err)
		}
		collected = tag.RowsAffected() > 0
		return nil
	})
	return collected, err
}

func (s *Store) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("blob: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("blob: commit: %w", err)
	}
	return nil
}

// syncDir fsyncs a directory so a rename into it is durable.
//
// On Windows a directory cannot be opened this way; the rename is durable
// enough there for our purposes and the error is ignored deliberately rather
// than by omission.
func syncDir(dir string) error {
	// #nosec G304 -- dir is always a path this package constructed under its
	// own configured root (s.path's parent), never caller-supplied input, and
	// it is opened read-only solely to obtain a handle to fsync.
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if err := f.Sync(); err != nil {
		// EINVAL / ERROR_ACCESS_DENIED on platforms that do not support
		// fsync on a directory handle.
		if errors.Is(err, os.ErrInvalid) || errors.Is(err, os.ErrPermission) {
			return nil
		}
		return err
	}
	return nil
}
