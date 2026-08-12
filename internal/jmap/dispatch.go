package jmap

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sort"
)

// Handler processes one method call. args is the arguments object with any
// back-references already resolved.
//
// A handler returns either a result — any JSON-marshalable value, typically a
// struct with json tags, which becomes the response's arguments object under
// the method's own name — or a *MethodError from errors.go. It must not
// panic; if it does, the dispatcher converts the panic into a serverFail error
// response and the daemon survives (RFC 8620 §3.6.2 requires the remaining
// method calls to be processed as normal).
//
// The authenticated caller travels in ctx (see Caller / CallerFromContext);
// the HTTP layer guarantees it is present before any handler runs.
//
// Phase-1 methods each produce exactly one response. When a later phase needs
// a multi-response method (/copy makes implicit /set calls, §5.4), the Handler
// type grows then — not speculatively now.
type Handler func(ctx context.Context, args json.RawMessage) (any, *MethodError)

// Registry maps method names to handlers and to the capability each method
// belongs to.
//
// It is populated at startup (Register panics on programmer error, like a
// duplicate name) and read-only afterwards, which is what makes it safe for
// concurrent dispatch without locks.
type Registry struct {
	methods map[string]registeredMethod
}

type registeredMethod struct {
	capability string
	handler    Handler
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{methods: make(map[string]registeredMethod)}
}

// Register adds a method under the capability it belongs to. The capability
// gates dispatch: a request whose "using" set lacks it gets unknownMethod for
// this method (see Engine.Process for the RFC citation).
//
// It panics on a duplicate name or empty arguments: both are wiring bugs that
// must fail at startup, not at request time.
func (r *Registry) Register(name, capability string, h Handler) {
	if name == "" || capability == "" || h == nil {
		panic("jmap: Register requires a name, a capability and a handler")
	}
	if _, dup := r.methods[name]; dup {
		panic(fmt.Sprintf("jmap: method %q registered twice", name))
	}
	r.methods[name] = registeredMethod{capability: capability, handler: h}
}

// MethodNames returns the registered method names, sorted. For logs and tests.
func (r *Registry) MethodNames() []string {
	out := make([]string, 0, len(r.methods))
	for name := range r.methods {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Engine executes JMAP requests: parsing, capability gating, sequential
// dispatch, back-reference resolution, and error mapping. It is stateless per
// request and safe for concurrent use.
type Engine struct {
	registry     *Registry
	limits       Limits
	capabilities map[string]bool
	log          *slog.Logger
}

// NewEngine builds an engine over a registry.
//
// capabilities is the set the server supports — the same set advertised in
// the Session object, which is what keeps "advertised" and "accepted in
// using" mechanically identical. limits must be the same values the session
// advertises (declared == applied is an explicit AC of this epic).
func NewEngine(registry *Registry, limits Limits, capabilities []string, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	caps := make(map[string]bool, len(capabilities))
	for _, c := range capabilities {
		caps[c] = true
	}
	return &Engine{registry: registry, limits: limits, capabilities: caps, log: logger}
}

// Process executes one request body and returns the Response, or a
// request-level error (RFC 8620 §3.6.1) that rejects the request as a whole.
//
// sessionState is the current value of the caller's Session "state" string,
// echoed on the Response per §3.4. It is a parameter rather than an Engine
// field because the session — and therefore its state — is per-account.
func (e *Engine) Process(ctx context.Context, body []byte, sessionState string) (*Response, *RequestError) {
	req, rerr := ParseRequest(body)
	if rerr != nil {
		return nil, rerr
	}

	// unknownCapability (§3.6.1): any member of "using" the server does not
	// support rejects the whole request.
	for _, c := range req.Using {
		if !e.capabilities[c] {
			return nil, NewRequestError(ProblemUnknownCapability,
				fmt.Sprintf("the Request object used capability %q, which is not supported by this server", c))
		}
	}

	// maxCallsInRequest (§3.6.1 limit): enforced from the very Limits struct
	// the session advertises.
	if len(req.MethodCalls) > e.limits.MaxCallsInRequest {
		return nil, NewLimitError(http.StatusBadRequest, "maxCallsInRequest",
			fmt.Sprintf("the request has %d method calls; the server accepts at most %d",
				len(req.MethodCalls), e.limits.MaxCallsInRequest))
	}

	using := make(map[string]bool, len(req.Using))
	for _, c := range req.Using {
		using[c] = true
	}

	// §3.3: "The method calls MUST be processed sequentially, in order."
	// §3.6.2: after a method-level error, "any further method calls in the
	// request MUST then be processed as normal."
	responses := make([]Invocation, 0, len(req.MethodCalls))
	for _, call := range req.MethodCalls {
		responses = append(responses, e.dispatchOne(ctx, call, using, responses))
	}

	return &Response{
		MethodResponses: responses,
		CreatedIDs:      req.CreatedIDs, // §3.4: returned iff given; no /set methods add to it in phase 1
		SessionState:    sessionState,
	}, nil
}

// dispatchOne runs a single method call and returns its response invocation.
func (e *Engine) dispatchOne(ctx context.Context, call Invocation, using map[string]bool, prior []Invocation) Invocation {
	m, known := e.registry.methods[call.Name]
	if !known {
		// §3.6.2: unknownMethod — the server does not recognize this method
		// name.
		return NewMethodError(CodeUnknownMethod).Invocation(call.CallID)
	}

	// Capability gating. RFC 8620 §1.8: "The client MUST opt in to use an
	// extension by passing the appropriate capability identifier in the
	// 'using' array of the Request object ... The server MUST only follow the
	// specifications that are opted into and behave as though it does not
	// implement anything else when processing a request" — reaffirmed for all
	// capabilities by §2: "Clients MUST opt in to any capability it wishes to
	// use (see Section 3.3)". A server behaving as though it does not
	// implement the method answers unknownMethod (§3.6.2). The description is
	// an extra property §3.6.2 permits, so a developer can tell this apart
	// from a typo in the method name.
	if !using[m.capability] {
		return NewMethodError(CodeUnknownMethod).
			WithDescription("method %q requires capability %q in the request's using list", call.Name, m.capability).
			Invocation(call.CallID)
	}

	args, merr := ResolveBackReferences(call.Args, prior)
	if merr != nil {
		return merr.Invocation(call.CallID)
	}

	result, merr := e.safeCall(ctx, call.Name, m.handler, args)
	if merr != nil {
		return merr.Invocation(call.CallID)
	}

	raw, err := json.Marshal(result)
	if err != nil {
		e.log.Error("jmap: marshaling method result failed",
			"method", call.Name, "error", err)
		return NewMethodError(CodeServerFail).
			WithDescription("encoding the method result failed").
			Invocation(call.CallID)
	}
	if !isJSONObject(raw) {
		// The response arguments' type signature is "String[*]" — an object
		// (§3.2). A handler returning anything else is a bug caught here
		// rather than shipped to the client as malformed JMAP.
		e.log.Error("jmap: method result is not a JSON object", "method", call.Name)
		return NewMethodError(CodeServerFail).
			WithDescription("method produced a non-object result").
			Invocation(call.CallID)
	}
	return Invocation{Name: call.Name, Args: raw, CallID: call.CallID}
}

// safeCall invokes a handler with panic containment: a panicking handler
// yields serverFail (§3.6.2 — "An unexpected or unknown error occurred") for
// its own call while the daemon and the request's remaining calls proceed.
func (e *Engine) safeCall(ctx context.Context, name string, h Handler, args json.RawMessage) (result any, merr *MethodError) {
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("jmap: method handler panicked",
				"method", name, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			result = nil
			merr = NewMethodError(CodeServerFail).WithDescription("internal error")
		}
	}()
	return h(ctx, args)
}
