# Contributing to Moov Mail

Thank you for considering a contribution. Moov Mail is the first open source
product of [NU Desarrollos Conscientes](https://gruponu.com), and the quality of
its code, documentation and governance is part of the product rather than an
afterthought. This document explains what that means in practice.

**Please read the status section of the [README](README.md) first.** Moov is
pre-alpha: the sync engine is under construction and nothing is usable yet.
Contributions are welcome, but the codebase moves fast and interfaces are not
stable.

---

## Language

- **Code, comments, commit messages, issues and pull requests: English.**
- Research documents and architecture specifications under `docs/` are currently
  in Spanish, for project-heritage reasons. English translations are planned.
  You do not need Spanish to contribute to the code; the contracts you need are
  restated in English in the package documentation (`internal/*/doc.go`).

## Before you write code

For anything beyond a typo fix, **open an issue first**. Moov has an accepted
architecture ([ADR-001](docs/adr/ADR-001-arquitectura.md)) and an accepted
specification for the current phase
([L2 sync engine](docs/specs/L2-sync-engine.md)) that were derived from four
validation spikes against a real Mailcow. A change that contradicts either is
not necessarily wrong — but it needs a conversation, not a surprise pull
request.

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

You need **Go 1.24+**, **Docker** (for the development database) and **git**.

```sh
git clone https://github.com/GrupoNU/moov.git
cd moov

make db-up        # PostgreSQL 17 on 127.0.0.1:5433
make migrate      # apply the migrations
make ci           # the full local gate: fmt, vet, lint, build, corpus, tests
```

`make help` lists every target. The ones you will use most:

| Target | What it does |
|---|---|
| `make build` | Build `moovd` into `./bin`, with version stamped in |
| `make test` | Full test suite (`-race`) |
| `make test-short` | Skip anything needing external services |
| `make fmt` / `make fmt-check` | Format / verify formatting |
| `make lint` | golangci-lint (install it once with `make lint-install`) |
| `make corpus-check` | Validate the MIME corpus against its manifest |
| `make ci` | Everything CI runs, minus the service-container jobs |

The store tests need a database. They read `MOOV_TEST_DATABASE_URL` and skip
with an explanatory message when it is unset:

```sh
export MOOV_TEST_DATABASE_URL='postgres://moov:moov@localhost:5433/moov?sslmode=disable'
```

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
`index`, `crypto`, `ci`, `docs`).

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
