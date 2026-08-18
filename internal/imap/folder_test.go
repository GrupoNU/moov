package imap

import (
	"errors"
	"strings"
	"testing"

	goimap "github.com/emersion/go-imap/v2"
)

// The folder primitives split cleanly into two halves: the part that talks to
// a connection (covered by the integration suite against real Dovecot) and the
// part that decides WHETHER to talk to it at all. This file tests the second,
// which is the half where a bug corrupts something — a name that injects a
// line into the command stream, an INBOX rename that empties the inbox.

func TestValidateMailboxNameRefusesUnrepresentableNames(t *testing.T) {
	cases := []struct {
		name  string
		input string
		why   string
	}{
		{"empty", "", "no mailbox has no name"},
		{"whitespace only", "   ", "invisible in every client"},
		{"tab only", "\t", "invisible in every client"},
		{"carriage return", "Work\rNoop", "CR terminates an IMAP line"},
		{"line feed", "Work\nA001 DELETE INBOX", "the command-injection shape"},
		{"nul", "Work\x00hidden", "not representable in an IMAP string"},
		{"too long", strings.Repeat("x", MaxMailboxNameBytes+1), "over the byte cap"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMailboxName(tc.input)
			if err == nil {
				t.Fatalf("accepted %q (%s)", tc.input, tc.why)
			}
			if !errors.Is(err, ErrInvalidMailboxName) {
				t.Fatalf("got %v, want ErrInvalidMailboxName", err)
			}
		})
	}
}

func TestValidateMailboxNameAcceptsRealNames(t *testing.T) {
	// Names a real account carries, including the non-ASCII ones modified
	// UTF-7 exists for and the hierarchy paths RENAME has to carry children
	// through. None of these may be refused by Moov's own validation — the
	// server is the authority on what it stores.
	for _, name := range []string{
		"Work",
		"INBOX/Work",
		"INBOX/Facturación",
		"Проекты",
		"日本語",
		"Work & Play",
		"a/b/c/d",
		strings.Repeat("x", MaxMailboxNameBytes),
	} {
		if err := validateMailboxName(name); err != nil {
			t.Errorf("refused a legitimate name %q: %v", name, err)
		}
	}
}

func TestIsInboxNameIsCaseInsensitiveAndDoesNotMatchChildren(t *testing.T) {
	// RFC 3501 §5.1: INBOX is reserved case-insensitively.
	for _, name := range []string{"INBOX", "inbox", "InBoX"} {
		if !isInboxName(name) {
			t.Errorf("%q is INBOX and was not recognized", name)
		}
	}
	// A child of INBOX is an ordinary folder: renaming and deleting it are
	// perfectly legal, and treating it as reserved would lock users out of
	// their own folder tree.
	for _, name := range []string{"INBOX/Work", "INBOXES", "INBOX ", "Inbox2", ""} {
		if isInboxName(name) {
			t.Errorf("%q is not INBOX but was treated as reserved", name)
		}
	}
}

func TestRenameMailboxRefusesInboxBeforeTouchingTheConnection(t *testing.T) {
	// A client with no connection at all: if the INBOX guard did not come
	// first, this would fail with ErrNotConnected instead, which is what
	// proves the refusal is a decision rather than an accident of ordering.
	cl := &client{}
	err := cl.RenameMailbox(t.Context(), "INBOX", "Archive2026")
	if !errors.Is(err, ErrRenameInbox) {
		t.Fatalf("got %v, want ErrRenameInbox", err)
	}
	// The reason has to be IN the error: this is the one refusal a maintainer
	// will be tempted to "fix" later, and the RFC citation is the argument.
	if !strings.Contains(err.Error(), "6.3.5") {
		t.Errorf("the refusal does not cite RFC 3501 §6.3.5: %v", err)
	}
}

func TestDeleteMailboxRefusesInboxBeforeTouchingTheConnection(t *testing.T) {
	cl := &client{}
	err := cl.DeleteMailbox(t.Context(), "inbox")
	if !errors.Is(err, ErrDeleteInbox) {
		t.Fatalf("got %v, want ErrDeleteInbox", err)
	}
}

func TestCreateMailboxValidatesBeforeTouchingTheConnection(t *testing.T) {
	cl := &client{}
	err := cl.CreateMailbox(t.Context(), "Work\r\nA002 DELETE INBOX")
	if !errors.Is(err, ErrInvalidMailboxName) {
		t.Fatalf("got %v, want ErrInvalidMailboxName", err)
	}
}

func TestRenameMailboxToTheSameNameIsANoop(t *testing.T) {
	// A replayed Mailbox/set update reaches here with from == to. Answering
	// success without a round trip keeps the replay idempotent regardless of
	// whether the server answers OK or "already exists" to a self-rename.
	cl := &client{}
	if err := cl.RenameMailbox(t.Context(), "Work", "Work"); err != nil {
		t.Fatalf("a self-rename must be a no-op, got %v", err)
	}
}

func TestRenameMailboxRequiresBothNames(t *testing.T) {
	cl := &client{}
	if err := cl.RenameMailbox(t.Context(), "", "Work"); err == nil {
		t.Error("an empty source was accepted")
	}
	if err := cl.RenameMailbox(t.Context(), "Work", ""); !errors.Is(err, ErrInvalidMailboxName) {
		t.Errorf("an empty target must be ErrInvalidMailboxName, got %v", err)
	}
}

func TestMailboxExistsErrorRecognizesBothShapes(t *testing.T) {
	// The response code is the reliable signal (RFC 9051 §6.3.4) and Dovecot
	// emits it; the text fallback covers a server that does not.
	coded := &goimap.Error{Type: goimap.StatusResponseTypeNo, Code: "ALREADYEXISTS", Text: "Mailbox already exists"}
	if !mailboxExistsError(coded) {
		t.Error("[ALREADYEXISTS] was not recognized")
	}
	if !mailboxExistsError(errors.New("imap: CREATE: NO Mailbox already exists")) {
		t.Error("the text form was not recognized")
	}
	if mailboxExistsError(nil) {
		t.Error("nil is not an exists error")
	}
	if mailboxExistsError(errors.New("imap: CREATE: NO Permission denied")) {
		t.Error("an unrelated failure was misread as exists")
	}
}

func TestMailboxMissingErrorRecognizesBothShapes(t *testing.T) {
	coded := &goimap.Error{Type: goimap.StatusResponseTypeNo, Code: "NONEXISTENT", Text: "Mailbox doesn't exist"}
	if !mailboxMissingError(coded) {
		t.Error("[NONEXISTENT] was not recognized")
	}
	if !mailboxMissingError(errors.New(`imap: DELETE "Work": NO Mailbox doesn't exist: Work`)) {
		t.Error("the text form was not recognized")
	}
	if mailboxMissingError(errors.New("imap: DELETE: NO Internal error")) {
		t.Error("an unrelated failure was misread as missing")
	}
}

func TestFolderPrimitivesRefuseADeadConnection(t *testing.T) {
	// Every primitive must fail loudly rather than panic on a client that was
	// never connected — the same contract the rest of the package holds.
	cl := &client{}
	ctx := t.Context()
	if err := cl.CreateMailbox(ctx, "Work"); !errors.Is(err, ErrNotConnected) {
		t.Errorf("CreateMailbox: got %v, want ErrNotConnected", err)
	}
	if err := cl.RenameMailbox(ctx, "Work", "Play"); !errors.Is(err, ErrNotConnected) {
		t.Errorf("RenameMailbox: got %v, want ErrNotConnected", err)
	}
	if err := cl.DeleteMailbox(ctx, "Work"); !errors.Is(err, ErrNotConnected) {
		t.Errorf("DeleteMailbox: got %v, want ErrNotConnected", err)
	}
	if err := cl.SetSubscribed(ctx, "Work", true); !errors.Is(err, ErrNotConnected) {
		t.Errorf("SetSubscribed: got %v, want ErrNotConnected", err)
	}
	if _, err := cl.StatusMailbox(ctx, "Work"); !errors.Is(err, ErrNotConnected) {
		t.Errorf("StatusMailbox: got %v, want ErrNotConnected", err)
	}
}

func TestSetSubscribedAndStatusRequireAName(t *testing.T) {
	cl := &client{}
	if err := cl.SetSubscribed(t.Context(), "", true); errors.Is(err, ErrNotConnected) {
		t.Error("an empty name must be rejected before the connection check")
	}
	if _, err := cl.StatusMailbox(t.Context(), ""); errors.Is(err, ErrNotConnected) {
		t.Error("an empty name must be rejected before the connection check")
	}
}
