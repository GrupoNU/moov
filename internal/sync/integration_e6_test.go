package sync

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
)

// E6 against a real Dovecot.
//
// The fake proves the engine's LOGIC — orderings, races, failure paths that a
// real server cannot be made to produce on demand. This proves the same paths
// against the thing that actually decides what the protocol means: Dovecot's
// QRESYNC replay, its NOTIFY delivery, its modseq bookkeeping. Spike S2 is the
// reason both exist: go-imap's own suite was green against bytes a real server
// rejected outright, and every finding that mattered came from the server.
//
// This is also where the E6 acceptance criterion "promote the S2 harness" is
// discharged: the S2 scenarios — QRESYNC delta after reconnection, CONDSTORE
// changes, NOTIFY on a non-selected folder, the flag-change case that only the
// patched encoder can see — now run through the REAL stack (imap client → sync
// engine → store) rather than through a spike's bespoke client.
//
// Environment: the same variables as internal/imap's suite plus
// MOOV_TEST_DATABASE_URL. See integration_test.go.

// e6Env is a real-server fixture: a scratch folder on the test mailbox, a
// synced account, and a mutator to change the folder from outside the engine.
type e6Env struct {
	*testEnv

	folder  string
	clients []imap.Client
	mutator *imap.MailboxMutator
	syncer  *Syncer
	opts    Options
}

// newE6Env connects, creates a scratch folder, seeds it and runs the initial
// sync, leaving an account whose cursor is current.
func newE6Env(t *testing.T, seed int) *e6Env {
	t.Helper()

	cfg := integrationConfig(t)
	env := newTestEnv(t)
	env.mustSyncableAccount(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	// Three connections: two for the syncer's pool, one for the mutator, so a
	// mutation never contends with the engine for a connection's selected
	// mailbox.
	clients := make([]imap.Client, 0, 3)
	for range 3 {
		c := imap.New(env.logger)
		if err := c.Connect(ctx, cfg); err != nil {
			t.Fatalf("connecting: %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		clients = append(clients, c)
	}

	mutator, err := imap.Mutator(clients[2])
	if err != nil {
		t.Fatalf("imap.Mutator: %v", err)
	}

	// A scratch folder per test: the test mailbox is shared with the other
	// suites, and a test that mutated INBOX would corrupt their fixtures and
	// theirs would corrupt this one.
	folder := fmt.Sprintf("MoovE6/%s-%d", sanitizeTestName(t.Name()), time.Now().UnixNano())
	if err := mutator.CreateMailbox(ctx, folder); err != nil {
		t.Fatalf("creating %q: %v", folder, err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancelCleanup()

		if err := mutator.DeleteMailbox(cleanupCtx, folder); err == nil {
			return
		} else if !strings.Contains(err.Error(), "deleted under us") {
			t.Logf("cleanup: deleting %q: %v (delete it by hand if it lingers)", folder, err)
			return
		}

		// This session's view of the folder went stale — the test recreated it
		// — so this connection can no longer address it at all. A fresh
		// connection can, which is the same recovery the watcher performs in
		// production.
		fresh := imap.New(env.logger)
		if cerr := fresh.Connect(cleanupCtx, cfg); cerr != nil {
			t.Logf("cleanup: reconnecting to delete %q: %v (delete it by hand)", folder, cerr)
			return
		}
		defer func() { _ = fresh.Close() }()

		freshMutator, merr := imap.Mutator(fresh)
		if merr != nil {
			t.Logf("cleanup: %v (delete %q by hand)", merr, folder)
			return
		}
		if derr := freshMutator.DeleteMailbox(cleanupCtx, folder); derr != nil {
			t.Logf("cleanup: deleting %q on a fresh connection: %v (delete it by hand)", folder, derr)
		}
	})

	for i := range seed {
		if _, err := mutator.Append(ctx, folder,
			realMessage(i, fmt.Sprintf("Seed %d", i)), nil, time.Now()); err != nil {
			t.Fatalf("seeding %q: %v", folder, err)
		}
	}

	opts := Options{
		Logger:       env.logger,
		Connections:  2,
		FetchWindow:  DefaultFetchWindow,
		BatchSize:    DefaultBatchSize,
		ParseWorkers: 4,
	}
	syncer, err := New(env.store, env.blobs, clients[:2], opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := syncer.Run(ctx, env.account); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	return &e6Env{
		testEnv: env, folder: folder, clients: clients,
		mutator: mutator, syncer: syncer, opts: opts,
	}
}

// realMessage renders a small RFC 5322 message with CRLF line endings, which is
// what a real server requires.
func realMessage(idx int, subject string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "Message-ID: <e6-%d-%d@moov.test>\r\n", idx, time.Now().UnixNano())
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "From: Moov E6 <moov-test@example.com>\r\n")
	fmt.Fprintf(&b, "To: Moov E6 <moov-test@example.com>\r\n")
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	fmt.Fprintf(&b, "Cuerpo de prueba E6 para %s.\r\n", subject)
	return []byte(b.String())
}

// mailboxRow reads the scratch folder's stored row.
func (e *e6Env) mailboxRow(t *testing.T) store.Mailbox {
	t.Helper()
	row, err := e.store.GetMailboxByName(context.Background(), e.account.ID, e.folder)
	if err != nil {
		t.Fatalf("GetMailboxByName(%q): %v", e.folder, err)
	}
	return row
}

// pass runs one incremental pass over the scratch folder.
func (e *e6Env) pass(t *testing.T) IncrementalResult {
	t.Helper()
	ctx := context.Background()

	row := e.mailboxRow(t)
	mb := syncMailbox{row: row, info: imap.MailboxInfo{Name: e.folder}}

	var res IncrementalResult
	err := e.syncer.conns.withConn(ctx, func(c imap.Client) error {
		var perr error
		res, perr = e.syncer.incrementalMailbox(ctx, c, e.account, mb, e.logger)
		return perr
	})
	if err != nil {
		t.Fatalf("incremental pass: %v", err)
	}
	return res
}

// liveUIDs returns the UIDs stored and not tombstoned for the scratch folder.
func (e *e6Env) liveUIDs(t *testing.T) []int64 {
	t.Helper()
	row := e.mailboxRow(t)

	rows, err := e.store.Pool().Query(context.Background(), `
		SELECT uid FROM message_state
		 WHERE mailbox_id = $1 AND deleted_at IS NULL ORDER BY uid`, row.ID)
	if err != nil {
		t.Fatalf("querying live uids: %v", err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			t.Fatalf("scanning uid: %v", err)
		}
		out = append(out, uid)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading live uids: %v", err)
	}
	return out
}

// flagsOf reads a stored message's flags by UID.
func (e *e6Env) flagsOf(t *testing.T, uid int64) (store.Flags, bool) {
	t.Helper()
	row := e.mailboxRow(t)
	st, err := e.store.GetMessageStateByUID(context.Background(), row.ID, row.UIDValidityOrZero(), uid)
	if err != nil {
		return 0, false
	}
	return st.Flags, true
}

// TestIntegrationE6DeltaAfterOfflineChanges is the E6 acceptance criterion
// against a real server: flags, an expunge and an arrival all happen while the
// engine is not looking, and one reconnection has to reconcile every one of
// them.
//
// This is the S2 QRESYNC/CONDSTORE scenario promoted to the real stack: the
// delta arrives through Dovecot's own VANISHED (EARLIER) replay and its own
// CHANGEDSINCE fetch, and lands in the store through the production pipeline.
func TestIntegrationE6DeltaAfterOfflineChanges(t *testing.T) {
	e := newE6Env(t, 6)
	ctx := context.Background()

	before := e.liveUIDs(t)
	if len(before) != 6 {
		t.Fatalf("the initial sync stored %d messages, want 6: %v", len(before), before)
	}

	// --- changes made while the engine is "offline" -------------------------

	if err := e.mutator.Select(ctx, e.folder); err != nil {
		t.Fatalf("selecting %q on the mutator: %v", e.folder, err)
	}

	// Another client marks one message read and flagged.
	flagTarget := imap.UID(before[1])
	if _, err := e.clients[2].StoreFlags(ctx, []imap.UID{flagTarget},
		imap.FlagDelta{Op: imap.FlagsAdd, Flags: []string{"seen", "flagged"}}, 0); err != nil {
		t.Fatalf("setting flags on uid %d: %v", flagTarget, err)
	}

	// Another client deletes one.
	expungeTarget := imap.UID(before[4])
	if err := e.mutator.Expunge(ctx, []imap.UID{expungeTarget}); err != nil {
		t.Fatalf("expunging uid %d: %v", expungeTarget, err)
	}

	// And two messages arrive.
	newUID, err := e.mutator.Append(ctx, e.folder, realMessage(100, "Arrived offline A"), nil, time.Now())
	if err != nil {
		t.Fatalf("appending: %v", err)
	}
	if _, err := e.mutator.Append(ctx, e.folder, realMessage(101, "Arrived offline B"), nil, time.Now()); err != nil {
		t.Fatalf("appending: %v", err)
	}

	// --- one reconnection has to fix all of it ------------------------------

	res := e.pass(t)
	t.Logf("REAL SERVER delta: new=%d flags=%d vanished=%d modseq %d->%d in %s",
		res.New, res.FlagsUpdated, res.Vanished, res.FromModSeq, res.ToModSeq,
		res.Elapsed.Round(time.Millisecond))

	if res.New != 2 {
		t.Errorf("the delta stored %d new messages, want 2", res.New)
	}
	if res.Vanished != 1 {
		t.Errorf("the delta tombstoned %d messages, want 1", res.Vanished)
	}
	if res.FlagsUpdated != 1 {
		t.Errorf("the delta updated %d flag sets, want 1", res.FlagsUpdated)
	}

	flags, ok := e.flagsOf(t, int64(flagTarget))
	if !ok {
		t.Fatalf("uid %d disappeared", flagTarget)
	}
	if !flags.Has(store.FlagSeen) || !flags.Has(store.FlagFlagged) {
		t.Errorf("uid %d has flags %v, want \\Seen and \\Flagged", flagTarget, flags)
	}

	live := e.liveUIDs(t)
	if len(live) != 7 {
		t.Errorf("the folder holds %d live messages, want 7 (6 - 1 + 2): %v", len(live), live)
	}
	for _, u := range live {
		if imap.UID(u) == expungeTarget {
			t.Errorf("the expunged uid %d is still live", expungeTarget)
		}
	}
	var sawNew bool
	for _, u := range live {
		if imap.UID(u) == newUID {
			sawNew = true
		}
	}
	if !sawNew && newUID != 0 {
		t.Errorf("the appended message (uid %d) is not stored", newUID)
	}

	// A second pass must be a no-op: idempotency against a real server.
	if second := e.pass(t); second.Changed() {
		t.Errorf("a second pass over an unchanged folder reported %+v", second)
	}
}

// TestIntegrationE6NotifyLatency is the "<1 s" acceptance criterion, measured.
//
// It is the whole product claim of regla 1 ("push real") reduced to a number: a
// message is appended by another client, and the time until it is queryable in
// Moov's store is what a user experiences as the mail arriving.
func TestIntegrationE6NotifyLatency(t *testing.T) {
	e := newE6Env(t, 3)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// The watcher gets its own connections, as it does in production.
	cfg := integrationConfig(t)
	observations := make(chan WatchObservation, 64)
	watcher, err := NewPushWatcher(e.store, e.blobs, WatcherOptions{
		Options: e.opts,
		Connector: ConnectorFunc(func(ctx context.Context, _ store.Account, n int) ([]imap.Client, error) {
			out := make([]imap.Client, 0, n)
			for range n {
				c := imap.New(e.logger)
				if err := c.Connect(ctx, cfg); err != nil {
					for _, open := range out {
						_ = open.Close()
					}
					return nil, err
				}
				out = append(out, c)
			}
			return out, nil
		}),
		ReconcileInterval: -1, // only push may explain what lands
		OnEvent: func(obs WatchObservation) {
			select {
			case observations <- obs:
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("NewPushWatcher: %v", err)
	}

	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	done := make(chan error, 1)
	go func() { done <- watcher.Watch(watchCtx, e.account) }()

	// Wait for the connect-time sweep to finish, so the measurement below times
	// the notification and not the startup.
	waitFor(t, 60*time.Second, func() bool {
		for {
			select {
			case obs := <-observations:
				if obs.Kind == ObsConnected {
					return true
				}
			default:
				return false
			}
		}
	}, "the watcher never connected to the real server")

	// Let the initial sweep settle.
	time.Sleep(2 * time.Second)
	baseline := len(e.liveUIDs(t))

	// --- the measurement ----------------------------------------------------

	// More than a handful of rounds, because the first one after a cold start
	// pays for things the criterion is not about — the parse pool's first
	// allocations, PostgreSQL's first plan for each statement, the connection's
	// first write — and a single sample of a warm system is not evidence
	// either.
	const rounds = 7
	samples := make([]time.Duration, 0, rounds)
	var total time.Duration
	var worst time.Duration

	for round := range rounds {
		if err := e.mutator.Select(ctx, e.folder); err != nil {
			t.Fatalf("selecting for round %d: %v", round, err)
		}

		started := time.Now()
		if _, err := e.mutator.Append(ctx, e.folder,
			realMessage(200+round, fmt.Sprintf("Push latency %d", round)), nil, time.Now()); err != nil {
			t.Fatalf("appending in round %d: %v", round, err)
		}

		want := baseline + round + 1
		deadline := time.Now().Add(30 * time.Second)
		var elapsed time.Duration
		for {
			if len(e.liveUIDs(t)) >= want {
				elapsed = time.Since(started)
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("round %d: the pushed message never reached the store within 30s", round)
			}
			time.Sleep(10 * time.Millisecond)
		}

		samples = append(samples, elapsed)
		total += elapsed
		if elapsed > worst {
			worst = elapsed
		}
		t.Logf("round %d: NOTIFY -> visible in the store in %s", round, elapsed.Round(time.Millisecond))
	}

	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	median := sorted[len(sorted)/2]
	average := total / rounds

	t.Logf("NOTIFY -> store latency over %d rounds: median %s, average %s, worst %s",
		rounds, median.Round(time.Millisecond), average.Round(time.Millisecond),
		worst.Round(time.Millisecond))

	// The acceptance criterion, measured on the median.
	//
	// The median rather than the maximum, because this runs on a shared VPS
	// against a production Mailcow that is simultaneously serving real mail:
	// the slowest sample measures what else the box was doing, not what the
	// engine costs. The debounce alone spends 150 ms of the budget, so what is
	// really being asserted is that the rest of the path — notification, SELECT,
	// FETCH, parse, insert — fits in the remainder.
	//
	// The worst sample is still reported, and a worst case far outside the bar
	// is called out, because a push engine whose tail is seconds is a push
	// engine with a problem even when its median is fine.
	if median > time.Second {
		t.Errorf("median NOTIFY -> store latency was %s, want under 1s (L2 §3/E6)", median)
	}
	if worst > 3*time.Second {
		t.Errorf("worst NOTIFY -> store latency was %s; the tail is too long even "+
			"allowing for a shared server", worst)
	}

	stopWatch()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Error("the watcher did not stop after cancellation")
	}
}

// TestIntegrationE6ReconcilerRepairsAnInjectedDivergence is the reconciler's
// acceptance criterion against a real server.
//
// The divergence is injected by bypassing the watcher entirely: the mutation is
// made while NO watcher is running, so no notification could have been
// delivered to anyone. The reconciler's STATUS sweep is then the only mechanism
// that could possibly find it.
func TestIntegrationE6ReconcilerRepairsAnInjectedDivergence(t *testing.T) {
	e := newE6Env(t, 4)
	ctx := context.Background()

	before := e.liveUIDs(t)
	if len(before) != 4 {
		t.Fatalf("the initial sync stored %d messages, want 4", len(before))
	}

	// The change happens with no watcher anywhere: this is a lost event by
	// construction, which is exactly what the reconciler exists for.
	if err := e.mutator.Select(ctx, e.folder); err != nil {
		t.Fatalf("selecting: %v", err)
	}
	if _, err := e.mutator.Append(ctx, e.folder,
		realMessage(300, "Injected divergence"), nil, time.Now()); err != nil {
		t.Fatalf("appending: %v", err)
	}
	// And a flag change, which moves only HIGHESTMODSEQ (S2 T4) — the case a
	// counter-only reconciler would miss.
	if _, err := e.clients[2].StoreFlags(ctx, []imap.UID{imap.UID(before[0])},
		imap.FlagDelta{Op: imap.FlagsAdd, Flags: []string{"flagged"}}, 0); err != nil {
		t.Fatalf("setting flags: %v", err)
	}

	if got := len(e.liveUIDs(t)); got != 4 {
		t.Fatalf("the store changed without a sync (%d messages)", got)
	}

	watcher, err := NewPushWatcher(e.store, e.blobs, WatcherOptions{
		Options:           e.opts,
		Connector:         ConnectorFunc(func(context.Context, store.Account, int) ([]imap.Client, error) { return nil, nil }),
		ReconcileInterval: -1,
	})
	if err != nil {
		t.Fatalf("NewPushWatcher: %v", err)
	}

	res, err := watcher.Reconcile(ctx, e.syncer, e.account, e.logger)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	t.Logf("REAL SERVER reconcile: checked=%d diverged=%d repaired=%d in %s",
		res.Checked, res.Diverged, res.Repaired, res.Elapsed.Round(time.Millisecond))
	for _, d := range res.Divergences {
		t.Logf("  divergence: %s — %s", d.Mailbox, d.Reason)
	}

	var found bool
	for _, d := range res.Divergences {
		if d.Mailbox == e.folder {
			found = true
		}
	}
	if !found {
		t.Fatalf("the sweep did not report a divergence on %q: %+v", e.folder, res.Divergences)
	}

	if got := len(e.liveUIDs(t)); got != 5 {
		t.Errorf("after the sweep the folder holds %d messages, want 5", got)
	}
	flags, ok := e.flagsOf(t, before[0])
	if !ok || !flags.Has(store.FlagFlagged) {
		t.Errorf("the sweep did not repair the flag change (flags=%v)", flags)
	}

	// And a second sweep must be quiet FOR THIS FOLDER, which is what makes the
	// reconciler affordable on a six-hour schedule.
	//
	// Scoped to this test's own scratch folder rather than to the whole
	// account: the test mailbox is shared with the other suites and with any
	// real mail arriving at it, so INBOX legitimately moves underneath this
	// assertion. A divergence there is the reconciler working, not failing.
	second, err := watcher.Reconcile(ctx, e.syncer, e.account, e.logger)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	for _, d := range second.Divergences {
		if d.Mailbox == e.folder {
			t.Errorf("the second sweep still reports %q as diverged: %s", e.folder, d.Reason)
		}
	}
}

// TestIntegrationE6UIDValidityResync proves the one failure mode that produces
// visibly WRONG mail rather than merely missing mail, against a real server.
//
// A folder deleted and recreated under the same name gets a fresh UIDVALIDITY,
// so every UID Moov holds now names a different message. The engine must throw
// the local state away rather than deltaing across the discontinuity.
//
// # What this test established about Dovecot, which shaped the code
//
// A connection that had the folder SELECTED when it was deleted refuses that
// name for the rest of its session — "NO Mailbox was deleted under us", or a
// SERVERBUG naming a changed indexid — and the refusal survives UNSELECT and
// survives a plain SELECT without QRESYNC. Only a new connection recovers.
// All three were measured, in that order, while writing this.
//
// The engine's own connections are NOT normally in that state, because the
// syncer selects a mailbox per pass rather than holding one open, which is why
// the resync below simply works. The condition is real for a connection that
// was parked on the folder, so internal/imap reports it as
// imap.ErrMailboxStale and the watcher's reconnection loop is what clears it.
// TestIsStaleIndexError pins the detection; this test covers the recovery that
// matters in production.
func TestIntegrationE6UIDValidityResync(t *testing.T) {
	e := newE6Env(t, 3)
	ctx := context.Background()

	first := e.mailboxRow(t)
	if first.UIDValidity == nil {
		t.Fatal("the folder has no stored uidvalidity after the initial sync")
	}
	originalValidity := *first.UIDValidity

	// Delete and recreate: same name, new UIDVALIDITY.
	if err := e.mutator.DeleteMailbox(ctx, e.folder); err != nil {
		t.Fatalf("deleting %q: %v", e.folder, err)
	}
	if err := e.mutator.CreateMailbox(ctx, e.folder); err != nil {
		t.Fatalf("recreating %q: %v", e.folder, err)
	}
	for i := range 2 {
		if _, err := e.mutator.Append(ctx, e.folder,
			realMessage(400+i, fmt.Sprintf("After recreation %d", i)), nil, time.Now()); err != nil {
			t.Fatalf("appending after recreation: %v", err)
		}
	}

	res := e.pass(t)
	if !res.Resynced {
		t.Fatal("the recreated folder did not trigger a resync")
	}

	after := e.mailboxRow(t)
	if after.UIDValidity == nil || *after.UIDValidity == originalValidity {
		t.Errorf("the stored uidvalidity is %v, want a new one (was %d)",
			after.UIDValidity, originalValidity)
	}

	live := e.liveUIDs(t)
	if len(live) != 2 {
		t.Errorf("after the resync the folder holds %d messages, want 2: %v", len(live), live)
	}

	// The stored content must be the NEW folder's.
	var recreated int
	if err := e.store.Pool().QueryRow(ctx, `
		SELECT count(*) FROM messages m
		  JOIN message_state ms ON ms.message_id = m.id
		 WHERE ms.mailbox_id = $1 AND m.subject LIKE 'After recreation%'`,
		after.ID).Scan(&recreated); err != nil {
		t.Fatalf("counting recreated messages: %v", err)
	}
	if recreated != 2 {
		t.Errorf("%d messages carry the recreated folder's subjects, want 2", recreated)
	}
}

// TestIntegrationE6WatcherSeesAFlagChangeOnANonSelectedFolder is the S2 T4
// finding promoted to the real stack.
//
// A \Flagged toggle moves neither MESSAGES nor UNSEEN, so HIGHESTMODSEQ is the
// only counter that changes — and stock go-imap, which cannot emit the NOTIFY
// STATUS keyword, receives NOTHING for it. With the unpatched encoder this test
// fails, which is precisely why it is here: it is the regression test for the
// patch set the whole design depends on.
func TestIntegrationE6WatcherSeesAFlagChangeOnANonSelectedFolder(t *testing.T) {
	e := newE6Env(t, 3)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	before := e.liveUIDs(t)
	if len(before) == 0 {
		t.Fatal("no messages were synced")
	}
	target := imap.UID(before[0])

	cfg := integrationConfig(t)
	connected := make(chan struct{}, 1)
	watcher, err := NewPushWatcher(e.store, e.blobs, WatcherOptions{
		Options: e.opts,
		Connector: ConnectorFunc(func(ctx context.Context, _ store.Account, n int) ([]imap.Client, error) {
			out := make([]imap.Client, 0, n)
			for range n {
				c := imap.New(e.logger)
				if err := c.Connect(ctx, cfg); err != nil {
					for _, open := range out {
						_ = open.Close()
					}
					return nil, err
				}
				out = append(out, c)
			}
			return out, nil
		}),
		ReconcileInterval: -1, // only push may explain what lands
		OnEvent: func(obs WatchObservation) {
			if obs.Kind == ObsConnected {
				select {
				case connected <- struct{}{}:
				default:
				}
			}
		},
	})
	if err != nil {
		t.Fatalf("NewPushWatcher: %v", err)
	}

	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	go func() { _ = watcher.Watch(watchCtx, e.account) }()

	select {
	case <-connected:
	case <-time.After(60 * time.Second):
		t.Fatal("the watcher never connected")
	}
	time.Sleep(2 * time.Second)

	// The watcher's own connection is parked in NOTIFY+IDLE and has no mailbox
	// selected, so this folder is a NON-SELECTED folder from its point of view
	// — which is the case S2 T4 measured.
	if err := e.mutator.Select(ctx, e.folder); err != nil {
		t.Fatalf("selecting on the mutator: %v", err)
	}
	started := time.Now()
	if _, err := e.clients[2].StoreFlags(ctx, []imap.UID{target},
		imap.FlagDelta{Op: imap.FlagsAdd, Flags: []string{"flagged"}}, 0); err != nil {
		t.Fatalf("setting the flag: %v", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		flags, ok := e.flagsOf(t, int64(target))
		if ok && flags.Has(store.FlagFlagged) {
			t.Logf("flag change on a non-selected folder reached the store in %s "+
				"(this is the NOTIFY STATUS patch working — S2 T4)",
				time.Since(started).Round(time.Millisecond))
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a flag change on a non-selected folder never reached the store; " +
				"the NOTIFY encoder patch (patches/0002) is what makes this visible — see S2 T4")
		}
		time.Sleep(20 * time.Millisecond)
	}

	stopWatch()
}
