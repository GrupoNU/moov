package store_test

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// THE SMOKE BENCHMARK (E3 acceptance criterion) — a CANARY, NOT A BENCHMARK.
//
// What this is NOT: spike S3. That ran 5M messages on a tuned VPS for hours
// and produced the numbers this project's search design is built on
// (spikes/s3-fts/RESULTS.md). Nothing here reproduces or replaces it, and the
// absolute milliseconds below are not comparable to S3's: a 20k-row corpus on
// CI hardware is a different machine, a different working set, and a different
// planner regime.
//
// What this IS: a tripwire. It loads a small synthetic corpus through the
// store's OWN batch API and checks that the eight interactive shapes still
// behave like indexed queries. It catches the class of regression that
// functional tests cannot see at all — a dropped composite index, a lost
// statistics target, a query rewritten so it falls off the index, a
// plan_cache_mode that reverted — each of which leaves every test green and
// makes search two orders of magnitude slower.
//
// It must stay under a minute so it can live in CI.
//
// Thresholds are deliberately loose (see smokeBudget): the point is to catch
// a 100x collapse, not to police a 20% drift on noisy shared runners.

const (
	// smokeCorpusSize is small enough to load in seconds and large enough that
	// a sequential scan is measurably worse than an index scan.
	smokeCorpusSize = 20_000

	// smokeBudget is the per-shape p95 ceiling. An indexed query on this
	// corpus lands in single-digit milliseconds; a sequential scan of 20k rows
	// with a tsvector match lands far above this. The gap is what makes the
	// threshold meaningful despite being loose.
	smokeBudget = 150 * time.Millisecond

	smokeRuns    = 30
	smokeWarmups = 5
)

// TestSearchSmokeBenchmark is the CI-facing canary. It is skipped without a
// database, and skipped in -short mode.
func TestSearchSmokeBenchmark(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke benchmark skipped in -short mode")
	}
	if os.Getenv(testDBEnv) == "" {
		t.Skipf("%s is not set; the smoke benchmark needs a real database", testDBEnv)
	}

	s := testStore(t)
	ctx := context.Background()

	acct, mbox := seedSmokeCorpus(t, s)
	results := runSmokeShapes(ctx, t, s, acct, mbox)
	budget := effectiveBudget()

	var failures int
	t.Log("shape latencies (p50 / p95), corpus = 20k messages:")
	for _, r := range results {
		t.Logf("  %-42s %7.2f ms / %7.2f ms",
			r.name, ms(r.p50), ms(r.p95))
		if r.p95 > budget {
			failures++
			t.Errorf("shape %q p95 = %.2f ms, over the %.0f ms canary budget.\n"+
				"This usually means an index is missing or a query stopped using one. Check:\n"+
				"  - the composite index gin(account_id, tsv) exists (S3 §5.2)\n"+
				"  - messages.tsv still has STATISTICS 4000 (S3 §5.3)\n"+
				"  - plan_cache_mode is force_custom_plan on the connection (S3 §5.1)",
				r.name, ms(r.p95), ms(budget))
		}
	}
	if failures == 0 {
		t.Logf("all %d shapes within the %.0f ms canary budget", len(results), ms(budget))
	}
}

// BenchmarkSearchShapes is the same repertoire under `go test -bench`, for a
// developer investigating a specific shape. It is not run by CI.
func BenchmarkSearchShapes(b *testing.B) {
	if os.Getenv(testDBEnv) == "" {
		b.Skipf("%s is not set", testDBEnv)
	}

	t := &testing.T{}
	s := testStore(t)
	if t.Failed() {
		b.Skip("could not open the store")
	}
	ctx := context.Background()

	acct, mbox := seedSmokeCorpusB(b, s)

	for _, shape := range smokeShapes(acct, mbox) {
		b.Run(shape.name, func(b *testing.B) {
			for range b.N {
				if err := shape.run(ctx, s); err != nil {
					b.Fatalf("%s: %v", shape.name, err)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// corpus
// ---------------------------------------------------------------------------

// seedSmokeCorpus loads smokeCorpusSize messages through InsertMessages — the
// store's own batch API, so the benchmark exercises the real write path rather
// than a COPY that production never uses.
func seedSmokeCorpus(t *testing.T, s *store.Store) (store.Account, store.Mailbox) {
	t.Helper()
	acct := newAccount(t, s)
	mbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)
	loadCorpus(t, s, acct.ID, mbox.ID)
	return acct, mbox
}

func seedSmokeCorpusB(b *testing.B, s *store.Store) (store.Account, store.Mailbox) {
	b.Helper()
	ctx := context.Background()

	email := fmt.Sprintf("bench-%d@example.test", time.Now().UnixNano())
	acct, err := s.CreateAccount(ctx, store.Account{Email: email, IMAPHost: "dovecot.internal"})
	if err != nil {
		b.Fatalf("CreateAccount: %v", err)
	}
	b.Cleanup(func() { _ = s.DeleteAccount(context.Background(), acct.ID) })

	mbox, err := s.UpsertMailbox(ctx, store.Mailbox{
		AccountID: acct.ID, Name: "INBOX", Role: store.RoleInbox,
		Subscribed: true, Selectable: true,
	})
	if err != nil {
		b.Fatalf("UpsertMailbox: %v", err)
	}
	loadCorpus(b, s, acct.ID, mbox.ID)
	return acct, mbox
}

// tb is the shared surface of *testing.T and *testing.B this file needs.
type tb interface {
	Helper()
	Fatalf(string, ...any)
	Logf(string, ...any)
}

func loadCorpus(t tb, s *store.Store, accountID, mailboxID int64) {
	t.Helper()
	ctx := context.Background()
	started := time.Now()

	// Vocabulary shaped like S3's: a few common words that appear everywhere,
	// and one rare needle. Zipfian in spirit without the machinery.
	common := []string{"reunion", "proyecto", "factura", "cliente", "informe", "equipo"}
	rare := "zanzibarita"

	// 2,000 per batch. The corpus load is not what this test measures, and a
	// bigger batch amortizes the per-statement overhead of pgx.Batch: at 500
	// the load dominated the test's runtime (~47 s of a 54 s run) purely in
	// round-trip overhead, against ~3 s for the same inserts server-side.
	const batchSize = 2000
	base := time.Now().UTC().Add(-time.Duration(smokeCorpusSize) * time.Minute)

	for offset := 0; offset < smokeCorpusSize; offset += batchSize {
		n := min(batchSize, smokeCorpusSize-offset)
		msgs := make([]store.NewMessage, n)

		// The blob rows this batch's messages reference, inserted together:
		// raw_sha256 is a foreign key, so they must exist first.
		hashes := make([][]byte, n)
		for i := range n {
			hashes[i] = smokeBlobHash(accountID, offset+i)
		}
		seedSmokeBlobs(t, s, hashes)

		for i := range n {
			idx := offset + i
			w1 := common[idx%len(common)]
			w2 := common[(idx/3)%len(common)]

			body := fmt.Sprintf(
				"mensaje %d sobre %s y %s con detalle adicional para dar cuerpo al texto indexado",
				idx, w1, w2)
			// The rare needle in ~0.05% of messages, as a selective term.
			if idx%2000 == 0 {
				body += " " + rare
			}

			msgs[i] = store.NewMessage{
				Message: store.Message{
					AccountID: accountID,
					RawSHA256: hashes[i],
					RawSize:   int64(len(body)),
					Subject:   fmt.Sprintf("%s %s %d", w1, w2, idx),
					FromAddr:  fmt.Sprintf("remitente%d@example.test", idx%500),
					ToAddrs:   "destinatario@example.test",
					BodyText:  body,
					Preview:   body[:min(60, len(body))],
					Date:      base.Add(time.Duration(idx) * time.Minute),
				},
				State: store.MessageState{
					AccountID: accountID, MailboxID: mailboxID,
					UID: int64(idx + 1), UIDValidity: 1,
					// ~30% unread, matching S3's corpus.
					Flags:      map[bool]store.Flags{true: 0, false: store.FlagSeen}[idx%10 < 3],
					ModSeqSeen: int64(idx),
				},
			}
		}

		if _, err := s.InsertMessages(ctx, msgs); err != nil {
			t.Fatalf("loading corpus at offset %d: %v", offset, err)
		}
	}

	// ANALYZE so the planner has statistics for a table that went from empty
	// to 20k rows in seconds. Without it the estimates are whatever autovacuum
	// last saw, which on a fresh table is nothing — and the shape of the plan
	// is exactly what this test is measuring.
	if _, err := s.Pool().Exec(ctx, `ANALYZE messages`); err != nil {
		t.Fatalf("ANALYZE: %v", err)
	}

	t.Logf("loaded %d messages in %s", smokeCorpusSize, time.Since(started).Round(time.Millisecond))
}

// smokeBlobHash builds the deterministic 32-byte identity of a corpus blob.
// sha256 of the identity would be needless work: uniqueness is all the foreign
// key requires here.
func smokeBlobHash(accountID int64, idx int) []byte {
	h := make([]byte, 32)
	copy(h, fmt.Sprintf("smoke-%d-%d", accountID, idx))
	return h
}

// seedSmokeBlobs inserts a whole batch of blob rows in ONE statement.
//
// One round trip per blob was the entire cost of this test: 20,000 sequential
// Exec calls took ~47 s of a 54 s run, against ~3 s for the same inserts done
// server-side. The bytes never exist on disk — this file measures the store,
// and internal/blob has its own tests for the filesystem half.
func seedSmokeBlobs(t tb, s *store.Store, hashes [][]byte) {
	t.Helper()
	if _, err := s.Pool().Exec(context.Background(), `
		INSERT INTO blobs (sha256, size, refcount)
		SELECT h, 1, 1 FROM unnest($1::bytea[]) AS h
		ON CONFLICT (sha256) DO NOTHING`, hashes); err != nil {
		t.Fatalf("seeding %d blobs: %v", len(hashes), err)
	}
}

// ---------------------------------------------------------------------------
// shapes
// ---------------------------------------------------------------------------

type smokeShape struct {
	name string
	run  func(context.Context, *store.Store) error
}

// smokeShapes mirrors S3's interactive shapes #1-#8, expressed through the
// store's methods rather than raw SQL — which is the point: these are the
// calls the JMAP layer will actually make.
func smokeShapes(acct store.Account, mbox store.Mailbox) []smokeShape {
	since := time.Now().UTC().Add(-90 * 24 * time.Hour)
	mboxID := mbox.ID

	q := func(text string) store.SearchQuery {
		return store.SearchQuery{AccountID: acct.ID, Text: text}
	}

	return []smokeShape{
		{"1_common_word_date_desc", func(ctx context.Context, s *store.Store) error {
			_, err := s.Search(ctx, q("reunion"))
			return err
		}},
		{"2_rare_word_date_desc", func(ctx context.Context, s *store.Store) error {
			_, err := s.Search(ctx, q("zanzibarita"))
			return err
		}},
		{"3_two_word_and", func(ctx context.Context, s *store.Store) error {
			_, err := s.Search(ctx, q("factura cliente"))
			return err
		}},
		{"4_phrase", func(ctx context.Context, s *store.Store) error {
			_, err := s.Search(ctx, q(`"detalle adicional"`))
			return err
		}},
		{"5_prefix_search_as_you_type", func(ctx context.Context, s *store.Store) error {
			_, err := s.SearchPrefix(ctx, q("factur"))
			return err
		}},
		{"6_common_word_mailbox_90d", func(ctx context.Context, s *store.Store) error {
			query := q("proyecto")
			query.MailboxID = &mboxID
			query.Since = &since
			return errOf(s.Search(ctx, query))
		}},
		{"7_common_word_unread", func(ctx context.Context, s *store.Store) error {
			query := q("informe")
			query.UnreadOnly = true
			return errOf(s.Search(ctx, query))
		}},
		{"8_from_address", func(ctx context.Context, s *store.Store) error {
			_, err := s.Search(ctx, q("remitente42@example.test"))
			return err
		}},
		// The two bounded mitigations, measured so a regression in THEM is
		// visible too — they are the shapes with the least headroom.
		{"9_bounded_relevance", func(ctx context.Context, s *store.Store) error {
			_, err := s.SearchByRelevance(ctx, q("factura cliente"))
			return err
		}},
		{"10_capped_count", func(ctx context.Context, s *store.Store) error {
			_, _, err := s.CountCapped(ctx, q("reunion"), store.DefaultCountCap)
			return err
		}},
	}
}

func errOf(_ []store.SearchResult, err error) error { return err }

type shapeResult struct {
	name     string
	p50, p95 time.Duration
}

func runSmokeShapes(ctx context.Context, t *testing.T, s *store.Store, acct store.Account, mbox store.Mailbox) []shapeResult {
	t.Helper()

	var out []shapeResult
	for _, shape := range smokeShapes(acct, mbox) {
		for range smokeWarmups {
			if err := shape.run(ctx, s); err != nil {
				t.Fatalf("warming up %s: %v", shape.name, err)
			}
		}

		samples := make([]time.Duration, 0, smokeRuns)
		for range smokeRuns {
			started := time.Now()
			if err := shape.run(ctx, s); err != nil {
				t.Fatalf("running %s: %v", shape.name, err)
			}
			samples = append(samples, time.Since(started))
		}

		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		out = append(out, shapeResult{
			name: shape.name,
			p50:  percentile(samples, 50),
			p95:  percentile(samples, 95),
		})
	}
	return out
}

// percentile returns the nearest-rank percentile, matching how S3 reported.
func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := (p*len(sorted) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

func ms(d time.Duration) float64 {
	return float64(d.Nanoseconds()) / 1e6
}

// effectiveBudget is smokeBudget, unless an operator loosened it for a slow or
// heavily shared runner: MOOV_SMOKE_BUDGET_MS=500.
func effectiveBudget() time.Duration {
	if v := os.Getenv("MOOV_SMOKE_BUDGET_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return smokeBudget
}
