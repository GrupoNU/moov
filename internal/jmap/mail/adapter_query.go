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
func (a *Adapter) SearchEmails(ctx context.Context, accountID int64, f searchFilter, s sortSpec) ([]int64, error) {
	q := store.SearchQuery{
		AccountID:  accountID,
		Text:       f.text,
		MailboxID:  f.mailboxID,
		Since:      f.since,
		UnreadOnly: f.unreadOnly,
		Keyword:    f.keyword,
		Limit:      store.MaxSearchLimit,
	}

	var (
		results []store.SearchResult
		err     error
		// postFilter records which conditions the chosen store method did NOT
		// apply, so they can be enforced below rather than silently dropped.
		postFilterFolderView bool
	)
	switch {
	case s.byRelevance:
		results, err = a.store.SearchByRelevance(ctx, q)
	case f.text != "":
		results, err = a.store.Search(ctx, q)
	case f.accountWide:
		// RFC 8620 §5.5 `filter: null` — the whole account, newest first (J4).
		// Like the folder view below, this method takes no unread/keyword/date
		// parameters, so those conditions are applied after the fetch; but
		// translateFilter refuses to pair them with an account-wide filter in
		// the first place, so the post-filter here is only the `before` bound
		// the loop below applies to every path.
		results, err = a.store.ListAccountMessages(ctx, accountID, store.MaxSearchLimit)
	default:
		// ListMailboxMessages takes only (account, mailbox, limit): it has no
		// parameters for unread, keyword or date. Those conditions are
		// therefore applied HERE, after the fetch — never dropped.
		//
		// The narrowing this causes is stated plainly: the database truncates
		// to MaxSearchLimit BEFORE these predicates run, so a folder view with
		// a keyword or unread filter can return fewer results than exist, if
		// the matches sit deeper than the window. Correct-but-incomplete, never
		// incorrect: nothing is returned that the filter excludes.
		//
		// The fix is a store method, not more code here. The J3 report names
		// it: ListMailboxMessages should take a SearchQuery-shaped filter (or
		// Search should accept an empty Text and skip the tsquery predicate),
		// which makes all of these index-served rather than post-applied.
		results, err = a.store.ListMailboxMessages(ctx, accountID, *f.mailboxID, store.MaxSearchLimit)
		postFilterFolderView = true
	}
	if err != nil {
		return nil, err
	}

	hits := make([]searchHit, 0, len(results))
	for _, r := range results {
		// The `before` bound is applied here for every path because
		// SearchQuery has no upper date bound — it carries Since only. Applying
		// it after the fact is exact for the rows fetched, but it can only
		// SHRINK a window the database already truncated, so a query with a
		// before filter may see fewer than MaxSearchLimit results while more
		// exist beyond the window.
		//
		// That is a real narrowing and it is why an upper bound belongs in the
		// store: the J3 report names `SearchQuery.Until *time.Time` as the
		// one-line store change that makes this exact, served by the same
		// (account_id, date DESC) index the shape already walks.
		if f.before != nil && !r.Date.Before(*f.before) {
			continue
		}
		if postFilterFolderView {
			// \Seen is bit 0 of the stored flags — the same definition
			// store.SearchQuery.UnreadOnly encodes as `(flags & 1) = 0`.
			if f.unreadOnly && r.Flags.Has(store.FlagSeen) {
				continue
			}
			if f.since != nil && r.Date.Before(*f.since) {
				continue
			}
			// A keyword filter cannot reach this branch: store.SearchResult
			// carries no keywords column, so the folder view cannot evaluate
			// one, and translateCondition therefore refuses a keyword filter
			// that names no text (query.go applyHasKeyword). If that refusal
			// were ever relaxed without a store change, this assertion is where
			// the mistake would surface instead of silently returning
			// unfiltered mail.
			if f.keyword != "" {
				return nil, errKeywordNeedsTextPath
			}
		}
		hits = append(hits, searchHit{
			id:   r.MessageID,
			date: r.Date,
			// The keywords ride along on the store row (J4), so evaluating the
			// §4.4.2 hasKeyword comparator costs no extra query — just a lookup
			// in the slice the row already carried.
			hasKeyword: s.keyword != "" && hasKeyword(r, s.keyword),
		})
	}

	if s.byRelevance {
		// The relevance order is the store's, and it is already the ranked
		// order — re-sorting by date would discard the ranking that cost 134 ms
		// to compute.
		out := make([]int64, 0, len(hits))
		for _, h := range hits {
			out = append(out, h.id)
		}
		return out, nil
	}
	return sortIDsStable(hits, s.ascending, s.keyword != "", s.keywordFirst), nil
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
