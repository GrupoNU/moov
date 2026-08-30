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
		if got.Type != want.Type {
			t.Errorf("field %s is %s here and %s in the store", want.Name, want.Type, got.Type)
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
	}
	if got := storePrefs(prefsValue(in)); got != in {
		t.Errorf("round trip changed the value:\n in: %+v\nout: %+v", in, got)
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
