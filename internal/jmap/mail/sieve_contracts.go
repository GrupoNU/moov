package mail

import (
	"context"
	"errors"
	"time"
)

// The E6 contracts: what the SieveScript, VacationResponse, FilterRule,
// Forwarding and ForwardingAddress handlers need, in this package's own
// types — the same boundary discipline contracts.go states. The store-backed
// implementation is sieve_adapter.go; the fakes in the tests drive the
// handlers without a ManageSieve server or PostgreSQL.

// Sentinel errors of the sieve surface, mapped by the handlers onto the
// RFC 9661 §2.4 SetError vocabulary.
var (
	// ErrSieveScriptActive maps to the §2.4 sieveIsActive SetError: the
	// active script cannot be destroyed without a prior deactivation.
	ErrSieveScriptActive = errors.New("mail: the sieve script is active")

	// ErrSieveManaged marks an operation on the server-managed script that
	// RFC 9661 §4 forbids (update/destroy of the script materializing the
	// VacationResponse): mapped to a forbidden SetError.
	ErrSieveManaged = errors.New("mail: the moov script is server-managed")

	// ErrForwardingInUse refuses destroying a forwarding address the current
	// script still redirects to.
	ErrForwardingInUse = errors.New("mail: the forwarding address is referenced by the active configuration")

	// ErrForwardingExists refuses a duplicate forwarding address.
	ErrForwardingExists = errors.New("mail: the forwarding address already exists")

	// ErrTokenInvalid is the single refusal for a verification token that is
	// wrong, expired or foreign — indistinguishable on purpose, the same
	// no-oracle rule the HTTP tokens follow.
	ErrTokenInvalid = errors.New("mail: invalid or expired verification token")
)

// SieveInvalidError carries the server's diagnostic for content that failed
// Sieve validation — the text RFC 9661's invalidSieve SetError shows the
// user, line numbers included.
type SieveInvalidError struct {
	Description string
}

func (e *SieveInvalidError) Error() string { return "mail: invalid sieve: " + e.Description }

// SieveNameTakenError maps to the RFC 9661 §2.4 alreadyExists SetError,
// which MUST carry the existing script's id.
type SieveNameTakenError struct {
	ExistingID int64
}

func (e *SieveNameTakenError) Error() string {
	return "mail: a sieve script with that name already exists"
}

// SieveScriptInfo is one stored script as SieveScript/get serves it.
type SieveScriptInfo struct {
	// ID is the ledger id (stable across renames, RFC 9661 §2.1); the wire
	// spelling is EncodeSieveScriptID.
	ID     int64
	Name   string
	Active bool

	// BlobID is the sha256 hex of the script content, downloadable through
	// the standard blob route (the account holds a pin reference).
	BlobID string
	Size   int64
}

// SieveStore is the RFC 9661 surface as the handlers see it.
type SieveStore interface {
	// ListScripts returns every stored script, content pinned into the blob
	// store so the returned BlobIDs resolve at the download route.
	ListScripts(ctx context.Context, accountID int64) ([]SieveScriptInfo, error)

	// ManagedScriptID resolves the ledger id of the Moov-managed script, ok
	// false when it does not exist yet. The handlers use it to enforce the
	// RFC 9661 §4 protection.
	ManagedScriptID(ctx context.Context, accountID int64) (int64, bool, error)

	// CreateScript stores a new script. The server validates the content;
	// invalid content is a *SieveInvalidError, a taken name ErrSieveNameTaken.
	CreateScript(ctx context.Context, accountID int64, name string, content []byte) (SieveScriptInfo, error)

	// UpdateScript renames and/or replaces content (nil content keeps it).
	UpdateScript(ctx context.Context, accountID, id int64, newName *string, content []byte) (SieveScriptInfo, error)

	// DestroyScript removes a stored script. The active one answers
	// ErrSieveScriptActive (§2.4: deactivate first, in a separate call).
	DestroyScript(ctx context.Context, accountID, id int64) error

	// ActivateScript makes one script active; id 0 deactivates all
	// (onSuccessDeactivateScript).
	ActivateScript(ctx context.Context, accountID, id int64) error

	// ValidateScript checks content without storing (§2.6 / CHECKSCRIPT).
	// Invalid content is a *SieveInvalidError; nil means valid.
	ValidateScript(ctx context.Context, accountID int64, content []byte) error

	// CheckRedirectPolicy enforces the GC-4 rule on raw content: every
	// redirect target must be a verified forwarding address. It fails
	// CLOSED — content whose redirects cannot be read is refused.
	CheckRedirectPolicy(ctx context.Context, accountID int64, content []byte) error

	// SieveState is the SieveScript state cursor.
	SieveState(ctx context.Context, accountID int64) (string, error)
}

// VacationValue is the RFC 8621 §8 singleton as the handlers see it.
// Pointers carry the §8 String|null / UTCDate|null distinction.
type VacationValue struct {
	IsEnabled bool
	FromDate  *time.Time
	ToDate    *time.Time
	Subject   *string
	TextBody  *string
	HTMLBody  *string
}

// VacationStore reads and writes the vacation section of the managed script.
type VacationStore interface {
	// GetVacation returns the singleton. An account that never configured
	// one gets the all-null, disabled object §8 describes.
	GetVacation(ctx context.Context, accountID int64) (VacationValue, error)

	// SetVacation stores the COMPLETE object (read-patch-write is the
	// handler's job) and materializes it into the managed script, performing
	// the takeover-with-backup dance when another script holds the active
	// slot. The HTMLBody arrives ALREADY SANITIZED from the handler.
	SetVacation(ctx context.Context, accountID int64, v VacationValue) error

	// VacationState is the singleton's state cursor.
	VacationState(ctx context.Context, accountID int64) (string, error)
}

// FilterRuleValue is one rule of the vendor surface — the wire face of
// sieve.Rule (the adapter maps 1:1; a test pins that no field is dropped).
type FilterRuleValue struct {
	ID      string
	Name    string
	Type    string // "filter" | "blocked" | "neverSpam"
	Enabled bool

	From          []string
	To            []string
	Subject       []string
	SizeOver      int64
	SizeUnder     int64
	HasAttachment *bool

	MoveTo   string
	Labels   []string
	MarkRead bool
	Star     bool
	Forward  string
	Delete   bool
	Stop     bool
}

// ForwardAllValue is the Forwarding singleton (settings-level forward-all).
type ForwardAllValue struct {
	Enabled     bool
	Address     string
	Disposition string // "keep" | "archive"
}

// FilterConfig is the whole rule surface read in one piece.
type FilterConfig struct {
	Rules      []FilterRuleValue
	ForwardAll ForwardAllValue

	// ScriptActive reports whether the Moov-managed script currently holds
	// the account's active slot — the honesty bit: when another script
	// (hand-written, SOGo's, Bulwark's) is active, these rules exist but do
	// not filter mail, and the UI must say so instead of pretending.
	ScriptActive bool
}

// FilterStore reads and writes the rule model in the managed script.
type FilterStore interface {
	GetFilters(ctx context.Context, accountID int64) (FilterConfig, error)

	// PutFilters stores the complete rule set and forward-all recipe,
	// regenerates the script and activates it (takeover with backup when a
	// foreign script was active). Validation errors arrive as
	// *SieveInvalidError with the model's problem list.
	PutFilters(ctx context.Context, accountID int64, rules []FilterRuleValue, forwardAll ForwardAllValue) error

	FiltersState(ctx context.Context, accountID int64) (string, error)
}

// ForwardingAddressValue is one destination and its verification state.
type ForwardingAddressValue struct {
	ID         int64
	Email      string
	State      string // "pending" | "accepted"
	VerifiedAt *time.Time
}

// ForwardingStore manages the verified-destination ledger and its
// verification flow.
type ForwardingStore interface {
	ListForwardingAddresses(ctx context.Context, accountID int64) ([]ForwardingAddressValue, error)

	// CreateForwardingAddress inserts the pending row, mints a token and
	// sends the verification mail through the server's own sending path. If
	// the mail cannot be sent the row is not left behind.
	CreateForwardingAddress(ctx context.Context, accountID int64, email string) (ForwardingAddressValue, error)

	// DestroyForwardingAddress removes a destination. One that the current
	// configuration still redirects to answers ErrForwardingInUse.
	DestroyForwardingAddress(ctx context.Context, accountID, id int64) error

	// VerifyForwarding consumes a token (the authenticated HTTP GET route's
	// backend) and flips the row to accepted, returning the address.
	VerifyForwarding(ctx context.Context, accountID int64, token string) (string, error)

	ForwardingState(ctx context.Context, accountID int64) (string, error)
}

// ForwardingTokens mints and verifies the verification tokens. Implemented
// in cmd/moovd over the existing master keyring (crypto.Keyring seals
// address+expiry bound to the account by AAD) — the GC-4 rule of reusing the
// secret infrastructure rather than inventing a scheme.
type ForwardingTokens interface {
	Mint(accountID int64, email string, expires time.Time) (string, error)
	// Verify returns the sealed address; any failure is ErrTokenInvalid.
	Verify(accountID int64, token string) (string, error)
}

// VerificationMailer sends the verification mail. Implemented in cmd/moovd
// over the same SMTP transport the outbox uses (same credentials, same
// submission path), synchronously — the failure surfaces to the settings UI
// as the create's error instead of dying in a background queue.
type VerificationMailer interface {
	SendVerification(ctx context.Context, accountID int64, to, token string, expires time.Time) error
}

// SieveObserver counts what the server can honestly count (E6 metrics):
// script pushes and verification mails. Vacation REPLIES are deliberately
// not here — Dovecot sends them, this server never sees one.
type SieveObserver interface {
	ScriptPushed(result string)         // "ok" | "error"
	VerificationMailSent(result string) // "sent" | "failed"
	VacationConfigured(enabled bool)    // one call per successful VacationResponse/set
}

// QuotaValue is one RFC 9425 Quota object's data, read live from Dovecot.
type QuotaValue struct {
	// Name is the quota root name as the server reports it.
	Name string
	// ResourceType is "octets" (STORAGE) or "count" (MESSAGE) — §3.2.
	ResourceType string
	Used         uint64
	HardLimit    uint64
}

// QuotaReader reads the account's quota over IMAP (GETQUOTAROOT INBOX). An
// account without quota limits returns an empty slice — no objects, not
// fabricated ones.
type QuotaReader interface {
	ReadQuota(ctx context.Context, accountID int64) ([]QuotaValue, error)
}
