package jmaphttp

import (
	"encoding/json"
	"testing"

	"github.com/GrupoNU/moov/internal/jmap"
	"github.com/GrupoNU/moov/internal/jmap/mail"
)

// The Session object's advertisement of Moov's VENDOR preference capability
// (L3 epic E0), against RFC 8620 §2's extensibility contract.
//
// Two properties are at stake and they pull in opposite directions, which is
// why both are pinned here rather than assumed:
//
//  1. a client that speaks the extension must be able to DISCOVER it from the
//     session alone, in all three places a data capability belongs;
//  2. a client that does not must be able to parse the very same session
//     document without knowing the URI exists.
//
// The second is the one worth testing hardest, because it is the promise E0
// makes to every other JMAP client in the world.

// TestSessionAdvertisesThePrefsCapability covers discovery.
func TestSessionAdvertisesThePrefsCapability(t *testing.T) {
	s, _, _, _ := newTestServer(t, func(c *Config) { c.Prefs = true })
	obj := fetchSession(t, s)

	caps := asObject(t, obj["capabilities"], "capabilities")
	prefsCap, ok := caps[jmap.CapPrefs].(map[string]any)
	if !ok {
		t.Fatalf("the session does not advertise %s", jmap.CapPrefs)
	}
	// The schema version is what lets a client with cached preferences know
	// whether its vocabulary still matches the server's.
	if prefsCap["schemaVersion"] != float64(mail.PrefsSchemaVersion) {
		t.Errorf("schemaVersion = %v, want %d", prefsCap["schemaVersion"], mail.PrefsSchemaVersion)
	}
	// Prefs/changes is really implemented rather than registered-to-decline,
	// and the capability says so: a client can poll instead of refetching.
	if prefsCap["maxChangesSupported"] != true {
		t.Error("maxChangesSupported must be true: Prefs/changes answers for real")
	}

	accounts := asObject(t, obj["accounts"], "accounts")
	acct := asObject(t, accounts["a7"], "accounts.a7")
	acctCaps := asObject(t, acct["accountCapabilities"], "accountCapabilities")
	perAccount, ok := acctCaps[jmap.CapPrefs].(map[string]any)
	if !ok {
		t.Fatalf("the account does not carry the %s accountCapability", jmap.CapPrefs)
	}

	primary := asObject(t, obj["primaryAccounts"], "primaryAccounts")
	if primary[jmap.CapPrefs] != "a7" {
		t.Errorf("primaryAccounts[%s] = %v, want the caller's account", jmap.CapPrefs, primary[jmap.CapPrefs])
	}

	// declared == applied, the J1 rule extended to value domains: the lists a
	// client builds its settings UI from must be exactly what Prefs/set
	// accepts, or the UI offers controls whose every save is refused.
	assertAdvertisedDomain(t, perAccount, "imagesPolicyValues", mail.ImagesPolicyChoices())
	assertAdvertisedDomain(t, perAccount, "autoAdvanceValues", mail.AutoAdvanceChoices())
	assertAdvertisedDomain(t, perAccount, "densityValues", mail.DensityChoices())
	assertAdvertisedDomain(t, perAccount, "readingPaneValues", mail.ReadingPaneChoices())
	assertAdvertisedDomain(t, perAccount, "inboxTypeValues", mail.InboxTypeChoices())
	assertAdvertisedDomain(t, perAccount, "notificationsValues", mail.NotificationsChoices())
	assertAdvertisedDomain(t, perAccount, "themeValues", mail.ThemeChoices())
	// v3's folder rail. The two caps ride along, because a client that curates
	// the rail must be able to stop the user AT the boundary rather than after
	// a refused save — the same declared == applied rule, applied to numbers.
	assertAdvertisedDomain(t, perAccount, "folderVisibilityValues", mail.FolderVisibilityChoices())
	for key, want := range map[string]int{
		"maxFolderVisibility": mail.MaxFolderVisibility(),
		"maxFolderNameBytes":  mail.MaxFolderNameBytes(),
	} {
		if perAccount[key] != float64(want) {
			t.Errorf("%s = %v, want the enforced %d", key, perAccount[key], want)
		}
	}
	// No default is advertised, and that absence is deliberate: what to draw for
	// a folder the user never named is the CLIENT's policy, so a server
	// publishing a default here would be publishing a decision it does not make.
	for _, key := range []string{"defaultFolderVisibility", "folderVisibilityDefault"} {
		if _, present := perAccount[key]; present {
			t.Errorf("the capability advertises %q: the rail's defaults belong to the client", key)
		}
	}

	seconds, ok := perAccount["undoSendSeconds"].([]any)
	if !ok {
		t.Fatalf("undoSendSeconds = %v, want the offered set", perAccount["undoSendSeconds"])
	}
	want := mail.UndoSendSecondsChoices()
	if len(seconds) != len(want) {
		t.Fatalf("undoSendSeconds has %d values, want Gmail's %d (canon §2.3)", len(seconds), len(want))
	}
	for i, v := range seconds {
		if v != float64(want[i]) {
			t.Errorf("undoSendSeconds[%d] = %v, want %d", i, v, want[i])
		}
	}
}

func assertAdvertisedDomain(t *testing.T, capability map[string]any, key string, want []string) {
	t.Helper()
	got, ok := capability[key].([]any)
	if !ok {
		t.Errorf("%s = %v, want the enforced value domain", key, capability[key])
		return
	}
	if len(got) != len(want) {
		t.Errorf("%s has %d values, want %d (declared == applied)", key, len(got), len(want))
		return
	}
	for i, v := range got {
		if v != want[i] {
			t.Errorf("%s[%d] = %v, want %q", key, i, v, want[i])
		}
	}
}

// TestSessionWithoutPrefsIsUnchanged pins the other side of the config flag:
// a deployment that does not mount the preference methods must not advertise
// them (advertised == registered), or a client would opt into a capability
// whose methods answer unknownMethod.
func TestSessionWithoutPrefsIsUnchanged(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)
	obj := fetchSession(t, s)

	caps := asObject(t, obj["capabilities"], "capabilities")
	if _, present := caps[jmap.CapPrefs]; present {
		t.Error("the preference capability is advertised although it was not mounted")
	}
	accounts := asObject(t, obj["accounts"], "accounts")
	acct := asObject(t, accounts["a7"], "accounts.a7")
	acctCaps := asObject(t, acct["accountCapabilities"], "accountCapabilities")
	if _, present := acctCaps[jmap.CapPrefs]; present {
		t.Error("the account advertises a preference accountCapability that was not mounted")
	}
}

// TestSessionRemainsValidForAClientIgnoringThePrefsCapability is E0's promise
// to every standards-conforming client, checked structurally rather than by
// assertion about our own code.
//
// The method: fetch the session with the capability mounted, DELETE every
// mention of the vendor URI (which is precisely what a client that does not
// recognize it effectively does), and then verify that what remains is a
// complete, valid RFC 8620 §2 Session object — every required property
// present, correctly typed, with the two IETF capabilities intact.
//
// If the extension had been bolted onto the mail capability, or had replaced a
// required property, or had made a required property's type depend on the
// extension being understood, this test would fail. That is the failure mode
// it exists to catch, and it cannot be caught by any test that keeps the
// vendor keys in the document.
func TestSessionRemainsValidForAClientIgnoringThePrefsCapability(t *testing.T) {
	s, _, _, _ := newTestServer(t, func(c *Config) {
		c.Prefs = true
		c.Submission = true
	})
	obj := fetchSession(t, s)

	// The naive client's view: drop what it does not know.
	caps := asObject(t, obj["capabilities"], "capabilities")
	delete(caps, jmap.CapPrefs)
	accounts := asObject(t, obj["accounts"], "accounts")
	for name, raw := range accounts {
		acct := asObject(t, raw, "accounts."+name)
		acctCaps := asObject(t, acct["accountCapabilities"], "accountCapabilities")
		delete(acctCaps, jmap.CapPrefs)
	}
	delete(asObject(t, obj["primaryAccounts"], "primaryAccounts"), jmap.CapPrefs)

	// §2 lists the Session object's required properties. Each is checked for
	// presence AND type, because a client's decoder fails on either.
	if _, ok := caps[jmap.CapCore].(map[string]any); !ok {
		t.Error("core capability missing or not an object after dropping the vendor URI")
	}
	if mailCap, ok := caps[jmap.CapMail].(map[string]any); !ok {
		t.Error("mail capability missing after dropping the vendor URI")
	} else if len(mailCap) != 0 {
		t.Errorf("mail session capability = %v, want an empty object (RFC 8621 §1.3.1): "+
			"the extension must not have added keys to a standard capability", mailCap)
	}

	for _, key := range []string{"username", "apiUrl", "downloadUrl", "uploadUrl", "eventSourceUrl", "state"} {
		if v, ok := obj[key].(string); !ok || v == "" {
			t.Errorf("%s = %v, want a non-empty string (RFC 8620 §2)", key, obj[key])
		}
	}
	if len(accounts) != 1 {
		t.Fatalf("accounts has %d entries, want exactly the caller's", len(accounts))
	}
	for name, raw := range accounts {
		acct := asObject(t, raw, "accounts."+name)
		if _, ok := acct["name"].(string); !ok {
			t.Errorf("account %s has no name", name)
		}
		if _, ok := acct["isPersonal"].(bool); !ok {
			t.Errorf("account %s has no isPersonal", name)
		}
		if _, ok := acct["isReadOnly"].(bool); !ok {
			t.Errorf("account %s has no isReadOnly", name)
		}
		acctCaps := asObject(t, acct["accountCapabilities"], "accountCapabilities")
		if _, ok := acctCaps[jmap.CapMail].(map[string]any); !ok {
			t.Errorf("account %s lost its mail accountCapability", name)
		}
	}

	// And the reduced document still round-trips as JSON, which is the actual
	// operation a client performs on it.
	if _, err := json.Marshal(obj); err != nil {
		t.Fatalf("the reduced session does not re-encode: %v", err)
	}
}
