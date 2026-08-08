package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// rawConn is a minimal, hand-rolled IMAP client used by the T1 raw-protocol
// tests. It exists on purpose: T1 must validate what OUR Dovecot does, with no
// go-imap in the way. It speaks just enough IMAP to script a QRESYNC session
// and it records a full transcript of every byte exchanged (password redacted).
type rawConn struct {
	name       string
	conn       net.Conn
	r          *bufio.Reader
	w          *bufio.Writer
	tag        int
	cfg        *Config
	transcript *[]string
}

func dialRaw(name string, cfg *Config, transcript *[]string) (*rawConn, error) {
	// STARTTLS on port 143. The internal Mailcow certificate is issued for the
	// public hostname, not for the `dovecot` network alias (finding H2 of spike
	// S1), so certificate verification is disabled for the spike only.
	// Production code MUST pin/verify properly.
	c, err := net.DialTimeout("tcp", cfg.Addr(), 15*time.Second)
	if err != nil {
		return nil, err
	}
	rc := &rawConn{
		name:       name,
		conn:       c,
		r:          bufio.NewReader(c),
		w:          bufio.NewWriter(c),
		cfg:        cfg,
		transcript: transcript,
	}
	// Greeting.
	if _, err := rc.readLine(); err != nil {
		return nil, err
	}
	if _, err := rc.cmd("STARTTLS"); err != nil {
		return nil, err
	}
	tlsConn := tls.Client(c, &tls.Config{
		ServerName:         cfg.Host,
		InsecureSkipVerify: true, // spike only, see comment above
	})
	if err := tlsConn.Handshake(); err != nil {
		return nil, err
	}
	rc.conn = tlsConn
	rc.r = bufio.NewReader(tlsConn)
	rc.w = bufio.NewWriter(tlsConn)
	return rc, nil
}

func (rc *rawConn) log(dir, line string) {
	*rc.transcript = append(*rc.transcript,
		fmt.Sprintf("%s %s %s", rc.name, dir, rc.cfg.redact(strings.TrimRight(line, "\r\n"))))
}

func (rc *rawConn) readLine() (string, error) {
	line, err := rc.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	rc.log("S:", line)
	return strings.TrimRight(line, "\r\n"), nil
}

func (rc *rawConn) writeLine(s string) error {
	rc.log("C:", s)
	if _, err := rc.w.WriteString(s + "\r\n"); err != nil {
		return err
	}
	return rc.w.Flush()
}

func (rc *rawConn) nextTag() string {
	rc.tag++
	return fmt.Sprintf("%s%03d", rc.name, rc.tag)
}

// cmd sends a command and reads until its tagged completion. It returns every
// line received, including untagged responses, so tests can assert on them.
func (rc *rawConn) cmd(format string, args ...any) ([]string, error) {
	tag := rc.nextTag()
	cmd := fmt.Sprintf(format, args...)
	if err := rc.writeLine(tag + " " + cmd); err != nil {
		return nil, err
	}
	return rc.readUntilTagged(tag)
}

func (rc *rawConn) readUntilTagged(tag string) ([]string, error) {
	var lines []string
	for {
		line, err := rc.readLine()
		if err != nil {
			return lines, err
		}
		lines = append(lines, line)
		if strings.HasPrefix(line, tag+" ") {
			if !strings.HasPrefix(line, tag+" OK") {
				return lines, fmt.Errorf("command failed: %s", rc.cfg.redact(line))
			}
			return lines, nil
		}
	}
}

// append sends an APPEND with a literal payload, handling the "+" continuation.
// It returns the APPENDUID-reported UID when the server provides one.
func (rc *rawConn) append(mailbox, subject, body string) (uint32, error) {
	msg := buildMessage(subject, body)
	tag := rc.nextTag()
	if err := rc.writeLine(fmt.Sprintf("%s APPEND %s {%d}", tag, mailbox, len(msg))); err != nil {
		return 0, err
	}
	line, err := rc.readLine()
	if err != nil {
		return 0, err
	}
	if !strings.HasPrefix(line, "+") {
		return 0, fmt.Errorf("expected continuation, got: %s", line)
	}
	rc.log("C:", "<literal "+strconv.Itoa(len(msg))+" bytes>")
	if _, err := rc.w.WriteString(msg + "\r\n"); err != nil {
		return 0, err
	}
	if err := rc.w.Flush(); err != nil {
		return 0, err
	}
	lines, err := rc.readUntilTagged(tag)
	if err != nil {
		return 0, err
	}
	for _, l := range lines {
		if i := strings.Index(l, "APPENDUID "); i >= 0 {
			rest := l[i+len("APPENDUID "):]
			rest = strings.TrimSuffix(rest, "]")
			parts := strings.Fields(rest)
			if len(parts) >= 2 {
				uid, _ := strconv.ParseUint(strings.TrimSuffix(parts[1], "]"), 10, 32)
				return uint32(uid), nil
			}
		}
	}
	return 0, nil
}

func (rc *rawConn) close() {
	if rc.conn != nil {
		_ = rc.writeLine(rc.nextTag() + " LOGOUT")
		_ = rc.conn.Close()
	}
}

func buildMessage(subject, body string) string {
	return strings.Join([]string{
		"From: Moov Spike <moov-spike@example.invalid>",
		"To: Moov Test <moov-test@atmosfera.cloud>",
		"Subject: " + subject,
		"Date: " + time.Now().Format(time.RFC1123Z),
		"Message-ID: <" + strconv.FormatInt(time.Now().UnixNano(), 36) + "@moov-spike.invalid>",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		body,
	}, "\r\n")
}

// findValue extracts a numeric response code value such as UIDVALIDITY or
// HIGHESTMODSEQ from a set of response lines.
func findValue(lines []string, key string) (uint64, bool) {
	for _, l := range lines {
		if i := strings.Index(l, key+" "); i >= 0 {
			rest := l[i+len(key)+1:]
			num := ""
			for _, ch := range rest {
				if ch >= '0' && ch <= '9' {
					num += string(ch)
				} else {
					break
				}
			}
			if num != "" {
				v, err := strconv.ParseUint(num, 10, 64)
				if err == nil {
					return v, true
				}
			}
		}
	}
	return 0, false
}

func anyLineContains(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func linesContaining(lines []string, sub string) []string {
	var out []string
	for _, l := range lines {
		if strings.Contains(l, sub) {
			out = append(out, l)
		}
	}
	return out
}
