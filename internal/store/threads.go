package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Store-side threading: assignment at insert, merge, and the thread reads.
//
// Migration 0004 carries the full design rationale and the RFC citations. The
// two invariants this file exists to maintain, restated because every function
// below depends on them:
//
//	I1. thread_id is the messages.id of the thread's OLDEST member. It is
//	    therefore a minimum over a growing set, so it only ever decreases.
//	I2. A merge moves the YOUNGER thread's members onto the OLDER id, never the
//	    reverse. RFC 8621 §4.1.1 makes threadId immutable; keeping the older id
//	    means the common case (a reply arriving after its parent) changes NO
//	    existing message's threadId, and the rare case (an ancestor arriving
//	    late) changes only the younger members' — which ADR-001 §2 arbitrated as
//	    "fusión de threads emite destroyed+created".
//
// Violating either turns thread ids into something that can oscillate, and an
// oscillating id is worse than an approximate one: a client caches it.

// threadMergeMaxMembers bounds how many messages one merge may move.
//
// A merge rewrites the losing members' `messages` rows, which means rewriting
// their generated tsv into two GIN indexes at ~0.58 ms each (S3 §4.5). That is
// affordable for a conversation and not for a mailing list archive that a
// single late "parent" would try to absorb whole.
//
// Past the bound the merge is REFUSED rather than truncated: a half-merged
// thread would leave two ids for one conversation with no record that they
// belong together, which is a corruption. Refusing leaves the two threads
// separate — the same state as before the message arrived, which is a
// documented weakness (the pre-0004 behavior) rather than a wrong answer.
// ReindexThreads can complete it offline if an operator wants it.
const threadMergeMaxMembers = 1000

// threadSubjectWindow bounds how stale a subject key may be and still join.
//
// JWZ's step 5 and RFC 8621 §3's second condition both compare subjects with no
// notion of time, and taken literally that means a "Re: invoice" today joins the
// "invoice" thread from three years ago. Every real implementation bounds it;
// Gmail's is famously around a month. 30 days is chosen for the same reason
// Gmail's is: a conversation that has been silent for a month and resumes with
// no References header is, to a reader, a new conversation.
const threadSubjectWindow = 30 * 24 * time.Hour

// ThreadAssignment is the outcome of threading one message.
type ThreadAssignment struct {
	// MessageID is the message that was threaded.
	MessageID int64
	// ThreadID is the thread it belongs to (invariant I1).
	ThreadID int64
	// MergedFrom lists the thread ids that were absorbed into ThreadID by this
	// assignment — empty in the common case.
	//
	// The JMAP layer needs these: RFC 8621 §3 requires that a merged thread be
	// reported, and ADR-001 §2 chose destroyed+created for it. A caller that
	// ignores this field is not wrong, only less prompt: the next /changes poll
	// sees the moved members through their bumped updated_at anyway.
	MergedFrom []int64
	// MovedMessages is how many EXISTING messages had their thread_id changed.
	// Zero in the common case; non-zero only when an ancestor arrived late.
	MovedMessages int
}

// AssignThreads threads a batch of freshly inserted messages, in one
// transaction, and returns what each was assigned.
//
// # Why this is a separate call and not part of InsertMessages
//
// InsertMessages is the A5 write path and is deliberately narrow: it writes two
// rows per message and nothing else. Threading needs to READ the account's
// existing messages (to find ancestors) and may WRITE other messages' rows (a
// merge), which is a fundamentally different transaction shape. Fusing them
// would make every insert hold locks on rows it usually does not touch.
//
// The two run back to back inside the sync pipeline's commit step, which is the
// same arrangement blob references already use (pipeline.go commitBatch): a
// crash between them leaves messages with thread_id equal to their own id —
// i.e. each its own thread — which is a VALID state, not a corrupt one, and
// which ReindexThreads converges. That is why thread_id is NOT NULL with the
// row's own id as the natural default rather than nullable: there is no torn
// state to represent.
//
// ids and candidates must be parallel. A zero id (a message InsertMessages
// skipped) is ignored.
func (s *Store) AssignThreads(ctx context.Context, accountID int64, ids []int64, candidates []ThreadCandidate) ([]ThreadAssignment, error) {
	if len(ids) != len(candidates) {
		return nil, fmt.Errorf("threading: %d ids but %d candidates", len(ids), len(candidates))
	}
	if len(ids) == 0 {
		return nil, nil
	}

	var out []ThreadAssignment
	err := s.InTx(ctx, func(tx pgx.Tx) error {
		out = out[:0]
		for i, id := range ids {
			if id == 0 {
				continue
			}
			a, err := assignOne(ctx, tx, accountID, id, candidates[i])
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// assignOne threads a single message inside an open transaction.
//
// # The algorithm (JWZ simplified, L2 §2.3)
//
//  1. Collect every thread that this message's References/In-Reply-To chain
//     reaches, by resolving each named Message-ID to a stored message in the
//     SAME ACCOUNT and taking that message's thread.
//  2. Collect the message's OWN Message-ID: anything already stored that
//     references it is a child that arrived first, and its thread joins too.
//     This is the out-of-order parent case, and it is why step 2 exists at all.
//  3. If the graph found nothing, and the subject looks like a reply, consult
//     the subject-key hint (RFC 8621 §3 condition 2).
//  4. The winner is the SMALLEST id among {this message} ∪ {every thread
//     found}. Every other thread merges into it.
//
// Cost: step 1 is one indexed query (messages_acct_msgid), step 2 is one
// indexed query, step 3 is one primary-key lookup, and the merge is one UPDATE
// that usually matches nothing. That is O(1) round trips per message regardless
// of thread size, which is the property the insert path needs.
func assignOne(ctx context.Context, tx pgx.Tx, accountID, id int64, c ThreadCandidate) (ThreadAssignment, error) {
	assignment := ThreadAssignment{MessageID: id, ThreadID: id}

	// The set of threads this message belongs to, including its own (which is
	// `id` until proven otherwise, because a message with no stored relatives is
	// its own thread — the JWZ base case).
	threads := map[int64]bool{}

	// ---- step 1: ancestors named by this message ---------------------------
	if refs := c.referenceSet(); len(refs) > 0 {
		found, err := threadsOfMessageIDs(ctx, tx, accountID, refs)
		if err != nil {
			return assignment, err
		}
		for _, t := range found {
			threads[t] = true
		}
	}

	// ---- step 2: descendants that arrived before this message --------------
	//
	// THE OUT-OF-ORDER CASE. Children stored earlier could not resolve this
	// message because it did not exist; now it does, and their threads must
	// join. Skipping this step is exactly the "weaker on non-local roots" gap
	// the derived implementation had.
	if c.MessageID != "" {
		found, err := threadsReferencing(ctx, tx, accountID, c.MessageID)
		if err != nil {
			return assignment, err
		}
		for _, t := range found {
			threads[t] = true
		}
	}

	// ---- step 3: the subject fallback, only when the graph is silent -------
	subjectKey, isReply := NormalizeSubject(c.Subject)
	if len(threads) == 0 && isReply && subjectKey != "" {
		t, err := threadBySubjectKey(ctx, tx, accountID, subjectKey)
		switch {
		case errors.Is(err, ErrNotFound):
			// No prior thread with this subject; the message starts one.
		case err != nil:
			return assignment, err
		default:
			threads[t] = true
		}
	}

	// ---- step 4: the winner is the oldest (invariants I1 and I2) -----------
	winner := id
	for t := range threads {
		if t < winner {
			winner = t
		}
	}
	assignment.ThreadID = winner

	// This message takes the winning id. It is the only write in the common
	// case, and it is a write to a row that was just inserted, so it is already
	// in cache.
	if winner != id {
		if _, err := tx.Exec(ctx,
			`UPDATE messages SET thread_id = $2 WHERE id = $1 AND account_id = $3`,
			id, winner, accountID); err != nil {
			return assignment, fmt.Errorf("threading message %d: %w", id, err)
		}
	}

	// Every OTHER thread merges into the winner. In the common case (a reply
	// joining its parent's thread) this loop finds exactly one thread and it IS
	// the winner, so nothing is moved and no existing message is touched.
	for t := range threads {
		if t == winner {
			continue
		}
		moved, err := mergeThread(ctx, tx, accountID, t, winner)
		if err != nil {
			return assignment, err
		}
		if moved == 0 {
			continue
		}
		assignment.MergedFrom = append(assignment.MergedFrom, t)
		assignment.MovedMessages += moved
	}

	// The subject hint is recorded for every message with a usable key, reply or
	// not: the hint's value is letting a FUTURE "Re: x" find the thread that
	// started with a bare "x", so the original must be in the table.
	if subjectKey != "" {
		if err := recordSubjectKey(ctx, tx, accountID, subjectKey, winner); err != nil {
			return assignment, err
		}
	}

	return assignment, nil
}

// threadsOfMessageIDs resolves a set of Message-ID headers to the threads of
// the messages carrying them, within one account.
//
// Served by messages_acct_msgid (migration 0002, created with the comment
// "Threading (JWZ) resolves parents by Message-ID within an account" — this is
// that query, finally).
//
// DISTINCT because a chain routinely names several messages of the same thread,
// and the caller only needs the set.
func threadsOfMessageIDs(ctx context.Context, tx pgx.Tx, accountID int64, messageIDs []string) ([]int64, error) {
	const q = `
		SELECT DISTINCT thread_id
		  FROM messages
		 WHERE account_id = $1 AND message_id = ANY($2)
		 LIMIT $3`

	rows, err := tx.Query(ctx, q, accountID, messageIDs, threadLookupLimit)
	if err != nil {
		return nil, fmt.Errorf("resolving thread ancestors: %w", err)
	}
	defer rows.Close()
	return scanInt64s(rows, "resolving thread ancestors")
}

// threadsReferencing finds the threads of messages that name this Message-ID —
// the children that arrived first.
//
// This is the query with no perfect index, and it is worth being explicit about
// why that is acceptable. `$1 = ANY(references_ids)` cannot use a btree; the
// planner uses messages_acct_date to scope by account and filters. At pilot
// scale that is nothing. At S3 scale it would be a per-insert account scan,
// which is why the LIMIT is here and why the GIN index below exists.
//
// The bound is the real protection: with LIMIT, the worst case is bounded work
// per insert rather than work proportional to the account. A thread that
// already has more than threadLookupLimit stored children is one whose thread
// id is long since settled, so finding "only" that many of them still yields
// the same winner.
func threadsReferencing(ctx context.Context, tx pgx.Tx, accountID int64, messageID string) ([]int64, error) {
	const q = `
		SELECT DISTINCT thread_id
		  FROM messages
		 WHERE account_id = $1
		   AND (in_reply_to = $2 OR references_ids @> ARRAY[$2]::text[])
		 LIMIT $3`

	rows, err := tx.Query(ctx, q, accountID, messageID, threadLookupLimit)
	if err != nil {
		return nil, fmt.Errorf("resolving thread descendants: %w", err)
	}
	defer rows.Close()
	return scanInt64s(rows, "resolving thread descendants")
}

// threadLookupLimit bounds each of the two graph queries.
//
// Small on purpose: the answer is a SET of thread ids, and a message reaching
// more than a handful of distinct threads is already pathological. 64 is far
// above any real chain (References caps out in the low tens in practice) and
// low enough that the unindexed descendant scan cannot become the insert path's
// cost center.
const threadLookupLimit = 64

// threadBySubjectKey reads the subject hint, refusing one that is too old.
//
// The window is applied in SQL rather than in Go so a stale row costs nothing to
// read: the primary-key lookup either returns a usable hint or nothing.
func threadBySubjectKey(ctx context.Context, tx pgx.Tx, accountID int64, key string) (int64, error) {
	const q = `
		SELECT thread_id
		  FROM thread_subject_keys
		 WHERE account_id = $1 AND subject_key = $2 AND last_seen > now() - $3::interval`

	var id int64
	err := tx.QueryRow(ctx, q, accountID, key, threadSubjectWindow.String()).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("reading subject thread key: %w", err)
	}
	return id, nil
}

// recordSubjectKey stores or refreshes the subject hint.
//
// The stored thread_id only ever moves DOWN (invariant I1): `least()` in the
// conflict clause is what enforces that, and it is what keeps the hint pointing
// at the oldest thread even when messages arrive in any order. last_seen always
// moves up, so an active conversation keeps its hint alive within the window.
func recordSubjectKey(ctx context.Context, tx pgx.Tx, accountID int64, key string, threadID int64) error {
	const q = `
		INSERT INTO thread_subject_keys (account_id, subject_key, thread_id, last_seen)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (account_id, subject_key) DO UPDATE
		   SET thread_id = least(thread_subject_keys.thread_id, EXCLUDED.thread_id),
		       last_seen = now()`

	if _, err := tx.Exec(ctx, q, accountID, key, threadID); err != nil {
		return fmt.Errorf("recording subject thread key: %w", err)
	}
	return nil
}

// mergeThread moves every member of `loser` onto `winner`, and returns how many
// messages moved.
//
// # Transactional safety under concurrent batch inserts
//
// Two sync workers can thread messages of the same account at the same time —
// the watcher and the reconciler routinely overlap (E6). Both may decide to
// merge the same pair of threads, or opposite pairs.
//
// Three things make that safe:
//
//   - The whole assignment runs in ONE transaction, and the UPDATE below takes
//     row locks on the losing members. A concurrent transaction wanting the same
//     rows blocks until this one commits, then re-reads.
//   - The direction is DETERMINISTIC: smaller id always wins. Two transactions
//     merging the same pair therefore agree on the direction, so one is a no-op
//     rather than the reverse of the other. This is what makes concurrent merges
//     converge instead of fighting.
//   - The rows are locked in ascending id order (ORDER BY in the subquery),
//     which gives every merge in this package one global lock order and makes a
//     deadlock cycle between two merges impossible — the same technique
//     pipeline.go uses for blob rows.
//
// The guard `winner < loser` is an assertion of invariant I2 expressed as code:
// a caller that gets the direction backwards writes nothing rather than
// corrupting thread identity.
func mergeThread(ctx context.Context, tx pgx.Tx, accountID, loser, winner int64) (int, error) {
	if winner >= loser {
		return 0, fmt.Errorf("threading: refusing to merge thread %d into younger thread %d "+
			"(invariant: the oldest thread always wins)", loser, winner)
	}

	// Bound the merge before doing it. Counting first costs one indexed read and
	// avoids starting an unbounded rewrite (see threadMergeMaxMembers).
	var members int
	err := tx.QueryRow(ctx,
		`SELECT count(*) FROM messages
		  WHERE thread_id IS NOT NULL AND thread_id = $2 AND account_id = $1`,
		accountID, loser).Scan(&members)
	if err != nil {
		return 0, fmt.Errorf("counting thread %d: %w", loser, err)
	}
	if members == 0 {
		return 0, nil
	}
	if members > threadMergeMaxMembers {
		// Refused, not truncated. The two threads stay separate, which is the
		// pre-0004 behavior for this message rather than a corruption.
		return 0, nil
	}

	// The members are locked in ascending id order so concurrent merges take
	// rows in one global sequence and cannot deadlock.
	const q = `
		UPDATE messages
		   SET thread_id = $3
		 WHERE id IN (
		       SELECT id FROM messages
		        WHERE thread_id IS NOT NULL AND thread_id = $2 AND account_id = $1
		        ORDER BY id
		          FOR UPDATE
		 )`
	tag, err := tx.Exec(ctx, q, accountID, loser, winner)
	if err != nil {
		return 0, fmt.Errorf("merging thread %d into %d: %w", loser, winner, err)
	}
	moved := int(tag.RowsAffected())
	if moved == 0 {
		return 0, nil
	}

	// THE CONSISTENCY REQUIREMENT (deliverable 5). A message whose threadId
	// changed must be reported by Email/changes, and that feed is driven by
	// message_state.updated_at (messages.go ChangedSince). The messages row
	// changed but the state row did not, so without this bump a client would
	// keep serving a stale threadId from its cache forever.
	//
	// It is deliberately scoped to the rows that actually moved: bumping more
	// would make clients refetch messages that did not change, which the
	// MessageStatesByUID doc calls out as the reason no-op updates are skipped.
	if _, err := tx.Exec(ctx, `
		UPDATE message_state
		   SET updated_at = now()
		 WHERE account_id = $1 AND message_id IN (
		       SELECT id FROM messages
		        WHERE thread_id IS NOT NULL AND thread_id = $2 AND account_id = $1
		 )`, accountID, winner); err != nil {
		return 0, fmt.Errorf("advancing state cursors after merging thread %d: %w", loser, err)
	}

	// The subject hints that pointed at the loser now point at the winner, or
	// they would resurrect a dead thread id on the next subject-fallback join.
	if _, err := tx.Exec(ctx, `
		UPDATE thread_subject_keys SET thread_id = $3
		 WHERE account_id = $1 AND thread_id = $2`, accountID, loser, winner); err != nil {
		return 0, fmt.Errorf("repointing subject keys after merging thread %d: %w", loser, err)
	}

	return moved, nil
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

// MessagesByIDs reads a set of messages and their state in ONE round trip.
//
// # Why this exists (the J2 performance gap)
//
// Email/get legitimately asks for up to maxObjectsInGet = 500 ids, and the JMAP
// adapter served that by calling GetMessage + GetMessageState per id: 1,000
// round trips for one request. On a LAN that is ~50 ms of pure latency; over
// anything slower it is the dominant cost of the whole method.
//
// Results are keyed by message id, so the caller reconstructs the requested
// order without depending on the database's. Ids that do not exist, belong to
// another account, or are tombstoned are simply absent — the same "absent means
// notFound" contract the JMAP readers already have.
//
// Tombstones are excluded here, unlike in MessageStatesByUID, because this
// serves /get rather than /changes: a destroyed message is not gettable.
func (s *Store) MessagesByIDs(ctx context.Context, accountID int64, ids []int64) (map[int64]MessageWithState, error) {
	out := make(map[int64]MessageWithState, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	// The columns of both halves, joined on the primary key of message_state —
	// which is messages.id, so this is a merge of two primary-key lookups per
	// row and nothing more.
	q := `SELECT ` + prefixColumns("m", messageColumns) + `, ` +
		prefixColumns("ms", messageStateColumns) + `
		  FROM messages m
		  JOIN message_state ms ON ms.message_id = m.id
		 WHERE m.account_id = $1 AND m.id = ANY($2) AND ms.deleted_at IS NULL`

	rows, err := s.pool.Query(ctx, q, accountID, ids)
	if err != nil {
		return nil, fmt.Errorf("reading %d messages: %w", len(ids), err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			m  Message
			st MessageState
		)
		if err := scanMessageAndState(rows, &m, &st); err != nil {
			return nil, fmt.Errorf("reading %d messages: %w", len(ids), err)
		}
		out[m.ID] = MessageWithState{Message: m, State: st}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading %d messages: %w", len(ids), err)
	}
	return out, nil
}

// ThreadMembers returns the message ids of each requested thread, ordered by
// date oldest-first, in one round trip.
//
// RFC 8621 §3 defines emailIds as "The ids of the Emails in the Thread, sorted
// by the receivedAt date of the Email, oldest first", so the ordering is part of
// the contract rather than a convenience. It comes straight off
// messages_acct_thread (account_id, thread_id, date), so there is no sort node.
//
// The id tiebreak makes the order TOTAL: two messages with the same date must
// still come back in a stable sequence, or a client paging a thread sees rows
// move between requests.
//
// Tombstoned messages are excluded: a destroyed message is not a member of a
// thread any more.
func (s *Store) ThreadMembers(ctx context.Context, accountID int64, threadIDs []int64) (map[int64][]int64, error) {
	out := make(map[int64][]int64, len(threadIDs))
	if len(threadIDs) == 0 {
		return out, nil
	}

	// `thread_id IS NOT NULL` is redundant as a filter — the column is NOT NULL
	// — and load-bearing as a PLAN HINT: messages_acct_thread is a partial index
	// with exactly that predicate (migration 0004), and PostgreSQL only
	// considers a partial index for a query it can prove implies the predicate.
	// Without this clause the thread reads fall back to a heap scan. Every query
	// in this file that wants the index carries it for the same reason.
	const q = `
		SELECT m.thread_id, m.id
		  FROM messages m
		  JOIN message_state ms ON ms.message_id = m.id
		 WHERE m.thread_id IS NOT NULL
		   AND m.thread_id = ANY($2)
		   AND m.account_id = $1
		   AND ms.deleted_at IS NULL
		 ORDER BY m.thread_id, m.date, m.id
		 LIMIT $3`

	// The limit is the per-thread bound times the number of threads asked for,
	// so one pathological thread cannot starve the others in the same request.
	limit := len(threadIDs) * ThreadMemberLimit
	rows, err := s.pool.Query(ctx, q, accountID, threadIDs, limit)
	if err != nil {
		return nil, fmt.Errorf("reading thread members: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var threadID, messageID int64
		if err := rows.Scan(&threadID, &messageID); err != nil {
			return nil, fmt.Errorf("reading thread members: %w", err)
		}
		if len(out[threadID]) >= ThreadMemberLimit {
			continue
		}
		out[threadID] = append(out[threadID], messageID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading thread members: %w", err)
	}
	return out, nil
}

// ThreadMemberLimit bounds one thread's reported membership.
//
// A conversation with more members than this is a mailing list archive rather
// than a thread, and returning all of it would make one Thread/get unbounded —
// the exact failure the store's LIMIT rule exists to prevent (search.go). The
// value is far above any real conversation.
const ThreadMemberLimit = 500

// ThreadsOfMessages maps message ids to their thread ids, in one round trip.
//
// Email/get needs this for every message it returns (threadId is a mandated
// property, RFC 8621 §4.1.1), and MessagesByIDs already reads the messages row,
// so in practice the adapter takes thread_id from there. This method exists for
// callers that have ids and want only the threads.
func (s *Store) ThreadsOfMessages(ctx context.Context, accountID int64, messageIDs []int64) (map[int64]int64, error) {
	out := make(map[int64]int64, len(messageIDs))
	if len(messageIDs) == 0 {
		return out, nil
	}

	const q = `
		SELECT id, thread_id FROM messages
		 WHERE account_id = $1 AND id = ANY($2)`

	rows, err := s.pool.Query(ctx, q, accountID, messageIDs)
	if err != nil {
		return nil, fmt.Errorf("reading thread ids: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id, threadID int64
		if err := rows.Scan(&id, &threadID); err != nil {
			return nil, fmt.Errorf("reading thread ids: %w", err)
		}
		out[id] = threadID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading thread ids: %w", err)
	}
	return out, nil
}

// ThreadExists reports which of the given thread ids actually name a thread of
// this account.
//
// Thread/get must answer notFound for an id that names nothing, and a thread id
// is only real if some message carries it. EXISTS per id would be N round
// trips; this is one.
func (s *Store) ThreadExists(ctx context.Context, accountID int64, threadIDs []int64) (map[int64]bool, error) {
	out := make(map[int64]bool, len(threadIDs))
	if len(threadIDs) == 0 {
		return out, nil
	}

	const q = `
		SELECT DISTINCT m.thread_id
		  FROM messages m
		  JOIN message_state ms ON ms.message_id = m.id
		 WHERE m.thread_id IS NOT NULL
		   AND m.thread_id = ANY($2)
		   AND m.account_id = $1
		   AND ms.deleted_at IS NULL`

	rows, err := s.pool.Query(ctx, q, accountID, threadIDs)
	if err != nil {
		return nil, fmt.Errorf("checking threads: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("checking threads: %w", err)
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("checking threads: %w", err)
	}
	return out, nil
}

// CountMailboxThreads returns the EXACT totalThreads and unreadThreads of a
// mailbox (RFC 8621 §2).
//
// The RFC defines them as:
//
//	totalThreads:  "The number of Threads where at least one Email in the
//	                Thread is in this Mailbox."
//	unreadThreads: the same, counting threads with at least one unread Email.
//
// Both are COUNT(DISTINCT thread_id) over the mailbox's messages, which is
// precisely what the definitions say and what the adapter could not compute
// before migration 0004 (it approximated them with the MESSAGE counts, which
// over-counts whenever a thread has two messages in one folder).
//
// # Cost
//
// The join is message_state -> messages on a primary key, filtered by the
// mailbox. It reads the same rows CountMailboxMessages already reads, plus one
// primary-key lookup each to fetch thread_id. Both counts come from ONE scan
// rather than two, which is why they share a method: asking separately would
// double the work for an answer that is always wanted together.
func (s *Store) CountMailboxThreads(ctx context.Context, mailboxID int64) (total, unread int64, err error) {
	// This one is driven by the mailbox, not by a thread, so it reads
	// message_state's index and fetches thread_id by primary key. The partial
	// index is not involved and the NOT NULL clause is therefore omitted: adding
	// it here would suggest a plan that is not the one this query wants.
	const q = `
		SELECT count(DISTINCT m.thread_id),
		       count(DISTINCT m.thread_id) FILTER (WHERE (ms.flags & 1) = 0)
		  FROM message_state ms
		  JOIN messages m ON m.id = ms.message_id
		 WHERE ms.mailbox_id = $1 AND ms.deleted_at IS NULL`

	if err := s.pool.QueryRow(ctx, q, mailboxID).Scan(&total, &unread); err != nil {
		return 0, 0, fmt.Errorf("counting threads in mailbox %d: %w", mailboxID, err)
	}
	return total, unread, nil
}

// MailboxCounts is the four counts RFC 8621 §2 requires of every Mailbox.
type MailboxCounts struct {
	TotalEmails   int64
	UnreadEmails  int64
	TotalThreads  int64
	UnreadThreads int64
}

// CountMailboxes returns all four counts for every mailbox of an account, in
// ONE round trip.
//
// # Why this replaces two queries per mailbox
//
// Mailbox/get renders the whole folder tree, and the adapter was issuing
// CountMailboxMessages per mailbox — 12 round trips for the pilot's 12 folders,
// and that was before threads were exact. Adding a second per-mailbox query for
// threads would have doubled it. Grouping instead makes the whole tree one
// aggregate scan, which is both fewer round trips and less total work: the
// planner reads message_state once instead of once per folder.
func (s *Store) CountMailboxes(ctx context.Context, accountID int64) (map[int64]MailboxCounts, error) {
	const q = `
		SELECT ms.mailbox_id,
		       count(*),
		       count(*) FILTER (WHERE (ms.flags & 1) = 0),
		       count(DISTINCT m.thread_id),
		       count(DISTINCT m.thread_id) FILTER (WHERE (ms.flags & 1) = 0)
		  FROM message_state ms
		  JOIN messages m ON m.id = ms.message_id
		 WHERE ms.account_id = $1 AND ms.deleted_at IS NULL
		 GROUP BY ms.mailbox_id`

	rows, err := s.pool.Query(ctx, q, accountID)
	if err != nil {
		return nil, fmt.Errorf("counting mailboxes of account %d: %w", accountID, err)
	}
	defer rows.Close()

	out := map[int64]MailboxCounts{}
	for rows.Next() {
		var (
			mailboxID int64
			c         MailboxCounts
		)
		if err := rows.Scan(&mailboxID, &c.TotalEmails, &c.UnreadEmails,
			&c.TotalThreads, &c.UnreadThreads); err != nil {
			return nil, fmt.Errorf("counting mailboxes of account %d: %w", accountID, err)
		}
		out[mailboxID] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("counting mailboxes of account %d: %w", accountID, err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Reindex
// ---------------------------------------------------------------------------

// ReindexThreads re-threads an account's messages in bounded batches, and
// returns how many messages changed thread.
//
// # What this is for
//
// Three things, all documented in migration 0004:
//
//  1. Completing the migration's backfill on a large installation, where the
//     migration's in-transaction backfill is too expensive to run on start.
//  2. Populating thread_subject_keys, which the migration deliberately leaves
//     empty because the normalization is Go code and a SQL approximation of it
//     would seed keys the runtime never produces.
//  3. Converging chains deeper than the migration's three flattening passes
//     resolve.
//
// # Why it is safe to run at any time
//
// It is idempotent and monotone: it only ever moves a message to an OLDER
// thread id (invariant I1), so running it twice changes nothing the second
// time, and running it while the sync engine is inserting cannot fight with the
// insert path — both agree the smaller id wins.
//
// batchSize bounds one transaction. The caller loops until the return value is
// zero, which is what makes the whole operation online rather than a single
// unbounded statement.
func (s *Store) ReindexThreads(ctx context.Context, accountID int64, batchSize int, afterID int64) (changed int, lastID int64, err error) {
	if batchSize <= 0 {
		batchSize = 1000
	}

	err = s.InTx(ctx, func(tx pgx.Tx) error {
		// Read one batch of messages in id order, which is also age order, so a
		// message is always threaded after its potential ancestors.
		rows, qerr := tx.Query(ctx, `
			SELECT id, message_id, in_reply_to, references_ids, subject
			  FROM messages
			 WHERE account_id = $1 AND id > $2
			 ORDER BY id
			 LIMIT $3`, accountID, afterID, batchSize)
		if qerr != nil {
			return fmt.Errorf("reindexing threads: %w", qerr)
		}

		type work struct {
			id        int64
			candidate ThreadCandidate
		}
		var batch []work
		for rows.Next() {
			var (
				id        int64
				messageID *string
				inReplyTo *string
				refs      []string
				subject   string
			)
			if err := rows.Scan(&id, &messageID, &inReplyTo, &refs, &subject); err != nil {
				rows.Close()
				return fmt.Errorf("reindexing threads: %w", err)
			}
			c := ThreadCandidate{References: refs, Subject: subject}
			if messageID != nil {
				c.MessageID = *messageID
			}
			if inReplyTo != nil && *inReplyTo != "" {
				c.References = append(append([]string{}, refs...), *inReplyTo)
			}
			batch = append(batch, work{id: id, candidate: c})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("reindexing threads: %w", err)
		}
		if len(batch) == 0 {
			return nil
		}

		for _, w := range batch {
			before, berr := threadOfMessage(ctx, tx, accountID, w.id)
			if berr != nil {
				return berr
			}
			a, aerr := assignOne(ctx, tx, accountID, w.id, w.candidate)
			if aerr != nil {
				return aerr
			}
			if a.ThreadID != before || a.MovedMessages > 0 {
				changed++
			}
			lastID = w.id
		}
		return nil
	})
	if err != nil {
		return 0, afterID, err
	}
	if lastID == 0 {
		lastID = afterID
	}
	return changed, lastID, nil
}

func threadOfMessage(ctx context.Context, tx pgx.Tx, accountID, id int64) (int64, error) {
	var threadID int64
	err := tx.QueryRow(ctx,
		`SELECT thread_id FROM messages WHERE id = $1 AND account_id = $2`,
		id, accountID).Scan(&threadID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("reading thread of message %d: %w", id, err)
	}
	return threadID, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// MessageWithState is both halves of a message, as MessagesByIDs returns them.
type MessageWithState struct {
	Message Message
	State   MessageState
}

func scanInt64s(rows pgx.Rows, what string) ([]int64, error) {
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return out, nil
}

// prefixColumns qualifies a comma-separated column list with a table alias, so
// the two halves of a join can share the column constants that the single-table
// reads already use.
//
// It exists so MessagesByIDs cannot drift out of step with messageColumns and
// messageStateColumns: adding a column to either constant automatically reaches
// the batch read, instead of being silently missing from it until a scan fails
// at runtime.
func prefixColumns(alias, columns string) string {
	parts := splitColumns(columns)
	for i, p := range parts {
		parts[i] = alias + "." + p
	}
	return joinColumns(parts)
}
