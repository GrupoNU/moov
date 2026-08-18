# Upstream contribution — patch 0002: NOTIFY encoder, two RFC 5465 violations

> **Status: PREPARED, NOT SUBMITTED.** This document is the handoff for the
> actual submission. The director reviews it before anything is sent upstream.

| Field | Value |
|---|---|
| Target repository | [`emersion/go-imap`](https://github.com/emersion/go-imap) |
| Target branch | `v2` |
| Base commit | `f68ef419e622a283e0cf8ddab4498b84f9bd038d` |
| Local patch | `patches/0002-notify-encoder-rfc5465.patch` |
| Files touched | `imapclient/notify.go`, `imapclient/notify_encode_test.go` |
| Evidence | `spikes/s2-goimap/RESULTS.md` — T2d, T4 |
| Rebase needed | **None** — see [Applying against current upstream](#applying-against-current-upstream) |

---

## 1. Problem statement

`encodeNotifyOptions` in `imapclient/notify.go` emits two command forms that
violate the RFC 5465 grammar. Dovecot rejects both outright:

```
BAD Error in IMAP command NOTIFY: Invalid arguments
```

### 1.1 `Status: true` parenthesises the STATUS indicator

RFC 5465 §6 gives the grammar:

```abnf
notify-set = "SET" [SP "STATUS"] 1*(SP event-group)
```

`STATUS` is an **optional bare atom**, not a parenthesised list. The encoder
wraps it in a one-element list:

```
sent:      NOTIFY SET (STATUS) (PERSONAL (MessageNew MessageExpunge FlagChange))
required:  NOTIFY SET STATUS (PERSONAL (MessageNew MessageExpunge FlagChange))
```

### 1.2 An explicit mailbox list omits the mandatory `MAILBOXES` keyword

RFC 5465 §6 again:

```abnf
filter-mailboxes     = filter-mailboxes-selected / filter-mailboxes-other
filter-mailboxes-other = ("SUBTREE" / "MAILBOXES") SP one-or-more-mailbox
```

The selector keyword is mandatory in **both** branches. The encoder emits it for
`SUBTREE` but silently drops it for `MAILBOXES`, leaving a bare list where the
grammar requires a keyword:

```
sent:      NOTIFY SET ((INBOX "Sent") (MessageNew MessageExpunge FlagChange))
required:  NOTIFY SET (MAILBOXES (INBOX "Sent") (MessageNew MessageExpunge FlagChange))
```

### 1.3 Why the unit tests did not catch this

`notify_encode_test.go` asserts the **wrong expected bytes** for four cases — it
was written to agree with the encoder rather than with the grammar, so the suite
is green while every command it validates is rejected by a real server.

This is visible in the history rather than inferred. `imapclient/notify.go` has
exactly one commit since it was created — [`22f41a9`, *"imapclient: implement
support for NOTIFY"*](https://github.com/emersion/go-imap/commit/22f41a99378e2d3e7fe67599252c3f97ece9d241)
— whose own message notes:

> Client–server tests run only with dovecot; the imapmemserver doesn't support
> NOTIFY.

So the client–server tests that would have exercised the encoder against Dovecot
do not run for NOTIFY, and the encoder's only coverage is the unit test that
encodes the same misreading of the grammar. **Correcting those assertions is
part of this patch** and is the reason the bugs are worth reporting together:
fixing the encoder without fixing the test would turn the suite red.

---

## 2. Why this is a correctness fix, not an ergonomic one

Bug 1.1 is easy to mistake for cosmetics — a client can simply not set
`Status: true`. It cannot, and the consequence is silent data loss in the
client's view of the mailbox.

Without the `STATUS` indicator, the server's notification for a non-selected
mailbox carries only `MESSAGES`/`UIDNEXT`/`UNSEEN`. **A flag change moves none of
them.** Toggling `\Flagged` leaves the message count identical, the unseen count
identical, and `UIDNEXT` identical — so with no `HIGHESTMODSEQ` in the STATUS
response there is nothing for the server to report, and it reports nothing at
all.

Spike S2 T4 measured this directly against Dovecot 2.3.21.1, holding the
mutations constant and changing **only** the NOTIFY SET syntax:

**Variant A — `NOTIFY SET STATUS (PERSONAL …)`** (correct RFC 5465; the stock
encoder cannot emit this):

```
* STATUS S2/folder2 (MESSAGES 1 UIDNEXT 4 UNSEEN 1 HIGHESTMODSEQ 11)   <- APPEND
* STATUS S2/folder2 (HIGHESTMODSEQ 12)                                 <- FlagChange
* STATUS S2/folder2 (MESSAGES 0 UIDNEXT 4 UNSEEN 0 HIGHESTMODSEQ 14)   <- EXPUNGE
```

**Variant B — `NOTIFY SET (PERSONAL …)`** (what the encoder emits today, minus
the syntax error):

```
* STATUS S2/folder4 (MESSAGES 1 UIDNEXT 4 UNSEEN 1)                    <- APPEND
                             (nothing at all)                          <- FlagChange
* STATUS S2/folder4 (MESSAGES 0 UIDNEXT 4 UNSEEN 0)                    <- EXPUNGE
```

```
HIGHESTMODSEQ present: variant A 3/3, variant B 0/2
```

Variant A's middle line is the *only* signal that a flag changed. For any client
maintaining a local cache, shipping on the stock encoder means another client
marking a message read is **invisible** until an unrelated event happens to
reveal it.

A useful side finding for the maintainer: this experiment also **exonerates
Dovecot** of a suspected RFC 5465 violation (STATUS responses omitting
`HIGHESTMODSEQ`). Dovecot includes it whenever the client asks for the STATUS
indicator; the omission is caused entirely by the client's malformed request.

---

## 3. Minimal reproduction against a real Dovecot

Server: Dovecot 2.3.21.1 (`d492236fa0`), reached over STARTTLS on port 143.
Full transcripts in `spikes/s2-goimap/RESULTS.md` §T2d and §T4.

### 3.1 Through the library (both forms rejected)

```go
c, _ := imapclient.DialStartTLS("dovecot:143", nil)
defer c.Close()
c.Login("user@example.com", pw).Wait()

// Bug 1.1
err := c.Notify(&imap.NotifyOptions{
    Status: true,
    Items: []imap.NotifyEventGroup{{
        MailboxSpec: imap.NotifyMailboxSpecPersonal,
        Events: []imap.NotifyEvent{
            imap.NotifyEventMessageNew, imap.NotifyEventMessageExpunge,
            imap.NotifyEventFlagChange,
        },
    }},
}).Wait()
// -> BAD Error in IMAP command NOTIFY: Invalid arguments

// Bug 1.2
err = c.Notify(&imap.NotifyOptions{
    Items: []imap.NotifyEventGroup{{
        Mailboxes: []string{"INBOX"},
        Events:    []imap.NotifyEvent{imap.NotifyEventMessageNew},
    }},
}).Wait()
// -> BAD Error in IMAP command NOTIFY: Invalid arguments
```

Observed wire bytes and the server's reply:

```
C: T4 NOTIFY SET (STATUS) (PERSONAL (MessageNew MessageExpunge FlagChange))
S: T4 BAD Error in IMAP command NOTIFY: Invalid arguments

C: T4 NOTIFY SET ((INBOX) (MessageNew MessageExpunge FlagChange))
S: T4 BAD Error in IMAP command NOTIFY: Invalid arguments
```

### 3.2 By hand, confirming the corrected forms (accepted)

```
C: N003 NOTIFY SET STATUS (PERSONAL (MessageNew MessageExpunge FlagChange))
S: N003 OK NOTIFY completed

C: N005 NOTIFY SET (MAILBOXES (INBOX "S2/folder1") (MessageNew MessageExpunge FlagChange))
S: N005 OK NOTIFY completed
```

Both patched outputs are **byte-identical** to these hand-verified commands.

Additional forms probed by hand, all accepted, so the fix does not narrow what
is expressible: `NOTIFY SET (PERSONAL (…))`,
`NOTIFY SET STATUS (MAILBOXES (…) (…))`,
`NOTIFY SET STATUS (SELECTED (…)) (PERSONAL (…))`.

---

## 4. Patch rationale

Two edits in `encodeNotifyOptions`, each a direct transcription of the grammar:

1. `Status: true` emits `SP "STATUS"` as a bare atom instead of a one-element
   list.
2. The non-`Subtree` branch of an explicit mailbox list emits the `MAILBOXES`
   keyword, mirroring the `SUBTREE` branch immediately above it.

Both are commented in place with the ABNF they implement, so the next reader
sees the rule rather than rediscovering it against a server.

The four corrected assertions in `notify_encode_test.go` are the same change
viewed from the test side; they now encode the grammar rather than the
implementation.

**API impact.** The patch changes the observable output of a public API, which
is the one thing a maintainer may reasonably push back on. The mitigating
argument: the current output is rejected by Dovecot with `BAD`, so no working
client can depend on it. Any client that currently sets `Status: true` or an
explicit `Mailboxes` list is receiving a protocol error today.

---

## 5. Proposed PR

### Title

```
imapclient: fix NOTIFY encoder to match the RFC 5465 grammar
```

### Body

```markdown
`encodeNotifyOptions` emits two forms that violate the RFC 5465 grammar, and
Dovecot rejects both with `BAD Error in IMAP command NOTIFY: Invalid arguments`.

**1. The STATUS indicator is parenthesised.** RFC 5465 section 6:

    notify-set = "SET" [SP "STATUS"] 1*(SP event-group)

`STATUS` is a bare atom, not a list.

    -NOTIFY SET (STATUS) (PERSONAL (MessageNew MessageExpunge FlagChange))
    +NOTIFY SET STATUS (PERSONAL (MessageNew MessageExpunge FlagChange))

**2. An explicit mailbox list omits the MAILBOXES keyword.** RFC 5465 section 6:

    filter-mailboxes-other = ("SUBTREE" / "MAILBOXES") SP one-or-more-mailbox

The keyword is mandatory in both branches; it is emitted for SUBTREE but not
for MAILBOXES.

    -NOTIFY SET ((INBOX "Sent") (MessageNew MessageExpunge FlagChange))
    +NOTIFY SET (MAILBOXES (INBOX "Sent") (MessageNew MessageExpunge FlagChange))

Both corrected forms were verified by hand against Dovecot 2.3.21.1, which
accepts them with `OK NOTIFY completed`. The patched encoder's output is
byte-identical to those commands.

### Why the first one is a correctness bug, not a cosmetic one

Without the STATUS indicator, a flag change in a non-selected mailbox produces
**no notification at all**. Toggling `\Flagged` moves neither MESSAGES nor
UNSEEN nor UIDNEXT, so with no HIGHESTMODSEQ in the STATUS response the server
has nothing to report. Measured against Dovecot 2.3.21.1, same mutations,
changing only the NOTIFY SET syntax:

    NOTIFY SET STATUS (PERSONAL ...)     HIGHESTMODSEQ present 3/3
    NOTIFY SET (PERSONAL ...)            HIGHESTMODSEQ present 0/2
                                         + the FlagChange event missing entirely

For a client maintaining a local cache, that means another client marking a
message read is invisible until some unrelated event reveals it.

### The tests asserted the wrong bytes

`notify_encode_test.go` expected the malformed output for four cases, which is
why the suite stayed green. This PR corrects those assertions, so the fix and
the test move together — otherwise the encoder fix turns the suite red.

For context, `imapclient/notify.go` has one commit since it was added (22f41a9),
whose message notes that the client–server tests run only with Dovecot and that
imapmemserver does not support NOTIFY — so the encoder had no live-server
coverage, and the unit test encoded the same misreading of the grammar as the
implementation.

### Testing

- `go test ./imapclient/...` with the corrected assertions.
- Both corrected forms exercised against Dovecot 2.3.21.1 over STARTTLS,
  accepted with `OK NOTIFY completed`; a NOTIFY watcher on the personal
  namespace then received STATUS events carrying HIGHESTMODSEQ for APPEND,
  FlagChange and EXPUNGE.

Signed-off-by: <name> <email>
```

> **Note before sending.** Upstream commits carry a `Signed-off-by` trailer
> (DCO). Fill in the real name and address of whoever submits; do not send the
> placeholder.

---

## 6. Applying against current upstream

Checked on 2026-08-18.

- Branch `v2` HEAD is **`f68ef419e622a283e0cf8ddab4498b84f9bd038d`** — identical
  to the pin in `patches/README.md`. **No drift, no rebase.**
- `imapclient/notify.go` has had exactly one commit in its history
  (`22f41a9`, 2026-02-14), which is the commit that created it. Nothing has
  touched it since.
- `imapclient/notify_encode_test.go` is likewise untouched since that commit.
- A search of the issue tracker found **no existing report** of either bug, so
  the PR does not duplicate open work and has no issue to reference.

The patch therefore applies to current upstream tip exactly as it applies to the
vendored tree. Re-verify with:

```sh
git clone --branch v2 https://github.com/emersion/go-imap.git /tmp/goimap
cd /tmp/goimap
git rev-parse HEAD    # expect f68ef419e622a283e0cf8ddab4498b84f9bd038d
git apply --check /path/to/moov/patches/0002-notify-encoder-rfc5465.patch
```

If HEAD has moved by submission time, re-run that check: the fix is confined to
one function and four test assertions, so a conflict would be mechanical.
```
