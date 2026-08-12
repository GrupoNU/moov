package mail_test

// RFC conformance suite (epic J4, L2-jmap-server §2.5).
//
// # Why this file exists instead of the official jmapio suite
//
// L2 §2.5 mandates "JMAP TestSuite (jmapio) corriendo contra el server en CI".
// That instruction was followed to its conclusion, and the conclusion — reached
// by actually running the suite against the live pilot server on 2026-08-12 —
// is that it CANNOT be used here. Two independent blockers, both verified
// rather than assumed:
//
//  1. LICENSING. github.com/jmapio/jmap-test-suite has no LICENSE file, no
//     `license` field in package.json, and no license statement in its README
//     (checked at commit 0f2c117, 2026-02-18; the GitHub API reports
//     license: null). No license means all rights reserved. Moov is a PUBLIC
//     AGPL-3.0 repository whose supply chain is part of the product (regla 3),
//     so an unlicensed dependency cannot be vendored, redistributed, or made a
//     required CI input.
//
//  2. ARCHITECTURE. The suite is not read-only-compatible in any configurable
//     way. Its runner calls cleanAccount() BEFORE the first test, which
//     enumerates the account and then DESTROYS it to seed known fixtures.
//     Observed against the pilot:
//
//     without -f: "Account is not empty (4 emails, 6 custom mailboxes).
//     Use -f to force-delete existing data."
//     with -f:    "JMAP method error: unknownMethod"   ← at Email/set
//
//     Because that is SETUP rather than a test, --filter cannot skip past it:
//     there is no subset of the ~300 tests reachable on a server without
//     Email/set. It is a phase-2 blocker by construction, not a coverage gap.
//
// The suite did earn its keep once, and the result was acted on rather than
// filed: its setup failed FIRST on `Email/query` with `filter: null`, which
// this server refused (the J3 risk map's top item). J4 implemented it
// (store.ListAccountMessages), after which the suite enumerated the account
// correctly — "4 emails, 6 custom mailboxes", the exact contents of the pilot
// mailbox. When Email/set lands in phase 2, the official suite becomes runnable
// and should be revisited THEN, license permitting.
//
// # What runs instead
//
// This suite: assertions written against the RFC text, cited clause by clause,
// driven through the real dispatch engine against a real PostgreSQL store. It
// does not claim the official suite's breadth. It claims the part of that
// breadth this phase can honestly verify, with every phase-2 gap recorded as an
// explicit skip (L2 §2.5: "nunca silencioso") rather than as silence.
//
// Requires MOOV_TEST_DATABASE_URL; skips cleanly without it.

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/GrupoNU/moov/internal/jmap"
	"github.com/GrupoNU/moov/internal/jmap/mail"
)

// newConformanceFixture seeds an account with three plain messages in INBOX.
// Deliberately simple content: this file tests PROTOCOL conformance, and the
// pathological MIME corpus is already exercised by the parser suite (S4) and by
// the J2 integration tests.
func newConformanceFixture(t *testing.T) (*fixture, []string) {
	t.Helper()
	f := newFixture(t)

	ids := make([]string, 0, 3)
	for i := range 3 {
		raw := fmt.Appendf(nil,
			"From: sender%d@example.test\r\n"+
				"To: conformance@example.test\r\n"+
				"Subject: Conformance message %d\r\n"+
				"Message-ID: <conf-%d@example.test>\r\n"+
				"Date: Mon, 10 Aug 2026 1%d:00:00 +0000\r\n"+
				"Content-Type: text/plain; charset=utf-8\r\n"+
				"\r\n"+
				"Body of conformance message %d.\r\n", i, i, i, i, i)
		id := f.seedRaw(t, raw, f.inbox, int64(i+1), 0, nil)
		ids = append(ids, mail.EncodeEmailID(id))
	}
	return f, ids
}

// queryConformance dispatches an Email/query through the full engine.
func queryConformance(t *testing.T, f *fixture, args string) (string, map[string]any) {
	t.Helper()
	registry := jmap.NewRegistry()
	mail.RegisterGetMethods(registry, f.deps)
	mail.RegisterQueryMethods(registry, f.deps)

	engine := jmap.NewEngine(registry, jmap.DefaultLimits(),
		[]string{jmap.CapCore, jmap.CapMail}, nil)

	body := fmt.Sprintf(
		`{"using":["urn:ietf:params:jmap:core","urn:ietf:params:jmap:mail"],`+
			`"methodCalls":[["Email/query",%s,"c1"]]}`, args)

	resp, rerr := engine.Process(f.callerCtx(), []byte(body), "session-1")
	if rerr != nil {
		t.Fatalf("request-level error: %v", rerr)
	}
	inv := resp.MethodResponses[0]
	return inv.Name, decodeArgs(t, inv.Args)
}

// decodeArgs decodes an invocation's arguments as generic JSON, so assertions
// are made against the WIRE shape rather than against Go structs.
func decodeArgs(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decoding args: %v", err)
	}
	return out
}

// conformanceIDs runs an Email/query and returns its ids, failing on refusal.
func conformanceIDs(t *testing.T, f *fixture, args string) []string {
	t.Helper()
	name, out := queryConformance(t, f, args)
	if name != "Email/query" {
		t.Fatalf("Email/query was refused: %v", out)
	}
	raw, _ := out["ids"].([]any)
	ids := make([]string, 0, len(raw))
	for _, v := range raw {
		s, _ := v.(string)
		ids = append(ids, s)
	}
	return ids
}

// ---------------------------------------------------------------------------
// RFC 8620 §5.5 — Foo/query
// ---------------------------------------------------------------------------

// §5.5: "If null, all objects in the account of this type are included in the
// results."
//
// This is the clause the official suite's SETUP depends on, and the one this
// server refused until J4 — which is why it leads this file.
func TestConformanceQueryNullFilterEnumeratesAccount(t *testing.T) {
	f, _ := newConformanceFixture(t)

	ids := conformanceIDs(t, f, fmt.Sprintf(`{"accountId":%q,"filter":null}`, f.accountID()))
	if len(ids) != 3 {
		t.Errorf("filter:null returned %d ids, want all 3 in the account", len(ids))
	}
}

// §5.5: "canCalculateChanges ... true if the server supports calling
// Foo/queryChanges with these filter/sort parameters."
//
// This server answers cannotCalculateChanges always (ADR §2), so the advertised
// value MUST be false. Advertising true would send a conforming client down a
// path that always fails — and the second half of this test proves the promise
// is kept rather than merely made.
func TestConformanceQueryCanCalculateChangesIsHonest(t *testing.T) {
	f, _ := newConformanceFixture(t)

	name, args := queryConformance(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":%q}}`, f.accountID(), mail.EncodeMailboxID(f.inbox.ID)))
	if name != "Email/query" {
		t.Fatalf("Email/query was refused: %v", args)
	}
	if args["canCalculateChanges"] != false {
		t.Errorf("canCalculateChanges = %v, want false", args["canCalculateChanges"])
	}
}

// §5.5: "position: UnsignedInt — The zero-based index of the first result."
// Two disjoint windows must not overlap and must reconstruct the full order.
func TestConformanceQueryPagingIsConsistent(t *testing.T) {
	f, _ := newConformanceFixture(t)
	mb := mail.EncodeMailboxID(f.inbox.ID)

	full := conformanceIDs(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":%q}}`, f.accountID(), mb))
	if len(full) != 3 {
		t.Fatalf("setup: got %d ids, want 3", len(full))
	}

	first := conformanceIDs(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":%q},"position":0,"limit":2}`, f.accountID(), mb))
	rest := conformanceIDs(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":%q},"position":2,"limit":2}`, f.accountID(), mb))

	if len(first) != 2 || len(rest) != 1 {
		t.Fatalf("pages are %d and %d, want 2 and 1", len(first), len(rest))
	}
	joined := append(append([]string{}, first...), rest...)
	for i := range full {
		if joined[i] != full[i] {
			t.Errorf("paged order diverges at %d: %q vs %q", i, joined[i], full[i])
		}
	}
}

// §5.5: "If the index is greater than or equal to the total number of objects
// in the results list, then the ids array in the response will be empty, but
// this is not an error."
func TestConformanceQueryPositionBeyondEndIsNotAnError(t *testing.T) {
	f, _ := newConformanceFixture(t)

	name, args := queryConformance(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":%q},"position":9999}`,
		f.accountID(), mail.EncodeMailboxID(f.inbox.ID)))
	if name != "Email/query" {
		t.Fatalf("a position past the end must not be an error, got: %v", args)
	}
	ids, ok := args["ids"].([]any)
	if !ok {
		t.Fatalf("ids is %T, want an empty ARRAY (never null)", args["ids"])
	}
	if len(ids) != 0 {
		t.Errorf("ids = %v, want empty", ids)
	}
}

// §5.5: "anchorNotFound — An anchor argument was supplied, but it cannot be
// found in the results of the query."
func TestConformanceQueryAnchorNotFound(t *testing.T) {
	f, _ := newConformanceFixture(t)

	name, args := queryConformance(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":%q},"anchor":"e999999"}`,
		f.accountID(), mail.EncodeMailboxID(f.inbox.ID)))
	if name != "error" || args["type"] != "anchorNotFound" {
		t.Errorf("got %q/%v, want error/anchorNotFound", name, args["type"])
	}
}

// RFC 8621 §4.4.2: "The server MUST support sorting by receivedAt."
// Ascending must be exactly the reverse of descending over the same set.
func TestConformanceQueryReceivedAtSortIsMandatory(t *testing.T) {
	f, _ := newConformanceFixture(t)
	mb := mail.EncodeMailboxID(f.inbox.ID)

	desc := conformanceIDs(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":%q},
		  "sort":[{"property":"receivedAt","isAscending":false}]}`, f.accountID(), mb))
	asc := conformanceIDs(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":%q},
		  "sort":[{"property":"receivedAt","isAscending":true}]}`, f.accountID(), mb))

	if len(desc) != 3 || len(asc) != 3 {
		t.Fatalf("got %d/%d ids, want 3 each", len(desc), len(asc))
	}
	for i := range desc {
		if desc[i] != asc[len(asc)-1-i] {
			t.Fatalf("ascending is not the reverse of descending: %v vs %v", desc, asc)
		}
	}
}

// §5.5: "unsupportedSort — The 'sort' is syntactically valid, but it includes a
// property the server does not support sorting on."
//
// Refusing is required, not merely honest: a server that silently substitutes
// its own order returns a different list than the client asked for, and the
// client's paging is then built on a false premise.
func TestConformanceQueryRefusesUnsupportedSort(t *testing.T) {
	f, _ := newConformanceFixture(t)

	name, args := queryConformance(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":%q},"sort":[{"property":"size"}]}`,
		f.accountID(), mail.EncodeMailboxID(f.inbox.ID)))
	if name != "error" || args["type"] != "unsupportedSort" {
		t.Errorf("got %q/%v, want error/unsupportedSort", name, args["type"])
	}
}

// RFC 8621 §4.4.2 lists hasKeyword among the sorts a server SHOULD support.
// Moov serves it over the bounded window (J4) — the shape Bulwark opens every
// folder with.
func TestConformanceQueryHasKeywordSort(t *testing.T) {
	f, _ := newConformanceFixture(t)
	mb := mail.EncodeMailboxID(f.inbox.ID)

	// A fourth message carrying the keyword, so the partition has both sides.
	raw := []byte("From: pinned@example.test\r\n" +
		"Subject: Pinned message\r\n" +
		"Message-ID: <conf-pinned@example.test>\r\n" +
		"Date: Mon, 10 Aug 2026 09:00:00 +0000\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\nPinned.\r\n")
	pinned := mail.EncodeEmailID(f.seedRaw(t, raw, f.inbox, 4, 0, []string{"$pinned"}))

	ids := conformanceIDs(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":%q},
		  "sort":[{"property":"hasKeyword","keyword":"$pinned","isAscending":false},
		          {"property":"receivedAt","isAscending":false}]}`, f.accountID(), mb))

	if len(ids) != 4 {
		t.Fatalf("got %d ids, want 4", len(ids))
	}
	// It is the OLDEST message by date, so only the keyword partition can put
	// it first — which is exactly what this asserts.
	if ids[0] != pinned {
		t.Errorf("first id = %q, want the pinned message %q (order: %v)", ids[0], pinned, ids)
	}
}

// ---------------------------------------------------------------------------
// RFC 8620 §5.1 — Foo/get
// ---------------------------------------------------------------------------

// §5.1: "notFound: Id[] — The ids of the objects that were not found." An id
// the server never issued belongs there, not in an error.
func TestConformanceGetReportsNotFound(t *testing.T) {
	f, _ := newConformanceFixture(t)

	resp := f.call(t, "Email/get", fmt.Sprintf(`{"accountId":%q,"ids":["e999999"]}`, f.accountID()))

	notFound, _ := resp["notFound"].([]any)
	if len(notFound) != 1 {
		t.Errorf("notFound = %v, want the one unknown id", resp["notFound"])
	}
	if list, _ := resp["list"].([]any); len(list) != 0 {
		t.Errorf("list = %v, want it empty", list)
	}
}

// RFC 8621 §2: a Mailbox "MUST" carry these properties.
func TestConformanceMailboxRequiredProperties(t *testing.T) {
	f, _ := newConformanceFixture(t)

	resp := f.call(t, "Mailbox/get", fmt.Sprintf(`{"accountId":%q,"ids":null}`, f.accountID()))
	list, _ := resp["list"].([]any)
	if len(list) == 0 {
		t.Fatal("Mailbox/get returned no mailboxes")
	}
	mb, _ := list[0].(map[string]any)
	for _, required := range []string{
		"id", "name", "parentId", "role", "sortOrder",
		"totalEmails", "unreadEmails", "totalThreads", "unreadThreads", "myRights",
	} {
		if _, ok := mb[required]; !ok {
			t.Errorf("Mailbox is missing the required property %q (RFC 8621 §2)", required)
		}
	}
	if resp["state"] == nil {
		t.Error("Mailbox/get must return a state string (RFC 8620 §5.1)")
	}
}

// RFC 8621 §4.2: bodyValues are returned only when asked for, and every
// returned value carries isTruncated (§4.1.4).
func TestConformanceEmailBodyValuesAreOptIn(t *testing.T) {
	f, ids := newConformanceFixture(t)

	resp := f.call(t, "Email/get", fmt.Sprintf(
		`{"accountId":%q,"ids":[%q],"properties":["id","bodyValues"]}`, f.accountID(), ids[0]))
	list, _ := resp["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("got %d emails, want 1", len(list))
	}
	if bv, _ := list[0].(map[string]any)["bodyValues"].(map[string]any); len(bv) != 0 {
		t.Errorf("bodyValues = %v, want empty without fetchTextBodyValues (§4.2)", bv)
	}

	resp = f.call(t, "Email/get", fmt.Sprintf(
		`{"accountId":%q,"ids":[%q],"properties":["id","bodyValues","textBody"],
		  "fetchTextBodyValues":true}`, f.accountID(), ids[0]))
	list, _ = resp["list"].([]any)
	bv, _ := list[0].(map[string]any)["bodyValues"].(map[string]any)
	if len(bv) == 0 {
		t.Fatal("bodyValues is empty with fetchTextBodyValues:true")
	}
	for partID, v := range bv {
		val, _ := v.(map[string]any)
		if _, ok := val["value"]; !ok {
			t.Errorf("bodyValue[%s] has no \"value\" property: %v", partID, val)
		}
		if _, ok := val["isTruncated"]; !ok {
			t.Errorf("bodyValue[%s] has no \"isTruncated\" (§4.1.4): %v", partID, val)
		}
	}
}

// The tenancy boundary: an accountId that is not the caller's must be rejected.
func TestConformanceForeignAccountIsRejected(t *testing.T) {
	f, _ := newConformanceFixture(t)

	registry := jmap.NewRegistry()
	mail.RegisterGetMethods(registry, f.deps)
	engine := jmap.NewEngine(registry, jmap.DefaultLimits(),
		[]string{jmap.CapCore, jmap.CapMail}, nil)

	body := `{"using":["urn:ietf:params:jmap:core","urn:ietf:params:jmap:mail"],
		"methodCalls":[["Mailbox/get",{"accountId":"a999999","ids":null},"c1"]]}`
	resp, rerr := engine.Process(f.callerCtx(), []byte(body), "session-1")
	if rerr != nil {
		t.Fatalf("request-level error: %v", rerr)
	}
	inv := resp.MethodResponses[0]
	if inv.Name != "error" {
		t.Fatalf("method = %q, want error for a foreign accountId", inv.Name)
	}
	args := decodeArgs(t, inv.Args)
	if args["type"] != "accountNotFound" {
		t.Errorf("type = %v, want accountNotFound", args["type"])
	}
}

// ---------------------------------------------------------------------------
// Explicit phase-2 gaps (L2 §2.5: skips are "nunca silencioso")
// ---------------------------------------------------------------------------

// Everything this phase does not implement is listed HERE, one skipping subtest
// each, so the gap appears in the CI log instead of being absent from it. Each
// names why, and what closes it.
func TestConformancePhase2Gaps(t *testing.T) {
	gaps := []struct{ name, reason string }{
		{"Email_set", "writes are phase 2 (L2 §1 no-scope); the session advertises isReadOnly:true. " +
			"THIS is what blocks the official jmapio suite, whose setup deletes the account first"},
		{"Mailbox_set", "writes are phase 2; mayCreateTopLevelMailbox is advertised false"},
		{"EmailSubmission", "submission is phase 2; the capability is deliberately NOT advertised, " +
			"which is why Bulwark's Identity/get gets unknownCapability (observed, benign)"},
		{"Blob_upload", "uploadUrl is advertised but answers 501 in phase 1"},
		{"EventSource_push", "SSE push is phase 2 (ADR §2); eventSourceUrl answers 501"},
		{"SearchSnippet_get", "phase 3 (L2 §1)"},
		{"VacationResponse", "phase 3; the capability is not advertised"},
		{"Email_queryChanges", "answered with cannotCalculateChanges BY DESIGN (ADR §2), " +
			"pre-announced via canCalculateChanges:false — conforming, not missing"},
		{"cross_account", "one account per credential in phase 1; the official suite's " +
			"cross-account tests would skip for the same reason"},
	}
	for _, g := range gaps {
		t.Run(g.name, func(t *testing.T) {
			t.Skipf("phase 1 does not implement this: %s", g.reason)
		})
	}
}
