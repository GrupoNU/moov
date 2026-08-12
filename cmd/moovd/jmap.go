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
	"github.com/GrupoNU/moov/internal/jmap"
	"github.com/GrupoNU/moov/internal/jmap/mail"
	"github.com/GrupoNU/moov/internal/jmaphttp"
	"github.com/GrupoNU/moov/internal/store"
)

// The JMAP server's wiring (J1, L2-jmap-server §2.1): same daemon as the sync
// engine, its own listener, opt-in via MOOV_JMAP_ENABLED.

// jmapComponents holds what the JMAP server needs, so shutdown can release it
// in one place.
type jmapComponents struct {
	store  *store.Store
	server *http.Server
	ln     net.Listener
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
func startJMAP(ctx context.Context, cfg config.Config, logger *slog.Logger, fatal func(error)) (*jmapComponents, error) {
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

	srv, err := jmaphttp.New(jmaphttp.Config{
		BaseURL:        cfg.JMAP.ExternalURL,
		AllowedOrigins: cfg.JMAP.CORSOrigins,
		Limits:         limits,
		Logger:         logger,
		Blobs:          deps.Blobs,
	}, auth)
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("building jmap server: %w", err)
	}

	// The mail methods (J2: the get family). J3 adds RegisterQueryMethods
	// here, over the same Deps. Registration must happen before Handler() is
	// mounted, which is why it sits above the http.Server construction.
	mail.RegisterGetMethods(srv.Registry(), deps)

	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: jmaphttp.ReadHeaderTimeout,
		ReadTimeout:       jmaphttp.ReadTimeout,
		WriteTimeout:      jmaphttp.WriteTimeout,
		IdleTimeout:       jmaphttp.IdleTimeout,
	}

	ln, err := net.Listen("tcp", cfg.JMAP.Addr)
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("binding jmap listener on %s: %w", cfg.JMAP.Addr, err)
	}

	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal(fmt.Errorf("jmap server: %w", err))
		}
	}()

	logger.Info("jmap server listening",
		"addr", ln.Addr().String(),
		"external_url", cfg.JMAP.ExternalURL,
		"cors_origins", len(cfg.JMAP.CORSOrigins),
		"imap_host", cfg.JMAP.IMAPHost,
	)
	return &jmapComponents{store: st, server: httpSrv, ln: ln, log: logger}, nil
}

// shutdown drains the JMAP server within ctx's deadline, then releases its
// store.
func (c *jmapComponents) shutdown(ctx context.Context) {
	if c == nil {
		return
	}
	if err := c.server.Shutdown(ctx); err != nil {
		c.log.Warn("jmap server did not drain in time, closing", "error", err)
		_ = c.server.Close()
	}
	c.store.Close()
}
