package main

import (
	"strings"
	"testing"

	"github.com/GrupoNU/moov/internal/metrics"
	"github.com/GrupoNU/moov/internal/submit"
)

// submissionMetrics is the ONE place internal/submit's result vocabulary,
// internal/jmap/mail's cancel signal and internal/metrics' label values meet.
// Neither of the first two imports the exporter — that is deliberate (an
// executor that needs a metrics registry to be correct is untestable) — and
// the cost of the decoupling is exactly this: three string constants that must
// agree and no compiler to check them. This test is the check.
func TestSubmissionResultConstantsAgree(t *testing.T) {
	if submit.ResultSent != metrics.SubmissionSent {
		t.Errorf("submit.ResultSent = %q, metrics.SubmissionSent = %q; the labels must match",
			submit.ResultSent, metrics.SubmissionSent)
	}
	if submit.ResultFailed != metrics.SubmissionFailed {
		t.Errorf("submit.ResultFailed = %q, metrics.SubmissionFailed = %q; the labels must match",
			submit.ResultFailed, metrics.SubmissionFailed)
	}
}

// The adapter must route each seam to the right label, and a nil metric set
// must be inert rather than a panic: startOutbox is reachable in tests and
// tools that build no registry.
func TestSubmissionMetricsAdapter(t *testing.T) {
	m := metrics.New()
	obs := submissionMetrics{m}

	obs.SubmissionFinished(submit.ResultSent)
	obs.SubmissionFinished(submit.ResultFailed)
	obs.SubmissionCanceled()

	var sb strings.Builder
	if err := m.Registry().Write(&sb); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := sb.String()

	for _, want := range []string{
		`moov_submissions_total{result="sent"} 1`,
		`moov_submissions_total{result="failed"} 1`,
		`moov_submissions_total{result="canceled"} 1`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("exposition is missing %q\ngot:\n%s", want, got)
		}
	}

	// A zero-value adapter carries no registry and must stay silent.
	var nilObs submissionMetrics
	nilObs.SubmissionFinished(submit.ResultSent)
	nilObs.SubmissionCanceled()
}
