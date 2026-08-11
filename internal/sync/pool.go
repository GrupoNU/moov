package sync

import (
	"context"

	"github.com/GrupoNU/moov/internal/imap"
)

// connPool hands out the account's IMAP connections one goroutine at a time.
//
// It is a channel rather than a mutex-guarded slice because acquisition has to
// be cancelable: a shutdown during a long backfill must not block waiting for a
// connection that a stuck fetch is holding. A channel receive composes with
// ctx.Done() in a select; a Mutex.Lock does not.
//
// The invariant it protects is stated in imap.Client's doc: a Client is a
// single command stream and is NOT safe for concurrent use. Every use of a
// connection in this package goes through acquire/release, so two goroutines
// can never interleave commands on one socket.
type connPool struct {
	free chan imap.Client
	size int
}

func newConnPool(clients []imap.Client) *connPool {
	p := &connPool{free: make(chan imap.Client, len(clients)), size: len(clients)}
	for _, c := range clients {
		p.free <- c
	}
	return p
}

// acquire takes a connection, waiting until one is free or ctx ends.
func (p *connPool) acquire(ctx context.Context) (imap.Client, error) {
	select {
	case c := <-p.free:
		return c, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// release returns a connection to the pool. It never blocks: the channel's
// capacity equals the number of connections, so a release can always proceed.
func (p *connPool) release(c imap.Client) {
	if c == nil {
		return
	}
	p.free <- c
}

// withConn runs fn with a connection held for its duration.
func (p *connPool) withConn(ctx context.Context, fn func(imap.Client) error) error {
	c, err := p.acquire(ctx)
	if err != nil {
		return err
	}
	defer p.release(c)
	return fn(c)
}
