package jmap

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

// Creation-id references (§3.3/§5.3) and the multi-response dispatch (§7.5's
// implicit calls), at the engine level.

func TestCreationIDsResolveAndSnapshot(t *testing.T) {
	c := NewCreationIDs(map[string]string{"seed": "m1"})

	// A plain id passes through; a seeded reference resolves; an unknown one
	// reports failure rather than inventing an id.
	if got, ok := c.Resolve("e42"); !ok || got != "e42" {
		t.Errorf("plain id = (%q, %v)", got, ok)
	}
	if got, ok := c.Resolve("#seed"); !ok || got != "m1" {
		t.Errorf("seeded ref = (%q, %v)", got, ok)
	}
	if _, ok := c.Resolve("#ghost"); ok {
		t.Error("an unknown creation id resolved")
	}
	if _, ok := c.Resolve("#"); ok {
		t.Error("the empty creation id resolved")
	}

	c.Record("fresh", "e7")
	if got, ok := c.Resolve("#fresh"); !ok || got != "e7" {
		t.Errorf("recorded ref = (%q, %v)", got, ok)
	}
	snap := c.Snapshot()
	if snap["seed"] != "m1" || snap["fresh"] != "e7" {
		t.Errorf("snapshot = %v; §3.4 requires the original entries plus the new ones", snap)
	}

	// The context plumbing: absent means an empty, non-nil map (handlers
	// outside an engine resolve plain ids and refuse refs).
	fallback := CreationIDsFromContext(context.Background())
	if fallback == nil {
		t.Fatal("CreationIDsFromContext returned nil")
	}
	if _, ok := fallback.Resolve("#anything"); ok {
		t.Error("the fallback map resolved a reference")
	}
}

func engineFor(t *testing.T, reg *Registry) *Engine {
	t.Helper()
	return NewEngine(reg, DefaultLimits(), []string{CapCore, CapMail}, slog.New(slog.DiscardHandler))
}

func TestProcessThreadsCreationIDsAcrossCalls(t *testing.T) {
	reg := NewRegistry()
	RegisterCore(reg)

	// A /set-shaped method that creates one record and records its id, and a
	// consumer that resolves a reference — the exact shape the §7.5 flow uses.
	reg.Register("Thing/set", CapMail, func(ctx context.Context, _ json.RawMessage) (any, *MethodError) {
		CreationIDsFromContext(ctx).Record("t1", "e99")
		return map[string]any{"created": map[string]any{"t1": map[string]any{"id": "e99"}}}, nil
	})
	var resolved string
	var resolvedOK bool
	reg.Register("Thing/consume", CapMail, func(ctx context.Context, args json.RawMessage) (any, *MethodError) {
		var req struct {
			Ref string `json:"ref"`
		}
		_ = json.Unmarshal(args, &req)
		resolved, resolvedOK = CreationIDsFromContext(ctx).Resolve(req.Ref)
		return map[string]any{}, nil
	})

	body := []byte(`{
		"using": ["urn:ietf:params:jmap:core", "urn:ietf:params:jmap:mail"],
		"methodCalls": [
			["Thing/set", {}, "0"],
			["Thing/consume", {"ref": "#t1"}, "1"]
		],
		"createdIds": {"prior": "e1"}
	}`)
	resp, rerr := engineFor(t, reg).Process(context.Background(), body, "s")
	if rerr != nil {
		t.Fatalf("Process: %v", rerr)
	}
	if !resolvedOK || resolved != "e99" {
		t.Errorf("cross-call resolution = (%q, %v), want e99", resolved, resolvedOK)
	}
	// §3.4: createdIds comes back BECAUSE the request carried it, updated
	// with the new creation.
	if resp.CreatedIDs == nil || resp.CreatedIDs["prior"] != "e1" || resp.CreatedIDs["t1"] != "e99" {
		t.Errorf("response createdIds = %v", resp.CreatedIDs)
	}

	// Without the request property, none comes back (§3.4 "only returned if
	// given").
	body2 := []byte(`{"using": ["urn:ietf:params:jmap:core", "urn:ietf:params:jmap:mail"],
		"methodCalls": [["Thing/set", {}, "0"]]}`)
	resp, rerr = engineFor(t, reg).Process(context.Background(), body2, "s")
	if rerr != nil {
		t.Fatal(rerr)
	}
	if resp.CreatedIDs != nil {
		t.Errorf("createdIds returned without being given: %v", resp.CreatedIDs)
	}
}

func TestRegisterMultiEmitsOrderedResponsesUnderOneCallID(t *testing.T) {
	reg := NewRegistry()
	reg.RegisterMulti("Multi/set", CapMail, func(context.Context, json.RawMessage) ([]NamedResult, *MethodError) {
		return []NamedResult{
			{Name: "Multi/set", Result: map[string]any{"n": 1}},
			{Name: "Email/set", Result: map[string]any{"n": 2}},
		}, nil
	})

	body := []byte(`{"using": ["urn:ietf:params:jmap:mail"], "methodCalls": [["Multi/set", {}, "c7"]]}`)
	resp, rerr := engineFor(t, reg).Process(context.Background(), body, "s")
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(resp.MethodResponses) != 2 {
		t.Fatalf("responses = %d, want 2", len(resp.MethodResponses))
	}
	// §3.2: every response initiated by the call carries ITS id; §7.5: the
	// implicit response follows the method's own and keeps the implicit
	// method's NAME.
	if resp.MethodResponses[0].Name != "Multi/set" || resp.MethodResponses[0].CallID != "c7" {
		t.Errorf("first response = %+v", resp.MethodResponses[0])
	}
	if resp.MethodResponses[1].Name != "Email/set" || resp.MethodResponses[1].CallID != "c7" {
		t.Errorf("second response = %+v", resp.MethodResponses[1])
	}
}

func TestRegisterMultiErrorAndPanicContainment(t *testing.T) {
	reg := NewRegistry()
	reg.RegisterMulti("Multi/err", CapMail, func(context.Context, json.RawMessage) ([]NamedResult, *MethodError) {
		return nil, NewMethodError(CodeForbidden)
	})
	reg.RegisterMulti("Multi/panic", CapMail, func(context.Context, json.RawMessage) ([]NamedResult, *MethodError) {
		panic("boom")
	})
	reg.RegisterMulti("Multi/empty", CapMail, func(context.Context, json.RawMessage) ([]NamedResult, *MethodError) {
		return nil, nil
	})

	body := []byte(`{"using": ["urn:ietf:params:jmap:mail"], "methodCalls": [
		["Multi/err", {}, "0"], ["Multi/panic", {}, "1"], ["Multi/empty", {}, "2"]]}`)
	resp, rerr := engineFor(t, reg).Process(context.Background(), body, "s")
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(resp.MethodResponses) != 3 {
		t.Fatalf("responses = %d, want 3 (§3.6.2: processing continues)", len(resp.MethodResponses))
	}
	for i, wantType := range []string{"forbidden", "serverFail", "serverFail"} {
		inv := resp.MethodResponses[i]
		if inv.Name != "error" {
			t.Errorf("response %d = %q, want error", i, inv.Name)
			continue
		}
		var args map[string]any
		_ = json.Unmarshal(inv.Args, &args)
		if args["type"] != wantType {
			t.Errorf("response %d error type = %v, want %s", i, args["type"], wantType)
		}
	}
}
