package main

import (
	"fmt"
	"io"
)

// outf and outln write to a stream, discarding the write error.
//
// They exist because errcheck is enabled for the whole repository — correctly,
// since an unchecked error is the classic Go bug — while a failed write to a
// CLI's stdout is the one case where there is genuinely nothing to do: the
// channel used to report the problem is the one that just failed. Rather than
// scattering `_ =` across every print, which makes the deliberate choice
// indistinguishable from an oversight, the decision is made once, here, with
// the reasoning attached.
//
// The one place a write error DOES matter is the tabwriter in account list,
// whose Flush error is checked: a partial table is a wrong answer rather than
// an undelivered message.
func outf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func outln(w io.Writer, args ...any) {
	_, _ = fmt.Fprintln(w, args...)
}

func out(w io.Writer, s string) {
	_, _ = io.WriteString(w, s)
}
