package submit

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// The SMTP client against an in-process fake server. Every property under
// test is a WIRE property — which commands were sent, in which order, with
// which parameters, and what happened between the 250 and QUIT — so the fake
// speaks real SMTP over a real TCP socket (loopback) and records the
// transcript. TLS is disabled through the documented test-only escape hatch;
// the TLS handshake itself is exercised by the VPS integration suite against
// the real Postfix.

// fakeSMTPServer is a scripted SMTP endpoint.
type fakeSMTPServer struct {
	ln net.Listener

	// ehloLines are the EHLO keyword lines to advertise.
	ehloLines []string

	// rcptReplies maps a recipient address to a scripted reply line
	// (e.g. "550 5.1.1 no such user"); unlisted recipients get 250.
	rcptReplies map[string]string

	// dataReply is the reply to the end of DATA. Default "250 2.0.0 Ok: queued as FAKE1".
	dataReply string

	mu sync.Mutex
	// commands is the transcript of client commands (verb + args), in order.
	commands []string
	// data is the received message body (dot-decoded), one entry per DATA.
	data [][]byte
}

func newFakeSMTPServer(t *testing.T) *fakeSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	s := &fakeSMTPServer{
		ln:        ln,
		ehloLines: []string{"PIPELINING", "SIZE 10240000", "8BITMIME", "AUTH PLAIN LOGIN"},
		dataReply: "250 2.0.0 Ok: queued as FAKE1",
	}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeSMTPServer) addr() (string, int) {
	a, ok := s.ln.Addr().(*net.TCPAddr)
	if !ok {
		panic("fake SMTP listener is not TCP")
	}
	return "127.0.0.1", a.Port
}

func (s *fakeSMTPServer) transcript() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

func (s *fakeSMTPServer) received() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.data...)
}

func (s *fakeSMTPServer) record(line string) {
	s.mu.Lock()
	s.commands = append(s.commands, line)
	s.mu.Unlock()
}

func (s *fakeSMTPServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.session(conn)
	}
}

func (s *fakeSMTPServer) session(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	say := func(line string) {
		_, _ = w.WriteString(line + "\r\n")
		_ = w.Flush()
	}
	say("220 fake.test ESMTP")

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		s.record(line)
		verb := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(verb, "EHLO"):
			for _, ext := range s.ehloLines {
				say("250-" + ext)
			}
			say("250 fake.test")
		case strings.HasPrefix(verb, "AUTH"):
			say("235 2.7.0 Authentication successful")
		case strings.HasPrefix(verb, "MAIL"):
			say("250 2.1.0 Ok")
		case strings.HasPrefix(verb, "RCPT"):
			addr := line
			if i := strings.Index(line, "<"); i >= 0 {
				if j := strings.Index(line[i:], ">"); j > 0 {
					addr = line[i+1 : i+j]
				}
			}
			if reply, scripted := s.rcptReplies[addr]; scripted {
				say(reply)
			} else {
				say("250 2.1.5 Ok")
			}
		case verb == "DATA":
			say("354 End data with <CR><LF>.<CR><LF>")
			var body []byte
			for {
				dl, err := r.ReadString('\n')
				if err != nil {
					return
				}
				trimmed := strings.TrimRight(dl, "\r\n")
				if trimmed == "." {
					break
				}
				// Dot-decoding (RFC 5321 §4.5.2).
				trimmed = strings.TrimPrefix(trimmed, ".")
				body = append(body, []byte(trimmed+"\r\n")...)
			}
			s.mu.Lock()
			s.data = append(s.data, body)
			s.mu.Unlock()
			say(s.dataReply)
		case verb == "RSET":
			say("250 2.0.0 Ok")
		case verb == "QUIT":
			say("221 2.0.0 Bye")
			return
		default:
			say("502 5.5.2 Command not recognized")
		}
	}
}

func testConfig(s *fakeSMTPServer) Config {
	host, port := s.addr()
	return Config{
		Host:       host,
		Port:       port,
		Username:   "moov-test@example.test",
		Password:   "app-password",
		DisableTLS: true, // the documented test-only escape hatch
	}
}

func testEnvelope() Envelope {
	return Envelope{
		MailFrom: "moov-test@example.test",
		RcptTo:   []string{"dest@example.test"},
	}
}

// ---------------------------------------------------------------------------

func TestSendHappyPathNegotiates8BitMIMEAndOrder(t *testing.T) {
	srv := newFakeSMTPServer(t)
	msg := "Subject: hi\r\n\r\nbody line\r\n"

	var events []string
	res, err := Send(context.Background(), testConfig(srv), testEnvelope(),
		strings.NewReader(msg), func(reply string) error {
			events = append(events, "accepted:"+reply)
			return nil
		})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !res.EightBitMIME {
		t.Error("8BITMIME was advertised but not negotiated")
	}
	if want := "250 2.0.0 Ok: queued as FAKE1"; res.Reply != want {
		t.Errorf("Reply = %q, want %q", res.Reply, want)
	}
	if len(events) != 1 || !strings.HasPrefix(events[0], "accepted:250") {
		t.Errorf("onAccepted events = %v, want exactly one acceptance", events)
	}

	// The wire: MAIL FROM carries BODY=8BITMIME and SIZE (rule 4), and QUIT
	// comes AFTER the DATA whose acceptance the callback saw — the rule-1
	// ordering, observed on the transcript rather than assumed.
	tr := srv.transcript()
	var mailLine string
	quitIdx, dataIdx := -1, -1
	for i, line := range tr {
		u := strings.ToUpper(line)
		if strings.HasPrefix(u, "MAIL") {
			mailLine = line
		}
		if u == "DATA" {
			dataIdx = i
		}
		if u == "QUIT" {
			quitIdx = i
		}
	}
	if !strings.Contains(mailLine, "BODY=8BITMIME") {
		t.Errorf("MAIL FROM lacks BODY=8BITMIME: %q", mailLine)
	}
	if dataIdx < 0 || quitIdx < dataIdx {
		t.Errorf("transcript order wrong: DATA at %d, QUIT at %d (%v)", dataIdx, quitIdx, tr)
	}
}

func TestSendDotStuffingRoundTrips(t *testing.T) {
	srv := newFakeSMTPServer(t)
	// Lines that begin with dots are the RFC 5321 §4.5.2 hazard: without
	// stuffing, the ".\r\n" line would terminate DATA early and the rest of
	// the message would be parsed as SMTP commands.
	msg := "Subject: dots\r\n\r\n.\r\n..\r\n.leading dot\r\nplain\r\n"

	_, err := Send(context.Background(), testConfig(srv), testEnvelope(), strings.NewReader(msg), nil)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := srv.received()
	if len(got) != 1 {
		t.Fatalf("server received %d messages, want 1", len(got))
	}
	if string(got[0]) != msg {
		t.Errorf("dot round-trip mangled the message:\n got %q\nwant %q", got[0], msg)
	}
}

func TestSendRcptPermanentRefusalAbortsBeforeData(t *testing.T) {
	srv := newFakeSMTPServer(t)
	srv.rcptReplies = map[string]string{"bad@example.test": "550 5.1.1 no such user"}

	env := Envelope{MailFrom: "moov-test@example.test",
		RcptTo: []string{"good@example.test", "bad@example.test"}}
	_, err := Send(context.Background(), testConfig(srv), env, strings.NewReader("x\r\n"), nil)
	if err == nil {
		t.Fatal("Send succeeded with a permanently refused recipient")
	}
	if !IsPermanent(err) {
		t.Errorf("a 550 must classify permanent, got %v", err)
	}

	// All-or-nothing: DATA was never issued, nothing was transmitted.
	for _, line := range srv.transcript() {
		if strings.EqualFold(line, "DATA") {
			t.Fatal("DATA was issued despite a refused recipient; a partial send cannot be retried safely")
		}
	}
	if n := len(srv.received()); n != 0 {
		t.Errorf("server received %d messages, want 0", n)
	}
}

func TestSendRcptTransientRefusalClassifiesTransient(t *testing.T) {
	srv := newFakeSMTPServer(t)
	srv.rcptReplies = map[string]string{"dest@example.test": "450 4.2.0 try again later"}

	_, err := Send(context.Background(), testConfig(srv), testEnvelope(), strings.NewReader("x\r\n"), nil)
	if err == nil {
		t.Fatal("Send succeeded with a refused recipient")
	}
	if IsPermanent(err) {
		t.Errorf("a 450 must classify transient, got %v", err)
	}
}

func TestSendDataCloseFailureClassifies(t *testing.T) {
	for _, tc := range []struct {
		reply     string
		permanent bool
	}{
		{"554 5.6.0 message rejected", true},
		{"451 4.3.0 queue file error", false},
	} {
		srv := newFakeSMTPServer(t)
		srv.dataReply = tc.reply
		accepted := false
		_, err := Send(context.Background(), testConfig(srv), testEnvelope(),
			strings.NewReader("x\r\n"), func(string) error { accepted = true; return nil })
		if err == nil {
			t.Fatalf("%s: Send succeeded", tc.reply)
		}
		if IsPermanent(err) != tc.permanent {
			t.Errorf("%s: permanent = %v, want %v", tc.reply, IsPermanent(err), tc.permanent)
		}
		if accepted {
			t.Errorf("%s: onAccepted ran for a refused DATA — the callback is for the 250 only", tc.reply)
		}
	}
}

func TestSendAcceptedUnrecordedWhenCallbackFails(t *testing.T) {
	srv := newFakeSMTPServer(t)
	cbErr := errors.New("store is down")
	res, err := Send(context.Background(), testConfig(srv), testEnvelope(),
		strings.NewReader("x\r\n"), func(string) error { return cbErr })

	// The message IS sent; the error must say exactly that, carrying the
	// reply so the caller can keep retrying the recording.
	if !errors.Is(err, ErrAcceptedUnrecorded) {
		t.Fatalf("err = %v, want ErrAcceptedUnrecorded", err)
	}
	var au *AcceptedUnrecordedError
	if !errors.As(err, &au) || !strings.HasPrefix(au.Reply, "250") {
		t.Errorf("AcceptedUnrecordedError.Reply = %q, want the 250 line", au.Reply)
	}
	if res.Reply == "" {
		t.Error("Result.Reply empty; the acceptance evidence must survive the callback failure")
	}
	if n := len(srv.received()); n != 1 {
		t.Errorf("server received %d messages, want 1", n)
	}
}

func TestSendRefusesSizeOverServerLimit(t *testing.T) {
	srv := newFakeSMTPServer(t)
	env := testEnvelope()
	env.Size = 20_000_000 // over the fake's SIZE 10240000

	_, err := Send(context.Background(), testConfig(srv), env, strings.NewReader("x\r\n"), nil)
	if err == nil {
		t.Fatal("Send succeeded past the server's declared SIZE limit")
	}
	if !IsPermanent(err) {
		t.Errorf("an over-SIZE refusal is permanent (the message will never fit), got %v", err)
	}
	// Refused locally: MAIL FROM never went out.
	for _, line := range srv.transcript() {
		if strings.HasPrefix(strings.ToUpper(line), "MAIL") {
			t.Fatal("MAIL FROM was issued for a message the SIZE limit already refused")
		}
	}
}

func TestSendRefusesAuthWithoutTLS(t *testing.T) {
	srv := newFakeSMTPServer(t)
	cfg := testConfig(srv)
	cfg.DisableTLS = false // production stance; the fake offers no STARTTLS

	_, err := Send(context.Background(), cfg, testEnvelope(), strings.NewReader("x\r\n"), nil)
	if err == nil {
		t.Fatal("Send agreed to proceed without STARTTLS toward AUTH")
	}
	var se *Error
	if !errors.As(err, &se) || se.Phase != PhaseStartTLS {
		t.Errorf("err = %v, want a %s-phase refusal", err, PhaseStartTLS)
	}
}

func TestSendConnectFailureIsTransient(t *testing.T) {
	// A port nothing listens on: connection refused, the textbook transient.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("listener is not TCP")
	}
	port := addr.Port
	_ = ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = Send(ctx, Config{Host: "127.0.0.1", Port: port, DisableTLS: true},
		testEnvelope(), strings.NewReader("x\r\n"), nil)
	if err == nil {
		t.Fatal("Send succeeded against a closed port")
	}
	if IsPermanent(err) {
		t.Errorf("a connection failure must classify transient, got %v", err)
	}
}

func TestErrorClassificationTable(t *testing.T) {
	for _, tc := range []struct {
		code      int
		permanent bool
	}{
		{550, true}, {554, true}, {552, true},
		{421, false}, {450, false}, {451, false},
		{0, false}, // network error, no reply
	} {
		e := &Error{Phase: PhaseMail, Code: tc.code, Err: fmt.Errorf("x")}
		if e.Permanent() != tc.permanent {
			t.Errorf("code %d: permanent = %v, want %v", tc.code, e.Permanent(), tc.permanent)
		}
	}
}
