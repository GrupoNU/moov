package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A guard that never fires is a guard nobody can trust. These tests build small
// synthetic corpora on disk and assert that each failure mode is reported. The
// end-of-line check is exercised separately against the real repository, since
// it needs a git work tree.

func writeCorpus(t *testing.T, files map[string]string, manifest string) (dir, manifestPath string) {
	t.Helper()

	dir = t.TempDir()
	for rel, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	manifestPath = filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return dir, manifestPath
}

func TestCheckAcceptsAConsistentCorpus(t *testing.T) {
	dir, mf := writeCorpus(t,
		map[string]string{"01-nesting/001-a.eml": "Subject: a\r\n\r\nbody\r\n"},
		`cases:
  - id: nest-001
    file: 01-nesting/001-a.eml
    expect: ok
`)

	problems, err := check(dir, mf, true)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(problems) != 0 {
		t.Errorf("consistent corpus reported problems: %v", problems)
	}
}

func TestCheckDetectsDrift(t *testing.T) {
	tests := []struct {
		name     string
		files    map[string]string
		manifest string
		want     string
	}{
		{
			name:  "manifest entry without a file",
			files: map[string]string{"a/001-a.eml": "x"},
			manifest: `cases:
  - id: c-001
    file: a/001-a.eml
    expect: ok
  - id: c-002
    file: a/002-missing.eml
    expect: ok
`,
			want: "does not exist on disk",
		},
		{
			name: "file without a manifest entry",
			files: map[string]string{
				"a/001-a.eml":          "x",
				"a/002-undeclared.eml": "x",
			},
			manifest: `cases:
  - id: c-001
    file: a/001-a.eml
    expect: ok
`,
			want: "no manifest entry",
		},
		{
			name:  "duplicate case id",
			files: map[string]string{"a/001-a.eml": "x", "a/002-b.eml": "x"},
			manifest: `cases:
  - id: c-001
    file: a/001-a.eml
    expect: ok
  - id: c-001
    file: a/002-b.eml
    expect: ok
`,
			want: "declared more than once",
		},
		{
			name:  "invalid expect value",
			files: map[string]string{"a/001-a.eml": "x"},
			manifest: `cases:
  - id: c-001
    file: a/001-a.eml
    expect: probably
`,
			want: "want ok|partial|failed",
		},
		{
			name:  "unexpectedly empty vector",
			files: map[string]string{"a/001-a.eml": ""},
			manifest: `cases:
  - id: c-001
    file: a/001-a.eml
    expect: ok
`,
			want: "which is empty on disk",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, mf := writeCorpus(t, tt.files, tt.manifest)

			problems, err := check(dir, mf, true)
			if err != nil {
				t.Fatalf("check: %v", err)
			}
			if !containsSubstring(problems, tt.want) {
				t.Errorf("problems = %v, want one containing %q", problems, tt.want)
			}
		})
	}
}

// The corpus deliberately contains one zero-byte message — an interrupted
// APPEND, a failed download — and it is the corpus's single `expect: failed`
// case. Emptiness is only a problem when the case does not expect failure.
func TestCheckAllowsADeliberateZeroByteCase(t *testing.T) {
	dir, mf := writeCorpus(t,
		map[string]string{"07-structural/002-empty-file.eml": ""},
		`cases:
  - id: structural-002
    file: 07-structural/002-empty-file.eml
    expect: failed
`)

	problems, err := check(dir, mf, true)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(problems) != 0 {
		t.Errorf("the deliberate zero-byte case was reported as a problem: %v", problems)
	}
}

// An external case is fetched at test time, so being absent from disk is
// correct and must not be reported.
func TestCheckToleratesExternalCases(t *testing.T) {
	dir, mf := writeCorpus(t,
		map[string]string{"a/001-a.eml": "x"},
		`cases:
  - id: c-001
    file: a/001-a.eml
    expect: ok
  - id: c-002
    file: 09-real-world/torture.eml
    external: true
    expect: partial
`)

	problems, err := check(dir, mf, true)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(problems) != 0 {
		t.Errorf("external case was reported as a problem: %v", problems)
	}
}

// The real corpus must pass, end-of-line check included. This is the assertion
// that matters most: it runs the tool against the actual 110 committed vectors
// in the actual git work tree.
func TestRealCorpusIsConsistent(t *testing.T) {
	root := repoRoot(t)
	corpus := filepath.Join(root, "testdata", "mime-corpus")
	if _, err := os.Stat(corpus); err != nil {
		t.Skipf("corpus not found at %s: %v", corpus, err)
	}

	// git ls-files resolves paths relative to the work tree, so run from root.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir %s: %v", root, err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	problems, err := check(filepath.Join("testdata", "mime-corpus"),
		filepath.Join("testdata", "mime-corpus", "manifest.yaml"), false)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(problems) != 0 {
		t.Errorf("the committed corpus is inconsistent:\n  %s", strings.Join(problems, "\n  "))
	}
}

func containsSubstring(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.Contains(h, needle) {
			return true
		}
	}
	return false
}

func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && strings.Contains(string(data), "module github.com/GrupoNU/moov\n") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate the repository root")
		}
		dir = parent
	}
}
