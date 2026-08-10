package imap

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// defaultWatchBuffer is the event channel capacity when WatchSpec leaves it at
// zero. Large enough that a consumer doing a batched FETCH per notification
// does not lose events during the round trip; small enough that a wedged
// consumer is noticed rather than accumulating unbounded memory.
const defaultWatchBuffer = 64

// watchState is one live watcher.
type watchState struct {
	// events is the channel handed to the caller. Closed when the watch ends.
	events chan Event
	// done is closed to ask the watch to stop, or when it stops itself.
	done chan struct{}
	// finished is closed once the watcher goroutine has fully unwound and no
	// longer touches the connection. Close waits on it before tearing the
	// connection down.
	finished chan struct{}

	dropped atomic.Int64

	closeOnce sync.Once
	err       error
}

// doneClosing returns the channel that closes once the watcher has released
// the connection.
func (w *watchState) doneClosing() <-chan struct{} { return w.finished }

// Watch implements Client.
//
// # Why NOTIFY and not one IDLE per folder
//
// A mailbox with 40 folders would otherwise need 40 connections, and Mailcow's
// fail2ban would treat that as an attack. NOTIFY collapses it: one connection
// watching PERSONAL received events for every folder in the account (S2 T2d).
//
// # Why the STATUS keyword is not optional
//
// Without it, a flag change in a non-selected folder produces no notification
// at all — MESSAGES and UNSEEN do not move when \Flagged is toggled, so with
// no HIGHESTMODSEQ there is nothing to report and the server stays silent
// (S2 T4). Another client marking a message read would be invisible to Moov
// until an unrelated event or the reconciler happened to reveal it. Stock
// go-imap cannot emit the keyword at all (it encodes "(STATUS)", which Dovecot
// rejects outright); patch 0002 is what makes this correct.
//
// # Why the IDLE loop
//
// go-imap only reads the socket while a command is in flight or during IDLE.
// After NOTIFY SET the connection would go quiet and no notification would
// ever be delivered, so the watcher parks in IDLE and stays there (S2 H9).
// The library restarts IDLE every 28 minutes by itself, which keeps the
// connection inside the RFC 2177 limit without any bookkeeping here.
func (cl *client) Watch(ctx context.Context, spec WatchSpec) (<-chan Event, error) {
	gc, err := cl.conn()
	if err != nil {
		return nil, err
	}

	cl.mu.Lock()
	if cl.watch != nil {
		cl.mu.Unlock()
		return nil, errors.New("imap: a watch is already running on this connection")
	}
	cl.mu.Unlock()

	if !cl.caps.Has(CapNotify) && !cl.caps.Has(CapIdle) {
		return nil, ErrWatchNotSupported
	}

	bufSize := spec.BufferSize
	if bufSize <= 0 {
		bufSize = defaultWatchBuffer
	}
	w := &watchState{
		events:   make(chan Event, bufSize),
		done:     make(chan struct{}),
		finished: make(chan struct{}),
	}

	cl.mu.Lock()
	cl.watch = w
	cl.mu.Unlock()

	useNotify := cl.caps.Has(CapNotify)
	if useNotify {
		if err := cl.setNotify(gc, spec); err != nil {
			cl.clearWatch()
			w.finish(err)
			close(w.finished)
			close(w.events)
			return nil, err
		}
	} else {
		// Without NOTIFY the watch degrades to the selected mailbox only.
		// This is a real reduction in coverage, not a silent equivalence, so
		// it is logged: the reconciler becomes the only thing catching changes
		// in every other folder.
		cl.log.Warn("imap: server lacks NOTIFY; watching only the selected mailbox via IDLE")
	}

	go cl.runWatch(ctx, gc, w, useNotify)
	return w.events, nil
}

// setNotify issues the NOTIFY SET command.
func (cl *client) setNotify(gc *imapclient.Client, spec WatchSpec) error {
	events := []goimap.NotifyEvent{
		goimap.NotifyEventMessageNew,
		goimap.NotifyEventMessageExpunge,
		goimap.NotifyEventFlagChange,
		goimap.NotifyEventMailboxName,
		goimap.NotifyEventSubscriptionChange,
	}

	opts := &goimap.NotifyOptions{
		// The load-bearing flag; see the type doc.
		Status: true,
	}

	if len(spec.Mailboxes) > 0 {
		opts.Items = []goimap.NotifyItem{{Mailboxes: spec.Mailboxes, Events: events}}
	} else {
		// The whole personal namespace: one connection, every folder.
		//
		// SELECTED is listed separately because PERSONAL alone does not cover
		// the currently selected mailbox for message events (RFC 5465 §5), and
		// the selected mailbox is exactly the one the user is looking at.
		opts.Items = []goimap.NotifyItem{
			{MailboxSpec: goimap.NotifyMailboxSpecSelected, Events: events},
			{MailboxSpec: goimap.NotifyMailboxSpecPersonal, Events: events},
		}
	}

	cmd, err := gc.Notify(opts)
	if err != nil {
		return fmt.Errorf("imap: NOTIFY SET: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		// Stock go-imap encodes "(STATUS)" and omits MAILBOXES, both of which
		// Dovecot answers with "BAD … Invalid arguments" (S2 T2d). A BAD here
		// is therefore the signature of an unpatched vendor tree, so the error
		// says where to look.
		return fmt.Errorf("imap: NOTIFY SET rejected (is the go-imap patch set applied? "+
			"see patches/README.md): %w", err)
	}
	return nil
}

// runWatch owns the connection for the lifetime of the watch: it parks in
// IDLE, and exits when the context is canceled, the client is closed, or the
// connection breaks.
func (cl *client) runWatch(ctx context.Context, gc *imapclient.Client, w *watchState, useNotify bool) {
	defer func() {
		cl.clearWatch()
		close(w.events)
		close(w.finished)
	}()

	idle, err := gc.Idle()
	if err != nil {
		w.finish(fmt.Errorf("imap: entering IDLE: %w", err))
		return
	}

	// Wait for whichever ends the watch first. The library keeps IDLE alive on
	// its own (restarting every 28 minutes), so there is nothing to poll here.
	select {
	case <-ctx.Done():
		w.finish(ctx.Err())
	case <-gc.Closed():
		w.finish(errors.New("imap: connection closed while watching"))
	case <-w.done:
		// Stopped from the other side (Close).
	}

	// Leave IDLE so the connection is usable again. Best-effort: on a closed
	// connection this fails and there is nothing to recover.
	if err := idle.Close(); err != nil {
		cl.log.Debug("imap: leaving IDLE failed", "error", err)
	}

	if useNotify {
		// NOTIFY NONE, so a connection returned to the pool is not still
		// pushing events at a watcher that no longer exists.
		if cmd, err := gc.Notify(nil); err != nil {
			cl.log.Debug("imap: NOTIFY NONE failed", "error", err)
		} else if err := cmd.Wait(); err != nil {
			cl.log.Debug("imap: NOTIFY NONE rejected", "error", err)
		}
	}

	if dropped := w.dropped.Load(); dropped > 0 {
		cl.log.Info("imap: watcher dropped events because the consumer was behind; "+
			"the reconciler will catch the difference", "dropped", dropped)
	}
}

// clearWatch detaches the watcher from the client.
func (cl *client) clearWatch() {
	cl.mu.Lock()
	cl.watch = nil
	cl.mu.Unlock()
}

// finish records the terminating error once and signals the watcher's end.
func (w *watchState) finish(err error) {
	w.closeOnce.Do(func() {
		w.err = err
		close(w.done)
	})
}
