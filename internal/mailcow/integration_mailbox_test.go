package mailcow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestIntegrationMailboxLifecycle is contract §6 M1 (i)'s "one integration
// test against the real Mailcow": create a mailbox, read it back with its
// usage fields, edit it, suspend and resume it, set its rate limit, re-issue
// an app password without SMTP, and delete it — every write the accounts API
// performs, in the order it performs them.
//
// The mailbox is CREATED by the test under a `moov-test-` local part in the
// domain of MOOV_MAILCOW_TEST_MAILBOX, and deleted again on every path. It
// is skipped, like the other integration tests, unless the MOOV_MAILCOW_TEST_*
// variables are set.
func TestIntegrationMailboxLifecycle(t *testing.T) {
	c, existing := testClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	at := strings.LastIndexByte(existing, '@')
	domain := existing[at+1:]
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	localPart := "moov-test-" + hex.EncodeToString(suffix[:])
	address := localPart + "@" + domain

	password, err := GeneratePassword()
	if err != nil {
		t.Fatal(err)
	}
	// Mailcow's complexity policy may demand a symbol; the generated alphabet
	// has none, so one is appended for the discarded mailbox password only.
	password += "!1"

	if err := c.CreateMailbox(ctx, CreateMailboxRequest{
		LocalPart: localPart, Domain: domain, Name: "Moov integration test",
		Password: password, QuotaMB: 64, Active: true,
	}); err != nil {
		t.Fatalf("CreateMailbox(%s): %v", address, err)
	}
	deleted := false
	t.Cleanup(func() {
		if deleted {
			return
		}
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		if err := c.DeleteMailbox(cctx, address); err != nil {
			t.Errorf("CLEANUP FAILED: mailbox %s must be deleted by hand: %v", address, err)
		}
	})

	// A second create is the idempotency signal the accounts API relies on.
	err = c.CreateMailbox(ctx, CreateMailboxRequest{
		LocalPart: localPart, Domain: domain, Name: "dup", Password: password, QuotaMB: 64, Active: true,
	})
	if !IsAPICode(err, CodeObjectExists) {
		t.Errorf("second create = %v, want object_exists", err)
	}

	mb, err := c.GetMailbox(ctx, address)
	if err != nil {
		t.Fatalf("GetMailbox: %v", err)
	}
	if mb.Username != address || !mb.IsActive() || mb.Quota != 64<<20 {
		t.Errorf("created mailbox = %+v", mb)
	}
	t.Logf("rl=%+v scope=%q quota_used=%d", mb.RL, mb.RLScope, mb.QuotaUsed)

	name, quota := "Moov integration test (edited)", 128
	if err := c.EditMailbox(ctx, address, MailboxEdit{Name: &name, QuotaMB: &quota}); err != nil {
		t.Fatalf("EditMailbox: %v", err)
	}
	mb, err = c.GetMailbox(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	if mb.Name != name || mb.Quota != 128<<20 {
		t.Errorf("after edit: name=%q quota=%d", mb.Name, mb.Quota)
	}

	off, on := false, true
	if err := c.EditMailbox(ctx, address, MailboxEdit{Active: &off}); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if mb, _ = c.GetMailbox(ctx, address); mb.IsActive() {
		t.Error("mailbox still active after suspend")
	}
	if err := c.EditMailbox(ctx, address, MailboxEdit{Active: &on}); err != nil {
		t.Fatalf("resume: %v", err)
	}

	if err := c.SetMailboxRateLimit(ctx, address, RateLimit{Value: 42, Frame: "d"}); err != nil {
		t.Fatalf("SetMailboxRateLimit: %v", err)
	}
	rl, err := c.GetMailboxRateLimit(ctx, address)
	if err != nil {
		t.Fatalf("GetMailboxRateLimit: %v", err)
	}
	if rl.Value != 42 || rl.Frame != "d" {
		t.Errorf("rate limit = %+v, want 42/d", rl)
	}

	// The read-only credential: an app password WITHOUT smtp_access.
	appPw, err := GeneratePassword()
	if err != nil {
		t.Fatal(err)
	}
	ap, err := c.CreateAppPassword(ctx, CreateAppPasswordRequest{
		Mailbox: address, Password: appPw, Scopes: []Protocol{ProtocolIMAP, ProtocolSieve},
	})
	if err != nil {
		t.Fatalf("CreateAppPassword (no smtp): %v", err)
	}
	if ap.SMTPAccess != 0 || ap.IMAPAccess == 0 || ap.SieveAccess == 0 {
		t.Errorf("read-only app password scopes: imap=%d smtp=%d sieve=%d", ap.IMAPAccess, ap.SMTPAccess, ap.SieveAccess)
	}
	if err := c.EditMailbox(ctx, address, MailboxEdit{SMTPAccess: &off}); err != nil {
		t.Fatalf("smtp_access:0: %v", err)
	}

	// Delete, and confirm the cascade F0 answer P2 reported.
	if err := c.DeleteMailbox(ctx, address); err != nil {
		t.Fatalf("DeleteMailbox: %v", err)
	}
	deleted = true
	if _, err := c.GetMailbox(ctx, address); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: GetMailbox = %v, want ErrNotFound", err)
	}
	list, err := c.ListAppPasswords(ctx, address)
	if err != nil {
		t.Fatalf("ListAppPasswords after delete: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("%d app password(s) survived the mailbox delete (F0 said they cascade)", len(list))
	}
	// Deleting again is access_denied, not a credential failure (rule 7).
	if err := c.DeleteMailbox(ctx, address); !IsAPICode(err, CodeAccessDenied) {
		t.Errorf("second delete = %v, want access_denied", err)
	}
}
