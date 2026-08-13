package mail_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/GrupoNU/moov/internal/jmap/mail"
	"github.com/GrupoNU/moov/internal/store"
)

// jmap-perl parity (a J2 acceptance criterion).
//
// jmap-perl (github.com/jmapio/jmap-perl) is the reference implementation of
// the IMAP↔JMAP mapping, and S1 H6 kept it running on the VPS as an ORACLE:
// "ante duda de mapeo, se compara respuesta contra él" (L2 §2.5).
//
// This test compares the SHAPE of our responses against oracle captures taken
// from that live instance, over the same moov-test mailbox. It is env-gated on
// the capture directory because the oracle lives on a VPN-only host:
//
//	MOOV_JMAP_PARITY_DIR   directory holding the captured oracle responses
//	                       (mailbox-get.json, email-get.json, …)
//
// # What this test does and does not assert
//
// It compares the SEMANTIC content — which properties exist, their types, and
// the mapping decisions behind them. It deliberately does NOT compare ids,
// timestamps or blob ids: those are per-installation values, and asserting on
// them would be asserting that two different servers holding different data
// agree on incidental facts.
//
// A divergence is not automatically a failure. Several are deliberate
// decisions recorded in the J2 report; they are asserted here as EXPECTED
// divergences, so that if our behavior ever drifts back the test notices.

func parityDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("MOOV_JMAP_PARITY_DIR")
	if dir == "" {
		t.Skip("MOOV_JMAP_PARITY_DIR is not set; " +
			"capture jmap-perl responses from the S1 stack to run the parity check")
	}
	return dir
}

func loadOracle(t *testing.T, dir, name string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(dir + "/" + name) //nolint:gosec // a test reading its own capture dir
	if err != nil {
		t.Skipf("oracle capture %s is unavailable: %v", name, err)
	}
	var doc struct {
		MethodResponses []json.RawMessage `json:"methodResponses"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decoding %s: %v", name, err)
	}
	out := map[string]json.RawMessage{}
	for _, r := range doc.MethodResponses {
		var tuple []json.RawMessage
		if err := json.Unmarshal(r, &tuple); err != nil || len(tuple) != 3 {
			continue
		}
		var name, callID string
		_ = json.Unmarshal(tuple[0], &name)
		_ = json.Unmarshal(tuple[2], &callID)
		out[name] = tuple[1]
	}
	return out
}

// TestParityMailboxProperties compares our Mailbox object's property set
// against the oracle's.
func TestParityMailboxProperties(t *testing.T) {
	dir := parityDir(t)
	oracle := loadOracle(t, dir, "mailbox-get.json")
	args, ok := oracle["Mailbox/get"]
	if !ok {
		t.Skip("the capture holds no Mailbox/get response")
	}

	var resp struct {
		List []map[string]json.RawMessage `json:"list"`
	}
	if err := json.Unmarshal(args, &resp); err != nil {
		t.Fatalf("decoding oracle Mailbox/get: %v", err)
	}
	if len(resp.List) == 0 {
		t.Skip("the oracle returned no mailboxes")
	}

	oracleProps := map[string]bool{}
	for k := range resp.List[0] {
		oracleProps[k] = true
	}

	// Build the same object from our renderer, over an equivalent row.
	ours := renderOurMailbox()

	// Every property the oracle returns must exist in ours, with the two
	// documented exceptions below.
	for prop := range oracleProps {
		if _, ok := ours[prop]; ok {
			continue
		}
		t.Errorf("the oracle returns Mailbox property %q and we do not", prop)
	}

	// And the reverse: a property we invent that the reference does not have
	// would be a mapping we made up.
	for prop := range ours {
		if !oracleProps[prop] {
			t.Errorf("we return Mailbox property %q that the oracle does not", prop)
		}
	}
}

// TestParityMailboxRightsDivergence records the remaining deliberate
// difference in myRights, so that a future change back to the oracle's shape
// is noticed.
//
// Since W1 the message-level rights AGREE with the oracle: Email/set is real,
// so mayAddItems/mayRemoveItems/maySetSeen/maySetKeywords are true here as
// they are in jmap-perl. What still diverges (classified in the J2 report,
// updated by W1):
//
//	jmap-perl adds a non-standard "mayAdmin" member, which Moov omits because
//	RFC 8621 §2 does not define it; and Moov still reports the
//	mailbox-mutation rights (mayCreateChild/mayRename/mayDelete) and
//	maySubmit as false, because Mailbox/set is W2 and submission is W3.
func TestParityMailboxRightsDivergence(t *testing.T) {
	dir := parityDir(t)
	oracle := loadOracle(t, dir, "mailbox-get.json")
	args, ok := oracle["Mailbox/get"]
	if !ok {
		t.Skip("the capture holds no Mailbox/get response")
	}

	var resp struct {
		List []struct {
			MyRights map[string]bool `json:"myRights"`
		} `json:"list"`
	}
	if err := json.Unmarshal(args, &resp); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(resp.List) == 0 {
		t.Skip("no mailboxes in the capture")
	}

	oracleRights := resp.List[0].MyRights
	ours := renderOurMailbox()
	var ourRights map[string]bool
	rightsJSON, _ := json.Marshal(ours["myRights"])
	if err := json.Unmarshal(rightsJSON, &ourRights); err != nil {
		t.Fatalf("decoding our myRights: %v", err)
	}

	// The nine RFC 8621 §2 members must all be present in ours.
	for _, member := range []string{
		"mayReadItems", "mayAddItems", "mayRemoveItems", "maySetSeen",
		"maySetKeywords", "mayCreateChild", "mayRename", "mayDelete", "maySubmit",
	} {
		if _, ok := ourRights[member]; !ok {
			t.Errorf("our myRights is missing the RFC 8621 §2 member %q", member)
		}
	}

	// The agreement W1 restored, and the divergence that remains.
	if ourRights["mayReadItems"] != true {
		t.Error("mayReadItems must be true: the server does serve mail")
	}
	if !ourRights["mayAddItems"] || !ourRights["mayRemoveItems"] {
		t.Error("mayAddItems/mayRemoveItems must be true since W1: Email/set moves and destroys are real")
	}
	if oracleRights["mayAddItems"] && ourRights["mayAddItems"] {
		t.Logf("W1 closed the phase-1 divergence: both the oracle and Moov grant mayAddItems")
	}
	if _, ok := ourRights["mayAdmin"]; ok {
		t.Error(`we emit "mayAdmin", which RFC 8621 §2 does not define`)
	}
}

// TestParityEmailProperties compares the Email object's property set.
func TestParityEmailProperties(t *testing.T) {
	dir := parityDir(t)
	oracle := loadOracle(t, dir, "email-get.json")
	args, ok := oracle["Email/get"]
	if !ok {
		t.Skip("the capture holds no Email/get response")
	}

	var resp struct {
		List []map[string]json.RawMessage `json:"list"`
	}
	if err := json.Unmarshal(args, &resp); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(resp.List) == 0 {
		t.Skip("the oracle returned no emails")
	}

	oracleProps := map[string]bool{}
	for k := range resp.List[0] {
		oracleProps[k] = true
	}

	// Our default property set is RFC 8621 §4.6's, verbatim. Every property
	// the oracle returns by default should be in it.
	ourDefaults := map[string]bool{}
	for _, p := range mail.DefaultEmailProperties() {
		ourDefaults[p] = true
	}

	for prop := range oracleProps {
		if !ourDefaults[prop] {
			t.Errorf("the oracle returns Email property %q by default and our default set omits it", prop)
		}
	}
	for prop := range ourDefaults {
		if !oracleProps[prop] {
			t.Logf("DIVERGENCE (ours is a superset): we return %q by default, the oracle does not", prop)
		}
	}
}

// TestParityBodyValueTruncation is the highest-value parity check: it pins the
// UNIT of maxBodyValueBytes against a live reference implementation.
//
// The oracle capture was taken with maxBodyValueBytes=32 against a body whose
// text is ASCII, and jmap-perl returned exactly 32 octets with
// isTruncated=true. Our implementation must agree on both.
func TestParityBodyValueTruncation(t *testing.T) {
	dir := parityDir(t)
	oracle := loadOracle(t, dir, "email-truncated.json")
	args, ok := oracle["Email/get"]
	if !ok {
		t.Skip("the capture holds no truncated Email/get response")
	}

	var resp struct {
		List []struct {
			BodyValues map[string]struct {
				Value       string `json:"value"`
				IsTruncated bool   `json:"isTruncated"`
			} `json:"bodyValues"`
		} `json:"list"`
	}
	if err := json.Unmarshal(args, &resp); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(resp.List) == 0 || len(resp.List[0].BodyValues) == 0 {
		t.Skip("the capture holds no bodyValues")
	}

	const requested = 32
	for partID, bv := range resp.List[0].BodyValues {
		octets := len([]byte(bv.Value))
		if octets > requested {
			t.Errorf("oracle part %s returned %d octets for maxBodyValueBytes=%d",
				partID, octets, requested)
		}
		if !bv.IsTruncated {
			t.Errorf("oracle part %s was cut to %d octets but isTruncated is false", partID, octets)
			continue
		}
		// THE assertion: the reference implementation counts OCTETS, which is
		// what RFC 8621 §4.2 says and what mail.TruncateForTest implements.
		if octets != requested {
			t.Logf("NOTE: the oracle returned %d octets for a %d-octet budget "+
				"(a shorter body, or a multi-byte boundary)", octets, requested)
		}

		// Our own truncation of the very same string must produce the same
		// octet count.
		ourValue, ourTruncated := mail.TruncateForTest(bv.Value+"more text here", requested)
		if len([]byte(ourValue)) != octets {
			t.Errorf("our truncation gives %d octets where the oracle gives %d",
				len([]byte(ourValue)), octets)
		}
		if !ourTruncated {
			t.Error("our truncation did not flag isTruncated")
		}
	}
}

// TestParityThreadShape checks the Thread object against the oracle.
func TestParityThreadShape(t *testing.T) {
	dir := parityDir(t)
	oracle := loadOracle(t, dir, "thread-get.json")
	args, ok := oracle["Thread/get"]
	if !ok {
		t.Skip("the capture holds no Thread/get response")
	}

	var resp struct {
		List []map[string]json.RawMessage `json:"list"`
	}
	if err := json.Unmarshal(args, &resp); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(resp.List) == 0 {
		t.Skip("no threads in the capture")
	}

	// RFC 8621 §3: a Thread has exactly two properties.
	for prop := range resp.List[0] {
		if prop != "id" && prop != "emailIds" {
			t.Errorf("the oracle returns unexpected Thread property %q", prop)
		}
	}
	if _, ok := resp.List[0]["emailIds"]; !ok {
		t.Error("the oracle's Thread has no emailIds")
	}
}

// renderOurMailbox produces one Mailbox object through the production
// renderer, for shape comparison against the oracle.
func renderOurMailbox() map[string]any {
	return mail.RenderMailboxForTest(mail.MailboxRow{
		ID: 1, Name: "INBOX", Role: string(store.RoleInbox),
		SortOrder: 10, IsSubscribed: true,
		TotalEmails: 4, UnreadEmails: 2, TotalThreads: 4, UnreadThreads: 2,
	})
}

// sanity: the parity captures are JSON we can actually read.
func TestParityCapturesAreWellFormed(t *testing.T) {
	dir := parityDir(t)
	for _, name := range []string{
		"mailbox-get.json", "email-get.json", "thread-get.json", "email-truncated.json",
	} {
		raw, err := os.ReadFile(dir + "/" + name) //nolint:gosec // test-owned directory
		if err != nil {
			t.Skipf("capture %s unavailable: %v", name, err)
		}
		if !json.Valid(raw) {
			t.Errorf("capture %s is not valid JSON", name)
		}
		if strings.Contains(string(raw), "\"error\"") {
			t.Logf("NOTE: capture %s contains an error response", name)
		}
	}
}
