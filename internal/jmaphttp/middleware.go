package jmaphttp

import (
	"context"
	"fmt"
	"net/http"
	"runtime/debug"
	"time"
)

// identityKey is the private context key carrying the authenticated Identity.
type identityKey struct{}

// identityFromContext returns the authenticated identity placed by
// requireAuth.
func identityFromContext(ctx context.Context) (*Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(*Identity)
	return id, ok
}

// requireAuth gates a route behind the J-A1 authentication flow and stores
// the resulting identity in the request context.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := s.auth.Authenticate(w, r)
		if !ok {
			return // Authenticate wrote the response
		}
		next(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
	}
}

// statusRecorder captures the status and size of a response for logging and
// for the panic recovery's "were headers already sent" decision.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
	wrote  bool
}

func (rec *statusRecorder) WriteHeader(status int) {
	if !rec.wrote {
		rec.status = status
		rec.wrote = true
	}
	rec.ResponseWriter.WriteHeader(status)
}

func (rec *statusRecorder) Write(p []byte) (int, error) {
	if !rec.wrote {
		rec.status = http.StatusOK
		rec.wrote = true
	}
	n, err := rec.ResponseWriter.Write(p)
	rec.bytes += int64(n)
	return n, err
}

// Flush passes through so a future streaming route (eventsource, phase 2)
// keeps working behind the middleware.
func (rec *statusRecorder) Flush() {
	if f, ok := rec.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// logMiddleware emits one structured slog record per request.
//
// It logs the path but never the query string (a future eventsource query
// carries client state) and never any header — the Authorization header
// passing through this server is a password.
func (s *Server) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(rec, r)
		s.log.Info("jmap http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", clientIP(r),
		)
	})
}

// recoverMiddleware converts a panic anywhere below it into a 500 problem
// response instead of a dead connection — and, critically, instead of a dead
// daemon. (A panic inside a METHOD handler never reaches here: the dispatch
// engine catches it per-call and answers the RFC 8620 §3.6.2 serverFail
// error response. This middleware is the outer hull for everything else —
// routing, auth, marshaling.)
func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec, isRec := w.(*statusRecorder)
		defer func() {
			if p := recover(); p != nil {
				s.log.Error("jmaphttp: panic recovered",
					"method", r.Method,
					"path", r.URL.Path,
					"panic", fmt.Sprint(p),
					"stack", string(debug.Stack()),
				)
				// Only write if the response has not started; a half-sent
				// response cannot be repaired, and the connection is closed
				// by the server anyway.
				if !isRec || !rec.wrote {
					writeGenericProblem(w, http.StatusInternalServerError, "internal server error")
				}
			}
		}()
		next.ServeHTTP(w, r)
	})
}
