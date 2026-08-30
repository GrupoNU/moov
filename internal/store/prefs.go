package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Per-account preferences (migration 0007, L3 epic E0): the typed schema, the
// JSON schema-version migration chain, and the two operations the JMAP layer
// needs.
//
// # The division of labor with internal/jmap
//
// This file owns the SHAPE (what keys exist, what type each has, what the
// default is) and the VERSION CHAIN (how a document written by an older build
// becomes a current one). It deliberately does NOT own value validation
// beyond types: rejecting `density: "enormous"` with a per-key JMAP
// invalidProperties detail is the protocol layer's job, because the error has
// to carry a client-facing explanation naming the JMAP property, and this
// package must not compose JMAP errors.
//
// The split is not merely stylistic. It means a document that somehow reaches
// the database with an out-of-domain value still READS — the user sees a
// wrong-but-present setting they can change — rather than making the whole
// preferences object unfetchable. A read path that can fail on data it wrote
// itself is a read path that can lock a user out of their own settings screen.
//
// # Why defaults are applied on read and never stored
//
// PutPrefs stores exactly what the user chose. Prefs (the typed struct) is
// materialized from the stored document by filling every unset key from
// DefaultPrefs. The consequence is the one migration 0007's header states: a
// product decision that MOVES a default — decision D-3 flipped keyboard
// shortcuts ON against Gmail's off-default, and a future arbitration could
// flip another — moves it for every user who never expressed an opinion,
// instead of only for accounts created after the change.
//
// This is why the stored form is a sparse map and the served form is a full
// struct, and why the two are different types rather than one type with
// pointers everywhere.

// PrefsSchemaVersion is the current version of the stored preference
// document — the value written into the document's "v" key and into
// account_prefs.schema_version.
//
// It is bumped when the MEANING of stored data changes in a way a reader must
// repair: a key renamed, a value domain re-encoded, a scalar becoming a
// structure. It is NOT bumped for a new key with a default, because migrating
// such a document is exactly what filling defaults on read already does.
//
// The version is the reason the very first release of preferences ships a
// chain rather than a bare unmarshal: risk 6 of the L3 plan ("drift del
// esquema de preferencias") is real, and the moment to build the chain is
// before there is any stored data to be careful with, not after.
const PrefsSchemaVersion = 1

// prefsVersionKey is the document key holding the schema version.
const prefsVersionKey = "v"

// ErrPrefsUnknownVersion is returned when a stored document declares a schema
// version this build does not know how to read — which happens on exactly one
// real occasion: a rollback to an older binary after a newer one wrote v2.
//
// The refusal is deliberate and total. The alternative — ignoring the version
// and reading what keys happen to be recognizable — would let an old build
// serve a user's preferences with the v2 semantics silently dropped, and then
// WRITE them back as v1, destroying the newer data on the first settings save.
// A read that fails loudly leaves the document intact for the newer binary
// that can read it, which is the recoverable failure of the two.
var ErrPrefsUnknownVersion = errors.New("store: unknown preference schema version")

// ---------------------------------------------------------------------------
// the typed schema (v1)
// ---------------------------------------------------------------------------

// Prefs is one account's preferences, fully materialized: every field carries
// either the user's choice or the product default.
//
// The domains are enforced above this package (internal/jmap/mail's prefs.go),
// where a rejection can name the offending JMAP property. The values below
// document what each domain IS, with the canon citation that fixed it — those
// comments are the specification the validator is written against, so the two
// cannot drift without the drift being visible in one file.
type Prefs struct {
	// UndoSendSeconds is the undo-send window. Gmail offers exactly
	// {5, 10, 20, 30} (canon §2.3, /2819488); the server additionally clamps
	// to [5, 30] so a value that reaches the send path out of band still
	// produces a window the outbox can honor.
	//
	// This is the one preference the SERVER acts on by itself: the browser
	// closes, and internal/submit still has to release the mail at the right
	// second. It is the concrete reason preferences could not stay in
	// localStorage.
	UndoSendSeconds int `json:"undoSendSeconds"`

	// ImagesPolicy is "always" or "ask" (decision D-4, canon §7.1). The
	// default is "always" — Gmail's own default — and it is only defensible
	// because our HMAC image proxy exists: "always" means "always through the
	// proxy", never "let the sender see the recipient's IP". The proxy is the
	// precondition, and it shipped before this default did.
	ImagesPolicy string `json:"imagesPolicy"`

	// ConversationView groups a thread into one row (canon §2.1, /5900).
	ConversationView bool `json:"conversationView"`

	// HoverActions shows the four row-hover actions Gmail defines — archive,
	// delete, snooze, mark read — ON by default with a single disable switch
	// (canon §2.2, /2473038).
	HoverActions bool `json:"hoverActions"`

	// AutoAdvance is where the reader goes after archiving or deleting:
	// "list", "newer" or "older" (canon §2.2, /6562). Gmail's default is the
	// conversation list, which is what "list" means here.
	AutoAdvance string `json:"autoAdvance"`

	// Density is "default", "comfortable" or "compact" (canon §2.4). The three
	// names are Gmail's; the pixel values are ours (§5 of the canon records
	// that Google publishes no fetchable page for them).
	Density string `json:"density"`

	// ShowSnippets renders the message preview beside the subject (canon
	// §2.4).
	ShowSnippets bool `json:"showSnippets"`

	// KeyboardShortcuts enables the keyboard map.
	//
	// DEFAULT TRUE, which is a REGISTERED DIVERGENCE from Gmail, not an
	// oversight: Gmail ships shortcuts off, Superhuman ships them on, and
	// decision D-3 (signed 2026-08-30) chose on for Moov's audience under
	// governing rule 2. The canon's own filter (§1.2) permits divergence only
	// when Gmail's reason is not a security or privacy stance; Google
	// publishes no reason for this default at all, which is precisely the case
	// D-3 arbitrated.
	KeyboardShortcuts bool `json:"keyboardShortcuts"`

	// Language is a BCP 47 tag, or empty for "follow the browser". Gmail's
	// equivalent is an RFC 3066 display language (canon §3, DIRECT).
	//
	// The empty string is the auto case rather than a nil pointer: "" is not a
	// valid BCP 47 tag, so it cannot collide with a real choice, and it keeps
	// the struct free of a pointer whose nil-ness every consumer would have to
	// remember to check. The JMAP layer renders it as the RFC's null.
	Language string `json:"language"`

	// ReadingPane is "none", "right" or "bottom" — Gmail's "No split",
	// "Right of inbox", "Below inbox" (canon §2.4, /9499937).
	ReadingPane string `json:"readingPane"`

	// InboxType is "default", "unread_first" or "starred_first".
	//
	// Gmail defines six (canon §2.4, /186531). These are the three
	// DETERMINISTIC ones. "Important first" and "Priority Inbox" both require
	// the importance classifier, and "Multiple Inboxes" is a per-section query
	// surface; the first two are deferred to the AI phase by the same rule
	// that reduced notifications to two modes (GC-2) — shipping a control
	// whose classifier does not exist would be a setting that does nothing.
	InboxType string `json:"inboxType"`

	// Notifications is "new" or "off" (GC-2). Gmail has three modes; the
	// third, "important mail only", is gated on the same missing classifier as
	// InboxType's deferred values and arrives with the AI phase. The paritary
	// behavior is FOREGROUND notification over the existing SSE stream (canon
	// §2.9): Gmail web notifies only while a session is open, and Web Push is
	// recorded as a beyond-Gmail deferral rather than a gap.
	Notifications string `json:"notifications"`

	// Theme is "light", "dark" or "system".
	//
	// Account-level, like Gmail's. The PWA keeps its localStorage copy — it
	// must, because the theme has to be applied BEFORE first paint, and the
	// session fetch has not happened yet — and reconciles against this value
	// once the session loads. The localStorage entry is therefore a pre-paint
	// cache of this field, not a second source of truth.
	Theme string `json:"theme"`
}

// DefaultPrefs is the product's factory setting for every preference.
//
// Each value is either Gmail's own default (the canon filter's first rule:
// adopt what Gmail chose, because it encodes twenty years of abuse data) or a
// signed divergence, and every divergence is named in the field's comment
// above.
func DefaultPrefs() Prefs {
	return Prefs{
		UndoSendSeconds:   10,       // Gmail's own default within {5,10,20,30}.
		ImagesPolicy:      "always", // D-4; the HMAC proxy is what makes it safe.
		ConversationView:  true,
		HoverActions:      true,   // canon §2.2: "on by default".
		AutoAdvance:       "list", // canon §2.2: Gmail returns to the list.
		Density:           "default",
		ShowSnippets:      true,
		KeyboardShortcuts: true, // D-3: registered divergence from Gmail's off.
		Language:          "",   // follow the browser.
		ReadingPane:       "right",
		InboxType:         "default",
		Notifications:     "new",
		Theme:             "light",
	}
}

// PrefsRecord is one account_prefs row as the JMAP layer reads it: the
// materialized preferences plus the watermark its state cursor is built from.
type PrefsRecord struct {
	AccountID int64

	// Prefs is fully materialized — stored choices with defaults filled in.
	Prefs Prefs

	// SchemaVersion is the version the STORED document carried. It equals
	// PrefsSchemaVersion after any write by this build, and may be lower for a
	// row written before a bump and not yet rewritten.
	SchemaVersion int

	// Exists reports whether the account has a row at all. An account that has
	// never changed a setting has none, and gets DefaultPrefs with a zero
	// watermark — which is the honest "no preferences expressed" state, and
	// what makes the state string move when the first one is.
	Exists bool

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ---------------------------------------------------------------------------
// the schema-version migration chain
// ---------------------------------------------------------------------------

// migratePrefs converts a stored document of ANY version this build knows into
// the current typed form, filling defaults for everything the document does
// not set.
//
// It is a pure function of its argument — no clock, no database, no
// randomness — which is what makes the whole chain testable by table, and what
// lets a future data migration replay it over a batch of rows offline.
//
// The three cases, and why each is what it is:
//
//	absent "v", or v == 0   -> treated as v1. A document with no version is
//	                           either the DEFAULT '{}' the column carries or a
//	                           row from before versions were written; both hold
//	                           v1-shaped keys, since v1 is the first schema
//	                           that ever existed. Refusing them would fail
//	                           reads on rows this very migration creates.
//	v == PrefsSchemaVersion -> decode and fill.
//	anything else           -> ErrPrefsUnknownVersion. That covers a version
//	                           from the FUTURE, which is the real case (see the
//	                           error's own documentation: an old binary must not
//	                           read, downgrade and then overwrite a newer
//	                           document), and a NEGATIVE one, which the column's
//	                           CHECK already makes unstorable and which is
//	                           therefore corruption rather than a version.
//
// Adding v2 means: add a `case 2:` that decodes the v2 shape, and add a step
// that lifts a v1 document to v2 before it. The chain is a switch precisely so
// that the step-by-step lift is written once and reused by every older
// version, rather than each version needing a direct-to-current decoder.
func migratePrefs(raw []byte) (Prefs, int, error) {
	out := DefaultPrefs()
	if len(raw) == 0 {
		return out, PrefsSchemaVersion, nil
	}

	// The version is read on its own first, before any attempt to decode the
	// body. A v2 document may hold a key whose TYPE changed, which would make
	// a v1-shaped unmarshal fail with a confusing type error instead of the
	// accurate "this build cannot read that version".
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return Prefs{}, 0, fmt.Errorf("decoding the stored preferences: %w", err)
	}

	version := 0
	if v, ok := envelope[prefsVersionKey]; ok {
		if err := json.Unmarshal(v, &version); err != nil {
			return Prefs{}, 0, fmt.Errorf("decoding the stored preference schema version: %w", err)
		}
	}

	switch version {
	case 0, 1:
		// v1, and the unversioned documents that are v1 by construction. An
		// unversioned document is reported AS v1, so a caller cannot mistake
		// "no version key" for "version zero".
		//
		// Unmarshaling ONTO the defaults is what fills unset keys: encoding/json
		// leaves a field untouched when the document omits it, so every absent
		// preference keeps the product default and every present one overrides
		// it. That is the whole "defaults on read" mechanism, in one line.
		if err := json.Unmarshal(raw, &out); err != nil {
			return Prefs{}, 0, fmt.Errorf("decoding the stored v1 preferences: %w", err)
		}
		return out, 1, nil

	default:
		return Prefs{}, 0, fmt.Errorf("%w: the stored document declares v%d, this build reads up to v%d",
			ErrPrefsUnknownVersion, version, PrefsSchemaVersion)
	}
}

// encodePrefs renders preferences for storage: the full typed object plus its
// version key.
//
// The document is written DENSE (every key present) rather than sparse, even
// though the read path is built to fill defaults for missing keys. The two are
// not in tension: sparseness on read exists so a row written before a default
// moved still tracks the new default for keys the user never touched, and a
// key the user DID save is by definition one they expressed an opinion about.
// Writing the whole object means a Prefs/set that names one property preserves
// the other twelve exactly as they were served, which is the idempotence
// property the JMAP layer's per-property patch depends on.
func encodePrefs(p Prefs) ([]byte, error) {
	// Marshal the struct, then splice the version in, rather than giving Prefs
	// a Version field: the version is metadata ABOUT the document, not a
	// preference, and a field on Prefs would leak onto the JMAP wire object
	// where RFC 8621 has no place for it.
	body, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("encoding preferences: %w", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("encoding preferences: %w", err)
	}
	doc[prefsVersionKey] = PrefsSchemaVersion
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encoding preferences: %w", err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// the two operations
// ---------------------------------------------------------------------------

// GetPrefs reads an account's preferences, materialized.
//
// An account with no row is NOT an error: it gets DefaultPrefs with
// Exists:false and a zero watermark. That is the accurate reading of "this
// user has never changed a setting", and it is what lets migration 0007 ship
// without a backfill — every pre-existing account behaves correctly with no
// rows written at all.
//
// A row this build cannot read (ErrPrefsUnknownVersion) IS an error, and
// deliberately does not degrade to defaults: serving factory settings for a
// document written by a newer binary would show the user a settings screen
// that silently disagrees with what they saved, and the first save from that
// screen would overwrite it.
func (s *Store) GetPrefs(ctx context.Context, accountID int64) (PrefsRecord, error) {
	// schema_version is not selected here, and that is the point of the column
	// rather than an omission: the READER's authority is the document's own
	// "v" key, because that is the value migratePrefs actually interprets.
	// Reporting the column instead could describe a migration that did not
	// happen. The column exists for operators and batch jobs (migration 0007's
	// header), and PutPrefs writes the two from one struct so they agree.
	var (
		raw       []byte
		createdAt time.Time
		updatedAt time.Time
	)
	err := s.pool.QueryRow(ctx, `
		SELECT prefs, created_at, updated_at
		  FROM account_prefs WHERE account_id = $1`, accountID).
		Scan(&raw, &createdAt, &updatedAt)
	if err != nil {
		if isNoRows(err) {
			return PrefsRecord{
				AccountID:     accountID,
				Prefs:         DefaultPrefs(),
				SchemaVersion: PrefsSchemaVersion,
				Exists:        false,
			}, nil
		}
		return PrefsRecord{}, fmt.Errorf("reading the preferences of account %d: %w", accountID, err)
	}

	prefs, docVersion, err := migratePrefs(raw)
	if err != nil {
		return PrefsRecord{}, fmt.Errorf("reading the preferences of account %d: %w", accountID, err)
	}
	return PrefsRecord{
		AccountID:     accountID,
		Prefs:         prefs,
		SchemaVersion: docVersion,
		Exists:        true,
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
	}, nil
}

// PutPrefs stores an account's complete preference document and returns the
// stored result.
//
// It is an upsert, because the row's existence is an implementation detail of
// "has this user ever changed anything" and no caller should have to branch on
// it. The caller supplies a FULL Prefs — the JMAP layer reads the current
// object, applies the client's patch, validates, and writes the result — so
// this method never merges. Merging here would need a second source of truth
// for what "unset" means, which is exactly what the sparse-vs-dense split in
// encodePrefs avoids.
//
// updated_at moves on every call, INCLUDING a write that changes nothing. That
// is the deliberate choice for the state cursor: a client that saved the same
// value twice still gets a state advance, which is a harmless extra refresh,
// whereas suppressing the bump would mean a legitimate save could silently not
// notify the user's other sessions. A no-op write is cheap; a missed push is a
// stale settings screen.
func (s *Store) PutPrefs(ctx context.Context, accountID int64, p Prefs) (PrefsRecord, error) {
	doc, err := encodePrefs(p)
	if err != nil {
		return PrefsRecord{}, fmt.Errorf("storing the preferences of account %d: %w", accountID, err)
	}

	var (
		stored    []byte
		createdAt time.Time
		updatedAt time.Time
	)
	err = s.pool.QueryRow(ctx, `
		INSERT INTO account_prefs (account_id, prefs, schema_version)
		VALUES ($1, $2::jsonb, $3)
		ON CONFLICT (account_id) DO UPDATE
		    SET prefs          = EXCLUDED.prefs,
		        schema_version = EXCLUDED.schema_version,
		        updated_at     = now()
		RETURNING prefs, created_at, updated_at`,
		accountID, doc, PrefsSchemaVersion).
		Scan(&stored, &createdAt, &updatedAt)
	if err != nil {
		return PrefsRecord{}, fmt.Errorf("storing the preferences of account %d: %w", accountID, err)
	}

	// Read the stored bytes back through the same chain the read path uses,
	// rather than echoing the argument. It costs one unmarshal and it proves,
	// on every write, that what landed in the column is something this build
	// can read — a round-trip failure surfaces at the write that caused it
	// instead of at some later read by a user who changed nothing.
	prefs, docVersion, err := migratePrefs(stored)
	if err != nil {
		return PrefsRecord{}, fmt.Errorf("storing the preferences of account %d: %w", accountID, err)
	}

	return PrefsRecord{
		AccountID:     accountID,
		Prefs:         prefs,
		SchemaVersion: docVersion,
		Exists:        true,
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
	}, nil
}

// PrefsWatermark is max(updated_at) over an account's preferences — at most
// one row, so it is that row's updated_at, or the zero time when the account
// has none.
//
// It exists as its own method, shaped like IdentityWatermark, because the
// state cursor's grammar is shared: internal/jmap/mail's stateFor takes a
// watermark and a count, and every type feeds it the same two values from the
// same two kinds of query.
func (s *Store) PrefsWatermark(ctx context.Context, accountID int64) (time.Time, error) {
	var t *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT max(updated_at) FROM account_prefs WHERE account_id = $1`, accountID).Scan(&t)
	if err != nil {
		return time.Time{}, fmt.Errorf("reading the preference watermark of account %d: %w", accountID, err)
	}
	if t == nil {
		return time.Time{}, nil
	}
	return *t, nil
}

// CountPrefs is the row count that rides alongside the watermark in the state
// string: 0 before the account has ever saved a preference, 1 after.
//
// The count looks trivial for a table that holds at most one row per account,
// and it is kept for the reason migration 0007's header gives: it is what makes
// the state move when the row first APPEARS. Without it, "no preferences" and
// "preferences saved at time T" would both have to be encoded in a watermark,
// and the transition between them would be invisible to a client holding the
// earlier state.
func (s *Store) CountPrefs(ctx context.Context, accountID int64) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM account_prefs WHERE account_id = $1`, accountID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting the preferences of account %d: %w", accountID, err)
	}
	return n, nil
}
