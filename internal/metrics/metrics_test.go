package metrics_test

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/metrics"
)

// The exposition format is a CONTRACT with Prometheus, so these tests assert on
// the exact bytes rather than on internal state: a scrape that parses is the
// only property that matters, and the only way a hand-written exporter earns the
// right to exist (see the package doc for why it is hand-written).

func TestCounterExposition(t *testing.T) {
	r := metrics.NewRegistry()
	c := r.Counter("moov_test_total", "A test counter.")

	c.Inc(metrics.Labels{"route": "/jmap/api"})
	c.Inc(metrics.Labels{"route": "/jmap/api"})
	c.Add(metrics.Labels{"route": "/healthz"}, 3)

	got := render(t, r)

	for _, want := range []string{
		"# HELP moov_test_total A test counter.",
		"# TYPE moov_test_total counter",
		`moov_test_total{route="/jmap/api"} 2`,
		`moov_test_total{route="/healthz"} 3`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("exposition is missing %q\ngot:\n%s", want, got)
		}
	}
}

// A counter that goes backwards breaks every rate() built on it, so the
// decrement is swallowed rather than exported.
func TestCounterRejectsNegative(t *testing.T) {
	r := metrics.NewRegistry()
	c := r.Counter("moov_test_total", "")
	c.Add(nil, 5)
	c.Add(nil, -3)

	if got := render(t, r); !strings.Contains(got, "moov_test_total 5") {
		t.Errorf("a negative delta must be ignored; got:\n%s", got)
	}
}

// Label order must not create distinct series: {a,b} and {b,a} are one series in
// Prometheus, and a key that preserved insertion order would silently double
// every metric recorded from two call sites.
func TestLabelOrderIsCanonical(t *testing.T) {
	r := metrics.NewRegistry()
	c := r.Counter("moov_test_total", "")

	c.Inc(metrics.Labels{"a": "1", "b": "2"})
	c.Inc(metrics.Labels{"b": "2", "a": "1"})

	got := render(t, r)
	if n := strings.Count(got, "moov_test_total{"); n != 1 {
		t.Errorf("want exactly 1 series, got %d\n%s", n, got)
	}
	if !strings.Contains(got, `moov_test_total{a="1",b="2"} 2`) {
		t.Errorf("the two recordings must land on one series; got:\n%s", got)
	}
}

func TestGaugeSetAndCollector(t *testing.T) {
	r := metrics.NewRegistry()

	stored := r.Gauge("moov_stored", "")
	stored.Set(metrics.Labels{"kind": "x"}, 42)

	collected := r.Gauge("moov_collected", "")
	collected.SetCollector(func() []metrics.Sample {
		return []metrics.Sample{
			{Labels: metrics.Labels{"account": "1"}, Value: 1.5},
			{Labels: metrics.Labels{"account": "2"}, Value: 0},
		}
	})

	got := render(t, r)
	for _, want := range []string{
		`moov_stored{kind="x"} 42`,
		`moov_collected{account="1"} 1.5`,
		`moov_collected{account="2"} 0`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("exposition is missing %q\ngot:\n%s", want, got)
		}
	}
}

// A collected family must reflect ONLY the current scrape. A deprovisioned
// account whose series lingered would report a stale sync lag forever, which is
// the monitoring lie this test exists to prevent.
func TestCollectorReplacesPreviousSeries(t *testing.T) {
	r := metrics.NewRegistry()
	g := r.Gauge("moov_lag", "")

	accounts := []string{"1", "2"}
	g.SetCollector(func() []metrics.Sample {
		out := make([]metrics.Sample, 0, len(accounts))
		for _, a := range accounts {
			out = append(out, metrics.Sample{Labels: metrics.Labels{"account": a}, Value: 1})
		}
		return out
	})

	if got := render(t, r); !strings.Contains(got, `moov_lag{account="2"}`) {
		t.Fatalf("setup: account 2 should be present; got:\n%s", got)
	}

	accounts = []string{"1"} // account 2 is deprovisioned

	got := render(t, r)
	if strings.Contains(got, `moov_lag{account="2"}`) {
		t.Errorf("a series absent from this scrape must not be re-emitted; got:\n%s", got)
	}
	if !strings.Contains(got, `moov_lag{account="1"} 1`) {
		t.Errorf("the remaining series must survive; got:\n%s", got)
	}
}

func TestHistogramExposition(t *testing.T) {
	r := metrics.NewRegistry()
	h := r.Histogram("moov_latency_seconds", "Latency.", []float64{0.1, 1})

	h.Observe(nil, 0.05) // <= 0.1 and <= 1
	h.Observe(nil, 0.5)  // <= 1 only
	h.Observe(nil, 5)    // +Inf only

	got := render(t, r)

	for _, want := range []string{
		"# TYPE moov_latency_seconds histogram",
		`moov_latency_seconds_bucket{le="0.1"} 1`,
		`moov_latency_seconds_bucket{le="1"} 2`,
		`moov_latency_seconds_bucket{le="+Inf"} 3`,
		"moov_latency_seconds_count 3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("exposition is missing %q\ngot:\n%s", want, got)
		}
	}

	// The sum must be the real sum, not a bucket artifact.
	if !strings.Contains(got, "moov_latency_seconds_sum 5.55") {
		t.Errorf("want sum 5.55; got:\n%s", got)
	}
}

// Buckets are cumulative in the exposition format. A monotonicity violation
// makes histogram_quantile() return nonsense, so it is asserted directly.
func TestHistogramBucketsAreCumulative(t *testing.T) {
	r := metrics.NewRegistry()
	h := r.Histogram("moov_h_seconds", "", []float64{0.01, 0.1, 1, 10})
	for _, v := range []float64{0.005, 0.05, 0.5, 5, 50} {
		h.Observe(nil, v)
	}

	got := render(t, r)
	var last uint64
	for _, le := range []string{"0.01", "0.1", "1", "10", "+Inf"} {
		n := bucketValue(t, got, "moov_h_seconds", le)
		if n < last {
			t.Errorf("bucket le=%s is %d, lower than the previous %d — not cumulative:\n%s",
				le, n, last, got)
		}
		last = n
	}
	if last != 5 {
		t.Errorf("the +Inf bucket must equal the total count 5, got %d", last)
	}
}

func TestObserveDuration(t *testing.T) {
	r := metrics.NewRegistry()
	h := r.Histogram("moov_d_seconds", "", []float64{0.5, 2})
	h.ObserveDuration(metrics.Labels{"method": "Email/get"}, 1500*time.Millisecond)

	// Note the label order: the family's own labels are emitted sorted, and the
	// histogram's "le" is appended after them. Prometheus treats a label set as
	// unordered, so this is purely about what the renderer produces.
	got := render(t, r)
	if !strings.Contains(got, `moov_d_seconds_bucket{method="Email/get",le="2"} 1`) {
		t.Errorf("a 1.5 s duration belongs in the le=2 bucket; got:\n%s", got)
	}
	if !strings.Contains(got, `moov_d_seconds_bucket{method="Email/get",le="0.5"} 0`) {
		t.Errorf("a 1.5 s duration must not land in le=0.5; got:\n%s", got)
	}
}

// Label values carry text from outside the process only in bounded forms, but
// the escaping rules are part of the format and a broken escape corrupts the
// whole scrape — not just the offending series.
func TestLabelValueEscaping(t *testing.T) {
	r := metrics.NewRegistry()
	c := r.Counter("moov_test_total", "")
	c.Inc(metrics.Labels{"v": `a"b\c` + "\n" + "d"})

	got := render(t, r)
	if !strings.Contains(got, `moov_test_total{v="a\"b\\c\nd"} 1`) {
		t.Errorf("label value is not escaped per the exposition format; got:\n%s", got)
	}
}

// The registry is written from every request goroutine and read by scrapes. This
// is the test that has to pass under -race.
func TestConcurrentUse(t *testing.T) {
	r := metrics.NewRegistry()
	c := r.Counter("moov_test_total", "")
	h := r.Histogram("moov_test_seconds", "", nil)
	g := r.Gauge("moov_test_gauge", "")

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for range 100 {
				c.Inc(metrics.Labels{"worker": string(rune('a' + i))})
				h.Observe(nil, 0.01)
				g.Set(metrics.Labels{"worker": string(rune('a' + i))}, float64(i))
			}
		}(i)
	}
	// Scrape concurrently with the writers: this is the real access pattern.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 50 {
			var sb strings.Builder
			if err := r.Write(&sb); err != nil {
				t.Errorf("Write: %v", err)
				return
			}
		}
	}()
	wg.Wait()

	got := render(t, r)
	if !strings.Contains(got, "moov_test_seconds_count 800") {
		t.Errorf("want 800 observations recorded; got:\n%s", got)
	}
}

// Registering the same family twice must be idempotent rather than duplicating
// the HELP/TYPE header, which would make the scrape unparseable.
func TestRegisterIsIdempotent(t *testing.T) {
	r := metrics.NewRegistry()
	r.Counter("moov_test_total", "help").Inc(nil)
	r.Counter("moov_test_total", "help").Inc(nil)

	got := render(t, r)
	if n := strings.Count(got, "# TYPE moov_test_total"); n != 1 {
		t.Errorf("want exactly one TYPE line, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, "moov_test_total 2") {
		t.Errorf("both increments must land on the same series; got:\n%s", got)
	}
}

func TestMoovMetricsSet(t *testing.T) {
	m := metrics.New()
	m.SetBuildInfo("1.2.3", "abc1234", "go1.24")
	m.ObserveHTTP("/jmap/api", 200, 12*time.Millisecond)
	m.ObserveHTTP("/jmap/api", 500, 30*time.Millisecond)
	m.ObserveMethod("Email/query", "ok", 9*time.Millisecond)
	m.ParseResults.Inc(metrics.Labels{"stage": "go-message"})

	got := render(t, m.Registry())

	for _, want := range []string{
		`moov_build_info{commit="abc1234",go="go1.24",version="1.2.3"} 1`,
		`moov_jmap_http_requests_total{route="/jmap/api",status="2xx"} 1`,
		`moov_jmap_http_requests_total{route="/jmap/api",status="5xx"} 1`,
		`moov_jmap_method_calls_total{method="Email/query",outcome="ok"} 1`,
		`moov_parse_results_total{stage="go-message"} 1`,
		"moov_jmap_http_request_duration_seconds_count",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("exposition is missing %q\ngot:\n%s", want, got)
		}
	}
}

// The submission counters (W4b). The three results are one family with a
// result label rather than three families, so the assertion that matters is
// that all three land on the SAME metric name — that is what makes
// "failed / (sent+failed+canceled)" a single rate() rather than a join.
func TestSubmissionCounters(t *testing.T) {
	m := metrics.New()
	m.IncSubmission(metrics.SubmissionSent)
	m.IncSubmission(metrics.SubmissionSent)
	m.IncSubmission(metrics.SubmissionFailed)
	m.IncSubmission(metrics.SubmissionCanceled)

	got := render(t, m.Registry())

	for _, want := range []string{
		"# TYPE moov_submissions_total counter",
		`moov_submissions_total{result="sent"} 2`,
		`moov_submissions_total{result="failed"} 1`,
		`moov_submissions_total{result="canceled"} 1`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("exposition is missing %q\ngot:\n%s", want, got)
		}
	}

	// One family, not three: a split would break the failure-rate query.
	if n := strings.Count(got, "# TYPE moov_submissions_total"); n != 1 {
		t.Errorf("want exactly one submissions family, got %d:\n%s", n, got)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func render(t *testing.T, r *metrics.Registry) string {
	t.Helper()
	var sb strings.Builder
	if err := r.Write(&sb); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return sb.String()
}

// bucketValue pulls one bucket's count out of a rendered exposition.
func bucketValue(t *testing.T, exposition, name, le string) uint64 {
	t.Helper()
	prefix := name + `_bucket{le="` + le + `"} `
	for _, line := range strings.Split(exposition, "\n") {
		if strings.HasPrefix(line, prefix) {
			n, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(line, prefix)), 10, 64)
			if err != nil {
				t.Fatalf("parsing %q: %v", line, err)
			}
			return n
		}
	}
	t.Fatalf("no bucket le=%s for %s in:\n%s", le, name, exposition)
	return 0
}
