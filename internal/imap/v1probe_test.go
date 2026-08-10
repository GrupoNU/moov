package imap

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// V1 — the labels/METADATA validation that L2 §2.3 makes an explicit E2
// acceptance criterion and the safety net of arbitrage A6.
//
// A6 puts label ASSIGNMENT in IMAP keywords and label DEFINITION in an IMAP
// METADATA annotation, so that both are reconstructible from Dovecot and the
// "Moov is a cache" invariant of ADR-001 survives labels. That only holds if
// Dovecot's METADATA and Maildir keywords behave well enough in practice. This
// measures it.
//
// It is a measurement harness rather than a pass/fail test: it reports numbers
// the design needs, and the findings are written up in
// docs/spikes/V1-metadata-dovecot.md. It only runs when explicitly asked for,
// because it creates and deletes a lot of state:
//
//	MOOV_IMAP_V1_PROBE=1 go test -run TestV1 -v ./internal/imap/
//
// It must run against a DEDICATED test mailbox.

func requireV1Probe(t *testing.T) {
	t.Helper()
	if os.Getenv("MOOV_IMAP_V1_PROBE") != "1" {
		t.Skip("V1 probe: set MOOV_IMAP_V1_PROBE=1 to run (it writes a lot of state)")
	}
}

// TestV1MetadataMaxSize finds the largest annotation Dovecot accepts, by
// binary search between a known-good and a known-bad size.
//
// It matters because it is the ceiling on how many labels an account can have:
// the whole definition set lives in one annotation.
func TestV1MetadataMaxSize(t *testing.T) {
	requireV1Probe(t)
	ctx := context.Background()
	cl := connectForTest(t)
	ops := cl.Metadata()

	entry := "/private/vendor/moov/v1-size-probe"
	t.Cleanup(func() {
		_ = ops.Set(context.Background(), "INBOX", []Annotation{{Name: entry, Value: nil}})
	})

	tryFn := func(size int) (bool, error) {
		value := []byte(strings.Repeat("x", size))
		if err := ops.Set(ctx, "INBOX", []Annotation{{Name: entry, Value: value}}); err != nil {
			return false, err
		}
		// Accepting the write is not enough: it has to come back intact. A
		// server that truncates silently is worse than one that refuses.
		got, err := ops.Get(ctx, "INBOX", []string{entry})
		if err != nil {
			return false, err
		}
		if len(got) != 1 || len(got[0].Value) != size {
			return false, fmt.Errorf("wrote %d bytes, read back %d", size, len(got[0].Value))
		}
		return true, nil
	}

	// Grow geometrically until something breaks, then bisect.
	lastOK := 0
	firstBad := 0
	for size := 1024; size <= 1<<24; size *= 4 {
		ok, err := tryFn(size)
		if !ok {
			firstBad = size
			t.Logf("size %d bytes REJECTED: %v", size, err)
			break
		}
		lastOK = size
		t.Logf("size %d bytes accepted and read back intact", size)
	}

	if firstBad == 0 {
		t.Logf("V1 RESULT max-annotation-size: >= %d bytes (no limit found up to 16 MiB)", lastOK)
		return
	}

	lo, hi := lastOK, firstBad
	for hi-lo > 1024 {
		mid := (lo + hi) / 2
		if ok, _ := tryFn(mid); ok {
			lo = mid
		} else {
			hi = mid
		}
	}
	t.Logf("V1 RESULT max-annotation-size: ~%d bytes accepted, %d rejected", lo, hi)
}

// TestV1MetadataPersistence checks an annotation survives a reconnect. If it
// did not, the definition would not be reconstructible and A6's fallback would
// have to be taken.
func TestV1MetadataPersistence(t *testing.T) {
	requireV1Probe(t)
	ctx := context.Background()

	entry := "/private/vendor/moov/v1-persist-probe"
	value := []byte(`{"labels":[{"keyword":"$MoovL1","name":"Persistencia","color":"#00695c"}]}`)

	writer := connectForTest(t)
	if err := writer.Metadata().Set(ctx, "INBOX", []Annotation{{Name: entry, Value: value}}); err != nil {
		t.Fatalf("writing the annotation: %v", err)
	}
	t.Cleanup(func() {
		cleanup := connectForTest(t)
		_ = cleanup.Metadata().Set(context.Background(), "INBOX", []Annotation{{Name: entry, Value: nil}})
	})
	if err := writer.Close(); err != nil {
		t.Logf("closing the writer: %v", err)
	}

	// A brand-new connection: nothing is carried over in memory.
	time.Sleep(500 * time.Millisecond)
	reader := connectForTest(t)
	got, err := reader.Metadata().Get(ctx, "INBOX", []string{entry})
	if err != nil {
		t.Fatalf("reading the annotation back: %v", err)
	}
	if len(got) != 1 || string(got[0].Value) != string(value) {
		t.Errorf("V1 RESULT persistence: FAILED — read back %q, want %q", got[0].Value, value)
		return
	}
	t.Log("V1 RESULT persistence: annotation survived a full reconnect")
}

// TestV1MetadataPrivateVsShared compares the /private/ and /shared/ prefixes.
//
// A6 specifies /private/ deliberately: the label definitions belong to the
// account, not to everyone with access to the mailbox. This confirms /private/
// works and records what /shared/ does on our server, since an ACL-sharing
// deployment might behave differently.
func TestV1MetadataPrivateVsShared(t *testing.T) {
	requireV1Probe(t)
	ctx := context.Background()
	cl := connectForTest(t)
	ops := cl.Metadata()

	for _, prefix := range []string{"/private", "/shared"} {
		entry := prefix + "/vendor/moov/v1-scope-probe"
		value := []byte("scope-" + prefix)

		err := ops.Set(ctx, "INBOX", []Annotation{{Name: entry, Value: value}})
		if err != nil {
			t.Logf("V1 RESULT scope %s: SET rejected: %v", prefix, err)
			continue
		}
		got, getErr := ops.Get(ctx, "INBOX", []string{entry})
		if getErr != nil {
			t.Logf("V1 RESULT scope %s: SET accepted but GET failed: %v", prefix, getErr)
			continue
		}
		if len(got) == 1 && string(got[0].Value) == string(value) {
			t.Logf("V1 RESULT scope %s: read-write, round trip intact", prefix)
		} else {
			t.Logf("V1 RESULT scope %s: SET accepted but read back %q", prefix, got[0].Value)
		}

		entryToClean := entry
		t.Cleanup(func() {
			_ = ops.Set(context.Background(), "INBOX", []Annotation{{Name: entryToClean, Value: nil}})
		})
	}

	// Server-level metadata (empty mailbox name) is the other axis: if it
	// worked, one annotation could hold the definitions for every mailbox.
	serverEntry := "/private/vendor/moov/v1-server-probe"
	if err := ops.Set(ctx, "", []Annotation{{Name: serverEntry, Value: []byte("server-level")}}); err != nil {
		t.Logf("V1 RESULT server-level metadata: rejected: %v", err)
	} else {
		t.Log("V1 RESULT server-level metadata: accepted")
		t.Cleanup(func() {
			_ = ops.Set(context.Background(), "", []Annotation{{Name: serverEntry, Value: nil}})
		})
	}
}

// TestV1KeywordCeiling measures how many distinct Maildir keywords a folder
// tolerates before the server refuses or the behavior degrades.
//
// This is the number A6 hangs on. Maildir encodes keywords as single
// characters in a per-folder `dovecot-keywords` file, and the classic limit is
// 26 (a-z). If it is really 26, Moov can offer at most that many labels per
// folder and must reject label creation past it with a clear error rather than
// silently creating a label that does not round-trip.
func TestV1KeywordCeiling(t *testing.T) {
	requireV1Probe(t)
	ctx := context.Background()
	cl := connectForTest(t)
	folder := scratchFolder(t, cl, "v1-keywords")

	appendTestMessage(t, cl, folder, "v1-keyword-target")
	if _, err := cl.SelectQResync(ctx, folder, 0, 0); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	uids := collectUIDs(ctx, t, cl)
	if len(uids) != 1 {
		t.Fatalf("expected 1 UID, got %v", uids)
	}
	target := uids[0]

	const attempt = 60
	var (
		accepted    int
		firstReject int
		firstLost   int
	)

	for i := 1; i <= attempt; i++ {
		keyword := fmt.Sprintf("$MoovL%d", i)

		if _, err := cl.StoreFlags(ctx, []UID{target},
			FlagDelta{Op: FlagsAdd, Flags: []string{keyword}}, 0); err != nil {
			firstReject = i
			t.Logf("keyword #%d (%s) REJECTED by the server: %v", i, keyword, err)
			break
		}

		// Accepting the STORE is not the same as storing it. A silently
		// dropped keyword is the dangerous outcome: the label would look
		// applied and vanish on the next sync.
		flags := flagsOf(ctx, t, cl, target)
		if !containsString(flags, keyword) {
			firstLost = i
			t.Logf("keyword #%d (%s) was accepted but did NOT persist — silent loss", i, keyword)
			break
		}
		accepted = i
	}

	switch {
	case firstReject > 0:
		t.Logf("V1 RESULT keyword-ceiling: %d keywords persisted; #%d refused outright",
			accepted, firstReject)
	case firstLost > 0:
		t.Logf("V1 RESULT keyword-ceiling: %d keywords persisted; #%d silently dropped "+
			"(the dangerous mode — Moov must refuse label creation at this point)",
			accepted, firstLost)
	default:
		t.Logf("V1 RESULT keyword-ceiling: all %d keywords persisted; no limit reached", accepted)
	}

	// Whatever the ceiling, everything below it must still be intact: a
	// partial loss would be worse than a hard limit.
	flags := flagsOf(ctx, t, cl, target)
	intact := 0
	for i := 1; i <= accepted; i++ {
		if containsString(flags, fmt.Sprintf("$MoovL%d", i)) {
			intact++
		}
	}
	t.Logf("V1 RESULT keyword-integrity: %d/%d keywords still present after the run",
		intact, accepted)
	if intact != accepted {
		t.Errorf("keywords were lost retroactively: %d of %d survived", intact, accepted)
	}
}
