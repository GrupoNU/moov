package sieve

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSuffix(string(p), "\n"))
	return len(p), nil
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// Connect: STARTTLS is negotiated for real, capabilities are the POST-TLS
// set (RFC 5804 §2.2 — the pre-TLS set advertised an empty SASL list), and
// PLAIN carries the app password.
func TestConnectReadsPostTLSCapabilities(t *testing.T) {
	f := startFake(t, true, nil)
	c := connectFake(t, f)

	caps := c.Capabilities()
	if !caps.HasExtension("vacation") || !caps.HasExtension("imap4flags") {
		t.Errorf("capabilities missing expected extensions: %v", caps.Extensions)
	}
	if caps.StartTLS {
		t.Error("post-TLS capabilities still advertise STARTTLS; §2.2 forbids that and the client kept the wrong set")
	}
	if len(caps.SASL) != 1 || caps.SASL[0] != "PLAIN" {
		t.Errorf("SASL = %v, want the post-TLS [PLAIN]", caps.SASL)
	}
	if caps.Version != "1.0" || caps.Implementation != "Dovecot Pigeonhole" {
		t.Errorf("version/implementation = %q/%q", caps.Version, caps.Implementation)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := f.wait(); err != nil {
		t.Fatalf("fake: %v", err)
	}
}

func TestConnectAuthFailure(t *testing.T) {
	f := startFake(t, false, nil)
	host, port, _ := strings.Cut(f.addr(), ":")
	var portN int
	if _, err := fmtSscan(port, &portN); err != nil {
		t.Fatalf("port: %v", err)
	}
	c := New(testLogger(t))
	err := c.Connect(testCtx(t), Config{
		Host: host, Port: portN,
		Username: "moov-test@example.test", Password: "app-password",
		TLSRootCAsPEM: f.caPEM,
	})
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("Connect error = %v, want ErrAuthFailed", err)
	}
	if strings.Contains(err.Error(), "app-password") {
		t.Fatal("the error leaked the password")
	}
	if err := f.wait(); err != nil {
		t.Fatalf("fake: %v", err)
	}
}

// ListScripts: quoted and literal names, the ACTIVE marker, and the §2.7
// single-active guarantee enforced.
func TestListScripts(t *testing.T) {
	f := startFake(t, true, []fakeStep{{
		expect: "LISTSCRIPTS\r\n",
		respond: "\"summer\"\r\n" +
			"{4}\r\nmoov ACTIVE\r\n" +
			"OK \"Listscripts completed.\"\r\n",
	}})
	c := connectFake(t, f)

	scripts, err := c.ListScripts(testCtx(t))
	if err != nil {
		t.Fatalf("ListScripts: %v", err)
	}
	want := []ScriptInfo{{Name: "summer"}, {Name: "moov", Active: true}}
	if len(scripts) != len(want) {
		t.Fatalf("scripts = %+v, want %+v", scripts, want)
	}
	for i := range want {
		if scripts[i] != want[i] {
			t.Errorf("script %d = %+v, want %+v", i, scripts[i], want[i])
		}
	}
	if err := f.wait(); err != nil {
		t.Fatalf("fake: %v", err)
	}
}

func TestListScriptsRefusesTwoActive(t *testing.T) {
	f := startFake(t, true, []fakeStep{{
		expect:  "LISTSCRIPTS\r\n",
		respond: "\"a\" ACTIVE\r\n\"b\" ACTIVE\r\nOK\r\n",
	}})
	c := connectFake(t, f)
	if _, err := c.ListScripts(testCtx(t)); err == nil {
		t.Fatal("two ACTIVE lines were accepted; RFC 5804 §2.7 forbids them")
	}
	if err := f.wait(); err != nil {
		t.Fatalf("fake: %v", err)
	}
}

// GetScript: the content arrives as a server literal, byte-preserved.
func TestGetScript(t *testing.T) {
	content := "require [\"fileinto\"];\r\nif true { fileinto \"X\"; }\r\n"
	f := startFake(t, true, []fakeStep{{
		expect:  "GETSCRIPT \"moov\"\r\n",
		respond: "{" + itoa(len(content)) + "}\r\n" + content + "\r\nOK\r\n",
	}})
	c := connectFake(t, f)
	got, err := c.GetScript(testCtx(t), "moov")
	if err != nil {
		t.Fatalf("GetScript: %v", err)
	}
	if string(got) != content {
		t.Errorf("content mismatch:\n got  %q\n want %q", got, content)
	}
	if err := f.wait(); err != nil {
		t.Fatalf("fake: %v", err)
	}
}

func TestGetScriptNotFound(t *testing.T) {
	f := startFake(t, true, []fakeStep{{
		expect:  "GETSCRIPT \"nope\"\r\n",
		respond: "NO (NONEXISTENT) \"There is no script by that name\"\r\n",
	}})
	c := connectFake(t, f)
	if _, err := c.GetScript(testCtx(t), "nope"); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("error = %v, want ErrScriptNotFound", err)
	}
	if err := f.wait(); err != nil {
		t.Fatalf("fake: %v", err)
	}
}

// PutScript: the content goes as a non-synchronizing literal, and a WARNINGS
// OK surfaces the diagnostic without failing.
func TestPutScriptWithWarnings(t *testing.T) {
	content := "#comment\r\nkeep;\r\n"
	f := startFake(t, true, []fakeStep{{
		expect:  "PUTSCRIPT \"moov\" {" + itoa(len(content)) + "+}\r\n" + content + "\r\n",
		respond: "OK (WARNINGS) \"line 1: something looks off\"\r\n",
	}})
	c := connectFake(t, f)
	warnings, err := c.PutScript(testCtx(t), "moov", []byte(content))
	if err != nil {
		t.Fatalf("PutScript: %v", err)
	}
	if !strings.Contains(warnings, "line 1") {
		t.Errorf("warnings = %q, want the server diagnostic", warnings)
	}
	if err := f.wait(); err != nil {
		t.Fatalf("fake: %v", err)
	}
}

func TestPutScriptInvalidIsScriptError(t *testing.T) {
	f := startFake(t, true, []fakeStep{{
		expect:  "PUTSCRIPT \"moov\" {9+}\r\nnot sieve\r\n",
		respond: "NO \"line 1: error: unknown command 'not'\"\r\n",
	}})
	c := connectFake(t, f)
	_, err := c.PutScript(testCtx(t), "moov", []byte("not sieve"))
	var serr *ScriptError
	if !errors.As(err, &serr) {
		t.Fatalf("error = %v, want *ScriptError", err)
	}
	if !strings.Contains(serr.Message, "line 1") {
		t.Errorf("diagnostic %q lost the server's line number", serr.Message)
	}
	if err := f.wait(); err != nil {
		t.Fatalf("fake: %v", err)
	}
}

func TestPutScriptQuota(t *testing.T) {
	f := startFake(t, true, []fakeStep{{
		expect:  "PUTSCRIPT \"moov\" {5+}\r\nkeep;\r\n",
		respond: "NO (QUOTA/MAXSIZE) \"Script too large.\"\r\n",
	}})
	c := connectFake(t, f)
	_, err := c.PutScript(testCtx(t), "moov", []byte("keep;"))
	var qerr *QuotaError
	if !errors.As(err, &qerr) || qerr.Code != "QUOTA/MAXSIZE" {
		t.Fatalf("error = %v, want *QuotaError with QUOTA/MAXSIZE", err)
	}
	if err := f.wait(); err != nil {
		t.Fatalf("fake: %v", err)
	}
}

// SetActive: including the §2.8 empty-string deactivation.
func TestSetActiveAndDeactivate(t *testing.T) {
	f := startFake(t, true, []fakeStep{
		{expect: "SETACTIVE \"moov\"\r\n", respond: "OK\r\n"},
		{expect: "SETACTIVE \"\"\r\n", respond: "OK\r\n"},
	})
	c := connectFake(t, f)
	if err := c.SetActive(testCtx(t), "moov"); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if err := c.SetActive(testCtx(t), ""); err != nil {
		t.Fatalf("SetActive(\"\"): %v", err)
	}
	if err := f.wait(); err != nil {
		t.Fatalf("fake: %v", err)
	}
}

func TestDeleteActiveScript(t *testing.T) {
	f := startFake(t, true, []fakeStep{{
		expect:  "DELETESCRIPT \"moov\"\r\n",
		respond: "NO (ACTIVE) \"You may not delete an active script\"\r\n",
	}})
	c := connectFake(t, f)
	if err := c.DeleteScript(testCtx(t), "moov"); !errors.Is(err, ErrScriptActive) {
		t.Fatalf("error = %v, want ErrScriptActive", err)
	}
	if err := f.wait(); err != nil {
		t.Fatalf("fake: %v", err)
	}
}

func TestRenameScriptTargetTaken(t *testing.T) {
	f := startFake(t, true, []fakeStep{{
		expect:  "RENAMESCRIPT \"a\" \"b\"\r\n",
		respond: "NO (ALREADYEXISTS) \"A script with that name already exists\"\r\n",
	}})
	c := connectFake(t, f)
	if err := c.RenameScript(testCtx(t), "a", "b"); !errors.Is(err, ErrScriptExists) {
		t.Fatalf("error = %v, want ErrScriptExists", err)
	}
	if err := f.wait(); err != nil {
		t.Fatalf("fake: %v", err)
	}
}

// Script names with quotes and backslashes are escaped on the wire, and the
// TRYLATER code is surfaced as a transient ServerError.
func TestQuotingAndTransient(t *testing.T) {
	f := startFake(t, true, []fakeStep{{
		expect:  "GETSCRIPT \"we\\\"ird\\\\name\"\r\n",
		respond: "NO (TRYLATER) \"busy\"\r\n",
	}})
	c := connectFake(t, f)
	_, err := c.GetScript(testCtx(t), `we"ird\name`)
	var serr *ServerError
	if !errors.As(err, &serr) || !serr.Transient() {
		t.Fatalf("error = %v, want a transient *ServerError", err)
	}
	if err := f.wait(); err != nil {
		t.Fatalf("fake: %v", err)
	}
}

func TestMethodsRefuseWhenNotConnected(t *testing.T) {
	c := New(testLogger(t))
	if _, err := c.ListScripts(testCtx(t)); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("ListScripts before Connect = %v, want ErrNotConnected", err)
	}
}

// small helpers kept out of the fake

func itoa(n int) string { return fmtInt(n) }

func fmtInt(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func fmtSscan(s string, out *int) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("not a number: " + s)
		}
		n = n*10 + int(r-'0')
	}
	*out = n
	return 1, nil
}
