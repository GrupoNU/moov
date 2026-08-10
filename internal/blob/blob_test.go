package blob_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx", for migrations

	"github.com/GrupoNU/moov/internal/blob"
	"github.com/GrupoNU/moov/internal/store"
)

// Blob store tests against a real PostgreSQL 17 and a real filesystem.
//
// The concurrency tests are the E3 acceptance criterion. They are the reason
// this package's design is what it is: the interesting failures here are races
// between a writer and the garbage collector, and they are invisible to
// sequential tests.

const testDBEnv = "MOOV_TEST_DATABASE_URL"

func testEnv(t *testing.T) (*blob.Store, *pgxpool.Pool, int64) {
	t.Helper()

	dsn := os.Getenv(testDBEnv)
	if dsn == "" {
		t.Skipf("%s is not set; start a dev database with `make db-up` to run the blob tests", testDBEnv)
	}

	// The schema must exist: blobs and blob_refs come from migration 0002.
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		_ = db.Close()
		t.Fatalf("Migrate: %v", err)
	}
	_ = db.Close()

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	// An account to own the references; removing it cascades them away.
	var accountID int64
	email := fmt.Sprintf("blob-%d@example.test", time.Now().UnixNano())
	err = pool.QueryRow(context.Background(), `
		INSERT INTO accounts (email, imap_host) VALUES ($1, 'dovecot.internal') RETURNING id`,
		email).Scan(&accountID)
	if err != nil {
		t.Fatalf("creating account: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM accounts WHERE id = $1`, accountID); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})

	// A grace period of zero makes GC collect immediately, which is what the
	// tests want; the race tests that need the grace window set their own.
	bs, err := blob.New(blob.Config{
		Root:          t.TempDir(),
		Pool:          pool,
		GCGracePeriod: time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("blob.New: %v", err)
	}
	return bs, pool, accountID
}

func TestPutIsContentAddressed(t *testing.T) {
	bs, _, _ := testEnv(t)
	ctx := context.Background()

	content := []byte("From: ana@example.test\r\nSubject: hola\r\n\r\ncuerpo\r\n")
	want := sha256.Sum256(content)

	h, size, err := bs.Put(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if h != blob.Hash(want) {
		t.Errorf("hash = %s, want %x", h, want)
	}
	if size != int64(len(content)) {
		t.Errorf("size = %d, want %d", size, len(content))
	}

	rc, err := bs.Open(h)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rc.Close() }()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading blob: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("read back %q, want %q", got, content)
	}
}

func TestOpenMissingBlob(t *testing.T) {
	bs, _, _ := testEnv(t)

	var h blob.Hash
	if _, err := bs.Open(h); !errors.Is(err, blob.ErrNotFound) {
		t.Errorf("Open of a missing blob = %v, want ErrNotFound", err)
	}
}

// The sharded layout: a blob must land at ab/cd/abcd… so a million blobs do
// not share one directory.
func TestPutShardsDirectories(t *testing.T) {
	bs, _, _ := testEnv(t)
	ctx := context.Background()

	h, _, err := bs.Put(ctx, bytes.NewReader([]byte("shard me")))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	name := h.String()
	shard := fmt.Sprintf("%s/%s/%s/%s", bs.Root(), name[0:2], name[2:4], name)
	if _, err := os.Stat(shard); err != nil {
		t.Errorf("blob is not at the sharded path %s: %v", shard, err)
	}
}

// E3 AC: parallel Put of IDENTICAL content.
//
// Every caller must get the same hash and size, exactly one file must exist,
// and no call may fail — this is the ordinary case when the same message is
// synced from two folders at once.
func TestConcurrentPutIdenticalContent(t *testing.T) {
	bs, pool, _ := testEnv(t)
	ctx := context.Background()

	content := bytes.Repeat([]byte("identical message bytes\n"), 500)
	want := blob.Hash(sha256.Sum256(content))

	const writers = 16
	var wg sync.WaitGroup
	hashes := make([]blob.Hash, writers)
	sizes := make([]int64, writers)
	errs := make([]error, writers)

	start := make(chan struct{})
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release them all at once, to maximize overlap
			hashes[i], sizes[i], errs[i] = bs.Put(ctx, bytes.NewReader(content))
		}()
	}
	close(start)
	wg.Wait()

	for i := range writers {
		if errs[i] != nil {
			t.Errorf("writer %d: %v", i, errs[i])
			continue
		}
		if hashes[i] != want {
			t.Errorf("writer %d: hash = %s, want %s", i, hashes[i], want)
		}
		if sizes[i] != int64(len(content)) {
			t.Errorf("writer %d: size = %d, want %d", i, sizes[i], len(content))
		}
	}

	// Exactly one database row.
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM blobs WHERE sha256 = $1`, want.Bytes()).Scan(&rows); err != nil {
		t.Fatalf("counting blob rows: %v", err)
	}
	if rows != 1 {
		t.Errorf("%d blob rows for identical content, want 1", rows)
	}

	// And no temp files left behind.
	entries, err := os.ReadDir(bs.Root() + "/tmp")
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading temp dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("%d temp files left behind after concurrent Put", len(entries))
	}
}

// Reference counting must be exact under concurrency, and idempotent per
// owner: a retried sync of the same message must not inflate the count.
func TestConcurrentAddRefIsIdempotent(t *testing.T) {
	bs, pool, accountID := testEnv(t)
	ctx := context.Background()

	h, _, err := bs.Put(ctx, bytes.NewReader([]byte("refcount test")))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// 8 goroutines each add the SAME reference (same owner) 4 times.
	const writers, repeats = 8, 4
	var wg sync.WaitGroup
	errs := make(chan error, writers*repeats)

	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range repeats {
				if err := bs.AddRefTx(ctx, h, accountID, blob.OwnerMessage, 1); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		// A serialization failure is an acceptable outcome of contention as
		// long as the final count is right; anything else is a real bug.
		t.Errorf("AddRefTx: %v", err)
	}

	count, err := bs.RefCount(ctx, h)
	if err != nil {
		t.Fatalf("RefCount: %v", err)
	}
	if count != 1 {
		t.Errorf("refcount = %d after %d concurrent adds of the same reference, want 1",
			count, writers*repeats)
	}

	// The denormalized count must agree with the reference rows it summarizes.
	var refRows int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM blob_refs WHERE sha256 = $1`, h.Bytes()).Scan(&refRows); err != nil {
		t.Fatalf("counting refs: %v", err)
	}
	if refRows != count {
		t.Errorf("refcount %d disagrees with %d blob_refs rows", count, refRows)
	}
}

// Distinct owners each contribute one reference, and removing them brings the
// count back to zero and stamps zero_ref_since.
func TestRefCountAcrossOwners(t *testing.T) {
	bs, pool, accountID := testEnv(t)
	ctx := context.Background()

	h, _, err := bs.Put(ctx, bytes.NewReader([]byte("multi-owner blob")))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	const owners = 5
	for i := range owners {
		if err := bs.AddRefTx(ctx, h, accountID, blob.OwnerMessage, int64(i+1)); err != nil {
			t.Fatalf("AddRefTx: %v", err)
		}
	}
	count, err := bs.RefCount(ctx, h)
	if err != nil {
		t.Fatalf("RefCount: %v", err)
	}
	if count != owners {
		t.Errorf("refcount = %d, want %d", count, owners)
	}

	// While referenced, zero_ref_since must be NULL, or the GC would consider
	// a live blob a candidate.
	var zeroRefSince *time.Time
	if err := pool.QueryRow(ctx, `SELECT zero_ref_since FROM blobs WHERE sha256 = $1`, h.Bytes()).Scan(&zeroRefSince); err != nil {
		t.Fatalf("reading zero_ref_since: %v", err)
	}
	if zeroRefSince != nil {
		t.Errorf("zero_ref_since = %v on a referenced blob, want NULL", zeroRefSince)
	}

	for i := range owners {
		if err := bs.RemoveRefTx(ctx, h, blob.OwnerMessage, int64(i+1)); err != nil {
			t.Fatalf("RemoveRefTx: %v", err)
		}
	}
	count, err = bs.RefCount(ctx, h)
	if err != nil {
		t.Fatalf("RefCount: %v", err)
	}
	if count != 0 {
		t.Errorf("refcount = %d after removing every reference, want 0", count)
	}

	if err := pool.QueryRow(ctx, `SELECT zero_ref_since FROM blobs WHERE sha256 = $1`, h.Bytes()).Scan(&zeroRefSince); err != nil {
		t.Fatalf("reading zero_ref_since: %v", err)
	}
	if zeroRefSince == nil {
		t.Error("zero_ref_since is NULL on an unreferenced blob; the GC will never collect it")
	}
}

// GC collects unreferenced blobs and leaves referenced ones alone.
func TestGCCollectsOnlyUnreferenced(t *testing.T) {
	bs, _, accountID := testEnv(t)
	ctx := context.Background()

	keep, _, err := bs.Put(ctx, bytes.NewReader([]byte("keep me: referenced")))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := bs.AddRefTx(ctx, keep, accountID, blob.OwnerMessage, 1); err != nil {
		t.Fatalf("AddRefTx: %v", err)
	}

	drop, _, err := bs.Put(ctx, bytes.NewReader([]byte("drop me: never referenced")))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	stats, err := bs.GC(ctx, 100)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if stats.Collected < 1 {
		t.Errorf("GC collected %d blobs, want at least the unreferenced one", stats.Collected)
	}

	if !bs.Exists(keep) {
		t.Error("GC collected a REFERENCED blob — this is data loss")
	}
	if bs.Exists(drop) {
		t.Error("GC did not collect the unreferenced blob")
	}
	if _, err := bs.RefCount(ctx, drop); !errors.Is(err, blob.ErrNotFound) {
		t.Errorf("the collected blob's row still exists: %v", err)
	}
}

// The grace period is what makes freshly written bytes safe: a blob written by
// a transaction that has not yet committed its reference is momentarily
// unreferenced, and collecting it then would delete live data.
func TestGCRespectsGracePeriod(t *testing.T) {
	dsn := os.Getenv(testDBEnv)
	if dsn == "" {
		t.Skipf("%s is not set", testDBEnv)
	}
	_, pool, _ := testEnv(t)
	ctx := context.Background()

	// A generous grace period: nothing written now may be collected.
	guarded, err := blob.New(blob.Config{
		Root:          t.TempDir(),
		Pool:          pool,
		GCGracePeriod: time.Hour,
	})
	if err != nil {
		t.Fatalf("blob.New: %v", err)
	}

	h, _, err := guarded.Put(ctx, bytes.NewReader([]byte("just written, not yet referenced")))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	stats, err := guarded.GC(ctx, 100)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if stats.Collected != 0 {
		t.Errorf("GC collected %d blobs inside the grace period, want 0", stats.Collected)
	}
	if !guarded.Exists(h) {
		t.Error("GC deleted a blob that was written moments ago; the grace period is not being honored")
	}
}

// E3 AC: the AddRef/GC race.
//
// This is the failure mode the design exists to prevent — a blob being
// referenced at the same moment the collector decides it is garbage. The two
// legal outcomes are "referenced and present" or "collected and unreferenced";
// the illegal one is a reference pointing at bytes that are gone.
func TestConcurrentAddRefAndGC(t *testing.T) {
	bs, pool, accountID := testEnv(t)
	ctx := context.Background()

	const rounds = 40
	var wg sync.WaitGroup

	for round := range rounds {
		// Distinct content per round, so each round is an independent race.
		content := fmt.Appendf(nil, "race round %d", round)
		h, _, err := bs.Put(ctx, bytes.NewReader(content))
		if err != nil {
			t.Fatalf("Put: %v", err)
		}

		var addErr, gcErr error
		start := make(chan struct{})

		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			addErr = bs.AddRefTx(ctx, h, accountID, blob.OwnerMessage, int64(round+1))
		}()
		go func() {
			defer wg.Done()
			<-start
			_, gcErr = bs.GC(ctx, 100)
		}()
		close(start)
		wg.Wait()

		if gcErr != nil {
			t.Fatalf("round %d: GC: %v", round, gcErr)
		}

		// AddRef legitimately fails with ErrNotFound if the GC won the race and
		// removed the blob row first. That is a correct outcome — the caller
		// retries by re-Putting the bytes. Any OTHER error is a real bug.
		if addErr != nil && !errors.Is(addErr, blob.ErrNotFound) {
			t.Fatalf("round %d: AddRef failed with an unexpected error: %v", round, addErr)
		}

		// What must never happen is a surviving reference to absent bytes.
		var refs int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM blob_refs WHERE sha256 = $1`, h.Bytes()).Scan(&refs); err != nil {
			t.Fatalf("round %d: counting refs: %v", round, err)
		}

		if refs > 0 {
			if !bs.Exists(h) {
				t.Fatalf("round %d: %d reference(s) point at a blob whose bytes were collected — "+
					"this is the race the grace period and the locked re-check exist to prevent", round, refs)
			}
			var refcount int64
			if err := pool.QueryRow(ctx,
				`SELECT refcount FROM blobs WHERE sha256 = $1`, h.Bytes()).Scan(&refcount); err != nil {
				t.Fatalf("round %d: reading refcount: %v", round, err)
			}
			if refcount != int64(refs) {
				t.Errorf("round %d: refcount %d disagrees with %d reference rows", round, refcount, refs)
			}
		} else if addErr == nil && !bs.Exists(h) {
			// AddRef reported success but left no reference and no bytes.
			t.Errorf("round %d: AddRef succeeded but the blob is gone and unreferenced", round)
		}
	}
}

// Several collectors running at once must not double-collect or error: GC is
// expected to be safe to run from more than one process.
func TestConcurrentGC(t *testing.T) {
	bs, _, _ := testEnv(t)
	ctx := context.Background()

	const blobs = 20
	for i := range blobs {
		if _, _, err := bs.Put(ctx, bytes.NewReader(fmt.Appendf(nil, "garbage %d", i))); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	const collectors = 4
	var wg sync.WaitGroup
	stats := make([]blob.GCStats, collectors)
	errs := make([]error, collectors)

	start := make(chan struct{})
	for i := range collectors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			stats[i], errs[i] = bs.GC(ctx, 100)
		}()
	}
	close(start)
	wg.Wait()

	total := 0
	for i := range collectors {
		if errs[i] != nil {
			t.Errorf("collector %d: %v", i, errs[i])
		}
		total += stats[i].Collected
	}

	// Each blob is collected exactly once across all collectors. More would
	// mean two of them deleted the same row, which the locked re-check
	// prevents.
	if total > blobs {
		t.Errorf("collectors reported %d collections for %d blobs; something was collected twice",
			total, blobs)
	}
}

// AddRef must commit atomically with whatever created the reference: if the
// surrounding transaction rolls back, so does the reference and the count.
func TestAddRefIsTransactional(t *testing.T) {
	bs, pool, accountID := testEnv(t)
	ctx := context.Background()

	h, _, err := bs.Put(ctx, bytes.NewReader([]byte("transactional reference")))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := blob.AddRef(ctx, tx, h, accountID, blob.OwnerMessage, 1); err != nil {
		t.Fatalf("AddRef: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		t.Fatalf("Rollback: %v", err)
	}

	count, err := bs.RefCount(ctx, h)
	if err != nil {
		t.Fatalf("RefCount: %v", err)
	}
	if count != 0 {
		t.Errorf("refcount = %d after a rolled-back AddRef, want 0", count)
	}
}

func TestHashRoundTrip(t *testing.T) {
	original := blob.Hash(sha256.Sum256([]byte("round trip")))

	parsed, err := blob.ParseHash(original.String())
	if err != nil {
		t.Fatalf("ParseHash: %v", err)
	}
	if parsed != original {
		t.Errorf("ParseHash round trip = %s, want %s", parsed, original)
	}

	fromBytes, err := blob.HashFromBytes(original.Bytes())
	if err != nil {
		t.Fatalf("HashFromBytes: %v", err)
	}
	if fromBytes != original {
		t.Errorf("HashFromBytes round trip = %s, want %s", fromBytes, original)
	}

	for _, bad := range []string{"", "zz", "not-hex", "abcd"} {
		if _, err := blob.ParseHash(bad); err == nil {
			t.Errorf("ParseHash(%q) returned no error", bad)
		}
	}
}
