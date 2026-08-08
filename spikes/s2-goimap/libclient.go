package main

import (
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// dialLib opens a go-imap/v2 client over STARTTLS and logs in.
//
// TLS verification is disabled because the Mailcow-internal certificate is
// issued for the public hostname and we connect via the `dovecot` container
// alias (spike S1, finding H2). This is a spike-only shortcut.
func dialLib(cfg *Config, opts *imapclient.Options) (*imapclient.Client, error) {
	if opts == nil {
		opts = &imapclient.Options{}
	}
	opts.TLSConfig = &tls.Config{
		ServerName:         cfg.Host,
		InsecureSkipVerify: true, // spike only
	}
	c, err := imapclient.DialStartTLS(cfg.Addr(), opts)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	if err := c.Login(cfg.User, cfg.Password).Wait(); err != nil {
		c.Close()
		return nil, fmt.Errorf("login: %w", err)
	}
	return c, nil
}

// appendLib appends a small test message to a mailbox and returns its UID.
func appendLib(c *imapclient.Client, mailbox, subject string) (imap.UID, error) {
	msg := buildMessage(subject, "moov spike S2 payload")
	ac := c.Append(mailbox, int64(len(msg)), nil)
	if _, err := ac.Write([]byte(msg)); err != nil {
		return 0, err
	}
	if err := ac.Close(); err != nil {
		return 0, err
	}
	data, err := ac.Wait()
	if err != nil {
		return 0, err
	}
	if data == nil {
		return 0, nil
	}
	return data.UID, nil
}

// testCaps dumps the post-login capabilities exactly as the library sees them.
func testCaps(cfg *Config) *Result {
	res := newResult("caps")
	c, err := dialLib(cfg, nil)
	if err != nil {
		return res.fail("%v", err)
	}
	defer c.Close()

	interesting := []imap.Cap{
		imap.CapIMAP4rev1, imap.CapIMAP4rev2, imap.CapCondStore, imap.CapQResync,
		imap.CapIdle, imap.CapESearch, imap.CapListStatus, imap.CapStatusSize,
		imap.CapSaveDate, imap.CapPreview, imap.CapMetadata, imap.CapSpecialUse,
		imap.CapObjectID, imap.CapUIDPlus, imap.CapMove, imap.CapBinary,
		imap.CapMultiAppend, imap.CapNamespace, imap.CapNotify,
	}
	var have, missing []string
	for _, cap := range interesting {
		if c.Caps().Has(cap) {
			have = append(have, string(cap))
		} else {
			missing = append(missing, string(cap))
		}
	}
	res.note("library sees supported: %s", strings.Join(have, " "))
	res.note("library sees MISSING:   %s", strings.Join(missing, " "))

	// Sanity: the extensions the Moov sync engine depends on must be present.
	for _, must := range []imap.Cap{imap.CapCondStore, imap.CapQResync, imap.CapIdle, imap.CapNotify} {
		if !c.Caps().Has(must) {
			res.fail("required capability %s not advertised", must)
		}
	}
	return res
}

// testQresyncLib records whether the library will ENABLE QRESYNC at all.
// Expected outcome per research: it refuses (allowlist in imapclient.Enable).
// A refusal is a FINDING, not a test failure, so this test never fails on it.
func testQresyncLib(cfg *Config) *Result {
	res := newResult("qresync-lib")
	c, err := dialLib(cfg, nil)
	if err != nil {
		return res.fail("%v", err)
	}
	defer c.Close()

	res.note("server advertises QRESYNC: %v", c.Caps().Has(imap.CapQResync))

	data, err := c.Enable(imap.CapQResync).Wait()
	if err != nil {
		res.note("FINDING: Client.Enable(CapQResync) refused by the library: %v", err)
		res.note("=> go-imap/v2 branch-v2 tip cannot enable QRESYNC; a patch or fork is required.")
	} else {
		res.note("UNEXPECTED: library enabled QRESYNC, caps=%v", data.Caps)
	}

	// Confirm the same client can still enable something it does support, i.e.
	// the refusal is a client-side allowlist and not a broken connection.
	if _, err := c.Enable(imap.CapMetadata).Wait(); err != nil {
		res.note("Enable(METADATA) also failed: %v", err)
	} else {
		res.note("control: Enable(METADATA) succeeded => refusal is a client-side allowlist")
	}
	return res
}

// testCondstore validates the CONDSTORE surface the sync engine will rely on:
// HIGHESTMODSEQ on SELECT, ChangedSince filtering, MODSEQ in FETCH results, and
// the UnchangedSince conflict path (RFC 7162 MODIFIED).
func testCondstore(cfg *Config) *Result {
	res := newResult("condstore")

	c, err := dialLib(cfg, nil)
	if err != nil {
		return res.fail("conn1: %v", err)
	}
	defer c.Close()

	sel, err := c.Select("INBOX", &imap.SelectOptions{CondStore: true}).Wait()
	if err != nil {
		return res.fail("SELECT CondStore: %v", err)
	}
	if sel.HighestModSeq == 0 {
		return res.fail("SelectData.HighestModSeq is 0 - CONDSTORE not activated")
	}
	res.note("SELECT (CONDSTORE): UIDVALIDITY=%d UIDNEXT=%d NumMessages=%d HighestModSeq=%d",
		sel.UIDValidity, sel.UIDNext, sel.NumMessages, sel.HighestModSeq)

	// Seed two messages so we can change exactly one of them.
	stamp := time.Now().Format("150405")
	uidA, err := appendLib(c, "INBOX", "moov-s2 condstore A "+stamp)
	if err != nil {
		return res.fail("append A: %v", err)
	}
	uidB, err := appendLib(c, "INBOX", "moov-s2 condstore B "+stamp)
	if err != nil {
		return res.fail("append B: %v", err)
	}
	res.note("seeded UIDs: A=%d B=%d", uidA, uidB)

	// Re-select to pick up a modseq that already includes both appends.
	sel2, err := c.Select("INBOX", &imap.SelectOptions{CondStore: true}).Wait()
	if err != nil {
		return res.fail("re-SELECT: %v", err)
	}
	baseModSeq := sel2.HighestModSeq
	res.note("baseline modseq after appends: %d", baseModSeq)

	// --- change a flag from a SECOND connection -----------------------------
	c2, err := dialLib(cfg, nil)
	if err != nil {
		return res.fail("conn2: %v", err)
	}
	if _, err := c2.Select("INBOX", &imap.SelectOptions{CondStore: true}).Wait(); err != nil {
		c2.Close()
		return res.fail("conn2 SELECT: %v", err)
	}
	setA := imap.UIDSetNum(uidA)
	if err := c2.Store(setA, &imap.StoreFlags{
		Op:    imap.StoreFlagsAdd,
		Flags: []imap.Flag{imap.FlagFlagged},
	}, nil).Close(); err != nil {
		c2.Close()
		return res.fail("conn2 STORE: %v", err)
	}
	c2.Close()
	res.note("connection 2 added \\Flagged to UID %d", uidA)

	// --- ChangedSince must return ONLY the changed message ------------------
	msgs, err := c.Fetch(imap.UIDSetNum(uidA, uidB), &imap.FetchOptions{
		UID:          true,
		Flags:        true,
		ModSeq:       true,
		ChangedSince: baseModSeq,
	}).Collect()
	if err != nil {
		return res.fail("FETCH ChangedSince: %v", err)
	}
	res.note("FETCH (CHANGEDSINCE %d) returned %d message(s)", baseModSeq, len(msgs))
	for _, m := range msgs {
		res.note("  UID=%d ModSeq=%d Flags=%v", m.UID, m.ModSeq, m.Flags)
		if m.ModSeq == 0 {
			res.fail("FETCH result for UID %d has ModSeq 0 (CONDSTORE data missing)", m.UID)
		}
	}
	switch {
	case len(msgs) == 0:
		res.fail("ChangedSince returned nothing; expected UID %d", uidA)
	case len(msgs) > 1:
		res.fail("ChangedSince returned %d messages; expected only UID %d", len(msgs), uidA)
	case msgs[0].UID != uidA:
		res.fail("ChangedSince returned UID %d; expected %d", msgs[0].UID, uidA)
	default:
		res.note("PASS: ChangedSince correctly isolated the single modified message")
	}

	// --- UnchangedSince conflict (RFC 7162 MODIFIED) ------------------------
	// Store against a deliberately stale modseq. The server must refuse the
	// update and report the conflicting message in a MODIFIED response code.
	staleModSeq := baseModSeq
	fc := c.Store(setA, &imap.StoreFlags{
		Op:    imap.StoreFlagsAdd,
		Flags: []imap.Flag{imap.FlagAnswered},
	}, &imap.StoreOptions{UnchangedSince: staleModSeq})
	conflictMsgs, storeErr := fc.Collect()
	res.note("STORE (UNCHANGEDSINCE %d) -> err=%v, %d FETCH response(s)",
		staleModSeq, storeErr, len(conflictMsgs))
	for _, m := range conflictMsgs {
		res.note("  conflict-path FETCH: UID=%d ModSeq=%d Flags=%v", m.UID, m.ModSeq, m.Flags)
	}

	// Verify the flag was actually NOT applied - that is the semantic that
	// matters for the sync engine's optimistic-concurrency writes.
	after, err := c.Fetch(setA, &imap.FetchOptions{UID: true, Flags: true, ModSeq: true}).Collect()
	if err != nil {
		return res.fail("verification FETCH: %v", err)
	}
	if len(after) != 1 {
		return res.fail("verification FETCH returned %d messages", len(after))
	}
	hasAnswered := false
	for _, f := range after[0].Flags {
		if f == imap.FlagAnswered {
			hasAnswered = true
		}
	}
	res.note("post-conflict state: UID=%d ModSeq=%d Flags=%v", after[0].UID, after[0].ModSeq, after[0].Flags)
	if hasAnswered {
		res.fail("UNCHANGEDSINCE did NOT protect the message: \\Answered was applied despite a stale modseq")
	} else {
		res.note("PASS: UNCHANGEDSINCE correctly rejected the conflicting update (\\Answered absent)")
	}
	if len(conflictMsgs) == 0 && storeErr == nil {
		res.note("NOTE: the library surfaced no MODIFIED detail; the rejection is only observable " +
			"by re-reading state. Moov must verify writes rather than trust STORE's return.")
	}

	// Cleanup.
	cleanupUIDs(c, imap.UIDSetNum(uidA, uidB))
	return res
}

func cleanupUIDs(c *imapclient.Client, set imap.UIDSet) {
	_ = c.Store(set, &imap.StoreFlags{
		Op:     imap.StoreFlagsAdd,
		Silent: true,
		Flags:  []imap.Flag{imap.FlagDeleted},
	}, nil).Close()
	_ = c.UIDExpunge(set).Close()
}
