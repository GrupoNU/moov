package mail

import (
	"strings"
	"testing"

	"github.com/GrupoNU/moov/internal/parser"
)

// E10 (canon §4.1.15): the suspicious-mail vendor property. The unit half
// pins the verdict grammar against the headers Mailcow's Rspamd actually
// stamps (E6 recon: X-Spam-Flag: YES is the header global_sieve_after files
// into Junk on; X-Spamd-Result is the extended_spam_headers form); the
// handler half pins the JMAP contract — served only when asked by name,
// absent from the default §4.6 set, false when there is no verdict to read.

// taggedSpamMessage is a delivered message as Mailcow's Rspamd stamps
// tagged-but-accepted spam (both the flag and the extended result header).
const taggedSpamMessage = "From: seller@example.net\r\n" +
	"To: bob@example.com\r\n" +
	"Subject: oferta imperdible\r\n" +
	"Date: Mon, 24 Aug 2026 10:00:00 +0000\r\n" +
	"Message-ID: <spam-1@example.net>\r\n" +
	"X-Spam-Flag: YES\r\n" +
	"X-Spamd-Result: default: True [16.10 / 15.00];\r\n" +
	" BAYES_SPAM(5.10)[99.99%];\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Compre ahora.\r\n"

func parseFixture(t *testing.T, raw string) *parser.ParsedMessage {
	t.Helper()
	p := parser.Parse(strings.NewReader(raw), parser.Limits{})
	if p.Status == parser.StatusFailed {
		t.Fatalf("fixture does not parse:\n%s", raw)
	}
	return &p
}

func headerFixture(t *testing.T, headers string) *parser.ParsedMessage {
	t.Helper()
	return parseFixture(t, "From: a@example.com\r\n"+headers+"\r\n\r\nbody\r\n")
}

func TestSuspiciousVerdictReadsTheRspamdHeaders(t *testing.T) {
	cases := []struct {
		name    string
		headers string
		want    bool
	}{
		// The Mailcow verdict header, exactly as stamped (E6 recon), and the
		// case laxity real pipelines exhibit.
		{"x-spam-flag yes", "X-Spam-Flag: YES\r\n", true},
		{"x-spam-flag mixed case", "X-Spam-Flag: Yes\r\n", true},
		{"x-spam-flag no", "X-Spam-Flag: NO\r\n", false},
		// Rspamd's spam-header routine under its default name.
		{"x-spam yes", "X-Spam: Yes\r\n", true},
		{"x-spam no", "X-Spam: No\r\n", false},
		// The extended form: the boolean after the settings-id colon.
		{"spamd-result true", "X-Spamd-Result: default: True [16.10 / 15.00];\r\n", true},
		{"spamd-result false", "X-Spamd-Result: default: False [1.31 / 15.00];\r\n", false},
		// The settings-id is a NAME, not necessarily "default".
		{"spamd-result custom settings id", "X-Spamd-Result: mx-policy: True [20.00 / 15.00];\r\n", true},
		// A sender cannot smuggle a verdict into an unrelated header.
		{"verdict text in subject only", "Subject: X-Spam-Flag: YES\r\n", false},
		// No scanner headers at all (mail that never crossed Rspamd — e.g. a
		// Sent copy Moov appended itself): no verdict is not a verdict.
		{"no scanner headers", "Subject: hola\r\n", false},
		// "True" must be the verdict token, not a substring elsewhere.
		{"spamd-result false with true in symbols", "X-Spamd-Result: default: False [1.00 / 15.00]; TRUEISH(0.0)[];\r\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := suspiciousVerdict(headerFixture(t, tc.headers)); got != tc.want {
				t.Errorf("suspiciousVerdict = %v, want %v for:\n%s", got, tc.want, tc.headers)
			}
		})
	}
}

func TestSuspiciousVerdictNilAndFailedParses(t *testing.T) {
	if suspiciousVerdict(nil) {
		t.Error("a missing blob must not read as suspicious")
	}
	// A hard-failed parse has empty headers by construction (types.go:
	// StatusFailed refuses partial headers); the verdict must be false, not a
	// panic and not true.
	failed := &parser.ParsedMessage{Status: parser.StatusFailed}
	if suspiciousVerdict(failed) {
		t.Error("a failed parse has no verdict to read")
	}
}

// The JMAP half: the property arrives when asked by name, with the verdict.
func TestEmailGetSuspiciousProperty(t *testing.T) {
	f := newFakeReaders()
	f.emails[testAccountID] = []EmailRow{sampleEmail(1, "oferta imperdible"), sampleEmail(2, "Hello")}
	f.raw[1] = []byte(taggedSpamMessage)
	f.raw[2] = []byte(plainMessage)
	d := f.deps()

	got := callGet(t, d.handleEmailGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":["`+EncodeEmailID(1)+`","`+EncodeEmailID(2)+`"],`+
			`"properties":["id","moov:suspicious"]}`)

	spam := firstObject(t, got, 0)
	if spam[PropSuspicious] != true {
		t.Errorf("tagged message: %s = %v, want true", PropSuspicious, spam[PropSuspicious])
	}
	clean := firstObject(t, got, 1)
	if clean[PropSuspicious] != false {
		t.Errorf("clean message: %s = %v, want false", PropSuspicious, clean[PropSuspicious])
	}
}

// A missing blob yields false — no verdict — never an error: the message
// must still render from what the store holds.
func TestEmailGetSuspiciousMissingBlobIsFalse(t *testing.T) {
	f := newFakeReaders()
	f.emails[testAccountID] = []EmailRow{sampleEmail(1, "Hello")}
	// No f.raw entry: the blob is absent.
	d := f.deps()

	got := callGet(t, d.handleEmailGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":["`+EncodeEmailID(1)+`"],`+
			`"properties":["id","moov:suspicious"]}`)

	e := firstObject(t, got, 0)
	if e[PropSuspicious] != false {
		t.Errorf("%s = %v, want false when the blob is gone", PropSuspicious, e[PropSuspicious])
	}
}

// The vendor property must never leak into the §4.6 default set: a client
// that did not ask (Bulwark) sees a byte-identical server.
func TestEmailGetSuspiciousAbsentFromDefaults(t *testing.T) {
	f := newFakeReaders()
	f.emails[testAccountID] = []EmailRow{sampleEmail(1, "oferta")}
	f.raw[1] = []byte(taggedSpamMessage)
	d := f.deps()

	got := callGet(t, d.handleEmailGet,
		`{"accountId":"`+testAccountJMAPID()+`","ids":["`+EncodeEmailID(1)+`"]}`)

	e := firstObject(t, got, 0)
	if _, present := e[PropSuspicious]; present {
		t.Errorf("%s served without being requested — the default §4.6 set must stay standard", PropSuspicious)
	}
	for _, p := range defaultEmailProperties {
		if p == PropSuspicious {
			t.Fatalf("%s found in defaultEmailProperties", PropSuspicious)
		}
	}
}
