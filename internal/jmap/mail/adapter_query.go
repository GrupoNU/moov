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
			After:      cursor,
			Limit:      limit,
		})

	case f.accountWide:
		// RFC 8620 §5.5 `filter: null` — the whole account, newest first (J4).
		// translateFilter refuses to pair an account-wide filter with
		// unread/keyword conditions, so the date bounds are the only narrowing
		// this shape can carry.
		return a.store.ListAccountMessages(ctx, store.AccountListQuery{
			AccountID: accountID,
			Until:     f.before,
			After:     cursor,
			Limit:     limit,
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
			After:      cursor,
			Limit:      limit,
		})
	}
}

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
