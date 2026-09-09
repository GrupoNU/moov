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
//
// # v2 (the E5/E7/E8/E9b roaming keys)
//
// v2 adds Labels, OfflineDepth, AddressAutocomplete, SendAndArchive,
// DefaultReplyBehavior and Signatures — client-side state those epics named as
// gaps, so it roams between a user's devices instead of dying with a browser
// profile.
//
// It is a PURE ADDITION: no v1 key is renamed, retyped or re-encoded, so a v1
// document read by this build is a v2 document whose new keys are absent, and
// absent keys are exactly what the defaults-on-read mechanism already fills.
// By the rule stated above ("NOT bumped for a new key with a default") this
// bump was therefore not strictly required — it is taken anyway for one
// reason: the version is the only signal an OLD binary has that a document
// holds data it would silently drop and then overwrite. A v1-stamped document
// carrying a user's twenty labels would be read by the previous release,
// re-encoded without them, and the labels would be gone. Stamping v2 makes
// that old binary refuse instead (ErrPrefsUnknownVersion), which is the
// recoverable failure of the two — the same argument the error's own
// documentation makes, now with real data behind it.
//
// # v3 (folderVisibility)
//
// v3 adds exactly one key, FolderVisibility: which mailboxes the client draws
// in its folder rail, keyed by the display name the client shows the user.
//
// It is a PURE ADDITION on the same terms v2 was — no key renamed, retyped or
// re-encoded — so the lift is again the empty operation, and the bump is taken
// for the one reason that is not about reading: a v2-stamped document carrying
// a user's folder choices would be read by the PREVIOUS release, re-encoded
// without them, and every hidden folder would silently reappear on the first
// settings save. Stamping v3 makes that binary refuse instead.
const PrefsSchemaVersion = 3

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

	// ---------------------------------------------------------------------
	// v2 — the roaming keys named as gaps by epics E5, E7, E8 and E9b
	// ---------------------------------------------------------------------

	// Labels is PRESENTATION metadata for labels, keyed by label name.
	//
	// The label ITSELF is not here and never will be: arbitrage A6 puts the
	// assignment in IMAP keywords and the definition in a METADATA annotation,
	// so a label survives in Dovecot with Moov's database deleted — the "Moov
	// is a reconstructible cache" invariant of ADR-001. What lives here is the
	// part Dovecot has no place for and no opinion about: which swatch the
	// chip is drawn in, and whether the label shows in the sidebar.
	//
	// The map is capped at internal/imap.MaxDurableKeywordsPerMailbox (26)
	// entries, cited at internal/imap/metadata.go:52. That constant is a
	// Maildir fact — keywords are one letter a-z in the filename, and
	// dovecot-keywords stops at index 25 — so a user can never durably hold a
	// 27th label, and metadata for a label that cannot exist is dead weight
	// that would grow without bound if a client kept writing it.
	//
	// Omitted (nil) is distinct from empty only in encoding, never in meaning:
	// both are "no label has custom presentation", and every label the map
	// does not name is drawn in the default swatch, visible.
	Labels map[string]LabelPrefs `json:"labels,omitempty"`

	// OfflineDepth is how much mail the PWA keeps for offline reading (E9b).
	//
	// It is a preference rather than a constant because the honest number
	// depends on the device: a phone on a metered connection and a desktop on
	// a fast link want different answers, and the user is the only one who
	// knows which they are on. It roams because the ANSWER usually does not —
	// a user who wants shallow caching wants it everywhere.
	OfflineDepth OfflineDepthPrefs `json:"offlineDepth"`

	// AddressAutocomplete is "auto" or "manual" — Gmail's "create contacts for
	// autocomplete" setting. "auto" adds an address to the autocomplete pool
	// when the user mails it; "manual" only offers addresses the user saved
	// deliberately.
	//
	// Gmail's own default is automatic, and it is adopted here under the canon
	// filter's first rule.
	AddressAutocomplete string `json:"addressAutocomplete"`

	// SendAndArchive shows the "Send & Archive" button in replies (E7).
	//
	// DEFAULT TRUE, which is a REGISTERED DIVERGENCE from Gmail (whose setting
	// ships off) and, unusually, one taken after the fact rather than before:
	// the button already shipped visible in Moov's composer, so a default of
	// false would REMOVE a control users already have. Changing what an
	// existing user sees is a worse failure than differing from Gmail on a
	// setting Google publishes no security reason for — the canon's §1.2 filter
	// permits divergence exactly there.
	SendAndArchive bool `json:"sendAndArchive"`

	// DefaultReplyBehavior is "reply" or "replyAll" (canon §2.3).
	//
	// Gmail's default is "reply", and the reason to adopt it is not deference:
	// the failure modes are asymmetric. Defaulting to reply-all means a user
	// eventually answers a mailing list in a message they meant for one person,
	// which cannot be taken back; defaulting to reply means they occasionally
	// have to click "reply all", which costs a click.
	DefaultReplyBehavior string `json:"defaultReplyBehavior"`

	// Signatures is the named-signature model of epic E7: several signatures a
	// user can pick between, plus which one new mail and replies start with.
	//
	// # Precedence against the per-identity signature — read this before using
	// either
	//
	// RFC 8621 §6 gives an Identity exactly ONE textSignature and ONE
	// htmlSignature, and those remain AUTHORITATIVE for any client that speaks
	// only standard JMAP — Bulwark, or any third-party client — because they
	// are the only signature such a client can see. Nothing here overrides
	// them on the wire and nothing here is injected into a message by the
	// server.
	//
	// The rule, stated once so both layers can cite it:
	//
	//	MOOV's own PWA, composing new mail : if Signatures.ForNew names an
	//	                                     existing item, use that item's
	//	                                     body; otherwise fall back to the
	//	                                     Identity's signature.
	//	MOOV's own PWA, composing a reply  : the same, with ForReply.
	//	Any other JMAP client              : the Identity's signature, always.
	//	                                     It never learns this key exists —
	//	                                     the vendor capability gates it.
	//
	// This is a PRESENTATION-LAYER preference, not a protocol divergence: the
	// signature is inserted into the body by the composer before the message
	// is submitted, exactly as §6 says a client "SHOULD" do with the Identity's
	// own. The server assembles no signature into any message, so there is no
	// state in which two clients disagree about what was actually sent — they
	// only ever disagree about what the composer PRE-FILLED, which is a client
	// preference by definition.
	Signatures SignaturePrefs `json:"signatures"`

	// ---------------------------------------------------------------------
	// v3 — the folder rail
	// ---------------------------------------------------------------------

	// FolderVisibility is which mailboxes the client draws in its folder rail,
	// keyed by the mailbox DISPLAY NAME as the client shows it to the user, with
	// values "show", "hide" or "showIfUnread" — the same three Gmail gives its
	// label list, and the same three LabelPrefs.Visibility carries.
	//
	// # It stores only EXPLICIT choices, and that is the whole design
	//
	// An absent key is not "hidden" and not "shown": it is "the client has no
	// instruction here, so its own policy decides". Gmail's rail is full of such
	// policy — Inbox always visible, Trash below the fold, an empty user folder
	// treated one way and a busy one another — and that policy belongs to the
	// client because it is a rendering decision that changes with the surface
	// (a phone rail and a desktop rail want different answers for the same
	// account).
	//
	// The server's job is narrower and more durable: remember what the user
	// SAID. Writing a default in here would freeze today's client policy into
	// every account's stored document, so a later improvement to the rail would
	// reach only accounts created after it — which is precisely the failure the
	// defaults-on-read mechanism exists to prevent, reintroduced one level down.
	//
	// The key is a display name rather than a mailbox id for the same reason
	// Labels' key is a label name: an id is a row in a cache Moov can rebuild
	// (ADR-001's reconstructible-cache invariant), and a preference that dies
	// with a resync is not a preference. A renamed folder loses its entry, which
	// is honest — the user hid a folder called something else.
	//
	// The map is capped at MaxFolderVisibility entries and each key at
	// MaxFolderNameBytes; both are enforced by the JMAP layer, which is where a
	// refusal can name the offending property.
	//
	// Omitted (nil) is distinct from empty only in encoding, never in meaning:
	// both are "the user has expressed no folder preference".
	FolderVisibility map[string]string `json:"folderVisibility,omitempty"`
}

// LabelPrefs is one label's presentation metadata (v2).
type LabelPrefs struct {
	// Color is a palette id — a NAME such as "amber", never a hex value. The
	// closed set is web/src/mail/labelPalette.ts, mirrored and enforced by the
	// JMAP layer's labelColorChoices.
	//
	// Ids rather than hex is the palette's own load-bearing decision: a future
	// contrast fix to the amber swatch reaches every existing label, instead of
	// leaving them pinned to a hex string chosen in 2026.
	Color string `json:"color"`

	// Visibility is "show", "showIfUnread" or "hide" — Gmail's three label-list
	// visibilities. It governs the SIDEBAR only; a hidden label still applies
	// to its messages and still shows on the message itself.
	Visibility string `json:"visibility"`
}

// OfflineDepthPrefs is how much mail the PWA keeps offline (v2, epic E9b).
type OfflineDepthPrefs struct {
	// HeadersPerMailbox is how many message headers per mailbox are cached for
	// offline listing: [50, 1000], default 200.
	//
	// The floor is not decoration. A depth below a screenful would make the
	// offline list visibly truncated at the first scroll, which reads as data
	// loss rather than as a setting; 50 is comfortably more than one viewport
	// at any density.
	HeadersPerMailbox int `json:"headersPerMailbox"`

	// Bodies is how many full message bodies are cached: [20, 500], default
	// 100. Lower than the header count by construction — a body is orders of
	// magnitude larger than a header, and the browser's storage quota is the
	// binding constraint.
	Bodies int `json:"bodies"`
}

// SignaturePrefs is the named-signature model (v2, epic E7). The precedence
// rule against the per-identity signature is documented on Prefs.Signatures.
type SignaturePrefs struct {
	// Items are the signatures, keyed by an opaque client-chosen id.
	Items map[string]SignatureItem `json:"items,omitempty"`

	// ForNew is the id used when composing new mail, or "" for none — in which
	// case the composer falls back to the Identity's own signature.
	//
	// The empty string is the "none" case rather than a nil pointer, for the
	// same reason Language's is: "" is not a valid id (the validator refuses
	// it), so it cannot collide with a real choice, and it keeps the struct
	// free of a pointer every consumer would have to nil-check. The JMAP layer
	// renders it as the RFC's null.
	ForNew string `json:"forNew"`

	// ForReply is the id used when replying or forwarding, or "" for none.
	ForReply string `json:"forReply"`
}

// SignatureItem is one named signature (v2).
type SignatureItem struct {
	// Name is what the user calls it in the picker: "Work", "Personal".
	Name string `json:"name"`

	// TextBody is the plain-text form.
	TextBody string `json:"textBody"`

	// HTMLBody is the HTML form, SANITIZED BY THE JMAP LAYER BEFORE IT
	// ARRIVES HERE, through the same sanitizeHTMLSignature the per-identity
	// htmlSignature goes through.
	//
	// The sanitizer is not called from this package, and that placement is the
	// same division of labor the file header states: this file owns the shape,
	// the protocol layer owns the values. It matters more here than elsewhere
	// because the reason signatures are sanitized on the way IN
	// (internal/jmap/mail/signature.go) is that they are content MOOV
	// TRANSMITS under its own DKIM key — so the write path is the only place
	// the cleaning can happen, and a second sanitizer here would be a second
	// policy to drift.
	HTMLBody string `json:"htmlBody"`
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

		// v2. Labels and Signatures.Items stay NIL rather than empty maps: an
		// empty map and a nil map mean the same thing here ("nothing
		// customized"), and a nil one cannot be mutated by a caller that
		// received the defaults, which encodePrefs' dense-write and the JMAP
		// layer's read-patch-write both rely on not happening.
		Labels: nil,
		OfflineDepth: OfflineDepthPrefs{
			HeadersPerMailbox: 200,
			Bodies:            100,
		},
		AddressAutocomplete:  "auto",  // Gmail's own default.
		SendAndArchive:       true,    // registered divergence — see the field.
		DefaultReplyBehavior: "reply", // canon §2.3; the asymmetric-failure argument.
		Signatures:           SignaturePrefs{},

		// v3. NIL, and unlike every other default this one is not a product
		// choice deferred to a constant — it is the ABSENCE of a choice, on
		// purpose. The rail's defaults are the client's policy (see the field),
		// so the honest factory setting is "the user has said nothing".
		FolderVisibility: nil,
	}
}

// Equal reports whether two preference values are identical.
//
// It exists because Prefs stopped being comparable with == when v2 gave it
// maps, and the alternative — reflect.DeepEqual at every call site — would
// treat a nil map and an empty one as different when this schema says they are
// the same thing (DefaultPrefs' comment states why nil is the canonical form).
// A method keeps that one judgement in one place instead of in every caller.
func (p Prefs) Equal(other Prefs) bool {
	if p.UndoSendSeconds != other.UndoSendSeconds ||
		p.ImagesPolicy != other.ImagesPolicy ||
		p.ConversationView != other.ConversationView ||
		p.HoverActions != other.HoverActions ||
		p.AutoAdvance != other.AutoAdvance ||
		p.Density != other.Density ||
		p.ShowSnippets != other.ShowSnippets ||
		p.KeyboardShortcuts != other.KeyboardShortcuts ||
		p.Language != other.Language ||
		p.ReadingPane != other.ReadingPane ||
		p.InboxType != other.InboxType ||
		p.Notifications != other.Notifications ||
		p.Theme != other.Theme ||
		p.OfflineDepth != other.OfflineDepth ||
		p.AddressAutocomplete != other.AddressAutocomplete ||
		p.SendAndArchive != other.SendAndArchive ||
		p.DefaultReplyBehavior != other.DefaultReplyBehavior ||
		p.Signatures.ForNew != other.Signatures.ForNew ||
		p.Signatures.ForReply != other.Signatures.ForReply {
		return false
	}
	if len(p.Labels) != len(other.Labels) {
		return false
	}
	for name, v := range p.Labels {
		if w, ok := other.Labels[name]; !ok || v != w {
			return false
		}
	}
	if len(p.Signatures.Items) != len(other.Signatures.Items) {
		return false
	}
	for id, v := range p.Signatures.Items {
		if w, ok := other.Signatures.Items[id]; !ok || v != w {
			return false
		}
	}
	if len(p.FolderVisibility) != len(other.FolderVisibility) {
		return false
	}
	for name, v := range p.FolderVisibility {
		if w, ok := other.FolderVisibility[name]; !ok || v != w {
			return false
		}
	}
	return true
}

// Clone returns a deep copy: the maps are duplicated, so a caller that mutates
// the result cannot reach into the value it was made from.
//
// Every path that hands a Prefs across a boundary uses it. Without it, GetPrefs
// would return a struct whose Labels map aliases the one just decoded, and the
// JMAP layer's read-patch-write — which mutates the map in place while applying
// a patch — would be editing an object it was only supposed to be reading from.
func (p Prefs) Clone() Prefs {
	out := p
	if p.Labels != nil {
		out.Labels = make(map[string]LabelPrefs, len(p.Labels))
		for k, v := range p.Labels {
			out.Labels[k] = v
		}
	}
	if p.Signatures.Items != nil {
		out.Signatures.Items = make(map[string]SignatureItem, len(p.Signatures.Items))
		for k, v := range p.Signatures.Items {
			out.Signatures.Items[k] = v
		}
	}
	if p.FolderVisibility != nil {
		out.FolderVisibility = make(map[string]string, len(p.FolderVisibility))
		for k, v := range p.FolderVisibility {
			out.FolderVisibility[k] = v
		}
	}
	return out
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
//	v == 1                  -> decode the v1 keys, LIFT to v2 and then to v3
//	                           (both the empty operation — see below), and fill.
//	v == 2                  -> decode, LIFT to v3, and fill.
//	v == PrefsSchemaVersion -> decode and fill.
//	anything else           -> ErrPrefsUnknownVersion. That covers a version
//	                           from the FUTURE, which is the real case (see the
//	                           error's own documentation: an old binary must not
//	                           read, downgrade and then overwrite a newer
//	                           document), and a NEGATIVE one, which the column's
//	                           CHECK already makes unstorable and which is
//	                           therefore corruption rather than a version.
//
// # The v1 -> v2 lift, and why it is one shared decode
//
// v2 is a pure addition: every v1 key keeps its name, its type and its
// meaning, and the six new keys are absent from a v1 document. Filling an
// absent key from the defaults is precisely what unmarshaling onto
// DefaultPrefs already does, so the lift is the EMPTY transformation and both
// versions can share one decode. liftPrefsV1ToV2 is written out anyway,
// called on the v1 path only, because the shape of the chain is the deliverable
// here: the next version that does rename or retype a key adds its step beside
// this one, and a v1 document then walks 1 -> 2 -> 3 through steps that were
// each written once, instead of needing a fresh direct-to-current decoder per
// stored version.
//
// The version a document reports is the version it was STORED as, never the
// version it was lifted to. GetPrefs surfaces that in PrefsRecord.SchemaVersion,
// so an operator counting un-rewritten rows sees the truth.
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
		//
		// A v1 document CAN contain v2-shaped keys only if something wrote them
		// without stamping the version — which nothing in this build does — and
		// decoding them would be harmless anyway, since the lift below would
		// leave them alone. The decode is shared because the shapes agree; see
		// the function's header.
		if err := json.Unmarshal(raw, &out); err != nil {
			return Prefs{}, 0, fmt.Errorf("decoding the stored v1 preferences: %w", err)
		}
		// The composition the chain was written for: a v1 document walks
		// 1 -> 2 -> 3 through steps each written and tested once, rather than
		// needing a fresh direct-to-current decoder per stored version.
		return liftPrefsV2ToV3(liftPrefsV1ToV2(out)), 1, nil

	case 2:
		if err := json.Unmarshal(raw, &out); err != nil {
			return Prefs{}, 0, fmt.Errorf("decoding the stored v2 preferences: %w", err)
		}
		return liftPrefsV2ToV3(out), 2, nil

	case 3:
		if err := json.Unmarshal(raw, &out); err != nil {
			return Prefs{}, 0, fmt.Errorf("decoding the stored v3 preferences: %w", err)
		}
		return out, 3, nil

	default:
		return Prefs{}, 0, fmt.Errorf("%w: the stored document declares v%d, this build reads up to v%d",
			ErrPrefsUnknownVersion, version, PrefsSchemaVersion)
	}
}

// liftPrefsV1ToV2 raises a decoded v1 document to the v2 schema.
//
// It is the IDENTITY, and that is the correct implementation rather than a
// placeholder: v2 renames nothing, retypes nothing and re-encodes nothing, so
// the only difference between a v1 document and a v2 one is the six keys v1
// omits — and the caller decoded onto DefaultPrefs, so those keys already hold
// their defaults by the time this function sees the value.
//
// Writing it out regardless is what makes the chain a chain. A future v3 that
// DOES transform something adds liftPrefsV2ToV3 beside this, and the v1 path
// becomes `liftPrefsV2ToV3(liftPrefsV1ToV2(out))` — one composition, each step
// tested on its own, no combinatorial set of direct decoders. Deleting this
// function because it does nothing today would delete the seam that makes that
// cheap.
func liftPrefsV1ToV2(p Prefs) Prefs { return p }

// liftPrefsV2ToV3 raises a decoded v2 document to the v3 schema.
//
// It is the IDENTITY, for exactly the reason liftPrefsV1ToV2 is: v3 renames
// nothing and retypes nothing, and its single new key (folderVisibility) is
// absent from a v2 document — which the defaults-on-read decode has already
// left at its default, nil.
//
// The nil default is what makes the empty lift CORRECT here rather than merely
// convenient. If v3's factory setting were a populated map ("hide Spam by
// default", say), a v2 document would have to be given that map on the way up,
// and the lift would stop being empty — but it is not, because the rail's
// defaults are the client's policy and the server stores only what the user
// said (see Prefs.FolderVisibility). A v2 user therefore arrives at v3 having
// expressed no folder preference, which is the truth about them.
func liftPrefsV2ToV3(p Prefs) Prefs { return p }

// encodePrefs renders preferences for storage: the full typed object plus its
// version key.
//
// The document is written DENSE (every key present) rather than sparse, even
// though the read path is built to fill defaults for missing keys. The two are
// not in tension: sparseness on read exists so a row written before a default
// moved still tracks the new default for keys the user never touched, and a
// key the user DID save is by definition one they expressed an opinion about.
// Writing the whole object means a Prefs/set that names one property preserves
// the others exactly as they were served, which is the idempotence property
// the JMAP layer's per-property patch depends on.
//
// The MAPS are the deliberate exception, tagged `omitempty` — the two v2 ones
// and v3's folderVisibility alike: an empty
// `labels` map carries no information a missing one does not, and writing
// `"labels":{}` into every row would put a key in the column whose only effect
// is to make a document that means "nothing customized" look different from
// another document that means "nothing customized". They read back as nil
// either way (migratePrefs decodes onto DefaultPrefs, whose maps are nil), so
// the round trip is exact.
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
