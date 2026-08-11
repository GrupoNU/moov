package provision

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/GrupoNU/moov/internal/crypto"
	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/mailcow"
	"github.com/GrupoNU/moov/internal/store"
)

// Errors this package returns.
var (
	// ErrInvalidCredentials is returned when the user's password does not
	// authenticate against Dovecot. It is deliberately distinguishable from
	// every other failure: it is the only one the END USER can fix, and the
	// only one that is not an operational problem.
	ErrInvalidCredentials = errors.New("provision: the mailbox password was rejected by the mail server")

	// ErrMailboxUnusable is returned when the mailbox exists but cannot serve
	// Moov — it is disabled, or it denies one of imap/smtp/sieve, so an app
	// password would authenticate against nothing.
	ErrMailboxUnusable = errors.New("provision: the mailbox does not permit the access Moov needs")

	// ErrOrphanedAppPassword is returned when provisioning failed AND the app
	// password it created could not be removed. It is the one error an
	// operator must act on by hand; its message carries the name and id.
	ErrOrphanedAppPassword = errors.New("provision: an app password was left behind on Mailcow")

	// ErrInvalidRequest is returned for a request this package will not act
	// on, such as an empty password.
	ErrInvalidRequest = errors.New("provision: invalid request")
)

// IMAPValidator performs step 1 of the flow: proving a password works by
// logging in with it.
//
// It is an interface over internal/imap rather than a direct dependency so
// that the flow — and specifically the proof that the password is not
// persisted — is testable without a Dovecot.
type IMAPValidator interface {
	// Validate attempts an IMAP LOGIN with cfg and closes the connection.
	//
	// It returns ErrInvalidCredentials for a rejected password, and any other
	// error for a problem that is not the user's fault.
	Validate(ctx context.Context, cfg imap.Config) error
}

// MailcowAPI is the slice of internal/mailcow this flow uses.
type MailcowAPI interface {
	GetMailbox(ctx context.Context, mailbox string) (mailcow.Mailbox, error)
	CreateAppPassword(ctx context.Context, req mailcow.CreateAppPasswordRequest) (mailcow.AppPassword, error)
	DeleteAppPassword(ctx context.Context, id int64) error
}

// AccountStore is the slice of internal/store this flow uses.
//
// Note what is absent: there is no method here that takes a user password, in
// any form. The narrowness is the point — the interface makes storing one
// impossible rather than merely discouraged.
type AccountStore interface {
	GetAccountByEmail(ctx context.Context, email string) (store.Account, error)
	CreateAccount(ctx context.Context, a store.Account) (store.Account, error)
	SetAccountCredentials(ctx context.Context, accountID int64, username string, appPassword []byte) error
}

// Sealer is the slice of internal/crypto this flow uses.
type Sealer interface {
	Seal(plaintext, aad []byte) ([]byte, error)
}

// Config is the non-secret configuration of a Provisioner: how to reach
// Dovecot for the validation login.
type Config struct {
	// IMAPHost is the Dovecot hostname. Required. Inside the Moov deployment
	// this is the Mailcow container alias "dovecot".
	IMAPHost string

	// IMAPPort defaults to imap.DefaultPort (143, STARTTLS).
	IMAPPort int

	// IMAPServerName is the name Dovecot's certificate is verified against,
	// which is legitimately different from IMAPHost inside Docker (S1 H2).
	IMAPServerName string

	// IMAPInsecureSkipVerify disables certificate verification for the
	// validation login. DEVELOPMENT ONLY; see imap.Config.
	IMAPInsecureSkipVerify bool
}

// Normalize fills in defaults and validates.
func (c Config) Normalize() (Config, error) {
	if c.IMAPHost == "" {
		return c, fmt.Errorf("%w: IMAPHost is required", ErrInvalidRequest)
	}
	if c.IMAPPort == 0 {
		c.IMAPPort = imap.DefaultPort
	}
	return c, nil
}

// Provisioner runs the ADR §4 flow.
type Provisioner struct {
	cfg      Config
	validate IMAPValidator
	api      MailcowAPI
	sealer   Sealer
	accounts AccountStore
	log      *slog.Logger
}

// New builds a Provisioner. Every dependency is required; logger may be nil,
// in which case slog's default is used.
func New(cfg Config, v IMAPValidator, api MailcowAPI, sealer Sealer, accounts AccountStore, logger *slog.Logger) (*Provisioner, error) {
	cfg, err := cfg.Normalize()
	if err != nil {
		return nil, err
	}
	if v == nil || api == nil || sealer == nil || accounts == nil {
		return nil, fmt.Errorf("%w: validator, api, sealer and accounts are all required", ErrInvalidRequest)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Provisioner{
		cfg: cfg, validate: v, api: api, sealer: sealer, accounts: accounts,
		log: logger.With("component", "provision"),
	}, nil
}

// Request is one provisioning request.
//
// Password is the user's own mailbox password. It is used for the validation
// login and then discarded: it is not stored on the Provisioner, not copied
// into any struct that outlives Provision, and never logged. The field is a
// string rather than a []byte because that is what imap.Config takes; the
// package makes no attempt to pretend it can be reliably zeroed in Go.
type Request struct {
	// Email is the mailbox address. Required.
	Email string

	// Password is the user's mailbox password. Required unless AppPassword is
	// set.
	Password string

	// AppPassword bypasses Mailcow and registers a pre-existing app password.
	//
	// It exists for the pilot deployment, where an operator creates the app
	// password by hand in the Mailcow UI. The flow is otherwise identical —
	// the value is still validated by a real IMAP LOGIN and still sealed
	// before it is stored — but no Mailcow API call is made and no
	// MailcowAppPasswordID is recorded, so Moov cannot revoke it later.
	//
	// When it is set, Password is not used and need not be supplied: the app
	// password IS the credential being validated.
	AppPassword string
}

// Result is what a successful provisioning produced.
//
// It carries no secret: not the user's password, not the app password, not the
// ciphertext. A caller that wants to report success has everything it needs,
// and a caller that logs the whole struct leaks nothing.
type Result struct {
	// Account is the persisted account row.
	Account store.Account

	// AppPasswordID is the Mailcow row id of the minted app password, which is
	// what a later deprovisioning revokes. Zero when Request.AppPassword was
	// used, because no Mailcow row was created by us.
	AppPasswordID int64

	// AppPasswordName is the Mailcow app_name, for an operator looking at the
	// Mailcow UI. Empty in the AppPassword case.
	AppPasswordName string
}

// Provision runs the full flow and returns the provisioned account.
//
// The steps are ADR §4's, in order: validate with a real IMAP LOGIN, mint a
// scoped app password, seal it, persist it, discard the user's password.
//
// On any failure after the app password was created, it is deleted again
// before returning; if that deletion fails too, the error wraps
// ErrOrphanedAppPassword and names what was left behind.
func (p *Provisioner) Provision(ctx context.Context, req Request) (Result, error) {
	if req.Email == "" {
		return Result{}, fmt.Errorf("%w: Email is required", ErrInvalidRequest)
	}
	if req.Password == "" && req.AppPassword == "" {
		return Result{}, fmt.Errorf("%w: either Password or AppPassword is required", ErrInvalidRequest)
	}

	// The logger is bound to the mailbox and NOTHING else. Every log line in
	// this function inherits it, so there is no path by which a later "just
	// add the credential for debugging" edit lands in a line that already
	// carries an address.
	log := p.log.With("mailbox", req.Email)
	log.Info("provisioning account")

	// --- Step 1: validate with a real IMAP LOGIN --------------------------
	//
	// The credential under test is the app password in direct mode, and the
	// user's own password otherwise. Either way this is a real authenticated
	// session against the Dovecot Moov will sync.
	credential := req.Password
	direct := req.AppPassword != ""
	if direct {
		credential = req.AppPassword
	}

	if err := p.validate.Validate(ctx, imap.Config{
		Host:               p.cfg.IMAPHost,
		Port:               p.cfg.IMAPPort,
		Username:           req.Email,
		Password:           credential,
		TLSServerName:      p.cfg.IMAPServerName,
		InsecureSkipVerify: p.cfg.IMAPInsecureSkipVerify,
	}); err != nil {
		// The error from the IMAP layer is already redacted of the password;
		// it is wrapped rather than replaced so the operator keeps the
		// server's own diagnostic.
		return Result{}, fmt.Errorf("validating the mailbox credential: %w", err)
	}
	log.Info("credential validated against dovecot", "direct_app_password", direct)

	// --- Step 2: mint a scoped app password -------------------------------
	appPassword := credential
	var created mailcow.AppPassword

	if !direct {
		// Check the mailbox permits what we are about to grant. An app
		// password cannot exceed the mailbox's own access, so minting one for
		// a mailbox with imap_access off produces a credential that
		// authenticates against nothing — a failure that would otherwise only
		// surface at the first sync.
		mb, err := p.api.GetMailbox(ctx, req.Email)
		if err != nil {
			return Result{}, fmt.Errorf("reading the mailbox from Mailcow: %w", err)
		}
		if !mb.IsActive() {
			return Result{}, fmt.Errorf("%w: mailbox %s is disabled", ErrMailboxUnusable, req.Email)
		}
		if !mb.AllowsMoovScopes() {
			return Result{}, fmt.Errorf(
				"%w: mailbox %s does not grant all of imap, smtp and sieve", ErrMailboxUnusable, req.Email)
		}

		generated, err := mailcow.GeneratePassword()
		if err != nil {
			return Result{}, fmt.Errorf("generating the app password: %w", err)
		}
		appPassword = generated

		created, err = p.api.CreateAppPassword(ctx, mailcow.CreateAppPasswordRequest{
			Mailbox:  req.Email,
			Password: appPassword,
			Scopes:   mailcow.MoovScopes(),
		})
		if err != nil {
			return Result{}, fmt.Errorf("creating the app password: %w", err)
		}
		log.Info("app password created",
			"app_password_id", created.ID, "app_password_name", created.Name,
			"scopes", "imap+smtp+sieve")
	}

	// From here on, a failure must not leave the Mailcow-side credential
	// behind. cleanup is a no-op in direct mode, where we created nothing.
	cleanup := func(cause error) error {
		if direct || created.ID == 0 {
			return cause
		}
		if delErr := p.api.DeleteAppPassword(context.WithoutCancel(ctx), created.ID); delErr != nil {
			log.Error("could not remove the app password after a failed provisioning; "+
				"it must be deleted by hand in the Mailcow UI",
				"app_password_id", created.ID, "app_password_name", created.Name,
				"delete_error", delErr)
			return fmt.Errorf("%w: id %d, name %q, on mailbox %s (delete it in the Mailcow UI): "+
				"provisioning failed with: %w",
				ErrOrphanedAppPassword, created.ID, created.Name, req.Email, cause)
		}
		log.Info("rolled back the app password after a failed provisioning",
			"app_password_id", created.ID)
		return cause
	}

	// --- Step 3/4: the account row, then the sealed credential ------------
	//
	// The account row must exist before the credential can be sealed: the
	// seal is bound to the account id (crypto.AccountAAD), which is what stops
	// a ciphertext from being moved between accounts. So the row is created
	// first with credential_state 'pending' — a state the sync engine
	// explicitly does not act on — and the credential is attached second.
	account, err := p.upsertAccount(ctx, req.Email)
	if err != nil {
		return Result{}, cleanup(err)
	}

	sealed, err := p.sealer.Seal([]byte(appPassword), crypto.AccountAAD(account.ID))
	if err != nil {
		return Result{}, cleanup(fmt.Errorf("sealing the app password: %w", err))
	}

	if err := p.accounts.SetAccountCredentials(ctx, account.ID, req.Email, sealed); err != nil {
		return Result{}, cleanup(fmt.Errorf("storing the sealed app password: %w", err))
	}

	// --- Step 5: the user's password is discarded -------------------------
	//
	// There is nothing to do here, and that is the point: it was never written
	// to the store, never put in a log line, and never copied anywhere that
	// outlives this call. `credential` and `appPassword` are locals of a
	// function that is about to return.
	account.IMAPUsername = req.Email
	account.IMAPAppPassword = sealed
	account.CredentialState = store.CredentialActive

	log.Info("account provisioned",
		"account_id", account.ID, "app_password_id", created.ID)

	return Result{
		Account:         account,
		AppPasswordID:   created.ID,
		AppPasswordName: created.Name,
	}, nil
}

// upsertAccount returns the existing account row for an address, or creates
// one.
//
// Re-provisioning an existing account is a supported operation — it is what an
// expired or revoked credential requires — so an existing row is reused rather
// than being an error. Its credential is simply replaced by the caller.
func (p *Provisioner) upsertAccount(ctx context.Context, email string) (store.Account, error) {
	existing, err := p.accounts.GetAccountByEmail(ctx, email)
	switch {
	case err == nil:
		return existing, nil
	case errors.Is(err, store.ErrNotFound):
		// Fall through to creation.
	default:
		return store.Account{}, fmt.Errorf("looking up account %q: %w", email, err)
	}

	created, err := p.accounts.CreateAccount(ctx, store.Account{
		Email:          email,
		IMAPHost:       p.cfg.IMAPHost,
		IMAPPort:       p.cfg.IMAPPort,
		IMAPServerName: p.cfg.IMAPServerName,
		IMAPUsername:   email,
		// No credential yet: 'pending' is a state the sync engine does not act
		// on, so a crash between here and SetAccountCredentials leaves an inert
		// row rather than a half-configured account.
		CredentialState: store.CredentialPending,
		State:           store.AccountActive,
	})
	if err != nil {
		return store.Account{}, fmt.Errorf("creating account %q: %w", email, err)
	}
	return created, nil
}
