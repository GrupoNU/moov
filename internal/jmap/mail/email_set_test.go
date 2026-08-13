package mail

import (
	"fmt"
	"testing"
)

// Email/set handler tests (W1): the RFC 8620 §5.3 /set mechanics and the RFC
// 8621 §4.6 update grammar, against the fake writer — the executor's own
// behavior has its suite in internal/sync.

// setFixture seeds one account with two mailboxes and two messages, which is
// enough to exercise every /set shape.
func setFixture() (*fakeReaders, *Deps) {
	f := newFakeReaders()
	f.mailboxes[testAccountID] = []MailboxRow{
		sampleMailbox(1, "INBOX", "inbox", 2, 1),
		sampleMailbox(2, "Archive", "archive", 0, 0),
	}
	f.emails[testAccountID] = []EmailRow{
		sampleEmail(10, "first"),
		sampleEmail(11, "second"),
	}
	// A foreign message that must be indistinguishable from a missing one.
	f.emails[otherAccountID] = []EmailRow{sampleEmail(99, "secret")}
	return f, f.deps()
}

func setArgs(body string) string {
	return `{"accountId":"` + testAccountJMAPID() + `",` + body + `}`
}

func emailID(id int64) string { return EncodeEmailID(id) }

func setErrorOf(t *testing.T, resp map[string]any, bucket, wire string) map[string]any {
	t.Helper()
	m, ok := resp[bucket].(map[string]any)
	if !ok {
		t.Fatalf("%s is %T, want an object (response: %v)", bucket, resp[bucket], resp)
	}
	e, ok := m[wire].(map[string]any)
	if !ok {
		t.Fatalf("%s has no entry for %s: %v", bucket, wire, m)
	}
	return e
}

// ---------------------------------------------------------------------------
// keywords
// ---------------------------------------------------------------------------

func TestEmailSetKeywordsFullSetTranslatesAndReplaces(t *testing.T) {
	f, d := setFixture()

	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"keywords":{"$seen":true,"$flagged":true,"ProjectX":true}}}`))

	if _, ok := object(t, resp, "updated")[emailID(10)]; !ok {
		t.Fatalf("updated lacks the id: %v", resp)
	}
	if len(f.flagCalls) != 1 {
		t.Fatalf("writer received %d flag calls, want 1", len(f.flagCalls))
	}
	call := f.flagCalls[0]
	if call.accountID != testAccountID || call.messageID != 10 {
		t.Errorf("call = %+v, want account %d message 10", call, testAccountID)
	}
	if !call.change.Replace {
		t.Error("a whole-property keywords write must be Replace (RFC 8621 §4.6 full-set semantics)")
	}
	// $seen/$flagged become bare system flag names; the custom keyword
	// passes through with its case preserved.
	want := map[string]bool{"seen": true, "flagged": true, "ProjectX": true}
	if len(call.change.Flags) != len(want) {
		t.Fatalf("flags = %v, want %v", call.change.Flags, want)
	}
	for _, fl := range call.change.Flags {
		if !want[fl] {
			t.Errorf("unexpected flag %q in %v", fl, call.change.Flags)
		}
	}
}

func TestEmailSetKeywordsPatchSyntax(t *testing.T) {
	f, d := setFixture()

	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"keywords/$flagged":true,"keywords/old~1tag":null}}`))

	if _, ok := object(t, resp, "updated")[emailID(10)]; !ok {
		t.Fatalf("updated lacks the id: %v", resp)
	}
	call := f.flagCalls[0]
	if call.change.Replace {
		t.Error("a keyword patch must not be a Replace")
	}
	if len(call.change.Add) != 1 || call.change.Add[0] != "flagged" {
		t.Errorf("Add = %v, want [flagged]", call.change.Add)
	}
	// The RFC 6901 escape: "old~1tag" is the keyword "old/tag".
	if len(call.change.Remove) != 1 || call.change.Remove[0] != "old/tag" {
		t.Errorf("Remove = %v, want [old/tag] (JSON Pointer ~1 unescaped)", call.change.Remove)
	}
}

func TestEmailSetKeywordFalseIsInvalid(t *testing.T) {
	f, d := setFixture()

	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"keywords/$seen":false}}`))

	e := setErrorOf(t, resp, "notUpdated", emailID(10))
	if e["type"] != setErrInvalidProperties {
		t.Errorf("type = %v, want invalidProperties (a keyword value MUST be true, RFC 8621 §4.1.1)", e["type"])
	}
	if len(f.flagCalls) != 0 {
		t.Error("an invalid patch reached the writer")
	}
}

func TestEmailSetInvalidKeywordGrammarIsRejected(t *testing.T) {
	f, d := setFixture()

	// '(' is an IMAP atom-special: this keyword could never become a flag.
	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"keywords":{"bad(kw":true}}}`))

	e := setErrorOf(t, resp, "notUpdated", emailID(10))
	if e["type"] != setErrInvalidProperties {
		t.Errorf("type = %v, want invalidProperties", e["type"])
	}
	if len(f.flagCalls) != 0 {
		t.Error("an invalid keyword reached the writer")
	}
}

func TestEmailSetPrefixConflictIsInvalidPatch(t *testing.T) {
	f, d := setFixture()

	// §5.3: a pointer must not be the prefix of another in one PatchObject.
	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"keywords":{"$seen":true},"keywords/$flagged":true}}`))

	e := setErrorOf(t, resp, "notUpdated", emailID(10))
	if e["type"] != setErrInvalidPatch {
		t.Errorf("type = %v, want invalidPatch", e["type"])
	}
	if len(f.flagCalls) != 0 {
		t.Error("a conflicting patch reached the writer")
	}
}

func TestEmailSetImmutablePropertyIsInvalidProperties(t *testing.T) {
	f, d := setFixture()

	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"subject":"new subject"}}`))

	e := setErrorOf(t, resp, "notUpdated", emailID(10))
	if e["type"] != setErrInvalidProperties {
		t.Errorf("type = %v, want invalidProperties (RFC 8621 §4.6: immutable)", e["type"])
	}
	props, _ := e["properties"].([]any)
	if len(props) != 1 || props[0] != "subject" {
		t.Errorf("properties = %v, want [subject] (§5.3: SHOULD list the invalid properties)", props)
	}
	if len(f.flagCalls)+len(f.moveCalls) != 0 {
		t.Error("an immutable-property update reached the writer")
	}
}

// ---------------------------------------------------------------------------
// mailboxIds and the one-mailbox constraint
// ---------------------------------------------------------------------------

func TestEmailSetMoveByFullSet(t *testing.T) {
	f, d := setFixture()

	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"mailboxIds":{"`+EncodeMailboxID(2)+`":true}}}`))

	if _, ok := object(t, resp, "updated")[emailID(10)]; !ok {
		t.Fatalf("updated lacks the id: %v", resp)
	}
	if len(f.moveCalls) != 1 || f.moveCalls[0].mailboxID != 2 {
		t.Fatalf("moveCalls = %+v, want one move to mailbox 2", f.moveCalls)
	}
}

func TestEmailSetMoveByPatch(t *testing.T) {
	f, d := setFixture()

	// The canonical client move: add the target, null the source. The net
	// membership is exactly one mailbox, so it must be accepted.
	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"mailboxIds/`+EncodeMailboxID(2)+`":true,`+
			`"mailboxIds/`+EncodeMailboxID(1)+`":null}}`))

	if _, ok := object(t, resp, "updated")[emailID(10)]; !ok {
		t.Fatalf("updated lacks the id: %v", resp)
	}
	if len(f.moveCalls) != 1 || f.moveCalls[0].mailboxID != 2 {
		t.Fatalf("moveCalls = %+v, want one move to mailbox 2", f.moveCalls)
	}
}

func TestEmailSetMultiMailboxViolatesTheConstraint(t *testing.T) {
	f, d := setFixture()

	for name, body := range map[string]string{
		"full-set with two": `{"mailboxIds":{"` + EncodeMailboxID(1) + `":true,"` + EncodeMailboxID(2) + `":true}}`,
		"patch adding one":  `{"mailboxIds/` + EncodeMailboxID(2) + `":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			resp := callGet(t, d.handleEmailSet, setArgs(
				`"update":{"`+emailID(10)+`":`+body+`}`))
			e := setErrorOf(t, resp, "notUpdated", emailID(10))
			if e["type"] != setErrInvalidProperties {
				t.Errorf("type = %v, want invalidProperties (one mailbox per message in phase 2)", e["type"])
			}
			if desc, _ := e["description"].(string); desc == "" {
				t.Error("the one-mailbox refusal must explain itself")
			}
			if len(f.moveCalls) != 0 {
				t.Error("a constraint-violating move reached the writer")
			}
		})
	}
}

func TestEmailSetRemovingSoleMailboxSaysUseDestroy(t *testing.T) {
	f, d := setFixture()

	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"mailboxIds/`+EncodeMailboxID(1)+`":null}}`))

	e := setErrorOf(t, resp, "notUpdated", emailID(10))
	if e["type"] != setErrInvalidProperties {
		t.Errorf("type = %v, want invalidProperties", e["type"])
	}
	if len(f.moveCalls)+len(f.destroyCalls) != 0 {
		t.Error("an empty-membership update reached the writer")
	}
}

func TestEmailSetNoOpMailboxFullSetDoesNotMove(t *testing.T) {
	f, d := setFixture()

	// Full-set naming the CURRENT mailbox: valid, and no move is issued.
	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"mailboxIds":{"`+EncodeMailboxID(1)+`":true}}}`))

	if _, ok := object(t, resp, "updated")[emailID(10)]; !ok {
		t.Fatalf("updated lacks the id: %v", resp)
	}
	if len(f.moveCalls) != 0 {
		t.Errorf("a no-op membership update issued a move: %+v", f.moveCalls)
	}
}

// ---------------------------------------------------------------------------
// per-record granularity, ifInState, create, destroy
// ---------------------------------------------------------------------------

func TestEmailSetOneBadIdNeverFailsTheBatch(t *testing.T) {
	f, d := setFixture()

	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{`+
			`"`+emailID(10)+`":{"keywords/$seen":true},`+
			`"garbage":{"keywords/$seen":true},`+
			`"`+emailID(99)+`":{"keywords/$seen":true}}`)) // foreign account's message

	if _, ok := object(t, resp, "updated")[emailID(10)]; !ok {
		t.Fatalf("the good update did not land: %v", resp)
	}
	for _, bad := range []string{"garbage", emailID(99)} {
		e := setErrorOf(t, resp, "notUpdated", bad)
		if e["type"] != setErrNotFound {
			t.Errorf("%s: type = %v, want notFound (unknown and foreign are indistinguishable)", bad, e["type"])
		}
	}
	if len(f.flagCalls) != 1 {
		t.Errorf("writer received %d calls, want exactly the good one", len(f.flagCalls))
	}
}

func TestEmailSetIfInState(t *testing.T) {
	f, d := setFixture()

	// Mismatch: the whole method aborts with stateMismatch and no write runs.
	merr := callGetErr(t, d.handleEmailSet, setArgs(
		`"ifInState":"not-the-state","update":{"`+emailID(10)+`":{"keywords/$seen":true}}`))
	if merr.Code != "stateMismatch" {
		t.Errorf("code = %v, want stateMismatch (RFC 8620 §5.3)", merr.Code)
	}
	if len(f.flagCalls) != 0 {
		t.Error("a stateMismatch call reached the writer")
	}

	// Match: proceeds.
	resp := callGet(t, d.handleEmailSet, setArgs(
		`"ifInState":"state-1","update":{"`+emailID(10)+`":{"keywords/$seen":true}}`))
	if _, ok := object(t, resp, "updated")[emailID(10)]; !ok {
		t.Fatalf("update with matching ifInState did not land: %v", resp)
	}
}

func TestEmailSetStateStringsBracketTheCall(t *testing.T) {
	_, d := setFixture()

	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"keywords/$seen":true}}`))

	// oldState is the state BEFORE this call's writes, newState after: a
	// client running Email/changes(sinceState=oldState) sees exactly its own
	// change. The fake writer advances the state on every landed write.
	if resp["oldState"] != "state-1" {
		t.Errorf("oldState = %v, want the pre-write state", resp["oldState"])
	}
	if resp["newState"] != "state-1'" {
		t.Errorf("newState = %v, want the post-write state", resp["newState"])
	}
}

func TestEmailSetCreateIsReservedForW3(t *testing.T) {
	f, d := setFixture()

	resp := callGet(t, d.handleEmailSet, setArgs(
		`"create":{"draft1":{"mailboxIds":{"`+EncodeMailboxID(1)+`":true},"subject":"hi"}}`))

	e := setErrorOf(t, resp, "notCreated", "draft1")
	if e["type"] != setErrServerUnavailable {
		t.Errorf("type = %v, want serverUnavailable naming W3", e["type"])
	}
	if desc, _ := e["description"].(string); desc == "" {
		t.Error("the refusal must say drafts arrive with W3, not be silent")
	}
	if resp["created"] != nil {
		t.Errorf("created = %v, want null", resp["created"])
	}
	if len(f.flagCalls)+len(f.moveCalls) != 0 {
		t.Error("a create reached the writer")
	}
}

func TestEmailSetDestroy(t *testing.T) {
	f, d := setFixture()

	resp := callGet(t, d.handleEmailSet, setArgs(
		`"destroy":["`+emailID(10)+`","garbage","`+emailID(99)+`","`+emailID(10)+`"]`))

	destroyed, _ := resp["destroyed"].([]any)
	if len(destroyed) != 1 || destroyed[0] != emailID(10) {
		t.Errorf("destroyed = %v, want exactly the owned id once (duplicates deduplicated)", destroyed)
	}
	for _, bad := range []string{"garbage", emailID(99)} {
		e := setErrorOf(t, resp, "notDestroyed", bad)
		if e["type"] != setErrNotFound {
			t.Errorf("%s: type = %v, want notFound", bad, e["type"])
		}
	}
	if len(f.destroyCalls) != 1 || f.destroyCalls[0] != 10 {
		t.Errorf("destroyCalls = %v, want [10]", f.destroyCalls)
	}
}

func TestEmailSetUpdatePlusDestroyIsWillDestroy(t *testing.T) {
	f, d := setFixture()

	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"keywords/$seen":true}},"destroy":["`+emailID(10)+`"]`))

	e := setErrorOf(t, resp, "notUpdated", emailID(10))
	if e["type"] != setErrWillDestroy {
		t.Errorf("type = %v, want willDestroy (§5.3: the update is ignored)", e["type"])
	}
	destroyed, _ := resp["destroyed"].([]any)
	if len(destroyed) != 1 {
		t.Errorf("destroyed = %v, want the id destroyed", destroyed)
	}
	if len(f.flagCalls) != 0 {
		t.Error("the ignored update reached the writer")
	}
}

func TestEmailSetWriteConflictSurfacesPerRecord(t *testing.T) {
	f, d := setFixture()
	f.writeErr = ErrWriteConflict

	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"keywords/$seen":true}}`))

	e := setErrorOf(t, resp, "notUpdated", emailID(10))
	if e["type"] != setErrStateMismatch {
		t.Errorf("type = %v, want the per-record stateMismatch (UNCHANGEDSINCE conflict, W1 AC)", e["type"])
	}
	if desc, _ := e["description"].(string); desc == "" {
		t.Error("a conflict must tell the client to re-read and retry")
	}
}

func TestEmailSetRequestTooLarge(t *testing.T) {
	_, d := setFixture()

	destroy := ""
	for i := 0; i < d.Limits.MaxObjectsInSet+1; i++ {
		if i > 0 {
			destroy += ","
		}
		destroy += fmt.Sprintf("%q", EncodeEmailID(int64(1000+i)))
	}
	merr := callGetErr(t, d.handleEmailSet, setArgs(`"destroy":[`+destroy+`]`))
	if merr.Code != "requestTooLarge" {
		t.Errorf("code = %v, want requestTooLarge (maxObjectsInSet, RFC 8620 §5.3)", merr.Code)
	}
}

func TestEmailSetForeignAccountIsRejected(t *testing.T) {
	f, d := setFixture()

	merr := callGetErr(t, d.handleEmailSet,
		`{"accountId":"a999999","destroy":["`+emailID(10)+`"]}`)
	if merr.Code != "accountNotFound" {
		t.Errorf("code = %v, want accountNotFound", merr.Code)
	}
	if len(f.destroyCalls) != 0 {
		t.Error("a foreign-account call reached the writer")
	}
}

func TestEmailSetEmptyResponseBucketsAreNull(t *testing.T) {
	_, d := setFixture()

	// §5.3 types every result map as "...|null": an empty /set answers null
	// buckets, not empty objects.
	resp := callGet(t, d.handleEmailSet, setArgs(`"update":{}`))
	for _, bucket := range []string{"created", "updated", "destroyed", "notCreated", "notUpdated", "notDestroyed"} {
		if resp[bucket] != nil {
			t.Errorf("%s = %v, want null", bucket, resp[bucket])
		}
	}
	if resp["oldState"] == "" || resp["newState"] == "" {
		t.Error("state strings must be present even on an empty call")
	}
}
