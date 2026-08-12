package jmap

import "context"

// Caller identifies the authenticated account a request is executing for.
//
// The HTTP layer authenticates (internal/jmaphttp, arbitration J-A1) and puts
// a Caller in the request context before any method handler runs; handlers —
// the mail methods of J2/J3 — read it back with CallerFromContext and MUST
// scope every store read to Caller.AccountID. This type lives here rather
// than in the HTTP package so that handler packages depend only on the
// protocol contract, never on the transport.
type Caller struct {
	// AccountID is the store account id (accounts.id).
	AccountID int64

	// Email is the authenticated address, as stored on the account row.
	Email string
}

// JMAPAccountID is the caller's account id in JMAP wire form.
func (c Caller) JMAPAccountID() string {
	return EncodeAccountID(c.AccountID)
}

// callerKey is the private context key type for Caller.
type callerKey struct{}

// WithCaller returns a context carrying the authenticated caller.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFromContext extracts the authenticated caller. ok is false when the
// context carries none — which for a method handler means a wiring bug in the
// transport, and the only safe answer is a forbidden error, never a guess.
func CallerFromContext(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}
