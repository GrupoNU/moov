package imap

import (
	"reflect"
	"strings"
	"testing"

	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// This file is the alarm for the vendored patch set (patches/README.md).
//
// `go mod vendor` regenerates vendor/ from the pristine module cache, so every
// vendor regeneration and every dependency bump silently reverts all three
// patches. Silently is the problem: an unpatched tree still compiles, and the
// symptoms only appear against a real server — QRESYNC refused at connect
// time, NOTIFY rejected as a syntax error, and, worst of all, conditional
// writes that fail without saying so.
//
// These tests deliberately do NOT grep the vendored source for patch text.
// They assert the BEHAVIOR each patch exists to provide, so a patch that
// applies but stops doing its job still fails here.

// TestVendoredPatchSetIsApplied is the single entry point named in
// patches/README.md. Each subtest maps to one patch.
func TestVendoredPatchSetIsApplied(t *testing.T) {
	t.Run("0001_qresync", testPatch0001QResync)
	t.Run("0002_notify_encoder", testPatch0002NotifyEncoder)
	t.Run("0003_condstore_modified", testPatch0003Modified)
}

// testPatch0001QResync checks upstream PR #757 is present.
//
// The behavior it must provide: QRESYNC can be enabled, and a SELECT can
// carry the QRESYNC parameter. Stock go-imap refuses the first with a
// client-side allowlist before any byte reaches the server (S2 T2e), which is
// why the check is that the API exists at all rather than that a call
// succeeds.
func testPatch0001QResync(t *testing.T) {
	// SelectOptions.QResync is what SELECT (QRESYNC (uidvalidity modseq))
	// needs. Its absence means the patch is gone.
	opts := reflect.TypeOf(goimap.SelectOptions{})
	if _, ok := opts.FieldByName("QResync"); !ok {
		t.Error("imap.SelectOptions has no QResync field: patch 0001 is missing from the " +
			"vendored tree. Run 'make vendor-patches'.")
	}

	// SelectData.VanishedUIDs is how VANISHED (EARLIER) reaches the caller
	// after a QRESYNC SELECT.
	data := reflect.TypeOf(goimap.SelectData{})
	if _, ok := data.FieldByName("VanishedUIDs"); !ok {
		t.Error("imap.SelectData has no VanishedUIDs field: patch 0001 is missing.")
	}

	// FetchOptions.Vanished is the VANISHED modifier on UID FETCH.
	fetch := reflect.TypeOf(goimap.FetchOptions{})
	if _, ok := fetch.FieldByName("Vanished"); !ok {
		t.Error("imap.FetchOptions has no Vanished field: patch 0001 is missing.")
	}

	// The unilateral handler must be able to deliver VANISHED.
	handler := reflect.TypeOf(imapclient.UnilateralDataHandler{})
	if _, ok := handler.FieldByName("Vanished"); !ok {
		t.Error("imapclient.UnilateralDataHandler has no Vanished callback: patch 0001 is missing.")
	}
}

// testPatch0002NotifyEncoder checks the NOTIFY encoder emits what Dovecot
// accepts.
//
// This is a genuine wire-level assertion: the options go through the real
// encoder and the resulting bytes are compared against the exact commands S2
// confirmed by hand against Dovecot 2.3.21.1. A regression here means flag
// changes in non-selected folders become silently invisible (S2 T4).
func testPatch0002NotifyEncoder(t *testing.T) {
	events := []goimap.NotifyEvent{
		goimap.NotifyEventMessageNew,
		goimap.NotifyEventMessageExpunge,
		goimap.NotifyEventFlagChange,
	}

	tests := []struct {
		name string
		opts *goimap.NotifyOptions
		want string
		why  string
	}{
		{
			name: "status keyword is a bare atom",
			opts: &goimap.NotifyOptions{
				Status: true,
				Items: []goimap.NotifyItem{
					{MailboxSpec: goimap.NotifyMailboxSpecPersonal, Events: events},
				},
			},
			want: "NOTIFY SET STATUS (PERSONAL (MessageNew MessageExpunge FlagChange))",
			why: "stock go-imap emits 'SET (STATUS)', which Dovecot answers with " +
				"BAD Invalid arguments; without the STATUS keyword a flag change in a " +
				"non-selected folder produces no notification at all (S2 T4)",
		},
		{
			name: "explicit mailbox list carries the MAILBOXES keyword",
			opts: &goimap.NotifyOptions{
				Items: []goimap.NotifyItem{
					{Mailboxes: []string{"INBOX", "S2/folder1"}, Events: []goimap.NotifyEvent{goimap.NotifyEventMessageNew}},
				},
			},
			want: `NOTIFY SET (MAILBOXES (INBOX "S2/folder1") (MessageNew))`,
			why: "stock go-imap drops the mandatory MAILBOXES keyword of RFC 5465 " +
				"filter-mailboxes-other, which Dovecot rejects (S2 T2d bug #2)",
		},
		{
			name: "SUBTREE keeps its own keyword",
			opts: &goimap.NotifyOptions{
				Items: []goimap.NotifyItem{
					{Subtree: true, Mailboxes: []string{"INBOX"}, Events: []goimap.NotifyEvent{goimap.NotifyEventMessageNew}},
				},
			},
			want: "NOTIFY SET (SUBTREE (INBOX) (MessageNew))",
			why:  "the patch must not break the branch that was already correct",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := encodeNotifyForTest(tc.opts)
			if err != nil {
				t.Fatalf("encoding NOTIFY: %v", err)
			}
			if got != tc.want {
				t.Errorf("NOTIFY encoded wrong.\n got: %s\nwant: %s\n\nwhy this matters: %s\n\n"+
					"Patch 0002 is missing from the vendored tree. Run 'make vendor-patches'.",
					got, tc.want, tc.why)
			}
		})
	}
}

// testPatch0003Modified checks the [MODIFIED] response code is reachable.
//
// The behavior: a caller must be able to ask a completed conditional STORE
// which messages the server refused. Without it, a rejected write and an
// applied one are the same observation (S2 T2b) — the silent corruption this
// package's StoreFlags is built to prevent.
func testPatch0003Modified(t *testing.T) {
	if _, ok := reflect.TypeOf(&imapclient.FetchCommand{}).MethodByName("Modified"); !ok {
		t.Fatal("imapclient.FetchCommand has no Modified() method: patch 0003 is missing " +
			"from the vendored tree. Run 'make vendor-patches'.\n" +
			"Without it a conditional STORE that the server REJECTS is indistinguishable " +
			"from one it applied, and Moov's flag state silently diverges from Dovecot's.")
	}

	// The method must return something a caller can actually use.
	m, _ := reflect.TypeOf(&imapclient.FetchCommand{}).MethodByName("Modified")
	if m.Type.NumOut() != 1 {
		t.Fatalf("FetchCommand.Modified() returns %d values, want 1", m.Type.NumOut())
	}
	want := reflect.TypeOf((*goimap.NumSet)(nil)).Elem()
	if got := m.Type.Out(0); got != want {
		t.Errorf("FetchCommand.Modified() returns %v, want %v", got, want)
	}

	// And the response code constant must exist, so callers can recognize it
	// in an imap.Error too.
	if goimap.ResponseCodeModified != "MODIFIED" {
		t.Errorf("imap.ResponseCodeModified = %q, want %q",
			goimap.ResponseCodeModified, "MODIFIED")
	}
}

// encodeNotifyForTest drives the real NOTIFY encoder and returns the command
// as it would go on the wire.
//
// encodeNotifyOptions is unexported in imapclient, so it cannot be called from
// here. Client.Notify is driven instead against a pipe that captures the bytes,
// which has the advantage of testing the whole path the production code uses
// rather than an internal helper.
func encodeNotifyForTest(opts *goimap.NotifyOptions) (string, error) {
	line, err := captureCommand(func(c *imapclient.Client) {
		// The command is never answered by the fake server, so Wait would
		// block forever; issuing it is enough to put the bytes on the wire.
		_, _ = c.Notify(opts)
	})
	if err != nil {
		return "", err
	}
	// Strip the command tag ("C01 NOTIFY SET …" -> "NOTIFY SET …") so the
	// assertion is about the syntax rather than about go-imap's tag counter.
	if i := strings.IndexByte(line, ' '); i >= 0 {
		line = line[i+1:]
	}
	return line, nil
}
