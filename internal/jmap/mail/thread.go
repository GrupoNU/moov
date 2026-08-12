package mail

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/GrupoNU/moov/internal/jmap"
)

// Thread/get — RFC 8621 §3.1 over the §3 Thread object.
//
// # STORE GAP, flagged for the director (J2 report)
//
// L2-sync-engine §2.3 specifies threading (a JWZ-simplified algorithm) and
// migration 0002 even creates the index it would need — "Threading (JWZ)
// resolves parents by Message-ID within an account", messages_acct_msgid.
// But no thread column, no threads table and no threading pass exist yet: the
// epic that owns them has not landed. J2 is scoped out of modifying
// internal/store, so this package derives threads instead, from the
// References/In-Reply-To data the store already persists per message.
//
// What that derivation is, precisely (adapter.go threadOf): a message's thread
// is keyed by the OLDEST ancestor reachable through its References chain that
// this account actually stores; a message with no stored ancestor is its own
// thread root. This satisfies the two properties Thread/get must have —
// every Email has exactly one threadId, and every id in a Thread's emailIds
// reports that same threadId back — and it matches what JWZ would produce for
// the common case of a well-formed reply chain.
//
// Where it is weaker than the real thing, stated plainly so the reader does
// not over-trust it:
//
//   - subject-based joining (JWZ step 5, the "Re:" heuristic) is not applied,
//     so a reply that drops References starts a new thread;
//   - a thread whose root is not stored locally splits into as many threads as
//     it has stored sub-roots, until the backfill brings the root in.
//
// Both resolve when the threading pass lands and the adapter reads a
// thread_id column instead. Handlers do not change: ThreadReader is the seam.

// handleThreadGet implements Thread/get.
func (d *Deps) handleThreadGet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseGet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}
	// RFC 8621 §3 gives Thread exactly two properties, and §5.1's `properties`
	// argument is honored for both. There is no meaningful selection to make —
	// id is always returned — but an unknown property is still invalidArguments.
	if bad := unknownProperties(req.Properties, map[string]bool{"id": true, "emailIds": true}); len(bad) > 0 {
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("unknown Thread properties: %v", bad)
	}
	props, _ := propertySet(req.Properties)

	// §5.1 allows a server to refuse "all records" for a type where that would
	// be unbounded: "If the server ... does not support the null value, it
	// MUST return a requestTooLarge error". Threads are derived per request
	// here, so returning every thread of an account means threading the entire
	// mailbox — the exact unbounded work the store's repertoire rule exists to
	// prevent (L2 §4.3). Refusing is the honest answer; Email/query (J3) is how
	// a client discovers thread ids.
	if req.IDs == nil {
		return nil, jmap.NewMethodError(jmap.CodeRequestTooLarge).
			WithDescription("Thread/get requires an explicit ids list; " +
				"this server does not enumerate every thread in an account")
	}

	state, err := d.State.ThreadState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading thread state", err)
	}
	resp := newGetResponse(req.AccountID, state)

	// Thread ids are validated but not decoded to an int here: the reader owns
	// what a thread id means (today, the root message id). An id that is not
	// even syntactically ours is notFound rather than an error, for the same
	// reason as in decodeIDList.
	wanted := make([]string, 0, len(*req.IDs))
	seen := make(map[string]bool, len(*req.IDs))
	for _, raw := range *req.IDs {
		if seen[raw] {
			continue
		}
		seen[raw] = true
		if _, err := DecodeThreadID(raw); err != nil {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		wanted = append(wanted, raw)
	}

	rows, err := d.Threads.ThreadsByID(ctx, caller.AccountID, wanted)
	if err != nil {
		return nil, serverFail("reading threads", err)
	}

	found := make(map[string]bool, len(rows))
	for _, r := range rows {
		found[r.ID] = true
	}
	for _, id := range wanted {
		if !found[id] {
			resp.NotFound = append(resp.NotFound, id)
		}
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	for _, row := range rows {
		resp.List = append(resp.List, renderThread(row, props))
	}
	return resp, nil
}

// renderThread builds one Thread object (RFC 8621 §3).
func renderThread(row ThreadRow, props map[string]bool) map[string]any {
	out := map[string]any{"id": row.ID}
	if wants(props, "emailIds") {
		// §3: "The ids of the Emails in the Thread, sorted by the receivedAt
		// date of the Email, oldest first." The reader guarantees that order.
		ids := make([]string, 0, len(row.EmailIDs))
		for _, id := range row.EmailIDs {
			ids = append(ids, EncodeEmailID(id))
		}
		out["emailIds"] = ids
	}
	return out
}
