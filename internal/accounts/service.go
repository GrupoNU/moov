package accounts

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/GrupoNU/moov/internal/mailcow"
	"github.com/GrupoNU/moov/internal/provision"
	"github.com/GrupoNU/moov/internal/store"
)

// MailcowAPI is the slice of internal/mailcow this service uses.
//
// It is an interface for the reason every seam in this repository is one: the
// whole state machine can then be driven against a fake that answers the F0
// error families exactly, including the failures that arrive inside an HTTP
// 200, without a Mailcow anywhere near the test.
type MailcowAPI interface {
	GetMailbox(ctx context.Context, mailbox string) (mailcow.Mailbox, error)
	CreateMailbox(ctx context.Context, req mailcow.CreateMailboxRequest) error
	EditMailbox(ctx context.Context, mailbox string, e mailcow.MailboxEdit) error
	DeleteMailbox(ctx context.Context, mailbox string) error
	GetMailboxRateLimit(ctx context.Context, mailbox string) (mailcow.RateLimit, error)
	SetMailboxRateLimit(ctx context.Context, mailbox string, rl mailcow.RateLimit) error
	ListAppPasswords(ctx context.Context, mailbox string) ([]mailcow.AppPassword, error)
	CreateAppPassword(ctx context.Context, req mailcow.CreateAppPasswordRequest) (mailcow.AppPassword, error)
	DeleteAppPassword(ctx context.Context, id int64) error
}

// Store is the slice of internal/store this service uses.
type Store interface {
	GetAccountByEmail(ctx context.Context, email string) (store.Account, error)
	DeleteAccount(ctx context.Context, accountID int64) error
	AccountSyncSummary(ctx context.Context, accountID int64) (store.AccountSyncSummary, error)

	SetAccountFacts(ctx context.Context, accountID int64, f store.AccountFacts) error
	SetAccountAppPasswordID(ctx context.Context, accountID int64, id int64) error
	SetAccountCredentials(ctx context.Context, accountID int64, username string, appPassword []byte) error
	SetAccountSuspended(ctx context.Context, accountID int64, suspended bool, at time.Time) error
	SetAccountReadOnly(ctx context.Context, accountID int64, at time.Time) error
	MarkAccountDeleting(ctx context.Context, accountID int64, at time.Time) error
	ListDeletingAccounts(ctx context.Context) ([]store.Account, error)

	AppendAudit(ctx context.Context, l store.AuditLine) error
	HasAuditFor(ctx context.Context, address, action string) (bool, error)

	CreateExport(ctx context.Context, id string, accountID int64, address string) (store.Export, error)
	GetExport(ctx context.Context, id string) (store.Export, error)
	LatestExport(ctx context.Context, address string) (store.Export, error)
}

// Provisioner is the slice of internal/provision this service uses. It is the
// EXISTING flow, not a fork of it: a mailbox created here is provisioned by
// exactly the same code path as "moovctl account add", which is what keeps
// one credential story in the product (ADR §4).
type Provisioner interface {
	Provision(ctx context.Context, req provision.Request) (provision.Result, error)

	// Reissue replaces the stored credential with one carrying a narrower
	// protocol set - the enforcing half of the read-only transition (§2.4,
	// F0 answer P3).
	Reissue(ctx context.Context, email string, scopes []mailcow.Protocol) (provision.Result, error)
}

// SessionRevoker ends every live session of one account.
//
// It is the seam suspension and deletion need and M2 extends: the accounts
// API must be able to say "this mailbox is done" without knowing what kinds
// of session exist. cmd/moovd implements it over the two artifacts that exist
// today - the authenticator's credential cache and the scoped tokens - and M2
// adds delegated sessions by calling RevokeDelegatedSessions inside that same
// implementation, one line, with nothing here to change.
type SessionRevoker interface {
	// RevokeAccount invalidates every credential, token and session of the
	// account. It is called on suspend and on delete, and must be idempotent:
	// revoking an account with no live session is a no-op, not an error.
	RevokeAccount(ctx context.Context, accountID int64) error
}

// Observer counts the outcome of one admin action, for the metrics exporter.
// A nil Observer counts nothing, which is what every unit test runs with.
type Observer interface {
	// IncAdminAction records one finished write. action is the contract's
	// verb ("create", "suspend", ...); result is "ok" or "error".
	IncAdminAction(action, result string)
}

// Config is the non-secret configuration of a Service.
type Config struct {
	// MaxQuotaMB is the installation's ceiling for quotaMB
	// (MOOV_ACCOUNTS_MAX_QUOTA_MB). Zero means DefaultMaxQuotaMB.
	MaxQuotaMB int

	// Now is the clock, injectable so the state machine's timestamps are
	// deterministic in tests. nil means time.Now.
	Now func() time.Time

	// Logger receives the audit lines (which also go to the store) and the
	// failures. nil means slog.Default().
	Logger *slog.Logger
}

// Service is the accounts API's domain layer. Construct with New.
type Service struct {
	mailcow MailcowAPI
	store   Store
	prov    Provisioner
	revoker SessionRevoker
	exports *ExportRunner
	obs     Observer

	maxQuotaMB int
	now        func() time.Time
	log        *slog.Logger
}

// New builds a Service. Every dependency except the export runner and the
// observer is required: a service that cannot reach Mailcow, the store, the
// provisioner or the revoker cannot honor a single transition of §2.4, and
// discovering that per request rather than at startup is the wrong trade.
func New(cfg Config, api MailcowAPI, st Store, prov Provisioner, revoker SessionRevoker, exports *ExportRunner, obs Observer) (*Service, error) {
	switch {
	case api == nil:
		return nil, errors.New("accounts: a MailcowAPI is required")
	case st == nil:
		return nil, errors.New("accounts: a Store is required")
	case prov == nil:
		return nil, errors.New("accounts: a Provisioner is required")
	case revoker == nil:
		return nil, errors.New("accounts: a SessionRevoker is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.MaxQuotaMB <= 0 {
		cfg.MaxQuotaMB = DefaultMaxQuotaMB
	}
	return &Service{
		mailcow: api, store: st, prov: prov, revoker: revoker, exports: exports, obs: obs,
		maxQuotaMB: cfg.MaxQuotaMB, now: cfg.Now, log: cfg.Logger,
	}, nil
}

// MaxQuotaMB reports the installation's ceiling, for the validation the HTTP
// layer does on the way in.
func (s *Service) MaxQuotaMB() int { return s.maxQuotaMB }

// Actor identifies the service account making a call, for the audit line.
type Actor struct {
	ID   string
	Name string
	// Domain is the key's domain: the ONLY domain this actor may name. It is
	// checked before any Mailcow call (see resolve).
	Domain string
}

// Call is the per-request context of one write: who, why, and under which
// request id the consumer will look for it.
type Call struct {
	Actor     Actor
	RequestID string
	Reason    string
}

// resolve is the gate every route passes through: it checks the address
// against the ACTOR'S domain and only then reads the account.
//
// The domain check happens HERE, before any Mailcow call, because Mailcow's
// key is not scoped by domain (contract §4): this function is the entire
// boundary between one consumer and another consumer's mail. It is pinned by
// TestDomainIsCheckedBeforeAnyMailcowCall, which drives every route with a
// foreign address against a Mailcow fake that fails the test if it is called
// at all.
//
// Everything it refuses is ErrNotFound, and the HTTP layer renders one body
// for all of it - a caller cannot tell "not your domain" from "no such
// mailbox" (§2.1).
func (s *Service) resolve(ctx context.Context, actor Actor, address string) (store.Account, error) {
	if !inDomain(actor, address) {
		return store.Account{}, ErrNotFound
	}
	a, err := s.store.GetAccountByEmail(ctx, address)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Account{}, ErrNotFound
		}
		return store.Account{}, fmt.Errorf("reading account %q: %w", address, err)
	}
	return a, nil
}

// inDomain is the domain half of resolve alone, for the create path (where
// there is no account to read yet).
func inDomain(actor Actor, address string) bool {
	return actor.Domain != "" && DomainOf(address) == actor.Domain
}

// audit writes one line to the store and one to the log (contract §2.4).
//
// A failure to write the audit row is logged and does NOT fail the operation
// it describes: the write already happened in Mailcow and in Moov, and
// answering 500 to a caller whose mailbox was in fact created would send it
// into a retry loop against a state that is already correct. The log line is
// the fallback record, and it carries everything the row would have.
func (s *Service) audit(ctx context.Context, c Call, action, address, result, note string) {
	line := store.AuditLine{
		ActorID: c.Actor.ID, ActorName: c.Actor.Name,
		Action: action, Address: address, Result: result,
		RequestID: c.RequestID, Reason: c.Reason, Note: note,
	}
	if err := s.store.AppendAudit(ctx, line); err != nil {
		s.log.Error("accounts: the audit line could not be stored",
			"action", action, "address", address, "result", result,
			"request_id", c.RequestID, "error", err)
	}
	s.log.Info("accounts: admin action",
		"actor", c.Actor.ID, "actor_name", c.Actor.Name, "action", action,
		"address", address, "result", result, "request_id", c.RequestID,
		"reason", c.Reason, "note", note)
	if s.obs != nil {
		s.obs.IncAdminAction(action, result)
	}
}

// finish records the outcome of one write, so no route can forget its audit
// line: every write path ends by calling it with the error it is about to
// return.
func (s *Service) finish(ctx context.Context, c Call, action, address string, err error, note string) {
	result := "ok"
	if err != nil {
		result = "error"
	}
	s.audit(ctx, c, action, address, result, note)
}

// randomPassword produces the mailbox password the create path DISCARDS.
//
// Nobody is meant to log in with it: Moov authenticates with the scoped app
// password it mints next, and this one exists only because Mailcow requires a
// password to create a mailbox. It is 32 bytes of entropy, with a suffix that
// satisfies Mailcow's complexity policy whatever the encoding happened to
// draw - a retry loop against a policy we do not control would be worse.
func randomPassword() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("accounts: generating a mailbox password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw) + "aA1!", nil
}

// classifyMailcow maps a Mailcow client error onto the contract's 502/503
// split: a refusal Mailcow ANSWERED is 502, anything that looks like "it did
// not answer" is 503.
func classifyMailcow(summary string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, mailcow.ErrAPI),
		errors.Is(err, mailcow.ErrNotFound),
		errors.Is(err, mailcow.ErrUnexpectedResponse),
		errors.Is(err, mailcow.ErrKeyNotValidated):
		return upstreamRefused(summary, err)
	case errors.Is(err, mailcow.ErrUnauthorized), errors.Is(err, mailcow.ErrIPDenied):
		// A key problem is an OPERATOR problem and it is not transient: 502
		// with Moov's own summary, and the real cause in the log - never on
		// the wire, where it would describe our key.
		return upstreamRefused(summary, err)
	default:
		// Timeouts, dial failures, a 5xx from nginx: nothing changed.
		return upstreamUnavailable(summary, err)
	}
}
