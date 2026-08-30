package sieve

import (
	"strconv"
	"strings"
)

// Capabilities is the server's advertised capability set (RFC 5804 §1.7),
// read at connect and re-read after the STARTTLS upgrade (§2.2 requires the
// server to re-issue it).
//
// Extensions is the load-bearing field: it is the SIEVE capability's
// extension list, and it governs what the script generator may emit — a
// generated script whose require line names an extension absent from this
// list is refused before it is ever pushed (generate.go, RequiredExtensions).
type Capabilities struct {
	// Implementation is the server's self-description (e.g. "Dovecot
	// Pigeonhole"). Diagnostic only.
	Implementation string

	// Version is the ManageSieve protocol version ("1.0"). §1.7: a server
	// advertising 1.0 supports RENAMESCRIPT, CHECKSCRIPT and NOOP.
	Version string

	// Extensions is the SIEVE extension list, verbatim and order-preserving.
	// Sieve capability strings are case-sensitive (RFC 9661 §1.2.1 carries
	// them into JMAP under exactly that rule), and Dovecot advertises them
	// lowercase; no normalization is applied here.
	Extensions []string

	// SASL is the advertised mechanism list. Before STARTTLS our Dovecot
	// advertises it EMPTY (verified on the wire), which is why the TLS
	// upgrade is not optional.
	SASL []string

	// StartTLS reports the STARTTLS capability. After the upgrade the
	// re-issued list must not include it (§2.2), so on a connected Client
	// this is normally false.
	StartTLS bool

	// MaxRedirects is the server's redirect cap, with HasMaxRedirects saying
	// whether it was advertised at all — RFC 9661 §1.2.1 wants null when the
	// server declares no limit, and 0 is not a spelling of "unknown".
	MaxRedirects    int
	HasMaxRedirects bool

	// Notify is the enotify method list (URI schemes).
	Notify []string

	// Owner is the authorization identity, only sent post-authentication and
	// only by servers that choose to.
	Owner string
}

// HasExtension reports whether the SIEVE list contains name, compared
// exactly (case-sensitive, per the RFC 9661 §1.2.1 reading of the strings).
func (c Capabilities) HasExtension(name string) bool {
	for _, e := range c.Extensions {
		if e == name {
			return true
		}
	}
	return false
}

// applyCapabilityLine folds one capability response line into c. Each line is
// a name string plus an optional value string (RFC 5804 §1.7); names are
// matched case-insensitively as the grammar's examples do.
func (c *Capabilities) applyCapabilityLine(name, value string) {
	switch strings.ToUpper(name) {
	case "IMPLEMENTATION":
		c.Implementation = value
	case "VERSION":
		c.Version = value
	case "SIEVE":
		c.Extensions = splitList(value)
	case "SASL":
		c.SASL = splitList(value)
	case "STARTTLS":
		c.StartTLS = true
	case "MAXREDIRECTS":
		if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && n >= 0 {
			c.MaxRedirects, c.HasMaxRedirects = n, true
		}
	case "NOTIFY":
		c.Notify = splitList(value)
	case "OWNER":
		c.Owner = value
	default:
		// Unknown capabilities are ignored, as §1.7 requires of clients.
	}
}

// splitList splits a space-separated capability value, dropping empties (an
// empty SASL value is a legitimate empty list).
func splitList(v string) []string {
	fields := strings.Fields(v)
	if len(fields) == 0 {
		return nil
	}
	return fields
}
