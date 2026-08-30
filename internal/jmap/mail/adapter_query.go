package mail

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// errKeywordNeedsTextPath guards an unreachable combination — see the comment
// at its only use in SearchEmails.
var errKeywordNeedsTextPath = errors.New(
	"mail: a keyword filter requires the text search path; the folder view cannot evaluate keywords")

// The store-backed SearchReader and ChangesReader (J3).
//
// Like adapter.go, this file is one of the only places in the JMAP surface
// that knows internal/store exists. Everything above it works against the
// interfaces in search.go.

// The search window this package exposes must be exactly the store's cap: a
// larger one would be silently clamped by the store (leaving Email/query
// believing it saw more than it did, which would make its "the window was not
// filled, so this count is exact" reasoning WRONG), and a smaller one would
// hide results for no reason.
//
// This is a compile-time assertion, not a comment: both expressions are
// untyped constants, so either one going negative is a build failure. It fails
// if store.MaxSearchLimit ever changes without this constant following.
const (
	_ = uint(DefaultSearchWindow - store.MaxSearchLimit)
	_ = uint(store.MaxSearchLimit - DefaultSearchWindow)
)

// ---------------------------------------------------------------------------
// SearchReader
// ---------------------------------------------------------------------------

// SearchEmails answers a translated Email/query filter through the repertoire.
//
// Which store method a call reaches is decided HERE, by what the filter
// contains, and the mapping is exhaustive over what translateFilter can
// produce — there is no default branch that invents SQL:
//
//	relevance sort            -> SearchByRelevance (bounded, analytic pool)
//	text present              -> Search            (S3 shapes #1-#8)
//	mailbox only, no text     -> ListMailboxMessages (the folder view)
//
// translateFilter guarantees at least one of text/mailbox is set, so the three
// branches are total.
func (a *Adapter) SearchEmails(ctx context.Context, accountID int64, f searchFilter, s sortSpec, reach int) ([]int64, error) {
	if reach <= 0 {
		return []int64{}, nil
	}
	if len(f.or) > 0 {
		return a.searchUnion(ctx, accountID, f, s, reach, a.SearchEmails)
	}
	f, err := a.resolveExclusions(ctx, accountID, f)
	if err != nil {
		return nil, err
	}

	// The relevance path is NOT paged, and that is a product decision rather
	// than an omission.
	//
	// SearchByRelevance ranks the RankCandidateWindow most recent matches and
	// returns them in rank order (S3 mitigation #102). Rank order is not the
	// index's order, so there is no keyset cursor that can resume it: paging it
	// would mean re-ranking a larger candidate window per page, which is the
	// 892 ms unbounded ranking S3 rejected. Relevance therefore stays a single
	// bounded window, and a client that needs depth uses the date sort — the
	// same trade S3 recorded when it made relevance an explicit opt-in.
	if s.byRelevance {
		results, err := a.store.SearchByRelevance(ctx, store.SearchQuery{
			AccountID:  accountID,
			Text:       f.text,
			MailboxID:  f.mailboxID,
			Since:      f.since,
			Until:      f.before,
			UnreadOnly: f.unreadOnly,
			Keyword:    f.keyword,
			Narrow:     narrowing(f),
			Limit:      store.MaxSearchLimit,
		})
		if err != nil {
			return nil, err
		}
		out := make([]int64, 0, len(results))
		for _, r := range results {
			out = append(out, r.MessageID)
		}
		return out, nil
	}

	// Everything else is date-ordered, which IS the index's order, so it pages
	// with a keyset cursor: fetch store-sized pages until `reach` rows have
	// been collected or the result set runs out. Each page is a bounded,
	// account-scoped, LIMITed call into the repertoire — the discipline of
	// L2 §4.3 is per page, and depth costs more pages rather than a deeper
	// query.
	var (
		hits   []searchHit
		cursor *store.SearchCursor
	)
	for len(hits) < reach {
		want := reach - len(hits)
		if want > store.MaxSearchLimit {
			want = store.MaxSearchLimit
		}

		results, err := a.fetchPage(ctx, accountID, f, want, cursor)
		if err != nil {
			return nil, err
		}
		if len(results) == 0 {
			break
		}

		for _, r := range results {
			hits = append(hits, searchHit{
				id:   r.MessageID,
				date: r.Date,
				// The keywords ride along on the store row (J4), so evaluating
				// the §4.4.2 hasKeyword comparator costs no extra query — just
				// a lookup in the slice the row already carried.
				hasKeyword: s.keyword != "" && hasKeyword(r, s.keyword),
			})
		}

		// A short page means the result set is exhausted: asking again would
		// return nothing and cost a round trip.
		if len(results) < want {
			break
		}
		last := results[len(results)-1]
		cursor = &store.SearchCursor{Date: last.Date, MessageID: last.MessageID}
	}

	return sortIDsStable(hits, s.ascending, s.keyword != "", s.keywordFirst), nil
}

// fetchPage runs one bounded page of the date-ordered repertoire.
//
// Every filter condition is now expressed IN SQL — including the `before`
// bound, which used to be applied in Go after the LIMIT and could therefore
// only shrink an already-truncated window. That matters more under paging than
// it did before: a post-applied predicate would drop rows from a page and make
// the page shorter than requested, which is indistinguishable from "the result
// set ended" and would silently stop the walk early.
func (a *Adapter) fetchPage(
	ctx context.Context,
	accountID int64,
	f searchFilter,
	limit int,
	cursor *store.SearchCursor,
) ([]store.SearchResult, error) {
	switch {
	case f.text != "":
		return a.store.Search(ctx, store.SearchQuery{
			AccountID:  accountID,
			Text:       f.text,
			MailboxID:  f.mailboxID,
			Since:      f.since,
			Until:      f.before,
			UnreadOnly: f.unreadOnly,
			Keyword:    f.keyword,
			Narrow:     narrowing(f),
			After:      cursor,
			Limit:      limit,
		})

	case f.accountWide:
		// RFC 8620 §5.5 `filter: null` — the whole account, newest first (J4).
		//
		// Since E3 the shape carries the same narrowing its two siblings do: the
		// account-wide method grew Since, UnreadOnly and Narrow so that a
		// condition could not be answerable on the folder path and silently
		// dropped here. The keyword predicate is still the exception, and
		// answerable() still refuses it without a text condition.
		return a.store.ListAccountMessages(ctx, store.AccountListQuery{
			AccountID:  accountID,
			Since:      f.since,
			Until:      f.before,
			UnreadOnly: f.unreadOnly,
			Narrow:     narrowing(f),
			After:      cursor,
			Limit:      limit,
		})

	default:
		// The folder view. A keyword filter cannot reach it: translateCondition
		// refuses a keyword filter that names no text (query.go
		// applyHasKeyword), because this shape has no keyword predicate. The
		// assertion is kept so that relaxing the refusal without a store change
		// surfaces here instead of silently returning unfiltered mail.
		if f.keyword != "" {
			return nil, errKeywordNeedsTextPath
		}
		// The unread and date conditions are now SQL predicates on the folder
		// view rather than post-filters applied here. That closes the narrowing
		// the J3 report recorded — the database used to truncate to the window
		// BEFORE these ran, so a folder view with an unread filter could return
		// fewer results than exist — and it is what makes each page a full page,
		// which the paging walk above depends on to know when to stop.
		return a.store.ListMailboxMessages(ctx, store.MailboxListQuery{
			AccountID:  accountID,
			MailboxID:  *f.mailboxID,
			Since:      f.since,
			Until:      f.before,
			UnreadOnly: f.unreadOnly,
			Narrow:     narrowing(f),
			After:      cursor,
			Limit:      limit,
		})
	}
}

// SearchThreads answers a collapsed Email/query (RFC 8621 §4.4.3).
//
// # Why this is a paging WALK and not one call
//
// The store's collapsed shape scans a bounded window of MESSAGES and returns the
// conversations found in it (store.ListCollapsedMessages). How many
// conversations that is depends entirely on the data: a folder of singleton
// threads yields a full page from one window, and a mailing-list folder can
// yield three. So a single call cannot honor this method's contract, which is
// the same one SearchEmails has — "fewer than reach means the result set is
// exhausted", the property Email/query's exact-total case rests on.
//
// The walk is therefore over WINDOWS, each one bounded, each one resuming the
// message-level keyset cursor the previous window ended at. That is the same
// discipline the uncollapsed walk follows: depth costs more bounded pages, never
// a deeper query.
//
// # The one bound this walk adds
//
// maxCollapsePasses. The uncollapsed walk terminates because every page it gets
// is full or final; the collapsed walk can receive a page of ONE conversation
// from a full window and legitimately need another. A pathological account — a
// single thread with a hundred thousand messages — would otherwise walk it all
// to fill a page of 50. The cap turns that into a short answer, which is the
// same honest boundedness the anchor and total paths already have, rather than a
// request that runs until it times out.
func (a *Adapter) SearchThreads(ctx context.Context, accountID int64, f searchFilter, s sortSpec, reach int) ([]int64, error) {
	if reach <= 0 {
		return []int64{}, nil
	}
	// The relevance sort has no collapsed form. query.go refuses the
	// combination before it reaches here (a bounded re-rank has no cursor to
	// resume, so there is no second page to collapse into); the assertion is
	// kept so that relaxing the refusal without a store shape surfaces here
	// rather than silently serving an uncollapsed list.
	if s.byRelevance {
		return nil, errCollapseNeedsDateOrder
	}
	if len(f.or) > 0 {
		// A collapsed OR is a union of collapsed branches. It is NOT the
		// collapse of a union, and the difference is real: collapsing after the
		// merge would need each branch's thread ids, which the branch does not
		// return. Collapsing first means a conversation appearing in two
		// branches is represented by two different messages, which the
		// deduplication below cannot see — so a collapsed OR can list one
		// conversation twice.
		//
		// It is served anyway rather than refused, and the reason is that the
		// duplicate is BOUNDED and VISIBLE (two rows of the same subject, at
		// most one per branch) while the refusal would remove OR from every
		// conversation view — which is where a user searches. The exact fix is
		// a thread-id-returning branch, named here so it is a known gap rather
		// than a surprise.
		return a.searchUnion(ctx, accountID, f, s, reach, a.SearchThreads)
	}
	f, err := a.resolveExclusions(ctx, accountID, f)
	if err != nil {
		return nil, err
	}

	var (
		hits   []searchHit
		cursor *store.SearchCursor
	)
	for pass := 0; len(hits) < reach && pass < maxCollapsePasses; pass++ {
		want := reach - len(hits)
		if want > store.MaxSearchLimit {
			want = store.MaxSearchLimit
		}

		res, err := a.store.ListCollapsedMessages(ctx, store.CollapsedQuery{
			AccountID:  accountID,
			MailboxID:  f.mailboxID,
			Text:       f.text,
			Since:      f.since,
			Until:      f.before,
			UnreadOnly: f.unreadOnly,
			Keyword:    f.keyword,
			Narrow:     narrowing(f),
			After:      cursor,
			Limit:      want,
		})
		if err != nil {
			return nil, err
		}

		for _, r := range res.Rows {
			hits = append(hits, searchHit{
				id:   r.MessageID,
				date: r.Date,
				// Same as the uncollapsed path: the keywords ride along on the
				// row, so the §4.4.2 hasKeyword comparator costs no extra query.
				//
				// It is evaluated on the SURVIVING message — the thread's newest
				// matching one — which is the only honest reading of a keyword
				// comparator over a collapsed list: the row the client sees is
				// that message, so the keyword it sorts by must be that
				// message's. A thread-wide "any member has it" would sort a row
				// by a property the row does not display.
				hasKeyword: s.keyword != "" && hasKeyword(r, s.keyword),
			})
		}

		// The result set is exhausted when the WINDOW was not filled — not when
		// the page was short. Conflating the two is what would stop the walk
		// early and hide conversations, which is precisely why the store reports
		// the two separately.
		if !res.WindowExhausted || res.NextCursor == nil {
			break
		}
		cursor = res.NextCursor
	}

	return sortIDsStable(hits, s.ascending, s.keyword != "", s.keywordFirst), nil
}

// maxCollapsePasses bounds the collapsed walk.
//
// Each pass scans store.CollapseWindow (1,000) messages, so this caps one
// Email/query at 32,000 messages examined — more than the pilot's largest real
// account holds (26,869) and far more than any page of 200 conversations needs
// unless the average thread runs to 160 messages in one folder.
//
// Reaching it returns a SHORT list rather than an error, which is the same
// contract a truncated window already has: the client sees fewer ids and pages
// on, and the response's `limit` property tells it a server bound applied.
const maxCollapsePasses = 32

// errCollapseNeedsDateOrder guards an unreachable combination — see the comment
// at its only use in SearchThreads.
var errCollapseNeedsDateOrder = errors.New(
	"mail: collapseThreads requires the date-ordered path; a bounded relevance window has no cursor to collapse across")

// hasKeyword reports whether a store row carries a JMAP keyword.
//
// It has to consult BOTH places a keyword can live, which is a consequence of
// arbitration A6 and of how IMAP itself is built:
//
//   - the four IMAP system flags ($seen, $answered, $flagged, $draft) are bits
//     in the flags bitmask, never strings in the keywords array;
//   - every other keyword — including the labels A6 maps onto IMAP keywords, and
//     including a client's own like $pinned — is a string in that array.
//
// Asking only the array would silently answer "no" for $flagged, and asking only
// the bitmask would answer "no" for every label. Both are consulted, and the
// comparison is case-insensitive because RFC 8621 §4.1.1 defines keywords as
// case-insensitive.
func hasKeyword(r store.SearchResult, keyword string) bool {
	if flag, ok := systemFlagForKeyword(keyword); ok {
		return r.Flags.Has(flag)
	}
	for _, k := range r.Keywords {
		if strings.EqualFold(k, keyword) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// ChangesReader
// ---------------------------------------------------------------------------

// ChangedSince adapts the store's change feed to the /changes view.
//
// The store's own ChangedSince returns MessageState rows, which carry
// everything except the message's creation time — and creation time is exactly
// what §5.2 needs to tell a "created" from an "updated". So this reads the two
// together, through the query documented in queries_changes.go.
func (a *Adapter) ChangedSince(ctx context.Context, accountID int64, since time.Time, limit int) ([]ChangeRow, error) {
	return a.changedSinceRows(ctx, accountID, since, limit)
}

// NewestChangeAt returns the account's current change watermark.
func (a *Adapter) NewestChangeAt(ctx context.Context, accountID int64) (time.Time, error) {
	return a.newestChangeAt(ctx, accountID)
}

// ---------------------------------------------------------------------------
// mailbox changes
// ---------------------------------------------------------------------------

// MailboxesTouchedSince returns the mailboxes whose contents changed after a
// cursor, plus whether any mailbox ROW itself changed (a rename, a new folder,
// a subscription change).
//
// The split is what makes RFC 8621 §2.2's updatedProperties answerable: that
// argument is "If only the 'totalEmails', 'unreadEmails', 'totalThreads',
// and/or 'unreadThreads' Mailbox properties have changed since the old state,
// this will be the list of properties that may have changed. If the server is
// unable to tell if only counts have changed, it MUST just be null."
//
// Moov CAN tell, because the two live in different tables: a count change is a
// message_state write, and any other Mailbox property change is a mailboxes
// row write. Comparing the two watermarks against the cursor answers the
// question exactly, so this server returns the property list rather than the
// null a less structured store would have to.
func (a *Adapter) MailboxesTouchedSince(ctx context.Context, accountID int64, since time.Time, limit int) (counts []int64, rowsChanged []int64, err error) {
	counts, err = a.mailboxesWithMessageChanges(ctx, accountID, since, limit)
	if err != nil {
		return nil, nil, err
	}
	rowsChanged, err = a.mailboxRowsChangedSince(ctx, accountID, since, limit)
	if err != nil {
		return nil, nil, err
	}
	return counts, rowsChanged, nil
}

// mergeMailboxIDs unions two id lists into a sorted, deduplicated list.
func mergeMailboxIDs(a, b []int64) []int64 {
	seen := make(map[int64]bool, len(a)+len(b))
	out := make([]int64, 0, len(a)+len(b))
	for _, list := range [][]int64{a, b} {
		for _, id := range list {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
