package imap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// client is the go-imap-backed implementation of Client.
//
// It owns exactly one connection. Everything about its design assumes a single
// owning goroutine (see the Client doc); the mutex here protects the small
// amount of state that Close and the unilateral-data callbacks touch from
// another goroutine, not the command stream.
type client struct {
	cfg Config
	log *slog.Logger

	// mu guards the fields below. It is never held across a network call.
	mu       sync.Mutex
	c        *imapclient.Client
	caps     Capabilities
	selected string
	closed   bool

	// watch is the live watcher's state, non-nil only while Watch runs.
	watch *watchState

	// uni is the target of the unilateral-data callbacks; see unilateral.go.
	uni unilateralState
}

// timeNow is time.Now, indirected so tests can pin timestamps.
var timeNow = time.Now

// New returns a Client that is not yet connected. Call Connect before anything
// else.
//
// logger may be nil, in which case slog's default is used. The logger is
// expected to be scoped to an account by the caller; this package never logs
// the password, and logs the username only at debug level.
func New(logger *slog.Logger) Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &client{log: logger}
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
		// Loud on purpose. If this ever appears in a production log, the
		// deployment is handing its app password to whatever answers the
		// socket. It is a warning rather than an error because the field
		// exists for a legitimate development case.
		cl.log.Warn("imap: TLS certificate verification is DISABLED for this connection; "+
			"never use InsecureSkipVerify outside development",
			"host", cfg.Host, "port", cfg.Port)
	}

	dialer := &net.Dialer{Timeout: cfg.DialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", cfg.Address())
	if err != nil {
		return fmt.Errorf("imap: dialing %s: %w", cfg.Address(), err)
	}

	// The connection is handed to go-imap below; until it is, this owns it.
	handedOver := false
	defer func() {
		if !handedOver {
			_ = conn.Close()
		}
	}()

	opts := &imapclient.Options{
		TLSConfig:             tlsCfg,
		UnilateralDataHandler: cl.unilateralHandler(),
	}

	// NewStartTLS negotiates STARTTLS on the already-dialed connection, which
	// is what lets the dial honor ctx. DialStartTLS would do its own dial
	// with its own timeout and ignore the context.
	gc, err := imapclient.NewStartTLS(conn, opts)
	if err != nil {
		return fmt.Errorf("imap: STARTTLS to %s: %w", cfg.Address(), err)
	}
	handedOver = true

	ok := false
	defer func() {
		if !ok {
			_ = gc.Close()
		}
	}()

	if err := gc.Login(cfg.Username, cfg.Password).Wait(); err != nil {
		return fmt.Errorf("imap: login failed for %s: %w", cfg.Username, redactErr(err))
	}

	// Capabilities MUST be read after login: Dovecot does not advertise
	// NOTIFY (among others) to an unauthenticated connection (S2 T2a).
	// go-imap re-requests them automatically after LOGIN invalidates its
	// cache, so Caps() here is the post-login set.
	caps := capsFromGoIMAP(gc.Caps())

	var missing []string
	for _, want := range requiredCapabilities {
		if !caps.Has(want) {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		return &MissingCapabilityError{Missing: missing}
	}

	// ENABLE QRESYNC. RFC 7162 says enabling QRESYNC implicitly enables
	// CONDSTORE, so one call covers both. Stock go-imap refuses this with a
	// client-side allowlist (S2 T2e); patch 0001 is what makes it reach the
	// wire, so a failure here is the first symptom of an unpatched vendor
	// tree.
	if _, err := gc.Enable(goimap.CapQResync).Wait(); err != nil {
		return fmt.Errorf("imap: ENABLE QRESYNC (is the go-imap patch set applied? see patches/README.md): %w", err)
	}

	// Identify Moov in Dovecot's logs. Best-effort: a server that dislikes ID
	// is no reason to fail the connection.
	if caps.Has("id") {
		if _, err := gc.ID(&goimap.IDData{Name: cfg.ClientName}).Wait(); err != nil {
			cl.log.Debug("imap: ID command failed, continuing", "error", err)
		}
	}

	cl.mu.Lock()
	cl.cfg = cfg
	cl.c = gc
	cl.caps = caps
	cl.closed = false
	cl.selected = ""
	cl.mu.Unlock()

	ok = true
	cl.log.Debug("imap: connected",
		"host", cfg.Host, "user", cfg.Username, "capabilities", len(caps))
	return nil
}

// Capabilities implements Client.
func (cl *client) Capabilities() Capabilities {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	out := make(Capabilities, len(cl.caps))
	for k, v := range cl.caps {
		out[k] = v
	}
	return out
}

// Close implements Client.
func (cl *client) Close() error {
	cl.mu.Lock()
	gc := cl.c
	w := cl.watch
	already := cl.closed
	cl.closed = true
	cl.c = nil
	cl.mu.Unlock()

	// Stop a running watch first, so its goroutine leaves IDLE and stops using
	// the connection before it is torn out from under it. This is the one
	// cross-goroutine call the Client contract allows.
	if w != nil {
		w.finish(nil)
		<-w.doneClosing()
	}

	if already || gc == nil {
		return nil
	}

	// Logout is best-effort: the connection is going away either way, and a
	// server that has already dropped it would only produce noise.
	if err := gc.Logout().Wait(); err != nil {
		cl.log.Debug("imap: LOGOUT failed, closing anyway", "error", err)
	} else {
		// Wait for go-imap's decoder to finish on the server's BYE before
		// tearing the socket down.
		//
		// Without this, Close() races the decoder: it shuts the connection
		// mid-read, the decoder's parse fails on the truncated input, and
		// Close returns that parse error. Dovecot's LOGOUT is textbook
		// correct — "* BYE Logging out" then the tagged OK, verified on the
		// wire — so the error is purely an artifact of the teardown order,
		// and returning it would make every clean disconnect look like a
		// protocol failure in the logs.
		select {
		case <-gc.Closed():
		case <-time.After(logoutDrainTimeout):
			cl.log.Debug("imap: server did not close the connection after LOGOUT")
		}
	}

	if err := gc.Close(); err != nil && !isBenignCloseError(err) {
		return err
	}
	return nil
}

// logoutDrainTimeout bounds the wait for the server to close the connection
// after LOGOUT. Short: the connection is already finished, and a server that
// does not hang up promptly is not worth waiting for.
const logoutDrainTimeout = 2 * time.Second

// isBenignCloseError reports errors that mean "the connection was already
// going away", which is the expected state after a completed LOGOUT.
func isBenignCloseError(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF)
}

// conn returns the live connection, or ErrNotConnected.
func (cl *client) conn() (*imapclient.Client, error) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.c == nil || cl.closed {
		return nil, ErrNotConnected
	}
	return cl.c, nil
}

// selectedMailbox returns the currently selected mailbox name, or an error.
func (cl *client) selectedMailbox() (string, error) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.c == nil || cl.closed {
		return "", ErrNotConnected
	}
	if cl.selected == "" {
		return "", ErrNoMailboxSelected
	}
	return cl.selected, nil
}

// requireCap returns ErrMissingCapability unless the server advertises name.
func (cl *client) requireCap(name string) error {
	cl.mu.Lock()
	has := cl.caps.Has(name)
	cl.mu.Unlock()
	if !has {
		return &MissingCapabilityError{Missing: []string{name}}
	}
	return nil
}

// redactErr strips anything that might carry credentials out of an error.
//
// go-imap includes the server's response text in its errors; a server that
// echoes part of the command in a BAD response would put the password in a log
// line. The repository is public and the logs are not, but the cheapest place
// to be sure is here.
func redactErr(err error) error {
	var imapErr *goimap.Error
	if errors.As(err, &imapErr) {
		return fmt.Errorf("%s %s", imapErr.Type, imapErr.Code)
	}
	return err
}
