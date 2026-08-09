// Command corpuscheck validates the pathological MIME corpus against its
// manifest, and validates that git is not rewriting the corpus bytes.
//
// The corpus (testdata/mime-corpus/) is the permanent regression suite of the
// MIME parser, and it has two silent failure modes that no unit test would ever
// catch:
//
//  1. Drift. A case is added to disk without a manifest entry, or a manifest
//     entry outlives the file it describes. Either way the suite quietly stops
//     covering what everyone believes it covers.
//
//  2. End-of-line normalization. The .eml files are BYTE-EXACT vectors: a case
//     testing "LF-only message" or "CR-only message" tests nothing the moment
//     git converts its line endings. .gitattributes marks *.eml as -text to
//     prevent that, and .gitignore carries an explicit negation because the
//     repository otherwise ignores *.eml. Both are load-bearing, and both are
//     one careless edit away from disappearing.
//
// This tool is that guard, run on every push and pull request. It is written in
// Go rather than shell so it behaves identically on a developer's Windows
// machine and on Linux CI — which matters more than usual here, since the bug
// class it defends against IS the Windows/Linux line-ending difference.
//
// Usage:
//
//	corpuscheck [-corpus DIR] [-manifest FILE] [-skip-eol]
//
// Exit status is 0 when the corpus is consistent, 1 when it is not.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// manifest mirrors the parts of manifest.yaml this tool needs. The file carries
// considerably more per case (pathology, expect_notes, parts, attachments); the
// parser layer's own tests consume those. Here we need identity and location.
type manifest struct {
	Cases []struct {
		ID         string `yaml:"id"`
		File       string `yaml:"file"`
		Category   string `yaml:"category"`
		Provenance string `yaml:"provenance"`
		Expect     string `yaml:"expect"`
		External   bool   `yaml:"external"`
	} `yaml:"cases"`
}

func main() {
	corpus := flag.String("corpus", filepath.Join("testdata", "mime-corpus"),
		"path to the corpus directory")
	manifestPath := flag.String("manifest", "",
		"path to manifest.yaml (default: <corpus>/manifest.yaml)")
	skipEOL := flag.Bool("skip-eol", false,
		"skip the git end-of-line check (for use outside a git work tree)")
	flag.Parse()

	if *manifestPath == "" {
		*manifestPath = filepath.Join(*corpus, "manifest.yaml")
	}

	problems, err := check(*corpus, *manifestPath, *skipEOL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "corpuscheck: %v\n", err)
		os.Exit(1)
	}
	if len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "corpuscheck: %d problem(s) found\n\n", len(problems))
		for _, p := range problems {
			fmt.Fprintf(os.Stderr, "  - %s\n", p)
		}
		fmt.Fprintf(os.Stderr, "\nThe MIME corpus is a specification, not a scratch directory.\n"+
			"See %s and testdata/mime-corpus/README.md.\n", *manifestPath)
		os.Exit(1)
	}

	fmt.Println("corpuscheck: corpus and manifest agree; no end-of-line normalization detected")
}

func check(corpusDir, manifestPath string, skipEOL bool) ([]string, error) {
	m, err := readManifest(manifestPath)
	if err != nil {
		return nil, err
	}
	if len(m.Cases) == 0 {
		return nil, fmt.Errorf("%s declares no cases; that is never correct", manifestPath)
	}

	problems := make([]string, 0)

	// --- Manifest -> disk, plus internal consistency of the manifest itself.
	declared := make(map[string]string, len(m.Cases)) // normalized path -> case id
	seenIDs := make(map[string]bool, len(m.Cases))

	for _, c := range m.Cases {
		switch {
		case c.ID == "":
			problems = append(problems, fmt.Sprintf("a case with file %q has no id", c.File))
			continue
		case c.File == "":
			problems = append(problems, fmt.Sprintf("case %s has no file", c.ID))
			continue
		}

		if seenIDs[c.ID] {
			problems = append(problems, fmt.Sprintf("case id %s is declared more than once; ids are stable and never reused", c.ID))
		}
		seenIDs[c.ID] = true

		switch c.Expect {
		case "ok", "partial", "failed":
		default:
			problems = append(problems, fmt.Sprintf("case %s: expect is %q, want ok|partial|failed", c.ID, c.Expect))
		}

		rel := filepath.ToSlash(c.File)
		if prev, dup := declared[rel]; dup {
			problems = append(problems, fmt.Sprintf("cases %s and %s both claim file %s", prev, c.ID, rel))
		}
		declared[rel] = c.ID

		// External cases are fetched at test time by fetch-external.sh and are
		// deliberately not committed, so their absence on disk is expected.
		if c.External {
			continue
		}

		full := filepath.Join(corpusDir, filepath.FromSlash(c.File))
		info, statErr := os.Stat(full)
		switch {
		case errors.Is(statErr, fs.ErrNotExist):
			problems = append(problems, fmt.Sprintf("case %s declares %s, which does not exist on disk", c.ID, rel))
		case statErr != nil:
			return nil, fmt.Errorf("stat %s: %w", full, statErr)
		case info.IsDir():
			problems = append(problems, fmt.Sprintf("case %s declares %s, which is a directory", c.ID, rel))
		case info.Size() == 0 && c.Expect != "failed":
			// A zero-byte file is normally a truncated checkout or a botched
			// commit. It is legitimate for exactly one kind of case: the corpus
			// contains a deliberate zero-byte message (an interrupted APPEND, a
			// failed download), and a message with nothing in it can only be
			// expect: failed. Anything else claiming to be empty is a mistake.
			problems = append(problems, fmt.Sprintf(
				"case %s declares %s, which is empty on disk; an empty vector tests nothing "+
					"unless the case is deliberately zero-byte, in which case it must be expect: failed",
				c.ID, rel))
		}
	}

	// --- Disk -> manifest.
	onDisk, err := findEML(corpusDir)
	if err != nil {
		return nil, err
	}
	if len(onDisk) == 0 {
		return nil, fmt.Errorf("no .eml files found under %s; is the path right?", corpusDir)
	}
	for _, rel := range onDisk {
		if _, ok := declared[rel]; !ok {
			problems = append(problems, fmt.Sprintf("%s is on disk but has no manifest entry; every case must be described", rel))
		}
	}

	// --- Byte fidelity.
	if !skipEOL {
		eolProblems, err := checkEOL(corpusDir)
		if err != nil {
			return nil, err
		}
		problems = append(problems, eolProblems...)
	}

	sort.Strings(problems)
	return problems, nil
}

func readManifest(path string) (*manifest, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is an operator-supplied flag
	if err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}
	var m manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &m, nil
}

// findEML returns every .eml under dir, as slash-separated paths relative to
// dir. The external/ directory is skipped: it holds third-party material
// fetched at test time and is git-ignored by design.
func findEML(dir string) ([]string, error) {
	out := make([]string, 0, 128)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "external" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".eml") {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", dir, err)
	}
	sort.Strings(out)
	return out, nil
}

// checkEOL asks git itself whether it considers the corpus files text.
//
// `git ls-files --eol` prints, per file, the index and working-tree line-ending
// state plus the effective attribute:
//
//	i/crlf  w/crlf  attr/-text      testdata/mime-corpus/01-nesting/001-....eml
//
// The attribute must be -text for every .eml. Anything else — text=auto, text,
// eol=lf — means git is free to rewrite the bytes of a vector whose entire
// purpose may be its line endings.
func checkEOL(dir string) ([]string, error) {
	cmd := exec.Command("git", "ls-files", "--eol", "--", dir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files --eol failed (%w): %s\n"+
			"If this is not a git work tree, pass -skip-eol", err, strings.TrimSpace(stderr.String()))
	}

	problems := make([]string, 0)
	checked := 0

	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		// The path is separated from the fields by a tab.
		fields, path, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		path = strings.TrimSpace(path)
		if !strings.EqualFold(filepath.Ext(path), ".eml") {
			continue
		}
		checked++

		attr := ""
		for _, f := range strings.Fields(fields) {
			if strings.HasPrefix(f, "attr/") {
				attr = strings.TrimPrefix(f, "attr/")
			}
		}
		if attr != "-text" {
			if attr == "" {
				attr = "(none)"
			}
			problems = append(problems, fmt.Sprintf(
				"%s has eol attribute %q, want \"-text\"; git may normalize its line endings "+
					"and silently invalidate the vector (see .gitattributes)", path, attr))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading git output: %w", err)
	}

	if checked == 0 {
		problems = append(problems, fmt.Sprintf(
			"git reported no tracked .eml files under %s; either the corpus is untracked "+
				"or .gitignore is hiding it (the *.eml rule needs its negation)", dir))
	}
	return problems, nil
}
