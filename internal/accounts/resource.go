package accounts

import (
	"time"

	"github.com/GrupoNU/moov/internal/mailcow"
	"github.com/GrupoNU/moov/internal/store"
)

// The account resource of contract §2.3 and its derived headline state.

// State is the derived headline a portal card shows.
type State string

// The states, in the precedence §2.3 fixes.
const (
	StateDeleting  State = "deleting"
	StateSuspended State = "suspended"
	StateReadOnly  State = "readonly"
	StateActive    State = "active"
)

// SyncState is the sync half of the resource.
type SyncState string

// The four sync states §2.3 defines. They describe MOOV's mirror, not the
// mailbox: "paused" is a suspended or deleting account, "error" a broken
// credential or an open breaker, "initial" an account with no checkpoint yet.
const (
	SyncInitial SyncState = "initial"
	SyncReady   SyncState = "ready"
	SyncPaused  SyncState = "paused"
	SyncError   SyncState = "error"
)

// Quota mirrors what Mailcow reports (real disk usage), never Moov's store.
type Quota struct {
	LimitMB   int
	UsedBytes int64
	Messages  int64
}

// Sync is what Moov's own store holds.
type Sync struct {
	State      SyncState
	LastSyncAt *time.Time
	Messages   int64
}

// Account is the resource §2.3 publishes. It is a value: the HTTP layer
// renders it and nothing mutates it after Service built it.
type Account struct {
	Address   string
	Domain    string
	Name      string
	State     State
	ReadOnly  bool
	Suspended bool
	Quota     Quota
	Limits    EffectiveLimits
	Sync      Sync

	LastAccessAt  *time.Time
	ReadOnlySince *time.Time
	SuspendedAt   *time.Time
	DeletingSince *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// EffectiveLimits are the limits as they APPLY, never as they were requested:
// a limit the account does not carry reads as the installation default, which
// is what the mailbox actually gets.
type EffectiveLimits struct {
	SendPerDay           int
	RecipientsPerMessage int
	AttachmentMB         int
}

// deriveState applies the precedence of §2.3: deleting > suspended >
// readonly > active. The booleans beside it stay the facts, so a suspended
// read-only account reports suspended AND readOnly:true, and resume can take
// it back to readonly without remembering anything.
func deriveState(a store.Account) State {
	switch {
	case a.DeletingSince != nil:
		return StateDeleting
	case a.Suspended:
		return StateSuspended
	case a.ReadOnly:
		return StateReadOnly
	default:
		return StateActive
	}
}

// effectiveLimits fills the installation defaults in for the columns the
// accounts API never wrote (an account provisioned by moovctl account add).
func effectiveLimits(a store.Account) EffectiveLimits {
	l := EffectiveLimits{
		SendPerDay:           DefaultSendPerDay,
		RecipientsPerMessage: DefaultRecipientsPerMessage,
		AttachmentMB:         DefaultAttachmentMB,
	}
	if a.SendPerDay != nil {
		l.SendPerDay = *a.SendPerDay
	}
	if a.RecipientsPerMessage != nil {
		l.RecipientsPerMessage = *a.RecipientsPerMessage
	}
	if a.AttachmentMB != nil {
		l.AttachmentMB = *a.AttachmentMB
	}
	return l
}

// syncStateOf maps the engine state and the store's sync summary onto the
// contract's four values.
//
// A suspended or deleting account reads `paused` regardless of what the
// engine column says, because both transitions set the engine state to
// disabled and "disabled" is not one of the contract's words. An account with
// no checkpoint yet is `initial`; one whose credential is broken is `error`.
func syncStateOf(a store.Account, sum store.AccountSyncSummary) SyncState {
	switch {
	case a.DeletingSince != nil, a.Suspended:
		return SyncPaused
	case a.CredentialState == store.CredentialInvalid, sum.BreakerOpen:
		return SyncError
	case !sum.EverSynced:
		return SyncInitial
	default:
		return SyncReady
	}
}

// buildResource assembles the published resource from the three sources it
// has: the account row (Moov's own facts), the sync summary (Moov's store)
// and the Mailcow mailbox (real disk usage). mb may be the zero value when
// Mailcow could not be read for a GET — quota then reports what Moov mirrored
// and zero usage, which is honest: Moov does not know the usage.
func buildResource(a store.Account, sum store.AccountSyncSummary, mb mailcow.Mailbox) Account {
	name := a.DisplayName
	if name == "" {
		name = mb.Name
	}
	quotaMB := a.QuotaMB
	if mb.Quota > 0 {
		quotaMB = int(mb.Quota / (1 << 20))
	}
	return Account{
		Address:       a.Email,
		Domain:        DomainOf(a.Email),
		Name:          name,
		State:         deriveState(a),
		ReadOnly:      a.ReadOnly,
		Suspended:     a.Suspended,
		Quota:         Quota{LimitMB: quotaMB, UsedBytes: mb.QuotaUsed, Messages: mb.Messages},
		Limits:        effectiveLimits(a),
		Sync:          Sync{State: syncStateOf(a, sum), LastSyncAt: sum.LastSyncAt, Messages: sum.Messages},
		LastAccessAt:  a.LastAccessAt,
		ReadOnlySince: a.ReadOnlySince,
		SuspendedAt:   a.SuspendedAt,
		DeletingSince: a.DeletingSince,
		CreatedAt:     a.CreatedAt,
		UpdatedAt:     a.UpdatedAt,
	}
}
