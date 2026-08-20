package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"

	"github.com/GrupoNU/moov/internal/blob"
	"github.com/GrupoNU/moov/internal/config"
	"github.com/GrupoNU/moov/internal/crypto"
	"github.com/GrupoNU/moov/internal/jmap"
	"github.com/GrupoNU/moov/internal/jmap/mail"
	"github.com/GrupoNU/moov/internal/jmaphttp"
	"github.com/GrupoNU/moov/internal/metrics"
	"github.com/GrupoNU/moov/internal/store"
	"github.com/GrupoNU/moov/internal/submit"
	syncengine "github.com/GrupoNU/moov/internal/sync"
)

// The JMAP server's wiring (J1, L2-jmap-server §2.1): same daemon as the sync
// engine, its own listener, opt-in via MOOV_JMAP_ENABLED.

// jmapComponents holds what the JMAP server needs, so shutdown can release it
// in one place.
type jmapComponents struct {
	store  *store.Store
	server *http.Server
	ln     net.Listener
	writer *syncengine.WriteExecutor
	outbox *outboxComponent
	log    *slog.Logger
}

// startJMAP builds and starts the JMAP HTTP server, or returns nil when it is
// not enabled — the same "disabled is a normal state, not an error" contract
// startSync follows.
//
// ctx bounds startup work (opening the store, binding the listener). fatal is
// invoked if the running server later fails on its own; the daemon treats
// that as a reason to shut down rather than limping on without its API.
//
// The JMAP server opens its OWN store handle rather than sharing the sync
// engine's: the two components have independent lifecycles (either may be
// disabled) and independent pool needs. Two pgx pools against one PostgreSQL
// are cheap; consolidating them is a J4 (deploy) decision, with numbers.
func startJMAP(ctx context.Context, cfg config.Config, logger *slog.Logger, m *metrics.Metrics, broker *syncengine.Broker, fatal func(error)) (*jmapComponents, error) {
	if !cfg.JMAP.Enabled {
		logger.Info("jmap server disabled", "hint", "MOOV_JMAP_ENABLED=1 enables it")
		return nil, nil //nolint:nilnil // "disabled" is a valid, non-error outcome
	}

	st, err := store.Open(ctx, store.Config{DSN: cfg.DatabaseURL})
	if err != nil {
		return nil, fmt.Errorf("opening store for jmap: %w", err)
	}

	auth, err := jmaphttp.NewAuthenticator(jmaphttp.AuthConfig{
		Validator: &jmaphttp.IMAPLoginValidator{
			Host:          cfg.JMAP.IMAPHost,
			Port:          cfg.JMAP.IMAPPort,
			TLSServerName: cfg.JMAP.IMAPServerName,
			Logger:        logger,
		},
		Directory: st,
		CacheTTL:  cfg.JMAP.AuthCacheTTL,
		Logger:    logger,
	})
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("building jmap authenticator: %w", err)
	}

	// The blob store backs both the download endpoint and Email/get's
	// bodyValues, which re-parse the raw message on demand (L2 §5 risk 2).
	blobs, err := blob.New(blob.Config{Root: cfg.Sync.BlobRoot, Pool: st.Pool()})
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("opening blob store for jmap: %w", err)
	}

	limits := jmap.DefaultLimits()
	deps, err := mail.NewDeps(st, blobs, limits)
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("building jmap mail dependencies: %w", err)
	}

	// The write path (W1). Email/set applies to Dovecot through the sync
	// engine's write executor, which needs the credential keyring to open
	// per-account IMAP connections — the same fail-fast rule as startSync:
	// a JMAP server that advertises writes it cannot perform would be lying
	// to every client, so a missing keyring stops the daemon at startup with
	// the real cause instead of failing every /set at runtime.
	keyring, err := crypto.LoadKeyring()
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("loading the master keyring for jmap writes: %w", err)
	}
	dialer := &accountDialer{keyring: keyring, serverName: cfg.JMAP.IMAPServerName, logger: logger}
	writer, err := syncengine.NewWriteExecutor(st, syncengine.ConnectorFunc(dialer.connect),
		// Blobs enables the append path (W3): Email/set create and the
		// outbox's \Sent copy store what they append.
		syncengine.WriteOptions{Logger: logger, Broker: broker, Blobs: blobs})
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("building the write executor: %w", err)
	}
	writerAdapter, err := mail.NewWriterAdapter(writer)
	if err != nil {
		writer.Close()
		st.Close()
		return nil, fmt.Errorf("building the writer adapter: %w", err)
	}
	// The same adapter serves every write contract: Email/set's EmailWriter
	// (W1), Mailbox/set's MailboxWriter (W2) and Email/set create's
	// EmailCreator (W3). One adapter over one executor means one IMAP
	// connection per account for every write, which is what keeps a folder
	// rename, a flag change and a draft append from racing each other on
	// separate sockets.
	deps.Writer = writerAdapter
	deps.Mailboxer = writerAdapter
	deps.Creator = writerAdapter

	// The submission surface (W3): EmailSubmission objects are outbox rows;
	// the broker rides along so an enqueue/cancel pushes an SSE StateChange
	// exactly like a flag write does.
	submissions, err := mail.NewSubmissionAdapter(st, broker)
	if err != nil {
		writer.Close()
		st.Close()
		return nil, fmt.Errorf("building the submission adapter: %w", err)
	}
	deps.Submissions = submissions
	deps.UndoWindow = cfg.Submit.UndoWindow

	// The uploader is the SAME adapter deps.Blobs is — one object serving
	// download's reads and upload's writes keeps the account-scoping rule in
	// one place. The assertion is structural: mail.Adapter implements both.
	uploader, _ := deps.Blobs.(jmaphttp.BlobUploader)

	srv, err := jmaphttp.New(jmaphttp.Config{
		BaseURL:        cfg.JMAP.ExternalURL,
		AllowedOrigins: cfg.JMAP.CORSOrigins,
		Limits:         limits,
		Logger:         logger,
		Blobs:          deps.Blobs,
		Uploader:       uploader,
		// Submission is advertised because RegisterSubmissionMethods is
		// called below — the two must move together (advertised ==
		// registered, the J1 rule).
		Submission: true,
		Metrics:    m,
		// Push (W4a): the broker says WHEN, the mail adapter says WHAT. The
		// State reader is deliberately the SAME object that answers Email/get
		// and Email/changes, which is what guarantees a pushed state string
		// equals the one a follow-up /changes call is compared against.
		Notifier:         brokerNotifier{broker},
		State:            deps.State,
		MaxSSEPerAccount: cfg.JMAP.MaxSSEPerAccount,
	}, auth)
	if err != nil {
		writer.Close()
		st.Close()
		return nil, fmt.Errorf("building jmap server: %w", err)
	}

	// The mail methods: J2's get family, J3's query/changes family, the set
	// family (W1's Email/set, W2's Mailbox/set) and W3's submission family,
	// over the same Deps. Registration must happen before Handler() is
	// mounted, which is why it sits above the http.Server construction.
	mail.RegisterGetMethods(srv.Registry(), deps)
	mail.RegisterQueryMethods(srv.Registry(), deps)
	mail.RegisterSetMethods(srv.Registry(), deps)
	mail.RegisterSubmissionMethods(srv.Registry(), deps)

	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: jmaphttp.ReadHeaderTimeout,
		ReadTimeout:       jmaphttp.ReadTimeout,
		WriteTimeout:      jmaphttp.WriteTimeout,
		IdleTimeout:       jmaphttp.IdleTimeout,
	}

	ln, err := net.Listen("tcp", cfg.JMAP.Addr)
	if err != nil {
		writer.Close()
		st.Close()
		return nil, fmt.Errorf("binding jmap listener on %s: %w", cfg.JMAP.Addr, err)
	}

	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal(fmt.Errorf("jmap server: %w", err))
		}
	}()

	// The outbox executor (W3): claims the send intents the submission
	// surface above enqueues and drives them through SMTP. It shares the JMAP
	// component's store, writer (for the \Sent copy) and adapter (for the
	// draft bytes), and its lifecycle: mounted with submission, stopped after
	// the HTTP server drains.
	raws, _ := deps.Emails.(submit.RawSource)
	outbox, err := startOutbox(cfg, st, &smtpTransport{cfg: cfg.Submit, dialer: dialer, logger: logger},
		writer, raws, broker, blobs, logger)
	if err != nil {
		_ = httpSrv.Close()
		writer.Close()
		st.Close()
		return nil, fmt.Errorf("starting the outbox: %w", err)
	}

	logger.Info("jmap server listening",
		"addr", ln.Addr().String(),
		"external_url", cfg.JMAP.ExternalURL,
		"cors_origins", len(cfg.JMAP.CORSOrigins),
		"imap_host", cfg.JMAP.IMAPHost,
	)
	return &jmapComponents{store: st, server: httpSrv, ln: ln, writer: writer, outbox: outbox, log: logger}, nil
}

// brokerNotifier adapts internal/sync's Broker to the HTTP layer's
// StateNotifier (W4a).
//
// It exists to drop the payload. The broker publishes a StateChange carrying
// an account id and a timestamp; the EventSource endpoint must NOT build its
// event from those, because RFC 8620 §7.1 defines the pushed state strings as
// "the 'state' property that would currently be returned by a call to
// 'Foo/get'" — which only the store, read at send time, can answer. Converting
// to an empty struct here makes that impossible to get wrong downstream:
// there is nothing left to misuse.
type brokerNotifier struct {
	broker *syncengine.Broker
}

func (n brokerNotifier) StateEvents(accountID int64) (<-chan jmaphttp.Notification, func()) {
	src, cancel := n.broker.StateEvents(accountID)
	out := make(chan jmaphttp.Notification, 1)

	go func() {
		// Closing out when src closes is what propagates the broker's
		// shutdown to the handler's select, so a stream ends cleanly rather
		// than hanging until its client gives up.
		defer close(out)
		for range src {
			select {
			case out <- jmaphttp.Notification{}:
			default:
				// out already holds an unread notification. Dropping this one
				// is correct and lossless in effect: the pending signal will
				// make the handler read the CURRENT state, which is the state
				// this notification would have pointed at. Same coalescing
				// argument as the broker's own one-slot mailbox.
			}
		}
	}()

	return out, cancel
}

func (n brokerNotifier) Subscribers(accountID int64) int {
	return n.broker.Subscribers(accountID)
}

// shutdown drains the JMAP server within ctx's deadline, then stops the
// outbox, then releases the write executor's IMAP connections and its store —
// in that order, so no in-flight /set loses its connection under it and the
// outbox's last pass keeps its \Sent path alive to the end.
func (c *jmapComponents) shutdown(ctx context.Context) {
	if c == nil {
		return
	}
	if err := c.server.Shutdown(ctx); err != nil {
		c.log.Warn("jmap server did not drain in time, closing", "error", err)
		_ = c.server.Close()
	}
	c.outbox.shutdown(ctx)
	if c.writer != nil {
		c.writer.Close()
	}
	c.store.Close()
}
