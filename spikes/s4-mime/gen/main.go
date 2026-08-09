// Command gen deterministically emits the Moov pathological MIME corpus.
//
// Every .eml file under testdata/mime-corpus/ is produced by this program.
// The generated files ARE committed to the repository (they are the stable
// test vectors the parser regresses against); this generator exists so the
// corpus is reproducible, auditable, and extendable — you can see exactly
// which pathology each byte sequence encodes.
//
// Usage:
//
//	go run ./gen -out ../../testdata/mime-corpus
//
// Determinism rules for anything added here:
//   - No time.Now(), no math/rand without a fixed seed, no map iteration
//     order dependence. Running twice must produce byte-identical output.
//   - Write CRLF explicitly. Never rely on the host platform's line ending.
//   - No real personal data. Addresses are @example.com / @example.org and
//     names are invented (RFC 2606 reserves example.com for exactly this).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CRLF is the only correct line ending in an RFC 5322 message on the wire.
// Cases that deliberately violate this build their bytes by hand.
const CRLF = "\r\n"

// caseFile is one emitted corpus entry.
type caseFile struct {
	Category string // directory name, e.g. "01-nesting"
	Name     string // file name, e.g. "001-nested-10.eml"
	Body     []byte // exact bytes written to disk
}

var emitted []caseFile

// emit registers a case. Body is written verbatim — no normalization.
func emit(category, name string, body []byte) {
	emitted = append(emitted, caseFile{Category: category, Name: name, Body: body})
}

// emitS is emit for cases built as a string.
func emitS(category, name, body string) {
	emit(category, name, []byte(body))
}

// hdr builds a conventional header block prologue shared by most cases:
// From/To/Subject/Date/Message-ID/MIME-Version, CRLF-terminated, WITHOUT the
// blank line that ends the header section (callers add it, because several
// pathologies are precisely about that blank line).
//
// The Date is fixed (determinism) and Message-ID is derived from the case id.
func hdr(id, subject string) string {
	var b strings.Builder
	b.WriteString("From: Ada Lovelace <ada@example.com>" + CRLF)
	b.WriteString("To: Grace Hopper <grace@example.org>" + CRLF)
	b.WriteString("Subject: " + subject + CRLF)
	b.WriteString("Date: Mon, 06 Jan 2025 10:00:00 +0000" + CRLF)
	b.WriteString("Message-ID: <" + id + "@corpus.example.com>" + CRLF)
	b.WriteString("MIME-Version: 1.0" + CRLF)
	return b.String()
}

func main() {
	out := flag.String("out", "../../testdata/mime-corpus", "corpus output directory")
	flag.Parse()

	genNesting()
	genBoundaries()
	genHeaders()
	genEncodedWords()
	genCharsets()
	genCTE()
	genStructural()
	genLineEndings()
	genRealWorld()

	// Deterministic write order.
	sort.SliceStable(emitted, func(i, j int) bool {
		if emitted[i].Category != emitted[j].Category {
			return emitted[i].Category < emitted[j].Category
		}
		return emitted[i].Name < emitted[j].Name
	})

	seen := map[string]bool{}
	for _, c := range emitted {
		key := c.Category + "/" + c.Name
		if seen[key] {
			fmt.Fprintf(os.Stderr, "duplicate case: %s\n", key)
			os.Exit(1)
		}
		seen[key] = true

		dir := filepath.Join(*out, c.Category)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		p := filepath.Join(dir, c.Name)
		if err := os.WriteFile(p, c.Body, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	fmt.Printf("wrote %d cases to %s\n", len(emitted), *out)
}

// ---------------------------------------------------------------------------
// 1. Nesting bombs
// ---------------------------------------------------------------------------

// nestedMultipart builds a multipart/mixed nested `depth` levels deep, with a
// single text/plain leaf at the bottom. Boundaries are unique per level, so
// the structure itself is legal — the pathology is pure depth. A parser that
// recurses without a depth limit will blow its stack or its memory here.
func nestedMultipart(depth int, leaf string) string {
	body := leaf
	for i := depth; i >= 1; i-- {
		b := fmt.Sprintf("=_lvl%04d_=", i)
		var sb strings.Builder
		sb.WriteString("Content-Type: multipart/mixed; boundary=\"" + b + "\"" + CRLF)
		sb.WriteString(CRLF)
		sb.WriteString("--" + b + CRLF)
		sb.WriteString(body)
		if !strings.HasSuffix(body, CRLF) {
			sb.WriteString(CRLF)
		}
		sb.WriteString("--" + b + "--" + CRLF)
		body = sb.String()
	}
	return body
}

func genNesting() {
	const cat = "01-nesting"

	leaf := "Content-Type: text/plain; charset=us-ascii" + CRLF + CRLF + "bottom of the well" + CRLF

	for _, d := range []struct {
		depth int
		name  string
	}{
		{10, "001-nested-multipart-10.eml"},
		{50, "002-nested-multipart-50.eml"},
		{100, "003-nested-multipart-100.eml"},
		{500, "004-nested-multipart-500.eml"},
	} {
		s := hdr(fmt.Sprintf("nest-%d", d.depth), fmt.Sprintf("Nested multipart x%d", d.depth)) +
			nestedMultipart(d.depth, leaf)
		emitS(cat, d.name, s)
	}

	// message/rfc822 nested recursively: each level is a complete message
	// wrapped as an attachment of the level above. Parsers that eagerly
	// descend into embedded messages multiply their work at every level.
	rfc822Nest := func(depth int) string {
		inner := hdr("rfc822-leaf", "Innermost message") +
			"Content-Type: text/plain; charset=us-ascii" + CRLF + CRLF +
			"the innermost payload" + CRLF
		for i := depth; i >= 1; i-- {
			b := fmt.Sprintf("=_msg%03d_=", i)
			var sb strings.Builder
			sb.WriteString(hdr(fmt.Sprintf("rfc822-lvl-%d", i), fmt.Sprintf("Wrapper level %d", i)))
			sb.WriteString("Content-Type: multipart/mixed; boundary=\"" + b + "\"" + CRLF)
			sb.WriteString(CRLF)
			sb.WriteString("--" + b + CRLF)
			sb.WriteString("Content-Type: message/rfc822" + CRLF)
			sb.WriteString("Content-Disposition: attachment; filename=\"forwarded.eml\"" + CRLF)
			sb.WriteString(CRLF)
			sb.WriteString(inner)
			sb.WriteString("--" + b + "--" + CRLF)
			inner = sb.String()
		}
		return inner
	}
	emitS(cat, "005-rfc822-recursive-5.eml", rfc822Nest(5))
	emitS(cat, "006-rfc822-recursive-20.eml", rfc822Nest(20))

	// 1000 sibling parts: flat but wide. Tests per-part allocation, not depth.
	{
		b := "=_wide_="
		var sb strings.Builder
		sb.WriteString(hdr("wide-1000", "One thousand sibling parts"))
		sb.WriteString("Content-Type: multipart/mixed; boundary=\"" + b + "\"" + CRLF)
		sb.WriteString(CRLF)
		for i := 0; i < 1000; i++ {
			sb.WriteString("--" + b + CRLF)
			sb.WriteString("Content-Type: text/plain; charset=us-ascii" + CRLF)
			sb.WriteString(fmt.Sprintf("Content-Disposition: inline; filename=\"part-%04d.txt\"", i) + CRLF)
			sb.WriteString(CRLF)
			sb.WriteString(fmt.Sprintf("part number %d", i) + CRLF)
		}
		sb.WriteString("--" + b + "--" + CRLF)
		emitS(cat, "007-thousand-sibling-parts.eml", sb.String())
	}

	// Alternating multipart/alternative and multipart/related, 40 deep, each
	// level carrying a real text part. Shape closer to real broken mail than
	// the pure mixed nest: work is done at every level, not just the leaf.
	{
		body := "Content-Type: text/plain; charset=us-ascii" + CRLF + CRLF + "leaf" + CRLF
		for i := 40; i >= 1; i-- {
			sub := "alternative"
			if i%2 == 0 {
				sub = "related"
			}
			b := fmt.Sprintf("=_alt%03d_=", i)
			var sb strings.Builder
			sb.WriteString("Content-Type: multipart/" + sub + "; boundary=\"" + b + "\"" + CRLF)
			sb.WriteString(CRLF)
			sb.WriteString("--" + b + CRLF)
			sb.WriteString("Content-Type: text/plain; charset=us-ascii" + CRLF + CRLF)
			sb.WriteString(fmt.Sprintf("filler text at level %d", i) + CRLF)
			sb.WriteString("--" + b + CRLF)
			sb.WriteString(body)
			sb.WriteString("--" + b + "--" + CRLF)
			body = sb.String()
		}
		emitS(cat, "008-alternating-alt-related-40.eml",
			hdr("alt-related-40", "Alternating alternative/related x40")+body)
	}
}

// ---------------------------------------------------------------------------
// 2. Broken boundaries
// ---------------------------------------------------------------------------

func genBoundaries() {
	const cat = "02-boundaries"

	// Unterminated: opening delimiter present, closing "--b--" never arrives,
	// message just ends. Extremely common in truncated mail.
	emitS(cat, "001-unterminated-boundary.eml",
		hdr("bnd-unterm", "Unterminated boundary")+
			"Content-Type: multipart/mixed; boundary=\"=_u_=\""+CRLF+CRLF+
			"--=_u_="+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"this part never gets a closing delimiter"+CRLF)

	// Duplicated closing delimiter: the terminator appears twice. Content
	// after the first terminator is epilogue and must be ignored.
	emitS(cat, "002-duplicate-close-delimiter.eml",
		hdr("bnd-dupclose", "Duplicated close delimiter")+
			"Content-Type: multipart/mixed; boundary=\"=_d_=\""+CRLF+CRLF+
			"--=_d_="+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"first and only real part"+CRLF+
			"--=_d_=--"+CRLF+
			"--=_d_=--"+CRLF+
			"trailing epilogue after a second terminator"+CRLF)

	// Parent boundary reused by the child. Per RFC 2046 the child's boundary
	// must be distinct; reusing it means the parent's delimiter terminates
	// the child too. Parsers disagree wildly about the resulting tree.
	emitS(cat, "003-child-reuses-parent-boundary.eml",
		hdr("bnd-reuse", "Child reuses parent boundary")+
			"Content-Type: multipart/mixed; boundary=\"=_same_=\""+CRLF+CRLF+
			"--=_same_="+CRLF+
			"Content-Type: multipart/alternative; boundary=\"=_same_=\""+CRLF+CRLF+
			"--=_same_="+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"inner plain"+CRLF+
			"--=_same_=--"+CRLF+
			"--=_same_=--"+CRLF)

	// Trailing garbage after the delimiter. RFC 2046 allows only linear
	// whitespace after the boundary before CRLF; other bytes make it not a
	// delimiter at all — strictly, this part is body text of the preamble.
	emitS(cat, "004-boundary-trailing-garbage.eml",
		hdr("bnd-garbage", "Boundary with trailing garbage")+
			"Content-Type: multipart/mixed; boundary=\"=_g_=\""+CRLF+CRLF+
			"--=_g_= XXXX"+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"is this a part or is it preamble?"+CRLF+
			"--=_g_=--"+CRLF)

	// Boundary declared but never appears: whole body is preamble, zero parts.
	emitS(cat, "005-boundary-never-appears.eml",
		hdr("bnd-missing", "Boundary declared but absent")+
			"Content-Type: multipart/mixed; boundary=\"=_nowhere_=\""+CRLF+CRLF+
			"There is no boundary delimiter anywhere in this body."+CRLF+
			"A strict reading yields a multipart with zero parts."+CRLF)

	// Content before the first delimiter (preamble) and after the last
	// (epilogue). Both are legal and both must be dropped, not surfaced as
	// body text — a parser that shows the preamble leaks "This is a
	// multi-part message in MIME format." into the user's reading pane.
	emitS(cat, "006-preamble-and-epilogue.eml",
		hdr("bnd-pre-epi", "Preamble and epilogue")+
			"Content-Type: multipart/mixed; boundary=\"=_p_=\""+CRLF+CRLF+
			"This is a multi-part message in MIME format."+CRLF+
			"PREAMBLE-MARKER-SHOULD-NOT-APPEAR-AS-BODY"+CRLF+
			"--=_p_="+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"the actual body"+CRLF+
			"--=_p_=--"+CRLF+
			"EPILOGUE-MARKER-SHOULD-NOT-APPEAR-AS-BODY"+CRLF)

	// Boundary containing characters that need quoting, unquoted in the
	// header. "=_a b_=" has a space: unquoted, parsing stops at the space.
	emitS(cat, "007-unquoted-boundary-with-space.eml",
		hdr("bnd-space", "Unquoted boundary containing a space")+
			"Content-Type: multipart/mixed; boundary==_a b_="+CRLF+CRLF+
			"--=_a b_="+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"part under an unquoted boundary with a space"+CRLF+
			"--=_a b_=--"+CRLF)

	// Boundary that is a prefix of another boundary used in the same message.
	// "--=_x_=" also matches the start of "--=_x_=extra"; a naive
	// prefix-match splitter mis-slices the message.
	emitS(cat, "008-boundary-prefix-collision.eml",
		hdr("bnd-prefix", "Boundary is a prefix of the inner boundary")+
			"Content-Type: multipart/mixed; boundary=\"=_x_=\""+CRLF+CRLF+
			"--=_x_="+CRLF+
			"Content-Type: multipart/mixed; boundary=\"=_x_=inner\""+CRLF+CRLF+
			"--=_x_=inner"+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"inner part"+CRLF+
			"--=_x_=inner--"+CRLF+
			"--=_x_=--"+CRLF)

	// Delimiter line missing its leading two hyphens on the close.
	emitS(cat, "009-close-delimiter-missing-hyphens.eml",
		hdr("bnd-nohyphen", "Close delimiter without leading hyphens")+
			"Content-Type: multipart/mixed; boundary=\"=_h_=\""+CRLF+CRLF+
			"--=_h_="+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"body"+CRLF+
			"=_h_=--"+CRLF)

	// Empty boundary parameter: boundary="". Every line "--" is a delimiter.
	emitS(cat, "010-empty-boundary-parameter.eml",
		hdr("bnd-empty", "Empty boundary parameter")+
			"Content-Type: multipart/mixed; boundary=\"\""+CRLF+CRLF+
			"--"+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"body under an empty boundary"+CRLF+
			"----"+CRLF)
}

// ---------------------------------------------------------------------------
// 3. Header pathology
// ---------------------------------------------------------------------------

func genHeaders() {
	const cat = "03-headers"

	// A single 20 KB header line, unfolded. RFC 5322 caps lines at 998
	// octets; real mail (long References chains, DKIM, spam scanner verdicts)
	// violates this constantly. Parsers with a fixed line buffer truncate or
	// error here.
	{
		var sb strings.Builder
		sb.WriteString(hdr("hdr-20k", "Single 20KB header line"))
		sb.WriteString("X-Huge-Header: ")
		sb.WriteString(strings.Repeat("A", 20*1024))
		sb.WriteString(CRLF)
		sb.WriteString("Content-Type: text/plain; charset=us-ascii" + CRLF + CRLF)
		sb.WriteString("body after a 20KB header" + CRLF)
		emitS(cat, "001-single-20kb-header.eml", sb.String())
	}

	// 10,000 short headers. Memory pressure and O(n^2) header lookup.
	{
		var sb strings.Builder
		sb.WriteString(hdr("hdr-10k", "Ten thousand headers"))
		for i := 0; i < 10000; i++ {
			sb.WriteString(fmt.Sprintf("X-Trace-%05d: hop-%05d", i, i) + CRLF)
		}
		sb.WriteString("Content-Type: text/plain; charset=us-ascii" + CRLF + CRLF)
		sb.WriteString("body after 10000 headers" + CRLF)
		emitS(cat, "002-ten-thousand-headers.eml", sb.String())
	}

	// Raw 8-bit bytes in a header, no RFC 2047 encoding. Illegal but ubiquitous
	// (many MUAs just emit the local charset). Here: UTF-8 "Añoranza señor" and
	// a latin-1 "Añoranza" in a second header — same text, different bytes.
	{
		var sb strings.Builder
		sb.WriteString("From: Ada Lovelace <ada@example.com>" + CRLF)
		sb.WriteString("To: Grace Hopper <grace@example.org>" + CRLF)
		sb.WriteString("Subject: A\xc3\xb1oranza se\xc3\xb1or" + CRLF) // raw UTF-8
		sb.WriteString("X-Latin1-Subject: A\xf1oranza se\xf1or" + CRLF) // raw ISO-8859-1
		sb.WriteString("Date: Mon, 06 Jan 2025 10:00:00 +0000" + CRLF)
		sb.WriteString("Message-ID: <hdr-8bit@corpus.example.com>" + CRLF)
		sb.WriteString("MIME-Version: 1.0" + CRLF)
		sb.WriteString("Content-Type: text/plain; charset=utf-8" + CRLF + CRLF)
		sb.WriteString("body" + CRLF)
		emitS(cat, "003-raw-8bit-header-bytes.eml", sb.String())
	}

	// Folded headers with mixed tabs and spaces, including a fold with zero
	// leading whitespace on a line that "looks like" a continuation.
	emitS(cat, "004-folded-mixed-tabs-spaces.eml",
		"From: Ada Lovelace <ada@example.com>"+CRLF+
			"To: Grace Hopper"+CRLF+
			"\t<grace@example.org>,"+CRLF+
			"   Alan Turing <alan@example.org>,"+CRLF+
			"\t \t Katherine Johnson <katherine@example.org>"+CRLF+
			"Subject: folded"+CRLF+
			"\tsubject"+CRLF+
			"   across"+CRLF+
			"\t\tfour lines"+CRLF+
			"Date: Mon, 06 Jan 2025 10:00:00 +0000"+CRLF+
			"Message-ID: <hdr-fold@corpus.example.com>"+CRLF+
			"MIME-Version: 1.0"+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"body"+CRLF)

	// A header line with no colon at all, sitting in the middle of the block.
	emitS(cat, "005-header-missing-colon.eml",
		hdr("hdr-nocolon", "Header line without a colon")+
			"ThisLineHasNoColonAtAll"+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"does the parser drop the bad line, or abandon the header block?"+CRLF)

	// Two Content-Type headers that disagree. RFC 5322 says at most one;
	// which wins is a real security question (content sniffing confusion).
	emitS(cat, "006-duplicate-contenttype-disagree.eml",
		hdr("hdr-dupct", "Two disagreeing Content-Type headers")+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+
			"Content-Type: text/html; charset=utf-8"+CRLF+CRLF+
			"<b>Is this HTML or plain text?</b>"+CRLF)

	// Header section never terminated: EOF arrives while still in headers.
	emitS(cat, "007-eof-in-headers.eml",
		"From: Ada Lovelace <ada@example.com>"+CRLF+
			"To: Grace Hopper <grace@example.org>"+CRLF+
			"Subject: truncated in the header block"+CRLF+
			"Date: Mon, 06 Jan 2025 10:00:00 +0000"+CRLF+
			"Content-Type: text/pla")

	// Header block terminated by a whitespace-only line rather than a truly
	// empty one. Strictly that line is a fold continuation, so the headers
	// never end — but many MUAs treat it as the separator.
	emitS(cat, "008-whitespace-only-separator.eml",
		hdr("hdr-wssep", "Whitespace-only line as separator")+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+
			" "+CRLF+
			"is this line body, or a continuation of Content-Type?"+CRLF)

	// Continuation line as the very first line of the message (a fold with no
	// header to continue). Classic artifact of naive header stripping.
	emitS(cat, "009-leading-continuation-line.eml",
		"   orphaned continuation with no preceding header"+CRLF+
			hdr("hdr-orphan", "Leading continuation line")+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"body"+CRLF)

	// Header name containing a space before the colon ("Subject : x"), and a
	// header with an empty name (": value").
	emitS(cat, "010-malformed-header-names.eml",
		"From: Ada Lovelace <ada@example.com>"+CRLF+
			"Subject : space before colon"+CRLF+
			": empty header name"+CRLF+
			"X-Empty-Value:"+CRLF+
			"Date: Mon, 06 Jan 2025 10:00:00 +0000"+CRLF+
			"Message-ID: <hdr-malformed@corpus.example.com>"+CRLF+
			"MIME-Version: 1.0"+CRLF+
			"Content-Type: text/plain; charset=us-ascii"+CRLF+CRLF+
			"body"+CRLF)

	// No headers whatsoever: message starts with the blank line.
	emitS(cat, "011-no-headers-at-all.eml",
		CRLF+"just a body, no headers, not even From"+CRLF)
}
