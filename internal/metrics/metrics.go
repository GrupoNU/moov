// Package metrics is moovd's Prometheus exposition (E8-lite, epic J4).
//
// # Why this is written by hand and not with the Prometheus client library
//
// The obvious move is prometheus/client_golang. It was rejected for this scope,
// deliberately, and the reasoning is worth recording because "just add the
// dependency" is the right answer in most projects:
//
//   - Moov vendors its ENTIRE dependency tree and builds with -mod=vendor so the
//     production image is hermetic (see the Dockerfile). client_golang pulls
//     client_model, common, procfs and protobuf — roughly ten modules and several
//     megabytes of vendored code — into a public AGPL repository whose supply
//     chain is part of the product (regla 3).
//   - What E8-lite actually needs is four metric families in a text format whose
//     grammar fits on one page. The library's value is its registry, its
//     collectors for Go runtime internals, and its protobuf negotiation — none of
//     which this scope uses.
//   - The exposition format is a stable, versioned contract. Writing it correctly
//     once is a smaller ongoing risk than tracking a dependency tree.
//
// The moment this package needs histograms with exemplars, native histograms, or
// a /federate endpoint, that calculus flips and the library is the right answer.
// It is a deliberate trade for the current scope, not a claim that hand-rolling
// metrics is generally wise.
//
// # What is exposed
//
// Everything here is a snapshot read at scrape time. There is no background
// aggregation goroutine: the sync engine's state already lives in the store
// (sync_log checkpoints) and in the watcher's own counters, so a scrape READS
// those rather than maintaining a shadow copy that could drift.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Registry holds the process's metrics.
//
// It is safe for concurrent use: HTTP handlers record into it from many
// goroutines while a scrape reads it.
type Registry struct {
	mu sync.Mutex

	counters   map[string]*counterFamily
	gauges     map[string]*gaugeFamily
	histograms map[string]*histogramFamily

	// order preserves first-registration order so a scrape's output is stable
	// between calls, which makes diffing two scrapes useful.
	order []familyRef
}

type familyKind int

const (
	kindCounter familyKind = iota
	kindGauge
	kindHistogram
)

type familyRef struct {
	kind familyKind
	name string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		counters:   make(map[string]*counterFamily),
		gauges:     make(map[string]*gaugeFamily),
		histograms: make(map[string]*histogramFamily),
	}
}

// ---------------------------------------------------------------------------
// Label sets
// ---------------------------------------------------------------------------

// Labels is one metric's label set. The zero value means no labels.
type Labels map[string]string

// key renders a label set as a canonical, order-independent map key. Sorting is
// what makes {a="1",b="2"} and {b="2",a="1"} the same series, as Prometheus
// requires.
func (l Labels) key() string {
	if len(l) == 0 {
		return ""
	}
	names := make([]string, 0, len(l))
	for k := range l {
		names = append(names, k)
	}
	sort.Strings(names)

	var b strings.Builder
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(n)
		b.WriteByte('=')
		b.WriteString(l[n])
	}
	return b.String()
}

// render writes the label set in exposition syntax, with values escaped.
func (l Labels) render(extra ...string) string {
	if len(l) == 0 && len(extra) == 0 {
		return ""
	}
	names := make([]string, 0, len(l))
	for k := range l {
		names = append(names, k)
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names)+len(extra)/2)
	for _, n := range names {
		parts = append(parts, n+`="`+escapeLabelValue(l[n])+`"`)
	}
	// extra arrives as name, value pairs (the histogram's "le").
	for i := 0; i+1 < len(extra); i += 2 {
		parts = append(parts, extra[i]+`="`+escapeLabelValue(extra[i+1])+`"`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// escapeLabelValue applies the exposition format's escaping rules: backslash,
// double quote and newline. Nothing else is escaped, per the spec.
func escapeLabelValue(v string) string {
	if !strings.ContainsAny(v, `\"`+"\n") {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(v)
}

// ---------------------------------------------------------------------------
// Counter
// ---------------------------------------------------------------------------

type counterFamily struct {
	help   string
	values map[string]*counterValue
}

type counterValue struct {
	labels Labels
	v      float64
}

// Counter registers (or looks up) a counter family. Calling it twice with the
// same name is safe and returns the same family.
func (r *Registry) Counter(name, help string) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.counters[name]; !ok {
		r.counters[name] = &counterFamily{help: help, values: make(map[string]*counterValue)}
		r.order = append(r.order, familyRef{kindCounter, name})
	}
	return &Counter{reg: r, name: name}
}

// Counter is a handle to a counter family.
type Counter struct {
	reg  *Registry
	name string
}

// Add increments the series identified by labels. Negative deltas are ignored:
// a counter that goes down breaks every rate() built on it, so the mistake is
// swallowed here rather than exported as corrupt data.
func (c *Counter) Add(labels Labels, delta float64) {
	if delta < 0 {
		return
	}
	c.reg.mu.Lock()
	defer c.reg.mu.Unlock()

	f := c.reg.counters[c.name]
	k := labels.key()
	cv, ok := f.values[k]
	if !ok {
		cv = &counterValue{labels: labels}
		f.values[k] = cv
	}
	cv.v += delta
}

// Inc adds one.
func (c *Counter) Inc(labels Labels) { c.Add(labels, 1) }

// ---------------------------------------------------------------------------
// Gauge
// ---------------------------------------------------------------------------

type gaugeFamily struct {
	help   string
	values map[string]*gaugeValue
	// collect, when set, replaces the stored values at scrape time. This is how
	// sync lag is exported: the truth lives in the store, and a scrape asks for
	// it rather than a goroutine mirroring it.
	collect func() []Sample
}

type gaugeValue struct {
	labels Labels
	v      float64
}

// Sample is one (labels, value) pair produced by a collect function.
type Sample struct {
	Labels Labels
	Value  float64
}

// Gauge registers (or looks up) a gauge family.
func (r *Registry) Gauge(name, help string) *Gauge {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.gauges[name]; !ok {
		r.gauges[name] = &gaugeFamily{help: help, values: make(map[string]*gaugeValue)}
		r.order = append(r.order, familyRef{kindGauge, name})
	}
	return &Gauge{reg: r, name: name}
}

// Gauge is a handle to a gauge family.
type Gauge struct {
	reg  *Registry
	name string
}

// Set assigns the series identified by labels.
func (g *Gauge) Set(labels Labels, v float64) {
	g.reg.mu.Lock()
	defer g.reg.mu.Unlock()

	f := g.reg.gauges[g.name]
	k := labels.key()
	gv, ok := f.values[k]
	if !ok {
		gv = &gaugeValue{labels: labels}
		f.values[k] = gv
	}
	gv.v = v
}

// SetCollector installs a scrape-time producer for this family.
//
// The function runs INSIDE the scrape, so it must be fast and must not block on
// anything unbounded — a metrics endpoint that hangs takes the alerting with it.
// Callers pass a function with its own short timeout.
func (g *Gauge) SetCollector(fn func() []Sample) {
	g.reg.mu.Lock()
	defer g.reg.mu.Unlock()
	g.reg.gauges[g.name].collect = fn
}

// ---------------------------------------------------------------------------
// Histogram
// ---------------------------------------------------------------------------

type histogramFamily struct {
	help    string
	buckets []float64
	values  map[string]*histogramValue
}

type histogramValue struct {
	labels Labels
	counts []uint64 // one per bucket, cumulative at render time
	sum    float64
	count  uint64
}

// DefaultLatencyBuckets are seconds, chosen around the product's own bar rather
// than around a library default.
//
// Regla 1 fixes the Gmail-class target at "search and actions under 100 ms
// perceived", so the buckets cluster tightly below and just above 100 ms — that
// is where the signal is. The long tail (1 s, 2.5 s, 5 s, 10 s) exists to make a
// pathological request visible without adding resolution nobody will read.
var DefaultLatencyBuckets = []float64{
	0.005, 0.010, 0.025, 0.050, 0.075, 0.100, 0.250, 0.500, 1, 2.5, 5, 10,
}

// Histogram registers (or looks up) a histogram family. Buckets must be sorted
// ascending; nil means DefaultLatencyBuckets.
func (r *Registry) Histogram(name, help string, buckets []float64) *Histogram {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.histograms[name]; !ok {
		if buckets == nil {
			buckets = DefaultLatencyBuckets
		}
		b := make([]float64, len(buckets))
		copy(b, buckets)
		sort.Float64s(b)
		r.histograms[name] = &histogramFamily{
			help:    help,
			buckets: b,
			values:  make(map[string]*histogramValue),
		}
		r.order = append(r.order, familyRef{kindHistogram, name})
	}
	return &Histogram{reg: r, name: name}
}

// Histogram is a handle to a histogram family.
type Histogram struct {
	reg  *Registry
	name string
}

// Observe records one observation.
func (h *Histogram) Observe(labels Labels, v float64) {
	h.reg.mu.Lock()
	defer h.reg.mu.Unlock()

	f := h.reg.histograms[h.name]
	k := labels.key()
	hv, ok := f.values[k]
	if !ok {
		hv = &histogramValue{labels: labels, counts: make([]uint64, len(f.buckets))}
		f.values[k] = hv
	}
	for i, b := range f.buckets {
		if v <= b {
			hv.counts[i]++
		}
	}
	hv.sum += v
	hv.count++
}

// ObserveDuration records a duration in seconds, which is the unit the
// exposition format mandates for time.
func (h *Histogram) ObserveDuration(labels Labels, d time.Duration) {
	h.Observe(labels, d.Seconds())
}

// ---------------------------------------------------------------------------
// Exposition
// ---------------------------------------------------------------------------

// Write renders the registry in the Prometheus text exposition format
// (version 0.0.4), which is what /metrics serves.
//
// Named Write rather than WriteTo on purpose: `WriteTo(io.Writer) error` looks
// like io.WriterTo but has the wrong signature, and go vet rejects the
// near-miss. Returning a byte count nobody uses would be worse than renaming.
func (r *Registry) Write(w io.Writer) error {
	// Collectors run BEFORE the lock is taken for rendering: a collect function
	// touches the database, and holding the registry mutex across that would
	// block every in-flight request's metric recording on a database round trip.
	collected := r.runCollectors()

	r.mu.Lock()
	defer r.mu.Unlock()

	var b strings.Builder
	for _, ref := range r.order {
		switch ref.kind {
		case kindCounter:
			r.writeCounter(&b, ref.name)
		case kindGauge:
			r.writeGauge(&b, ref.name, collected[ref.name])
		case kindHistogram:
			r.writeHistogram(&b, ref.name)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// runCollectors invokes every gauge collector without holding the render lock.
func (r *Registry) runCollectors() map[string][]Sample {
	r.mu.Lock()
	type job struct {
		name string
		fn   func() []Sample
	}
	jobs := make([]job, 0, len(r.gauges))
	for name, f := range r.gauges {
		if f.collect != nil {
			jobs = append(jobs, job{name, f.collect})
		}
	}
	r.mu.Unlock()

	out := make(map[string][]Sample, len(jobs))
	for _, j := range jobs {
		out[j.name] = j.fn()
	}
	return out
}

func (r *Registry) writeCounter(b *strings.Builder, name string) {
	f := r.counters[name]
	writeHeader(b, name, f.help, "counter")
	for _, k := range sortedKeys(f.values) {
		cv := f.values[k]
		fmt.Fprintf(b, "%s%s %s\n", name, cv.labels.render(), formatFloat(cv.v))
	}
}

func (r *Registry) writeGauge(b *strings.Builder, name string, collected []Sample) {
	f := r.gauges[name]
	writeHeader(b, name, f.help, "gauge")

	if f.collect != nil {
		// A collected family is entirely defined by this scrape's samples;
		// stale series from a previous scrape must NOT be re-emitted, or a
		// deprovisioned account would keep reporting a lag forever.
		for _, s := range collected {
			fmt.Fprintf(b, "%s%s %s\n", name, s.Labels.render(), formatFloat(s.Value))
		}
		return
	}
	for _, k := range sortedKeys(f.values) {
		gv := f.values[k]
		fmt.Fprintf(b, "%s%s %s\n", name, gv.labels.render(), formatFloat(gv.v))
	}
}

func (r *Registry) writeHistogram(b *strings.Builder, name string) {
	f := r.histograms[name]
	writeHeader(b, name, f.help, "histogram")

	for _, k := range sortedKeys(f.values) {
		hv := f.values[k]
		// The format requires CUMULATIVE bucket counts, and Observe already
		// increments every bucket a value falls into, so counts[i] is cumulative
		// as stored.
		for i, bucket := range f.buckets {
			fmt.Fprintf(b, "%s_bucket%s %d\n",
				name, hv.labels.render("le", formatFloat(bucket)), hv.counts[i])
		}
		// +Inf is mandatory and equals the total count.
		fmt.Fprintf(b, "%s_bucket%s %d\n", name, hv.labels.render("le", "+Inf"), hv.count)
		fmt.Fprintf(b, "%s_sum%s %s\n", name, hv.labels.render(), formatFloat(hv.sum))
		fmt.Fprintf(b, "%s_count%s %d\n", name, hv.labels.render(), hv.count)
	}
}

func writeHeader(b *strings.Builder, name, help, typ string) {
	if help != "" {
		// HELP escaping is narrower than label escaping: only backslash and
		// newline.
		esc := strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(help)
		fmt.Fprintf(b, "# HELP %s %s\n", name, esc)
	}
	fmt.Fprintf(b, "# TYPE %s %s\n", name, typ)
}

// sortedKeys gives a family's series a stable emission order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// formatFloat renders a value the way the exposition format expects: shortest
// round-trippable form, with the three special values spelled per spec.
func formatFloat(v float64) string {
	switch {
	case v != v: // NaN
		return "NaN"
	case v > 1.7976931348623157e308:
		return "+Inf"
	case v < -1.7976931348623157e308:
		return "-Inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// ContentType is the media type /metrics must serve.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"
