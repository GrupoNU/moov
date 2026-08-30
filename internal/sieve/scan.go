package sieve

import (
	"errors"
	"fmt"
	"strings"
)

// ScanRedirects finds every address a Sieve script's `redirect` commands
// target.
//
// It exists for the raw-script policy gate: SieveScript/set accepts
// client-supplied Sieve, and the GC-4 security design says mail may only be
// forwarded to VERIFIED addresses — through the rule model AND through raw
// uploads, or the recipe surface would be a fence with an open gate next to
// it. This is a shallow lexical scan, not a Sieve parser: it understands
// exactly enough (comments, strings, multiline text blocks) to find the
// `redirect` identifier and read its string arguments. On anything it cannot
// tokenize it returns an error, and the caller fails CLOSED — an unparseable
// script is refused rather than waved through.
func ScanRedirects(content []byte) ([]string, error) {
	text := string(content)
	var out []string
	i := 0
	n := len(text)

	readString := func() (string, error) {
		// caller consumed the opening quote
		var b strings.Builder
		for i < n {
			c := text[i]
			i++
			switch c {
			case '"':
				return b.String(), nil
			case '\\':
				if i >= n {
					return "", errors.New("sieve: unterminated escape in string")
				}
				b.WriteByte(text[i])
				i++
			default:
				b.WriteByte(c)
			}
		}
		return "", errors.New("sieve: unterminated string")
	}

	skipText := func() error {
		// caller is positioned right after "text:"; the block ends at a
		// CRLF "." CRLF line (RFC 5228 §2.4.2).
		for {
			nl := strings.Index(text[i:], "\n")
			if nl < 0 {
				return errors.New("sieve: unterminated text: block")
			}
			i += nl + 1
			rest := text[i:]
			if rest == "." || strings.HasPrefix(rest, ".\r\n") || strings.HasPrefix(rest, ".\n") {
				for i < n && text[i] != '\n' {
					i++
				}
				if i < n {
					i++
				}
				return nil
			}
		}
	}

	for i < n {
		c := text[i]
		switch {
		case c == '#':
			for i < n && text[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && text[i+1] == '*':
			end := strings.Index(text[i+2:], "*/")
			if end < 0 {
				return nil, errors.New("sieve: unterminated bracket comment")
			}
			i += 2 + end + 2
		case c == '"':
			i++
			if _, err := readString(); err != nil {
				return nil, err
			}
		case c == 't' && strings.HasPrefix(text[i:], "text:"):
			i += len("text:")
			if err := skipText(); err != nil {
				return nil, err
			}
		case isWordByte(c):
			start := i
			for i < n && isWordByte(text[i]) {
				i++
			}
			if text[start:i] != "redirect" {
				continue
			}
			// Read the command's arguments up to ';': tags are skipped,
			// strings and bracketed string lists are collected.
			before := len(out)
			for i < n && text[i] != ';' {
				switch {
				case text[i] == '"':
					i++
					s, err := readString()
					if err != nil {
						return nil, err
					}
					out = append(out, s)
				case text[i] == '#':
					for i < n && text[i] != '\n' {
						i++
					}
				case strings.HasPrefix(text[i:], "text:"):
					return nil, errors.New("sieve: redirect with a text: argument is not supported")
				default:
					i++
				}
			}
			if i >= n {
				return nil, errors.New("sieve: redirect command not terminated")
			}
			if len(out) == before {
				return nil, fmt.Errorf("sieve: redirect without a readable address")
			}
		default:
			i++
		}
	}
	return out, nil
}

func isWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}
