# Moov Mail

> A Gmail-class open source webmail for Mailcow and Dovecot.

**Status: pre-alpha — architecture approved, validation spikes in progress. Not usable yet.**

Moov Mail is the first open source product by [NU Desarrollos Conscientes](https://gruponu.com). It exists to close a gap nobody else is filling: every modern webmail (Bulwark, Twake Mail) requires a JMAP-native server (Stalwart, Apache James), while the huge installed base of **Mailcow / Dovecot** is stuck with architecturally-limited IMAP-direct clients (SOGo, Roundcube, SnappyMail) that can never deliver instant search, real push, offline support, or undo send.

## How it works

Moov Mail is **not** another IMAP-direct client. It ships a **sync engine** that mirrors your Dovecot mailboxes into its own store with a full-text index, and exposes a **standard JMAP API** (RFC 8620/8621) to a fast PWA frontend — while your Mailcow installation stays completely untouched.

```
Browser (React PWA)
      │  JMAP (RFC 8620/8621) + SSE push
      ▼
Moov backend (Go)
  · sync engine  — IMAP CONDSTORE/QRESYNC/IDLE against Dovecot
  · own store    — PostgreSQL metadata + content-addressed blobs + FTS index
  · JMAP server  — subset by phases, JMAP TestSuite in CI
      │  IMAP :143 · SMTP :587 · Sieve :4190 · Mailcow API
      ▼
Your existing Mailcow (Dovecot / Postfix / Rspamd) — unmodified
```

Design principles:

- **Mailcow is never modified.** Moov runs as a separate Docker stack joined to the Mailcow network. Everything goes through IMAP/SMTP/Sieve and the Mailcow API. The mail store on disk is never touched.
- **Dovecot is the source of truth.** Moov's store is a cache — any local data can be rebuilt from the server.
- **Standard JMAP, not a homegrown API.** Third-party JMAP clients work against Moov; conformance is verified in CI.
- **Gmail-class is measured, not claimed:** instant as-you-type search and every user action under 100 ms perceived, real push, full keyboard vocabulary, undo send, offline PWA.
- **Security first:** 3-layer HTML sanitization (server + client + sandboxed iframe with strict CSP), image proxying with SSRF protection, credentials encrypted at rest from day one.

## Project documents

| Document | Content |
|---|---|
| [ADR-001 — Architecture](docs/adr/ADR-001-arquitectura.md) | The approved architecture decision record |
| [Phase 0 synthesis](docs/research/00-sintesis-fase0.md) | Audited research synthesis and arbitrations |
| [Competitive landscape](docs/research/01-competitive-landscape.md) | Why no existing webmail fills this gap |
| [JMAP deep dive](docs/research/02-jmap-deep-dive.md) | Why standard JMAP, subset strategy, IMAP↔JMAP mapping |
| [Mailcow integration](docs/research/03-mailcow-integration.md) | Auth, API, Dovecot capabilities, deployment pattern |
| [Sync engine prior art](docs/research/04-sync-engine-prior-art.md) | Lessons from Nylas, Mailspring, Delta Chat; stack decisions |

> Research documents are currently in Spanish; the codebase and all code-facing docs are in English. English translations of the research will follow.

## Roadmap (high level)

1. **Validation spikes** — jmap-perl against a real Mailcow, QRESYNC/NOTIFY with go-imap v2, 5M-message search benchmark, pathological MIME corpus.
2. **Phase 1** — sync engine + read-only JMAP (a third-party JMAP client can read mail through Moov).
3. **Phase 2** — writes, sending (EmailSubmission), SSE push; the Moov PWA becomes usable.
4. **Phase 3** — Sieve filters (RFC 9661), quotas, search snippets, web push, WebSocket.

## License

[AGPL-3.0](LICENSE) © NU Desarrollos Conscientes
