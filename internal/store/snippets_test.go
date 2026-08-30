package store_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// The store half of SearchSnippet/get (RFC 8621 §5, L3 epic E3).
//
// The security argument lives in internal/store/snippets.go and its other half
// in internal/jmap/mail/snippet.go. What is proven HERE is the part that
// depends on PostgreSQL actually behaving as documented — because the whole
// escaping design rests on a claim about ts_headline that was measured rather
// than assumed, and a PostgreSQL upgrade could change it.

// The sentinels, restated as literals rather than imported.
//
// The constants are unexported, and this is an external test package — but that
// is not why they are repeated. They are repeated so that a change to the
// constants FAILS these tests instead of silently moving them: the tests assert
// facts about specific byte values (that PostgreSQL does not strip them, that
// they survive marking), and a test that read the value from the code under
// test could not assert anything about the value.
const (
	markStart = "\x02"
	markStop  = "\x03"
)

// seedSnippetMessage stores one message with the given subject and body.
func seedSnippetMessage(t *testing.T, s *store.Store, acct store.Account, mbox store.Mailbox,
	uid int64, subject, body string,
) int64 {
	t.Helper()
	ids, err := s.InsertMessages(context.Background(), []store.NewMessage{{
		Message: store.Message{
			AccountID: acct.ID,
			RawSHA256: seedBlob(t, s, fmt.Sprintf("snip-%d-%d", acct.ID, uid)),
			RawSize:   500,
			MessageID: fmt.Sprintf("snip-%d-%d@test", acct.ID, uid),
			Subject:   subject,
			FromAddr:  "remitente@example.test",
			ToAddrs:   "destinatario@example.test",
			BodyText:  body,
			Preview:   "preview",
			Date:      time.Now().UTC().Add(-time.Duration(uid) * time.Minute),
		},
		State: store.MessageState{
			AccountID: acct.ID, MailboxID: mbox.ID, UID: uid, UIDValidity: 1,
		},
	}})
	if err != nil {
		t.Fatalf("seeding a snippet message: %v", err)
	}
	return ids[0]
}

// The snippet must MARK the matching term — the feature, before the safety.
func TestSnippetsMarkTheMatchingTerm(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	inbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)

	id := seedSnippetMessage(t, s, acct, inbox, 1,
		"Presupuesto de la reunion",
		"Adjunto el presupuesto revisado para la reunion del martes con todo el detalle.")

	rows, err := s.Snippets(ctx, store.SnippetQuery{
		AccountID: acct.ID, MessageIDs: []int64{id}, Text: "presupuesto",
	})
	if err != nil {
		t.Fatalf("Snippets: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if !strings.Contains(rows[0].Subject, markStart+"Presupuesto"+markStop) {
		t.Errorf("subject = %q, want the term marked", rows[0].Subject)
	}
	if !strings.Contains(rows[0].Preview, markStart) {
		t.Errorf("preview = %q, want a marked fragment", rows[0].Preview)
	}
}

// THE SECURITY FACT this whole design rests on: ts_headline does not escape
// anything, so the marking must not use angle brackets.
//
// Verified against the live PostgreSQL rather than asserted from the docs,
// because a version that started escaping would make the sentinel scheme
// unnecessary — and, more importantly, a version that changed how it handles
// the sentinel would break the scheme silently. This test is the tripwire.
func TestTsHeadlineDoesNotEscapeMarkupSoSentinelsAreRequired(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	var out string
	if err := s.Pool().QueryRow(ctx, `
		SELECT ts_headline('simple', $1,
		         websearch_to_tsquery('simple', 'presupuesto'),
		         'StartSel=<mark>,StopSel=</mark>,HighlightAll=true')`,
		`presupuesto <script>alert(1)</script>`).Scan(&out); err != nil {
		t.Fatalf("ts_headline probe: %v", err)
	}

	if !strings.Contains(out, "<script>") {
		t.Skipf("this PostgreSQL escapes ts_headline input (%q).\n"+
			"That would be GOOD news, and it means the sentinel scheme in snippets.go "+
			"could be simplified — but simplify it deliberately, with this test rewritten "+
			"to assert the new behavior, rather than by noticing it stopped failing.", out)
	}
	// The documented, measured behavior: the script tag survives verbatim. This
	// is why StartSel must never be `<mark>`.
	if !strings.Contains(out, "<mark>presupuesto</mark>") {
		t.Errorf("ts_headline did not mark the term at all: %q", out)
	}
}

// Step 1 of the escaping contract: the source's OWN sentinels are stripped
// before ts_headline sees them.
//
// Without this, a message body containing the sentinel byte would emit a
// <mark> the highlighter never placed — letting a sender forge emphasis inside
// a snippet. It is a small attack and it is exactly the seam an escaping scheme
// is supposed to close, and it was verified to be REAL before being closed:
// PostgreSQL passes the source's sentinels straight through.
func TestSnippetsStripSentinelsFromTheSourceText(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	inbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)

	// A message that tries to smuggle its own marks in.
	id := seedSnippetMessage(t, s, acct, inbox, 1,
		"Presupuesto "+markStart+"falso"+markStop+" adjunto",
		"El presupuesto "+markStart+"tambien falso"+markStop+" en el cuerpo del mensaje.")

	rows, err := s.Snippets(ctx, store.SnippetQuery{
		AccountID: acct.ID, MessageIDs: []int64{id}, Text: "presupuesto",
	})
	if err != nil {
		t.Fatalf("Snippets: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}

	// Exactly one marked region per field: the one the highlighter placed
	// around "presupuesto". The message's own two must be gone.
	for _, f := range []struct {
		name string
		val  string
	}{{"subject", rows[0].Subject}, {"preview", rows[0].Preview}} {
		starts := strings.Count(f.val, markStart)
		stops := strings.Count(f.val, markStop)
		if starts != 1 || stops != 1 {
			t.Errorf("%s = %q has %d start and %d stop marks, want exactly one of each — "+
				"the message's own sentinels were not stripped, so a sender can forge "+
				"a highlight", f.name, f.val, starts, stops)
		}
		if !strings.Contains(strings.ToLower(f.val), markStart+"presupuesto"+markStop) {
			t.Errorf("%s = %q: the surviving mark is not the one the highlighter placed",
				f.name, f.val)
		}
	}
}

// Marks come back balanced, so the JMAP layer's mark-to-markup substitution can
// be a plain replacement without producing an unbalanced <mark>.
func TestSnippetsReturnBalancedMarks(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	inbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)

	// A long body, so the fragment machinery actually cuts — which is where an
	// unbalanced mark would come from.
	body := strings.Repeat("relleno de texto para forzar fragmentos varios. ", 60) +
		"presupuesto en el medio del cuerpo. " +
		strings.Repeat("mas relleno para el otro lado del fragmento. ", 60)
	id := seedSnippetMessage(t, s, acct, inbox, 1, "Presupuesto", body)

	rows, err := s.Snippets(ctx, store.SnippetQuery{
		AccountID: acct.ID, MessageIDs: []int64{id}, Text: "presupuesto",
	})
	if err != nil {
		t.Fatalf("Snippets: %v", err)
	}
	for _, f := range []string{rows[0].Subject, rows[0].Preview} {
		if strings.Count(f, markStart) != strings.Count(f, markStop) {
			t.Errorf("%q has unbalanced marks", f)
		}
	}
}

// The account scope is in the WHERE clause, not inherited from the caller's id
// list. A method that trusted its caller's ids would be the one place the
// repertoire's account rule did not hold — and it would be an oracle over
// another account's mail.
func TestSnippetsAreAccountScoped(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	mine := newAccount(t, s)
	myInbox := seedMailbox(t, s, mine.ID, "INBOX", store.RoleInbox)
	theirs := newAccount(t, s)
	theirInbox := seedMailbox(t, s, theirs.ID, "INBOX", store.RoleInbox)

	theirID := seedSnippetMessage(t, s, theirs, theirInbox, 1,
		"Presupuesto reservado", "el presupuesto secreto de la otra cuenta")
	_ = seedSnippetMessage(t, s, mine, myInbox, 1, "Presupuesto propio", "mi presupuesto")

	// Asking for THEIR message id under MY account must return nothing.
	rows, err := s.Snippets(ctx, store.SnippetQuery{
		AccountID: mine.ID, MessageIDs: []int64{theirID}, Text: "presupuesto",
	})
	if err != nil {
		t.Fatalf("Snippets: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("account %d received %d snippet(s) for account %d's message",
			mine.ID, len(rows), theirs.ID)
	}
}

// RFC 8621 §5.1: a filter with no text yields no snippet. The store answers
// with no rows and the handler turns that into §5's nulls.
func TestSnippetsWithoutTextReturnNothing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	inbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)
	id := seedSnippetMessage(t, s, acct, inbox, 1, "Asunto", "cuerpo")

	rows, err := s.Snippets(ctx, store.SnippetQuery{
		AccountID: acct.ID, MessageIDs: []int64{id}, Text: "",
	})
	if err != nil {
		t.Fatalf("Snippets: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %d, want none for a filter with no text", len(rows))
	}
}

// An accented search term must match the unaccented lexemes the tsvector
// stores — the same query-side immutable_unaccent every other shape applies,
// and the same silent-zero-results failure it prevents (search.go documents it
// at length; it hits precisely the Spanish mailboxes this installed base is
// made of).
func TestSnippetsUnaccentTheQueryTerm(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)
	inbox := seedMailbox(t, s, acct.ID, "INBOX", store.RoleInbox)
	id := seedSnippetMessage(t, s, acct, inbox, 1,
		"La reunion de accion", "hablamos de la accion pendiente")

	rows, err := s.Snippets(ctx, store.SnippetQuery{
		AccountID: acct.ID, MessageIDs: []int64{id}, Text: "acción",
	})
	if err != nil {
		t.Fatalf("Snippets: %v", err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Subject, markStart) {
		t.Errorf("an accented term did not mark the unaccented text: %+v", rows)
	}
}
