package jmaphttp

import "net/http"

// CORS (L2 §2.4). Spike S1 H7 established this as a day-one requirement:
// Bulwark in a browser cannot reach a JMAP server on another origin without
// it, and jmap-proxy's total lack of CORS forced the same-origin Caddy
// workaround. Moov implements it properly, behind an explicit allow-list.
//
// # The wildcard-with-credentials rule
//
// The Fetch spec forbids `Access-Control-Allow-Origin: *` combined with
// `Access-Control-Allow-Credentials: true`, and for good reason: it would let
// any website on the internet ride the browser's stored credentials. This
// policy makes the combination unrepresentable rather than merely avoided:
//
//   - a configured origin list ⇒ the matching origin is echoed back (with
//     `Vary: Origin`) and credentials are allowed — what a JMAP webmail
//     doing `fetch(..., {credentials: "include"})` needs;
//   - the single entry "*" ⇒ the literal wildcard is sent and the
//     credentials header is NEVER emitted. Clients that attach Basic auth as
//     an explicit Authorization header (Bulwark's mode) still work, because
//     an explicit header is not "credentials" in the CORS sense;
//   - an empty list ⇒ no CORS headers at all; browsers enforce same-origin.
type corsPolicy struct {
	allowAll bool
	origins  map[string]struct{}
}

func newCORSPolicy(allowed []string) *corsPolicy {
	p := &corsPolicy{origins: make(map[string]struct{}, len(allowed))}
	for _, o := range allowed {
		if o == "*" {
			p.allowAll = true
			continue
		}
		p.origins[o] = struct{}{}
	}
	return p
}

// allows returns the Access-Control-Allow-Origin value to send for a request
// from origin, and whether the origin is allowed at all.
func (p *corsPolicy) allows(origin string) (string, bool) {
	if origin == "" {
		return "", false
	}
	if _, ok := p.origins[origin]; ok {
		return origin, true
	}
	if p.allowAll {
		return "*", true
	}
	return "", false
}

// corsMiddleware sets the CORS response headers on EVERY response — success
// and error alike, because a browser withholds even the status code of a
// response without them, which turns a clean 401 into an undebuggable
// network error in the client.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Vary: Origin unconditionally when a policy exists: the response
		// depends on the Origin header, and a shared cache must know that
		// even for a disallowed origin's (header-less) response.
		if s.cors.allowAll || len(s.cors.origins) > 0 {
			w.Header().Add("Vary", "Origin")
		}
		if allowOrigin, ok := s.cors.allows(r.Header.Get("Origin")); ok {
			w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
			if allowOrigin != "*" {
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
		}
		next.ServeHTTP(w, r)
	})
}
