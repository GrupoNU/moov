# Contributing to Moov Mail

Thank you for considering a contribution. Moov Mail is the first open source
product of [NU Desarrollos Conscientes](https://gruponu.com), and the quality of
its code, documentation and governance is part of the product rather than an
afterthought. This document explains what that means in practice.

**Please read the status section of the [README](README.md) first.** Moov is
pre-1.0: the Gmail-class feature program is complete and running on a single
pilot, but there is no release, no compatibility promise, and interfaces —
including the vendor JMAP capabilities and the database schema — may still
change.

---

## Language

- **Code, comments, commit messages, issues and pull requests: English.**
- Research documents and architecture specifications under `docs/` are currently
  in Spanish, for project-heritage reasons. English translations are planned.
  You do not need Spanish to contribute to the code; the contracts you need are
  restated in English in the package documentation (`internal/*/doc.go`).
  [`docs/README.md`](docs/README.md) indexes everything and says which language
  each document is in.

## Before you write code

For anything beyond a typo fix, **open an issue first**. Moov has an accepted
architecture ([ADR-001](docs/adr/ADR-001-arquitectura.md)) and a specification
per subsystem, each derived from evidence rather than preference: the
[L2s](docs/specs/) for the sync engine, the JMAP server, writes and the PWA, and
the [L3 Gmail-class plan](docs/specs/L3-gmail-class-plan.md) above them. A
change that contradicts one is not necessarily wrong — but it needs a
conversation, not a surprise pull request.

Two documents will save you an argument. The
[Gmail canon](docs/research/06-gmail-canon.md) is the criterion for what belongs
in the product at all, cited to Google's own documentation; §6 of the L3 plan
lists what was deliberately **not** built, with the reason. If you are proposing
a feature, check both first — it may already have been decided, in either
direction.

Two invariants are not negotiable and no pull request may weaken them:

1. **Mailcow is never modified, and its mail store on disk is never touched.**
   Everything goes through IMAP, SMTP, Sieve and the Mailcow API. Mounting or
   writing `vmail` corrupts mailboxes.
2. **Dovecot is the source of truth; Moov's store is a reconstructible cache.**
   Any local state must be rebuildable from the server.

There is also a mechanically enforced architecture rule: **`go-imap` may only be
imported from `internal/imap`.** It is checked by `depguard` in lint and by
`TestGoIMAPIsConfinedToInternalIMAP` in tests. If you need something the
`Client` interface does not expose, extend the interface.

## Development setup

You need **Go 1.24+**, **Docker** (for the development database), **Node 20+**
(for the PWA) and **git**.

```sh
git clone https://github.com/GrupoNU/moov.git
cd moov

make db-up        # PostgreSQL 17 on 127.0.0.1:5433
make migrate      # apply the migrations
make ci           # the full local gate: fmt, vendor check, vet, lint, build, corpus, tests
```

`make help` lists every target. The ones you will use most:

| Target | What it does |
|---|---|
| `make build` | Build `moovd` into `./bin`, with version stamped in |
| `make test` | Full test suite (`-race`) |
| `make test-short` | Skip anything needing external services |
| `make fmt` / `make fmt-check` | Format / verify formatting |
| `make lint` | golangci-lint (install it once with `make lint-install`) |
| `make vendor-check` | Fail if the vendored `go-imap` is missing a patch |
| `make corpus-check` | Validate the MIME corpus against its manifest |
| `make ci` | Everything CI runs, minus the service-container jobs |

### The tests that need a database

The store, blob and sync suites talk to a real PostgreSQL. They read
`MOOV_TEST_DATABASE_URL` and skip with an explanatory message when it is unset —
so a bare `make test` passes without proving anything about them. Run them in
two phases, the way CI does:

```sh
# Phase 1 — everything that needs no external service
make test-short

# Phase 2 — the database-backed suites
export MOOV_TEST_DATABASE_URL='postgres://moov:moov@localhost:5433/moov?sslmode=disable'
go test -race -count=1 -p 1 ./internal/store/... ./internal/blob/... ./internal/sync/...
```

**`-p 1` is not optional here.** Those packages share the one database, and
`blob`'s global GC sweep collides with anything running concurrently against it.
Without it you get failures that look like flakes and are not.

The `internal/imap` integration suite talks to a real Dovecot and is gated the
same way — `make test-imap-integration` names the variables it needs. Its
password must come from the environment: this repository is public and no
credential may ever be written into a file in it.

### The PWA

```sh
cd web
npm install
npm run test       # vitest
npm run typecheck  # tsc -b
npm run lint       # eslint, --max-warnings 0 (jsx-a11y at error level)
npm run build      # tsc -b && vite build
npm run dev        # http://localhost:5173, proxying the API to a running server
```

All four of `test`, `typecheck`, `lint` and `build` run in CI, and `lint`
tolerates no warnings. See [`web/README.md`](web/README.md) for the design
rationale and what `npm run dev` proxies where.

The `spikes/` directory holds separate Go modules — exploratory code kept for
the record, deliberately outside the main module and not held to the product's
standards. Do not add product code there.

## Testing policy

This is the engineering policy of the project and CI enforces it:

- **A bug fix starts with a failing test.** Write the test that reproduces the
  bug, watch it fail, then fix it. A bug fix without a regression test will be
  asked for one.
- **A feature ships with tests for its acceptance criteria.** The specification
  states them; the tests demonstrate them.
- **CI must be green.** No exceptions, no "will fix in a follow-up".

### The MIME corpus is a specification

`testdata/mime-corpus/` holds 110 deliberately pathological messages plus a
manifest recording, for each one, what is wrong with it and what a correct
parser must do. It exists *before* the parser does, by design.

If your change makes a corpus case behave differently, **that is a finding to
examine, not an expectation to edit**. If an expectation genuinely has to
change, the reason belongs in the commit message. The `.eml` files are
byte-exact vectors: `.gitattributes` marks them `-text` and `.gitignore` carries
an explicit negation for them. Both are load-bearing, and `make corpus-check`
verifies they are still doing their job.

## Commit messages

Format: `type(scope): description`

```
feat(sync): resume backfill from the last checkpoint
fix(parser): keep partial bytes when base64 decoding fails
test(store): assert force_custom_plan on a fresh connection
docs(adr): record the label-storage arbitration
```

Types: `feat`, `fix`, `docs`, `refactor`, `chore`, `test`, `perf`, `ci`.
Scope is the package or area (`sync`, `parser`, `store`, `imap`, `blob`,
`index`, `crypto`, `jmap`, `jmaphttp`, `sieve`, `submit`, `pwa`, `deploy`,
`ci`, `docs`).

Write the description in the imperative mood, lower case, no trailing period.
Keep commits atomic: one logical change each. Explain *why* in the body when the
why is not obvious from the diff — that body is what someone reads in two years
while holding a production incident.

## Pull requests

1. Branch from `main`.
2. Make sure `make ci` passes locally.
3. Fill in the pull request template — particularly how you tested the change.
4. Keep the pull request focused. A large refactor mixed with a bug fix is two
   pull requests.

Review is not a formality here. Expect questions, especially about failure modes
and about what happens to a mailbox when your code is wrong.

## Sign-off (DCO)

Moov uses the [Developer Certificate of Origin](https://developercertificate.org/).
It is a lightweight statement that you wrote the contribution, or otherwise have
the right to submit it under the project's license. There is no CLA and you do
not assign copyright to anyone.

Sign each commit off:

```sh
git commit -s -m "fix(parser): keep partial bytes when base64 decoding fails"
```

which appends:

```
Signed-off-by: Your Name <your.email@example.com>
```

By signing off you certify the DCO, whose full text is at the link above.

## License

Moov Mail is licensed under the **GNU Affero General Public License v3.0**. By
contributing, you agree that your contribution is licensed under the same terms.

AGPL-3.0 is a deliberate choice: it means anyone who runs a modified Moov as a
network service has to share those modifications. That is the point.

## Security

Do **not** open a public issue for a security vulnerability. See
[SECURITY.md](SECURITY.md) for private disclosure.

## Code of conduct

Be decent. Assume good faith, critique the code rather than the person, and
remember that the people reading your review are people. Behavior that makes
contributing unpleasant for others is not welcome regardless of technical
contribution.
