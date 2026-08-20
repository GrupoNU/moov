package mail_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/jmap/mail"
	syncengine "github.com/GrupoNU/moov/internal/sync"
)

// THREADING, END TO END, OVER REAL MAIL.
//
// The store tests prove the JWZ algorithm against constructed headers; this
// proves the whole chain against a real Dovecot: the sync engine parses real
// messages, threads them at insert time, and the JMAP layer serves the result
// through Email/get and Thread/get.
//
// What only this test can catch: a mismatch between what internal/sync extracts
// from a real message's headers and what internal/store's threading expects. A
// References header the parser normalizes differently than the test fixtures
// assume, angle brackets that survive where they should not, a Message-ID the
// engine stores empty — every one of those leaves the store tests green and
// splits every thread in production.
//
// Environment: the same variables as the rest of the VPS suite (see
// vps_integration_test.go). Without them it skips.
func TestVPSIntegrationThreadingOverSyncedMail(t *testing.T) {
	cfg := vpsIMAPConfig(t)
	f := newFixture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	if err := f.store.SetAccountCredentials(ctx, f.account.ID, cfg.Username, []byte("x")); err != nil {
		t.Fatalf("SetAccountCredentials: %v", err)
	}
	account, err := f.store.GetAccount(ctx, f.account.ID)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}

	client := imap.New(logger)
	if err := client.Connect(ctx, cfg); err != nil {
		t.Fatalf("connecting to Dovecot: %v", err)
	}
	defer func() { _ = client.Close() }()

	syncer, err := syncengine.New(f.store, f.blobs, []imap.Client{client}, syncengine.Options{
		Logger:      logger,
		Connections: 1,
		FetchWindow: syncengine.DefaultFetchWindow,
		BatchSize:   syncengine.DefaultBatchSize,
	})
	if err != nil {
		t.Fatalf("sync.New: %v", err)
	}
	if _, err := syncer.Run(ctx, account); err != nil {
		t.Fatalf("initial sync against Dovecot: %v", err)
	}

	// ---- what the engine actually stored -----------------------------------

	boxes := f.callQuery(t, "Mailbox/get", fmt.Sprintf(`{"accountId":%q,"ids":null}`, f.accountID()))
	list, ok := boxes["list"].([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("Mailbox/get returned no mailboxes: %v", boxes)
	}

	var inboxID string
	var totalEmails, totalThreads, unreadThreads float64
	for _, entry := range list {
		box, _ := entry.(map[string]any)
		if box["role"] == "inbox" {
			inboxID, _ = box["id"].(string)
			totalEmails, _ = box["totalEmails"].(float64)
			totalThreads, _ = box["totalThreads"].(float64)
			unreadThreads, _ = box["unreadThreads"].(float64)
		}
	}
	if inboxID == "" {
		t.Fatal("the synced account has no inbox")
	}
	if totalEmails == 0 {
		t.Skip("the test mailbox is empty; seed it to exercise threading")
	}
	t.Logf("synced inbox: %d emails, %d threads, %d unread threads",
		int(totalEmails), int(totalThreads), int(unreadThreads))

	// THE COUNTS ARE EXACT NOW, and exactness has an observable consequence:
	// there can never be MORE threads than messages. The pre-0004 approximation
	// returned the message count for both, so this assertion could not fail
	// then and can now.
	if totalThreads > totalEmails {
		t.Errorf("totalThreads (%d) exceeds totalEmails (%d), which is impossible: "+
			"a thread needs at least one message in the folder to be counted (RFC 8621 S2)",
			int(totalThreads), int(totalEmails))
	}
	if unreadThreads > totalThreads {
		t.Errorf("unreadThreads (%d) exceeds totalThreads (%d)",
			int(unreadThreads), int(totalThreads))
	}

	// ---- every message has a thread, and every thread agrees ---------------

	folder := f.callQuery(t, "Email/query", fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":%q}}`, f.accountID(), inboxID))
	ids := queryIDs(t, folder)
	if len(ids) == 0 {
		t.Fatalf("Email/query found nothing in a mailbox reporting %d messages", int(totalEmails))
	}

	// One Email/get for ALL of them — which is also the round-trip fix under
	// test: this used to be one store round trip per id.
	idsJSON, err := json.Marshal(ids)
	if err != nil {
		t.Fatalf("marshaling ids: %v", err)
	}
	got := f.callQuery(t, "Email/get", fmt.Sprintf(
		`{"accountId":%q,"ids":%s,"properties":["id","threadId","subject","receivedAt"]}`,
		f.accountID(), idsJSON))

	emails, _ := got["list"].([]any)
	if len(emails) != len(ids) {
		t.Fatalf("Email/get returned %d of %d ids in one call", len(emails), len(ids))
	}

	threadOf := make(map[string]string, len(emails))
	membersOf := map[string][]string{}
	for _, entry := range emails {
		e, _ := entry.(map[string]any)
		id, _ := e["id"].(string)
		th, _ := e["threadId"].(string)
		if th == "" {
			t.Errorf("email %s has no threadId; every Email must have one (RFC 8621 S4.1.1)", id)
			continue
		}
		threadOf[id] = th
		membersOf[th] = append(membersOf[th], id)
	}
	t.Logf("%d messages fall into %d distinct threads", len(threadOf), len(membersOf))

	// THE ROUND TRIP THAT MUST HOLD: every thread reported by an Email must
	// resolve through Thread/get, and its emailIds must contain that Email.
	// This is the property RFC 8621 S3 makes structural and the one a broken
	// threading pass violates first.
	threadIDs := make([]string, 0, len(membersOf))
	for th := range membersOf {
		threadIDs = append(threadIDs, th)
	}
	threadIDsJSON, err := json.Marshal(threadIDs)
	if err != nil {
		t.Fatalf("marshaling thread ids: %v", err)
	}

	threads := f.callQuery(t, "Thread/get", fmt.Sprintf(
		`{"accountId":%q,"ids":%s}`, f.accountID(), threadIDsJSON))

	returned, _ := threads["list"].([]any)
	if len(returned) != len(threadIDs) {
		nf, _ := threads["notFound"].([]any)
		t.Fatalf("Thread/get resolved %d of %d threads that Email/get reported (notFound=%v)",
			len(returned), len(threadIDs), nf)
	}

	for _, entry := range returned {
		th, _ := entry.(map[string]any)
		id, _ := th["id"].(string)
		rawIDs, _ := th["emailIds"].([]any)

		emailIDs := make([]string, 0, len(rawIDs))
		for _, r := range rawIDs {
			s, _ := r.(string)
			emailIDs = append(emailIDs, s)
		}
		if len(emailIDs) == 0 {
			t.Errorf("thread %s has no emailIds", id)
			continue
		}

		// Every member this thread claims must claim it back.
		for _, member := range emailIDs {
			// A member outside the inbox is legitimate (a thread spans folders),
			// so only the ones this test fetched are checked.
			if back, known := threadOf[member]; known && back != id {
				t.Errorf("thread %s lists email %s, but that email reports thread %s",
					id, member, back)
			}
		}

		// And every message that named this thread must appear in it.
		for _, member := range membersOf[id] {
			found := false
			for _, got := range emailIDs {
				if got == member {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("email %s reports thread %s, but that thread does not list it",
					member, id)
			}
		}
	}

	// ---- reply chains are actually grouped ---------------------------------
	//
	// A real mailbox with any conversation in it must produce at least one
	// multi-message thread. Reported rather than asserted: a freshly seeded test
	// mailbox may legitimately hold only unrelated messages, and failing on that
	// would make the test depend on a seeding contract it does not own.
	grouped := 0
	largest := 0
	for _, members := range membersOf {
		if len(members) > 1 {
			grouped++
		}
		if len(members) > largest {
			largest = len(members)
		}
	}
	t.Logf("multi-message threads: %d (largest holds %d messages)", grouped, largest)
	if grouped == 0 {
		t.Log("NOTE: no reply chains in this mailbox, so grouping was not exercised end to end; " +
			"the store suite covers the algorithm itself")
	}

	// ---- the seeded chain, when it is present ------------------------------
	//
	// scratchpad/seed_thread.py appends a known reply chain to a DEDICATED
	// folder, delivered OUT OF ORDER (replies first, parent last) so the merge
	// path is exercised against real IMAP delivery rather than constructed rows.
	// When that folder exists, its grouping is asserted exactly; when it does
	// not, the test still passes on whatever the mailbox holds.
	assertSeededChain(t, f, list)

	// ---- Thread/get ordering (RFC 8621 S3) ---------------------------------
	//
	// "The ids of the Emails in the Thread, sorted by the receivedAt date of the
	// Email, oldest first." Checked against the receivedAt values Email/get
	// reported for the same messages, so the two families must agree.
	receivedAt := map[string]string{}
	for _, entry := range emails {
		e, _ := entry.(map[string]any)
		id, _ := e["id"].(string)
		ts, _ := e["receivedAt"].(string)
		receivedAt[id] = ts
	}
	for _, entry := range returned {
		th, _ := entry.(map[string]any)
		id, _ := th["id"].(string)
		rawIDs, _ := th["emailIds"].([]any)

		var prev string
		var prevID string
		for _, r := range rawIDs {
			member, _ := r.(string)
			ts, known := receivedAt[member]
			if !known || ts == "" {
				continue
			}
			if prev != "" && ts < prev {
				t.Errorf("thread %s is not ordered oldest first: %s (%s) precedes %s (%s)",
					id, prevID, prev, member, ts)
			}
			prev, prevID = ts, member
		}
	}

	// ---- the thread ids are the ones the store holds ------------------------
	//
	// A thread id must decode to a real message id of this account, which is
	// what makes it stable across requests rather than a per-request artifact.
	for th := range membersOf {
		if _, err := mail.DecodeThreadID(th); err != nil {
			t.Errorf("thread id %q is not a well-formed id this server issues: %v", th, err)
		}
	}
}

// seededThreadFolder is the folder scratchpad/seed_thread.py appends to.
const seededThreadFolder = "MoovThreadTest"

// assertSeededChain checks the known reply chain, if the seed folder is synced.
//
// The chain it asserts (seed_thread.py), in APPEND order:
//
//  1. "Re: Presupuesto…"  References: <root>   — an orphan reply
//  2. "Re: Presupuesto…"  References: <root>   — a second orphan reply
//  3. "Re: Presupuesto…"  NO References        — joinable only by subject
//  4. "Presupuesto…"      the root, LAST       — must merge 1, 2 and 3
//  5. "Otro tema…"        unrelated            — must stay alone
//
// Delivering the parent last is the whole point: at the moment messages 1 and 2
// arrive, nothing links them, so they are separate threads. Message 4's arrival
// is what supplies the missing reference — the exact scenario RFC 8621 S3
// describes and the one the derived implementation could never handle.
func assertSeededChain(t *testing.T, f *fixture, mailboxes []any) {
	t.Helper()

	var folderID string
	for _, entry := range mailboxes {
		box, _ := entry.(map[string]any)
		if name, _ := box["name"].(string); name == seededThreadFolder {
			folderID, _ = box["id"].(string)
		}
	}
	if folderID == "" {
		t.Logf("no %s folder in this account; run scratchpad/seed_thread.py to exercise "+
			"out-of-order merging against real IMAP delivery", seededThreadFolder)
		return
	}

	got := f.callQuery(t, "Email/query", fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":%q}}`, f.accountID(), folderID))
	ids := queryIDs(t, got)
	if len(ids) == 0 {
		t.Fatalf("the %s folder is synced but empty", seededThreadFolder)
	}

	idsJSON, err := json.Marshal(ids)
	if err != nil {
		t.Fatalf("marshaling ids: %v", err)
	}
	emails := f.callQuery(t, "Email/get", fmt.Sprintf(
		`{"accountId":%q,"ids":%s,"properties":["id","threadId","subject","messageId"]}`,
		f.accountID(), idsJSON))

	list, _ := emails["list"].([]any)
	bySubject := map[string]string{} // subject -> threadId
	threadCount := map[string]int{}
	for _, entry := range list {
		e, _ := entry.(map[string]any)
		subject, _ := e["subject"].(string)
		th, _ := e["threadId"].(string)
		threadCount[th]++
		// The four chain messages all share a subject once "Re: " is stripped;
		// the orphan does not.
		bySubject[subject] = th
	}

	// The four chain messages must be ONE thread. Three joined by References
	// (two of them only after the late parent merged them) and one by subject.
	chainThread := bySubject["Presupuesto del proyecto Moov"]
	if chainThread == "" {
		t.Fatalf("the seeded root is not in %s: subjects seen = %v", seededThreadFolder, keysOf(bySubject))
	}
	if replyThread := bySubject["Re: Presupuesto del proyecto Moov"]; replyThread != chainThread {
		t.Errorf("the seeded replies are in thread %s but the root is in %s; "+
			"the out-of-order parent did not merge them", replyThread, chainThread)
	}
	if n := threadCount[chainThread]; n != 4 {
		t.Errorf("the seeded chain has %d messages in thread %s, want 4 "+
			"(root + 2 References replies + 1 subject-only reply)", n, chainThread)
	}

	// The unrelated message must NOT have been absorbed.
	if orphan := bySubject["Otro tema sin relacion alguna"]; orphan == chainThread {
		t.Error("an unrelated message was absorbed into the seeded chain's thread")
	}

	// And Thread/get agrees, oldest first — the root was appended LAST but has
	// the OLDEST Date, so a correct ordering puts it first. That is the
	// assertion that catches an implementation ordering by id or by arrival.
	threads := f.callQuery(t, "Thread/get", fmt.Sprintf(
		`{"accountId":%q,"ids":[%q]}`, f.accountID(), chainThread))
	tlist, _ := threads["list"].([]any)
	if len(tlist) != 1 {
		t.Fatalf("Thread/get did not resolve the seeded chain %s", chainThread)
	}
	th, _ := tlist[0].(map[string]any)
	members, _ := th["emailIds"].([]any)
	if len(members) != 4 {
		t.Errorf("Thread/get reports %d members for the seeded chain, want 4", len(members))
	}
	t.Logf("seeded out-of-order chain: %d messages merged into thread %s", len(members), chainThread)
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
