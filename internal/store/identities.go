package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// The identities table (migration 0006): the stored form of RFC 8621 §6
// Identity objects.
//
// Everything here is deliberately typed. The JMAP layer never sees a jsonb
// column or a NULL — replyTo and bcc arrive and leave as []EmailAddress with
// a nil slice standing for the RFC's null, and the partial-update shape is an
// explicit struct of optional fields rather than a map the caller could put
// arbitrary column names into. That is the same package boundary every other
// store file keeps: no SQL, and no SQL-shaped values, cross it.
//
// # What this package does NOT do
//
// It does not sanitize html_signature. The sanitizer is a policy decision with
// a threat model attached and it lives in the JMAP layer (identity.go), for
// the same reason parser.SanitizeHook is declared and not implemented here:
// the store's job is to persist exactly what it was handed. The column
// comment records the invariant the caller is required to uphold, and
// identity.go's IdentityWriter is the only path that writes it.

// EmailAddress is the RFC 8621 §4.1.2.3 EmailAddress object, as stored in an
// identity's replyTo/bcc.
//
// Name is a plain string rather than a pointer even though the RFC types it
// "String|null": the two are indistinguishable to every consumer here (an
// absent name and an empty name both mean "render the address alone"), and
// collapsing them keeps a JSON round-trip through the jsonb column stable.
type EmailAddress struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

// Identity is one row of the identities table.
type Identity struct {
	ID        int64
	AccountID int64

	// IsDefault marks the identity that IS the account's mailbox. Exactly one
	// row per account carries it (migration 0006's partial unique index).
	IsDefault bool

	Email string
	Name  string

	// ReplyTo and Bcc are nil for the RFC's null — "this identity configures
	// none" — and non-nil for a configured list. An empty non-nil slice is
	// normalized to NULL on write, because an empty list configures nothing.
	ReplyTo []EmailAddress
	Bcc     []EmailAddress

	TextSignature string
	HTMLSignature string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// IdentityUpdate is a partial update: a nil field is "leave this column
// alone", which is exactly the RFC 8620 §5.3 PatchObject semantics the JMAP
// layer translates from.
//
// Email is absent by construction — §6 types it "(immutable)", so no update
// can ever name it and this struct gives a caller no way to try.
type IdentityUpdate struct {
	Name          *string
	TextSignature *string
	HTMLSignature *string

	// ReplyTo and Bcc are **[]EmailAddress so all three states the RFC
	// distinguishes are representable: nil (leave alone), a pointer to nil
	// (set to null), and a pointer to a list (set to that list).
	ReplyTo *[]EmailAddress
	Bcc     *[]EmailAddress
}

// IsEmpty reports whether the update would change nothing.
func (u IdentityUpdate) IsEmpty() bool {
	return u.Name == nil && u.TextSignature == nil && u.HTMLSignature == nil &&
		u.ReplyTo == nil && u.Bcc == nil
}

const identityColumns = `id, account_id, is_default, email, name, reply_to, bcc,
	text_signature, html_signature, created_at, updated_at`

// ListIdentities returns an account's identities, oldest first.
//
// The set is small by construction (one row today, a handful once alias
// identities land), so it is not paged — the same reasoning ListAccounts
// applies to the account list.
func (s *Store) ListIdentities(ctx context.Context, accountID int64) ([]Identity, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+identityColumns+`
		  FROM identities WHERE account_id = $1 ORDER BY id`, accountID)
	if err != nil {
		return nil, fmt.Errorf("listing identities of account %d: %w", accountID, err)
	}
	defer rows.Close()

	var out []Identity
	for rows.Next() {
		id, err := scanIdentity(rows)
		if err != nil {
			return nil, fmt.Errorf("listing identities of account %d: %w", accountID, err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing identities of account %d: %w", accountID, err)
	}
	return out, nil
}

// GetIdentity reads one identity, scoped to its account.
//
// The account_id predicate is not redundant with the primary key: it is the
// tenant check. A caller that hands in another account's identity id gets
// ErrNotFound rather than the row, which is what keeps the id space from
// working as a cross-account oracle.
func (s *Store) GetIdentity(ctx context.Context, accountID, id int64) (Identity, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+identityColumns+`
		  FROM identities WHERE account_id = $1 AND id = $2`, accountID, id)
	out, err := scanIdentity(row)
	if err != nil {
		return Identity{}, notFound(err, fmt.Sprintf("identity %d of account %d", id, accountID))
	}
	return out, nil
}

// DefaultIdentity returns the account's default identity — the one whose
// address IS the mailbox.
//
// It is the account's identity for every purpose that needs exactly one: the
// EmailSubmission fallback, and the identity a client that has never called
// Identity/get still names ("primary").
func (s *Store) DefaultIdentity(ctx context.Context, accountID int64) (Identity, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+identityColumns+`
		  FROM identities WHERE account_id = $1 AND is_default`, accountID)
	out, err := scanIdentity(row)
	if err != nil {
		return Identity{}, notFound(err, fmt.Sprintf("default identity of account %d", accountID))
	}
	return out, nil
}

// EnsureDefaultIdentity creates the account's default identity if it has none,
// and returns it either way.
//
// It exists because migration 0006's backfill can only reach accounts that
// existed WHEN IT RAN. An account provisioned afterwards needs its row too,
// and putting that in a trigger would hide a JMAP-visible object's creation
// inside the schema. Provisioning calls this; so does the read path, so a row
// that somehow went missing is repaired rather than served as a 404 on a
// mailbox the user can plainly see.
//
// ON CONFLICT DO NOTHING against the partial unique index makes it safe under
// concurrency: two callers racing to provision the same account produce one
// row, and the loser reads it back.
func (s *Store) EnsureDefaultIdentity(ctx context.Context, accountID int64) (Identity, error) {
	acct, err := s.GetAccount(ctx, accountID)
	if err != nil {
		return Identity{}, fmt.Errorf("ensuring the default identity: %w", err)
	}

	// The name defaults to the address, which is what the pre-0006 derived
	// identity reported — a new account and a backfilled one look identical.
	_, err = s.pool.Exec(ctx, `
		INSERT INTO identities (account_id, is_default, email, name)
		VALUES ($1, true, $2, $2)
		ON CONFLICT DO NOTHING`, accountID, acct.Email)
	if err != nil {
		return Identity{}, fmt.Errorf("ensuring the default identity of account %d: %w", accountID, err)
	}
	return s.DefaultIdentity(ctx, accountID)
}

// UpdateIdentity applies a partial update and returns the stored row.
//
// The whole update is one statement: COALESCE against a typed parameter per
// column, so an absent field re-writes the column's own value and a present
// one replaces it. That keeps the update atomic without a read-modify-write,
// which matters because two clients patching different properties of the same
// identity must not clobber each other.
//
// The one field COALESCE cannot express is a deliberate NULL (replyTo: null),
// so those two columns take a second boolean parameter that says "the caller
// named this property" — set-to-null is then distinguishable from absent.
func (s *Store) UpdateIdentity(ctx context.Context, accountID, id int64, u IdentityUpdate) (Identity, error) {
	if u.IsEmpty() {
		// A no-op update must still be a read, not a silent success: the
		// caller gets the current row and, importantly, ErrNotFound for an
		// id that does not exist.
		return s.GetIdentity(ctx, accountID, id)
	}

	replyTo, err := marshalAddresses(u.ReplyTo)
	if err != nil {
		return Identity{}, fmt.Errorf("updating identity %d: replyTo: %w", id, err)
	}
	bcc, err := marshalAddresses(u.Bcc)
	if err != nil {
		return Identity{}, fmt.Errorf("updating identity %d: bcc: %w", id, err)
	}

	row := s.pool.QueryRow(ctx, `
		UPDATE identities SET
		    name           = COALESCE($3, name),
		    text_signature = COALESCE($4, text_signature),
		    html_signature = COALESCE($5, html_signature),
		    reply_to       = CASE WHEN $6 THEN $7::jsonb ELSE reply_to END,
		    bcc            = CASE WHEN $8 THEN $9::jsonb ELSE bcc END,
		    updated_at     = now()
		 WHERE account_id = $1 AND id = $2
		 RETURNING `+identityColumns,
		accountID, id,
		u.Name, u.TextSignature, u.HTMLSignature,
		u.ReplyTo != nil, replyTo,
		u.Bcc != nil, bcc)

	out, err := scanIdentity(row)
	if err != nil {
		return Identity{}, notFound(err, fmt.Sprintf("identity %d of account %d", id, accountID))
	}
	return out, nil
}

// IdentityWatermark is max(updated_at) over an account's identities, or the
// zero time when it has none — the same watermark grammar every other type's
// state string is built from (adapter.go stateFor).
func (s *Store) IdentityWatermark(ctx context.Context, accountID int64) (time.Time, error) {
	var t *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT max(updated_at) FROM identities WHERE account_id = $1`, accountID).Scan(&t)
	if err != nil {
		return time.Time{}, fmt.Errorf("reading the identity watermark of account %d: %w", accountID, err)
	}
	if t == nil {
		return time.Time{}, nil
	}
	return *t, nil
}

// CountIdentities is the row count that rides alongside the watermark in the
// state string. It is what makes a DESTROY move the state even though no
// surviving row's updated_at changed.
func (s *Store) CountIdentities(ctx context.Context, accountID int64) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM identities WHERE account_id = $1`, accountID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting the identities of account %d: %w", accountID, err)
	}
	return n, nil
}

// IdentitiesChangedSince returns the identities whose updated_at is strictly
// after since, oldest change first — the /changes feed.
//
// Strictly after, not at-or-after, because the cursor a client holds IS the
// watermark of the changes it already saw; including that instant again would
// replay the last change on every poll.
func (s *Store) IdentitiesChangedSince(ctx context.Context, accountID int64, since time.Time, limit int) ([]Identity, error) {
	if limit <= 0 {
		limit = 256
	}
	rows, err := s.pool.Query(ctx, `SELECT `+identityColumns+`
		  FROM identities
		 WHERE account_id = $1 AND updated_at > $2
		 ORDER BY updated_at, id
		 LIMIT $3`, accountID, since, limit)
	if err != nil {
		return nil, fmt.Errorf("listing changed identities of account %d: %w", accountID, err)
	}
	defer rows.Close()

	var out []Identity
	for rows.Next() {
		id, err := scanIdentity(rows)
		if err != nil {
			return nil, fmt.Errorf("listing changed identities of account %d: %w", accountID, err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing changed identities of account %d: %w", accountID, err)
	}
	return out, nil
}

// marshalAddresses renders an optional address list for the jsonb columns.
//
// The three states map as: nil pointer -> nil (the CASE leaves the column
// alone), pointer to an empty-or-nil list -> nil (SQL NULL, the RFC's null),
// pointer to a list -> its JSON array.
func marshalAddresses(list *[]EmailAddress) ([]byte, error) {
	if list == nil || len(*list) == 0 {
		return nil, nil
	}
	return json.Marshal(*list)
}

// unmarshalAddresses reads a jsonb address column back.
//
// A malformed column is an error rather than a silent empty list: the only
// writer is this package, so bad JSON means corruption, and serving an
// identity with its Bcc quietly dropped would send mail to fewer people than
// the user configured.
func unmarshalAddresses(raw []byte, what string) ([]EmailAddress, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var out []EmailAddress
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decoding the stored %s: %w", what, err)
	}
	return out, nil
}

func scanIdentity(row pgx.Row) (Identity, error) {
	var id Identity
	var replyTo, bcc []byte
	if err := row.Scan(&id.ID, &id.AccountID, &id.IsDefault, &id.Email, &id.Name,
		&replyTo, &bcc, &id.TextSignature, &id.HTMLSignature,
		&id.CreatedAt, &id.UpdatedAt); err != nil {
		return Identity{}, err
	}
	var err error
	if id.ReplyTo, err = unmarshalAddresses(replyTo, "replyTo"); err != nil {
		return Identity{}, err
	}
	if id.Bcc, err = unmarshalAddresses(bcc, "bcc"); err != nil {
		return Identity{}, err
	}
	return id, nil
}
