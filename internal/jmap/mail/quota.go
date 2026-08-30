package mail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/GrupoNU/moov/internal/jmap"
)

// Quota (RFC 9425) — read live from Dovecot over IMAP (GETQUOTAROOT INBOX),
// through the same per-account credential the write executor already dials
// with. IMAP rather than the Mailcow admin API, deliberately: fewer moving
// parts, no admin credential in the request path, and the value Dovecot
// enforces is the value the user sees.
//
// Conformance decisions, stated once:
//
//   - Quota/get (§4.2) and Quota/query (§4.4) are implemented.
//   - Quota/changes (§4.3) is registered but answers
//     cannotCalculateChanges: usage moves with every delivery and Dovecot
//     pushes no quota events, so this server has no per-cursor delta to
//     report. §5.2 names the refusal; the state string still changes with
//     usage, so a client knows WHEN to refetch. Never silent.
//   - Quota/queryChanges (§4.5): same refusal.
//   - An account without quota limits serves an EMPTY list — §4.1 makes
//     hardLimit required, and fabricating an infinite one would be an
//     invented number. No objects is the truthful shape of "no quota".
//   - warnLimit/softLimit are always null: Dovecot's warning thresholds
//     (quota_warning) live in Mailcow's config, not on the IMAP wire, and
//     this server does not guess configuration it cannot read.

// quotaProperties is the §4.1 property set.
var quotaProperties = map[string]bool{
	"id": true, "resourceType": true, "used": true, "hardLimit": true,
	"scope": true, "name": true, "types": true,
	"warnLimit": true, "softLimit": true, "description": true,
}

// RegisterQuotaMethods mounts the RFC 9425 methods under CapQuota.
func RegisterQuotaMethods(registry *jmap.Registry, deps *Deps) {
	if registry == nil || deps == nil {
		panic("mail: RegisterQuotaMethods requires a registry and deps")
	}
	if deps.Quota == nil {
		panic("mail: RegisterQuotaMethods requires Quota")
	}
	registry.Register("Quota/get", jmap.CapQuota, deps.handleQuotaGet)
	registry.Register("Quota/query", jmap.CapQuota, deps.handleQuotaQuery)
	registry.Register("Quota/changes", jmap.CapQuota, deps.handleQuotaChanges)
	registry.Register("Quota/queryChanges", jmap.CapQuota, deps.handleQuotaQueryChanges)
}

// quotaWireID names one Quota object: "storage" (octets) or "message"
// (count). Literal ids are valid JMAP Ids (letters only) and stable — the id
// is the resource, which cannot be renamed.
func quotaWireID(resourceType string) string {
	if resourceType == "count" {
		return "message"
	}
	return "storage"
}

// handleQuotaGet implements §4.2: "The ids argument may be null to fetch all
// quotas of the account at once."
func (d *Deps) handleQuotaGet(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	req, caller, merr := parseGet(ctx, args, d.Limits)
	if merr != nil {
		return nil, merr
	}
	if bad := unknownProperties(req.Properties, quotaProperties); len(bad) > 0 {
		sort.Strings(bad)
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("unknown Quota properties: %s", strings.Join(bad, ", "))
	}

	values, err := d.Quota.ReadQuota(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the quota from the mail server", err)
	}

	resp := newGetResponse(req.AccountID, quotaState(values))
	byID := make(map[string]QuotaValue, len(values))
	for _, v := range values {
		byID[quotaWireID(v.ResourceType)] = v
	}
	if req.IDs == nil {
		// Deterministic order: storage first.
		for _, id := range []string{"storage", "message"} {
			if v, ok := byID[id]; ok {
				resp.List = append(resp.List, quotaObject(id, v, req.Properties))
			}
		}
		return resp, nil
	}
	for _, id := range *req.IDs {
		if v, ok := byID[id]; ok {
			resp.List = append(resp.List, quotaObject(id, v, req.Properties))
			continue
		}
		resp.NotFound = append(resp.NotFound, id)
	}
	sort.Strings(resp.NotFound)
	return resp, nil
}

// quotaObject renders one §4.1 object.
func quotaObject(id string, v QuotaValue, properties *[]string) map[string]any {
	full := map[string]any{
		"id":           id,
		"resourceType": v.ResourceType,
		"used":         v.Used,
		"hardLimit":    v.HardLimit,
		// §3.1: "account" — the quota root Dovecot reports for the user's
		// own INBOX applies to this account.
		"scope": "account",
		"name":  v.Name,
		// §4.1 types: "List of all the type names ... to which this quota
		// applies". Dovecot's mail quota counts messages.
		"types":       []string{"Email"},
		"warnLimit":   nil,
		"softLimit":   nil,
		"description": nil,
	}
	if properties == nil {
		return full
	}
	out := map[string]any{"id": full["id"]}
	for _, name := range *properties {
		if v, ok := full[name]; ok {
			out[name] = v
		}
	}
	return out
}

// quotaState digests the live values: it changes exactly when a /get would
// return something different, which is the §5.1 contract for a state string.
func quotaState(values []QuotaValue) string {
	var b strings.Builder
	for _, v := range values {
		fmt.Fprintf(&b, "%s|%s|%d|%d\n", v.Name, v.ResourceType, v.Used, v.HardLimit)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}

// quotaQueryFilter is §4.4's FilterCondition.
type quotaQueryFilter struct {
	Name         *string `json:"name"`
	Scope        *string `json:"scope"`
	ResourceType *string `json:"resourceType"`
	Type         *string `json:"type"`
	Operator     *string `json:"operator"`
}

// handleQuotaQuery implements §4.4 over the in-memory pair.
func (d *Deps) handleQuotaQuery(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	caller, ok := jmap.CallerFromContext(ctx)
	if !ok {
		return nil, jmap.NewMethodError(jmap.CodeForbidden).
			WithDescription("no authenticated caller in context")
	}
	var req queryRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, jmap.NewMethodError(jmap.CodeInvalidArguments).
			WithDescription("arguments did not parse: %v", err)
	}
	if req.AccountID != caller.JMAPAccountID() {
		return nil, jmap.NewMethodError(jmap.CodeAccountNotFound).
			WithDescription("accountId %q is not an account this session can use", req.AccountID)
	}
	var filter quotaQueryFilter
	if len(req.Filter) > 0 && string(req.Filter) != "null" {
		if err := json.Unmarshal(req.Filter, &filter); err != nil {
			return nil, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
				WithDescription("the filter did not parse: %v", err)
		}
		if filter.Operator != nil {
			return nil, jmap.NewMethodError(jmap.CodeUnsupportedFilter).
				WithDescription("FilterOperator is not supported for Quota/query")
		}
	}
	for _, c := range req.Sort {
		if c.Property != "name" && c.Property != "used" {
			return nil, jmap.NewMethodError(jmap.CodeUnsupportedSort).
				WithDescription("Quota/query sorts by name or used (RFC 9425 §4.4); %q is neither", c.Property)
		}
	}

	values, err := d.Quota.ReadQuota(ctx, caller.AccountID)
	if err != nil {
		return nil, serverFail("reading the quota from the mail server", err)
	}
	var matched []QuotaValue
	for _, v := range values {
		if filter.Name != nil && !strings.Contains(v.Name, *filter.Name) {
			continue
		}
		if filter.Scope != nil && *filter.Scope != "account" {
			continue
		}
		if filter.ResourceType != nil && v.ResourceType != *filter.ResourceType {
			continue
		}
		if filter.Type != nil && *filter.Type != "Email" {
			continue
		}
		matched = append(matched, v)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		for _, c := range req.Sort {
			asc := c.ascending()
			switch c.Property {
			case "name":
				if matched[i].Name == matched[j].Name {
					continue
				}
				return (matched[i].Name < matched[j].Name) == asc
			case "used":
				if matched[i].Used == matched[j].Used {
					continue
				}
				return (matched[i].Used < matched[j].Used) == asc
			}
		}
		return quotaWireID(matched[i].ResourceType) < quotaWireID(matched[j].ResourceType)
	})

	ids := make([]string, 0, len(matched))
	for _, v := range matched {
		ids = append(ids, quotaWireID(v.ResourceType))
	}
	total := uint64(len(ids))
	resp := queryResponse{
		AccountID:           req.AccountID,
		QueryState:          quotaState(values),
		CanCalculateChanges: false,
		IDs:                 ids,
	}
	if req.CalculateTotal {
		resp.Total = &total
	}
	return resp, nil
}

// handleQuotaChanges answers the documented refusal (package comment).
func (d *Deps) handleQuotaChanges(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	if _, ok := jmap.CallerFromContext(ctx); !ok {
		return nil, jmap.NewMethodError(jmap.CodeForbidden).
			WithDescription("no authenticated caller in context")
	}
	return nil, jmap.NewMethodError(jmap.CodeCannotCalculateChanges).
		WithDescription("quota usage has no changelog; refetch with Quota/get")
}

func (d *Deps) handleQuotaQueryChanges(ctx context.Context, args json.RawMessage) (any, *jmap.MethodError) {
	if _, ok := jmap.CallerFromContext(ctx); !ok {
		return nil, jmap.NewMethodError(jmap.CodeForbidden).
			WithDescription("no authenticated caller in context")
	}
	return nil, jmap.NewMethodError(jmap.CodeCannotCalculateChanges).
		WithDescription("quota usage has no changelog; re-run Quota/query")
}
