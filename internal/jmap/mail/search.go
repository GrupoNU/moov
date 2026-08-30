package mail

import (
	"context"
	"time"
)

// The search and changes reader contracts (J3), stated in the same style as
// contracts.go: this package's own view types, account id passed explicitly,
// absence rather than error for things that are not there.

// DefaultSearchWindow is how many rows one store round trip may return.
//
// It is store.MaxSearchLimit, restated here as an untyped constant because
// this file must not import the store (contracts.go's rule — the adapter is
// the only place that knows the store exists). adapter_query.go has a
// compile-time assertion that the two agree, so a change to the store's cap
// cannot silently desynchronize this one.
//
// Why 200 and not more: S3 validated the eight interactive shapes at LIMIT 50
// and the store caps at 200. Fetching a deeper single page is not a tuning
// knob — it is the unbounded-work failure the whole repertoire exists to
// prevent. Reaching PAST it is done by paging with a keyset cursor, where each
// page costs the same as the first, not by raising this number.
const DefaultSearchWindow = 200

// MaxQueryReach bounds how far into a result set Email/query will page.
//
// # Why there is a ceiling at all
//
// Keyset paging makes each page cost the same as the first, but a client that
// asks for position:1000000 in ONE request still makes the server walk a
// million rows to answer honestly. The repertoire's promise is bounded work per
// request (L2 §4.3), so the reach is bounded too — and the bound is stated in
// the response through the §5.5 `limit` property, exactly as the window cap is.
//
// # 100,000 — decision D-7 of the L3 plan, resolved with numbers
//
// The previous value was 10,000, "chosen against the product, not the
// database: deeper than any human scrolls". That reasoning was wrong in a way
// the plan caught and made a signed decision out of (D-7, risk 3): the owner's
// real account holds 26,869 messages, so the ceiling cut his own mailbox off at
// 37% of it, and ADR §6 names 100,000 as the target. The plan's instruction was
// to resolve it with measurements rather than judgment, and this is that
// resolution.
//
// MEASURED on a purpose-built corpus — 120,000 messages on the account under
// test plus 20,000 on a second account (so a missing account scope shows as
// wrong rows, not as a passing test), PostgreSQL 17.4, the dev instance, in the
// exact shape adapter_query.go issues: LIMIT 200 pages resumed by a row-value
// keyset cursor, ORDER BY (date DESC, id DESC).
//
//	ONE PAGE, at depth:          plan                      time     buffers
//	  depth 0      (first page)  Index Scan messages_acct_date   0.92 ms   631
//	  depth 100,000              Index Scan messages_acct_date   1.55 ms   628
//
// Identical buffer counts and sub-2 ms at both ends: the keyset cursor's
// constant-cost claim is not an argument here, it is a measurement. A page at
// the hundred-thousandth row touches the same number of pages as the first,
// because the cursor RESUMES the index walk instead of counting into it.
//
//	A WHOLE WALK to depth N, in one request (3 runs each):
//	  N        pages   total            per page
//	  10,000      50   55.9 / 63.7 / 75.3 ms    ~1.3 ms
//	  26,869     135   158.8 / 159.6 / 166.2 ms ~1.2 ms
//	  100,000    500   589.2 / 629.0 / 664.0 ms ~1.2 ms
//	  120,000    600   764.1 ms                 ~1.3 ms
//
// Linear in the depth, flat per page, no knee anywhere. 100,000 costs ~0.6 s of
// server time in the pathological case where a client asks for it in a SINGLE
// request — and that case is pathological rather than ordinary, which is the
// distinction the ceiling is actually protecting:
//
//   - A client SCROLLING carries its own cursor and pays ~1.2 ms per page. It
//     never asks the server for depth at all, at any position in the mailbox.
//     This is what the PWA does and what every conforming client does.
//   - A client sending position:100000 in one query pays the 0.6 s once. It is
//     over the Gmail-class 100 ms interactive bar, and deliberately so: this
//     ceiling is not a latency budget, it is the wall a runaway request hits.
//     A request that WANTS the hundred-thousandth row is not an interactive
//     search, and the honest answer is to serve it slowly rather than to refuse
//     the owner access to his own mail at row 10,001.
//
// So the number the measurements support is 100,000, which is also ADR §6's
// target and 3.7x the owner's real mailbox. It is NOT raised further: 120,000
// measured fine too, but a ceiling exists to be a ceiling, and the case for
// each further order of magnitude has to be made by someone who has a client
// that needs it.
//
// The deeper alternative remains what it always was and is now genuinely
// sufficient: narrow the filter, or follow Email/changes. Both are index-served
// at any depth.
const MaxQueryReach = 100000

// SearchReader answers Email/query over the store's typed search repertoire.
//
// It takes the TRANSLATED filter and sort rather than raw JMAP arguments, so
// the refusal decisions (what this server can and cannot answer) live in
// query.go where they are documented against the RFC, and this interface can
// only ever be asked for shapes the repertoire serves.
type SearchReader interface {
	// SearchEmails returns the matching message ids, ordered by the sort, and
	// bounded by reach — the number of rows the caller needs in order to serve
	// its page, i.e. position+limit, never more.
	//
	// A result shorter than reach means the result set was exhausted, which is
	// what lets Email/query report an exact total in the one case it can
	// (query.go queryTotal). The implementation walks the store's fixed-size
	// pages with a keyset cursor rather than issuing one deep query, so cost
	// scales with what the caller actually asked for and never with the size of
	// the mailbox.
	SearchEmails(ctx context.Context, accountID int64, f searchFilter, s sortSpec, reach int) ([]int64, error)

	// SearchThreads is SearchEmails with RFC 8621 §4.4.3 collapseThreads
	// applied: it returns at most ONE id per thread — the newest matching
	// message of each — in the same sort order, bounded by the same reach.
	//
	// It is a separate method rather than a flag on SearchEmails because the two
	// are different store shapes with different bounds, not one shape with a
	// parameter: the collapsed one scans a wider window than it returns
	// (store.CollapseWindow) precisely because collapsing shrinks a page by an
	// amount only the data knows. A boolean would hide that difference behind a
	// signature that promises the same cost either way.
	//
	// Its short-result contract is the SAME as SearchEmails': fewer than reach
	// ids means the result set is exhausted, so queryTotal's one exact case
	// stays exact. Honoring that is the implementation's job — it must page
	// until it has reach conversations or the mail runs out, never stop at a
	// window boundary.
	SearchThreads(ctx context.Context, accountID int64, f searchFilter, s sortSpec, reach int) ([]int64, error)
}

// searchHit is one result with the key its order depends on.
type searchHit struct {
	id   int64
	date time.Time
	// hasKeyword records whether this hit carries the keyword a hasKeyword
	// comparator names (§4.4.2). It is only meaningful when the sort asked for
	// one; otherwise it is false for every hit and partitions nothing.
	hasKeyword bool
}

// ChangesReader feeds Email/changes and Mailbox/changes (RFC 8620 §5.2).
type ChangesReader interface {
	// ChangedSince returns the account's message state changes strictly after
	// the cursor, oldest first, at most limit rows.
	//
	// Oldest-first is load-bearing: §5.2 forbids returning "a record as created
	// after a response that deems it as updated or destroyed", and processing
	// changes in the order they happened is what makes that hold across the
	// intermediate states maxChanges produces.
	ChangedSince(ctx context.Context, accountID int64, since time.Time, limit int) ([]ChangeRow, error)

	// NewestChangeAt returns the account's current change watermark — the same
	// max(updated_at) the /get state string is built from — or the zero time
	// when the account has no messages.
	//
	// It exists to detect a cursor from the FUTURE, which is the only
	// unanswerable-cursor case this store can actually recognize. changes.go
	// checkCursorReachable documents at length why the intuitive test (compare
	// against the OLDEST surviving change) is wrong and would force needless
	// full reloads.
	NewestChangeAt(ctx context.Context, accountID int64) (time.Time, error)

	// MailboxesTouchedSince returns, separately, the mailboxes whose COUNTS
	// changed after a cursor and those whose own ROW changed.
	//
	// The split is exactly what RFC 8621 §2.2's updatedProperties needs: counts
	// move in message_state, every other Mailbox property moves in the
	// mailboxes row, so comparing the two answers "did only counts change?"
	// without guessing.
	MailboxesTouchedSince(ctx context.Context, accountID int64, since time.Time, limit int) (counts, rows []int64, err error)
}

// ChangeRow is one changed message as /changes needs it.
type ChangeRow struct {
	// MessageID is the store message id.
	MessageID int64

	// MailboxID is the mailbox the message is in — the mailbox whose counts
	// this change moved, which is what Mailbox/changes reports.
	MailboxID int64

	// CreatedAt is when the message row was first written. Compared against the
	// client's cursor, it is what distinguishes a creation from an update:
	// §5.2's "created" is "records that have been created since the old state".
	CreatedAt time.Time

	// UpdatedAt is the change watermark this row advances — the cursor value
	// the next /changes call resumes from.
	UpdatedAt time.Time

	// Destroyed reports a tombstone (message_state.deleted_at is set). The
	// store keeps tombstones precisely so /changes can report them (store
	// messages.go MarkDeleted: "The rows are marked rather than deleted because
	// JMAP Email/changes must keep reporting them as destroyed until every
	// client has caught up").
	Destroyed bool
}
