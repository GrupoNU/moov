# L2 — Accounts API and delegated sign-in: the published contract

> Status: **PUBLISHED FOR CONSUMERS 2026-09-15, ahead of the implementation.** This is the
> wire contract for epics M1 (service accounts + accounts API) and M2 (delegated sign-in) of
> `docs/briefs/2026-09-14-corppass-event-mailboxes.md`, frozen so an external portal can be
> built against a mock while Moov builds the real thing. The machine-readable half is
> `docs/specs/openapi-accounts-and-delegated.yaml` (OpenAPI 3.1, with response examples for
> every status). Where the two disagree, THIS document wins and the YAML has a bug.
> Changes after publication are logged in §9; a consumer only needs to re-read that section.
>
> Nothing in this contract names a consumer. "Service account", "delegated issuer",
> "read-only account" are general Moov features, usable by any installation, documented for
> the operator in `deploy/README.md` (epic M3).

## 1. What this contract is for

An external system (a portal, a CRM, an event platform) needs two things from a Moov
installation:

1. **To manage mailboxes of ONE domain without ever touching Mailcow**: create a mailbox
   for a resource it owns, read its usage, suspend it, move it into a read-only retention
   phase, export it, delete it. This is the **accounts API** (§2), authenticated with a
   **service-account key** the Moov operator issues for that domain.
2. **To open Moov for one of those mailboxes without a password**: the external system signs
   a short-lived JWT, the browser lands on Moov with it in the URL fragment, and Moov turns it
   into a session. This is **delegated sign-in** (§3), enabled per host by configuring a
   **delegated issuer**.

The two are independent. An installation may enable either without the other. A service
account can never read mail; a delegated session can never manage accounts.

## 2. The accounts API

### 2.1 Authentication and the no-oracle rule

Every request carries `Authorization: Bearer <service-account key>`. The key is opaque to the
client (today `msa1_` + 43 base64url characters), shown once at creation, stored hashed, bound
to **one domain** and a **scope set**. Scopes in this version: `accounts:read` (GET routes)
and `accounts:write` (everything; implies read).

The key is issued and managed by the operator:

```
moovctl service-account create -domain eventos.example.test -scopes accounts:write -name "portal"
moovctl service-account list
moovctl service-account revoke -id sa_…
```

**The no-oracle rule.** Whatever the caller is not entitled to see answers the same
`404` with the same body, byte for byte, as a route that does not exist:

```http
HTTP/1.1 404 Not Found
Content-Type: application/problem+json

{"type":"about:blank","status":404,"detail":"not found"}
```

That covers: the accounts API is off on this installation (no Mailcow write key — see the
erratum in §9; the draft named a `MOOV_ACCOUNTS_API` variable that was never built), no
`Authorization` header, a malformed or revoked key, a key without the
needed scope, an address whose domain is not the key's domain, and an address that does not
exist. A consumer therefore cannot distinguish "wrong domain" from "no such mailbox" — that is
the point (same policy as the brand-admin API, where a non-admin gets the generic 404).

### 2.2 Error vocabulary

Three body shapes, selected by status, the same ones the existing brand-admin API uses:

| Status | Body | When |
|---|---|---|
| 400 | `{"field": "…", "reason": "…"}` | A field is invalid. Only the FIRST offending field is reported; `field` is the JSON name, dotted when nested (`limits.sendPerDay`), empty when the body itself is not a JSON object. Unknown fields are refused (`"reason": "unknown field"`), so a typo is a 400, not a silent no-op. |
| 401 / 403 / 404 / 410 / 500 | RFC 7807 `application/problem+json`, `type` always `about:blank` | 401/403 only exist on the delegated routes (§3); on the accounts API everything unauthorized is 404. |
| 409 | `{"reason": "…", "state": "deleting"}` | The operation is not allowed in the account's current state. In this version the only such state is `deleting`. |
| 413 / 415 | `{"reason": "…"}` | Body over 16 KiB; `Content-Type` not `application/json`. |
| 429 | `{"reason": "…"}` + `Retry-After` | Budget exhausted: 120 requests per minute per key, burst 30. |
| 502 | `{"reason": "…", "upstream": "mailcow"}` | Mailcow answered and refused. `reason` is Moov's summary, never the raw upstream body, never a credential. Nothing was left half-done: Moov rolls back what it created (the same guarantee `provision` gives today). |
| 503 | `{"reason": "…", "upstream": "mailcow"\|"dovecot"}` + `Retry-After` | Mailcow (or Dovecot, for the validation login) did not answer. Nothing changed; retry. |

`X-Request-Id`: if the client sends one (≤ 64 chars, `[A-Za-z0-9._-]`) it is echoed, otherwise
one is generated; either way it appears in the response and in Moov's audit line for the call,
so the consumer's own log and Moov's audit can be joined.

### 2.3 The account resource

```json
{
  "address": "expo-diseno-2026@eventos.example.test",
  "domain": "eventos.example.test",
  "name": "Expo Diseño 2026",
  "state": "active",
  "readOnly": false,
  "suspended": false,
  "quota": { "limitMB": 2048, "usedBytes": 183502848, "messages": 1274 },
  "limits": { "sendPerDay": 300, "recipientsPerMessage": 50, "attachmentMB": 25 },
  "sync": { "state": "ready", "lastSyncAt": "2026-10-22T09:41:07.002Z", "messages": 1274 },
  "lastAccessAt": "2026-10-22T09:40:55.310Z",
  "readOnlySince": null,
  "suspendedAt": null,
  "deletingSince": null,
  "createdAt": "2026-10-01T14:03:12.418Z",
  "updatedAt": "2026-10-01T14:03:12.418Z"
}
```

- `state` is the **derived headline** a portal card shows, with precedence
  `deleting` > `suspended` > `readonly` > `active`. The underlying facts are the two
  booleans: a suspended read-only account shows `state: "suspended"` with
  `readOnly: true`, and `resume` takes it back to `readonly`, not to `active`. This is why
  the facts are separate fields: the portal never has to remember what the account was
  before a suspension.
- `quota.usedBytes` and `quota.messages` are what Mailcow reports (real disk usage);
  `sync.messages` is what Moov's store holds. They differ during the initial sync and by
  a small margin at any moment; the portal shows `quota`.
- `lastAccessAt` is the last authenticated request by the mailbox, any scheme (Basic or
  delegated session). It is the field a retention job would use for "is anyone still
  reading this".
- Timestamps are RFC 3339 UTC with milliseconds. `*MB` are mebibytes, `*Bytes` are bytes.

### 2.4 State machine

```
                 POST /admin/accounts
                         │
                         ▼
      ┌───────────── active ─────────────┐
      │                │                 │
      │ suspend        │ readonly        │ DELETE
      ▼                ▼                 ▼
  suspended ◄──── readonly ──────────► deleting ──(purge)──► 404
      │  resume        │ suspend/resume
      └────────────────┘
```

| Transition | Route | Idempotent | Effect in Mailcow | Effect in Moov |
|---|---|---|---|---|
| create | `POST /admin/accounts` | yes, by `address` (200 with the current resource, body differences ignored) | mailbox created with a random password that is discarded; quota + rate limit set | provisioned (scoped app password, AES-256-GCM), identity name seeded, initial sync started, host brand inherited |
| update | `PATCH /admin/accounts/{a}` | yes | name, quota, rate limit updated | identity name updated; Moov-enforced limits updated |
| suspend | `POST …/suspend` | yes | mailbox inactive (no IMAP/SMTP) | every session and token revoked; sync worker stopped (`sync.state: paused`); next request by the browser fails |
| resume | `POST …/resume` | yes | mailbox active | sync restarted; returns to `readonly` if `readOnly` was true, else `active` |
| readonly | `POST …/readonly` | yes; **one-way in this version** | the account's app password is **re-issued without SMTP** (`protocols: ["imap_access","sieve_access"]`), the old one deleted; `smtp_access: 0` is also set but NOT relied on — F0 measured that Mailcow saves it and still accepts submission AUTH (`functions.auth.inc.php` skips the check when the service is `NONE`; no Postfix SQL map reads it) | `EmailSubmission/set` refused with a clear `forbidden`-class error; JMAP session and the delegated session carry `readOnly: true` so the client hides compose/reply/forward; drafts still open. Two independent locks: the UI one explains, the credential one enforces even for a non-Moov client |
| delete | `DELETE /admin/accounts/{a}` with `{"confirm": "<a>"}` | no (202 once; then 409 while deleting; then 404) | mailbox deleted (synchronous API answer, app passwords cascade — F0 verified); **maildir removal on disk not yet verified** for a mailbox that received mail, so `deleting` must not be assumed instantaneous until F3/F5 closes it | sessions and tokens revoked immediately; store rows and blobs purged in the background |
| export | `POST …/export` / `GET …/export` | POST is idempotent while a job is pending/running | — | see §2.6 |

Every transition writes one audit line: `actor` (the service account's id and name),
`action`, `address`, `result`, `requestId`, and the optional `reason` the caller supplied
in the body. Recreating an address after deletion is allowed and is an audited event
(`action: create, note: recreated`) — never silent (gate criterion 7).

### 2.5 Field rules

- `address`: lower-cased by the server; local part `^[a-z0-9]([a-z0-9._-]{0,62}[a-z0-9])?$`
  with no consecutive dots; the domain MUST equal the key's domain (else 404, per §2.1).
  Maximum 254 characters.
- `name`: 1–128 characters, no control characters. Becomes the Moov identity's name and
  the Mailcow mailbox name.
- `quotaMB`: 64 … `MOOV_ACCOUNTS_MAX_QUOTA_MB` (default 10240). Default 2048.
- `limits.sendPerDay` (default 300): applied as the Mailcow per-mailbox rate limit —
  **F0 confirmed** `POST /edit/rl-mbox` with `{"rl_value": 300, "rl_frame": "d"}` on our
  version (frames `s/m/h/d`). A domain-level limit is inherited by every new mailbox
  (`rl_scope: "domain"`), so Moov writes the per-mailbox value only when it differs from the
  requested one; the resource always reports the effective value.
  `limits.recipientsPerMessage` (default 50) and `limits.attachmentMB` (default 25): enforced
  by **Moov at submission** (`EmailSubmission/set` and the upload endpoint), because Mailcow
  has no per-mailbox knob for them; Mailcow's global Postfix limits still apply and this
  API never raises them.
- `DELETE` requires a JSON body `{"confirm": "<address>"}` equal to the path address after
  lower-casing. A client whose HTTP stack strips DELETE bodies is broken; the contract does
  not offer a query-string alternative because a confirmation that can be pasted into a URL
  is not a confirmation.

### 2.6 Export

An export is a background job producing a zip: one `.eml` per message under its mailbox
path (`INBOX/`, `Sent/`, …, IMAP names with `/` as separator), plus `manifest.json`:

```json
{
  "account": "expo-diseno-2026@eventos.example.test",
  "generatedAt": "2027-01-15T10:01:48.221Z",
  "messages": 1391,
  "mailboxes": 6,
  "entries": [
    { "path": "INBOX/00001.eml", "sha256": "…", "bytes": 48213, "receivedAt": "…", "messageId": "<…>" }
  ]
}
```

- `POST /admin/accounts/{a}/export` → `202` with the job. At most one job per account at a
  time: while one is `pending`/`running`, another POST returns `202` with **that** job. A
  `ready` export stays downloadable for **7 days**; a POST after that starts a fresh one.
  Works in every state except `deleting`.
- `GET /admin/accounts/{a}/export` → `200` with the latest job. `status: "none"` (a 200)
  means no export was ever requested — 404 keeps its single meaning. Poll every 5–15 s.
- When `ready`, `download.url` is **absolute, signed** (HMAC over id + expiry, key derived
  from the master key), valid **24 h**, and needs **no Authorization header**: it can be
  handed to a browser. It is served by `GET /admin/exports/{exportId}?exp=…&sig=…` as
  `application/zip` with `Content-Disposition: attachment`. A tampered, foreign-host or
  expired signature answers 404; a purged export answers 410.
- `manifest.sha256` in the API response is the SHA-256 of the zip file itself; the
  per-message hashes are inside the zip. Gate criterion 6 ("all messages, counts and hashes
  against the store") is verified by comparing `entries` with Moov's `messages` table.

Why POST + GET and not the single `GET …/export` the L2 spec sketched: a GET that starts
work in the background is neither cacheable nor safely retryable, and a portal's retry
policy would create jobs it did not mean to. The spec's intent (background job, signed
URL) is intact; only the verb split is new. Recorded as deviation §8-D1.

### 2.7 Worked examples

Create (first time):

```http
POST /admin/accounts HTTP/1.1
Host: mail.example.test
Authorization: Bearer msa1_…
Content-Type: application/json
X-Request-Id: portal-evt-8812-create

{"address": "expo-diseno-2026@eventos.example.test", "name": "Expo Diseño 2026"}
```
```http
HTTP/1.1 201 Created
Location: /admin/accounts/expo-diseno-2026@eventos.example.test
X-Request-Id: portal-evt-8812-create
Content-Type: application/json; charset=utf-8

{ "address": "expo-diseno-2026@eventos.example.test", "state": "active", "sync": {"state": "initial", …}, … }
```

Create again (a retry, or the portal does not know whether it succeeded): `200` with the same
resource, nothing changed. Create with the wrong domain: `404 not found`. Create with a bad
slug: `400 {"field":"address","reason":"local part must match … with no consecutive dots"}`.

Read-only, then export, then download:

```
POST …/readonly  {"reason":"event closed 90 days ago"}      → 200 state=readonly
POST …/export                                              → 202 status=pending
GET  …/export   (repeat)                                   → 200 status=running progress=640/1391
GET  …/export                                              → 200 status=ready download.url=https://…/admin/exports/exp_…?exp=…&sig=…
GET  https://…/admin/exports/exp_…?exp=…&sig=…   (no auth) → 200 application/zip
```

Delete:

```http
DELETE /admin/accounts/expo-diseno-2026@eventos.example.test HTTP/1.1
Authorization: Bearer msa1_…
Content-Type: application/json

{"confirm": "expo-diseno-2026@eventos.example.test"}
```
`202` with `state: "deleting"`; `GET` answers `200 deleting` during the purge and `404`
afterwards. `{"confirm": "other@…"}` → `400 {"field":"confirm","reason":"must repeat the address being deleted"}`.

## 3. Delegated sign-in

### 3.1 Flow

```
portal backend ──(signs JWT, 5 min)──► browser ──GET /auth/delegated#token=<jwt>──► Moov PWA
                                                                                     │ strips the fragment,
                                                                                     │ POST /auth/delegated/exchange {"token"}
                                                                                     ▼
                                                                                   moovd ──verifies──► session token (12 h)
                                                                                     │
                                                          PWA stores it in sessionStorage, uses `Authorization: Bearer` everywhere
```

1. The portal's backend builds and signs the JWT (§3.2) and sends the browser to
   `https://<moov-host>/auth/delegated#token=<jwt>` — **fragment, never query**: the fragment
   is not sent to the server, so it reaches no access log, no proxy log and no `Referer`.
2. The PWA shell serves that route like any other SPA route. The client reads
   `location.hash`, immediately replaces the URL (`history.replaceState`) so the token is gone
   from the address bar and history before anything else runs, and calls the exchange.
3. `POST /auth/delegated/exchange` returns a **Moov session** (§3.4). The PWA stores it the
   way it stores Basic credentials today (sessionStorage, tab-scoped) and sends it as
   `Authorization: Bearer <session>` on every request. Everything downstream — the JMAP
   session object, push/blob tokens, uploads — works unchanged, because the Bearer path is
   added beside Basic in the server's authenticator and the PWA's HTTP layer already sends
   `Authorization`.
4. The classic login screen keeps existing: a host with no issuer configured behaves exactly
   as today, and `/auth/delegated` on such a host answers the generic 404 from the API and
   a "this link is not valid here" screen from the PWA.

### 3.2 The token profile

Header:

```json
{"alg": "EdDSA", "kid": "cp-2026-09", "typ": "JWT"}
```

`alg` MUST be `EdDSA` (Ed25519) or `RS256`; anything else — `none`, HS\*, ES\* — is refused
before the signature is looked at. `kid` is required and must match a key in the issuer's
JWKS.

Claims:

| Claim | Rule |
|---|---|
| `iss` | Exactly one of the issuers configured for THIS host. String equality. |
| `aud` | The Moov host the browser reaches, e.g. `mail.example.test` (no scheme, no port). A string, or an array containing it. |
| `sub` | The mailbox address, lower-case. Must be a provisioned account of this host. |
| `iat` | Required. |
| `exp` | Required. `exp − iat ≤ 300` s and `exp > now − 30` s (30 s clock skew each way). Tokens that live longer are refused even if not yet expired. |
| `nbf` | Optional; honoured with the same skew. |
| `jti` | Required. Unique per token, ≥ 128 bits of entropy (a UUIDv4 is fine). Moov remembers every accepted `jti` until its `exp`: a second presentation is refused. |
| `purpose` | Required. `"login"` for the exchange, `"revoke"` for §3.6. A token is valid for exactly one route. |

No other claims are read. A `name` claim, for instance, is ignored: the display name comes
from the account (the accounts API set it), not from the token.

Size limit 4096 bytes. The token is verified with the issuer's **JWKS** fetched from the
configured HTTPS URL: cached 10 minutes; on an unknown `kid` the JWKS is re-fetched at most
once per 60 s (so a key rotation is picked up immediately without letting a flood of bad
`kid`s hammer the issuer). If the JWKS cannot be fetched and no cached key matches, the
exchange answers `503` with `Retry-After` — the only case where the issuer's availability
shows through.

JWKS requirements: `application/json`, keys with `kid`, `use: "sig"`, `alg`, and either
`kty: "OKP", crv: "Ed25519"` or `kty: "RSA"` (≥ 2048 bits). Publish the NEXT key before
signing with it, keep the previous one for at least one token lifetime after rotation, and
never reuse a `kid`.

### 3.3 Issuer configuration (operator side)

```
MOOV_DELEGATED_ISSUERS='[{"host":"mail.example.test","issuer":"https://id.example.test","jwksUrl":"https://id.example.test/.well-known/jwks.json"}]'
```

`host` is what the browser reaches and what `aud` must equal; several issuers may serve one
host and one issuer may serve several hosts (one entry each). Unset ⇒ the feature does not
exist on any host (404 everywhere under `/auth/delegated/*`).

### 3.4 The exchange and the session

```http
POST /auth/delegated/exchange HTTP/1.1
Host: mail.example.test
Content-Type: application/json

{"token": "eyJhbGciOiJFZERTQSIs…"}
```
```http
HTTP/1.1 200 OK
Cache-Control: no-store
Content-Type: application/json; charset=utf-8

{
  "tokenType": "Bearer",
  "sessionToken": "mds1_9vXk2Qm7Lp4Rt8Wz1Yc3Nb6Hd0Jf5Sg2Va7Ke4Mu9Xq1Zr",
  "expiresAt": "2026-10-22T21:40:55.310Z",
  "renewAfter": "2026-10-22T20:40:55.310Z",
  "absoluteExpiresAt": "2026-10-29T09:40:55.310Z",
  "account": { "address": "expo-diseno-2026@eventos.example.test", "name": "Expo Diseño 2026" },
  "readOnly": false,
  "jmap": { "sessionUrl": "/.well-known/jmap" }
}
```

- The session token is opaque (`mds1_…`), random, stored hashed server-side with the account
  id, the issuer and the expiry. **TTL 12 h**, renewable (§3.5) up to an **absolute lifetime
  of 7 days** from the exchange (`MOOV_DELEGATED_SESSION_MAX`, default `168h`); after that
  the user goes through the portal again. Revocable (§3.6). Account state is re-checked from
  the store on every request, exactly as the Basic path does: a suspension takes effect on
  the very next request, not after a TTL.
- `readOnly` mirrors the account's retention phase so the client can hide compose before it
  has even fetched the JMAP session (which carries the same flag).
- The token is accepted wherever Basic is accepted today: `/.well-known/jmap`, `/jmap/api`,
  `/jmap/upload/*`, `/jmap/token` (so push and downloads keep working), the forwarding
  verification, the image-proxy signing route and the brand-admin routes. It is **never**
  accepted in a query string; the token-in-query set stays exactly `push` and `blob` scoped
  tokens minted by `/jmap/token`.

Errors, with the rule that a refusal of the TOKEN says nothing about why, while a refusal of
the ACCOUNT says which screen to show:

| Status | Body | Meaning |
|---|---|---|
| 400 | `{"field":"token","reason":"required"}` | No token / not a JSON object. |
| 401 | `{"type":"about:blank","status":401,"detail":"invalid delegated token"}` | Bad signature, unknown issuer for this host, unknown `kid`, wrong `aud`, expired, too-long lifetime, `nbf` in the future, replayed `jti`, wrong `purpose`, malformed. **One message for all.** The PWA shows "this link has expired or is not valid; open the mail again from the portal". |
| 403 | problem + `"code":"notProvisioned"` | Signature fine, mailbox not provisioned in Moov. The PWA shows the existing not-provisioned screen. |
| 403 | problem + `"code":"suspended"` / `"disabled"` | Signature fine, account cannot be used. |
| 404 | generic | No issuer configured for this host. |
| 429 | `{"reason": …}` + `Retry-After` | 30 exchanges per minute per client IP. |
| 503 | `{"reason": …}` + `Retry-After` | JWKS unreachable and no cached key matches. |

### 3.5 Renewal and logout (client side)

- `POST /auth/delegated/renew` with `Authorization: Bearer <session>` → a NEW session
  (same shape). The old token stays valid for **60 s** so in-flight requests do not fail.
  The PWA renews when `renewAfter` passes and on wake-up after sleep. Refused with 401
  once `absoluteExpiresAt` is reached.
- `POST /auth/delegated/logout` with the session → 204, always (an already-dead session
  answers 204 too). Revokes the session and the push/blob tokens it minted.

### 3.6 Revocation by the issuer

"The user signed out of the portal" must end the Moov session too. The portal's backend
signs a token with the §3.2 profile and `purpose: "revoke"` and posts it:

```http
POST /auth/delegated/revoke
Content-Type: application/json

{"token": "eyJhbGciOiJFZERTQSIs…"}          →  200 {"revoked": 2}
```

Every session of `sub` that was created through THAT issuer is revoked (sessions from another
issuer, or Basic logins, are untouched). A revoke token cannot be exchanged for a session; a
login token is not accepted here. Idempotent; the count may be 0. Why a JWT rather than the
service-account key: delegated sign-in must work on installations that never issued a service
account, and the issuer already owns a signing key — one mechanism, one trust anchor.
`suspend` on the accounts API also revokes everything (§2.4) for the cases where the portal
does have a key.

### 3.7 What the PWA must do (M2 acceptance, for the record)

- Read the fragment and erase it (`history.replaceState`) before any other code runs;
  never log it; never put it in a `fetch` URL.
- Store the session exactly where Basic credentials live today (`web/src/auth/session.ts`
  is the one file that changes shape), and send `Authorization: Bearer`.
- Renew per §3.5; on 401 from any route, drop the session and show the "open again from
  the portal" screen — never the classic login form, which would invite the user to type a
  password they do not have.
- With `readOnly: true`: no Redactar, no reply/forward, drafts open read-only, a visible
  explanation in the reader ("Esta casilla está en modo de solo lectura") — gate criterion 5.

## 4. Security properties (both features)

- A service-account key can create, suspend, export and delete mailboxes of its domain and
  **read no mail**: the export is the only route that touches content, and it produces a
  file for the account's owner, never a JSON body with messages.
- A delegated session is the mailbox user's session: it can read and write mail like a
  password login and **manage no accounts** (the accounts API ignores session tokens; they
  answer the generic 404 there).
- Keys and tokens never appear in URLs (except the signed export download, which is a
  capability with a 24 h life and no access beyond one zip), never in logs (the request logger
  already never logs query strings or `Authorization`), never in error bodies.
- Every write is audited on Moov's side; the consumer is expected to audit on its side too
  (spec §5), and `X-Request-Id` joins the two.
- Mailcow's write key (`MOOV_MAILCOW_WRITE_KEY`) is the new trust boundary: Mailcow does not
  scope keys by domain, so Moov checks the domain BEFORE every call and refuses anything
  outside the service account's domain. That check is pinned by test and is the reason the
  API is per-domain and not per-installation.

## 5. Building against a mock

The YAML carries a complete example for every response, so any OpenAPI mock server
reproduces the contract. With Stoplight Prism:

```
npx @stoplight/prism-cli mock docs/specs/openapi-accounts-and-delegated.yaml --port 4010
curl -s -H "Authorization: Bearer msa1_mock" -H "Content-Type: application/json" \
     -d '{"address":"expo-diseno-2026@eventos.example.test","name":"Expo Diseño 2026"}' \
     http://127.0.0.1:4010/admin/accounts
```

Named examples select the scenario: `Prefer: example=readonly` on `GET /admin/accounts/{a}`,
`Prefer: code=404` to exercise the no-oracle path, `Prefer: example=ready` on `GET …/export`.
The mock does not enforce the state machine or the JWT rules — it answers by example — so
the consumer's own tests must cover: idempotent create (200 vs 201), the 404-on-everything
policy, 409 while deleting, polling an export to `ready`, and the 401/403 split of the
exchange. The real server will be available on the pilot host for integration once M1/M2
ship; the mock is for building, the pilot is for verifying.

## 6. Acceptance criteria this contract turns into tests (M1/M2)

M1: (a) scope — a key of domain A answers 404 for any address of domain B, byte-identical
to a missing route; (b) idempotency — two identical creates give 201 then 200 with equal
bodies and ONE Mailcow mailbox; (c) rollback — a Mailcow failure after mailbox creation
leaves no Moov account and no orphaned app password; (d) audit — one line per write with
`actor, action, address, result, requestId`; (e) read-only — `EmailSubmission/set` refused
and the JMAP session carries `readOnly`; (f) suspend — a live session gets 401/403 on its next
request; (g) delete — sessions revoked, Mailcow mailbox gone, store and blobs purged, GET
404, recreation audited; (h) export — manifest counts and hashes equal the store; download
URL unusable after `exp`, after tampering, and after purge (410); (i) Mailcow client against
a fake plus one integration test against the real Mailcow when `MOOV_MAILCOW_TEST_*` is set.

M2: (a) the verification matrix — every row of §3.2 refused with the single 401 message, and
the good token accepted; (b) replay — the same `jti` twice → 401; (c) `aud` of another host
→ 401; (d) unprovisioned `sub` → 403 `notProvisioned`; suspended → 403 `suspended`; (e) renew
past `absoluteExpiresAt` → 401; (f) issuer revoke ends the session on the next request and
leaves Basic sessions alone; (g) the Bearer-accepting route set is pinned by test and equals
the Basic set; (h) the token-in-query set is unchanged; (i) the PWA strips the fragment
before any network call (jsdom test asserting `location.hash === ""` and no request URL
contains the token).

## 7. F0 answers (VPS_Mail, 2026-09-15) and what they changed

Answered in `docs/briefs/2026-09-15-vpsmail-nota-dominio-y-mailcow-errores.md` §5 against
Mailcow 2026-07a on the real installation. **Nothing on the wire changed**; two internal
mechanisms did.

1. **Per-day rate limit: YES** (`rl_frame: "d"`). `sendPerDay` rides Mailcow; the outbox
   plan B is dropped (§2.5).
2. **Mailbox DELETE: synchronous API answer, app passwords cascade; maildir removal on
   disk UNVERIFIED** (the test mailbox never received mail). `deleting` is not promised to
   be instantaneous; F3/F5 close it against a mailbox with content (gate criterion 7).
3. **`smtp_access: 0` does NOT block submission** — measured: AUTH on 465 still answers
   `235`. Read-only therefore re-issues the app password without SMTP (§2.4); the JMAP
   refusal stays as the explaining lock. `rl_value: 0` as an MTA-level block is unverified
   and not used.
4. **IP allow-list on API keys: YES, strict**, with a distinct error
   (`api access denied for ip <IP>`, which names the IP Mailcow sees). Moov's client
   reports it apart from `authentication failed`: different cause, different fix.

Two more F0 facts the M1 client is built on (note §3): Mailcow reports **almost every
failure inside an HTTP 200** — `type: "error"` (transport/auth, single object) and
`type: "danger"` (operation failures, inside the result array, `msg` string OR array) are
BOTH failures, an empty body is a failure, and `{}` on a GET means "does not exist" and is
indistinguishable from a silently failed authentication. The client validates the key at
startup and never infers "no such mailbox" from a `{}` it cannot trust — otherwise the
idempotent create of §2.4 would mint duplicates. The API does not throttle itself; the
budget in §2.2 is Moov's.

## 8. Deviations from the L2 integration spec (§4.1/§4.2), for the record

- **D1 — Export is POST + GET**, not a single GET (§2.6 says why).
- **D2 — `PATCH /admin/accounts/{a}`** added for name/quota/limits, so the idempotent POST
  never has to "also update".
- **D3 — State is `state` + `readOnly` + `suspended`**, not one enum, so suspend/resume
  round-trips a read-only account correctly.
- **D4 — Revoke is authenticated with a `purpose: "revoke"` JWT** from the issuer, not with a
  service-account key, so delegated sign-in stands alone.
- **D5 — Renew/logout routes** exist for the client (`renewable, revocable` in the spec
  implied them; here they have names and rules).
- **D6 — `recipientsPerMessage` and `attachmentMB` are enforced by Moov**, not Mailcow (no
  per-mailbox knob exists); the spec's "applied in Mailcow" holds for quota and sendPerDay.

## 8b. Open decision: changing an address without losing the mailbox

Raised by the first consumer (2026-09-17) against a real product case: an event edition
wants to move from one address to the next year's while keeping two years of conversation,
and without breaking signage already printed with the old one. `PATCH` covers `name`,
`quotaMB` and `limits`; the address is the resource key, so today the only answers are
"reuse the address as it is" or "a new, empty mailbox".

**Director's arbitration: ALIAS, not rename.** The mailbox keeps its identity and gains a
second address that also delivers to it. Deferred to a later phase — it is not in M1 — but
the shape is decided now so nobody builds against a different one:

- **Why not rename.** The address is what makes `POST /admin/accounts` idempotent (§2.4).
  Moving it turns the one operation a portal retries blindly after a timeout into an
  operation whose key can change underneath it, and an idempotent create whose key moves is
  not idempotent. A rename also silently breaks every message already sent to the old
  address, which is precisely the failure the consumer flagged.
- **Why alias works.** Nothing bounces, the resource key never moves, and the old address
  keeps receiving for as long as the operator wants. Mailcow supports aliases natively
  (verified on our installation: `GET /api/v1/get/alias/all` returns live rows), so this is
  wiring, not new mail infrastructure.
- **What it will need when built:** aliases as a sub-resource of the account
  (`POST`/`DELETE /admin/accounts/{address}/aliases`), an `aliases` array on the account
  resource, the same domain check as every other route, and an explicit decision about which
  address outbound mail uses (the proposal: the primary, always — a reply that comes from an
  address the recipient has never seen is its own problem).

Until it exists, consumers should offer only the two behaviours that work today, and say so
in their interface. This is recorded here rather than silently deferred because the consumer
correctly identified that the decision belongs to the contract, not to them.

## 9. Change log

- 2026-09-15 — `1.0.0-draft.1` published. Consumers may build against it; breaking changes
  before M1/M2 ship will bump the draft number and be listed here with a migration note.
- 2026-09-16 — **Erratum, no wire change.** §2.1 named `MOOV_ACCOUNTS_API` as a switch for
  the feature. **That variable was never built and does not exist.** The accounts API's only
  switch is the Mailcow write key (`MOOV_MAILCOW_WRITE_KEY` or `MOOV_MAILCOW_WRITE_KEY_FILE`):
  absent, `buildAccountsAPI` returns nothing and every route answers the generic 404 —
  exactly the behaviour §2.1 describes, reached by one condition instead of two. Nothing a
  consumer sees changes; the erratum is recorded rather than edited away because the draft
  was published and built against. Found by the operator-documentation pass, which checked
  every variable it was about to document against the source.
- 2026-09-15 (later) — F0 answers folded in (§7). **No wire change**, draft number kept:
  read-only is enforced by re-issuing the app password without SMTP (§2.4), `sendPerDay`
  confirmed on Mailcow (§2.5), `deleting` not promised instantaneous (§2.4). The mailbox
  domain of the first consumer changed to a root domain; the contract never named it.
