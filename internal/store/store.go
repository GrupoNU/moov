package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned by every lookup that addresses a single row and
// finds none. Callers compare with errors.Is; the underlying pgx.ErrNoRows is
// deliberately not leaked, because it would tie every caller to the driver.
var ErrNotFound = errors.New("store: not found")

// Store is Moov's PostgreSQL persistence layer.
//
// It exposes methods, never SQL (L2 §4.3). The JMAP layer reads through this
// type and may not emit queries of its own — that is what keeps the
// performance envelope validated in spike S3 from being escaped by a caller
// who writes a reasonable-looking query that happens to fall off the composite
// GIN index.
//
// # Two pools, on purpose
//
// The interactive pool serves everything a user feels: message lists, folder
// views, and search shapes 1-8, all of which are bounded by LIMIT and measured
// at 3-24 ms p95 at 5M messages.
//
// The analytic pool serves only the two shapes that cannot be bounded that
// way: relevance ranking and result counting. S3 §4.3 measured what happens
// when they share: under 8-way concurrency, mixing them in drops throughput
// from 607 to 304 qps and takes the worst case from 678 ms to 68 seconds. One
// user sorting by relevance degrades search for everyone. Separating them,
// with a statement_timeout on the analytic side, bounds that blast radius.
type Store struct {
	pool     *pgxpool.Pool
	analytic *pgxpool.Pool

	// ownsPools is false when the caller supplied its own pools, in which case
	// Close must not shut them down.
	ownsPools bool
}

// Config configures the two pools.
type Config struct {
	// DSN is the PostgreSQL connection string. Required.
	DSN string

	// MaxConns bounds the interactive pool. Zero means pgx's default
	// (max(4, NumCPU)).
	MaxConns int32

	// AnalyticMaxConns bounds the analytic pool. It should stay small: its
	// queries are expensive by nature, and the point of the pool is to cap how
	// much of the server they can occupy at once. Zero means 2.
	AnalyticMaxConns int32

	// AnalyticStatementTimeout bounds any single rank or count query (S3 H5).
	// Zero means DefaultAnalyticStatementTimeout. A query that exceeds it
	// fails rather than dragging the whole instance's tail latency with it.
	AnalyticStatementTimeout time.Duration

	// StatementTimeout bounds interactive queries. Zero leaves the server
	// default (no timeout); it is offered because an interactive query that
	// runs for a second has already failed its purpose.
	StatementTimeout time.Duration
}

// Defaults for Config.
const (
	// DefaultAnalyticStatementTimeout is generous relative to the 134 ms p95
	// S3 measured for a bounded ranking query, because it is a backstop
	// against pathology, not a latency target.
	DefaultAnalyticStatementTimeout = 5 * time.Second
	DefaultAnalyticMaxConns         = 2
)

// Open connects both pools and verifies the database is reachable.
//
// It does not run migrations: that is Migrate, called explicitly by moovd on
// start, so that a process which only reads cannot silently alter the schema.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.DSN == "" {
		return nil, errors.New("store: DSN is required")
	}

	interactive, err := newPool(ctx, cfg.DSN, cfg.MaxConns, cfg.StatementTimeout)
	if err != nil {
		return nil, fmt.Errorf("interactive pool: %w", err)
	}

	analyticMax := cfg.AnalyticMaxConns
	if analyticMax <= 0 {
		analyticMax = DefaultAnalyticMaxConns
	}
	analyticTimeout := cfg.AnalyticStatementTimeout
	if analyticTimeout <= 0 {
		analyticTimeout = DefaultAnalyticStatementTimeout
	}
	analytic, err := newPool(ctx, cfg.DSN, analyticMax, analyticTimeout)
	if err != nil {
		interactive.Close()
		return nil, fmt.Errorf("analytic pool: %w", err)
	}

	return &Store{pool: interactive, analytic: analytic, ownsPools: true}, nil
}

// NewWithPools builds a Store over pools the caller already owns. Closing the
// returned Store does not close them.
//
// It exists for tests and for a daemon that manages its own pool lifecycle. If
// analytic is nil the interactive pool serves both roles — acceptable for a
// test, but not what production should do (see the type comment).
func NewWithPools(interactive, analytic *pgxpool.Pool) *Store {
	if analytic == nil {
		analytic = interactive
	}
	return &Store{pool: interactive, analytic: analytic}
}

func newPool(ctx context.Context, dsn string, maxConns int32, stmtTimeout time.Duration) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parsing DSN: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}

	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}

	// plan_cache_mode = force_custom_plan (S3 §5.1).
	//
	// Migration 0001 sets this at the database level, which covers every
	// connection. It is repeated here as a runtime parameter for the case that
	// setting cannot reach: a transaction-pooling PgBouncer does not reliably
	// carry ALTER DATABASE settings to the server connection (L2 §5, risk 2).
	// Setting it on the connection makes the guarantee independent of how the
	// connection was obtained.
	//
	// Without it, PostgreSQL switches to a generic plan after five executions
	// of a prepared statement. With the tsquery invisible to the planner, the
	// chosen plan materializes every row of the account: S3 measured
	// 19.4 ms -> 1,868 ms, a 145x regression that appears only after the fifth
	// query of a session and therefore never in development.
	cfg.ConnConfig.RuntimeParams["plan_cache_mode"] = "force_custom_plan"

	if stmtTimeout > 0 {
		cfg.ConnConfig.RuntimeParams["statement_timeout"] =
			fmt.Sprintf("%d", stmtTimeout.Milliseconds())
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("creating pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connecting: %w", err)
	}
	return pool, nil
}

// Close releases both pools, unless they were supplied by the caller.
func (s *Store) Close() {
	if !s.ownsPools {
		return
	}
	s.pool.Close()
	if s.analytic != s.pool {
		s.analytic.Close()
	}
}

// Pool exposes the interactive pool.
//
// It exists so tests and maintenance tasks (ANALYZE, a warm-up query) can
// reach the database without this package growing a method per chore. It is
// NOT the escape hatch for the JMAP layer: §4.3 is explicit that the API layer
// gets methods, and a query written against this pool has none of the
// guarantees the methods carry.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// AnalyticPool exposes the bounded rank/count pool, for the same reasons.
func (s *Store) AnalyticPool() *pgxpool.Pool { return s.analytic }

// InTx runs fn inside a transaction, committing on success and rolling back on
// error or panic.
//
// The rollback is deliberately best-effort: if fn already failed, a rollback
// error tells us nothing the original error does not, and returning it instead
// would hide the actual cause.
func (s *Store) InTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// isNoRows reports whether err is pgx's "query returned no rows".
func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// notFound maps pgx.ErrNoRows onto ErrNotFound, leaving every other error
// untouched and wrapped with context.
func notFound(err error, what string) error {
	if isNoRows(err) {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return fmt.Errorf("%s: %w", what, err)
}
