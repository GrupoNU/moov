# go-imap patch set

Moov vendors `github.com/emersion/go-imap/v2` and carries three local patches
against it. This directory holds them, records where each one came from, and
documents the procedure for bumping the pin without losing them.

The rule this implements is L2 §2.2. The evidence behind every patch is spike
S2 (`spikes/s2-goimap/RESULTS.md`), which validated the library against the
production Mailcow/Dovecot 2.3.21.1 that Moov actually targets.

## The pin

```
github.com/emersion/go-imap/v2 v2.0.0-beta.8.0.20260702120225-f68ef419e622
```

That pseudo-version is the tip of the upstream `v2` branch at commit
`f68ef419e622a283e0cf8ddab4498b84f9bd038d`.

It is pinned **by commit**, not by tag or branch, for a reason that is easy to
rediscover the hard way: `go get github.com/emersion/go-imap/v2@v2` does not
resolve. Go rejects the query because a branch literally named `v2` collides
with the major-version path suffix (S2 H2):

```
go: github.com/emersion/go-imap/v2@v2: no matching versions for query "v2"
```

There is no released tag with the client-side extensions Moov needs, so the
branch tip is the only option and it must be named by commit.

## The patches

| # | Patch | Origin | Upstream status |
|---|---|---|---|
| 0001 | `0001-pr757-imapclient-qresync.patch` | Upstream [PR #757](https://github.com/emersion/go-imap/pull/757) | **Open**, not merged |
| 0002 | `0002-notify-encoder-rfc5465.patch` | Written by Moov | **Not yet submitted** — ready to submit |
| 0003 | `0003-expose-condstore-modified.patch` | Written by Moov | **Not yet submitted** — ready to submit |

They are ordered and must be applied in order: 0001 touches `imapclient/enable.go`
and `imapclient/fetch.go`, and 0003 touches `imapclient/fetch.go` again. Applying
0003 first still works today, but the order is the one that is tested.

### 0001 — QRESYNC client support (upstream PR #757)

**What it fixes.** Stock go-imap refuses to enable QRESYNC at all. The
`Enable()` allowlist in `imapclient/enable.go` accepts only `IMAP4rev2`,
`UTF8=ACCEPT`, `METADATA` and `METADATA-SERVER`, so `Enable(CapQResync)` fails
client-side before a single byte reaches the server (S2 T2e). Without QRESYNC
there is no `SELECT (QRESYNC …)` and no `VANISHED`, which means no incremental
resync — the core of the Moov sync engine.

**What it adds.** `FetchOptions.Vanished`, `SelectOptions.QResync`,
`QResyncOptions`, `SeqMatchData`, `SelectData.VanishedUIDs`,
`UnilateralDataHandler.Vanished`, `imapclient/vanished.go`, and `CapQResync` +
`CapCondStore` in the `Enable()` allowlist. 324 insertions, 206 of them tests.

**Provenance.** Fetched verbatim from
`https://patch-diff.githubusercontent.com/raw/emersion/go-imap/pull/757.diff`.
Not modified by us. S2 T3 applied it to this exact pin and exercised it against
our Dovecot: `Enable(QRESYNC)` accepted, `SELECT (QRESYNC …)` replaying the
delta, `SelectData.VanishedUIDs` correct, and the `Vanished` handler firing for
`UID FETCH … VANISHED`.

**Risk.** This is an unmerged PR carrying a core code path. If upstream merges
it in a different shape, patch 0001 disappears and the code in `internal/imap`
adapts to the merged API. If upstream rejects it, Moov keeps carrying it.
Either way the encapsulation rule (ADR-001, `internal/imap/doc.go`) is what
keeps the blast radius to one package.

### 0002 — NOTIFY encoder, two RFC 5465 violations (ours)

**What it fixes.** `encodeNotifyOptions` in `imapclient/notify.go` emits two
forms that Dovecot rejects outright with
`BAD Error in IMAP command NOTIFY: Invalid arguments` (S2 T2d):

1. `Status: true` emitted `NOTIFY SET (STATUS) …`. RFC 5465 §6 gives
   `notify-set = "SET" [SP "STATUS"] 1*(SP event-group)` — STATUS is a bare
   atom, not a parenthesised list. Correct form: `NOTIFY SET STATUS (PERSONAL …)`.
2. An explicit `Mailboxes` list emitted `NOTIFY SET ((INBOX) …)`, dropping the
   mandatory `MAILBOXES` keyword of `filter-mailboxes-other`. Correct form:
   `NOTIFY SET (MAILBOXES (INBOX "Other") …)`.

The patch also corrects `notify_encode_test.go`, which asserted the wrong bytes
for four cases. That is why the bugs shipped: the encoder was merged with a
test suite that agreed with it and a server that does not.

**Why it is a correctness fix, not an ergonomic one.** Without the STATUS
keyword, a flag change in a non-selected folder produces **no notification at
all**. Toggling `\Flagged` moves neither `MESSAGES` nor `UNSEEN`, so with no
`HIGHESTMODSEQ` in the STATUS response there is nothing for the server to
report. S2 T4 measured it directly: `HIGHESTMODSEQ` present 3/3 with the
keyword, 0/2 without, and the flag-change event missing entirely in the second
case. Shipping on the stock encoder means another client marking a message read
is invisible to Moov until an unrelated event happens to reveal it.

**Verified wire forms.** Both patched outputs are byte-identical to the
commands S2 confirmed by hand against Dovecot 2.3.21.1:

```
NOTIFY SET STATUS (PERSONAL (MessageNew MessageExpunge FlagChange))    -> OK
NOTIFY SET (MAILBOXES (INBOX "S2/folder1") (MessageNew))               -> OK
```

**Upstream readiness.** Ready to submit as-is. It is a self-contained fix in one
function plus its test, it cites the RFC grammar, and it comes with empirical
evidence from a real server. The one thing a maintainer may push back on is that
it changes the observable output of a public API — but the current output is
rejected by Dovecot, so no working client can depend on it.

### 0003 — Expose the CONDSTORE `[MODIFIED]` response code (ours)

**What it fixes.** A conditional `STORE` (RFC 7162 `UNCHANGEDSINCE`) that the
server refuses does **not** fail. It completes with a tagged `OK`, sends no
`FETCH` for the refused messages, and names them only in the `[MODIFIED <set>]`
response code. `imapclient` parses that code into nothing: it falls through to
the `default` branch that discards unknown codes. The caller sees `err == nil`
and zero FETCH responses — indistinguishable from complete success (S2 T2b).

For a mail sync engine that is a data-corruption bug, not an inconvenience: the
optimistic-concurrency write silently no-ops, Moov records the flag as applied,
and its state diverges from Dovecot's. It only happens under concurrent
modification, so ordinary testing does not surface it.

**What it adds.** Three small pieces:

- `imap.ResponseCodeModified` in `response.go`, alongside the other codes.
- An unexported `modified imap.NumSet` field on `imapclient.FetchCommand`
  (`Store` returns a `*FetchCommand`), with an exported `Modified()` accessor.
- A `case "MODIFIED"` in `readResponseTagged`, which parses the set with the
  numbering kind of the command that produced it — a `UIDSet` for `UID STORE`,
  a `SeqSet` for `STORE`.

**Upstream readiness.** Ready to submit. It follows the existing pattern in
`readResponseTagged` exactly (`APPENDUID` and `COPYUID` do the same thing), adds
no exported type, and is ~30 lines. The design question a maintainer may raise
is where the accessor belongs — `FetchCommand.Modified()` is slightly odd for a
STORE result, but `Store` returning `*FetchCommand` is upstream's own choice, so
any alternative would be a larger API change than the fix warrants. Worth
offering both options in the PR description.

**Note for `internal/imap`.** The read-back verification in `store.go` is *not*
removed by this patch. Both paths stay: `[MODIFIED]` is used when present, and
the read-back covers a server that omits the code or a vendor tree that lost the
patch. `StoreResult.VerifiedByReadBack` reports which one answered, so the
fallback can be retired later on measurements rather than on assumption.

## Applying them

`go mod vendor` regenerates `vendor/` from the pristine module cache, so **every
vendor regeneration silently reverts the whole patch set**. That is the failure
this directory is built around.

```sh
make vendor-patches      # go mod vendor + re-apply every patch
sh patches/apply.sh      # re-apply only (idempotent)
sh patches/apply.sh --check   # report status, change nothing
```

`apply.sh` is idempotent: it reverse-checks each patch first and skips the ones
already applied, so running it twice is harmless.

**The alarm.** `TestVendoredPatchSetIsApplied` in `internal/imap` fails if the
vendored tree is missing any of the three. It does not grep for patch text — it
checks the *behaviour* each patch exists to provide:

- 0001: `imap.SelectOptions` has a `QResync` field and `CapQResync` is in the
  `Enable` allowlist.
- 0002: the encoder emits `SET STATUS (…)` and `SET (MAILBOXES (…) …)`, by
  round-tripping through the real encoder and comparing bytes.
- 0003: `FetchCommand.Modified()` exists and `imap.ResponseCodeModified` is
  defined.

A patch that applies but no longer does its job therefore still fails the test.

## Bumping the pin

Do not bump casually. The patch set is the maintenance cost of being on a
pre-release library, and S2's conclusion was that go-imap/v2 is
under-tested against real servers — the NOTIFY encoder shipped with unit tests
asserting bytes that Dovecot rejects.

1. **Check what landed upstream.** If PR #757 merged, drop `0001` and adapt
   `internal/imap` to the merged API. If our 0002/0003 were submitted and
   merged, drop them the same way. Deleting a patch that upstream absorbed is
   the goal; carrying a duplicate is how conflicts start.

2. **Update the pin and re-vendor.**

   ```sh
   go mod edit -require=github.com/emersion/go-imap/v2@<new pseudo-version>
   go mod tidy
   make vendor-patches
   ```

3. **Fix whatever no longer applies.** `apply.sh` stops with the offending
   patch named. Regenerate it against the new tip rather than hand-editing
   `vendor/`:

   ```sh
   git clone --branch v2 https://github.com/emersion/go-imap.git /tmp/goimap
   cd /tmp/goimap && git checkout <new commit>
   # apply the old patch, resolve, then:
   git diff -- <touched files> > /path/to/moov/patches/000N-....patch
   ```

4. **Re-run the unit gate.** `go test ./internal/imap/...` — the patch-set test
   and the wire-level encoder assertions run without a server and catch a patch
   that applied but stopped working.

5. **Re-run the integration suite against a real Dovecot.** This is the step
   that is not optional, because it is the one that found every bug in this
   directory. See `internal/imap/integration_test.go` for the environment
   variables; the S2 scenarios are Go tests there now.

   ```sh
   MOOV_IMAP_TEST_HOST=dovecot MOOV_IMAP_TEST_USER=… MOOV_IMAP_TEST_PASSWORD=… \
     go test ./internal/imap/ -run Integration -v
   ```

6. **Update this file** — the pin, and the upstream status of anything that
   moved.
