package metrics

import (
	"strconv"
	"time"
)

// The metric families moovd exports (E8-lite, L2-jmap-server §3/J4).
//
// Naming follows the Prometheus conventions: a `moov_` namespace, a unit suffix
// on every family that has one (`_seconds`, `_total`), and no unit on gauges
// whose unit is in the name already.
//
// Cardinality is the design constraint. Every label below is bounded by
// something structural — the number of accounts, the number of JMAP methods, the
// handful of HTTP status classes — and NOTHING is labeled by mailbox, message,
// blob id or remote address. A metrics endpoint that grows a series per message
// is how a monitoring system falls over, and it is the mistake that is easy to
// make here because the interesting questions ("which mailbox is slow?") point
// straight at it. Those questions belong to the structured logs, which already
// carry the detail.

// Metrics is moovd's metric set: one struct so every recording site names a
// field rather than re-deriving a metric name from a string.
type Metrics struct {
	reg *Registry

	// --- JMAP HTTP (J1-J3)

	// HTTPRequests counts JMAP HTTP requests by route and status class.
	HTTPRequests *Counter
	// HTTPDuration is the JMAP HTTP request latency histogram, by route.
	HTTPDuration *Histogram

	// MethodCalls counts individual JMAP METHOD invocations by name and
	// outcome. This is the one that answers "is Email/query erroring?", which
	// the HTTP status cannot: RFC 8620 returns a 200 whose body carries an
	// error invocation, so an HTTP-only view reports a healthy server while
	// every method call fails.
	MethodCalls *Counter
	// MethodDuration is per-method latency.
	MethodDuration *Histogram

	// AuthAttempts counts authentication outcomes (J-A1): a cache hit, a real
	// IMAP LOGIN, a rejection, a lockout. The rejection rate against Dovecot is
	// what the fail2ban breaker exists to bound, so it needs to be visible.
	AuthAttempts *Counter

	// --- Sync engine (E5/E6)

	// SyncLagSeconds is how long ago each account last completed a sync pass.
	// Collected at scrape time from the store's checkpoints.
	SyncLagSeconds *Gauge
	// SyncPasses counts completed sync passes by kind and outcome.
	SyncPasses *Counter
	// WatcherState is the per-account push watcher state (see WatcherStateValue).
	WatcherState *Gauge
	// BreakerOpen is 1 when an account's circuit breaker is open, 0 otherwise.
	// The breaker is the anti-fail2ban control (ADR §4), so "how many accounts
	// are locked out of Dovecot right now" must be a first-class question.
	BreakerOpen *Gauge

	// --- Parser (E4)

	// ParseResults counts MIME parses by which stage of the S4 cascade
	// succeeded (go-message, enmime, salvage) or that it failed outright. The
	// parse-failure RATE is a release-quality signal: a jump means a new class
	// of message in the wild that the corpus does not cover.
	ParseResults *Counter

	// --- Process

	// BuildInfo is the standard always-1 gauge carrying version labels, so a
	// dashboard can annotate a graph with the deploy that changed it.
	BuildInfo *Gauge
}

// New builds the metric set on a fresh registry.
func New() *Metrics {
	r := NewRegistry()
	return NewWithRegistry(r)
}

// NewWithRegistry builds the metric set on an existing registry.
func NewWithRegistry(r *Registry) *Metrics {
	return &Metrics{
		reg: r,

		HTTPRequests: r.Counter("moov_jmap_http_requests_total",
			"JMAP HTTP requests by route and status class."),
		HTTPDuration: r.Histogram("moov_jmap_http_request_duration_seconds",
			"JMAP HTTP request latency by route.", nil),

		MethodCalls: r.Counter("moov_jmap_method_calls_total",
			"JMAP method invocations by method name and outcome."),
		MethodDuration: r.Histogram("moov_jmap_method_duration_seconds",
			"JMAP method latency by method name.", nil),

		AuthAttempts: r.Counter("moov_jmap_auth_attempts_total",
			"JMAP authentication attempts by outcome."),

		SyncLagSeconds: r.Gauge("moov_sync_lag_seconds",
			"Seconds since each account's most recent sync checkpoint."),
		SyncPasses: r.Counter("moov_sync_passes_total",
			"Completed sync passes by kind and outcome."),
		WatcherState: r.Gauge("moov_sync_watcher_state",
			"Push watcher state per account: 1 watching, 0 idle, -1 failed."),
		BreakerOpen: r.Gauge("moov_sync_breaker_open",
			"1 when an account's circuit breaker is open, 0 otherwise."),

		ParseResults: r.Counter("moov_parse_results_total",
			"MIME parse outcomes by which stage of the cascade produced the result."),

		BuildInfo: r.Gauge("moov_build_info",
			"Always 1; the labels carry the build identity."),
	}
}

// Registry exposes the underlying registry, for the /metrics handler.
func (m *Metrics) Registry() *Registry { return m.reg }

// SetBuildInfo records the running build. Called once at startup.
func (m *Metrics) SetBuildInfo(version, commit, goVersion string) {
	m.BuildInfo.Set(Labels{
		"version": version,
		"commit":  commit,
		"go":      goVersion,
	}, 1)
}

// WatcherStateValue maps a watcher state onto the gauge's encoding.
const (
	WatcherIdle     = 0.0
	WatcherWatching = 1.0
	WatcherFailed   = -1.0
)

// ObserveHTTP records one JMAP HTTP request.
//
// route is the ROUTE PATTERN, never the concrete path: /jmap/download/{accountId}
// is one series, while the paths it matches are unbounded. Getting this backwards
// is the classic way to blow up a metrics store.
func (m *Metrics) ObserveHTTP(route string, status int, d time.Duration) {
	l := Labels{"route": route, "status": statusClass(status)}
	m.HTTPRequests.Inc(l)
	m.HTTPDuration.ObserveDuration(Labels{"route": route}, d)
}

// ObserveMethod records one JMAP method invocation.
func (m *Metrics) ObserveMethod(method, outcome string, d time.Duration) {
	m.MethodCalls.Inc(Labels{"method": method, "outcome": outcome})
	m.MethodDuration.ObserveDuration(Labels{"method": method}, d)
}

// statusClass buckets an HTTP status into its class ("2xx", "4xx", ...).
//
// The class rather than the code, on purpose: the codes this server returns are
// few, but a label of raw codes invites an unbounded set the moment a proxy
// injects one, and every alert worth writing is expressed over classes anyway.
func statusClass(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500:
		return "5xx"
	default:
		return strconv.Itoa(status)
	}
}
