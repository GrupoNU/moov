package imap_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The architecture rule of ADR-001 / L2 §2.1: internal/imap is the ONLY package
// that may import go-imap.
//
// The rule is enforced two ways on purpose. depguard in .golangci.yml is the
// fast feedback in the editor and in CI lint; this test is the enforcement that
// survives a lint config being loosened, a linter version regressing, or
// golangci-lint being unavailable. It runs with `go test ./...` and needs no
// tooling beyond the standard library.
//
// TestGoIMAPRuleFires below is the proof that the check is live rather than
// vacuously passing while nothing imports go-imap yet.

const goIMAPPath = "github.com/emersion/go-imap"

// allowedPrefixes are the import-path prefixes, relative to the repository
// root, that may import go-imap.
var allowedPrefixes = []string{
	filepath.Join("internal", "imap"),
	// The go-imap spike lives in its own module and is excluded from the main
	// module's build anyway; it is listed for the reader, not for the walk,
	// which never descends into a nested module.
}

func TestGoIMAPIsConfinedToInternalIMAP(t *testing.T) {
	root := repoRoot(t)

	violations := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir(root, path) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if isAllowed(rel) {
			return nil
		}
		for _, imp := range importsOf(t, path) {
			if imp == goIMAPPath || strings.HasPrefix(imp, goIMAPPath+"/") {
				violations = append(violations, filepath.ToSlash(rel)+" imports "+imp)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}

	if len(violations) > 0 {
		t.Errorf("go-imap must only be imported from internal/imap (ADR-001, L2 §2.1).\n"+
			"Wrap what you need behind the Client interface instead. Violations:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// TestGoIMAPRuleFires proves the detection above actually detects. Without it,
// a broken matcher would report success forever — and today, with no production
// code importing go-imap yet, that failure mode would be invisible.
func TestGoIMAPRuleFires(t *testing.T) {
	dir := t.TempDir()
	offender := filepath.Join(dir, "offender.go")
	src := "package offender\n\nimport _ \"" + goIMAPPath + "/v2/imapclient\"\n"
	if err := os.WriteFile(offender, []byte(src), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	imports := importsOf(t, offender)

	found := false
	for _, imp := range imports {
		if strings.HasPrefix(imp, goIMAPPath) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the architecture check failed to see a go-imap import in %v; "+
			"TestGoIMAPIsConfinedToInternalIMAP is not proving anything", imports)
	}

	// And the same file, placed inside internal/imap, must be accepted.
	if !isAllowed(filepath.Join("internal", "imap", "client.go")) {
		t.Error("internal/imap must be allowed to import go-imap")
	}
	if isAllowed(filepath.Join("internal", "sync", "worker.go")) {
		t.Error("internal/sync must NOT be allowed to import go-imap")
	}
}

func isAllowed(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, p := range allowedPrefixes {
		if strings.HasPrefix(rel, filepath.ToSlash(p)+"/") || rel == filepath.ToSlash(p) {
			return true
		}
	}
	return false
}

// skipDir keeps the walk inside the main module: vendored code, nested modules
// (spikes/*, testdata generators) and version control are not ours to police
// here — the spikes are a separate module by design (L2 §2.2).
func skipDir(root, path string) bool {
	base := filepath.Base(path)
	switch base {
	case ".git", "vendor", "node_modules", "testdata", "spikes", "patches":
		return true
	}
	if path == root {
		return false
	}
	// Any directory carrying its own go.mod is a different module.
	if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
		return true
	}
	return false
}

func importsOf(t *testing.T, path string) []string {
	t.Helper()

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	out := make([]string, 0, len(f.Imports))
	for _, spec := range f.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		out = append(out, p)
	}
	return out
}

// repoRoot walks up from the test's working directory until it finds the go.mod
// of the main module.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		candidate := filepath.Join(dir, "go.mod")
		if data, err := os.ReadFile(candidate); err == nil {
			if strings.Contains(string(data), "module github.com/GrupoNU/moov\n") {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate the repository root (go.mod of the main module)")
		}
		dir = parent
	}
}
