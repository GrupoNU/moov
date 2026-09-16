package main

import (
	"strings"
	"testing"
	"time"

	"github.com/GrupoNU/moov/internal/jmaphttp"
	"github.com/GrupoNU/moov/internal/metrics"
)

// The same check TestSubmissionResultConstantsAgree makes, for the same
// reason: internal/jmaphttp must not import the exporter (a server that needs
// a metrics registry to be correct is untestable), so the exchange-result
// vocabulary exists twice with no compiler to reconcile the two. This is the
// reconciliation.
func TestDelegatedResultConstantsAgree(t *testing.T) {
	pairs := []struct {
		name         string
		http, metric string
	}{
		{"ok", jmaphttp.DelegatedExchangeOK, metrics.DelegatedOK},
		{"invalid", jmaphttp.DelegatedExchangeInvalid, metrics.DelegatedInvalid},
		{"account", jmaphttp.DelegatedExchangeAccount, metrics.DelegatedAccount},
	}
	for _, p := range pairs {
		if p.http != p.metric {
			t.Errorf("%s: jmaphttp has %q, metrics has %q; the labels must match",
				p.name, p.http, p.metric)
		}
	}
}

// The adapter routes the observer's result straight onto the label, and a nil
// metric set is inert rather than a panic — buildDelegatedConfig is reachable
// from tests that build no registry.
func TestDelegatedMetricsAdapter(t *testing.T) {
	m := metrics.New()
	obs := delegatedMetrics{m}

	obs.DelegatedExchange(jmaphttp.DelegatedExchangeOK)
	obs.DelegatedExchange(jmaphttp.DelegatedExchangeInvalid)
	obs.DelegatedExchange(jmaphttp.DelegatedExchangeAccount)

	var sb strings.Builder
	if err := m.Registry().Write(&sb); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := sb.String()

	for _, want := range []string{
		`moov_delegated_exchanges_total{result="ok"} 1`,
		`moov_delegated_exchanges_total{result="invalid"} 1`,
		`moov_delegated_exchanges_total{result="account"} 1`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("exposition is missing %q\ngot:\n%s", want, got)
		}
	}

	// A nil metric set must not panic.
	delegatedMetrics{}.DelegatedExchange(jmaphttp.DelegatedExchangeOK)
}

// Unset means OFF, and off is not an error: an installation that never heard
// of a portal must start normally with every /auth/delegated/* route
// answering the generic 404.
func TestDelegatedConfigOffByDefault(t *testing.T) {
	t.Setenv(envDelegatedIssuers, "")
	cfg, err := buildDelegatedConfig(nil, nil)
	if err != nil {
		t.Fatalf("unset must not be an error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("unset must yield no configuration, got %+v", cfg)
	}
}

// A well-formed value produces the issuers verbatim, with the host
// lower-cased (it is compared against `aud`, which the contract specifies
// lower-case) and the default 7-day absolute lifetime.
func TestDelegatedConfigParsesIssuers(t *testing.T) {
	t.Setenv(envDelegatedIssuers,
		`[{"host":"Mail.Example.Test","issuer":"https://id.example.test",`+
			`"jwksUrl":"https://id.example.test/.well-known/jwks.json"}]`)
	t.Setenv(envDelegatedSessionMax, "")

	cfg, err := buildDelegatedConfig(nil, nil)
	if err != nil {
		t.Fatalf("buildDelegatedConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("a configured issuer must yield a configuration")
	}
	if len(cfg.Issuers) != 1 {
		t.Fatalf("want 1 issuer, got %d", len(cfg.Issuers))
	}
	got := cfg.Issuers[0]
	if got.Host != "mail.example.test" {
		t.Errorf("host = %q, want it lower-cased to mail.example.test", got.Host)
	}
	if got.Issuer != "https://id.example.test" {
		t.Errorf("issuer = %q", got.Issuer)
	}
	if got.JWKSURL != "https://id.example.test/.well-known/jwks.json" {
		t.Errorf("jwksUrl = %q", got.JWKSURL)
	}
	if cfg.SessionMax != jmaphttp.DefaultDelegatedSessionMax {
		t.Errorf("SessionMax = %s, want the 168h default", cfg.SessionMax)
	}
	// Until M1 lands there is no account-status source, and that ABSENCE is
	// the contract: jmaphttp's default answers name = address, readOnly and
	// suspended false. Asserting it here is what makes wiring M1 a visible,
	// deliberate change rather than a silent one.
	if cfg.Accounts != nil {
		t.Errorf("Accounts = %T; M2 ships with no status source (M1 supplies it)", cfg.Accounts)
	}
}

// MOOV_DELEGATED_SESSION_MAX overrides the ceiling.
func TestDelegatedConfigSessionMaxOverride(t *testing.T) {
	t.Setenv(envDelegatedIssuers,
		`[{"host":"mail.example.test","issuer":"https://id.example.test",`+
			`"jwksUrl":"https://id.example.test/jwks.json"}]`)
	t.Setenv(envDelegatedSessionMax, "48h")

	cfg, err := buildDelegatedConfig(nil, nil)
	if err != nil {
		t.Fatalf("buildDelegatedConfig: %v", err)
	}
	if cfg.SessionMax != 48*time.Hour {
		t.Errorf("SessionMax = %s, want 48h", cfg.SessionMax)
	}
}

// Every malformed value is fatal at startup, never a silent disable: an
// operator who wrote the JSON meant to enable the feature, and a 404 they
// would have to debug from the portal's side is the worst possible answer.
func TestDelegatedConfigRefusesMalformedValues(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"not JSON", `{`, "not a JSON array"},
		{"not an array", `{"host":"a"}`, "not a JSON array"},
		{"empty array", `[]`, "the array is empty"},
		{"no host", `[{"issuer":"https://i","jwksUrl":"https://j/k"}]`, "host is required"},
		{
			"host with a scheme",
			`[{"host":"https://mail.example.test","issuer":"https://i","jwksUrl":"https://j/k"}]`,
			"bare hostname",
		},
		{
			"host with a port",
			`[{"host":"mail.example.test:443","issuer":"https://i","jwksUrl":"https://j/k"}]`,
			"bare hostname",
		},
		{"no issuer", `[{"host":"h.example.test","jwksUrl":"https://j/k"}]`, "issuer is required"},
		{"no jwksUrl", `[{"host":"h.example.test","issuer":"https://i"}]`, "jwksUrl is required"},
		{
			"plaintext jwksUrl",
			`[{"host":"h.example.test","issuer":"https://i","jwksUrl":"http://j/k"}]`,
			"absolute https URL",
		},
		{
			"relative jwksUrl",
			`[{"host":"h.example.test","issuer":"https://i","jwksUrl":"/jwks.json"}]`,
			"absolute https URL",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(envDelegatedIssuers, c.value)
			_, err := buildDelegatedConfig(nil, nil)
			if err == nil {
				t.Fatalf("%s must be refused", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not explain %q", err, c.want)
			}
		})
	}
}

// A bad duration is refused too, and so is a non-positive one — a zero
// ceiling would make every session dead on arrival.
func TestDelegatedConfigRefusesBadSessionMax(t *testing.T) {
	t.Setenv(envDelegatedIssuers,
		`[{"host":"mail.example.test","issuer":"https://id.example.test",`+
			`"jwksUrl":"https://id.example.test/jwks.json"}]`)

	for _, value := range []string{"forever", "0", "-1h"} {
		t.Setenv(envDelegatedSessionMax, value)
		if _, err := buildDelegatedConfig(nil, nil); err == nil {
			t.Errorf("%s=%q must be refused", envDelegatedSessionMax, value)
		}
	}
}
