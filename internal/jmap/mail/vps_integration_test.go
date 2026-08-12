package mail_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/jmap/mail"
	"github.com/GrupoNU/moov/internal/store"
	syncengine "github.com/GrupoNU/moov/internal/sync"
)

// The J3 end-to-end test: the JMAP layer answering over mail that the SYNC
// ENGINE put in the store from a real Dovecot — not from a fixture.
//
// This is the point of the epic that no fake can make: Email/query and
// Email/changes read rows the watcher wrote, so a mismatch between what the
// engine persists and what the query layer expects (a column the engine leaves
// null, a flag the mapping misreads, a tsvector that never gets built) shows
// up here and nowhere else.
//
// Environment — the same names internal/sync's integration suite uses, so one
// configuration serves both:
//
//	MOOV_IMAP_TEST_HOST       required — "dovecot" inside the Mailcow network
//	MOOV_IMAP_TEST_PORT       optional, default 143
//	MOOV_IMAP_TEST_USER       required — a DEDICATED test mailbox
//	MOOV_IMAP_TEST_PASSWORD   required — environment only, never a file
//	MOOV_IMAP_TEST_SERVERNAME optional — the name the certificate carries
//	MOOV_IMAP_TEST_INSECURE   optional — "1" to skip verification (dev only)
//	MOOV_TEST_DATABASE_URL    required — the store
//
// Without them the test skips, exactly like every other integration suite in
// this repo.

// vpsIMAPConfig builds the IMAP configuration or skips.
func vpsIMAPConfig(t *testing.T) imap.Config {
	t.Helper()

	host := os.Getenv("MOOV_IMAP_TEST_HOST")
	user := os.Getenv("MOOV_IMAP_TEST_USER")
	pass := os.Getenv("MOOV_IMAP_TEST_PASSWORD")
	if host == "" || user == "" || pass == "" {
		t.Skip("integration test: set MOOV_IMAP_TEST_HOST, MOOV_IMAP_TEST_USER and " +
			"MOOV_IMAP_TEST_PASSWORD to run against a real Dovecot")
	}

	cfg := imap.Config{
		Host:          host,
		Username:      user,
		Password:      pass,
		TLSServerName: os.Getenv("MOOV_IMAP_TEST_SERVERNAME"),
	}
	if p := os.Getenv("MOOV_IMAP_TEST_PORT"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			t.Fatalf("MOOV_IMAP_TEST_PORT=%q is not a number: %v", p, err)
		}
		cfg.Port = n
	}
	if os.Getenv("MOOV_IMAP_TEST_INSECURE") == "1" {
		cfg.InsecureSkipVerify = true
	}
	return cfg
}

// TestVPSIntegrationJMAPOverSyncedMail runs a real initial sync and then drives
// the whole J3 surface over the result.
func TestVPSIntegrationJMAPOverSyncedMail(t *testing.T) {
	cfg := vpsIMAPConfig(t)
	f := newFixture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// The account must name the real server, since the syncer reads the folder
	// list from it.
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

	// ---- the JMAP layer, over what the engine just wrote -------------------

	// Mailbox/get tells us which folders the engine discovered; Email/query is
	// then driven against the one that actually holds mail.
	boxes := f.callQuery(t, "Mailbox/get", fmt.Sprintf(`{"accountId":%q,"ids":null}`, f.accountID()))
	list, ok := boxes["list"].([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("Mailbox/get returned no mailboxes: %v", boxes)
	}

	var inboxID string
	var inboxTotal float64
	for _, entry := range list {
		box, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("mailbox entry is %T", entry)
		}
		if box["role"] == "inbox" {
			inboxID, _ = box["id"].(string)
			inboxTotal, _ = box["totalEmails"].(float64)
		}
	}
	if inboxID == "" {
		t.Fatal("the synced account has no inbox")
	}
	t.Logf("synced inbox %s holds %d messages", inboxID, int(inboxTotal))
	if inboxTotal == 0 {
		t.Skip("the test mailbox is empty; seed it to exercise the query paths")
	}

	// Email/query over the folder the engine filled.
	folder := f.callQuery(t, "Email/query", fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":%q},"calculateTotal":true}`, f.accountID(), inboxID))
	ids := queryIDs(t, folder)
	if len(ids) == 0 {
		t.Fatalf("Email/query found nothing in a mailbox reporting %d messages", int(inboxTotal))
	}
	t.Logf("Email/query returned %d ids from the synced inbox", len(ids))

	// Every id must resolve through Email/get — the two families agreeing over
	// engine-written rows is what J4 will point Bulwark at.
	got := f.callQuery(t, "Email/get", fmt.Sprintf(
		`{"accountId":%q,"ids":[%q],"properties":["id","subject","from","receivedAt","preview"]}`,
		f.accountID(), ids[0]))
	if objs, ok := got["list"].([]any); !ok || len(objs) != 1 {
		t.Fatalf("Email/get did not resolve the queried id %s: %v", ids[0], got)
	}

	// A TEXT search over real, engine-parsed content. The subject of the first
	// result is used as the needle, so the test needs no seeding contract: it
	// proves the tsvector the engine built is queryable through JMAP.
	first := f.callQuery(t, "Email/get", fmt.Sprintf(
		`{"accountId":%q,"ids":[%q],"properties":["subject"]}`, f.accountID(), ids[0]))
	subject := firstSubject(t, first)
	if term := firstWord(subject); term != "" {
		hits := f.callQuery(t, "Email/query", fmt.Sprintf(
			`{"accountId":%q,"filter":{"text":%s}}`, f.accountID(), mustJSON(t, term)))
		found := false
		for _, id := range queryIDs(t, hits) {
			if id == ids[0] {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("a text search for %q (from the message's own subject) did not return %s; "+
				"the engine's tsvector and the JMAP text filter disagree", term, ids[0])
		} else {
			t.Logf("text search for %q found the message through the engine-built index", term)
		}
	}

	// ---- a live changes round trip ----------------------------------------
	//
	// The cursor is taken AFTER the sync, then a real flag change is applied
	// through the store exactly as the incremental sync would when Dovecot
	// reports one. Email/changes must report precisely that message.
	cursor := f.emailState(t)
	time.Sleep(10 * time.Millisecond)

	targetID, err := mail.DecodeEmailID(ids[0])
	if err != nil {
		t.Fatalf("decoding %s: %v", ids[0], err)
	}
	state, err := f.store.GetMessageState(ctx, targetID)
	if err != nil {
		t.Fatalf("GetMessageState: %v", err)
	}
	// Toggle \Seen, which is the single most common change a real mailbox
	// produces.
	newFlags := state.Flags ^ store.FlagSeen
	if err := f.store.UpdateFlags(ctx, []store.FlagUpdate{
		{MessageID: targetID, Flags: newFlags, Keywords: state.Keywords, ModSeqSeen: state.ModSeqSeen + 1},
	}); err != nil {
		t.Fatalf("UpdateFlags: %v", err)
	}

	changed := f.callQuery(t, "Email/changes", fmt.Sprintf(
		`{"accountId":%q,"sinceState":%q}`, f.accountID(), cursor))

	updated := stringSet(t, changed, "updated")
	if !updated[ids[0]] {
		t.Errorf("Email/changes did not report %s as updated after a real flag write; got %v",
			ids[0], keys(updated))
	}
	if created := stringSet(t, changed, "created"); created[ids[0]] {
		t.Errorf("a message that existed before the cursor was reported as created")
	}

	// And the keyword the JMAP layer now reports must match what was written —
	// the mapping the whole read path depends on.
	after := f.callQuery(t, "Email/get", fmt.Sprintf(
		`{"accountId":%q,"ids":[%q],"properties":["keywords"]}`, f.accountID(), ids[0]))
	obj, ok := after["list"].([]any)
	if !ok || len(obj) != 1 {
		t.Fatalf("Email/get after the change: %v", after)
	}
	email, ok := obj[0].(map[string]any)
	if !ok {
		t.Fatalf("email object is %T", obj[0])
	}
	keywords, ok := email["keywords"].(map[string]any)
	if !ok {
		t.Fatalf("keywords is %T, want an object", email["keywords"])
	}
	_, hasSeen := keywords[mail.KeywordSeen]
	if want := newFlags.Has(store.FlagSeen); hasSeen != want {
		t.Errorf("$seen present = %v after writing flags %v, want %v", hasSeen, newFlags, want)
	}

	// Mailbox/changes must notice too: the unread count moved.
	mboxChanged := f.callQuery(t, "Mailbox/changes", fmt.Sprintf(
		`{"accountId":%q,"sinceState":%q}`, f.accountID(), cursor))
	if got := stringSet(t, mboxChanged, "updated"); !got[inboxID] {
		t.Errorf("Mailbox/changes did not report the inbox after a flag change; got %v", keys(got))
	}
}

// firstSubject pulls the subject out of an Email/get response.
func firstSubject(t *testing.T, resp map[string]any) string {
	t.Helper()
	list, ok := resp["list"].([]any)
	if !ok || len(list) == 0 {
		return ""
	}
	obj, ok := list[0].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := obj["subject"].(string)
	return s
}

// firstWord returns the first token of a subject long enough to be a usable
// search term, or "" when there is none.
func firstWord(subject string) string {
	for _, field := range splitWords(subject) {
		if len([]rune(field)) >= 4 {
			return field
		}
	}
	return ""
}

// splitWords splits on anything that is not a letter or digit, which is close
// enough to the tokenizer for picking a search term out of a subject.
func splitWords(s string) []string {
	var out []string
	var cur []rune
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r > 127:
			cur = append(cur, r)
		default:
			if len(cur) > 0 {
				out = append(out, string(cur))
				cur = nil
			}
		}
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}

// mustJSON renders a value as a JSON literal for embedding in a request body.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshaling %v: %v", v, err)
	}
	return string(raw)
}

// Compile-time proof that the store adapter satisfies the changes contract.
//
// SearchReader is deliberately NOT asserted here: it is stated in terms of the
// unexported searchFilter/sortSpec (search.go), so only the package's own tests
// can name it — which is the same encapsulation that keeps the refusal
// decisions in query.go rather than in callers.
var _ mail.ChangesReader = (*mail.Adapter)(nil)
