package sieve

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"syscall"
	"time"
)

// ScriptInfo is one stored script as LISTSCRIPTS reports it.
type ScriptInfo struct {
	Name   string
	Active bool
}

// Client is the ManageSieve surface the rest of Moov sees. One command
// stream; NOT safe for concurrent use (doc.go).
type Client interface {
	// Connect dials the server, negotiates STARTTLS (refusing a server that
	// does not offer it), re-reads capabilities as RFC 5804 §2.2 requires,
	// and authenticates with SASL PLAIN.
	Connect(ctx context.Context, cfg Config) error

	// Capabilities reports the post-STARTTLS capability set. Valid only
	// after a successful Connect.
	Capabilities() Capabilities

	// ListScripts returns the stored scripts, with at most one marked active
	// (RFC 5804 §2.7).
	ListScripts(ctx context.Context) ([]ScriptInfo, error)

	// GetScript returns a script's raw content (§2.9). A missing script is
	// ErrScriptNotFound.
	GetScript(ctx context.Context, name string) ([]byte, error)

	// PutScript stores a script under a name, replacing any previous content
	// (§2.6). The server validates before storing: an invalid script is a
	// *ScriptError, a refused size or count is a *QuotaError. A non-empty
	// warnings return carries the server's WARNINGS diagnostic for a script
	// that was STORED but looks suspect.
	PutScript(ctx context.Context, name string, content []byte) (warnings string, err error)

	// CheckScript validates content without storing it (§2.12).
	CheckScript(ctx context.Context, content []byte) (warnings string, err error)

	// SetActive marks one script active, or deactivates all when name is
	// empty (§2.8).
	SetActive(ctx context.Context, name string) error

	// DeleteScript removes a stored script (§2.10). Deleting the active
	// script is ErrScriptActive; a missing one is ErrScriptNotFound.
	DeleteScript(ctx context.Context, name string) error

	// RenameScript renames a stored script (§2.11), keeping it active if it
	// was. A taken target name is ErrScriptExists.
	RenameScript(ctx context.Context, oldName, newName string) error

	// Close logs out (best-effort) and releases the connection. Safe to call
	// more than once.
	Close() error
}

// New returns a Client that is not yet connected. logger may be nil.
func New(logger *slog.Logger) Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &client{log: logger}
}

type client struct {
	cfg    Config
	log    *slog.Logger
	conn   net.Conn
	rd     *reader
	w      *bufio.Writer
	caps   Capabilities
	closed bool
}

// Connect implements Client.
func (cl *client) Connect(ctx context.Context, cfg Config) error {
	cfg, err := cfg.Normalize()
	if err != nil {
		return err
	}
	tlsCfg, err := cfg.tlsConfig()
	if err != nil {
		return err
	}
	if cfg.InsecureSkipVerify {
		// Loud on purpose — same contract as internal/imap's Connect.
		cl.log.Warn("sieve: TLS certificate verification is DISABLED for this connection; "+
			"never use InsecureSkipVerify outside development",
			"host", cfg.Host, "port", cfg.Port)
	}

	dialer := &net.Dialer{Timeout: cfg.DialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", cfg.Address())
	if err != nil {
		return fmt.Errorf("sieve: dialing %s: %w", cfg.Address(), err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = conn.Close()
		}
	}()

	cl.cfg = cfg
	cl.conn = conn
	cl.rd = newReader(conn)
	cl.w = bufio.NewWriter(conn)
	cl.closed = false

	// The greeting: a capability listing ending in OK (§1.7).
	if err := cl.setDeadline(ctx); err != nil {
		return err
	}
	greeting, err := cl.rd.readResponse()
	if err != nil {
		return err
	}
	if !greeting.isOK() {
		return fmt.Errorf("sieve: server greeting refused the connection: %w", respError(greeting))
	}
	caps := capsFromResponse(greeting)

	// STARTTLS, mandatory (doc.go). §2.2: OK, then the TLS handshake, then
	// the server MUST re-issue capabilities followed by OK.
	if !caps.StartTLS {
		return ErrNoSTARTTLS
	}
	if err := writeCommand(cl.w, "STARTTLS"); err != nil {
		return fmt.Errorf("sieve: sending STARTTLS: %w", err)
	}
	resp, err := cl.rd.readResponse()
	if err != nil {
		return err
	}
	if !resp.isOK() {
		return fmt.Errorf("sieve: STARTTLS refused: %w", respError(resp))
	}
	tconn := tls.Client(conn, tlsCfg)
	if err := tconn.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("sieve: TLS handshake with %s: %w", cfg.Address(), err)
	}
	cl.conn = tconn
	cl.rd = newReader(tconn)
	cl.w = bufio.NewWriter(tconn)

	if err := cl.setDeadline(ctx); err != nil {
		return err
	}
	recap, err := cl.rd.readResponse()
	if err != nil {
		return err
	}
	if !recap.isOK() {
		return fmt.Errorf("sieve: post-TLS capability listing refused: %w", respError(recap))
	}
	cl.caps = capsFromResponse(recap)

	// AUTHENTICATE "PLAIN" with the initial response inline (§2.1). The
	// base64 alphabet needs no literal framing.
	ir := base64.StdEncoding.EncodeToString(
		[]byte("\x00" + cfg.Username + "\x00" + cfg.Password))
	if err := writeCommand(cl.w, "AUTHENTICATE", stringArg("PLAIN"), stringArg(ir)); err != nil {
		return fmt.Errorf("sieve: sending AUTHENTICATE: %w", err)
	}
	auth, err := cl.rd.readResponse()
	if err != nil {
		return err
	}
	if !auth.isOK() {
		// Deliberately not carrying the server's text: redaction rule of
		// errors.go. The username is safe and useful.
		return fmt.Errorf("sieve: %w for %s", ErrAuthFailed, cfg.Username)
	}

	ok = true
	cl.log.Debug("sieve: connected",
		"host", cfg.Host, "user", cfg.Username, "extensions", len(cl.caps.Extensions))
	return nil
}

// Capabilities implements Client. It returns a copy with its own slices.
func (cl *client) Capabilities() Capabilities {
	out := cl.caps
	out.Extensions = append([]string(nil), cl.caps.Extensions...)
	out.SASL = append([]string(nil), cl.caps.SASL...)
	out.Notify = append([]string(nil), cl.caps.Notify...)
	return out
}

// ListScripts implements Client.
func (cl *client) ListScripts(ctx context.Context) ([]ScriptInfo, error) {
	resp, err := cl.cmd(ctx, "LISTSCRIPTS")
	if err != nil {
		return nil, err
	}
	var out []ScriptInfo
	activeSeen := false
	for _, line := range resp.data {
		if len(line.tokens) == 0 || line.tokens[0].kind != tokenString {
			continue
		}
		info := ScriptInfo{Name: line.tokens[0].text}
		for _, t := range line.tokens[1:] {
			if t.kind == tokenAtom && strings.EqualFold(t.text, "ACTIVE") {
				info.Active = true
			}
		}
		if info.Active {
			if activeSeen {
				// §2.7: "The atom ACTIVE MUST NOT appear on more than one
				// response line." A server violating that is lying about
				// something this package's callers depend on.
				return nil, &ServerError{Message: "LISTSCRIPTS reported more than one active script"}
			}
			activeSeen = true
		}
		out = append(out, info)
	}
	return out, nil
}

// GetScript implements Client.
func (cl *client) GetScript(ctx context.Context, name string) ([]byte, error) {
	resp, err := cl.cmd(ctx, "GETSCRIPT", stringArg(name))
	if err != nil {
		return nil, err
	}
	for _, line := range resp.data {
		if len(line.tokens) > 0 && line.tokens[0].kind == tokenString {
			return []byte(line.tokens[0].text), nil
		}
	}
	// An empty script is a legitimate answer (a zero-length literal).
	return []byte{}, nil
}

// PutScript implements Client.
func (cl *client) PutScript(ctx context.Context, name string, content []byte) (string, error) {
	resp, err := cl.cmd(ctx, "PUTSCRIPT", stringArg(name), literalArg(content))
	if err != nil {
		return "", err
	}
	return warningsOf(resp), nil
}

// CheckScript implements Client.
func (cl *client) CheckScript(ctx context.Context, content []byte) (string, error) {
	resp, err := cl.cmd(ctx, "CHECKSCRIPT", literalArg(content))
	if err != nil {
		return "", err
	}
	return warningsOf(resp), nil
}

// SetActive implements Client.
func (cl *client) SetActive(ctx context.Context, name string) error {
	_, err := cl.cmd(ctx, "SETACTIVE", stringArg(name))
	return err
}

// DeleteScript implements Client.
func (cl *client) DeleteScript(ctx context.Context, name string) error {
	_, err := cl.cmd(ctx, "DELETESCRIPT", stringArg(name))
	return err
}

// RenameScript implements Client.
func (cl *client) RenameScript(ctx context.Context, oldName, newName string) error {
	_, err := cl.cmd(ctx, "RENAMESCRIPT", stringArg(oldName), stringArg(newName))
	return err
}

// Close implements Client.
func (cl *client) Close() error {
	if cl.closed || cl.conn == nil {
		return nil
	}
	cl.closed = true
	// LOGOUT is best-effort: the connection is going away either way.
	_ = cl.conn.SetDeadline(time.Now().Add(2 * time.Second))
	_ = writeCommand(cl.w, "LOGOUT")
	if err := cl.conn.Close(); err != nil && !isBenignCloseError(err) {
		return err
	}
	return nil
}

// isBenignCloseError reports errors that mean "the connection was already
// going away" — the expected state when the server hung up first (it answered
// our LOGOUT, or dropped us). Same contract as internal/imap's helper; the
// TLS closeNotify on a dead socket arrives as EPIPE/ECONNRESET.
func isBenignCloseError(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET)
}

// cmd runs one command round trip and maps a NO/BYE answer onto the error
// vocabulary of errors.go.
func (cl *client) cmd(ctx context.Context, verb string, args ...wireArg) (*response, error) {
	if cl.conn == nil || cl.closed {
		return nil, ErrNotConnected
	}
	if err := cl.setDeadline(ctx); err != nil {
		return nil, err
	}
	if err := writeCommand(cl.w, verb, args...); err != nil {
		return nil, fmt.Errorf("sieve: sending %s: %w", verb, err)
	}
	resp, err := cl.rd.readResponse()
	if err != nil {
		return nil, err
	}
	if resp.isOK() {
		return resp, nil
	}
	return nil, commandError(verb, resp)
}

// setDeadline applies the command deadline: the earlier of ctx's deadline and
// now+CommandTimeout. Context cancellation between commands is honored; a
// cancellation mid-read is bounded by the deadline rather than immediate,
// which is the documented trade of a deadline-based protocol client.
func (cl *client) setDeadline(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(cl.cfg.CommandTimeout)
	if cl.cfg.CommandTimeout <= 0 {
		deadline = time.Now().Add(DefaultCommandTimeout)
	}
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	return cl.conn.SetDeadline(deadline)
}

// capsFromResponse folds a capability listing into a Capabilities value.
// Each data line is a name string plus an optional value string (§1.7).
func capsFromResponse(resp *response) Capabilities {
	var caps Capabilities
	for _, line := range resp.data {
		if len(line.tokens) == 0 || line.tokens[0].kind != tokenString {
			continue
		}
		name := line.tokens[0].text
		value := ""
		if len(line.tokens) > 1 && line.tokens[1].kind == tokenString {
			value = line.tokens[1].text
		}
		caps.applyCapabilityLine(name, value)
	}
	return caps
}

// warningsOf extracts the WARNINGS diagnostic from a successful PUTSCRIPT or
// CHECKSCRIPT answer (§2.6: "An OK response MAY contain the WARNINGS response
// code").
func warningsOf(resp *response) string {
	if resp.code != "WARNINGS" {
		return ""
	}
	if resp.message != "" {
		return resp.message
	}
	return "the server reported warnings for this script"
}

// commandError maps a refused command onto the package's error vocabulary,
// using the response code first (§1.3) and the command as a tiebreak for the
// codeless NO that PUTSCRIPT/CHECKSCRIPT answer for an invalid script.
func commandError(verb string, resp *response) error {
	switch resp.code {
	case "NONEXISTENT":
		return ErrScriptNotFound
	case "ACTIVE":
		return ErrScriptActive
	case "ALREADYEXISTS":
		return ErrScriptExists
	case "QUOTA", "QUOTA/MAXSIZE", "QUOTA/MAXSCRIPTS":
		return &QuotaError{Code: resp.code, Message: resp.message}
	}
	if resp.status == "NO" && (verb == "PUTSCRIPT" || verb == "CHECKSCRIPT") {
		// §2.6/§2.12: an invalid script is refused with a human-readable
		// diagnostic and, in Dovecot's case, no response code.
		return &ScriptError{Message: resp.message}
	}
	return respError(resp)
}

// respError renders any refused response as a ServerError.
func respError(resp *response) error {
	return &ServerError{Code: resp.code, Message: resp.message}
}
