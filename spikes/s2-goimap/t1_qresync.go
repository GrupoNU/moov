package main

import (
	"fmt"
	"strings"
	"time"
)

// testQresyncRaw is T1: validate that our Dovecot implements QRESYNC semantics
// correctly, at the wire level, independently of any Go IMAP library.
//
// Scenario:
//  1. LOGIN + ENABLE QRESYNC -> * ENABLED QRESYNC
//  2. SELECT INBOX -> record UIDVALIDITY + HIGHESTMODSEQ
//  3. APPEND 3 messages, record their UIDs and the new HIGHESTMODSEQ
//  4. Disconnect (client goes "offline")
//  5. From a second connection: flag one message, expunge another
//  6. Reconnect with SELECT INBOX (QRESYNC (uidvalidity modseq)) and verify
//     the server replays VANISHED (EARLIER) + the flag change
//  7. On the resynced connection, exercise
//     UID FETCH 1:* (FLAGS) (CHANGEDSINCE <modseq> VANISHED)
func testQresyncRaw(cfg *Config) *Result {
	res := newResult("t1-qresync-raw")
	var tr []string
	defer func() { res.Transcript = tr }()

	// --- Step 1-2: initial session -------------------------------------------
	c1, err := dialRaw("A", cfg, &tr)
	if err != nil {
		return res.fail("dial: %v", err)
	}
	if _, err := c1.cmd("LOGIN %s %s", cfg.User, cfg.Password); err != nil {
		return res.fail("login: %v", err)
	}

	enabled, err := c1.cmd("ENABLE QRESYNC")
	if err != nil {
		return res.fail("ENABLE QRESYNC: %v", err)
	}
	if !anyLineContains(enabled, "ENABLED") || !anyLineContains(enabled, "QRESYNC") {
		return res.fail("server did not confirm * ENABLED QRESYNC")
	}
	res.note("ENABLE QRESYNC confirmed: %s", strings.Join(linesContaining(enabled, "ENABLED"), " | "))

	sel, err := c1.cmd("SELECT INBOX")
	if err != nil {
		return res.fail("SELECT INBOX: %v", err)
	}
	uidValidity, ok := findValue(sel, "UIDVALIDITY")
	if !ok {
		return res.fail("no UIDVALIDITY in SELECT response")
	}
	modSeqBefore, ok := findValue(sel, "HIGHESTMODSEQ")
	if !ok {
		return res.fail("no HIGHESTMODSEQ in SELECT response (CONDSTORE not active?)")
	}
	res.note("baseline: UIDVALIDITY=%d HIGHESTMODSEQ=%d", uidValidity, modSeqBefore)

	// --- Step 3: append 3 messages -------------------------------------------
	stamp := time.Now().Format("20060102T150405")
	var uids []uint32
	for i := 1; i <= 3; i++ {
		uid, err := c1.append("INBOX",
			fmt.Sprintf("moov-s2 T1 msg%d %s", i, stamp),
			fmt.Sprintf("QRESYNC raw protocol test message %d", i))
		if err != nil {
			return res.fail("APPEND %d: %v", i, err)
		}
		if uid == 0 {
			return res.fail("APPEND %d returned no APPENDUID", i)
		}
		uids = append(uids, uid)
	}
	res.note("appended UIDs: %v", uids)

	// NOOP so the connection learns about its own appends and the modseq moves.
	noop, err := c1.cmd("NOOP")
	if err != nil {
		return res.fail("NOOP: %v", err)
	}
	_ = noop
	st, err := c1.cmd("STATUS INBOX (HIGHESTMODSEQ UIDNEXT MESSAGES)")
	if err != nil {
		return res.fail("STATUS: %v", err)
	}
	modSeqAfterAppend, _ := findValue(st, "HIGHESTMODSEQ")
	res.note("HIGHESTMODSEQ after 3 appends: %d (was %d)", modSeqAfterAppend, modSeqBefore)
	if modSeqAfterAppend <= modSeqBefore {
		return res.fail("HIGHESTMODSEQ did not advance after APPEND")
	}

	// This is the modseq the "offline client" remembers.
	syncModSeq := modSeqAfterAppend

	// --- Step 4: client goes offline BEFORE the changes ----------------------
	c1.close()
	res.note("connection A closed (client offline) at modseq %d", syncModSeq)

	// --- Step 5: a second client mutates the mailbox -------------------------
	flaggedUID := uids[0]
	expungedUID := uids[1]

	c2, err := dialRaw("B", cfg, &tr)
	if err != nil {
		return res.fail("dial 2: %v", err)
	}
	if _, err := c2.cmd("LOGIN %s %s", cfg.User, cfg.Password); err != nil {
		return res.fail("login 2: %v", err)
	}
	if _, err := c2.cmd("ENABLE QRESYNC"); err != nil {
		return res.fail("ENABLE QRESYNC on B: %v", err)
	}
	if _, err := c2.cmd("SELECT INBOX"); err != nil {
		return res.fail("SELECT on B: %v", err)
	}
	if _, err := c2.cmd("UID STORE %d +FLAGS (\\Flagged)", flaggedUID); err != nil {
		return res.fail("UID STORE +Flagged: %v", err)
	}
	if _, err := c2.cmd("UID STORE %d +FLAGS (\\Deleted)", expungedUID); err != nil {
		return res.fail("UID STORE +Deleted: %v", err)
	}
	if _, err := c2.cmd("UID EXPUNGE %d", expungedUID); err != nil {
		return res.fail("UID EXPUNGE: %v", err)
	}
	res.note("connection B: flagged UID %d, expunged UID %d", flaggedUID, expungedUID)
	c2.close()

	// --- Step 6: reconnect with QRESYNC --------------------------------------
	c3, err := dialRaw("C", cfg, &tr)
	if err != nil {
		return res.fail("dial 3: %v", err)
	}
	if _, err := c3.cmd("LOGIN %s %s", cfg.User, cfg.Password); err != nil {
		return res.fail("login 3: %v", err)
	}
	if _, err := c3.cmd("ENABLE QRESYNC"); err != nil {
		return res.fail("ENABLE QRESYNC on C: %v", err)
	}
	resync, err := c3.cmd("SELECT INBOX (QRESYNC (%d %d))", uidValidity, syncModSeq)
	if err != nil {
		return res.fail("SELECT (QRESYNC ...): %v", err)
	}

	vanished := linesContaining(resync, "VANISHED")
	if len(vanished) == 0 {
		res.fail("no VANISHED response on QRESYNC SELECT")
	} else {
		res.note("VANISHED lines: %s", strings.Join(vanished, " | "))
		gotEarlier := false
		gotUID := false
		for _, l := range vanished {
			if strings.Contains(l, "(EARLIER)") {
				gotEarlier = true
			}
			if strings.Contains(l, fmt.Sprint(expungedUID)) {
				gotUID = true
			}
		}
		if !gotEarlier {
			res.fail("VANISHED response lacks (EARLIER)")
		}
		if !gotUID {
			res.fail("VANISHED does not mention expunged UID %d", expungedUID)
		}
	}

	fetches := linesContaining(resync, "FETCH")
	res.note("FETCH replay lines on QRESYNC SELECT: %d", len(fetches))
	for _, l := range fetches {
		res.note("  %s", l)
	}
	sawFlagChange := false
	for _, l := range fetches {
		if strings.Contains(l, "\\Flagged") && strings.Contains(l, fmt.Sprintf("UID %d", flaggedUID)) {
			sawFlagChange = true
		}
	}
	if !sawFlagChange {
		res.fail("QRESYNC SELECT did not replay the \\Flagged change for UID %d", flaggedUID)
	} else {
		res.note("flag change for UID %d correctly replayed", flaggedUID)
	}
	if !anyLineContains(fetches, "MODSEQ") {
		res.fail("replayed FETCH lines carry no MODSEQ")
	}

	// --- Step 7: UID FETCH ... (CHANGEDSINCE n VANISHED) ---------------------
	// Make a fresh change so there is something to report, then ask for
	// everything changed since syncModSeq, with VANISHED enabled.
	flagged2 := uids[2]
	if _, err := c3.cmd("UID STORE %d +FLAGS (\\Seen)", flagged2); err != nil {
		return res.fail("UID STORE \\Seen: %v", err)
	}
	cs, err := c3.cmd("UID FETCH 1:* (FLAGS) (CHANGEDSINCE %d VANISHED)", syncModSeq)
	if err != nil {
		return res.fail("UID FETCH CHANGEDSINCE VANISHED: %v", err)
	}
	csVanished := linesContaining(cs, "VANISHED")
	csFetch := linesContaining(cs, "FETCH")
	res.note("CHANGEDSINCE %d VANISHED -> %d VANISHED lines, %d FETCH lines",
		syncModSeq, len(csVanished), len(csFetch))
	for _, l := range append(append([]string{}, csVanished...), csFetch...) {
		res.note("  %s", l)
	}
	if len(csVanished) == 0 {
		res.fail("UID FETCH (CHANGEDSINCE .. VANISHED) returned no VANISHED line for the expunged UID")
	} else if !anyLineContains(csVanished, "EARLIER") {
		res.note("NOTE: VANISHED in FETCH has no (EARLIER) tag - correct per RFC 7162 for this form")
	}
	if len(csFetch) == 0 {
		res.fail("UID FETCH (CHANGEDSINCE ..) returned no changed messages")
	}

	// Cleanup: remove the messages this test created so runs stay idempotent.
	for _, u := range uids {
		_, _ = c3.cmd("UID STORE %d +FLAGS (\\Deleted)", u)
	}
	_, _ = c3.cmd("UID EXPUNGE %s", joinUIDs(uids))
	c3.close()

	return res
}

func joinUIDs(uids []uint32) string {
	parts := make([]string, 0, len(uids))
	for _, u := range uids {
		parts = append(parts, fmt.Sprint(u))
	}
	return strings.Join(parts, ",")
}
