package mail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GC-6, pinned as a source-tree invariant (canon §4.1.2): nothing in this
// server may ever AUTO-ANSWER a Disposition-Notification-To request, and
// nothing may put one on outgoing mail. Gmail's posture, adopted whole —
// consumer Gmail refuses MDNs entirely.
//
// A behavioral test cannot pin the ABSENCE of a subsystem, so this test pins
// it the way architecture_test.go pins import purity: by walking the Go
// source. Any new file that mentions the MDN request header trips the test
// and forces a conscious decision in review; today the only sanctioned
// mentions are:
//
//   - internal/parser/gomessage.go — the "message/disposition-notification"
//     MEDIA TYPE, for parsing MDNs that arrive in incoming mail (reading is
//     fine; GC-6 is about answering and requesting);
//   - internal/jmap/mail/email_create.go — the refusal itself: the
//     header:Disposition-Notification-To escape hatch answered with
//     `forbidden`;
//   - this test.
//
// The companion invariant on the web side (web/src/mail/trustInvariants.
// test.ts) does the same walk over the client source.

// mdnAllowedFiles are the module-relative files that may mention the MDN
// header family, each with the reason above.
var mdnAllowedFiles = map[string]bool{
	"internal/parser/gomessage.go":             true,
	"internal/jmap/mail/email_create.go":       true,
	"internal/jmap/mail/mdn_invariant_test.go": true,
	"internal/jmap/mail/email_create_test.go":  true, // the refusal's behavioral test
}

// mdnMarkers are the strings whose appearance means MDN machinery. The header
// names are matched case-insensitively; "auto-answer an MDN" would need at
// least one of them.
var mdnMarkers = []string{
	"disposition-notification",
	"return-receipt-to",
	"x-confirm-reading-to",
}

func TestNoMDNMachineryOutsideTheAllowlist(t *testing.T) {
	root := moduleRoot(t)

	var violations []string
	seenAllowed := map[string]bool{}

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Vendored code and spikes are not this server; testdata is
			// hostile INPUT (the corpus may well contain MDN requests — that
			// is what a parser is for).
			switch d.Name() {
			case "vendor", "spikes", "testdata", "web", ".git", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lower := strings.ToLower(string(content))
		for _, marker := range mdnMarkers {
			if !strings.Contains(lower, marker) {
				continue
			}
			if mdnAllowedFiles[rel] {
				seenAllowed[rel] = true
				continue
			}
			violations = append(violations, rel+" mentions "+marker)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(violations) > 0 {
		t.Errorf("GC-6: MDN machinery may not appear outside the allowlist — "+
			"this server never auto-answers or requests read receipts. Violations:\n  %s",
			strings.Join(violations, "\n  "))
	}

	// The matcher must be proven live, and the refusal must not silently
	// vanish: email_create.go carrying the refusal is what makes the header
	// unmintable from the JMAP surface.
	if !seenAllowed["internal/jmap/mail/email_create.go"] {
		t.Error("email_create.go no longer mentions Disposition-Notification-To — " +
			"the GC-6 refusal has been removed, or this matcher went dead")
	}
}

// moduleRoot walks up from the package directory to go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the package directory")
		}
		dir = parent
	}
}
