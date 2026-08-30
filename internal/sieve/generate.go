package sieve

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The script generator: model -> Sieve, deterministically. Determinism is a
// correctness property here, not tidiness — the drift detector (parse.go)
// decides whether the stored script was hand-edited by regenerating from the
// parsed metadata and comparing bytes, so two generations of the same model
// MUST be identical.
//
// Layout of the generated script:
//
//	/* @moov:begin
//	{metadata JSON}
//	@moov:end */
//	# Managed by Moov Mail. Do not edit: edits outside Moov are preserved
//	# but this file is regenerated. Put your own rules in a separate script.
//	require [...];
//	<blocked rules>          # first: a blocked sender gets no vacation reply
//	<forward-all recipe>     # spam excluded (canon §2.11)
//	<vacation section>       # before user rules, so a rule's stop cannot
//	                         # silence the responder
//	<filter / never-spam rules>
//	<external section>       # foreign content, verbatim
//
// Section order is load-bearing and documented inline where it matters.

// Markers. The metadata sits in a Sieve bracket comment; the JSON is
// guaranteed not to contain "*/" because marshalMetadata escapes the slash of
// any "*/" pair (JSON's \/ escape — same bytes after decoding).
const (
	metaBegin     = "/* @moov:begin"
	metaEnd       = "@moov:end */"
	externalBegin = "# --- moov:external begin (foreign content, preserved verbatim) ---"
	externalEnd   = "# --- moov:external end ---"
)

// Generate renders the model. It validates first — a model that fails
// Validate never produces bytes — and enforces the verified-forward rule
// independently of Validate, so no future refactor can open a path where an
// unverified address reaches a redirect (the GC-4 pin).
func Generate(s *Script, env GenerateEnv, caps *Capabilities) ([]byte, error) {
	env = env.withDefaults()
	if err := s.Validate(env, caps); err != nil {
		return nil, err
	}
	// The independent enforcement: even if Validate regresses, generation
	// refuses to emit a redirect to an unverified address.
	for _, r := range s.Rules {
		if r.Enabled && r.Actions.Forward != "" && !env.VerifiedForward[strings.ToLower(r.Actions.Forward)] {
			return nil, fmt.Errorf("sieve: refusing to generate a redirect to unverified %q", r.Actions.Forward)
		}
	}
	if f := s.ForwardAll; f != nil && f.Enabled && !env.VerifiedForward[strings.ToLower(f.Address)] {
		return nil, fmt.Errorf("sieve: refusing to generate a redirect to unverified %q", f.Address)
	}

	var b strings.Builder

	meta, err := marshalMetadata(s)
	if err != nil {
		return nil, err
	}
	b.WriteString(metaBegin + "\r\n")
	b.WriteString(meta)
	b.WriteString("\r\n" + metaEnd + "\r\n")
	b.WriteString("# Managed by Moov Mail. Edits to this script are preserved as foreign\r\n")
	b.WriteString("# content but not merged; keep hand-written rules in a separate script.\r\n")

	if reqs := s.RequiredExtensions(); len(reqs) > 0 {
		b.WriteString("require " + stringList(reqs) + ";\r\n")
	}

	// Blocked senders, first. Placement is the anti-annoyance rule: the
	// stop keeps the vacation section (below) from replying to a sender the
	// user blocked, which is Gmail's own "never replies to spam" applied to
	// mail the user declared spam.
	for _, r := range s.Rules {
		if r.Enabled && r.Type == RuleBlocked {
			writeBlockedRule(&b, r, env)
		}
	}

	if f := s.ForwardAll; f != nil && f.Enabled {
		writeForwardAll(&b, f, env)
	}

	if v := s.Vacation; v != nil && v.Enabled {
		if err := writeVacation(&b, v, env); err != nil {
			return nil, err
		}
	}

	for _, r := range s.Rules {
		if !r.Enabled {
			continue
		}
		switch r.Type {
		case RuleFilter:
			writeFilterRule(&b, r, env)
		case RuleNeverSpam:
			writeNeverSpamRule(&b, r, env)
		}
	}

	if s.ExternalBody != "" {
		b.WriteString(externalBegin + "\r\n")
		b.WriteString(s.ExternalBody)
		if !strings.HasSuffix(s.ExternalBody, "\n") {
			b.WriteString("\r\n")
		}
		b.WriteString(externalEnd + "\r\n")
	}

	return []byte(b.String()), nil
}

// marshalMetadata renders the metadata JSON, breaking any "*/" byte pair so
// the bracket comment cannot be terminated early by user-controlled strings.
func marshalMetadata(s *Script) (string, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("sieve: marshaling metadata: %w", err)
	}
	// JSON permits escaping the solidus: "*\/" decodes to the same "*/",
	// but the raw bytes no longer contain the comment terminator.
	return strings.ReplaceAll(string(raw), "*/", "*\\/"), nil
}

// ---------------------------------------------------------------------------
// rule emission
// ---------------------------------------------------------------------------

func writeBlockedRule(b *strings.Builder, r Rule, env GenerateEnv) {
	b.WriteString("# blocked:" + r.ID + "\r\n")
	// Exact address match: blocking is Gmail's per-sender verdict, not a
	// substring filter. :is on the :all address part still matches inside
	// display-name forms ("Name <x@y>").
	fmt.Fprintf(b, "if address :all :is \"from\" %s {\r\n", stringList(lowerAll(r.Criteria.From)))
	fmt.Fprintf(b, "    fileinto %s;\r\n", quoteSieve(env.JunkFolder))
	b.WriteString("    stop;\r\n}\r\n")
}

func writeForwardAll(b *strings.Builder, f *ForwardAll, env GenerateEnv) {
	b.WriteString("# forward-all\r\n")
	// Spam excluded, per the canon's Gmail forwarding contract: a tagged
	// message is not redirected. redirect :copy keeps local delivery alive.
	fmt.Fprintf(b, "if not header :contains %s %s {\r\n",
		quoteSieve(strings.ToLower(env.SpamHeader)), quoteSieve(env.SpamValue))
	fmt.Fprintf(b, "    redirect :copy %s;\r\n", quoteSieve(f.Address))
	if f.Disposition == ForwardArchive {
		fmt.Fprintf(b, "    fileinto %s;\r\n", quoteSieve(env.ArchiveFolder))
	}
	b.WriteString("}\r\n")
}

func writeFilterRule(b *strings.Builder, r Rule, env GenerateEnv) {
	b.WriteString("# rule:" + r.ID + "\r\n")
	fmt.Fprintf(b, "if %s {\r\n", criteriaTest(r.Criteria, env, nil))
	writeRuleActions(b, r.Actions, env)
	b.WriteString("}\r\n")
}

// writeNeverSpamRule compiles "never send to spam": the rule matches ONLY
// when Rspamd tagged the message, and then delivers it explicitly, which
// ends the Sieve sequence before Mailcow's global_sieve_after files it into
// Junk (the empirically validated escape — model.go RuleNeverSpam). Untagged
// mail from the same sender flows through the normal pipeline untouched.
func writeNeverSpamRule(b *strings.Builder, r Rule, env GenerateEnv) {
	b.WriteString("# never-spam:" + r.ID + "\r\n")
	spamGuard := fmt.Sprintf("header :contains %s %s",
		quoteSieve(strings.ToLower(env.SpamHeader)), quoteSieve(env.SpamValue))
	fmt.Fprintf(b, "if %s {\r\n", criteriaTest(r.Criteria, env, &spamGuard))
	target := r.Actions.MoveTo
	if target == "" {
		target = "INBOX"
	}
	fmt.Fprintf(b, "    fileinto %s;\r\n", quoteSieve(target))
	b.WriteString("    stop;\r\n}\r\n")
}

// writeRuleActions emits a filter's actions in a fixed order: flags first
// (they never end anything), then the redirect copy, then the filing, then
// stop — so a rule that both labels and moves does all of it.
func writeRuleActions(b *strings.Builder, a Actions, env GenerateEnv) {
	var flags []string
	if a.MarkRead {
		flags = append(flags, `\Seen`)
	}
	if a.Star {
		flags = append(flags, `\Flagged`)
	}
	for _, l := range a.Labels {
		flags = append(flags, "$label:"+l)
	}
	if len(flags) > 0 {
		// quoteSieve escapes the flags' backslashes into the "\\Seen" the
		// script needs, and whatever a label carries along the way.
		fmt.Fprintf(b, "    addflag %s;\r\n", stringList(flags))
	}
	if a.Forward != "" {
		fmt.Fprintf(b, "    redirect :copy %s;\r\n", quoteSieve(a.Forward))
	}
	switch {
	case a.Delete:
		fmt.Fprintf(b, "    fileinto %s;\r\n", quoteSieve(env.TrashFolder))
	case a.MoveTo != "":
		fmt.Fprintf(b, "    fileinto %s;\r\n", quoteSieve(a.MoveTo))
	}
	if a.Stop {
		b.WriteString("    stop;\r\n")
	}
}

// criteriaTest renders the allof(...) test for a criteria set. extra, when
// non-nil, is prepended (the never-spam guard).
func criteriaTest(c Criteria, env GenerateEnv, extra *string) string {
	var tests []string
	if extra != nil {
		tests = append(tests, *extra)
	}
	if len(c.From) > 0 {
		tests = append(tests, fmt.Sprintf("header :contains \"from\" %s", stringList(c.From)))
	}
	if len(c.To) > 0 {
		// Headers To and Cc, plus the envelope recipient — the only witness
		// of a Bcc delivery.
		tests = append(tests, fmt.Sprintf(
			"anyof (header :contains [\"to\", \"cc\"] %s, envelope :all :contains \"to\" %s)",
			stringList(c.To), stringList(c.To)))
	}
	if len(c.Subject) > 0 {
		tests = append(tests, fmt.Sprintf("header :contains \"subject\" %s", stringList(c.Subject)))
	}
	if c.SizeOver > 0 {
		tests = append(tests, fmt.Sprintf("size :over %d", c.SizeOver))
	}
	if c.SizeUnder > 0 {
		tests = append(tests, fmt.Sprintf("size :under %d", c.SizeUnder))
	}
	if c.HasAttachment != nil {
		// RFC 5703: a part with a filename parameter, looked for in both
		// Content-Disposition and Content-Type (older senders only set the
		// latter — the Bulwark finding, kept).
		att := "anyof (header :mime :anychild :param \"filename\" :matches \"content-disposition\" \"*\", " +
			"header :mime :anychild :param \"name\" :matches \"content-type\" \"*\")"
		if *c.HasAttachment {
			tests = append(tests, att)
		} else {
			tests = append(tests, "not "+att)
		}
	}
	if len(tests) == 1 {
		return tests[0]
	}
	return "allof (" + strings.Join(tests, ", ") + ")"
}

// ---------------------------------------------------------------------------
// vacation emission
// ---------------------------------------------------------------------------

// writeVacation emits the vacation section with Gmail's exact anti-annoyance
// spec (canon §2.8):
//
//   - :days 4 — resend only after 4 days.
//   - :handle carries a digest of subject+body, so EDITING the response
//     resets the throttle (Gmail: "or when edited") without waiting out the
//     4 days on the old text.
//   - never to spam: guarded on the Rspamd verdict header. The guard keys on
//     the VERDICT, not the final disposition, because the Junk filing happens
//     in global_sieve_after — AFTER this script ran — so disposition is not
//     knowable here; a message a never-spam rule later rescues still carries
//     the verdict and still gets no auto-reply, which is the conservative
//     reading of "never replies to spam".
//   - never to lists: RFC 5230 §4.6 already forbids replying to
//     Auto-Submitted mail and Pigeonhole implements it; the explicit List-Id/
//     List-Unsubscribe/List-Post and Precedence guards are belt-and-braces
//     for lists that mark themselves only that way.
//   - date window honored to the second, in UTC (model.go Vacation states
//     the timezone decision).
func writeVacation(b *strings.Builder, v *Vacation, env GenerateEnv) error {
	guards := []string{
		fmt.Sprintf("not header :contains %s %s",
			quoteSieve(strings.ToLower(env.SpamHeader)), quoteSieve(env.SpamValue)),
		`not exists ["list-id", "list-unsubscribe", "list-post"]`,
		`not header :is "precedence" ["bulk", "list", "junk"]`,
		`anyof (not exists "auto-submitted", header :is "auto-submitted" "no")`,
	}
	if !v.FromDate.IsZero() {
		guards = append(guards, dateBound(v.FromDate, true))
	}
	if !v.ToDate.IsZero() {
		guards = append(guards, dateBound(v.ToDate, false))
	}

	b.WriteString("# vacation\r\n")
	b.WriteString("if allof (\r\n    " + strings.Join(guards, ",\r\n    ") + "\r\n) {\r\n")

	handle := vacationHandle(v)
	fmt.Fprintf(b, "    vacation :days %d :handle %s", VacationDays, quoteSieve(handle))
	if v.Subject != "" {
		b.WriteString(" :subject " + quoteSieve(v.Subject))
	}
	if v.HTMLBody != "" {
		mime, err := vacationMIME(v)
		if err != nil {
			return err
		}
		b.WriteString(" :mime text:\r\n" + dotStuff(mime) + "\r\n.\r\n;\r\n")
	} else {
		body := v.TextBody
		if body == "" {
			// Subject-only configuration: the reply still needs a body line.
			body = v.Subject
		}
		b.WriteString(" text:\r\n" + dotStuff(body) + "\r\n.\r\n;\r\n")
	}
	b.WriteString("}\r\n")
	return nil
}

// dateBound renders one bound of the vacation window, exact to the second in
// UTC: strictly past the day, or on the day at/before the time. Zero-padded
// ISO date and time compare correctly with the default i;ascii-casemap.
func dateBound(t time.Time, from bool) string {
	t = t.UTC()
	day := t.Format("2006-01-02")
	clock := t.Format("15:04:05")
	cd := `currentdate :zone "+0000"`
	if from {
		return fmt.Sprintf(
			"anyof (%s :value \"gt\" \"date\" %s, allof (%s :is \"date\" %s, %s :value \"ge\" \"time\" %s))",
			cd, quoteSieve(day), cd, quoteSieve(day), cd, quoteSieve(clock))
	}
	return fmt.Sprintf(
		"anyof (%s :value \"lt\" \"date\" %s, allof (%s :is \"date\" %s, %s :value \"le\" \"time\" %s))",
		cd, quoteSieve(day), cd, quoteSieve(day), cd, quoteSieve(clock))
}

// vacationHandle digests the response content, so an edit changes the handle
// and resets the RFC 5230 duplicate tracking.
func vacationHandle(v *Vacation) string {
	h := sha256.Sum256([]byte(v.Subject + "\x00" + v.TextBody + "\x00" + v.HTMLBody))
	return "moov-vacation-" + hex.EncodeToString(h[:8])
}

// vacationMIME builds the multipart/alternative body for an HTML response.
// The HTML arrives pre-sanitized (model.go Vacation.HTMLBody); the text part
// is the stored textBody, or a crude tag-stripped fallback so text-only
// clients always get something readable.
func vacationMIME(v *Vacation) (string, error) {
	text := v.TextBody
	if text == "" {
		text = stripTags(v.HTMLBody)
	}
	const boundary = "=_moov_vacation_alt"
	if strings.Contains(v.HTMLBody, boundary) || strings.Contains(text, boundary) {
		// Vanishingly unlikely, but a body containing the boundary would
		// corrupt the MIME structure; refuse rather than emit garbage.
		return "", fmt.Errorf("sieve: vacation body collides with the MIME boundary")
	}
	var b strings.Builder
	b.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n")
	b.WriteString("MIME-Version: 1.0\r\n\r\n")
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(text + "\r\n")
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/html; charset=utf-8\r\n\r\n")
	b.WriteString(v.HTMLBody + "\r\n")
	b.WriteString("--" + boundary + "--")
	return b.String(), nil
}

// stripTags is the crude HTML-to-text fallback for the alternative part. It
// is not a renderer and does not try to be: the HTML is already sanitized,
// so dropping tags and unescaping the four entities the sanitizer emits is
// enough for a legible plain-text alternative.
func stripTags(html string) string {
	var b strings.Builder
	inTag := false
	for _, r := range html {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	out := b.String()
	for _, pair := range [][2]string{{"&amp;", "&"}, {"&lt;", "<"}, {"&gt;", ">"}, {"&quot;", `"`}, {"&#39;", "'"}} {
		out = strings.ReplaceAll(out, pair[0], pair[1])
	}
	return strings.TrimSpace(out)
}

// ---------------------------------------------------------------------------
// string rendering
// ---------------------------------------------------------------------------

// quoteSieve renders one Sieve quoted string. Validate has already refused
// control characters; the escape covers backslash and double quote (RFC 5228
// §2.4.2).
func quoteSieve(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	b.WriteByte('"')
	return b.String()
}

// stringList renders a Sieve string list.
func stringList(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = quoteSieve(v)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// lowerAll lowercases a value list (blocked-address matching is
// case-insensitive by normalization on both sides).
func lowerAll(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = strings.ToLower(v)
	}
	return out
}

// dotStuff prepares text for a Sieve multiline (text:) literal: CRLF line
// endings and a doubled leading dot on any line that starts with one, so the
// terminating "." line cannot be forged by content (RFC 5228 §2.4.2).
func dotStuff(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, ".") {
			lines[i] = "." + l
		}
	}
	return strings.Join(lines, "\r\n")
}
