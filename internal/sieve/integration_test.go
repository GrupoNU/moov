package sieve

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"
)

// Env-gated integration against a REAL Dovecot ManageSieve, following the
// internal/imap precedent exactly: the variables name a dedicated test
// account (never a production mailbox), and the test skips cleanly — never
// fails — when they are unset.
//
//	MOOV_SIEVE_TEST_HOST      ManageSieve host
//	MOOV_SIEVE_TEST_PORT      port (default 4190)
//	MOOV_SIEVE_TEST_USER      account (the moov-test mailbox only)
//	MOOV_SIEVE_TEST_PASSWORD  its app password
//	MOOV_SIEVE_TEST_TLS_NAME  certificate name, when it differs from host
func integrationConfig(t *testing.T) (Config, bool) {
	t.Helper()
	host := os.Getenv("MOOV_SIEVE_TEST_HOST")
	user := os.Getenv("MOOV_SIEVE_TEST_USER")
	pass := os.Getenv("MOOV_SIEVE_TEST_PASSWORD")
	if host == "" || user == "" || pass == "" {
		t.Skip("MOOV_SIEVE_TEST_HOST/USER/PASSWORD not set; skipping the live ManageSieve test")
		return Config{}, false
	}
	cfg := Config{
		Host:          host,
		Username:      user,
		Password:      pass,
		TLSServerName: os.Getenv("MOOV_SIEVE_TEST_TLS_NAME"),
	}
	if p := os.Getenv("MOOV_SIEVE_TEST_PORT"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			t.Fatalf("MOOV_SIEVE_TEST_PORT: %v", err)
		}
		cfg.Port = n
	}
	return cfg, true
}

// A full read-mostly round trip against the live server: connect, list,
// CHECKSCRIPT a known-good and a known-bad script. Deliberately performs NO
// writes to the account's stored scripts: the write cycle is exercised by the
// managed-script integration in manager_integration_test.go under its own
// dedicated script name.
func TestIntegrationConnectAndCheck(t *testing.T) {
	cfg, ok := integrationConfig(t)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c := New(nil)
	if err := c.Connect(ctx, cfg); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = c.Close() }()

	caps := c.Capabilities()
	// The generator's floor: what the whole managed-script model assumes the
	// Mailcow Dovecot advertises (captured in the E6 recon).
	for _, ext := range []string{"fileinto", "imap4flags", "vacation", "copy", "date", "relational", "variables"} {
		if !caps.HasExtension(ext) {
			t.Errorf("server does not advertise %q; the generator's floor assumption is broken", ext)
		}
	}

	if _, err := c.ListScripts(ctx); err != nil {
		t.Fatalf("ListScripts: %v", err)
	}

	if _, err := c.CheckScript(ctx, []byte("require [\"fileinto\"];\r\nif true { fileinto \"INBOX\"; }\r\n")); err != nil {
		t.Errorf("CheckScript of a valid script: %v", err)
	}
	_, err := c.CheckScript(ctx, []byte("this is not sieve\r\n"))
	var serr *ScriptError
	if !errors.As(err, &serr) {
		t.Errorf("CheckScript of garbage = %v, want *ScriptError", err)
	}
}
