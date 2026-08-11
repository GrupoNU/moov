package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/GrupoNU/moov/internal/crypto"
	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/mailcow"
	"github.com/GrupoNU/moov/internal/provision"
	"github.com/GrupoNU/moov/internal/store"
)

// Environment variables read by the account commands.
const (
	envDatabaseURL    = "MOOV_DATABASE_URL"
	envIMAPHost       = "MOOV_IMAP_HOST"
	envIMAPPort       = "MOOV_IMAP_PORT"
	envIMAPServerName = "MOOV_IMAP_SERVER_NAME"

	// envAccountPassword names the variable a non-interactive run reads the
	// mailbox password from. The value below is the variable's NAME; no
	// password is compiled into this binary.
	//
	// #nosec G101 -- an environment variable name, not a credential.
	envAccountPassword = "MOOV_ACCOUNT_PASSWORD"

	// defaultIMAPHost is the Mailcow Dovecot container alias on the shared
	// Docker network, which is where Moov runs (ADR §4).
	defaultIMAPHost = "dovecot"
)

func accountCommand(ctx context.Context, e *env, args []string) error {
	if len(args) == 0 {
		return usageErrorf("account needs a subcommand (add, list, disable)")
	}
	switch args[0] {
	case "add":
		return accountAdd(ctx, e, args[1:])
	case "list":
		return accountList(ctx, e, args[1:])
	case "disable":
		return accountDisable(ctx, e, args[1:])
	default:
		return usageErrorf("unknown account subcommand %q (want add, list or disable)", args[0])
	}
}

// accountAdd runs the ADR §4 provisioning flow for one mailbox.
func accountAdd(ctx context.Context, e *env, args []string) error {
	fs := flag.NewFlagSet("account add", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	appPassword := fs.Bool("app-password", false,
		"register an existing app password instead of minting one through the Mailcow API "+
			"(pilot mode: the value is prompted for, still validated by a real IMAP login, "+
			"and still encrypted before storage)")
	fs.Usage = func() {
		out(e.stderr, "Usage: moovctl account add [-app-password] <email>\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return usageErrorf("account add needs exactly one email address")
	}
	email := strings.TrimSpace(fs.Arg(0))
	if email == "" || !strings.Contains(email, "@") {
		return usageErrorf("%q is not an email address", email)
	}

	// --- the credential ---------------------------------------------------
	//
	// Read before anything is opened, so a typo does not cost a database
	// connection, and never from a command-line argument.
	prompt := fmt.Sprintf("Mailbox password for %s: ", email)
	if *appPassword {
		prompt = fmt.Sprintf("Existing Mailcow app password for %s: ", email)
	}
	secret, err := readSecret(e, prompt, envAccountPassword)
	if err != nil {
		return err
	}
	if secret == "" {
		return errors.New("no password was given")
	}

	// --- dependencies -----------------------------------------------------
	keyring, err := crypto.LoadKeyring()
	if err != nil {
		return err
	}

	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer st.Close()

	imapCfg, err := imapConfigFromEnv()
	if err != nil {
		return err
	}

	// In -app-password mode the Mailcow API is not used at all, so its
	// configuration is not required. Demanding an admin API key to register a
	// credential an operator already created by hand would be gratuitous —
	// and would put the broadest secret on the host for no reason.
	var api provision.MailcowAPI = unavailableMailcowAPI{}
	if !*appPassword {
		mcCfg, err := mailcow.LoadConfig()
		if err != nil {
			return err
		}
		client, err := mailcow.New(mcCfg)
		if err != nil {
			return err
		}
		e.logger.Debug("mailcow client configured", "config", mcCfg.String())
		api = client
	}

	p, err := provision.New(imapCfg, provision.NewIMAPValidator(e.logger), api, keyring, st, e.logger)
	if err != nil {
		return err
	}

	req := provision.Request{Email: email}
	if *appPassword {
		req.AppPassword = secret
	} else {
		req.Password = secret
	}

	res, err := p.Provision(ctx, req)
	if err != nil {
		return err
	}

	// stdout carries the result; nothing here is a secret.
	outf(e.stdout, "Provisioned %s (account %d).\n", res.Account.Email, res.Account.ID)
	if res.AppPasswordID != 0 {
		outf(e.stdout, "  Mailcow app password: %s (id %d), scoped imap+smtp+sieve.\n",
			res.AppPasswordName, res.AppPasswordID)
	} else {
		outf(e.stdout,
			"  Registered an existing app password. Moov cannot revoke it: "+
				"remove it in the Mailcow UI when this account is deleted.\n")
	}
	outf(e.stdout, "  The mailbox password was not stored.\n")
	return nil
}

// accountList prints the provisioned accounts.
func accountList(ctx context.Context, e *env, args []string) error {
	fs := flag.NewFlagSet("account list", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if fs.NArg() != 0 {
		return usageErrorf("account list takes no arguments")
	}

	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer st.Close()

	accounts, err := st.ListAccounts(ctx)
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		outln(e.stdout, "No accounts are provisioned.")
		return nil
	}

	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	outln(w, "ID\tEMAIL\tSTATE\tCREDENTIAL\tKEY\tIMAP\tCREATED")
	for _, a := range accounts {
		// The KEY column is the credential's master key id, read from the
		// envelope header WITHOUT decrypting. It is what tells an operator
		// whether a rotation still has rows to re-seal.
		key := "-"
		if len(a.IMAPAppPassword) > 0 {
			if id, err := crypto.EnvelopeKeyID(a.IMAPAppPassword); err == nil {
				key = fmt.Sprintf("%d", id)
			} else {
				key = "invalid"
			}
		}
		outf(w, "%d\t%s\t%s\t%s\t%s\t%s:%d\t%s\n",
			a.ID, a.Email, a.State, a.CredentialState, key,
			a.IMAPHost, a.IMAPPort, a.CreatedAt.UTC().Format("2006-01-02"))
	}
	return w.Flush()
}

// accountDisable stops the engine from syncing an account without deleting
// anything.
func accountDisable(ctx context.Context, e *env, args []string) error {
	fs := flag.NewFlagSet("account disable", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.Usage = func() {
		out(e.stderr, "Usage: moovctl account disable <email>\n\n"+
			"Stops syncing the account. Its stored mail and credentials are kept,\n"+
			"and the Mailcow app password is NOT revoked.\n")
	}
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return usageErrorf("account disable needs exactly one email address")
	}
	email := strings.TrimSpace(fs.Arg(0))

	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer st.Close()

	account, err := st.GetAccountByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no account for %s", email)
		}
		return err
	}
	if account.State == store.AccountDisabled {
		outf(e.stdout, "%s is already disabled.\n", email)
		return nil
	}

	if err := st.SetAccountState(ctx, account.ID, store.AccountDisabled); err != nil {
		return err
	}

	outf(e.stdout, "Disabled %s (account %d). Its mail and credentials are kept.\n",
		email, account.ID)
	// Say the thing that is not obvious: disabling does not revoke.
	outf(e.stdout,
		"  The Mailcow app password is still valid; revoke it in the Mailcow UI to cut access.\n")
	return nil
}

// unavailableMailcowAPI stands in for the Mailcow client in -app-password
// mode, where no API key is configured.
//
// Every method fails loudly. It is not a silent no-op: reaching one of these
// would mean the direct path took a branch it should not have, and a
// provisioning flow that silently skipped creating a credential would be far
// worse than one that stops.
type unavailableMailcowAPI struct{}

var errMailcowNotConfigured = errors.New(
	"the Mailcow API is not configured (this command was run with -app-password)")

func (unavailableMailcowAPI) GetMailbox(context.Context, string) (mailcow.Mailbox, error) {
	return mailcow.Mailbox{}, errMailcowNotConfigured
}

func (unavailableMailcowAPI) CreateAppPassword(context.Context, mailcow.CreateAppPasswordRequest) (mailcow.AppPassword, error) {
	return mailcow.AppPassword{}, errMailcowNotConfigured
}

func (unavailableMailcowAPI) DeleteAppPassword(context.Context, int64) error {
	return errMailcowNotConfigured
}

// openStore connects to the database described by the environment.
func openStore(ctx context.Context) (*store.Store, error) {
	dsn := os.Getenv(envDatabaseURL)
	if dsn == "" {
		return nil, fmt.Errorf("%s is not set", envDatabaseURL)
	}
	st, err := store.Open(ctx, store.Config{DSN: dsn})
	if err != nil {
		return nil, fmt.Errorf("connecting to the database: %w", err)
	}
	return st, nil
}

// imapConfigFromEnv builds the provisioning config for the validation login.
func imapConfigFromEnv() (provision.Config, error) {
	cfg := provision.Config{
		IMAPHost:       envOr(envIMAPHost, defaultIMAPHost),
		IMAPServerName: os.Getenv(envIMAPServerName),
	}
	if v := os.Getenv(envIMAPPort); v != "" {
		var port int
		if _, err := fmt.Sscanf(v, "%d", &port); err != nil {
			return provision.Config{}, fmt.Errorf("%s: %q is not a port number", envIMAPPort, v)
		}
		cfg.IMAPPort = port
	}
	if cfg.IMAPPort == 0 {
		cfg.IMAPPort = imap.DefaultPort
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
