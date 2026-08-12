package jmaphttp

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/GrupoNU/moov/internal/jmap"
)

// problemContentType is the RFC 7807 media type; RFC 8620 §3.6.1 prescribes
// problem-details bodies for request-level errors.
const problemContentType = "application/problem+json"

// writeRequestError writes a JMAP request-level error (RFC 8620 §3.6.1) as
// its problem-details body.
func writeRequestError(w http.ResponseWriter, e *jmap.RequestError) {
	body, err := json.Marshal(e)
	if err != nil {
		// Unreachable with RequestError's fixed shape; kept total.
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", problemContentType)
	w.WriteHeader(e.Status)
	_, _ = w.Write(body)
}

// writeGenericProblem writes an RFC 7807 problem for conditions RFC 8620
// defines no URN for (401/403/404/501/500 outcomes of this transport). The
// "about:blank" type means "the HTTP status code is the whole story"
// (RFC 7807 §4.2).
func writeGenericProblem(w http.ResponseWriter, status int, detail string) {
	body, err := json.Marshal(map[string]any{
		"type":   "about:blank",
		"status": status,
		"detail": detail,
	})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", problemContentType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writeJSON writes v as an application/json response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		slog.Default().Error("jmaphttp: response marshaling failed", "error", err)
		writeGenericProblem(w, http.StatusInternalServerError, "response encoding failed")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
