package submit

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// The SMTP submission client (RFC 6409 over RFC 5321), written on the
// standard library's net + crypto/tls + net/textproto.
//
// # Why not net/smtp
//
// net/smtp is frozen ("Frozen: this package is not accepting new features" —
// its own doc since Go 1.10), and two of its gaps are load-bearing here:
//
//   - It cannot add MAIL FROM parameters, so BODY=8BITMIME (RFC 6152) and
//     SIZE (RFC 1870) are unsendable through it — and rule 4 of this epic
//     requires 8BITMIME when the server offers it, because the assembled
//     drafts are UTF-8 and downgrading them to 7bit at the client would be a
//     silent re-encode of bytes the \Sent copy must match.
//   - Its Data() API hides WHICH reply failed behind a generic error, while
//     the outbox's whole crash model hangs on knowing exactly when the 250 to
//     end-of-DATA was read (doc.go rule 1). This client reads that reply
//     itself and invokes the acceptance callback between the read and QUIT,
//     making the persist-before-anything ordering structural.
//
// What is deliberately NOT implemented, each with its reason:
//
//   - PIPELINING: the submission path sends one message per connection to a
//     server one Docker hop away; saving round trips is not worth blurring
//     which command a reply belongs to.
//   - SMTPUTF8 (RFC 6531): the JMAP layer rejects non-ASCII addresses at
//     submission create (invalidRecipients), so an EAI envelope can never
//     reach this client. Advertised-capability plumbing without a producer
//     would be dead code.
//   - CRAM-MD5/LOGIN auth: AUTH PLAIN over TLS is what Postfix submission
//     accepts and what the app-password model needs; weaker mechanisms add
//     surface, not capability.

// Config configures one submission connection.
type Config struct {
	// Host and Port name the submission server — in the Moov deployment the
	// Mailcow Postfix container, "postfix":587 (ADR §4).
	Host string
	Port int

	// HeloName is the EHLO argument. Default "moov".
	HeloName string

	// TLSServerName is the name the server certificate is verified against,
	// which legitimately differs from Host: Moov dials the container alias
	// while the certificate carries the public mail hostname (the same S1 H2
	// split the IMAP side handles).
	TLSServerName string

	// InsecureSkipVerify disables certificate verification. DEVELOPMENT ONLY —
	// the same contract, wording and warning as internal/imap's Config.
	InsecureSkipVerify bool

	// DisableTLS skips STARTTLS entirely and permits AUTH over cleartext.
	// TEST ONLY: it exists so the unit suite can run a plain in-process fake
	// server. Production paths never set it, and Send refuses AUTH without
	// TLS unless it is set.
	DisableTLS bool

	// Username and Password authenticate via AUTH PLAIN (the account's
	// Mailcow app password, scope smtp).
	Username string
	Password string

	// DialTimeout bounds establishing the TCP connection. Default 15s.
	DialTimeout time.Duration

	// CommandTimeout bounds one command/response exchange. Default 2m.
	CommandTimeout time.Duration

	// DataTimeout bounds streaming the message body plus the final reply.
	// Default 10m — RFC 5321 §4.5.3.2.6's own minimum for the DATA
	// termination wait.
	DataTimeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.HeloName == "" {
		c.HeloName = "moov"
	}
	if c.Port == 0 {
		c.Port = 587
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = 15 * time.Second
	}
	if c.CommandTimeout <= 0 {
		c.CommandTimeout = 2 * time.Minute
	}
	if c.DataTimeout <= 0 {
		c.DataTimeout = 10 * time.Minute
	}
	return c
}

// Envelope is the SMTP envelope: what MAIL FROM and RCPT TO say, independent
// of the message's headers (RFC 5321 §2.3.1 makes the two distinct on
// purpose, and Bcc lives in exactly that distinction).
type Envelope struct {
	MailFrom string
	RcptTo   []string

	// Size, when > 0, is the transmitted message's byte count, declared via
	// the SIZE parameter (RFC 1870) when the server offers it — which lets an
	// oversized message be refused before a single body byte is sent.
	Size int64
}

// Result reports an accepted transmission.
type Result struct {
	// Reply is the server's 250 line to the end of DATA, kept verbatim — it
	// carries Postfix's queue id and becomes deliveryStatus.smtpReply
	// (RFC 8621 §7.1).
	Reply string

	// EightBitMIME reports whether BODY=8BITMIME was negotiated.
	EightBitMIME bool
}

// The protocol phases, for error classification and logs.
const (
	PhaseConnect   = "connect"
	PhaseEHLO      = "ehlo"
	PhaseStartTLS  = "starttls"
	PhaseAuth      = "auth"
	PhaseMail      = "mail"
	PhaseRcpt      = "rcpt"
	PhaseData      = "data"
	PhaseDataClose = "data-close"
)

// Error is one classified SMTP failure.
//
// Classification is the outbox's retry policy input, so it follows RFC 5321
// §4.2.1 exactly: a 5yz reply is a Permanent Negative Completion (never
// retried), a 4yz is a Transient Negative Completion (retried with backoff),
// and anything without a code — a network error, a timeout — is transient by
// definition: the server said nothing final.
type Error struct {
	Phase string
	// Code is the SMTP reply code, 0 when the failure was not an SMTP reply.
	Code int
	Msg  string
	Err  error
}

func (e *Error) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("submit: %s: %d %s", e.Phase, e.Code, e.Msg)
	}
	return fmt.Sprintf("submit: %s: %v", e.Phase, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// Permanent reports a 5yz refusal.
func (e *Error) Permanent() bool { return e.Code >= 500 && e.Code < 600 }

// IsPermanent reports whether err is a permanent SMTP refusal. Everything
// else — transient replies, network errors, timeouts — is retryable.
func IsPermanent(err error) bool {
	var se *Error
	return errors.As(err, &se) && se.Permanent()
}

// ErrAcceptedUnrecorded means the server accepted the message (the 250 was
// read) but the caller's onAccepted callback failed — the message IS SENT and
// the caller must treat it as such. See doc.go's residual-window analysis.
var ErrAcceptedUnrecorded = errors.New("submit: message accepted by the server but the acceptance callback failed")

// AcceptedUnrecordedError carries the reply of an acceptance whose recording
// failed, so the caller can keep retrying the recording with the real reply.
type AcceptedUnrecordedError struct {
	Reply string
	Err   error
}

func (e *AcceptedUnrecordedError) Error() string {
	return fmt.Sprintf("%v: %v", ErrAcceptedUnrecorded, e.Err)
}
func (e *AcceptedUnrecordedError) Unwrap() error { return ErrAcceptedUnrecorded }

// Send transmits one message and returns after the server's final DATA reply.
//
// onAccepted, when non-nil, is invoked the moment the 250 to end-of-DATA is
// read — after the acceptance, before QUIT, before returning. It is the
// persistence hook of doc.go rule 1; an error from it does NOT un-send the
// message, so Send then returns an *AcceptedUnrecordedError and the caller
// must never re-transmit.
//
// The recipient policy is all-or-nothing: if ANY recipient is refused, the
// transaction is aborted with RSET before DATA and nothing is sent. A partial
// send cannot be retried without duplicating to the recipients that were
// accepted, so it is never begun — the strictest reading that keeps the
// outbox's single-execution guarantee meaningful per message rather than per
// recipient. The refusal classifies as the worst refusal seen (permanent
// beats transient), so one permanently bad address fails the submission
// visibly instead of looping.
func Send(ctx context.Context, cfg Config, env Envelope, msg io.Reader, onAccepted func(reply string) error) (Result, error) {
	cfg = cfg.withDefaults()
	var out Result

	if env.MailFrom == "" {
		return out, &Error{Phase: PhaseMail, Err: errors.New("empty MAIL FROM")}
	}
	if len(env.RcptTo) == 0 {
		return out, &Error{Phase: PhaseRcpt, Err: errors.New("no recipients")}
	}

	s, err := open(ctx, cfg)
	if err != nil {
		return out, err
	}
	defer s.close()

	if err := s.hello(); err != nil {
		return out, err
	}
	if err := s.authenticate(); err != nil {
		return out, err
	}

	eightBit := s.has("8BITMIME")
	if err := s.mail(env, eightBit); err != nil {
		return out, err
	}
	if err := s.rcpt(env.RcptTo); err != nil {
		return out, err
	}

	reply, err := s.data(msg)
	if err != nil {
		return out, err
	}

	// THE ordering of rule 1: the acceptance reaches the caller's storage
	// before this function does anything else at all — QUIT included, because
	// a hang or crash inside QUIT must not be able to lose the fact.
	out = Result{Reply: reply, EightBitMIME: eightBit}
	if onAccepted != nil {
		if cbErr := onAccepted(reply); cbErr != nil {
			s.quit()
			return out, &AcceptedUnrecordedError{Reply: reply, Err: cbErr}
		}
	}
	s.quit()
	return out, nil
}

// ---------------------------------------------------------------------------
// the session
// ---------------------------------------------------------------------------

// session is one connection's protocol state.
type session struct {
	cfg  Config
	conn net.Conn
	text *textproto.Conn

	// ext holds the EHLO keywords, uppercased, keyword -> parameter string.
	ext map[string]string

	// stop cancels the ctx watchdog.
	stop func()
}

// open dials and reads the greeting.
func open(ctx context.Context, cfg Config) (*session, error) {
	dialer := &net.Dialer{Timeout: cfg.DialTimeout}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, &Error{Phase: PhaseConnect, Err: err}
	}

	s := &session{cfg: cfg, conn: conn, text: textproto.NewConn(conn)}

	// The watchdog: net.Conn deadlines bound each exchange, but only closing
	// the socket unblocks a read when the CALLER's context ends first. Stopped
	// on close; the connection outliving Send is impossible either way.
	watchCtx, stop := context.WithCancel(ctx)
	s.stop = stop
	go func() {
		<-watchCtx.Done()
		if ctx.Err() != nil {
			_ = conn.Close()
		}
	}()

	if _, _, err := s.read(PhaseConnect, 220); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

// hello runs EHLO, negotiates STARTTLS, and re-runs EHLO on the secured
// channel (RFC 3207 §4.2: "The client MUST discard any knowledge obtained
// from the server ... prior to the TLS negotiation").
func (s *session) hello() error {
	if err := s.ehlo(); err != nil {
		return err
	}
	if s.cfg.DisableTLS {
		return nil
	}
	if !s.has("STARTTLS") {
		return &Error{Phase: PhaseStartTLS, Err: errors.New("server does not offer STARTTLS; refusing to authenticate in cleartext")}
	}
	if err := s.cmd(PhaseStartTLS, 220, "STARTTLS"); err != nil {
		return err
	}

	serverName := s.cfg.TLSServerName
	if serverName == "" {
		serverName = s.cfg.Host
	}
	tlsConn := tls.Client(s.conn, &tls.Config{
		ServerName: serverName,
		MinVersion: tls.VersionTLS12,
		// #nosec G402 -- honoring the documented development-only escape hatch,
		// same contract as internal/imap's Config.InsecureSkipVerify.
		InsecureSkipVerify: s.cfg.InsecureSkipVerify,
	})
	if err := s.deadline(s.cfg.CommandTimeout); err != nil {
		return &Error{Phase: PhaseStartTLS, Err: err}
	}
	if err := tlsConn.Handshake(); err != nil {
		return &Error{Phase: PhaseStartTLS, Err: err}
	}
	s.conn = tlsConn
	s.text = textproto.NewConn(tlsConn)
	return s.ehlo()
}

// ehlo issues EHLO and records the extension keywords.
func (s *session) ehlo() error {
	if err := s.deadline(s.cfg.CommandTimeout); err != nil {
		return &Error{Phase: PhaseEHLO, Err: err}
	}
	id, err := s.text.Cmd("EHLO %s", s.cfg.HeloName)
	if err != nil {
		return &Error{Phase: PhaseEHLO, Err: err}
	}
	s.text.StartResponse(id)
	defer s.text.EndResponse(id)
	code, msg, err := s.text.ReadResponse(250)
	if err != nil {
		return smtpError(PhaseEHLO, code, msg, err)
	}

	s.ext = map[string]string{}
	lines := strings.Split(msg, "\n")
	for _, line := range lines[1:] { // line 0 is the server's greeting text
		keyword, param, _ := strings.Cut(strings.TrimSpace(line), " ")
		if keyword != "" {
			s.ext[strings.ToUpper(keyword)] = param
		}
	}
	return nil
}

// has reports whether the server advertised an EHLO keyword.
func (s *session) has(keyword string) bool {
	_, ok := s.ext[strings.ToUpper(keyword)]
	return ok
}

// authenticate runs AUTH PLAIN (RFC 4616). Refused off-TLS unless the
// test-only DisableTLS is set: an app password on a cleartext socket is a
// credential leak, not a degraded mode.
func (s *session) authenticate() error {
	if s.cfg.Username == "" && s.cfg.Password == "" {
		return nil
	}
	if _, isTLS := s.conn.(*tls.Conn); !isTLS && !s.cfg.DisableTLS {
		return &Error{Phase: PhaseAuth, Err: errors.New("refusing AUTH on an unencrypted connection")}
	}
	ir := base64.StdEncoding.EncodeToString([]byte("\x00" + s.cfg.Username + "\x00" + s.cfg.Password))
	return s.cmd(PhaseAuth, 235, "AUTH PLAIN %s", ir)
}

// mail issues MAIL FROM with the negotiated parameters.
func (s *session) mail(env Envelope, eightBit bool) error {
	var params strings.Builder
	if eightBit {
		// RFC 6152 §3: declare the body form when the extension is offered.
		params.WriteString(" BODY=8BITMIME")
	}
	if env.Size > 0 {
		if raw, ok := s.ext["SIZE"]; ok {
			// RFC 1870 §6.1: a client that knows the size and the server's
			// limit refuses locally rather than wasting the transfer.
			if limit, err := strconv.ParseInt(raw, 10, 64); err == nil && limit > 0 && env.Size > limit {
				return &Error{Phase: PhaseMail, Code: 552,
					Msg: fmt.Sprintf("message is %d bytes; the server's declared SIZE limit is %d", env.Size, limit)}
			}
			fmt.Fprintf(&params, " SIZE=%d", env.Size)
		}
	}
	return s.cmd(PhaseMail, 250, "MAIL FROM:<%s>%s", env.MailFrom, params.String())
}

// rcpt issues every RCPT TO, aborting all-or-nothing on any refusal (see
// Send's doc for why partial delivery is never begun).
func (s *session) rcpt(rcptTo []string) error {
	var worst *Error
	for _, rcpt := range rcptTo {
		// 250 and 251 ("will forward") are both acceptance (RFC 5321 §4.2.3).
		err := s.cmd(PhaseRcpt, 25, "RCPT TO:<%s>", rcpt)
		if err == nil {
			continue
		}
		var se *Error
		if !errors.As(err, &se) {
			return err
		}
		se.Msg = fmt.Sprintf("recipient <%s>: %s", rcpt, se.Msg)
		if worst == nil || (se.Permanent() && !worst.Permanent()) {
			worst = se
		}
	}
	if worst != nil {
		// RSET so the server's state is clean before QUIT; best-effort — the
		// abort is decided regardless of whether the RSET lands.
		_ = s.cmd(PhaseRcpt, 250, "RSET")
		return worst
	}
	return nil
}

// data streams the message and returns the server's final 250 reply verbatim.
func (s *session) data(msg io.Reader) (string, error) {
	if err := s.cmd(PhaseData, 354, "DATA"); err != nil {
		return "", err
	}
	if err := s.deadline(s.cfg.DataTimeout); err != nil {
		return "", &Error{Phase: PhaseData, Err: err}
	}

	// DotWriter performs the RFC 5321 §4.5.2 dot-stuffing and the CRLF.CRLF
	// termination; Close flushes the terminator.
	w := s.text.DotWriter()
	if _, err := io.Copy(w, msg); err != nil {
		_ = w.Close()
		return "", &Error{Phase: PhaseData, Err: err}
	}
	if err := w.Close(); err != nil {
		return "", &Error{Phase: PhaseData, Err: err}
	}

	code, reply, err := s.text.ReadResponse(250)
	if err != nil {
		return "", smtpError(PhaseDataClose, code, reply, err)
	}
	return fmt.Sprintf("%d %s", code, reply), nil
}

// cmd sends one command and checks its reply against the expected code (a
// 2-digit expectCode matches the class-and-subject prefix, textproto's own
// convention).
func (s *session) cmd(phase string, expectCode int, format string, args ...any) error {
	if err := s.deadline(s.cfg.CommandTimeout); err != nil {
		return &Error{Phase: phase, Err: err}
	}
	id, err := s.text.Cmd(format, args...)
	if err != nil {
		return &Error{Phase: phase, Err: err}
	}
	s.text.StartResponse(id)
	defer s.text.EndResponse(id)
	if code, msg, err := s.text.ReadResponse(expectCode); err != nil {
		return smtpError(phase, code, msg, err)
	}
	return nil
}

// read reads one unsolicited reply (the greeting).
func (s *session) read(phase string, expectCode int) (int, string, error) {
	if err := s.deadline(s.cfg.CommandTimeout); err != nil {
		return 0, "", &Error{Phase: phase, Err: err}
	}
	code, msg, err := s.text.ReadResponse(expectCode)
	if err != nil {
		return code, msg, smtpError(phase, code, msg, err)
	}
	return code, msg, nil
}

// quit ends the session politely, best-effort: the transaction's outcome is
// already decided and persisted by the time this runs.
func (s *session) quit() {
	_ = s.deadline(5 * time.Second)
	if id, err := s.text.Cmd("QUIT"); err == nil {
		s.text.StartResponse(id)
		_, _, _ = s.text.ReadResponse(221)
		s.text.EndResponse(id)
	}
}

func (s *session) close() {
	if s.stop != nil {
		s.stop()
	}
	_ = s.conn.Close()
}

func (s *session) deadline(d time.Duration) error {
	return s.conn.SetDeadline(time.Now().Add(d))
}

// smtpError classifies one failed exchange.
func smtpError(phase string, code int, msg string, err error) error {
	var te *textproto.Error
	if errors.As(err, &te) {
		return &Error{Phase: phase, Code: te.Code, Msg: te.Msg, Err: err}
	}
	if code != 0 {
		return &Error{Phase: phase, Code: code, Msg: msg, Err: err}
	}
	return &Error{Phase: phase, Err: err}
}
