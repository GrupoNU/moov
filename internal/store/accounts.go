package store

import (
	"context"
	"fmt"
)

const accountColumns = `id, email, imap_host, imap_port, imap_server_name,
	imap_username, imap_app_password, credential_state, state, created_at, updated_at`

// CreateAccount inserts an account and returns it with its assigned id.
//
// The credential fields may be empty: provisioning (E7) fills them in a second
// step, and an account with credential_state 'pending' is a legitimate state
// the sync engine simply does not act on yet.
func (s *Store) CreateAccount(ctx context.Context, a Account) (Account, error) {
	if a.IMAPPort == 0 {
		a.IMAPPort = 143
	}
	if a.State == "" {
		a.State = AccountActive
	}
	if a.CredentialState == "" {
		a.CredentialState = CredentialPending
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO accounts (email, imap_host, imap_port, imap_server_name,
		                      imap_username, imap_app_password, credential_state, state)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+accountColumns,
		a.Email, a.IMAPHost, a.IMAPPort, a.IMAPServerName,
		a.IMAPUsername, a.IMAPAppPassword, a.CredentialState, a.State)

	out, err := scanAccount(row)
	if err != nil {
		if isUniqueViolation(err) {
			return Account{}, fmt.Errorf("creating account %q: already exists", a.Email)
		}
		return Account{}, fmt.Errorf("creating account %q: %w", a.Email, err)
	}
	return out, nil
}

// GetAccount looks an account up by id.
func (s *Store) GetAccount(ctx context.Context, id int64) (Account, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+accountColumns+` FROM accounts WHERE id = $1`, id)
	a, err := scanAccount(row)
	if err != nil {
		return Account{}, notFound(err, fmt.Sprintf("account %d", id))
	}
	return a, nil
}

// GetAccountByEmail looks an account up by its address.
func (s *Store) GetAccountByEmail(ctx context.Context, email string) (Account, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+accountColumns+` FROM accounts WHERE email = $1`, email)
	a, err := scanAccount(row)
	if err != nil {
		return Account{}, notFound(err, fmt.Sprintf("account %q", email))
	}
	return a, nil
}

// ListAccounts returns every account, oldest first. The installation-wide list
// is small by construction (one row per mailbox owner), so it is not paged.
func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+accountColumns+` FROM accounts ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("listing accounts: %w", err)
	}
	defer rows.Close()

	var out []Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("listing accounts: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing accounts: %w", err)
	}
	return out, nil
}

// SetAccountCredentials stores the encrypted app password and marks the
// credentials active.
//
// The ciphertext is opaque here: this package neither encrypts nor decrypts
// (E7 owns AES-256-GCM and the master key). The user's own password is never
// an argument to anything in this package.
func (s *Store) SetAccountCredentials(ctx context.Context, accountID int64, username string, appPassword []byte) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE accounts
		   SET imap_username = $2, imap_app_password = $3,
		       credential_state = 'active', updated_at = now()
		 WHERE id = $1`, accountID, username, appPassword)
	if err != nil {
		return fmt.Errorf("setting credentials for account %d: %w", accountID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("setting credentials for account %d: %w", accountID, ErrNotFound)
	}
	return nil
}

// SetAccountCredentialState records that credentials became invalid or were
// revoked upstream, without touching the ciphertext itself.
func (s *Store) SetAccountCredentialState(ctx context.Context, accountID int64, state CredentialState) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE accounts SET credential_state = $2, updated_at = now() WHERE id = $1`,
		accountID, state)
	if err != nil {
		return fmt.Errorf("setting credential state for account %d: %w", accountID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("setting credential state for account %d: %w", accountID, ErrNotFound)
	}
	return nil
}

// SetAccountState pauses, resumes or disables an account.
func (s *Store) SetAccountState(ctx context.Context, accountID int64, state AccountState) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE accounts SET state = $2, updated_at = now() WHERE id = $1`,
		accountID, state)
	if err != nil {
		return fmt.Errorf("setting state for account %d: %w", accountID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("setting state for account %d: %w", accountID, ErrNotFound)
	}
	return nil
}

// DeleteAccount removes an account and, by cascade, its mailboxes, messages
// and message state.
//
// Blobs are NOT deleted: they are content-addressed and may be shared with
// another account. Their references go away with the messages, which drops the
// refcount to zero and makes them collectable by the blob GC after the grace
// period — which is exactly the path a blob should take, rather than being
// unlinked inside a transaction that might roll back.
func (s *Store) DeleteAccount(ctx context.Context, accountID int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, accountID)
	if err != nil {
		return fmt.Errorf("deleting account %d: %w", accountID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("deleting account %d: %w", accountID, ErrNotFound)
	}
	return nil
}

// scanner is the shared surface of pgx.Row and pgx.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanAccount(row scanner) (Account, error) {
	var a Account
	err := row.Scan(&a.ID, &a.Email, &a.IMAPHost, &a.IMAPPort, &a.IMAPServerName,
		&a.IMAPUsername, &a.IMAPAppPassword, &a.CredentialState, &a.State,
		&a.CreatedAt, &a.UpdatedAt)
	return a, err
}
