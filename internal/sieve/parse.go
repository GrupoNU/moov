package sieve

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Parsing the managed script back into the model, and importing foreign
// scripts without destroying a byte of them.
//
// The metadata block is authoritative — the same call Bulwark's design makes
// (docs/research/05 §1.3: "parseScript() prefers the metadata block"). The
// generated Sieve between the metadata and the external section is DERIVED
// output; it is never re-parsed into editables. What protects a hand-edited
// script from being silently regenerated over is the drift check: ParseManaged
// reports whether the stored bytes still equal what the parsed model
// generates, and the manager backs the drifted bytes up before pushing.

// ErrNotManaged reports content without a Moov metadata header: a foreign
// script (hand-written, SOGo's, Bulwark's). Foreign content is never parsed
// into editables — it is imported verbatim (ImportForeign).
var ErrNotManaged = errors.New("sieve: script has no Moov metadata header")

// ParseManaged reads a managed script: metadata JSON plus the verbatim
// external section.
//
// drifted reports that the stored bytes are NOT what the model generates —
// the script was edited outside Moov. The caller decides what that means;
// the manager's answer is "back the bytes up before regenerating" (never
// destroy what we did not write, applied to our own script's file).
//
// The drift comparison needs the same environment the script was generated
// with; env is that. caps is nil on the comparison because the stored script
// already passed the server's own CHECKSCRIPT when it was stored.
func ParseManaged(content []byte, env GenerateEnv) (s *Script, drifted bool, err error) {
	text := string(content)

	begin := strings.Index(text, metaBegin)
	end := strings.Index(text, metaEnd)
	if begin < 0 || end < 0 || end < begin {
		return nil, false, ErrNotManaged
	}
	rawJSON := strings.TrimSpace(text[begin+len(metaBegin) : end])

	var model Script
	if err := json.Unmarshal([]byte(rawJSON), &model); err != nil {
		return nil, false, fmt.Errorf("sieve: metadata header is not valid JSON: %w", err)
	}
	if model.Version != MetadataVersion {
		return nil, false, fmt.Errorf("sieve: metadata version %d is not supported (this build speaks %d)",
			model.Version, MetadataVersion)
	}

	// The external body travels in the script, not the JSON — recover it.
	if eb := strings.Index(text, externalBegin); eb >= 0 {
		rest := text[eb+len(externalBegin):]
		rest = strings.TrimPrefix(rest, "\r\n")
		rest = strings.TrimPrefix(rest, "\n")
		ee := strings.LastIndex(rest, externalEnd)
		if ee < 0 {
			return nil, false, errors.New("sieve: external section is not terminated")
		}
		// Verbatim. ImportForeign always normalizes a body to end in CRLF,
		// and Generate adds nothing after a newline-terminated body, so the
		// bytes here ARE the model's ExternalBody for every script this code
		// wrote. (A hand-built model with an unterminated body reads back
		// with the CRLF Generate added; the drift check below then reports
		// it, which errs on the safe side — a backup, never a loss.)
		model.ExternalBody = rest[:ee]
	}

	// Drift detection: regenerate and compare bytes. A model that no longer
	// validates (e.g. a forward address that lost its verification since) is
	// treated as drifted rather than as an error — the bytes exist, the model
	// exists, and the caller must still be able to read both.
	regen, gerr := Generate(&model, env, nil)
	if gerr != nil {
		// Deliberate: a model that cannot regenerate IS the drifted case,
		// not a failure — the caller still gets both the model and the bytes.
		return &model, true, nil //nolint:nilerr // see comment above
	}
	if string(regen) != text {
		return &model, true, nil
	}
	return &model, false, nil
}

// ImportForeign folds a foreign script into the model's external section,
// preserving it verbatim except for the one transformation Sieve forces:
// leading `require` statements are lifted out (a require after any other
// command is invalid, and the external body no longer sits first) and merged
// into ExternalRequires so the generated require line keeps honoring them.
//
// If the external section already holds content, the import is appended
// under a source comment; importing content that is already present is a
// no-op, so a repeated takeover cannot duplicate rules.
func ImportForeign(s *Script, name string, content []byte) {
	requires, body := splitRequires(string(content))

	s.ExternalRequires = mergeSorted(s.ExternalRequires, requires)
	if s.ExternalSource == "" {
		s.ExternalSource = name
	} else if !strings.Contains(s.ExternalSource, name) {
		s.ExternalSource += ", " + name
	}

	body = strings.TrimRight(body, "\r\n") + "\r\n"
	if strings.TrimSpace(body) == "" {
		return
	}
	if strings.Contains(s.ExternalBody, body) {
		return // already imported, byte-identical
	}
	header := "# imported from script " + quoteSieve(name) + "\r\n"
	if s.ExternalBody != "" && !strings.HasSuffix(s.ExternalBody, "\n") {
		s.ExternalBody += "\r\n"
	}
	s.ExternalBody += header + body
}

// splitRequires lifts the leading require statements off a script,
// returning the named extensions and the remaining body untouched.
//
// Only LEADING requires are lifted — Sieve only allows them there (RFC 5228
// §3.2), so anything after the first non-require command is body by
// definition. Comment lines and blank lines before/between requires are
// preserved in the body (they cannot invalidate anything).
func splitRequires(text string) (requires []string, body string) {
	rest := text
	var kept []string
	for {
		line, remainder, found := cutLine(rest)
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "" || strings.HasPrefix(trimmed, "#"):
			kept = append(kept, line)
			rest = remainder
			if !found {
				return requires, strings.Join(kept, "")
			}
			continue
		case strings.HasPrefix(trimmed, "require"):
			names, ok := parseRequireLine(trimmed)
			if !ok {
				// A require we cannot parse is left in the body untouched:
				// mangling it would be worse than a server-side complaint.
				return requires, strings.Join(kept, "") + rest
			}
			requires = append(requires, names...)
			rest = remainder
			if !found {
				return requires, strings.Join(kept, "")
			}
			continue
		default:
			return requires, strings.Join(kept, "") + rest
		}
	}
}

// cutLine splits off the first line INCLUDING its terminator.
func cutLine(text string) (line, rest string, found bool) {
	i := strings.IndexByte(text, '\n')
	if i < 0 {
		return text, "", false
	}
	return text[:i+1], text[i+1:], true
}

// parseRequireLine reads `require "x";` or `require ["x", "y"];`. ok is
// false for anything it cannot parse completely.
func parseRequireLine(line string) (names []string, ok bool) {
	rest := strings.TrimSpace(strings.TrimPrefix(line, "require"))
	rest = strings.TrimSuffix(rest, ";")
	rest = strings.TrimSpace(rest)
	rest = strings.TrimPrefix(rest, "[")
	rest = strings.TrimSuffix(rest, "]")
	for _, part := range strings.Split(rest, ",") {
		part = strings.TrimSpace(part)
		if len(part) < 2 || part[0] != '"' || part[len(part)-1] != '"' {
			return nil, false
		}
		name := part[1 : len(part)-1]
		if strings.ContainsAny(name, "\\\"") {
			return nil, false // extension names never need escapes
		}
		names = append(names, name)
	}
	return names, len(names) > 0
}

// mergeSorted merges two string lists into a sorted, deduped one.
func mergeSorted(a, b []string) []string {
	set := map[string]bool{}
	for _, v := range a {
		set[v] = true
	}
	for _, v := range b {
		set[v] = true
	}
	if len(set) == 0 {
		return nil
	}
	return sortedSet(set)
}
