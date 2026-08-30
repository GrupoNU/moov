package sieve

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"testing"
	"time"
)

// A scripted fake ManageSieve server, following the precedent of
// internal/imap's fakes: the test declares the exact byte exchange, the fake
// plays the server side, and any divergence fails the test with both sides
// shown. STARTTLS is REAL — the fake upgrades with a self-signed certificate
// the client verifies through TLSRootCAsPEM, so the production TLS path is
// the tested path and verification stays ON in the unit suite.

// fakeStep is one expected command and its canned response, both raw bytes.
type fakeStep struct {
	expect  string
	respond string
}

// fakeAuthLine is the AUTHENTICATE line the client must emit for the fixed
// test credentials: base64("\x00moov-test@example.test\x00app-password").
const fakeAuthLine = "AUTHENTICATE \"PLAIN\" \"AG1vb3YtdGVzdEBleGFtcGxlLnRlc3QAYXBwLXBhc3N3b3Jk\"\r\n"

// fakeGreeting is the capability listing our Mailcow Dovecot 2.3.21.1 sends
// before STARTTLS, captured on the wire (E6 recon): note the EMPTY SASL list.
const fakeGreeting = "\"IMPLEMENTATION\" \"Dovecot Pigeonhole\"\r\n" +
	"\"SIEVE\" \"fileinto reject envelope vacation imap4flags copy include variables body relational date index duplicate mime foreverypart regex\"\r\n" +
	"\"NOTIFY\" \"mailto\"\r\n" +
	"\"SASL\" \"\"\r\n" +
	"\"STARTTLS\"\r\n" +
	"\"VERSION\" \"1.0\"\r\n" +
	"OK \"Dovecot ready.\"\r\n"

// fakeRecap is the re-issued listing after the TLS upgrade — no STARTTLS, a
// populated SASL list (RFC 5804 §2.2).
const fakeRecap = "\"IMPLEMENTATION\" \"Dovecot Pigeonhole\"\r\n" +
	"\"SIEVE\" \"fileinto reject envelope vacation imap4flags copy include variables body relational date index duplicate mime foreverypart regex\"\r\n" +
	"\"NOTIFY\" \"mailto\"\r\n" +
	"\"SASL\" \"PLAIN\"\r\n" +
	"\"VERSION\" \"1.0\"\r\n" +
	"OK \"TLS negotiation successful.\"\r\n"

// fakeServer runs the scripted exchange on one accepted connection.
type fakeServer struct {
	t     *testing.T
	ln    net.Listener
	caPEM []byte
	steps []fakeStep
	errc  chan error
}

// startFake starts the fake. Steps are what follows a successful connect
// (greeting, STARTTLS, upgrade, recap and AUTHENTICATE are handled by the
// fake itself; authOK controls the AUTHENTICATE answer).
func startFake(t *testing.T, authOK bool, steps []fakeStep) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	certPEM, keyPEM := selfSigned(t)
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}

	f := &fakeServer{t: t, ln: ln, caPEM: certPEM, steps: steps, errc: make(chan error, 1)}
	go func() {
		f.errc <- f.serve(cert, authOK)
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeServer) addr() string { return f.ln.Addr().String() }

// wait returns the server side's verdict; every test must call it so a
// mismatch on the fake's side fails the test rather than vanishing.
func (f *fakeServer) wait() error { return <-f.errc }

func (f *fakeServer) serve(cert tls.Certificate, authOK bool) error {
	conn, err := f.ln.Accept()
	if err != nil {
		return fmt.Errorf("accept: %w", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	if _, err := io.WriteString(conn, fakeGreeting); err != nil {
		return err
	}
	if err := expectBytes(conn, "STARTTLS\r\n"); err != nil {
		return err
	}
	if _, err := io.WriteString(conn, "OK \"Begin TLS negotiation now.\"\r\n"); err != nil {
		return err
	}
	tconn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err := tconn.Handshake(); err != nil {
		return fmt.Errorf("tls handshake: %w", err)
	}
	_ = tconn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(tconn, fakeRecap); err != nil {
		return err
	}

	// AUTHENTICATE "PLAIN" "<base64>" — verified byte for byte against the
	// fixed test credentials, so a regression in the SASL PLAIN encoding
	// fails loudly here.
	authLine, err := readLineBytes(tconn)
	if err != nil {
		return fmt.Errorf("reading AUTHENTICATE: %w", err)
	}
	if string(authLine) != fakeAuthLine {
		return fmt.Errorf("AUTHENTICATE mismatch:\n got  %q\n want %q", authLine, fakeAuthLine)
	}
	if !authOK {
		_, err := io.WriteString(tconn, "NO \"Authentication failed.\"\r\n")
		return err
	}
	if _, err := io.WriteString(tconn, "OK \"Logged in.\"\r\n"); err != nil {
		return err
	}

	for i, step := range f.steps {
		if err := expectBytes(tconn, step.expect); err != nil {
			return fmt.Errorf("step %d: %w", i, err)
		}
		if _, err := io.WriteString(tconn, step.respond); err != nil {
			return fmt.Errorf("step %d respond: %w", i, err)
		}
	}
	return nil
}

// expectBytes reads exactly len(want) bytes and compares.
func expectBytes(r io.Reader, want string) error {
	buf := make([]byte, len(want))
	if _, err := io.ReadFull(r, buf); err != nil {
		return fmt.Errorf("reading %d bytes (want %q): %w", len(want), want, err)
	}
	if !bytes.Equal(buf, []byte(want)) {
		return fmt.Errorf("command mismatch:\n got  %q\n want %q", buf, want)
	}
	return nil
}

// readLineBytes reads through the next CRLF.
func readLineBytes(r io.Reader) ([]byte, error) {
	var out []byte
	one := make([]byte, 1)
	for {
		if _, err := r.Read(one); err != nil {
			return nil, err
		}
		out = append(out, one[0])
		if len(out) >= 2 && out[len(out)-2] == '\r' && out[len(out)-1] == '\n' {
			return out, nil
		}
		if len(out) > 4096 {
			return nil, fmt.Errorf("line too long: %q", out)
		}
	}
}

// selfSigned mints a certificate for 127.0.0.1, so the unit suite exercises
// the verifying TLS path rather than InsecureSkipVerify.
func selfSigned(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "sieve-fake"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:                  true, // self-signed roots must be CAs to verify
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshaling key: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

// connectFake dials the fake with a fully verifying config.
func connectFake(t *testing.T, f *fakeServer) Client {
	t.Helper()
	host, port, err := net.SplitHostPort(f.addr())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	var portN int
	if _, err := fmt.Sscanf(port, "%d", &portN); err != nil {
		t.Fatalf("port: %v", err)
	}
	c := New(testLogger(t))
	cfg := Config{
		Host:          host,
		Port:          portN,
		Username:      "moov-test@example.test",
		Password:      "app-password",
		TLSRootCAsPEM: f.caPEM,
	}
	if err := c.Connect(testCtx(t), cfg); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
