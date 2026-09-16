package accounts

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// The field rules of contract §2.5, in one place, so the create path and the
// PATCH path cannot drift apart: both call these.

// Address limits and the defaults of §2.5.
const (
	// MaxAddressLen is the contract's maximum (§2.5).
	MaxAddressLen = 254

	// MaxNameRunes bounds the display name. Runes, not bytes: "Expo Diseño"
	// must not cost more of the budget for being spelled correctly.
	MaxNameRunes = 128

	// MinQuotaMB and DefaultMaxQuotaMB bracket quotaMB; the upper bound is
	// overridable per installation (MOOV_ACCOUNTS_MAX_QUOTA_MB).
	MinQuotaMB        = 64
	DefaultMaxQuotaMB = 10240

	// DefaultQuotaMB is what a create without quotaMB gets.
	DefaultQuotaMB = 2048

	// The limit defaults and their bounds (§2.5 and the OpenAPI schema).
	DefaultSendPerDay           = 300
	MinSendPerDay               = 1
	MaxSendPerDay               = 10000
	DefaultRecipientsPerMessage = 50
	MinRecipientsPerMessage     = 1
	MaxRecipientsPerMessage     = 500
	DefaultAttachmentMB         = 25
	MinAttachmentMB             = 1
	MaxAttachmentMB             = 100

	// MaxReasonLen bounds the audit reason a caller may attach.
	MaxReasonLen = 256
)

// addressReason is the exact prose the contract's 400 example carries, so a
// consumer built against the published example sees the published text.
const addressReason = `local part must match ^[a-z0-9]([a-z0-9._-]{0,62}[a-z0-9])?$ with no consecutive dots`

// NormalizeAddress lower-cases and validates a mailbox address under §2.5,
// returning the normalized form.
//
// It validates the SHAPE only. Whether the domain is the caller's is a
// separate question with a different answer on the wire (a 404, not a 400),
// and it is asked in Service.resolve — never here, so that no validation path
// can accidentally tell a caller which domains exist.
func NormalizeAddress(field, raw string) (string, *FieldError) {
	addr := strings.ToLower(strings.TrimSpace(raw))
	if addr == "" {
		return "", fieldErr(field, "is required")
	}
	if len(addr) > MaxAddressLen {
		return "", fieldErr(field, "is longer than %d characters", MaxAddressLen)
	}
	at := strings.LastIndex(addr, "@")
	if at <= 0 || at == len(addr)-1 {
		return "", fieldErr(field, "must be local@domain")
	}
	if !validLocalPart(addr[:at]) {
		return "", fieldErr(field, "%s", addressReason)
	}
	if !validDomain(addr[at+1:]) {
		return "", fieldErr(field, "is not a valid domain name")
	}
	return addr, nil
}

// validLocalPart implements ^[a-z0-9]([a-z0-9._-]{0,62}[a-z0-9])?$ with the
// additional "no consecutive dots" rule the pattern cannot express.
func validLocalPart(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	if !isLowerAlnum(s[0]) || !isLowerAlnum(s[len(s)-1]) {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !isLowerAlnum(c) && c != '.' && c != '_' && c != '-' {
			return false
		}
		if c == '.' && i > 0 && s[i-1] == '.' {
			return false
		}
	}
	return true
}

// validDomain checks the label shape of the contract's Address pattern: at
// least two labels, each starting and ending alphanumeric, hyphens inside.
func validDomain(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	labels := strings.Split(s, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return false
		}
		if !isLowerAlnum(l[0]) || !isLowerAlnum(l[len(l)-1]) {
			return false
		}
		for i := 0; i < len(l); i++ {
			if !isLowerAlnum(l[i]) && l[i] != '-' {
				return false
			}
		}
	}
	return true
}

func isLowerAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

// DomainOf returns the domain part of an address already normalized.
func DomainOf(address string) string {
	if at := strings.LastIndex(address, "@"); at >= 0 {
		return address[at+1:]
	}
	return ""
}

// ValidateName checks the display name of §2.5: 1–128 characters, no control
// characters.
func ValidateName(field, raw string) (string, *FieldError) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fieldErr(field, "must be 1–%d characters without control characters", MaxNameRunes)
	}
	if utf8.RuneCountInString(name) > MaxNameRunes || hasControl(name) {
		return "", fieldErr(field, "must be 1–%d characters without control characters", MaxNameRunes)
	}
	return name, nil
}

// ValidateReason checks the optional audit reason.
func ValidateReason(raw string) (string, *FieldError) {
	reason := strings.TrimSpace(raw)
	if utf8.RuneCountInString(reason) > MaxReasonLen {
		return "", fieldErr("reason", "is longer than %d characters", MaxReasonLen)
	}
	if hasControl(reason) {
		return "", fieldErr("reason", "must not contain control characters")
	}
	return reason, nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if r != '\t' && unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// Limits are the per-mailbox limits of §2.5. A nil field means "unchanged" on
// a PATCH and "the default" on a create.
type Limits struct {
	SendPerDay           *int
	RecipientsPerMessage *int
	AttachmentMB         *int
}

// Validate checks each present limit against its bounds, reporting the FIRST
// offender with its dotted JSON name.
func (l Limits) Validate() *FieldError {
	for _, c := range []struct {
		name     string
		v        *int
		min, max int
	}{
		{"limits.sendPerDay", l.SendPerDay, MinSendPerDay, MaxSendPerDay},
		{"limits.recipientsPerMessage", l.RecipientsPerMessage, MinRecipientsPerMessage, MaxRecipientsPerMessage},
		{"limits.attachmentMB", l.AttachmentMB, MinAttachmentMB, MaxAttachmentMB},
	} {
		if c.v == nil {
			continue
		}
		if *c.v < c.min || *c.v > c.max {
			return fieldErr(c.name, "must be between %d and %d", c.min, c.max)
		}
	}
	return nil
}

// ValidateQuota checks quotaMB against the installation's maximum.
func ValidateQuota(field string, mb, maxMB int) *FieldError {
	if maxMB <= 0 {
		maxMB = DefaultMaxQuotaMB
	}
	if mb < MinQuotaMB || mb > maxMB {
		return fieldErr(field, "must be between %d and %d", MinQuotaMB, maxMB)
	}
	return nil
}
