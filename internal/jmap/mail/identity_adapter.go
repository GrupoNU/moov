package mail

import (
	"context"
	"errors"
	"time"

	"github.com/GrupoNU/moov/internal/store"
)

// The store-backed IdentityStore: the only file in the JMAP surface that knows
// §6 Identity objects are rows in the identities table — the same confinement
// submission_adapter.go gives EmailSubmission.

// IdentityAdapter implements IdentityStore over the real store.
type IdentityAdapter struct {
	store    *store.Store
	notifier SubmissionNotifier
}

// NewIdentityAdapter builds the adapter.
//
// notifier may be nil. When set it is the same *sync.Broker the submission
// adapter uses, so saving a signature pushes an SSE StateChange and the user's
// OTHER sessions pick the new signature up without a reload — which is the
// behavior a Gmail-class client is measured against.
func NewIdentityAdapter(st *store.Store, notifier SubmissionNotifier) (*IdentityAdapter, error) {
	if st == nil {
		return nil, errors.New("mail: a store is required")
	}
	return &IdentityAdapter{store: st, notifier: notifier}, nil
}

var _ IdentityStore = (*IdentityAdapter)(nil)

// ListIdentities implements IdentityStore.
//
// An account with no identity row gets one materialized here rather than being
// served an empty list. That is the ONLY net for an account provisioned after
// migration 0006 ran, and it is deliberately here rather than in
// internal/provision: that package's AccountStore interface is documented as
// narrow on purpose ("the narrowness is the point"), and widening it to carry
// a JMAP-visible object would trade a real safety property for an
// optimization. The cost of materializing lazily is one extra query on the
// rare read that finds no row; the cost of an account with no identity is a
// mailbox that cannot send at all.
//
// EnsureDefaultIdentity is itself idempotent and concurrency-safe (ON CONFLICT
// against the partial unique index), so two sessions opening the same new
// account race to one row.
func (a *IdentityAdapter) ListIdentities(ctx context.Context, accountID int64) ([]IdentityRow, error) {
	rows, err := a.store.ListIdentities(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		row, err := a.store.EnsureDefaultIdentity(ctx, accountID)
		if err != nil {
			return nil, err
		}
		rows = []store.Identity{row}
	}
	return identityRows(rows), nil
}

// IdentityState implements IdentityStore — the same watermark-and-count
// grammar every other type's state uses (adapter.go stateFor).
func (a *IdentityAdapter) IdentityState(ctx context.Context, accountID int64) (string, error) {
	return identityStateString(ctx, a.store, accountID)
}

// IdentitiesChangedSince implements IdentityStore.
func (a *IdentityAdapter) IdentitiesChangedSince(ctx context.Context, accountID int64, since time.Time, limit int) ([]IdentityRow, error) {
	rows, err := a.store.IdentitiesChangedSince(ctx, accountID, since, limit)
	if err != nil {
		return nil, err
	}
	return identityRows(rows), nil
}

// UpdateIdentity implements IdentityStore.
func (a *IdentityAdapter) UpdateIdentity(ctx context.Context, accountID, id int64, patch IdentityPatch) (IdentityRow, error) {
	updated, err := a.store.UpdateIdentity(ctx, accountID, id, store.IdentityUpdate{
		Name:          patch.Name,
		TextSignature: patch.TextSignature,
		HTMLSignature: patch.HTMLSignature,
		ReplyTo:       storeAddressList(patch.ReplyTo),
		Bcc:           storeAddressList(patch.Bcc),
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return IdentityRow{}, ErrNotFound
		}
		return IdentityRow{}, err
	}
	if a.notifier != nil {
		a.notifier.Notify(accountID)
	}
	return identityRow(updated), nil
}

// identityStateString is the Identity type's state.
func identityStateString(ctx context.Context, st *store.Store, accountID int64) (string, error) {
	watermark, err := st.IdentityWatermark(ctx, accountID)
	if err != nil {
		return "", err
	}
	count, err := st.CountIdentities(ctx, accountID)
	if err != nil {
		return "", err
	}
	return stateFor(watermark, count), nil
}

// ---------------------------------------------------------------------------
// row mapping
// ---------------------------------------------------------------------------

func identityRows(in []store.Identity) []IdentityRow {
	out := make([]IdentityRow, 0, len(in))
	for _, r := range in {
		out = append(out, identityRow(r))
	}
	return out
}

func identityRow(r store.Identity) IdentityRow {
	return IdentityRow{
		ID:            r.ID,
		IsDefault:     r.IsDefault,
		Email:         r.Email,
		Name:          r.Name,
		ReplyTo:       jmapAddressList(r.ReplyTo),
		Bcc:           jmapAddressList(r.Bcc),
		TextSignature: r.TextSignature,
		HTMLSignature: r.HTMLSignature,
		UpdatedAt:     r.UpdatedAt,
	}
}

// jmapAddressList converts the store's address shape to this package's,
// preserving nil (the RFC's null) rather than normalizing it to an empty
// slice — the whole replyTo/bcc null distinction rests on it.
func jmapAddressList(in []store.EmailAddress) []EmailAddress {
	if in == nil {
		return nil
	}
	out := make([]EmailAddress, 0, len(in))
	for _, a := range in {
		out = append(out, EmailAddress{Name: a.Name, Email: a.Email})
	}
	return out
}

// storeAddressList converts back, preserving the three-state pointer: nil
// pointer (not named), pointer to nil (set to null), pointer to a list.
func storeAddressList(in *[]EmailAddress) *[]store.EmailAddress {
	if in == nil {
		return nil
	}
	if *in == nil {
		var none []store.EmailAddress
		return &none
	}
	out := make([]store.EmailAddress, 0, len(*in))
	for _, a := range *in {
		out = append(out, store.EmailAddress{Name: a.Name, Email: a.Email})
	}
	return &out
}

// IdentityState exposes the identity state on the SAME reader every other
// type's state comes from (deps.State, which jmaphttp's EventSource consults),
// so a pushed Identity state string equals the one Identity/get returns.
func (a *Adapter) IdentityState(ctx context.Context, accountID int64) (string, error) {
	return identityStateString(ctx, a.store, accountID)
}
