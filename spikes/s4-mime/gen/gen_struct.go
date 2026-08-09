package main

import (
	"bytes"
	"strings"
)

// ---------------------------------------------------------------------------
// 7. Structural absurdities
// ---------------------------------------------------------------------------

func genStructural() {
	const cat = "07-structural"

	// Headers only: no blank line, no body, but the header block is complete
	// and well formed. (Distinct from 03-headers/007, which is cut mid-header.)
	emitS(cat, "001-headers-only-no-body.eml",
		hdr("st-nobody", "Headers only, no body")+
			"Content-Type: text/plain; charset=us-ascii"+CRLF)

	// Truly empty file: zero bytes.
	emit(cat, "002-empty-file.eml", []byte{})

	// Header block correctly terminated, body is exactly one blank line.
	emitS(cat, "003-blank-line-only-body.eml",
		hdr("st-blank", "Body is a single blank line")+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+CRLF)

	// multipart with no boundary parameter at all. Nothing can be split;
	// the whole body is undelimited.
	emitS(cat, "004-multipart-no-boundary-param.eml",
		hdr("st-noboundary", "multipart without a boundary parameter")+
			"Content-Type: multipart/mixed"+CRLF+CRLF+
			"There is no boundary parameter, so there is nothing to split on."+CRLF)

	// text/plain carrying a boundary parameter — meaningless, but a parser
	// that keys off the presence of "boundary=" rather than the top-level
	// type will try to split a leaf part.
	emitS(cat, "005-text-plain-with-boundary.eml",
		hdr("st-textboundary", "text/plain with a boundary parameter")+
			"Content-Type: text/plain; charset=us-ascii; boundary=\"=_t_=\""+CRLF+CRLF+
			"--=_t_="+CRLF+
			"this looks like a delimiter but the type is not multipart"+CRLF+
			"--=_t_=--"+CRLF)

	// A message that is nothing but an attachment: no text part anywhere.
	// The list view still needs a preview snippet for this.
	emitS(cat, "006-attachment-only-no-text.eml",
		hdr("st-attachonly", "Attachment only, no text part")+
			"Content-Type: application/pdf; name=\"informe.pdf\""+CRLF+
			"Content-Disposition: attachment; filename=\"informe.pdf\""+CRLF+
			"Content-Transfer-Encoding: base64"+CRLF+CRLF+
			b64([]byte("%PDF-1.4 fake pdf bytes for corpus purposes"))+CRLF)

	// Contradictory disposition: Content-Disposition says attachment but the
	// part has a Content-ID and is referenced by the sibling HTML (so it is
	// really inline). And the reverse: disposition inline on an application/*
	// part with a filename.
	{
		b := "=_disp_="
		var sb strings.Builder
		sb.WriteString(hdr("st-disp", "Contradictory dispositions"))
		sb.WriteString("Content-Type: multipart/related; boundary=\"" + b + "\"" + CRLF + CRLF)
		sb.WriteString("--" + b + CRLF)
		sb.WriteString("Content-Type: text/html; charset=us-ascii" + CRLF + CRLF)
		sb.WriteString("<p>Logo: <img src=\"cid:logo@example.com\"></p>" + CRLF)
		sb.WriteString("--" + b + CRLF)
		sb.WriteString("Content-Type: image/png" + CRLF)
		sb.WriteString("Content-ID: <logo@example.com>" + CRLF)
		sb.WriteString("Content-Disposition: attachment; filename=\"logo.png\"" + CRLF)
		sb.WriteString("Content-Transfer-Encoding: base64" + CRLF + CRLF)
		sb.WriteString(b64([]byte("\x89PNG\r\n\x1a\n fake png")) + CRLF)
		sb.WriteString("--" + b + CRLF)
		sb.WriteString("Content-Type: application/zip; name=\"archivo.zip\"" + CRLF)
		sb.WriteString("Content-Disposition: inline; filename=\"archivo.zip\"" + CRLF)
		sb.WriteString("Content-Transfer-Encoding: base64" + CRLF + CRLF)
		sb.WriteString(b64([]byte("PK\x03\x04 fake zip")) + CRLF)
		sb.WriteString("--" + b + "--" + CRLF)
		emitS(cat, "007-disposition-contradictions.eml", sb.String())
	}

	// Filename disagreement between Content-Type name= and
	// Content-Disposition filename=. Which one the client shows determines
	// what the user believes they are opening — this is a security-relevant
	// choice, not cosmetic.
	emitS(cat, "008-filename-name-vs-filename.eml",
		hdr("st-fname", "name= and filename= disagree")+
			"Content-Type: application/octet-stream; name=\"factura.pdf\""+CRLF+
			"Content-Disposition: attachment; filename=\"factura.pdf.exe\""+CRLF+
			"Content-Transfer-Encoding: base64"+CRLF+CRLF+
			b64([]byte("MZ fake executable header"))+CRLF)

	// RFC 2231: parameter continuation + charset/language encoding, done
	// correctly. filename*0*, filename*1*, filename*2 — UTF-8 Spanish name.
	emitS(cat, "009-rfc2231-continuation-correct.eml",
		hdr("st-2231-ok", "RFC 2231 continuation, correct")+
			"Content-Type: application/pdf"+CRLF+
			"Content-Disposition: attachment;"+CRLF+
			" filename*0*=UTF-8''Informe%20de%20gesti;"+CRLF+
			" filename*1*=%C3%B3n%20a%C3%B1o%202025;"+CRLF+
			" filename*2*=.pdf"+CRLF+
			"Content-Transfer-Encoding: base64"+CRLF+CRLF+
			b64([]byte("%PDF-1.4 fake"))+CRLF)

	// RFC 2231 with a lying charset: the percent-escaped bytes are UTF-8 but
	// the parameter declares ISO-8859-1.
	emitS(cat, "010-rfc2231-wrong-charset.eml",
		hdr("st-2231-lie", "RFC 2231 with wrong charset label")+
			"Content-Type: application/pdf"+CRLF+
			"Content-Disposition: attachment;"+CRLF+
			" filename*=ISO-8859-1''Informe%20gesti%C3%B3n.pdf"+CRLF+
			"Content-Transfer-Encoding: base64"+CRLF+CRLF+
			b64([]byte("%PDF-1.4 fake"))+CRLF)

	// RFC 2231 continuation with a gap (0, then 2 — index 1 missing) and
	// out-of-order segments.
	emitS(cat, "011-rfc2231-gap-and-disorder.eml",
		hdr("st-2231-gap", "RFC 2231 continuation with a gap")+
			"Content-Type: application/pdf"+CRLF+
			"Content-Disposition: attachment;"+CRLF+
			" filename*2=\"-final.pdf\";"+CRLF+
			" filename*0=\"primero\""+CRLF+
			"Content-Transfer-Encoding: base64"+CRLF+CRLF+
			b64([]byte("%PDF-1.4 fake"))+CRLF)

	// Both an RFC 2231 encoded filename* and a plain filename=, disagreeing.
	// RFC 2231 says the encoded form wins; some clients take the plain one.
	emitS(cat, "012-rfc2231-and-plain-disagree.eml",
		hdr("st-2231-both", "filename* and filename= disagree")+
			"Content-Type: application/octet-stream"+CRLF+
			"Content-Disposition: attachment;"+CRLF+
			" filename=\"inocuo.txt\";"+CRLF+
			" filename*=UTF-8''peligroso.exe"+CRLF+
			"Content-Transfer-Encoding: base64"+CRLF+CRLF+
			b64([]byte("MZ fake"))+CRLF)

	// Filename with path traversal and control characters — must never be
	// used as a filesystem path verbatim.
	emitS(cat, "013-filename-traversal-and-controls.eml",
		hdr("st-fname-evil", "Filename with traversal and control chars")+
			"Content-Type: application/octet-stream"+CRLF+
			"Content-Disposition: attachment; filename=\"../../../etc/passwd\""+CRLF+
			"Content-Transfer-Encoding: base64"+CRLF+CRLF+
			b64([]byte("not really passwd"))+CRLF)

	// multipart/alternative whose parts are in the wrong order (HTML before
	// plain). RFC 2046: last part is the most faithful representation, so a
	// naive "take the last part" picks the plain text here.
	emitS(cat, "014-alternative-inverted-order.eml",
		hdr("st-altorder", "multipart/alternative in inverted order")+
			"Content-Type: multipart/alternative; boundary=\"=_ao_=\""+CRLF+CRLF+
			"--=_ao_="+CRLF+
			"Content-Type: text/html; charset=us-ascii"+CRLF+CRLF+
			"<p>The <b>rich</b> version, placed first.</p>"+CRLF+
			"--=_ao_="+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"The plain version, placed last."+CRLF+
			"--=_ao_=--"+CRLF)

	// An empty part (zero bytes between two delimiters) and a part with
	// headers but no body.
	emitS(cat, "015-empty-parts.eml",
		hdr("st-emptyparts", "Empty parts inside a multipart")+
			"Content-Type: multipart/mixed; boundary=\"=_e_=\""+CRLF+CRLF+
			"--=_e_="+CRLF+
			"--=_e_="+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"--=_e_="+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"the only part with actual content"+CRLF+
			"--=_e_=--"+CRLF)

	// message/rfc822 part whose payload is not a message at all.
	emitS(cat, "016-rfc822-part-not-a-message.eml",
		hdr("st-fakerfc822", "message/rfc822 containing non-message data")+
			"Content-Type: multipart/mixed; boundary=\"=_f_=\""+CRLF+CRLF+
			"--=_f_="+CRLF+
			"Content-Type: message/rfc822"+CRLF+CRLF+
			"\x00\x01\x02 this is binary garbage, not an RFC 5322 message \xff\xfe"+CRLF+
			"--=_f_=--"+CRLF)

	// No MIME-Version header, but a full MIME structure. Strictly the
	// Content-Type should be ignored; in practice everyone honours it.
	emitS(cat, "017-mime-structure-without-mime-version.eml",
		"From: Ada Lovelace <ada@example.com>"+CRLF+
			"To: Grace Hopper <grace@example.org>"+CRLF+
			"Subject: MIME structure with no MIME-Version"+CRLF+
			"Date: Mon, 06 Jan 2025 10:00:00 +0000"+CRLF+
			"Message-ID: <st-nomimever@corpus.example.com>"+CRLF+
			"Content-Type: multipart/mixed; boundary=\"=_nv_=\""+CRLF+CRLF+
			"--=_nv_="+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"body"+CRLF+
			"--=_nv_=--"+CRLF)
}

// ---------------------------------------------------------------------------
// 8. Line-ending chaos
// ---------------------------------------------------------------------------

// toLF rewrites every CRLF in s as a bare LF.
func toLF(s string) string { return strings.ReplaceAll(s, CRLF, "\n") }

// toCR rewrites every CRLF in s as a bare CR (classic Mac line endings).
func toCR(s string) string { return strings.ReplaceAll(s, CRLF, "\r") }

func genLineEndings() {
	const cat = "08-line-endings"

	base := hdr("le-base", "Line ending variants") +
		"Content-Type: multipart/mixed; boundary=\"=_le_=\"" + CRLF + CRLF +
		"--=_le_=" + CRLF +
		"Content-Type: text/plain; charset=us-ascii" + CRLF + CRLF +
		"first part body" + CRLF +
		"--=_le_=" + CRLF +
		"Content-Type: text/plain; charset=us-ascii" + CRLF + CRLF +
		"second part body" + CRLF +
		"--=_le_=--" + CRLF

	// LF only, throughout — the on-disk form of nearly every Unix mailbox.
	emitS(cat, "001-lf-only.eml", toLF(base))

	// CR only — classic Mac OS. Nothing in the message is a line at all to a
	// CRLF- or LF-oriented parser: it is one gigantic line.
	emitS(cat, "002-cr-only.eml", toCR(base))

	// Alternating CRLF and LF line by line.
	{
		lines := strings.Split(strings.TrimSuffix(base, CRLF), CRLF)
		var sb strings.Builder
		for i, l := range lines {
			sb.WriteString(l)
			if i%2 == 0 {
				sb.WriteString(CRLF)
			} else {
				sb.WriteString("\n")
			}
		}
		emitS(cat, "003-mixed-crlf-lf.eml", sb.String())
	}

	// Headers in CRLF, body in LF (the most common real-world mix: an MTA
	// normalizes headers, the original body survives untouched).
	{
		idx := strings.Index(base, CRLF+CRLF)
		emitS(cat, "004-crlf-headers-lf-body.eml",
			base[:idx+4]+toLF(base[idx+4:]))
	}

	// Boundary delimiter lines with LF while the rest is CRLF: the delimiter
	// match itself depends on the line ending.
	{
		s := strings.ReplaceAll(base, "--=_le_="+CRLF, "--=_le_=\n")
		s = strings.ReplaceAll(s, "--=_le_=--"+CRLF, "--=_le_=--\n")
		emitS(cat, "005-lf-delimiters-crlf-body.eml", s)
	}

	// CRLF injected into the middle of a base64 stream at positions that do
	// not align to 4-character groups. Legal per RFC 2045 (all whitespace is
	// ignored) but breaks naive chunked decoders.
	{
		enc := b64([]byte(strings.Repeat("misaligned base64 stream. ", 20)))
		var sb strings.Builder
		for i, r := range enc {
			sb.WriteRune(r)
			if i%7 == 6 { // deliberately not a multiple of 4
				sb.WriteString(CRLF)
			}
		}
		emitS(cat, "006-b64-misaligned-crlf.eml",
			hdr("le-b64", "base64 with CRLF at misaligned positions")+
				"Content-Type: text/plain; charset=us-ascii"+CRLF+
				"Content-Transfer-Encoding: base64"+CRLF+CRLF+
				sb.String()+CRLF)
	}

	// NUL bytes in headers.
	{
		var body bytes.Buffer
		body.WriteString("From: Ada Lovelace <ada@example.com>" + CRLF)
		body.WriteString("To: Grace Hopper <grace@example.org>" + CRLF)
		body.WriteString("Subject: NUL\x00byte\x00in\x00subject" + CRLF)
		body.WriteString("X-Nul-Header\x00: value" + CRLF)
		body.WriteString("Date: Mon, 06 Jan 2025 10:00:00 +0000" + CRLF)
		body.WriteString("Message-ID: <le-nul-hdr@corpus.example.com>" + CRLF)
		body.WriteString("MIME-Version: 1.0" + CRLF)
		body.WriteString("Content-Type: text/plain; charset=us-ascii" + CRLF + CRLF)
		body.WriteString("body" + CRLF)
		emit(cat, "007-nul-bytes-in-headers.eml", body.Bytes())
	}

	// NUL bytes in a text/plain body — PostgreSQL text columns cannot store
	// U+0000, so the sync engine must strip them before insert. This case is
	// as much a storage-layer test as a parser test.
	{
		var body bytes.Buffer
		body.WriteString(hdr("le-nul-body", "NUL bytes in a text body"))
		body.WriteString("Content-Type: text/plain; charset=utf-8" + CRLF + CRLF)
		body.WriteString("before\x00after\x00\x00end" + CRLF)
		emit(cat, "008-nul-bytes-in-body.eml", body.Bytes())
	}

	// A message with no trailing newline at all after the final delimiter.
	emitS(cat, "009-no-trailing-newline.eml",
		strings.TrimSuffix(base, CRLF))

	// Bare CR inside an otherwise-CRLF body (a lone \r not followed by \n).
	emitS(cat, "010-bare-cr-in-body.eml",
		hdr("le-barecr", "Bare CR inside the body")+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"line with a bare\rcarriage return in the middle"+CRLF+
			"normal line"+CRLF)

	// Very long body line with no line ending at all (1 MB single line).
	emitS(cat, "011-megabyte-single-line.eml",
		hdr("le-longline", "One megabyte body line with no break")+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			strings.Repeat("x", 1024*1024)+CRLF)
}

// ---------------------------------------------------------------------------
// 9. Real-world-shaped weirdness
// ---------------------------------------------------------------------------

func genRealWorld() {
	const cat = "09-real-world"

	// TNEF: Outlook's winmail.dat. The bytes below are a minimal, synthetic
	// TNEF stream — correct signature (0x223E9F78 little-endian) and key, then
	// truncated. Enough to exercise detection, not a real message.
	{
		var tnef bytes.Buffer
		tnef.Write([]byte{0x78, 0x9F, 0x3E, 0x22}) // TNEF signature
		tnef.Write([]byte{0x00, 0x00})             // attach key
		tnef.WriteString("synthetic tnef stream, truncated on purpose")
		emitS(cat, "001-tnef-winmail-dat.eml",
			hdr("rw-tnef", "TNEF winmail.dat attachment")+
				"Content-Type: multipart/mixed; boundary=\"=_tnef_=\""+CRLF+CRLF+
				"--=_tnef_="+CRLF+
				"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
				"This message was sent with Rich Text Format."+CRLF+
				"--=_tnef_="+CRLF+
				"Content-Type: application/ms-tnef; name=\"winmail.dat\""+CRLF+
				"Content-Disposition: attachment; filename=\"winmail.dat\""+CRLF+
				"Content-Transfer-Encoding: base64"+CRLF+CRLF+
				b64(tnef.Bytes())+CRLF+
				"--=_tnef_=--"+CRLF)
	}

	// S/MIME multipart/signed with a mangled signature part. The signature
	// must NOT be shown as an attachment to the user, and its corruption must
	// not prevent reading the signed content.
	emitS(cat, "002-smime-mangled-signature.eml",
		hdr("rw-smime", "multipart/signed with mangled signature")+
			"Content-Type: multipart/signed; protocol=\"application/pkcs7-signature\";"+CRLF+
			" micalg=sha-256; boundary=\"=_sig_=\""+CRLF+CRLF+
			"--=_sig_="+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"This is the signed content and it must remain readable."+CRLF+
			"--=_sig_="+CRLF+
			"Content-Type: application/pkcs7-signature; name=\"smime.p7s\""+CRLF+
			"Content-Disposition: attachment; filename=\"smime.p7s\""+CRLF+
			"Content-Transfer-Encoding: base64"+CRLF+CRLF+
			"MIIF!!!truncated&&&not-valid-base64-nor-valid-pkcs7"+CRLF+
			"--=_sig_=--"+CRLF)

	// PGP/MIME multipart/encrypted with a missing control part.
	emitS(cat, "003-pgpmime-missing-control-part.eml",
		hdr("rw-pgp", "multipart/encrypted missing its control part")+
			"Content-Type: multipart/encrypted; protocol=\"application/pgp-encrypted\";"+CRLF+
			" boundary=\"=_pgp_=\""+CRLF+CRLF+
			"--=_pgp_="+CRLF+
			"Content-Type: application/octet-stream"+CRLF+CRLF+
			"-----BEGIN PGP MESSAGE-----"+CRLF+CRLF+
			"hQEMA0000000000000synthetic"+CRLF+
			"-----END PGP MESSAGE-----"+CRLF+
			"--=_pgp_=--"+CRLF)

	// DSN: multipart/report; report-type=delivery-status, with the
	// message/delivery-status part and a truncated returned message.
	emitS(cat, "004-dsn-delivery-status.eml",
		"From: Mail Delivery System <MAILER-DAEMON@example.org>"+CRLF+
			"To: ada@example.com"+CRLF+
			"Subject: Undelivered Mail Returned to Sender"+CRLF+
			"Date: Mon, 06 Jan 2025 10:00:00 +0000"+CRLF+
			"Message-ID: <rw-dsn@corpus.example.com>"+CRLF+
			"MIME-Version: 1.0"+CRLF+
			"Content-Type: multipart/report; report-type=delivery-status;"+CRLF+
			" boundary=\"=_dsn_=\""+CRLF+CRLF+
			"--=_dsn_="+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"This is the mail system at host mx.example.org."+CRLF+CRLF+
			"<nosuchuser@example.org>: host mx.example.org said: 550 5.1.1 User unknown"+CRLF+
			"--=_dsn_="+CRLF+
			"Content-Type: message/delivery-status"+CRLF+CRLF+
			"Reporting-MTA: dns; mx.example.org"+CRLF+CRLF+
			"Final-Recipient: rfc822; nosuchuser@example.org"+CRLF+
			"Action: failed"+CRLF+
			"Status: 5.1.1"+CRLF+
			"--=_dsn_="+CRLF+
			"Content-Type: message/rfc822"+CRLF+CRLF+
			"From: ada@example.com"+CRLF+
			"To: nosuchuser@example.org"+CRLF+
			"Subject: the original message, truncated by the MTA"+CRLF+CRLF+
			"original bo"+CRLF+
			"--=_dsn_=--"+CRLF)

	// text/calendar invite with broken RFC 5545 folding: continuation lines
	// that lack the leading space, and a line folded mid-escape.
	emitS(cat, "005-calendar-broken-folding.eml",
		hdr("rw-ical", "Calendar invite with broken folding")+
			"Content-Type: multipart/alternative; boundary=\"=_cal_=\""+CRLF+CRLF+
			"--=_cal_="+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"Reunion de equipo"+CRLF+
			"--=_cal_="+CRLF+
			"Content-Type: text/calendar; charset=utf-8; method=REQUEST"+CRLF+CRLF+
			"BEGIN:VCALENDAR"+CRLF+
			"VERSION:2.0"+CRLF+
			"BEGIN:VEVENT"+CRLF+
			"UID:corpus-event-1@example.com"+CRLF+
			"DTSTART:20250106T100000Z"+CRLF+
			"SUMMARY:Reunion con un summary muy largo que se pliega mal"+CRLF+
			"porque esta linea no empieza con espacio"+CRLF+
			"DESCRIPTION:texto con escape partido \\"+CRLF+
			" n al doblar"+CRLF+
			"END:VEVENT"+CRLF+
			"END:VCALENDAR"+CRLF+
			"--=_cal_=--"+CRLF)

	// HTML-only mail whose <meta charset> contradicts the MIME header. The
	// MIME header must win (RFC 2046); browsers would prefer the meta tag.
	// Bytes here are UTF-8; MIME says ISO-8859-1; meta says UTF-8.
	emitS(cat, "006-html-meta-charset-conflict.eml",
		hdr("rw-meta", "HTML meta charset contradicts MIME header")+
			"Content-Type: text/html; charset=ISO-8859-1"+CRLF+
			"Content-Transfer-Encoding: 8bit"+CRLF+CRLF+
			"<html><head><meta charset=\"utf-8\"></head>"+CRLF+
			"<body><p>El se\xc3\xb1or con acci\xc3\xb3n.</p></body></html>"+CRLF)

	// format=flowed edge cases: trailing spaces that mean "flowed", a
	// space-stuffed line, DelSp=yes, and a quoted block.
	emitS(cat, "007-format-flowed-edges.eml",
		hdr("rw-flowed", "format=flowed edge cases")+
			"Content-Type: text/plain; charset=us-ascii; format=flowed; delsp=yes"+CRLF+
			"Content-Transfer-Encoding: 8bit"+CRLF+CRLF+
			"This line ends with a space and therefore flows "+CRLF+
			"into this one, which is a hard break."+CRLF+
			" space-stuffed line that must lose exactly one leading space"+CRLF+
			">quoted line that flows "+CRLF+
			">into this quoted continuation"+CRLF+
			"final hard-wrapped line."+CRLF)

	// Content-Length header that lies — an mbox-damage artifact. Some parsers
	// honour Content-Length and truncate or overrun the body.
	emitS(cat, "008-lying-content-length.eml",
		hdr("rw-clen", "Content-Length header that lies")+
			"Content-Length: 5"+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"This body is far longer than the five bytes the Content-Length header claims."+CRLF+
			"If a parser trusts Content-Length, everything after byte five is lost."+CRLF)

	// "From " line at the start (mbox envelope leaking into the message) and
	// an unescaped "From " at the start of a body line (mbox damage: the
	// classic >From mangling, here in its un-mangled, ambiguous form).
	emitS(cat, "009-mbox-from-line-leak.eml",
		"From MAILER-DAEMON Mon Jan  6 10:00:00 2025"+CRLF+
			hdr("rw-mbox", "mbox From_ line leaked into the message")+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"A body line follows that starts with the mbox separator token:"+CRLF+
			"From here on, an mbox splitter would think a new message started."+CRLF)

	// Long chain of Received headers and a References header with 200 IDs —
	// realistic list mail, and a stress test for header-size limits.
	{
		var sb strings.Builder
		sb.WriteString("From: Ada Lovelace <ada@example.com>" + CRLF)
		sb.WriteString("To: Lista <lista@example.org>" + CRLF)
		sb.WriteString("Subject: Re: long thread" + CRLF)
		sb.WriteString("Date: Mon, 06 Jan 2025 10:00:00 +0000" + CRLF)
		sb.WriteString("Message-ID: <rw-longrefs@corpus.example.com>" + CRLF)
		sb.WriteString("MIME-Version: 1.0" + CRLF)
		for i := 0; i < 40; i++ {
			sb.WriteString("Received: from hop" + itoa(i) + ".example.org (hop" + itoa(i) +
				".example.org [192.0.2." + itoa(i%254+1) + "])" + CRLF)
			sb.WriteString("\tby mx.example.org with ESMTPS id " + itoa(i) + CRLF)
			sb.WriteString("\tfor <lista@example.org>; Mon, 06 Jan 2025 10:00:00 +0000" + CRLF)
		}
		sb.WriteString("References:")
		for i := 0; i < 200; i++ {
			sb.WriteString(CRLF + "\t<thread-" + itoa(i) + "@example.org>")
		}
		sb.WriteString(CRLF)
		sb.WriteString("Content-Type: text/plain; charset=us-ascii" + CRLF + CRLF)
		sb.WriteString("Reply in a very long thread." + CRLF)
		emitS(cat, "010-long-received-and-references.eml", sb.String())
	}

	// Apple Mail style: multipart/mixed containing multipart/alternative with
	// an inline image between the text parts — the "attachment in the middle"
	// shape that breaks naive alternative selection.
	emitS(cat, "011-apple-inline-image-in-alternative.eml",
		hdr("rw-apple", "Inline image inside multipart/alternative")+
			"Content-Type: multipart/mixed; boundary=\"=_out_=\""+CRLF+CRLF+
			"--=_out_="+CRLF+
			"Content-Type: multipart/alternative; boundary=\"=_in_=\""+CRLF+CRLF+
			"--=_in_="+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"Text before the image."+CRLF+
			"--=_in_="+CRLF+
			"Content-Type: image/jpeg"+CRLF+
			"Content-Disposition: inline; filename=\"foto.jpg\""+CRLF+
			"Content-Transfer-Encoding: base64"+CRLF+CRLF+
			b64([]byte("\xff\xd8\xff\xe0 fake jpeg"))+CRLF+
			"--=_in_="+CRLF+
			"Content-Type: text/html; charset=us-ascii"+CRLF+CRLF+
			"<p>Text after the image.</p>"+CRLF+
			"--=_in_=--"+CRLF+
			"--=_out_=--"+CRLF)

	// Bounce shaped like a report but with a missing report-type parameter
	// and a message/delivery-status part that is empty.
	emitS(cat, "012-report-missing-type-empty-status.eml",
		hdr("rw-report", "multipart/report without report-type")+
			"Content-Type: multipart/report; boundary=\"=_rp_=\""+CRLF+CRLF+
			"--=_rp_="+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"Delivery failed."+CRLF+
			"--=_rp_="+CRLF+
			"Content-Type: message/delivery-status"+CRLF+CRLF+
			"--=_rp_=--"+CRLF)
}

// itoa avoids pulling strconv into every file's import block.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}
