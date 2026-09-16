package provision

import (
	"context"
	"fmt"

	"github.com/GrupoNU/moov/internal/crypto"
	"github.com/GrupoNU/moov/internal/imap"
	"github.com/GrupoNU/moov/internal/mailcow"
	"github.com/GrupoNU/moov/internal/store"
)

// Reissue replaces an ALREADY PROVISIONED account's stored credential with a
// freshly minted app password carrying a narrower protocol set.
//
// It exists for the accounts API's read-only transition (epic M1, contract
// §2.4): F0 measured that Mailcow's smtp_access:0 does NOT stop submission
// AUTH, so the only real lock is a credential that was never granted SMTP.
// Re-issuing is therefore a product operation, not a workaround.
//
// It is a sibling of Provision rather than a branch inside it because it
// answers a different question. Provision starts from a HUMAN credential and
// proves the human owns the mailbox; Reissue starts from an account Moov has
// already provisioned and changes only the scope of what Moov holds. It needs
// no user password — there is none to ask for at this point in an account's
// life — and it must not create an account row that did not exist.
//
// What it keeps from Provision, because these are the properties that make a
// stored credential trustworthy:
//
//   - the minted password is proven by a real IMAP LOGIN before it replaces
//     anything, so a credential that Mailcow accepted but Dovecot will not is
//     caught here rather than at the next sync;
//   - it is sealed under the account's AAD before it is stored;
//   - a failure after the app password was created deletes it again, and
//     reports ErrOrphanedAppPassword when even that fails.
//
// The OLD app password is deliberately NOT deleted here: the caller knows its
// id (accounts.mailcow_app_password_id) and deletes it after this returns, so
// that a failure anywhere in this function leaves the account working on the
// credential it already had.
func (p *Provisioner) Reissue(ctx context.Context, email string, scopes []mailcow.Protocol) (Result, error) {
	if email == "" {
		return Result{}, fmt.Errorf("%w: Email is required", ErrInvalidRequest)
	}
	if len(scopes) == 0 {
		return Result{}, fmt.Errorf("%w: at least one protocol scope is required", ErrInvalidRequest)
	}

	log := p.log.With("mailbox", email)

	account, err := p.accounts.GetAccountByEmail(ctx, email)
	if err != nil {
		return Result{}, fmt.Errorf("reading the account to re-issue: %w", err)
	}

	generated, err := mailcow.GeneratePassword()
	if err != nil {
		return Result{}, fmt.Errorf("generating the app password: %w", err)
	}

	created, err := p.api.CreateAppPassword(ctx, mailcow.CreateAppPasswordRequest{
		Mailbox:  email,
		Password: generated,
		Scopes:   scopes,
	})
	if err != nil {
		return Result{}, fmt.Errorf("creating the app password: %w", err)
	}
	log.Info("app password re-issued", "app_password_id", created.ID,
		"app_password_name", created.Name, "scopes", scopes)

	cleanup := func(cause error) error {
		if created.ID == 0 {
			return cause
		}
		if delErr := p.api.DeleteAppPassword(context.WithoutCancel(ctx), created.ID); delErr != nil {
			log.Error("could not remove the re-issued app password after a failure; "+
				"it must be deleted by hand in the Mailcow UI",
				"app_password_id", created.ID, "delete_error", delErr)
			return fmt.Errorf("%w: id %d, name %q, on mailbox %s (delete it in the Mailcow UI): "+
				"re-issuing failed with: %w",
				ErrOrphanedAppPassword, created.ID, created.Name, email, cause)
		}
		return cause
	}

	// The new credential is proven before it replaces the working one.
	if err := p.validate.Validate(ctx, imap.Config{
		Host:               p.cfg.IMAPHost,
		Port:               p.cfg.IMAPPort,
		Username:           email,
		Password:           generated,
		TLSServerName:      p.cfg.IMAPServerName,
		InsecureSkipVerify: p.cfg.IMAPInsecureSkipVerify,
	}); err != nil {
		return Result{}, cleanup(fmt.Errorf("validating the re-issued credential: %w", err))
	}

	sealed, err := p.sealer.Seal([]byte(generated), crypto.AccountAAD(account.ID))
	if err != nil {
		return Result{}, cleanup(fmt.Errorf("sealing the re-issued app password: %w", err))
	}
	if err := p.accounts.SetAccountCredentials(ctx, account.ID, email, sealed); err != nil {
		return Result{}, cleanup(fmt.Errorf("storing the re-issued app password: %w", err))
	}

	account.IMAPUsername = email
	account.IMAPAppPassword = sealed
	account.CredentialState = store.CredentialActive

	return Result{
		Account:         account,
		AppPasswordID:   created.ID,
		AppPasswordName: created.Name,
	}, nil
}
