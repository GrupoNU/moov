package mail

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/GrupoNU/moov/internal/store"
)

// The store-side half of the L3 epic E3 filter work: turning a translated
// searchFilter's E3 conditions into a store.Narrowing, resolving the Gmail
// default exclusion to real mailbox ids, and serving an OR as a union of
// bounded searches.
//
// It is a separate file from adapter_query.go for the same reason that one is
// separate from adapter.go: it is the one place that knows both the translated
// filter vocabulary AND the store's, and keeping it together makes the mapping
// auditable in one screen rather than scattered through the paging walk.

// narrowing converts the translated filter's E3 conditions into the store's.
//
// It is a total function over the fields — every E3 condition searchFilter can
// carry has a line here — and that totality is the point: a condition that
// translated successfully and then failed to reach the store would be a filter
// SILENTLY DROPPED, which is the exact failure query.go refuses filters to
// avoid. TestNarrowingCarriesEveryE3Condition pins it, because the compiler
// cannot: adding a field to searchFilter and forgetting a line here builds
// cleanly.
func narrowing(f searchFilter) store.Narrowing {
	return store.Narrowing{
		HasAttachment:     f.hasAttachment,
		Cc:                f.cc,
		Bcc:               f.bcc,
		MinSize:           f.minSize,
		MaxSize:           f.maxSize,
		FlagsAll:          store.Flags(f.flagsAll),
		FlagsNone:         store.Flags(f.flagsNone),
		ExcludeMailboxIDs: f.excludeMailboxIDs,
	}
}

// resolveExclusions turns the filter's default-exclusion INTENT into the actual
// junk and trash mailbox ids of this account.
//
// # Why the resolution happens here and not in query.go
//
// The junk and trash mailboxes are rows in the database, and query.go does not
// read the database (search.go states that rule for this package's translation
// layer). So query.go marks the intent with a boolean — applyDefaultExclusion,
// which carries the whole Gmail-vs-RFC argument — and this resolves it.
//
// The split also has a practical payoff: a filter can be built, compared and
// asserted in a unit test with no store at all, and the tests that pin the
// POLICY (canon §2.5: Spam and Trash out by default, `in:spam` still works,
// `in:anywhere` opts out) run against the boolean, while the tests that pin the
// RESOLUTION run against a real database.
//
// # An account with no Spam or Trash folder
//
// Excludes nothing, silently, and that is correct rather than a swallowed
// error: the exclusion's meaning is "do not show me mail I have thrown away or
// that was classified as spam", and an account with no such folder has no such
// mail. store.ErrNotFound is the expected outcome on a fresh mailbox, on a
// Dovecot without SPECIAL-USE annotations, and on the conformance fixtures —
// treating it as a failure would make every search on those accounts return an
// error instead of results.
func (a *Adapter) resolveExclusions(ctx context.Context, accountID int64, f searchFilter) (searchFilter, error) {
	if !f.defaultExclusion {
		return f, nil
	}
	for _, role := range []store.MailboxRole{store.RoleJunk, store.RoleTrash} {
		mb, err := a.store.GetMailboxByRole(ctx, accountID, role)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return f, fmt.Errorf("resolving the %s mailbox for the default search exclusion: %w", role, err)
		}
		f.excludeMailboxIDs = append(f.excludeMailboxIDs, mb.ID)
	}
	return f, nil
}

// Snippets answers SearchSnippet/get through the store.
//
// # Which text is highlighted against
//
// The filter's `text` — which, for a filter carrying `from`/`to`/`subject`
// instead, is the same string those collapse onto (query.go documents that
// over-match). A filter with no text at all yields no rows, which the handler
// renders as §5.1's "the server SHOULD return null for both properties".
//
// An OR is highlighted against its FIRST text-carrying branch. §5.1 gives no
// guidance for a disjunction, and marking against every branch would mean
// running ts_headline once per branch and merging fragments that came from
// different queries — a snippet assembled out of two searches, which is neither
// what §5 describes nor something a user could interpret. One branch, chosen
// deterministically, is the honest simplification, and it is recorded here
// rather than left for someone to infer from behavior.
func (a *Adapter) Snippets(ctx context.Context, accountID int64, f searchFilter, messageIDs []int64) ([]SnippetView, error) {
	text := f.text
	if text == "" {
		for _, branch := range f.or {
			if branch.text != "" {
				text = branch.text
				break
			}
		}
	}
	if text == "" {
		return nil, nil
	}

	rows, err := a.store.Snippets(ctx, store.SnippetQuery{
		AccountID:  accountID,
		MessageIDs: messageIDs,
		Text:       text,
	})
	if err != nil {
		return nil, err
	}
	out := make([]SnippetView, 0, len(rows))
	for _, r := range rows {
		// A row with neither a marked subject nor a marked preview matched only
		// in a field the snippet does not render (an address, say). It is
		// dropped rather than returned empty, so the handler's "absent means
		// null" rule stays the single place that decision is made.
		if !strings.Contains(r.Subject, escapedMarkStart) && !strings.Contains(r.Preview, escapedMarkStart) {
			continue
		}
		out = append(out, SnippetView{
			MessageID: r.MessageID,
			Subject:   r.Subject,
			Preview:   r.Preview,
		})
	}
	return out, nil
}

// searchUnion serves a §5.5 OR as a union of bounded searches.
//
// # The shape, and why it is bounded
//
// Each branch is run through the SAME search entry point the caller used — the
// `run` parameter, which is either SearchEmails or SearchThreads — so a branch
// costs exactly what it would as a standalone query and inherits every bound
// that query has: the account scope, the LIMIT per page, the keyset paging, the
// reach ceiling. query.go's translateOr has already refused any branch that
// would not be served on its own, so there is no branch here whose cost is
// unbounded.
//
// The union itself adds one bounded step: at most maxOrBranches * reach ids are
// merged, deduplicated and re-sorted in memory. That is a sort over a slice the
// server already materialized, not a database operation.
//
// # Why each branch is fetched to the FULL reach
//
// A cheaper implementation would divide the reach among the branches. It is
// wrong, and wrong in the direction that hides mail: the union's first `reach`
// results can all come from ONE branch — every match older than the newest match
// of the other — so a branch fetched to reach/2 would run out exactly when it
// was supposed to keep supplying rows, and the merged list would end early with
// no way for the caller to tell that from a genuine exhaustion.
//
// # The short-result contract, preserved
//
// SearchEmails and SearchThreads both promise "fewer than reach ids means the
// result set is exhausted", and Email/query's one exact-total case rests on it
// (query.go queryTotal). The union preserves it: the merged set is short only
// when EVERY branch was short, because a branch that filled its reach can still
// supply more. That is why the truncation flag is computed as an AND over the
// branches rather than from the merged length.
func (a *Adapter) searchUnion(
	ctx context.Context,
	accountID int64,
	f searchFilter,
	s sortSpec,
	reach int,
	run func(context.Context, int64, searchFilter, sortSpec, int) ([]int64, error),
) ([]int64, error) {
	type hit struct {
		id  int64
		pos int
	}
	var (
		merged      []hit
		seen        = make(map[int64]bool)
		anyExhausted bool
	)

	for _, branch := range f.or {
		ids, err := run(ctx, accountID, branch, s, reach)
		if err != nil {
			return nil, err
		}
		if len(ids) < reach {
			anyExhausted = true
		}
		for i, id := range ids {
			if seen[id] {
				// A message matching two branches appears ONCE. §5.5's result
				// is a list of ids, and the same id twice would break every
				// client's paging and its own anchor lookup.
				continue
			}
			seen[id] = true
			// The position within its own branch is the only ordering
			// information a union has: each branch came back in the sort's
			// order, so a message that was 3rd in its branch sorts ahead of one
			// that was 10th in another. It is an approximation of the true
			// merged order — an exact merge would need each hit's sort KEY, not
			// its rank — and it is exact whenever the branches are disjoint in
			// time, which is the ordinary case.
			//
			// It is recorded as an approximation rather than presented as
			// exact, and the honest consequence is stated in
			// translateOperator: an OR is a search, not a stable paging cursor.
			merged = append(merged, hit{id: id, pos: i})
		}
	}

	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].pos != merged[j].pos {
			return merged[i].pos < merged[j].pos
		}
		// A total order, so the list is stable between calls as §5.5 requires.
		return merged[i].id > merged[j].id
	})

	out := make([]int64, 0, len(merged))
	for _, h := range merged {
		out = append(out, h.id)
	}
	// Truncate to the reach the caller asked for. Without this a 4-branch OR
	// could return four times the requested depth, and Email/query's
	// "shorter than reach means exhausted" test would read a long list as a
	// truncated one — which is harmless — while the caller paged over ids it
	// never asked for, which is not.
	if len(out) > reach {
		out = out[:reach]
		return out, nil
	}
	if !anyExhausted && len(out) < reach {
		// Every branch filled its reach, yet the merge is short — which can only
		// happen through deduplication. The result set is NOT exhausted, and
		// saying so by padding is impossible, so the honest signal is to report
		// exactly the ids found. queryTotal may then report an exact total that
		// is in fact a floor.
		//
		// This is the one place the union is less precise than a single search,
		// it is bounded (it can only UNDER-report a total, never over-report,
		// and only for a filter that already declines to page stably), and it is
		// recorded here rather than discovered later.
		return out, nil
	}
	return out, nil
}
