package jmaphttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// corsRoutes is the concrete instance of every route in the table
// (routes.go), for the preflight sweep. If a route is added there without
// being added here, TestPreflightCoversEveryRoute fails.
var corsRoutes = []struct{ method, path string }{
	{http.MethodGet, "/.well-known/jmap"},
	{http.MethodPost, "/jmap/api"},
	{http.MethodGet, "/jmap/download/a7/blob123/report.pdf"},
	{http.MethodPost, "/jmap/upload/a7"},
	{http.MethodGet, "/jmap/eventsource"},
}

func TestPreflightCoversEveryRoute(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)
	if got, want := len(s.routes()), len(corsRoutes); got != want {
		t.Fatalf("route table has %d routes but the CORS sweep tests %d — update corsRoutes", got, want)
	}

	for _, rt := range corsRoutes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodOptions, rt.path, nil)
			r.Header.Set("Origin", testOrigin)
			r.Header.Set("Access-Control-Request-Method", rt.method)
			r.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
			// Deliberately NO credentials: browsers never authenticate a
			// preflight, so the preflight must not require auth.
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)

			if w.Code != http.StatusNoContent {
				t.Fatalf("preflight status = %d, want 204", w.Code)
			}
			h := w.Header()
			if h.Get("Access-Control-Allow-Origin") != testOrigin {
				t.Fatalf("Allow-Origin = %q", h.Get("Access-Control-Allow-Origin"))
			}
			if h.Get("Access-Control-Allow-Credentials") != "true" {
				t.Fatal("allow-listed origin must get credentials support")
			}
			methods := h.Get("Access-Control-Allow-Methods")
			if !strings.Contains(methods, rt.method) || !strings.Contains(methods, http.MethodOptions) {
				t.Fatalf("Allow-Methods = %q, want %s and OPTIONS", methods, rt.method)
			}
			allowHeaders := h.Get("Access-Control-Allow-Headers")
			if !strings.Contains(allowHeaders, "Authorization") || !strings.Contains(allowHeaders, "Content-Type") {
				t.Fatalf("Allow-Headers = %q (L2 §2.4 requires Authorization and Content-Type)", allowHeaders)
			}
			if h.Get("Access-Control-Max-Age") == "" {
				t.Fatal("no Max-Age on preflight")
			}
			if !strings.Contains(strings.Join(h.Values("Vary"), ","), "Origin") {
				t.Fatal("preflight lacks Vary: Origin")
			}
		})
	}
}

func TestCORSDisallowedOriginGetsNoGrant(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)
	r := httptest.NewRequest(http.MethodOptions, PathAPI, nil)
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("Access-Control-Request-Method", http.MethodPost)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	h := w.Header()
	for _, hd := range []string{
		"Access-Control-Allow-Origin",
		"Access-Control-Allow-Credentials",
		"Access-Control-Allow-Methods",
		"Access-Control-Allow-Headers",
	} {
		if h.Get(hd) != "" {
			t.Errorf("disallowed origin received %s: %q", hd, h.Get(hd))
		}
	}
}

func TestCORSHeadersOnActualResponsesIncludingErrors(t *testing.T) {
	s, _, _, _ := newTestServer(t, nil)

	// Success response.
	w := doReq(s, http.MethodGet, PathWellKnown, "", true, map[string]string{"Origin": testOrigin})
	if w.Header().Get("Access-Control-Allow-Origin") != testOrigin {
		t.Fatal("no CORS grant on the actual response")
	}

	// Error response: a browser client can only see the 401 if the CORS
	// headers are present on it too.
	w = doReq(s, http.MethodGet, PathWellKnown, "", false, map[string]string{"Origin": testOrigin})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != testOrigin {
		t.Fatal("401 without CORS headers is invisible to browser clients")
	}
}

func TestCORSWildcardNeverCombinesWithCredentials(t *testing.T) {
	s, _, _, _ := newTestServer(t, func(c *Config) {
		c.AllowedOrigins = []string{"*"}
	})

	for _, origin := range []string{"https://anywhere.example", "https://elsewhere.example"} {
		r := httptest.NewRequest(http.MethodOptions, PathAPI, nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("Access-Control-Request-Method", http.MethodPost)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)

		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Fatalf("wildcard config: Allow-Origin = %q, want *", got)
		}
		if w.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatal("FORBIDDEN combination: wildcard origin with credentials")
		}
	}
}

func TestCORSDisabledWhenNoOriginsConfigured(t *testing.T) {
	s, _, _, _ := newTestServer(t, func(c *Config) {
		c.AllowedOrigins = nil
	})
	w := doReq(s, http.MethodGet, PathWellKnown, "", true, map[string]string{"Origin": testOrigin})
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("CORS grant emitted with no configured origins")
	}
}
