# The Moov documentation

This directory holds the project's reasoning, not its reference manual. If you
want to *run* Moov, go to [`deploy/README.md`](../deploy/README.md); if you want
to work on the PWA, [`web/README.md`](../web/README.md); if you want to
contribute, [`CONTRIBUTING.md`](../CONTRIBUTING.md).

What lives here is the record of **why the software is shaped the way it is** —
the decisions, the evidence they rest on, the specifications derived from them,
and the audits that checked whether the code actually did what was specified.
It is kept in the repository rather than in a wiki because a decision that
cannot be traced from the code is a decision nobody can safely revisit.

> **A note on language.** Most documents in this directory are written in
> **Spanish**, the working language between the project's author and its owner.
> Code, comments, commits, issues and every public-facing document (the root
> README, CONTRIBUTING, SECURITY, and the deploy and web guides) are in
> **English**. The Gmail canon (`research/06`) is in English because it quotes
> English-language sources throughout. You do not need Spanish to contribute:
> each package's `doc.go` restates its contract in English, and the
> specifications' acceptance criteria are mirrored in test names.

---

## `adr/` — the architecture decision

One document, and it is the one to read first.

- **[ADR-001 — Architecture](adr/ADR-001-arquitectura.md)** *(Spanish)* — Why
  Moov is a sync engine with its own store rather than an IMAP-direct client or
  a migration to a JMAP-native server; the stack; how it integrates with Mailcow
  without touching it; the security posture; and the measurable acceptance
  criteria for "Gmail-class" (§6) that every later document is held to.
  Accepted, and the rest of the repository is downstream of it.

## `research/` — the evidence

Nothing in the specifications is allowed to rest on memory or opinion. This is
where the sourcing lives.

- **[06 — The Gmail canon](research/06-gmail-canon.md)** *(English)* — The most
  important document here. It defines what "Gmail-class" actually means, with
  every claim cited to Google-authored documentation retrieved live on a stated
  date. It carries the three-question filter every feature had to pass, the
  catalogue of core versus secondary behaviour, a register of what could *not*
  be sourced (so it funds no decision), and a kill-list of features from other
  webmails that fail the filter, each with its reason.
- **[05 — The Gmail-class surface](research/05-gmail-class-surface.md)**
  *(Spanish)* — The earlier study of a modern JMAP client's surface. Superseded
  as a product criterion by 06; it survives as a catalogue of *mechanisms*
  (how a thing is built, once Gmail has decided that it should exist).
- **[00 — Phase 0 synthesis](research/00-sintesis-fase0.md)** *(Spanish)* — The
  audited synthesis of the four founding studies, with the arbitrations and the
  risk register that fed ADR-001.
- **[01 — Competitive landscape](research/01-competitive-landscape.md)**,
  **[02 — JMAP deep dive](research/02-jmap-deep-dive.md)**,
  **[03 — Mailcow integration](research/03-mailcow-integration.md)**,
  **[04 — Sync engine prior art](research/04-sync-engine-prior-art.md)**
  *(Spanish)* — The four phase-0 studies: why no existing webmail fills this
  gap, why standard JMAP and how to subset it, how to authenticate and deploy
  against Mailcow without modifying it, and the lessons of everyone who built a
  mail sync engine before us.

## `specs/` — what was to be built

Two levels. An **L2** specifies one subsystem in enough detail that its
acceptance criteria become test names. The **L3** is the program that spans
them.

- **[L2 — Sync engine](specs/L2-sync-engine.md)** *(Spanish)* — Epics E1–E8:
  the IMAP layer, the store, the parser cascade, initial and incremental sync,
  the watcher, provisioning, observability. Contains the arbitrations about
  volatile state, the hybrid label model, and the two initial-sync paths.
- **[L2 — JMAP server](specs/L2-jmap-server.md)** *(Spanish)* — Epics J1–J4:
  the read-only JMAP surface, HTTP, authentication. Its closing milestone was a
  third-party JMAP client reading real mail through Moov.
- **[L2 — JMAP write](specs/L2-jmap-write.md)** *(Spanish)* — Writes, mailbox
  mutation, submission with an undo window, and SSE push.
- **[L2 — The PWA](specs/L2-pwa.md)** *(Spanish)* — The client: foundations,
  reading, the secure HTML renderer, writing and sending.
- **[L3 — The Gmail-class plan](specs/L3-gmail-class-plan.md)** *(Spanish)* —
  The eleven-epic program (E0–E11) that took Moov from "a working webmail" to
  "measured against Gmail". It carries the ten director arbitrations
  (GC-1…GC-10), the eight decisions the owner signed (D-1…D-8), and — §6 —
  everything deliberately **not** built, each item with its size, its reason and
  its risk. Read §6 if you want to know what Moov does not do.

## `reports/` — the audits

A specification that is never checked is a wish. Each program closes with an
independent audit, run by an agent that implemented none of the work, verdict by
verdict against a file and line or a test name.

- **[Final gate — Gmail-class](reports/gate-final-gmail-class.md)** *(Spanish)*
  — The audit that closed the L3 program. It is published with its findings
  intact, including two critical ones, because an audit that lists only
  successes is not an audit. It also contains the owner's hands-on test script,
  which doubles as a walkthrough of what the product does.

## `spikes/` — what was tested before it was trusted

Before any product code was written, the four assumptions that would have been
expensive to be wrong about were tested against a real Mailcow. These reports
record the hypotheses, the method and the results — including the results that
changed the plan.

- **[S1 — JMAP over Dovecot](spikes/S1-jmap-sobre-dovecot.md)** *(Spanish)* —
  Can JMAP work over an unmodified Dovecot, third-party client included?
- **[S2 — go-imap against Dovecot](spikes/S2-go-imap-dovecot.md)** *(Spanish)*
  — Do QRESYNC, CONDSTORE, IDLE and NOTIFY behave? (Answer: yes, with a patch
  set we carry; see `upstream/` below.)
- **[S3 — Full-text search benchmark](spikes/S3-benchmark-fts.md)** *(Spanish)*
  — Does PostgreSQL `tsvector`+GIN hold the Gmail-class bar at 5M messages? The
  three configurations it found to be mandatory are enforced by a test.
- **[S4 — The pathological MIME corpus](spikes/S4-corpus-mime.md)** *(Spanish)*
  — 110 deliberately broken messages, committed *before* the parser existed.
  The corpus itself is in [`testdata/mime-corpus/`](../testdata/mime-corpus/).
- **[V1 — Dovecot METADATA](spikes/V1-metadata-dovecot.md)** *(Spanish)* — A
  later verification that corrected an arbitration: it measured the durable
  keyword ceiling at 26 per folder, which is why labels are designed the way
  they are.

## `upstream/` — what we owe back

Notes on the patches Moov carries against its vendored `go-imap/v2`, written so
they can be upstreamed rather than kept.

- [`go-imap-0002-notify.md`](upstream/go-imap-0002-notify.md) — a NOTIFY encoder fix.
- [`go-imap-0003-modified.md`](upstream/go-imap-0003-modified.md) — exposing the
  `MODIFIED` response code.

---

## Reading paths

**"I want to understand the architecture in twenty minutes."**
[ADR-001](adr/ADR-001-arquitectura.md) → the spike results in
[00-sintesis](research/00-sintesis-fase0.md) §4.

**"I want to know what the product does and how good it is."**
[The Gmail canon](research/06-gmail-canon.md) §2 → the
[final gate report](reports/gate-final-gmail-class.md) → §6 of the
[L3 plan](specs/L3-gmail-class-plan.md) for what it deliberately does not do.

**"I am about to change code in `internal/X`."**
That package's `doc.go` → the section of its L2 it names → the tests, which
carry the acceptance criteria in their names.
