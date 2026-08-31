package sieve

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// The managed-script model (GC-4, L3 epic E6): ONE Moov-managed script whose
// authoritative state is a machine-readable JSON header inside the script
// itself. Dovecot stores the script; Dovecot is therefore the source of truth
// for the rules, and Moov's database holds nothing about them — a store
// rebuild loses no filters.
//
// Origin partitioning (the Bulwark design docs/research/05 §1.3 tells us to
// steal, adapted): content is either `moov` (the generated sections, editable
// through the rule model) or foreign. Foreign content is preserved VERBATIM
// in the external section — never parsed into editables, never rewritten,
// byte-identical through any number of edit cycles (pinned by test). Its
// leading `require` statements are the one exception: Sieve demands requires
// before any other command, so they are lifted out at import time and merged
// into the generated require line, recorded in the metadata so regeneration
// keeps honoring them.

// Rule types. The type tag is what lets the UI render "Bloqueados" and
// "Reenvío" as their own settings sections while the storage is one script.
const (
	// RuleFilter is a user filter: GC-4's label algebra.
	RuleFilter = "filter"

	// RuleBlocked is a blocked sender (Gmail: "go to Spam"). Uses only
	// Criteria.From; compiled as an exact address match filed to Junk.
	RuleBlocked = "blocked"

	// RuleNeverSpam is Gmail's "never send to spam", compiled as the
	// empirically validated Mailcow pipeline escape: the Junk filing lives in
	// global_sieve_after, which only runs when the user script yields a keep
	// result, so an explicit `fileinto "INBOX"; stop;` on spam-tagged mail
	// ends the sequence before the filing happens (E6 recon, sieve-test
	// against the real Dovecot image). What it CANNOT do — documented, not
	// hidden — is resurrect mail Rspamd rejected at SMTP time, which never
	// reached Dovecot; the same limit Gmail's own filters have.
	RuleNeverSpam = "neverSpam"
)

// Script is the complete managed-script model: everything the metadata
// header carries plus the verbatim external body.
type Script struct {
	// Version is the metadata schema version. Generate writes
	// MetadataVersion; Parse accepts only versions it knows.
	Version int `json:"version"`

	// Rules in evaluation order. Blocked rules are emitted first regardless
	// of position (a blocked sender must not receive a vacation reply), then
	// forward-all, then vacation, then the filter/neverSpam rules in their
	// stored order.
	Rules []Rule `json:"rules,omitempty"`

	// Vacation is the RFC 8621 §8 singleton, materialized as a script
	// section. nil means never configured.
	Vacation *Vacation `json:"vacation,omitempty"`

	// ForwardAll is the settings-level forwarding recipe (canon §2.11:
	// verified address, spam excluded, two dispositions).
	ForwardAll *ForwardAll `json:"forwardAll,omitempty"`

	// ExternalRequires are the require names lifted from imported foreign
	// content, merged into the generated require line.
	ExternalRequires []string `json:"externalRequires,omitempty"`

	// ExternalSource records where the external body came from (the imported
	// script's name), informational only.
	ExternalSource string `json:"externalSource,omitempty"`

	// ExternalBody is the foreign Sieve preserved verbatim. It lives in the
	// script's external section, NOT in the metadata JSON — one copy, and the
	// copy that survives is the one Dovecot executes.
	ExternalBody string `json:"-"`
}

// MetadataVersion is the current metadata schema version.
const MetadataVersion = 1

// Rule is one entry of the rule model.
type Rule struct {
	// ID is a stable opaque identifier assigned when the rule is created and
	// preserved across edits — the JMAP object id of the vendor surface.
	ID string `json:"id"`

	// Name is the user's label for the rule, optional.
	Name string `json:"name,omitempty"`

	// Type is one of RuleFilter, RuleBlocked, RuleNeverSpam.
	Type string `json:"type"`

	// Enabled rules are compiled into Sieve; disabled ones ride only in the
	// metadata (Gmail has no disabled state for filters — this is the cheap
	// superset that makes "turn it off without losing it" possible).
	Enabled bool `json:"enabled"`

	Criteria Criteria `json:"criteria"`
	Actions  Actions  `json:"actions"`
}

// Criteria is GC-4's closed criterion set: from, to (incl. cc), subject,
// size, hasAttachment. No date criterion, no free-text query — Gmail's own
// restriction and Sieve's, honestly aligned (canon §4.1 item 5).
type Criteria struct {
	// From matches the From header, substring, case-insensitive (Gmail's
	// from: matches display names and address fragments alike). For
	// RuleBlocked the match is instead an exact address match.
	From []string `json:"from,omitempty"`

	// To matches the To and Cc headers AND the envelope recipient — the
	// envelope test is what catches mail delivered via Bcc, which has no
	// visible header to match.
	To []string `json:"to,omitempty"`

	// Subject matches the Subject header, substring.
	Subject []string `json:"subject,omitempty"`

	// SizeOver / SizeUnder are message-size bounds in bytes. Zero means
	// unset.
	SizeOver  int64 `json:"sizeOver,omitempty"`
	SizeUnder int64 `json:"sizeUnder,omitempty"`

	// HasAttachment tests for a MIME part carrying a filename, in both
	// Content-Disposition and Content-Type (older senders only set the
	// latter). nil means unset.
	HasAttachment *bool `json:"hasAttachment,omitempty"`
}

// empty reports a criteria set with nothing in it.
func (c Criteria) empty() bool {
	return len(c.From) == 0 && len(c.To) == 0 && len(c.Subject) == 0 &&
		c.SizeOver == 0 && c.SizeUnder == 0 && c.HasAttachment == nil
}

// Actions is GC-4's closed action set. Notably absent, on purpose: discard.
// A filter that silently destroys mail is refused by the model itself —
// Delete files into Trash, where 30-day retention applies (E2), and there is
// deliberately no way to express "lose it without a trace".
type Actions struct {
	// MoveTo files the message into a folder (IMAP name, "/" hierarchy).
	MoveTo string `json:"moveTo,omitempty"`

	// Labels adds keywords using E8's $label:<name> convention.
	Labels []string `json:"labels,omitempty"`

	// MarkRead adds \Seen; Star adds \Flagged.
	MarkRead bool `json:"markRead,omitempty"`
	Star     bool `json:"star,omitempty"`

	// Forward redirects a copy (redirect :copy — local delivery continues
	// unless MoveTo/Delete also apply). The address MUST be verified: the
	// generator refuses anything not in GenerateEnv.VerifiedForward, and the
	// refusal is pinned by test (GC-4's security design).
	Forward string `json:"forward,omitempty"`

	// Delete files into Trash. Mutually exclusive with MoveTo.
	Delete bool `json:"delete,omitempty"`

	// Stop ends the moov script for this message after this rule (later moov
	// rules and the external section are skipped; Dovecot's global after-
	// scripts still apply their own semantics).
	Stop bool `json:"stop,omitempty"`
}

// empty reports an action set with nothing in it.
func (a Actions) empty() bool {
	return a.MoveTo == "" && len(a.Labels) == 0 && !a.MarkRead && !a.Star &&
		a.Forward == "" && !a.Delete && !a.Stop
}

// Vacation is the RFC 8621 §8 VacationResponse, stored in the metadata and
// compiled to RFC 5230 `vacation` with Gmail's exact anti-annoyance spec
// (canon §2.8): re-send only after 4 days or when edited, never to spam,
// never to lists, date-window guarded.
type Vacation struct {
	Enabled bool `json:"enabled"`

	// FromDate / ToDate are the RFC 8621 UTCDate bounds, or zero for open.
	// They are honored to the second in UTC by the generated guard. The
	// server keeps no per-account timezone: Gmail's "starts 12:00 AM, ends
	// 11:59 PM" day-boundary semantics are produced by the CLIENT sending
	// day-aligned instants in the user's zone, which the RFC 8621 wire shape
	// (a UTCDate) carries losslessly. Documented decision, not an accident.
	FromDate time.Time `json:"fromDate,omitempty"`
	ToDate   time.Time `json:"toDate,omitempty"`

	// Subject of the auto-reply; empty means the server default (Dovecot
	// prepends "Auto: " to the original subject).
	Subject string `json:"subject,omitempty"`

	// TextBody and HTMLBody per RFC 8621 §8. HTMLBody is stored ALREADY
	// SANITIZED — the JMAP layer runs it through the identity-signature
	// sanitizer before it reaches this model.
	TextBody string `json:"textBody,omitempty"`
	HTMLBody string `json:"htmlBody,omitempty"`
}

// VacationDays is Gmail's exact re-send throttle: "will be sent again only
// after 4 days" (canon §2.8), mapped 1:1 to RFC 5230 `:days 4`.
const VacationDays = 4

// ForwardAll is the settings-level "forward all mail" recipe.
type ForwardAll struct {
	Enabled bool `json:"enabled"`

	// Address receives the forwarded copy. Must be verified, same rule as
	// Actions.Forward.
	Address string `json:"address,omitempty"`

	// Disposition is what happens to Moov's copy: "keep" leaves it in the
	// inbox, "archive" files it into the Archive folder — the two Gmail
	// dispositions the canon retains.
	Disposition string `json:"disposition,omitempty"`
}

// The ForwardAll dispositions.
const (
	ForwardKeep    = "keep"
	ForwardArchive = "archive"
)

// GenerateEnv is the environment the generator resolves against: the
// deployment's folder names, the spam verdict header, and the verified
// forwarding addresses. The JMAP adapter builds it per account (folder roles
// from the store, addresses from the forwarding table).
type GenerateEnv struct {
	// JunkFolder, TrashFolder, ArchiveFolder are the account's role folders'
	// IMAP names. Empty falls back to the Mailcow defaults.
	JunkFolder    string
	TrashFolder   string
	ArchiveFolder string

	// SpamHeader and SpamValue identify Rspamd's verdict on tagged-but-
	// accepted spam. The Mailcow default is X-Spam-Flag: YES — the exact
	// header its own global_sieve_after files into Junk on (read from the
	// live deployment, E6 recon).
	SpamHeader string
	SpamValue  string

	// VerifiedForward is the set of addresses redirect may target, keyed by
	// lowercased address. Anything else is refused at validate AND generate
	// time.
	VerifiedForward map[string]bool
}

// DefaultSpamHeader / DefaultSpamValue name Rspamd's verdict on tagged-but-
// accepted spam as Mailcow stamps it: `X-Spam-Flag: YES`, the exact header
// Mailcow's own global_sieve_after files into Junk on (read from the live
// deployment, E6 recon).
//
// Exported because TWO layers must agree on what "the scanner said spam"
// means, and a private default in each would drift: the Sieve generator's
// guards (never-spam, vacation, forward-all all key on it) and the JMAP
// layer's suspicious-mail surfacing (E10, canon §4.1.15 — a spam-verdict
// message that was NOT filed to Junk still gets the warning treatment).
const (
	DefaultSpamHeader = "X-Spam-Flag"
	DefaultSpamValue  = "YES"
)

// withDefaults fills the Mailcow defaults.
func (e GenerateEnv) withDefaults() GenerateEnv {
	if e.JunkFolder == "" {
		e.JunkFolder = "Junk"
	}
	if e.TrashFolder == "" {
		e.TrashFolder = "Trash"
	}
	if e.ArchiveFolder == "" {
		e.ArchiveFolder = "Archive"
	}
	if e.SpamHeader == "" {
		e.SpamHeader = DefaultSpamHeader
	}
	if e.SpamValue == "" {
		e.SpamValue = DefaultSpamValue
	}
	return e
}

// ValidationError carries every problem found, not just the first — the same
// report-everything contract the JMAP /set validators keep.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	return "sieve: invalid script model: " + strings.Join(e.Problems, "; ")
}

// ErrUnsupportedExtension is wrapped into validation problems when a rule
// needs a Sieve extension the server does not advertise.
var ErrUnsupportedExtension = errors.New("sieve: required extension not advertised by the server")

// Validate checks the model against the environment and, when caps is
// non-nil, against the server's advertised SIEVE extension list — a rule
// needing an unadvertised extension is refused here with a clear error, never
// pushed to fail on the server (the C0 contract).
func (s *Script) Validate(env GenerateEnv, caps *Capabilities) error {
	env = env.withDefaults()
	var problems []string
	addf := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	seen := map[string]bool{}
	for i, r := range s.Rules {
		where := fmt.Sprintf("rule %d (%s)", i, ruleLabel(r))
		if r.ID == "" {
			addf("%s: missing id", where)
		} else if seen[r.ID] {
			addf("%s: duplicate id %q", where, r.ID)
		}
		seen[r.ID] = true

		switch r.Type {
		case RuleFilter, RuleNeverSpam:
			if r.Criteria.empty() {
				addf("%s: a rule needs at least one criterion", where)
			}
			if r.Type == RuleFilter && r.Actions.empty() {
				addf("%s: a filter needs at least one action", where)
			}
		case RuleBlocked:
			if len(r.Criteria.From) == 0 {
				addf("%s: a blocked-sender rule needs the sender address", where)
			}
			for _, a := range r.Criteria.From {
				if !looksLikeAddress(a) {
					addf("%s: %q is not an email address", where, a)
				}
			}
		default:
			addf("%s: unknown rule type %q", where, r.Type)
		}

		if r.Actions.MoveTo != "" && r.Actions.Delete {
			addf("%s: moveTo and delete are mutually exclusive", where)
		}
		if r.Actions.Forward != "" {
			if !looksLikeAddress(r.Actions.Forward) {
				addf("%s: forward target %q is not an email address", where, r.Actions.Forward)
			} else if !env.VerifiedForward[strings.ToLower(r.Actions.Forward)] {
				addf("%s: forward target %q is not a verified forwarding address", where, r.Actions.Forward)
			}
		}
		for _, v := range criteriaStrings(r.Criteria) {
			if !safeScriptString(v) {
				addf("%s: criterion %q contains control characters", where, v)
			}
		}
		if !safeScriptString(r.Actions.MoveTo) {
			addf("%s: folder name contains control characters", where)
		}
		for _, l := range r.Actions.Labels {
			if l == "" || !safeScriptString(l) {
				addf("%s: label %q is not usable", where, l)
			}
		}
		if r.Criteria.SizeOver < 0 || r.Criteria.SizeUnder < 0 {
			addf("%s: negative size bound", where)
		}
	}

	if v := s.Vacation; v != nil && v.Enabled {
		if strings.TrimSpace(v.Subject) == "" && strings.TrimSpace(v.TextBody) == "" && strings.TrimSpace(v.HTMLBody) == "" {
			// The canon's API contract: subject-or-body required.
			addf("vacation: a subject or a body is required")
		}
		if !safeScriptString(v.Subject) {
			addf("vacation: the subject contains control characters")
		}
		if !v.FromDate.IsZero() && !v.ToDate.IsZero() && v.ToDate.Before(v.FromDate) {
			addf("vacation: toDate is before fromDate")
		}
	}

	if f := s.ForwardAll; f != nil && f.Enabled {
		switch {
		case !looksLikeAddress(f.Address):
			addf("forwarding: %q is not an email address", f.Address)
		case !env.VerifiedForward[strings.ToLower(f.Address)]:
			addf("forwarding: %q is not a verified forwarding address", f.Address)
		}
		if f.Disposition != "" && f.Disposition != ForwardKeep && f.Disposition != ForwardArchive {
			addf("forwarding: disposition must be %q or %q", ForwardKeep, ForwardArchive)
		}
	}

	if caps != nil {
		for _, ext := range s.RequiredExtensions() {
			if !caps.HasExtension(ext) {
				problems = append(problems, fmt.Sprintf(
					"the server does not advertise the %q Sieve extension (%v)", ext, ErrUnsupportedExtension))
			}
		}
	}

	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

// RequiredExtensions computes the require set of the generated script:
// exactly the extensions the emitted code uses, plus the imported external
// requires, sorted and deduped. This is the list Validate checks against the
// server's SIEVE capability.
func (s *Script) RequiredExtensions() []string {
	set := map[string]bool{}
	for _, r := range s.Rules {
		if !r.Enabled {
			continue
		}
		switch r.Type {
		case RuleBlocked, RuleNeverSpam:
			set["fileinto"] = true
		}
		if r.Actions.MoveTo != "" || r.Actions.Delete {
			set["fileinto"] = true
		}
		if len(r.Actions.Labels) > 0 || r.Actions.MarkRead || r.Actions.Star {
			set["imap4flags"] = true
		}
		if r.Actions.Forward != "" {
			set["copy"] = true
		}
		if len(r.Criteria.To) > 0 {
			set["envelope"] = true
		}
		if r.Criteria.HasAttachment != nil {
			set["mime"] = true
		}
	}
	if s.Vacation != nil && s.Vacation.Enabled {
		set["vacation"] = true
		if !s.Vacation.FromDate.IsZero() || !s.Vacation.ToDate.IsZero() {
			set["date"] = true
			set["relational"] = true
		}
	}
	if s.ForwardAll != nil && s.ForwardAll.Enabled {
		set["copy"] = true
		if s.ForwardAll.Disposition == ForwardArchive {
			set["fileinto"] = true
		}
	}
	for _, ext := range s.ExternalRequires {
		set[ext] = true
	}
	return sortedSet(set)
}

// ruleLabel names a rule for an error message.
func ruleLabel(r Rule) string {
	if r.Name != "" {
		return r.Name
	}
	if r.ID != "" {
		return r.ID
	}
	return "unnamed"
}

// criteriaStrings flattens every string criterion for validation.
func criteriaStrings(c Criteria) []string {
	out := make([]string, 0, len(c.From)+len(c.To)+len(c.Subject))
	out = append(out, c.From...)
	out = append(out, c.To...)
	out = append(out, c.Subject...)
	return out
}

// safeScriptString refuses the octets a Sieve quoted string cannot carry and
// the controls that would let a value break out of the generated structure.
func safeScriptString(v string) bool {
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// looksLikeAddress is the shallow shape check the model applies to addresses:
// one @, something on both sides, no spaces or controls. Deliverability is
// the mail system's problem; this only keeps garbage out of generated code.
func looksLikeAddress(a string) bool {
	if a == "" || !safeScriptString(a) || strings.ContainsAny(a, " \t") {
		return false
	}
	at := strings.IndexByte(a, '@')
	return at > 0 && at < len(a)-1 && !strings.Contains(a[at+1:], "@")
}

// sortedSet renders a string set sorted.
func sortedSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
