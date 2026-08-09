// Command harness runs the Moov pathological MIME corpus through both
// candidate Go parsers and records, for every (case x parser) pair, how the
// parser behaved.
//
// Why two parsers: the sync engine's operating rule (research 04 §4.2) is that
// a message that fails to parse must never break a folder's sync. The plan is
// a primary streaming parser (emersion/go-message) with a tree-based fallback
// (jhillyerd/enmime), on the hypothesis that the two fail on DIFFERENT broken
// mail. This harness is how that hypothesis gets tested rather than assumed.
//
// Outcome taxonomy, worst-first:
//
//	panic   — the parser panicked. A panic inside the sync engine's parse path
//	          is a DoS vector: one crafted message kills the worker.
//	timeout — the parser did not return within the watchdog. Worse than a
//	          crash: a hung goroutine holds its memory and its folder forever.
//	          This is the most plausible mechanism behind eternally-open
//	          "Incomplete sync" bugs.
//	error   — the parser returned an error. Fine: the engine marks
//	          parse_status='failed', stores the raw blob, and moves on.
//	defects — enmime parsed but reported defects (its recovery path), or
//	          go-message returned a soft/unknown-charset error. Usable output.
//	ok      — clean parse.
//
// Each case runs in its own subprocess-free goroutine with a recover() and a
// watchdog. A goroutine that times out is abandoned, not killed (Go cannot
// kill a goroutine); the harness keeps going and reports the leak. Run the
// whole thing under a container memory cap so the nesting bombs cannot take
// the host down.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // registers non-UTF-8 charset decoders
	"github.com/emersion/go-message/mail"
	"github.com/jhillyerd/enmime/v2"
)

type Outcome string

const (
	OutcomeOK      Outcome = "ok"
	OutcomeDefects Outcome = "defects"
	OutcomeError   Outcome = "error"
	OutcomePanic   Outcome = "panic"
	OutcomeTimeout Outcome = "timeout"
)

// Result is one (case x parser) observation.
type Result struct {
	Case    string  `json:"case"`    // "01-nesting/001-nested-multipart-10.eml"
	Parser  string  `json:"parser"`  // "go-message" | "enmime"
	Outcome Outcome `json:"outcome"`
	Detail  string  `json:"detail,omitempty"` // error text / panic value / defect summary

	Subject    string `json:"subject,omitempty"`
	Parts      int    `json:"parts"`       // leaf parts walked
	Attach     int    `json:"attachments"` // parts with attachment disposition
	TextBytes  int    `json:"text_bytes"`  // decoded text/* bytes recovered
	DurationMs int64  `json:"duration_ms"`
	AllocMB    int64  `json:"alloc_mb"` // heap delta observed during the parse

	// Severe is true when enmime flagged at least one defect as severe.
	// enmime's own contract: severe defects mean the output is questionable,
	// warnings mean it recovered cleanly. The fallback policy cares about the
	// difference — a severe-defect parse should be cross-checked, not trusted.
	Severe bool `json:"severe,omitempty"`
}

var (
	// maxDepth bounds the harness's OWN walk of go-message parts, as a
	// backstop against a malicious depth this corpus does not contain.
	//
	// It MUST stay above the deepest corpus case (500), or the harness's limit
	// masks the parser's real behavior and shows up as a parser failure that
	// is actually ours. That mistake was made once during this spike: at
	// maxDepth=100 the 500-deep case was recorded as "go-message hard-fails"
	// when go-message in fact walks all 500 levels without complaint. Any
	// result attributable to this limit is a harness artifact and must be
	// reported as such, never as a parser finding.
	maxDepth = 2000

	// maxParts bounds how many parts the harness walks, for the same reason.
	maxParts = 5000

	// harnessLimitHit records cases where one of the two limits above bound
	// the walk, so they can be excluded from parser findings.
	harnessLimitPrefix = "harness "

	timedOut atomic.Int64 // count of abandoned goroutines
)

func main() {
	corpus := flag.String("corpus", "../../testdata/mime-corpus", "corpus root")
	outJSON := flag.String("json", "results.json", "raw results output")
	watchdog := flag.Duration("timeout", 10*time.Second, "per-parse watchdog")
	flag.Parse()

	cases, err := findCases(*corpus)
	if err != nil {
		fmt.Fprintln(os.Stderr, "corpus scan:", err)
		os.Exit(1)
	}
	fmt.Printf("corpus: %d cases under %s\n", len(cases), *corpus)

	var results []Result
	for _, c := range cases {
		raw, err := os.ReadFile(filepath.Join(*corpus, c))
		if err != nil {
			fmt.Fprintln(os.Stderr, "read:", err)
			continue
		}
		results = append(results,
			run(c, "go-message", raw, *watchdog, parseGoMessage),
			run(c, "enmime", raw, *watchdog, parseEnmime),
		)
	}

	f, err := os.Create(*outJSON)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(results); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	f.Close()

	summarize(results)
	if n := timedOut.Load(); n > 0 {
		fmt.Printf("\nWARNING: %d parse goroutines were abandoned after timing out\n", n)
	}
	fmt.Printf("raw results: %s\n", *outJSON)
}

// findCases returns corpus-relative paths of every .eml, sorted.
func findCases(root string) ([]string, error) {
	var out []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(p, ".eml") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out, err
}

type parseFn func(raw []byte) Result

// run executes fn under a watchdog and a recover(). The parse runs in its own
// goroutine so a hang does not stop the harness; the goroutine is abandoned on
// timeout (Go offers no way to kill one) and that abandonment is counted,
// because in the real sync engine it is a permanent resource leak.
func run(caseName, parser string, raw []byte, watchdog time.Duration, fn parseFn) Result {
	done := make(chan Result, 1)
	start := time.Now()

	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- Result{
					Outcome: OutcomePanic,
					Detail:  fmt.Sprintf("%v", r),
				}
			}
		}()
		done <- fn(raw)
	}()

	var res Result
	select {
	case res = <-done:
		res.DurationMs = time.Since(start).Milliseconds()
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		if after.TotalAlloc > before.TotalAlloc {
			res.AllocMB = int64((after.TotalAlloc - before.TotalAlloc) / (1024 * 1024))
		}
	case <-time.After(watchdog):
		timedOut.Add(1)
		res = Result{
			Outcome:    OutcomeTimeout,
			Detail:     fmt.Sprintf("no result after %s; goroutine abandoned", watchdog),
			DurationMs: watchdog.Milliseconds(),
		}
	}
	res.Case = caseName
	res.Parser = parser
	return res
}

// ---------------------------------------------------------------------------
// go-message (streaming)
// ---------------------------------------------------------------------------

// parseGoMessage reads the message with emersion/go-message and walks every
// part, decoding text bodies — i.e. it does what the sync engine would do,
// not merely what a smoke test would do. Header decoding uses the mail
// package's WordDecoder so RFC 2047 handling is exercised.
func parseGoMessage(raw []byte) Result {
	res := Result{Outcome: OutcomeOK}

	ent, err := message.Read(strings.NewReader(string(raw)))
	if err != nil {
		if message.IsUnknownCharset(err) {
			// go-message's documented soft failure: structure is fine, the
			// charset could not be resolved. The engine can still use this.
			res.Outcome = OutcomeDefects
			res.Detail = "unknown charset: " + err.Error()
		} else {
			return Result{Outcome: OutcomeError, Detail: err.Error()}
		}
	}
	if ent == nil {
		return Result{Outcome: OutcomeError, Detail: "nil entity with nil error"}
	}

	// Subject via the mail header (applies RFC 2047 decoding).
	mh := mail.Header{Header: ent.Header}
	if subj, serr := mh.Subject(); serr == nil {
		res.Subject = subj
	} else {
		res.Subject = ent.Header.Get("Subject")
		if res.Outcome == OutcomeOK {
			res.Outcome = OutcomeDefects
			res.Detail = "subject decode: " + serr.Error()
		}
	}

	var walk func(e *message.Entity, depth int) error
	walk = func(e *message.Entity, depth int) error {
		if depth > maxDepth {
			return fmt.Errorf(harnessLimitPrefix+"depth limit %d exceeded", maxDepth)
		}
		if res.Parts > maxParts {
			return fmt.Errorf(harnessLimitPrefix+"part limit %d exceeded", maxParts)
		}

		if mr := e.MultipartReader(); mr != nil {
			for {
				p, err := mr.NextPart()
				if err == io.EOF {
					return nil
				}
				if err != nil {
					if message.IsUnknownCharset(err) {
						if res.Outcome == OutcomeOK {
							res.Outcome = OutcomeDefects
							res.Detail = "part: " + err.Error()
						}
						continue
					}
					return err
				}
				if err := walk(p, depth+1); err != nil {
					return err
				}
			}
		}

		// Leaf part.
		res.Parts++
		ct, _, _ := e.Header.ContentType()
		disp, dparams, _ := e.Header.ContentDisposition()
		if disp == "attachment" || (dparams != nil && dparams["filename"] != "") {
			res.Attach++
		}
		// Read the body — this is where CTE decoding and charset conversion
		// actually happen, and where a lying CTE bites.
		//
		// io.ReadAll returns the bytes read so far ALONGSIDE the error. Those
		// bytes must be kept: on a lying base64 body the decoder yields real
		// content right up to the offending byte, and discarding it on error
		// turns a partial decode into total data loss. The sync engine must
		// make the same choice — this is a caller bug waiting to happen, not
		// a parser limitation.
		b, err := io.ReadAll(io.LimitReader(e.Body, 8<<20))
		if strings.HasPrefix(ct, "text/") {
			res.TextBytes += len(b)
		}
		if err != nil {
			if res.Outcome == OutcomeOK {
				res.Outcome = OutcomeDefects
				res.Detail = fmt.Sprintf("body read: %v (kept %d partial bytes)", err, len(b))
			}
			return nil // a bad leaf must not abort the whole walk
		}
		return nil
	}

	if err := walk(ent, 0); err != nil {
		if res.Parts > 0 {
			// Partial recovery: some parts were extracted before the failure.
			res.Outcome = OutcomeDefects
			res.Detail = "walk aborted: " + err.Error()
		} else {
			return Result{Outcome: OutcomeError, Detail: "walk: " + err.Error(), Subject: res.Subject}
		}
	}
	return res
}

// ---------------------------------------------------------------------------
// enmime (tree-based)
// ---------------------------------------------------------------------------

// parseEnmime uses ReadEnvelope, enmime's recovering front door. enmime's
// distinguishing feature is that it reports defects rather than failing: a
// non-empty Errors slice with only warning-severity entries still yields a
// usable envelope.
func parseEnmime(raw []byte) Result {
	res := Result{Outcome: OutcomeOK}

	env, err := enmime.ReadEnvelope(strings.NewReader(string(raw)))
	if err != nil {
		return Result{Outcome: OutcomeError, Detail: err.Error()}
	}
	if env == nil {
		return Result{Outcome: OutcomeError, Detail: "nil envelope with nil error"}
	}

	res.Subject = env.GetHeader("Subject")
	res.TextBytes = len(env.Text)
	res.Attach = len(env.Attachments) + len(env.Inlines)

	// Count leaf parts by walking the root.
	var count func(p *enmime.Part, depth int)
	count = func(p *enmime.Part, depth int) {
		if p == nil || depth > maxDepth || res.Parts > maxParts {
			return
		}
		if p.FirstChild == nil {
			res.Parts++
		}
		for c := p.FirstChild; c != nil; c = c.NextSibling {
			count(c, depth+1)
		}
	}
	count(env.Root, 0)

	if len(env.Errors) > 0 {
		res.Outcome = OutcomeDefects
		var msgs []string
		for _, e := range env.Errors {
			sev := "warn"
			if e.Severe {
				sev = "SEVERE"
				res.Severe = true
			}
			msgs = append(msgs, fmt.Sprintf("[%s] %s: %s", sev, e.Name, e.Detail))
		}
		res.Detail = strings.Join(msgs, " | ")
		if len(res.Detail) > 400 {
			res.Detail = res.Detail[:400] + "…"
		}
	}
	return res
}

// ---------------------------------------------------------------------------
// summary
// ---------------------------------------------------------------------------

func summarize(rs []Result) {
	byCase := map[string]map[string]Result{}
	for _, r := range rs {
		if byCase[r.Case] == nil {
			byCase[r.Case] = map[string]Result{}
		}
		byCase[r.Case][r.Parser] = r
	}

	var bothHard, onlyGM, onlyEN, bothOK int
	var panics, timeouts []string

	cases := make([]string, 0, len(byCase))
	for c := range byCase {
		cases = append(cases, c)
	}
	sort.Strings(cases)

	hard := func(r Result) bool {
		return r.Outcome == OutcomeError || r.Outcome == OutcomePanic || r.Outcome == OutcomeTimeout
	}

	for _, c := range cases {
		gm, en := byCase[c]["go-message"], byCase[c]["enmime"]
		switch {
		case hard(gm) && hard(en):
			bothHard++
		case hard(gm):
			onlyGM++
		case hard(en):
			onlyEN++
		default:
			bothOK++
		}
		for _, r := range []Result{gm, en} {
			if r.Outcome == OutcomePanic {
				panics = append(panics, r.Parser+" "+c+": "+r.Detail)
			}
			if r.Outcome == OutcomeTimeout {
				timeouts = append(timeouts, r.Parser+" "+c)
			}
		}
	}

	fmt.Println()
	fmt.Println("=== SUMMARY ===")
	fmt.Printf("cases                         : %d\n", len(cases))
	fmt.Printf("both parsers hard-fail        : %d  (-> parse_status='failed' + raw blob)\n", bothHard)
	fmt.Printf("only go-message hard-fails    : %d  (enmime rescues)\n", onlyGM)
	fmt.Printf("only enmime hard-fails        : %d  (go-message rescues)\n", onlyEN)
	fmt.Printf("neither hard-fails            : %d\n", bothOK)
	fmt.Printf("panics                        : %d\n", len(panics))
	for _, p := range panics {
		fmt.Println("   PANIC  " + p)
	}
	fmt.Printf("timeouts                      : %d\n", len(timeouts))
	for _, t := range timeouts {
		fmt.Println("   HANG   " + t)
	}
}
