package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// The search repertoire.
//
// This file is the whole of what the JMAP layer may ask the database for, and
// that is deliberate (L2 §4.3). Spike S3 validated ten query shapes at 5M
// messages; eight passed with 4x-30x headroom and two failed for reasons no
// index can fix. Exposing them as METHODS rather than letting callers write
// SQL is what keeps that result true in production: a caller cannot invent a
// ninth shape that silently falls off the composite GIN index, and cannot
// forget the account scope or the LIMIT.
//
// Every method here therefore guarantees three things:
//   - account_id is always in the WHERE clause, and always first, so the
//     composite gin(account_id, tsv) index applies (S3 §5.2: without it, cost
//     scales with the whole installation instead of the user's mailbox — up to
//     6,600x on a rare term);
//   - there is always a LIMIT;
//   - ranking and counting run on the analytic pool and are bounded (S3 §6).

// Default and maximum result-set sizes.
const (
	// DefaultSearchLimit is one screen of results, which is what S3 measured.
	DefaultSearchLimit = 50
	// MaxSearchLimit caps what a caller may ask for. The shapes were validated
	// at 50; allowing an unbounded LIMIT would reintroduce exactly the
	// unbounded-work problem that sinks shapes #9 and #10.
	MaxSearchLimit = 200

	// RankCandidateWindow is how many recent matches the bounded relevance
	// sort scores (S3 mitigation #102). 200 rows cost 134 ms p95 on a
	// 1M-message account, against 892 ms for unbounded ranking. 500 rows was
	// measured too and costs 265 ms — outside any plausible budget.
	RankCandidateWindow = 200

	// DefaultCountCap is the capped-count ceiling (S3 mitigation #104): 98 ms
	// p95 against 452 ms for an exact count. The UI renders a capped result as
	// "199+", which is the affordance Gmail itself offers.
	DefaultCountCap = 200
)

// SearchQuery is a search request. Text is required; everything else narrows.
//
// The zero value of Limit means DefaultSearchLimit. This is not a general
// query language: fields that are not here cannot be filtered on, by design.
type SearchQuery struct {
	AccountID int64
	// Text is user input, parsed with websearch_to_tsquery: it accepts quoted
	// phrases, OR and -exclusion, and it never errors on malformed input —
	// which is what makes it safe to feed a search box directly.
	Text string

	// MailboxID restricts to one folder when non-nil.
	MailboxID *int64
	// Since restricts to messages at or after this instant when non-nil.
	Since *time.Time
	// UnreadOnly restricts to unread messages.
	UnreadOnly bool
	// Keyword restricts to messages carrying an IMAP keyword — which is how
	// labels are stored (A6).
	Keyword string

	// Limit caps the result set (default DefaultSearchLimit, max
	// MaxSearchLimit).
	Limit int
}

// SearchResult is one hit: enough to render a message-list row without a
// second query, and no more.
type SearchResult struct {
	MessageID int64
	Date      time.Time
	Subject   string
	FromAddr  string
	Preview   string
	MailboxID int64
	Flags     Flags
	// Keywords are the message's IMAP keywords — where user labels live after
	// arbitration A6.
	//
	// Added in J4 for a concrete client requirement: Bulwark sorts its message
	// list by `hasKeyword $pinned` before receivedAt (RFC 8621 §4.4.2 lists
	// hasKeyword as a SHOULD-support sort), and a sort cannot be evaluated over
	// rows that do not carry the value being sorted on. It rides along in the
	// SELECT rather than costing a second query: message_state is already
	// joined by primary key for the flags, so this is one more column from a
	// row the plan already touches.
	Keywords []string
	// Rank is set only by SearchByRelevance.
	Rank float32
}

// Search runs the interactive, date-ordered search: shapes #1-#8 of S3.
//
// Which shape a call becomes depends on the filters set, and all of them are
// served by the same plan family — a bitmap index scan on
// gin(account_id, tsv), then a sort by date that stops at LIMIT. Measured p95
// at 5M messages: 3.1-23.6 ms.
//
// The join to message_state exists because flags and the mailbox live there
// after arbitration A5. It is a primary-key join on message_id, which is why
// it does not disturb the plan.
func (s *Store) Search(ctx context.Context, q SearchQuery) ([]SearchResult, error) {
	if q.Text == "" {
		return nil, fmt.Errorf("search: text is required")
	}
	sql, args := q.build(false)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()
	return scanSearchResults(rows, false)
}

// SearchPrefix is search-as-you-type: shape #5, 5.0 ms p95.
//
// The last token becomes a prefix match. This method exists separately because
// websearch_to_tsquery has no prefix syntax, so the tsquery is built with
// to_tsquery — and to_tsquery DOES error on malformed input, which is why the
// term is sanitized here rather than passed through. Callers must never build
// this string themselves.
//
// It also carries the recall burden of the 'simple' configuration: with no
// stemming, "factura" does not match "facturas", and a prefix query is how the
// product recovers morphological variants (S3 §"open risks").
func (s *Store) SearchPrefix(ctx context.Context, q SearchQuery) ([]SearchResult, error) {
	term := sanitizePrefixTerm(q.Text)
	if term == "" {
		return nil, nil
	}
	q.Text = term
	sql, args := q.build(true)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("prefix search: %w", err)
	}
	defer rows.Close()
	return scanSearchResults(rows, false)
}

// SearchByRelevance is the BOUNDED relevance sort (S3 mitigation #102).
//
// It ranks only the RankCandidateWindow most recent matches, not every match.
// Unbounded ts_rank_cd must score every hit before it knows which are best —
// 34,814 rows for a common two-word query on a 1M-message account — which
// measured 892 ms p95 and, under concurrency, took the whole instance's worst
// case from 678 ms to 68 seconds (S3 §4.3).
//
// It runs on the analytic pool, which carries a statement_timeout, so a
// pathological query fails alone instead of degrading everyone's search.
//
// The product consequence, recorded so it is not rediscovered later: date sort
// is 9 ms and relevance is 134 ms, so date should stay the default and
// relevance should be an explicit opt-in.
func (s *Store) SearchByRelevance(ctx context.Context, q SearchQuery) ([]SearchResult, error) {
	if q.Text == "" {
		return nil, fmt.Errorf("search: text is required")
	}
	limit := q.effectiveLimit()

	where, args := q.conditions(false)
	// $-numbering continues after the conditions' arguments.
	windowArg := len(args) + 1
	limitArg := len(args) + 2
	args = append(args, RankCandidateWindow, limit)

	sql := `
		SELECT message_id, date, subject, from_addr, preview, mailbox_id, flags, keywords, rank
		  FROM (
			SELECT m.id AS message_id, m.date, m.subject, m.from_addr, m.preview,
			       ms.mailbox_id, ms.flags, ms.keywords,
			       ts_rank_cd(m.tsv, websearch_to_tsquery('simple', immutable_unaccent($2))) AS rank
			  FROM messages m
			  JOIN message_state ms ON ms.message_id = m.id
			 WHERE ` + where + `
			 ORDER BY m.date DESC
			 LIMIT $` + fmt.Sprint(windowArg) + `
		  ) candidates
		 ORDER BY rank DESC, date DESC
		 LIMIT $` + fmt.Sprint(limitArg)

	rows, err := s.analytic.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("relevance search: %w", err)
	}
	defer rows.Close()
	return scanSearchResults(rows, true)
}

// CountCapped counts matches up to a ceiling, reporting whether the ceiling
// was hit (S3 mitigation #104).
//
// capped == true means "at least count", which the UI renders as "199+". An
// exact count is NOT offered by this package: it measured 452 ms p95 at 1M
// messages because counting has no LIMIT shortcut and must visit every match,
// and Gmail itself shows "1-50 of many" rather than a true total. If an exact
// count is ever genuinely required, it needs its own justification and its own
// budget — not a quiet call to count(*).
//
// Runs on the analytic pool for the same reason as ranking.
func (s *Store) CountCapped(ctx context.Context, q SearchQuery, ceiling int) (count int, capped bool, err error) {
	if q.Text == "" {
		return 0, false, fmt.Errorf("count: text is required")
	}
	if ceiling <= 0 {
		ceiling = DefaultCountCap
	}

	where, args := q.conditions(false)
	capArg := len(args) + 1
	args = append(args, ceiling)

	sql := `
		SELECT count(*) FROM (
			SELECT 1
			  FROM messages m
			  JOIN message_state ms ON ms.message_id = m.id
			 WHERE ` + where + `
			 LIMIT $` + fmt.Sprint(capArg) + `
		) capped`

	if err := s.analytic.QueryRow(ctx, sql, args...).Scan(&count); err != nil {
		return 0, false, fmt.Errorf("capped count: %w", err)
	}
	return count, count >= ceiling, nil
}

// ListMailboxMessages is the folder view: no search text, newest first.
//
// It is here rather than in messages.go because it shares the account-scoped,
// always-limited discipline of the search methods and is served by the same
// (account_id, date DESC) index that shape #1 walks.
func (s *Store) ListMailboxMessages(ctx context.Context, accountID, mailboxID int64, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	if limit > MaxSearchLimit {
		limit = MaxSearchLimit
	}

	rows, err := s.pool.Query(ctx, `
		SELECT m.id, m.date, m.subject, m.from_addr, m.preview, ms.mailbox_id, ms.flags, ms.keywords
		  FROM messages m
		  JOIN message_state ms ON ms.message_id = m.id
		 WHERE m.account_id = $1 AND ms.mailbox_id = $2 AND ms.deleted_at IS NULL
		 ORDER BY m.date DESC
		 LIMIT $3`, accountID, mailboxID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing mailbox messages: %w", err)
	}
	defer rows.Close()
	return scanSearchResults(rows, false)
}

// ListAccountMessages is the account-wide view: every live message an account
// holds, newest first, regardless of mailbox.
//
// # Why this shape exists
//
// It closes the one gap the JMAP layer could not honestly paper over: RFC 8620
// §5.5 defines `filter: null` as "all objects in the account of this type", and
// until J4 the repertoire had no method for it, so Email/query answered
// unsupportedFilter. Two independent things then failed against real software:
//
//   - the official JMAP conformance suite (jmapio/jmap-test-suite) cannot even
//     START, because its account-cleaning setup step enumerates the account
//     before the first test runs;
//   - any client offering an "All Mail" view asks exactly this question.
//
// # Why it is safe to add
//
// It is the SAME plan family the eight validated S3 shapes use, minus a
// predicate: the (account_id, date DESC) index that serves shape #1 serves this
// directly, the account scope leads the WHERE clause, and the LIMIT is
// mandatory. Removing the mailbox predicate does not widen the scan — the index
// is already account-first — so this is bounded by the same LIMIT as every other
// method here rather than by how much mail the account happens to hold.
func (s *Store) ListAccountMessages(ctx context.Context, accountID int64, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	if limit > MaxSearchLimit {
		limit = MaxSearchLimit
	}

	rows, err := s.pool.Query(ctx, `
		SELECT m.id, m.date, m.subject, m.from_addr, m.preview, ms.mailbox_id, ms.flags, ms.keywords
		  FROM messages m
		  JOIN message_state ms ON ms.message_id = m.id
		 WHERE m.account_id = $1 AND ms.deleted_at IS NULL
		 ORDER BY m.date DESC
		 LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing account messages: %w", err)
	}
	defer rows.Close()
	return scanSearchResults(rows, false)
}

// ---------------------------------------------------------------------------
// query construction
// ---------------------------------------------------------------------------

func (q SearchQuery) effectiveLimit() int {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	if limit > MaxSearchLimit {
		limit = MaxSearchLimit
	}
	return limit
}

// conditions builds the shared WHERE clause and its arguments.
//
// $1 is always account_id and $2 is always the search text, so the tsv
// predicate leads with the account — the shape the composite GIN index is
// built for.
func (q SearchQuery) conditions(prefix bool) (string, []any) {
	// immutable_unaccent on the QUERY side as well as the indexed side.
	//
	// This is not symmetry for its own sake: the generated tsv column stores
	// unaccented lexemes ('accion'), so an accented search term ('acción')
	// produces the lexeme 'acción' and matches NOTHING. The failure is silent
	// — no error, just zero results — and it hits precisely the Spanish and
	// Portuguese mailboxes this installed base is made of. Spike S3 never saw
	// it because its generated corpus only ever queried unaccented terms.
	tsquery := `websearch_to_tsquery('simple', immutable_unaccent($2))`
	if prefix {
		// The prefix path builds its own tsquery text (see
		// sanitizePrefixTerm), so the :* markers must survive unaccenting —
		// they do: unaccent only rewrites accented letters.
		tsquery = `to_tsquery('simple', immutable_unaccent($2))`
	}

	conds := []string{
		"m.account_id = $1",
		"m.tsv @@ " + tsquery,
		"ms.deleted_at IS NULL",
	}
	args := []any{q.AccountID, q.Text}

	if q.MailboxID != nil {
		args = append(args, *q.MailboxID)
		conds = append(conds, fmt.Sprintf("ms.mailbox_id = $%d", len(args)))
	}
	if q.Since != nil {
		args = append(args, *q.Since)
		conds = append(conds, fmt.Sprintf("m.date >= $%d", len(args)))
	}
	if q.UnreadOnly {
		// Literal 1 rather than a parameter: it is the \Seen bit by
		// definition, and it matches the partial index predicate exactly.
		conds = append(conds, "(ms.flags & 1) = 0")
	}
	if q.Keyword != "" {
		args = append(args, q.Keyword)
		conds = append(conds, fmt.Sprintf("ms.keywords @> ARRAY[$%d]::text[]", len(args)))
	}

	return strings.Join(conds, " AND "), args
}

func (q SearchQuery) build(prefix bool) (string, []any) {
	where, args := q.conditions(prefix)
	args = append(args, q.effectiveLimit())

	sql := `
		SELECT m.id, m.date, m.subject, m.from_addr, m.preview, ms.mailbox_id, ms.flags, ms.keywords
		  FROM messages m
		  JOIN message_state ms ON ms.message_id = m.id
		 WHERE ` + where + `
		 ORDER BY m.date DESC
		 LIMIT $` + fmt.Sprint(len(args))

	return sql, args
}

func scanSearchResults(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}, withRank bool,
) ([]SearchResult, error) {
	var out []SearchResult
	for rows.Next() {
		var r SearchResult
		var flags int64
		var err error
		if withRank {
			err = rows.Scan(&r.MessageID, &r.Date, &r.Subject, &r.FromAddr,
				&r.Preview, &r.MailboxID, &flags, &r.Keywords, &r.Rank)
		} else {
			err = rows.Scan(&r.MessageID, &r.Date, &r.Subject, &r.FromAddr,
				&r.Preview, &r.MailboxID, &flags, &r.Keywords)
		}
		if err != nil {
			return nil, fmt.Errorf("scanning search result: %w", err)
		}
		r.Flags = flagsFromDB(flags)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading search results: %w", err)
	}
	return out, nil
}

// sanitizePrefixTerm turns raw user input into a safe to_tsquery argument.
//
// to_tsquery, unlike websearch_to_tsquery, is a strict parser: an unbalanced
// quote or a stray operator raises an error rather than degrading. Since this
// runs on every keystroke of search-as-you-type, an error is not an acceptable
// outcome, so the input is reduced to alphanumeric tokens joined with & and
// the final token given the :* prefix marker.
//
// This is defense in depth, not the injection barrier: the result is still
// passed as a bound parameter. It exists so a user typing "(" gets no results
// rather than an error.
func sanitizePrefixTerm(text string) string {
	fields := strings.Fields(strings.ToLower(text))
	tokens := make([]string, 0, len(fields))
	for _, f := range fields {
		var b strings.Builder
		for _, r := range f {
			// Unicode letters and digits are kept, so accented input survives
			// to reach unaccent(); everything else is a separator.
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r > 127:
				b.WriteRune(r)
			}
		}
		if b.Len() > 0 {
			tokens = append(tokens, b.String())
		}
	}
	if len(tokens) == 0 {
		return ""
	}
	// Only the last token is a prefix: the earlier ones are complete words the
	// user has finished typing.
	tokens[len(tokens)-1] += ":*"
	return strings.Join(tokens, " & ")
}
