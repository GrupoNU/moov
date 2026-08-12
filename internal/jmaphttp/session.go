package jmaphttp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"

	"github.com/GrupoNU/moov/internal/jmap"
	"github.com/GrupoNU/moov/internal/jmap/mail"
	"github.com/GrupoNU/moov/internal/version"
)

// The JMAP Session resource (RFC 8620 §2), served DIRECTLY at
// /.well-known/jmap — no redirect. Spike S1 H7 found the redirect variant
// (well-known → /session) confuses real clients and complicates fronting
// proxies; §2.2's "following any redirects" permits a redirect but nothing
// requires one, and serving directly is the strictly simpler conforming
// choice.

// handleSession serves the authenticated Session object.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFromContext(r.Context())
	if !ok {
		writeGenericProblem(w, http.StatusInternalServerError, "authentication context missing")
		return
	}
	// §2: "it is RECOMMENDED to disable HTTP caching altogether" for the
	// session — clients refetch only on a sessionState change.
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	writeJSON(w, http.StatusOK, s.sessionObject(s.baseURL(r), id))
}

// sessionObject builds the RFC 8620 §2 Session object for one authenticated
// account.
//
// Truthfulness rules (regla J1: never advertise what is not enforced):
//
//   - The capability limits come from the SAME jmap.Limits the engine and
//     transport enforce.
//   - accounts contains exactly the caller's own account. Basic auth
//     authenticates one mailbox owner; there are no shared or delegated
//     accounts in phase 1, so isPersonal is true by construction.
//   - isReadOnly is true: the phase-1 server registers no method that can
//     modify state (L2 §1 — read-only by design), and §2 defines the flag as
//     "true if the entire account is read-only". This flips when the write
//     phase lands.
func (s *Server) sessionObject(base string, id *Identity) map[string]any {
	return map[string]any{
		"capabilities": map[string]any{
			jmap.CapCore: s.cfg.Limits.CoreCapability(),
			// §1.3.1 of RFC 8621: the mail capability's value in the SESSION
			// capabilities object is an empty object; the per-account limits
			// live in accountCapabilities below.
			jmap.CapMail: map[string]any{},
		},
		"accounts": map[string]any{
			id.AccountID: map[string]any{
				"name":       id.Account.Email,
				"isPersonal": true,
				"isReadOnly": true,
				"accountCapabilities": map[string]any{
					jmap.CapMail: mailAccountCapability(),
				},
			},
		},
		// §2: "urn:ietf:params:jmap:core SHOULD NOT be present" in
		// primaryAccounts; mail points at the caller's only account.
		"primaryAccounts": map[string]any{
			jmap.CapMail: id.AccountID,
		},
		"username": id.Account.Email,
		"apiUrl":   base + PathAPI,
		// URI Templates (level 1) with the variables §2 REQUIRES for each
		// endpoint; {type} rides the query string as §2 recommends ("due to
		// potential encoding issues with slashes in content types").
		"downloadUrl":    base + "/jmap/download/{accountId}/{blobId}/{name}?accept={type}",
		"uploadUrl":      base + "/jmap/upload/{accountId}",
		"eventSourceUrl": base + "/jmap/eventsource?types={types}&closeafter={closeafter}&ping={ping}",
		"state":          s.sessionState0(base, id),
	}
}

// mailAccountCapability is the urn:ietf:params:jmap:mail account capability
// object (RFC 8621 §1.3.1), with phase-1-truthful values.
func mailAccountCapability() map[string]any {
	return map[string]any{
		// null means no limit (§1.3.1). Moov imposes none of its own: the
		// A6 label model maps labels onto keywords, and mailbox membership
		// mirrors IMAP, where a message lives in one folder.
		"maxMailboxesPerEmail": nil,
		"maxMailboxDepth":      nil,
		// "MUST be at least 100" (§1.3.1). 255 matches what Dovecot accepts
		// for one mailbox name component in practice; with no Mailbox/set in
		// phase 1 nothing can exceed it, and the write phase will enforce it.
		"maxSizeMailboxName": 255,
		// No Email creation in phase 1; 25 MB mirrors the Mailcow default
		// message size ceiling the future submission path inherits.
		"maxSizeAttachmentsPerEmail": 25_000_000,
		// The sort properties Email/query actually accepts — exactly what
		// mail.translateSort implements, no more (J1's declared == applied
		// rule, applied to comparators).
		//
		// "receivedAt" is the one RFC 8621 §4.4.2 says MUST be supported, and
		// it is the store's native date order (S3 shape #1, 9.3 ms p95).
		//
		// "relevance" is not an RFC 8621 property name: §4.4.2 permits extra
		// ones ("The server MAY support sorting based on other properties as
		// well. A client can discover which properties are supported by
		// inspecting the account's capabilities object"), and this server
		// deliberately does NOT name it after any standard property, because
		// what it implements is relevance within a bounded recent window
		// (store.RankCandidateWindow = 200, S3 mitigation #102) rather than a
		// general relevance sort. Advertising it under a standard name would
		// promise the unbounded version that measured 892 ms p95.
		//
		// The §4.4.2 SHOULD list (size, from, to, subject, sentAt, hasKeyword
		// and the inThread variants) is absent because the store has no index
		// that orders by them — advertising them would be the lie this array
		// exists to avoid.
		"emailQuerySortOptions": []string{
			mail.SortReceivedAt,
			mail.SortRelevance,
		},
		// Read-only server: nobody may create mailboxes (§1.3.1).
		"mayCreateTopLevelMailbox": false,
	}
}

// baseURL resolves the external base URL for session links: configuration
// when set, otherwise derived from the request (scheme from the fronting
// proxy's X-Forwarded-Proto or the connection itself, host from Host).
// Deriving from headers is safe here: the URL only feeds the caller's own
// response, so a forged header misleads no one but its sender.
func (s *Server) baseURL(r *http.Request) string {
	if s.cfg.BaseURL != "" {
		return s.cfg.BaseURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if xf := r.Header.Get("X-Forwarded-Proto"); xf == "https" || xf == "http" {
		scheme = xf
	}
	return scheme + "://" + r.Host
}

// sessionState computes the caller's session "state" string for API
// responses (Response.sessionState, §3.4 — "the current value of the 'state'
// string on the Session object").
func (s *Server) sessionState(r *http.Request, id *Identity) string {
	return s.sessionState0(s.baseURL(r), id)
}

// sessionState0 fingerprints everything the Session object is built from, so
// the string changes exactly when the object would (§2: "If the value of any
// other property on the Session object changes, this string will change"):
// the account row (email/state/updatedAt), the URLs' base, the advertised
// limits, and the server build (a deploy may change capabilities).
func (s *Server) sessionState0(base string, id *Identity) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%d\x00%s\x00%s\x00%d\x00%+v\x00%s",
		base,
		id.Account.ID,
		id.Account.Email,
		id.Account.State,
		id.Account.UpdatedAt.UTC().UnixNano(),
		s.cfg.Limits,
		version.Get().Version,
	)
	return hex.EncodeToString(h.Sum(nil))[:16]
}
