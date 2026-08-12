package jmap

import (
	"context"
	"encoding/json"
)

// RegisterCore registers the methods of the core capability. Phase 1 that is
// exactly one: Core/echo.
func RegisterCore(r *Registry) {
	r.Register("Core/echo", CapCore, echoHandler)
}

// echoHandler implements Core/echo (RFC 8620 §4): "This method returns
// exactly the same arguments as it is given."
//
// The arguments come back as the raw bytes received (json.RawMessage
// marshals verbatim), so the echo is byte-identical — no decode/re-encode
// that could reorder keys or reformat numbers. The one deviation from raw
// input is deliberate and RFC-required: back-references are resolved BEFORE
// any handler runs (§3.7 — "the result reference should be resolved and the
// value used as the 'real' argument. The method is then processed as
// normal"), so an echo of a "#"-referenced argument echoes the resolved
// value.
func echoHandler(_ context.Context, args json.RawMessage) (any, *MethodError) {
	return args, nil
}
