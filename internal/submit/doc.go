// Package submit is the transactional outbox of ADR-001 §4 and arbitration
// W-A3 (L2-jmap-write §2): the SMTP submission client and the executor that
// drives 'send' intents from queued to done without ever duplicating or losing
// a message.
//
// # The submission state machine
//
// Every state below is a persisted fact on the intents row (migration 0005) —
// state, accepted_at, appended_at, canceled_at — never memory. Every
// transition is a single-statement UPDATE, and each is observable through
// EmailSubmission/get (undoStatus, deliveryStatus) and the broker's push.
//
//	             EmailSubmission/set create
//	                       │  enqueue: not_before = now + undoWindow
//	                       ▼
//	undoStatus→canceled  ┌────────┐   not_before ≤ now,
//	or destroy, while    │ queued │   claim (FOR UPDATE SKIP LOCKED)
//	queued ∧ ¬accepted   └────────┘
//	(one-statement CAS)   │      │
//	             ┌────────┘      └──────────►┌───────────┐
//	             ▼                           │ in_flight │◄─────────────┐
//	       ┌──────────┐                      └───────────┘              │
//	       │ canceled │                            │ SMTP transaction   │
//	       └──────────┘        ┌───────────────────┼────────────┐       │
//	       undoStatus          │                   │            │       │
//	       "canceled"          ▼                   ▼            ▼       │
//	                     transient pre-250   permanent 5xx   250 read   │
//	                     (4xx / network)           │            │       │
//	                       │                       ▼            ▼       │
//	                       │ re-queue with   ┌────────┐  persist        │
//	                       │ backoff; cap at │ failed │  accepted_at ◄──┼── the point of
//	                       └──── attempts ──►└────────┘     │           │   no return
//	                                         undoStatus     ▼           │
//	                                         "final",   [accepted]      │
//	                                         delivery   accepted_at     │
//	                                         "no"       IS NOT NULL     │
//	                                                        │           │
//	                                                        │ APPEND to \Sent,
//	                                                        │ deduped by Message-ID;
//	                                                        │ a failure here re-queues
//	                                                        │ ONLY this step ──────────┘
//	                                                        ▼
//	                                                    ┌──────┐
//	                                                    │ done │  undoStatus "final",
//	                                                    └──────┘  delivered "unknown"
//
// # The three rules, and where each is enforced
//
// RULE 1 — THE 250 IS SACRED. The instant the server's 250 answer to the end
// of DATA is read, the acceptance is persisted (store.MarkSendIntentAccepted,
// a single autocommit UPDATE) before ANY subsequent action — before QUIT,
// before the \Sent copy, before anything. Client.Send takes the persist as a
// callback and invokes it between reading the 250 and issuing QUIT, so the
// ordering is structural, not conventional.
//
// RULE 2 — NEVER RETRY AFTER 250. execute() branches on accepted_at FIRST: a
// row with it set goes straight to the post-send steps and the transport is
// never touched again, no matter what the \Sent copy or anything after it
// does. Pre-acceptance transients (connection refused, 4xx) re-queue with
// exponential backoff up to an attempt cap; a permanent 5xx is final — state
// 'failed', a visible deliveryStatus, no retry.
//
// RULE 3 — SINGLE EXECUTION. The claim is FOR UPDATE SKIP LOCKED
// (store.ClaimDueSendIntents): of any number of concurrent executors, exactly
// one takes a given row. The cancel is a compare-and-set on state='queued' ∧
// accepted_at IS NULL, so cancel-vs-claim also has exactly one winner.
//
// # The residual double-send window, stated honestly
//
// The design cannot make the pair (SMTP accept, local persist) atomic: the
// 250 happens on the server, the persist happens in PostgreSQL, and no
// two-phase protocol exists between them. The residual window is therefore
// exactly the persist call itself:
//
//   - A crash after the 250 is read but before MarkSendIntentAccepted commits
//     leaves a row that looks unsent. Recovery re-probes \Sent by Message-ID
//     first (the second net of ADR §4 — it catches the case where the copy
//     already landed, including a server-side auto-save), and only then
//     re-sends. If neither net holds — crash inside that single UPDATE, no
//     Sent copy yet — the message CAN go out twice.
//   - The same window reopens if PostgreSQL is unreachable at the moment of
//     acceptance: Send has succeeded, the persist fails. The executor retries
//     the persist in place with backoff (persistAcceptance) and, if the store
//     stays down, leaves the row in_flight and logs at Error level — it never
//     re-queues an acceptance it could not record, so the double-send needs a
//     SECOND failure (a crash before the store recovers) to materialize.
//
// Everything else — crash before claim, during connect, mid-DATA, after
// persist, between post-send steps — is proven single-delivery by the crash
// matrix in outbox_test.go: the recovery path completes post-send steps
// without ever re-transmitting.
//
// # Undo (W-A3)
//
// The undo window is not a timer: it is the row's not_before, which the claim
// query respects. Within the window the row is state='queued' and cancelable
// by the CAS above (RFC 8621 §7.5's undoStatus "pending" → "canceled");
// after it, the executor claims the row and the cancel loses the race
// honestly (§7.5 cannotUnsend). A canceled submission transmits nothing and
// leaves nothing — no SMTP connection is ever opened for it.
package submit
