package mail

import (
	"fmt"
	"strings"
	"testing"
)

// The durable-keyword ceiling (A6 / validation V1) — a W2 acceptance
// criterion, tested at the boundary.
//
// The rule under test: a folder stores at most 26 DISTINCT keywords durably,
// Dovecot enforces nothing and reports nothing, so Moov counts and refuses the
// 27th itself. Every case below is about where exactly that line falls, since
// getting it off by one either blocks a legitimate write or lets a keyword be
// accepted and silently lost weeks later.

// keywordFixture seeds a mailbox already carrying n custom keywords.
func keywordFixture(inUse int) (*fakeReaders, *Deps) {
	f, d := setFixture()
	names := make([]string, 0, inUse)
	for i := range inUse {
		names = append(names, fmt.Sprintf("label%02d", i))
	}
	f.keywordsInUse = map[int64][]string{1: names}
	return f, d
}

func TestKeywordCeilingAcceptsTheTwentySixth(t *testing.T) {
	// 25 in use, one new: exactly at the limit, and it must go through. This
	// is the case an off-by-one refuses, which would be a bug the user
	// experiences as "the last label never works".
	f, d := keywordFixture(25)

	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"keywords/Final":true}}`))

	if _, ok := object(t, resp, "updated")[emailID(10)]; !ok {
		t.Fatalf("the 26th distinct keyword was refused: %v", resp)
	}
	if len(f.flagCalls) != 1 {
		t.Errorf("the write did not reach the writer: %v", f.flagCalls)
	}
}

func TestKeywordCeilingRefusesTheTwentySeventh(t *testing.T) {
	f, d := keywordFixture(26)

	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"keywords/Overflow":true}}`))

	serr := setErrorOf(t, resp, "notUpdated", emailID(10))
	if serr["type"] != setErrInvalidProperties {
		t.Fatalf("type = %v, want invalidProperties", serr["type"])
	}
	if props := fmt.Sprint(serr["properties"]); !strings.Contains(props, "keywords") {
		t.Errorf("the error does not name keywords: %v", serr)
	}

	// The message has to be actionable: it names the ceiling, the format that
	// causes it, and what to do. A user cannot possibly guess any of that.
	desc := fmt.Sprint(serr["description"])
	for _, want := range []string{"26", "dovecot-keywords", "Overflow"} {
		if !strings.Contains(desc, want) {
			t.Errorf("the error omits %q: %s", want, desc)
		}
	}

	// And nothing was written: Dovecot would have ACCEPTED this and lost it
	// later, which is the entire reason for the check.
	if len(f.flagCalls) != 0 {
		t.Error("the refused keyword still reached the writer")
	}
}

func TestKeywordCeilingAllowsReusingAKeywordAlreadyInTheFolder(t *testing.T) {
	// A full folder must still accept its OWN keywords on more messages —
	// "tag 500 messages with an existing label" occupies no new slot.
	f, d := keywordFixture(26)

	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"keywords/label00":true}}`))

	if _, ok := object(t, resp, "updated")[emailID(10)]; !ok {
		t.Fatalf("reusing an existing keyword was refused: %v", resp)
	}
	if len(f.flagCalls) != 1 {
		t.Errorf("the write did not reach the writer: %v", f.flagCalls)
	}
}

func TestKeywordCeilingMatchesCaseInsensitively(t *testing.T) {
	// dovecot-keywords allocates ONE letter per case-folded name, so "Label00"
	// and "label00" are the same slot. Counting them separately would let a
	// client cross the real ceiling while Moov believed it had room.
	f, d := keywordFixture(26)

	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"keywords/LABEL00":true}}`))

	if _, ok := object(t, resp, "updated")[emailID(10)]; !ok {
		t.Fatalf("a case variant of an existing keyword was refused: %v", resp)
	}
	if len(f.flagCalls) != 1 {
		t.Error("the write did not reach the writer")
	}
}

func TestKeywordCeilingDoesNotCountSystemKeywords(t *testing.T) {
	// The four RFC 8621 §4.1.1 system keywords live in the Maildir filename's
	// flag field, not in dovecot-keywords. Marking a message read in a folder
	// at its ceiling must always work — it is the single most common write
	// this server takes.
	f, d := keywordFixture(26)

	for _, kw := range []string{"$seen", "$flagged", "$answered", "$draft"} {
		f.flagCalls = nil
		resp := callGet(t, d.handleEmailSet, setArgs(fmt.Sprintf(
			`"update":{%q:{"keywords/%s":true}}`, emailID(10), strings.TrimPrefix(kw, ""))))
		if _, ok := object(t, resp, "updated")[emailID(10)]; !ok {
			t.Errorf("%s was refused in a full folder: %v", kw, resp)
		}
		if len(f.flagCalls) != 1 {
			t.Errorf("%s did not reach the writer", kw)
		}
	}
}

func TestKeywordCeilingDoesNotReadTheBudgetForASystemOnlyWrite(t *testing.T) {
	// Marking read must not cost a query. A budget error that would fail the
	// call proves whether the read happened at all.
	f, d := setFixture()
	f.budgetErr = errSecret

	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"keywords/$seen":true}}`))

	if _, ok := object(t, resp, "updated")[emailID(10)]; !ok {
		t.Fatalf("a $seen write consulted the keyword budget: %v", resp)
	}
}

func TestKeywordCeilingCountsStandardNonSystemKeywords(t *testing.T) {
	// $forwarded, $mdnsent and NonJunk are ordinary IMAP keywords: they DO
	// take a dovecot-keywords slot, and the error message says so.
	f, d := keywordFixture(26)

	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"keywords/$forwarded":true}}`))

	serr := setErrorOf(t, resp, "notUpdated", emailID(10))
	if serr["type"] != setErrInvalidProperties {
		t.Fatalf("$forwarded did not count against the ceiling: %v", resp)
	}
	if !strings.Contains(fmt.Sprint(serr["description"]), "$forwarded") {
		t.Errorf("the error does not say which keyword consumes the budget: %v", serr)
	}
	if len(f.flagCalls) != 0 {
		t.Error("the refused keyword reached the writer")
	}
}

func TestKeywordCeilingCountsEveryNewKeywordOfOneWrite(t *testing.T) {
	// Three new names with two slots left must be refused as a whole: applying
	// two of three silently would be exactly the partial write the ceiling
	// exists to prevent.
	f, d := keywordFixture(24)

	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"keywords/a":true,"keywords/b":true,"keywords/c":true}}`))

	serr := setErrorOf(t, resp, "notUpdated", emailID(10))
	if serr["type"] != setErrInvalidProperties {
		t.Fatalf("three new keywords into two slots were accepted: %v", resp)
	}
	desc := fmt.Sprint(serr["description"])
	if !strings.Contains(desc, "2 slot") || !strings.Contains(desc, "3 new") {
		t.Errorf("the error does not state the arithmetic honestly: %s", desc)
	}
	if len(f.flagCalls) != 0 {
		t.Error("a partially-applicable write reached the writer")
	}
}

func TestKeywordCeilingAcceptsExactlyAsManyNewKeywordsAsSlotsRemain(t *testing.T) {
	f, d := keywordFixture(24)

	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"keywords/a":true,"keywords/b":true}}`))

	if _, ok := object(t, resp, "updated")[emailID(10)]; !ok {
		t.Fatalf("two new keywords into two free slots were refused: %v", resp)
	}
	if len(f.flagCalls) != 1 {
		t.Error("the write did not reach the writer")
	}
}

func TestKeywordCeilingAppliesToTheFullSetForm(t *testing.T) {
	// A whole-property "keywords" write is a replace, but within one call it
	// can only GROW the folder's distinct set — whatever it removes from this
	// message may still be on others.
	f, d := keywordFixture(26)

	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"keywords":{"$seen":true,"BrandNew":true}}}`))

	serr := setErrorOf(t, resp, "notUpdated", emailID(10))
	if serr["type"] != setErrInvalidProperties {
		t.Fatalf("a full-set replace bypassed the ceiling: %v", resp)
	}
	if len(f.flagCalls) != 0 {
		t.Error("the refused replace reached the writer")
	}
}

func TestKeywordCeilingIgnoresRemovals(t *testing.T) {
	// Removing a keyword never needs a slot, so a full folder must accept it.
	f, d := keywordFixture(26)

	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"keywords/label00":null}}`))

	if _, ok := object(t, resp, "updated")[emailID(10)]; !ok {
		t.Fatalf("removing a keyword from a full folder was refused: %v", resp)
	}
	if len(f.flagCalls) != 1 {
		t.Error("the removal did not reach the writer")
	}
}

func TestKeywordBudgetHelpers(t *testing.T) {
	b := KeywordBudget{InUse: []string{"work", "personal"}, Limit: maxDurableKeywords}
	if got := b.Remaining(); got != 24 {
		t.Errorf("Remaining = %d, want 24", got)
	}
	if !b.Has("Work") || !b.Has("work") || !b.Has("  work  ") {
		t.Error("Has must match case-insensitively and ignore surrounding space")
	}
	if b.Has("other") {
		t.Error("Has matched a keyword that is not in use")
	}

	// An over-full folder (another client crossed the line before Moov did)
	// reports zero remaining, never a negative.
	over := KeywordBudget{InUse: make([]string, 30), Limit: maxDurableKeywords}
	if got := over.Remaining(); got != 0 {
		t.Errorf("Remaining on an over-full folder = %d, want 0", got)
	}
}

func TestKeywordCeilingSkippedWhenTheWriterReportsNoLimit(t *testing.T) {
	// A backend without the Maildir constraint (a future store, a test double)
	// must not have every keyword write blocked by a zero limit.
	f, d := keywordFixture(26)
	f.keywordLimit = -1

	resp := callGet(t, d.handleEmailSet, setArgs(
		`"update":{"`+emailID(10)+`":{"keywords/Anything":true}}`))

	if _, ok := object(t, resp, "updated")[emailID(10)]; !ok {
		t.Fatalf("a writer reporting no limit still blocked the write: %v", resp)
	}
}

func TestIsSystemFlagNameCoversExactlyTheMaildirFlagField(t *testing.T) {
	// These five live in the Maildir filename's flag field, so they take no
	// dovecot-keywords letter.
	for _, name := range []string{"seen", "answered", "flagged", "draft", "deleted", "SEEN"} {
		if !isSystemFlagName(name) {
			t.Errorf("%q must not count against the keyword budget", name)
		}
	}
	// Everything else does, including the standard $-keywords that are not
	// system flags.
	for _, name := range []string{"$forwarded", "$mdnsent", "NonJunk", "work", "recent"} {
		if isSystemFlagName(name) {
			t.Errorf("%q occupies a dovecot-keywords slot and must count", name)
		}
	}
}
