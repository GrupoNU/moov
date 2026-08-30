package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// The COLLAPSED repertoire: one row per conversation instead of one per message.
//
// # What this answers, and why it is a repertoire method rather than a caller's
// post-filter
//
// RFC 8621 §4.4.3 defines the Email/query argument that turns a message list
// into a conversation list:
//
//	"collapseThreads: Boolean (default: false) — If true, Emails in the same
//	 Thread as a previous Email in the list (given the filter and sort order)
//	 will be removed from the list."
//
// A caller could try to serve that above the store: fetch a window of messages,
// keep the first of each thread, and page until enough survive. That is exactly
// what this method exists to prevent. The collapse ratio is data-dependent — the
// pilot's largest real thread is 24 messages (W4b) and a mailing-list folder is
// worse — so a caller-side collapse of a 200-row window can return as few as ONE
// row, and the only recovery is fetching more windows until the page fills. That
// is a loop whose depth the DATA chooses, which is precisely the unbounded work
// L2 §4.3 forbids and S3 measured an instance collapsing under.
//
// So the collapse happens IN THE DATABASE, inside a bounded window, and this
// file is where that bound lives.
//
// # The shape, and the two candidates it was chosen over
//
// Measured with EXPLAIN (ANALYZE, BUFFERS) on a seeded account of 30,000
// messages in 7,500 threads (PostgreSQL 17, the dev instance; collapse_test.go
// keeps the plan assertions so a regression fails CI rather than production):
//
//	A. DISTINCT ON (thread_id) over the WHOLE folder, then ORDER BY date DESC
//	   LIMIT n.
//	     -> 94.4 ms. Seq Scan on messages + Seq Scan on message_state + a sort
//	        of all 30,000 rows before the outer LIMIT can discard anything.
//	     REJECTED: correct, but its cost is the FOLDER's size, not the page's.
//	     It is already over the Gmail-class 100 ms bar at 30k and grows linearly
//	     — the exact failure mode the repertoire exists to make unrepresentable.
//
//	B. A bounded window in (date DESC, id DESC) — the order messages_acct_date
//	   already walks — then DISTINCT ON over THAT window, then a re-sort.
//	     -> 6.8 ms folder / 6.0 ms text / 5.5 ms account-wide / 6.3 ms on a page
//	        ~20 deep behind a keyset cursor.
//	     -> Index Scan using messages_acct_date, 1,001 rows touched, and with a
//	        selective term the text path reaches messages_acct_tsv_gin (0.5 ms,
//	        the same plan family as the uncollapsed search's 0.4 ms).
//	     CHOSEN: 14x faster than A at 30k, and CONSTANT in the folder's size —
//	     the deep-cursor page costs the same as the first.
//
//	C. A NOT EXISTS anti-join ("no newer message of my thread is also here").
//	     -> 4.2 ms on a smaller corpus, using messages_acct_thread.
//	     REJECTED despite being fastest on that sample: it is a correlated
//	     subquery per candidate row, so its cost per returned row scales with
//	     THREAD DEPTH, and it has no natural window — filling a page of 50 from a
//	     deeply threaded folder walks arbitrarily far with nothing bounding it.
//	     It also offers no cursor: there is no "last row scanned" to resume from,
//	     because the anti-join never materializes a window.
//
// # No new index
//
// Every path above is served by an index migration 0002 already creates
// (messages_acct_date, messages_acct_tsv_gin, message_state_pkey). Migration
// 0008 was budgeted for this work and is NOT written: adding an index whose
// only justification is a shape that already has one would, per migration
// 0004's own rule, hand the planner one more way to compete with the composite
// GIN on every search query. The EXPLAIN evidence above is what replaces it.
//
// # The cross-page duplicate, and the anti-join that closes it
//
// Within ONE window each thread appears exactly once by construction. Across
// PAGES it does not, and the first version of this code shipped that bug until
// TestCollapseNeverRepeatsAThreadAcrossPages caught it: a thread whose newest
// member sits near the window's edge can have an older member fall into the NEXT
// window, and that older member is then the newest of its thread within page 2 —
// so the conversation is listed twice, at two different positions, with two
// different messages representing it. In a client that is a conversation
// appearing twice in one scrolled list.
//
// The fix is a NOT EXISTS on the collapsed rows of a resumed page: keep a thread
// only if it has NO member at or above the cursor under the same filter. It is
// exact — "above the cursor" is precisely "already offered on an earlier page" —
// and it is nearly free, because it is the one query messages_acct_thread was
// built for: measured at 0.003 ms per candidate row over 251 rows, taking the
// resumed page from 6.3 ms to 7.3 ms. Page ONE carries no anti-join at all,
// since nothing precedes it.
//
// # The honesty this shape still owes the caller
//
// A bounded window collapses only what is IN the window, so it cannot guarantee
// a FULL page: a window of 1,000 messages that are all one thread yields one row.
// CollapsedResult therefore reports the window's own exhaustion
// (WindowExhausted) and a resume cursor, so the caller can distinguish "the
// result set ended" from "the window ended" and page rather than guess. That
// distinction is the whole reason this returns a struct instead of a slice.

// CollapseWindow is how many messages one collapse pass scans before collapsing
// them.
//
// It is deliberately LARGER than MaxSearchLimit (200), because the two bound
// different things: MaxSearchLimit bounds how many ROWS a caller may receive,
// and this bounds how many messages must be READ to produce them. A collapsed
// page of 50 conversations can legitimately require reading several hundred
// messages when threads are deep, and a window equal to the row cap would make
// a deep-threaded folder return near-empty pages forever.
//
// 1,000 is chosen against the measurement rather than by feel: it is the window
// the 5.5-6.8 ms numbers above were taken at, it is 40x the pilot's largest real
// thread (24 messages), and it leaves 93 ms of the Gmail-class budget unspent.
const CollapseWindow = 1000

// MaxCollapseWindow caps what a caller may ask to scan in one pass.
//
// The bound exists for the same reason MaxSearchLimit does: a caller that could
// name an arbitrary window could reintroduce, one parameter at a time, exactly
// the unbounded scan shape A was rejected for.
const MaxCollapseWindow = 5000

// CollapsedQuery is a request for one page of CONVERSATIONS.
//
// It carries the same narrowing the message-list shapes do, minus the two
// things a collapse cannot express:
//
//   - no relevance sort. Relevance is already a bounded re-rank of a recent
//     window (SearchByRelevance), and collapsing a re-ranked window would
//     collapse by an order no index can resume — so there would be no cursor for
//     the second page. The JMAP layer refuses that combination by name rather
//     than serving a first page it cannot continue.
//   - no keyword predicate on the folder path, for the same reason
//     ListMailboxMessages has none: the folder view has no keyword index.
//     ListCollapsedMessages refuses it rather than dropping it silently.
type CollapsedQuery struct {
	AccountID int64

	// MailboxID restricts to one folder when non-nil. Nil with an empty Text is
	// the account-wide collapse (RFC 8620 §5.5's `filter: null`).
	MailboxID *int64

	// Text is the full-text term when non-empty, which selects the FTS shape
	// (the composite gin(account_id, tsv)) instead of the date walk.
	Text string

	// Since and Until bound the date range: at or after Since, strictly before
	// Until. Both optional.
	Since *time.Time
	Until *time.Time

	// UnreadOnly restricts to unread messages.
	UnreadOnly bool

	// Keyword restricts to messages carrying an IMAP keyword — where labels
	// live after arbitration A6. Only honored alongside Text.
	Keyword string

	// Narrow carries the E3 filter conditions (see Narrowing).
	//
	// It is applied to BOTH the candidate window and the cross-page dedupe
	// anti-join, for the reason dedupeClause states at length: "already offered"
	// means offered by a query with THIS filter, so a thread whose only newer
	// member fails the narrowing was never on an earlier page and excluding it
	// would silently drop a conversation.
	Narrow Narrowing

	// After resumes the underlying (date DESC, id DESC) MESSAGE walk after a
	// previous page's last SCANNED row, which is not its last RETURNED row —
	// see CollapsedResult.NextCursor.
	After *SearchCursor

	// Limit caps how many CONVERSATIONS come back (default DefaultSearchLimit,
	// max MaxSearchLimit).
	Limit int

	// Window overrides how many messages are scanned to produce them. Zero
	// means CollapseWindow; it is clamped to [Limit, MaxCollapseWindow].
	Window int
}

func (q CollapsedQuery) effectiveLimit() int {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	if limit > MaxSearchLimit {
		limit = MaxSearchLimit
	}
	return limit
}

func (q CollapsedQuery) effectiveWindow() int {
	window := q.Window
	if window <= 0 {
		window = CollapseWindow
	}
	if window > MaxCollapseWindow {
		window = MaxCollapseWindow
	}
	// A window smaller than the page it must fill can never fill it. Raising it
	// is right here — the alternative is a caller that asks for 50 conversations,
	// names a window of 10, and receives 10 with no way to learn why.
	if limit := q.effectiveLimit(); window < limit {
		window = limit
	}
	return window
}

// CollapsedResult is one page of conversations plus the facts a caller needs in
// order to page correctly.
type CollapsedResult struct {
	// Rows are the surviving messages — the newest message of each distinct
	// thread within the scanned window — in (date DESC, id DESC) order.
	Rows []SearchResult

	// NextCursor resumes the underlying MESSAGE walk after the last row the
	// window SCANNED, not after the last row it returned.
	//
	// The distinction is what makes paging here correct. A returned row is
	// followed, in message order, by the older members of its thread that the
	// collapse dropped — and by whole threads whose newest member the window did
	// not reach. Resuming from the last RETURNED row would re-scan the dropped
	// members and re-emit their thread as a duplicate conversation on the next
	// page. Resuming from the last SCANNED row skips exactly what was already
	// considered and nothing else.
	//
	// Nil when the underlying result set was exhausted.
	NextCursor *SearchCursor

	// Scanned is how many messages the window actually read.
	Scanned int

	// WindowExhausted reports that the scan stopped because the WINDOW filled,
	// not because the result set ran out. A caller that treats a short page as
	// "the end" would hide mail; this is how it tells the two apart.
	WindowExhausted bool
}

// ListCollapsedMessages returns one page of conversations: the newest message
// of each distinct thread, newest thread first.
//
// # Which SQL shape a call becomes
//
// Exactly as in the uncollapsed repertoire, the filter decides — and in all
// three cases the collapse is the SAME two-stage plan, so there is one query
// builder here rather than three methods:
//
//	Text set        -> the FTS candidate set (composite gin(account_id, tsv))
//	MailboxID set   -> the folder walk (messages_acct_date + the mailbox predicate)
//	neither         -> the account-wide walk (messages_acct_date)
//
// # Why the inner window carries the ORDER BY and the outer query re-sorts
//
// DISTINCT ON requires its ORDER BY to lead with the distinct expression, so
// the collapse itself is ordered by (thread_id, date DESC, id DESC) — which is
// not the order the caller wants. The re-sort is a top-N heapsort over at most
// `window` rows already in memory (measured: 150 kB at window 1000), so it costs
// a sort node on a bounded set and no additional I/O. Sorting in Go instead
// would transfer the same rows, sort them anyway, and lose the planner's ability
// to stop the outer LIMIT early.
func (s *Store) ListCollapsedMessages(ctx context.Context, q CollapsedQuery) (CollapsedResult, error) {
	var out CollapsedResult

	if q.Keyword != "" && q.Text == "" {
		// The folder and account-wide walks join message_state for the mailbox
		// and the flags, but neither has a keyword predicate — the same gap
		// ListMailboxMessages documents. Refusing beats returning a list that
		// silently ignores the label the user filtered by.
		return out, fmt.Errorf("collapsed search: a keyword filter requires a text condition; " +
			"the folder view has no keyword predicate")
	}

	limit := q.effectiveLimit()
	window := q.effectiveWindow()

	where, args := q.conditions()
	windowArg := len(args) + 1
	args = append(args, window)

	// The cross-page dedupe, built before the LIMIT parameter so its own
	// placeholders are contiguous with the window's.
	dedupe, args := q.dedupeClause(args)

	limitArg := len(args) + 1
	args = append(args, limit)

	// Three levels, each doing exactly one job:
	//
	//   w     — the BOUNDED candidate window, in the index's own
	//           (date DESC, id DESC) order. The only level that touches the heap,
	//           and its LIMIT is what makes the whole statement bounded.
	//   d     — the collapse. DISTINCT ON (thread_id) with the window's order as
	//           the tiebreak keeps each thread's NEWEST member, which is §4.4.3's
	//           "Emails in the same Thread as a PREVIOUS Email in the list ...
	//           will be removed" applied to a newest-first list.
	//   outer — the re-sort into the caller's order, plus the page LIMIT, plus
	//           the three window facts.
	//
	// The window facts are scalar subqueries over the SAME CTE rather than a
	// second statement: `w` is materialized once (it is referenced four times, so
	// PostgreSQL materializes it), which is why `scanned` and the edge row cost
	// three CTE scans of an in-memory tuplestore — measured at 0.13-0.28 ms each
	// — instead of a second index walk.
	sql := `
		WITH w AS (
			SELECT m.id, m.date, m.subject, m.from_addr, m.preview, m.thread_id,
			       ms.mailbox_id, ms.flags, ms.keywords
			  FROM messages m
			  JOIN message_state ms ON ms.message_id = m.id
			 WHERE ` + where + `
			 ORDER BY m.date DESC, m.id DESC
			 LIMIT $` + fmt.Sprint(windowArg) + `
		), d AS (
			SELECT DISTINCT ON (w.thread_id)
			       w.id, w.date, w.subject, w.from_addr, w.preview, w.thread_id,
			       w.mailbox_id, w.flags, w.keywords
			  FROM w
			 ORDER BY w.thread_id, w.date DESC, w.id DESC
		)
		SELECT d.id, d.date, d.subject, d.from_addr, d.preview,
		       d.mailbox_id, d.flags, d.keywords,
		       (SELECT count(*) FROM w) AS scanned,
		       (SELECT w2.date FROM w w2 ORDER BY w2.date, w2.id LIMIT 1) AS edge_date,
		       (SELECT w2.id   FROM w w2 ORDER BY w2.date, w2.id LIMIT 1) AS edge_id
		  FROM d` + dedupe + `
		 ORDER BY d.date DESC, d.id DESC
		 LIMIT $` + fmt.Sprint(limitArg)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return out, fmt.Errorf("collapsed search: %w", err)
	}
	defer rows.Close()

	var (
		edgeDate *time.Time
		edgeID   *int64
		scanned  int64
		haveEdge bool
	)
	for rows.Next() {
		var (
			r     SearchResult
			flags int64
			ed    *time.Time
			eid   *int64
			sc    int64
		)
		if err := rows.Scan(&r.MessageID, &r.Date, &r.Subject, &r.FromAddr, &r.Preview,
			&r.MailboxID, &flags, &r.Keywords, &sc, &ed, &eid); err != nil {
			return out, fmt.Errorf("scanning collapsed result: %w", err)
		}
		r.Flags = flagsFromDB(flags)
		out.Rows = append(out.Rows, r)
		// Every row carries the same window facts — they are scalar subqueries
		// over one CTE — so reading them from the first row is enough, and doing
		// it explicitly is what says so.
		if !haveEdge {
			scanned, edgeDate, edgeID, haveEdge = sc, ed, eid, true
		}
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("reading collapsed results: %w", err)
	}

	out.Scanned = int(scanned)
	out.WindowExhausted = out.Scanned >= window
	if out.WindowExhausted && edgeDate != nil && edgeID != nil {
		// The window's OLDEST scanned row is the resume point: everything newer
		// has been considered, so the next page starts strictly after it in the
		// (date DESC, id DESC) walk.
		out.NextCursor = &SearchCursor{Date: *edgeDate, MessageID: *edgeID}
	}
	return out, nil
}

// dedupeClause builds the anti-join that keeps a resumed page from re-offering a
// conversation an earlier page already showed.
//
// # Why this is needed at all
//
// The window collapses what it sees. A thread whose newest member sat near the
// previous window's edge can have an OLDER member land in this window, and that
// older member is the newest of its thread HERE — so without this the same
// conversation appears on two pages, at two positions, represented by two
// different messages. RFC 8621 §4.4.3's whole promise is that a collapsed list
// contains each Thread once.
//
// # Why it is exact
//
// "Already offered" is exactly "has a member at or above the cursor, under the
// same filter". The cursor is a position in the (date DESC, id DESC) message
// walk, and every page consumed a contiguous prefix of that walk, so a thread
// with any member above the cursor was necessarily collapsed into some earlier
// page — no bookkeeping of what was actually returned is required, which is what
// keeps this stateless.
//
// # Why it is cheap
//
// It is the query messages_acct_thread exists for (migration 0004): the index
// leads with thread_id and carries date, so each probe is a range scan of one
// key prefix that usually returns nothing. Measured at 0.003 ms per candidate
// row over 251 rows — the resumed page went from 6.3 ms to 7.3 ms.
//
// PAGE ONE gets no clause at all: nothing precedes it, so there is nothing to
// exclude, and the empty string keeps its plan free of an anti-join node.
//
// The filter is REPEATED here rather than reusing the window's, and it must be:
// "already offered" means offered by a query with THIS filter. A thread whose
// only newer member is in another folder, or is read when the filter is unread,
// was never on an earlier page, and excluding it would silently drop a
// conversation the user is entitled to see.
func (q CollapsedQuery) dedupeClause(args []any) (string, []any) {
	if q.After == nil {
		return "", args
	}

	conds := []string{
		"p.thread_id IS NOT NULL",
		"p.thread_id = d.thread_id",
		// The account is repeated on the probe so the index's second key column
		// is bound, and so this correlated read carries the repertoire's own
		// account-scope rule rather than inheriting it from the outer query.
		fmt.Sprintf("p.account_id = $%d", 1),
		"ps.deleted_at IS NULL",
	}

	if q.Text != "" {
		// $2 is the tsquery term whenever there is one (conditions() puts it
		// there first), so this reuses that parameter rather than binding the
		// text a second time.
		conds = append(conds,
			"p.tsv @@ websearch_to_tsquery('simple', immutable_unaccent($2))")
	}
	if q.MailboxID != nil {
		args = append(args, *q.MailboxID)
		conds = append(conds, fmt.Sprintf("ps.mailbox_id = $%d", len(args)))
	}
	if q.Since != nil {
		args = append(args, *q.Since)
		conds = append(conds, fmt.Sprintf("p.date >= $%d", len(args)))
	}
	if q.Until != nil {
		args = append(args, *q.Until)
		conds = append(conds, fmt.Sprintf("p.date < $%d", len(args)))
	}
	if q.UnreadOnly {
		conds = append(conds, "(ps.flags & 1) = 0")
	}
	if q.Keyword != "" {
		args = append(args, q.Keyword)
		conds = append(conds, fmt.Sprintf("ps.keywords @> ARRAY[$%d]::text[]", len(args)))
	}
	// The E3 narrowing, over the probe's own aliases. Omitting it here would
	// break the anti-join in the direction that HIDES mail: a thread whose only
	// newer member has no attachment, under a hasAttachment filter, was never
	// offered on an earlier page, so excluding it now would drop the
	// conversation entirely.
	conds, args = q.Narrow.appendConditions(conds, args, "p", "ps")

	// At or ABOVE the cursor — the inclusive complement of the window's strict
	// "below". The cursor row itself was the last row an earlier page SCANNED,
	// so a thread reaching it has been considered.
	args = append(args, q.After.Date, q.After.MessageID)
	conds = append(conds, fmt.Sprintf("(p.date, p.id) >= ($%d, $%d)", len(args)-1, len(args)))

	return `
		 WHERE NOT EXISTS (
		   SELECT 1
		     FROM messages p
		     JOIN message_state ps ON ps.message_id = p.id
		    WHERE ` + strings.Join(conds, " AND ") + `)`, args
}

// conditions builds the candidate window's WHERE clause.
//
// It mirrors SearchQuery.conditions and the two list shapes rather than reusing
// one of them, because the three differ in exactly the place that matters: $2 is
// the tsquery term on the text path and does not exist on the others, so a
// shared builder would have to renumber parameters — the kind of shared helper
// whose only lasting effect is to make an off-by-one possible.
func (q CollapsedQuery) conditions() (string, []any) {
	conds := []string{"m.account_id = $1", "ms.deleted_at IS NULL"}
	args := []any{q.AccountID}

	if q.Text != "" {
		// The same query-side immutable_unaccent the uncollapsed shapes apply,
		// for the same reason: the generated tsv stores unaccented lexemes, so an
		// accented term matches NOTHING without it — silently, with zero results
		// and no error (search.go documents the failure this prevents).
		args = append(args, q.Text)
		conds = append(conds, fmt.Sprintf(
			"m.tsv @@ websearch_to_tsquery('simple', immutable_unaccent($%d))", len(args)))
	}
	if q.MailboxID != nil {
		args = append(args, *q.MailboxID)
		conds = append(conds, fmt.Sprintf("ms.mailbox_id = $%d", len(args)))
	}
	if q.Since != nil {
		args = append(args, *q.Since)
		conds = append(conds, fmt.Sprintf("m.date >= $%d", len(args)))
	}
	if q.Until != nil {
		args = append(args, *q.Until)
		conds = append(conds, fmt.Sprintf("m.date < $%d", len(args)))
	}
	if q.UnreadOnly {
		// Literal 1: the \Seen bit by definition, matching the partial index
		// message_state_unread exactly.
		conds = append(conds, "(ms.flags & 1) = 0")
	}
	if q.Keyword != "" {
		args = append(args, q.Keyword)
		conds = append(conds, fmt.Sprintf("ms.keywords @> ARRAY[$%d]::text[]", len(args)))
	}
	conds, args = q.Narrow.appendConditions(conds, args, "m", "ms")
	if q.After != nil {
		// The row-value comparison, not a disjunction — SearchCursor documents
		// why the difference decides whether a deep page resumes the index walk
		// or re-walks everything above it.
		args = append(args, q.After.Date, q.After.MessageID)
		conds = append(conds, fmt.Sprintf("(m.date, m.id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	return strings.Join(conds, " AND "), args
}
