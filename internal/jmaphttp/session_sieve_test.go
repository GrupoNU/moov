package jmaphttp

import (
	"testing"

	"github.com/GrupoNU/moov/internal/jmap"
	"github.com/GrupoNU/moov/internal/jmap/mail"
)

// The Session's advertisement of the E6 capabilities: RFC 9661 §1.2.1's
// values live in the ACCOUNT capability, the RFC 8621 §1.3.3 and RFC 9425
// §2.1 objects are empty in both places, and none of it appears on a server
// that did not mount the methods (advertised == registered).

func TestSessionAdvertisesTheSieveCapabilities(t *testing.T) {
	maxRedirects := 100
	s, _, _, _ := newTestServer(t, func(c *Config) {
		c.Sieve = &SieveCapability{
			Extensions:          []string{"fileinto", "vacation", "imap4flags"},
			NotificationMethods: []string{"mailto"},
			MaxRedirects:        &maxRedirects,
		}
		c.Vacation = true
		c.Quota = true
		c.Filters = true
	})
	obj := fetchSession(t, s)
	caps := asObject(t, obj["capabilities"], "capabilities")
	accounts := asObject(t, obj["accounts"], "accounts")
	acctCaps := asObject(t, asObject(t, accounts["a7"], "accounts.a7")["accountCapabilities"], "accountCapabilities")
	primary := asObject(t, obj["primaryAccounts"], "primaryAccounts")

	for _, uri := range []string{jmap.CapSieve, jmap.CapVacation, jmap.CapQuota, jmap.CapFilters} {
		if _, ok := caps[uri]; !ok {
			t.Errorf("capabilities missing %s", uri)
		}
		if _, ok := acctCaps[uri]; !ok {
			t.Errorf("accountCapabilities missing %s", uri)
		}
		if primary[uri] != "a7" {
			t.Errorf("primaryAccounts[%s] = %v", uri, primary[uri])
		}
	}

	// RFC 9661 §1.2.1's values, in the account capability.
	sieveCap := asObject(t, acctCaps[jmap.CapSieve], "sieve accountCapability")
	if sieveCap["maxSizeScriptName"] != float64(512) {
		t.Errorf("maxSizeScriptName = %v, want 512 (§1.2.1: MUST be at least 512)", sieveCap["maxSizeScriptName"])
	}
	// declared == applied: the advertised script ceiling is the one the /set
	// path enforces.
	if sieveCap["maxSizeScript"] != float64(mail.MaxSieveScriptSize) {
		t.Errorf("maxSizeScript = %v, want %d", sieveCap["maxSizeScript"], mail.MaxSieveScriptSize)
	}
	if sieveCap["maxNumberScripts"] != nil {
		t.Errorf("maxNumberScripts = %v, want null (no limit)", sieveCap["maxNumberScripts"])
	}
	if sieveCap["maxNumberRedirects"] != float64(100) {
		t.Errorf("maxNumberRedirects = %v, want the probed 100", sieveCap["maxNumberRedirects"])
	}
	exts, ok := sieveCap["sieveExtensions"].([]any)
	if !ok || len(exts) != 3 || exts[0] != "fileinto" {
		t.Errorf("sieveExtensions = %v, want the probed list verbatim", sieveCap["sieveExtensions"])
	}
	if sieveCap["externalLists"] != nil {
		t.Errorf("externalLists = %v, want null (extlists unsupported)", sieveCap["externalLists"])
	}

	// §1.3.3 / §2.1: empty objects for vacation and quota.
	for _, uri := range []string{jmap.CapVacation, jmap.CapQuota} {
		if v := asObject(t, acctCaps[uri], uri); len(v) != 0 {
			t.Errorf("%s account capability = %v, want an empty object", uri, v)
		}
	}
}

// A server without the E6 flags advertises none of it — a client that never
// heard of Sieve sees the same session it saw before the epic.
func TestSessionWithoutSieveAdvertisesNothingOfIt(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)
	obj := fetchSession(t, s)
	caps := asObject(t, obj["capabilities"], "capabilities")
	for _, uri := range []string{jmap.CapSieve, jmap.CapVacation, jmap.CapQuota, jmap.CapFilters} {
		if _, ok := caps[uri]; ok {
			t.Errorf("an unmounted capability is advertised: %s", uri)
		}
	}
}
