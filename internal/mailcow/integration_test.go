package mailcow

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// Integration tests against a REAL Mailcow instance.
//
// They are skipped unless the environment names one. Every value comes from the
// environment — this repository is public and no credential may ever be written
// into a file here.
//
//	MOOV_MAILCOW_TEST_BASE_URL     e.g. https://172.22.1.12  (the nginx container)
//	MOOV_MAILCOW_TEST_API_KEY      a read-write API key
//	MOOV_MAILCOW_TEST_MAILBOX      the mailbox to act on
//	MOOV_MAILCOW_TEST_HOST_HEADER  the Host header nginx routes on
//	MOOV_MAILCOW_TEST_INSECURE     "1" to skip TLS verification (container IP)
//
// # Safety
//
// These tests CREATE and DELETE app passwords on a live mail server. The
// mailbox is therefore required to be an explicitly designated test mailbox:
// testMailbox refuses to run against anything whose local part does not mark it
// as one. A test that provisions credentials on a customer's mailbox because
// somebody exported the wrong variable is not a risk worth leaving open.
//
// Everything created is deleted again, including on failure.
const (
	envTestBaseURL    = "MOOV_MAILCOW_TEST_BASE_URL"
	envTestAPIKey     = "MOOV_MAILCOW_TEST_API_KEY"
	envTestMailbox    = "MOOV_MAILCOW_TEST_MAILBOX"
	envTestHostHeader = "MOOV_MAILCOW_TEST_HOST_HEADER"
	envTestInsecure   = "MOOV_MAILCOW_TEST_INSECURE"
)

// testMailboxMarkers are the substrings that mark a mailbox as safe to write
// to. A mailbox not matching one of these is refused.
var testMailboxMarkers = []string{"moov-test", "test@", "-test@"}

func testClient(t *testing.T) (*Client, string) {
	t.Helper()

	baseURL := os.Getenv(envTestBaseURL)
	apiKey := os.Getenv(envTestAPIKey)
	mailbox := os.Getenv(envTestMailbox)
	if baseURL == "" || apiKey == "" || mailbox == "" {
		t.Skipf("set %s, %s and %s to run the Mailcow integration tests",
			envTestBaseURL, envTestAPIKey, envTestMailbox)
	}

	safe := false
	for _, marker := range testMailboxMarkers {
		if strings.Contains(mailbox, marker) {
			safe = true
			break
		}
	}
	if !safe {
		t.Fatalf("%s is %q, which is not marked as a test mailbox. These tests create and "+
			"delete credentials on a live server and will not run against a mailbox whose "+
			"name does not contain one of %v", envTestMailbox, mailbox, testMailboxMarkers)
	}

	cfg := Config{
		BaseURL:            baseURL,
		APIKey:             apiKey,
		HostHeader:         os.Getenv(envTestHostHeader),
		Timeout:            20 * time.Second,
		InsecureSkipVerify: os.Getenv(envTestInsecure) == "1",
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// The IPv4 pin (S1 H5) must be on: that is what these tests are partly
	// here to confirm still works against the real allowlist.
	if !c.Config().ForceIPv4 {
		t.Fatal("ForceIPv4 is off; S1 H5 requires it")
	}
	return c, mailbox
}

func TestIntegrationGetMailbox(t *testing.T) {
	c, mailbox := testClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mb, err := c.GetMailbox(ctx, mailbox)
	if err != nil {
		t.Fatalf("GetMailbox(%s): %v", mailbox, err)
	}
	if mb.Username != mailbox {
		t.Errorf("Username = %q, want %q", mb.Username, mailbox)
	}
	if !mb.IsActive() {
		t.Error("the test mailbox is not active")
	}
	if !mb.AllowsMoovScopes() {
		t.Error("the test mailbox does not grant imap+smtp+sieve")
	}
	t.Logf("mailbox %s: domain=%s quota=%d messages=%d imap=%d smtp=%d sieve=%d",
		mb.Username, mb.Domain, mb.Quota, mb.Messages,
		mb.Attributes.IMAPAccess, mb.Attributes.SMTPAccess, mb.Attributes.SieveAccess)
}

func TestIntegrationGetMailboxNotFound(t *testing.T) {
	c, _ := testClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.GetMailbox(ctx, "definitely-not-a-mailbox-9f8e7d6c@invalid.example")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

// TestIntegrationAppPasswordLifecycle is the E7 acceptance criterion against
// the real API: create a scoped app password, confirm it exists with exactly
// the scopes asked for, and delete it again.
//
// The cleanup runs even if the assertions fail, because a leaked app password
// on a live mail server is not an acceptable test artifact.
func TestIntegrationAppPasswordLifecycle(t *testing.T) {
	c, mailbox := testClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	before, err := c.ListAppPasswords(ctx, mailbox)
	if err != nil {
		t.Fatalf("listing app passwords before: %v", err)
	}
	t.Logf("%d app password(s) exist before the test", len(before))

	password, err := GeneratePassword()
	if err != nil {
		t.Fatalf("GeneratePassword: %v", err)
	}

	created, err := c.CreateAppPassword(ctx, CreateAppPasswordRequest{
		Mailbox:  mailbox,
		Password: password,
		Scopes:   MoovScopes(),
	})
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	// Registered immediately, so an assertion failure below still cleans up.
	//
	// deleted is set by the explicit delete further down, which is the one
	// under test. The cleanup is the safety net for every path that does NOT
	// reach it — a failed assertion, a panic — and skips when the delete
	// already succeeded, because deleting a row twice is an error Mailcow
	// reports and would turn a passing test red.
	deleted := false
	t.Cleanup(func() {
		if deleted {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if err := c.DeleteAppPassword(cleanupCtx, created.ID); err != nil {
			// Loud: this leaves a live credential on a real mail server.
			t.Errorf("CLEANUP FAILED: app password id %d (%q) on %s must be deleted by hand: %v",
				created.ID, created.Name, mailbox, err)
			return
		}
		t.Logf("cleaned up app password id %d", created.ID)
	})

	t.Logf("created app password id=%d name=%q", created.ID, created.Name)
	if created.ID == 0 {
		t.Fatal("the created app password has no id, so it could never be revoked")
	}
	if !strings.HasPrefix(created.Name, DefaultAppNamePrefix) {
		t.Errorf("app_name %q does not carry the %q prefix", created.Name, DefaultAppNamePrefix)
	}
	if created.Mailbox != mailbox {
		t.Errorf("the app password belongs to %q, want %q", created.Mailbox, mailbox)
	}

	// The scopes are the part that upstream issue #4588 gets wrong. Confirm
	// against the server's own view, not against what we sent.
	list, err := c.ListAppPasswords(ctx, mailbox)
	if err != nil {
		t.Fatalf("listing app passwords after create: %v", err)
	}
	var found *AppPassword
	for i := range list {
		if list[i].ID == created.ID {
			found = &list[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("the created app password id %d is not in the list", created.ID)
	}
	// Note what is NOT asserted: the total number of rows. The server is
	// shared — another package's integration test may legitimately be
	// provisioning against the same mailbox at the same time — so this test
	// asserts only about the row it created. A count assertion here fails on
	// somebody else's correct behavior, which is a worse test than none.
	if found.IMAPAccess == 0 {
		t.Error("imap_access is 0: the app password cannot be used to sync (upstream #4588)")
	}
	if found.SMTPAccess == 0 {
		t.Error("smtp_access is 0: the app password cannot be used to send")
	}
	if found.SieveAccess == 0 {
		t.Error("sieve_access is 0: the app password cannot manage filters")
	}
	if found.Active == 0 {
		t.Error("the app password is not active")
	}

	// Delete explicitly here rather than leaving it to the cleanup, so that
	// the delete path is what is under test rather than only a teardown side
	// effect. Recording it disarms the cleanup's safety net.
	if err := c.DeleteAppPassword(ctx, created.ID); err != nil {
		t.Fatalf("DeleteAppPassword: %v", err)
	}
	deleted = true

	after, err := c.ListAppPasswords(ctx, mailbox)
	if err != nil {
		t.Fatalf("listing app passwords after delete: %v", err)
	}
	for _, ap := range after {
		if ap.ID == created.ID {
			t.Fatalf("app password id %d survived the delete", created.ID)
		}
	}
	t.Logf("deleted app password id=%d; %d row(s) remain (not asserted: the server is shared)",
		created.ID, len(after))
}

func TestIntegrationBadAPIKeyIsRejected(t *testing.T) {
	// Confirms the failure is distinguishable, which is what makes the S1 H5
	// allowlist problem diagnosable in production.
	_, mailbox := testClient(t)

	cfg := Config{
		BaseURL:            os.Getenv(envTestBaseURL),
		APIKey:             "000000-000000-000000-000000-000000",
		HostHeader:         os.Getenv(envTestHostHeader),
		Timeout:            20 * time.Second,
		InsecureSkipVerify: os.Getenv(envTestInsecure) == "1",
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = c.GetMailbox(ctx, mailbox)
	if err == nil {
		t.Fatal("a bogus API key was accepted")
	}
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v, want ErrUnauthorized", err)
	}
	t.Logf("rejected as expected: %v", err)
}
