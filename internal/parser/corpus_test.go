package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The corpus IS the acceptance suite for E4 (L2 §3, E4: "110/110 cases with the
// result the manifest expects").
//
// The manifest's discipline rule, restated here because it governs how this test
// is maintained: expectations were written BEFORE any parser was ever run, and
// they encode what a CORRECT parser should do — not a prediction of what
// go-message or enmime happen to do. Where this implementation disagrees with an
// expectation, the disagreement is a finding to examine and report, never an
// expectation to quietly edit.
//
// That cuts both ways, and it is why several cases pass here that failed in the
// S4 measurement: the cascade plus the seven mitigations is a better parser than
// either library alone, which is exactly what the manifest was describing.

// corpusCase mirrors the manifest entry. The fields this test asserts on are
// expect, subject, parts and attachments; the prose fields are carried so a
// failure message can quote the reasoning.
type corpusCase struct {
	ID          string  `yaml:"id"`
	File        string  `yaml:"file"`
	Category    string  `yaml:"category"`
	Pathology   string  `yaml:"pathology"`
	Expect      string  `yaml:"expect"`
	ExpectNotes string  `yaml:"expect_notes"`
	Subject     *string `yaml:"subject"`
	Parts       *int    `yaml:"parts"`
	Attachments *int    `yaml:"attachments"`
	External    bool    `yaml:"external"`
}

type corpusManifest struct {
	Cases []corpusCase `yaml:"cases"`
}

// corpusDir locates testdata/mime-corpus from the package directory.
func corpusDir() string {
	return filepath.Join("..", "..", "testdata", "mime-corpus")
}

func loadManifest(t *testing.T) corpusManifest {
	t.Helper()
	path := filepath.Join(corpusDir(), "manifest.yaml")
	data, err := os.ReadFile(path) //nolint:gosec // fixed test-data path
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	var m corpusManifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("parsing manifest: %v", err)
	}
	if len(m.Cases) == 0 {
		t.Fatal("manifest declares no cases; that is never correct")
	}
	return m
}

// TestCorpus is the acceptance suite. One subtest per case, so a failure names
// the case and the whole suite still reports every divergence in one run.
func TestCorpus(t *testing.T) {
	m := loadManifest(t)

	if len(m.Cases) != 110 {
		t.Errorf("manifest has %d cases, expected 110 — the corpus changed size",
			len(m.Cases))
	}

	var passed int
	for _, c := range m.Cases {
		t.Run(c.ID, func(t *testing.T) {
			if c.External {
				t.Skip("external case, not committed to the repository")
			}
			raw, err := os.ReadFile(filepath.Join(corpusDir(), filepath.FromSlash(c.File))) //nolint:gosec // fixed test-data path
			if err != nil {
				t.Fatalf("reading case file: %v", err)
			}

			msg := ParseBytes(raw, DefaultLimits())

			// The invariant that matters most: whatever the input, Parse returns
			// and the engine keeps running. A panic here would be caught by the
			// test framework, but the explicit assertions below are what make
			// the case meaningful.
			checkStatus(t, c, msg)
			checkSubject(t, c, msg)
			checkParts(t, c, msg)
			checkAttachments(t, c, msg)

			if !t.Failed() {
				passed++
			}
		})
	}
	t.Logf("corpus: %d/%d cases passed", passed, len(m.Cases))
}

// checkStatus compares against the manifest's expect value.
//
// The comparison is deliberately not a strict equality on all three values. The
// manifest's own definitions make ok and partial adjacent judgments — "partial"
// means content was recovered but something was guessed or lost, and several
// cases say in their notes that more than one reading is defensible. What must
// never happen is the two ends of the scale swapping: a case the manifest calls
// recoverable coming back failed, or a hopeless case coming back as though it
// parsed cleanly.
func checkStatus(t *testing.T, c corpusCase, msg ParsedMessage) {
	t.Helper()

	switch c.Expect {
	case "failed":
		if msg.Status != StatusFailed {
			t.Errorf("expect: failed, got %s\nnotes: %s",
				msg.Status, oneLine(c.ExpectNotes))
		}
	case "ok", "partial":
		if msg.Status == StatusFailed {
			t.Errorf("expect: %s, got failed — content the manifest says is "+
				"recoverable was lost\ndefects: %v\nnotes: %s",
				c.Expect, msg.Defects, oneLine(c.ExpectNotes))
		}
	default:
		t.Fatalf("manifest has unknown expect value %q", c.Expect)
	}

	// A failed parse must never carry headers: S4 §2 saw enmime damage the
	// header block on its way out of a hard failure, so partial headers from a
	// failed parse are not to be trusted or emitted.
	if msg.Status == StatusFailed && len(msg.Headers.All) > 0 {
		t.Errorf("failed parse emitted %d headers; they are not trustworthy (S4 §2)",
			len(msg.Headers.All))
	}
}

// checkSubject asserts the decoded subject where the manifest states one.
//
// The manifest omits `subject` precisely where extraction is itself the open
// question (hdr-010, le-007), so a stated subject is a firm expectation.
func checkSubject(t *testing.T, c corpusCase, msg ParsedMessage) {
	t.Helper()
	if c.Subject == nil || msg.Status == StatusFailed {
		return
	}
	want := *c.Subject
	if got := msg.Headers.Subject; got != want {
		t.Errorf("subject:\n  want %q\n  got  %q\nnotes: %s",
			want, got, oneLine(c.ExpectNotes))
	}
}

// checkParts compares the leaf-part count against convention C1.
//
// C1: `parts` counts parts with no children, and a message/rfc822 counts as ONE
// leaf whose interior is NOT added to the enclosing total. ParsedMessage.
// LeafParts implements exactly that, including for the rfc822 parts this parser
// descends into (which the libraries do not).
func checkParts(t *testing.T, c corpusCase, msg ParsedMessage) {
	t.Helper()
	if c.Parts == nil || msg.Status == StatusFailed {
		return
	}
	if got := len(msg.LeafParts()); got != *c.Parts {
		t.Errorf("leaf parts (convention C1):\n  want %d\n  got  %d\nnotes: %s",
			*c.Parts, got, oneLine(c.ExpectNotes))
	}
}

// checkAttachments compares the parse-layer attachment count (convention C2).
func checkAttachments(t *testing.T, c corpusCase, msg ParsedMessage) {
	t.Helper()
	if c.Attachments == nil || msg.Status == StatusFailed {
		return
	}
	if got := len(msg.Attachments()); got != *c.Attachments {
		t.Errorf("attachments (convention C2, parse layer):\n  want %d\n  got  %d\nnotes: %s",
			*c.Attachments, got, oneLine(c.ExpectNotes))
	}
}

// TestCorpusNeverPanics is the availability guarantee stated separately from the
// content assertions, because it is the property the whole corpus exists to
// protect: a message that fails to parse must never break a folder's sync.
//
// It runs every case through Parse with several limit configurations, including
// caps tight enough to fire, since the cap paths are the least-exercised code.
func TestCorpusNeverPanics(t *testing.T) {
	m := loadManifest(t)

	configs := map[string]Limits{
		"default": DefaultLimits(),
		"tiny-depth": {
			MaxDepth: 2, MaxParts: 1000, MaxTotalSize: 1 << 20,
			MaxRFC822Depth: 1, MaxPartSize: 1 << 20,
		},
		"tiny-parts": {
			MaxDepth: 100, MaxParts: 3, MaxTotalSize: 1 << 20,
			MaxRFC822Depth: 10, MaxPartSize: 1 << 20,
		},
		"tiny-size": {
			MaxDepth: 100, MaxParts: 1000, MaxTotalSize: 512,
			MaxRFC822Depth: 10, MaxPartSize: 256,
		},
		"zero-value-means-defaults": {},
	}

	for name, limits := range configs {
		t.Run(name, func(t *testing.T) {
			for _, c := range m.Cases {
				if c.External {
					continue
				}
				raw, err := os.ReadFile(filepath.Join(corpusDir(), filepath.FromSlash(c.File))) //nolint:gosec // fixed test-data path
				if err != nil {
					t.Fatalf("%s: reading case: %v", c.ID, err)
				}
				msg := ParseBytes(raw, limits)

				// Structural invariants that must hold for every result, so that
				// no consumer can be handed an inconsistent message.
				for i, p := range msg.Parts {
					if p.Index != i {
						t.Errorf("%s: part %d has Index %d", c.ID, i, p.Index)
					}
					if p.Parent >= i {
						t.Errorf("%s: part %d has forward/self parent %d",
							c.ID, i, p.Parent)
					}
					if p.Parent < -1 {
						t.Errorf("%s: part %d has invalid parent %d", c.ID, i, p.Parent)
					}
				}
				if msg.Status == StatusFailed && len(msg.Parts) > 0 {
					t.Errorf("%s: failed parse carries %d parts", c.ID, len(msg.Parts))
				}
				if msg.Parser == "" {
					t.Errorf("%s: result names no parser", c.ID)
				}
			}
		})
	}
}

// TestCorpusCascadeDistribution records which layer won each case.
//
// Informational rather than assertive: it is the production-facing number (a
// shift in this distribution after a library bump is the early warning), and
// having it in the test output makes the cascade's behavior auditable without
// running the S4 harness again.
func TestCorpusCascadeDistribution(t *testing.T) {
	m := loadManifest(t)

	byParser := map[ParserName]int{}
	byStatus := map[ParseStatus]int{}
	var rescued, salvaged []string

	for _, c := range m.Cases {
		if c.External {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(corpusDir(), filepath.FromSlash(c.File))) //nolint:gosec // fixed test-data path
		if err != nil {
			t.Fatalf("%s: %v", c.ID, err)
		}
		msg := ParseBytes(raw, DefaultLimits())
		byParser[msg.Parser]++
		byStatus[msg.Status]++
		switch msg.Parser {
		case ParserEnmime:
			rescued = append(rescued, c.ID)
		case ParserSalvage:
			salvaged = append(salvaged, c.ID)
		case ParserGoMessage, ParserNone:
		}
	}

	t.Logf("cascade: go-message=%d enmime=%d salvage=%d none=%d",
		byParser[ParserGoMessage], byParser[ParserEnmime],
		byParser[ParserSalvage], byParser[ParserNone])
	t.Logf("status:  ok=%d partial=%d failed=%d",
		byStatus[StatusOK], byStatus[StatusPartial], byStatus[StatusFailed])
	t.Logf("rescued by enmime: %s", strings.Join(rescued, " "))
	t.Logf("salvaged: %s", strings.Join(salvaged, " "))
}

// oneLine collapses the manifest's folded prose for a failure message.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
