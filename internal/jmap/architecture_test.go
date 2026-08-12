package jmap_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The architecture rule of L2-jmap-server §4: internal/jmap defines the
// protocol contracts and MUST NOT import the store, the blob layer, the
// parser, IMAP, or any driver. The dependency arrow points the other way:
// internal/jmap/mail (J2/J3) implements this package's interfaces OVER those
// packages, and internal/jmaphttp wires them together.
//
// Same double enforcement as internal/imap's confinement rule: depguard in
// .golangci.yml for editor/CI feedback, and this test so the rule survives a
// lint config change. TestJMAPPurityRuleFires proves the matcher is live.

// forbiddenPrefixes are import-path prefixes internal/jmap may never use.
var forbiddenPrefixes = []string{
	"github.com/GrupoNU/moov/internal/store",
	"github.com/GrupoNU/moov/internal/blob",
	"github.com/GrupoNU/moov/internal/parser",
	"github.com/GrupoNU/moov/internal/imap",
	"github.com/GrupoNU/moov/internal/sync",
	"github.com/GrupoNU/moov/internal/mailcow",
	"github.com/GrupoNU/moov/internal/crypto",
	"github.com/GrupoNU/moov/internal/config",
	"github.com/GrupoNU/moov/internal/jmaphttp",
	"github.com/jackc/pgx",
	"github.com/emersion/go-imap",
}

func TestJMAPCoreImportsNothingBelowTheContract(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	var violations []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		for _, imp := range importsOf(t, filepath.Join(dir, e.Name())) {
			if bad := matchForbidden(imp); bad != "" {
				violations = append(violations, e.Name()+" imports "+imp)
			}
		}
	}
	if len(violations) > 0 {
		t.Errorf("internal/jmap is the protocol contract and must not import "+
			"storage, parsing or transport packages (L2 §4). Violations:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// TestJMAPPurityRuleFires proves the matcher detects a violation instead of
// passing vacuously.
func TestJMAPPurityRuleFires(t *testing.T) {
	if matchForbidden("github.com/GrupoNU/moov/internal/store") == "" {
		t.Fatal("the matcher does not flag a direct store import")
	}
	if matchForbidden("github.com/jackc/pgx/v5/pgxpool") == "" {
		t.Fatal("the matcher does not flag a pgx subpackage import")
	}
	if matchForbidden("encoding/json") != "" {
		t.Fatal("the matcher flags the standard library")
	}
}

func matchForbidden(imp string) string {
	for _, p := range forbiddenPrefixes {
		if imp == p || strings.HasPrefix(imp, p+"/") {
			return p
		}
	}
	return ""
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
