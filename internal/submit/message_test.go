package submit

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// Transmission preparation: the goldens that pin what leaves the account.
// Every case states its RFC clause, because a Bcc that survives stripping is
// a privacy leak and a Message-ID that changes between preparations breaks
// the dedupe net.

var prepDate = time.Date(2026, 8, 15, 10, 30, 0, 0, time.UTC)

func TestPrepareTransmissionStripsBcc(t *testing.T) {
	for name, tc := range map[string]struct {
		in   string
		want string
	}{
		// RFC 5322 §3.6.3: "the 'Bcc:' line is removed even though all of
		// the recipients (including those specified in the 'Bcc:' field) are
		// sent a copy of the message."
		"simple": {
			in: "From: a@x.test\r\nBcc: hidden@x.test\r\nTo: b@x.test\r\n" +
				"Message-ID: <m1@x.test>\r\nDate: Fri, 15 Aug 2026 10:00:00 +0000\r\n\r\nbody\r\n",
			want: "From: a@x.test\r\nTo: b@x.test\r\n" +
				"Message-ID: <m1@x.test>\r\nDate: Fri, 15 Aug 2026 10:00:00 +0000\r\n\r\nbody\r\n",
		},
		// RFC 5322 §2.2.3: a folded header continues on lines starting with
		// WSP — the continuation carries recipients and must go with it.
		"folded": {
			in: "Bcc: one@x.test,\r\n two@x.test,\r\n\tthree@x.test\r\nTo: b@x.test\r\n" +
				"Message-ID: <m1@x.test>\r\nDate: Fri, 15 Aug 2026 10:00:00 +0000\r\n\r\nbody\r\n",
			want: "To: b@x.test\r\n" +
				"Message-ID: <m1@x.test>\r\nDate: Fri, 15 Aug 2026 10:00:00 +0000\r\n\r\nbody\r\n",
		},
		// RFC 5322 §4.5.8 obsolete syntax permits WSP before the colon; a
		// stripper matching only "Bcc:" would leak these recipients.
		"obsolete-space": {
			in: "Bcc : hidden@x.test\r\nTo: b@x.test\r\n" +
				"Message-ID: <m1@x.test>\r\nDate: Fri, 15 Aug 2026 10:00:00 +0000\r\n\r\nbody\r\n",
			want: "To: b@x.test\r\n" +
				"Message-ID: <m1@x.test>\r\nDate: Fri, 15 Aug 2026 10:00:00 +0000\r\n\r\nbody\r\n",
		},
		// Case-insensitive field names (§1.2.2), and every occurrence goes.
		"case-and-repeat": {
			in: "BCC: one@x.test\r\nTo: b@x.test\r\nbcc: two@x.test\r\n" +
				"Message-ID: <m1@x.test>\r\nDate: Fri, 15 Aug 2026 10:00:00 +0000\r\n\r\nbody\r\n",
			want: "To: b@x.test\r\n" +
				"Message-ID: <m1@x.test>\r\nDate: Fri, 15 Aug 2026 10:00:00 +0000\r\n\r\nbody\r\n",
		},
		// A body line that LOOKS like a Bcc header is body, not header, and
		// must survive verbatim.
		"bcc-in-body": {
			in: "To: b@x.test\r\nMessage-ID: <m1@x.test>\r\nDate: Fri, 15 Aug 2026 10:00:00 +0000\r\n" +
				"\r\nBcc: not-a-header@x.test\r\n",
			want: "To: b@x.test\r\nMessage-ID: <m1@x.test>\r\nDate: Fri, 15 Aug 2026 10:00:00 +0000\r\n" +
				"\r\nBcc: not-a-header@x.test\r\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := PrepareTransmission([]byte(tc.in), "m1@x.test", prepDate)
			if string(got) != tc.want {
				t.Errorf("PrepareTransmission:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestPrepareTransmissionEnsuresIdentityHeaders(t *testing.T) {
	// RFC 5322 §3.6: Message-ID and Date SHOULD be present; a foreign draft
	// without them gets both, from the intent row's fixed inputs.
	in := "To: b@x.test\r\nSubject: s\r\n\r\nbody\r\n"
	got := string(PrepareTransmission([]byte(in), "generated@x.test", prepDate))

	if !strings.Contains(got, "Message-ID: <generated@x.test>\r\n") {
		t.Errorf("Message-ID not added:\n%q", got)
	}
	if !strings.Contains(got, "Date: Sat, 15 Aug 2026 10:30:00 +0000\r\n") {
		t.Errorf("Date not added from the fixed creation time:\n%q", got)
	}

	// Present headers are never touched.
	withBoth := "Message-ID: <own@x.test>\r\nDate: Thu, 01 Jan 2026 00:00:00 +0000\r\nTo: b@x.test\r\n\r\nbody\r\n"
	got2 := string(PrepareTransmission([]byte(withBoth), "generated@x.test", prepDate))
	if strings.Contains(got2, "generated@x.test") || strings.Contains(got2, "10:30:00") {
		t.Errorf("existing identity headers were overridden:\n%q", got2)
	}
}

func TestPrepareTransmissionIsDeterministic(t *testing.T) {
	// The whole crash-recovery design depends on re-preparation yielding the
	// SAME bytes (message.go's package comment): the \Sent copy and the
	// dedupe key both derive from them.
	in := "To: b@x.test\nBcc: hidden@x.test\nSubject: mixed line endings\n\nbody\n"
	a := PrepareTransmission([]byte(in), "det@x.test", prepDate)
	b := PrepareTransmission([]byte(in), "det@x.test", prepDate)
	if !bytes.Equal(a, b) {
		t.Error("two preparations of the same inputs differ")
	}
}

func TestPrepareTransmissionNormalizesLineEndings(t *testing.T) {
	// RFC 5321 §2.3.8: the wire format is CRLF. Bare LF and bare CR both
	// normalize; already-clean input passes through untouched (same slice).
	in := "To: b@x.test\nSubject: s\rX-A: y\r\n\nbody\n"
	got := PrepareTransmission([]byte(in), "m@x.test", prepDate)
	if bytes.Contains(bytes.ReplaceAll(got, []byte("\r\n"), nil), []byte("\n")) {
		t.Errorf("bare LF survived normalization: %q", got)
	}

	clean := []byte("To: b@x.test\r\nMessage-ID: <m@x.test>\r\nDate: Fri, 15 Aug 2026 10:00:00 +0000\r\n\r\nbody\r\n")
	if got := PrepareTransmission(clean, "m@x.test", prepDate); !bytes.Equal(got, clean) {
		t.Errorf("clean CRLF input was rewritten:\n got %q\nwant %q", got, clean)
	}
}

func TestMessageIDOf(t *testing.T) {
	for in, want := range map[string]string{
		"Message-ID: <abc@x.test>\r\n\r\n":            "abc@x.test",
		"Message-Id: <abc@x.test>\r\n\r\n":            "abc@x.test", // §1.2.2 case-insensitive
		"Message-ID: <folded@x.test\r\n >\r\n\r\nb":   "folded@x.test",
		"Subject: none\r\n\r\n":                       "",
		"Message-ID:   <spaced@x.test>  \r\n\r\nbody": "spaced@x.test",
	} {
		if got := MessageIDOf([]byte(in)); got != want {
			t.Errorf("MessageIDOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHeaderValueUnfolds(t *testing.T) {
	in := "Subject: a long\r\n subject line\r\nTo: b@x.test\r\n\r\nbody"
	if got := HeaderValue([]byte(in), "Subject"); got != "a long subject line" {
		t.Errorf("HeaderValue = %q", got)
	}
}
