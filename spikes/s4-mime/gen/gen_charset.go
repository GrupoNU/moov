package main

import (
	"bytes"
	"encoding/base64"
	"strings"
)

// ---------------------------------------------------------------------------
// 4. RFC 2047 encoded-words
// ---------------------------------------------------------------------------

func b64(s []byte) string { return base64.StdEncoding.EncodeToString(s) }

func genEncodedWords() {
	const cat = "04-encoded-words"

	// The canonical hard case: a multi-byte UTF-8 character split across two
	// encoded-words. RFC 2047 forbids it explicitly ("each encoded-word MUST
	// represent an integral number of characters") and real MUAs do it anyway
	// when they fold at a fixed byte count.
	//
	// "Presupuesto acción ñandú" in UTF-8. We split so that the two bytes of
	// "ó" (0xC3 0xB3) land in different encoded-words.
	{
		full := []byte("Presupuesto acci\xc3\xb3n \xc3\xb1and\xc3\xba")
		cut := bytes.Index(full, []byte("\xc3\xb3")) + 1 // between 0xC3 and 0xB3
		w1 := "=?UTF-8?B?" + b64(full[:cut]) + "?="
		w2 := "=?UTF-8?B?" + b64(full[cut:]) + "?="
		emitS(cat, "001-split-multibyte-b64.eml",
			hdrRaw("ew-split-b64", w1+" "+w2)+
				"Content-Type: text/plain; charset=utf-8"+CRLF+CRLF+
				"Correct decoding is: Presupuesto accion nandu (with accents)."+CRLF)
	}

	// Same pathology in Q-encoding: "=C3" in one word, "=B3" in the next.
	emitS(cat, "002-split-multibyte-q.eml",
		hdrRaw("ew-split-q", "=?UTF-8?Q?Presupuesto_acci=C3?= =?UTF-8?Q?=B3n?=")+
			"Content-Type: text/plain; charset=utf-8"+CRLF+CRLF+
			"The 'o' with acute accent is split across two encoded-words."+CRLF)

	// Wrong charset declared: bytes are UTF-8, the word says ISO-8859-1.
	// A conforming parser produces mojibake ("AcciÃ³n") — and that mojibake is
	// the CORRECT output for the input. Detecting and "fixing" it is a
	// heuristic decision the sync engine has to make consciously.
	emitS(cat, "003-wrong-charset-declared.eml",
		hdrRaw("ew-wrongcs", "=?ISO-8859-1?B?"+b64([]byte("Acci\xc3\xb3n requerida"))+"?=")+
			"Content-Type: text/plain; charset=utf-8"+CRLF+CRLF+
			"Bytes are UTF-8; the encoded-word claims ISO-8859-1."+CRLF)

	// Base64 encoded-word with bad padding (one '=' stripped).
	{
		enc := b64([]byte("Reunión mensual"))
		enc = strings.TrimRight(enc, "=")
		emitS(cat, "004-b64-bad-padding.eml",
			hdrRaw("ew-badpad", "=?UTF-8?B?"+enc+"?=")+
				"Content-Type: text/plain; charset=utf-8"+CRLF+CRLF+
				"Padding removed from the base64 encoded-word."+CRLF)
	}

	// Encoded-word far over the 75-character RFC 2047 limit (~5000 chars).
	{
		payload := strings.Repeat("largo ", 800) // ~4800 bytes
		emitS(cat, "005-oversized-encoded-word.eml",
			hdrRaw("ew-huge", "=?UTF-8?B?"+b64([]byte(payload))+"?=")+
				"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
				"One encoded-word of roughly 6500 characters, unfolded."+CRLF)
	}

	// Adjacent encoded-words. Per RFC 2047 the whitespace BETWEEN two
	// encoded-words is deleted; whitespace between an encoded-word and plain
	// text is kept. Getting this backwards is the single most common
	// encoded-word bug.
	//   "=?..?Q?uno?= =?..?Q?dos?=" -> "unodos"   (space removed)
	//   "=?..?Q?uno?= dos"          -> "uno dos"  (space kept)
	emitS(cat, "006-adjacent-words-whitespace.eml",
		hdrRaw("ew-adjacent", "=?UTF-8?Q?uno?= =?UTF-8?Q?dos?= tres =?UTF-8?Q?cuatro?=")+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"Expected: 'unodos tres cuatro' — space between adjacent words is dropped."+CRLF)

	// Encoded-words with no separating whitespace at all, and one glued
	// directly to surrounding plain text (illegal: an encoded-word must be
	// delimited by whitespace from ordinary text).
	emitS(cat, "007-glued-to-plain-text.eml",
		hdrRaw("ew-glued", "prefix=?UTF-8?Q?medio?=sufijo =?UTF-8?Q?a?==?UTF-8?Q?b?=")+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"Encoded-words glued to plain text without whitespace delimiters."+CRLF)

	// Encoded-word inside an address display-name (legal in the phrase
	// position) and, illegally, inside the addr-spec itself.
	emitS(cat, "008-encoded-word-in-address.eml",
		"From: =?UTF-8?Q?Mar=C3=ADa_Jos=C3=A9_Pe=C3=B1a?= <maria@example.com>"+CRLF+
			"To: \"=?UTF-8?B?"+b64([]byte("Grace Hopper (dirección)"))+"?=\" <grace@example.org>"+CRLF+
			"Cc: =?UTF-8?Q?ilegal?=@example.org"+CRLF+
			"Subject: Encoded-words in address fields"+CRLF+
			"Date: Mon, 06 Jan 2025 10:00:00 +0000"+CRLF+
			"Message-ID: <ew-address@corpus.example.com>"+CRLF+
			"MIME-Version: 1.0"+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"body"+CRLF)

	// Encoded-word with an unknown charset token, and one with an unknown
	// encoding letter (neither B nor Q).
	emitS(cat, "009-unknown-charset-and-encoding.eml",
		hdrRaw("ew-unknown", "=?X-MADE-UP-CHARSET?B?"+b64([]byte("payload"))+"?= =?UTF-8?X?cGF5bG9hZA==?=")+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"Unknown charset token and unknown encoding letter."+CRLF)

	// Unterminated encoded-word: opening "=?" with no closing "?=".
	emitS(cat, "010-unterminated-encoded-word.eml",
		hdrRaw("ew-unterm", "=?UTF-8?Q?nunca_se_cierra")+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"Opening delimiter with no terminator: must be treated as literal text."+CRLF)

	// Q-encoding edge cases: '_' means space (not underscore), a bare '=' not
	// followed by two hex digits, and lowercase hex digits.
	emitS(cat, "011-q-encoding-edges.eml",
		hdrRaw("ew-qedge", "=?UTF-8?Q?a_b=3Dc=g=c3=b1?=")+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"Underscore-as-space, bare '=g', and lowercase hex '=c3=b1'."+CRLF)

	// Encoded-word folded across a line break in the middle of the word.
	emitS(cat, "012-encoded-word-folded-midword.eml",
		"From: Ada Lovelace <ada@example.com>"+CRLF+
			"To: Grace Hopper <grace@example.org>"+CRLF+
			"Subject: =?UTF-8?Q?primera_parte"+CRLF+
			" _segunda_parte?="+CRLF+
			"Date: Mon, 06 Jan 2025 10:00:00 +0000"+CRLF+
			"Message-ID: <ew-folded@corpus.example.com>"+CRLF+
			"MIME-Version: 1.0"+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"A single encoded-word folded across two lines mid-token."+CRLF)
}

// hdrRaw is hdr() but the subject is inserted verbatim (used by the
// encoded-word cases, whose whole point is the exact subject bytes).
func hdrRaw(id, rawSubject string) string {
	var b strings.Builder
	b.WriteString("From: Ada Lovelace <ada@example.com>" + CRLF)
	b.WriteString("To: Grace Hopper <grace@example.org>" + CRLF)
	b.WriteString("Subject: " + rawSubject + CRLF)
	b.WriteString("Date: Mon, 06 Jan 2025 10:00:00 +0000" + CRLF)
	b.WriteString("Message-ID: <" + id + "@corpus.example.com>" + CRLF)
	b.WriteString("MIME-Version: 1.0" + CRLF)
	return b.String()
}

// ---------------------------------------------------------------------------
// 5. Charset hell
// ---------------------------------------------------------------------------

func genCharsets() {
	const cat = "05-charsets"

	// Declared UTF-8, actually windows-1252. The bytes 0x93/0x94 are curly
	// quotes in cp1252 and invalid UTF-8 sequences. A decoder that trusts the
	// declaration emits U+FFFD; the correct behavior is to detect the failure
	// and fall back (research doc: fall back to windows-1252, not UTF-8,
	// because 1252 decodes every byte).
	emitS(cat, "001-declared-utf8-actually-cp1252.eml",
		hdr("cs-1252-as-utf8", "Declared UTF-8, actually cp1252")+
			"Content-Type: text/plain; charset=utf-8"+CRLF+
			"Content-Transfer-Encoding: 8bit"+CRLF+CRLF+
			"El se\xf1or dijo \x93hola\x94 y se fue \x97 sin m\xe1s."+CRLF)

	// Declared windows-1252, actually UTF-8. Decoding as 1252 yields the
	// classic "seÃ±or" mojibake — and produces NO error, so nothing flags it.
	emitS(cat, "002-declared-cp1252-actually-utf8.eml",
		hdr("cs-utf8-as-1252", "Declared cp1252, actually UTF-8")+
			"Content-Type: text/plain; charset=windows-1252"+CRLF+
			"Content-Transfer-Encoding: 8bit"+CRLF+CRLF+
			"El se\xc3\xb1or dijo \xe2\x80\x9chola\xe2\x80\x9d y se fue."+CRLF)

	// Double-encoded mojibake round trip: text was UTF-8, got decoded as
	// latin-1 and re-encoded as UTF-8, then declared ISO-8859-1. Two layers of
	// damage. Correct output is the mojibake; "repairing" it is a heuristic.
	{
		// "señor" UTF-8 = C3 B1 -> read as latin1 "Ã±" -> re-encoded UTF-8:
		// C3 83 C2 B1
		emitS(cat, "003-double-encoded-mojibake.eml",
			hdr("cs-double", "Double-encoded mojibake")+
				"Content-Type: text/plain; charset=ISO-8859-1"+CRLF+
				"Content-Transfer-Encoding: 8bit"+CRLF+CRLF+
				"El se\xc3\x83\xc2\xb1or con doble codificaci\xc3\x83\xc2\xb3n."+CRLF)
	}

	// ISO-2022-JP with the escape sequence cut mid-stream: the message
	// switches to JIS X 0208 with ESC $ B, emits two-byte chars, and is
	// truncated before the ESC ( B that returns to ASCII.
	emitS(cat, "004-iso2022jp-truncated-escape.eml",
		hdr("cs-2022jp", "ISO-2022-JP escape cut mid-stream")+
			"Content-Type: text/plain; charset=ISO-2022-JP"+CRLF+
			"Content-Transfer-Encoding: 7bit"+CRLF+CRLF+
			"Hello \x1b$B$3$s$K$A$O"+CRLF+
			"more ascii text after an unterminated JIS shift"+CRLF+
			"and a truncated escape at the very end: \x1b$"+CRLF)

	// GB18030 body, correctly declared. "中文测试" in GB18030.
	emitS(cat, "005-gb18030-correct.eml",
		hdr("cs-gb18030", "GB18030 declared correctly")+
			"Content-Type: text/plain; charset=GB18030"+CRLF+
			"Content-Transfer-Encoding: 8bit"+CRLF+CRLF+
			"\xd6\xd0\xce\xc4\xb2\xe2\xca\xd4 - Chinese test text."+CRLF)

	// Same GB18030 bytes, lying: declared UTF-8.
	emitS(cat, "006-gb18030-declared-utf8.eml",
		hdr("cs-gb18030-lie", "GB18030 bytes declared as UTF-8")+
			"Content-Type: text/plain; charset=utf-8"+CRLF+
			"Content-Transfer-Encoding: 8bit"+CRLF+CRLF+
			"\xd6\xd0\xce\xc4\xb2\xe2\xca\xd4 - same bytes, wrong declaration."+CRLF)

	// KOI8-R, correctly declared. "Привет" in KOI8-R.
	emitS(cat, "007-koi8r-correct.eml",
		hdr("cs-koi8r", "KOI8-R declared correctly")+
			"Content-Type: text/plain; charset=KOI8-R"+CRLF+
			"Content-Transfer-Encoding: 8bit"+CRLF+CRLF+
			"\xf0\xd2\xc9\xd7\xc5\xd4 - Russian greeting in KOI8-R."+CRLF)

	// KOI8-R bytes declared as ISO-8859-1: decodes cleanly, produces garbage
	// Latin letters, and no error is raised anywhere. Silent-wrong-data class.
	emitS(cat, "008-koi8r-declared-latin1.eml",
		hdr("cs-koi8r-lie", "KOI8-R bytes declared ISO-8859-1")+
			"Content-Type: text/plain; charset=ISO-8859-1"+CRLF+
			"Content-Transfer-Encoding: 8bit"+CRLF+CRLF+
			"\xf0\xd2\xc9\xd7\xc5\xd4 - decodes without error into nonsense."+CRLF)

	// windows-1256 (Arabic), correctly declared.
	emitS(cat, "009-windows1256-correct.eml",
		hdr("cs-1256", "windows-1256 declared correctly")+
			"Content-Type: text/plain; charset=windows-1256"+CRLF+
			"Content-Transfer-Encoding: 8bit"+CRLF+CRLF+
			"\xe3\xd1\xcd\xc8\xc7 - Arabic greeting in windows-1256."+CRLF)

	// Bogus charset names: x-user-defined, unknown-8bit, and an empty string.
	emitS(cat, "010-charset-x-user-defined.eml",
		hdr("cs-xuser", "charset=x-user-defined")+
			"Content-Type: text/plain; charset=x-user-defined"+CRLF+
			"Content-Transfer-Encoding: 8bit"+CRLF+CRLF+
			"raw high bytes: \x80\x81\xfe\xff under a non-charset label."+CRLF)

	emitS(cat, "011-charset-unknown-8bit.eml",
		hdr("cs-unknown8", "charset=unknown-8bit")+
			"Content-Type: text/plain; charset=unknown-8bit"+CRLF+
			"Content-Transfer-Encoding: 8bit"+CRLF+CRLF+
			"El se\xf1or - latin1 bytes labelled unknown-8bit (RFC 1428 token)."+CRLF)

	emitS(cat, "012-charset-empty-string.eml",
		hdr("cs-empty", "charset=\"\"")+
			"Content-Type: text/plain; charset=\"\""+CRLF+
			"Content-Transfer-Encoding: 8bit"+CRLF+CRLF+
			"El se\xf1or - empty charset parameter."+CRLF)

	// Charset name with junk around it: whitespace, quotes, trailing semicolon
	// and a duplicated charset parameter that disagrees with itself.
	emitS(cat, "013-charset-parameter-junk.eml",
		hdr("cs-junk", "Charset parameter with junk")+
			"Content-Type: text/plain;  charset = \" utf-8 \" ; charset=iso-8859-1;"+CRLF+
			"Content-Transfer-Encoding: 8bit"+CRLF+CRLF+
			"El se\xc3\xb1or - which charset parameter wins?"+CRLF)

	// UTF-8 with a BOM at the start of the body, plus an unpaired surrogate
	// encoded as CESU-8 (ED A0 80) which is invalid UTF-8.
	emitS(cat, "014-utf8-bom-and-invalid-sequences.eml",
		hdr("cs-bom", "UTF-8 BOM and invalid sequences")+
			"Content-Type: text/plain; charset=utf-8"+CRLF+
			"Content-Transfer-Encoding: 8bit"+CRLF+CRLF+
			"\xef\xbb\xbfBOM at start. Lone surrogate: \xed\xa0\x80. Overlong: \xc0\xaf. Truncated: \xe2\x82"+CRLF)

	// A multipart where each part declares a different charset — the sync
	// engine must decode per-part, not per-message.
	{
		b := "=_mixcs_="
		var sb strings.Builder
		sb.WriteString(hdr("cs-perpart", "Different charset per part"))
		sb.WriteString("Content-Type: multipart/mixed; boundary=\"" + b + "\"" + CRLF + CRLF)
		parts := []struct{ cs, body string }{
			{"utf-8", "parte uno: se\xc3\xb1or"},
			{"ISO-8859-1", "parte dos: se\xf1or"},
			{"KOI8-R", "parte tres: \xf0\xd2\xc9\xd7\xc5\xd4"},
			{"windows-1252", "parte cuatro: \x93comillas\x94"},
		}
		for _, p := range parts {
			sb.WriteString("--" + b + CRLF)
			sb.WriteString("Content-Type: text/plain; charset=" + p.cs + CRLF)
			sb.WriteString("Content-Transfer-Encoding: 8bit" + CRLF + CRLF)
			sb.WriteString(p.body + CRLF)
		}
		sb.WriteString("--" + b + "--" + CRLF)
		emitS(cat, "015-per-part-charsets.eml", sb.String())
	}
}

// ---------------------------------------------------------------------------
// 6. Content-Transfer-Encoding lies
// ---------------------------------------------------------------------------

func genCTE() {
	const cat = "06-cte"

	// Declared base64, body is plain text. Note the text is chosen so it
	// contains characters outside the base64 alphabet — a decoder that skips
	// invalid characters (Go's does not, by default) yields different garbage
	// than one that errors.
	emitS(cat, "001-declared-b64-actual-text.eml",
		hdr("cte-b64-text", "Declared base64, actually plain text")+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+
			"Content-Transfer-Encoding: base64"+CRLF+CRLF+
			"This is obviously not base64! It has spaces, punctuation, and '!'."+CRLF+
			"Second line of very much not base64."+CRLF)

	// Declared quoted-printable, body is base64.
	emitS(cat, "002-declared-qp-actual-b64.eml",
		hdr("cte-qp-b64", "Declared quoted-printable, actually base64")+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+
			"Content-Transfer-Encoding: quoted-printable"+CRLF+CRLF+
			b64([]byte("The real payload was base64 all along."))+CRLF)

	// Valid base64 with invalid characters interleaved (spaces mid-token,
	// a '!' and a '@'). Whitespace in base64 is legal per RFC 2045 and must be
	// skipped; '!' and '@' are not.
	{
		enc := b64([]byte("payload with interleaved garbage in its encoding"))
		var sb strings.Builder
		for i, r := range enc {
			sb.WriteRune(r)
			if i == 8 {
				sb.WriteString(" ")
			}
			if i == 16 {
				sb.WriteString("!")
			}
			if i == 24 {
				sb.WriteString("@")
			}
		}
		emitS(cat, "003-b64-interleaved-garbage.eml",
			hdr("cte-b64-garbage", "base64 with interleaved invalid characters")+
				"Content-Type: text/plain; charset=us-ascii"+CRLF+
				"Content-Transfer-Encoding: base64"+CRLF+CRLF+
				sb.String()+CRLF)
	}

	// base64 with the padding stripped, and base64 with excess padding.
	{
		enc := b64([]byte("padding matters"))
		emitS(cat, "004-b64-missing-padding.eml",
			hdr("cte-b64-nopad", "base64 with padding stripped")+
				"Content-Type: text/plain; charset=us-ascii"+CRLF+
				"Content-Transfer-Encoding: base64"+CRLF+CRLF+
				strings.TrimRight(enc, "=")+CRLF)

		emitS(cat, "005-b64-excess-padding.eml",
			hdr("cte-b64-overpad", "base64 with excess padding")+
				"Content-Type: text/plain; charset=us-ascii"+CRLF+
				"Content-Transfer-Encoding: base64"+CRLF+CRLF+
				enc+"===="+CRLF)
	}

	// Quoted-printable with a bare '=' at end of input (a soft line break
	// with nothing after it), lowercase hex, an '=' followed by non-hex, and
	// trailing whitespace before a soft break (which must be stripped).
	emitS(cat, "006-qp-bare-equals-eof.eml",
		hdr("cte-qp-bare", "QP with a bare = at EOF")+
			"Content-Type: text/plain; charset=utf-8"+CRLF+
			"Content-Transfer-Encoding: quoted-printable"+CRLF+CRLF+
			"a line ending in a soft break ="+CRLF+
			"and then a truncated escape at the very end: =")

	emitS(cat, "007-qp-lowercase-hex.eml",
		hdr("cte-qp-lower", "QP with lowercase hex digits")+
			"Content-Type: text/plain; charset=utf-8"+CRLF+
			"Content-Transfer-Encoding: quoted-printable"+CRLF+CRLF+
			"se=c3=b1or with lowercase hex, se=C3=B1or with uppercase."+CRLF)

	emitS(cat, "008-qp-invalid-escapes.eml",
		hdr("cte-qp-bad", "QP with invalid escape sequences")+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+
			"Content-Transfer-Encoding: quoted-printable"+CRLF+CRLF+
			"not hex: =ZZ and =G1 and = (space after equals)."+CRLF+
			"trailing whitespace before soft break:   ="+CRLF+
			"final line."+CRLF)

	// Raw binary (NUL, 0xFF, control characters) declared as 7bit.
	{
		var body bytes.Buffer
		body.WriteString(hdr("cte-binary-7bit", "Binary content declared 7bit"))
		body.WriteString("Content-Type: application/octet-stream" + CRLF)
		body.WriteString("Content-Disposition: attachment; filename=\"blob.bin\"" + CRLF)
		body.WriteString("Content-Transfer-Encoding: 7bit" + CRLF + CRLF)
		for i := 0; i < 256; i++ {
			body.WriteByte(byte(i))
		}
		body.WriteString(CRLF)
		emit(cat, "009-binary-declared-7bit.eml", body.Bytes())
	}

	// Unknown / non-standard CTE values. RFC 2045 says an unrecognized CTE
	// must be treated as application/octet-stream, not silently as 8bit.
	emitS(cat, "010-unknown-cte-value.eml",
		hdr("cte-unknown", "Unknown Content-Transfer-Encoding")+
			"Content-Type: text/plain; charset=utf-8"+CRLF+
			"Content-Transfer-Encoding: x-uuencode"+CRLF+CRLF+
			"begin 644 file.txt"+CRLF+
			"92&5L;&\\@=V]R;&0*"+CRLF+
			"end"+CRLF)

	// CTE header present on a multipart container (illegal: RFC 2045 §6.4
	// forbids anything but 7bit/8bit/binary on composite types).
	emitS(cat, "011-cte-base64-on-multipart.eml",
		hdr("cte-on-multipart", "base64 CTE on a multipart container")+
			"Content-Type: multipart/mixed; boundary=\"=_c_=\""+CRLF+
			"Content-Transfer-Encoding: base64"+CRLF+CRLF+
			"--=_c_="+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"the container claims to be base64 encoded"+CRLF+
			"--=_c_=--"+CRLF)

	// Duplicate, disagreeing CTE headers.
	emitS(cat, "012-duplicate-cte-disagree.eml",
		hdr("cte-dup", "Two disagreeing CTE headers")+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+
			"Content-Transfer-Encoding: base64"+CRLF+
			"Content-Transfer-Encoding: 7bit"+CRLF+CRLF+
			b64([]byte("which encoding header wins?"))+CRLF)

	// base64 with extremely long lines (no folding at 76 chars) and one with
	// a single character per line.
	{
		payload := strings.Repeat("Moov corpus payload. ", 400)
		enc := b64([]byte(payload))
		emitS(cat, "013-b64-unfolded-long-line.eml",
			hdr("cte-b64-long", "base64 as one very long line")+
				"Content-Type: text/plain; charset=us-ascii"+CRLF+
				"Content-Transfer-Encoding: base64"+CRLF+CRLF+
				enc+CRLF)

		var sb strings.Builder
		for _, r := range b64([]byte("one character per line")) {
			sb.WriteRune(r)
			sb.WriteString(CRLF)
		}
		emitS(cat, "014-b64-one-char-per-line.eml",
			hdr("cte-b64-narrow", "base64 folded to one character per line")+
				"Content-Type: text/plain; charset=us-ascii"+CRLF+
				"Content-Transfer-Encoding: base64"+CRLF+CRLF+
				sb.String())
	}
}
