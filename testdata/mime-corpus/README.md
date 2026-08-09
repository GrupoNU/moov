# Pathological MIME corpus

110 deliberately broken, extreme, or ambiguous email messages, plus a manifest
recording what is wrong with each one and what a correct parser should do with it.

This is not a scratch directory for a spike. It is the permanent regression suite
of the sync engine's MIME parser, and it exists **before** the parser does — by
design (`docs/research/04-sync-engine-prior-art.md` §4.2, risk R4).

## Why

The sync engine's operating rule is that **a message that fails to parse must never
break a folder's sync**. A message that cannot be parsed is marked
`parse_status='failed'`, its raw blob is kept, and the sync continues. Nylas had
"Incomplete sync" issues open indefinitely; a parser that can halt a folder is almost
certainly the mechanism.

You cannot honour that rule without knowing what actually breaks. This corpus is that
knowledge, in executable form.

## Layout

```
manifest.yaml          Machine-readable index: one entry per case.
                       Read the header comment first — it defines the schema,
                       the meaning of ok/partial/failed, and four corpus-wide
                       conventions (C1-C4) that individual entries rely on.
fetch-external.sh      Downloads third-party test messages at test time.
                       No sources enabled; see the script for why.
01-nesting/            Depth and width bombs (8)
02-boundaries/         Broken, duplicated, absent boundaries (10)
03-headers/            Oversized, malformed, unterminated headers (11)
04-encoded-words/      RFC 2047 pathology (12)
05-charsets/           Declared-vs-actual encoding lies (15)
06-cte/                Content-Transfer-Encoding lies (14)
07-structural/         Absurd but legal-ish structures, filename attacks (17)
08-line-endings/       CRLF/LF/CR chaos, NUL bytes (11)
09-real-world/         TNEF, S/MIME, DSN, calendar, mbox damage (12)
```

## Byte fidelity — two load-bearing details

`.eml` files are **byte-exact test vectors**. CRLF vs LF is semantically significant
in MIME: the case that tests "LF-only message" stops testing anything the moment git
normalizes its line endings.

Two repository settings keep this true, and **both** are required:

1. `.gitattributes` marks `*.eml` and `*.mbox` as `-text`, disabling all end-of-line
   conversion. This rule must stay **after** the `* text=auto` line — in
   `.gitattributes` the last matching pattern wins, so a trailing catch-all would
   silently re-enable normalization.
2. `.gitignore` ignores `*.eml` repo-wide (to catch stray spike downloads) and carries
   an explicit **negation** for this directory. Without it the entire corpus is
   invisible to git.

Both were verified by comparing every file's index blob against its working-tree
bytes. To re-verify after any change:

```bash
git ls-files --eol -- testdata/mime-corpus | head    # expect: attr/-text
```

## Regenerating

Every `.eml` here is emitted by a deterministic generator. The files are committed
(they are the stable vectors); the generator documents how each came to be and makes
the corpus extendable.

```bash
cd spikes/s4-mime/gen && go run . -out ../../../testdata/mime-corpus
```

Output must be byte-identical on every run. If regenerating produces a diff, either
the generator lost determinism or a file was hand-edited — investigate before
committing.

## Running the parsers over it

```bash
cd spikes/s4-mime && go run . -corpus ../../testdata/mime-corpus -timeout 10s
```

Findings from the first full run: `spikes/s4-mime/RESULTS.md`.

## Working on this corpus

- **Expectations are written before the parser runs.** They encode what a correct
  parser *should* do, not what any particular library *does*. When observed behavior
  disagrees, examine which is right and document it — do not quietly edit the
  expectation to match. At least one case (`04-encoded-words/004`) is a case where
  both candidate parsers are wrong and the expectation stands.
- **No real personal data, ever.** Addresses use `example.com` / `example.org`
  (reserved by RFC 2606); names are invented; binary payloads are synthetic stubs with
  correct magic bytes. If you adapt a real-world message, sanitize it first.
- **Adding a case** means: add it to the generator (or hand-author it), add a manifest
  entry with its pathology, provenance, license and expectation, and regenerate.
  A case without a manifest entry is not a test — it is an orphan file.
