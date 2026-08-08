package main

import (
	"strings"
	"time"
)

// testNotifyRaw is the control experiment that decides whether the missing
// HIGHESTMODSEQ and the missing FlagChange notifications observed in the
// go-imap NOTIFY test are a Dovecot defect or a client-side artifact.
//
// It runs the SAME mutations twice against the SAME server, changing only the
// NOTIFY SET syntax:
//
//	A) NOTIFY SET STATUS (PERSONAL (...))   <- correct RFC 5465, library CANNOT emit
//	B) NOTIFY SET (PERSONAL (...))          <- what go-imap emits today
//
// If A yields HIGHESTMODSEQ and FlagChange while B does not, Dovecot is
// compliant and the library's encoder is the sole limitation.
func testNotifyRaw(cfg *Config) *Result {
	res := newResult("notify-raw")
	var tr []string
	defer func() { res.Transcript = tr }()

	runVariant := func(label, notifyCmd, mailbox string) (statusLines []string, err error) {
		mark := func(s string) { tr = append(tr, "### "+label+" "+s) }
		mark("BEGIN using: " + notifyCmd)

		w, err := dialRaw("W", cfg, &tr)
		if err != nil {
			return nil, err
		}
		defer w.close()
		if _, err := w.cmd("LOGIN %s %s", cfg.User, cfg.Password); err != nil {
			return nil, err
		}
		if _, err := w.cmd("%s", notifyCmd); err != nil {
			return nil, err
		}

		m, err := dialRaw("M", cfg, &tr)
		if err != nil {
			return nil, err
		}
		defer m.close()
		if _, err := m.cmd("LOGIN %s %s", cfg.User, cfg.Password); err != nil {
			return nil, err
		}
		if _, err := m.cmd("SELECT %q", mailbox); err != nil {
			return nil, err
		}

		// Drain the initial burst the server sends on NOTIFY SET, so it is not
		// confused with the notifications caused by our mutations.
		w.writeLine("W800 NOOP")
		w.readUntilTagged("W800")
		drainMark := len(tr)

		uid, err := m.append("\""+mailbox+"\"", "moov-s2 notify-raw "+label, "probe")
		if err != nil {
			return nil, err
		}
		time.Sleep(1200 * time.Millisecond)
		w.writeLine("W900 NOOP")
		w.readUntilTagged("W900")
		mark("after APPEND")

		// A pure flag change that does NOT alter MESSAGES: the discriminating case.
		if _, err := m.cmd("UID STORE %d +FLAGS (\\Flagged)", uid); err != nil {
			return nil, err
		}
		time.Sleep(1200 * time.Millisecond)
		w.writeLine("W901 NOOP")
		w.readUntilTagged("W901")
		mark("after FlagChange")

		// Cleanup.
		_, _ = m.cmd("UID STORE %d +FLAGS (\\Deleted)", uid)
		_, _ = m.cmd("UID EXPUNGE %d", uid)
		time.Sleep(1000 * time.Millisecond)
		w.writeLine("W902 NOOP")
		w.readUntilTagged("W902")
		mark("after EXPUNGE")

		// Collect only the watcher's unsolicited STATUS lines produced after
		// the initial burst.
		for _, l := range tr[drainMark:] {
			if strings.HasPrefix(l, "W S: * STATUS") {
				statusLines = append(statusLines, strings.TrimPrefix(l, "W S: "))
			}
		}
		return statusLines, nil
	}

	// --- variant A: correct RFC 5465 syntax with the STATUS keyword ---------
	withStatus, err := runVariant("A/with-STATUS",
		"NOTIFY SET STATUS (PERSONAL (MessageNew MessageExpunge FlagChange))",
		"S2/folder2")
	if err != nil {
		return res.fail("variant A: %v", err)
	}
	res.note("A) `NOTIFY SET STATUS (PERSONAL ...)` -> %d STATUS response(s):", len(withStatus))
	for _, l := range withStatus {
		res.note("   %s", l)
	}

	// --- variant B: what go-imap emits today --------------------------------
	noStatus, err := runVariant("B/no-STATUS",
		"NOTIFY SET (PERSONAL (MessageNew MessageExpunge FlagChange))",
		"S2/folder4")
	if err != nil {
		return res.fail("variant B: %v", err)
	}
	res.note("B) `NOTIFY SET (PERSONAL ...)` -> %d STATUS response(s):", len(noStatus))
	for _, l := range noStatus {
		res.note("   %s", l)
	}

	// --- analysis ------------------------------------------------------------
	countModSeq := func(ls []string) int {
		n := 0
		for _, l := range ls {
			if strings.Contains(l, "HIGHESTMODSEQ") {
				n++
			}
		}
		return n
	}
	aModSeq, bModSeq := countModSeq(withStatus), countModSeq(noStatus)
	res.note("HIGHESTMODSEQ present: variant A %d/%d, variant B %d/%d",
		aModSeq, len(withStatus), bModSeq, len(noStatus))

	if aModSeq == 0 {
		res.fail("variant A produced no HIGHESTMODSEQ - Dovecot really does violate RFC 5465")
	} else {
		res.note("CONCLUSION: Dovecot DOES include HIGHESTMODSEQ when the client asks for the " +
			"STATUS keyword => NOT an RFC 5465 violation. The go-imap encoder is the limitation.")
	}

	// The FlagChange discriminator: variant A must show a STATUS whose only
	// meaningful delta is the modseq (MESSAGES unchanged).
	aHasFlagOnly := false
	for _, l := range withStatus {
		if strings.Contains(l, "HIGHESTMODSEQ") && !strings.Contains(l, "MESSAGES") {
			aHasFlagOnly = true
		}
	}
	res.note("variant A delivered a modseq-only STATUS (pure FlagChange signal): %v", aHasFlagOnly)
	if len(noStatus) >= len(withStatus) {
		res.note("NOTE: variant B produced >= as many STATUS lines as A; inspect the transcript, " +
			"the difference is in CONTENT (HIGHESTMODSEQ), not only in count.")
	}
	res.note("Variant B cannot signal a pure flag change: without HIGHESTMODSEQ, a STATUS whose " +
		"MESSAGES/UNSEEN are unchanged carries no observable delta.")

	return res
}
