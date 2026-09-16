package jmaphttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/GrupoNU/moov/internal/accounts"
)

// The handlers of the accounts API (contract §2). admin_accounts.go holds the
// transport's two rules — the no-oracle 404 and the second authentication
// class; this file holds the routes themselves.
//
// Every handler is the same three steps in the same order: decode and
// validate the body against §2.5, call internal/accounts, render. Nothing
// here decides anything about the state machine — a handler that looked at
// account.State to choose a status would be a second copy of §2.4, and the
// first copy would then be the one that stops being true.

// --- the wire shapes ---------------------------------------------------------

// accountBody is the account resource of §2.3.
//
// It is a hand-written struct rather than accounts.Account with tags, because
// the wire shape and the domain value are two different things with two
// different reasons to change: the JSON names, the nesting and the
// null-versus-absent choices are the CONTRACT, pinned by the OpenAPI
// document, and they must not move because someone renamed a Go field.
type accountBody struct {
	Address   string     `json:"address"`
	Domain    string     `json:"domain"`
	Name      string     `json:"name"`
	State     string     `json:"state"`
	ReadOnly  bool       `json:"readOnly"`
	Suspended bool       `json:"suspended"`
	Quota     quotaBody  `json:"quota"`
	Limits    limitsBody `json:"limits"`
	Sync      syncBody   `json:"sync"`

	// The four nullable timestamps. §2.3 publishes them as `null`, not as
	// absent, so every one is a pointer WITHOUT omitempty: a portal reading
	// `readOnlySince` gets the key whether or not the account is read-only.
	LastAccessAt  *string `json:"lastAccessAt"`
	ReadOnlySince *string `json:"readOnlySince"`
	SuspendedAt   *string `json:"suspendedAt"`
	DeletingSince *string `json:"deletingSince"`

	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type quotaBody struct {
	LimitMB   int   `json:"limitMB"`
	UsedBytes int64 `json:"usedBytes"`
	Messages  int64 `json:"messages"`
}

type limitsBody struct {
	SendPerDay           int `json:"sendPerDay"`
	RecipientsPerMessage int `json:"recipientsPerMessage"`
	AttachmentMB         int `json:"attachmentMB"`
}

type syncBody struct {
	State      string  `json:"state"`
	LastSyncAt *string `json:"lastSyncAt"`
	Messages   int64   `json:"messages"`
}

// rfc3339Milli is §2.3's timestamp format: RFC 3339 UTC with milliseconds.
// One spelling, used by every field, so a consumer's parser never meets two.
const rfc3339Milli = "2006-01-02T15:04:05.000Z"

func formatTime(t time.Time) string { return t.UTC().Format(rfc3339Milli) }

func formatTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := formatTime(*t)
	return &s
}

// renderAccount maps the domain resource onto the wire.
func renderAccount(a accounts.Account) accountBody {
	return accountBody{
		Address:   a.Address,
		Domain:    a.Domain,
		Name:      a.Name,
		State:     string(a.State),
		ReadOnly:  a.ReadOnly,
		Suspended: a.Suspended,
		Quota: quotaBody{
			LimitMB:   a.Quota.LimitMB,
			UsedBytes: a.Quota.UsedBytes,
			Messages:  a.Quota.Messages,
		},
		Limits: limitsBody{
			SendPerDay:           a.Limits.SendPerDay,
			RecipientsPerMessage: a.Limits.RecipientsPerMessage,
			AttachmentMB:         a.Limits.AttachmentMB,
		},
		Sync: syncBody{
			State:      string(a.Sync.State),
			LastSyncAt: formatTimePtr(a.Sync.LastSyncAt),
			Messages:   a.Sync.Messages,
		},
		LastAccessAt:  formatTimePtr(a.LastAccessAt),
		ReadOnlySince: formatTimePtr(a.ReadOnlySince),
		SuspendedAt:   formatTimePtr(a.SuspendedAt),
		DeletingSince: formatTimePtr(a.DeletingSince),
		CreatedAt:     formatTime(a.CreatedAt),
		UpdatedAt:     formatTime(a.UpdatedAt),
	}
}

// exportBody is the export resource of §2.6. The three sub-objects are
// pointers because the contract types them as "object|null" and says exactly
// when each is present: progress while running, download and manifest only
// when ready.
type exportBody struct {
	Status      string              `json:"status"`
	ID          *string             `json:"id"`
	RequestedAt *string             `json:"requestedAt"`
	CompletedAt *string             `json:"completedAt"`
	Progress    *exportProgressBody `json:"progress"`
	Download    *exportDownloadBody `json:"download"`
	Manifest    *exportManifestBody `json:"manifest"`
	Error       *string             `json:"error"`
}

type exportProgressBody struct {
	MessagesDone  int `json:"messagesDone"`
	MessagesTotal int `json:"messagesTotal"`
}

type exportDownloadBody struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expiresAt"`
	Bytes     int64  `json:"bytes"`
}

type exportManifestBody struct {
	Messages  int    `json:"messages"`
	Mailboxes int    `json:"mailboxes"`
	SHA256    string `json:"sha256"`
}

// renderExport maps the domain view onto the wire, presence by presence.
func renderExport(v accounts.ExportView) exportBody {
	b := exportBody{Status: string(v.Status)}
	if v.Status == accounts.ExportNone {
		// §2.6: "none" is a 200 with nothing else to say. Every other field
		// stays null so a portal's parser meets one shape, not two.
		return b
	}
	if v.ID != "" {
		id := v.ID
		b.ID = &id
	}
	b.RequestedAt = formatTimePtr(v.RequestedAt)
	b.CompletedAt = formatTimePtr(v.CompletedAt)

	if v.Status == accounts.ExportRunning {
		b.Progress = &exportProgressBody{MessagesDone: v.MessagesDone, MessagesTotal: v.MessagesTotal}
	}
	if v.Status == accounts.ExportReady {
		if v.DownloadURL != "" && v.ExpiresAt != nil {
			b.Download = &exportDownloadBody{
				URL: v.DownloadURL, ExpiresAt: formatTime(*v.ExpiresAt), Bytes: v.Bytes,
			}
		}
		b.Manifest = &exportManifestBody{
			Messages: v.Messages, Mailboxes: v.Mailboxes, SHA256: v.SHA256,
		}
	}
	if v.Status == accounts.ExportFailed && v.Error != "" {
		e := v.Error
		b.Error = &e
	}
	return b
}

// --- request bodies ----------------------------------------------------------

// createRequestBody is POST /admin/accounts. Unknown fields are refused
// (§2.2), so a consumer's typo is a 400 rather than a silent default.
type createRequestBody struct {
	Address string       `json:"address"`
	Name    string       `json:"name"`
	QuotaMB *int         `json:"quotaMB"`
	Limits  *limitsPatch `json:"limits"`
}

// updateRequestBody is PATCH /admin/accounts/{a}: every field optional.
type updateRequestBody struct {
	Name    *string      `json:"name"`
	QuotaMB *int         `json:"quotaMB"`
	Limits  *limitsPatch `json:"limits"`
}

type limitsPatch struct {
	SendPerDay           *int `json:"sendPerDay"`
	RecipientsPerMessage *int `json:"recipientsPerMessage"`
	AttachmentMB         *int `json:"attachmentMB"`
}

func (l *limitsPatch) domain() accounts.Limits {
	if l == nil {
		return accounts.Limits{}
	}
	return accounts.Limits{
		SendPerDay:           l.SendPerDay,
		RecipientsPerMessage: l.RecipientsPerMessage,
		AttachmentMB:         l.AttachmentMB,
	}
}

// transitionRequestBody is the optional body of suspend/resume/readonly and
// the POST export: a reason for the audit line, never shown to the mailbox
// user.
type transitionRequestBody struct {
	Reason string `json:"reason"`
}

// deleteRequestBody is §2.5's confirmation.
type deleteRequestBody struct {
	Confirm string `json:"confirm"`
}

// decodeAccountsBody reads a JSON body under §2.2's rules: the media type,
// the 16 KiB cap, and unknown fields refused.
//
// It returns false having ALREADY written the response, so a handler's
// decode step is one `if !ok { return }` and cannot forget a status.
//
// optional says whether an empty body is acceptable. The transitions take a
// body that may legitimately be absent (a suspend with no reason), and a
// consumer that sends no Content-Type for a body it does not have is not
// making a mistake worth a 415.
func decodeAccountsBody(w http.ResponseWriter, r *http.Request, dst any, optional bool) bool {
	ct := strings.TrimSpace(r.Header.Get("Content-Type"))
	if ct == "" && optional && r.ContentLength == 0 {
		return true
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil || mt != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, reasonBody{
			Reason: "Content-Type must be application/json",
		})
		return false
	}

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAccountsBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, reasonBody{Reason: "the body is too large"})
			return false
		}
		if errors.Is(err, io.EOF) && optional {
			// An empty body with a JSON content type: the caller declared the
			// type and sent nothing, which for an optional body is the same
			// as sending {}.
			return true
		}
		writeJSON(w, http.StatusBadRequest, fieldErrorBody{
			Field: fieldNameFromDecodeError(err), Reason: decodeReason(err),
		})
		return false
	}
	return true
}

// decodeReason turns a json error into §2.2's prose without leaking a Go type
// name into the contract's vocabulary where it can be avoided.
func decodeReason(err error) string {
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) {
		return "must be of type " + ute.Type.String()
	}
	if strings.Contains(err.Error(), "unknown field") {
		return "unknown field"
	}
	return "the body is not a valid JSON object"
}

// fieldNameFromDecodeError extracts the offending field for §2.2's `field`,
// which is "" when the body itself is not a JSON object.
func fieldNameFromDecodeError(err error) string {
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) && ute.Field != "" {
		return ute.Field
	}
	// encoding/json spells this one only in prose: `json: unknown field "x"`.
	const marker = `unknown field "`
	if i := strings.Index(err.Error(), marker); i >= 0 {
		rest := err.Error()[i+len(marker):]
		if j := strings.Index(rest, `"`); j > 0 {
			return rest[:j]
		}
	}
	return ""
}

// pathAddress reads and normalizes the {address} path variable.
//
// A malformed address in the PATH is a 404, not a 400: the path names a
// RESOURCE, and a resource whose name cannot exist does not exist. That also
// keeps §2.1's promise exactly — a probe cannot tell a malformed address from
// a foreign-domain one from an absent one.
func pathAddress(r *http.Request) (string, bool) {
	raw := r.PathValue("address")
	addr, ferr := accounts.NormalizeAddress("address", raw)
	if ferr != nil {
		return "", false
	}
	return addr, true
}

// callOf builds the per-request context of one write.
func callOf(c serviceCall, reason string) accounts.Call {
	return accounts.Call{Actor: c.actor, RequestID: c.requestID, Reason: reason}
}

// readTransition decodes the optional {"reason"} body shared by suspend,
// resume, readonly and POST export, validating it under §2.5.
func readTransition(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body transitionRequestBody
	if !decodeAccountsBody(w, r, &body, true) {
		return "", false
	}
	reason, ferr := accounts.ValidateReason(body.Reason)
	if ferr != nil {
		writeJSON(w, http.StatusBadRequest, fieldErrorBody{Field: ferr.Field, Reason: ferr.Reason})
		return "", false
	}
	return reason, true
}

// --- the handlers ------------------------------------------------------------

// handleAccountCreate serves POST /admin/accounts (§2.4 create).
//
// 201 the first time, 200 on every repeat: the Service decides which, because
// it is the one that knows whether it created anything. The handler only
// renders — a status chosen here from the body's shape would be a guess.
func (s *Server) handleAccountCreate(w http.ResponseWriter, r *http.Request, c serviceCall) {
	var body createRequestBody
	if !decodeAccountsBody(w, r, &body, false) {
		return
	}

	address, ferr := accounts.NormalizeAddress("address", body.Address)
	if ferr != nil {
		s.writeAccountsError(w, r, ferr)
		return
	}
	name, ferr := accounts.ValidateName("name", body.Name)
	if ferr != nil {
		s.writeAccountsError(w, r, ferr)
		return
	}
	quotaMB := accounts.DefaultQuotaMB
	if body.QuotaMB != nil {
		quotaMB = *body.QuotaMB
		if ferr := accounts.ValidateQuota("quotaMB", quotaMB, s.accountsAPI.svc.MaxQuotaMB()); ferr != nil {
			s.writeAccountsError(w, r, ferr)
			return
		}
	}
	limits := body.Limits.domain()
	if ferr := limits.Validate(); ferr != nil {
		s.writeAccountsError(w, r, ferr)
		return
	}

	acct, created, err := s.accountsAPI.svc.Create(r.Context(), callOf(c, ""), accounts.CreateRequest{
		Address: address, Name: name, QuotaMB: quotaMB, Limits: limits,
	})
	if err != nil {
		s.writeAccountsError(w, r, err)
		return
	}
	if created {
		// §2.7's Location. It is built from the route constant and the
		// NORMALIZED address, so it names the resource the server actually
		// owns rather than echoing whatever casing the caller sent.
		w.Header().Set("Location", PathAdminAccounts+"/"+acct.Address)
		writeJSON(w, http.StatusCreated, renderAccount(acct))
		return
	}
	writeJSON(w, http.StatusOK, renderAccount(acct))
}

// handleAccountGet serves GET /admin/accounts/{address}.
func (s *Server) handleAccountGet(w http.ResponseWriter, r *http.Request, c serviceCall) {
	address, ok := pathAddress(r)
	if !ok {
		writeNotFound(w)
		return
	}
	acct, err := s.accountsAPI.svc.Get(r.Context(), c.actor, address)
	if err != nil {
		s.writeAccountsError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, renderAccount(acct))
}

// handleAccountUpdate serves PATCH /admin/accounts/{address} (deviation D2).
func (s *Server) handleAccountUpdate(w http.ResponseWriter, r *http.Request, c serviceCall) {
	address, ok := pathAddress(r)
	if !ok {
		writeNotFound(w)
		return
	}
	var body updateRequestBody
	if !decodeAccountsBody(w, r, &body, false) {
		return
	}

	req := accounts.UpdateRequest{QuotaMB: body.QuotaMB, Limits: body.Limits.domain()}
	if body.Name != nil {
		name, ferr := accounts.ValidateName("name", *body.Name)
		if ferr != nil {
			s.writeAccountsError(w, r, ferr)
			return
		}
		req.Name = &name
	}
	if body.QuotaMB != nil {
		if ferr := accounts.ValidateQuota("quotaMB", *body.QuotaMB, s.accountsAPI.svc.MaxQuotaMB()); ferr != nil {
			s.writeAccountsError(w, r, ferr)
			return
		}
	}
	if ferr := req.Limits.Validate(); ferr != nil {
		s.writeAccountsError(w, r, ferr)
		return
	}
	if req.IsEmpty() {
		// The OpenAPI schema's minProperties: 1. A PATCH that changes nothing
		// is a caller mistake worth naming, not a 200 that pretends work
		// happened.
		writeJSON(w, http.StatusBadRequest, fieldErrorBody{
			Field: "", Reason: "at least one field must be present",
		})
		return
	}

	acct, err := s.accountsAPI.svc.Update(r.Context(), callOf(c, ""), address, req)
	if err != nil {
		s.writeAccountsError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, renderAccount(acct))
}

// handleAccountDelete serves DELETE /admin/accounts/{address} (§2.5's
// confirmation, §2.4's 202).
func (s *Server) handleAccountDelete(w http.ResponseWriter, r *http.Request, c serviceCall) {
	address, ok := pathAddress(r)
	if !ok {
		writeNotFound(w)
		return
	}
	var body deleteRequestBody
	if !decodeAccountsBody(w, r, &body, false) {
		return
	}
	// §2.5: the confirmation must repeat the path address after lower-casing.
	// It is compared to the NORMALIZED path address, so "A@x" in the path and
	// "a@x" in the body agree — the confirmation is about intent, not about
	// byte-equality with a string the caller already sent twice.
	if strings.ToLower(strings.TrimSpace(body.Confirm)) != address {
		writeJSON(w, http.StatusBadRequest, fieldErrorBody{
			Field: "confirm", Reason: "must repeat the address being deleted",
		})
		return
	}

	acct, err := s.accountsAPI.svc.Delete(r.Context(), callOf(c, ""), address)
	if err != nil {
		s.writeAccountsError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, renderAccount(acct))
}

// transitionHandler builds the handler of one §2.4 transition. Suspend,
// resume and readonly differ only in which method they call, so they share
// everything else rather than being three copies with three chances to drift.
func (s *Server) transitionHandler(
	apply func(*accounts.Service) func(context.Context, accounts.Call, string) (accounts.Account, error),
) func(http.ResponseWriter, *http.Request, serviceCall) {
	return func(w http.ResponseWriter, r *http.Request, c serviceCall) {
		address, ok := pathAddress(r)
		if !ok {
			writeNotFound(w)
			return
		}
		reason, ok := readTransition(w, r)
		if !ok {
			return
		}
		acct, err := apply(s.accountsAPI.svc)(r.Context(), callOf(c, reason), address)
		if err != nil {
			s.writeAccountsError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, renderAccount(acct))
	}
}

// handleExportStart serves POST /admin/accounts/{address}/export (§2.6).
func (s *Server) handleExportStart(w http.ResponseWriter, r *http.Request, c serviceCall) {
	address, ok := pathAddress(r)
	if !ok {
		writeNotFound(w)
		return
	}
	reason, ok := readTransition(w, r)
	if !ok {
		return
	}
	view, err := s.accountsAPI.svc.StartExport(r.Context(), callOf(c, reason), address)
	if err != nil {
		s.writeAccountsError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, renderExport(view))
}

// handleExportGet serves GET /admin/accounts/{address}/export (§2.6).
func (s *Server) handleExportGet(w http.ResponseWriter, r *http.Request, c serviceCall) {
	address, ok := pathAddress(r)
	if !ok {
		writeNotFound(w)
		return
	}
	view, err := s.accountsAPI.svc.GetExport(r.Context(), c.actor, address)
	if err != nil {
		s.writeAccountsError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, renderExport(view))
}

// handleExportDownload serves GET /admin/exports/{exportId} — the signed
// capability of §2.6.
//
// It is PUBLIC in the route table's sense and takes no service-account key at
// all: the URL is handed to a browser tab, which attaches no Authorization
// header. Its authority is the HMAC in the query string, bound to the origin,
// the export id and the expiry (accounts/signing.go), and it grants exactly
// one thing — this one zip, until this one expiry.
//
// Every refusal but one is the generic 404, so a signature probe learns
// nothing; the single exception is 410 for an export that WAS ready and has
// since been swept, which is information the holder of a valid signature
// already had.
func (s *Server) handleExportDownload(w http.ResponseWriter, r *http.Request) {
	a := s.accountsAPI
	if a == nil || !a.enabled || a.exports == nil {
		writeNotFound(w)
		return
	}
	id := r.PathValue("exportId")
	expRaw := r.URL.Query().Get("exp")
	sig := r.URL.Query().Get("sig")
	unix, err := strconv.ParseInt(expRaw, 10, 64)
	if err != nil {
		writeNotFound(w)
		return
	}

	rc, export, err := a.exports.OpenDownload(r.Context(), s.exportOrigin(r), id, time.Unix(unix, 0), sig)
	if err != nil {
		switch {
		case errors.Is(err, accounts.ErrExportPurged):
			writeGenericProblem(w, http.StatusGone, "this export has been purged; request a new one")
		case errors.Is(err, accounts.ErrNotFound):
			writeNotFound(w)
		default:
			s.log.Error("accounts api: opening an export download failed", "error", err, "export", id)
			writeNotFound(w)
		}
		return
	}
	defer func() { _ = rc.Close() }()

	// The filename is built from the account address and the completion date,
	// quoted, with every character the header cannot carry removed — a
	// Content-Disposition is a place a stored string reaches a client's
	// filesystem, so nothing goes in that was not filtered here.
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+exportFilename(export.Address, export.CompletedAt)+`"`)
	if export.Bytes > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(export.Bytes, 10))
	}
	// A capability URL must never be cached by a shared proxy: the zip is one
	// mailbox's entire mail.
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, rc); err != nil {
		// The status is already on the wire; all that is left is the record.
		s.log.Warn("accounts api: the export download was interrupted", "error", err, "export", id)
	}
}

// exportOrigin is the origin the signature was minted against: the scheme and
// host THIS request arrived on. It must be derived the same way SignedURL
// built it, or no signature ever verifies.
func (s *Server) exportOrigin(r *http.Request) string {
	return s.baseURL(r)
}

// exportFilename builds the download's filename from the address and the
// completion date, keeping only characters that are safe in a quoted
// Content-Disposition.
func exportFilename(address string, completed *time.Time) string {
	day := time.Now().UTC().Format("2006-01-02")
	if completed != nil {
		day = completed.UTC().Format("2006-01-02")
	}
	var b strings.Builder
	for _, ch := range address {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
			b.WriteRune(ch)
		case ch == '@', ch == '.', ch == '-', ch == '_':
			b.WriteRune(ch)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		b.WriteString("export")
	}
	return b.String() + "-" + day + ".zip"
}
