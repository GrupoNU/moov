package jmaphttp

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/GrupoNU/moov/internal/jmap"
)

// The upload endpoint — RFC 8620 §6.1, real since W3 (it answered 501 through
// phase 1, and still does on a deployment with no uploader wired).
//
// §6.1: "To upload a file, the client submits an authenticated POST request
// to the file upload resource ... The Content-Type MUST be set to the media
// type of the file ... The response MUST be of type 'application/json' and
// consist of a single JSON object" carrying accountId, blobId, type and size.
//
// # Account scoping and retention
//
// An uploaded blob lands in the content-addressed store WITH a reference
// owned by the uploading account (mail.Adapter.UploadBlob), which is what
// makes it downloadable by its uploader and attachable by Email/set create —
// and makes it invisible to every other account, the same no-oracle rule as
// download. §6.1's retention clause ("The server SHOULD use a separate quota
// for uploaded blobs ... MAY delete unreferenced uploads after an hour") is
// honored generously: the reference pins the blob until the pin sweep
// releases it, after which the ordinary GC grace period still applies.
//
// # Limits, declared == applied
//
// maxSizeUpload bounds the body via MaxBytesReader — the same enforcement
// shape maxSizeRequest has on the API route — and maxConcurrentUpload gets
// its own per-user gate, both from the very Limits struct the session
// advertises (the J1 rule).

// BlobUploader stores one uploaded blob for an account. mail.Adapter
// satisfies it; nil keeps the route answering 501 exactly as before W3.
type BlobUploader interface {
	// UploadBlob stores the bytes and records the account's reference,
	// returning the blobId and the stored size.
	UploadBlob(ctx context.Context, accountID int64, r io.Reader) (blobID string, size int64, err error)
}

// handleUpload serves POST /jmap/upload/{accountId} (RFC 8620 §6.1).
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFromContext(r.Context())
	if !ok {
		writeGenericProblem(w, http.StatusInternalServerError, "authentication context missing")
		return
	}
	if s.uploader == nil {
		writeGenericProblem(w, http.StatusNotImplemented,
			"upload is not available on this server (no blob store is wired)")
		return
	}

	// §6.1's {accountId} names the account to bill the upload to; this server
	// serves exactly the caller's own, and a foreign one is the same 404 as
	// everywhere else — never a 403 that confirms the account exists.
	accountID, err := jmap.DecodeAccountID(r.PathValue("accountId"))
	if err != nil || accountID != id.Account.ID {
		writeGenericProblem(w, http.StatusNotFound, "not found")
		return
	}

	// maxConcurrentUpload (§2), per authenticated user, same shape as the API
	// route's request gate.
	user := strings.ToLower(id.Account.Email)
	if !s.uploadGate.tryAcquire(user) {
		w.Header().Set("Retry-After", "2")
		writeRequestError(w, jmap.NewLimitError(http.StatusTooManyRequests,
			"maxConcurrentUpload", "too many concurrent uploads for this user"))
		return
	}
	defer s.uploadGate.release(user)

	// maxSizeUpload (§2): the body is hard-capped by the declared value;
	// MaxBytesReader fails the copy mid-stream so an oversized upload costs
	// bandwidth only up to the limit.
	body := http.MaxBytesReader(w, r.Body, s.cfg.Limits.MaxSizeUpload)
	blobID, size, err := s.uploader.UploadBlob(r.Context(), accountID, body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeRequestError(w, jmap.NewLimitError(http.StatusRequestEntityTooLarge,
				"maxSizeUpload", "the uploaded file exceeds maxSizeUpload"))
			return
		}
		s.log.Error("jmap: storing an upload failed", "account_id", accountID, "error", err)
		writeGenericProblem(w, http.StatusInternalServerError, "storing the upload failed")
		return
	}

	// §6.1: type is "as specified in the Content-Type header of the upload
	// HTTP request" — echoed, not trusted for anything: it is data the client
	// will hand back in a create, where the assembler treats it as a label.
	contentType := "application/octet-stream"
	if mt, params, perr := mime.ParseMediaType(r.Header.Get("Content-Type")); perr == nil {
		contentType = mime.FormatMediaType(mt, params)
	}

	// 201: the request created a server-side resource with an id of its own.
	writeJSON(w, http.StatusCreated, map[string]any{
		"accountId": id.AccountID,
		"blobId":    blobID,
		"type":      contentType,
		"size":      size,
	})
}
