package mail

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// E10: the server half of the executable-attachment block (canon §2.3,
// /mail/answer/6590). Three pins: the final-extension rule's edge cases (the
// same table the client's finalExtension tests walk), the declared==enforced
// parity with the client's list, and the honest `forbidden` SetError on the
// wire.

func TestFinalAttachmentExtensionEdgeCases(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"invoice.pdf", "pdf"},
		// The classic: the FINAL extension decides, in both directions.
		{"invoice.pdf.exe", "exe"},
		{"payload.exe.txt", "txt"},
		// No dot: no extension — a file CALLED "exe" is not an executable name.
		{"README", ""},
		{"exe", ""},
		// A dotfile's name is not an extension.
		{".bashrc", ""},
		// Windows discards ALL trailing dots and spaces when opening a file.
		{"payload.exe.", "exe"},
		{"payload.exe. . .", "exe"},
		{"payload.exe   ", "exe"},
		// Case-insensitive: the OS that would run it does not care.
		{"PAYLOAD.EXE", "exe"},
		// Only the basename: a path cannot hide the extension behind an
		// earlier dot.
		{"../../payload.exe", "exe"},
		{"dir.d\\payload.exe", "exe"},
		{"", ""},
		{"...", ""},
	}
	for _, tc := range cases {
		if got := finalAttachmentExtension(tc.name); got != tc.want {
			t.Errorf("finalAttachmentExtension(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Declared == enforced, across the language boundary: the Go list must be
// EXACTLY the client's (web/src/mail/blockedExtensions.ts), which is itself
// Gmail's published list transcribed verbatim. The test reads the TypeScript
// source out of the repo — the same discipline that pins maxAttachmentsBytes
// to the advertised capability value — so neither list can drift silently.
func TestBlockedExtensionsMatchTheClientList(t *testing.T) {
	root := moduleRoot(t)
	path := filepath.Join(root, "web", "src", "mail", "blockedExtensions.ts")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the client list must exist for the parity pin: %v", err)
	}

	// The list literal: everything between the declaration and its closing
	// bracket, entries as double-quoted strings.
	block := regexp.MustCompile(`(?s)BLOCKED_EXTENSIONS[^=]*=\s*\[(.*?)\];`).FindSubmatch(content)
	if block == nil {
		t.Fatal("BLOCKED_EXTENSIONS literal not found in blockedExtensions.ts")
	}
	entries := regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(block[1], -1)

	var clientList []string
	for _, m := range entries {
		clientList = append(clientList, string(m[1]))
	}
	if len(clientList) == 0 {
		t.Fatal("parsed zero entries from the client list — the extractor went dead")
	}

	if got, want := strings.Join(blockedExtensionList, ","), strings.Join(clientList, ","); got != want {
		t.Errorf("the server and client blocked-extension lists drifted:\nserver: %s\nclient: %s", got, want)
	}
}

// The wire behavior: an Email/set create with a blocked attachment fails
// with `forbidden`, names the offending property, and creates nothing.
func TestEmailCreateRefusesBlockedAttachmentExtensions(t *testing.T) {
	f := newFakeReaders()
	drafts := sampleMailbox(31, "Drafts", "drafts", 0, 0)
	f.mailboxes[testAccountID] = []MailboxRow{drafts}
	blobID := "aa" + repeatHex(62)
	f.emails[testAccountID] = []EmailRow{sampleEmail(1, "holder")}
	f.blobs[blobID] = []byte("MZ...not really")
	deps := f.deps()

	body := draftCreateBody(EncodeMailboxID(31))
	body["attachments"] = []map[string]any{{
		"blobId": blobID, "type": "application/octet-stream", "name": "invoice.pdf.exe",
	}}

	res, merr := deps.handleEmailSet(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(),
		"create":    map[string]any{"d1": body},
	}))
	if merr != nil {
		t.Fatalf("Email/set: %v", merr)
	}
	resp := res.(*setResponse) //nolint:errcheck // handler contract
	serr, ok := resp.NotCreated["d1"]
	if !ok {
		t.Fatalf("create succeeded with a blocked attachment: %+v", resp.Created)
	}
	if serr.Type != setErrForbidden {
		t.Errorf("SetError type = %q, want forbidden", serr.Type)
	}
	if !strings.Contains(serr.Description, ".exe") || !strings.Contains(strings.ToLower(serr.Description), "blocked") {
		t.Errorf("the error must name the extension and the block honestly: %q", serr.Description)
	}
	if len(serr.Properties) != 1 || serr.Properties[0] != "attachments/0" {
		t.Errorf("properties = %v, want the offending attachment path", serr.Properties)
	}
	if len(f.createCalls) != 0 {
		t.Error("the creator was reached despite the block")
	}
}

// The block covers every part shape, not just the attachments list: a
// bodyStructure leaf with a blocked filename is the same file.
func TestEmailCreateRefusesBlockedExtensionInBodyStructure(t *testing.T) {
	f := newFakeReaders()
	drafts := sampleMailbox(31, "Drafts", "drafts", 0, 0)
	f.mailboxes[testAccountID] = []MailboxRow{drafts}
	blobID := "aa" + repeatHex(62)
	f.emails[testAccountID] = []EmailRow{sampleEmail(1, "holder")}
	f.blobs[blobID] = []byte("bytes")
	deps := f.deps()

	res, merr := deps.handleEmailSet(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(),
		"create": map[string]any{"d1": map[string]any{
			"mailboxIds": map[string]bool{EncodeMailboxID(31): true},
			"bodyStructure": map[string]any{
				"type": "multipart/mixed",
				"subParts": []map[string]any{
					{"partId": "t", "type": "text/plain"},
					{"blobId": blobID, "type": "application/octet-stream", "name": "run.bat"},
				},
			},
			"bodyValues": map[string]any{"t": map[string]any{"value": "hola"}},
		}},
	}))
	if merr != nil {
		t.Fatalf("Email/set: %v", merr)
	}
	resp := res.(*setResponse) //nolint:errcheck // handler contract
	serr, ok := resp.NotCreated["d1"]
	if !ok {
		t.Fatalf("create succeeded with a blocked bodyStructure leaf: %+v", resp.Created)
	}
	if serr.Type != setErrForbidden || !strings.Contains(serr.Description, ".bat") {
		t.Errorf("SetError = %+v, want forbidden naming .bat", serr)
	}
}

// An ordinary attachment is untouched by the block — the negative control.
func TestEmailCreateAcceptsOrdinaryAttachment(t *testing.T) {
	f := newFakeReaders()
	drafts := sampleMailbox(31, "Drafts", "drafts", 0, 0)
	f.mailboxes[testAccountID] = []MailboxRow{drafts}
	blobID := "aa" + repeatHex(62)
	f.emails[testAccountID] = []EmailRow{sampleEmail(1, "holder")}
	f.blobs[blobID] = []byte("%PDF-1.7 ...")
	deps := f.deps()

	body := draftCreateBody(EncodeMailboxID(31))
	body["attachments"] = []map[string]any{{
		"blobId": blobID, "type": "application/pdf", "name": "payload.exe.pdf",
	}}

	res, merr := deps.handleEmailSet(callerCtx(), jsonArgs(t, map[string]any{
		"accountId": testAccountJMAPID(),
		"create":    map[string]any{"d1": body},
	}))
	if merr != nil {
		t.Fatalf("Email/set: %v", merr)
	}
	resp := res.(*setResponse) //nolint:errcheck // handler contract
	if _, ok := resp.Created["d1"]; !ok {
		t.Fatalf("an ordinary attachment was refused: %+v", resp.NotCreated)
	}
}
