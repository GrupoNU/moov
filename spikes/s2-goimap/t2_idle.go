package main

import (
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// testIdle validates that IDLE on a selected mailbox delivers unilateral data
// (EXISTS / FETCH) when another connection modifies the mailbox, and measures
// the delivery latency informally.
func testIdle(cfg *Config) *Result {
	res := newResult("idle")

	var mu sync.Mutex
	var events []string
	start := time.Now()
	firstEvent := time.Time{}

	record := func(kind string) {
		mu.Lock()
		defer mu.Unlock()
		if firstEvent.IsZero() {
			firstEvent = time.Now()
		}
		events = append(events, kind)
	}

	opts := &imapclient.Options{
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Mailbox: func(d *imapclient.UnilateralDataMailbox) {
				if d.NumMessages != nil {
					record("EXISTS=" + itoa(uint64(*d.NumMessages)))
				} else {
					record("MAILBOX")
				}
			},
			Expunge: func(seqNum uint32) { record("EXPUNGE seq=" + itoa(uint64(seqNum))) },
			Fetch: func(m *imapclient.FetchMessageData) {
				record("FETCH seq=" + itoa(uint64(m.SeqNum)))
				// The payload must be consumed, otherwise the protocol pump stalls.
				_, _ = m.Collect()
			},
		},
	}

	watcher, err := dialLib(cfg, opts)
	if err != nil {
		return res.fail("watcher conn: %v", err)
	}
	defer watcher.Close()

	if _, err := watcher.Select("INBOX", &imap.SelectOptions{CondStore: true}).Wait(); err != nil {
		return res.fail("watcher SELECT: %v", err)
	}

	idle, err := watcher.Idle()
	if err != nil {
		return res.fail("Idle(): %v", err)
	}
	res.note("watcher entered IDLE on INBOX")

	// Give the server a moment to settle into idle before mutating.
	time.Sleep(300 * time.Millisecond)

	// --- second connection appends a message --------------------------------
	c2, err := dialLib(cfg, nil)
	if err != nil {
		return res.fail("conn2: %v", err)
	}
	start = time.Now()
	uid, err := appendLib(c2, "INBOX", "moov-s2 idle "+time.Now().Format("150405.000"))
	if err != nil {
		c2.Close()
		return res.fail("append from conn2: %v", err)
	}
	res.note("connection 2 appended UID %d to INBOX", uid)

	// Wait for the unilateral data to reach the idling connection.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(events)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := idle.Close(); err != nil {
		res.note("idle.Close(): %v", err)
	}

	mu.Lock()
	got := append([]string{}, events...)
	fe := firstEvent
	mu.Unlock()

	if len(got) == 0 {
		res.fail("no unilateral data reached the IDLE connection within 10s")
	} else {
		res.note("unilateral events during IDLE: %v", got)
		res.note("latency APPEND -> first unilateral event: %v (informal, same host)", fe.Sub(start))
	}

	// Cleanup the appended message.
	if uid != 0 {
		if _, err := c2.Select("INBOX", nil).Wait(); err == nil {
			cleanupUIDs(c2, imap.UIDSetNum(uid))
		}
	}
	c2.Close()
	return res
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}
