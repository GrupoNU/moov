package mail_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
	"github.com/GrupoNU/moov/internal/jmap/mail"
	"github.com/GrupoNU/moov/internal/store"
)

// J3 integration: Email/query and the changes family against a REAL store,
// with needles planted per filter shape.
//
// Same env gate and fixture as the J2 integration tests (integration_test.go):
//
//	MOOV_TEST_DATABASE_URL   the PostgreSQL DSN (migrations are applied)
//
// The point of running these against Postgres rather than the fakes is that
// the translation layer's whole job is to produce SHAPES the store's repertoire
// actually serves — a filter that translates cleanly but hits a query the
// database rejects would pass every unit test and fail in production.

// callQuery dispatches a J3 method through a registry carrying both families,
// exactly as the daemon wires them.
func (f *fixture) callQuery(t *testing.T, method, args string) map[string]any {
	t.Helper()
	inv := f.invokeQuery(t, method, args)
	if inv.Name == "error" {
		t.Fatalf("%s failed: %s", method, inv.Args)
	}
	var out map[string]any
	if err := json.Unmarshal(inv.Args, &out); err != nil {
		t.Fatalf("decoding args: %v", err)
	}
	return out
}

// callQueryError dispatches a method expected to fail, returning the error
// object.
func (f *fixture) callQueryError(t *testing.T, method, args string) map[string]any {
	t.Helper()
	inv := f.invokeQuery(t, method, args)
	if inv.Name != "error" {
		t.Fatalf("%s unexpectedly succeeded: %s", method, inv.Args)
	}
	var out map[string]any
	if err := json.Unmarshal(inv.Args, &out); err != nil {
		t.Fatalf("decoding error args: %v", err)
	}
	return out
}

func (f *fixture) invokeQuery(t *testing.T, method, args string) jmap.Invocation {
	t.Helper()
	registry := jmap.NewRegistry()
	mail.RegisterGetMethods(registry, f.deps)
	mail.RegisterQueryMethods(registry, f.deps)
	if f.deps.Writer != nil {
		// The write family mounts only when a fixture wires a writer (the W1
		// integration suite); the read-only fixtures keep their exact surface.
		mail.RegisterSetMethods(registry, f.deps)
	}

	engine := jmap.NewEngine(registry, jmap.DefaultLimits(),
		[]string{jmap.CapCore, jmap.CapMail}, nil)

	body := fmt.Sprintf(
		`{"using":["urn:ietf:params:jmap:core","urn:ietf:params:jmap:mail"],`+
			`"methodCalls":[[%q,%s,"c1"]]}`, method, args)

	resp, rerr := engine.Process(f.callerCtx(), []byte(body), "session-1")
	if rerr != nil {
		t.Fatalf("request-level error: %v", rerr)
	}
	if len(resp.MethodResponses) != 1 {
		t.Fatalf("got %d method responses", len(resp.MethodResponses))
	}
	return resp.MethodResponses[0]
}

func queryIDs(t *testing.T, resp map[string]any) []string {
	t.Helper()
	raw, ok := resp["ids"].([]any)
	if !ok {
		t.Fatalf("ids is %T, want an array", resp["ids"])
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("id is %T, want a string", v)
		}
		out = append(out, s)
	}
	return out
}

// intField reads a numeric response property, failing rather than panicking
// when it is absent or of the wrong type.
func intField(t *testing.T, resp map[string]any, key string) int {
	t.Helper()
	v, ok := resp[key].(float64)
	if !ok {
		t.Fatalf("%s is %T, want a number", key, resp[key])
	}
	return int(v)
}

// seedNeedle stores a message with controlled searchable content, so a filter
// shape can be verified by whether it finds exactly the planted needle.
func (f *fixture) seedNeedle(t *testing.T, mailbox store.Mailbox, uid int64, subject, from, body string, flags store.Flags, keywords []string, received time.Time) int64 {
	t.Helper()
	raw := []byte(fmt.Sprintf(
		"From: %s\r\nTo: destinatario@example.test\r\nSubject: %s\r\n"+
			"Message-ID: <needle-%d@example.test>\r\nDate: %s\r\n"+
			"Content-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n",
		from, subject, uid, received.Format(time.RFC1123Z), body))

	id := f.seedRaw(t, raw, mailbox, uid, flags, keywords)

	// The parsed Date header drives the date ordering the repertoire sorts by,
	// and INTERNALDATE is what receivedAt reports. Set both explicitly so the
	// ordering assertions below are about the query, not about clock skew
	// during seeding.
	if _, err := f.store.Pool().Exec(f.ctx,
		`UPDATE messages SET date = $2, internal_date = $2 WHERE id = $1`, id, received); err != nil {
		t.Fatalf("setting message date: %v", err)
	}
	return id
}

// ---------------------------------------------------------------------------
// filter shapes against real SQL
// ---------------------------------------------------------------------------

// Every filter shape J3 translates, run against the real repertoire with a
// planted needle and planted decoys. A shape that translates but produces SQL
// the database rejects fails here.
func TestIntegrationEmailQueryFilterShapes(t *testing.T) {
	f := newFixture(t)

	archive, err := f.store.UpsertMailbox(f.ctx, store.Mailbox{
		AccountID: f.account.ID, Name: "Archive", Delimiter: "/",
		Role: store.RoleArchive, Subscribed: true, Selectable: true,
	})
	if err != nil {
		t.Fatalf("UpsertMailbox: %v", err)
	}

	base := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)

	// The needles, each isolating one filter shape.
	needleText := f.seedNeedle(t, f.inbox, 1,
		"Presupuesto trimestral", "alice@example.test", "el zorbatron llego ayer",
		store.FlagSeen, nil, base.Add(72*time.Hour))
	needleFrom := f.seedNeedle(t, f.inbox, 2,
		"Reunion", "quintaesencia@example.test", "nos vemos",
		store.FlagSeen, nil, base.Add(48*time.Hour))
	needleUnread := f.seedNeedle(t, f.inbox, 3,
		"Sin leer", "bob@example.test", "pendiente de lectura",
		0, nil, base.Add(24*time.Hour))
	needleKeyword := f.seedNeedle(t, f.inbox, 4,
		"Etiquetado", "carol@example.test", "mensaje con etiqueta",
		store.FlagSeen, []string{"proyecto-moov"}, base.Add(12*time.Hour))
	needleArchive := f.seedNeedle(t, archive, 5,
		"Archivado", "dave@example.test", "en el archivo",
		store.FlagSeen, nil, base)
	// A sender with a display name, which IS tokenized into words — unlike the
	// bare address (see the "from" cases below).
	needleDisplayName := f.seedNeedle(t, f.inbox, 6,
		"Con nombre", "Zorbatrina Perez <zp@example.test>", "cuerpo cualquiera",
		store.FlagSeen, nil, base.Add(6*time.Hour))

	cases := []struct {
		name   string
		filter string
		want   []int64
	}{{
		name:   "inMailbox is the folder view",
		filter: fmt.Sprintf(`{"inMailbox":%q}`, mail.EncodeMailboxID(f.inbox.ID)),
		want:   []int64{needleText, needleFrom, needleUnread, needleKeyword, needleDisplayName},
	}, {
		name:   "inMailbox isolates the other folder",
		filter: fmt.Sprintf(`{"inMailbox":%q}`, mail.EncodeMailboxID(archive.ID)),
		want:   []int64{needleArchive},
	}, {
		name:   "text finds the needle in the body",
		filter: `{"text":"zorbatron"}`,
		want:   []int64{needleText},
	}, {
		name:   "text finds the needle in the subject",
		filter: `{"text":"Presupuesto"}`,
		want:   []int64{needleText},
	}, {
		// The FULL address matches. A bare local-part does NOT, and that is a
		// property of PostgreSQL's parser rather than of this code:
		// to_tsvector('simple', 'quintaesencia@example.test') emits a single
		// indivisible `email` token, so websearch_to_tsquery('quintaesencia')
		// has nothing to match. Verified directly against PG 17 while writing
		// this test.
		//
		// It is recorded here because it is a real product limitation of the
		// from/to filters — "search by sender" only works on the whole address
		// or on the display name — and the fix is a store-side change (index
		// the address parts separately, or a prefix query), named in the J3
		// report. A test asserting the bare local-part matched would have been
		// asserting a bug.
		name:   "from matches the sender's full address",
		filter: `{"from":"quintaesencia@example.test"}`,
		want:   []int64{needleFrom},
	}, {
		name:   "from matches the display name when there is one",
		filter: `{"from":"Zorbatrina"}`,
		want:   []int64{needleDisplayName},
	}, {
		name:   "subject matches",
		filter: `{"subject":"Etiquetado"}`,
		want:   []int64{needleKeyword},
	}, {
		name: "notKeyword $seen is the unread filter",
		filter: fmt.Sprintf(`{"inMailbox":%q,"notKeyword":"$seen"}`,
			mail.EncodeMailboxID(f.inbox.ID)),
		want: []int64{needleUnread},
	}, {
		name:   "hasKeyword finds the labeled message (A6)",
		filter: `{"text":"etiqueta","hasKeyword":"proyecto-moov"}`,
		want:   []int64{needleKeyword},
	}, {
		name: "after is the inclusive lower date bound",
		filter: fmt.Sprintf(`{"inMailbox":%q,"after":%q}`,
			mail.EncodeMailboxID(f.inbox.ID),
			base.Add(48*time.Hour).Format(time.RFC3339)),
		want: []int64{needleText, needleFrom},
	}, {
		name: "before is the exclusive upper date bound",
		filter: fmt.Sprintf(`{"inMailbox":%q,"before":%q}`,
			mail.EncodeMailboxID(f.inbox.ID),
			base.Add(24*time.Hour).Format(time.RFC3339)),
		want: []int64{needleKeyword, needleDisplayName},
	}, {
		name: "an AND of shapes narrows to the intersection",
		filter: fmt.Sprintf(
			`{"operator":"AND","conditions":[{"inMailbox":%q},{"notKeyword":"$seen"},{"text":"pendiente"}]}`,
			mail.EncodeMailboxID(f.inbox.ID)),
		want: []int64{needleUnread},
	}, {
		name:   "an accented query matches unaccented content",
		filter: `{"text":"reunión"}`,
		want:   []int64{needleFrom},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.callQuery(t, "Email/query", fmt.Sprintf(
				`{"accountId":%q,"filter":%s}`, f.accountID(), tc.filter))

			got := queryIDs(t, resp)
			want := make([]string, 0, len(tc.want))
			for _, id := range tc.want {
				want = append(want, mail.EncodeEmailID(id))
			}
			if len(got) != len(want) {
				t.Fatalf("got %v (%d ids), want %v", got, len(got), want)
			}
			// The default sort is newest first, and the wants are written in
			// that order.
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("ids[%d] = %s, want %s", i, got[i], want[i])
				}
			}
		})
	}
}

// The bounded relevance sort must actually reach SearchByRelevance — which
// runs on the ANALYTIC pool with its own statement_timeout (S3 mitigation
// #102). A sort that silently fell back to the date path would pass a fake
// test and lose the isolation that protects every other user's search.
func TestIntegrationEmailQueryRelevanceSort(t *testing.T) {
	f := newFixture(t)
	base := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)

	// The older message mentions the term twice; the newer one once. Relevance
	// must rank the denser one first even though the date sort would not.
	dense := f.seedNeedle(t, f.inbox, 1,
		"Zorbatron zorbatron", "alice@example.test", "zorbatron zorbatron zorbatron",
		store.FlagSeen, nil, base)
	recent := f.seedNeedle(t, f.inbox, 2,
		"Otro asunto", "bob@example.test", "una sola mencion de zorbatron",
		store.FlagSeen, nil, base.Add(48*time.Hour))

	byDate := f.callQuery(t, "Email/query", fmt.Sprintf(
		`{"accountId":%q,"filter":{"text":"zorbatron"}}`, f.accountID()))
	if ids := queryIDs(t, byDate); len(ids) != 2 || ids[0] != mail.EncodeEmailID(recent) {
		t.Fatalf("date sort = %v, want the recent message first", ids)
	}

	byRank := f.callQuery(t, "Email/query", fmt.Sprintf(
		`{"accountId":%q,"filter":{"text":"zorbatron"},"sort":[{"property":"relevance","isAscending":false}]}`,
		f.accountID()))
	ids := queryIDs(t, byRank)
	if len(ids) != 2 {
		t.Fatalf("relevance sort returned %v, want both messages", ids)
	}
	if ids[0] != mail.EncodeEmailID(dense) {
		t.Errorf("relevance sort put %s first, want the denser match %s",
			ids[0], mail.EncodeEmailID(dense))
	}
}

// receivedAt ascending must reverse the real result set.
func TestIntegrationEmailQueryAscendingSort(t *testing.T) {
	f := newFixture(t)
	base := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)

	oldest := f.seedNeedle(t, f.inbox, 1, "Uno", "a@example.test", "cuerpo", store.FlagSeen, nil, base)
	middle := f.seedNeedle(t, f.inbox, 2, "Dos", "b@example.test", "cuerpo", store.FlagSeen, nil, base.Add(time.Hour))
	newest := f.seedNeedle(t, f.inbox, 3, "Tres", "c@example.test", "cuerpo", store.FlagSeen, nil, base.Add(2*time.Hour))

	resp := f.callQuery(t, "Email/query", fmt.Sprintf(
		`{"accountId":%q,"filter":{"inMailbox":%q},"sort":[{"property":"receivedAt","isAscending":true}]}`,
		f.accountID(), mail.EncodeMailboxID(f.inbox.ID)))

	got := queryIDs(t, resp)
	want := []string{
		mail.EncodeEmailID(oldest), mail.EncodeEmailID(middle), mail.EncodeEmailID(newest),
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ids[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

// A tombstoned message must vanish from query results: the repertoire filters
// deleted_at, and this proves the JMAP layer inherits that rather than
// re-exposing destroyed mail.
func TestIntegrationEmailQueryExcludesTombstones(t *testing.T) {
	f := newFixture(t)
	base := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)

	kept := f.seedNeedle(t, f.inbox, 1, "Vive", "a@example.test", "zorbatron", store.FlagSeen, nil, base)
	f.seedNeedle(t, f.inbox, 2, "Muere", "b@example.test", "zorbatron", store.FlagSeen, nil, base.Add(time.Hour))

	if err := f.store.MarkDeleted(f.ctx, f.inbox.ID, 1, []int64{2}); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}

	resp := f.callQuery(t, "Email/query", fmt.Sprintf(
		`{"accountId":%q,"filter":{"text":"zorbatron"}}`, f.accountID()))

	got := queryIDs(t, resp)
	if len(got) != 1 || got[0] != mail.EncodeEmailID(kept) {
		t.Errorf("ids = %v, want only the surviving message %s", got, mail.EncodeEmailID(kept))
	}
}

// Paging over a real corpus must never skip or duplicate — the same property
// the unit test asserts over fakes, verified here against real SQL ordering
// (including the id tiebreak on equal dates).
func TestIntegrationEmailQueryPagingIsConsistent(t *testing.T) {
	f := newFixture(t)
	base := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)

	const corpus = 12
	for i := range corpus {
		// Deliberately give two messages the SAME timestamp, so the tiebreak
		// is exercised: without a total order, a page walk can repeat a row.
		ts := base.Add(time.Duration(i/2) * time.Hour)
		f.seedNeedle(t, f.inbox, int64(i+1),
			fmt.Sprintf("Asunto %d", i), "a@example.test", "zorbatron",
			store.FlagSeen, nil, ts)
	}

	full := queryIDs(t, f.callQuery(t, "Email/query", fmt.Sprintf(
		`{"accountId":%q,"filter":{"text":"zorbatron"}}`, f.accountID())))
	if len(full) != corpus {
		t.Fatalf("the unpaged query returned %d ids, want %d", len(full), corpus)
	}

	for _, pageSize := range []int{1, 5, 7} {
		t.Run(fmt.Sprintf("page=%d", pageSize), func(t *testing.T) {
			var walked []string
			for position := 0; position < corpus; position += pageSize {
				page := queryIDs(t, f.callQuery(t, "Email/query", fmt.Sprintf(
					`{"accountId":%q,"filter":{"text":"zorbatron"},"position":%d,"limit":%d}`,
					f.accountID(), position, pageSize)))
				walked = append(walked, page...)
			}
			if len(walked) != len(full) {
				t.Fatalf("walked %d ids, want %d", len(walked), len(full))
			}
			for i := range full {
				if walked[i] != full[i] {
					t.Errorf("walk[%d] = %s, want %s", i, walked[i], full[i])
				}
			}
		})
	}
}

// An anchor must resolve against the real result set, and page identically to
// the position it stands for.
func TestIntegrationEmailQueryAnchor(t *testing.T) {
	f := newFixture(t)
	base := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)

	for i := range 6 {
		f.seedNeedle(t, f.inbox, int64(i+1),
			fmt.Sprintf("Asunto %d", i), "a@example.test", "zorbatron",
			store.FlagSeen, nil, base.Add(time.Duration(i)*time.Hour))
	}

	full := queryIDs(t, f.callQuery(t, "Email/query", fmt.Sprintf(
		`{"accountId":%q,"filter":{"text":"zorbatron"}}`, f.accountID())))

	resp := f.callQuery(t, "Email/query", fmt.Sprintf(
		`{"accountId":%q,"filter":{"text":"zorbatron"},"anchor":%q,"limit":2}`,
		f.accountID(), full[2]))

	got := queryIDs(t, resp)
	if len(got) != 2 || got[0] != full[2] || got[1] != full[3] {
		t.Errorf("anchored page = %v, want %v", got, full[2:4])
	}
	if p := intField(t, resp, "position"); p != 2 {
		t.Errorf("position = %d, want 2", p)
	}

	// An id that exists but is not in THIS query's results is not found.
	errObj := f.callQueryError(t, "Email/query", fmt.Sprintf(
		`{"accountId":%q,"filter":{"text":"inexistente-zzz"},"anchor":%q}`,
		f.accountID(), full[0]))
	if errObj["type"] != "anchorNotFound" {
		t.Errorf("type = %v, want anchorNotFound", errObj["type"])
	}
}

// calculateTotal over a real store: exact when the result set was exhausted.
func TestIntegrationEmailQueryTotal(t *testing.T) {
	f := newFixture(t)
	base := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)

	for i := range 4 {
		f.seedNeedle(t, f.inbox, int64(i+1),
			fmt.Sprintf("Asunto %d", i), "a@example.test", "zorbatron",
			store.FlagSeen, nil, base.Add(time.Duration(i)*time.Hour))
	}

	resp := f.callQuery(t, "Email/query", fmt.Sprintf(
		`{"accountId":%q,"filter":{"text":"zorbatron"},"calculateTotal":true}`, f.accountID()))
	total, present := resp["total"]
	if !present {
		t.Fatal("total is missing although the result set was exhausted")
	}
	if n, ok := total.(float64); !ok || int(n) != 4 {
		t.Errorf("total = %v, want 4", total)
	}

	// And absent when not requested (§5.5 MUST).
	plain := f.callQuery(t, "Email/query", fmt.Sprintf(
		`{"accountId":%q,"filter":{"text":"zorbatron"}}`, f.accountID()))
	if _, present := plain["total"]; present {
		t.Error("total is present although calculateTotal was not requested")
	}
}

// Account scoping over real data: another account's messages must be
// invisible even when they match the filter perfectly.
func TestIntegrationEmailQueryIsAccountScoped(t *testing.T) {
	f := newFixture(t)
	base := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)

	mine := f.seedNeedle(t, f.inbox, 1, "Mio", "a@example.test", "zorbatron", store.FlagSeen, nil, base)

	// A second account with identical content.
	other, err := f.store.CreateAccount(f.ctx, store.Account{
		Email:    fmt.Sprintf("j3-other-%d@example.test", time.Now().UnixNano()),
		IMAPHost: "dovecot.internal", IMAPPort: 143,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	t.Cleanup(func() {
		if err := f.store.DeleteAccount(context.Background(), other.ID); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})
	otherBox, err := f.store.UpsertMailbox(f.ctx, store.Mailbox{
		AccountID: other.ID, Name: "INBOX", Delimiter: "/",
		Role: store.RoleInbox, Subscribed: true, Selectable: true,
	})
	if err != nil {
		t.Fatalf("UpsertMailbox: %v", err)
	}
	// Seeded directly rather than through f.seedRaw, which is bound to the
	// fixture's own account: the whole point here is a SECOND account holding
	// content identical to the first.
	otherRaw := []byte("From: a@example.test\r\nSubject: Ajeno\r\n" +
		"Message-ID: <ajeno@example.test>\r\n\r\nzorbatron\r\n")
	otherHash, otherSize, err := f.blobs.Put(f.ctx, bytes.NewReader(otherRaw))
	if err != nil {
		t.Fatalf("blob.Put: %v", err)
	}
	if _, err := f.store.InsertMessages(f.ctx, []store.NewMessage{{
		Message: store.Message{
			AccountID: other.ID, RawSHA256: otherHash.Bytes(), RawSize: otherSize,
			MessageID: "ajeno@example.test", Subject: "Ajeno",
			FromAddr: "a@example.test", BodyText: "zorbatron",
			Date: base, ParseStatus: store.ParseOK, Parser: "go-message", ParserVersion: 1,
		},
		State: store.MessageState{
			AccountID: other.ID, MailboxID: otherBox.ID,
			UID: 1, UIDValidity: 1, Flags: store.FlagSeen,
		},
	}}); err != nil {
		t.Fatalf("InsertMessages for the other account: %v", err)
	}

	resp := f.callQuery(t, "Email/query", fmt.Sprintf(
		`{"accountId":%q,"filter":{"text":"zorbatron"}}`, f.accountID()))
	got := queryIDs(t, resp)
	if len(got) != 1 || got[0] != mail.EncodeEmailID(mine) {
		t.Errorf("ids = %v, want only this account's message %s", got, mail.EncodeEmailID(mine))
	}
}

// ---------------------------------------------------------------------------
// the changes family over a real store
// ---------------------------------------------------------------------------

// A full changes round trip against real rows: create, update a flag, destroy,
// and the created-then-destroyed case that §5.2 says must vanish.
func TestIntegrationEmailChangesRoundTrip(t *testing.T) {
	f := newFixture(t)
	base := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)

	// Two messages that exist BEFORE the client's cursor.
	existing := f.seedNeedle(t, f.inbox, 1, "Viejo", "a@example.test", "cuerpo", 0, nil, base)
	doomed := f.seedNeedle(t, f.inbox, 2, "Condenado", "b@example.test", "cuerpo", 0, nil, base)

	// The cursor the client holds: the state after those two exist.
	cursor := f.emailState(t)

	// updated_at has microsecond resolution and the cursor comparison is
	// strictly greater, so the writes below must land after it.
	time.Sleep(10 * time.Millisecond)

	// Now: one flag update, one tombstone, one brand-new message, and one that
	// is created AND destroyed within the window.
	if err := f.store.UpdateFlags(f.ctx, []store.FlagUpdate{
		{MessageID: existing, Flags: store.FlagSeen, ModSeqSeen: 10},
	}); err != nil {
		t.Fatalf("UpdateFlags: %v", err)
	}
	if err := f.store.MarkDeleted(f.ctx, f.inbox.ID, 1, []int64{2}); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}
	created := f.seedNeedle(t, f.inbox, 3, "Nuevo", "c@example.test", "cuerpo", 0, nil, base.Add(time.Hour))
	ephemeral := f.seedNeedle(t, f.inbox, 4, "Efimero", "d@example.test", "cuerpo", 0, nil, base.Add(2*time.Hour))
	if err := f.store.MarkDeleted(f.ctx, f.inbox.ID, 1, []int64{4}); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}

	resp := f.callQuery(t, "Email/changes", fmt.Sprintf(
		`{"accountId":%q,"sinceState":%q}`, f.accountID(), cursor))

	createdIDs := stringSet(t, resp, "created")
	updatedIDs := stringSet(t, resp, "updated")
	destroyedIDs := stringSet(t, resp, "destroyed")

	if !createdIDs[mail.EncodeEmailID(created)] {
		t.Errorf("created = %v, want it to contain the new message", keys(createdIDs))
	}
	if !updatedIDs[mail.EncodeEmailID(existing)] {
		t.Errorf("updated = %v, want it to contain the reflagged message", keys(updatedIDs))
	}
	if !destroyedIDs[mail.EncodeEmailID(doomed)] {
		t.Errorf("destroyed = %v, want it to contain the tombstoned message", keys(destroyedIDs))
	}
	// §5.2: "If a record has been created AND destroyed since the old state,
	// the server SHOULD remove the id from the response entirely."
	eph := mail.EncodeEmailID(ephemeral)
	if createdIDs[eph] || updatedIDs[eph] || destroyedIDs[eph] {
		t.Errorf("the created-and-destroyed message %s appears in the response; §5.2 says remove it entirely", eph)
	}

	// Applying the returned newState must leave nothing further to report.
	next, ok := resp["newState"].(string)
	if !ok {
		t.Fatalf("newState is %T, want a string", resp["newState"])
	}
	settled := f.callQuery(t, "Email/changes", fmt.Sprintf(
		`{"accountId":%q,"sinceState":%q}`, f.accountID(), next))
	for _, bucket := range []string{"created", "updated", "destroyed"} {
		if got := stringSet(t, settled, bucket); len(got) != 0 {
			t.Errorf("after applying newState, %s = %v, want empty", bucket, keys(got))
		}
	}
}

// The maxChanges split over real rows: every change is seen exactly once
// across the intermediate states.
func TestIntegrationEmailChangesSplitsAcrossRealRows(t *testing.T) {
	f := newFixture(t)
	base := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)

	cursor := f.emailState(t)
	time.Sleep(10 * time.Millisecond)

	const n = 9
	want := map[string]bool{}
	for i := range n {
		id := f.seedNeedle(t, f.inbox, int64(i+1),
			fmt.Sprintf("Asunto %d", i), "a@example.test", "cuerpo",
			0, nil, base.Add(time.Duration(i)*time.Hour))
		want[mail.EncodeEmailID(id)] = true
	}

	seen := map[string]int{}
	state := cursor
	for round := 0; round < n+5; round++ {
		resp := f.callQuery(t, "Email/changes", fmt.Sprintf(
			`{"accountId":%q,"sinceState":%q,"maxChanges":2}`, f.accountID(), state))

		ids := stringSet(t, resp, "created")
		if len(ids) > 2 {
			t.Fatalf("returned %d ids, exceeding maxChanges=2", len(ids))
		}
		for id := range ids {
			seen[id]++
		}
		more, ok := resp["hasMoreChanges"].(bool)
		if !ok {
			t.Fatalf("hasMoreChanges is %T, want a boolean", resp["hasMoreChanges"])
		}
		if !more {
			break
		}
		state, ok = resp["newState"].(string)
		if !ok {
			t.Fatalf("newState is %T, want a string", resp["newState"])
		}
	}

	if len(seen) != n {
		t.Errorf("saw %d distinct ids across the split, want %d", len(seen), n)
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("id %s reported %d times across intermediate states", id, count)
		}
	}
	for id := range want {
		if seen[id] == 0 {
			t.Errorf("id %s was never reported", id)
		}
	}
}

// Mailbox/changes over real rows, including the updatedProperties distinction
// that only a store splitting counts from rows can answer.
func TestIntegrationMailboxChanges(t *testing.T) {
	f := newFixture(t)
	base := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)

	// A first message must exist BEFORE the cursor is taken. From the zero
	// cursor of a brand-new account the mailbox row itself is also "new", so
	// updatedProperties would correctly be null — which is a different case
	// than the one under test here (only counts moved).
	f.seedNeedle(t, f.inbox, 1, "Primero", "z@example.test", "cuerpo", 0, nil, base)

	cursor := f.emailState(t)
	time.Sleep(10 * time.Millisecond)

	f.seedNeedle(t, f.inbox, 2, "Nuevo", "a@example.test", "cuerpo", 0, nil, base.Add(time.Hour))

	resp := f.callQuery(t, "Mailbox/changes", fmt.Sprintf(
		`{"accountId":%q,"sinceState":%q}`, f.accountID(), cursor))

	updated := stringSet(t, resp, "updated")
	if !updated[mail.EncodeMailboxID(f.inbox.ID)] {
		t.Errorf("updated = %v, want the inbox whose counts moved", keys(updated))
	}
	// Only a message landed, so only counts changed — RFC 8621 §2.2's property
	// list is the honest answer, and Moov can give it because counts and rows
	// live in different tables.
	props, ok := resp["updatedProperties"].([]any)
	if !ok {
		t.Fatalf("updatedProperties = %v (%T), want the four count properties",
			resp["updatedProperties"], resp["updatedProperties"])
	}
	if len(props) != 4 {
		t.Errorf("updatedProperties = %v, want the four count properties", props)
	}
}

// From the zero cursor — a client that has never synced — the mailbox ROW is
// itself new, so updatedProperties must be null rather than the count list:
// RFC 8621 §2.2's property list means "ONLY the counts changed", and for a
// client that has never seen the folder, everything changed.
func TestIntegrationMailboxChangesFromTheZeroCursorIsNotCountsOnly(t *testing.T) {
	f := newFixture(t)
	f.seedNeedle(t, f.inbox, 1, "Primero", "a@example.test", "cuerpo",
		0, nil, time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC))

	resp := f.callQuery(t, "Mailbox/changes", fmt.Sprintf(
		`{"accountId":%q,"sinceState":"0-0"}`, f.accountID()))

	if v, present := resp["updatedProperties"]; !present || v != nil {
		t.Errorf("updatedProperties = %v, want null when the mailbox row is itself new to the client", v)
	}
	if got := stringSet(t, resp, "updated"); !got[mail.EncodeMailboxID(f.inbox.ID)] {
		t.Errorf("updated = %v, want the inbox", keys(got))
	}
}

// A cursor this server never issued must be refused rather than read as zero.
func TestIntegrationChangesRefusesAForeignState(t *testing.T) {
	f := newFixture(t)
	errObj := f.callQueryError(t, "Email/changes", fmt.Sprintf(
		`{"accountId":%q,"sinceState":"no-soy-un-cursor"}`, f.accountID()))
	if errObj["type"] != "cannotCalculateChanges" {
		t.Errorf("type = %v, want cannotCalculateChanges", errObj["type"])
	}
}

// Both /queryChanges methods decline conformingly against the real wiring.
func TestIntegrationQueryChangesDeclines(t *testing.T) {
	f := newFixture(t)
	for _, method := range []string{"Email/queryChanges", "Mailbox/queryChanges"} {
		t.Run(method, func(t *testing.T) {
			errObj := f.callQueryError(t, method, fmt.Sprintf(
				`{"accountId":%q,"sinceQueryState":"q0"}`, f.accountID()))
			if errObj["type"] != "cannotCalculateChanges" {
				t.Errorf("type = %v, want cannotCalculateChanges", errObj["type"])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// emailState reads the current Email state string — the cursor a client would
// be holding.
//
// It comes from Email/get rather than from a query response, because the /get
// state is what §5.2 says a client feeds to /changes; the queryState is a
// deliberately different cursor (query.go queryStateFor) and passing it here
// would be the exact confusion that prefix exists to prevent.
func (f *fixture) emailState(t *testing.T) string {
	t.Helper()
	get := f.callQuery(t, "Email/get", fmt.Sprintf(
		`{"accountId":%q,"ids":[]}`, f.accountID()))
	state, ok := get["state"].(string)
	if !ok || state == "" {
		t.Fatalf("Email/get returned no state: %v", get)
	}
	return state
}

func stringSet(t *testing.T, resp map[string]any, key string) map[string]bool {
	t.Helper()
	raw, ok := resp[key].([]any)
	if !ok {
		t.Fatalf("%s is %T, want an array", key, resp[key])
	}
	out := make(map[string]bool, len(raw))
	for _, v := range raw {
		id, ok := v.(string)
		if !ok {
			t.Fatalf("%s contains %T, want a string id", key, v)
		}
		out[id] = true
	}
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
