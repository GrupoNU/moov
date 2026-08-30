package jmaphttp

import (
	"net/http"
)

// GET /jmap/forwarding/verify — the E6 verification endpoint (GC-4).
//
// The flow it completes: ForwardingAddress/set create sent a token to the
// DESTINATION address through the server's own sending path. The destination
// owner relays the token (or clicks the link, which the PWA turns into this
// call) and the ACCOUNT OWNER's authenticated session consumes it here. The
// consent property holds because the token only ever travels through the
// destination mailbox: the requester cannot produce it without the
// destination owner's cooperation — the same confirmation-code shape Gmail's
// forwarding verification uses (canon §3 ADAPT).
//
// Authenticated like every aux route (the route table's default): the token
// alone is NOT a credential — it is bound to the account by the keyring's
// AAD, so it is only consumable by the very account that requested the
// verification, and only until its sealed expiry.

// PathForwardingVerify is the route. GET with ?token=..., authenticated.
const PathForwardingVerify = "/jmap/forwarding/verify"

// handleForwardingVerify serves the route. 501 when the deployment did not
// wire forwarding (the same degradation shape the push endpoint uses).
func (s *Server) handleForwardingVerify(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Forwarding == nil {
		writeGenericProblem(w, http.StatusNotImplemented, "forwarding verification is not enabled on this server")
		return
	}
	id, ok := identityFromContext(r.Context())
	if !ok {
		writeGenericProblem(w, http.StatusInternalServerError, "authentication context missing")
		return
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		writeGenericProblem(w, http.StatusBadRequest, "the token query parameter is required")
		return
	}
	email, err := s.cfg.Forwarding.VerifyForwarding(r.Context(), id.Account.ID, token)
	if err != nil {
		// One refusal for every internal reason (bad MAC, expired, foreign
		// account, destroyed row): the same no-oracle rule the access tokens
		// follow. 403, not 401 — a challenge would pop the browser dialog.
		writeGenericProblem(w, http.StatusForbidden, "invalid or expired verification token")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"verified": email})
}
