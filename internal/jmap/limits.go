package jmap

// Limits is every limit of the core capability object (RFC 8620 §2),
// declared once.
//
// The rule of this struct — an explicit AC of epic J1 — is declared ==
// applied: the session endpoint advertises these values and every enforcement
// point reads the same field. Nothing may advertise a limit it does not
// enforce, and nothing may enforce a number that is not advertised. The test
// suite iterates the struct's fields by reflection and fails if any field
// lacks a registered enforcement proof.
type Limits struct {
	// MaxSizeUpload is "the maximum file size, in octets, that the server
	// will accept for a single file upload" (§2). Enforced by the upload
	// endpoint — which in phase 1 accepts no uploads at all (501), so no
	// upload can exceed it; J-later enforces it against real bodies.
	MaxSizeUpload int64

	// MaxConcurrentUpload is "the maximum number of concurrent requests the
	// server will accept to the upload endpoint" (§2). Same phase-1 status as
	// MaxSizeUpload.
	MaxConcurrentUpload int

	// MaxSizeRequest is "the maximum size, in octets, that the server will
	// accept for a single request to the API endpoint" (§2). Enforced with an
	// http.MaxBytesReader on the API route; exceeding it yields the §3.6.1
	// "limit" problem naming maxSizeRequest.
	MaxSizeRequest int64

	// MaxConcurrentRequests is "the maximum number of concurrent requests the
	// server will accept to the API endpoint" (§2). Enforced per
	// authenticated user by the API route's concurrency gate.
	MaxConcurrentRequests int

	// MaxCallsInRequest is "the maximum number of method calls the server
	// will accept in a single request to the API endpoint" (§2). Enforced by
	// Engine.Process before dispatch.
	MaxCallsInRequest int

	// MaxObjectsInGet is "the maximum number of objects that the client may
	// request in a single /get type method call" (§2). Enforced by every /get
	// handler through CheckObjectsInGet — the J2 contract requires calling it
	// before touching the store.
	MaxObjectsInGet int

	// MaxObjectsInSet is "the maximum number of objects the client may send
	// to create, update, or destroy in a single /set type method call",
	// combined total (§2). Phase 1 registers no /set methods, so no set can
	// exceed it; the /set contract of a later phase requires
	// CheckObjectsInSet.
	MaxObjectsInSet int
}

// DefaultLimits returns the server's default limits. Each meets or exceeds
// the suggested minimum of RFC 8620 §2, so clients tuned to those minimums
// never hit an artificial wall.
func DefaultLimits() Limits {
	return Limits{
		MaxSizeUpload:         50_000_000, // suggested minimum (§2)
		MaxConcurrentUpload:   4,          // suggested minimum (§2)
		MaxSizeRequest:        10_000_000, // suggested minimum (§2)
		MaxConcurrentRequests: 8,          // suggested minimum is 4
		MaxCallsInRequest:     32,         // suggested minimum is 16
		MaxObjectsInGet:       500,        // suggested minimum (§2)
		MaxObjectsInSet:       500,        // suggested minimum (§2)
	}
}

// CoreCapability renders the "urn:ietf:params:jmap:core" capability object of
// the Session (RFC 8620 §2) from these limits — the same fields the engine
// and the HTTP layer enforce, by construction.
//
// collationAlgorithms is the RFC 4790 collation list. Phase 1 implements no
// /query methods at all, so Moov truthfully advertises none; J3 extends this
// when Email/query lands with its real comparator support (regla: never
// advertise what is not enforced).
func (l Limits) CoreCapability() map[string]any {
	return map[string]any{
		"maxSizeUpload":         l.MaxSizeUpload,
		"maxConcurrentUpload":   l.MaxConcurrentUpload,
		"maxSizeRequest":        l.MaxSizeRequest,
		"maxConcurrentRequests": l.MaxConcurrentRequests,
		"maxCallsInRequest":     l.MaxCallsInRequest,
		"maxObjectsInGet":       l.MaxObjectsInGet,
		"maxObjectsInSet":       l.MaxObjectsInSet,
		"collationAlgorithms":   []string{},
	}
}

// CheckObjectsInGet is the enforcement point for MaxObjectsInGet. Every /get
// handler MUST call it with the number of requested ids before doing any
// work; a violation is the requestTooLarge error of RFC 8620 §5.1.
func (l Limits) CheckObjectsInGet(n int) *MethodError {
	if n > l.MaxObjectsInGet {
		return NewMethodError(CodeRequestTooLarge).
			WithDescription("%d ids requested; the server accepts at most %d per /get call (maxObjectsInGet)",
				n, l.MaxObjectsInGet)
	}
	return nil
}

// CheckObjectsInSet is the enforcement point for MaxObjectsInSet: n is the
// combined create+update+destroy total (RFC 8620 §2), and a violation is the
// requestTooLarge error of §5.3. No phase-1 method calls it yet; it exists so
// the first /set method has exactly one correct thing to do.
func (l Limits) CheckObjectsInSet(n int) *MethodError {
	if n > l.MaxObjectsInSet {
		return NewMethodError(CodeRequestTooLarge).
			WithDescription("%d objects in /set call; the server accepts at most %d (maxObjectsInSet)",
				n, l.MaxObjectsInSet)
	}
	return nil
}
