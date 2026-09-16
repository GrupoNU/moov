package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/GrupoNU/moov/internal/jmaphttp"
	"github.com/GrupoNU/moov/internal/metrics"
	"github.com/GrupoNU/moov/internal/store"
)

// Delegated sign-in's daemon wiring (epic M2; contract
// docs/specs/L2-accounts-api-contract.md §3.3).
//
// internal/jmaphttp owns the feature; this file owns the two environment
// variables that turn it on and the adapters that connect it to the things
// the HTTP package deliberately does not import — the store's concrete
// methods and the metrics exporter.
//
// # Why the configuration is read here and not in internal/config
//
// Every other MOOV_* variable is parsed in internal/config, and that is the
// right default. This one is not, for a reason worth stating: its value is a
// JSON document whose shape is jmaphttp.DelegatedIssuer. Parsing it in
// internal/config would mean either importing internal/jmaphttp there (a
// config package that depends on an HTTP server is backwards) or declaring a
// parallel struct and copying it field by field — a second definition of the
// same wire shape, which is exactly the kind of duplication that drifts. The
// variable is read once, next to the only thing that consumes it.
//
// # Off is the default and is not an error
//
// MOOV_DELEGATED_ISSUERS unset means the feature does not exist: every
// /auth/delegated/* route answers the generic 404 and `Authorization: Bearer`
// is refused, on every host (§3.3). A MALFORMED value, by contrast, is fatal
// at startup — an operator who wrote the JSON meant to enable delegated
// sign-in, and silently serving without it would hand their portal a 404 they
// would have to debug from the outside.

const (
	// envDelegatedIssuers is the JSON array of issuer entries (§3.3).
	envDelegatedIssuers = "MOOV_DELEGATED_ISSUERS"
	// envDelegatedSessionMax is the absolute session lifetime, as a Go
	// duration. Default 168h (7 days), per §3.4.
	envDelegatedSessionMax = "MOOV_DELEGATED_SESSION_MAX"
)

// delegatedIssuerJSON is the wire shape of one MOOV_DELEGATED_ISSUERS entry.
// Field names are the contract's, verbatim.
type delegatedIssuerJSON struct {
	Host    string `json:"host"`
	Issuer  string `json:"issuer"`
	JWKSURL string `json:"jwksUrl"`
}

// buildDelegatedConfig reads the environment and returns the delegated
// configuration, or nil when the feature is off.
//
// st supplies the session and replay-cache persistence. m may be nil.
//
// The returned config carries NO AccountStatusSource: until M1's accounts API
// lands, a provisioned account has no read-only or suspended fact to report,
// and jmaphttp's own default (name = the address, readOnly = false,
// suspended = false) is the honest answer. Wiring M1 is one line — see the
// Accounts comment below.
func buildDelegatedConfig(st *store.Store, m *metrics.Metrics) (*jmaphttp.DelegatedConfig, error) {
	raw := strings.TrimSpace(os.Getenv(envDelegatedIssuers))
	if raw == "" {
		return nil, nil //nolint:nilnil // "off" is a valid, non-error outcome
	}

	var entries []delegatedIssuerJSON
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("%s: not a JSON array of issuer objects: %w", envDelegatedIssuers, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf(
			"%s: the array is empty; unset the variable to disable delegated sign-in",
			envDelegatedIssuers)
	}

	issuers := make([]jmaphttp.DelegatedIssuer, 0, len(entries))
	for i, e := range entries {
		if err := validateIssuerEntry(i, e); err != nil {
			return nil, err
		}
		issuers = append(issuers, jmaphttp.DelegatedIssuer{
			Host:    strings.ToLower(strings.TrimSpace(e.Host)),
			Issuer:  strings.TrimSpace(e.Issuer),
			JWKSURL: strings.TrimSpace(e.JWKSURL),
		})
	}

	sessionMax := jmaphttp.DefaultDelegatedSessionMax
	if v := strings.TrimSpace(os.Getenv(envDelegatedSessionMax)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", envDelegatedSessionMax, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("%s: must be positive, got %s", envDelegatedSessionMax, d)
		}
		sessionMax = d
	}

	cfg := &jmaphttp.DelegatedConfig{
		Issuers:  issuers,
		Sessions: st,
		// Accounts: M1's accounts API supplies name / readOnly / suspended.
		// When it lands this becomes one line — `Accounts: <M1 source>` — and
		// NOTHING else in this file or in internal/jmaphttp changes: the
		// Session response, the 403 `suspended` refusal and the read-only
		// flag the PWA consumes all already read through that interface.
		SessionMax: sessionMax,
	}
	if m != nil {
		cfg.Observer = delegatedMetrics{m}
	}
	return cfg, nil
}

// validateIssuerEntry refuses a malformed entry at startup rather than at the
// first exchange. The three rules are the contract's §3.2/§3.3: `host` is a
// bare hostname (it is compared against `aud`), `issuer` is the exact `iss`
// string, and the JWKS URL is HTTPS — a plaintext key document would let
// anyone on the path mint tokens for the installation.
func validateIssuerEntry(i int, e delegatedIssuerJSON) error {
	where := fmt.Sprintf("%s[%d]", envDelegatedIssuers, i)
	host := strings.TrimSpace(e.Host)
	switch {
	case host == "":
		return fmt.Errorf("%s: host is required", where)
	case strings.Contains(host, "/"), strings.Contains(host, ":"):
		return fmt.Errorf("%s: host must be a bare hostname, no scheme and no port (got %q)", where, host)
	}
	if strings.TrimSpace(e.Issuer) == "" {
		return fmt.Errorf("%s: issuer is required", where)
	}
	jwksURL := strings.TrimSpace(e.JWKSURL)
	if jwksURL == "" {
		return fmt.Errorf("%s: jwksUrl is required", where)
	}
	u, err := url.Parse(jwksURL)
	if err != nil {
		return fmt.Errorf("%s: jwksUrl is not a URL: %w", where, err)
	}
	if u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%s: jwksUrl must be an absolute https URL (got %q)", where, jwksURL)
	}
	return nil
}

// delegatedMetrics adapts the metric set to jmaphttp.DelegatedObserver.
//
// The same seam shape as submissionMetrics and triageMetrics, for the same
// reason: internal/jmaphttp must not import internal/metrics, so this is the
// one place the two vocabularies meet — and the constants are asserted equal
// by a test rather than assumed, because nothing else checks that agreement.
type delegatedMetrics struct{ m *metrics.Metrics }

// DelegatedExchange implements jmaphttp.DelegatedObserver.
func (d delegatedMetrics) DelegatedExchange(result string) {
	if d.m == nil {
		return
	}
	d.m.IncDelegatedExchange(result)
}

// installDelegatedCollector points the active-sessions gauge at the store.
//
// A collector rather than a counter pair: sessions die three ways no code
// path observes (the sliding expiry lapses, the absolute lifetime is reached,
// an account cascade removes them), so an incremented counter would drift on
// the first of those and never recover. The store answers exactly.
//
// A failed read emits NO series rather than a zero: "no delegated sessions"
// and "the database did not answer" are different facts, and reporting the
// second as the first is how a dashboard shows a healthy flat line through an
// outage.
func installDelegatedCollector(m *metrics.Metrics, st *store.Store, logger *slog.Logger) {
	m.DelegatedSessionsActive.SetCollector(func() []metrics.Sample {
		ctx, cancel := context.WithTimeout(context.Background(), scrapeCollectTimeout)
		defer cancel()
		n, err := st.CountActiveDelegatedSessions(ctx, time.Now())
		if err != nil {
			logger.Warn("metrics: counting delegated sessions failed", "error", err)
			return nil
		}
		return []metrics.Sample{{Value: float64(n)}}
	})
}
