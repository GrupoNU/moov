package mail

import (
	"reflect"
	"testing"

	"github.com/GrupoNU/moov/internal/store"
)

// The store <-> JMAP preference mapping (prefs_adapter.go), and the agreement
// between the value domains this package ENFORCES and the defaults the store
// ships.
//
// These are the checks the compiler cannot make. Both types are plain structs
// of the same shape, so a field added to one and forgotten in the other
// compiles perfectly and simply loses the user's setting on every save.

// TestPrefsMappingCoversEveryField walks the two structs by reflection and
// proves they carry the same fields, then proves every one survives a round
// trip with a distinct value.
//
// The reflection is what makes it a real guard: an assertion written by hand
// would have to be remembered at the same moment the mapping would have to be,
// which is exactly the moment it is forgotten.
func TestPrefsMappingCoversEveryField(t *testing.T) {
	jmapType := reflect.TypeOf(PrefsValue{})
	storeType := reflect.TypeOf(store.Prefs{})

	if jmapType.NumField() != storeType.NumField() {
		t.Fatalf("PrefsValue has %d fields, store.Prefs has %d: a preference was added to one side only",
			jmapType.NumField(), storeType.NumField())
	}
	for i := range jmapType.NumField() {
		want := jmapType.Field(i)
		got, ok := storeType.FieldByName(want.Name)
		if !ok {
			t.Errorf("store.Prefs has no field %s", want.Name)
			continue
		}
		if !sameShape(got.Type, want.Type) {
			t.Errorf("field %s is %s here and %s in the store: the two are not the same shape",
				want.Name, want.Type, got.Type)
		}
	}

	// A round trip with every field distinct from every other, so a mapping
	// that crossed two fields of the same type is caught too.
	in := store.Prefs{
		UndoSendSeconds:   20,
		ImagesPolicy:      "ask",
		ConversationView:  false,
		HoverActions:      true,
		AutoAdvance:       "older",
		Density:           "compact",
		ShowSnippets:      false,
		KeyboardShortcuts: true,
		Language:          "es-AR",
		ReadingPane:       "bottom",
		InboxType:         "starred_first",
		Notifications:     "off",
		Theme:             "system",

		// v2. Populated, because a map whose mapping is only exercised empty
		// is a mapping that would pass while dropping every entry.
		Labels: map[string]store.LabelPrefs{
			"Facturas": {Color: "amber", Visibility: "showIfUnread"},
			"Equipo":   {Color: "teal", Visibility: "hide"},
		},
		OfflineDepth:         store.OfflineDepthPrefs{HeadersPerMailbox: 300, Bodies: 60},
		AddressAutocomplete:  "manual",
		SendAndArchive:       false,
		DefaultReplyBehavior: "replyAll",
		Signatures: store.SignaturePrefs{
			Items: map[string]store.SignatureItem{
				"work": {Name: "Work", TextBody: "-- \nD", HTMLBody: "<p>D</p>"},
			},
			ForNew:   "work",
			ForReply: "work",
		},

		// v3. Populated for the same reason the v2 maps are, and with a key
		// carrying a slash because that is the shape a real Dovecot folder name
		// takes and the shape a naive mapping would be tempted to split.
		FolderVisibility: map[string]string{
			"Archivo":               "hide",
			"Sync issues/Conflicts": "showIfUnread",
		},
	}
	got := storePrefs(prefsValue(in))
	if !got.Equal(in) {
		t.Errorf("round trip changed the value:\n in: %+v\nout: %+v", in, got)
	}
	// The nested values specifically: Equal walks the maps, but a mapping that
	// carried the KEYS and dropped a struct field would need this to be caught.
	if l := got.Labels["Facturas"]; l.Color != "amber" || l.Visibility != "showIfUnread" {
		t.Errorf("a label lost a field in the mapping: %+v", l)
	}
	if s := got.Signatures.Items["work"]; s.Name != "Work" || s.TextBody != "-- \nD" || s.HTMLBody != "<p>D</p>" {
		t.Errorf("a signature lost a field in the mapping: %+v", s)
	}
	if got.FolderVisibility["Sync issues/Conflicts"] != "showIfUnread" {
		t.Errorf("a folder name containing a slash did not survive the mapping: %v", got.FolderVisibility)
	}
}

// TestPrefsMappingDoesNotAliasTheMaps pins that the two layers hold SEPARATE
// maps. An aliased map would let the JMAP layer's read-patch-write edit the
// value it was only supposed to be reading from — and the bug would be
// invisible until two requests raced.
func TestPrefsMappingDoesNotAliasTheMaps(t *testing.T) {
	src := store.Prefs{
		Labels:           map[string]store.LabelPrefs{"a": {Color: "red", Visibility: "show"}},
		Signatures:       store.SignaturePrefs{Items: map[string]store.SignatureItem{"s": {Name: "S"}}},
		FolderVisibility: map[string]string{"Archivo": "hide"},
	}

	mapped := prefsValue(src)
	mapped.Labels["a"] = LabelPrefsValue{Color: "blue", Visibility: "hide"}
	mapped.Signatures.Items["s"] = SignatureItemValue{Name: "TAMPERED"}
	mapped.FolderVisibility["Archivo"] = "show"

	if src.Labels["a"].Color != "red" {
		t.Error("prefsValue aliases the store's label map: mutating the JMAP value changed the store's")
	}
	if src.Signatures.Items["s"].Name != "S" {
		t.Error("prefsValue aliases the store's signature map")
	}
	if src.FolderVisibility["Archivo"] != "hide" {
		t.Error("prefsValue aliases the store's folder map")
	}

	back := storePrefs(mapped)
	back.Labels["a"] = store.LabelPrefs{Color: "lime", Visibility: "show"}
	if mapped.Labels["a"].Color != "blue" {
		t.Error("storePrefs aliases the JMAP value's label map")
	}

	// A nil map must stay nil rather than becoming empty, in both directions:
	// nil is the canonical "nothing customized" and the encoder omits it.
	if prefsValue(store.Prefs{}).Labels != nil {
		t.Error("prefsValue turned a nil label map into an empty one")
	}
	if storePrefs(PrefsValue{}).Signatures.Items != nil {
		t.Error("storePrefs turned a nil signature map into an empty one")
	}
	if prefsValue(store.Prefs{}).FolderVisibility != nil {
		t.Error("prefsValue turned a nil folder map into an empty one")
	}
	if storePrefs(PrefsValue{}).FolderVisibility != nil {
		t.Error("storePrefs turned a nil folder map into an empty one")
	}
}

// TestFolderVisibilityShareTheLabelVocabulary pins the coincidence the two
// domains are allowed to have and must not be assumed to keep: folder rail and
// label list offer the SAME three visibilities today, because Gmail gives its
// user one vocabulary for both and a user who learned it on one surface must
// not meet a different one on the other.
//
// The domains are separate variables on purpose (a folder is a mailbox, a label
// is an IMAP keyword — arbitrage A6), so this test is what turns a future
// divergence into a deliberate edit here rather than a silent inconsistency a
// user meets in the settings screen.
func TestFolderVisibilityShareTheLabelVocabulary(t *testing.T) {
	folder := FolderVisibilityChoices()
	label := LabelVisibilityChoices()
	if len(folder) != len(label) {
		t.Fatalf("folder visibilities %v and label visibilities %v differ in size", folder, label)
	}
	for _, v := range label {
		if !prefsAllowedString(v, folderVisibilityChoices) {
			t.Errorf("the label list offers %q but the folder rail does not: "+
				"one vocabulary, two surfaces", v)
		}
	}
	for _, v := range folder {
		if !prefsAllowedString(v, labelVisibilityChoices) {
			t.Errorf("the folder rail offers %q but the label list does not", v)
		}
	}
	// The accessor must return a COPY, like every other advertised domain: the
	// session builder calls it per request, and a shared slice would let one
	// caller change what the server enforces for every subsequent one.
	folder[0] = "tampered"
	if folderVisibilityChoices[0] == "tampered" {
		t.Error("FolderVisibilityChoices exposes the package's own slice")
	}
}

// TestPrefsLabelCapIsTheKeywordCeiling pins the duplication this package
// accepted deliberately: maxLabelPrefs is internal/imap's
// MaxDurableKeywordsPerMailbox, copied because the protocol layer must not
// import a transport package.
//
// The constant is re-derived here from its documented value rather than
// imported, so this test fails if either side moves — which is the only thing
// keeping the copy honest.
func TestPrefsLabelCapIsTheKeywordCeiling(t *testing.T) {
	// internal/imap/metadata.go:52 — MaxDurableKeywordsPerMailbox = 26, a
	// Maildir fact: a keyword is one letter a-z in the filename, and
	// dovecot-keywords stops at index 25.
	const durableKeywordCeiling = 26
	if maxLabelPrefs != durableKeywordCeiling {
		t.Errorf("maxLabelPrefs = %d, want the durable keyword ceiling %d "+
			"(internal/imap.MaxDurableKeywordsPerMailbox): presentation metadata for a label "+
			"that cannot durably exist is dead weight", maxLabelPrefs, durableKeywordCeiling)
	}
}

// TestPrefsPaletteMirrorsTheClient pins the OTHER accepted duplication: the
// twelve palette ids are written in Go here and in TypeScript in
// web/src/mail/labelPalette.ts, across a boundary no compiler spans.
//
// The list below is transcribed from that file. It cannot detect a change made
// to BOTH sides — nothing can, short of codegen — but it catches the realistic
// failure, which is one side moving alone.
func TestPrefsPaletteMirrorsTheClient(t *testing.T) {
	// web/src/mail/labelPalette.ts, LABEL_COLORS, in order.
	client := []string{
		"slate", "red", "orange", "amber", "lime", "green",
		"teal", "cyan", "blue", "indigo", "purple", "pink",
	}
	got := LabelColorChoices()
	if len(got) != len(client) {
		t.Fatalf("the server advertises %d palette colors, the client ships %d", len(got), len(client))
	}
	for i, want := range client {
		if got[i] != want {
			t.Errorf("palette[%d] = %q, want %q (web/src/mail/labelPalette.ts)", i, got[i], want)
		}
	}
	// "slate" is the client's DEFAULT_LABEL_COLOR_ID and its fallback for an id
	// a build does not know, so it must be a member the server accepts — or a
	// label the client renders in the default swatch would be one the server
	// refuses to store.
	if !prefsAllowedString("slate", labelColorChoices) {
		t.Error(`"slate" is the client's default and fallback color but the server does not accept it`)
	}
}

// sameShape reports whether two types are structurally identical, looking
// THROUGH the named struct types the two layers deliberately keep separate.
//
// The v1 preferences were all scalars, so identical types was the same question
// as identical shape and the test could ask the easy one. v2's three structured
// properties made them different questions: store.LabelPrefs and
// mail.LabelPrefsValue are distinct types ON PURPOSE — contracts.go' boundary,
// "a type named in this package's interfaces is a type this package owns" —
// and requiring type identity would be requiring the boundary not to exist.
//
// What still has to hold is that they carry the same FIELDS with the same
// names and shapes, which is the property whose violation actually loses a
// user's setting. So the walk descends into structs, maps and slices, comparing
// names and kinds, and only demands exact identity at the leaves.
func sameShape(a, b reflect.Type) bool {
	if a == b {
		return true
	}
	if a.Kind() != b.Kind() {
		return false
	}
	switch a.Kind() {
	case reflect.Struct:
		if a.NumField() != b.NumField() {
			return false
		}
		for i := range a.NumField() {
			fa := a.Field(i)
			fb, ok := b.FieldByName(fa.Name)
			if !ok || !sameShape(fa.Type, fb.Type) {
				return false
			}
		}
		return true
	case reflect.Map:
		return sameShape(a.Key(), b.Key()) && sameShape(a.Elem(), b.Elem())
	case reflect.Slice, reflect.Ptr:
		return sameShape(a.Elem(), b.Elem())
	default:
		return false
	}
}

// TestPrefsDefaultsAreInsideTheEnforcedDomains is the agreement that keeps a
// factory setting from being one the validator would refuse.
//
// It is not hypothetical: the defaults live in internal/store (a product
// decision) and the domains live here (a protocol decision), and the two files
// are edited by different concerns. A default outside its domain would produce
// the worst kind of bug — an account that works until the first time the user
// touches an unrelated setting, at which point the whole object is refused.
func TestPrefsDefaultsAreInsideTheEnforcedDomains(t *testing.T) {
	d := DefaultPrefsValue()

	if !prefsAllowedInt(d.UndoSendSeconds, undoSendSecondsChoices) {
		t.Errorf("the default undoSendSeconds (%d) is not one of the offered values %v",
			d.UndoSendSeconds, undoSendSecondsChoices)
	}
	for _, c := range []struct {
		name   string
		value  string
		domain []string
	}{
		{"imagesPolicy", d.ImagesPolicy, imagesPolicyChoices},
		{"autoAdvance", d.AutoAdvance, autoAdvanceChoices},
		{"density", d.Density, densityChoices},
		{"readingPane", d.ReadingPane, readingPaneChoices},
		{"inboxType", d.InboxType, inboxTypeChoices},
		{"notifications", d.Notifications, notificationsChoices},
		{"theme", d.Theme, themeChoices},
		// v2.
		{"addressAutocomplete", d.AddressAutocomplete, addressAutocompleteChoices},
		{"defaultReplyBehavior", d.DefaultReplyBehavior, defaultReplyBehaviorChoices},
	} {
		found := false
		for _, allowed := range c.domain {
			if c.value == allowed {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the default %s (%q) is not in the enforced domain %v", c.name, c.value, c.domain)
		}
	}
	// The auto case, which is "" in the stored form and null on the wire.
	if d.Language != "" {
		t.Errorf("the default language is %q, want \"\" (follow the browser)", d.Language)
	}

	// v2's bounded integers. A default outside its own bound would be the worst
	// shape of this bug: an account that works until the user touches an
	// unrelated setting, at which point the whole object is refused.
	if d.OfflineDepth.HeadersPerMailbox < minOfflineHeaders || d.OfflineDepth.HeadersPerMailbox > maxOfflineHeaders {
		t.Errorf("the default offlineDepth.headersPerMailbox (%d) is outside the enforced [%d, %d]",
			d.OfflineDepth.HeadersPerMailbox, minOfflineHeaders, maxOfflineHeaders)
	}
	if d.OfflineDepth.Bodies < minOfflineBodies || d.OfflineDepth.Bodies > maxOfflineBodies {
		t.Errorf("the default offlineDepth.bodies (%d) is outside the enforced [%d, %d]",
			d.OfflineDepth.Bodies, minOfflineBodies, maxOfflineBodies)
	}
	// The default names no signature, so the composer falls back to the
	// Identity's own — which is the precedence rule's base case.
	if d.Signatures.ForNew != "" || d.Signatures.ForReply != "" {
		t.Errorf("the default signature selection is %+v, want none", d.Signatures)
	}

	// v3. The default is the ABSENCE of a choice, so there is nothing here for
	// the domain to admit — but if a default ever appears, every one of its
	// values must be inside the enforced set, or an account would work until the
	// user touched an unrelated setting and had the whole object refused.
	for name, v := range d.FolderVisibility {
		if !prefsAllowedString(v, folderVisibilityChoices) {
			t.Errorf("the default folderVisibility[%q] = %q is not in the enforced domain %v",
				name, v, folderVisibilityChoices)
		}
	}
}

// TestPrefsPropertySetMatchesTheObject pins that prefsProperties — the set
// /get's `properties` filter is validated against — lists exactly what
// prefsObject renders. A property missing from the set would be rejected as
// unknown although the server serves it; a property in the set that the object
// does not render would be silently absent from a filtered response.
func TestPrefsPropertySetMatchesTheObject(t *testing.T) {
	rendered := prefsObject(DefaultPrefsValue(), nil)

	for name := range rendered {
		if !prefsProperties[name] {
			t.Errorf("the object renders %q but the property set does not list it: "+
				"a client filtering on it would get invalidArguments", name)
		}
	}
	for name := range prefsProperties {
		if _, ok := rendered[name]; !ok {
			t.Errorf("the property set lists %q but the object does not render it: "+
				"a client filtering on it would get an object missing the key", name)
		}
	}
}

// TestPrefsAdvertisedChoicesAreCopies pins that the exported accessors cannot
// be used to mutate what the server enforces. The session builder calls them
// per request; a shared slice would let one caller silently change the
// advertised domain for every subsequent one.
func TestPrefsAdvertisedChoicesAreCopies(t *testing.T) {
	got := ThemeChoices()
	if len(got) == 0 {
		t.Fatal("ThemeChoices is empty")
	}
	got[0] = "tampered"
	if themeChoices[0] == "tampered" {
		t.Error("ThemeChoices exposes the package's own slice: a caller can change what the server enforces")
	}

	seconds := UndoSendSecondsChoices()
	seconds[0] = 999
	if undoSendSecondsChoices[0] == 999 {
		t.Error("UndoSendSecondsChoices exposes the package's own slice")
	}
}

// TestValidLanguageTag pins the deliberately shallow BCP 47 shape check: it
// accepts what a real client sends and refuses what would turn the column into
// free text, without pretending to be a registry lookup.
func TestValidLanguageTag(t *testing.T) {
	for _, tag := range []string{"en", "es", "es-AR", "pt-BR", "zh-Hans-CN", "de-DE-1996"} {
		if !validLanguageTag(tag) {
			t.Errorf("validLanguageTag(%q) = false, want true", tag)
		}
	}
	for _, tag := range []string{
		"",
		"e",                  // a one-character primary subtag is a singleton, not a language
		"1234",               // digits cannot open a tag
		"en_US",              // underscore is not the BCP 47 separator
		"en-",                // empty subtag
		"-en",                // empty subtag
		"please use spanish", // free text
		"en-US; DROP TABLE",  // free text with punctuation
		"abcdefghi",          // subtag over 8 characters
		"../../etc/passwd",   // a path
	} {
		if validLanguageTag(tag) {
			t.Errorf("validLanguageTag(%q) = true, want false", tag)
		}
	}
}
