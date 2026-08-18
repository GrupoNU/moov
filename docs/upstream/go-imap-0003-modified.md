# Upstream contribution — patch 0003: expose the CONDSTORE `[MODIFIED]` response code

> **Status: PREPARED, NOT SUBMITTED.** This document is the handoff for the
> actual submission. The director reviews it before anything is sent upstream.

| Field | Value |
|---|---|
| Target repository | [`emersion/go-imap`](https://github.com/emersion/go-imap) |
| Target branch | `v2` |
| Base commit | `f68ef419e622a283e0cf8ddab4498b84f9bd038d` |
| Local patch | `patches/0003-expose-condstore-modified.patch` |
| Files touched | `response.go`, `imapclient/client.go`, `imapclient/fetch.go` |
| Evidence | `spikes/s2-goimap/RESULTS.md` — T2b |
| Rebase needed | **None** — see [Applying against current upstream](#applying-against-current-upstream) |

---

## 1. Problem statement

A conditional `STORE` — RFC 7162's `UNCHANGEDSINCE`, the mechanism for
optimistic concurrency on flags — that the server **refuses** is currently
indistinguishable from one that succeeded.

RFC 7162 §3.1.3 specifies the behaviour. When the modification sequence of one
or more messages has advanced past the client's `UNCHANGEDSINCE` value, the
server:

- does **not** apply the flag change to those messages;
- does **not** send an untagged `FETCH` for them;
- completes the command with a **tagged `OK`**, not `NO` or `BAD`;
- names the refused messages in the `[MODIFIED <set>]` response code attached to
  that tagged `OK`.

`imapclient` parses the response code into nothing. `readResponseTagged` has no
`case "MODIFIED"`, so it falls through to the `default` branch that discards
unrecognised codes. The caller therefore observes:

- `err == nil` — because the command really did complete with `OK`;
- zero `FETCH` responses — because the server correctly sent none.

Which is **exactly** what a fully successful conditional store looks like when
the flags were already in the requested state. The information that
distinguishes the two — the `MODIFIED` set — is parsed and thrown away.

### Why this matters

For any client doing optimistic concurrency, this is a silent data-corruption
bug rather than an inconvenience. The write is a no-op, the client records the
flag as applied, and its state diverges from the server's with no error at any
layer. It only occurs under concurrent modification, which is precisely the
condition the feature exists to handle and the one ordinary testing does not
reproduce.

The only workaround available today is to re-read every conditionally stored
message's flags and modseq and compare — an extra round trip per write, to
recover information the server already sent and the library already parsed.

---

## 2. Minimal reproduction against a real Dovecot

Server: Dovecot 2.3.21.1 (`d492236fa0`), which advertises `CONDSTORE`. Full
transcript in `spikes/s2-goimap/RESULTS.md` §T2b.

Setup: select with CONDSTORE, note `HIGHESTMODSEQ`, then have a **second
connection** modify UID 8 so its modseq advances past the noted value.

```go
// Connection 1
sel, _ := c.Select("INBOX", &imap.SelectOptions{CondStore: true}).Wait()
baseline := sel.HighestModSeq            // 19

// Connection 2 (elsewhere): adds \Flagged to UID 8, advancing its modseq to 20.

// Connection 1: a conditional store that the server MUST refuse.
cmd := c.Store(imap.UIDSetNum(8), &imap.StoreFlags{
    Op:    imap.StoreFlagsAdd,
    Flags: []imap.Flag{imap.FlagAnswered},
}, &imap.StoreOptions{UnchangedSince: baseline})

msgs, err := cmd.Collect()
// err  == nil          <- the command completed with a tagged OK
// msgs == []            <- no FETCH, because nothing was updated
// ...and no way to learn that UID 8 was refused.
```

Observed:

```
STORE (UNCHANGEDSINCE 19) -> err=<nil>, 0 FETCH response(s)
post-conflict state: UID=8 ModSeq=20 Flags=[\Flagged]
```

`\Answered` was never applied — the server behaved correctly and refused the
write — yet the library reported success. The `[MODIFIED 8]` response code that
says so was discarded.

With the patch, the same code reads:

```go
if mod := cmd.Modified(); mod != nil {
    // UID 8 was refused; re-read and retry.
}
```

---

## 3. Patch rationale

Three small pieces, each following an existing pattern in the file it touches:

1. **`imap.ResponseCodeModified`** in `response.go`, alongside the other
   response codes, under a `// CONDSTORE` heading matching the existing
   `// METADATA` grouping.

2. **An unexported `modified imap.NumSet` field on `imapclient.FetchCommand`**
   (`fetch.go`), with an exported `Modified()` accessor. `FetchCommand` is what
   `Client.Store` returns, so this is where a STORE's results already live.
   The field is written by the decoder goroutine before the command completes
   and read after `Close`/`Wait`/`Collect`, so it needs no lock — the same
   discipline as the surrounding fields.

3. **A `case "MODIFIED"` in `readResponseTagged`** (`client.go`), which parses
   the set with the numbering kind of the command that produced it: a `UIDSet`
   for `UID STORE`, a `SeqSet` for `STORE`. This mirrors the existing
   `APPENDUID` and `COPYUID` cases exactly, including their `cmd.(*…Command)`
   type check and the `DiscardUntilByte(']')` fallback when the code arrives on
   an unexpected command.

Roughly 30 lines. No new exported type; one new exported constant and one new
exported method.

### The design question worth surfacing

`FetchCommand.Modified()` is slightly odd as the home for a **STORE** result.
The honest framing is that it follows from upstream's own choice to have `Store`
return `*FetchCommand`, so any alternative — a distinct `StoreCommand`, or
returning the set from `Store` — is a larger API change than this fix warrants.
The PR body offers the alternatives explicitly rather than presenting the chosen
placement as the only option.

---

## 4. Proposed PR

### Title

```
imapclient: expose the CONDSTORE MODIFIED response code
```

### Body

```markdown
A conditional STORE (RFC 7162 `UNCHANGEDSINCE`) that the server refuses is
currently indistinguishable from one that succeeded.

Per RFC 7162 section 3.1.3, when a message's modseq has advanced past the
client's UNCHANGEDSINCE value the server does not apply the change, sends no
untagged FETCH for it, completes the command with a **tagged OK**, and names
the refused messages in `[MODIFIED <set>]`.

`readResponseTagged` has no case for MODIFIED, so the code falls through to the
default branch that discards unknown codes. The caller sees `err == nil` and
zero FETCH responses — exactly what a successful conditional store looks like
when the flags were already in the requested state.

For a client doing optimistic concurrency this is a silent correctness bug: the
write no-ops, the client records the flag as applied, and its state diverges
from the server's with no error anywhere. It only happens under concurrent
modification, so it does not show up in ordinary testing.

Reproduced against Dovecot 2.3.21.1 — a second connection advances UID 8's
modseq, then a STORE with the stale UNCHANGEDSINCE:

    STORE (UNCHANGEDSINCE 19) -> err=<nil>, 0 FETCH response(s)
    post-conflict state: UID=8 ModSeq=20 Flags=[\Flagged]

`\Answered` was never applied. The server was right; the library reported
success anyway.

### The change

- `imap.ResponseCodeModified` in `response.go`, alongside the other codes.
- An unexported `modified imap.NumSet` on `FetchCommand` (which is what `Store`
  returns) plus an exported `Modified()` accessor. It is written by the decoder
  goroutine before the command completes and read after Close/Wait/Collect, so
  no lock is needed.
- A `case "MODIFIED"` in `readResponseTagged`, parsing the set with the
  numbering kind of the command that produced it — a UIDSet for UID STORE, a
  SeqSet for STORE. This follows the existing APPENDUID/COPYUID cases, including
  the type check and the `DiscardUntilByte(']')` fallback.

Callers can then write:

    if mod := cmd.Modified(); mod != nil {
        // these messages were refused; re-read and retry
    }

Without it, the only way to detect a refused conditional write is to re-read
every stored message's flags and modseq — an extra round trip per write, to
recover something the server already sent and the library already parsed.

### One open question on placement

`FetchCommand.Modified()` is a slightly odd home for a STORE result. It follows
from `Store` returning `*FetchCommand`, so the alternatives — a separate
`StoreCommand` type, or returning the set directly from `Store` — are larger API
changes than this fix seemed to warrant. Happy to reshape it whichever way you
prefer; the parsing side is the same either way.

Signed-off-by: <name> <email>
```

> **Note before sending.** Upstream commits carry a `Signed-off-by` trailer
> (DCO). Fill in the real name and address of whoever submits; do not send the
> placeholder.

### Suggested addition before submitting

The patch as carried has **no test** — it was written to unblock Moov, whose own
coverage lives in `internal/imap`. A maintainer will reasonably want one, and it
does not need a live server: `imapclient` has a client–server test harness
(`newClientServerPair`), and the decoding path can be exercised by a server stub
that answers a conditional `STORE` with

```
A001 OK [MODIFIED 8] Conditional STORE failed
```

then asserting `cmd.Modified()` contains UID 8 and that `Collect` returns no
messages and no error. Writing that test is the one piece of work outstanding
before this PR is sent; it is small, and it is what makes the difference between
a fix the maintainer has to take on trust and one the suite protects.

---

## 5. Applying against current upstream

Checked on 2026-08-18.

- Branch `v2` HEAD is **`f68ef419e622a283e0cf8ddab4498b84f9bd038d`** — identical
  to the pin in `patches/README.md`. **No drift, no rebase.**
- `imapclient/fetch.go` last changed in `3fda9fc` (2026-04-12, *"imapclient:
  wrap inner errors"*), which predates the pin; that commit rewrote `%v` to `%w`
  in error paths and does not touch `FetchCommand`'s fields or the region this
  patch edits.
- `imapclient/client.go` last changed in `f68ef41` itself — the COPYUID
  teardown fix. It touches `readRespCodeCopyUID`, i.e. **the same function
  family this patch extends**, so it is the one file worth re-checking if HEAD
  moves. At the pinned commit the patch applies cleanly.
- No existing issue in the tracker reports the missing `MODIFIED` handling.

### Interaction with patch 0001

Patch 0001 (upstream [PR #757](https://github.com/emersion/go-imap/pull/757),
QRESYNC client support) also touches `imapclient/fetch.go`. In the Moov tree the
patches are applied in order for that reason. **The upstream PR for 0003 should
be opened against a clean `v2`, independent of #757** — the two changes edit
different regions of `fetch.go` (0001 adds `FetchOptions.Vanished`, 0003 adds a
field to `FetchCommand` and a method below it) and neither depends on the other.
If #757 merges first, re-check the hunk offsets in `fetch.go`; the content does
not conflict.

Re-verify with:

```sh
git clone --branch v2 https://github.com/emersion/go-imap.git /tmp/goimap
cd /tmp/goimap
git rev-parse HEAD    # expect f68ef419e622a283e0cf8ddab4498b84f9bd038d
git apply --check /path/to/moov/patches/0003-expose-condstore-modified.patch
```

---

## 6. What Moov keeps regardless of the outcome

`internal/imap/store.go` retains its read-back verification of conditional
writes. Both paths stay live: `[MODIFIED]` is used when the server sends it, and
the read-back covers a server that omits the code or a vendored tree that lost
the patch. `StoreResult.VerifiedByReadBack` reports which one answered, so the
fallback can be retired later on measurements rather than on assumption.

This matters for the submission in one way: Moov is not blocked on the PR being
accepted, so there is no reason to press the maintainer for a decision or to
accept a worse API shape to get it merged quickly.
