package mail

import (
	"context"
	"time"
)

// The search and changes reader contracts (J3), stated in the same style as
// contracts.go: this package's own view types, account id passed explicitly,
// absence rather than error for things that are not there.

// DefaultSearchWindow is how deep Email/query looks.
//
// It is store.MaxSearchLimit, restated here as an untyped constant because
// this file must not import the store (contracts.go's rule — the adapter is
// the only place that knows the store exists). adapter_query.go has a
// compile-time assertion that the two agree, so a change to the store's cap
// cannot silently desynchronize this one.
//
// Why 200 and not more: S3 validated the eight interactive shapes at LIMIT 50
// and the store caps at 200. Fetching deeper is not a tuning knob — it is the
// unbounded-work failure the whole repertoire exists to prevent.
const DefaultSearchWindow = 200

// SearchReader answers Email/query over the store's typed search repertoire.
//
// It takes the TRANSLATED filter and sort rather than raw JMAP arguments, so
// the refusal decisions (what this server can and cannot answer) live in
// query.go where they are documented against the RFC, and this interface can
// only ever be asked for shapes the repertoire serves.
type SearchReader interface {
	// SearchEmails returns the matching message ids, ordered by the sort, and
	// bounded by the search window. A result shorter than the window means the
	// window was not filled — which is what lets Email/query report an exact
	// total in the one case it can (query.go queryTotal).
	SearchEmails(ctx context.Context, accountID int64, f searchFilter, s sortSpec) ([]int64, error)
}

// searchHit is one result with the key its order depends on.
type searchHit struct {
	id   int64
	date time.Time
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
