package mail_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/GrupoNU/moov/internal/jmap/mail"
	"github.com/GrupoNU/moov/internal/store"
)

// RFC conformance for L3 epic E3, cited clause by clause, driven through the
// real dispatch engine against a real PostgreSQL store — the same discipline as
// conformance_test.go, which explains at length why the official jmapio suite
// cannot be used here.
//
// What this file adds over query_e3_test.go: those tests run against fakes and
// prove a condition SURVIVED TRANSLATION. These run against the database and
// prove the condition SELECTS THE RIGHT MESSAGES. The two failure modes are
// different and neither test catches the other's.
//
// Requires MOOV_TEST_DATABASE_URL; skips cleanly without it.

// newE3Fixture seeds an account whose messages differ along every E3 axis, plus
// a Spam and a Trash folder so the default-exclusion clauses are testable.
//
// Every message shares the term "presupuesto", so a text search matches them
// all and each condition's job is to NARROW that set — which is what makes an
// assertion on the returned count meaningful rather than accidental.
func newE3Fixture(t *testing.T) (*fixture, map[string]string) {
	t.Helper()
	f := newFixture(t)

	junk := seedRoleMailbox(t, f, "Spam", store.RoleJunk)
	trash := seedRoleMailbox(t, f, "Trash", store.RoleTrash)

	ids := map[string]string{}
	seed := func(key, subject, body, cc, bcc string, size int, box store.Mailbox, uid int64, flags store.Flags) {
		t.Helper()
		raw := fmt.Appendf(nil,
			"From: remitente@example.test\r\n"+
				"To: destinatario@example.test\r\n"+
				"Cc: %s\r\n"+
				"Bcc: %s\r\n"+
				"Subject: %s\r\n"+
				"Message-ID: <e3-%s@example.test>\r\n"+
				"Date: Mon, 10 Aug 2026 12:00:00 +0000\r\n"+
				"Content-Type: text/plain; charset=utf-8\r\n"+
				"\r\n%s\r\n%s\r\n",
			cc, bcc, subject, key, body, strings.Repeat("x", size))
		ids[key] = mail.EncodeEmailID(f.seedRaw(t, raw, box, uid, flags, nil))
	}

	// plain: the baseline — matches the text, carries nothing else.
	seed("plain", "Presupuesto simple", "el presupuesto del mes", "", "", 0, f.inbox, 1, 0)
	// cc: the only message with a Cc.
	seed("cc", "Presupuesto con copia", "el presupuesto copiado",
		"copiado@example.test", "", 0, f.inbox, 2, 0)
	// bcc: the only message with a Bcc — and its address must NOT be findable
	// by full-text search, which is what made bcc an index-or-refusal call.
	seed("bcc", "Presupuesto con copia oculta", "el presupuesto oculto",
		"", "escondido@example.test", 0, f.inbox, 3, 0)
	// big: padded so a size bound separates it from the rest.
	seed("big", "Presupuesto grande", "el presupuesto largo", "", "", 20000, f.inbox, 4, 0)
	// flagged: the $flagged bit, which is Gmail's `is:starred`.
	seed("flagged", "Presupuesto destacado", "el presupuesto marcado", "", "", 0,
		f.inbox, 5, store.FlagFlagged)
	// spam and trashed: the two the default exclusion must remove.
	seed("spam", "Presupuesto sospechoso", "el presupuesto basura", "", "", 0, junk, 6, 0)
	seed("trashed", "Presupuesto borrado", "el presupuesto tirado", "", "", 0, trash, 7, 0)

	return f, ids
}

// seedRoleMailbox creates a role mailbox on the fixture's account.
//
// The ROLE is what matters: the Gmail default exclusion resolves the junk and
// trash mailboxes by role (adapter_query_e3.go resolveExclusions), so a folder
// merely NAMED "Spam" would not be excluded and the policy tests would pass for
// the wrong reason.
func seedRoleMailbox(t *testing.T, f *fixture, name string, role store.MailboxRole) store.Mailbox {
	t.Helper()
	mb, err := f.store.UpsertMailbox(f.ctx, store.Mailbox{
		AccountID: f.account.ID, Name: name, Delimiter: "/",
		Role: role, Subscribed: true, Selectable: true,
	})
	if err != nil {
		t.Fatalf("seeding the %s mailbox: %v", role, err)
	}
	return mb
}

// mailboxIDByRole resolves a role mailbox's store id.
func mailboxIDByRole(t *testing.T, f *fixture, role store.MailboxRole) int64 {
	t.Helper()
	mb, err := f.store.GetMailboxByRole(f.ctx, f.account.ID, role)
	if err != nil {
		t.Fatalf("resolving the %s mailbox: %v", role, err)
	}
	return mb.ID
}

// e3Query runs an Email/query and returns the ids, failing on a refusal.
func e3Query(t *testing.T, f *fixture, filter string) []string {
	t.Helper()
	return conformanceIDs(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":%s,"limit":50}`, f.accountID(), filter))
}

// hasKey reports whether the id of the named seeded message is in the result.
func hasKey(ids []string, seeded map[string]string, key string) bool {
	for _, id := range ids {
		if id == seeded[key] {
			return true
		}
	}
	return false
}

// RFC 8621 §4.4.1: "hasAttachment: Boolean — If true, filters on Emails where
// the attachments property is not empty; if false, filters on Emails where it
// is empty."
func TestConformanceFilterHasAttachment(t *testing.T) {
	f, ids := newE3Fixture(t)

	// Every seeded message is text/plain with no attachment, so hasAttachment
	// false returns them and true returns none. The assertion is that the two
	// DIFFER — a silently dropped condition would make them identical, which is
	// the failure this whole layer refuses filters to avoid.
	none := e3Query(t, f, `{"text":"presupuesto","hasAttachment":true}`)
	all := e3Query(t, f, `{"text":"presupuesto","hasAttachment":false}`)

	if len(none) != 0 {
		t.Errorf("hasAttachment:true returned %d ids; no seeded message has an attachment", len(none))
	}
	if len(all) == 0 {
		t.Error("hasAttachment:false returned nothing; every seeded message is attachment-free")
	}
	_ = ids
}

// §4.4.1: "cc: String — Looks for the text in the Cc header field of the
// message."
//
// The exactness is the assertion: this server answers `cc` from the Cc column
// rather than from the whole-message tsvector, which is a DELIBERATE departure
// from the posture it takes for from/to/subject.
func TestConformanceFilterCcMatchesTheCcHeaderOnly(t *testing.T) {
	f, ids := newE3Fixture(t)

	got := e3Query(t, f, `{"text":"presupuesto","cc":"copiado@example.test"}`)
	if len(got) != 1 || !hasKey(got, ids, "cc") {
		t.Fatalf("cc filter returned %v, want only the message with that Cc", got)
	}
}

// §4.4.1: "bcc: String — Looks for the text in the Bcc header field."
//
// Bcc is the condition with no over-match alternative: migration 0002 keeps it
// out of the tsvector, so it was an index or a refusal. Both halves of that fact
// are asserted here — the filter finds it, and a text search does not.
func TestConformanceFilterBccReadsAHeaderTheTextSearchCannot(t *testing.T) {
	f, ids := newE3Fixture(t)

	got := e3Query(t, f, `{"text":"presupuesto","bcc":"escondido@example.test"}`)
	if len(got) != 1 || !hasKey(got, ids, "bcc") {
		t.Fatalf("bcc filter returned %v, want only the message with that Bcc", got)
	}

	viaText := e3Query(t, f, `{"text":"escondido"}`)
	if len(viaText) != 0 {
		t.Errorf("a Bcc address is reachable by full-text search (%v).\n"+
			"Migration 0002 keeps bcc OUT of the tsvector, and migration 0008's whole "+
			"justification rests on that", viaText)
	}
}

// §4.4.1: "minSize: UnsignedInt — The size of the Email in octets is greater
// than or equal to this number"; "maxSize: ... is less than this number."
func TestConformanceFilterSizeBounds(t *testing.T) {
	f, ids := newE3Fixture(t)

	big := e3Query(t, f, `{"text":"presupuesto","minSize":10000}`)
	if len(big) != 1 || !hasKey(big, ids, "big") {
		t.Errorf("minSize:10000 returned %v, want only the padded message", big)
	}

	small := e3Query(t, f, `{"text":"presupuesto","maxSize":10000}`)
	if hasKey(small, ids, "big") {
		t.Error("maxSize:10000 returned the 20 kB message")
	}
	if len(small) == 0 {
		t.Error("maxSize:10000 returned nothing; most seeded messages are small")
	}
}

// §4.4.1 hasKeyword over an IMAP SYSTEM flag — Gmail's `is:starred`
// (docs/research/06-gmail-canon.md §2.5).
//
// This was refused before E3 with "an IMAP system flag stored as a bitmask; the
// repertoire has no predicate for it". True when written; E3 added the
// predicate, and the refusal had to go with it.
func TestConformanceFilterHasKeywordSystemFlag(t *testing.T) {
	f, ids := newE3Fixture(t)

	got := e3Query(t, f, `{"text":"presupuesto","hasKeyword":"$flagged"}`)
	if len(got) != 1 || !hasKey(got, ids, "flagged") {
		t.Fatalf("hasKeyword:$flagged returned %v, want only the flagged message", got)
	}

	// §4.4.1 notKeyword: "A keyword that must not be in the Email's keywords
	// property." The complement, over the same bit.
	rest := e3Query(t, f, `{"text":"presupuesto","notKeyword":"$flagged"}`)
	if hasKey(rest, ids, "flagged") {
		t.Error("notKeyword:$flagged returned the flagged message")
	}
	if len(rest) == 0 {
		t.Error("notKeyword:$flagged returned nothing")
	}
}

// §4.4.1: "inMailboxOtherThan: Id[] — A list of Mailbox ids. The Email must be
// in at least one Mailbox not in this list."
func TestConformanceFilterInMailboxOtherThan(t *testing.T) {
	f, ids := newE3Fixture(t)

	junk := mailboxIDByRole(t, f, store.RoleJunk)
	got := e3Query(t, f, fmt.Sprintf(
		`{"text":"presupuesto","inMailboxOtherThan":[%q]}`, mail.EncodeMailboxID(junk)))

	if hasKey(got, ids, "spam") {
		t.Error("inMailboxOtherThan returned a message from the excluded mailbox")
	}
	// The client named ONLY Spam, so Trash must still be there — the server's
	// own default exclusion is suppressed by an explicit one, and a merge of
	// the two would give the client a scope it did not ask for.
	if !hasKey(got, ids, "trashed") {
		t.Error("an explicit inMailboxOtherThan naming only Spam also excluded Trash; " +
			"the client's exclusion must REPLACE the server default, not add to it")
	}
}

// THE GMAIL DEFAULT EXCLUSION — a deliberate, cited deviation from a naive
// reading of RFC 8621, and the one behavior in E3 that a conformance suite must
// pin rather than discover.
//
// docs/research/06-gmail-canon.md §2.5, citing Google's operator reference
// (support.google.com/mail/answer/7190, retrieved 2026-08-30): "Spam/Trash
// excluded by default; `in:anywhere` includes them."
//
// The RFC does not forbid this. §4.4.1 defines which messages MATCH a
// condition; it says nothing about a server's own notion of search scope, and
// §5.5's `filter: null` — the one clause that DOES fix a scope, "all objects in
// the account of this type" — is honored exactly, which is the fourth case
// below.
//
// All four cases are here because together they ARE the policy. Any one of them
// changing means the search stops behaving the way the canon says Gmail
// behaves, and that must be a decision rather than a regression.
func TestConformanceGmailDefaultExclusionOfSpamAndTrash(t *testing.T) {
	f, ids := newE3Fixture(t)

	t.Run("a plain search excludes Spam and Trash", func(t *testing.T) {
		got := e3Query(t, f, `{"text":"presupuesto"}`)
		if hasKey(got, ids, "spam") {
			t.Error("a standard search returned a message from Spam (canon §2.5)")
		}
		if hasKey(got, ids, "trashed") {
			t.Error("a standard search returned a message from Trash (canon §2.5)")
		}
		if !hasKey(got, ids, "plain") {
			t.Error("the exclusion removed ordinary inbox mail")
		}
	})

	t.Run("an explicit inMailbox is in:spam and is served", func(t *testing.T) {
		junk := mailboxIDByRole(t, f, store.RoleJunk)
		got := e3Query(t, f, fmt.Sprintf(
			`{"text":"presupuesto","inMailbox":%q}`, mail.EncodeMailboxID(junk)))
		if !hasKey(got, ids, "spam") {
			t.Error("in:spam returned nothing; an exclusion applied on top of an explicit " +
				"inMailbox would make that operator silently do the opposite of what it says")
		}
	})

	t.Run("an empty inMailboxOtherThan is in:anywhere", func(t *testing.T) {
		got := e3Query(t, f, `{"text":"presupuesto","inMailboxOtherThan":[]}`)
		if !hasKey(got, ids, "spam") || !hasKey(got, ids, "trashed") {
			t.Errorf("in:anywhere did not include Spam and Trash: %v (canon §2.5)", got)
		}
	})

	t.Run("filter:null is the RFC's enumeration, untouched", func(t *testing.T) {
		got := conformanceIDs(t, f, fmt.Sprintf(
			`{"accountId":%q,"filter":null,"limit":50}`, f.accountID()))
		if !hasKey(got, ids, "spam") || !hasKey(got, ids, "trashed") {
			t.Errorf("filter:null did not enumerate the whole account: %v.\n"+
				"RFC 8620 §5.5 says 'all objects in the account of this type', and the "+
				"Gmail exclusion is scoped to SEARCHES precisely so this clause stays exact",
				got)
		}
	})
}

// RFC 8620 §5.5 FilterOperator: "operator: String — This MUST be one of the
// following strings: 'AND' / 'OR' / 'NOT'."
//
// OR is served as a union of bounded searches. The union's contract is that
// every message matching ANY branch appears, exactly once.
func TestConformanceFilterOperatorOr(t *testing.T) {
	f, ids := newE3Fixture(t)

	got := e3Query(t, f, `{"operator":"OR","conditions":[
		{"text":"presupuesto","cc":"copiado@example.test"},
		{"text":"presupuesto","bcc":"escondido@example.test"}]}`)

	if !hasKey(got, ids, "cc") || !hasKey(got, ids, "bcc") {
		t.Errorf("the OR returned %v, want both branches' matches", got)
	}
	if len(got) != 2 {
		t.Errorf("the OR returned %d ids, want exactly the 2 matches", len(got))
	}

	// A message matching BOTH branches appears once: §5.5's result is a list of
	// ids, and a duplicate would break a client's paging and its anchor lookup.
	both := e3Query(t, f, `{"operator":"OR","conditions":[
		{"text":"presupuesto"},
		{"text":"presupuesto"}]}`)
	seen := map[string]bool{}
	for _, id := range both {
		if seen[id] {
			t.Errorf("the OR returned id %q twice", id)
		}
		seen[id] = true
	}
}

// §5.5 NOT: "all of the conditions must be FALSE for the filter to pass."
//
// Refused, on principle rather than effort: the result is the COMPLEMENT of a
// match set, and no index in this store produces a complement — so it would mean
// testing every message in the account, which is the unbounded work L2 §4.3
// forbids. §5.5's unsupportedFilter is the conforming way to say so, and the
// refusal must NAME the cheap alternatives rather than leaving the client with
// nothing.
func TestConformanceFilterOperatorNotIsRefusedConformingly(t *testing.T) {
	f, _ := newE3Fixture(t)

	name, args := queryConformance(t, f, fmt.Sprintf(
		`{"accountId":%q,"filter":{"operator":"NOT","conditions":[{"text":"presupuesto"}]}}`,
		f.accountID()))

	if name != "error" {
		t.Fatalf("NOT was accepted; got %s %v", name, args)
	}
	if args["type"] != "unsupportedFilter" {
		t.Errorf("error type = %v, want unsupportedFilter (§5.5)", args["type"])
	}
	desc, _ := args["description"].(string)
	for _, want := range []string{"notKeyword", "inMailboxOtherThan"} {
		if !strings.Contains(desc, want) {
			t.Errorf("the refusal does not name the served alternative %q: %s", want, desc)
		}
	}
}

// RFC 8621 §5: the SearchSnippet object and its two nullable properties.
//
// §5: "subject: String|null — The subject of the Email with matching search
// terms highlighted"; "preview: String|null — The relevant section of the body
// with matching search terms highlighted."
func TestConformanceSearchSnippet(t *testing.T) {
	f, ids := newE3Fixture(t)

	name, args := dispatchConformance(t, f, "SearchSnippet/get", fmt.Sprintf(
		`{"accountId":%q,"filter":{"text":"presupuesto"},"emailIds":[%q]}`,
		f.accountID(), ids["plain"]))

	if name != "SearchSnippet/get" {
		t.Fatalf("SearchSnippet/get was refused: %v", args)
	}
	list, _ := args["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("list = %d entries, want 1", len(list))
	}
	entry, _ := list[0].(map[string]any)
	if entry["emailId"] != ids["plain"] {
		t.Errorf("emailId = %v, want the requested id", entry["emailId"])
	}
	subject, _ := entry["subject"].(string)
	if !strings.Contains(subject, "<mark>") {
		t.Errorf("subject = %q, want the matching term highlighted", subject)
	}

	// THE ESCAPING CONTRACT, asserted on the wire: the only markup a snippet may
	// carry is <mark>. ts_headline does not escape its input (verified in
	// internal/store/snippets_test.go), so this is what stands between a
	// message's own HTML and the client's DOM.
	for _, field := range []string{"subject", "preview"} {
		v, ok := entry[field].(string)
		if !ok {
			continue // null is legal per §5
		}
		stripped := strings.ReplaceAll(strings.ReplaceAll(v, "<mark>", ""), "</mark>", "")
		if strings.ContainsAny(stripped, "<>") {
			t.Errorf("%s = %q carries markup other than <mark>", field, v)
		}
	}
}

// §5.1: "If the filter does not include a TEXT-based condition ... the server
// SHOULD return null for both properties."
func TestConformanceSearchSnippetNullWithoutText(t *testing.T) {
	f, ids := newE3Fixture(t)

	name, args := dispatchConformance(t, f, "SearchSnippet/get", fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":%q},"emailIds":[%q]}`,
		f.accountID(), mail.EncodeMailboxID(f.inbox.ID), ids["plain"]))

	if name != "SearchSnippet/get" {
		t.Fatalf("SearchSnippet/get was refused: %v", args)
	}
	list, _ := args["list"].([]any)
	entry, _ := list[0].(map[string]any)
	if entry["subject"] != nil || entry["preview"] != nil {
		t.Errorf("subject = %v, preview = %v; §5.1 says both SHOULD be null "+
			"when the filter has no text condition", entry["subject"], entry["preview"])
	}
}

// §5.1: "notFound: Id[]|null — A list of Email ids requested that could not be
// found."
func TestConformanceSearchSnippetNotFound(t *testing.T) {
	f, _ := newE3Fixture(t)

	_, args := dispatchConformance(t, f, "SearchSnippet/get", fmt.Sprintf(
		`{"accountId":%q,"filter":{"text":"presupuesto"},"emailIds":["no-such-id"]}`,
		f.accountID()))

	notFound, _ := args["notFound"].([]any)
	if len(notFound) != 1 || notFound[0] != "no-such-id" {
		t.Errorf("notFound = %v, want the unknown id", args["notFound"])
	}
}
