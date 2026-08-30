package mail

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/GrupoNU/moov/internal/blob"
	"github.com/GrupoNU/moov/internal/sieve"
	"github.com/GrupoNU/moov/internal/store"
)

// SieveAdapter implements SieveStore, VacationStore, FilterStore and
// ForwardingStore over the real dependencies: a per-account ManageSieve
// connection (dialed per operation — this is settings-frequency traffic, and
// a connection cache would be state to invalidate for no measured win), the
// store's two E6 ledgers, and the blob store for script content.
//
// Dovecot remains the source of truth throughout: every read starts at
// LISTSCRIPTS, and the only durable Moov-side facts are the id ledger
// (reconstructible) and the forwarding verifications (security facts that
// must not live in a user-editable script).
type SieveAdapter struct {
	store   *store.Store
	blobs   *blob.Store
	connect SieveConnector
	tokens  ForwardingTokens
	mailer  VerificationMailer

	// notifier, when set, pushes an SSE StateChange after every write —
	// the same seam the identity and preference adapters use.
	notifier SubmissionNotifier

	// observer, when set, feeds the E6 metrics.
	observer SieveObserver

	log *slog.Logger
}

// SieveConnector opens a connected ManageSieve client for one account. It is
// cmd/moovd's seam, exactly like the sync engine's Connector: this package
// can never decrypt credentials.
type SieveConnector func(ctx context.Context, account store.Account) (sieve.Client, error)

// SieveAdapterConfig wires a SieveAdapter.
type SieveAdapterConfig struct {
	Store    *store.Store
	Blobs    *blob.Store
	Connect  SieveConnector
	Tokens   ForwardingTokens
	Mailer   VerificationMailer
	Notifier SubmissionNotifier
	Observer SieveObserver
	Logger   *slog.Logger
}

// NewSieveAdapter builds the adapter. Store, Blobs and Connect are required;
// Tokens and Mailer only when the forwarding surface is mounted.
func NewSieveAdapter(cfg SieveAdapterConfig) (*SieveAdapter, error) {
	if cfg.Store == nil || cfg.Blobs == nil || cfg.Connect == nil {
		return nil, errors.New("mail: SieveAdapter requires Store, Blobs and Connect")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &SieveAdapter{
		store:    cfg.Store,
		blobs:    cfg.Blobs,
		connect:  cfg.Connect,
		tokens:   cfg.Tokens,
		mailer:   cfg.Mailer,
		notifier: cfg.Notifier,
		observer: cfg.Observer,
		log:      log.With("component", "sieve-adapter"),
	}, nil
}

var (
	_ SieveStore      = (*SieveAdapter)(nil)
	_ VacationStore   = (*SieveAdapter)(nil)
	_ FilterStore     = (*SieveAdapter)(nil)
	_ ForwardingStore = (*SieveAdapter)(nil)
)

// forwardingTokenTTL is how long a verification token is honored. Three days:
// long enough for a destination owner in another timezone to relay the code,
// short enough that a forgotten pending row does not stay actionable for
// weeks. Documented rather than sourced — Gmail publishes no figure.
const forwardingTokenTTL = 72 * time.Hour

// maxForwardingAddresses bounds the ledger per account.
const maxForwardingAddresses = 20

// withClient dials the account's ManageSieve endpoint, runs fn, and closes.
func (a *SieveAdapter) withClient(ctx context.Context, accountID int64, fn func(sieve.Client) error) error {
	account, err := a.store.GetAccount(ctx, accountID)
	if err != nil {
		return fmt.Errorf("loading account %d: %w", accountID, err)
	}
	c, err := a.connect(ctx, account)
	if err != nil {
		return fmt.Errorf("connecting managesieve for account %d: %w", accountID, err)
	}
	defer func() {
		if cerr := c.Close(); cerr != nil {
			a.log.Debug("closing managesieve connection", "error", cerr)
		}
	}()
	return fn(c)
}

// generateEnv builds the per-account generation environment: role folders
// from the store (falling back to the Mailcow defaults when a role is not
// mapped) and the accepted forwarding set — THE enforcement input.
func (a *SieveAdapter) generateEnv(ctx context.Context, accountID int64) (sieve.GenerateEnv, error) {
	env := sieve.GenerateEnv{}
	if mb, err := a.store.GetMailboxByRole(ctx, accountID, store.RoleJunk); err == nil {
		env.JunkFolder = mb.Name
	}
	if mb, err := a.store.GetMailboxByRole(ctx, accountID, store.RoleTrash); err == nil {
		env.TrashFolder = mb.Name
	}
	if mb, err := a.store.GetMailboxByRole(ctx, accountID, store.RoleArchive); err == nil {
		env.ArchiveFolder = mb.Name
	}
	verified, err := a.store.AcceptedForwardingAddresses(ctx, accountID)
	if err != nil {
		return env, err
	}
	env.VerifiedForward = verified
	return env, nil
}

// ---------------------------------------------------------------------------
// SieveStore (RFC 9661 backing)
// ---------------------------------------------------------------------------

// ListScripts implements SieveStore: LISTSCRIPTS + GETSCRIPT each, content
// pinned into the blob store, ledger reconciled.
func (a *SieveAdapter) ListScripts(ctx context.Context, accountID int64) ([]SieveScriptInfo, error) {
	var out []SieveScriptInfo
	err := a.withClient(ctx, accountID, func(c sieve.Client) error {
		scripts, err := c.ListScripts(ctx)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(scripts))
		activeByName := map[string]bool{}
		contentByName := map[string][]byte{}
		for _, s := range scripts {
			names = append(names, s.Name)
			activeByName[s.Name] = s.Active
			content, err := c.GetScript(ctx, s.Name)
			if err != nil {
				return fmt.Errorf("reading script %q: %w", s.Name, err)
			}
			contentByName[s.Name] = content
		}

		rows, err := a.store.SyncSieveScripts(ctx, accountID, names)
		if err != nil {
			return err
		}
		for _, row := range rows {
			content := contentByName[row.Name]
			blobID, size, err := a.pinScriptContent(ctx, accountID, row, content)
			if err != nil {
				return err
			}
			out = append(out, SieveScriptInfo{
				ID:     row.ID,
				Name:   row.Name,
				Active: activeByName[row.Name],
				BlobID: blobID,
				Size:   size,
			})
		}
		return nil
	})
	return out, err
}

// pinScriptContent stores content into the blob store under the account's
// pin (the same retention uploads get) and records the digest in the ledger.
func (a *SieveAdapter) pinScriptContent(ctx context.Context, accountID int64, row store.SieveScriptRow, content []byte) (string, int64, error) {
	hash, size, err := a.blobs.Put(ctx, bytes.NewReader(content))
	if err != nil {
		return "", 0, fmt.Errorf("storing script %q content: %w", row.Name, err)
	}
	if err := a.blobs.AddRefTx(ctx, hash, accountID, blob.OwnerPin, accountID); err != nil {
		return "", 0, fmt.Errorf("pinning script %q content: %w", row.Name, err)
	}
	if err := a.store.SetSieveScriptSHA(ctx, accountID, row.ID, hash.String()); err != nil {
		return "", 0, err
	}
	return hash.String(), size, nil
}

// ManagedScriptID implements SieveStore: a read-only ledger lookup (no
// pruning, no upsert — the managed script legitimately may not exist yet).
func (a *SieveAdapter) ManagedScriptID(ctx context.Context, accountID int64) (int64, bool, error) {
	row, err := a.store.GetSieveScriptByName(ctx, accountID, sieve.ManagedScriptName)
	if errors.Is(err, store.ErrNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return row.ID, true, nil
}

// CreateScript implements SieveStore.
func (a *SieveAdapter) CreateScript(ctx context.Context, accountID int64, name string, content []byte) (SieveScriptInfo, error) {
	var info SieveScriptInfo
	err := a.withClient(ctx, accountID, func(c sieve.Client) error {
		// PUTSCRIPT replaces silently; RFC 9661 §2.4 wants alreadyExists for
		// a create over a taken name, so existence is checked first.
		scripts, err := c.ListScripts(ctx)
		if err != nil {
			return err
		}
		for _, s := range scripts {
			if s.Name == name {
				// The script exists on the server, so upserting its ledger
				// row is correct and gives §2.4's mandatory existingId.
				row, uerr := a.store.UpsertSieveScript(ctx, accountID, name)
				if uerr != nil {
					return uerr
				}
				return &SieveNameTakenError{ExistingID: row.ID}
			}
		}
		if name == sieve.ManagedScriptName {
			// Reserved: the managed script is only ever written through the
			// vacation/filter surfaces (RFC 9661 §4 protection).
			return ErrSieveManaged
		}
		if _, err := c.PutScript(ctx, name, content); err != nil {
			return mapSieveError(err)
		}
		row, err := a.store.UpsertSieveScript(ctx, accountID, name)
		if err != nil {
			return err
		}
		blobID, size, err := a.pinScriptContent(ctx, accountID, row, content)
		if err != nil {
			return err
		}
		info = SieveScriptInfo{ID: row.ID, Name: row.Name, BlobID: blobID, Size: size}
		return nil
	})
	if err == nil {
		a.notify(accountID)
	}
	return info, err
}

// UpdateScript implements SieveStore.
func (a *SieveAdapter) UpdateScript(ctx context.Context, accountID, id int64, newName *string, content []byte) (SieveScriptInfo, error) {
	var info SieveScriptInfo
	err := a.withClient(ctx, accountID, func(c sieve.Client) error {
		row, err := a.store.GetSieveScript(ctx, accountID, id)
		if err != nil {
			return err
		}
		if row.Name == sieve.ManagedScriptName {
			return ErrSieveManaged
		}
		name := row.Name
		if newName != nil && *newName != name {
			if *newName == sieve.ManagedScriptName {
				return ErrSieveManaged
			}
			if err := c.RenameScript(ctx, name, *newName); err != nil {
				if errors.Is(err, sieve.ErrScriptExists) {
					if existing, gerr := a.store.UpsertSieveScript(ctx, accountID, *newName); gerr == nil {
						return &SieveNameTakenError{ExistingID: existing.ID}
					}
				}
				return mapSieveError(err)
			}
			if err := a.store.RenameSieveScript(ctx, accountID, id, *newName); err != nil {
				return err
			}
			name = *newName
		}
		if content != nil {
			if _, err := c.PutScript(ctx, name, content); err != nil {
				return mapSieveError(err)
			}
		} else {
			content, err = c.GetScript(ctx, name)
			if err != nil {
				return mapSieveError(err)
			}
		}
		row.Name = name
		blobID, size, err := a.pinScriptContent(ctx, accountID, row, content)
		if err != nil {
			return err
		}
		active := false
		if scripts, err := c.ListScripts(ctx); err == nil {
			for _, s := range scripts {
				if s.Name == name {
					active = s.Active
				}
			}
		}
		info = SieveScriptInfo{ID: id, Name: name, Active: active, BlobID: blobID, Size: size}
		return nil
	})
	if err == nil {
		a.notify(accountID)
	}
	return info, err
}

// DestroyScript implements SieveStore.
func (a *SieveAdapter) DestroyScript(ctx context.Context, accountID, id int64) error {
	err := a.withClient(ctx, accountID, func(c sieve.Client) error {
		row, err := a.store.GetSieveScript(ctx, accountID, id)
		if err != nil {
			return err
		}
		if row.Name == sieve.ManagedScriptName {
			return ErrSieveManaged
		}
		if err := c.DeleteScript(ctx, row.Name); err != nil {
			return mapSieveError(err)
		}
		return a.store.DeleteSieveScript(ctx, accountID, id)
	})
	if err == nil {
		a.notify(accountID)
	}
	return err
}

// ActivateScript implements SieveStore.
func (a *SieveAdapter) ActivateScript(ctx context.Context, accountID, id int64) error {
	err := a.withClient(ctx, accountID, func(c sieve.Client) error {
		if id == 0 {
			return c.SetActive(ctx, "")
		}
		row, err := a.store.GetSieveScript(ctx, accountID, id)
		if err != nil {
			return err
		}
		return mapSieveError(c.SetActive(ctx, row.Name))
	})
	if err == nil {
		a.notify(accountID)
	}
	return err
}

// ValidateScript implements SieveStore (CHECKSCRIPT).
func (a *SieveAdapter) ValidateScript(ctx context.Context, accountID int64, content []byte) error {
	return a.withClient(ctx, accountID, func(c sieve.Client) error {
		_, err := c.CheckScript(ctx, content)
		return mapSieveError(err)
	})
}

// CheckRedirectPolicy implements SieveStore. Fail-closed: an unscannable
// script is refused, because "could not read the redirects" and "may forward
// anywhere" must never be the same outcome.
func (a *SieveAdapter) CheckRedirectPolicy(ctx context.Context, accountID int64, content []byte) error {
	targets, err := sieve.ScanRedirects(content)
	if err != nil {
		return fmt.Errorf("the script's redirect commands could not be verified (%w); "+
			"forwarding requires verified addresses", err)
	}
	if len(targets) == 0 {
		return nil
	}
	verified, err := a.store.AcceptedForwardingAddresses(ctx, accountID)
	if err != nil {
		return err
	}
	var bad []string
	for _, t := range targets {
		if !verified[strings.ToLower(t)] {
			bad = append(bad, t)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("the script redirects to unverified addresses (%s); "+
			"verify them first in Settings > Forwarding", strings.Join(bad, ", "))
	}
	return nil
}

// SieveState implements SieveStore: "<nanos>-<count>" over the ledger.
func (a *SieveAdapter) SieveState(ctx context.Context, accountID int64) (string, error) {
	wm, err := a.store.SieveScriptWatermark(ctx, accountID)
	if err != nil {
		return "", err
	}
	n, err := a.store.CountSieveScripts(ctx, accountID)
	if err != nil {
		return "", err
	}
	return stateFor(wm, n), nil
}

// mapSieveError converts internal/sieve's vocabulary into this package's.
func mapSieveError(err error) error {
	if err == nil {
		return nil
	}
	var scriptErr *sieve.ScriptError
	if errors.As(err, &scriptErr) {
		return &SieveInvalidError{Description: scriptErr.Message}
	}
	var quotaErr *sieve.QuotaError
	if errors.As(err, &quotaErr) {
		// RFC 9661 §2.4 splits the QUOTA family: MAXSIZE is tooLarge,
		// everything else overQuota.
		if quotaErr.Code == "QUOTA/MAXSIZE" {
			return errSieveTooLarge
		}
		return errSieveOverQuota
	}
	switch {
	case errors.Is(err, sieve.ErrScriptActive):
		return ErrSieveScriptActive
	case errors.Is(err, sieve.ErrScriptExists):
		return &SieveNameTakenError{}
	case errors.Is(err, sieve.ErrScriptNotFound):
		return ErrNotFound
	}
	return err
}

// The QUOTA refusal family, split per RFC 9661 §2.4 (tooLarge vs overQuota).
var (
	errSieveTooLarge  = errors.New("mail: the script exceeds the server's size limit")
	errSieveOverQuota = errors.New("mail: the script would exceed the server's script quota")
)

// ---------------------------------------------------------------------------
// managed model plumbing shared by vacation and filters
// ---------------------------------------------------------------------------

// readManaged pulls the managed state over one connection.
func (a *SieveAdapter) readManaged(ctx context.Context, accountID int64) (*sieve.ManagedState, sieve.GenerateEnv, error) {
	env, err := a.generateEnv(ctx, accountID)
	if err != nil {
		return nil, env, err
	}
	var state *sieve.ManagedState
	err = a.withClient(ctx, accountID, func(c sieve.Client) error {
		mgr := sieve.NewManager(c, a.log)
		s, rerr := mgr.Read(ctx, env)
		state = s
		return rerr
	})
	return state, env, err
}

// pushManaged mutates the managed model under one connection: read, apply
// mutate, push (with the manager's takeover-and-backup ladder), reconcile
// the ledger, notify.
func (a *SieveAdapter) pushManaged(ctx context.Context, accountID int64, mutate func(model *sieve.Script) error) error {
	env, err := a.generateEnv(ctx, accountID)
	if err != nil {
		return err
	}
	err = a.withClient(ctx, accountID, func(c sieve.Client) error {
		mgr := sieve.NewManager(c, a.log)
		state, err := mgr.Read(ctx, env)
		if err != nil {
			return err
		}
		if err := mutate(state.Model); err != nil {
			return err
		}
		warnings, err := mgr.Push(ctx, state.Model, env)
		if err != nil {
			var verr *sieve.ValidationError
			if errors.As(err, &verr) {
				return &SieveInvalidError{Description: strings.Join(verr.Problems, "; ")}
			}
			return mapSieveError(err)
		}
		if warnings != "" {
			a.log.Warn("sieve: server warnings on the managed script push",
				"account_id", accountID, "warnings", warnings)
		}
		// Reconcile the ledger so the SieveScript state moves (the push may
		// have created the managed script and backups).
		scripts, lerr := c.ListScripts(ctx)
		if lerr == nil {
			names := make([]string, 0, len(scripts))
			for _, s := range scripts {
				names = append(names, s.Name)
			}
			if _, serr := a.store.SyncSieveScripts(ctx, accountID, names); serr != nil {
				a.log.Warn("sieve: ledger reconcile after push failed", "error", serr)
			}
		}
		return nil
	})
	if a.observer != nil {
		result := "ok"
		if err != nil {
			result = "error"
		}
		a.observer.ScriptPushed(result)
	}
	if err == nil {
		a.notify(accountID)
	}
	return err
}

// ---------------------------------------------------------------------------
// VacationStore
// ---------------------------------------------------------------------------

// GetVacation implements VacationStore.
func (a *SieveAdapter) GetVacation(ctx context.Context, accountID int64) (VacationValue, error) {
	state, _, err := a.readManaged(ctx, accountID)
	if err != nil {
		return VacationValue{}, err
	}
	return vacationValueFromModel(state.Model.Vacation), nil
}

// SetVacation implements VacationStore.
func (a *SieveAdapter) SetVacation(ctx context.Context, accountID int64, v VacationValue) error {
	err := a.pushManaged(ctx, accountID, func(model *sieve.Script) error {
		model.Vacation = vacationModelFromValue(v)
		return nil
	})
	if err == nil && a.observer != nil {
		a.observer.VacationConfigured(v.IsEnabled)
	}
	return err
}

// VacationState implements VacationStore: a digest of the vacation section's
// stored form. It moves exactly when the object changes, including a change
// made by an out-of-band script edit that survives the parse.
func (a *SieveAdapter) VacationState(ctx context.Context, accountID int64) (string, error) {
	state, _, err := a.readManaged(ctx, accountID)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(state.Model.Vacation)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8]), nil
}

func vacationValueFromModel(v *sieve.Vacation) VacationValue {
	if v == nil {
		return VacationValue{}
	}
	out := VacationValue{IsEnabled: v.Enabled}
	if !v.FromDate.IsZero() {
		t := v.FromDate
		out.FromDate = &t
	}
	if !v.ToDate.IsZero() {
		t := v.ToDate
		out.ToDate = &t
	}
	if v.Subject != "" {
		s := v.Subject
		out.Subject = &s
	}
	if v.TextBody != "" {
		s := v.TextBody
		out.TextBody = &s
	}
	if v.HTMLBody != "" {
		s := v.HTMLBody
		out.HTMLBody = &s
	}
	return out
}

func vacationModelFromValue(v VacationValue) *sieve.Vacation {
	out := &sieve.Vacation{Enabled: v.IsEnabled}
	if v.FromDate != nil {
		out.FromDate = v.FromDate.UTC().Truncate(time.Second)
	}
	if v.ToDate != nil {
		out.ToDate = v.ToDate.UTC().Truncate(time.Second)
	}
	if v.Subject != nil {
		out.Subject = *v.Subject
	}
	if v.TextBody != nil {
		out.TextBody = *v.TextBody
	}
	if v.HTMLBody != nil {
		out.HTMLBody = *v.HTMLBody
	}
	return out
}

// ---------------------------------------------------------------------------
// FilterStore
// ---------------------------------------------------------------------------

// GetFilters implements FilterStore.
func (a *SieveAdapter) GetFilters(ctx context.Context, accountID int64) (FilterConfig, error) {
	state, _, err := a.readManaged(ctx, accountID)
	if err != nil {
		return FilterConfig{}, err
	}
	cfg := FilterConfig{ScriptActive: state.OursActive}
	for _, r := range state.Model.Rules {
		cfg.Rules = append(cfg.Rules, filterValueFromRule(r))
	}
	if f := state.Model.ForwardAll; f != nil {
		cfg.ForwardAll = ForwardAllValue{Enabled: f.Enabled, Address: f.Address, Disposition: f.Disposition}
	}
	return cfg, nil
}

// PutFilters implements FilterStore.
func (a *SieveAdapter) PutFilters(ctx context.Context, accountID int64, rules []FilterRuleValue, forwardAll ForwardAllValue) error {
	return a.pushManaged(ctx, accountID, func(model *sieve.Script) error {
		model.Rules = model.Rules[:0]
		for _, v := range rules {
			model.Rules = append(model.Rules, ruleFromFilterValue(v))
		}
		if forwardAll.Enabled || forwardAll.Address != "" {
			model.ForwardAll = &sieve.ForwardAll{
				Enabled:     forwardAll.Enabled,
				Address:     forwardAll.Address,
				Disposition: forwardAll.Disposition,
			}
		} else {
			model.ForwardAll = nil
		}
		return nil
	})
}

// FiltersState implements FilterStore: a digest over rules + forwardAll +
// the active bit, so activating another script (which stops the rules from
// filtering) moves the state too.
func (a *SieveAdapter) FiltersState(ctx context.Context, accountID int64) (string, error) {
	state, _, err := a.readManaged(ctx, accountID)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(struct {
		Rules      []sieve.Rule
		ForwardAll *sieve.ForwardAll
		Active     bool
	}{state.Model.Rules, state.Model.ForwardAll, state.OursActive})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8]), nil
}

// filterValueFromRule and ruleFromFilterValue are the 1:1 mapping; the
// parity test pins that no field is dropped in either direction.
func filterValueFromRule(r sieve.Rule) FilterRuleValue {
	return FilterRuleValue{
		ID:            r.ID,
		Name:          r.Name,
		Type:          r.Type,
		Enabled:       r.Enabled,
		From:          r.Criteria.From,
		To:            r.Criteria.To,
		Subject:       r.Criteria.Subject,
		SizeOver:      r.Criteria.SizeOver,
		SizeUnder:     r.Criteria.SizeUnder,
		HasAttachment: r.Criteria.HasAttachment,
		MoveTo:        r.Actions.MoveTo,
		Labels:        r.Actions.Labels,
		MarkRead:      r.Actions.MarkRead,
		Star:          r.Actions.Star,
		Forward:       r.Actions.Forward,
		Delete:        r.Actions.Delete,
		Stop:          r.Actions.Stop,
	}
}

func ruleFromFilterValue(v FilterRuleValue) sieve.Rule {
	return sieve.Rule{
		ID:      v.ID,
		Name:    v.Name,
		Type:    v.Type,
		Enabled: v.Enabled,
		Criteria: sieve.Criteria{
			From:          v.From,
			To:            v.To,
			Subject:       v.Subject,
			SizeOver:      v.SizeOver,
			SizeUnder:     v.SizeUnder,
			HasAttachment: v.HasAttachment,
		},
		Actions: sieve.Actions{
			MoveTo:   v.MoveTo,
			Labels:   v.Labels,
			MarkRead: v.MarkRead,
			Star:     v.Star,
			Forward:  v.Forward,
			Delete:   v.Delete,
			Stop:     v.Stop,
		},
	}
}

// ---------------------------------------------------------------------------
// ForwardingStore
// ---------------------------------------------------------------------------

// ListForwardingAddresses implements ForwardingStore.
func (a *SieveAdapter) ListForwardingAddresses(ctx context.Context, accountID int64) ([]ForwardingAddressValue, error) {
	rows, err := a.store.ListForwardingAddresses(ctx, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]ForwardingAddressValue, 0, len(rows))
	for _, r := range rows {
		out = append(out, ForwardingAddressValue{
			ID: r.ID, Email: r.Email, State: r.State, VerifiedAt: r.VerifiedAt,
		})
	}
	return out, nil
}

// CreateForwardingAddress implements ForwardingStore: row first, then the
// verification mail; a failed send removes the row so a retry is a clean
// re-create rather than a stuck pending entry with no token in flight.
func (a *SieveAdapter) CreateForwardingAddress(ctx context.Context, accountID int64, email string) (ForwardingAddressValue, error) {
	if a.tokens == nil || a.mailer == nil {
		return ForwardingAddressValue{}, errors.New("mail: forwarding verification is not wired")
	}
	email = strings.ToLower(strings.TrimSpace(email))

	existing, err := a.store.ListForwardingAddresses(ctx, accountID)
	if err != nil {
		return ForwardingAddressValue{}, err
	}
	if len(existing) >= maxForwardingAddresses {
		return ForwardingAddressValue{}, fmt.Errorf("at most %d forwarding addresses per account", maxForwardingAddresses)
	}
	for _, f := range existing {
		if f.Email == email {
			return ForwardingAddressValue{}, ErrForwardingExists
		}
	}

	expires := time.Now().Add(forwardingTokenTTL).UTC()
	row, err := a.store.CreateForwardingAddress(ctx, accountID, email, expires)
	if err != nil {
		return ForwardingAddressValue{}, err
	}

	token, err := a.tokens.Mint(accountID, email, expires)
	if err == nil {
		err = a.mailer.SendVerification(ctx, accountID, email, token, expires)
	}
	if a.observer != nil {
		result := "sent"
		if err != nil {
			result = "failed"
		}
		a.observer.VerificationMailSent(result)
	}
	if err != nil {
		if derr := a.store.DeleteForwardingAddress(ctx, accountID, row.ID); derr != nil {
			a.log.Error("sieve: cleaning up after a failed verification mail",
				"account_id", accountID, "row", row.ID, "error", derr)
		}
		return ForwardingAddressValue{}, fmt.Errorf("sending the verification mail: %w", err)
	}

	a.notify(accountID)
	return ForwardingAddressValue{ID: row.ID, Email: row.Email, State: row.State}, nil
}

// DestroyForwardingAddress implements ForwardingStore. An address the
// current managed configuration still redirects to is refused: destroying it
// would leave a live redirect whose permission no longer exists.
func (a *SieveAdapter) DestroyForwardingAddress(ctx context.Context, accountID, id int64) error {
	row, err := a.store.GetForwardingAddress(ctx, accountID, id)
	if err != nil {
		return err
	}
	state, _, err := a.readManaged(ctx, accountID)
	if err != nil {
		return err
	}
	target := strings.ToLower(row.Email)
	if f := state.Model.ForwardAll; f != nil && f.Enabled && strings.ToLower(f.Address) == target {
		return ErrForwardingInUse
	}
	for _, r := range state.Model.Rules {
		if r.Enabled && strings.ToLower(r.Actions.Forward) == target {
			return ErrForwardingInUse
		}
	}
	if err := a.store.DeleteForwardingAddress(ctx, accountID, id); err != nil {
		return err
	}
	a.notify(accountID)
	return nil
}

// VerifyForwarding implements ForwardingStore.
func (a *SieveAdapter) VerifyForwarding(ctx context.Context, accountID int64, token string) (string, error) {
	if a.tokens == nil {
		return "", ErrTokenInvalid
	}
	email, err := a.tokens.Verify(accountID, token)
	if err != nil {
		return "", ErrTokenInvalid
	}
	if _, err := a.store.AcceptForwardingAddress(ctx, accountID, strings.ToLower(email)); err != nil {
		// The row was destroyed after the mail went out. Same answer as a
		// bad token: the no-oracle rule.
		return "", ErrTokenInvalid
	}
	a.notify(accountID)
	return strings.ToLower(email), nil
}

// ForwardingState implements ForwardingStore.
func (a *SieveAdapter) ForwardingState(ctx context.Context, accountID int64) (string, error) {
	wm, err := a.store.ForwardingWatermark(ctx, accountID)
	if err != nil {
		return "", err
	}
	n, err := a.store.CountForwardingAddresses(ctx, accountID)
	if err != nil {
		return "", err
	}
	return stateFor(wm, n), nil
}

// notify pushes an SSE StateChange, nil-safe.
func (a *SieveAdapter) notify(accountID int64) {
	if a.notifier != nil {
		a.notifier.Notify(accountID)
	}
}
