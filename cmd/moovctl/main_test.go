package main

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/GrupoNU/moov/internal/crypto"
	"github.com/GrupoNU/moov/internal/mailcow"
)

// mailcowCreateRequestForTest is a minimal well-formed request, used only to
// call the stand-in API and assert it refuses.
func mailcowCreateRequestForTest() mailcow.CreateAppPasswordRequest {
	return mailcow.CreateAppPasswordRequest{Mailbox: "user@example.com", Password: "pw"}
}

// runCLI invokes the CLI with the given arguments and captures its streams.
func runCLI(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"no command", nil},
		{"unknown command", []string{"frobnicate"}},
		{"account without a subcommand", []string{"account"}},
		{"unknown account subcommand", []string{"account", "frobnicate"}},
		{"key without a subcommand", []string{"key"}},
		{"unknown key subcommand", []string{"key", "frobnicate"}},
		{"account add without an email", []string{"account", "add"}},
		{"account add with two emails", []string{"account", "add", "a@b.c", "d@e.f"}},
		{"account disable without an email", []string{"account", "disable"}},
		{"account list with an argument", []string{"account", "list", "extra"}},
		{"key generate with an argument", []string{"key", "generate", "extra"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runCLI(t, "", tc.args...)
			if code != exitUsage {
				t.Fatalf("exit code = %d, want %d (usage)\nstderr: %s", code, exitUsage, stderr)
			}
		})
	}
}

func TestHelpAndVersion(t *testing.T) {
	code, stdout, _ := runCLI(t, "", "help")
	if code != exitOK {
		t.Fatalf("help exited %d", code)
	}
	for _, want := range []string{"account add", "account list", "account disable", "key generate"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help does not document %q", want)
		}
	}
	// The usage text must warn about secrets on the command line.
	if !strings.Contains(stdout, "ps") {
		t.Error("help does not explain why secrets are not arguments")
	}

	code, stdout, _ = runCLI(t, "", "-version")
	if code != exitOK {
		t.Fatalf("-version exited %d", code)
	}
	if stdout == "" {
		t.Error("-version printed nothing")
	}
}

func TestKeyGenerate(t *testing.T) {
	code, stdout, stderr := runCLI(t, "", "key", "generate")
	if code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", code, stderr)
	}

	// stdout is "<id>:<base64>" and must parse straight back into a keyring.
	line := strings.TrimSpace(stdout)
	kr, err := crypto.ParseKeyring(line)
	if err != nil {
		t.Fatalf("the generated key does not parse as a keyring: %v", err)
	}
	if kr.PrimaryID() != 1 {
		t.Errorf("PrimaryID = %d, want the default 1", kr.PrimaryID())
	}

	// And it must be a real 32-byte key, not a placeholder.
	_, encoded, found := strings.Cut(line, ":")
	if !found {
		t.Fatalf("output %q is not in <id>:<key> form", line)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("the key is not base64: %v", err)
	}
	if len(raw) != crypto.KeySize {
		t.Fatalf("key is %d bytes, want %d", len(raw), crypto.KeySize)
	}
	if bytes.Equal(raw, make([]byte, crypto.KeySize)) {
		t.Fatal("the generated key is all zeroes")
	}

	// The guidance belongs on stderr, so the key can be piped alone.
	for _, want := range []string{crypto.EnvMasterKey, "rotate"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the guidance does not mention %q", want)
		}
	}
}

func TestKeyGenerateIsRandom(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 10; i++ {
		_, stdout, _ := runCLI(t, "", "key", "generate")
		if seen[stdout] {
			t.Fatal("key generate produced the same key twice")
		}
		seen[stdout] = true
	}
}

func TestKeyGenerateBare(t *testing.T) {
	code, stdout, _ := runCLI(t, "", "key", "generate", "-bare")
	if code != exitOK {
		t.Fatalf("exit code = %d", code)
	}
	line := strings.TrimSpace(stdout)
	if strings.Contains(line, ":") {
		t.Fatalf("-bare printed the id prefix: %q", line)
	}
	// A bare key is the single-key configuration form.
	if _, err := crypto.ParseKeyring(line); err != nil {
		t.Fatalf("the bare key does not parse: %v", err)
	}
}

func TestKeyGenerateWithID(t *testing.T) {
	code, stdout, _ := runCLI(t, "", "key", "generate", "-id", "7")
	if code != exitOK {
		t.Fatalf("exit code = %d", code)
	}
	kr, err := crypto.ParseKeyring(strings.TrimSpace(stdout))
	if err != nil {
		t.Fatalf("ParseKeyring: %v", err)
	}
	if kr.PrimaryID() != 7 {
		t.Errorf("PrimaryID = %d, want 7", kr.PrimaryID())
	}
}

func TestKeyGenerateRejectsBadID(t *testing.T) {
	for _, id := range []string{"0", "256", "999"} {
		code, _, _ := runCLI(t, "", "key", "generate", "-id", id)
		if code != exitUsage {
			t.Errorf("-id %s exited %d, want %d", id, code, exitUsage)
		}
	}
}

func TestCommandsRefuseToRunWithoutADatabase(t *testing.T) {
	// Every command that touches the store must fail with a clear message
	// rather than a nil-pointer panic when it is misconfigured.
	t.Setenv("MOOV_DATABASE_URL", "")
	t.Setenv(crypto.EnvMasterKey, "")
	t.Setenv(crypto.EnvMasterKeyFile, "")

	for _, args := range [][]string{
		{"account", "list"},
		{"account", "disable", "user@example.com"},
		{"key", "rotate"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, _, stderr := runCLI(t, "", args...)
			if code != exitFailure {
				t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitFailure, stderr)
			}
			if stderr == "" {
				t.Error("the failure was silent")
			}
		})
	}
}

func TestAccountAddRefusesWithoutAMasterKey(t *testing.T) {
	// No master key means no way to protect the credential, so the flow must
	// stop before it validates anything or touches Mailcow.
	t.Setenv(crypto.EnvMasterKey, "")
	t.Setenv(crypto.EnvMasterKeyFile, "")
	t.Setenv("MOOV_DATABASE_URL", "postgres://invalid/invalid")
	t.Setenv(envAccountPassword, "some-password")

	code, _, stderr := runCLI(t, "", "account", "add", "user@example.com")
	if code != exitFailure {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", code, exitFailure, stderr)
	}
	if !strings.Contains(stderr, crypto.EnvMasterKey) {
		t.Errorf("the error does not name the missing variable: %s", stderr)
	}
}

func TestAccountAddRejectsANonAddress(t *testing.T) {
	t.Setenv(envAccountPassword, "pw")
	code, _, _ := runCLI(t, "", "account", "add", "not-an-address")
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
}

func TestSecretsAreNeverEchoed(t *testing.T) {
	// A password supplied through the environment or a pipe must not appear in
	// either output stream, whatever else goes wrong.
	const password = "PIPED-SECRET-PASSWORD-abc123"

	t.Setenv(crypto.EnvMasterKey, "")
	t.Setenv(crypto.EnvMasterKeyFile, "")
	t.Setenv("MOOV_DATABASE_URL", "postgres://invalid/invalid")

	// Via a pipe on stdin.
	t.Setenv(envAccountPassword, "")
	_, stdout, stderr := runCLI(t, password+"\n", "account", "add", "user@example.com")
	assertNoSecret(t, "piped password", stdout+stderr, password)

	// Via the environment.
	t.Setenv(envAccountPassword, password)
	_, stdout, stderr = runCLI(t, "", "account", "add", "user@example.com")
	assertNoSecret(t, "environment password", stdout+stderr, password)
}

func TestMasterKeyIsNeverEchoed(t *testing.T) {
	// A configured master key must not be printed by any command, including
	// the ones that fail.
	material := bytes.Repeat([]byte{0x5C}, crypto.KeySize)
	encoded := crypto.EncodeKey(material)

	t.Setenv(crypto.EnvMasterKey, encoded)
	t.Setenv(crypto.EnvMasterKeyFile, "")
	t.Setenv("MOOV_DATABASE_URL", "postgres://invalid/invalid")
	t.Setenv(envAccountPassword, "pw")

	for _, args := range [][]string{
		{"key", "rotate"},
		{"account", "list"},
		{"-v", "account", "add", "user@example.com"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, stdout, stderr := runCLI(t, "", args...)
			assertNoSecret(t, "output", stdout+stderr, encoded)
		})
	}
}

func assertNoSecret(t *testing.T, where, haystack, needle string) {
	t.Helper()
	if needle == "" {
		t.Fatal("the test's own needle is empty")
	}
	if strings.Contains(haystack, needle) {
		t.Fatalf("a secret appears in %s:\n%s", where, haystack)
	}
	// Catch a partial echo too.
	if len(needle) > 12 && strings.Contains(haystack, needle[:12]) {
		t.Fatalf("a prefix of a secret appears in %s:\n%s", where, haystack)
	}
}

func TestUnavailableMailcowAPIFailsLoudly(t *testing.T) {
	// The stand-in used in -app-password mode must never silently succeed:
	// a provisioning that skipped creating a credential would be worse than
	// one that stops.
	api := unavailableMailcowAPI{}
	if _, err := api.GetMailbox(t.Context(), "user@example.com"); err == nil {
		t.Error("GetMailbox succeeded")
	}
	if _, err := api.CreateAppPassword(t.Context(), mailcowCreateRequestForTest()); err == nil {
		t.Error("CreateAppPassword succeeded")
	}
	if err := api.DeleteAppPassword(t.Context(), 1); err == nil {
		t.Error("DeleteAppPassword succeeded")
	}
}

func TestIMAPConfigFromEnv(t *testing.T) {
	t.Setenv(envIMAPHost, "")
	t.Setenv(envIMAPPort, "")
	t.Setenv(envIMAPServerName, "")

	cfg, err := imapConfigFromEnv()
	if err != nil {
		t.Fatalf("imapConfigFromEnv: %v", err)
	}
	if cfg.IMAPHost != defaultIMAPHost {
		t.Errorf("IMAPHost = %q, want the Mailcow container alias %q", cfg.IMAPHost, defaultIMAPHost)
	}
	if cfg.IMAPPort != 143 {
		t.Errorf("IMAPPort = %d, want 143 (STARTTLS)", cfg.IMAPPort)
	}

	t.Setenv(envIMAPHost, "mail.example.com")
	t.Setenv(envIMAPPort, "1143")
	t.Setenv(envIMAPServerName, "cert.example.com")
	cfg, err = imapConfigFromEnv()
	if err != nil {
		t.Fatalf("imapConfigFromEnv: %v", err)
	}
	if cfg.IMAPHost != "mail.example.com" || cfg.IMAPPort != 1143 || cfg.IMAPServerName != "cert.example.com" {
		t.Errorf("environment overrides did not apply: %+v", cfg)
	}

	t.Setenv(envIMAPPort, "not-a-port")
	if _, err := imapConfigFromEnv(); err == nil {
		t.Error("a non-numeric port was accepted")
	}
}
