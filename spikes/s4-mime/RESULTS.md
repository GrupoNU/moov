# Spike S4 — Pathological MIME corpus and dual-parser validation

**Status:** complete · **Date:** 2026-08-09 · **Corpus:** 110 cases
**Parsers:** `github.com/emersion/go-message v0.18.2` · `github.com/jhillyerd/enmime/v2 v2.4.1`
**Runtime:** `golang:1.25-bookworm`, container capped at `--cpus=2 --memory=2g`, no network.
**Watchdog:** 10 s per parse.

Validates risk **R4** and the operating rule from
`docs/research/04-sync-engine-prior-art.md` §4.2: *a message that fails to parse must
never break a folder's sync.*

---

## 1. Headline numbers

| Outcome | Cases | Meaning for the engine |
|---|---:|---|
| Neither parser hard-fails | **97** | Normal path. |
| Only go-message hard-fails | **9** | enmime rescues. Fallback earns its place. |
| Only enmime hard-fails | **1** | go-message rescues. Fallback works both ways. |
| **Both hard-fail** | **3** | `parse_status='failed'` + raw blob. |
| Panics | **0** | No DoS vector found in either parser. |
| Hangs / timeouts | **0** | No case approached the 10 s watchdog. |

Per-parser outcome distribution over the 110 cases:

| Parser | ok | defects (recovered) | hard error |
|---|---:|---:|---:|
| go-message | 87 | 11 | 12 |
| enmime | 92 | 14 | 4 |

**The dual-parser strategy is validated.** 10 of 110 cases (9.1%) are parseable by
exactly one of the two, and the failures are asymmetric in both directions. Neither
parser is a superset of the other, so neither alone is sufficient. Running only
go-message would hard-fail 12 cases; adding enmime as fallback reduces that to 3.

**No panics and no hangs is the single most reassuring result.** The two failure
modes that would genuinely threaten sync availability — a crash that kills the
worker, and a hang that holds a folder open forever — did not appear anywhere in
110 deliberately hostile inputs. R4's worst case does not materialize with these
libraries; the residual risk is data quality, not availability.

---

## 2. The three cases where both parsers hard-fail

These define the `parse_status='failed'` path. All three share one root cause: the
message is missing the structural information needed to split it at all.

| Case | go-message | enmime |
|---|---|---|
| `02-boundaries/010-empty-boundary-parameter.eml` | `multipart: boundary is empty` | `unable to locate boundary param` |
| `07-structural/004-multipart-no-boundary-param.eml` | `multipart: boundary is empty` | `unable to locate boundary param` |
| `03-headers/009-leading-continuation-line.eml` | `malformed MIME header initial line` | `malformed MIME header initial line` |

The first two are `multipart/*` declared with no usable boundary — genuinely
unsplittable, and refusing is correct. The manifest expects `partial` for these
(the body text is human-readable even if the structure is not recoverable), so
**the engine should still salvage the raw body as a single text part rather than
showing the user nothing.** That is engine work; neither library does it.

The third is a message whose very first line is a fold continuation. Both parsers
reject the entire header block. Note enmime's error text shows it concatenated the
orphan line with the following `From:` header — it damaged the header block while
failing, which is worth remembering if partial header data is ever trusted from a
failed parse.

**Implication:** 3 of 110 (2.7%) require the raw-blob path. That path is not
optional, and this is the concrete evidence for building it in phase 1 rather than
deferring it.

---

## 3. Where exactly one parser fails — the dual-parser evidence

### go-message hard-fails, enmime recovers (9 cases)

| Case | go-message error | enmime result |
|---|---|---|
| `02-boundaries/003-child-reuses-parent-boundary.eml` | `NextPart: EOF` | ok, 2 parts |
| `02-boundaries/005-boundary-never-appears.eml` | `NextPart: EOF` | ok |
| `02-boundaries/007-unquoted-boundary-with-space.eml` | `boundary is empty` | ok |
| `03-headers/005-header-missing-colon.eml` | `malformed MIME header line` | recovered, defect flagged |
| `03-headers/008-whitespace-only-separator.eml` | `malformed MIME header line` | recovered |
| `06-cte/010-unknown-cte-value.eml` | `unhandled encoding "x-uuencode"` | recovered, defect flagged |
| `07-structural/015-empty-parts.eml` | `malformed MIME header line: --=_e_=` | ok |
| `08-line-endings/007-nul-bytes-in-headers.eml` | `malformed MIME header key` | recovered, **severe** defect |
| `09-real-world/009-mbox-from-line-leak.eml` | `malformed MIME header key: From MAILER-DAEMON…` | recovered |

Two clusters, both of them realistic rather than exotic:

- **Header strictness.** go-message delegates to `net/textproto`, which rejects the
  whole header block on any malformed line. A single bad header costs the entire
  message. `09-real-world/009` is the one to worry about: an mbox `From ` envelope
  line leaking into a message is an ordinary artifact of mailbox handling, not an
  attack, and it takes go-message down.
- **Boundary edge cases.** go-message returns a bare `EOF` from `NextPart()` when a
  multipart is unterminated or its boundary never appears. Both are extremely common
  in truncated mail.

### enmime hard-fails, go-message recovers (1 case)

| Case | enmime error | go-message result |
|---|---|---|
| `05-charsets/013-charset-parameter-junk.eml` | `Failed to ReadParts: mime: duplicate parameter name` | ok, 1 part, text recovered |

A `Content-Type` carrying the `charset` parameter twice makes enmime reject the
message outright, via Go's own `mime.ParseMediaType`. Duplicate parameters are not
rare in mail generated by broken templating. This single case is what makes the
fallback bidirectional rather than a simple "enmime is more lenient" story — a
one-directional cascade (go-message, then enmime) would still lose this message.

**Recommended cascade:** go-message → enmime → raw blob. Both directions must be
tried before declaring failure.

---

## 4. Silent wrong data — the findings that matter most

A parser that fails loudly is safe; the engine catches it. These are the cases where
a parser reports success and returns something wrong or incomplete. They are ranked
above the hard failures in importance because nothing downstream will catch them.

### 4.1 Unpadded base64 encoded-word: both parsers surface raw markup to the user

`04-encoded-words/004-b64-bad-padding.eml` — **both parsers fail identically**, and
both report a clean parse.

- Expected subject: `Reunión mensual`
- Both parsers return: `=?UTF-8?B?UmV1bmnDs24gbWVuc3VhbA?=`

The payload is 22 characters, so `len % 4 == 2`. It is unambiguously decodable —
Go's own `base64.RawStdEncoding` decodes it to `Reunión mensual` correctly (verified
independently). Both libraries decline to decode and fall back to emitting the raw
encoded-word, with **no error and no defect flag**. The user sees MIME markup in
their subject line, and nothing in the parse output indicates a problem.

The manifest's expectation was written before the run and is **correct**; both
parsers are wrong here. This is not an expectation to relax.

> **Engine action:** after RFC 2047 decoding, detect any residual `=?…?=` pattern in
> a decoded header and retry with `RawStdEncoding`/`RawURLEncoding`. Cheap, and it
> converts a visible user-facing defect into a correct subject.

### 4.2 Lying CTE: partial decodes are real data, and `io.ReadAll` hides them

On `06-cte/001` (declared base64, actually plain text) and `06-cte/003` (base64 with
interleaved garbage), go-message's decoder returns **partial bytes alongside its
error**. The first version of this harness discarded them on error and recorded zero
text — which looked like total data loss by the parser but was a **bug in the
caller, not the parser**.

This is a trap the sync engine will walk into. `io.ReadAll` returns `(data, err)`
with `data` populated up to the failure point; the idiomatic `if err != nil { return }`
throws away recoverable content. On `06-cte/003` that is the difference between
`"payload with"` and nothing.

> **Engine action:** in the parse path, never discard the byte slice on a body read
> error. Keep what was decoded, mark the part as partially decoded, and continue.

enmime handles these better on both cases (91 B and 48 B recovered vs 18 B and 12 B),
because it skips invalid base64 characters and records each as a non-severe defect
rather than aborting the stream.

### 4.3 Charset lies that decode cleanly (the undetectable class)

As predicted in the manifest (convention C3), the cases where a legacy charset is
declared over bytes of a different encoding produce **clean parses with wrong text
and no defect flag from either parser**:

- `05-charsets/002` — UTF-8 bytes declared windows-1252 → `El seÃ±or dijo â€œholaâ€`
- `05-charsets/008` — KOI8-R bytes declared ISO-8859-1 → `ðÒÉ×ÅÔ` instead of `Привет`
- `05-charsets/003` — double-encoded mojibake, decodes without complaint

This is **conforming behavior**, not a parser defect: ISO-8859-1 and windows-1252 map
every possible byte, so there is nothing for a decoder to detect. No amount of error
handling reaches this class.

> **Engine action:** this is the concrete justification for the heuristic cascade in
> research §4.2 (`saintfish/chardet` + `charset_guessed` flag). "No parse error" must
> never be treated as "text is correct". Confirmed: the control cases `05-charsets/005`
> (GB18030), `007` (KOI8-R) and `009` (windows-1256) all decode correctly when
> declared honestly, so the decoder tables are present and wired up — the gap is
> detection, not coverage.

### 4.4 Neither parser descends into `message/rfc822`

`01-nesting/005` and `006` (recursive embedded messages, 5 and 20 deep) both report
depth 1 and a single leaf in **both** parsers: the embedded message is treated as an
opaque leaf, not descended into.

Not a bug — it matches manifest convention C1 — but it is load-bearing for the
product: **forwarded-message content will not be indexed for search unless the engine
recursively re-parses `message/rfc822` parts itself.** Users expect to find text
inside forwarded mail. This is a feature requirement discovered by the corpus, and it
is also good news for the nesting bombs: recursive embedding is not an amplification
vector because neither parser follows it.

---

## 5. Resource behavior on the bombs

No case came close to the 10 s watchdog or the 2 GB ceiling.

| Case | go-message | enmime |
|---|---|---|
| `004-nested-multipart-500` | 38 ms / 3 MB | 458 ms / 10 MB |
| `007-thousand-sibling-parts` | 14 ms / 3 MB | 243 ms / 50 MB |
| `011-megabyte-single-line` | 7 ms / 6 MB | 46 ms / 12 MB |
| `002-ten-thousand-headers` | 15 ms / 4 MB | 16 ms / 2 MB |

Both parsers walk **500 levels of nesting without a depth limit of their own** and
without stack exhaustion. enmime is consistently slower and allocates more (it builds
the whole tree; go-message streams), most visibly at 50 MB for 1000 sibling parts —
roughly 50 KB per part for a corpus file of 84 KB. A message with 100,000 parts would
be an enmime memory problem.

> **Engine action:** neither library will stop a nesting bomb for you. The engine must
> impose its own depth cap (suggest 100), part-count cap (suggest 1000), and total-size
> cap, and treat exceeding them as `parse_status='failed'` rather than as something to
> parse harder. Manifest convention C4 already states a bounded refusal is correct
> behavior.

**Harness artifact, recorded for honesty:** the first run of this harness used a
depth limit of 100 and reported the 500-deep case as a go-message hard failure. That
was the harness's limit, not the parser's. It was corrected (limit raised to 2000,
above the deepest corpus case) and the run repeated; the headline numbers in §1 are
from the corrected run. Any future result attributable to a harness limit is tagged
`harness …` in `results.json` and must not be reported as a parser finding.

---

## 6. Where reality disagreed with the manifest

Per the spike's discipline rule, expectations were written before the run and are not
edited to match observed behavior. Every disagreement was examined:

| Case | Expected | Observed | Verdict |
|---|---|---|---|
| `04-encoded-words/004` | subject `Reunión mensual` | raw encoded-word, both parsers | **Manifest right, parsers wrong.** See §4.1. Expectation kept. |
| `03-headers/007-eof-in-headers` | 0 leaf parts | 1 leaf part, both parsers | **Manifest too strict.** A message truncated mid-header still has an implied empty body; reporting one empty leaf is defensible. Left unchanged as a documented divergence — it costs nothing and records the judgment. |
| `08-line-endings/002-cr-only` | 2 leaf parts | 1, both parsers | **Manifest optimistic.** Neither parser normalizes bare CR, so the message is one long line and the boundary never matches. The expectation described the *desired* engine behavior; both parsers decline. CR-only mail needs pre-normalization in the engine if it is to be supported at all. |
| `08-line-endings/003-mixed-crlf-lf` | 2 leaf parts | go-message 1, enmime 2 | **Real divergence.** enmime tolerates LF-only delimiter lines; go-message does not. Another point for the fallback. |

Note `08-line-endings/001-lf-only` (LF-only throughout — the normal on-disk form of
Unix mail) parses correctly in **both** parsers, 2 parts each. That was the case that
most needed to pass, and it does.

---

## 7. Full matrix

Legend: `ok` clean · `defects` recovered with reported defects · `error` hard failure.
`parts` is leaf-part count (`-` when the parse failed).

<!-- BEGIN MATRIX -->
| case | go-message | parts | enmime | parts | note |
|---|---|---|---|---|---|
| 01-nesting/001-nested-multipart-10.eml | ok | 1 | ok | 1 |  |
| 01-nesting/002-nested-multipart-50.eml | ok | 1 | ok | 1 |  |
| 01-nesting/003-nested-multipart-100.eml | ok | 1 | ok | 1 |  |
| 01-nesting/004-nested-multipart-500.eml | ok | 1 | ok | 1 |  |
| 01-nesting/005-rfc822-recursive-5.eml | ok | 1 | ok | 1 |  |
| 01-nesting/006-rfc822-recursive-20.eml | ok | 1 | ok | 1 |  |
| 01-nesting/007-thousand-sibling-parts.eml | ok | 1000 | ok | 1000 |  |
| 01-nesting/008-alternating-alt-related-40.eml | ok | 41 | ok | 41 |  |
| 02-boundaries/001-unterminated-boundary.eml | defects | 1 | defects | 1 |  |
| 02-boundaries/002-duplicate-close-delimiter.eml | ok | 1 | ok | 1 |  |
| 02-boundaries/003-child-reuses-parent-boundary.eml | error | - | ok | 2 | enmime rescues |
| 02-boundaries/004-boundary-trailing-garbage.eml | ok | 0 | ok | 1 |  |
| 02-boundaries/005-boundary-never-appears.eml | error | - | ok | 1 | enmime rescues |
| 02-boundaries/006-preamble-and-epilogue.eml | ok | 1 | ok | 1 |  |
| 02-boundaries/007-unquoted-boundary-with-space.eml | error | - | ok | 1 | enmime rescues |
| 02-boundaries/008-boundary-prefix-collision.eml | ok | 1 | ok | 1 |  |
| 02-boundaries/009-close-delimiter-missing-hyphens.eml | defects | 1 | defects | 1 |  |
| 02-boundaries/010-empty-boundary-parameter.eml | error | - | error | - | **both fail** |
| 03-headers/001-single-20kb-header.eml | ok | 1 | ok | 1 |  |
| 03-headers/002-ten-thousand-headers.eml | ok | 1 | ok | 1 |  |
| 03-headers/003-raw-8bit-header-bytes.eml | ok | 1 | ok | 1 |  |
| 03-headers/004-folded-mixed-tabs-spaces.eml | ok | 1 | ok | 1 |  |
| 03-headers/005-header-missing-colon.eml | error | - | defects | 1 | enmime rescues |
| 03-headers/006-duplicate-contenttype-disagree.eml | ok | 1 | ok | 1 |  |
| 03-headers/007-eof-in-headers.eml | ok | 1 | ok | 1 |  |
| 03-headers/008-whitespace-only-separator.eml | error | - | defects | 1 | enmime rescues |
| 03-headers/009-leading-continuation-line.eml | error | - | error | - | **both fail** |
| 03-headers/010-malformed-header-names.eml | ok | 1 | defects | 1 | enmime severe |
| 03-headers/011-no-headers-at-all.eml | ok | 1 | ok | 1 |  |
| 04-encoded-words/001-split-multibyte-b64.eml | ok | 1 | ok | 1 |  |
| 04-encoded-words/002-split-multibyte-q.eml | ok | 1 | ok | 1 |  |
| 04-encoded-words/003-wrong-charset-declared.eml | ok | 1 | ok | 1 |  |
| 04-encoded-words/004-b64-bad-padding.eml | ok | 1 | ok | 1 |  |
| 04-encoded-words/005-oversized-encoded-word.eml | ok | 1 | ok | 1 |  |
| 04-encoded-words/006-adjacent-words-whitespace.eml | ok | 1 | ok | 1 |  |
| 04-encoded-words/007-glued-to-plain-text.eml | ok | 1 | ok | 1 |  |
| 04-encoded-words/008-encoded-word-in-address.eml | ok | 1 | ok | 1 |  |
| 04-encoded-words/009-unknown-charset-and-encoding.eml | defects | 1 | ok | 1 |  |
| 04-encoded-words/010-unterminated-encoded-word.eml | ok | 1 | ok | 1 |  |
| 04-encoded-words/011-q-encoding-edges.eml | ok | 1 | ok | 1 |  |
| 04-encoded-words/012-encoded-word-folded-midword.eml | ok | 1 | ok | 1 |  |
| 05-charsets/001-declared-utf8-actually-cp1252.eml | ok | 1 | ok | 1 |  |
| 05-charsets/002-declared-cp1252-actually-utf8.eml | ok | 1 | ok | 1 |  |
| 05-charsets/003-double-encoded-mojibake.eml | ok | 1 | ok | 1 |  |
| 05-charsets/004-iso2022jp-truncated-escape.eml | ok | 1 | ok | 1 |  |
| 05-charsets/005-gb18030-correct.eml | ok | 1 | ok | 1 |  |
| 05-charsets/006-gb18030-declared-utf8.eml | ok | 1 | ok | 1 |  |
| 05-charsets/007-koi8r-correct.eml | ok | 1 | ok | 1 |  |
| 05-charsets/008-koi8r-declared-latin1.eml | ok | 1 | ok | 1 |  |
| 05-charsets/009-windows1256-correct.eml | ok | 1 | ok | 1 |  |
| 05-charsets/010-charset-x-user-defined.eml | ok | 1 | ok | 1 |  |
| 05-charsets/011-charset-unknown-8bit.eml | defects | 1 | defects | 1 |  |
| 05-charsets/012-charset-empty-string.eml | defects | 1 | ok | 1 |  |
| 05-charsets/013-charset-parameter-junk.eml | ok | 1 | error | - | go-message rescues |
| 05-charsets/014-utf8-bom-and-invalid-sequences.eml | ok | 1 | ok | 1 |  |
| 05-charsets/015-per-part-charsets.eml | ok | 4 | ok | 4 |  |
| 06-cte/001-declared-b64-actual-text.eml | defects | 1 | defects | 1 |  |
| 06-cte/002-declared-qp-actual-b64.eml | ok | 1 | ok | 1 |  |
| 06-cte/003-b64-interleaved-garbage.eml | defects | 1 | defects | 1 |  |
| 06-cte/004-b64-missing-padding.eml | ok | 1 | ok | 1 |  |
| 06-cte/005-b64-excess-padding.eml | defects | 1 | ok | 1 |  |
| 06-cte/006-qp-bare-equals-eof.eml | ok | 1 | ok | 1 |  |
| 06-cte/007-qp-lowercase-hex.eml | ok | 1 | ok | 1 |  |
| 06-cte/008-qp-invalid-escapes.eml | ok | 1 | ok | 1 |  |
| 06-cte/009-binary-declared-7bit.eml | ok | 1 | ok | 1 |  |
| 06-cte/010-unknown-cte-value.eml | error | - | defects | 1 | enmime rescues |
| 06-cte/011-cte-base64-on-multipart.eml | ok | 1 | ok | 1 |  |
| 06-cte/012-duplicate-cte-disagree.eml | ok | 1 | ok | 1 |  |
| 06-cte/013-b64-unfolded-long-line.eml | ok | 1 | ok | 1 |  |
| 06-cte/014-b64-one-char-per-line.eml | ok | 1 | ok | 1 |  |
| 07-structural/001-headers-only-no-body.eml | ok | 1 | ok | 1 |  |
| 07-structural/002-empty-file.eml | ok | 1 | ok | 1 |  |
| 07-structural/003-blank-line-only-body.eml | ok | 1 | ok | 1 |  |
| 07-structural/004-multipart-no-boundary-param.eml | error | - | error | - | **both fail** |
| 07-structural/005-text-plain-with-boundary.eml | ok | 1 | ok | 1 |  |
| 07-structural/006-attachment-only-no-text.eml | ok | 1 | ok | 1 |  |
| 07-structural/007-disposition-contradictions.eml | ok | 3 | defects | 3 |  |
| 07-structural/008-filename-name-vs-filename.eml | ok | 1 | ok | 1 |  |
| 07-structural/009-rfc2231-continuation-correct.eml | ok | 1 | ok | 1 |  |
| 07-structural/010-rfc2231-wrong-charset.eml | ok | 1 | ok | 1 |  |
| 07-structural/011-rfc2231-gap-and-disorder.eml | ok | 1 | ok | 1 |  |
| 07-structural/012-rfc2231-and-plain-disagree.eml | ok | 1 | ok | 1 |  |
| 07-structural/013-filename-traversal-and-controls.eml | ok | 1 | ok | 1 |  |
| 07-structural/014-alternative-inverted-order.eml | ok | 2 | ok | 2 |  |
| 07-structural/015-empty-parts.eml | error | - | defects | 3 | enmime rescues |
| 07-structural/016-rfc822-part-not-a-message.eml | ok | 1 | ok | 1 |  |
| 07-structural/017-mime-structure-without-mime-version.eml | ok | 1 | ok | 1 |  |
| 08-line-endings/001-lf-only.eml | ok | 2 | ok | 2 |  |
| 08-line-endings/002-cr-only.eml | ok | 1 | ok | 1 |  |
| 08-line-endings/003-mixed-crlf-lf.eml | defects | 1 | ok | 2 |  |
| 08-line-endings/004-crlf-headers-lf-body.eml | ok | 2 | ok | 2 |  |
| 08-line-endings/005-lf-delimiters-crlf-body.eml | ok | 2 | ok | 2 |  |
| 08-line-endings/006-b64-misaligned-crlf.eml | ok | 1 | ok | 1 |  |
| 08-line-endings/007-nul-bytes-in-headers.eml | error | - | defects | 1 | enmime rescues |
| 08-line-endings/008-nul-bytes-in-body.eml | ok | 1 | ok | 1 |  |
| 08-line-endings/009-no-trailing-newline.eml | ok | 2 | ok | 2 |  |
| 08-line-endings/010-bare-cr-in-body.eml | ok | 1 | ok | 1 |  |
| 08-line-endings/011-megabyte-single-line.eml | ok | 1 | ok | 1 |  |
| 09-real-world/001-tnef-winmail-dat.eml | ok | 2 | ok | 2 |  |
| 09-real-world/002-smime-mangled-signature.eml | defects | 2 | defects | 2 |  |
| 09-real-world/003-pgpmime-missing-control-part.eml | ok | 1 | ok | 1 |  |
| 09-real-world/004-dsn-delivery-status.eml | ok | 3 | ok | 3 |  |
| 09-real-world/005-calendar-broken-folding.eml | ok | 2 | ok | 2 |  |
| 09-real-world/006-html-meta-charset-conflict.eml | ok | 1 | defects | 1 |  |
| 09-real-world/007-format-flowed-edges.eml | ok | 1 | ok | 1 |  |
| 09-real-world/008-lying-content-length.eml | ok | 1 | ok | 1 |  |
| 09-real-world/009-mbox-from-line-leak.eml | error | - | ok | 1 | enmime rescues |
| 09-real-world/010-long-received-and-references.eml | ok | 1 | ok | 1 |  |
| 09-real-world/011-apple-inline-image-in-alternative.eml | ok | 3 | ok | 3 |  |
| 09-real-world/012-report-missing-type-empty-status.eml | ok | 2 | ok | 2 |  |
<!-- END MATRIX -->

---

## 8. Conclusions and actions

1. **Adopt the dual-parser cascade: go-message → enmime → raw blob.** Validated by
   10 divergent cases in both directions. Neither library subsumes the other.
2. **Build the raw-blob path in phase 1.** 3 of 110 cases require it; it is not a
   long-tail nicety.
3. **Never discard partial body reads on error** (§4.2). This is the highest-value
   one-line lesson in the spike, and the harness itself got it wrong first.
4. **Retry unpadded base64 in encoded-words** (§4.1) — both parsers leak raw MIME
   markup into subject lines without flagging it.
5. **Impose engine-level depth/part/size caps** (§5). Neither parser has any.
6. **Recursively re-parse `message/rfc822`** if forwarded content is to be searchable
   (§4.4).
7. **Treat "no parse error" as no guarantee of correct text** (§4.3) — the charset
   heuristic cascade is required, not optional.

### Open risks

- **The corpus is synthetic.** Every case is handcrafted or generated, which makes it
  reproducible but means it encodes *our* model of what breaks. Real mailboxes will
  contain shapes nobody predicted. The Crash migration (89 accounts, 584 GB) is the
  obvious source of real-world adversarial input; a follow-up that runs both parsers
  over real messages **read-only** and records outcome distributions would test this
  corpus's coverage rather than the parsers.
- **No external classic test messages are included.** Mark Crispin's MIME torture
  test and similar public material would add independently-authored cases, but its
  licensing was not established. `fetch-external.sh` is the placeholder for adding it
  under `external: true` once licensing is confirmed; nothing was committed with
  unclear provenance.
- **Attachment-count expectations are parse-layer only** (manifest convention C2).
  The `cid:`-referenced-inline-vs-attachment policy is a presentation decision that
  this spike deliberately did not settle.
- **Version pinning.** Findings are specific to go-message v0.18.2 and enmime v2.4.1.
  enmime v2 requires Go ≥ 1.25. The corpus is the regression suite that will catch
  behavior changes on upgrade — wire it into CI alongside the JMAP TestSuite.

## 9. Reproducing

```bash
# Regenerate the corpus (deterministic — output must be byte-identical)
cd spikes/s4-mime/gen && go run . -out ../../../testdata/mime-corpus

# Run both parsers over it
cd spikes/s4-mime && go run . -corpus ../../testdata/mime-corpus -timeout 10s
```

Run under a memory cap when testing untrusted corpora:

```bash
docker run --rm --cpus=2 --memory=2g -v "$PWD:/w" -w /w golang:1.25-bookworm \
  go run . -corpus /w/testdata/mime-corpus
```

Raw per-case data, including error strings and defect lists: `results.json`.
