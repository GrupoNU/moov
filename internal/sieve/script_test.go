package sieve

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The round-trip property the whole managed-script model rests on:
// parse(generate(x)) == x, and foreign content byte-identical through a full
// edit cycle. These are the tests the epic's spec pins by name.

func testEnv() GenerateEnv {
	return GenerateEnv{
		VerifiedForward: map[string]bool{"dest@example.org": true},
	}
}

func boolp(b bool) *bool { return &b }

func fullModel() *Script {
	return &Script{
		Version: MetadataVersion,
		Rules: []Rule{
			{ID: "r1", Name: "facturas", Type: RuleFilter, Enabled: true,
				Criteria: Criteria{From: []string{"billing@acme.com"}, Subject: []string{"factura"},
					SizeUnder: 5_000_000, HasAttachment: boolp(true)},
				Actions: Actions{MoveTo: "Facturas", Labels: []string{"Contabilidad"},
					MarkRead: true, Stop: true}},
			{ID: "r2", Type: RuleBlocked, Enabled: true,
				Criteria: Criteria{From: []string{"Spammer@Junkmail.example"}}},
			{ID: "r3", Type: RuleNeverSpam, Enabled: true,
				Criteria: Criteria{From: []string{"newsletter@trusted.example"}}},
			{ID: "r4", Type: RuleFilter, Enabled: false,
				Criteria: Criteria{To: []string{"lista@example.org"}},
				Actions:  Actions{Star: true}},
			{ID: "r5", Type: RuleFilter, Enabled: true,
				Criteria: Criteria{SizeOver: 10_000_000},
				Actions:  Actions{Forward: "dest@example.org", Delete: true}},
		},
		Vacation: &Vacation{
			Enabled:  true,
			FromDate: time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC),
			ToDate:   time.Date(2026, 9, 15, 2, 59, 59, 0, time.UTC),
			Subject:  "Fuera de la oficina",
			TextBody: "Vuelvo el 15.\r\n.empieza con punto",
			HTMLBody: "<p>Vuelvo el <b>15</b>.</p>",
		},
		ForwardAll: &ForwardAll{Enabled: true, Address: "dest@example.org", Disposition: ForwardArchive},
	}
}

func TestRoundTripFullModel(t *testing.T) {
	env := testEnv()
	x := fullModel()

	content, err := Generate(x, env, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	got, drifted, err := ParseManaged(content, env)
	if err != nil {
		t.Fatalf("ParseManaged: %v", err)
	}
	if drifted {
		t.Fatal("a freshly generated script parsed as drifted; generation is not deterministic")
	}
	if !reflect.DeepEqual(got, x) {
		t.Errorf("parse(generate(x)) != x\n got  %+v\n want %+v", got, x)
	}
}

// Foreign content survives a full edit cycle byte-identical: import, push,
// parse, edit the model, regenerate, parse again — the external body never
// changes by a byte.
func TestForeignContentByteIdenticalThroughEditCycle(t *testing.T) {
	env := testEnv()
	foreign := "# SOGo made this\r\n" +
		"require [\"fileinto\", \"regex\"];\r\n" +
		"if header :regex \"subject\" \".*urgent.*\" {\r\n    fileinto \"Urgent\";\r\n}\r\n"

	x := &Script{Version: MetadataVersion}
	ImportForeign(x, "sogo", []byte(foreign))

	if !reflect.DeepEqual(x.ExternalRequires, []string{"fileinto", "regex"}) {
		t.Fatalf("external requires = %v", x.ExternalRequires)
	}
	wantBody := x.ExternalBody
	if !strings.Contains(wantBody, "fileinto \"Urgent\"") {
		t.Fatalf("imported body lost content: %q", wantBody)
	}
	if strings.Contains(wantBody, "require") {
		t.Fatalf("imported body still contains a require, which would be invalid mid-script: %q", wantBody)
	}

	// Cycle 1: generate, parse.
	content, err := Generate(x, env, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(string(content), "\"regex\"") {
		t.Error("the merged require line lost the foreign extension")
	}
	got, drifted, err := ParseManaged(content, env)
	if err != nil || drifted {
		t.Fatalf("ParseManaged: err=%v drifted=%v", err, drifted)
	}
	if got.ExternalBody != wantBody {
		t.Fatalf("cycle 1 changed foreign bytes:\n got  %q\n want %q", got.ExternalBody, wantBody)
	}

	// Cycle 2: edit the model (add a rule), regenerate, parse.
	got.Rules = append(got.Rules, Rule{ID: "n1", Type: RuleBlocked, Enabled: true,
		Criteria: Criteria{From: []string{"x@y.example"}}})
	content2, err := Generate(got, env, nil)
	if err != nil {
		t.Fatalf("Generate 2: %v", err)
	}
	got2, drifted, err := ParseManaged(content2, env)
	if err != nil || drifted {
		t.Fatalf("ParseManaged 2: err=%v drifted=%v", err, drifted)
	}
	if got2.ExternalBody != wantBody {
		t.Fatalf("cycle 2 changed foreign bytes:\n got  %q\n want %q", got2.ExternalBody, wantBody)
	}
}

// Importing the same content twice must not duplicate it.
func TestImportForeignIsIdempotent(t *testing.T) {
	x := &Script{Version: MetadataVersion}
	ImportForeign(x, "sogo", []byte("keep;\r\n"))
	once := x.ExternalBody
	ImportForeign(x, "sogo", []byte("keep;\r\n"))
	if x.ExternalBody != once {
		t.Fatalf("re-import duplicated content:\n%q", x.ExternalBody)
	}
}

// The verified-forward pin (GC-4): an unverified address never reaches a
// redirect — refused by Validate AND, independently, by Generate.
func TestUnverifiedForwardIsRefused(t *testing.T) {
	env := testEnv()
	x := &Script{Version: MetadataVersion, Rules: []Rule{{
		ID: "r1", Type: RuleFilter, Enabled: true,
		Criteria: Criteria{From: []string{"a@b.c"}},
		Actions:  Actions{Forward: "attacker@evil.example"},
	}}}
	if err := x.Validate(env, nil); err == nil {
		t.Fatal("Validate accepted an unverified forward address")
	}
	if _, err := Generate(x, env, nil); err == nil {
		t.Fatal("Generate emitted a redirect to an unverified address")
	}

	fa := &Script{Version: MetadataVersion,
		ForwardAll: &ForwardAll{Enabled: true, Address: "attacker@evil.example"}}
	if _, err := Generate(fa, env, nil); err == nil {
		t.Fatal("Generate emitted a forward-all redirect to an unverified address")
	}
}

// The model refuses discard by construction: there is no way to express it,
// and delete compiles to a Trash filing.
func TestDeleteFilesIntoTrashNeverDiscards(t *testing.T) {
	env := testEnv()
	x := &Script{Version: MetadataVersion, Rules: []Rule{{
		ID: "r1", Type: RuleFilter, Enabled: true,
		Criteria: Criteria{From: []string{"a@b.c"}},
		Actions:  Actions{Delete: true},
	}}}
	content, err := Generate(x, env, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	s := string(content)
	if !strings.Contains(s, `fileinto "Trash";`) {
		t.Errorf("delete did not file into Trash:\n%s", s)
	}
	if strings.Contains(s, "discard") {
		t.Errorf("generated script contains discard:\n%s", s)
	}
}

// Never-spam compiles to the empirically validated pipeline escape: guarded
// on the Rspamd verdict header, explicit INBOX delivery, stop.
func TestNeverSpamCompilesToGuardedInboxDelivery(t *testing.T) {
	env := testEnv()
	x := &Script{Version: MetadataVersion, Rules: []Rule{{
		ID: "ns", Type: RuleNeverSpam, Enabled: true,
		Criteria: Criteria{From: []string{"news@ok.example"}},
	}}}
	content, err := Generate(x, env, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	s := string(content)
	for _, want := range []string{
		`header :contains "x-spam-flag" "YES"`,
		`fileinto "INBOX";`,
		"stop;",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("never-spam output missing %q:\n%s", want, s)
		}
	}
}

// Vacation: Gmail's anti-annoyance spec is in the emitted code — :days 4,
// the edit-sensitive :handle, the spam and list guards, the second-exact UTC
// window.
func TestVacationEmission(t *testing.T) {
	env := testEnv()
	x := fullModel()
	content, err := Generate(x, env, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	s := string(content)
	for _, want := range []string{
		"vacation :days 4 :handle \"moov-vacation-",
		`not header :contains "x-spam-flag" "YES"`,
		`not exists ["list-id", "list-unsubscribe", "list-post"]`,
		`not header :is "precedence" ["bulk", "list", "junk"]`,
		`anyof (not exists "auto-submitted", header :is "auto-submitted" "no")`,
		`:value "gt" "date" "2026-09-01"`,
		`:value "ge" "time" "03:00:00"`,
		`:value "lt" "date" "2026-09-15"`,
		`:value "le" "time" "02:59:59"`,
		"multipart/alternative",
		"..empieza con punto", // dot-stuffing of the text body
	} {
		if !strings.Contains(s, want) {
			t.Errorf("vacation output missing %q", want)
		}
	}

	// Editing the response changes the handle, which is what resets the
	// 4-day throttle "when edited" (canon §2.8).
	before := vacationHandle(x.Vacation)
	edited := *x.Vacation
	edited.TextBody += " (editado)"
	if vacationHandle(&edited) == before {
		t.Error("editing the body did not change the vacation handle")
	}
}

// The metadata block survives hostile strings: a criterion containing the
// bracket-comment terminator cannot break out of the header.
func TestMetadataCannotBeTerminatedByContent(t *testing.T) {
	env := testEnv()
	x := &Script{Version: MetadataVersion, Rules: []Rule{{
		ID: "r1", Type: RuleFilter, Enabled: true,
		Criteria: Criteria{Subject: []string{"evil */ require whatever"}},
		Actions:  Actions{Star: true},
	}}}
	content, err := Generate(x, env, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	meta := string(content)[:strings.Index(string(content), metaEnd)]
	if strings.Contains(meta, "*/") {
		t.Fatal("the metadata comment contains */ from user content; the header can be terminated early")
	}
	got, drifted, err := ParseManaged(content, env)
	if err != nil || drifted {
		t.Fatalf("ParseManaged: err=%v drifted=%v", err, drifted)
	}
	if got.Rules[0].Criteria.Subject[0] != "evil */ require whatever" {
		t.Errorf("the hostile subject did not round-trip: %q", got.Rules[0].Criteria.Subject[0])
	}
}

// Required extensions are computed from used features and checked against
// the advertised list.
func TestRequiredExtensionsAndCapabilityCheck(t *testing.T) {
	x := fullModel()
	got := x.RequiredExtensions()
	// r4 (the only To-criterion rule) is disabled, so "envelope" is
	// correctly NOT required: the require line covers emitted code only.
	want := []string{"copy", "date", "fileinto", "imap4flags", "mime", "relational", "vacation"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RequiredExtensions = %v, want %v", got, want)
	}

	caps := &Capabilities{Extensions: []string{"fileinto", "imap4flags"}}
	err := x.Validate(testEnv(), caps)
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("Validate against a poor server = %v, want *ValidationError", err)
	}
	if !strings.Contains(err.Error(), "vacation") {
		t.Errorf("the validation error does not name the missing extension: %v", err)
	}
}

// Disabled rules ride in the metadata but emit no code.
func TestDisabledRulesEmitNothing(t *testing.T) {
	env := testEnv()
	x := fullModel()
	content, err := Generate(x, env, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.Contains(string(content), "# rule:r4") {
		t.Error("a disabled rule emitted code")
	}
	got, _, err := ParseManaged(content, env)
	if err != nil {
		t.Fatalf("ParseManaged: %v", err)
	}
	if len(got.Rules) != 5 {
		t.Errorf("the disabled rule was lost from the metadata: %d rules", len(got.Rules))
	}
}

// A hand-edited managed script is reported drifted; a foreign script is
// ErrNotManaged.
func TestDriftAndForeignDetection(t *testing.T) {
	env := testEnv()
	content, err := Generate(fullModel(), env, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	edited := strings.Replace(string(content), `fileinto "Facturas";`, `fileinto "Otro";`, 1)
	_, drifted, err := ParseManaged([]byte(edited), env)
	if err != nil {
		t.Fatalf("ParseManaged(edited): %v", err)
	}
	if !drifted {
		t.Error("a hand-edited script was not reported drifted")
	}

	_, _, err = ParseManaged([]byte("require \"fileinto\";\r\nkeep;\r\n"), env)
	if !errors.Is(err, ErrNotManaged) {
		t.Errorf("foreign content = %v, want ErrNotManaged", err)
	}
}

// The redirect scanner: finds targets through comments, strings and lists,
// and fails closed on garbage.
func TestScanRedirects(t *testing.T) {
	script := "# redirect \"decoy@x\" in a comment\r\n" +
		"/* redirect \"decoy2@x\" */\r\n" +
		"require [\"copy\"];\r\n" +
		"if true { redirect :copy \"real@dest.example\"; }\r\n" +
		"redirect [\"a@b.example\", \"c@d.example\"];\r\n" +
		"if header :contains \"subject\" \"redirect \\\"decoy3@x\\\"\" { keep; }\r\n"
	got, err := ScanRedirects([]byte(script))
	if err != nil {
		t.Fatalf("ScanRedirects: %v", err)
	}
	want := []string{"real@dest.example", "a@b.example", "c@d.example"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ScanRedirects = %v, want %v", got, want)
	}

	if _, err := ScanRedirects([]byte("redirect \"unterminated")); err == nil {
		t.Error("an unterminated string did not fail the scan; the policy gate would fail open")
	}
	if _, err := ScanRedirects([]byte("redirect ;")); err == nil {
		t.Error("a redirect without a readable address did not fail the scan")
	}
}

// The generated script passes the REAL server's CHECKSCRIPT — env-gated like
// every live test, skipping cleanly otherwise. This is what pins that the
// emission (mime tests, date guards, text: blocks, vacation :mime) is valid
// Pigeonhole Sieve, not just plausible-looking text.
func TestIntegrationGeneratedScriptChecks(t *testing.T) {
	cfg, ok := integrationConfig(t)
	if !ok {
		return
	}
	ctx := testCtx(t)
	c := New(nil)
	if err := c.Connect(ctx, cfg); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = c.Close() }()

	caps := c.Capabilities()
	content, err := Generate(fullModel(), testEnv(), &caps)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if warnings, err := c.CheckScript(ctx, content); err != nil {
		t.Fatalf("the real server rejected the generated script: %v\n---\n%s", err, content)
	} else if warnings != "" {
		t.Logf("server warnings (not a failure): %s", warnings)
	}
}
