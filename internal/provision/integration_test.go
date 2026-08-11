package provision

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/crypto"
	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/mailcow"
)

// Integration test of the full ADR §4 flow against a REAL Dovecot and a REAL
// Mailcow API. It is skipped unless the environment names both.
//
//	MOOV_IMAP_TEST_HOST            Dovecot host (e.g. "dovecot")
//	MOOV_IMAP_TEST_USER            the test mailbox
//	MOOV_IMAP_TEST_PASSWORD        that mailbox's own password
//	MOOV_IMAP_TEST_SERVER_NAME     name the certificate is verified against
//	MOOV_IMAP_TEST_INSECURE        "1" to skip certificate verification
//	MOOV_MAILCOW_TEST_BASE_URL     the Mailcow API root
//	MOOV_MAILCOW_TEST_API_KEY      a read-write API key
//	MOOV_MAILCOW_TEST_HOST_HEADER  the Host header nginx routes on
//	MOOV_MAILCOW_TEST_INSECURE     "1" to skip TLS verification
//
// The store is faked: this test is about the flow against the two real
// external systems, and requiring a PostgreSQL as well would make it skip in
// most environments for no extra coverage of the parts that matter.
//
// Everything it creates on Mailcow is deleted again.
func TestIntegrationProvisionAgainstRealServices(t *testing.T) {
	imapHost := os.Getenv("MOOV_IMAP_TEST_HOST")
	mailbox := os.Getenv("MOOV_IMAP_TEST_USER")
	password := os.Getenv("MOOV_IMAP_TEST_PASSWORD")
	mcBaseURL := os.Getenv("MOOV_MAILCOW_TEST_BASE_URL")
	mcAPIKey := os.Getenv("MOOV_MAILCOW_TEST_API_KEY")

	if imapHost == "" || mailbox == "" || password == "" || mcBaseURL == "" || mcAPIKey == "" {
		t.Skip("set MOOV_IMAP_TEST_* and MOOV_MAILCOW_TEST_* to run the provisioning integration test")
	}
	// The same safety rule as the mailcow package: this creates and deletes
	// credentials on a live server.
	if !strings.Contains(mailbox, "moov-test") && !strings.Contains(mailbox, "test@") {
		t.Fatalf("MOOV_IMAP_TEST_USER is %q, which is not marked as a test mailbox", mailbox)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// --- real dependencies -------------------------------------------------
	mcClient, err := mailcow.New(mailcow.Config{
		BaseURL:            mcBaseURL,
		APIKey:             mcAPIKey,
		HostHeader:         os.Getenv("MOOV_MAILCOW_TEST_HOST_HEADER"),
		Timeout:            20 * time.Second,
		InsecureSkipVerify: os.Getenv("MOOV_MAILCOW_TEST_INSECURE") == "1",
	})
	if err != nil {
		t.Fatalf("mailcow.New: %v", err)
	}

	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	material, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	key, err := crypto.NewKey(1, material)
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	keyring, err := crypto.NewKeyring(key)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}

	fakeSt := newFakeStore()

	p, err := New(Config{
		IMAPHost:               imapHost,
		IMAPServerName:         os.Getenv("MOOV_IMAP_TEST_SERVER_NAME"),
		IMAPInsecureSkipVerify: os.Getenv("MOOV_IMAP_TEST_INSECURE") == "1",
	}, NewIMAPValidator(logger), mcClient, keyring, fakeSt, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// --- the flow ----------------------------------------------------------
	res, err := p.Provision(ctx, Request{Email: mailbox, Password: password})
	if err != nil {
		t.Fatalf("Provision against the real services: %v", err)
	}

	var cleaned bool
	t.Cleanup(func() {
		if cleaned || res.AppPasswordID == 0 {
			return
		}
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		if err := mcClient.DeleteAppPassword(cctx, res.AppPasswordID); err != nil {
			t.Errorf("CLEANUP FAILED: app password id %d (%q) on %s must be deleted by hand: %v",
				res.AppPasswordID, res.AppPasswordName, mailbox, err)
		}
	})

	t.Logf("provisioned account %d, app password id=%d name=%q",
		res.Account.ID, res.AppPasswordID, res.AppPasswordName)
	if res.AppPasswordID == 0 {
		t.Fatal("no Mailcow app password was created")
	}

	// --- the credential that was stored actually works ---------------------
	//
	// This is the assertion that makes the whole flow meaningful: the sealed
	// bytes in the store, decrypted, are a credential that logs in to the
	// real Dovecot.
	stored := fakeSt.accounts[mailbox]
	if len(stored.IMAPAppPassword) == 0 {
		t.Fatal("no credential was stored")
	}
	opened, err := keyring.Open(stored.IMAPAppPassword, crypto.AccountAAD(stored.ID))
	if err != nil {
		t.Fatalf("the stored credential does not open: %v", err)
	}
	if string(opened) == password {
		t.Fatal("the stored credential IS the user's own password")
	}

	client := imap.New(logger)
	loginCfg := imap.Config{
		Host:               imapHost,
		Username:           mailbox,
		Password:           string(opened),
		TLSServerName:      os.Getenv("MOOV_IMAP_TEST_SERVER_NAME"),
		InsecureSkipVerify: os.Getenv("MOOV_IMAP_TEST_INSECURE") == "1",
	}
	if err := client.Connect(ctx, loginCfg); err != nil {
		t.Fatalf("the provisioned app password does not authenticate against Dovecot: %v", err)
	}
	boxes, err := client.ListMailboxes(ctx)
	if err != nil {
		t.Fatalf("listing mailboxes with the provisioned credential: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Logf("closing the verification connection: %v", err)
	}
	t.Logf("the provisioned app password authenticated and listed %d mailboxes", len(boxes))

	// --- and the user's password went nowhere ------------------------------
	assertAbsent(t, "store writes", fakeSt.allWrittenBytes(), password)
	assertAbsent(t, "log output", logs.Bytes(), password)

	// --- explicit revocation -----------------------------------------------
	if err := mcClient.DeleteAppPassword(ctx, res.AppPasswordID); err != nil {
		t.Fatalf("revoking the provisioned app password: %v", err)
	}
	cleaned = true

	list, err := mcClient.ListAppPasswords(ctx, mailbox)
	if err != nil {
		t.Fatalf("listing after revocation: %v", err)
	}
	for _, ap := range list {
		if ap.ID == res.AppPasswordID {
			t.Fatalf("app password id %d survived revocation", res.AppPasswordID)
		}
	}
	t.Logf("revoked app password id=%d", res.AppPasswordID)
}
