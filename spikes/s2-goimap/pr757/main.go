package main

import (
	"crypto/tls"
	"fmt"
	"os"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Validation of go-imap PR #757 (imapclient: support QRESYNC) against the real
// Dovecot. Proves whether the patched library can do what the sync engine needs:
// ENABLE QRESYNC, SELECT (QRESYNC (uidvalidity modseq)), and receive VANISHED.
func dial(handler *imapclient.UnilateralDataHandler) (*imapclient.Client, error) {
	c, err := imapclient.DialStartTLS(os.Getenv("IMAP_HOST")+":"+os.Getenv("IMAP_PORT"),
		&imapclient.Options{
			TLSConfig:             &tls.Config{InsecureSkipVerify: true},
			UnilateralDataHandler: handler,
		})
	if err != nil {
		return nil, err
	}
	if err := c.Login(os.Getenv("IMAP_USER"), os.Getenv("IMAP_PASSWORD")).Wait(); err != nil {
		return nil, err
	}
	return c, nil
}

func appendMsg(c *imapclient.Client, mbox, subj string) (imap.UID, error) {
	msg := "From: a@b.invalid\r\nTo: c@d.invalid\r\nSubject: " + subj +
		"\r\nDate: " + time.Now().Format(time.RFC1123Z) +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\npr757 probe\r\n"
	ac := c.Append(mbox, int64(len(msg)), nil)
	ac.Write([]byte(msg))
	if err := ac.Close(); err != nil {
		return 0, err
	}
	d, err := ac.Wait()
	if err != nil {
		return 0, err
	}
	return d.UID, nil
}

func main() {
	fail := 0
	check := func(cond bool, msg string) {
		if cond {
			fmt.Println("  [PASS]", msg)
		} else {
			fmt.Println("  [FAIL]", msg)
			fail++
		}
	}

	// 1. Enable(QRESYNC) must now be accepted.
	c, err := dial(nil)
	if err != nil {
		fmt.Println("dial:", err)
		os.Exit(1)
	}
	defer c.Close()
	_, enErr := c.Enable(imap.CapQResync).Wait()
	check(enErr == nil, fmt.Sprintf("Enable(QRESYNC) accepted (err=%v)", enErr))

	sel, err := c.Select("INBOX", &imap.SelectOptions{CondStore: true}).Wait()
	if err != nil {
		fmt.Println("select:", err)
		os.Exit(1)
	}
	uidValidity := sel.UIDValidity
	fmt.Printf("  baseline UIDVALIDITY=%d HighestModSeq=%d\n", uidValidity, sel.HighestModSeq)

	// Seed two messages.
	u1, _ := appendMsg(c, "INBOX", "pr757 one")
	u2, _ := appendMsg(c, "INBOX", "pr757 two")
	sel2, _ := c.Select("INBOX", &imap.SelectOptions{CondStore: true}).Wait()
	syncModSeq := sel2.HighestModSeq
	fmt.Printf("  seeded UIDs %d,%d; sync modseq=%d\n", u1, u2, syncModSeq)

	// Mutate: flag u1, expunge u2.
	c.Store(imap.UIDSetNum(u1), &imap.StoreFlags{
		Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagFlagged}}, nil).Close()
	c.Store(imap.UIDSetNum(u2), &imap.StoreFlags{
		Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close()
	c.UIDExpunge(imap.UIDSetNum(u2)).Close()
	c.Close()

	// 2. Reconnect and SELECT (QRESYNC ...) -> must replay VANISHED.
	var vanishedUIDs imap.UIDSet
	var vanishedEarlier bool
	var vanishedCalls int
	c2, err := dial(&imapclient.UnilateralDataHandler{
		Vanished: func(uids imap.UIDSet, earlier bool) {
			vanishedCalls++
			vanishedUIDs = uids
			vanishedEarlier = earlier
		},
	})
	if err != nil {
		fmt.Println("dial2:", err)
		os.Exit(1)
	}
	defer c2.Close()
	if _, err := c2.Enable(imap.CapQResync).Wait(); err != nil {
		fmt.Println("enable2:", err)
		os.Exit(1)
	}

	qsel, err := c2.Select("INBOX", &imap.SelectOptions{
		CondStore: true,
		QResync: &imap.QResyncOptions{
			UIDValidity: uidValidity,
			ModSeq:      syncModSeq,
		},
	}).Wait()
	check(err == nil, fmt.Sprintf("SELECT (QRESYNC ...) accepted (err=%v)", err))
	if err == nil {
		fmt.Printf("  SelectData.VanishedUIDs=%v\n", qsel.VanishedUIDs)
		check(len(qsel.VanishedUIDs) > 0 || vanishedCalls > 0,
			"VANISHED surfaced (SelectData.VanishedUIDs or Vanished handler)")
		fmt.Printf("  Vanished handler calls=%d uids=%v earlier=%v\n",
			vanishedCalls, vanishedUIDs, vanishedEarlier)
	}

	// 3. FETCH with Vanished:true + ChangedSince.
	vanishedCalls = 0
	// "1:*" as a UID range.
	all := imap.UIDSet{}
	all.AddRange(1, 0)
	msgs, ferr := c2.Fetch(all, &imap.FetchOptions{
		UID: true, Flags: true, ModSeq: true,
		ChangedSince: syncModSeq,
		Vanished:     true,
	}).Collect()
	check(ferr == nil, fmt.Sprintf("UID FETCH (CHANGEDSINCE .. VANISHED) accepted (err=%v)", ferr))
	fmt.Printf("  changed msgs=%d, Vanished handler calls=%d uids=%v earlier=%v\n",
		len(msgs), vanishedCalls, vanishedUIDs, vanishedEarlier)
	for _, m := range msgs {
		fmt.Printf("    UID=%d ModSeq=%d Flags=%v\n", m.UID, m.ModSeq, m.Flags)
	}
	check(vanishedCalls > 0, "Vanished handler fired for FETCH VANISHED")

	// cleanup
	c2.Store(imap.UIDSetNum(u1), &imap.StoreFlags{
		Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close()
	c2.UIDExpunge(imap.UIDSetNum(u1)).Close()

	fmt.Printf("\nPR757 VALIDATION: %d failure(s)\n", fail)
	if fail > 0 {
		os.Exit(1)
	}
}
