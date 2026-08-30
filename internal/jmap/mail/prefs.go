package mail

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/GrupoNU/moov/internal/jmap"
)

// Prefs — Moov's per-account preferences, served as a JMAP object under the
// VENDOR capability jmap.CapPrefs (L3 epic E0).
//
// # The shape: a singleton, deliberately
//
// There is exactly one Prefs object per account and its id is the constant
// "singleton". That is not an invention: RFC 8621 §8 gives VacationResponse
// precisely this shape — "The id of the object is 'singleton'" — because a
// per-account configuration document has no plural. Reusing it means every
// client library's /get and /set machinery works unchanged: ids:null returns
// the one object, an ids array containing "singleton" returns it, any other id
// is notFound, and /set updates it by that id.
//
// The alternative — inventing arguments like `Prefs/getForAccount` — would be
// a method shape no JMAP client has code for, in exchange for saving one
// string on the wire.
//
// # Why the methods live in this package
//
// Everything here is the RFC 8620 §5.1/§5.3/§5.2 skeleton the mail types
// already use: parseGet, parseSet, getResponse, setError, stateFor,
// cursorFromState. A separate internal/jmap/prefs package would have to
// duplicate every one of them or export them, and a second copy of the /set
// error vocabulary is exactly the kind of drift that produces two servers
// answering the same client differently. The CAPABILITY is what isolates
// preferences from the mail types, and it does so at the dispatch layer
// (registry gating) rather than at the package layer — which is where RFC 8620
// §1.8 puts the isolation.
//
// # Validation: strict, per key, with the domain in the error
//
// Every property has a closed domain (or is a bool, or is a language tag), and
// a value outside it is refused with §5.3's invalidProperties, listing ALL the
// offending keys and describing each. Unknown keys are refused the same way,
// per §5.3's "any property [...] that is not a valid property of the object".
//
// This is the strictest reading available and it is chosen on purpose: a
// silently-ignored preference is the worst possible failure for a settings
// screen, because the user sees the control move, the save succeed, and the
// behavior not change. Refusing loudly means the client can show what went
// wrong.

// prefsID is the wire id of the singleton (RFC 8621 §8's shape).
const prefsID = "singleton"

// ---------------------------------------------------------------------------
// contracts
// ---------------------------------------------------------------------------

// PrefsValue is one account's preferences as the handlers see them: fully
// materialized, every field carrying either the user's choice or the product
// default.
//
// It mirrors store.Prefs field for field, and that duplication is the same
// deliberate boundary every other type here keeps (contracts.go): a type named
// in this package's interfaces is a type this package owns, so the JMAP layer
// never has store shapes in its signatures and the store never has to care
// what the wire looks like. The adapter maps between them (prefs_adapter.go),
// and a test pins that no field is dropped in either direction.
type PrefsValue struct {
	UndoSendSeconds   int
	ImagesPolicy      string
	ConversationView  bool
	HoverActions      bool
	AutoAdvance       string
	Density           string
	ShowSnippets      bool
	KeyboardShortcuts bool

	// Language is a BCP 47 tag, or "" for "follow the browser" — which the
	// wire renders as JSON null (see prefsObject).
	Language string

	ReadingPane   string
	InboxType     string
	Notifications string
	Theme         string

	// --- v2: the roaming keys of epics E5, E7, E8 and E9b ---

	// Labels is presentation metadata per label NAME. The label itself lives
	// in IMAP keywords (arbitrage A6); this is only the swatch and the sidebar
	// visibility, which Dovecot has no place for.
	Labels map[string]LabelPrefsValue

	// OfflineDepth is how much mail the PWA keeps for offline reading (E9b).
	OfflineDepth OfflineDepthValue

	// AddressAutocomplete is "auto" or "manual".
	AddressAutocomplete string

	// SendAndArchive shows the Send & Archive button in replies (E7).
	SendAndArchive bool

	// DefaultReplyBehavior is "reply" or "replyAll" (canon §2.3).
	DefaultReplyBehavior string

	// Signatures is the E7 named-signature model. store.Prefs.Signatures
	// carries the precedence rule against the per-identity signature; the short
	// form is that RFC 8621 §6's textSignature/htmlSignature stay AUTHORITATIVE
	// for any client that speaks only standard JMAP, and this is a
	// presentation-layer preference our own composer consults first.
	Signatures SignaturePrefsValue
}

// LabelPrefsValue is one label's presentation metadata.
type LabelPrefsValue struct {
	// Color is a palette id — a name such as "amber", never a hex value.
	Color string
	// Visibility is "show", "showIfUnread" or "hide".
	Visibility string
}

// OfflineDepthValue is the offline cache depth (E9b).
type OfflineDepthValue struct {
	HeadersPerMailbox int
	Bodies            int
}

// SignaturePrefsValue is the named-signature model (E7).
type SignaturePrefsValue struct {
	Items map[string]SignatureItemValue

	// ForNew and ForReply are item ids, or "" for none — which the wire renders
	// as null and which means "fall back to the Identity's own signature".
	ForNew   string
	ForReply string
}

// SignatureItemValue is one named signature. HTMLBody is stored SANITIZED, by
// the same sanitizeHTMLSignature the per-identity htmlSignature goes through
// (signature.go states why that one string is cleaned on the way in).
type SignatureItemValue struct {
	Name     string
	TextBody string
	HTMLBody string
}

// PrefsRecord is the singleton plus the watermark its state cursor is built
// from.
type PrefsRecord struct {
	Prefs PrefsValue

	// UpdatedAt is the row's watermark, which /changes pages on. It is not a
	// wire property and is never rendered.
	UpdatedAt time.Time

	// Exists reports whether the account has ever saved a preference. It is
	// carried because it is what makes the state string move when the FIRST
	// save happens: the cursor's count term is 0 before and 1 after (migration
	// 0007's header explains why a watermark alone could not express it).
	Exists bool
}

// PrefsStore is the preference surface as the JMAP layer sees it. The
// store-backed implementation is prefs_adapter.go; the fakes in the tests
// drive the handlers without PostgreSQL.
type PrefsStore interface {
	// GetPrefs returns the account's preferences, materialized. An account
	// that has never saved one is NOT an error: it gets the defaults with
	// Exists false.
	GetPrefs(ctx context.Context, accountID int64) (PrefsRecord, error)

	// PutPrefs stores the COMPLETE preference object. The handler reads,
	// patches, validates and writes the whole thing, so this never merges.
	PutPrefs(ctx context.Context, accountID int64, p PrefsValue) (PrefsRecord, error)

	// PrefsState is the state cursor, in the same "<nanos>-<count>" grammar
	// every other type uses (adapter.go stateFor).
	PrefsState(ctx context.Context, accountID int64) (string, error)
}

// ---------------------------------------------------------------------------
// the value domains
// ---------------------------------------------------------------------------

// The closed value sets, each with the source that fixed it. They are the
// specification the validator is written against; store.Prefs' field comments
// carry the same citations, and a test pins that the two agree on the
// defaults.
var (
	// undoSendSecondsChoices is Gmail's exact offered set (canon §2.3,
	// support.google.com/mail/answer/2819488). The server ALSO clamps to
	// [5, 30] on the send path — see prefsUndoWindow — so a value that reaches
	// the outbox by any other route still produces an honorable window.
	undoSendSecondsChoices = []int{5, 10, 20, 30}

	// imagesPolicyChoices — decision D-4. "always" means "always through the
	// HMAC proxy"; the proxy is the precondition that makes Gmail's own
	// default defensible here (canon §7.1).
	imagesPolicyChoices = []string{"always", "ask"}

	// autoAdvanceChoices — canon §2.2: Gmail's opt-in offers "older messages,
	// newer messages, or the conversation list".
	autoAdvanceChoices = []string{"list", "newer", "older"}

	// densityChoices — canon §2.4. The three names are Gmail's; the pixel
	// values behind them are ours (the canon's §5 records that Google
	// publishes no fetchable page for them).
	densityChoices = []string{"default", "comfortable", "compact"}

	// readingPaneChoices — canon §2.4 (/9499937): "No split", "Right of
	// inbox", "Below inbox".
	readingPaneChoices = []string{"none", "right", "bottom"}

	// inboxTypeChoices — the DETERMINISTIC subset of Gmail's six (canon §2.4,
	// /186531). "Important first" and "Priority Inbox" need the importance
	// classifier and are deferred to the AI phase by the same rule that cut
	// notifications to two modes (GC-2): a control whose classifier does not
	// exist is a control that does nothing. "Multiple Inboxes" is a separate
	// per-section query surface, deferred by name in the plan's §6.
	inboxTypeChoices = []string{"default", "unread_first", "starred_first"}

	// notificationsChoices — GC-2: two modes pre-AI. The third Gmail mode
	// ("important mail only") is gated on the same missing classifier. The
	// paritary behavior is FOREGROUND notification over the existing SSE
	// stream, because that is what Gmail web does (canon §2.9).
	notificationsChoices = []string{"new", "off"}

	// themeChoices — account-level, like Gmail's. The PWA additionally keeps a
	// localStorage copy because the theme must be applied before first paint,
	// and reconciles it against this value once the session loads; that copy is
	// a pre-paint cache, not a second source of truth.
	themeChoices = []string{"light", "dark", "system"}

	// --- v2 ---

	// labelColorChoices is the CLOSED label palette, mirroring the twelve ids
	// of web/src/mail/labelPalette.ts in its order.
	//
	// # Why ids and not colors
	//
	// The palette stores a NAME ("amber"), never a hex value, and that is the
	// palette's own load-bearing decision: a future contrast fix to the amber
	// swatch reaches every existing label instead of leaving them pinned to a
	// hex string chosen in 2026. The server therefore validates membership in a
	// closed set of names and holds no color data at all — the four hex values
	// behind each id are the client's business, and the server having a second
	// copy of them would be a second thing to drift.
	//
	// # Why the palette is closed at the SERVER too
	//
	// The client's reason (a free picker lets a user choose #f0f0f0 on #ffffff
	// and the unreadable chip is one we shipped) is a client-side argument, and
	// on its own it would justify enforcing this only in the UI. The server
	// enforces it because the client is not the only writer: any JMAP client
	// that opts into the vendor capability can Prefs/set, and an unvalidated
	// color field is a free-text column reachable over the API. A closed set
	// keeps the column holding ids and nothing else.
	//
	// # The duplication, stated plainly
	//
	// This list and labelPalette.ts are the same twelve names written twice,
	// across a language boundary no compiler spans. It is a real duplication
	// and it is accepted here because the alternatives are worse: generating Go
	// from TypeScript would put a codegen step between a designer and a swatch,
	// and serving the palette from the server would make the client unable to
	// render a chip until the session loads. What makes it safe is that the
	// server advertises this exact list in the account capability
	// (labelColorValues), so a client whose palette disagreed would see the
	// disagreement in the session object rather than in a rejected save.
	labelColorChoices = []string{
		"slate", "red", "orange", "amber", "lime", "green",
		"teal", "cyan", "blue", "indigo", "purple", "pink",
	}

	// labelVisibilityChoices — Gmail's three label-list visibilities. It
	// governs the SIDEBAR only: a hidden label still applies to its messages
	// and still renders on the message itself.
	labelVisibilityChoices = []string{"show", "showIfUnread", "hide"}

	// addressAutocompleteChoices — Gmail's "create contacts for autocomplete":
	// automatic, or only contacts the user saved deliberately.
	addressAutocompleteChoices = []string{"auto", "manual"}

	// defaultReplyBehaviorChoices — canon §2.3. Gmail's default is "reply", and
	// the reason to adopt it is that the failure modes are asymmetric: a
	// reply-all sent by accident to a mailing list cannot be taken back, while
	// a missing reply-all costs one click.
	defaultReplyBehaviorChoices = []string{"reply", "replyAll"}
)

// The caps on the v2 collections. Each is a limit with a REASON, not a round
// number, and each is advertised in the account capability so a client can
// stop a user before a save is refused rather than after.
const (
	// maxLabelPrefs caps the label-presentation map at the KEYWORD CEILING:
	// internal/imap.MaxDurableKeywordsPerMailbox, 26, cited at
	// internal/imap/metadata.go:52.
	//
	// The ceiling is a Maildir fact rather than a policy: keywords are encoded
	// as one letter a-z in the message filename and dovecot-keywords stops at
	// index 25, so a 27th label cannot exist DURABLY — validation V1 showed
	// Dovecot accepting 500 keywords in its warm index and keeping 26 after a
	// force-resync. Presentation metadata for a label that cannot exist is
	// therefore dead weight, and an uncapped map is one a buggy client can grow
	// without bound in a column every session read pulls.
	//
	// The constant is duplicated rather than imported because internal/jmap
	// must not depend on internal/imap — the protocol layer knows nothing about
	// transports — and prefs_mapping_test.go pins the two against each other so
	// the copy cannot drift.
	maxLabelPrefs = 26

	// maxSignatureItems caps the named signatures at 10.
	//
	// Unlike the label cap this is a product judgement, not a protocol fact:
	// signatures are picked from a dropdown, and a dropdown of more than about
	// ten is a list the user scrolls rather than scans. It also bounds the
	// object a settings screen fetches on every load — ten signatures at the
	// per-signature cap is the worst case, and it is what maxSignaturesBytes
	// below is derived from.
	maxSignatureItems = 10

	// maxSignatureNameBytes caps a signature's display name. It is a label in a
	// dropdown, not content.
	maxSignatureNameBytes = 64

	// maxSignatureIDBytes caps a signature's id. The id is opaque to the server
	// — the client chooses it — so the only constraint that matters is that it
	// cannot be used to smuggle content into a key.
	maxSignatureIDBytes = 64

	// maxSignaturesBytes caps the TOTAL size of the signature collection at
	// 128 KiB — TWICE the per-signature cap (maxSignatureBytes, 64 KiB, shared
	// with Identity and reasoned about on that constant), not ten times it.
	//
	// The per-item cap alone does not bound the object: ten signatures each
	// just under 64 KiB is 640 KiB in a JSONB column that every settings load
	// reads, every Prefs/get serializes, and every save rewrites whole. The
	// obvious total — items × per-item — is the arithmetic that FEELS right and
	// is exactly the one that can never bind, since it is the sum of the maxima
	// the per-item check already enforces. A cap that cannot be reached is not
	// a cap.
	//
	// 128 KiB is the honest bound for what this collection IS: a handful of
	// signatures, the largest image-free corporate ones running a few KiB. It
	// still admits two signatures at the full per-item cap, so the two limits
	// do not contradict each other for the single-signature case that shares
	// its constant with Identity.
	maxSignaturesBytes = 128 * 1024
)

// The exported accessors the session object builds its accountCapabilities
// from (jmaphttp's prefsAccountCapability).
//
// They return COPIES, and the underlying slices stay unexported. That is not
// defensive habit: the session builder runs per request, and an exported slice
// is a package-level variable any caller could reorder or truncate in place —
// which would silently change what the server advertises for every subsequent
// request while the validator kept enforcing the original. Copying makes the
// advertised list and the enforced list the same data with no way to make them
// diverge.

// UndoSendSecondsChoices is the offered undo-send window, in seconds.
func UndoSendSecondsChoices() []int { return append([]int(nil), undoSendSecondsChoices...) }

// ImagesPolicyChoices is the remote-image policy domain.
func ImagesPolicyChoices() []string { return append([]string(nil), imagesPolicyChoices...) }

// AutoAdvanceChoices is the auto-advance domain.
func AutoAdvanceChoices() []string { return append([]string(nil), autoAdvanceChoices...) }

// DensityChoices is the list-density domain.
func DensityChoices() []string { return append([]string(nil), densityChoices...) }

// ReadingPaneChoices is the reading-pane domain.
func ReadingPaneChoices() []string { return append([]string(nil), readingPaneChoices...) }

// InboxTypeChoices is the inbox-type domain (the deterministic subset).
func InboxTypeChoices() []string { return append([]string(nil), inboxTypeChoices...) }

// NotificationsChoices is the notification-mode domain (two modes pre-AI).
func NotificationsChoices() []string { return append([]string(nil), notificationsChoices...) }

// ThemeChoices is the theme domain.
func ThemeChoices() []string { return append([]string(nil), themeChoices...) }

// LabelColorChoices is the closed label palette, by id.
func LabelColorChoices() []string { return append([]string(nil), labelColorChoices...) }

// LabelVisibilityChoices is the label-visibility domain.
func LabelVisibilityChoices() []string { return append([]string(nil), labelVisibilityChoices...) }

// AddressAutocompleteChoices is the address-autocomplete domain.
func AddressAutocompleteChoices() []string {
	return append([]string(nil), addressAutocompleteChoices...)
}

// DefaultReplyBehaviorChoices is the reply-behavior domain.
func DefaultReplyBehaviorChoices() []string {
	return append([]string(nil), defaultReplyBehaviorChoices...)
}

// The numeric limits the account capability advertises, so a settings screen
// can stop a user at the boundary instead of after a refused save.

// MaxLabelPrefs is the label-metadata cap — the durable-keyword ceiling.
func MaxLabelPrefs() int { return maxLabelPrefs }

// MaxSignatureItems is the named-signature cap.
func MaxSignatureItems() int { return maxSignatureItems }

// MaxSignatureBytes is the per-signature byte cap, shared with Identity.
func MaxSignatureBytes() int { return maxSignatureBytes }

// MaxSignaturesBytes is the cap on the whole signature collection. It is
// SMALLER than MaxSignatureItems × MaxSignatureBytes on purpose — see the
// constant — so a client must advertise both to describe what it enforces.
func MaxSignaturesBytes() int { return maxSignaturesBytes }

// OfflineDepthBounds is the inclusive range each offline depth accepts.
func OfflineDepthBounds() (headersMin, headersMax, bodiesMin, bodiesMax int) {
	return minOfflineHeaders, maxOfflineHeaders, minOfflineBodies, maxOfflineBodies
}

// The offline-depth bounds (E9b).
//
// The floors are not decoration. A header depth below a screenful would make
// the offline list visibly truncated at the first scroll, which a user reads as
// data loss rather than as a setting; 50 is comfortably more than one viewport
// at any density. The ceilings are what the browser's storage quota tolerates
// for a mailbox of realistic size.
const (
	minOfflineHeaders = 50
	maxOfflineHeaders = 1000
	minOfflineBodies  = 20
	maxOfflineBodies  = 500
)

// prefsProperties is the property set this object serves — the keys /get's
// `properties` filter is validated against.
var prefsProperties = map[string]bool{
	"id":                true,
	"undoSendSeconds":   true,
	"imagesPolicy":      true,
	"conversationView":  true,
	"hoverActions":      true,
	"autoAdvance":       true,
	"density":           true,
	"showSnippets":      true,
	"keyboardShortcuts": true,
	"language":          true,
	"readingPane":       true,
	"inboxType":         true,
	"notifications":     true,
	"theme":             true,
	// v2.
	"labels":               true,
	"offlineDepth":         true,
	"addressAutocomplete":  true,
	"sendAndArchive":       true,
	"defaultReplyBehavior": true,
	"signatures":           true,
}

// ---------------------------------------------------------------------------
// registration
// ---------------------------------------------------------------------------

// RegisterPrefsMethods mounts the preference methods under the vendor
// capability.
//
// Same contract as every other registrar in this package: a missing dependency
// panics at STARTUP rather than at the first settings save, because a server
// that advertises the capability and cannot answer it is lying to every client
// that opted in.
//
// All three of /get, /set and /changes are registered. /changes is NOT a
// deliberate decline here — unlike Mailbox/queryChanges, which registers only
// to answer cannotCalculateChanges — because the singleton makes it genuinely
// cheap: there is one object, its watermark is a column, and "did it change
// since your cursor" is a comparison. Declining a question this server can
// actually answer would be the dishonest option.
func RegisterPrefsMethods(registry *jmap.Registry, deps *Deps) {
	if registry == nil || deps == nil {
		panic("mail: RegisterPrefsMethods requires a registry and deps")
	}
	if deps.Prefs == nil {
		panic("mail: RegisterPrefsMethods requires Prefs")
	}
	registry.Register("Prefs/get", jmap.CapPrefs, deps.handlePrefsGet)
	registry.Register("Prefs/changes", jmap.CapPrefs, deps.handlePrefsChanges)
	registry.Register("Prefs/set", jmap.CapPrefs, deps.handlePrefsSet)
}

// ---------------------------------------------------------------------------
// Prefs/get
// ---------------------------------------------------------------------------

// handlePrefsGet implements the standard /get method (RFC 8620 §5.1) over the
// singleton.
//
// ids:null returns the one object, as §5.1 requires ("If null, then all
// records in the data set are returned"). An ids array is honored literally:
// "singleton" resolves, anything else lands in notFound — which is the answer
// §5.1 prescribes and keeps the id space from pretending to be plural.
func (d *Deps) handlePrefsGet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseGet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}
	// §5.1: "If any of the properties are not valid [...] MUST return
	// invalidArguments." The same check every other /get here performs.
	if bad := unknownProperties(req.Properties, prefsProperties); len(bad) > 0 {
		sort.Strings(bad)
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("unknown Prefs properties: %s", strings.Join(bad, ", "))
	}

	rec, err := d.Prefs.GetPrefs(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading preferences", err)
	}
	state, err := d.Prefs.PrefsState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the preference state", err)
	}

	resp := newGetResponse(req.AccountID, state)
	if req.IDs == nil {
		resp.List = append(resp.List, prefsObject(rec.Prefs, req.Properties))
	} else {
		for _, id := range *req.IDs {
			if id == prefsID {
				resp.List = append(resp.List, prefsObject(rec.Prefs, req.Properties))
				continue
			}
			resp.NotFound = append(resp.NotFound, id)
		}
	}
	sort.Strings(resp.NotFound)
	return resp, nil
}

// prefsObject renders the singleton, honoring the /get properties filter.
//
// id is always present regardless of the filter — RFC 8620 §5.1: "The id
// property of the object is always returned, even if not explicitly
// requested."
func prefsObject(p PrefsValue, properties *[]string) map[string]any {
	full := map[string]any{
		"id":                prefsID,
		"undoSendSeconds":   p.UndoSendSeconds,
		"imagesPolicy":      p.ImagesPolicy,
		"conversationView":  p.ConversationView,
		"hoverActions":      p.HoverActions,
		"autoAdvance":       p.AutoAdvance,
		"density":           p.Density,
		"showSnippets":      p.ShowSnippets,
		"keyboardShortcuts": p.KeyboardShortcuts,
		// "String|null": null is "follow the browser". The empty string is not
		// used on the wire because "" is not a BCP 47 tag and a client would
		// have to guess what it meant; null is the JSON idiom for absence and
		// is what the /set path accepts back.
		"language":      prefsLanguageValue(p.Language),
		"readingPane":   p.ReadingPane,
		"inboxType":     p.InboxType,
		"notifications": p.Notifications,
		"theme":         p.Theme,

		// v2. The three structured properties are rendered as objects rather
		// than flattened into dotted scalars, because §5.3's PatchObject
		// addresses nested values with JSON Pointers — "labels/Facturas" — and
		// a flattened wire shape would have no pointer to address.
		"labels":               prefsLabelsValue(p.Labels),
		"offlineDepth":         prefsOfflineDepthValue(p.OfflineDepth),
		"addressAutocomplete":  p.AddressAutocomplete,
		"sendAndArchive":       p.SendAndArchive,
		"defaultReplyBehavior": p.DefaultReplyBehavior,
		"signatures":           prefsSignaturesValue(p.Signatures),
	}
	if properties == nil {
		return full
	}
	out := map[string]any{"id": full["id"]}
	for _, name := range *properties {
		if v, ok := full[name]; ok {
			out[name] = v
		}
	}
	return out
}

// prefsLanguageValue renders the language tag, mapping "" onto JSON null.
func prefsLanguageValue(tag string) any {
	if tag == "" {
		return nil
	}
	return tag
}

// prefsLabelsValue renders the label-presentation map.
//
// An empty or nil map renders as `{}` and NOT as null, which is the one place
// this object's wire form deliberately differs from the stored one: the store
// omits an empty map (its `omitempty` tag) because a missing key and an empty
// map carry the same information, but a CLIENT reading `null` would have to
// decide whether to treat it as "no labels" or "unknown", and a client patching
// into it would have to create the container first. An always-present object is
// the shape a client can read and patch without a special case.
func prefsLabelsValue(labels map[string]LabelPrefsValue) map[string]any {
	out := make(map[string]any, len(labels))
	for name, l := range labels {
		out[name] = map[string]any{
			"color":      l.Color,
			"visibility": l.Visibility,
		}
	}
	return out
}

// prefsOfflineDepthValue renders the offline cache depth.
func prefsOfflineDepthValue(d OfflineDepthValue) map[string]any {
	return map[string]any{
		"headersPerMailbox": d.HeadersPerMailbox,
		"bodies":            d.Bodies,
	}
}

// prefsSignaturesValue renders the named-signature model.
//
// forNew and forReply are "String|null": null means "no named signature is
// selected", which is where the precedence rule (documented on
// store.Prefs.Signatures) falls back to the Identity's own signature. Null
// rather than "" for the same reason language uses it — "" is not a valid id,
// so a client would have to guess what it meant.
func prefsSignaturesValue(s SignaturePrefsValue) map[string]any {
	items := make(map[string]any, len(s.Items))
	for id, item := range s.Items {
		items[id] = map[string]any{
			"name":     item.Name,
			"textBody": item.TextBody,
			"htmlBody": item.HTMLBody,
		}
	}
	return map[string]any{
		"items":    items,
		"forNew":   prefsOptionalID(s.ForNew),
		"forReply": prefsOptionalID(s.ForReply),
	}
}

// prefsOptionalID renders an id reference, mapping "" onto JSON null.
func prefsOptionalID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

// ---------------------------------------------------------------------------
// Prefs/changes
// ---------------------------------------------------------------------------

// handlePrefsChanges implements the standard /changes method (RFC 8620 §5.2)
// over the singleton.
//
// The answer is one of three, and all three are exact rather than approximate
// — which is why this method is implemented instead of declined:
//
//	the account has no row yet          -> nothing changed (created/updated
//	                                       empty). It cannot have: the object
//	                                       has never been written.
//	the row's watermark is after the
//	  client's cursor                   -> the singleton is UPDATED, or CREATED
//	                                       if the cursor predates its creation.
//	otherwise                           -> nothing changed.
//
// created vs updated is a real distinction here, unlike Identity/changes where
// created_at is not carried on the row: a cursor whose count term is 0 was
// taken before the singleton existed, so the object is genuinely new to that
// client and §5.2's created array is where it belongs. A client that only
// handles `updated` still refetches, which is the same repair.
//
// destroyed is always empty and always will be: the singleton cannot be
// destroyed (Prefs/set refuses it — see handlePrefsSet), so there is no state
// in which it could appear.
//
// maxChanges needs no honoring beyond validation: one object never exceeds a
// positive limit, so hasMoreChanges is always false. §5.2 permits exactly this
// ("if false, the server has returned all the changes").
func (d *Deps) handlePrefsChanges(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseChanges(ctx, args)
	if merr != nil {
		return nil, merr
	}
	since, merr := cursorFromState(req.SinceState)
	if merr != nil {
		return nil, merr
	}
	// The cursor's count term says whether the client's state was taken before
	// the singleton existed, which is what separates created from updated.
	existedAtCursor := prefsCursorSawTheObject(req.SinceState)

	rec, err := d.Prefs.GetPrefs(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading preferences", err)
	}
	state, err := d.Prefs.PrefsState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the preference state", err)
	}

	resp := newChangesResponse(req.AccountID, req.SinceState)
	resp.NewState = state
	// Strictly after, matching every other /changes here: the cursor a client
	// holds IS the watermark of what it already saw, so including that instant
	// again would replay the last change on every poll.
	if rec.Exists && rec.UpdatedAt.After(since) {
		if existedAtCursor {
			resp.Updated = append(resp.Updated, prefsID)
		} else {
			resp.Created = append(resp.Created, prefsID)
		}
	}
	return resp, nil
}

// prefsCursorSawTheObject reads the count term of a state string issued by
// stateFor ("<nanos>-<count>"): a non-zero count means the singleton already
// existed when the client took its cursor.
//
// A string this server did not issue never reaches here — cursorFromState has
// already answered cannotCalculateChanges for it — so the only parse failure
// possible is a count term this function cannot read, which is treated as
// "the object existed". That is the conservative direction: reporting an
// update for an object the client may already know costs one refetch, whereas
// reporting a creation for an object it already has could make a naive client
// duplicate it in a list.
func prefsCursorSawTheObject(state string) bool {
	_, count, ok := strings.Cut(state, "-")
	if !ok {
		return true
	}
	return strings.TrimSpace(count) != "0"
}

// ---------------------------------------------------------------------------
// Prefs/set
// ---------------------------------------------------------------------------

// handlePrefsSet implements the standard /set method (RFC 8620 §5.3) over the
// singleton.
//
// The three sub-operations, against §5.3:
//
//	create  -> refused with `forbidden` for every creation id. The singleton
//	           already exists by definition — GetPrefs materializes defaults
//	           for an account that has never saved one — so "create" names an
//	           object that cannot be brought into being twice. §5.3 lists
//	           forbidden among the SetError types for exactly this class of
//	           refusal, and it is the same answer RFC 8621 §8's VacationResponse
//	           implies for its own singleton.
//	update  -> the real operation. A PatchObject naming "singleton" is applied
//	           over the CURRENT object, validated as a whole, and stored whole.
//	destroy -> refused with `forbidden`. Preferences have no null state: an
//	           account always has an effective configuration, and "destroy"
//	           would at best mean "reset to defaults", which is an update
//	           naming every property and should be spelled that way.
//
// Idempotence is a property of the read-patch-write shape rather than an extra
// check: applying the same patch twice produces the same stored object, and
// the second application reports the same result. The state string still
// advances (PutPrefs always moves updated_at), which is the deliberate choice
// documented on the store method — an extra refresh in the user's other tabs
// is harmless; a save that fails to notify them is a stale settings screen.
func (d *Deps) handlePrefsSet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseSet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}

	oldState, err := d.Prefs.PrefsState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the preference state", err)
	}
	// §5.3 ifInState: "If supplied, the string must match the current state of
	// the account [...] otherwise, the method will be aborted and a
	// 'stateMismatch' error returned."
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, jmap.NewMethodError(jmap.CodeStateMismatch).
			WithDescription("the preference state has changed since the given ifInState")
	}

	resp := &setResponse{AccountID: req.AccountID, OldState: oldState}

	for creationID := range req.Create {
		if resp.NotCreated == nil {
			resp.NotCreated = map[string]setError{}
		}
		resp.NotCreated[creationID] = setError{
			Type: setErrForbidden,
			Description: "Prefs is a singleton and always exists: it cannot be created. " +
				`Update the object with id "` + prefsID + `" instead.`,
		}
	}

	for _, id := range req.Destroy {
		if resp.NotDestroyed == nil {
			resp.NotDestroyed = map[string]setError{}
		}
		if id != prefsID {
			resp.NotDestroyed[id] = setError{Type: setErrNotFound,
				Description: `the only Prefs object is "` + prefsID + `"`}
			continue
		}
		resp.NotDestroyed[id] = setError{
			Type: setErrForbidden,
			Description: "Prefs is a singleton and cannot be destroyed: an account always has an " +
				"effective configuration. To restore the defaults, update every property to its default value.",
		}
	}

	// The update. Reading once outside the loop is safe because the loop can
	// contain at most one applicable entry — any other id is notFound — and it
	// keeps a batch naming "singleton" twice from reading twice.
	if len(req.Update) > 0 {
		current, err := d.Prefs.GetPrefs(ctx, caller.AccountID)
		if err != nil {
			return nil, serverFail("reading preferences", err)
		}
		for id, patchRaw := range req.Update {
			if id != prefsID {
				setNotUpdated(resp, id, setError{Type: setErrNotFound,
					Description: `the only Prefs object is "` + prefsID + `"`})
				continue
			}
			next, serr := applyPrefsPatch(current.Prefs, patchRaw)
			if serr != nil {
				setNotUpdated(resp, id, *serr)
				continue
			}
			stored, err := d.Prefs.PutPrefs(ctx, caller.AccountID, *next)
			if err != nil {
				setNotUpdated(resp, id, setError{Type: setErrServerFail,
					Description: "storing the preferences failed"})
				continue
			}
			if resp.Updated == nil {
				resp.Updated = map[string]any{}
			}
			// §5.3: the updated map carries "any properties that changed on
			// the server as a side effect", or "null if no properties changed
			// besides those set by the client". Nothing here is transformed on
			// the way in — every value is either accepted verbatim or refused
			// — so null is the truthful answer, and reporting the whole object
			// would falsely claim the server rewrote the client's input.
			resp.Updated[id] = nil
			current.Prefs = stored.Prefs
		}
	}

	newState, err := d.Prefs.PrefsState(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the preference state", err)
	}
	resp.NewState = newState
	return resp, nil
}

// applyPrefsPatch validates a §5.3 PatchObject against the current object and
// returns the whole updated value.
//
// Every rejection is collected before returning, because §5.3 asks for all of
// them at once: "The SetError object SHOULD also have a property called
// 'properties' [...] that lists ALL the properties that were invalid." A
// settings screen that has to submit thirteen times to discover thirteen
// mistakes is a settings screen nobody finishes using.
func applyPrefsPatch(current PrefsValue, raw json.RawMessage) (*PrefsValue, *setError) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, &setError{Type: setErrInvalidPatch,
			Description: "an update must be a PatchObject (RFC 8620 §5.3)"}
	}

	next := clonePrefsValue(current)
	var bad []string
	reasons := map[string]string{}
	fail := func(property, why string) {
		if _, seen := reasons[property]; !seen {
			bad = append(bad, property)
		}
		reasons[property] = why
	}

	// The v2 properties with internal structure are collected first and applied
	// after the loop, because a MAP can be addressed two ways in one patch —
	// `{"labels": {...}}` replaces it whole, `{"labels/Work": {...}}` edits one
	// entry — and the cap has to be checked against the RESULT of both, not
	// against each in isolation. A patch that removes twenty labels and adds
	// twenty must pass; checking per-key would refuse it at the first addition.
	//
	// Map iteration order is random in Go, so an in-loop application would also
	// make "whole replacement plus per-entry edit in the same patch" resolve
	// differently on different runs. Deferring makes the order defined: the
	// whole-value replacement lands first, then the per-entry edits apply on
	// top of it, which is the only reading under which a patch means one thing.
	labelEdits := map[string]json.RawMessage{}
	var labelsWhole json.RawMessage
	signatureEdits := map[string]json.RawMessage{}
	var signaturesWhole json.RawMessage
	offlineEdits := map[string]json.RawMessage{}
	var offlineWhole json.RawMessage

	for key, val := range fields {
		property, sub, hasSub, ok := splitPatchPointer(key)
		if !ok {
			// A pointer deeper than one sub-level. "signatures/items/work" is
			// the realistic case and it is genuinely not addressable here: §5.3
			// invalidPatch is the answer for a pointer that cannot apply.
			return nil, &setError{Type: setErrInvalidPatch,
				Description: fmt.Sprintf("%q is not a patchable path on a Prefs object", key)}
		}
		if hasSub {
			// Only the three structured v2 properties have anything a
			// sub-pointer could name. Every other property is a scalar, so
			// "theme/dark" cannot apply to the object at all.
			switch property {
			case "labels":
				labelEdits[sub] = val
			case "signatures":
				signatureEdits[sub] = val
			case "offlineDepth":
				offlineEdits[sub] = val
			default:
				return nil, &setError{Type: setErrInvalidPatch,
					Description: fmt.Sprintf("%q is not a patchable path on a Prefs object: "+
						"%q has no nested properties", key, property)}
			}
			continue
		}

		switch property {
		case "undoSendSeconds":
			n, ok := prefsPatchInt(val)
			if !ok {
				fail(property, "undoSendSeconds must be a number")
				continue
			}
			if !prefsAllowedInt(n, undoSendSecondsChoices) {
				fail(property, fmt.Sprintf("undoSendSeconds must be one of %s (the values Gmail offers)",
					prefsJoinInts(undoSendSecondsChoices)))
				continue
			}
			next.UndoSendSeconds = n

		case "imagesPolicy":
			prefsPatchEnum(val, imagesPolicyChoices, property, &next.ImagesPolicy, fail)
		case "autoAdvance":
			prefsPatchEnum(val, autoAdvanceChoices, property, &next.AutoAdvance, fail)
		case "density":
			prefsPatchEnum(val, densityChoices, property, &next.Density, fail)
		case "readingPane":
			prefsPatchEnum(val, readingPaneChoices, property, &next.ReadingPane, fail)
		case "inboxType":
			prefsPatchEnum(val, inboxTypeChoices, property, &next.InboxType, fail)
		case "notifications":
			prefsPatchEnum(val, notificationsChoices, property, &next.Notifications, fail)
		case "theme":
			prefsPatchEnum(val, themeChoices, property, &next.Theme, fail)

		case "conversationView":
			prefsPatchBool(val, property, &next.ConversationView, fail)
		case "hoverActions":
			prefsPatchBool(val, property, &next.HoverActions, fail)
		case "showSnippets":
			prefsPatchBool(val, property, &next.ShowSnippets, fail)
		case "keyboardShortcuts":
			prefsPatchBool(val, property, &next.KeyboardShortcuts, fail)

		case "language":
			// "String|null": null is "follow the browser", which the stored
			// form spells as "". §5.3 also gives null the meaning "set to the
			// default value if specified", and here the two coincide — the
			// default IS auto — so there is no ambiguity to resolve.
			if prefsIsNull(val) {
				next.Language = ""
				continue
			}
			var tag string
			if err := json.Unmarshal(val, &tag); err != nil {
				fail(property, "language must be a string or null")
				continue
			}
			tag = strings.TrimSpace(tag)
			if tag == "" {
				// An empty string is not a language tag. It is accepted as a
				// spelling of null rather than refused, because a client that
				// clears a text field naturally produces "" and refusing it
				// would make "clear the setting" fail for no user-visible
				// reason.
				next.Language = ""
				continue
			}
			if !validLanguageTag(tag) {
				fail(property, "language must be a BCP 47 tag such as \"en\" or \"es-AR\", or null to follow the browser")
				continue
			}
			next.Language = tag

		// --- v2 ---

		case "addressAutocomplete":
			prefsPatchEnum(val, addressAutocompleteChoices, property, &next.AddressAutocomplete, fail)
		case "defaultReplyBehavior":
			prefsPatchEnum(val, defaultReplyBehaviorChoices, property, &next.DefaultReplyBehavior, fail)
		case "sendAndArchive":
			prefsPatchBool(val, property, &next.SendAndArchive, fail)

		case "labels", "signatures", "offlineDepth":
			// Whole-value replacement, applied after the loop so it can be
			// composed with any per-entry edits in the same patch.
			switch property {
			case "labels":
				labelsWhole = val
			case "signatures":
				signaturesWhole = val
			default:
				offlineWhole = val
			}

		case "id":
			// §5.3: an immutable/server-set property named in an update "MUST
			// be rejected with an 'invalidProperties' SetError".
			fail(property, `id is server-set: the Prefs object is the singleton "`+prefsID+`"`)

		default:
			// §5.3: "any property [...] that is not a valid property of the
			// object" is the invalidProperties condition. Refusing rather than
			// ignoring is the point — a silently dropped preference is a
			// control the user watched move and that changed nothing.
			fail(property, fmt.Sprintf("%q is not a property of a Prefs object", property))
		}
	}

	// The structured properties, in the defined order: whole-value replacement
	// first, then the per-entry edits on top of it.
	applyLabelsPatch(&next, labelsWhole, labelEdits, fail)
	applySignaturesPatch(&next, signaturesWhole, signatureEdits, fail)
	applyOfflineDepthPatch(&next, offlineWhole, offlineEdits, fail)

	if len(bad) > 0 {
		sort.Strings(bad)
		details := make([]string, 0, len(bad))
		for _, property := range bad {
			details = append(details, reasons[property])
		}
		return nil, &setError{Type: setErrInvalidProperties, Properties: bad,
			Description: strings.Join(details, "; ")}
	}
	return &next, nil
}

// ---------------------------------------------------------------------------
// the v2 structured properties
// ---------------------------------------------------------------------------

// clonePrefsValue deep-copies a preference value so a patch can be applied to
// the maps in place without editing the object it was read from.
func clonePrefsValue(p PrefsValue) PrefsValue {
	out := p
	if p.Labels != nil {
		out.Labels = make(map[string]LabelPrefsValue, len(p.Labels))
		for k, v := range p.Labels {
			out.Labels[k] = v
		}
	}
	if p.Signatures.Items != nil {
		out.Signatures.Items = make(map[string]SignatureItemValue, len(p.Signatures.Items))
		for k, v := range p.Signatures.Items {
			out.Signatures.Items[k] = v
		}
	}
	return out
}

// applyLabelsPatch applies a whole-map replacement and/or per-label edits.
//
// The three shapes a client can send, and what each means:
//
//	{"labels": {...}}            replace the whole map.
//	{"labels": null}             §5.3's "set to the default value if specified"
//	                             — the default is no custom presentation, so
//	                             this clears it.
//	{"labels/Work": {...}}       set one label's metadata.
//	{"labels/Work": null}        §5.3's "otherwise remove the property" —
//	                             remove that label's metadata, which returns it
//	                             to the default swatch, visible. It does NOT
//	                             delete the label: the label is an IMAP keyword
//	                             (A6) and this map holds only presentation.
//
// The cap is checked ONCE, on the result, for the reason applyPrefsPatch
// states: a patch that removes twenty labels and adds twenty is legal and a
// per-key check would refuse it at the first addition.
func applyLabelsPatch(next *PrefsValue, whole json.RawMessage, edits map[string]json.RawMessage, fail func(string, string)) {
	if whole == nil && len(edits) == 0 {
		return
	}

	if whole != nil {
		if prefsIsNull(whole) {
			next.Labels = nil
		} else {
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(whole, &raw); err != nil {
				fail("labels", "labels must be an object keyed by label name, or null")
				return
			}
			replacement := make(map[string]LabelPrefsValue, len(raw))
			for name, val := range raw {
				l, why := parseLabelPrefs(name, val)
				if why != "" {
					fail("labels", why)
					return
				}
				replacement[name] = *l
			}
			next.Labels = replacement
		}
	}

	for name, val := range edits {
		if prefsIsNull(val) {
			delete(next.Labels, name)
			continue
		}
		l, why := parseLabelPrefs(name, val)
		if why != "" {
			// The property named in the error is the POINTER the client sent,
			// not the bare "labels": §5.3's invalidProperties list is what a
			// settings screen highlights, and highlighting "labels" when one
			// label of twenty is wrong tells the user nothing.
			fail("labels/"+name, why)
			continue
		}
		if next.Labels == nil {
			next.Labels = map[string]LabelPrefsValue{}
		}
		next.Labels[name] = *l
	}

	if len(next.Labels) > maxLabelPrefs {
		fail("labels", fmt.Sprintf(
			"at most %d labels can carry presentation metadata (the durable IMAP keyword ceiling: "+
				"Maildir encodes a keyword as one letter a-z in the filename, so a %dth label cannot "+
				"survive an index rebuild); the patch would leave %d",
			maxLabelPrefs, maxLabelPrefs+1, len(next.Labels)))
	}
	if len(next.Labels) == 0 {
		// Normalize empty to nil, so the stored form has one spelling for
		// "nothing customized" and a round trip cannot change the value.
		next.Labels = nil
	}
}

// parseLabelPrefs validates one label's metadata. It returns a reason string
// (empty when valid) rather than an error, because the caller composes it into
// §5.3's per-property description.
func parseLabelPrefs(name string, raw json.RawMessage) (*LabelPrefsValue, string) {
	if strings.TrimSpace(name) == "" {
		return nil, "a label name cannot be empty"
	}
	if len(name) > maxLabelNameBytes {
		return nil, fmt.Sprintf("a label name is at most %d bytes", maxLabelNameBytes)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Sprintf("the metadata for label %q must be an object with color and visibility", name)
	}
	// Unknown NESTED keys are refused for the same reason unknown top-level
	// ones are: a silently dropped key is a control the user watched move that
	// changed nothing.
	for key := range fields {
		if key != "color" && key != "visibility" {
			return nil, fmt.Sprintf("%q is not a property of a label: expected color and visibility", key)
		}
	}

	out := LabelPrefsValue{}
	color, ok := fields["color"]
	if !ok {
		return nil, fmt.Sprintf("the metadata for label %q must name a color", name)
	}
	var colorID string
	if err := json.Unmarshal(color, &colorID); err != nil {
		return nil, "a label color must be a string"
	}
	if !prefsAllowedString(colorID, labelColorChoices) {
		return nil, fmt.Sprintf("%q is not a palette color: one of %s "+
			"(the palette is closed and stores ids, not hex, so a contrast fix reaches every existing label)",
			colorID, prefsJoinStrings(labelColorChoices))
	}
	out.Color = colorID

	visibility, ok := fields["visibility"]
	if !ok {
		return nil, fmt.Sprintf("the metadata for label %q must name a visibility", name)
	}
	var vis string
	if err := json.Unmarshal(visibility, &vis); err != nil {
		return nil, "a label visibility must be a string"
	}
	if !prefsAllowedString(vis, labelVisibilityChoices) {
		return nil, fmt.Sprintf("%q is not a label visibility: one of %s",
			vis, prefsJoinStrings(labelVisibilityChoices))
	}
	out.Visibility = vis
	return &out, ""
}

// maxLabelNameBytes caps a label NAME used as a map key.
//
// It is not the label's authoritative length limit — the label lives in an IMAP
// keyword and Dovecot has its own opinion — but a key in this map must not be
// a place to store content, and an unbounded key in a JSONB document is exactly
// that.
const maxLabelNameBytes = 256

// applySignaturesPatch applies a whole-value replacement and/or per-key edits
// to the named-signature model.
//
// The addressable sub-keys are the three properties of the object — "items",
// "forNew", "forReply" — and NOT individual signature ids: "signatures/items"
// replaces the whole item map, while "signatures/items/work" is a two-level
// pointer that splitPatchPointer already refused as invalidPatch. That is a
// real limitation and it is accepted: RFC 8620 §5.3's pointers are one level
// deep in every other type here, and a client that wants to change one
// signature sends the whole items map, which is a few kilobytes.
func applySignaturesPatch(next *PrefsValue, whole json.RawMessage, edits map[string]json.RawMessage, fail func(string, string)) {
	if whole == nil && len(edits) == 0 {
		return
	}

	if whole != nil {
		if prefsIsNull(whole) {
			next.Signatures = SignaturePrefsValue{}
		} else {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(whole, &fields); err != nil {
				fail("signatures", "signatures must be an object with items, forNew and forReply, or null")
				return
			}
			for key := range fields {
				if key != "items" && key != "forNew" && key != "forReply" {
					fail("signatures", fmt.Sprintf(
						"%q is not a property of signatures: expected items, forNew and forReply", key))
					return
				}
			}
			replacement := SignaturePrefsValue{}
			if items, ok := fields["items"]; ok {
				parsed, why := parseSignatureItems(items)
				if why != "" {
					fail("signatures/items", why)
					return
				}
				replacement.Items = parsed
			}
			if v, ok := fields["forNew"]; ok {
				id, why := parseSignatureRef(v, "forNew")
				if why != "" {
					fail("signatures/forNew", why)
					return
				}
				replacement.ForNew = id
			}
			if v, ok := fields["forReply"]; ok {
				id, why := parseSignatureRef(v, "forReply")
				if why != "" {
					fail("signatures/forReply", why)
					return
				}
				replacement.ForReply = id
			}
			next.Signatures = replacement
		}
	}

	for key, val := range edits {
		switch key {
		case "items":
			if prefsIsNull(val) {
				next.Signatures.Items = nil
				continue
			}
			parsed, why := parseSignatureItems(val)
			if why != "" {
				fail("signatures/items", why)
				continue
			}
			next.Signatures.Items = parsed
		case "forNew":
			id, why := parseSignatureRef(val, "forNew")
			if why != "" {
				fail("signatures/forNew", why)
				continue
			}
			next.Signatures.ForNew = id
		case "forReply":
			id, why := parseSignatureRef(val, "forReply")
			if why != "" {
				fail("signatures/forReply", why)
				continue
			}
			next.Signatures.ForReply = id
		default:
			fail("signatures/"+key, fmt.Sprintf(
				"%q is not a property of signatures: expected items, forNew and forReply", key))
		}
	}

	// REFERENTIAL INTEGRITY, checked on the RESULT rather than per-key: forNew
	// and items can arrive in the same patch, in either order, and a check that
	// ran while one of them was still the old value would refuse a legal patch
	// that creates a signature and selects it at once.
	//
	// A dangling reference is refused rather than silently cleared because the
	// fallback it would silently produce — the Identity's own signature — is a
	// DIFFERENT signature going out under the user's name, which is exactly the
	// class of silent substitution a settings screen must never do.
	for property, id := range map[string]string{
		"signatures/forNew":   next.Signatures.ForNew,
		"signatures/forReply": next.Signatures.ForReply,
	} {
		if id == "" {
			continue
		}
		if _, ok := next.Signatures.Items[id]; !ok {
			fail(property, fmt.Sprintf(
				"%q names no signature: set it to null to fall back to the identity's own signature", id))
		}
	}

	if len(next.Signatures.Items) == 0 {
		next.Signatures.Items = nil
	}
}

// parseSignatureItems validates the whole item map, applying the count, name,
// id and byte caps — and SANITIZING each htmlBody through the same
// sanitizeHTMLSignature the per-identity htmlSignature goes through.
func parseSignatureItems(raw json.RawMessage) (map[string]SignatureItemValue, string) {
	var items map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, "signatures.items must be an object keyed by signature id"
	}
	if len(items) > maxSignatureItems {
		return nil, fmt.Sprintf("at most %d named signatures (a picker longer than that is a list "+
			"the user scrolls rather than scans); the patch names %d", maxSignatureItems, len(items))
	}

	out := make(map[string]SignatureItemValue, len(items))
	total := 0
	for id, val := range items {
		if strings.TrimSpace(id) == "" {
			return nil, "a signature id cannot be empty"
		}
		if len(id) > maxSignatureIDBytes {
			return nil, fmt.Sprintf("a signature id is at most %d bytes", maxSignatureIDBytes)
		}

		var fields map[string]json.RawMessage
		if err := json.Unmarshal(val, &fields); err != nil {
			return nil, fmt.Sprintf("signature %q must be an object with name, textBody and htmlBody", id)
		}
		for key := range fields {
			if key != "name" && key != "textBody" && key != "htmlBody" {
				return nil, fmt.Sprintf("%q is not a property of a signature: expected name, textBody and htmlBody", key)
			}
		}

		item := SignatureItemValue{}
		if v, ok := fields["name"]; ok {
			s, ok := patchString(v)
			if !ok {
				return nil, fmt.Sprintf("the name of signature %q must be a string", id)
			}
			if len(s) > maxSignatureNameBytes {
				return nil, fmt.Sprintf("the name of signature %q exceeds %d bytes", id, maxSignatureNameBytes)
			}
			item.Name = s
		}
		if v, ok := fields["textBody"]; ok {
			s, ok := patchString(v)
			if !ok {
				return nil, fmt.Sprintf("the textBody of signature %q must be a string", id)
			}
			if len(s) > maxSignatureBytes {
				return nil, fmt.Sprintf("the textBody of signature %q exceeds this server's %d-byte limit",
					id, maxSignatureBytes)
			}
			item.TextBody = s
		}
		if v, ok := fields["htmlBody"]; ok {
			s, ok := patchString(v)
			if !ok {
				return nil, fmt.Sprintf("the htmlBody of signature %q must be a string", id)
			}
			// The INPUT is measured, exactly as Identity/set measures it, so an
			// oversize signature is reported as the user's problem rather than
			// silently dropped by the sanitizer's own cap.
			if len(s) > maxSignatureBytes {
				return nil, fmt.Sprintf("the htmlBody of signature %q exceeds this server's %d-byte limit",
					id, maxSignatureBytes)
			}
			// Sanitized HERE, before storage, through the SAME function the
			// per-identity htmlSignature uses. signature.go documents why that
			// one string inverts the project's sanitize-on-render rule, and
			// every word of it applies identically to a named signature: it is
			// content Moov transmits under its own DKIM key, the database is
			// the only copy, and it is served back into a contenteditable.
			item.HTMLBody = sanitizeHTMLSignature(s)
		}
		total += len(item.Name) + len(item.TextBody) + len(item.HTMLBody)
		out[id] = item
	}

	if total > maxSignaturesBytes {
		return nil, fmt.Sprintf("the signatures total %d bytes, over this server's %d-byte limit "+
			"for the whole collection (each one may still be up to %d bytes)",
			total, maxSignaturesBytes, maxSignatureBytes)
	}
	return out, ""
}

// parseSignatureRef validates a forNew/forReply reference. Existence is checked
// by the caller against the RESULT of the whole patch.
func parseSignatureRef(raw json.RawMessage, property string) (string, string) {
	if prefsIsNull(raw) {
		return "", ""
	}
	var id string
	if err := json.Unmarshal(raw, &id); err != nil {
		return "", fmt.Sprintf("%s must be a signature id or null", property)
	}
	if strings.TrimSpace(id) == "" {
		// "" is accepted as a spelling of null for the same reason language's
		// empty string is: a client clearing a selection naturally produces it,
		// and refusing would make "no signature" fail for no visible reason.
		return "", ""
	}
	if len(id) > maxSignatureIDBytes {
		return "", fmt.Sprintf("%s is at most %d bytes", property, maxSignatureIDBytes)
	}
	return id, ""
}

// applyOfflineDepthPatch applies a whole-value replacement and/or per-key edits
// to the offline cache depth.
//
// Unlike the two maps, a whole replacement here does NOT reset the unnamed key
// to its default: {"offlineDepth": {"bodies": 50}} keeps the current
// headersPerMailbox. The object has exactly two members and they are
// independent settings, so the read a user expects from naming one is "change
// that one" — the same read every scalar property of this object gets.
func applyOfflineDepthPatch(next *PrefsValue, whole json.RawMessage, edits map[string]json.RawMessage, fail func(string, string)) {
	if whole == nil && len(edits) == 0 {
		return
	}

	if whole != nil {
		if prefsIsNull(whole) {
			next.OfflineDepth = DefaultPrefsValue().OfflineDepth
		} else {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(whole, &fields); err != nil {
				fail("offlineDepth", "offlineDepth must be an object with headersPerMailbox and bodies, or null")
				return
			}
			for key, val := range fields {
				switch key {
				case "headersPerMailbox", "bodies":
					// Merged into the per-key edits so the two spellings take
					// exactly one code path and cannot validate differently.
					if _, already := edits[key]; !already {
						edits[key] = val
					}
				default:
					fail("offlineDepth", fmt.Sprintf(
						"%q is not a property of offlineDepth: expected headersPerMailbox and bodies", key))
					return
				}
			}
		}
	}

	for key, val := range edits {
		switch key {
		case "headersPerMailbox":
			prefsPatchBoundedInt(val, "offlineDepth/headersPerMailbox",
				minOfflineHeaders, maxOfflineHeaders, &next.OfflineDepth.HeadersPerMailbox, fail)
		case "bodies":
			prefsPatchBoundedInt(val, "offlineDepth/bodies",
				minOfflineBodies, maxOfflineBodies, &next.OfflineDepth.Bodies, fail)
		default:
			fail("offlineDepth/"+key, fmt.Sprintf(
				"%q is not a property of offlineDepth: expected headersPerMailbox and bodies", key))
		}
	}
}

// prefsPatchBoundedInt reads an integer property constrained to an inclusive
// range, reporting the range in the refusal so a client can show it.
func prefsPatchBoundedInt(raw json.RawMessage, property string, low, high int, dst *int, fail func(string, string)) {
	n, ok := prefsPatchInt(raw)
	if !ok {
		fail(property, fmt.Sprintf("%s must be a whole number between %d and %d", property, low, high))
		return
	}
	if n < low || n > high {
		fail(property, fmt.Sprintf("%s must be between %d and %d, not %d", property, low, high, n))
		return
	}
	*dst = n
}

// prefsAllowedString reports membership in a closed domain.
func prefsAllowedString(v string, choices []string) bool {
	for _, c := range choices {
		if v == c {
			return true
		}
	}
	return false
}

// prefsPatchEnum reads a closed-domain string property.
func prefsPatchEnum(raw json.RawMessage, choices []string, property string, dst *string, fail func(string, string)) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		fail(property, fmt.Sprintf("%s must be a string, one of %s", property, prefsJoinStrings(choices)))
		return
	}
	for _, c := range choices {
		if s == c {
			*dst = s
			return
		}
	}
	fail(property, fmt.Sprintf("%s must be one of %s", property, prefsJoinStrings(choices)))
}

// prefsPatchBool reads a boolean property.
func prefsPatchBool(raw json.RawMessage, property string, dst *bool, fail func(string, string)) {
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		fail(property, property+" must be true or false")
		return
	}
	*dst = b
}

// prefsPatchInt reads a JSON number as an integer, refusing a fractional one.
//
// json.Unmarshal into an int already rejects "10.5", but it ACCEPTS "1e1" as
// 10; going through json.Number keeps the accepted spellings to the ones a
// client would actually send and makes the refusal explicit rather than
// dependent on encoding/json's coercion rules.
func prefsPatchInt(raw json.RawMessage) (int, bool) {
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, false
	}
	v, err := n.Int64()
	if err != nil {
		return 0, false
	}
	// The domain check that follows only accepts members of a small set of
	// small integers, so this conversion cannot lose information for any value
	// that will be stored; the bound is checked here anyway so an absurd input
	// is refused as a value error rather than wrapping.
	if v < 0 || v > 1<<31-1 {
		return 0, false
	}
	return int(v), true
}

// prefsIsNull reports whether a patch value is JSON null.
func prefsIsNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

func prefsAllowedInt(v int, choices []int) bool {
	for _, c := range choices {
		if v == c {
			return true
		}
	}
	return false
}

func prefsJoinStrings(choices []string) string {
	quoted := make([]string, 0, len(choices))
	for _, c := range choices {
		quoted = append(quoted, `"`+c+`"`)
	}
	return strings.Join(quoted, ", ")
}

func prefsJoinInts(choices []int) string {
	out := make([]string, 0, len(choices))
	for _, c := range choices {
		out = append(out, fmt.Sprint(c))
	}
	return strings.Join(out, ", ")
}

// validLanguageTag applies a deliberately SHALLOW BCP 47 check: one or more
// subtags of ASCII letters and digits, separated by hyphens, the first being
// 2-8 letters, and at most 64 characters overall.
//
// It is not a registry lookup and does not pretend to be. A full RFC 5646
// validation would need the IANA subtag registry — a data file to vendor and
// keep current — to reject strings like "xx-YY" that are well-formed but
// unassigned. The value is used to pick a UI translation, so the real failure
// mode of an unassigned-but-well-formed tag is "falls back to English", which
// is the same thing an unsupported-but-assigned tag does. What this check DOES
// buy is that the column cannot hold a sentence, a script, or a path: the
// shape is constrained even though the vocabulary is not.
func validLanguageTag(tag string) bool {
	if len(tag) == 0 || len(tag) > 64 {
		return false
	}
	for i, part := range strings.Split(tag, "-") {
		if len(part) == 0 || len(part) > 8 {
			return false
		}
		for _, r := range part {
			isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
			isDigit := r >= '0' && r <= '9'
			if !isLetter && !isDigit {
				return false
			}
		}
		if i == 0 {
			// The primary language subtag is letters only, 2-8 of them
			// (RFC 5646 §2.2.1; the 1-character singletons are private-use and
			// extension prefixes, never a primary language).
			if len(part) < 2 {
				return false
			}
			for _, r := range part {
				if r >= '0' && r <= '9' {
					return false
				}
			}
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// the send path
// ---------------------------------------------------------------------------

// prefsUndoWindow renders a stored undoSendSeconds as the duration the
// submission path applies, clamped to the contract [MinUndoWindow,
// MaxUndoWindow] that the config and the outbox already share.
//
// The clamp is applied here even though the /set validator already restricts
// the value to Gmail's four choices, and the redundancy is deliberate: this
// function is what the SEND path calls, and its correctness must not depend on
// every write having gone through the current validator. A row written by an
// older build, by a future one, or by an operator's UPDATE still produces a
// window the outbox can honor. clampUndoWindow is the single implementation of
// that contract (submission.go), so there is one clamp, used twice.
func prefsUndoWindow(seconds int) time.Duration {
	if seconds <= 0 {
		return DefaultUndoWindow
	}
	return clampUndoWindow(time.Duration(seconds) * time.Second)
}
