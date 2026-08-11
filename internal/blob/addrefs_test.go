package blob_test

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/GrupoNU/moov/internal/blob"
)

// AddRefs: the batched reference path E6 added.
//
// The properties it must preserve are exactly AddRef's — lock the blob row
// before touching blob_refs, and RECOMPUTE the count from the rows rather than
// incrementing it — because those are what make the count correct under
// concurrency rather than usually right. Batching may only amortize them.

// TestAddRefsRecordsEveryReference is the basic contract.
func TestAddRefsRecordsEveryReference(t *testing.T) {
	bs, pool, accountID := testEnv(t)
	ctx := context.Background()

	// Three distinct blobs, each referenced by two different messages.
	hashes := make([]blob.Hash, 0, 3)
	for i := range 3 {
		h, _, err := bs.Put(ctx, bytes.NewReader([]byte(fmt.Sprintf("addrefs blob %d", i))))
		if err != nil {
			t.Fatalf("Put: %v", err)
		}
		hashes = append(hashes, h)
	}

	var refs []blob.Ref
	for i, h := range hashes {
		for owner := range 2 {
			refs = append(refs, blob.Ref{
				Hash:      h,
				AccountID: accountID,
				Kind:      blob.OwnerMessage,
				OwnerID:   int64(i*10 + owner + 1),
			})
		}
	}

	if err := inTx(t, pool, func(tx pgx.Tx) error {
		return blob.AddRefs(ctx, tx, refs)
	}); err != nil {
		t.Fatalf("AddRefs: %v", err)
	}

	for i, h := range hashes {
		count, err := bs.RefCount(ctx, h)
		if err != nil {
			t.Fatalf("RefCount(%d): %v", i, err)
		}
		if count != 2 {
			t.Errorf("blob %d has refcount %d, want 2", i, count)
		}
	}
}

// TestAddRefsIsIdempotent covers a retried batch, which the sync engine
// produces whenever a deadlock is retried or a pass repeats work.
func TestAddRefsIsIdempotent(t *testing.T) {
	bs, pool, accountID := testEnv(t)
	ctx := context.Background()

	h, _, err := bs.Put(ctx, bytes.NewReader([]byte("idempotent addrefs")))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	refs := []blob.Ref{
		{Hash: h, AccountID: accountID, Kind: blob.OwnerMessage, OwnerID: 1},
		{Hash: h, AccountID: accountID, Kind: blob.OwnerMessage, OwnerID: 2},
	}

	for range 3 {
		if err := inTx(t, pool, func(tx pgx.Tx) error {
			return blob.AddRefs(ctx, tx, refs)
		}); err != nil {
			t.Fatalf("AddRefs: %v", err)
		}
	}

	count, err := bs.RefCount(ctx, h)
	if err != nil {
		t.Fatalf("RefCount: %v", err)
	}
	if count != 2 {
		t.Errorf("refcount = %d after three identical batches, want 2", count)
	}
}

// TestAddRefsDeduplicatesWithinOneBatch covers a batch naming the same
// (blob, owner) twice, which is harmless precisely because the count is derived
// from the rows that exist rather than from how many inserts were attempted.
func TestAddRefsDeduplicatesWithinOneBatch(t *testing.T) {
	bs, pool, accountID := testEnv(t)
	ctx := context.Background()

	h, _, err := bs.Put(ctx, bytes.NewReader([]byte("duplicate inside a batch")))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	ref := blob.Ref{Hash: h, AccountID: accountID, Kind: blob.OwnerMessage, OwnerID: 1}
	if err := inTx(t, pool, func(tx pgx.Tx) error {
		return blob.AddRefs(ctx, tx, []blob.Ref{ref, ref, ref})
	}); err != nil {
		t.Fatalf("AddRefs: %v", err)
	}

	count, err := bs.RefCount(ctx, h)
	if err != nil {
		t.Fatalf("RefCount: %v", err)
	}
	if count != 1 {
		t.Errorf("refcount = %d after a batch naming one reference three times, want 1", count)
	}
}

// TestConcurrentAddRefsOverlappingBatches is the test that matters.
//
// Blobs are content-addressed and therefore SHARED: two accounts holding the
// same message — a mailing list post, a company-wide announcement — reference
// one row. So concurrent batches routinely want an overlapping set of blob
// rows, and in ARRIVAL order they take those locks in different sequences and
// deadlock (SQLSTATE 40P01). That is not hypothetical: E5's bulk-migration test
// found it within seconds of running several accounts at once.
//
// AddRefs sorts by hash internally, giving every transaction one global lock
// order, which makes the cycle impossible rather than merely unlikely. This
// test runs deliberately overlapping batches in deliberately opposite orders
// and requires both no deadlock and an exactly correct final count.
func TestConcurrentAddRefsOverlappingBatches(t *testing.T) {
	bs, pool, accountID := testEnv(t)
	ctx := context.Background()

	// A pool of shared blobs every writer will reference.
	const blobs = 12
	hashes := make([]blob.Hash, 0, blobs)
	for i := range blobs {
		h, _, err := bs.Put(ctx, bytes.NewReader([]byte(fmt.Sprintf("shared blob %d", i))))
		if err != nil {
			t.Fatalf("Put: %v", err)
		}
		hashes = append(hashes, h)
	}

	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)

	for w := range writers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()

			// Every writer references EVERY blob, and the odd ones build their
			// batch in reverse — the arrangement that deadlocks without a
			// canonical lock order.
			refs := make([]blob.Ref, 0, blobs)
			for i := range blobs {
				idx := i
				if w%2 == 1 {
					idx = blobs - 1 - i
				}
				refs = append(refs, blob.Ref{
					Hash:      hashes[idx],
					AccountID: accountID,
					Kind:      blob.OwnerMessage,
					OwnerID:   int64(w*100 + idx + 1),
				})
			}

			if err := inTx(t, pool, func(tx pgx.Tx) error {
				return blob.AddRefs(ctx, tx, refs)
			}); err != nil {
				errs <- fmt.Errorf("writer %d: %w", w, err)
			}
		}(w)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("AddRefs under contention: %v", err)
	}

	// Every blob must be referenced by exactly one owner per writer, and the
	// denormalized count must agree with the rows it summarizes.
	for i, h := range hashes {
		count, err := bs.RefCount(ctx, h)
		if err != nil {
			t.Fatalf("RefCount(%d): %v", i, err)
		}
		if count != writers {
			t.Errorf("blob %d has refcount %d, want %d", i, count, writers)
		}

		var rows int64
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM blob_refs WHERE sha256 = $1`, h.Bytes()).Scan(&rows); err != nil {
			t.Fatalf("counting refs: %v", err)
		}
		if rows != count {
			t.Errorf("blob %d: refcount %d disagrees with %d blob_refs rows", i, count, rows)
		}
	}
}

// TestAddRefsIsTransactional keeps the property the whole design rests on: a
// reference commits with whatever created it, or not at all.
func TestAddRefsIsTransactional(t *testing.T) {
	bs, pool, accountID := testEnv(t)
	ctx := context.Background()

	h, _, err := bs.Put(ctx, bytes.NewReader([]byte("rolled back addrefs")))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := blob.AddRefs(ctx, tx, []blob.Ref{
		{Hash: h, AccountID: accountID, Kind: blob.OwnerMessage, OwnerID: 1},
	}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("AddRefs: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	count, err := bs.RefCount(ctx, h)
	if err != nil {
		t.Fatalf("RefCount: %v", err)
	}
	if count != 0 {
		t.Errorf("refcount = %d after a rolled-back AddRefs, want 0", count)
	}
}

// TestAddRefsRejectsAMissingBlob keeps AddRef's rule: never create the blob row
// here, because that would point a reference at bytes that are not on disk.
func TestAddRefsRejectsAMissingBlob(t *testing.T) {
	_, pool, accountID := testEnv(t)
	ctx := context.Background()

	var absent blob.Hash
	for i := range absent {
		absent[i] = 0xAB
	}

	err := inTx(t, pool, func(tx pgx.Tx) error {
		return blob.AddRefs(ctx, tx, []blob.Ref{
			{Hash: absent, AccountID: accountID, Kind: blob.OwnerMessage, OwnerID: 1},
		})
	})
	if err == nil {
		t.Fatal("AddRefs referenced a blob that does not exist")
	}
}

// TestAddRefsEmptyIsANoOp keeps the trivial case from opening a transaction's
// worth of work.
func TestAddRefsEmptyIsANoOp(t *testing.T) {
	_, pool, _ := testEnv(t)
	ctx := context.Background()

	if err := inTx(t, pool, func(tx pgx.Tx) error {
		return blob.AddRefs(ctx, tx, nil)
	}); err != nil {
		t.Errorf("AddRefs(nil) = %v, want nil", err)
	}
}

// inTx runs fn in a transaction, committing on success.
func inTx(t *testing.T, pool interface {
	Begin(context.Context) (pgx.Tx, error)
}, fn func(pgx.Tx) error,
) error {
	t.Helper()
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}
