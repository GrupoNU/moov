package submit

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/store"
)

// The VPS integration suite: the outbox against the REAL Mailcow stack —
// Postfix submission on :587 with STARTTLS and AUTH, delivery observed over
// real IMAP, recovery against real PostgreSQL rows.
//
// Environment (all required, or the suite skips):
//
//	MOOV_TEST_DATABASE_URL      the PostgreSQL DSN (own migrations applied)
//	MOOV_SMTP_TEST_HOST         the submission server ("postfix" on the mailcow network)
//	MOOV_IMAP_TEST_HOST         the IMAP server ("dovecot")
//	MOOV_IMAP_TEST_USER         the TEST mailbox — moov-test@..., never a real account
//	MOOV_IMAP_TEST_PASSWORD     its password (env only; never a file in this repo)
//
// Optional: MOOV_SMTP_TEST_PORT (587), MOOV_SMTP_TEST_SERVERNAME,
// MOOV_IMAP_TEST_PORT (143), MOOV_IMAP_TEST_SERVERNAME,
// MOOV_IMAP_TEST_INSECURE=1.
//
// Every message this suite sends is a SELF-SEND of the test mailbox, tagged
// with a unique Message-ID, and expunged again in cleanup.

type vpsEnv struct {
	t     *testing.T
	store *store.Store
	smtp  Config
	imap  imap.Config
	user  string

	// sent Message-IDs, expunged from INBOX and Sent in cleanup.
	mu     sync.Mutex
	msgIDs []string
}

func newVPSEnv(t *testing.T) *vpsEnv {
	t.Helper()
	smtpHost := os.Getenv("MOOV_SMTP_TEST_HOST")
	imapHost := os.Getenv("MOOV_IMAP_TEST_HOST")
	user := os.Getenv("MOOV_IMAP_TEST_USER")
	pass := os.Getenv("MOOV_IMAP_TEST_PASSWORD")
	if smtpHost == "" || imapHost == "" || user == "" || pass == "" {
		t.Skip("VPS integration: set MOOV_SMTP_TEST_HOST, MOOV_IMAP_TEST_HOST, MOOV_IMAP_TEST_USER, MOOV_IMAP_TEST_PASSWORD")
	}
	if !strings.HasPrefix(user, "moov-test@") {
		// The brief's hard rule: ONLY the dedicated test mailbox, never a
		// production account. Refusing here beats trusting every future
		// invocation to remember.
		t.Fatalf("VPS integration refuses to run against %q; only the moov-test mailbox is allowed", user)
	}

	env := &vpsEnv{
		t:    t,
		user: user,
		smtp: Config{
			Host:               smtpHost,
			Port:               587,
			TLSServerName:      os.Getenv("MOOV_SMTP_TEST_SERVERNAME"),
			InsecureSkipVerify: os.Getenv("MOOV_IMAP_TEST_INSECURE") == "1",
			Username:           user,
			Password:           pass,
		},
		imap: imap.Config{
			Host:               imapHost,
			Port:               143,
			Username:           user,
			Password:           pass,
			TLSServerName:      os.Getenv("MOOV_IMAP_TEST_SERVERNAME"),
			InsecureSkipVerify: os.Getenv("MOOV_IMAP_TEST_INSECURE") == "1",
		},
	}
	if p := os.Getenv("MOOV_SMTP_TEST_PORT"); p != "" {
		_, _ = fmt.Sscanf(p, "%d", &env.smtp.Port)
	}
	if p := os.Getenv("MOOV_IMAP_TEST_PORT"); p != "" {
		_, _ = fmt.Sscanf(p, "%d", &env.imap.Port)
	}

	env.store = pgStore(t)
	t.Cleanup(env.expungeSent)
	return env
}

func (e *vpsEnv) account(t *testing.T) store.Account {
	return pgAccount(t, e.store)
}

// draftFor assembles the raw draft the RawSource serves: real headers, a Bcc
// that must never reach the wire, and the given Message-ID.
func draftFor(user, msgID string) []byte {
	return []byte("Message-ID: <" + msgID + ">\r\n" +
		"Date: " + time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 +0000") + "\r\n" +
		"From: <" + user + ">\r\n" +
		"To: <" + user + ">\r\n" +
		"Bcc: <" + user + ">\r\n" +
		"Subject: [moov-w3] integration self-send\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" +
		"This message was sent by Moov's W3 integration suite and is deleted by its cleanup.\r\n")
}

func (e *vpsEnv) newMsgID(tag string) string {
	id := fmt.Sprintf("w3-%s-%d.moov@atmosfera.cloud", tag, time.Now().UnixNano())
	e.mu.Lock()
	e.msgIDs = append(e.msgIDs, id)
	e.mu.Unlock()
	return id
}

// realTransport counts real submissions through submit.Send.
type realTransport struct {
	cfg   Config
	mu    sync.Mutex
	sends int
}

func (rt *realTransport) Send(ctx context.Context, _ store.Account, env Envelope, msg io.Reader, onAccepted func(string) error) (Result, error) {
	rt.mu.Lock()
	rt.sends++
	rt.mu.Unlock()
	return Send(ctx, rt.cfg, env, msg, onAccepted)
}

func (rt *realTransport) count() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.sends
}

// imapClient opens a fresh observation connection.
func (e *vpsEnv) imapClient(t *testing.T) imap.Client {
	t.Helper()
	c := imap.New(slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.Connect(ctx, e.imap); err != nil {
		t.Fatalf("IMAP connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// findByMessageID scans a mailbox's most recent messages for a Message-ID and
// returns the matching UIDs plus each match's header block.
func findByMessageID(t *testing.T, c imap.Client, mailbox, msgID string) ([]imap.UID, [][]byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sel, err := c.SelectQResync(ctx, mailbox, 0, 0)
	if err != nil {
		t.Fatalf("selecting %s: %v", mailbox, err)
	}
	// The recent window: the mailbox is a dedicated test account, so a small
	// tail covers everything this suite could have produced.
	var uids []imap.UID
	low := imap.UID(1)
	if sel.UIDNext > 60 {
		low = sel.UIDNext - 60
	}
	for uid := low; uid < sel.UIDNext; uid++ {
		uids = append(uids, uid)
	}
	if len(uids) == 0 {
		return nil, nil
	}

	it, err := c.FetchMessages(ctx, uids, imap.FetchSpec{Headers: true})
	if err != nil {
		t.Fatalf("fetching %s headers: %v", mailbox, err)
	}
	defer func() { _ = it.Close() }()

	var matches []imap.UID
	var headers [][]byte
	for {
		msg, err := it.Next()
		if err != nil {
			t.Fatalf("iterating %s: %v", mailbox, err)
		}
		if msg == nil {
			break
		}
		header := append([]byte(nil), msg.Header...)
		if MessageIDOf(append(header, "\r\n\r\n"...)) == msgID {
			matches = append(matches, msg.UID)
			headers = append(headers, header)
		}
	}
	return matches, headers
}

// waitForDelivery polls INBOX until the Message-ID lands or the deadline
// passes, returning the matches.
func (e *vpsEnv) waitForDelivery(t *testing.T, msgID string, deadline time.Duration) ([]imap.UID, [][]byte) {
	t.Helper()
	c := e.imapClient(t)
	end := time.Now().Add(deadline)
	for {
		uids, _ := findByMessageID(t, c, "INBOX", msgID)
		if len(uids) > 0 {
			// One settle pass so a racing duplicate would be seen too.
			time.Sleep(2 * time.Second)
			return findByMessageID(t, c, "INBOX", msgID)
		}
		if time.Now().After(end) {
			return nil, nil
		}
		time.Sleep(time.Second)
	}
}

// expungeSent removes every message this suite delivered, from INBOX and the
// Sent folder both — the "VPS cleaned" contract.
func (e *vpsEnv) expungeSent() {
	e.mu.Lock()
	ids := append([]string(nil), e.msgIDs...)
	e.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	c := imap.New(slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := c.Connect(ctx, e.imap); err != nil {
		e.t.Logf("cleanup: IMAP connect failed: %v", err)
		return
	}
	defer func() { _ = c.Close() }()

	for _, mailbox := range []string{"INBOX", "Sent"} {
		for _, id := range ids {
			uids, _ := findByMessageID(e.t, c, mailbox, id)
			if len(uids) == 0 {
				continue
			}
			if err := c.Expunge(ctx, uids); err != nil {
				e.t.Logf("cleanup: expunging %v from %s: %v", uids, mailbox, err)
			}
		}
	}
}

// vpsOutbox wires a real-store, real-SMTP outbox with fast polling.
func (e *vpsEnv) vpsOutbox(t *testing.T, transport Transport, raws RawSource) *Outbox {
	t.Helper()
	ob, err := NewOutbox(e.store, transport, newFakeSent(), raws, Options{
		Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	return ob
}

// ---------------------------------------------------------------------------

func TestVPSSelfSendDeliversExactlyOnce(t *testing.T) {
	e := newVPSEnv(t)
	acct := e.account(t)
	msgID := e.newMsgID("send")
	transport := &realTransport{cfg: e.smtp}
	raws := &fakeRaws{raw: map[int64][]byte{7: draftFor(e.user, msgID)}}
	ob := e.vpsOutbox(t, transport, raws)

	payload := fmt.Sprintf(`{"identityId":"primary","mailFrom":%q,"rcptTo":[%q]}`, e.user, e.user)
	in, err := e.store.EnqueueSendIntent(context.Background(), acct.ID, 7, msgID,
		[]byte(payload), time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}

	ob.runOnce(context.Background())

	row, err := e.store.GetSendIntent(context.Background(), acct.ID, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !row.Accepted() {
		t.Fatalf("Postfix did not accept: state=%s err=%q", row.State, row.LastError)
	}
	if !strings.HasPrefix(row.AcceptedReply, "250") || !strings.Contains(row.AcceptedReply, "queued as") {
		t.Errorf("accepted reply = %q, want Postfix's 250 with a queue id", row.AcceptedReply)
	}
	if row.State != store.IntentDone {
		t.Errorf("state = %s, want done", row.State)
	}
	if transport.count() != 1 {
		t.Errorf("transport invoked %d times, want 1", transport.count())
	}

	uids, headers := e.waitForDelivery(t, msgID, 60*time.Second)
	if len(uids) != 1 {
		t.Fatalf("INBOX holds %d copies of %s, want exactly 1", len(uids), msgID)
	}
	// Rule 4 on the real wire: the delivered headers carry no Bcc.
	if bytes.Contains(bytes.ToLower(headers[0]), []byte("\nbcc")) ||
		bytes.HasPrefix(bytes.ToLower(headers[0]), []byte("bcc")) {
		t.Errorf("the delivered message carries a Bcc header — a blind-copy leak:\n%s", headers[0])
	}

	// The empirical question of rule 5, answered with evidence: does the
	// Mailcow stack auto-save a Sent copy for a submission? (The dedupe net
	// exists for either answer; the log records which world we are in.)
	c := e.imapClient(t)
	sentUIDs, _ := findByMessageID(t, c, "Sent", msgID)
	t.Logf("EVIDENCE: Mailcow/Postfix auto-saved Sent copies for this submission: %d (empirical check of rule 5)", len(sentUIDs))
}

func TestVPSUndoLeavesNoTrace(t *testing.T) {
	e := newVPSEnv(t)
	acct := e.account(t)
	msgID := e.newMsgID("undo")
	transport := &realTransport{cfg: e.smtp}
	raws := &fakeRaws{raw: map[int64][]byte{7: draftFor(e.user, msgID)}}
	ob := e.vpsOutbox(t, transport, raws)

	// Enqueued with a 6 s window; canceled inside it.
	payload := fmt.Sprintf(`{"identityId":"primary","mailFrom":%q,"rcptTo":[%q]}`, e.user, e.user)
	in, err := e.store.EnqueueSendIntent(context.Background(), acct.ID, 7, msgID,
		[]byte(payload), time.Now().Add(6*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ob.runOnce(context.Background()) // inside the window: must not touch it
	if _, err := e.store.CancelSendIntent(context.Background(), acct.ID, in.ID); err != nil {
		t.Fatalf("cancel inside the window failed: %v", err)
	}

	// The window passes; the executor sweeps repeatedly; the canceled row
	// must never be claimed.
	time.Sleep(8 * time.Second)
	for range 3 {
		ob.runOnce(context.Background())
	}

	if transport.count() != 0 {
		t.Fatalf("a canceled submission reached Postfix %d times — undo left a trace", transport.count())
	}
	row, err := e.store.GetSendIntent(context.Background(), acct.ID, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != store.IntentCanceled || row.Accepted() {
		t.Errorf("row after undo = state %s accepted %v", row.State, row.Accepted())
	}

	// And the mailbox agrees: nothing arrives.
	if uids, _ := e.waitForDelivery(t, msgID, 20*time.Second); len(uids) != 0 {
		t.Errorf("INBOX received %d copies of a canceled submission", len(uids))
	}
}

func TestVPSPost250CrashRecoveryNeverResends(t *testing.T) {
	e := newVPSEnv(t)
	acct := e.account(t)
	msgID := e.newMsgID("crash")
	raws := &fakeRaws{raw: map[int64][]byte{7: draftFor(e.user, msgID)}}

	// Phase 1 by hand, exactly as the executor performs it, up to and
	// including the acceptance persist — and then the "crash": the row stays
	// in_flight, postSend never runs. This is boundary 5 of the matrix
	// against the real stack: a REAL delivery happened and the acceptance is
	// durable.
	payload := fmt.Sprintf(`{"identityId":"primary","mailFrom":%q,"rcptTo":[%q]}`, e.user, e.user)
	in, err := e.store.EnqueueSendIntent(context.Background(), acct.ID, 7, msgID,
		[]byte(payload), time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := e.store.ClaimDueSendIntents(context.Background(), acct.ID, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	prepared := PrepareTransmission(draftFor(e.user, msgID), msgID, claimed[0].CreatedAt)
	_, err = Send(context.Background(), e.smtp,
		Envelope{MailFrom: e.user, RcptTo: []string{e.user}, Size: int64(len(prepared))},
		bytes.NewReader(prepared),
		func(reply string) error {
			return e.store.MarkSendIntentAccepted(context.Background(), in.ID, reply)
		})
	if err != nil {
		t.Fatalf("the real send failed: %v", err)
	}
	// -- crash here: nothing after the persist ran --

	// The restart: a fresh outbox recovers. The transport counter is the
	// proof of rule 2 — the recovery completes the post-send steps and NEVER
	// transmits again.
	transport := &realTransport{cfg: e.smtp}
	ob := e.vpsOutbox(t, transport, raws)
	ob.recover(context.Background())

	if transport.count() != 0 {
		t.Fatalf("recovery re-transmitted an accepted message %d times — the 250 was not sacred", transport.count())
	}
	row, err := e.store.GetSendIntent(context.Background(), acct.ID, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != store.IntentDone || row.AppendedAt == nil {
		t.Errorf("recovery did not complete the post-send steps: state=%s appended=%v", row.State, row.AppendedAt)
	}

	// The mailbox agrees: exactly one copy ever existed.
	uids, _ := e.waitForDelivery(t, msgID, 60*time.Second)
	if len(uids) != 1 {
		t.Errorf("INBOX holds %d copies after crash recovery, want exactly 1", len(uids))
	}
}
