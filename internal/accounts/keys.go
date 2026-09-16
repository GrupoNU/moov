package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// Service-account keys (contract §2.1): the credential an external system
// presents, issued by the operator with moovctl, shown once, stored hashed,
// bound to ONE domain and a scope set.
//
// # Why SHA-256 and not bcrypt
//
// The secret is 32 bytes from crypto/rand, so there is no guessing attack to
// slow down: an attacker who can try 2^128 candidates is not the threat
// model. The hash exists so that a database dump does not hand out live keys,
// and a fast hash is what lets authentication be one indexed read rather than
// a KDF per request. This is the same reasoning that makes a random session
// token hashed-not-KDF'd everywhere else.

// KeyPrefix is the version prefix every key carries. It is in the ciphertext
// space, not a secret: it tells an operator what they are looking at in a
// config file and lets a future format change be recognizable rather than
// ambiguous.
const KeyPrefix = "msa1_"

// keySecretBytes is the entropy behind a key: 32 bytes is 43 base64url
// characters, which is what §2.1 publishes.
const keySecretBytes = 32

// idBytes is the entropy behind a service-account id. The id is not a
// secret - it appears in every audit line - so it is short.
const idBytes = 8

// The scopes of §2.1.
const (
	ScopeRead  = store.ScopeAccountsRead
	ScopeWrite = store.ScopeAccountsWrite
)

// ErrUnknownScope is returned for a scope this version does not define.
var ErrUnknownScope = errors.New("accounts: unknown scope")

// ValidateScopes normalizes and checks a scope set.
func ValidateScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return nil, fmt.Errorf("%w: at least one scope is required", ErrUnknownScope)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		s = strings.TrimSpace(s)
		switch s {
		case ScopeRead, ScopeWrite:
		default:
			return nil, fmt.Errorf("%w: %q (want %s or %s)", ErrUnknownScope, s, ScopeRead, ScopeWrite)
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

// KeyStore is the slice of internal/store the key layer uses.
type KeyStore interface {
	CreateServiceAccount(ctx context.Context, sa store.ServiceAccount) (store.ServiceAccount, error)
	GetServiceAccountByHash(ctx context.Context, keyHash []byte) (store.ServiceAccount, error)
	ListServiceAccounts(ctx context.Context) ([]store.ServiceAccount, error)
	RevokeServiceAccount(ctx context.Context, id string, at time.Time) error
}

// IssuedKey is what a mint returns: the record, and the secret ONCE.
type IssuedKey struct {
	ServiceAccount store.ServiceAccount

	// Secret is the full key as the consumer must present it. It exists only
	// in this struct, only in this process, and only until the caller has
	// printed it: nothing stores it and nothing can recover it.
	Secret string
}

// MintKey issues a service-account key for one domain.
func MintKey(ctx context.Context, ks KeyStore, domain, name string, scopes []string) (IssuedKey, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if !validDomain(domain) {
		return IssuedKey{}, fmt.Errorf("accounts: %q is not a valid domain name", domain)
	}
	normalized, err := ValidateScopes(scopes)
	if err != nil {
		return IssuedKey{}, err
	}

	secretRaw := make([]byte, keySecretBytes)
	if _, err := rand.Read(secretRaw); err != nil {
		return IssuedKey{}, fmt.Errorf("accounts: generating a service-account key: %w", err)
	}
	secret := KeyPrefix + base64.RawURLEncoding.EncodeToString(secretRaw)

	idRaw := make([]byte, idBytes)
	if _, err := rand.Read(idRaw); err != nil {
		return IssuedKey{}, fmt.Errorf("accounts: generating a service-account id: %w", err)
	}
	id := "sa_" + hex.EncodeToString(idRaw)

	sum := HashKey(secret)
	sa, err := ks.CreateServiceAccount(ctx, store.ServiceAccount{
		ID: id, KeyHash: sum, Domain: domain, Scopes: normalized,
		Name: strings.TrimSpace(name),
	})
	if err != nil {
		return IssuedKey{}, err
	}
	return IssuedKey{ServiceAccount: sa, Secret: secret}, nil
}

// HashKey is the stored form of a presented key. Exported so the CLI, the
// authenticator and the tests all hash identically - a second spelling of
// this is how a key stops verifying after a refactor.
func HashKey(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

// Authenticator resolves a presented key to an Actor.
type Authenticator struct {
	keys KeyStore
	now  func() time.Time
}

// NewAuthenticator builds one.
func NewAuthenticator(ks KeyStore, now func() time.Time) *Authenticator {
	if now == nil {
		now = time.Now
	}
	return &Authenticator{keys: ks, now: now}
}

// Authenticate resolves a presented bearer credential.
//
// EVERY failure is ErrNotFound - no header, a malformed key, an unknown one,
// a revoked one, one without the scope - because the wire has one answer for
// all of them (§2.1). The reasons differ only in what the caller logs.
func (a *Authenticator) Authenticate(ctx context.Context, presented, needScope string) (Actor, error) {
	if a == nil || a.keys == nil {
		return Actor{}, ErrNotFound
	}
	if !strings.HasPrefix(presented, KeyPrefix) {
		return Actor{}, ErrNotFound
	}
	// The length is checked before the store is touched, so a flood of
	// obviously-malformed keys costs no query.
	if len(presented) != len(KeyPrefix)+base64.RawURLEncoding.EncodedLen(keySecretBytes) {
		return Actor{}, ErrNotFound
	}
	sa, err := a.keys.GetServiceAccountByHash(ctx, HashKey(presented))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Actor{}, ErrNotFound
		}
		return Actor{}, fmt.Errorf("resolving a service-account key: %w", err)
	}
	if sa.Revoked() || !sa.HasScope(needScope) {
		return Actor{}, ErrNotFound
	}
	return Actor{ID: sa.ID, Name: sa.Name, Domain: sa.Domain}, nil
}

// BearerToken extracts the credential from an Authorization header value. It
// returns "" for anything that is not a Bearer scheme, which the caller then
// treats as an absent credential.
func BearerToken(header string) string {
	const scheme = "bearer "
	if len(header) <= len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return ""
	}
	return strings.TrimSpace(header[len(scheme):])
}
