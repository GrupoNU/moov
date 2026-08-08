# Spike S2 — go-imap/v2 vs. our real Dovecot: QRESYNC, CONDSTORE, IDLE, NOTIFY

**Date:** 2026-08-08
**Author:** implementation agent (raw results for director audit)
**Status:** all tests executed; verdicts below

Purpose: decide whether `go-imap/v2` is a viable base for the Moov sync engine,
by validating empirically — against the production-grade Mailcow/Dovecot we
actually target — the four IMAP extensions the engine depends on.

---

## 0. Environment

| Item | Value |
|---|---|
| Server | Mailcow container `mailcowdockerized-dovecot-mailcow-1` |
| Dovecot | **2.3.21.1 (d492236fa0)** |
| Reached as | `dovecot:143` (STARTTLS) from `mailcowdockerized_mailcow-network` |
| Test account | `moov-test@atmosfera.cloud` (dedicated spike mailbox, no real data) |
| Runner | `docker run --rm --network mailcowdockerized_mailcow-network … golang:1.24-bookworm` |
| Spike code on VPS | `/root/moov-s2` (left in place as reference) |
| PR #757 probe on VPS | `/root/moov-s2-pr757` (patched go-imap checkout, left in place) |
| Captured outputs | `/root/moov-s2-out/*.txt` |

**Library pin (resolved empirically).** The documented `@v2` query does **not**
work — Go rejects it because a branch named `v2` collides with the major-version
suffix (`go: github.com/emersion/go-imap/v2@v2: no matching versions for query "v2"`).
Branch `v2` tip is commit `f68ef419e622a283e0cf8ddab4498b84f9bd038d`, which
resolves to:

```
github.com/emersion/go-imap/v2 v2.0.0-beta.8.0.20260702120225-f68ef419e622
```

**TLS caveat.** All connections use `InsecureSkipVerify: true`. The
Mailcow-internal certificate is issued for the public hostname, not for the
`dovecot` network alias (spike S1, finding H2). This is a spike-only shortcut;
production code must verify properly.

### T0 — Server preconditions

```
$ doveconf mailbox_list_index
mailbox_list_index = yes          # required for efficient NOTIFY; confirmed ON
```

`doveconf -n | grep -iE 'notify|list_index'` returned only Sieve/replication
notify settings (`managesieve_notify_capability = mailto`, `sieve_extensions
= +notify …`, `replication-notify` listeners, `quota_notify.py`). **No IMAP
NOTIFY tuning is set**, i.e. defaults are in force.

Post-login CAPABILITY (verbatim, from the T1 transcript):

```
IMAP4rev1 SASL-IR LOGIN-REFERRALS ID ENABLE IDLE SORT SORT=DISPLAY
THREAD=REFERENCES THREAD=REFS THREAD=ORDEREDSUBJECT MULTIAPPEND URL-PARTIAL
CATENATE UNSELECT CHILDREN NAMESPACE UIDPLUS LIST-EXTENDED I18NLEVEL=1
CONDSTORE QRESYNC ESEARCH ESORT SEARCHRES WITHIN CONTEXT=SEARCH LIST-STATUS
BINARY MOVE SNIPPET=FUZZY PREVIEW=FUZZY PREVIEW STATUS=SIZE SAVEDATE LITERAL+
NOTIFY METADATA SPECIAL-USE COMPRESS=DEFLATE QUOTA ACL RIGHTS=texk
```

No `OBJECTID`, no `IMAP4rev2` — as expected.

---

## Verdict summary

| # | Test | Verdict | One-line result |
|---|---|---|---|
| T1 | `t1-qresync` (raw protocol) | **PASS** | Dovecot's QRESYNC is textbook-correct |
| T2a | `caps` | **PASS** | All four required extensions advertised |
| T2b | `condstore` | **PASS** (1 caveat) | Works; library hides the MODIFIED code |
| T2c | `idle` | **PASS** | Unilateral data delivered, ~0.5 s |
| T2d | `notify` | **PASS** (2 library bugs) | 1 connection watched 3 mailboxes |
| T2e | `qresync-lib` | **FINDING** | Library refuses to ENABLE QRESYNC |
| T4 | `notify-raw` (control) | **PASS** | Exonerates Dovecot; blames the library |
| T3 | PR #757 feasibility | **VALIDATED** | Patch applies clean and works end-to-end |

**Headline:** every limitation found is **client-side (go-imap)**. Our Dovecot
implemented every behaviour correctly, including the one the research flagged as
a suspected RFC 5465 violation.

---

## T1 — Raw-protocol QRESYNC validation: **PASS**

Independent of any Go library (hand-rolled IMAP client, `rawimap.go`) so that
the result describes **Dovecot**, not go-imap.

Sequence: `ENABLE QRESYNC` → `SELECT INBOX` → APPEND 3 → disconnect →
second connection flags UID 5 and expunges UID 6 → reconnect with
`SELECT INBOX (QRESYNC (uidvalidity modseq))`.

Key transcript (password redacted; full transcript in `/root/moov-s2-out/t1-qresync.txt`):

```
A C: A003 ENABLE QRESYNC
A S: * ENABLED QRESYNC
A S: A004 OK ... [UIDVALIDITY 1786153920] [HIGHESTMODSEQ 7]
   ... 3 APPENDs -> UIDs 5,6,7; HIGHESTMODSEQ 8 -> 9 -> 10
   (connection A closes: client is "offline" at modseq 10)

B C: B005 UID STORE 5 +FLAGS (\Flagged)
B S: * 5 FETCH (UID 5 MODSEQ (11) FLAGS (\Flagged))
B C: B007 UID EXPUNGE 6
B S: * VANISHED 6
B S: B007 OK [HIGHESTMODSEQ 13] Expunge completed

C C: C004 SELECT INBOX (QRESYNC (1786153920 10))
C S: * OK [HIGHESTMODSEQ 13] Highest
C S: * VANISHED (EARLIER) 6
C S: * 5 FETCH (UID 5 FLAGS (\Flagged) MODSEQ (11))
C S: C004 OK [READ-WRITE] Select completed
```

Every required behaviour is present and correct:

- `* ENABLED QRESYNC` confirmed.
- `HIGHESTMODSEQ` advances monotonically on every mutation (7→8→9→10→11→13).
- On reconnect, the server replays **exactly** the delta since the client's
  remembered modseq: `VANISHED (EARLIER) 6` for the expunge and one `FETCH`
  carrying the flag change **with** its `MODSEQ`. Unchanged messages are not
  re-sent.
- Step 7, `UID FETCH 1:* (FLAGS) (CHANGEDSINCE 10 VANISHED)` on an
  already-selected QRESYNC connection:

```
C S: * VANISHED (EARLIER) 6
C S: * 5 FETCH (UID 5 FLAGS (\Flagged) MODSEQ (11))
C S: * 6 FETCH (UID 7 FLAGS (\Seen) MODSEQ (14))
```

**Conclusion:** the incremental-resync foundation of the Moov sync engine is
sound on our server. Any QRESYNC problem from here on is a client problem.

---

## T2a — `caps`: **PASS**

As seen through the library after login:

```
supported: IMAP4rev1 CONDSTORE QRESYNC IDLE ESEARCH LIST-STATUS STATUS=SIZE
           SAVEDATE PREVIEW METADATA SPECIAL-USE UIDPLUS MOVE BINARY
           MULTIAPPEND NAMESPACE NOTIFY
MISSING:   IMAP4rev2 OBJECTID
```

All four extensions this spike depends on (CONDSTORE, QRESYNC, IDLE, NOTIFY) are
advertised. The absence of `OBJECTID` matters for design: **Moov cannot rely on
server-side immutable message IDs** and must derive its own stable identity
(UIDVALIDITY + UID, plus a content hash for move/copy detection).

---

## T2b — `condstore`: **PASS**, with one important caveat

```
SELECT (CONDSTORE): UIDVALIDITY=1786153920 UIDNEXT=8 NumMessages=4 HighestModSeq=17
seeded UIDs: A=8 B=9 ; baseline modseq after appends: 19
connection 2 added \Flagged to UID 8
FETCH (CHANGEDSINCE 19) returned 1 message(s)
  UID=8 ModSeq=20 Flags=[\Flagged]
PASS: ChangedSince correctly isolated the single modified message
```

- `SelectData.HighestModSeq` is populated. ✔
- `FetchOptions.ChangedSince` returns **only** the changed message, and
  `FetchMessageBuffer.ModSeq` is populated. ✔ This is the incremental-sync
  primitive; it works.

**Caveat — the library swallows the MODIFIED response code.** A `STORE` with a
deliberately stale `UnchangedSince`:

```
STORE (UNCHANGEDSINCE 19) -> err=<nil>, 0 FETCH response(s)
post-conflict state: UID=8 ModSeq=20 Flags=[\Flagged]
PASS: UNCHANGEDSINCE correctly rejected the conflicting update (\Answered absent)
```

The server correctly refused the write (`\Answered` was never applied), but the
library reported **`err=nil` and zero FETCH responses**. RFC 7162's
`[MODIFIED <set>]` response code, which names the conflicting messages, is not
surfaced by `imapclient`.

> **Design consequence.** Moov's optimistic-concurrency writes **cannot trust
> `Store(...)` returning nil as success**. Every conditional write must be
> verified by re-reading the message's flags/modseq, or the client must be
> patched to expose `MODIFIED`. Silent no-ops are otherwise indistinguishable
> from applied writes — a correctness bug that would corrupt flag state.

---

## T2c — `idle`: **PASS**

```
watcher entered IDLE on INBOX
connection 2 appended UID 10 to INBOX
unilateral events during IDLE: [EXISTS=5]
latency APPEND -> first unilateral event: 530.742848ms  (informal, same host)
```

IDLE works and delivers unilateral data. Note the event delivered is a bare
`EXISTS` — the notification says "something arrived", not "what arrived", so a
follow-up FETCH is always required.

**Caveat on the latency number:** ~0.5 s measured container-to-container on the
same host, and it includes a deliberate 300 ms settle sleep before the APPEND.
Treat it as an upper bound of the same order, not a precise figure. It is
comfortably inside the ADR-001 push budget.

---

## T2e — `qresync-lib`: **FINDING (blocking, but solved by T3)**

```
server advertises QRESYNC: true
FINDING: Client.Enable(CapQResync) refused by the library:
         imapclient: cannot enable "QRESYNC": not supported
control: Enable(METADATA) succeeded => refusal is a client-side allowlist
```

Confirmed by source inspection of `imapclient/enable.go` at branch-v2 tip: the
`switch` allowlist accepts only `IMAP4rev2`, `UTF8=ACCEPT`, `METADATA`,
`METADATA-SERVER`. QRESYNC is rejected **before any byte reaches the server**.
The METADATA control proves the connection is healthy and the refusal is purely
client-side policy.

Since QRESYNC cannot be enabled, `SELECT (QRESYNC …)` and `VANISHED` are
unreachable through the stock library. **Unpatched go-imap/v2 cannot implement
the Moov sync engine's core resync path.** See T3 — this is already fixed
upstream in an open PR.

---

## T2d — `notify`: **PASS on the core hypothesis**, plus two library bugs

### The core question — does NOTIFY collapse the connection fan-out?

**Yes.** One connection, watching the whole personal namespace, received events
for three different non-selected mailboxes:

```
[  2202ms] STATUS  S2/folder1  MESSAGES=1 UIDNEXT=3 UNSEEN=1 HIGHESTMODSEQ=<absent>
[  2202ms] STATUS  S2/folder3  MESSAGES=1 UIDNEXT=3 UNSEEN=1 HIGHESTMODSEQ=<absent>
[  2702ms] STATUS  S2/folder5  MESSAGES=1 UIDNEXT=3 UNSEEN=1 HIGHESTMODSEQ=<absent>
[  4307ms] STATUS  S2/folder3  MESSAGES=0 UIDNEXT=3 UNSEEN=0 HIGHESTMODSEQ=<absent>
PASS: a single connection received events for 3 distinct mailboxes
      => NOTIFY collapses the per-folder connection fan-out
```

This is the result the sync engine's architecture depends on: **one IMAP
connection per user, not one per folder.**

Two operational facts worth recording:

1. **A NOTIFY watcher must sit in IDLE.** `imapclient` only reads the socket
   while a command is in flight or during IDLE. After `NOTIFY SET`, the watcher
   must enter IDLE (or poll with NOOP) or notifications are never pumped.
2. **For non-selected mailboxes every event class arrives as one `STATUS`.**
   The event *type* is not recoverable from the notification; the client learns
   only "this mailbox changed" and must diff against its own state.

### Library bug #1 — `Status: true` emits invalid syntax

```
library sends:  T4 NOTIFY SET (STATUS) (PERSONAL (MessageNew MessageExpunge FlagChange))
server replies: T4 BAD Error in IMAP command NOTIFY: Invalid arguments
```

RFC 5465 wants the STATUS indicator as a **bare atom**, not a parenthesised
list. Confirmed by hand against the same server:

```
N C: N003 NOTIFY SET STATUS (PERSONAL (MessageNew MessageExpunge FlagChange))
N S: N003 OK NOTIFY completed        <-- accepted
```

### Library bug #2 — explicit `Mailboxes` omits the `MAILBOXES` keyword

```
library sends:  T4 NOTIFY SET ((INBOX) (MessageNew MessageExpunge FlagChange))
server replies: T4 BAD Error in IMAP command NOTIFY: Invalid arguments
```

The `MAILBOXES` keyword is missing entirely. Correct form, accepted by our
server:

```
N C: N005 NOTIFY SET (MAILBOXES (INBOX "S2/folder1") (MessageNew MessageExpunge FlagChange))
N S: N005 OK NOTIFY completed
```

Both bugs are in `encodeNotifyOptions` (`imapclient/notify.go`). The library's
own unit test (`notify_encode_test.go`) asserts the **wrong** expected bytes, so
its test suite is green while the emitted command is rejected by a real server —
i.e. the NOTIFY encoder was merged without ever being tested against a live
Dovecot.

Only `NOTIFY SET (PERSONAL (…))` — no STATUS keyword, no explicit mailbox list —
is usable today. The `notify` test therefore runs in that degraded mode.

### Other NOTIFY syntax probed by hand

| Command | Result |
|---|---|
| `NOTIFY SET STATUS (PERSONAL (…))` | OK |
| `NOTIFY SET (PERSONAL (…))` | OK |
| `NOTIFY SET (MAILBOXES (INBOX "S2/folder1") (…))` | OK |
| `NOTIFY SET STATUS (MAILBOXES (…) (…))` | OK |
| `NOTIFY SET STATUS (SELECTED (…)) (PERSONAL (…))` | OK |
| `NOTIFY SET (PERSONAL (MessageNew (UID FLAGS BODY.PEEK[…]) …))` | **BAD** |

The last line confirms the research note: Dovecot rejects the `MessageNew
(fetch-att)` form, so **NOTIFY is notification-only** on this server — a
follow-up FETCH is always required. `SELECTED` and `SELECTED-DELAYED` are both
accepted, as is combining `SELECTED` with `PERSONAL` in one command.

---

## T4 — `notify-raw`: the control experiment that exonerates Dovecot

The research flagged a suspected **RFC 5465 violation**: NOTIFY-induced STATUS
omitting `HIGHESTMODSEQ`. The go-imap run above appears to confirm it
(`HIGHESTMODSEQ=<absent>`, 4/4). **It does not.** That observation is an
artifact of library bug #1.

Control: identical mutations (APPEND, then a pure `+FLAGS (\Flagged)`, then
EXPUNGE), same server, changing **only** the NOTIFY SET syntax.

**Variant A — `NOTIFY SET STATUS (PERSONAL …)`** (correct RFC 5465; the library
cannot emit this):

```
W S: * STATUS S2/folder2 (MESSAGES 1 UIDNEXT 4 UNSEEN 1 HIGHESTMODSEQ 11)   <- APPEND
W S: * STATUS S2/folder2 (HIGHESTMODSEQ 12)                                 <- FlagChange
W S: * STATUS S2/folder2 (MESSAGES 0 UIDNEXT 4 UNSEEN 0 HIGHESTMODSEQ 14)   <- EXPUNGE
```

**Variant B — `NOTIFY SET (PERSONAL …)`** (what go-imap emits today):

```
W S: * STATUS S2/folder4 (MESSAGES 1 UIDNEXT 4 UNSEEN 1)                    <- APPEND
                                        (nothing at all)                     <- FlagChange
W S: * STATUS S2/folder4 (MESSAGES 0 UIDNEXT 4 UNSEEN 0)                    <- EXPUNGE
```

```
HIGHESTMODSEQ present: variant A 3/3, variant B 0/2
```

Two conclusions, both important:

1. **Dovecot does NOT violate RFC 5465.** It includes `HIGHESTMODSEQ` whenever
   the client requests the STATUS indicator. The research's suspicion is
   **refuted** for Dovecot 2.3.21.1. *(Recorded as a correction to the S2 brief.)*
2. **Under the library's degraded form, flag changes are invisible.** Variant A's
   middle line — `* STATUS S2/folder2 (HIGHESTMODSEQ 12)` — is the *only* signal
   that a flag changed, because `MESSAGES`/`UNSEEN` are unchanged by a
   `\Flagged` toggle. Variant B emits **nothing** for that mutation.

> **Design consequence (correctness, not performance).** If Moov ships on stock
> go-imap NOTIFY, flag changes made by other clients on non-selected folders are
> **silently lost** until some unrelated event or a full poll happens to reveal
> them. Fixing `encodeNotifyOptions` to emit the STATUS keyword is a
> **prerequisite**, not an optimisation.

---

## T3 — PR #757 feasibility: **VALIDATED end-to-end (better than expected)**

[PR #757 "imapclient: support QRESYNC"](https://github.com/emersion/go-imap/pull/757),
open against base branch `v2`, June 2026.

**Scope is wider than the brief assumed.** The brief expected it to cover only
`FetchOptions.Vanished` + the `Vanished` handler, leaving Moov to write the
`SELECT (QRESYNC …)` parameter itself. In fact the author expanded it into the
complete client-side QRESYNC story:

| Added | Where |
|---|---|
| `FetchOptions.Vanished bool` | `fetch.go` |
| `SelectOptions.QResync *QResyncOptions` | `select.go` |
| `QResyncOptions{UIDValidity, ModSeq, KnownUIDs, SeqMatchData}` | `select.go` |
| `SeqMatchData{KnownSeqSet, KnownUIDSet}` | `select.go` |
| `SelectData.VanishedUIDs UIDSet` | `select.go` |
| `UnilateralDataHandler.Vanished func(uids, earlier bool)` | `imapclient/client.go` |
| `handleVanished()` | `imapclient/vanished.go` (new) |
| **`CapQResync` + `CapCondStore` added to the `Enable()` allowlist** | `imapclient/enable.go` |

### Does it still apply to branch-v2 tip? Yes — cleanly.

```
branch-v2 HEAD: f68ef41
diff bytes: 14030  lines: 417
 fetch.go                    |    6 +
 imapclient/client.go        |   11 ++
 imapclient/enable.go        |    2
 imapclient/fetch.go         |    9 ++
 imapclient/select.go        |   16 +++
 imapclient/vanished.go      |   46 ++++++++++
 imapclient/vanished_test.go |  206 +++++++++++++++++++++++++++++++++++++++++++
 select.go                   |   31 ++++++
 8 files changed, 324 insertions(+), 3 deletions(-)
--- apply check ---
APPLIES_CLEANLY
```

**324 insertions, of which 206 are tests** — only ~118 lines of production code.

### It does not merely apply — it works against our Dovecot

Rather than stop at an effort estimate, the patch was applied to the branch-v2
tip and exercised against the real server (`spikes/s2-goimap/pr757/`):

```
[PASS] Enable(QRESYNC) accepted (err=<nil>)
  baseline UIDVALIDITY=1786153920 HighestModSeq=30
  seeded UIDs 13,14; sync modseq=32
[PASS] SELECT (QRESYNC ...) accepted (err=<nil>)
  SelectData.VanishedUIDs=14
[PASS] VANISHED surfaced (SelectData.VanishedUIDs or Vanished handler)
[PASS] UID FETCH (CHANGEDSINCE .. VANISHED) accepted (err=<nil>)
  changed msgs=1, Vanished handler calls=1 uids=14 earlier=true
    UID=13 ModSeq=33 Flags=[\Flagged]
[PASS] Vanished handler fired for FETCH VANISHED

PR757 VALIDATION: 0 failure(s)
```

Every value is correct: UID 14 (the expunged one) is reported as vanished with
`earlier=true`; UID 13 (the flagged one) comes back as a changed message with
its new modseq. **The T2e blocker is fully resolved by this patch.**

### What Moov would still have to write itself

- **Nothing for QRESYNC.** The patch covers `ENABLE`, `SELECT (QRESYNC …)`,
  `FETCH … VANISHED`, and both delivery paths (`SelectData.VanishedUIDs` and the
  unilateral `Vanished` handler).
- **The NOTIFY encoder fixes** (bugs #1 and #2) — *not* in this PR. Small:
  two `encodeNotifyOptions` corrections plus fixing the wrong assertions in
  `notify_encode_test.go`. Good upstream contribution candidates.
- **Surfacing the `MODIFIED` response code** (T2b caveat) — not in this PR
  either, and the larger of the remaining gaps.

**Effort estimate: low.** Carrying PR #757 is a vendored-patch decision, not an
engineering project. The risk is maintenance (an unmerged PR) rather than
correctness.

---

## Open risks for the director

1. **go-imap/v2 is pre-release and demonstrably under-tested against real
   servers.** The NOTIFY encoder ships with unit tests asserting bytes that a
   real Dovecot rejects. This is evidence about the library's overall maturity,
   not one isolated bug. ADR-001 already mandates vendoring + encapsulation
   behind `internal/imap`; **this spike is empirical justification for that
   rule**, and the encapsulation should be treated as mandatory, not stylistic.
2. **Moov depends on an unmerged PR for a core code path.** PR #757 is open, not
   merged. Options: vendor the patch (recommended — it applies cleanly and is
   validated), or maintain a fork. Either way the pin must be exact and the
   patch re-validated on every bump.
3. **Silent-write hazard (T2b).** `Store()` returning nil does not mean the write
   applied. Until `MODIFIED` is surfaced, every conditional write needs
   read-back verification. This is a correctness risk that will not show up in
   normal testing — it only bites under concurrent modification.
4. **Silent flag-loss hazard (T4).** Until the NOTIFY encoder emits the STATUS
   keyword, flag changes on non-selected folders produce no notification at all.
5. **NOTIFY is notification-only.** Dovecot rejects `MessageNew (fetch-att)`, so
   every notification costs a follow-up round-trip. The sync engine should batch
   follow-up FETCHes per notified mailbox rather than reacting per event.
6. **No OBJECTID.** Message identity must be derived by Moov
   (UIDVALIDITY+UID plus a content hash to survive moves).
7. **Watcher liveness.** A NOTIFY connection only receives data while in IDLE.
   The engine needs an explicit IDLE-maintenance loop (with the library's 28-min
   auto-restart) and must handle `NOTIFICATIONOVERFLOW` by falling back to a
   full resync.

---

## Reproducing

```bash
# from the repo, sync to the VPS:
tar czf - . | ssh root@217.216.83.79 'tar xzf - -C /root/moov-s2'

# run any test (never put the password in a file):
ssh root@217.216.83.79 'docker run --rm \
  --network mailcowdockerized_mailcow-network \
  -v /root/moov-s2:/app -v /root/gomodcache:/go/pkg/mod -w /app \
  -e IMAP_HOST=dovecot -e IMAP_PORT=143 \
  -e IMAP_USER=moov-test@atmosfera.cloud -e IMAP_PASSWORD=<secret> \
  golang:1.24-bookworm go run . <t1-qresync|caps|condstore|idle|notify|notify-raw|qresync-lib>'
```

For the PR #757 probe see `pr757/README.md`.

**Secrets:** the password is passed only via `IMAP_PASSWORD` and is redacted
(`<REDACTED>`) in every transcript. A grep for the literal password across the
spike tree and all captured outputs returns nothing.
