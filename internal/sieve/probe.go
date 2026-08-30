package sieve

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
)

// Probe reads the server's capability listing WITHOUT authenticating: dial,
// greeting, STARTTLS, the re-issued post-TLS listing, LOGOUT.
//
// It exists for the session object: the RFC 9661 capability advertises the
// live SIEVE extension list and notification methods, which are server-wide
// facts (one Dovecot behind every account) that cmd/moovd probes once at
// startup rather than per request. Username/Password are ignored; every
// other Config field keeps its Connect meaning, TLS verification included.
func Probe(ctx context.Context, cfg Config) (Capabilities, error) {
	// Normalize requires credentials for Connect's sake; a probe has none.
	// Fill placeholders for validation only — they never reach the wire.
	probeCfg := cfg
	probeCfg.Username = "probe"
	probeCfg.Password = "probe"
	probeCfg, err := probeCfg.Normalize()
	if err != nil {
		return Capabilities{}, err
	}
	tlsCfg, err := probeCfg.tlsConfig()
	if err != nil {
		return Capabilities{}, err
	}

	dialer := &net.Dialer{Timeout: probeCfg.DialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", probeCfg.Address())
	if err != nil {
		return Capabilities{}, fmt.Errorf("sieve: dialing %s: %w", probeCfg.Address(), err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	rd := newReader(conn)
	w := bufio.NewWriter(conn)

	greeting, err := rd.readResponse()
	if err != nil {
		return Capabilities{}, err
	}
	if !greeting.isOK() {
		return Capabilities{}, fmt.Errorf("sieve: greeting refused: %w", respError(greeting))
	}
	caps := capsFromResponse(greeting)
	if !caps.StartTLS {
		// The pre-TLS listing is still a truthful SIEVE list on servers
		// that skip STARTTLS on a trusted network; refuse anyway, matching
		// Connect's posture — a deployment this package will not talk to is
		// a deployment it should not advertise either.
		return Capabilities{}, ErrNoSTARTTLS
	}
	if err := writeCommand(w, "STARTTLS"); err != nil {
		return Capabilities{}, fmt.Errorf("sieve: sending STARTTLS: %w", err)
	}
	resp, err := rd.readResponse()
	if err != nil {
		return Capabilities{}, err
	}
	if !resp.isOK() {
		return Capabilities{}, fmt.Errorf("sieve: STARTTLS refused: %w", respError(resp))
	}
	tconn := tls.Client(conn, tlsCfg)
	if err := tconn.HandshakeContext(ctx); err != nil {
		return Capabilities{}, fmt.Errorf("sieve: TLS handshake: %w", err)
	}
	trd := newReader(tconn)
	recap, err := trd.readResponse()
	if err != nil {
		return Capabilities{}, err
	}
	if !recap.isOK() {
		return Capabilities{}, fmt.Errorf("sieve: post-TLS capability listing refused: %w", respError(recap))
	}
	_ = writeCommand(bufio.NewWriter(tconn), "LOGOUT")
	return capsFromResponse(recap), nil
}
