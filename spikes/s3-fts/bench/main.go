// Command bench runs the Spike S3 query-latency benchmark against the loaded
// 5M-message corpus and prints Markdown-ready tables.
//
// Modes:
//
//	-mode needles      assert the planted-needle counts (correctness gate)
//	-mode warm         warm-cache latency, n runs per shape per account
//	-mode cold         cold-cache latency, first-run after restart
//	-mode explain      EXPLAIN (ANALYZE, BUFFERS) for every shape
//	-mode inserts      incremental-insert cost with the GIN index live
//	-mode concurrency  8 parallel clients running the mix for 60 s
//	-mode mitigation   the shape-#1 rescue variants
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Accounts under test: the 1M-message power user and a median (~27k) account.
const (
	bigAccount    = 1
	medianAccount = 0 // resolved at runtime to the account nearest the median
)

var corpusParams = params{
	// "factura" is a high-frequency business word in the Spanish list; it
	// lands in a large share of messages, which is the point of shape #1.
	CommonWord: "factura",
	// The planted needle: 37 messages corpus-wide.
	RareWord:   "zanzibarita",
	TwoWordAND: "factura vencimiento",
	Phrase:     `"quetzal ferroviario nocturno"`,
	PrefixTerm: "factur",
	MailboxID:  1, // INBOX
}

func main() {
	var (
		mode    = flag.String("mode", "warm", "needles|warm|cold|explain|inserts|concurrency|mitigation|sizes")
		runs    = flag.Int("runs", 50, "runs per shape (warm)")
		warmups = flag.Int("warmups", 5, "warm-up runs per shape before timing")
		dsn     = flag.String("dsn", envOr("PGDSN", "postgres://postgres:s3bench_throwaway@localhost/moov_s3"), "postgres DSN")
		dur     = flag.Duration("duration", 60*time.Second, "concurrency test duration")
		clients = flag.Int("clients", 8, "concurrency test clients")
		// Shapes #9 (ts_rank_cd) and #10 (exact count) have no LIMIT shortcut
		// and dominate the tail under load; -mix lets the concurrency run
		// measure the interactive mix with and without them.
		mix = flag.String("mix", "all", "concurrency shape mix: all|interactive (excludes #9,#10)")
		// FINDING (see RESULTS.md §"generic plan trap"): pgx uses prepared
		// statements, so after five executions PostgreSQL switches to a
		// GENERIC plan that cannot see the tsquery's selectivity. It then
		// picks a BitmapAnd that materialises every row of the account —
		// 1.9 s instead of 19 ms on shape #3. force_custom_plan makes the
		// planner re-plan per execution with the actual parameter values.
		customPlan = flag.Bool("custom-plan", true, "SET plan_cache_mode = force_custom_plan on every connection")
	)
	flag.Parse()

	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(*dsn)
	must(err)
	if *customPlan {
		cfg.ConnConfig.RuntimeParams["plan_cache_mode"] = "force_custom_plan"
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	must(err)
	defer pool.Close()

	medAcct := resolveMedianAccount(ctx, pool)
	fromAddr := resolveFrequentSender(ctx, pool, bigAccount)
	corpusParams.FromAddr = fromAddr
	fmt.Printf("<!-- median account = %d, frequent sender = %s -->\n\n", medAcct, fromAddr)

	switch *mode {
	case "needles":
		runNeedles(ctx, pool)
	case "warm":
		runLatency(ctx, pool, shapes, []int{bigAccount, medAcct}, *runs, *warmups, "Warm-cache")
	case "cold":
		// Cold pass: few runs, no warm-up — the container was just restarted.
		runLatency(ctx, pool, shapes, []int{bigAccount, medAcct}, 10, 0, "Cold-cache")
	case "mitigation":
		runLatency(ctx, pool, mitigationShapes, []int{bigAccount, medAcct}, *runs, *warmups, "Mitigation warm-cache")
	case "explain":
		runExplain(ctx, pool, append(append([]shape{}, shapes...), mitigationShapes...), []int{bigAccount, medAcct})
	case "inserts":
		runInserts(ctx, pool)
	case "concurrency":
		runConcurrency(ctx, pool, cfg.ConnConfig, *clients, *dur, medAcct, *mix)
	case "sizes":
		runSizes(ctx, pool)
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q\n", *mode)
		os.Exit(2)
	}
}

// ---------------------------------------------------------------------------
// Corpus introspection
// ---------------------------------------------------------------------------

// resolveMedianAccount picks the account whose message count is closest to the
// median, so the "small mailbox" column reflects a typical user rather than a
// hand-picked one.
func resolveMedianAccount(ctx context.Context, pool *pgxpool.Pool) int {
	var acct int
	err := pool.QueryRow(ctx, `
		WITH c AS (
		  SELECT account_id, count(*) n FROM messages GROUP BY account_id
		), m AS (
		  SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY n) med FROM c
		)
		SELECT account_id FROM c, m ORDER BY abs(c.n - m.med) LIMIT 1`).Scan(&acct)
	must(err)
	return acct
}

// resolveFrequentSender returns a correspondent that actually appears in the
// account, so shape #8 measures a real weight-B hit rather than an empty set.
func resolveFrequentSender(ctx context.Context, pool *pgxpool.Pool, acct int) string {
	var addr string
	err := pool.QueryRow(ctx, `
		SELECT from_addr FROM messages WHERE account_id = $1
		GROUP BY from_addr ORDER BY count(*) DESC LIMIT 1`, acct).Scan(&addr)
	must(err)
	// Search the local-part token; the '@' and dots are token separators for
	// the 'simple' parser, so searching the full address would be a phrase.
	if i := strings.IndexByte(addr, '@'); i > 0 {
		addr = addr[:i]
	}
	return addr
}

// ---------------------------------------------------------------------------
// Correctness: planted needles
// ---------------------------------------------------------------------------

func runNeedles(ctx context.Context, pool *pgxpool.Pool) {
	type check struct {
		name string
		sql  string
		arg  any
		want int64
	}
	checks := []check{
		{"rare token 'zanzibarita' (corpus-wide)",
			`SELECT count(*) FROM messages WHERE tsv @@ websearch_to_tsquery('simple',$1)`,
			"zanzibarita", 37},
		{"unique invoice 'INV-2024-0857' (corpus-wide)",
			`SELECT count(*) FROM messages WHERE tsv @@ websearch_to_tsquery('simple',$1)`,
			"INV-2024-0857", 1},
		{"phrase 'quetzal ferroviario nocturno' (corpus-wide)",
			`SELECT count(*) FROM messages WHERE tsv @@ websearch_to_tsquery('simple',$1)`,
			`"quetzal ferroviario nocturno"`, 5},
		{"rare token in account 1",
			`SELECT count(*) FROM messages WHERE account_id=1 AND tsv @@ websearch_to_tsquery('simple',$1)`,
			"zanzibarita", 10},
		{"rare token in account 2",
			`SELECT count(*) FROM messages WHERE account_id=2 AND tsv @@ websearch_to_tsquery('simple',$1)`,
			"zanzibarita", 8},
		{"total corpus size",
			`SELECT count(*) FROM messages WHERE $1::text IS NOT NULL`, "x", 5000000},
	}

	fmt.Println("| Check | Expected | Actual | Result |")
	fmt.Println("|---|---:|---:|---|")
	allOK := true
	for _, c := range checks {
		var got int64
		must(pool.QueryRow(ctx, c.sql, c.arg).Scan(&got))
		status := "PASS"
		if got != c.want {
			status = "**FAIL**"
			allOK = false
		}
		fmt.Printf("| %s | %d | %d | %s |\n", c.name, c.want, got, status)
	}
	fmt.Println()
	if allOK {
		fmt.Println("All needle checks PASS.")
	} else {
		fmt.Println("**Needle checks FAILED — latency numbers below are not trustworthy.**")
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Latency measurement
// ---------------------------------------------------------------------------

type stats struct {
	p50, p95, p99, min, max, mean float64
	rows                          int64
	n                             int
}

func summarize(ds []time.Duration, rows int64) stats {
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }
	pct := func(p float64) float64 {
		if len(ds) == 0 {
			return 0
		}
		// nearest-rank percentile
		i := int(math.Ceil(p*float64(len(ds)))) - 1
		if i < 0 {
			i = 0
		}
		if i >= len(ds) {
			i = len(ds) - 1
		}
		return ms(ds[i])
	}
	var sum float64
	for _, d := range ds {
		sum += ms(d)
	}
	return stats{
		p50: pct(0.50), p95: pct(0.95), p99: pct(0.99),
		min: ms(ds[0]), max: ms(ds[len(ds)-1]),
		mean: sum / float64(len(ds)), rows: rows, n: len(ds),
	}
}

// timeQuery runs the query once and returns wall-clock including row drain,
// which is what the API layer actually pays.
func timeQuery(ctx context.Context, pool *pgxpool.Pool, sql string, args []any) (time.Duration, int64, error) {
	start := time.Now()
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return 0, 0, err
	}
	var n int64
	for rows.Next() {
		n++
	}
	err = rows.Err()
	rows.Close()
	return time.Since(start), n, err
}

func runLatency(ctx context.Context, pool *pgxpool.Pool, sh []shape, accts []int, runs, warmups int, label string) {
	fmt.Printf("### %s latency (n=%d per cell)\n\n", label, runs)
	fmt.Println("| # | Shape | Account | Msgs | Rows | p50 ms | p95 ms | p99 ms | min | max |")
	fmt.Println("|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|")

	for _, s := range sh {
		for _, a := range accts {
			args := s.ArgsFor(a, corpusParams)
			for i := 0; i < warmups; i++ {
				if _, _, err := timeQuery(ctx, pool, s.SQL, args); err != nil {
					fmt.Printf("| %d | %s | %d | | ERROR | %v |\n", s.ID, s.Name, a, err)
					goto next
				}
			}
			{
				ds := make([]time.Duration, 0, runs)
				var rowsOut int64
				fail := false
				for i := 0; i < runs; i++ {
					d, n, err := timeQuery(ctx, pool, s.SQL, args)
					if err != nil {
						fmt.Printf("| %d | %s | %d | | ERROR: %v |\n", s.ID, s.Name, a, err)
						fail = true
						break
					}
					ds = append(ds, d)
					rowsOut = n
				}
				if fail {
					goto next
				}
				st := summarize(ds, rowsOut)
				fmt.Printf("| %d | %s | %d | %s | %d | %.1f | %.1f | %.1f | %.1f | %.1f |\n",
					s.ID, s.Name, a, acctSize(ctx, pool, a), st.rows,
					st.p50, st.p95, st.p99, st.min, st.max)
			}
		next:
		}
	}
	fmt.Println()
}

var acctSizeCache = map[int]string{}

func acctSize(ctx context.Context, pool *pgxpool.Pool, a int) string {
	if v, ok := acctSizeCache[a]; ok {
		return v
	}
	var n int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE account_id=$1`, a).Scan(&n); err != nil {
		return "?"
	}
	v := fmt.Sprintf("%d", n)
	acctSizeCache[a] = v
	return v
}

// ---------------------------------------------------------------------------
// EXPLAIN
// ---------------------------------------------------------------------------

func runExplain(ctx context.Context, pool *pgxpool.Pool, sh []shape, accts []int) {
	for _, s := range sh {
		for _, a := range accts {
			args := s.ArgsFor(a, corpusParams)
			// warm first so the plan reflects a warm cache
			for i := 0; i < 3; i++ {
				timeQuery(ctx, pool, s.SQL, args)
			}
			fmt.Printf("#### Shape %d — %s — account %d\n\n```\n", s.ID, s.Name, a)
			rows, err := pool.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS, TIMING ON) "+s.SQL, args...)
			if err != nil {
				fmt.Printf("ERROR: %v\n", err)
			} else {
				for rows.Next() {
					var line string
					rows.Scan(&line)
					fmt.Println(line)
				}
				rows.Close()
			}
			fmt.Println("```")
			fmt.Println()
		}
	}
}

// ---------------------------------------------------------------------------
// Incremental insert cost
// ---------------------------------------------------------------------------

// runInserts measures what the sync engine actually pays: continuous small
// batches against a live GIN index. The GIN "pending list" (fastupdate=on, the
// default) makes individual inserts cheap but defers work to whichever
// unlucky query triggers the flush — so we also measure search latency right
// after each batch.
func runInserts(ctx context.Context, pool *pgxpool.Pool) {
	const batches = 30
	const batchSize = 100

	for _, fastupdate := range []bool{true, false} {
		_, err := pool.Exec(ctx, fmt.Sprintf(
			"ALTER INDEX messages_tsv_gin SET (fastupdate = %v)", fastupdate))
		must(err)
		// Force the setting to take effect and drain any pending list.
		_, err = pool.Exec(ctx, "VACUUM (ANALYZE) messages")
		must(err)

		fmt.Printf("### Incremental inserts — fastupdate = %v\n\n", fastupdate)
		fmt.Println("| Batch | Insert ms (100 rows) | Post-batch search ms |")
		fmt.Println("|---:|---:|---:|")

		var insDs, srchDs []time.Duration
		for b := 0; b < batches; b++ {
			start := time.Now()
			_, err := pool.Exec(ctx, `
				INSERT INTO messages (account_id, mailbox_id, uid, date, flags, from_addr, to_addrs, subject, body_text)
				SELECT 1, 1, 90000000 + g, now(), 1,
				       'sync.probe@example.com', 'inbox@example.com',
				       'probe batch subject factura pendiente ' || g,
				       'cuerpo del mensaje de prueba con factura y vencimiento numero ' || g ||
				       ' texto adicional para dar volumen realista al indice invertido de prueba'
				FROM generate_series(1, $1) g`, batchSize)
			must(err)
			insD := time.Since(start)

			// Immediately search: if the pending list just grew, this query
			// may pay the flush.
			sd, _, err := timeQuery(ctx, pool,
				`SELECT id FROM messages WHERE account_id=$1 AND tsv @@ websearch_to_tsquery('simple',$2)
				 ORDER BY date DESC LIMIT 50`,
				[]any{1, corpusParams.CommonWord})
			must(err)

			insDs = append(insDs, insD)
			srchDs = append(srchDs, sd)
			if b < 10 || b%5 == 0 {
				fmt.Printf("| %d | %.1f | %.1f |\n", b+1,
					float64(insD.Microseconds())/1000, float64(sd.Microseconds())/1000)
			}
		}
		si := summarize(insDs, 0)
		ss := summarize(srchDs, 0)
		fmt.Printf("\n**Insert p50/p95/max: %.1f / %.1f / %.1f ms.  Post-batch search p50/p95/max: %.1f / %.1f / %.1f ms**\n\n",
			si.p50, si.p95, si.max, ss.p50, ss.p95, ss.max)

		// Clean up the probe rows so the corpus stays at 5M for later modes.
		_, err = pool.Exec(ctx, "DELETE FROM messages WHERE uid >= 90000000 AND account_id = 1")
		must(err)
	}
	// Restore the default.
	_, err := pool.Exec(ctx, "ALTER INDEX messages_tsv_gin SET (fastupdate = on)")
	must(err)
	_, err = pool.Exec(ctx, "VACUUM (ANALYZE) messages")
	must(err)
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

func runConcurrency(ctx context.Context, pool *pgxpool.Pool, connCfg *pgx.ConnConfig, clients int, dur time.Duration, medAcct int, mix string) {
	active := shapes
	if mix == "interactive" {
		active = nil
		for _, s := range shapes {
			if s.ID != 9 && s.ID != 10 {
				active = append(active, s)
			}
		}
	}
	// Distinct accounts per client so they do not all hammer the same cache
	// footprint — this is the realistic multi-user shape.
	accts := []int{1, 2, 3, 46, 52, 58, medAcct, 17}
	for len(accts) < clients {
		accts = append(accts, accts[len(accts)%len(accts)])
	}

	var wg sync.WaitGroup
	results := make([][]time.Duration, clients)
	deadline := time.Now().Add(dur)

	for c := 0; c < clients; c++ {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			conn, err := pgx.ConnectConfig(ctx, connCfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "client %d: %v\n", c, err)
				return
			}
			defer conn.Close(ctx)
			acct := accts[c%len(accts)]
			var ds []time.Duration
			i := 0
			for time.Now().Before(deadline) {
				s := active[i%len(active)]
				i++
				args := s.ArgsFor(acct, corpusParams)
				start := time.Now()
				rows, err := conn.Query(ctx, s.SQL, args...)
				if err != nil {
					continue
				}
				for rows.Next() {
				}
				rows.Close()
				ds = append(ds, time.Since(start))
			}
			results[c] = ds
		}(c)
	}
	wg.Wait()

	var all []time.Duration
	total := 0
	for _, ds := range results {
		all = append(all, ds...)
		total += len(ds)
	}
	if len(all) == 0 {
		fmt.Println("no samples collected")
		return
	}
	st := summarize(all, 0)
	fmt.Printf("### Concurrency: %d clients × %s (mix=%s)\n\n", clients, dur, mix)
	fmt.Printf("- Total queries: **%d** (%.0f qps)\n", total, float64(total)/dur.Seconds())
	fmt.Printf("- Aggregate p50 / p95 / p99: **%.1f / %.1f / %.1f ms** (max %.1f)\n\n", st.p50, st.p95, st.p99, st.max)
}

// ---------------------------------------------------------------------------
// Sizes
// ---------------------------------------------------------------------------

func runSizes(ctx context.Context, pool *pgxpool.Pool) {
	rows, err := pool.Query(ctx, `
		SELECT 'table (heap+toast, no indexes)', pg_size_pretty(pg_table_size('messages'))
		UNION ALL SELECT 'heap only', pg_size_pretty(pg_relation_size('messages'))
		UNION ALL SELECT 'toast', pg_size_pretty(pg_total_relation_size(reltoastrelid))
		         FROM pg_class WHERE relname='messages' AND reltoastrelid <> 0
		UNION ALL SELECT 'GIN index on tsv', pg_size_pretty(pg_relation_size('messages_tsv_gin'))
		UNION ALL SELECT 'btree (account_id,date)', pg_size_pretty(pg_relation_size('messages_acct_date'))
		UNION ALL SELECT 'btree (account_id,mailbox_id,date)', pg_size_pretty(pg_relation_size('messages_acct_mbox_date'))
		UNION ALL SELECT 'TOTAL with indexes', pg_size_pretty(pg_total_relation_size('messages'))`)
	must(err)
	fmt.Println("| Object | Size |")
	fmt.Println("|---|---:|")
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		fmt.Printf("| %s | %s |\n", k, v)
	}
	rows.Close()

	// tsv column contribution: measured by summing the on-disk length of the
	// tsvector values over a sample and extrapolating (pg_column_size includes
	// the varlena header and any compression).
	var avgTsv, avgBody float64
	err = pool.QueryRow(ctx, `
		SELECT avg(pg_column_size(tsv)), avg(pg_column_size(body_text))
		FROM (SELECT tsv, body_text FROM messages TABLESAMPLE SYSTEM (0.2)) s`).Scan(&avgTsv, &avgBody)
	if err == nil {
		fmt.Printf("\n- Mean `pg_column_size(tsv)`: **%.0f bytes** → ~%.1f GB over 5M rows\n",
			avgTsv, avgTsv*5e6/1e9)
		fmt.Printf("- Mean `pg_column_size(body_text)`: **%.0f bytes** → ~%.1f GB over 5M rows\n",
			avgBody, avgBody*5e6/1e9)
	}
	fmt.Println()
}

// ---------------------------------------------------------------------------

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func must(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}
