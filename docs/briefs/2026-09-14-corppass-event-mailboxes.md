# Brief — Moov team: event mailboxes for an external portal (delegated sign-in, accounts API, service accounts)

> Owner: technical director (Moov session). The cross-team specification lives in the
> group's private repo (`docs/director/L2-integracion-moov-corppass.md`). Everything here is
> built as a GENERAL Moov capability, documented in `deploy/README.md`, tested, and usable
> by any installation. CorpPass is the first consumer, not a special case in the code.

> **Contract published 2026-09-15:** `docs/specs/L2-accounts-api-contract.md` (prose, wins on
> conflict) + `docs/specs/openapi-accounts-and-delegated.yaml` (OpenAPI 3.1 with examples).
> M1/M2 implement THAT contract; its §6 lists the acceptance tests, its §8 the deviations
> from the L2 integration spec.

## Scope (three epics)

### M1 — Service accounts + accounts API (size L)
- `moovctl service-account create|list|revoke -domain <d> -scopes accounts:write` → an API
  key (shown once), stored hashed, scoped to ONE domain, with audit and rate limit.
- Routes (authenticated with `Authorization: Bearer <key>`; 404 indistinguishable when the
  feature is off or the key has no scope):
  `POST /admin/accounts`, `GET|DELETE /admin/accounts/{address}`,
  `POST /admin/accounts/{address}/suspend|resume|readonly`, `GET /admin/accounts/{address}/export`.
- Mailcow write client behind an interface in `internal/mailcow`: create/update/suspend/delete
  mailbox, quota, rate limit, app password. Domain allow-list enforced in Moov BEFORE any
  call. Key from env, encrypted at rest with the master key; absent ⇒ feature off.
- Creating an account = Mailcow mailbox (random password, discarded) + Moov provisioning
  (today's `provision` flow) + identity name + the host brand. Idempotent by address.
- Read-only phase: sending blocked by re-issuing the app password WITHOUT SMTP (F0 proved
  `smtp_access:0` does not block submission) and in JMAP (`EmailSubmission/set` refused
  with a clear error); the PWA hides Redactar/reply when the account is read-only (flag
  exposed to the client).
- The Mailcow client follows the F0 error contract (note §3): failures inside HTTP 200,
  both `error` and `danger` families, `msg` string-or-array, `{}` never trusted as
  "absent", key validated at startup, rate limit is Moov's.
- Export: background job producing a zip of EML files from the blob store, signed expiring
  download URL, counts and a sha256 manifest.
- Tests per AC: scope enforcement (another domain ⇒ 404), idempotency, audit lines, Mailcow
  client against a fake plus one integration test against the real Mailcow when the
  `MOOV_MAILCOW_TEST_*` variables are set, read-only refusal, export manifest.

### M2 — Delegated sign-in (size M)
- `MOOV_DELEGATED_ISSUERS`: issuer, JWKS URL, audience. JWT (EdDSA/RS256, `kid`), `aud` =
  host, `sub` = mailbox, `exp` ≤ 5 min, `jti` anti-replay.
- `GET /auth/delegated#token=…` → PWA route that exchanges via `POST /auth/delegated/exchange`
  → Moov session token (bearer, 12 h, renewable, revocable; `POST /auth/delegated/revoke` by
  `sub` for the issuer). The PWA's HTTP layer already sends `Authorization`; add the Bearer
  path next to Basic in `internal/jmaphttp/auth.go` (the token-accepting set is pinned by
  test: extend it deliberately).
- The token must never appear in query strings, logs or Referer; the PWA strips the fragment
  immediately.
- Tests: signature/aud/exp/jti matrix, unprovisioned mailbox ⇒ the existing notProvisioned
  screen, revoke ends the session, Basic unaffected.

### M3 — Operator docs (size S)
- `deploy/README.md`: service accounts, the accounts API, delegated issuers, the Mailcow write
  key and its trust boundary, read-only/export/deletion semantics.

## Rules
- Fable for M1/M2 (security boundaries); Opus for M3. Commit per item; gates as usual (web +
  Go on the VPS); no push without the owner's confirmation; never touch `/opt/moov` outside
  an authorized deploy.
- Nothing consumer-specific in code or strings: "delegated issuer", "service account",
  "read-only account" are the concepts.
