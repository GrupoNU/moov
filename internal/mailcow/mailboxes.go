package mailcow

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// The mailbox write operations of the accounts API (epic M1). Every request
// shape here was verified against Mailcow 2026-07a in F0
// (docs/briefs/2026-09-15-vpsmail-nota-dominio-y-mailcow-errores.md §4):
//
//	POST /api/v1/add/mailbox        object body, quota in MB
//	POST /api/v1/edit/mailbox       {"items":[addr],"attr":{...}} — items is a
//	                                FLAT array; the {"anyOf":[...]} form some
//	                                examples show fails with username_invalid
//	POST /api/v1/delete/mailbox     bare array of addresses, no envelope
//	GET  /api/v1/get/rl-mbox/{addr} {"value":"300","frame":"d"}; `{}` for none
//	POST /api/v1/edit/rl-mbox       {"items":[addr],"attr":{"rl_value","rl_frame"}}
//
// Suspension has no endpoint of its own: it is edit/mailbox with active 0/1.
//
// None of these is retried, for the reason CreateAppPassword states: a write
// whose response was lost may have happened, and a blind retry of a create
// is a duplicate. The caller (internal/accounts) reconciles by reading.

// CreateMailboxRequest is the input to CreateMailbox.
type CreateMailboxRequest struct {
	// LocalPart and Domain form the address. Both required.
	LocalPart string
	Domain    string
	// Name is the display name Mailcow shows. Required by the API.
	Name string
	// Password is the mailbox's own password. The accounts API generates a
	// random one and DISCARDS it: Moov authenticates with an app password it
	// mints next, and nobody is meant to log in with this one. Mailcow
	// enforces its complexity policy on it (password_complexity).
	Password string
	// QuotaMB is the quota in MEBIBYTES — Mailcow writes MB and reads bytes.
	QuotaMB int
	// Active 0/1; the accounts API creates active mailboxes.
	Active bool
}

// CreateMailbox creates a mailbox.
//
// The response is an ARRAY of results — Mailcow saves the domain's rate limit
// onto the new mailbox as a separate result before the create itself — and
// every element is inspected. A duplicate address is reported as an APIError
// with CodeObjectExists, which the accounts API treats as its idempotent path.
func (c *Client) CreateMailbox(ctx context.Context, req CreateMailboxRequest) error {
	if err := validateMailbox(req.LocalPart + "@" + req.Domain); err != nil {
		return err
	}
	if strings.Contains(req.LocalPart, "@") {
		return fmt.Errorf("%w: local part %q contains @", ErrInvalidConfig, req.LocalPart)
	}
	if req.Password == "" {
		return fmt.Errorf("%w: Password is required", ErrInvalidConfig)
	}
	if req.QuotaMB <= 0 {
		return fmt.Errorf("%w: QuotaMB must be positive", ErrInvalidConfig)
	}
	payload := map[string]any{
		"local_part":      req.LocalPart,
		"domain":          req.Domain,
		"name":            req.Name,
		"password":        req.Password,
		"password2":       req.Password,
		"quota":           req.QuotaMB,
		"active":          boolInt(req.Active),
		"force_pw_update": 0,
		"tls_enforce_in":  1,
		"tls_enforce_out": 1,
	}
	body, err := c.do(ctx, http.MethodPost, "/add/mailbox", payload)
	if err != nil {
		return err
	}
	return checkAPIResult(body, "mailbox_added")
}

// MailboxEdit is a partial update: a nil field is left untouched.
type MailboxEdit struct {
	Name    *string
	QuotaMB *int
	// Active suspends (false) or resumes (true) the mailbox: no IMAP, no
	// SMTP AUTH while inactive.
	Active *bool
	// SMTPAccess is set to false by the read-only transition as belt and
	// braces. F0 answer P3 MEASURED that it does NOT block submission on its
	// own (functions.auth.inc.php skips the check when the service is
	// NONE, and no Postfix map reads it); the real lock is the app password
	// re-issued without SMTP. It is still written so an operator reading the
	// Mailcow UI sees the intent.
	SMTPAccess *bool
}

// IsEmpty reports whether the edit would change nothing.
func (e MailboxEdit) IsEmpty() bool {
	return e.Name == nil && e.QuotaMB == nil && e.Active == nil && e.SMTPAccess == nil
}

// EditMailbox applies a partial update to one mailbox.
func (c *Client) EditMailbox(ctx context.Context, mailbox string, e MailboxEdit) error {
	if err := validateMailbox(mailbox); err != nil {
		return err
	}
	if e.IsEmpty() {
		return nil
	}
	attr := map[string]any{}
	if e.Name != nil {
		attr["name"] = *e.Name
	}
	if e.QuotaMB != nil {
		if *e.QuotaMB <= 0 {
			return fmt.Errorf("%w: QuotaMB must be positive", ErrInvalidConfig)
		}
		attr["quota"] = *e.QuotaMB
	}
	if e.Active != nil {
		attr["active"] = boolInt(*e.Active)
	}
	if e.SMTPAccess != nil {
		attr["smtp_access"] = boolInt(*e.SMTPAccess)
	}
	// items is a FLAT array (F0 §4): the anyOf wrapper fails username_invalid.
	payload := map[string]any{"items": []string{mailbox}, "attr": attr}
	body, err := c.do(ctx, http.MethodPost, "/edit/mailbox", payload)
	if err != nil {
		return err
	}
	return checkAPIResult(body, "mailbox_modified")
}

// DeleteMailbox removes a mailbox and, by Mailcow's own cascade (F0 answer
// P2), its app passwords. The body is a bare array, like delete/app-passwd.
//
// Deleting a mailbox that does not exist answers access_denied (F0 rule 7:
// that code is also "no such entity"), which is surfaced as an APIError with
// CodeAccessDenied for the caller to decide about.
func (c *Client) DeleteMailbox(ctx context.Context, mailbox string) error {
	if err := validateMailbox(mailbox); err != nil {
		return err
	}
	body, err := c.do(ctx, http.MethodPost, "/delete/mailbox", []string{mailbox})
	if err != nil {
		return err
	}
	return checkAPIResult(body, "mailbox_removed")
}

// GetMailboxRateLimit reads the mailbox's OWN rate limit. A mailbox with no
// limit of its own (one inheriting the domain's) answers `{}`, which is
// returned as the zero RateLimit — subject to the same validated-key rule as
// GetMailbox, since `{}` is also what a rejected key produces.
func (c *Client) GetMailboxRateLimit(ctx context.Context, mailbox string) (RateLimit, error) {
	if err := validateMailbox(mailbox); err != nil {
		return RateLimit{}, err
	}
	body, err := c.do(ctx, http.MethodGet, "/get/rl-mbox/"+url.PathEscape(mailbox), nil)
	if err != nil {
		return RateLimit{}, err
	}
	var rl RateLimit
	if err := json.Unmarshal(body, &rl); err != nil {
		return RateLimit{}, fmt.Errorf("%w: decoding rate limit: %w", ErrUnexpectedResponse, err)
	}
	if rl.IsZero() && !c.validated.Load() {
		return RateLimit{}, c.emptyObject(fmt.Sprintf("rate limit of %q", mailbox))
	}
	return rl, nil
}

// validFrames are the rate-limit windows Mailcow accepts.
var validFrames = map[string]bool{"s": true, "m": true, "h": true, "d": true}

// SetMailboxRateLimit writes a per-mailbox rate limit, overriding the
// domain's for this mailbox only.
func (c *Client) SetMailboxRateLimit(ctx context.Context, mailbox string, rl RateLimit) error {
	if err := validateMailbox(mailbox); err != nil {
		return err
	}
	if rl.Value <= 0 || !validFrames[rl.Frame] {
		return fmt.Errorf("%w: rate limit must be a positive value with frame s, m, h or d; got %d/%q",
			ErrInvalidConfig, rl.Value, rl.Frame)
	}
	payload := map[string]any{
		"items": []string{mailbox},
		"attr":  map[string]any{"rl_value": rl.Value, "rl_frame": rl.Frame},
	}
	body, err := c.do(ctx, http.MethodPost, "/edit/rl-mbox", payload)
	if err != nil {
		return err
	}
	return checkAPIResult(body, "rl_saved")
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
