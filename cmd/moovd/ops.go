package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/GrupoNU/moov/internal/config"
	"github.com/GrupoNU/moov/internal/metrics"
	"github.com/GrupoNU/moov/internal/store"
	"github.com/GrupoNU/moov/internal/version"
)

// The operational HTTP server (E8-lite, epic J4): /healthz and /metrics.
//
// It is a SEPARATE listener from the JMAP API, and that separation is the point:
//
//   - /metrics exposes account ids and sync state. It is operational data, not
//     user data, but it is not public either — so it lives on a port that the
//     deploy never proxies (deploy/docker-compose.yml publishes nothing; only
//     the JMAP port is reachable through Caddy, and only over the VPN).
//   - /healthz must answer while the JMAP server is saturated or still starting.
//     Sharing a listener with the API would make the health check report the
//     API's queueing rather than the process's health, which is exactly backwards
//     for the thing an orchestrator restarts on.
//
// Neither endpoint is authenticated, because neither is reachable from outside
// the Docker networks. That is a property of the deployment, stated here so it
// is re-checked if this listener is ever published.

// opsComponents holds the operational server.
type opsComponents struct {
	server *http.Server
	ln     net.Listener
	log    *slog.Logger
}

// Timeouts for the operational listener. Short: both endpoints are supposed to
// answer immediately, and a slow one is itself the signal.
const (
	opsReadHeaderTimeout = 5 * time.Second
	opsReadTimeout       = 10 * time.Second
	opsWriteTimeout      = 30 * time.Second
	opsIdleTimeout       = 60 * time.Second

	// scrapeCollectTimeout bounds the database work a scrape does. A Prometheus
	// scrape that hangs is worse than one that reports nothing: it stalls the
	// scrape loop and takes the alerting with it.
	scrapeCollectTimeout = 3 * time.Second
)

// startOps starts the operational HTTP server, or returns nil when MOOV_HTTP_ADDR
// is empty.
//
// syncStore may be nil (the sync engine is disabled), in which case the sync
// gauges simply produce no series — which is honest: there is nothing syncing.
func startOps(
	ctx context.Context,
	cfg config.Config,
	m *metrics.Metrics,
	syncStore *store.Store,
	logger *slog.Logger,
	fatal func(error),
) (*opsComponents, error) {
	if cfg.HTTPAddr == "" {
		logger.Info("operational http server disabled", "hint", "MOOV_HTTP_ADDR enables /healthz and /metrics")
		return nil, nil //nolint:nilnil // "disabled" is a valid, non-error outcome
	}

	build := version.Get()
	m.SetBuildInfo(build.Version, build.Commit, build.Go)

	if syncStore != nil {
		installSyncCollectors(m, syncStore, logger)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /metrics", handleMetrics(m))

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: opsReadHeaderTimeout,
		ReadTimeout:       opsReadTimeout,
		WriteTimeout:      opsWriteTimeout,
		IdleTimeout:       opsIdleTimeout,
	}

	ln, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return nil, fmt.Errorf("binding operational listener on %s: %w", cfg.HTTPAddr, err)
	}

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal(fmt.Errorf("operational server: %w", err))
		}
	}()

	logger.Info("operational server listening", "addr", ln.Addr().String(),
		"endpoints", "/healthz /metrics")
	_ = ctx
	return &opsComponents{server: srv, ln: ln, log: logger}, nil
}

// handleHealthz answers the liveness probe.
//
// LIVENESS, not readiness, and the distinction is deliberate. This reports "the
// process is up and its HTTP stack works" — it does NOT check the database,
// because a health check that fails when PostgreSQL blips would have Docker
// restart a healthy daemon during a database restart, turning a recoverable
// outage into a crash loop. Store reachability is reported through /metrics
// (moov_sync_lag_seconds going stale) and through the logs, where an operator can
// act on it rather than an orchestrator reacting to it.
func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	build := version.Get()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"version": build.Version,
		"commit":  build.Commit,
	})
}

// handleMetrics serves the Prometheus exposition.
func handleMetrics(m *metrics.Metrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", metrics.ContentType)
		w.Header().Set("Cache-Control", "no-store")
		if err := m.Registry().Write(w); err != nil {
			// The response is already partly written, so there is nothing to
			// repair; the scrape will fail and that is the correct signal.
			slog.Default().Warn("writing metrics failed", "error", err)
		}
		_ = r
	}
}

// installSyncCollectors wires the scrape-time gauges that read the store.
//
// Sync lag and breaker state are COLLECTED rather than pushed because the truth
// already lives in the sync_log checkpoints: a background goroutine mirroring it
// into a gauge would be a second copy that can drift from the first, and the
// drift would show up as a monitoring lie precisely during an incident.
//
// Both gauges are served from ONE store read per scrape, cached between the two
// collectors, so a scrape costs a single pass over the checkpoints rather than
// two.
func installSyncCollectors(m *metrics.Metrics, st *store.Store, logger *slog.Logger) {
	// snapshot reads every account's checkpoints once.
	snapshot := func() (lag, breaker []metrics.Sample) {
		ctx, cancel := context.WithTimeout(context.Background(), scrapeCollectTimeout)
		defer cancel()

		accounts, err := st.ListAccounts(ctx)
		if err != nil {
			logger.Warn("metrics: listing accounts failed", "error", err)
			return nil, nil
		}

		now := time.Now()
		for _, a := range accounts {
			checkpoints, err := st.ListCheckpoints(ctx, a.ID)
			if err != nil {
				logger.Warn("metrics: listing checkpoints failed", "account", a.ID, "error", err)
				continue
			}
			label := metrics.Labels{"account": strconv.FormatInt(a.ID, 10)}

			// The account's lag is its OLDEST scope: a mailbox that stopped
			// syncing is the problem, and taking the newest checkpoint would
			// hide it behind whichever folder updated most recently.
			var oldest time.Time
			anyOpen := false
			for _, cp := range checkpoints {
				if cp.BreakerState == store.BreakerOpen {
					anyOpen = true
				}
				if cp.LastSuccessAt == nil {
					continue
				}
				if oldest.IsZero() || cp.LastSuccessAt.Before(oldest) {
					oldest = *cp.LastSuccessAt
				}
			}

			// An account with no checkpoints at all has nothing to report on
			// either gauge; emitting a 0 would read as "healthy and current".
			if len(checkpoints) > 0 {
				breaker = append(breaker, metrics.Sample{Labels: label, Value: boolGauge(anyOpen)})
			}
			if oldest.IsZero() {
				// Never synced: no lag to report. An absent series is honest;
				// a zero would read as "perfectly fresh".
				continue
			}
			lag = append(lag, metrics.Sample{Labels: label, Value: now.Sub(oldest).Seconds()})
		}
		return lag, breaker
	}

	// The two collectors run within the same scrape, microseconds apart. Rather
	// than share mutable state between them (which would need its own locking
	// for a benefit measured in one query), each asks for what it needs; the
	// cache below collapses that to one store read per scrape anyway.
	cache := &scrapeCache{ttl: time.Second, fn: snapshot}

	m.SyncLagSeconds.SetCollector(func() []metrics.Sample {
		lag, _ := cache.get()
		return lag
	})
	m.BreakerOpen.SetCollector(func() []metrics.Sample {
		_, breaker := cache.get()
		return breaker
	})
}

// scrapeCache memoizes one store read across the collectors of a single scrape.
//
// The TTL is deliberately tiny (one second): long enough that the two
// collectors of one scrape share a read, far shorter than any scrape interval,
// so consecutive scrapes always see fresh data.
type scrapeCache struct {
	mu   sync.Mutex
	ttl  time.Duration
	fn   func() ([]metrics.Sample, []metrics.Sample)
	at   time.Time
	lag  []metrics.Sample
	brkr []metrics.Sample
}

func (c *scrapeCache) get() (lag, breaker []metrics.Sample) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.at) < c.ttl {
		return c.lag, c.brkr
	}
	c.lag, c.brkr = c.fn()
	c.at = time.Now()
	return c.lag, c.brkr
}

// boolGauge renders a boolean as the 1/0 a Prometheus gauge expects.
func boolGauge(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// probeHealth implements the `moovd -health` container healthcheck.
//
// It dials the address this same process would serve /healthz on (MOOV_HTTP_ADDR,
// with a bare or wildcard host rewritten to loopback, since a healthcheck runs
// INSIDE the container) and reports whether it answered 200.
//
// It deliberately does NOT load the full configuration: a healthcheck must work
// even when a secret is missing, or a misconfigured daemon would report itself
// unhealthy for the wrong reason and the operator would chase the probe instead
// of the cause.
func probeHealth() error {
	addr := os.Getenv("MOOV_HTTP_ADDR")
	if addr == "" {
		addr = config.DefaultHTTPAddr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("MOOV_HTTP_ADDR %q: %w", addr, err)
	}
	// ":8080" and "0.0.0.0:8080" both mean "every interface"; from inside the
	// container, loopback is the one that always works.
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}

	ctx, cancel := context.WithTimeout(context.Background(), healthProbeTimeout)
	defer cancel()

	url := "http://" + net.JoinHostPort(host, port) + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/healthz returned %s", resp.Status)
	}
	return nil
}

// healthProbeTimeout bounds the self-probe. Shorter than the healthcheck's own
// timeout in the compose file, so the probe reports a failure rather than being
// killed by Docker.
const healthProbeTimeout = 3 * time.Second

// shutdown drains the operational server.
func (c *opsComponents) shutdown(ctx context.Context) {
	if c == nil {
		return
	}
	if err := c.server.Shutdown(ctx); err != nil {
		c.log.Warn("operational server did not drain in time, closing", "error", err)
		_ = c.server.Close()
	}
}
