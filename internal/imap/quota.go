package imap

import (
	"context"
	"fmt"

	goimap "github.com/emersion/go-imap/v2"
)

// IMAP QUOTA (RFC 9208 / RFC 2087) — the transport behind JMAP's Quota/get
// (L3 epic E6). Read-only: Moov never sets quotas; Mailcow administers them.

// QuotaResource is one resource of the account's quota root, in this
// package's own vocabulary (no go-imap type escapes, per doc.go).
type QuotaResource struct {
	// Root is the quota root name as the server reports it (Dovecot:
	// "User quota").
	Root string

	// Resource is the RFC 9208 resource name, uppercased: "STORAGE" (in
	// KiB on the wire — converted to BYTES here, so no caller ever guesses
	// the unit) or "MESSAGE" (a count).
	Resource string

	// Usage and Limit are current use and hard limit, in bytes for STORAGE
	// and in messages for MESSAGE.
	Usage int64
	Limit int64
}

// GetQuota reads the quota roots that govern INBOX (GETQUOTAROOT, RFC 9208
// §6.2.2 — INBOX because every account has one and Dovecot attaches the user
// quota to it).
//
// A server without the QUOTA capability, or an account without any quota
// root, returns an EMPTY slice and no error: "no quota" is a normal state
// for an unlimited mailbox, not a failure. The JMAP layer serves it as an
// empty Quota list — no fabricated limits.
func (cl *client) GetQuota(ctx context.Context) ([]QuotaResource, error) {
	gc, err := cl.conn()
	if err != nil {
		return nil, err
	}
	if !cl.Capabilities().Has("quota") {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	data, err := gc.GetQuotaRoot("INBOX").Wait()
	if err != nil {
		return nil, fmt.Errorf("imap: GETQUOTAROOT INBOX: %w", err)
	}

	var out []QuotaResource
	for _, q := range data {
		for typ, res := range q.Resources {
			r := QuotaResource{Root: q.Root, Resource: string(typ), Usage: res.Usage, Limit: res.Limit}
			if typ == goimap.QuotaResourceStorage {
				// RFC 9208 §5: STORAGE is "in units of 1024 octets". Bytes
				// here, so the one conversion lives next to the one citation.
				r.Usage *= 1024
				r.Limit *= 1024
			}
			out = append(out, r)
		}
	}
	// Deterministic order for callers and tests: by root, then resource.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && quotaLess(out[j], out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

func quotaLess(a, b QuotaResource) bool {
	if a.Root != b.Root {
		return a.Root < b.Root
	}
	return a.Resource < b.Resource
}
