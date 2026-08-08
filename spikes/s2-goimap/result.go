package main

import (
	"fmt"
	"strings"
)

// Result accumulates the outcome of one spike test: a verdict, human-readable
// evidence notes, and (for raw-protocol tests) a full wire transcript.
type Result struct {
	Name       string
	Failures   []string
	Notes      []string
	Transcript []string
}

func newResult(name string) *Result {
	return &Result{Name: name}
}

func (r *Result) note(format string, args ...any) *Result {
	r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
	return r
}

func (r *Result) fail(format string, args ...any) *Result {
	r.Failures = append(r.Failures, fmt.Sprintf(format, args...))
	return r
}

func (r *Result) passed() bool { return len(r.Failures) == 0 }

func (r *Result) print() {
	verdict := "PASS"
	if !r.passed() {
		verdict = "FAIL"
	}
	fmt.Printf("\n==================== %s: %s ====================\n", r.Name, verdict)
	if len(r.Failures) > 0 {
		fmt.Println("-- FAILURES --")
		for _, f := range r.Failures {
			fmt.Printf("  [FAIL] %s\n", f)
		}
	}
	fmt.Println("-- EVIDENCE --")
	for _, n := range r.Notes {
		fmt.Printf("  %s\n", n)
	}
	if len(r.Transcript) > 0 {
		fmt.Println("-- TRANSCRIPT (password redacted) --")
		fmt.Println(strings.Join(r.Transcript, "\n"))
	}
	fmt.Printf("==================== end %s ====================\n", r.Name)
}
