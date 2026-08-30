package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/GrupoNU/moov/internal/store"
)

// The account_prefs table (migration 0007) against a real PostgreSQL 17, plus
// the pure schema-version chain.
//
// What is proven here rather than at the JMAP layer: an account with no row
// reads as defaults, a write round-trips through jsonb, the state cursor's two
// inputs move at the right moments, the version chain refuses a document from
// the future, and the database's own CHECK constraints are the database's and
// not a convention.

func TestPrefsDefaultsWithoutARow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)

	rec, err := s.GetPrefs(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetPrefs on a fresh account: %v", err)
	}
	// The no-backfill decision (migration 0007) rests on exactly this: an
	// account that has never saved a preference must behave correctly with no
	// row at all, or every pre-existing mailbox would need one written.
	if rec.Exists {
		t.Error("a fresh account must have no preferences row")
	}
	if !rec.Prefs.Equal(store.DefaultPrefs()) {
		t.Errorf("prefs = %+v, want the product defaults %+v", rec.Prefs, store.DefaultPrefs())
	}
	if !rec.UpdatedAt.IsZero() {
		t.Errorf("updatedAt = %v, want the zero time for an account with no row", rec.UpdatedAt)
	}
}

func TestPrefsRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)

	// Every field set to something OTHER than its default, so a field the
	// encoder or decoder drops cannot hide behind agreeing with the default.
	want := store.Prefs{
		UndoSendSeconds:   30,
		ImagesPolicy:      "ask",
		ConversationView:  false,
		HoverActions:      false,
		AutoAdvance:       "older",
		Density:           "compact",
		ShowSnippets:      false,
		KeyboardShortcuts: false,
		Language:          "es-AR",
		ReadingPane:       "bottom",
		InboxType:         "unread_first",
		Notifications:     "off",
		Theme:             "dark",

		// v2. The maps carry entries too: a map that round-trips only when
		// empty is a map whose encoding was never exercised.
		Labels: map[string]store.LabelPrefs{
			"Facturas": {Color: "amber", Visibility: "showIfUnread"},
			"Equipo":   {Color: "blue", Visibility: "hide"},
		},
		OfflineDepth:         store.OfflineDepthPrefs{HeadersPerMailbox: 500, Bodies: 40},
		AddressAutocomplete:  "manual",
		SendAndArchive:       false,
		DefaultReplyBehavior: "replyAll",
		Signatures: store.SignaturePrefs{
			Items: map[string]store.SignatureItem{
				"work": {Name: "Work", TextBody: "-- \nDiego", HTMLBody: "<p>Diego</p>"},
			},
			ForNew:   "work",
			ForReply: "work",
		},
	}
	if want.Equal(store.DefaultPrefs()) {
		t.Fatal("the fixture must differ from every default, or it proves nothing")
	}

	put, err := s.PutPrefs(ctx, acct.ID, want)
	if err != nil {
		t.Fatalf("PutPrefs: %v", err)
	}
	if !put.Prefs.Equal(want) {
		t.Errorf("PutPrefs returned %+v, want %+v", put.Prefs, want)
	}
	if !put.Exists {
		t.Error("a stored record must report Exists")
	}
	if put.SchemaVersion != store.PrefsSchemaVersion {
		t.Errorf("schema version = %d, want %d", put.SchemaVersion, store.PrefsSchemaVersion)
	}

	got, err := s.GetPrefs(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetPrefs: %v", err)
	}
	if !got.Prefs.Equal(want) {
		t.Errorf("GetPrefs returned %+v, want %+v", got.Prefs, want)
	}
}

// TestPrefsAreScopedToTheirAccount pins the tenancy property every store
// method owes: one account's write is invisible to another's read.
func TestPrefsAreScopedToTheirAccount(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := newAccount(t, s)
	b := newAccount(t, s)

	custom := store.DefaultPrefs()
	custom.Theme = "dark"
	if _, err := s.PutPrefs(ctx, a.ID, custom); err != nil {
		t.Fatalf("PutPrefs on account A: %v", err)
	}

	got, err := s.GetPrefs(ctx, b.ID)
	if err != nil {
		t.Fatalf("GetPrefs on account B: %v", err)
	}
	if got.Exists {
		t.Fatal("account B must not see account A's preferences row")
	}
	if got.Prefs.Theme != store.DefaultPrefs().Theme {
		t.Errorf("account B theme = %q, want the default", got.Prefs.Theme)
	}
}

// TestPrefsUpsertReplacesRatherThanDuplicating pins that the second write is
// an UPDATE: the primary key makes a duplicate impossible, and the created_at
// of the first write survives.
func TestPrefsUpsertReplacesRatherThanDuplicating(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)

	first, err := s.PutPrefs(ctx, acct.ID, store.DefaultPrefs())
	if err != nil {
		t.Fatalf("first PutPrefs: %v", err)
	}

	second := store.DefaultPrefs()
	second.Density = "compact"
	got, err := s.PutPrefs(ctx, acct.ID, second)
	if err != nil {
		t.Fatalf("second PutPrefs: %v", err)
	}
	if !got.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("createdAt moved on update: %v -> %v", first.CreatedAt, got.CreatedAt)
	}
	if got.Prefs.Density != "compact" {
		t.Errorf("density = %q, want the updated value", got.Prefs.Density)
	}

	n, err := s.CountPrefs(ctx, acct.ID)
	if err != nil {
		t.Fatalf("CountPrefs: %v", err)
	}
	if n != 1 {
		t.Errorf("row count = %d, want exactly 1 after two writes", n)
	}
}

// TestPrefsStateCursorInputsMove pins the two values the state string is built
// from, including the case the count exists for: the transition from "no
// preferences" to "preferences saved" must be visible.
func TestPrefsStateCursorInputsMove(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)

	mark0, err := s.PrefsWatermark(ctx, acct.ID)
	if err != nil {
		t.Fatalf("PrefsWatermark before any write: %v", err)
	}
	count0, err := s.CountPrefs(ctx, acct.ID)
	if err != nil {
		t.Fatalf("CountPrefs before any write: %v", err)
	}
	if !mark0.IsZero() || count0 != 0 {
		t.Fatalf("before any write: watermark=%v count=%d, want zero/0", mark0, count0)
	}

	if _, err := s.PutPrefs(ctx, acct.ID, store.DefaultPrefs()); err != nil {
		t.Fatalf("PutPrefs: %v", err)
	}

	mark1, err := s.PrefsWatermark(ctx, acct.ID)
	if err != nil {
		t.Fatalf("PrefsWatermark after the first write: %v", err)
	}
	count1, err := s.CountPrefs(ctx, acct.ID)
	if err != nil {
		t.Fatalf("CountPrefs after the first write: %v", err)
	}
	// The count is why the first write is visible at all: a watermark alone
	// would render "never saved" and "saved" in the same shape.
	if mark1.IsZero() || count1 != 1 {
		t.Fatalf("after the first write: watermark=%v count=%d, want non-zero/1", mark1, count1)
	}

	// A second write moves the watermark even when it stores the same values —
	// the deliberate choice documented on PutPrefs: a harmless extra refresh
	// beats a save that silently fails to notify the user's other sessions.
	second, err := s.PutPrefs(ctx, acct.ID, store.DefaultPrefs())
	if err != nil {
		t.Fatalf("second PutPrefs: %v", err)
	}
	if !second.UpdatedAt.After(mark1) {
		t.Errorf("a no-op write left updatedAt at %v (was %v); the state would not advance",
			second.UpdatedAt, mark1)
	}
}

// TestPrefsUnknownVersionIsRefused is the rollback-safety property: an older
// binary that meets a newer document must fail loudly rather than read it
// partially and then overwrite it with a downgraded one.
func TestPrefsUnknownVersionIsRefused(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)

	// Write a future document directly, which is exactly what a NEWER binary
	// would have left behind. Going around PutPrefs is the point: this state
	// is unreachable through the Go API by construction, and it is the state
	// the chain exists to handle.
	future := []byte(`{"v":99,"theme":"dark","somethingNew":{"nested":true}}`)
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO account_prefs (account_id, prefs, schema_version)
		VALUES ($1, $2::jsonb, $3)`, acct.ID, future, 99); err != nil {
		t.Fatalf("seeding a future document: %v", err)
	}

	_, err := s.GetPrefs(ctx, acct.ID)
	if err == nil {
		t.Fatal("GetPrefs read a document from a schema version this build does not know")
	}
	if !errors.Is(err, store.ErrPrefsUnknownVersion) {
		t.Errorf("error = %v, want ErrPrefsUnknownVersion", err)
	}
}

// TestPrefsUnversionedDocumentReadsAsV1 covers the DEFAULT '{}' the column
// carries and any row written before versions existed: both are v1 by
// construction, and refusing them would fail reads on rows the schema itself
// creates.
func TestPrefsUnversionedDocumentReadsAsV1(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)

	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO account_prefs (account_id, prefs, schema_version)
		VALUES ($1, '{"theme":"dark"}'::jsonb, 1)`, acct.ID); err != nil {
		t.Fatalf("seeding an unversioned document: %v", err)
	}

	rec, err := s.GetPrefs(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetPrefs on an unversioned document: %v", err)
	}
	if rec.SchemaVersion != 1 {
		t.Errorf("schema version = %d, want 1 for an unversioned document", rec.SchemaVersion)
	}
	if rec.Prefs.Theme != "dark" {
		t.Errorf("theme = %q, want the stored value", rec.Prefs.Theme)
	}
	// Everything the sparse document did not name must come from the defaults.
	// This is the "defaults on read" mechanism, which is what lets a product
	// decision that MOVES a default reach users who never expressed an opinion.
	if rec.Prefs.UndoSendSeconds != store.DefaultPrefs().UndoSendSeconds {
		t.Errorf("undoSendSeconds = %d, want the default %d for an unset key",
			rec.Prefs.UndoSendSeconds, store.DefaultPrefs().UndoSendSeconds)
	}
	if rec.Prefs.KeyboardShortcuts != store.DefaultPrefs().KeyboardShortcuts {
		t.Error("keyboardShortcuts did not fall back to the default for an unset key")
	}
}

// TestPrefsStoredDocumentCarriesItsVersion pins the wire form in the column:
// PutPrefs must write the "v" key, or a later build's chain has nothing to
// dispatch on.
func TestPrefsStoredDocumentCarriesItsVersion(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)

	if _, err := s.PutPrefs(ctx, acct.ID, store.DefaultPrefs()); err != nil {
		t.Fatalf("PutPrefs: %v", err)
	}

	var raw []byte
	var column int
	if err := s.Pool().QueryRow(ctx,
		`SELECT prefs, schema_version FROM account_prefs WHERE account_id = $1`, acct.ID).
		Scan(&raw, &column); err != nil {
		t.Fatalf("reading the stored document: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the stored document is not an object: %v", err)
	}
	v, ok := doc["v"].(float64)
	if !ok {
		t.Fatalf(`the stored document has no numeric "v" key: %s`, raw)
	}
	if int(v) != store.PrefsSchemaVersion {
		t.Errorf(`document "v" = %v, want %d`, v, store.PrefsSchemaVersion)
	}
	// The column mirrors the key (migration 0007's header). Nothing keeps them
	// in sync but the single writer, so the agreement is pinned here.
	if column != int(v) {
		t.Errorf("schema_version column = %d, document v = %v; the two must agree", column, v)
	}
}

// TestPrefsColumnRejectsANonObject proves the CHECK constraint is the
// database's, not a convention the Go layer happens to honor. A jsonb column
// accepts arrays and scalars, every one of which would make the read path fail
// for a user who did nothing wrong.
func TestPrefsColumnRejectsANonObject(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)

	_, err := s.Pool().Exec(ctx, `
		INSERT INTO account_prefs (account_id, prefs, schema_version)
		VALUES ($1, '[1,2,3]'::jsonb, 1)`, acct.ID)
	if err == nil {
		t.Fatal("the database accepted a JSON array as a preference document")
	}
}

// TestPrefsColumnRejectsANonPositiveVersion is the second CHECK: a version
// this chain could never start from must not be storable.
func TestPrefsColumnRejectsANonPositiveVersion(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	acct := newAccount(t, s)

	_, err := s.Pool().Exec(ctx, `
		INSERT INTO account_prefs (account_id, prefs, schema_version)
		VALUES ($1, '{"v":1}'::jsonb, 0)`, acct.ID)
	if err == nil {
		t.Fatal("the database accepted schema_version 0")
	}
}

// TestPrefsCascadeOnAccountDelete pins the FK: a deleted mailbox takes its
// preferences with it, so a recycled account id can never inherit a stranger's
// settings.
func TestPrefsCascadeOnAccountDelete(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// A local account rather than newAccount's, because this test deletes it
	// and newAccount registers its own cleanup deletion.
	acct := newAccount(t, s)
	if _, err := s.PutPrefs(ctx, acct.ID, store.DefaultPrefs()); err != nil {
		t.Fatalf("PutPrefs: %v", err)
	}
	if err := s.DeleteAccount(ctx, acct.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}

	var n int64
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM account_prefs WHERE account_id = $1`, acct.ID).Scan(&n); err != nil {
		t.Fatalf("counting orphaned preference rows: %v", err)
	}
	if n != 0 {
		t.Errorf("%d preference rows survived their account", n)
	}
}
