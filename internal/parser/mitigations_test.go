package parser

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// One test per mandatory mitigation (L2 §2.4), exercised in ISOLATION.
//
// The corpus suite proves the pipeline produces the right answers end to end.
// These prove each mitigation is actually the thing producing them — a corpus
// case can pass for the wrong reason (another layer compensating), and when one
// of these fails it names the specific mechanism that broke.

// --- Mitigation 1: engine-owned caps (S4 H8, convention C4) ---------------

func TestCapsFailTheMessageRatherThanParsingHarder(t *testing.T) {
	deepNest := buildNestedMultipart(50)

	tests := []struct {
		name     string
		raw      []byte
		limits   Limits
		wantCode DefectCode
	}{
		{
			name: "depth cap",
			raw:  deepNest,
			limits: Limits{
				MaxDepth: 5, MaxParts: 1000, MaxTotalSize: 1 << 20,
				MaxRFC822Depth: 10, MaxPartSize: 1 << 20,
			},
			wantCode: DefectDepthCapExceeded,
		},
		{
			name: "part cap",
			raw:  buildWideMultipart(50),
			limits: Limits{
				MaxDepth: 100, MaxParts: 5, MaxTotalSize: 1 << 20,
				MaxRFC822Depth: 10, MaxPartSize: 1 << 20,
			},
			wantCode: DefectPartCapExceeded,
		},
		{
			name: "total size cap",
			raw:  []byte("Subject: big\r\n\r\n" + strings.Repeat("x", 4096)),
			limits: Limits{
				MaxDepth: 100, MaxParts: 1000, MaxTotalSize: 512,
				MaxRFC822Depth: 10, MaxPartSize: 1 << 20,
			},
			wantCode: DefectSizeCapExceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := ParseBytes(tt.raw, tt.limits)

			if msg.Status != StatusFailed {
				t.Errorf("status = %s, want failed: exceeding a cap is a bounded "+
					"refusal, which convention C4 settles as correct behavior",
					msg.Status)
			}
			if !msg.hasDefect(tt.wantCode) {
				t.Errorf("no %s defect recorded; defects = %v", tt.wantCode, msg.Defects)
			}
			if len(msg.Parts) != 0 {
				t.Errorf("failed parse carries %d parts, want 0", len(msg.Parts))
			}
		})
	}
}

func TestCapsDoNotFireOnLegitimateMail(t *testing.T) {
	// The defaults must not refuse the corpus's most extreme STRUCTURALLY VALID
	// cases, or a cap meant to stop bombs becomes a cause of data loss.
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"500 levels deep", buildNestedMultipart(500)},
		{"1000 siblings", buildWideMultipart(1000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := ParseBytes(tc.raw, DefaultLimits())
			if msg.Status == StatusFailed {
				t.Errorf("default limits refused structurally valid mail: %v", msg.Defects)
			}
		})
	}
}

func TestZeroLimitsMeansDefaultsNotRefuseEverything(t *testing.T) {
	// A zero cap must read as "unset", never as "zero allowed" — otherwise a
	// config oversight turns into total data loss.
	msg := ParseBytes([]byte("Subject: hi\r\n\r\nbody\r\n"), Limits{})
	if msg.Status == StatusFailed {
		t.Fatalf("zero Limits refused an ordinary message: %v", msg.Defects)
	}
	if msg.Headers.Subject != "hi" {
		t.Errorf("subject = %q, want %q", msg.Headers.Subject, "hi")
	}
}

func TestUnterminatedDeepNestIsRefusedQuickly(t *testing.T) {
	// Regression test for a finding this epic's fuzzing turned up, which S4 did
	// not see because every nesting bomb in the corpus is well FORMED.
	//
	// enmime wraps each nesting level's reader in the level below it, so an
	// unterminated nest makes every level re-scan the remaining input for a
	// boundary that never arrives. Measured on the VPS: 289 ms at depth 18,
	// 1.1 s at 20, 5.0 s at 22, 18.1 s at 24 — about 4x per two levels, from
	// under 1.5 KB of input. Deeper still is hours, which is precisely the
	// "hang that holds a folder open forever" S4 §1 named as one of the two
	// failure modes that genuinely threaten sync availability.
	var b strings.Builder
	b.WriteString("Subject: unterminated bomb\r\nMIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/mixed; boundary=\"b0\"\r\n\r\n")
	for i := 0; i < 40; i++ {
		b.WriteString("--b" + itoa(i) + "\r\n")
		b.WriteString("Content-Type: multipart/mixed; boundary=\"b" + itoa(i+1) + "\"\r\n\r\n")
	}
	// Deliberately no close delimiters.
	raw := []byte(b.String())

	done := make(chan ParsedMessage, 1)
	go func() { done <- ParseBytes(raw, DefaultLimits()) }()

	select {
	case msg := <-done:
		if msg.Status != StatusFailed {
			t.Errorf("status = %s, want failed", msg.Status)
		}
		if !msg.hasDefect(DefectDepthCapExceeded) {
			t.Errorf("no depth_cap_exceeded defect; defects = %v", msg.Defects)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parsing an unterminated deep nest took over 5s: the pre-scan " +
			"did not fire and the superlinear path in the MIME library was reached")
	}
}

func TestWellFormedDeepNestIsStillAccepted(t *testing.T) {
	// The guard must be narrow: a CLOSED nest of the same depth is cheap for both
	// libraries and is legitimate MIME, so it must keep parsing.
	raw := buildNestedMultipart(40)

	msg := ParseBytes(raw, DefaultLimits())
	if msg.Status == StatusFailed {
		t.Errorf("a well-formed 40-level nest was refused: %v", msg.Defects)
	}
	if !strings.Contains(msg.BodyText, "bottom of the well") {
		t.Errorf("BodyText = %q, want the innermost leaf", msg.BodyText)
	}
}

// --- Mitigation 2: never discard partial decode bytes (S4 §4.2) ----------

func TestPartialDecodeBytesAreKept(t *testing.T) {
	// The io.ReadAll trap: the decoder returns (data, err) with data populated up
	// to the failure point, and `if err != nil { return nil, err }` throws away
	// recoverable content. S4 calls this the highest-value lesson of the spike,
	// and its own harness got it wrong the first time.
	raw := []byte("Subject: lying cte\r\n" +
		"Content-Type: text/plain; charset=us-ascii\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		"cGF5bG9hZCB3aXRo!!!!not base64 at all\r\n")

	msg := ParseBytes(raw, DefaultLimits())

	if msg.Status == StatusFailed {
		t.Fatalf("a lying Content-Transfer-Encoding must not fail the message: %v",
			msg.Defects)
	}
	if len(msg.Parts) == 0 {
		t.Fatal("no parts extracted")
	}
	got := string(msg.Parts[len(msg.Parts)-1].Content)
	if !strings.Contains(got, "payload with") {
		t.Errorf("decoded prefix was discarded: content = %q, want it to contain "+
			"%q (S4 §4.2)", got, "payload with")
	}
}

func TestPartialReadOfRawInputIsKept(t *testing.T) {
	// The same principle one level up: a truncated stream still yields a
	// readable message far more often than not.
	msg := Parse(&truncatingReader{
		data: []byte("Subject: truncated\r\n\r\nthe body survives"),
	}, DefaultLimits())

	if msg.Status == StatusFailed {
		t.Fatalf("a read error discarded a readable message: %v", msg.Defects)
	}
	if msg.Headers.Subject != "truncated" {
		t.Errorf("subject = %q, want %q", msg.Headers.Subject, "truncated")
	}
	if !msg.hasDefect(DefectBodyReadError) {
		t.Error("the read error was not recorded as a defect")
	}
}

// truncatingReader returns its data and then a non-EOF error, which is what a
// dropped connection looks like to io.ReadAll.
type truncatingReader struct {
	data []byte
	done bool
}

func (r *truncatingReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, errors.New("connection reset by peer")
	}
	r.done = true
	return copy(p, r.data), nil
}

// --- Mitigation 3: RFC 2047 retry pass (S4 §4.1, corpus ew-004) ----------

func TestUnpaddedBase64EncodedWordDecodes(t *testing.T) {
	// The finding both libraries miss identically, with no error and no defect
	// flag: a base64 encoded-word whose payload length is not a multiple of 4.
	// The user would otherwise see raw MIME markup in the subject line.
	const subject = "=?UTF-8?B?UmV1bmnDs24gbWVuc3VhbA?=" // len % 4 == 2
	raw := []byte("Subject: " + subject + "\r\nFrom: a@example.com\r\n\r\nbody\r\n")

	msg := ParseBytes(raw, DefaultLimits())

	if got, want := msg.Headers.Subject, "Reunión mensual"; got != want {
		t.Errorf("subject = %q, want %q — the raw-base64 retry pass did not fire",
			got, want)
	}
	if !msg.Headers.RFC2047Retried {
		t.Error("RFC2047Retried flag not set on the headers")
	}
	if !msg.hasDefect(DefectRFC2047Retried) {
		t.Errorf("no rfc2047_retried defect; defects = %v", msg.Defects)
	}
}

func TestEncodedWordDecodingEdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		want    string
	}{
		{
			name:    "padded base64 still works",
			subject: "=?UTF-8?B?UmV1bmnDs24gbWVuc3VhbA==?=",
			want:    "Reunión mensual",
		},
		{
			name:    "q encoding",
			subject: "=?UTF-8?Q?Presupuesto_acci=C3=B3n?=",
			want:    "Presupuesto acción",
		},
		{
			name:    "adjacent words join without a space",
			subject: "=?UTF-8?B?YWNjaQ==?= =?UTF-8?B?w7Nu?=",
			want:    "acción",
		},
		{
			name:    "unterminated word is left as literal text",
			subject: "=?UTF-8?Q?nunca_se_cierra",
			want:    "=?UTF-8?Q?nunca_se_cierra",
		},
		{
			name:    "plain ascii is untouched",
			subject: "just a subject",
			want:    "just a subject",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte("Subject: " + tt.subject + "\r\n\r\nbody\r\n")
			msg := ParseBytes(raw, DefaultLimits())
			if got := msg.Headers.Subject; got != tt.want {
				t.Errorf("subject = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- Mitigation 4: charset cascade (S4 H6, research 04 §4.2) -------------

func TestCharsetCascadeFallsBackTo1252NotUTF8(t *testing.T) {
	// research 04 §4.2 is specific that the floor is windows-1252, NOT UTF-8.
	// 0x93/0x94 are smart quotes in 1252 and undefined in ISO-8859-1; decoding
	// them as UTF-8 would produce replacement characters instead of text.
	raw := []byte("Subject: junk charset\r\n" +
		"Content-Type: text/plain; charset=definitely-not-a-charset\r\n\r\n" +
		"He said \x93hello\x94 to the se\xf1or and paid 50\x80 for it\r\n")

	msg := ParseBytes(raw, DefaultLimits())

	if len(msg.Parts) == 0 {
		t.Fatal("no parts extracted")
	}
	body := string(msg.Parts[len(msg.Parts)-1].Content)
	for _, want := range []string{"“hello”", "señor", "50€"} {
		if !strings.Contains(body, want) {
			t.Errorf("body = %q, want it to contain %q (windows-1252 floor)", body, want)
		}
	}
	if strings.Contains(body, "�") {
		t.Errorf("body contains replacement characters, so the fallback was not "+
			"windows-1252: %q", body)
	}
}

func TestCharsetGuessedIsFlagged(t *testing.T) {
	// "No parse error" must never be read as "text is correct" (S4 §4.3), so any
	// guess has to be visible to the layer above.
	raw := []byte("Subject: unknown 8bit\r\n" +
		"Content-Type: text/plain; charset=unknown-8bit\r\n\r\n" +
		"El se\xf1or dijo hola y todo el mundo se qued\xf3 contento\r\n")

	msg := ParseBytes(raw, DefaultLimits())

	if len(msg.Parts) == 0 {
		t.Fatal("no parts extracted")
	}
	leaf := msg.Parts[len(msg.Parts)-1]
	if !leaf.CharsetGuessed {
		t.Error("CharsetGuessed not set on a part whose charset was guessed")
	}
	if !msg.hasDefect(DefectCharsetGuessed) {
		t.Errorf("no charset_guessed defect; defects = %v", msg.Defects)
	}
	if msg.Status != StatusPartial {
		t.Errorf("status = %s, want partial: a guessed charset is exactly the "+
			"'recovered but something was guessed' case", msg.Status)
	}
}

func TestHonestlyDeclaredCharsetsAreNotGuessed(t *testing.T) {
	// The control: when the declaration is honest, the cascade must stop at step
	// one and NOT mark the text as guessed.
	tests := []struct {
		name    string
		charset string
		body    []byte
		want    string
	}{
		{"koi8-r", "koi8-r", []byte{0xf0, 0xd2, 0xc9, 0xd7, 0xc5, 0xd4}, "Привет"},
		{"utf-8", "utf-8", []byte("señor"), "señor"},
		{"windows-1256", "windows-1256", []byte{0xE3, 0xD1, 0xCD, 0xC8, 0xC7}, "مرحبا"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := append([]byte("Subject: honest\r\nContent-Type: text/plain; charset="+
				tt.charset+"\r\n\r\n"), tt.body...)
			msg := ParseBytes(raw, DefaultLimits())
			if len(msg.Parts) == 0 {
				t.Fatal("no parts")
			}
			leaf := msg.Parts[len(msg.Parts)-1]
			if got := strings.TrimSpace(string(leaf.Content)); got != tt.want {
				t.Errorf("body = %q, want %q", got, tt.want)
			}
			if leaf.CharsetGuessed {
				t.Error("an honestly declared charset was marked as guessed")
			}
		})
	}
}

func TestPerPartCharsetScoping(t *testing.T) {
	// A parser that latches the first charset it sees and applies it to the rest
	// produces silent wrong data across most of the message.
	raw := []byte("Subject: mixed\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b\"\r\n\r\n" +
		"--b\r\nContent-Type: text/plain; charset=iso-8859-1\r\n\r\n" +
		"uno: se\xf1or\r\n" +
		"--b\r\nContent-Type: text/plain; charset=koi8-r\r\n\r\n" +
		"dos: \xf0\xd2\xc9\xd7\xc5\xd4\r\n" +
		"--b--\r\n")

	msg := ParseBytes(raw, DefaultLimits())
	body := msg.BodyText

	for _, want := range []string{"uno: señor", "dos: Привет"} {
		if !strings.Contains(body, want) {
			t.Errorf("body text = %q, want it to contain %q — decoding must be "+
				"per-part, never per-message", body, want)
		}
	}
}

// --- Mitigation 5: message/rfc822 descent (S4 H7 / §4.4) ----------------

func TestRFC822DescentReachesForwardedText(t *testing.T) {
	// Neither library descends into message/rfc822 on its own, so forwarded
	// content would never be searchable. Users expect to find it.
	const secret = "quixotic-forwarded-payload"
	inner := "Subject: the forwarded one\r\nFrom: b@example.com\r\n\r\n" + secret + "\r\n"
	raw := []byte("Subject: the outer one\r\nFrom: a@example.com\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b\"\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nplease see attached\r\n" +
		"--b\r\nContent-Type: message/rfc822\r\n\r\n" + inner +
		"--b--\r\n")

	msg := ParseBytes(raw, DefaultLimits())

	if !strings.Contains(msg.BodyText, secret) {
		t.Errorf("forwarded text is not in BodyText, so it will not be indexed "+
			"(S4 §4.4)\nBodyText = %q", msg.BodyText)
	}
	// The outermost headers are the ones that belong to the stored message.
	if got, want := msg.Headers.Subject, "the outer one"; got != want {
		t.Errorf("subject = %q, want %q — the embedded message's headers must "+
			"not overwrite the outer ones", got, want)
	}
	// Convention C1: the rfc822 part counts as ONE leaf and its interior is not
	// added to the enclosing total.
	if got, want := len(msg.LeafParts()), 2; got != want {
		t.Errorf("leaf parts = %d, want %d (convention C1)", got, want)
	}
}

func TestRFC822DescentIsBounded(t *testing.T) {
	// Descent multiplies work per level, so it needs a budget of its own — and
	// running out must leave the wrapper opaque rather than failing the message.
	raw := buildRecursiveRFC822(20)

	limits := DefaultLimits()
	limits.MaxRFC822Depth = 3
	msg := ParseBytes(raw, limits)

	if msg.Status == StatusFailed {
		t.Fatalf("hitting the rfc822 budget must not fail the message: %v", msg.Defects)
	}
	if !msg.hasDefect(DefectRFC822DepthCapped) {
		t.Errorf("no rfc822_depth_capped defect; defects = %v", msg.Defects)
	}
	if msg.Status != StatusPartial {
		t.Errorf("status = %s, want partial", msg.Status)
	}
}

// --- Mitigation 6: line-ending normalization (S4 H9) --------------------

func TestCROnlyInputIsNormalized(t *testing.T) {
	// Neither parser normalizes bare CR, so a CR-only message is one long line
	// with no header/body separator and no delimiters anywhere.
	crOnly := []byte("Subject: cr only\rContent-Type: multipart/mixed; boundary=\"b\"\r" +
		"\r--b\rContent-Type: text/plain\r\rfirst part body\r" +
		"--b\rContent-Type: text/plain\r\rsecond part body\r--b--\r")

	msg := ParseBytes(crOnly, DefaultLimits())

	if !msg.hasDefect(DefectLineEndingNormalized) {
		t.Errorf("no line_ending_normalized defect; defects = %v", msg.Defects)
	}
	if got, want := msg.Headers.Subject, "cr only"; got != want {
		t.Errorf("subject = %q, want %q", got, want)
	}
	if got, want := len(msg.LeafParts()), 2; got != want {
		t.Errorf("leaf parts = %d, want %d — normalization did not restore the "+
			"structure", got, want)
	}
}

func TestLFOnlyKeepsWorkingAndBareCRInBodyIsUntouched(t *testing.T) {
	// The normalization must be narrowly scoped: applied only when the buffer has
	// CR and no LF at all, so it can never corrupt these two.
	t.Run("lf-only", func(t *testing.T) {
		raw := []byte("Subject: lf only\nContent-Type: multipart/mixed; boundary=\"b\"\n\n" +
			"--b\nContent-Type: text/plain\n\nfirst\n--b\nContent-Type: text/plain\n\nsecond\n--b--\n")
		msg := ParseBytes(raw, DefaultLimits())
		if msg.hasDefect(DefectLineEndingNormalized) {
			t.Error("LF-only input was normalized; it must be left alone")
		}
		if got, want := len(msg.LeafParts()), 2; got != want {
			t.Errorf("leaf parts = %d, want %d", got, want)
		}
	})

	t.Run("bare cr inside a body", func(t *testing.T) {
		raw := []byte("Subject: bare cr\r\n\r\nbefore\rafter\r\n")
		msg := ParseBytes(raw, DefaultLimits())
		if msg.hasDefect(DefectLineEndingNormalized) {
			t.Error("a message containing LF was normalized; the CR-only rule " +
				"must not fire here (corpus le-010)")
		}
		if len(msg.Parts) == 0 {
			t.Fatal("no parts")
		}
		if got := string(msg.Parts[0].Content); !strings.Contains(got, "before\rafter") {
			t.Errorf("body = %q, want the bare CR preserved", got)
		}
	})
}

// --- Mitigation 7: both-fail -> salvage -> failed (S4 H3, §2) -----------

func TestSalvageRecoversBodyWhenStructureIsUnrecoverable(t *testing.T) {
	// A multipart with no boundary parameter: genuinely unsplittable, and both
	// libraries refuse it. The manifest is emphatic that losing the bytes is not
	// acceptable — the user would see a blank message.
	const body = "There is no boundary parameter, so there is nothing to split on."
	raw := []byte("Subject: no boundary\r\nFrom: a@example.com\r\n" +
		"Content-Type: multipart/mixed\r\n\r\n" + body + "\r\n")

	msg := ParseBytes(raw, DefaultLimits())

	if msg.Status == StatusFailed {
		t.Fatalf("salvage did not run: %v", msg.Defects)
	}
	if msg.Parser != ParserSalvage && !strings.Contains(msg.BodyText, body) {
		t.Errorf("body was lost: parser = %s, BodyText = %q", msg.Parser, msg.BodyText)
	}
	if !strings.Contains(msg.BodyText, body) {
		t.Errorf("BodyText = %q, want it to contain the salvaged body", msg.BodyText)
	}
	// A salvaged message must never be presented as a clean parse.
	if msg.Status == StatusOK && msg.Parser == ParserSalvage {
		t.Error("a salvaged message reported status ok")
	}
}

func TestSalvageDiscardsOrphanedContinuationRatherThanMisattributingIt(t *testing.T) {
	// S4 §2 caught enmime concatenating the orphan line into the following From
	// header while failing. The case notes call that a defect explicitly.
	raw := []byte("   orphaned continuation with nothing to continue\r\n" +
		"From: ada@example.com\r\n" +
		"Subject: Leading continuation line\r\n" +
		"Content-Type: text/plain\r\n\r\nbody\r\n")

	msg := ParseBytes(raw, DefaultLimits())

	if msg.Status == StatusFailed {
		t.Fatalf("the message was lost: %v", msg.Defects)
	}
	if got, want := msg.Headers.Subject, "Leading continuation line"; got != want {
		t.Errorf("subject = %q, want %q", got, want)
	}
	for _, a := range msg.Headers.From {
		if strings.Contains(a.Name, "orphaned") || strings.Contains(a.Address, "orphaned") {
			t.Errorf("the orphan line was misattributed to From: %+v", a)
		}
	}
}

func TestEmptyInputFailsCleanly(t *testing.T) {
	// The requirement is to fail CLEANLY — never a partially-initialized struct
	// that downstream code mistakes for a real message.
	msg := ParseBytes(nil, DefaultLimits())

	if msg.Status != StatusFailed {
		t.Errorf("status = %s, want failed", msg.Status)
	}
	if !msg.hasDefect(DefectEmptyInput) {
		t.Errorf("no empty_input defect; defects = %v", msg.Defects)
	}
	if len(msg.Parts) != 0 || msg.BodyText != "" || msg.SubjectText != "" {
		t.Error("a failed parse returned content")
	}
}

func TestFailedParseNeverEmitsHeaders(t *testing.T) {
	// S4 §2: enmime damaged the header block on its way out of a hard failure, so
	// partial headers from a failed parse must never be trusted or propagated.
	msg := ParseBytes([]byte{}, DefaultLimits())
	if len(msg.Headers.All) != 0 {
		t.Errorf("failed parse emitted headers: %v", msg.Headers.All)
	}
	if msg.Headers.Subject != "" || len(msg.Headers.From) != 0 {
		t.Error("failed parse emitted typed header fields")
	}
}

func TestNonASCIICaseFoldingDoesNotPanic(t *testing.T) {
	// Regression test for a panic found by fuzzing: code that searched a
	// strings.ToLower copy of a header and then indexed the ORIGINAL with the
	// result. Unicode lowercasing can change a string's byte length — U+212A
	// KELVIN SIGN is three bytes and lowercases to a one-byte "k" — so the two
	// strings' offsets are not interchangeable, and the mismatch is a slice
	// bounds panic reachable by any sender.
	//
	// A panic in the parse path kills the sync worker, which is one of the two
	// failure modes S4 §1 named as genuinely threatening sync availability.
	kelvin := "K" // uppercase in Unicode; lowercases to ASCII "k"

	inputs := [][]byte{
		[]byte("Content-Type: text/plain; " + kelvin + "charset=utf-8\r\n\r\nbody\r\n"),
		[]byte("Content-Type: text/plain; x=" + kelvin + "; charset=\"utf-8\"\r\n\r\nbody\r\n"),
		[]byte("Content-Type: text/" + kelvin + "; charset=utf-8; charset=ascii\r\n\r\nb\r\n"),
		[]byte("Content-Type: text/html\r\n\r\n<p>" + kelvin + "</p><SCRIPT>x</SCRIPT>hi\r\n"),
		[]byte("Content-Type: text/html\r\n\r\n" + kelvin + "<img src=\"CID:" + kelvin + "@x\">\r\n"),
		[]byte("Content-Type: text/html\r\n\r\n<STYLE>" + kelvin + "</STYLE>text\r\n"),
	}

	for i, raw := range inputs {
		// A panic here fails the test by unwinding; the assertions below just
		// confirm the result is still coherent.
		msg := ParseBytes(raw, DefaultLimits())
		if msg.Parser == "" {
			t.Errorf("input %d: result names no parser", i)
		}
	}
}

func TestEverythingEmittedIsStorableUTF8(t *testing.T) {
	// The store's tsv, subject and body columns are PostgreSQL TEXT, which
	// rejects NUL outright and fails on invalid UTF-8 — either aborts the
	// transaction that stores the message. So "valid UTF-8, no NUL" is a hard
	// contract of this package, not a nicety.
	//
	// Regression test: fuzzing caught raw 8-bit header bytes (corpus hdr-003's
	// shape) reaching SubjectText undecoded, because they never pass through an
	// encoded-word and so nothing had decoded them.
	cases := map[string][]byte{
		"raw 8-bit subject":     []byte("Subject: se\xf1or dijo hola\r\n\r\nbody\r\n"),
		"lone continuation":     []byte("Subject: a\x80b\r\n\r\nbody\r\n"),
		"truncated utf-8":       []byte("Subject: caf\xc3\r\n\r\nbody\r\n"),
		"8-bit display name":    []byte("From: Se\xf1or <a@b.c>\r\nSubject: x\r\n\r\nbody\r\n"),
		"lying encoded-word":    []byte("Subject: =?UTF-8?Q?se=F1or?=\r\n\r\nbody\r\n"),
		"8-bit body no charset": []byte("Subject: s\r\n\r\nse\xf1or dijo hola y se qued\xf3\r\n"),
		"nul in subject":        []byte("Subject: a\x00b\r\n\r\nbody\r\n"),
		"nul in body":           []byte("Subject: s\r\n\r\nbe\x00fore\r\n"),
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			msg := ParseBytes(raw, DefaultLimits())

			for field, s := range map[string]string{
				"SubjectText":     msg.SubjectText,
				"AddressText":     msg.AddressText,
				"BodyText":        msg.BodyText,
				"Headers.Subject": msg.Headers.Subject,
			} {
				if !utf8.ValidString(s) {
					t.Errorf("%s is not valid UTF-8: %q", field, s)
				}
				if strings.ContainsRune(s, 0) {
					t.Errorf("%s contains a NUL byte: %q", field, s)
				}
			}
		})
	}
}

func TestRaw8BitHeaderDecodesToRealText(t *testing.T) {
	// Not merely valid UTF-8 — the right text. Latin-1/1252 bytes in a header are
	// ordinary in real mail, and the user must see "señor", not "se?or".
	msg := ParseBytes([]byte("Subject: se\xf1or dijo \x93hola\x94\r\n\r\nbody\r\n"),
		DefaultLimits())

	if got, want := msg.Headers.Subject, "señor dijo “hola”"; got != want {
		t.Errorf("subject = %q, want %q", got, want)
	}
}

// --- The output contract -------------------------------------------------

func TestWeightedTextFieldsAreSeparate(t *testing.T) {
	// The store applies distinct tsvector weights to subject, addresses and body
	// (L2 §2.3), so they must arrive separated rather than pre-joined.
	raw := []byte("Subject: the subject line\r\n" +
		"From: Ada Lovelace <ada@example.com>\r\n" +
		"To: Grace Hopper <grace@example.org>\r\n\r\n" +
		"the body text\r\n")

	msg := ParseBytes(raw, DefaultLimits())

	if got, want := msg.SubjectText, "the subject line"; got != want {
		t.Errorf("SubjectText = %q, want %q", got, want)
	}
	if !strings.Contains(msg.AddressText, "Ada Lovelace") ||
		!strings.Contains(msg.AddressText, "grace@example.org") {
		t.Errorf("AddressText = %q, want the display names and addresses", msg.AddressText)
	}
	if got, want := strings.TrimSpace(msg.BodyText), "the body text"; got != want {
		t.Errorf("BodyText = %q, want %q", got, want)
	}
	// No cross-contamination: the body must not carry the subject, or the store's
	// weighting would be meaningless.
	if strings.Contains(msg.BodyText, "the subject line") {
		t.Error("BodyText contains the subject; the fields are not separated")
	}
	// And the flat form still works for consumers that want it.
	flat := msg.TextForFTS()
	for _, want := range []string{"the subject line", "Ada Lovelace", "the body text"} {
		if !strings.Contains(flat, want) {
			t.Errorf("TextForFTS() = %q, want it to contain %q", flat, want)
		}
	}
}

func TestBccIsNotIndexed(t *testing.T) {
	// Bcc is not part of the message as the recipient received it, and indexing
	// it would leak the blind-copy list into search results.
	raw := []byte("Subject: s\r\nFrom: a@example.com\r\n" +
		"Bcc: secret-recipient@example.com\r\n\r\nbody\r\n")

	msg := ParseBytes(raw, DefaultLimits())

	if strings.Contains(msg.AddressText, "secret-recipient") {
		t.Errorf("AddressText leaks the Bcc list: %q", msg.AddressText)
	}
}

func TestHTMLAlternativePrefersPlainTextForIndexing(t *testing.T) {
	raw := []byte("Subject: alt\r\n" +
		"Content-Type: multipart/alternative; boundary=\"b\"\r\n\r\n" +
		"--b\r\nContent-Type: text/html\r\n\r\n<p>the <b>content</b></p>\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nthe content\r\n" +
		"--b--\r\n")

	msg := ParseBytes(raw, DefaultLimits())

	if n := strings.Count(msg.BodyText, "the content"); n != 1 {
		t.Errorf("BodyText = %q: the same content appears %d times, want 1 "+
			"(the plain-text alternative should win)", msg.BodyText, n)
	}
	if strings.Contains(msg.BodyText, "<b>") {
		t.Errorf("BodyText contains markup: %q", msg.BodyText)
	}
}

func TestHTMLOnlyBodyIsStrippedForIndexing(t *testing.T) {
	raw := []byte("Subject: html\r\nContent-Type: text/html\r\n\r\n" +
		"<html><head><style>p{color:red}</style></head>" +
		"<body><p>hello</p><script>alert(1)</script><p>world</p></body></html>\r\n")

	msg := ParseBytes(raw, DefaultLimits())

	if !strings.Contains(msg.BodyText, "hello") || !strings.Contains(msg.BodyText, "world") {
		t.Errorf("BodyText = %q, want the prose", msg.BodyText)
	}
	for _, unwanted := range []string{"alert(1)", "color:red", "<p>"} {
		if strings.Contains(msg.BodyText, unwanted) {
			t.Errorf("BodyText = %q, want it to exclude %q", msg.BodyText, unwanted)
		}
	}
	// "a<br>b" must not index as "ab": tag boundaries are word boundaries.
	msg2 := ParseBytes([]byte("Content-Type: text/html\r\n\r\na<br>b\r\n"), DefaultLimits())
	if strings.Contains(msg2.BodyText, "ab") {
		t.Errorf("BodyText = %q: a tag boundary was not treated as a word boundary",
			msg2.BodyText)
	}
}

func TestParseAcceptsAnyReaderAndNeverReturnsAnError(t *testing.T) {
	// The signature is the contract of L2 §4.2: no error return, because the
	// engine's rule is that a bad message must not break a folder's sync.
	for _, r := range []io.Reader{
		strings.NewReader(""),
		strings.NewReader("garbage"),
		&truncatingReader{data: []byte("Subject: x\r\n\r\ny")},
	} {
		msg := Parse(r, DefaultLimits())
		if msg.Parser == "" {
			t.Error("result names no parser")
		}
	}
}

// --- helpers -------------------------------------------------------------

// buildNestedMultipart makes a message with depth levels of multipart/mixed
// around a single text/plain leaf.
func buildNestedMultipart(depth int) []byte {
	var b strings.Builder
	b.WriteString("Subject: nested\r\nMIME-Version: 1.0\r\n")
	for i := 0; i < depth; i++ {
		if i == 0 {
			b.WriteString("Content-Type: multipart/mixed; boundary=\"b0\"\r\n\r\n")
		} else {
			b.WriteString("\r\n")
		}
		b.WriteString("--b" + itoa(i) + "\r\n")
		if i < depth-1 {
			b.WriteString("Content-Type: multipart/mixed; boundary=\"b" + itoa(i+1) + "\"\r\n")
		}
	}
	b.WriteString("Content-Type: text/plain\r\n\r\nbottom of the well\r\n")
	for i := depth - 1; i >= 0; i-- {
		b.WriteString("--b" + itoa(i) + "--\r\n")
	}
	return []byte(b.String())
}

// buildWideMultipart makes a flat multipart with n sibling text/plain parts.
func buildWideMultipart(n int) []byte {
	var b strings.Builder
	b.WriteString("Subject: wide\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"w\"\r\n\r\n")
	for i := 0; i < n; i++ {
		b.WriteString("--w\r\nContent-Type: text/plain\r\n\r\npart number " + itoa(i) + "\r\n")
	}
	b.WriteString("--w--\r\n")
	return []byte(b.String())
}

// buildRecursiveRFC822 makes depth levels of message/rfc822 wrapping.
func buildRecursiveRFC822(depth int) []byte {
	inner := "Subject: innermost\r\nContent-Type: text/plain\r\n\r\nthe innermost payload\r\n"
	for i := depth; i > 0; i-- {
		inner = "Subject: Wrapper level " + itoa(i) + "\r\n" +
			"Content-Type: message/rfc822\r\n\r\n" + inner
	}
	return []byte(inner)
}
