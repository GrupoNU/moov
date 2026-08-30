package metrics

import (
	"strconv"
	"sync/atomic"
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

	// --- Push (W4a)

	// SSEConnections is the number of EventSource streams currently open.
	// It is the one number that says whether push is actually being used, and
	// the one that would reveal a connection leak: streams are long-lived, so
	// a handler that failed to return would show up here and nowhere else.
	SSEConnections *Gauge
	// SSEEvents counts emitted server-sent events by kind ("state", "ping").
	SSEEvents *Counter

	// --- Outbox (W3/W4b)

	// Submissions counts EmailSubmission outcomes by result: "sent" when the
	// SMTP server's 250 was read and persisted, "failed" when the submission
	// reached its permanent end (a 5xx, or the transient attempt cap), and
	// "canceled" when the user undid it inside the window.
	//
	// One family with a result label rather than three counters: the three are
	// mutually exclusive terminal outcomes of the SAME state machine, so the
	// question worth alerting on — "what fraction of submissions failed?" — is
	// one rate() over one family. Split families would force a join.
	//
	// Deliberately unlabeled by account: submission volume per account is a
	// question for the logs (which carry account_id on every transition), and
	// an account label here would grow the series set with the user base for a
	// number nobody alerts on per-account.
	Submissions *Counter

	// --- Triage (L3 epic E4)

	// SnoozesWoken counts snoozed messages returned to their origin folder by
	// the waker. It is the one number that says the feature is ALIVE: a
	// snooze that is never woken is indistinguishable, from the outside, from
	// a message the user filed away — so a flat line here while
	// moov_snoozes_pending is non-zero is the signature of a broken waker.
	SnoozesWoken *Counter

	// MutesApplied counts replies the engine archived because their
	// conversation is muted. Deliberately NOT split by escape hatch: the
	// hatches are per-message decisions whose detail belongs to the structured
	// logs (which name the hatch on every let-through), and a label per hatch
	// would invite alerting on a ratio that is a property of the user's mail
	// rather than of the system.
	MutesApplied *Counter

	// ScheduledSends is the number of submissions currently waiting for a
	// FUTURE release, per account — a gauge rather than a counter because the
	// question worth asking is "how many are queued right now", which is also
	// what the canon's cap of 100 bounds.
	ScheduledSends *Gauge

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

	// --- Sieve (E6)

	// SievePushes counts pushes of the managed Sieve script (vacation,
	// filter and forwarding writes all end in one), labeled by result
	// (ok/error). Vacation REPLIES are deliberately not counted anywhere:
	// Dovecot sends them and this server never sees one — a counter would
	// be a guess wearing a unit.
	SievePushes *Counter

	// VerificationMails counts forwarding-verification mails, labeled by
	// result (sent/failed) — the half of the GC-4 flow this server CAN
	// honestly observe.
	VerificationMails *Counter

	// VacationUpdates counts VacationResponse/set applications, labeled by
	// the resulting enabled state — the observable proxy for "vacation
	// state" that does not require a per-account gauge.
	VacationUpdates *Counter

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

	// sseOpen is the running total behind SSEConnections; see
	// AddSSEConnections.
	sseOpen atomic.Int64
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

		SSEConnections: r.Gauge("moov_jmap_sse_connections",
			"Currently open JMAP EventSource connections."),
		SSEEvents: r.Counter("moov_jmap_sse_events_total",
			"Server-sent events emitted by the JMAP EventSource endpoint, by kind."),

		Submissions: r.Counter("moov_submissions_total",
			"EmailSubmission terminal outcomes by result (sent, failed, canceled)."),

		SnoozesWoken: r.Counter("moov_snoozes_woken_total",
			"Snoozed messages returned to their origin folder by the waker."),
		MutesApplied: r.Counter("moov_mutes_applied_total",
			"Replies archived because their conversation is muted."),
		ScheduledSends: r.Gauge("moov_scheduled_sends",
			"Submissions currently waiting for a future release, per account."),

		SyncLagSeconds: r.Gauge("moov_sync_lag_seconds",
			"Seconds since each account's most recent sync checkpoint."),
		SyncPasses: r.Counter("moov_sync_passes_total",
			"Completed sync passes by kind and outcome."),
		WatcherState: r.Gauge("moov_sync_watcher_state",
			"Push watcher state per account: 1 watching, 0 idle, -1 failed."),
		BreakerOpen: r.Gauge("moov_sync_breaker_open",
			"1 when an account's circuit breaker is open, 0 otherwise."),

		SievePushes: r.Counter("moov_sieve_script_pushes_total",
			"Managed Sieve script pushes by result (ok, error)."),
		VerificationMails: r.Counter("moov_sieve_verification_mails_total",
			"Forwarding verification mails by result (sent, failed)."),
		VacationUpdates: r.Counter("moov_vacation_updates_total",
			"VacationResponse/set applications by resulting enabled state (true, false)."),
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

// AddSSEConnections adjusts the live EventSource connection count (W4a).
//
// The running total is kept here as an atomic rather than by giving Gauge an
// Add method: a gauge series is a value, and two handlers incrementing it
// concurrently through read-modify-write on the registry would need the
// registry's lock held across both halves. One atomic counter, one Set, and
// the gauge stays a plain value — no change to the registry's shape.
func (m *Metrics) AddSSEConnections(delta float64) {
	n := m.sseOpen.Add(int64(delta))
	m.SSEConnections.Set(nil, float64(n))
}

// IncSSEEvents counts one emitted server-sent event.
func (m *Metrics) IncSSEEvents(kind string) {
	m.SSEEvents.Inc(Labels{"kind": kind})
}

// The submission results Submissions counts. They are constants rather than
// bare strings because the three are a closed set — the outbox's terminal
// states — and a typo'd label would silently create a fourth series that no
// dashboard queries.
const (
	// SubmissionSent is a message the SMTP server accepted (the 250 was read
	// and persisted). It says nothing about DELIVERY, which happens after the
	// relay takes ownership and is not observable from here.
	SubmissionSent = "sent"
	// SubmissionFailed is a submission that reached a permanent end: a 5xx, or
	// the transient retry cap.
	SubmissionFailed = "failed"
	// SubmissionCanceled is an undo inside the window.
	SubmissionCanceled = "canceled"
)

// IncSubmission counts one terminal submission outcome.
//
// Called once per submission at the point the outcome becomes final and
// persisted, never on the intermediate transitions: a transient re-queue is
// not a failure (the message may still go out), and counting it as one would
// make the failure rate report retries rather than lost mail.
func (m *Metrics) IncSubmission(result string) {
	m.Submissions.Inc(Labels{"result": result})
}

// IncSievePush counts one managed-script push (E6). result: "ok"/"error".
func (m *Metrics) IncSievePush(result string) {
	m.SievePushes.Inc(Labels{"result": result})
}

// IncVerificationMail counts one forwarding verification mail (E6).
// result: "sent"/"failed".
func (m *Metrics) IncVerificationMail(result string) {
	m.VerificationMails.Inc(Labels{"result": result})
}

// IncVacationUpdate counts one vacation configuration change (E6).
func (m *Metrics) IncVacationUpdate(enabled bool) {
	label := "false"
	if enabled {
		label = "true"
	}
	m.VacationUpdates.Inc(Labels{"enabled": label})
}

// IncSnoozeWoken counts one snoozed message returned to its folder (L3 E4).
func (m *Metrics) IncSnoozeWoken() { m.SnoozesWoken.Inc(nil) }

// IncMuteApplied counts one reply archived by the mute rule (L3 E4).
func (m *Metrics) IncMuteApplied() { m.MutesApplied.Inc(nil) }

// SetScheduledSends records how many submissions an account has waiting for a
// future release (L3 E4).
//
// A gauge SET at collection time rather than incremented on every schedule,
// for the same reason SyncLagSeconds is: the number is a property of the queue
// at this instant, and a counter pair (scheduled/unscheduled) would drift the
// first time a process restarted mid-window.
func (m *Metrics) SetScheduledSends(accountID int64, n int) {
	m.ScheduledSends.Set(Labels{"account_id": strconv.FormatInt(accountID, 10)}, float64(n))
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
