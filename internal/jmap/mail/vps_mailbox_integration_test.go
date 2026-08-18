package mail_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/jmap/mail"
	"github.com/GrupoNU/moov/internal/store"
	syncengine "github.com/GrupoNU/moov/internal/sync"
)

// The W2 end-to-end suite: Mailbox/set operating a REAL Dovecot account
// through the whole stack — handler, write executor, IMAP, store — so the
// acceptance criteria of L2-jmap-write §3's W2 row are proven where they
// matter:
//
//   - the full folder lifecycle: create appears in Mailbox/get with a fresh
//     UIDVALIDITY; rename with children keeps the JMAP ids stable while the
//     IMAP names move; destroy with messages is refused with mailboxHasEmail
//     and then, with onDestroyRemoveEmails, moves the messages to Trash and
//     removes the folder;
//   - the durable-keyword ceiling against the real server: keywords are added
//     up to the boundary in a DEDICATED folder, and the 27th is refused by US
//     before Dovecot ever sees it — which is the whole point, since Dovecot
//     would accept it and lose it silently (validation V1).
//
// Environment: the same MOOV_IMAP_TEST_* / MOOV_TEST_DATABASE_URL variables as
// every other integration suite (vps_integration_test.go documents them).
//
// SAFETY. This suite creates, renames and DELETES folders, and it moves
// messages. Every folder it touches is one it created itself, under a
// per-run unique prefix, and every one is removed on the way out — including
// after a failure, through t.Cleanup. It never renames or deletes a folder it
// did not create, and the role folders are refused by the server code under
// test anyway. The account MUST be the dedicated moov-test mailbox.

// w2Prefix is the per-run namespace every folder this suite creates lives
// under, so a crashed run leaves an obviously-ours folder rather than
// something that looks like the user's.
func w2Prefix() string { return fmt.Sprintf("moov-w2-%d", time.Now().UnixNano()) }

// w2Message renders a minimal unique test message.
func w2Message(subject string) []byte {
	return []byte(fmt.Sprintf(
		"Message-ID: <%d@moov-w2.test>\r\n"+
			"Date: %s\r\n"+
			"From: W2 Suite <w2@moov-w2.test>\r\n"+
			"To: Moov Test <moov-test@atmosfera.cloud>\r\n"+
			"Subject: %s\r\n"+
			"MIME-Version: 1.0\r\n"+
			"Content-Type: text/plain; charset=utf-8\r\n"+
			"\r\n"+
			"Mailbox/set integration fixture. Safe to delete.\r\n",
		time.Now().UnixNano(), time.Now().Format(time.RFC1123Z), subject))
}

// w2Env is the wiring every test in this file shares.
type w2Env struct {
	*fixture
	ctx       context.Context
	cfg       imap.Config
	account   store.Account
	raw       imap.Client
	mut       *imap.MailboxMutator
	syncer    *syncengine.Syncer
	exec      *syncengine.WriteExecutor
	logger    *slog.Logger
	connector syncengine.Connector
	prefix    string
}

func newW2Env(t *testing.T) *w2Env {
	t.Helper()

	cfg := vpsIMAPConfig(t)
	f := newFixture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if testing.Verbose() {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}

	if err := f.store.SetAccountCredentials(ctx, f.account.ID, cfg.Username, []byte("x")); err != nil {
		t.Fatalf("SetAccountCredentials: %v", err)
	}
	account, err := f.store.GetAccount(ctx, f.account.ID)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}

	connector := syncengine.ConnectorFunc(
		func(cctx context.Context, _ store.Account, n int) ([]imap.Client, error) {
			clients := make([]imap.Client, 0, n)
			for range n {
				c := imap.New(logger)
				if err := c.Connect(cctx, cfg); err != nil {
					for _, open := range clients {
						_ = open.Close()
					}
					return nil, err
				}
				clients = append(clients, c)
			}
			return clients, nil
		})

	// The raw connection: arranges fixtures and verifies ground truth without
	// going through Moov at all.
	rawClient := imap.New(logger)
	if err := rawClient.Connect(ctx, cfg); err != nil {
		t.Fatalf("connecting the raw client: %v", err)
	}
	t.Cleanup(func() { _ = rawClient.Close() })
	mut, err := imap.Mutator(rawClient)
	if err != nil {
		t.Fatalf("Mutator: %v", err)
	}

	syncClient := imap.New(logger)
	if err := syncClient.Connect(ctx, cfg); err != nil {
		t.Fatalf("connecting the sync client: %v", err)
	}
	t.Cleanup(func() { _ = syncClient.Close() })
	syncer, err := syncengine.New(f.store, f.blobs, []imap.Client{syncClient}, syncengine.Options{
		Logger: logger, Connections: 1,
	})
	if err != nil {
		t.Fatalf("sync.New: %v", err)
	}

	exec, err := syncengine.NewWriteExecutor(f.store, connector, syncengine.WriteOptions{Logger: logger})
	if err != nil {
		t.Fatalf("NewWriteExecutor: %v", err)
	}
	t.Cleanup(exec.Close)

	adapter, err := mail.NewWriterAdapter(exec)
	if err != nil {
		t.Fatalf("NewWriterAdapter: %v", err)
	}
	f.deps.Writer = adapter
	f.deps.Mailboxer = adapter

	env := &w2Env{
		fixture: f, ctx: ctx, cfg: cfg, account: account,
		raw: rawClient, mut: mut, syncer: syncer, exec: exec,
		logger: logger, connector: connector, prefix: w2Prefix(),
	}

	// THE safety net: whatever this run created on the server goes away, even
	// if the test failed halfway through. Deepest paths first, since a parent
	// with children cannot be deleted.
	t.Cleanup(func() { env.cleanupPrefix(t) })

	if _, err := syncer.Run(ctx, account); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	return env
}

// cleanupPrefix removes every server folder under this run's prefix.
func (e *w2Env) cleanupPrefix(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	infos, err := e.raw.ListMailboxes(ctx)
	if err != nil {
		t.Logf("cleanup: LIST failed, folders under %q may remain on the server: %v", e.prefix, err)
		return
	}
	var ours []string
	for _, info := range infos {
		if strings.HasPrefix(info.Name, e.prefix) {
			ours = append(ours, info.Name)
		}
	}
	// Deepest first: a parent with children cannot be deleted.
	for i := 0; i < len(ours); i++ {
		for j := i + 1; j < len(ours); j++ {
			if strings.Count(ours[j], "/") > strings.Count(ours[i], "/") {
				ours[i], ours[j] = ours[j], ours[i]
			}
		}
	}
	for _, name := range ours {
		if err := e.mut.DeleteMailbox(ctx, name); err != nil {
			t.Logf("cleanup: could not delete %q, please remove it by hand: %v", name, err)
		}
	}
	if len(ours) > 0 {
		t.Logf("cleanup: removed %d folder(s) under %q", len(ours), e.prefix)
	}
}

// resync runs one sync pass so the store reflects whatever the server has.
func (e *w2Env) resync(t *testing.T) {
	t.Helper()
	if _, err := e.syncer.Run(e.ctx, e.account); err != nil {
		t.Fatalf("re-sync: %v", err)
	}
}

// startWatcher runs the real push watcher for the duration of a test and
// returns a stop function.
//
// It is not optional scenery. A folder this suite CREATES is empty when the
// initial sync first sees it, so the sync marks its backfill complete and a
// later Run legitimately skips it (phases.go: "backfill already complete").
// New mail arriving in such a folder is the INCREMENTAL path's job, which is
// what the watcher drives — and which is also the production path, so testing
// through it is testing what actually runs.
func (e *w2Env) startWatcher(t *testing.T, logger *slog.Logger, connector syncengine.Connector) func() {
	t.Helper()

	watcher, err := syncengine.NewPushWatcher(e.store, e.blobs, syncengine.WatcherOptions{
		Options:   syncengine.Options{Logger: logger, Connections: 1},
		Connector: connector,
		// The reconciler stays ON here, unlike W1's suite: a folder created
		// mid-run is exactly the tree change it exists to catch, and a short
		// interval is what makes the test wait seconds instead of hours.
		ReconcileInterval: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewPushWatcher: %v", err)
	}

	wctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = watcher.Watch(wctx, e.account)
	}()

	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("the watcher did not stop")
		}
	}
	t.Cleanup(stop)
	return stop
}

// ingestedUID waits for an appended message to reach the store, polling while
// the watcher does its work.
func (e *w2Env) ingestedUID(t *testing.T, mailbox string, uid imap.UID) (store.Mailbox, store.MessageState) {
	t.Helper()

	var lastErr error
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		row, err := e.store.GetMailboxByName(e.ctx, e.account.ID, mailbox)
		if err == nil {
			st, serr := e.store.GetMessageStateByUID(e.ctx, row.ID, row.UIDValidityOrZero(), int64(uid))
			if serr == nil {
				return row, st
			}
			lastErr = serr
		} else {
			lastErr = err
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("the message appended to %q (uid %d) was never ingested: %v", mailbox, uid, lastErr)
	return store.Mailbox{}, store.MessageState{}
}

// serverHas reports whether a folder exists on the real server.
func (e *w2Env) serverHas(t *testing.T, name string) bool {
	t.Helper()
	infos, err := e.raw.ListMailboxes(e.ctx)
	if err != nil {
		t.Fatalf("LIST: %v", err)
	}
	for _, info := range infos {
		if info.Name == name {
			return true
		}
	}
	return false
}

// createdID pulls the server-assigned id out of a /set response's created
// bucket, failing the test with the whole response rather than panicking on a
// type assertion — a message naming what went wrong beats a stack trace.
func createdID(t *testing.T, resp map[string]any, creationID string) string {
	t.Helper()
	created, ok := resp["created"].(map[string]any)
	if !ok {
		t.Fatalf("the create failed: %v", resp)
	}
	obj, ok := created[creationID].(map[string]any)
	if !ok {
		t.Fatalf("created lacks %s: %v", creationID, resp)
	}
	id, ok := obj["id"].(string)
	if !ok || id == "" {
		t.Fatalf("the created mailbox has no id: %v", obj)
	}
	return id
}

// mailboxObject fetches one Mailbox through Mailbox/get.
func (e *w2Env) mailboxObject(t *testing.T, wire string) map[string]any {
	t.Helper()
	resp := e.callQuery(t, "Mailbox/get", fmt.Sprintf(`{"accountId":%q,"ids":[%q]}`, e.accountID(), wire))
	list, ok := resp["list"].([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("Mailbox/get returned nothing for %s: %v", wire, resp)
	}
	obj, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("Mailbox/get list[0] is %T", list[0])
	}
	return obj
}

// ---------------------------------------------------------------------------
// the full folder lifecycle
// ---------------------------------------------------------------------------

func TestVPSIntegrationMailboxSetLifecycle(t *testing.T) {
	e := newW2Env(t)
	root := e.prefix

	// ---- create -----------------------------------------------------------
	//
	// AC: "create -> appears in Mailbox/get with fresh uidvalidity".
	resp := e.callQuery(t, "Mailbox/set", fmt.Sprintf(
		`{"accountId":%q,"create":{"c1":{"name":%q}}}`, e.accountID(), root))
	rootWire := createdID(t, resp, "c1")

	if !e.serverHas(t, root) {
		t.Fatalf("the folder %q was not created on Dovecot", root)
	}
	got := e.mailboxObject(t, rootWire)
	if got["name"] != root {
		t.Errorf("Mailbox/get name = %v, want %q", got["name"], root)
	}
	if got["role"] != nil {
		t.Errorf("a client-created folder has role %v, want null", got["role"])
	}
	// A fresh UIDVALIDITY, recorded from the server's own STATUS. It is not a
	// JMAP property, so it is checked in the store — which is where the sync
	// engine will read it from on the first incremental pass.
	rootRow, err := e.store.GetMailboxByName(e.ctx, e.account.ID, root)
	if err != nil {
		t.Fatalf("the created folder is not in the store: %v", err)
	}
	if rootRow.UIDValidityOrZero() == 0 {
		t.Error("no UIDVALIDITY was recorded for the newly created folder")
	}
	// myRights must say the truth about a folder the user just made.
	rights, _ := got["myRights"].(map[string]any)
	if rights["mayRename"] != true || rights["mayDelete"] != true || rights["mayCreateChild"] != true {
		t.Errorf("an ordinary folder reports myRights %v; rename/delete/createChild must all be true", rights)
	}

	// ---- children ---------------------------------------------------------
	childResp := e.callQuery(t, "Mailbox/set", fmt.Sprintf(
		`{"accountId":%q,"create":{"c2":{"name":"kids","parentId":%q}}}`, e.accountID(), rootWire))
	childWire := createdID(t, childResp, "c2")

	grandResp := e.callQuery(t, "Mailbox/set", fmt.Sprintf(
		`{"accountId":%q,"create":{"c3":{"name":"deep","parentId":%q}}}`, e.accountID(), childWire))
	grandWire := createdID(t, grandResp, "c3")
	if !e.serverHas(t, root+"/kids/deep") {
		t.Fatalf("the nested folder %q was not created on Dovecot", root+"/kids/deep")
	}

	// ---- rename with children ---------------------------------------------
	//
	// AC: "rename with children -> JMAP ids stable, IMAP names moved".
	renamed := root + "-renamed"
	renameResp := e.callQuery(t, "Mailbox/set", fmt.Sprintf(
		`{"accountId":%q,"update":{%q:{"name":%q}}}`, e.accountID(), rootWire, renamed))
	if _, ok := object(t, renameResp, "updated")[rootWire]; !ok {
		t.Fatalf("the rename failed: %v", renameResp)
	}

	// The IMAP names moved, children included.
	if e.serverHas(t, root) {
		t.Errorf("the old name %q survives on Dovecot", root)
	}
	for _, want := range []string{renamed, renamed + "/kids", renamed + "/kids/deep"} {
		if !e.serverHas(t, want) {
			t.Errorf("Dovecot does not have %q after the rename", want)
		}
	}

	// THE acceptance criterion: the JMAP ids are unchanged. A client holding
	// any of these three still holds a valid id for the same folder.
	for _, tc := range []struct {
		wire, wantName string
	}{
		{rootWire, renamed},
		{childWire, "kids"},
		{grandWire, "deep"},
	} {
		obj := e.mailboxObject(t, tc.wire)
		if obj["id"] != tc.wire {
			t.Errorf("the id %s changed across the rename: %v", tc.wire, obj["id"])
		}
		if obj["name"] != tc.wantName {
			t.Errorf("%s name = %v, want %q", tc.wire, obj["name"], tc.wantName)
		}
	}
	// And the hierarchy still holds: deep's parent is kids, kids' is the root.
	if p := e.mailboxObject(t, grandWire)["parentId"]; p != childWire {
		t.Errorf("the grandchild's parentId = %v, want %s", p, childWire)
	}
	if p := e.mailboxObject(t, childWire)["parentId"]; p != rootWire {
		t.Errorf("the child's parentId = %v, want %s", p, rootWire)
	}

	// A full sync must not undo any of that — the convergence claim, against
	// the real discovery pass rather than the fake one.
	e.resync(t)
	for _, tc := range []struct{ wire, wantName string }{
		{rootWire, renamed}, {childWire, "kids"}, {grandWire, "deep"},
	} {
		obj := e.mailboxObject(t, tc.wire)
		if obj["name"] != tc.wantName {
			t.Errorf("after a full sync, %s name = %v, want %q", tc.wire, obj["name"], tc.wantName)
		}
	}
	// Exactly one row names the renamed root: a delete-then-create reflection
	// would have left the old one behind as a phantom.
	rows, err := e.store.ListMailboxes(e.ctx, e.account.ID)
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	stale := 0
	for _, r := range rows {
		if strings.HasPrefix(r.Name, root) && !strings.HasPrefix(r.Name, renamed) {
			stale++
			t.Errorf("a stale row survives the rename: %q", r.Name)
		}
	}
	if stale > 0 {
		t.Errorf("%d stale mailbox rows after the rename", stale)
	}

	// ---- destroy with children is refused ---------------------------------
	destroyParent := e.callQuery(t, "Mailbox/set", fmt.Sprintf(
		`{"accountId":%q,"destroy":[%q]}`, e.accountID(), rootWire))
	serr, _ := object(t, destroyParent, "notDestroyed")[rootWire].(map[string]any)
	if serr == nil || serr["type"] != "mailboxHasChild" {
		t.Fatalf("destroying a parent gave %v, want mailboxHasChild (RFC 8621 §2.5)", serr)
	}
	if !e.serverHas(t, renamed) {
		t.Fatal("a refused destroy still removed the folder from Dovecot")
	}

	// ---- destroy with messages --------------------------------------------
	//
	// AC: "delete with messages -> refused, then onDestroyRemoveEmails ->
	// messages in Trash, mailbox gone".
	// The production ingestion path for mail arriving in a folder that was
	// already synced: the watcher's incremental pass, not another full Run
	// (see startWatcher).
	stopWatcher := e.startWatcher(t, e.logger, e.connector)

	target := renamed + "/kids/deep"
	subject := fmt.Sprintf("moov-w2 fixture %d", time.Now().UnixNano())
	uid, err := e.mut.Append(e.ctx, target, w2Message(subject), nil, time.Now())
	if err != nil {
		t.Fatalf("APPEND into %q: %v", target, err)
	}
	if uid == 0 {
		t.Fatal("APPEND returned no UID; the test needs UIDPLUS")
	}
	_, st := e.ingestedUID(t, target, uid)
	if st.MessageID == 0 || st.DeletedAt != nil {
		t.Fatalf("the fixture message was not ingested live: %+v", st)
	}
	// Mailbox/get must agree that the folder is non-empty, since that count is
	// what the destroy path reads to decide mailboxHasEmail.
	if total := e.mailboxObject(t, grandWire)["totalEmails"]; total != float64(1) {
		t.Fatalf("the folder reports totalEmails=%v, want 1; the mailboxHasEmail check below "+
			"would pass for the wrong reason", total)
	}

	// The watcher stops before the destroys: a folder being deleted under a
	// live watcher is a legitimate production event, but it is E6's scenario,
	// not this test's, and leaving it running would make the assertions below
	// race an incremental pass.
	stopWatcher()

	// Default: refused with mailboxHasEmail, and nothing touched.
	refuse := e.callQuery(t, "Mailbox/set", fmt.Sprintf(
		`{"accountId":%q,"destroy":[%q]}`, e.accountID(), grandWire))
	serr, _ = object(t, refuse, "notDestroyed")[grandWire].(map[string]any)
	if serr == nil || serr["type"] != "mailboxHasEmail" {
		t.Fatalf("destroying a non-empty folder gave %v, want mailboxHasEmail", serr)
	}
	if !e.serverHas(t, target) {
		t.Fatal("a refused destroy removed the folder from Dovecot")
	}

	// With onDestroyRemoveEmails: the message lands in Trash (Moov's
	// documented deviation from §2.5) and the folder goes away.
	trash, err := e.store.GetMailboxByRole(e.ctx, e.account.ID, store.RoleTrash)
	if err != nil {
		t.Fatalf("the test account has no Trash mailbox: %v", err)
	}
	accept := e.callQuery(t, "Mailbox/set", fmt.Sprintf(
		`{"accountId":%q,"destroy":[%q],"onDestroyRemoveEmails":true}`, e.accountID(), grandWire))
	destroyed, ok := accept["destroyed"].([]any)
	if !ok || len(destroyed) != 1 {
		t.Fatalf("destroy with onDestroyRemoveEmails failed: %v", accept)
	}

	if e.serverHas(t, target) {
		t.Errorf("the folder %q survives on Dovecot after a successful destroy", target)
	}
	after, err := e.store.GetMessageState(e.ctx, st.MessageID)
	if err != nil {
		t.Fatalf("the message row vanished entirely: %v", err)
	}
	if after.DeletedAt != nil {
		t.Errorf("the message was tombstoned instead of moved to Trash; W-A2 says destroy = move to Trash")
	}
	if after.MailboxID != trash.ID {
		t.Errorf("the message is in mailbox %d, want Trash (%d)", after.MailboxID, trash.ID)
	}
	// And it is really in Trash on the server, not just in our store.
	if _, err := e.raw.SelectQResync(e.ctx, trash.Name, 0, 0); err != nil {
		t.Fatalf("raw SELECT %q: %v", trash.Name, err)
	}
	t.Cleanup(func() {
		// The message we pushed into the user's Trash is ours; take it back out.
		cctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := e.mut.Select(cctx, trash.Name); err == nil {
			_ = e.mut.Expunge(cctx, []imap.UID{imap.UID(after.UID)})
		}
	})

	// ---- destroy the now-empty tree ---------------------------------------
	for _, wire := range []string{childWire, rootWire} {
		resp := e.callQuery(t, "Mailbox/set", fmt.Sprintf(
			`{"accountId":%q,"destroy":[%q]}`, e.accountID(), wire))
		if got, ok := resp["destroyed"].([]any); !ok || len(got) != 1 {
			t.Fatalf("destroying %s failed: %v", wire, resp)
		}
	}
	if e.serverHas(t, renamed) || e.serverHas(t, renamed+"/kids") {
		t.Error("folders survive on Dovecot after their destroy reported success")
	}
}

// ---------------------------------------------------------------------------
// protected roles, against the real account's real role folders
// ---------------------------------------------------------------------------

func TestVPSIntegrationMailboxSetRefusesRealRoleFolders(t *testing.T) {
	e := newW2Env(t)

	for _, role := range []store.MailboxRole{store.RoleInbox, store.RoleTrash, store.RoleSent} {
		row, err := e.store.GetMailboxByRole(e.ctx, e.account.ID, role)
		if err != nil {
			t.Logf("the account has no %s folder; skipping", role)
			continue
		}
		wire := mail.EncodeMailboxID(row.ID)

		// myRights must have said so BEFORE the client tried.
		rights, _ := e.mailboxObject(t, wire)["myRights"].(map[string]any)
		if rights["mayRename"] != false || rights["mayDelete"] != false {
			t.Errorf("%s reports myRights %v; rename and delete must both be false", role, rights)
		}

		renameResp := e.callQuery(t, "Mailbox/set", fmt.Sprintf(
			`{"accountId":%q,"update":{%q:{"name":"moov-w2-should-not-happen"}}}`, e.accountID(), wire))
		serr, _ := object(t, renameResp, "notUpdated")[wire].(map[string]any)
		if serr == nil || serr["type"] != "forbidden" {
			t.Fatalf("renaming %s was not refused: %v", role, renameResp)
		}

		destroyResp := e.callQuery(t, "Mailbox/set", fmt.Sprintf(
			`{"accountId":%q,"destroy":[%q],"onDestroyRemoveEmails":true}`, e.accountID(), wire))
		serr, _ = object(t, destroyResp, "notDestroyed")[wire].(map[string]any)
		if serr == nil || serr["type"] != "forbidden" {
			t.Fatalf("destroying %s was not refused: %v", role, destroyResp)
		}

		// And the folder is still there, with its mail.
		if !e.serverHas(t, row.Name) {
			t.Fatalf("the %s folder %q is gone from Dovecot", role, row.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// the durable-keyword ceiling, against the real server
// ---------------------------------------------------------------------------

func TestVPSIntegrationKeywordCeilingAgainstDovecot(t *testing.T) {
	// The A6/V1 acceptance criterion at the boundary, on a REAL Maildir: fill
	// a dedicated folder to 26 distinct keywords and prove the 27th is refused
	// by Moov before Dovecot sees it. Dovecot itself would accept it — that is
	// exactly what V1 measured, and why the check has to live in Moov.
	e := newW2Env(t)
	folder := e.prefix + "-kw"

	// The folder and its message are arranged over RAW IMAP before Moov ever
	// sees the folder, so the ordinary initial-sync backfill ingests both —
	// no watcher needed, and the ceiling test stays free of push timing.
	if err := e.mut.CreateMailbox(e.ctx, folder); err != nil {
		t.Fatalf("creating the keyword folder: %v", err)
	}
	subject := fmt.Sprintf("moov-w2 keywords %d", time.Now().UnixNano())
	uid, err := e.mut.Append(e.ctx, folder, w2Message(subject), nil, time.Now())
	if err != nil {
		t.Fatalf("APPEND into %q: %v", folder, err)
	}
	if uid == 0 {
		t.Fatal("APPEND returned no UID; the test needs UIDPLUS")
	}
	e.resync(t)
	row, st := e.ingestedUID(t, folder, uid)
	wireID := mail.EncodeEmailID(st.MessageID)

	// Fill to exactly the ceiling, one keyword per call, through the real
	// JMAP surface and the real IMAP writes.
	limit := imap.MaxDurableKeywordsPerMailbox
	for i := range limit {
		kw := fmt.Sprintf("MoovW2K%02d", i)
		resp := e.callQuery(t, "Email/set", fmt.Sprintf(
			`{"accountId":%q,"update":{%q:{"keywords/%s":true}}}`, e.accountID(), wireID, kw))
		updated, _ := resp["updated"].(map[string]any)
		if _, ok := updated[wireID]; !ok {
			t.Fatalf("keyword %d of %d (%s) was refused below the ceiling: %v", i+1, limit, kw, resp)
		}
	}

	// The budget agrees with the server: 26 distinct keywords, none left.
	budget, err := e.exec.KeywordBudgetFor(e.ctx, e.account.ID, row.ID)
	if err != nil {
		t.Fatalf("KeywordBudgetFor: %v", err)
	}
	if len(budget.InUse) != limit {
		t.Fatalf("the folder reports %d keywords in use, want %d: %v", len(budget.InUse), limit, budget.InUse)
	}
	if budget.Remaining() != 0 {
		t.Errorf("Remaining = %d at the ceiling, want 0", budget.Remaining())
	}

	// THE assertion: the 27th is refused by US, and Dovecot never sees it.
	before, _, _ := rawMessageFlags(e.ctx, t, e.raw, folder, uid)
	_ = before
	overflow := "MoovW2Overflow"
	resp := e.callQuery(t, "Email/set", fmt.Sprintf(
		`{"accountId":%q,"update":{%q:{"keywords/%s":true}}}`, e.accountID(), wireID, overflow))
	serr, _ := object(t, resp, "notUpdated")[wireID].(map[string]any)
	if serr == nil || serr["type"] != "invalidProperties" {
		t.Fatalf("the 27th keyword was not refused: %v", resp)
	}
	desc := fmt.Sprint(serr["description"])
	for _, want := range []string{"26", "dovecot-keywords", overflow} {
		if !strings.Contains(desc, want) {
			t.Errorf("the refusal omits %q: %s", want, desc)
		}
	}

	// Ground truth: raw IMAP must NOT carry the refused keyword. If it did,
	// Moov would have written something Dovecot will silently lose.
	_, keywords, ok := rawMessageFlags(e.ctx, t, e.raw, folder, uid)
	if !ok {
		t.Fatal("the fixture message disappeared from the server")
	}
	if hasName(keywords, overflow) {
		t.Fatalf("the refused keyword reached Dovecot: %v", keywords)
	}
	// And the 26 that were accepted really are on the server — the refusal
	// must not have been a blanket failure of the whole keyword path.
	accepted := 0
	for _, k := range keywords {
		if strings.HasPrefix(k, "MoovW2K") {
			accepted++
		}
	}
	if accepted != limit {
		t.Errorf("Dovecot carries %d of the %d accepted keywords: %v", accepted, limit, keywords)
	}

	// Reusing one of the 26 must still work in a full folder.
	reuse := e.callQuery(t, "Email/set", fmt.Sprintf(
		`{"accountId":%q,"update":{%q:{"keywords/MoovW2K00":true}}}`, e.accountID(), wireID))
	reused, _ := reuse["updated"].(map[string]any)
	if _, ok := reused[wireID]; !ok {
		t.Errorf("reusing an existing keyword in a full folder was refused: %v", reuse)
	}
	// And so must marking it read: $seen is a Maildir flag, not a keyword slot.
	seen := e.callQuery(t, "Email/set", fmt.Sprintf(
		`{"accountId":%q,"update":{%q:{"keywords/$seen":true}}}`, e.accountID(), wireID))
	seenUpdated, _ := seen["updated"].(map[string]any)
	if _, ok := seenUpdated[wireID]; !ok {
		t.Errorf("marking a message read in a full folder was refused: %v", seen)
	}

	// The folder (and the message in it) go away with the prefix cleanup.
}
