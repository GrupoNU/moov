// Command s2-goimap is the Moov spike S2 harness.
//
// It empirically validates, against a REAL Dovecot (Mailcow) server, the IMAP
// extensions the Moov sync engine depends on:
//
//	t1-qresync   raw-protocol QRESYNC validation (no library involved)
//	condstore    go-imap/v2 CONDSTORE support
//	idle         go-imap/v2 IDLE support and unilateral data delivery
//	notify       go-imap/v2 NOTIFY (branch v2 only): multi-mailbox watching
//	notify-raw   raw-protocol control: does the NOTIFY "STATUS" keyword change
//	             what Dovecot delivers? (decides library-bug vs server-bug)
//	caps         post-login capabilities as the library sees them
//	qresync-lib  whether go-imap/v2 will ENABLE QRESYNC at all
//
// Configuration comes exclusively from the environment (IMAP_HOST, IMAP_PORT,
// IMAP_USER, IMAP_PASSWORD). No credential is ever written to disk or printed.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(2)
	}

	var res *Result
	switch os.Args[1] {
	case "t1-qresync":
		res = testQresyncRaw(cfg)
	case "caps":
		res = testCaps(cfg)
	case "condstore":
		res = testCondstore(cfg)
	case "idle":
		res = testIdle(cfg)
	case "notify":
		res = testNotify(cfg)
	case "notify-raw":
		res = testNotifyRaw(cfg)
	case "qresync-lib":
		res = testQresyncLib(cfg)
	default:
		usage()
		os.Exit(2)
	}

	res.print()
	if !res.passed() {
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: s2-goimap <t1-qresync|caps|condstore|idle|notify|notify-raw|qresync-lib>")
}
