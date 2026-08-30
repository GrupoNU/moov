package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/GrupoNU/moov/internal/blob"
	"github.com/GrupoNU/moov/internal/config"
	"github.com/GrupoNU/moov/internal/crypto"
	"github.com/GrupoNU/moov/internal/jmap/mail"
	"github.com/GrupoNU/moov/internal/jmaphttp"
	"github.com/GrupoNU/moov/internal/metrics"
	"github.com/GrupoNU/moov/internal/sieve"
	"github.com/GrupoNU/moov/internal/store"
	"github.com/GrupoNU/moov/internal/submit"
)

// E6's daemon-side wiring: the ManageSieve dialer (the sieve twin of
// accountDialer.connect — the SAME app password opens both, ADR §4), the
// forwarding verification tokens over the EXISTING master keyring, and the
// verification mailer over the outbox's own SMTP transport.

// sieveDialer opens per-account ManageSieve connections.
type sieveDialer struct {
	inner      *accountDialer
	host       string
	port       int
	serverName string
	logger     *slog.Logger
}

// connect implements mail.SieveConnector.
func (d *sieveDialer) connect(ctx context.Context, account store.Account) (sieve.Client, error) {
	password, err := d.inner.password(account)
	if err != nil {
		return nil, fmt.Errorf("account %d: %w", account.ID, err)
	}
	serverName := d.serverName
	if serverName == "" {
		serverName = account.IMAPServerName
	}
	c := sieve.New(d.logger)
	err = c.Connect(ctx, sieve.Config{
		Host:          d.host,
		Port:          d.port,
		Username:      account.IMAPUsername,
		Password:      password,
		TLSServerName: serverName,
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// forwardingTokens implements mail.ForwardingTokens over the master keyring:
// the token IS a keyring envelope (AES-256-GCM) over "email|expiryUnix",
// bound to the requesting account by AAD — the GC-4 instruction to reuse the
// existing secret infrastructure rather than mint a new scheme. The GCM tag
// is the authenticator; the envelope's key id makes rotation Just Work; and
// unlike the per-process HTTP tokens this survives a restart, which a mail
// that may be read days later requires.
type forwardingTokens struct {
	keyring *crypto.Keyring
}

// forwardingAAD binds a token to its account, the same shape AccountAAD uses
// for credentials (a distinct prefix so the two spaces can never collide).
func forwardingAAD(accountID int64) []byte {
	return []byte("moov:fwdverify:" + strconv.FormatInt(accountID, 10))
}

// Mint implements mail.ForwardingTokens.
func (t *forwardingTokens) Mint(accountID int64, email string, expires time.Time) (string, error) {
	payload := email + "|" + strconv.FormatInt(expires.Unix(), 10)
	sealed, err := t.keyring.Seal([]byte(payload), forwardingAAD(accountID))
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Verify implements mail.ForwardingTokens. Every failure is the same
// failure (the adapter maps to its no-oracle sentinel).
func (t *forwardingTokens) Verify(accountID int64, token string) (string, error) {
	sealed, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", fmt.Errorf("undecodable token")
	}
	payload, err := t.keyring.Open(sealed, forwardingAAD(accountID))
	if err != nil {
		return "", fmt.Errorf("unverifiable token")
	}
	email, expiryRaw, ok := strings.Cut(string(payload), "|")
	if !ok {
		return "", fmt.Errorf("malformed token payload")
	}
	expiry, err := strconv.ParseInt(expiryRaw, 10, 64)
	if err != nil || time.Now().Unix() > expiry {
		return "", fmt.Errorf("expired token")
	}
	return email, nil
}

// verificationMailer implements mail.VerificationMailer over the SAME SMTP
// transport the outbox uses: same per-account credential, same Postfix path.
//
// Synchronous by design, and a recorded deviation from "through the outbox"
// read literally: the outbox's claim scan is kind='send' with draft-backed
// semantics and a crash-recovery contract built around accepted_at — adding
// a second intent kind would touch exactly the invariants W3 calls sacred,
// for a mail whose failure the settings UI should surface IMMEDIATELY (the
// user just clicked "add address"; a background retry with no visible error
// is the worse UX and the worse audit trail). Same sending path, no queue.
type verificationMailer struct {
	store     *store.Store
	transport submit.Transport
	timeout   time.Duration
}

// SendVerification implements mail.VerificationMailer.
func (m *verificationMailer) SendVerification(ctx context.Context, accountID int64, to, token string, expires time.Time) error {
	account, err := m.store.GetAccount(ctx, accountID)
	if err != nil {
		return fmt.Errorf("loading account %d: %w", accountID, err)
	}
	raw, err := verificationMessage(account.Email, to, token, expires)
	if err != nil {
		return err
	}
	sendCtx := ctx
	if m.timeout > 0 {
		var cancel context.CancelFunc
		sendCtx, cancel = context.WithTimeout(ctx, m.timeout)
		defer cancel()
	}
	env := submit.Envelope{MailFrom: account.Email, RcptTo: []string{to}, Size: int64(len(raw))}
	_, err = m.transport.Send(sendCtx, account, env, bytes.NewReader(raw), func(string) error { return nil })
	if err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	return nil
}

// verificationMessage assembles the verification mail: plain text, the code
// prominent, honest about who asked and until when it works. English on
// purpose — the recipient is an ARBITRARY external mailbox whose language
// the server cannot know; i18n of transactional mail is a deferred item by
// name.
func verificationMessage(from, to, token string, expires time.Time) ([]byte, error) {
	var idRaw [8]byte
	if _, err := rand.Read(idRaw[:]); err != nil {
		return nil, fmt.Errorf("generating a Message-ID: %w", err)
	}
	domain := from
	if at := strings.LastIndexByte(from, '@'); at >= 0 {
		domain = from[at+1:]
	}
	var b bytes.Buffer
	write := func(s string) { b.WriteString(s + "\r\n") }
	write("From: <" + from + ">")
	write("To: <" + to + ">")
	write("Subject: Mail forwarding confirmation for " + from)
	write("Date: " + time.Now().UTC().Format(time.RFC1123Z))
	write("Message-ID: <moov-fwd-" + hex.EncodeToString(idRaw[:]) + "@" + domain + ">")
	// Never answered by vacation responders (RFC 3834); never a candidate
	// for a reply chain.
	write("Auto-Submitted: auto-generated")
	write("MIME-Version: 1.0")
	write("Content-Type: text/plain; charset=utf-8")
	write("")
	write("The owner of " + from + " asked to forward their mail to this address.")
	write("")
	write("If you agree, give them this confirmation code so they can enter it in")
	write("their mail settings:")
	write("")
	write("    " + token)
	write("")
	write("The code works until " + expires.UTC().Format("2006-01-02 15:04 MST") + " and only in")
	write("the settings of " + from + ".")
	write("")
	write("If you do not know who this is, ignore this message: without the code,")
	write("no mail will be forwarded here.")
	return b.Bytes(), nil
}

// sieveMetrics adapts the metric set to mail.SieveObserver.
type sieveMetrics struct{ m *metrics.Metrics }

func (s sieveMetrics) ScriptPushed(result string) {
	if s.m != nil {
		s.m.IncSievePush(result)
	}
}

func (s sieveMetrics) VerificationMailSent(result string) {
	if s.m != nil {
		s.m.IncVerificationMail(result)
	}
}

func (s sieveMetrics) VacationConfigured(enabled bool) {
	if s.m != nil {
		s.m.IncVacationUpdate(enabled)
	}
}

// sieveProbeTimeout bounds the startup capability probe.
const sieveProbeTimeout = 10 * time.Second

// probeSieveCapability reads the live ManageSieve capabilities for the
// session advertisement. A failure returns nil: the deployment serves mail
// WITHOUT the sieve capabilities rather than advertising extension lists it
// could not read — logged loudly, because filters silently missing is a
// support ticket.
func probeSieveCapability(ctx context.Context, cfg config.JMAPConfig, logger *slog.Logger) *jmaphttp.SieveCapability {
	probeCtx, cancel := context.WithTimeout(ctx, sieveProbeTimeout)
	defer cancel()
	caps, err := sieve.Probe(probeCtx, sieve.Config{
		Host:          cfg.SieveHost,
		Port:          cfg.SievePort,
		TLSServerName: cfg.IMAPServerName,
	})
	if err != nil {
		logger.Error("managesieve capability probe failed; the sieve/vacation/filter/quota "+
			"capabilities will NOT be served this run",
			"host", cfg.SieveHost, "port", cfg.SievePort, "error", err)
		return nil
	}
	sc := &jmaphttp.SieveCapability{
		Extensions:          caps.Extensions,
		NotificationMethods: caps.Notify,
	}
	if caps.HasMaxRedirects {
		v := caps.MaxRedirects
		sc.MaxRedirects = &v
	}
	logger.Info("managesieve capabilities probed",
		"extensions", len(caps.Extensions), "implementation", caps.Implementation)
	return sc
}

// buildSieveSurfaces wires the E6 adapter and installs it into deps.
func buildSieveSurfaces(cfg config.Config, st *store.Store, blobs *blob.Store, deps *mail.Deps,
	dialer *accountDialer, keyring *crypto.Keyring, transport submit.Transport,
	broker mail.SubmissionNotifier, m *metrics.Metrics, logger *slog.Logger) (*mail.SieveAdapter, error) {

	sd := &sieveDialer{
		inner:      dialer,
		host:       cfg.JMAP.SieveHost,
		port:       cfg.JMAP.SievePort,
		serverName: cfg.JMAP.IMAPServerName,
		logger:     logger,
	}
	adapter, err := mail.NewSieveAdapter(mail.SieveAdapterConfig{
		Store:    st,
		Blobs:    blobs,
		Connect:  sd.connect,
		Tokens:   &forwardingTokens{keyring: keyring},
		Mailer:   &verificationMailer{store: st, transport: transport, timeout: 30 * time.Second},
		Notifier: broker,
		Observer: sieveMetrics{m},
		Logger:   logger,
	})
	if err != nil {
		return nil, fmt.Errorf("building the sieve adapter: %w", err)
	}
	deps.Sieve = adapter
	deps.Vacation = adapter
	deps.Filters = adapter
	deps.Forwarding = adapter
	return adapter, nil
}
