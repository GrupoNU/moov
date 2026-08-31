# Moov Mail

> A Gmail-class open source webmail for the Mailcow and Dovecot installed base.

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)

**Status: pre-1.0, running in production on one pilot.** The Gmail-class feature
program is complete and deployed, serving its authors' own mail. There is no
tagged release, no packaged distribution and no support promise yet. See
[Status, honestly](#status-honestly) for exactly where things stand.

---

## What this is

Moov Mail is the first open source product of
[NU Desarrollos Conscientes](https://gruponu.com). It exists to close a gap
nobody else is filling.

Every modern webmail worth using — Bulwark, Twake Mail — is built on JMAP and
therefore requires a JMAP-native server such as Stalwart or Apache James.
Meanwhile the very large installed base running **Mailcow / Dovecot** is left
with IMAP-direct clients: SOGo, Roundcube, SnappyMail. Those clients are not bad
software; they are architecturally capped. A client that talks IMAP directly on
every keystroke cannot deliver instant search across a decade of mail, real
push, offline use, or undo send. Dovecot has no JMAP server and is not going to
grow one.

Moov's answer is to put the missing piece in between, without asking anyone to
migrate their mail server. **That piece now exists and runs.**

## What it does today

Everything below is implemented, tested and deployed on the pilot. Where a
feature is partial, it says so.

**Conversation view.** Mail groups into threads server-side (a References graph
in both directions, with a normalized-subject fallback), and the list shows one
row per conversation with its message count. The reader collapses older
messages, opens the newest, hides quoted tails behind "show trimmed content",
and separates per-message actions (reply, forward) from per-thread ones
(archive, delete, label). Conversation view can be turned off.

**Full triage.** Archive, delete with a real Trash, star, mark read/unread, move
and label. **Snooze** removes a message from the inbox and brings it back to the
*top* — and it does so by really moving it to a `Snoozed` folder over IMAP, so
your phone's mail app sees the same thing. **Mute** makes a thread's replies
skip the inbox, keyed on durable Message-IDs rather than on cache row ids.
**Schedule send** queues up to 100 messages, and cancelling one turns it back
into a draft. Report spam and not-spam, block a sender (a Sieve rule, not a
local filter), unsubscribe from the message. Hover actions are exactly the four
Gmail chose. `z` undoes the last action with a real inverse patch, not a re-fetch.

**Search with an operator language.** `from: to: cc: bcc: subject: has:attachment
is:starred is:unread is:muted in:anywhere before: after: older_than: newer_than:`,
plus `OR`, negation with `-`, and quoted phrases. Spam and Trash are excluded by
default. Results carry highlighted snippets (`SearchSnippet/get`). Operators
outside the implemented set are **rejected visibly** rather than silently
swallowed. Deep paging was measured, not assumed: a constant 3–27 ms per page
down to a depth of 100,000 messages.

**Settings that roam.** Preferences live on the server, in PostgreSQL, behind a
versioned JMAP vendor capability — not in the browser's localStorage. Reading
pane placement, density, undo-send window, remote-image policy, hover actions,
auto-advance, notification mode, inbox type, keyboard on/off, signature, theme,
language. The settings panel is searchable.

**Server-side filters, vacation and forwarding, over Sieve.** Filters compile to
Sieve scripts on your own Dovecot through ManageSieve — so they run when Moov is
not. The vacation responder emits the exact Gmail shape (`vacation :days 4`, no
replies to lists, spam or auto-submitted mail). Forwarding requires the
recipient to accept a token mailed to them first. **The guarantee that matters:
Moov partitions Sieve by origin and never destroys a script it did not write.**
Your hand-written or SOGo-managed rules are preserved byte for byte, and every
write is validated with `CHECKSCRIPT` before `PUTSCRIPT`.

**Labels under an honest ceiling.** Labels are real IMAP keywords, so they are
visible to every other client you use. Maildir gives 26 durable keywords per
folder, and the flags other mail apps set share that budget. Moov does not hide
this: the ceiling is explained in the interface, the colour palette is closed
(so contrast is guaranteed in both themes), and folders — which have no such
limit — carry the filing.

**An installable PWA with real offline.** Manifest, service worker, icons, and a
`mailto:` protocol handler. Offline you can read what you have seen, search it
(the index is in IndexedDB), and reply — the reply waits in a real **Outbox**
folder and sends itself when the connection returns. New mail arrives with no
polling, over SSE, in well under a second end to end.

**Security posture** — the part that is easiest to get wrong quietly:

- **Three independent layers of HTML sanitization**: `bluemonday` server-side, `DOMPurify`
  in the client, and rendering inside a sandboxed `<iframe>` with no
  `allow-scripts`, under a `default-src 'none'` CSP. A test verifies that no
  render path bypasses the single sanitization chokepoint.
- **Remote images are proxied**, never fetched by the browser directly, through
  HMAC-signed URLs with SSRF protection that re-checks after every redirect.
  Because the proxy exists, images can display by default — and are still
  suppressed on anything Rspamd flagged, which overrides the user's setting.
- **Links are never rewritten.** No redirector, no tracking wrapper. An
  invariant test compares the URL byte for byte.
- **Read receipts are never answered automatically** and never requested by
  default. `Disposition-Notification-To` is ignored, with a test that watches
  the guard itself.
- **Sender identity is BIMI-only.** No favicon fetching, no third-party logo
  lookup — nothing that lends a sender unearned credibility or leaks that you
  opened the mail. Everyone else gets initials.
- The user's own password is **never stored**: it is used once for a validating
  IMAP login and discarded in favour of a scoped app password, encrypted with
  AES-256-GCM under a key held outside the database.

## The method

The reason to trust the list above is the way it was built.

**Gmail defines what and why.** Every feature had to pass a three-question
filter: does Gmail have it; if not, why not; and is that reason a Google
business artifact (in which case we may diverge) or a security and privacy
stance (in which case we follow). The answers are not from memory — they are
cited, item by item, to Google-authored documentation retrieved live, in
[the Gmail canon](docs/research/06-gmail-canon.md). What could not be sourced is
listed as unsourced rather than quietly asserted, and the features of other
webmails that *fail* the filter are recorded with the reason they failed.

**Every feature was gate-audited.** The program closed with an independent
audit — an agent that implemented none of it — checking the canon row by row
against the code, with a file-and-line citation or a test name for each verdict,
and re-running every quality check. It found real gaps and they are published
in full, including two critical ones:
[the final gate report](docs/reports/gate-final-gmail-class.md).

**Deferred means named.** Nothing leaves the plan by omission. What is not built
is listed with its size, the reason, and the risk, in
[§6 of the plan](docs/specs/L3-gmail-class-plan.md).

## Architecture

Moov is **not** another IMAP-direct client. It runs a **sync engine** that
mirrors your Dovecot mailboxes into its own store — PostgreSQL metadata with a
full-text index, plus content-addressed blobs for the raw messages — and exposes
a **standard JMAP API** (RFC 8620 / RFC 8621) that a fast PWA talks to. Your
Mailcow installation is never modified.

```
  ┌─────────────────────────────────────────┐
  │  Browser — React / TypeScript PWA       │
  │  offline, keyboard-first, <100 ms UI    │
  └────────────────────┬────────────────────┘
                       │  JMAP (RFC 8620/8621) + SSE push
                       ▼
  ┌─────────────────────────────────────────┐
  │  Moov backend (Go)                      │
  │                                         │
  │   JMAP server   standard, conformance   │
  │                 suite in CI             │
  │        ▲                                │
  │        │ reads only                     │
  │   own store     PostgreSQL 17: metadata │
  │                 + tsvector FTS + blobs  │
  │        ▲                                │
  │        │ writes                         │
  │   sync engine   IMAP CONDSTORE/QRESYNC/ │
  │                 NOTIFY/IDLE, reconciler │
  └────────────────────┬────────────────────┘
                       │  IMAP :143 · SMTP :587 · Sieve :4190 · Mailcow API
                       ▼
  ┌─────────────────────────────────────────┐
  │  Your existing Mailcow — UNMODIFIED     │
  │  Dovecot · Postfix · Rspamd             │
  └─────────────────────────────────────────┘
```

The full reasoning, including the options rejected and why, is
[ADR-001](docs/adr/ADR-001-arquitectura.md). Design principles, each of which
constrains the code:

- **Mailcow is never modified.** Moov runs as a separate Docker stack joined to
  the Mailcow network. Everything goes through IMAP, SMTP, Sieve and the Mailcow
  API. The mail store on disk is never touched — not even read-only.
- **Dovecot is the source of truth.** Moov's store is a cache. Every byte of it
  can be rebuilt from the server, which is what makes it safe to be fast. This
  is why snooze is a real IMAP move and labels are real IMAP keywords: no
  user-visible mail state lives only in PostgreSQL.
- **Standard JMAP, not a homegrown API.** Third-party JMAP clients work against
  Moov, and conformance is verified in CI rather than asserted.
- **Gmail-class is measured, not claimed.** Search pages in 3–27 ms at any
  depth; flag changes 19–44 ms, archive 166 ms, new mail visible over SSE in
  671–942 ms — all measured against the live pilot.
- **Security first**, as described above, from the first commit rather than
  retrofitted.

## Standards implemented

Verified against the code, not against intent. Vendor extensions are marked as
such with their URIs, because a client is entitled to know what is standard and
what is ours.

| Standard | Status in Moov |
|---|---|
| **RFC 8620** — JMAP core | Session, `Core/echo`, batching with back-references, JSON-Pointer result references ([RFC 6901](https://www.rfc-editor.org/rfc/rfc6901)), upload/download, request-level error types |
| **RFC 8621** — JMAP Mail | `Mailbox`, `Email`, `Thread`, `Identity`, `EmailSubmission` (`get`/`set`/`query`/`changes`/`queryChanges` as each object defines them), `SearchSnippet/get` (§5), `VacationResponse` (§8) |
| **RFC 9661** — JMAP Sieve | `SieveScript/get`, `/set`, `/query`, `/queryChanges`, `/changes`, `/validate` with blob upload |
| **RFC 9425** — JMAP Quota | `Quota/get`, `/query`, `/changes`, `/queryChanges`, carried from IMAP QUOTA |
| **RFC 5804** — ManageSieve | Client, used to install filters and the vacation responder on Dovecot |
| **RFC 7162** — IMAP CONDSTORE / QRESYNC | The delta source of the sync engine |
| **RFC 5465 / RFC 2177** — IMAP NOTIFY / IDLE | The watcher: one connection observes many folders |
| **RFC 5228 / RFC 5230** — Sieve and the vacation extension | What filters and the responder compile to |
| **RFC 5322 / RFC 2045-2047 / RFC 2231** — Internet messages and MIME | The parser cascade, exercised by a 110-case pathological corpus |
| **RFC 8058** — one-click unsubscribe | **Partial.** The header is parsed and the `mailto:` and link paths work; the server-side one-click POST is not built ([deferred, named](docs/specs/L3-gmail-class-plan.md)) |
| `https://moov.email/ns/prefs` | **Vendor.** Per-account preferences (`Prefs/get`, `/set`, `/changes`) |
| `https://moov.email/ns/triage` | **Vendor.** `Snooze/*` and `Mute/*`, which RFC 8621 has no equivalent for |
| `https://moov.email/ns/filters` | **Vendor.** The rule model above Sieve (`FilterRule/*`, `Forwarding/*`, `ForwardingAddress/*`) |

A client that never opts into a vendor capability sees a plain, conformant
RFC 8620/8621 server.

**On the official JMAP TestSuite:** it is not usable here, for two documented
reasons — it publishes no license at all (incompatible with a public AGPL repo),
and its setup deletes and reseeds the account before the first test runs, which
no filter can skip. In its place CI runs a conformance suite written against the
RFCs and cited clause by clause, with every skip carrying an explicit reason.

## Running it

Deployment is documented in **[deploy/README.md](deploy/README.md)**: the compose
stack, the master-key handling, account provisioning, the fronting proxy, and
the operational metrics.

Be aware of what that document targets. It describes **one pattern** — Moov
colocated on the same host as an existing Mailcow, joined to its Docker network,
reached over a VPN or through a second public IP. It is the pattern that runs in
production, and it is the only one that has been run at all. There is no
published image, no Helm chart, no one-line installer, and no upgrade path
guaranteed across commits. Migration 0004 backfills thread ids over the whole
store and takes tens of seconds on a populated one, during which the daemon does
not serve.

If you are running Mailcow and want to try this, read that file completely
first, and treat it as pre-1.0 software touching your mail.

## Status, honestly

Moov is **feature-complete against a verified Gmail canon, and pre-1.0.**

| | |
|---|---|
| ✅ **Phase 0 research** | Four studies: competitive landscape, JMAP, Mailcow integration, sync-engine prior art. Synthesized and audited. |
| ✅ **Architecture decided** | [ADR-001](docs/adr/ADR-001-arquitectura.md), accepted. |
| ✅ **Four validation spikes** | Every load-bearing assumption tested against a real Mailcow before product code was written — see below. |
| ✅ **Sync engine** | Initial sync, incremental QRESYNC, NOTIFY watcher, reconciler, crash recovery. [L2 spec](docs/specs/L2-sync-engine.md). |
| ✅ **JMAP server** | Reads, writes, submission with undo, SSE push. [L2 read](docs/specs/L2-jmap-server.md) · [L2 write](docs/specs/L2-jmap-write.md). |
| ✅ **The PWA** | [L2 spec](docs/specs/L2-pwa.md). |
| ✅ **Gmail-class program** | Eleven epics, [plan](docs/specs/L3-gmail-class-plan.md), closed by an [independent gate audit](docs/reports/gate-final-gmail-class.md). |
| 🟡 **Deployed** | **One pilot, four real mailboxes** — the authors' own mail, ~27,000 messages in the largest account. |
| ⬜ **Released** | No tag, no packaged build, no public demo, no support commitment. |

What that means in practice, stated plainly:

- It is **not battle-tested**. One deployment, on hardware its authors control,
  with users who can read the source when something is wrong.
- **APIs may change**, including the vendor capabilities and the database
  schema. Migrations exist, but no compatibility promise does.
- The [gate report](docs/reports/gate-final-gmail-class.md) is published with
  its findings intact rather than summarized away, because a report that only
  lists successes is not an audit.
- Deferred work is named in [§6 of the plan](docs/specs/L3-gmail-class-plan.md):
  Web Push, superstars, multiple inboxes, delegation, multi-account, S/MIME, and
  the AI-gated features (importance, categories) that are only reachable behind
  an explicit consent toggle, and are a later phase by decision.

Quality checks, as of the gate: **2,046 web tests** across 109 files, a Go suite
that is `-race` clean, `golangci-lint` at zero issues, and five CI jobs — build,
MIME corpus, migrations against PostgreSQL 17, JMAP conformance, and the web
pipeline.

The spikes remain the reason the architecture was more than a hope:

| Spike | Question it answered | Result |
|---|---|---|
| [S1](docs/spikes/S1-jmap-sobre-dovecot.md) | Can JMAP work over an unmodified Dovecot? | Yes — validated end to end against a real Mailcow. |
| [S2](docs/spikes/S2-go-imap-dovecot.md) | Do QRESYNC, CONDSTORE and NOTIFY work with `go-imap/v2`? | Yes, with a patch set we carry and are upstreaming. |
| [S3](docs/spikes/S3-benchmark-fts.md) | Does PostgreSQL full-text search hold up at 5M messages? | Yes, with a specific and now-mandatory configuration. |
| [S4](docs/spikes/S4-corpus-mime.md) | What does pathological MIME do to the parser? | 110 committed test cases, written before the parser existed. |

## Development

You need **Go 1.24+**, **Docker**, **Node 20+** and **git**.

```sh
git clone https://github.com/GrupoNU/moov.git
cd moov

make db-up     # PostgreSQL 17 on 127.0.0.1:5433 (development only)
make migrate   # apply the migrations
make ci        # fmt, vendor check, vet, lint, build, corpus check, tests
make build     # ./bin/moovd

cd web && npm install && npm run test
```

`make help` lists every target. [CONTRIBUTING.md](CONTRIBUTING.md) has the full
setup, the test recipe (including the database-backed suites), the testing
policy and the commit conventions.

## Repository map

```
cmd/moovd/          The daemon
cmd/moovctl/        The operator CLI: keys, accounts, branding
internal/imap/      The only package that may import go-imap (enforced by lint AND test)
internal/parser/    MIME parsing cascade
internal/store/     PostgreSQL: schema, migrations, the whole query repertoire
internal/blob/      Content-addressed raw messages, refcounted and GC'd
internal/sync/      Orchestration: initial sync, incremental, watcher, reconciler
internal/jmap/      The JMAP server: core, mail, submission, Sieve, quota, vendor extensions
internal/jmaphttp/  The HTTP layer: auth, routing, SSE, blob upload/download, rate limiting
internal/sieve/     ManageSieve client and the Sieve generator
internal/submit/    The transactional outbox: SMTP, undo window, Sent APPEND
internal/index/     Search backend behind an interface
internal/crypto/    AES-256-GCM for stored credentials
web/                The PWA — React + TypeScript (see web/README.md)
deploy/             The pilot deployment (see deploy/README.md)
docs/               ADR, specifications, research, gate reports, spikes (see docs/README.md)
tools/              corpuscheck (a CI guard), migrate
testdata/           The pathological MIME corpus: 110 cases plus a manifest
spikes/             Validation spikes, separate Go modules, kept for the record
```

Each `internal/*/doc.go` documents that package's purpose and points at the
section of the specification that defines its contract. Start there.

## Documentation

[docs/README.md](docs/README.md) is the index: what each kind of document is and
why it exists. The short version:

| Document | Content |
|---|---|
| [ADR-001 — Architecture](docs/adr/ADR-001-arquitectura.md) | The accepted architecture decision |
| [The Gmail canon](docs/research/06-gmail-canon.md) | What "Gmail-class" means, cited to Google's own documentation |
| [L3 — Gmail-class plan](docs/specs/L3-gmail-class-plan.md) | The eleven-epic program, its arbitrations, and what was deferred |
| [Final gate report](docs/reports/gate-final-gmail-class.md) | The independent audit that closed the program |
| [Phase 0 synthesis](docs/research/00-sintesis-fase0.md) | Audited research synthesis and arbitrations |
| [Spike reports S1–S4](docs/spikes/) | What was tested, and what was found |

> **A note on language.** The research documents, the ADR and the
> specifications under `docs/` are written in **Spanish** — this project started
> inside a Spanish-speaking team and those documents carry that heritage. The
> **code, comments, commit messages, issues and all public documentation are in
> English**, and each package's `doc.go` restates its contract in English.
> Translations of the design documents are planned. You do not need Spanish to
> contribute.

## Contributing

Contributions are welcome — read [CONTRIBUTING.md](CONTRIBUTING.md) first. For
anything beyond a typo, open an issue before writing code: there is an accepted
architecture, and a conversation is cheaper than a rejected pull request.

Security vulnerabilities go through a private advisory, never a public issue:
see [SECURITY.md](SECURITY.md).

## License

[AGPL-3.0](LICENSE) © NU Desarrollos Conscientes

AGPL is deliberate. Anyone who runs a modified Moov as a network service has to
share those modifications. Webmail is exactly the kind of software that gets
forked into a proprietary hosted product, and this license is how that stays
impossible.
