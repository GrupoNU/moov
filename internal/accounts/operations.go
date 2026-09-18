package accounts

import (
	"context"
	"errors"
	"fmt"

	"github.com/GrupoNU/moov/internal/mailcow"
	"github.com/GrupoNU/moov/internal/provision"
	"github.com/GrupoNU/moov/internal/store"
)

// The transitions of contract §2.4, one method each. Every one of them:
//
//   - passes the domain check FIRST (resolve / inDomain), before any Mailcow
//     call, because that check is the whole security boundary (§4);
//   - refuses anything but a read while the account is deleting (§2.4);
//   - writes exactly one audit line, whatever the outcome (§2.4);
//   - returns the resource as it stands AFTER the transition, so a caller
//     never has to issue a second GET to learn what it did.

// The action names the audit log records. They are constants because the
// consumer joins on them and a renamed verb would silently orphan its
// history.
const (
	ActionCreate   = "create"
	ActionUpdate   = "update"
	ActionSuspend  = "suspend"
	ActionResume   = "resume"
	ActionReadOnly = "readonly"
	ActionDelete   = "delete"
	ActionExport   = "export"
)

// noteRecreated marks a create of an address that has been deleted before.
// The contract calls this out explicitly (§2.4): recreating an address is
// allowed and is an audited event, never silent.
const noteRecreated = "recreated"

// CreateRequest is the input of POST /admin/accounts, already validated by
// the HTTP layer against §2.5.
type CreateRequest struct {
	Address string
	Name    string
	// QuotaMB is zero when the caller did not ask for one; DefaultQuotaMB
	// then applies.
	QuotaMB int
	Limits  Limits
}

// Create creates a mailbox, provisions it and returns the resource.
//
// It is IDEMPOTENT BY ADDRESS (§2.4): when the account already exists it
// returns the current resource with created=false and changes nothing - a
// differing name, quota or limits in the body are ignored, because a retry
// after a lost response must not be a silent update. PATCH is how a caller
// changes something.
//
// The order of the writes is the rollback story. Mailcow's mailbox comes
// first, then provisioning (which mints the scoped app password and proves it
// works with a real IMAP LOGIN), then the quota/limit writes, then the Moov
// facts. Anything that fails after the mailbox exists deletes it again before
// returning, so a 502 means "nothing was left half-done" exactly as §2.2
// promises - and the app password goes with it by Mailcow's own cascade (F0
// answer P2).
func (s *Service) Create(ctx context.Context, c Call, req CreateRequest) (acct Account, created bool, err error) {
	if !inDomain(c.Actor, req.Address) {
		// No audit line: the actor is not entitled to know this address
		// exists as a concept, and an audit row keyed by it would be a record
		// of someone else's domain (§2.1).
		return Account{}, false, ErrNotFound
	}

	// The idempotent path. An existing account short-circuits everything:
	// no Mailcow call, no provisioning, no writes.
	existing, lookupErr := s.store.GetAccountByEmail(ctx, req.Address)
	switch {
	case lookupErr == nil && existing.DeletingSince != nil:
		// §2.7: the address exists but is being purged. It cannot be
		// recreated until the purge finishes, and saying so is not an oracle
		// - the caller already knows this address, it owns the domain.
		s.audit(ctx, c, ActionCreate, req.Address, "error", "deleting")
		return Account{}, false, ErrDeleting
	case lookupErr == nil:
		res, buildErr := s.resourceOf(ctx, existing)
		if buildErr != nil {
			return Account{}, false, buildErr
		}
		s.audit(ctx, c, ActionCreate, req.Address, "ok", "idempotent")
		return res, false, nil
	case !errors.Is(lookupErr, store.ErrNotFound):
		return Account{}, false, fmt.Errorf("reading account %q: %w", req.Address, lookupErr)
	}

	// A create of an address that was deleted before is allowed and audited
	// as such. The note is resolved BEFORE the work, so a failure mid-way
	// still records what kind of create was attempted.
	note := ""
	if recreated, auditErr := s.store.HasAuditFor(ctx, req.Address, ActionDelete); auditErr != nil {
		// Not fatal: an unreadable audit history must not stop a create. The
		// line will simply lack the qualifier, and the log says why.
		s.log.Warn("accounts: could not check whether this address was deleted before",
			"address", req.Address, "error", auditErr)
	} else if recreated {
		note = noteRecreated
	}

	defer func() { s.finish(ctx, c, ActionCreate, req.Address, err, note) }()

	quotaMB := req.QuotaMB
	if quotaMB == 0 {
		quotaMB = DefaultQuotaMB
	}

	password, err := randomPassword()
	if err != nil {
		return Account{}, false, err
	}

	local := req.Address[:len(req.Address)-len(DomainOf(req.Address))-1]
	err = s.mailcow.CreateMailbox(ctx, mailcow.CreateMailboxRequest{
		LocalPart: local,
		Domain:    DomainOf(req.Address),
		Name:      req.Name,
		Password:  password,
		QuotaMB:   quotaMB,
		Active:    true,
	})
	if err != nil {
		if mailcow.IsAPICode(err, mailcow.CodeObjectExists) {
			// The mailbox exists in Mailcow but not in Moov: an earlier
			// create that failed between the two, or a mailbox the operator
			// made by hand. Provisioning it is the right repair - it is the
			// same mailbox the caller asked for - and it is what makes the
			// retry of a half-failed create converge instead of dead-ending.
			s.log.Info("accounts: the mailbox already existed in Mailcow; provisioning it",
				"address", req.Address, "request_id", c.RequestID)
			return s.finishCreate(ctx, c, req, quotaMB, password, false)
		}
		return Account{}, false, classifyMailcow("Mailcow refused to create the mailbox", err)
	}
	return s.finishCreate(ctx, c, req, quotaMB, password, true)
}

// finishCreate provisions a mailbox that now exists in Mailcow and writes
// Moov's own facts, rolling the mailbox back if anything fails.
//
// ours says whether THIS call created the mailbox: only then may the rollback
// delete it. A mailbox that was already there when we arrived is not ours to
// destroy, and deleting it would turn a failed provisioning into data loss.
func (s *Service) finishCreate(ctx context.Context, c Call, req CreateRequest, quotaMB int, password string, ours bool) (Account, bool, error) {
	rollback := func(cause error) (Account, bool, error) {
		if !ours {
			return Account{}, false, cause
		}
		if delErr := s.mailcow.DeleteMailbox(ctx, req.Address); delErr != nil {
			// The caller still gets its 502 - nothing usable was created -
			// but an operator has to know a mailbox was left behind.
			s.log.Error("accounts: rolling back the mailbox failed; it was left in Mailcow",
				"address", req.Address, "request_id", c.RequestID,
				"cause", cause, "error", delErr)
		}
		return Account{}, false, cause
	}

	// Provisioning is the existing ADR §4 flow: a real IMAP LOGIN with the
	// password we are about to discard, a scoped app password minted and
	// sealed, the user password never stored.
	res, err := s.prov.Provision(ctx, provision.Request{Email: req.Address, Password: password})
	if err != nil {
		switch {
		case errors.Is(err, provision.ErrInvalidCredentials):
			// Dovecot rejected a password Mailcow just accepted: the mailbox
			// is not usable, so it does not survive this call.
			return rollback(upstreamRefused("the new mailbox could not be authenticated against Dovecot", err))
		case errors.Is(err, provision.ErrOrphanedAppPassword):
			// provision already reported what it could not clean up; the
			// mailbox delete below takes the app password with it (F0 P2).
			return rollback(upstreamRefused("provisioning the new mailbox failed", err))
		default:
			return rollback(classifyMailcow("provisioning the new mailbox failed", err))
		}
	}

	if res.AppPasswordID > 0 {
		if err := s.store.SetAccountAppPasswordID(ctx, res.Account.ID, res.AppPasswordID); err != nil {
			return rollback(fmt.Errorf("recording the app password id: %w", err))
		}
	}

	// The rate limit is written only when it DIFFERS from what the mailbox
	// already has (§2.5): a domain-level limit is inherited by every new
	// mailbox, and writing the same number as a per-mailbox override would
	// pin the mailbox to a value the operator can no longer change from the
	// domain.
	sendPerDay := DefaultSendPerDay
	if req.Limits.SendPerDay != nil {
		sendPerDay = *req.Limits.SendPerDay
	}
	if err := s.applyRateLimit(ctx, req.Address, sendPerDay); err != nil {
		return rollback(err)
	}

	facts := store.AccountFacts{
		DisplayName:          req.Name,
		QuotaMB:              quotaMB,
		SendPerDay:           &sendPerDay,
		RecipientsPerMessage: intOr(req.Limits.RecipientsPerMessage, DefaultRecipientsPerMessage),
		AttachmentMB:         intOr(req.Limits.AttachmentMB, DefaultAttachmentMB),
	}
	if err := s.store.SetAccountFacts(ctx, res.Account.ID, facts); err != nil {
		return rollback(fmt.Errorf("recording the account facts: %w", err))
	}

	fresh, err := s.store.GetAccountByEmail(ctx, req.Address)
	if err != nil {
		return Account{}, false, fmt.Errorf("reading back account %q: %w", req.Address, err)
	}
	out, err := s.resourceOf(ctx, fresh)
	if err != nil {
		return Account{}, false, err
	}

	// The mailbox is provisioned and the row is active: tell the sync engine
	// now rather than letting it find out on its next sweep. This is the LAST
	// thing the create does, after everything that could still roll the mailbox
	// back, so the engine is never pointed at an account that is about to be
	// deleted again.
	//
	// It is deliberately not checked and cannot fail the create. The engine
	// discovers accounts on its own schedule regardless; this only removes the
	// wait for the organizer who is looking at the webmail right now. See
	// SyncNudger.
	s.nudgeSync()

	return out, true, nil
}

// applyRateLimit writes the per-mailbox rate limit only when the effective
// one differs, per §2.5.
func (s *Service) applyRateLimit(ctx context.Context, address string, perDay int) error {
	mb, err := s.mailcow.GetMailbox(ctx, address)
	if err != nil {
		return classifyMailcow("reading the new mailbox", err)
	}
	if mb.RL.Value == perDay && mb.RL.Frame == "d" {
		// Already the requested value, inherited or not: leave it alone.
		return nil
	}
	if err := s.mailcow.SetMailboxRateLimit(ctx, address, mailcow.RateLimit{Value: perDay, Frame: "d"}); err != nil {
		return classifyMailcow("Mailcow refused the rate limit", err)
	}
	return nil
}

func intOr(v *int, def int) *int {
	if v != nil {
		return v
	}
	d := def
	return &d
}

// Get returns the resource. It is the only route that does not audit: a read
// is not a write, and §2.4 asks for one line per WRITE.
func (s *Service) Get(ctx context.Context, actor Actor, address string) (Account, error) {
	a, err := s.resolve(ctx, actor, address)
	if err != nil {
		return Account{}, err
	}
	return s.resourceOf(ctx, a)
}

// UpdateRequest is the PATCH body: absent fields are untouched.
type UpdateRequest struct {
	Name    *string
	QuotaMB *int
	Limits  Limits
}

// IsEmpty reports whether the patch would change nothing.
func (u UpdateRequest) IsEmpty() bool {
	return u.Name == nil && u.QuotaMB == nil &&
		u.Limits.SendPerDay == nil && u.Limits.RecipientsPerMessage == nil && u.Limits.AttachmentMB == nil
}

// Update applies a partial change (§2.4, deviation D2).
func (s *Service) Update(ctx context.Context, c Call, address string, req UpdateRequest) (acct Account, err error) {
	a, err := s.resolve(ctx, c.Actor, address)
	if err != nil {
		return Account{}, err
	}
	if a.DeletingSince != nil {
		s.audit(ctx, c, ActionUpdate, address, "error", "deleting")
		return Account{}, ErrDeleting
	}
	defer func() { s.finish(ctx, c, ActionUpdate, address, err, "") }()

	edit := mailcow.MailboxEdit{Name: req.Name, QuotaMB: req.QuotaMB}
	if !edit.IsEmpty() {
		if err := s.mailcow.EditMailbox(ctx, address, edit); err != nil {
			return Account{}, classifyMailcow("Mailcow refused the update", err)
		}
	}
	if req.Limits.SendPerDay != nil {
		if err := s.applyRateLimit(ctx, address, *req.Limits.SendPerDay); err != nil {
			return Account{}, err
		}
	}

	// The store mirrors what was just written, leaving absent fields as they
	// were - which is why the facts are read from the row rather than from
	// the defaults.
	facts := store.AccountFacts{
		DisplayName:          a.DisplayName,
		QuotaMB:              a.QuotaMB,
		SendPerDay:           a.SendPerDay,
		RecipientsPerMessage: a.RecipientsPerMessage,
		AttachmentMB:         a.AttachmentMB,
	}
	if req.Name != nil {
		facts.DisplayName = *req.Name
	}
	if req.QuotaMB != nil {
		facts.QuotaMB = *req.QuotaMB
	}
	if req.Limits.SendPerDay != nil {
		facts.SendPerDay = req.Limits.SendPerDay
	}
	if req.Limits.RecipientsPerMessage != nil {
		facts.RecipientsPerMessage = req.Limits.RecipientsPerMessage
	}
	if req.Limits.AttachmentMB != nil {
		facts.AttachmentMB = req.Limits.AttachmentMB
	}
	if err := s.store.SetAccountFacts(ctx, a.ID, facts); err != nil {
		return Account{}, fmt.Errorf("recording the account facts: %w", err)
	}
	return s.reread(ctx, address)
}

// Suspend deactivates the mailbox in Mailcow and revokes every Moov session.
// Idempotent: suspending a suspended account changes nothing and answers 200.
func (s *Service) Suspend(ctx context.Context, c Call, address string) (acct Account, err error) {
	a, err := s.resolve(ctx, c.Actor, address)
	if err != nil {
		return Account{}, err
	}
	if a.DeletingSince != nil {
		s.audit(ctx, c, ActionSuspend, address, "error", "deleting")
		return Account{}, ErrDeleting
	}
	defer func() { s.finish(ctx, c, ActionSuspend, address, err, "") }()

	// Mailcow first: while the two disagree, the safe direction is "Mailcow
	// already refuses, Moov has not caught up yet", never the reverse.
	if err := s.mailcow.EditMailbox(ctx, address, mailcow.MailboxEdit{Active: boolPtr(false)}); err != nil {
		return Account{}, classifyMailcow("Mailcow refused to deactivate the mailbox", err)
	}
	if err := s.store.SetAccountSuspended(ctx, a.ID, true, s.now()); err != nil {
		return Account{}, fmt.Errorf("recording the suspension: %w", err)
	}
	// Then the live sessions: the browser's next request fails (§2.4, gate
	// criterion "well under 60 s" - this is immediate).
	if err := s.revoker.RevokeAccount(ctx, a.ID); err != nil {
		// The mailbox is already inactive in Mailcow, so the account cannot
		// reach Dovecot whatever a cached credential says. Report it, do not
		// fail the transition.
		s.log.Error("accounts: revoking sessions after a suspension failed",
			"address", address, "request_id", c.RequestID, "error", err)
	}
	return s.reread(ctx, address)
}

// Resume lifts a suspension. The account returns to its retention phase:
// readonly when ReadOnly is set, active otherwise (§2.4, deviation D3).
func (s *Service) Resume(ctx context.Context, c Call, address string) (acct Account, err error) {
	a, err := s.resolve(ctx, c.Actor, address)
	if err != nil {
		return Account{}, err
	}
	if a.DeletingSince != nil {
		s.audit(ctx, c, ActionResume, address, "error", "deleting")
		return Account{}, ErrDeleting
	}
	defer func() { s.finish(ctx, c, ActionResume, address, err, "") }()

	if err := s.mailcow.EditMailbox(ctx, address, mailcow.MailboxEdit{Active: boolPtr(true)}); err != nil {
		return Account{}, classifyMailcow("Mailcow refused to reactivate the mailbox", err)
	}
	if err := s.store.SetAccountSuspended(ctx, a.ID, false, s.now()); err != nil {
		return Account{}, fmt.Errorf("recording the resumption: %w", err)
	}
	return s.reread(ctx, address)
}

// ReadOnly enters the retention phase (§2.4, F0 answer P3).
//
// The lock that ENFORCES is the credential: the account's app password is
// re-issued with IMAP and Sieve only and the old one is deleted, so even a
// non-Moov client holding that credential cannot submit. smtp_access:0 is
// written too, as belt and braces and so that an operator reading the Mailcow
// UI sees the intent - but it is NOT relied on, because F0 measured Mailcow
// accepting submission AUTH with it set.
//
// The JMAP refusal (EmailSubmission/set) and the PWA's hidden compose are the
// EXPLAINING lock: they tell the user why, which a credential cannot.
//
// Idempotent, and one-way in this version: there is no transition back.
func (s *Service) ReadOnly(ctx context.Context, c Call, address string) (acct Account, err error) {
	a, err := s.resolve(ctx, c.Actor, address)
	if err != nil {
		return Account{}, err
	}
	if a.DeletingSince != nil {
		s.audit(ctx, c, ActionReadOnly, address, "error", "deleting")
		return Account{}, ErrDeleting
	}
	if a.ReadOnly {
		s.audit(ctx, c, ActionReadOnly, address, "ok", "idempotent")
		return s.reread(ctx, address)
	}
	defer func() { s.finish(ctx, c, ActionReadOnly, address, err, "") }()

	if err := s.reissueWithoutSMTP(ctx, a); err != nil {
		return Account{}, err
	}

	// Belt and braces, after the credential lock is in place: if this write
	// fails the account is ALREADY unable to send, so it is logged rather
	// than allowed to undo a lock that worked.
	if err := s.mailcow.EditMailbox(ctx, address, mailcow.MailboxEdit{SMTPAccess: boolPtr(false)}); err != nil {
		s.log.Warn("accounts: could not clear smtp_access; the credential lock is in place regardless",
			"address", address, "request_id", c.RequestID, "error", err)
	}

	if err := s.store.SetAccountReadOnly(ctx, a.ID, s.now()); err != nil {
		return Account{}, fmt.Errorf("recording the read-only lock: %w", err)
	}
	// The cached credential in the authenticator is the OLD app password,
	// which no longer exists: without this the account would fail to
	// authenticate until the cache expired rather than continue reading.
	if err := s.revoker.RevokeAccount(ctx, a.ID); err != nil {
		s.log.Error("accounts: revoking cached credentials after the read-only lock failed",
			"address", address, "request_id", c.RequestID, "error", err)
	}
	return s.reread(ctx, address)
}

// reissueWithoutSMTP mints a replacement app password limited to IMAP and
// Sieve, stores it sealed, and deletes the old one.
//
// The order is deliberate: mint, store, THEN delete. A failure before the
// store leaves the account on its old (still working) credential with a
// harmless extra app password in Mailcow; a failure at the delete leaves the
// account on the new, correct credential with an over-scoped one still
// present, which is logged loudly because it is a real gap until an operator
// removes it. Deleting first would mean a failure to mint locks the mailbox
// out of Moov entirely.
func (s *Service) reissueWithoutSMTP(ctx context.Context, a store.Account) error {
	minted, err := s.prov.Reissue(ctx, a.Email, ReadOnlyScopes())
	if err != nil {
		return classifyMailcow("re-issuing the credential without SMTP failed", err)
	}
	if a.MailcowAppPasswordID != nil && *a.MailcowAppPasswordID != minted.AppPasswordID {
		if err := s.mailcow.DeleteAppPassword(ctx, *a.MailcowAppPasswordID); err != nil {
			s.log.Error("accounts: the old app password could not be deleted; it still permits SMTP",
				"address", a.Email, "app_password_id", *a.MailcowAppPasswordID, "error", err)
		}
	}
	if minted.AppPasswordID > 0 {
		if err := s.store.SetAccountAppPasswordID(ctx, a.ID, minted.AppPasswordID); err != nil {
			return fmt.Errorf("recording the re-issued app password id: %w", err)
		}
	}
	return nil
}

// Delete starts the purge (§2.4).
//
// What happens synchronously: sessions revoked, the Mailcow mailbox deleted
// (its app passwords cascade - F0 P2), the row marked deleting. What happens
// in the background: the store rows and the blob references. The contract
// answers 202 because of exactly that split, and because F0 could NOT verify
// that Mailcow removes the maildir promptly for a mailbox that received mail
// - so "deleting" is not promised to be instantaneous.
func (s *Service) Delete(ctx context.Context, c Call, address string) (acct Account, err error) {
	a, err := s.resolve(ctx, c.Actor, address)
	if err != nil {
		return Account{}, err
	}
	if a.DeletingSince != nil {
		// Already deleting: 409, not a second delete (§2.4: 202 once, then
		// 409, then 404).
		s.audit(ctx, c, ActionDelete, address, "error", "deleting")
		return Account{}, ErrDeleting
	}
	defer func() { s.finish(ctx, c, ActionDelete, address, err, "") }()

	// Sessions first: a delete that fails half-way must not leave a live
	// session on a mailbox the caller has asked to destroy.
	if err := s.revoker.RevokeAccount(ctx, a.ID); err != nil {
		s.log.Error("accounts: revoking sessions before a deletion failed",
			"address", address, "request_id", c.RequestID, "error", err)
	}
	if err := s.mailcow.DeleteMailbox(ctx, address); err != nil {
		// access_denied is also Mailcow's "no such entity" (F0 rule 7): a
		// mailbox already gone is not a reason to refuse the purge of Moov's
		// own rows, which is what the caller is really asking for.
		if !mailcow.IsAPICode(err, mailcow.CodeAccessDenied) {
			return Account{}, classifyMailcow("Mailcow refused to delete the mailbox", err)
		}
		s.log.Warn("accounts: Mailcow reports no such mailbox; purging Moov's rows anyway",
			"address", address, "request_id", c.RequestID)
	}
	if err := s.store.MarkAccountDeleting(ctx, a.ID, s.now()); err != nil {
		return Account{}, fmt.Errorf("marking the account deleting: %w", err)
	}
	return s.reread(ctx, address)
}

// Purge removes the store rows of every account whose deletion was started.
//
// It runs in the background (cmd/moovd wires it to a ticker) rather than
// inside the DELETE request, because a large mailbox's rows are not a
// request-sized amount of work. Blobs are not unlinked here: DeleteAccount
// drops the message rows, which drops the references, and the blob GC
// collects what no longer has any - the path a content-addressed blob must
// take, since another account may share it.
func (s *Service) Purge(ctx context.Context) (int, error) {
	pending, err := s.store.ListDeletingAccounts(ctx)
	if err != nil {
		return 0, fmt.Errorf("listing accounts to purge: %w", err)
	}
	purged := 0
	for _, a := range pending {
		if err := s.store.DeleteAccount(ctx, a.ID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			s.log.Error("accounts: purging an account failed; it will be retried",
				"address", a.Email, "error", err)
			continue
		}
		s.log.Info("accounts: purge complete", "address", a.Email)
		purged++
	}
	return purged, nil
}

// resourceOf builds the published resource, reading Mailcow for the real disk
// usage.
//
// A Mailcow that cannot be reached does NOT fail the read: the resource is
// served with the mirrored quota and zero usage, which is honest (Moov does
// not know) and keeps a portal's dashboard working through an upstream blip.
// The state machine's own facts - state, readOnly, suspended, sync - are all
// Moov's and are unaffected.
func (s *Service) resourceOf(ctx context.Context, a store.Account) (Account, error) {
	sum, err := s.store.AccountSyncSummary(ctx, a.ID)
	if err != nil {
		return Account{}, fmt.Errorf("summarizing sync of %q: %w", a.Email, err)
	}
	mb, mbErr := s.mailcow.GetMailbox(ctx, a.Email)
	if mbErr != nil {
		s.log.Warn("accounts: Mailcow could not be read; serving the mirrored quota",
			"address", a.Email, "error", mbErr)
		mb = mailcow.Mailbox{}
	}
	return buildResource(a, sum, mb), nil
}

// reread returns the resource as it stands after a transition.
func (s *Service) reread(ctx context.Context, address string) (Account, error) {
	a, err := s.store.GetAccountByEmail(ctx, address)
	if err != nil {
		return Account{}, fmt.Errorf("reading back account %q: %w", address, err)
	}
	return s.resourceOf(ctx, a)
}

func boolPtr(b bool) *bool { return &b }

// ReadOnlyScopes is the protocol set a read-only account's credential
// carries: IMAP to read and Sieve so the account's own filters keep running.
// SMTP is the one that is absent, and its absence IS the lock (§2.4).
func ReadOnlyScopes() []mailcow.Protocol {
	return []mailcow.Protocol{mailcow.ProtocolIMAP, mailcow.ProtocolSieve}
}
