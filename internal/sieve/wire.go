package sieve

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// The ManageSieve wire format (RFC 5804 §4), confined to this file: tokens,
// literals, quoted strings, and the response structure every command shares.

// maxLiteral bounds a server literal. Dovecot's script-size ceiling in the
// Mailcow deployment is 1 MiB (sieve_max_script_size); 8 MiB leaves room for
// any sane configuration while refusing a garbage length before allocating it.
const maxLiteral = 8 << 20

// maxLineBytes bounds one response line, so a stream that never sends CRLF
// cannot grow a buffer without limit.
const maxLineBytes = 64 << 10

// tokenKind classifies one parsed token.
type tokenKind int

const (
	tokenAtom tokenKind = iota
	tokenString
)

// token is one atom or string from a response line. Strings carry their
// content with quoting/literal framing already removed.
type token struct {
	kind tokenKind
	text string
}

// respLine is one parsed response line: its tokens, plus the parenthesized
// response code (with arguments flattened to strings) when present.
type respLine struct {
	tokens   []token
	code     string
	codeArgs []string
}

// response is one command's complete answer: zero or more data lines, then
// the final OK/NO/BYE line.
type response struct {
	// status is "OK", "NO" or "BYE", uppercased.
	status string
	// code and codeArgs are the final line's response code, e.g.
	// "QUOTA/MAXSIZE", or "TAG" with its argument.
	code     string
	codeArgs []string
	// message is the final line's human-readable string, if any.
	message string
	// data are the lines before the final one (capability lines, LISTSCRIPTS
	// entries, GETSCRIPT content).
	data []respLine
}

// isOK reports a successful final status.
func (r *response) isOK() bool { return r.status == "OK" }

// reader parses server responses.
type reader struct {
	br *bufio.Reader

	// lastDelim remembers what terminated the most recent atom (' ', ')' or
	// '\n'), because an atom's end is only discovered by reading past it and
	// the caller needs to know whether the line ended with it.
	lastDelim byte
}

func newReader(r io.Reader) *reader {
	return &reader{br: bufio.NewReader(r)}
}

// readResponse reads lines until a final OK/NO/BYE line arrives.
func (rd *reader) readResponse() (*response, error) {
	resp := &response{}
	for {
		line, err := rd.readLine()
		if err != nil {
			return nil, err
		}
		if len(line.tokens) == 0 {
			continue // a bare CRLF; tolerated, never sent by Dovecot
		}
		first := line.tokens[0]
		if first.kind == tokenAtom {
			switch strings.ToUpper(first.text) {
			case "OK", "NO", "BYE":
				resp.status = strings.ToUpper(first.text)
				resp.code = line.code
				resp.codeArgs = line.codeArgs
				// The trailing human-readable string, when present, is the
				// last string token of the line.
				for _, t := range line.tokens[1:] {
					if t.kind == tokenString {
						resp.message = t.text
					}
				}
				return resp, nil
			}
		}
		resp.data = append(resp.data, line)
	}
}

// readLine reads one logical line: tokens up to CRLF, with server literals
// ({n} CRLF followed by n octets, §4) resolved inline into string tokens.
func (rd *reader) readLine() (respLine, error) {
	var line respLine
	var consumed int

	readByte := func() (byte, error) {
		b, err := rd.br.ReadByte()
		if err != nil {
			return 0, err
		}
		consumed++
		if consumed > maxLineBytes {
			return 0, errors.New("sieve: response line exceeds the size limit")
		}
		return b, nil
	}

	for {
		b, err := readByte()
		if err != nil {
			return line, wireErr(err)
		}
		switch b {
		case '\r':
			nb, err := readByte()
			if err != nil {
				return line, wireErr(err)
			}
			if nb != '\n' {
				return line, fmt.Errorf("sieve: CR not followed by LF in response")
			}
			return line, nil
		case '\n':
			// Tolerate bare LF from a nonconforming server.
			return line, nil
		case ' ':
			continue
		case '"':
			s, err := rd.readQuoted(readByte)
			if err != nil {
				return line, err
			}
			line.tokens = append(line.tokens, token{tokenString, s})
		case '{':
			s, err := rd.readLiteral(readByte)
			if err != nil {
				return line, err
			}
			line.tokens = append(line.tokens, token{tokenString, s})
		case '(':
			if err := rd.readCode(readByte, &line); err != nil {
				return line, err
			}
		default:
			atom, err := rd.readAtom(b, readByte)
			if err != nil {
				return line, err
			}
			line.tokens = append(line.tokens, token{tokenAtom, atom})
			// readAtom consumes the delimiter; a CRLF delimiter ends the line.
			if rd.lastDelim == '\n' {
				return line, nil
			}
			if rd.lastDelim == ')' {
				return line, fmt.Errorf("sieve: unexpected ')' in response")
			}
		}
	}
}

// readQuoted reads a quoted string after its opening quote. §4: backslash
// escapes backslash and double-quote; no other escapes exist; CR, LF and NUL
// cannot appear.
func (rd *reader) readQuoted(readByte func() (byte, error)) (string, error) {
	var b strings.Builder
	for {
		c, err := readByte()
		if err != nil {
			return "", wireErr(err)
		}
		switch c {
		case '"':
			return b.String(), nil
		case '\\':
			n, err := readByte()
			if err != nil {
				return "", wireErr(err)
			}
			b.WriteByte(n)
		case '\r', '\n', 0:
			return "", errors.New("sieve: illegal control octet inside a quoted string")
		default:
			b.WriteByte(c)
		}
	}
}

// readLiteral reads a server literal after its opening brace: digits, '}',
// CRLF, then exactly n octets.
func (rd *reader) readLiteral(readByte func() (byte, error)) (string, error) {
	var digits strings.Builder
	for {
		c, err := readByte()
		if err != nil {
			return "", wireErr(err)
		}
		if c == '}' {
			break
		}
		if c == '+' {
			// {n+} is client-to-server only, but tolerated on read.
			continue
		}
		if c < '0' || c > '9' {
			return "", fmt.Errorf("sieve: malformed literal length")
		}
		digits.WriteByte(c)
	}
	n, err := strconv.Atoi(digits.String())
	if err != nil || n < 0 || n > maxLiteral {
		return "", fmt.Errorf("sieve: literal length %q out of range", digits.String())
	}
	// CRLF after the length.
	if c, err := readByte(); err != nil {
		return "", wireErr(err)
	} else if c == '\r' {
		if c2, err := readByte(); err != nil {
			return "", wireErr(err)
		} else if c2 != '\n' {
			return "", errors.New("sieve: malformed literal header")
		}
	} else if c != '\n' {
		return "", errors.New("sieve: malformed literal header")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(rd.br, buf); err != nil {
		return "", wireErr(err)
	}
	return string(buf), nil
}

// readCode reads a parenthesized response code after its opening paren:
// an atom (possibly with '/' inside, e.g. QUOTA/MAXSIZE) plus optional
// string/atom arguments, up to the closing paren.
func (rd *reader) readCode(readByte func() (byte, error), line *respLine) error {
	for {
		c, err := readByte()
		if err != nil {
			return wireErr(err)
		}
		switch c {
		case ')':
			return nil
		case ' ':
			continue
		case '"':
			s, err := rd.readQuoted(readByte)
			if err != nil {
				return err
			}
			line.codeArgs = append(line.codeArgs, s)
		case '{':
			s, err := rd.readLiteral(readByte)
			if err != nil {
				return err
			}
			line.codeArgs = append(line.codeArgs, s)
		default:
			atom, err := rd.readAtom(c, readByte)
			if err != nil {
				return err
			}
			if line.code == "" {
				line.code = strings.ToUpper(atom)
			} else {
				line.codeArgs = append(line.codeArgs, atom)
			}
			switch rd.lastDelim {
			case ')':
				return nil
			case '\n':
				return errors.New("sieve: response code not closed before end of line")
			}
		}
	}
}

// readAtom reads an atom starting with first, stopping at space, ')' or CRLF.
// The terminating delimiter is recorded in rd.lastDelim (' ', ')' or '\n').
func (rd *reader) readAtom(first byte, readByte func() (byte, error)) (string, error) {
	var b strings.Builder
	b.WriteByte(first)
	for {
		c, err := readByte()
		if err != nil {
			return "", wireErr(err)
		}
		switch c {
		case ' ':
			rd.lastDelim = ' '
			return b.String(), nil
		case ')':
			rd.lastDelim = ')'
			return b.String(), nil
		case '\r':
			n, err := readByte()
			if err != nil {
				return "", wireErr(err)
			}
			if n != '\n' {
				return "", errors.New("sieve: CR not followed by LF after atom")
			}
			rd.lastDelim = '\n'
			return b.String(), nil
		case '\n':
			rd.lastDelim = '\n'
			return b.String(), nil
		default:
			b.WriteByte(c)
		}
	}
}

// wireErr wraps a transport error uniformly, so callers see one vocabulary.
func wireErr(err error) error {
	if errors.Is(err, io.EOF) {
		return fmt.Errorf("sieve: connection closed by server: %w", io.ErrUnexpectedEOF)
	}
	return fmt.Errorf("sieve: reading response: %w", err)
}

// ---------------------------------------------------------------------------
// writing
// ---------------------------------------------------------------------------

// writeCommand writes one command line: the verb plus arguments, each
// rendered as a quoted string or a non-synchronizing literal ({n+}, §4 —
// chosen so a PUTSCRIPT never waits for a continuation), ending with CRLF.
func writeCommand(w *bufio.Writer, verb string, args ...wireArg) error {
	if _, err := w.WriteString(verb); err != nil {
		return err
	}
	for _, a := range args {
		if err := w.WriteByte(' '); err != nil {
			return err
		}
		if err := a.write(w); err != nil {
			return err
		}
	}
	if _, err := w.WriteString("\r\n"); err != nil {
		return err
	}
	return w.Flush()
}

// wireArg is one command argument.
type wireArg interface {
	write(w *bufio.Writer) error
}

// stringArg renders as a quoted string when the content permits it, else as a
// literal. §4's quoted strings cannot carry NUL, CR or LF; anything else is
// escapable.
type stringArg string

func (s stringArg) write(w *bufio.Writer) error {
	if strings.ContainsAny(string(s), "\x00\r\n") {
		return literalArg(s).write(w)
	}
	if err := w.WriteByte('"'); err != nil {
		return err
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' || c == '\\' {
			if err := w.WriteByte('\\'); err != nil {
				return err
			}
		}
		if err := w.WriteByte(c); err != nil {
			return err
		}
	}
	return w.WriteByte('"')
}

// literalArg always renders as a non-synchronizing literal — the right form
// for script content, whose bytes are arbitrary.
type literalArg string

func (s literalArg) write(w *bufio.Writer) error {
	if _, err := fmt.Fprintf(w, "{%d+}\r\n", len(s)); err != nil {
		return err
	}
	_, err := w.WriteString(string(s))
	return err
}
