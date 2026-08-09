<!--
Thanks for contributing to Moov Mail.

Keep the pull request focused: one logical change. A refactor bundled with a bug
fix is two pull requests.

Commit messages follow `type(scope): description` — see CONTRIBUTING.md.
-->

## What this changes

<!-- What does it do, and why is it needed? Link the issue: "Closes #123". -->

## How it was tested

<!--
Be specific. "Tests pass" is not an answer; which test demonstrates the change?

For a bug fix, name the regression test and confirm it fails without the fix —
that is the project's testing policy, not a formality.
-->

## Checklist

- [ ] `make ci` passes locally (fmt, vet, lint, build, corpus-check, tests)
- [ ] Commits follow `type(scope): description` and are signed off (`git commit -s`)
- [ ] Tests were added or updated for this change
- [ ] For a bug fix: a test reproduces the bug and fails without the fix
- [ ] Documentation updated where it applies (package `doc.go`, README, `docs/`)
- [ ] No secrets, credentials or real mail content added anywhere

## Architecture invariants

<!-- Tick these deliberately. They are the rules a reviewer will check first. -->

- [ ] Mailcow is not modified and its mail store on disk is not touched — everything goes through IMAP, SMTP, Sieve or the API
- [ ] Moov's store remains reconstructible from Dovecot
- [ ] `go-imap` is not imported outside `internal/imap`
- [ ] N/A — this change does not touch the engine

## Anything a reviewer should know

<!--
Trade-offs you made, alternatives you rejected, parts you are unsure about, or
follow-up work you deliberately left out. Saying "I'm not sure about X" is
useful, not weak.
-->
