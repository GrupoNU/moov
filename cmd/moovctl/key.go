package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/GrupoNU/moov/internal/crypto"
	"github.com/GrupoNU/moov/internal/store"
)

func keyCommand(ctx context.Context, e *env, args []string) error {
	if len(args) == 0 {
		return usageErrorf("key needs a subcommand (generate, rotate)")
	}
	switch args[0] {
	case "generate":
		return keyGenerate(e, args[1:])
	case "rotate":
		return keyRotate(ctx, e, args[1:])
	default:
		return usageErrorf("unknown key subcommand %q (want generate or rotate)", args[0])
	}
}

// keyGenerate prints a fresh master key.
//
// The key goes to stdout and nowhere else: not to a file, not to a log, not
// into the repository. Writing it somewhere is the operator's decision, made
// with their own secret manager, and a CLI that helpfully dropped a master key
// into a file would be creating a copy nobody asked for.
func keyGenerate(e *env, args []string) error {
	fs := flag.NewFlagSet("key generate", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	id := fs.Uint("id", 1, "key id to label the output with (1-255)")
	bare := fs.Bool("bare", false, "print only the base64 key, without the id prefix or guidance")
	fs.Usage = func() {
		out(e.stderr, "Usage: moovctl key generate [-id N] [-bare]\n\n"+
			"Prints a new AES-256 master key. Nothing is written to disk.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if fs.NArg() != 0 {
		return usageErrorf("key generate takes no arguments")
	}
	if *id == 0 || *id > 255 {
		return usageErrorf("key id must be between 1 and 255, got %d", *id)
	}

	material, err := crypto.GenerateKey()
	if err != nil {
		return err
	}
	encoded := crypto.EncodeKey(material)

	if *bare {
		outln(e.stdout, encoded)
		return nil
	}

	// The guidance goes to stderr so that `moovctl key generate | ...` still
	// pipes only the key itself.
	outf(e.stdout, "%d:%s\n", *id, encoded)
	outf(e.stderr, `
Set this as %s (or put it in a file named by %s).

  %s="%d:%s"

Keep it outside the database: a database dump plus this key is a full
credential compromise, and a dump without it is useless. Losing it means every
provisioned account must be provisioned again.

To rotate later, put the NEW key first and keep the old one loaded:

  %s="2:<new key>,%d:<this key>"

then run "moovctl key rotate", and only remove the old key once
"moovctl account list" shows no account still using it.
`, crypto.EnvMasterKey, crypto.EnvMasterKeyFile,
		crypto.EnvMasterKey, *id, encoded, crypto.EnvMasterKey, *id)
	return nil
}

// keyRotate re-seals every stored credential under the primary key.
//
// It is step 3 of the rotation procedure documented in internal/crypto. The
// operation is idempotent and interruptible: a row already under the primary
// key is verified and skipped, so the command may be re-run after a failure or
// a Ctrl-C without doing damage.
func keyRotate(ctx context.Context, e *env, args []string) error {
	fs := flag.NewFlagSet("key rotate", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	dryRun := fs.Bool("dry-run", false, "report what would be re-sealed without writing anything")
	fs.Usage = func() {
		out(e.stderr, "Usage: moovctl key rotate [-dry-run]\n\n"+
			"Re-seals every stored credential under the primary key of MOOV_MASTER_KEY.\n"+
			"Load both the new and the old key before running this.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if fs.NArg() != 0 {
		return usageErrorf("key rotate takes no arguments")
	}

	keyring, err := crypto.LoadKeyring()
	if err != nil {
		return err
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

	primary := keyring.PrimaryID()
	outf(e.stdout, "Primary key is %d; keys loaded: %v.\n", primary, keyring.IDs())

	var resealed, alreadyCurrent, skipped, failed int
	for _, a := range accounts {
		if len(a.IMAPAppPassword) == 0 {
			// A pending account has no credential to rotate.
			skipped++
			continue
		}

		aad := crypto.AccountAAD(a.ID)
		envelope, changed, err := keyring.Rotate(a.IMAPAppPassword, aad)
		if err != nil {
			// One unreadable row must not stop the pass: the remaining
			// accounts are still rotatable, and stopping would leave the
			// deployment unable to drop the old key at all. Every failure is
			// reported and the command exits non-zero at the end.
			failed++
			outf(e.stderr, "  account %d (%s): %v\n", a.ID, a.Email, err)
			if errors.Is(err, crypto.ErrUnknownKey) {
				outf(e.stderr,
					"    this row needs a key that is not loaded; add it to %s and re-run\n",
					crypto.EnvMasterKey)
			}
			continue
		}
		if !changed {
			alreadyCurrent++
			continue
		}

		if *dryRun {
			outf(e.stdout, "  would re-seal account %d (%s)\n", a.ID, a.Email)
			resealed++
			continue
		}

		if err := st.SetAccountCredentials(ctx, a.ID, a.IMAPUsername, envelope); err != nil {
			failed++
			outf(e.stderr, "  account %d (%s): storing the re-sealed credential: %v\n",
				a.ID, a.Email, err)
			continue
		}
		resealed++
		e.logger.Debug("re-sealed credential", "account_id", a.ID, "key_id", primary)
	}

	verb := "Re-sealed"
	if *dryRun {
		verb = "Would re-seal"
	}
	outf(e.stdout, "%s %d credential(s); %d already current, %d without a credential, %d failed.\n",
		verb, resealed, alreadyCurrent, skipped, failed)

	if failed > 0 {
		return fmt.Errorf("%d credential(s) could not be rotated; the old key must stay loaded", failed)
	}
	if !*dryRun && resealed > 0 {
		outf(e.stdout,
			"Every credential is now under key %d. "+
				"Verify with \"moovctl account list\", then remove the old key from %s.\n",
			primary, crypto.EnvMasterKey)
	}
	return nil
}

// compile-time assertion that the store satisfies what key rotate needs, so a
// signature change in internal/store fails here rather than at runtime.
var _ interface {
	ListAccounts(context.Context) ([]store.Account, error)
	SetAccountCredentials(context.Context, int64, string, []byte) error
} = (*store.Store)(nil)
