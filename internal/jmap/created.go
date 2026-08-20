package jmap

import (
	"context"
	"strings"
	"sync"
)

// Creation-id references (RFC 8620 §3.3, §5.3).
//
// §5.3: "Some records may hold references to other records ... To allow this,
// the server MUST resolve creation references: if the value of an Id property
// is a string starting with '#', the rest of the string is a creation id; the
// server MUST look up the corresponding record id and use it instead." §3.3
// scopes the map to the REQUEST: it starts from the client's optional
// createdIds argument, every successful /set create adds to it, and §3.4
// returns the updated map when the client passed one.
//
// This existed nowhere before W3 because nothing needed it: the phase-1
// server had no /set, and W1/W2's shapes never referenced a same-request
// creation. W3's canonical flow (RFC 8621 §7.5) is BUILT on it — Email/set
// creates the draft under a creation id and EmailSubmission/set names it as
// "emailId": "#draft" in the same request — so the machinery arrives now,
// with the engine owning the map's lifecycle and the /set handlers owning the
// entries.

// CreationIDs is one request's creation-id map: client-chosen creation id →
// server-assigned id, in wire form.
//
// It is a mutable, request-scoped value carried in the context. A mutex
// guards it not because one request's methods run concurrently — §3.3 forbids
// that ("The method calls MUST be processed sequentially") — but because a
// data structure whose safety depends on a protocol invariant elsewhere is a
// data structure waiting for a refactor to break it.
type CreationIDs struct {
	mu sync.Mutex
	m  map[string]string
}

// NewCreationIDs builds the request's map, seeded from the client's optional
// createdIds argument (§3.3).
func NewCreationIDs(seed map[string]string) *CreationIDs {
	m := make(map[string]string, len(seed)+4)
	for k, v := range seed {
		m[k] = v
	}
	return &CreationIDs{m: m}
}

// Record adds one successful create. The /set handlers call it for every
// entry they put in a response's "created" map.
func (c *CreationIDs) Record(creationID, serverID string) {
	if c == nil || creationID == "" || serverID == "" {
		return
	}
	c.mu.Lock()
	c.m[creationID] = serverID
	c.mu.Unlock()
}

// Resolve maps a wire id through the creation-id table when it is a "#"
// reference; a plain id passes through unchanged.
//
// ok is false only for a "#" reference that names nothing — which §5.3 makes
// the caller's error to report ("If the creation id is not found, the server
// MUST reject the create/update with an invalidProperties SetError" for the
// /set case; a submission's emailId answers analogously).
func (c *CreationIDs) Resolve(wireID string) (string, bool) {
	rest, isRef := strings.CutPrefix(wireID, "#")
	if !isRef {
		return wireID, true
	}
	if c == nil || rest == "" {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	id, ok := c.m[rest]
	return id, ok
}

// Snapshot returns a copy of the current map — what §3.4's response-side
// createdIds carries.
func (c *CreationIDs) Snapshot() map[string]string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(c.m))
	for k, v := range c.m {
		out[k] = v
	}
	return out
}

type creationIDsKey struct{}

// WithCreationIDs installs the request's creation-id map in the context.
func WithCreationIDs(ctx context.Context, c *CreationIDs) context.Context {
	return context.WithValue(ctx, creationIDsKey{}, c)
}

// CreationIDsFromContext retrieves the request's map. It returns a non-nil,
// empty map when none was installed, so a handler running outside an engine
// (a direct-call test) resolves plain ids and rejects "#" references — the
// same behavior an empty request map produces.
func CreationIDsFromContext(ctx context.Context) *CreationIDs {
	if c, ok := ctx.Value(creationIDsKey{}).(*CreationIDs); ok && c != nil {
		return c
	}
	return NewCreationIDs(nil)
}
