# Moov Mail

> A Gmail-class open source webmail for the Mailcow and Dovecot installed base.

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)

**Status: pre-alpha.** The architecture is decided and validated; the engine is
being written. There is nothing to install yet. See
[Status](#status-honestly) for exactly where things stand.

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
migrate their mail server.

## How it works

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
  │   JMAP server   standard, subset by     │
  │                 phases, TestSuite in CI │
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

Design principles, each of which constrains the code:

- **Mailcow is never modified.** Moov runs as a separate Docker stack joined to
  the Mailcow network. Everything goes through IMAP, SMTP, Sieve and the Mailcow
  API. The mail store on disk is never touched — not even read-only.
- **Dovecot is the source of truth.** Moov's store is a cache. Every byte of it
  can be rebuilt from the server, which is what makes it safe to be fast.
- **Standard JMAP, not a homegrown API.** Third-party JMAP clients work against
  Moov, and conformance is verified in CI rather than asserted.
- **Gmail-class is measured, not claimed.** As-you-type search and every user
  action under 100 ms perceived, real push, a full keyboard vocabulary, undo
  send, offline PWA. Those are acceptance criteria with numbers attached.
- **Security first.** Three-layer HTML sanitization (server, client, and a
  sandboxed iframe under a strict CSP with no script execution), proxied remote
  images with SSRF protection, credentials encrypted at rest from the first
  commit, and the user's own password never persisted at all.

## Status, honestly

Moov is **pre-alpha and not usable**. No release, no installation instructions,
no screenshots — because there is no interface to screenshot yet. Here is the
real state:

| | |
|---|---|
| ✅ **Phase 0 research** | Four studies: competitive landscape, JMAP, Mailcow integration, sync-engine prior art. Synthesized and audited. |
| ✅ **Architecture decided** | [ADR-001](docs/adr/ADR-001-arquitectura.md), accepted. |
| ✅ **Four validation spikes, all run against a real Mailcow** | Every load-bearing assumption was tested before any product code was written — see below. |
| ✅ **Phase 1 specification** | [L2 sync engine](docs/specs/L2-sync-engine.md), accepted, with contracts and acceptance criteria per epic. |
| 🔨 **Sync engine** | In development. This is where the work is now. |
| ⬜ **JMAP server** | Specified, not started. |
| ⬜ **PWA frontend** | Not started. |

The spikes are the reason the architecture is more than a hope:

| Spike | Question it answered | Result |
|---|---|---|
| [S1](docs/spikes/S1-jmap-sobre-dovecot.md) | Can JMAP work over an unmodified Dovecot? | Yes — validated end to end against our own Mailcow. |
| [S2](docs/spikes/S2-go-imap-dovecot.md) | Do QRESYNC, CONDSTORE and NOTIFY work with `go-imap/v2`? | Yes, with a patch set we carry and are upstreaming. |
| [S3](docs/spikes/S3-benchmark-fts.md) | Does PostgreSQL full-text search hold up at 5M messages? | Yes, with a specific and now-mandatory configuration. |
| [S4](docs/spikes/S4-corpus-mime.md) | What does pathological MIME do to the parser? | 110 committed test cases, written before the parser exists. |

We would rather publish an honest "not yet" than a roadmap that reads like a
product page.

## Development setup

You need **Go 1.24+**, **Docker** and **git**.

```sh
git clone https://github.com/GrupoNU/moov.git
cd moov

make db-up     # PostgreSQL 17 on 127.0.0.1:5433 (development only)
make migrate   # apply the migrations
make ci        # fmt, vet, lint, build, corpus check, tests
make build     # ./bin/moovd

./bin/moovd -version
```

`make help` lists every target. [CONTRIBUTING.md](CONTRIBUTING.md) has the full
setup, the testing policy and the commit conventions.

Repository layout:

```
cmd/moovd/          The daemon
internal/imap/      The only package that may import go-imap (enforced by lint AND test)
internal/parser/    MIME parsing cascade
internal/store/     PostgreSQL: schema, migrations, the whole query repertoire
internal/blob/      Content-addressed raw messages, refcounted and GC'd
internal/sync/      Orchestration: initial sync, incremental, watcher, reconciler
internal/index/     Search backend behind an interface
internal/crypto/    AES-256-GCM for stored credentials
tools/              corpuscheck (a CI guard), migrate
testdata/           The pathological MIME corpus: 110 cases plus a manifest
spikes/             Validation spikes, separate Go modules, kept for the record
docs/               ADR, specifications, research, spike reports
```

Each `internal/*/doc.go` documents that package's purpose and points at the
section of the specification that defines its contract. Start there.

## Roadmap

1. **Phase 1** — sync engine plus read-only JMAP: a third-party JMAP client can
   read mail through Moov. *(in progress)*
2. **Phase 2** — writes, sending via `EmailSubmission`, SSE push; the Moov PWA
   becomes usable.
3. **Phase 3** — Sieve filters (RFC 9661), quotas, search snippets, web push,
   WebSocket.

## Documentation

| Document | Content |
|---|---|
| [ADR-001 — Architecture](docs/adr/ADR-001-arquitectura.md) | The accepted architecture decision |
| [L2 — Sync engine](docs/specs/L2-sync-engine.md) | Phase 1 specification: contracts, epics, acceptance criteria |
| [Phase 0 synthesis](docs/research/00-sintesis-fase0.md) | Audited research synthesis and arbitrations |
| [Competitive landscape](docs/research/01-competitive-landscape.md) | Why no existing webmail fills this gap |
| [JMAP deep dive](docs/research/02-jmap-deep-dive.md) | Why standard JMAP, subset strategy, IMAP↔JMAP mapping |
| [Mailcow integration](docs/research/03-mailcow-integration.md) | Auth, API, Dovecot capabilities, deployment pattern |
| [Sync engine prior art](docs/research/04-sync-engine-prior-art.md) | Lessons from Nylas, Mailspring, Delta Chat |
| [Spike reports S1–S4](docs/spikes/) | What was tested, and what was found |

> **A note on language.** The research documents, the ADR and the
> specifications under `docs/` are written in **Spanish** — this project started
> inside a Spanish-speaking team and those documents carry that heritage. The
> **code, comments, commit messages, issues and all public communication are in
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
