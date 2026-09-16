package main

import (
	"context"
	"flag"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/GrupoNU/moov/internal/accounts"
)

// `moovctl service-account` issues and manages the API keys of the per-domain
// accounts API (docs/specs/L2-accounts-api-contract.md §2.1).
//
// # Why the operator issues these and no API does
//
// A service-account key can create, suspend, export and delete mailboxes of
// its domain. Deciding that an external system may do that to a domain is an
// operator decision with shell access behind it, exactly like granting a
// brand admin - so it lives here, in the binary that runs for the length of
// one command, and not behind a network route that would need its own
// bootstrap credential.
//
// # The key is shown once
//
// Only its SHA-256 is stored. There is no command to print it again, and that
// is not an oversight: a key an operator can re-read is a key a stolen
// database can re-read. An operator who lost one revokes it and issues
// another, which is a 20-second operation and leaves an audit trail.

func serviceAccountCommand(ctx context.Context, e *env, args []string) error {
	if len(args) == 0 {
		return usageErrorf("service-account needs a subcommand (create, list, revoke)")
	}
	switch args[0] {
	case "create":
		return serviceAccountCreate(ctx, e, args[1:])
	case "list":
		return serviceAccountList(ctx, e, args[1:])
	case "revoke":
		return serviceAccountRevoke(ctx, e, args[1:])
	case "help", "-h", "--help":
		out(e.stdout, serviceAccountUsage)
		return nil
	default:
		return usageErrorf("unknown service-account subcommand %q", args[0])
	}
}

const serviceAccountUsage = `moovctl service-account — API keys of the accounts API

Usage:
  moovctl service-account create -domain <d> -scopes accounts:write [-name <n>]
  moovctl service-account list
  moovctl service-account revoke -id <sa_...>

A key is bound to ONE domain and is shown ONCE, at creation. Only its hash is
stored; there is no way to print it again.
`

func serviceAccountCreate(ctx context.Context, e *env, args []string) error {
	fs := flag.NewFlagSet("service-account create", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	domain := fs.String("domain", "", "the one domain this key may manage (required)")
	scopes := fs.String("scopes", accounts.ScopeWrite,
		"comma-separated scopes: accounts:read, accounts:write (write implies read)")
	name := fs.String("name", "", "a label for the consumer, shown in audit lines")
	fs.Usage = func() {
		out(e.stderr, "Usage: moovctl service-account create -domain <domain> [-scopes ...] [-name ...]\n\n"+
			"Issues a key for the accounts API. The key is printed ONCE and only its\n"+
			"hash is stored: copy it now or revoke it and issue another.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if fs.NArg() != 0 {
		return usageErrorf("service-account create takes no positional arguments")
	}
	if strings.TrimSpace(*domain) == "" {
		return usageErrorf("-domain is required")
	}

	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer st.Close()

	issued, err := accounts.MintKey(ctx, st, *domain, *name, strings.Split(*scopes, ","))
	if err != nil {
		return err
	}

	sa := issued.ServiceAccount
	outf(e.stdout, "Service account %s created for %s (scopes: %s).\n\n",
		sa.ID, sa.Domain, strings.Join(sa.Scopes, ", "))
	// The key goes to STDOUT on its own line so it can be piped into a secret
	// store; everything around it is prose an operator reads.
	outf(e.stdout, "%s\n\n", issued.Secret)
	outln(e.stdout, "This is the only time the key is shown. Store it now.")
	return nil
}

func serviceAccountList(ctx context.Context, e *env, args []string) error {
	fs := flag.NewFlagSet("service-account list", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if fs.NArg() != 0 {
		return usageErrorf("service-account list takes no arguments")
	}

	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer st.Close()

	list, err := st.ListServiceAccounts(ctx)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		outln(e.stdout, "No service accounts are configured.")
		return nil
	}

	w := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	outln(w, "ID\tDOMAIN\tSCOPES\tNAME\tSTATE\tCREATED\tLAST USED")
	for _, sa := range list {
		state := "active"
		if sa.Revoked() {
			state = "revoked " + sa.RevokedAt.UTC().Format("2006-01-02")
		}
		outf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			sa.ID, sa.Domain, strings.Join(sa.Scopes, "+"), orDash(sa.Name), state,
			sa.CreatedAt.UTC().Format("2006-01-02"), formatTime(sa.LastUsedAt))
	}
	return w.Flush()
}

func serviceAccountRevoke(ctx context.Context, e *env, args []string) error {
	fs := flag.NewFlagSet("service-account revoke", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	id := fs.String("id", "", "the service account id (required, sa_...)")
	fs.Usage = func() {
		out(e.stderr, "Usage: moovctl service-account revoke -id <sa_...>\n\n"+
			"Revokes a key immediately. The next request presenting it gets the same\n"+
			"generic 404 an unknown key gets; the audit lines it already wrote stay.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if fs.NArg() != 0 {
		return usageErrorf("service-account revoke takes no positional arguments")
	}
	if strings.TrimSpace(*id) == "" {
		return usageErrorf("-id is required")
	}

	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := st.RevokeServiceAccount(ctx, strings.TrimSpace(*id), time.Now()); err != nil {
		return err
	}
	outf(e.stdout, "Service account %s revoked.\n", *id)
	return nil
}

func formatTime(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.UTC().Format("2006-01-02 15:04")
}
