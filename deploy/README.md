# Deploying Moov Mail

The pilot deployment: `moovd` (sync engine + JMAP server) and its PostgreSQL
store, joined to Mailcow's Docker network, reachable only over the VPN.

This is the ADR-001 §4 pattern made concrete. Read that section first if you
want the reasoning; this file is the operation.

---

## The rules this stack obeys

These are not deployment preferences. They are the invariants the architecture
depends on, and breaking any of them breaks something that is expensive to
notice and expensive to fix.

1. **Mailcow is never modified.** This stack joins
   `mailcowdockerized_mailcow-network` as an *external* network and talks to
   Mailcow only over IMAP, SMTP, Sieve and its HTTP API. Mailcow's `./update.sh`
   does not know this stack exists and never will.
2. **The vmail filesystem is never mounted.** Not here, not in a debug
   container, not "just to look". Dovecot runs with `maildir_very_dirty_syncs`,
   and a second writer on those files is guaranteed corruption. Every byte of
   mail arrives over IMAP.
3. **Nothing is published publicly.** There is no `ports:` mapping to a public
   interface anywhere in `docker-compose.yml`. The pilot is reached through the
   existing Caddy front, which binds the Tailscale interface only.
4. **PostgreSQL is on a private network.** It is reachable from `moovd` and from
   nothing else — not from Mailcow's network, not from the VPN.

---

## Layout

```
/opt/moov/
├── src/                      a checkout of this repository
│   └── deploy/
│       ├── docker-compose.yml
│       ├── env.example
│       └── .env              ← secrets, git-ignored, chmod 600
└── ...
```

The compose file builds `moovd` from the repository root (`context: ..`), so it
must live inside a checkout. The image is built from the **vendored** tree, which
means the build needs no network and is reproducible from the commit alone.

---

## First deployment

```bash
# 1. Get the source onto the host
mkdir -p /opt/moov && cd /opt/moov
git clone https://github.com/GrupoNU/moov src
cd src/deploy

# 2. Configure
cp env.example .env
chmod 600 .env

# 3. Generate the master key and paste it into .env as MOOV_MASTER_KEY
docker run --rm --entrypoint /usr/local/bin/moovctl moov/moovd:pilot key generate
#    …or, before any image exists:  go run ./cmd/moovctl key generate

# 4. Fill in the rest of the required values (see env.example):
#      MOOV_PG_PASSWORD         openssl rand -base64 32
#      MOOV_IMAP_SERVER_NAME    the hostname on Mailcow's certificate
#      MOOV_JMAP_EXTERNAL_URL   the URL the BROWSER reaches (the Caddy front)

# 5. Start
docker compose up -d --build
docker compose logs -f moovd
```

`moovd` applies its own embedded migrations on start
(`MOOV_MIGRATE_ON_START=1`), so there is no separate migration step and no
second image. See `cmd/moovd/migrate.go` for why that is safe here and why it
is opt-in.

### Back up the master key before adding an account

`MOOV_MASTER_KEY` decrypts every stored app password. A database dump plus this
key is a full credential compromise; a dump without it is useless. **Losing it
means every account must be provisioned again.** Store it somewhere that is not
this repository and not the same backup as the database.

---

## Provisioning an account

The ADR §4 flow: validate the mailbox password by a real IMAP LOGIN, mint (or
register) an app password, encrypt it, and discard the user's password.

```bash
docker compose exec -T \
  -e MOOV_ACCOUNT_PASSWORD='<the mailbox password>' \
  -e MOOV_IMAP_HOST=dovecot \
  -e MOOV_IMAP_SERVER_NAME=mail.example.com \
  moovd /usr/local/bin/moovctl account add -app-password user@example.com

docker compose exec -T moovd /usr/local/bin/moovctl account list
```

`-app-password` registers an existing Mailcow app password instead of minting
one through the API — the pilot mode. The credential is still validated by a
real IMAP login and still encrypted before storage; Moov simply cannot revoke
it later, so remove it in the Mailcow UI when the account is deleted.

The supervisor picks up new accounts on start, so restart `moovd` after adding
the first one:

```bash
docker compose restart moovd
```

---

## Managing a domain's mailboxes from outside (epics M1, M2)

An external system — a portal, a CRM, an event platform — can manage the
mailboxes of ONE domain without ever touching Mailcow: create them, read their
usage, suspend them, move them into read-only retention, export them, delete
them. It can also sign its own users straight into Moov, with no second
password. The wire contract is
[`docs/specs/L2-accounts-api-contract.md`](../docs/specs/L2-accounts-api-contract.md)
and it is what a consumer's developer reads; this section is what an operator
does and what an operator has to know before doing it.

Three capabilities, three switches, all of them off by default:

| Capability | Turned on by | Off means |
|---|---|---|
| Service-account keys | `moovctl service-account create` | no keys exist, so nothing can authenticate |
| The accounts API | `MOOV_MAILCOW_WRITE_KEY` in **moovd's** environment | every `/admin/accounts` route answers the generic 404 |
| Delegated sign-in | `MOOV_DELEGATED_ISSUERS` in **moovd's** environment | every `/auth/delegated/*` route answers the generic 404, and `Authorization: Bearer` is refused |

They are independent. A domain can have keys issued and the API off (nothing
works, nothing leaks), or delegated sign-in on without the accounts API (a
portal signs users into mailboxes an operator provisioned by hand).

### Issuing a service-account key

A key is the credential a consumer presents on every accounts-API call. It is
issued from the shell, never over a network route — deciding that an external
system may create and delete mailboxes of a domain is an operator decision with
shell access behind it, exactly like granting a brand admin, and giving that
decision a bootstrap credential of its own would only move the problem.

```bash
docker compose exec moovd /usr/local/bin/moovctl service-account create \
  -domain events.example.test -scopes accounts:write -name "the portal"

docker compose exec moovd /usr/local/bin/moovctl service-account list
docker compose exec moovd /usr/local/bin/moovctl service-account revoke -id sa_...
```

`-domain` is required and is the whole of the key's authority: **one key, one
domain**, and there is no flag to widen it. `-scopes` takes a comma-separated
list of `accounts:read` and `accounts:write` (write implies read) and defaults
to `accounts:write`. `-name` is a label that appears in every audit line the key
produces, so give it the consumer's name rather than leaving it blank.

**The key is printed once and only its SHA-256 is stored.** There is no command
to print it again, and that is not an oversight: a key an operator can re-read
is a key a stolen database dump can re-read. It goes to stdout on its own line
so it can be piped into a secret store. An operator who lost one does not
recover it — they revoke it and issue another, which is a twenty-second
operation and leaves a trail of both events.

**Rotation is therefore issue-then-revoke, in that order.** Issue the new key,
hand it to the consumer, wait for the consumer to be using it, then revoke the
old one. There is no overlap window to configure, because both keys are simply
valid until one is revoked. `service-account list` shows `LAST USED`, which is
how you tell whether the old key is still in traffic before you pull it.

Revocation is immediate: the next request presenting a revoked key gets the same
generic 404 an unknown key gets. The audit lines it already wrote stay — the
record of what a key did must outlive the key.

### The accounts API

**What it does.** Creates a mailbox (idempotent by address), reads it, updates
its display name and quota, suspends and resumes it, moves it into read-only
retention, exports it, deletes it. Seven verbs, each one audited.

**What it cannot do: read mail.** There is no route on this API that returns a
message, a subject, a sender or a mailbox listing. A service-account key is a
second authentication class that shares nothing with a mailbox credential: a
session token presented to `/admin/accounts` resolves to no service account and
gets the 404, and a service-account key is not a mailbox credential and gets
nowhere on `/jmap`. Both directions are pinned by test, because "it happened to
work" is how two authentication classes quietly merge into one.

**Turning it on is handing over a credential.** Read *The Mailcow write key*
below before setting it. With the variable absent the routes answer the generic
404 — deliberately, and not a 501: a prober must not be able to learn that the
feature exists here and is merely off.

```bash
# In the moovd service's environment (deploy/.env):
MOOV_MAILCOW_WRITE_KEY=...          # or MOOV_MAILCOW_WRITE_KEY_FILE=/run/secrets/...
MOOV_ACCOUNTS_MAX_QUOTA_MB=10240    # optional; the installation's ceiling for a
                                    # mailbox quota. Default 10240. A create
                                    # without quotaMB gets 2048; the floor is 64.
```

On start the daemon logs `accounts api enabled` with the redacted Mailcow config,
or `accounts api disabled` naming the variable that would enable it. That line is
the fastest answer to "is it on".

#### The no-oracle 404, and why debugging it is annoying

**Six different causes return the same 404, byte for byte, as a route that does
not exist**: the feature is off; the `Authorization` header is missing; the key is
malformed, revoked, or lacks the scope; the address is outside the key's domain;
the account does not exist. It is enforced in one place — every
`accounts.ErrNotFound` is rendered as that one body — so no handler can forget it.

This is deliberate and it is right: otherwise the API is an oracle for which
domains and which mailboxes an installation has, and a consumer could enumerate
another customer's mail estate with a key it legitimately holds. But it means
**an operator debugging a consumer's integration learns nothing from the
response.** The answers are all on this side:

- `docker compose logs moovd | grep '"msg":"accounts'` — the cause of a refusal
  is logged even though it is never sent.
- `moovctl service-account list` — is the key active, is its domain what the
  consumer thinks it is, has it *ever* been used? `LAST USED` = `never` means the
  key in the consumer's configuration is not this one.
- The startup line above — is the feature on at all.

Ask the consumer for the `X-Request-Id` they sent. Moov echoes it and writes it
on every audit line and log line, and it is what joins their trace to ours.

#### The rate limit

**120 requests per minute per key, burst 30.** Budgeted per key and not per IP,
because the budget belongs to the credential: one consumer behind a NAT cannot
spend another's. Over budget is a `429` with `Retry-After` in seconds. A request
body is capped at 16 KiB, and an echoed `X-Request-Id` at 64 characters.

This is a different limiter from the login lockout under *Rate limiting: what is
honestly there*. That one counts failed mailbox logins; this one counts
accounts-API requests. Neither knows about the other.

#### The audit line

Every write — not reads; `GET` deliberately writes no line — produces one row in
the store and one `accounts: admin action` line in the log, carrying the actor id,
the actor's name, the verb, the address, `ok` or `error`, the consumer's
`X-Request-Id`, and the `reason` string the consumer supplied. Insist on reasons
when agreeing an integration: the row answers *what* happened by itself, and only
the consumer can supply *why*.

**A failure to write the audit row does not fail the operation it describes.** The
mailbox was already created in Mailcow and in Moov, and answering 500 to a caller
whose mailbox in fact exists sends it into a retry loop against a state that is
already correct. The log line is the fallback record and carries everything the
row would have — so a gap in the table is a reason to read the log, never a reason
to assume nothing happened.

Migrations 0012 (accounts API) and 0013 (delegated sessions) create the tables.
Both add tables and indexes and **backfill nothing**, so they stay sub-second even
on a populated store — unlike 0004, which is documented under *Troubleshooting*.

### The Mailcow write key and its trust boundary

`MOOV_MAILCOW_WRITE_KEY` — or `MOOV_MAILCOW_WRITE_KEY_FILE` for deployments that
mount secrets as files; set exactly one, setting both is a startup error — is a
**read-write Mailcow API key**. It can create mailboxes, delete mailboxes and mint
app passwords.

**It is a different variable from `MOOV_MAILCOW_API_KEY` on purpose, and the
difference is the point.** `MOOV_MAILCOW_API_KEY` is `moovctl`'s: it lives on an
operator's machine for the length of one command. `MOOV_MAILCOW_WRITE_KEY` lives
in a long-running, network-facing process. Those are not the same risk, they
should not be satisfied by the same value in the same file, and collapsing them
into one variable would mean anyone who enabled the CLI had also enabled the API.
Setting this one is the operator's deliberate act of opening a new trust boundary.

**Mailcow does not scope API keys by domain.** There is no per-domain key to
issue; the key Moov holds can touch every mailbox on the server. So the only thing
standing between one consumer and another consumer's mail is **Moov's own check**
that the address belongs to the service account's domain. That check
(`accounts.Service.resolve`) runs before any Mailcow call, refuses with the
generic 404, and is pinned by a test that fails if a foreign address reaches the
Mailcow client at all. It is deliberately the only place in the code that decides
that question: a second one could disagree with the first.

Operationally: **the blast radius of this key is the whole mail server, and the
boundary is software.** Treat it the way `MOOV_MASTER_KEY` is treated — `chmod
600`, out of the repository, out of the database backup.

#### The IP allow-list on Mailcow's side

Mailcow can restrict an API key to source IPs, and **F0 verified it honours that
strictly**. Use it: it is the one part of this boundary that is not Moov's own
code. Authorise the address `moovd` reaches Mailcow from — inside the shared
Docker network that is the container's address on
`mailcowdockerized_mailcow-network`, not the host's public IP.

The useful property when you get it wrong is that Mailcow's refusal **names the IP
it actually saw**:

```
{"type":"error","msg":"api access denied for ip 203.0.113.7"}
```

That is how you learn what to authorise — read the IP out of the error rather
than reasoning about the topology. Two traps F0 measured alongside it:

- It is a **different** error from a bad credential (`authentication failed`).
  Same symptom for a consumer, opposite remediation. Do not conflate them.
- Mailcow returns these with **HTTP 200** and an error body, not a 4xx. Any check
  that only looks at the status code will call a rejected key healthy.

### Delegated sign-in

An external system that owns a mailbox has a user who has no mailbox password —
the portal authenticated them its own way. Delegated sign-in lets that portal sign
the user into Moov: the portal mints a short-lived JWT, the browser carries it to
Moov, and Moov exchanges it for an opaque session of its own.

```bash
# In the moovd service's environment (deploy/.env):
MOOV_DELEGATED_ISSUERS='[{"host":"mail.example.test","issuer":"https://portal.example.test","jwksUrl":"https://portal.example.test/.well-known/jwks.json"}]'
MOOV_DELEGATED_SESSION_MAX=168h     # optional; default 168h (7 days)
```

`MOOV_DELEGATED_ISSUERS` is a **JSON array** of objects with exactly three fields:

- `host` — the Moov hostname the browser reaches, as a **bare hostname**: no
  scheme, no port. It is compared against the token's `aud`, and a value
  containing `/` or `:` is refused at startup.
- `issuer` — the exact `iss` string the portal puts in its tokens. Compared by
  string equality; it is never fetched and need not resolve.
- `jwksUrl` — an absolute **HTTPS** URL. Plaintext is refused at startup, because
  a key document anyone on the path can rewrite lets anyone on the path mint
  tokens for the installation.

One host may have several issuers and one issuer may serve several hosts; an entry
is the pairing of the two.

`MOOV_DELEGATED_SESSION_MAX` is the **absolute** session lifetime as a Go duration
(`168h`, `72h`, `30m`), default `168h`. It is a ceiling, not the session length: a
session also carries a sliding 12-hour expiry, so an idle one dies long before
this. Zero or negative is a startup error.

**A malformed value is fatal at startup; an unset one silently disables the
feature.** The asymmetry is deliberate. An operator who wrote the JSON meant to
enable delegated sign-in, and a daemon that started while ignoring bad
configuration would hand the portal a 404 to debug from the outside — where the
no-oracle rule guarantees it learns nothing. So: JSON that does not parse, an
empty array, a missing field, a `host` with a port, a non-HTTPS `jwksUrl`, an
unparseable duration — the daemon refuses to start and names the offending entry
by its index. If the daemon is running, the configuration was valid.

#### What the issuer must publish

This is the half an operator does not control, and it is where a working
integration breaks months later. Give the consumer these rules in writing:

- A **JWKS document over HTTPS** at the configured URL, `application/json`, each
  key carrying `kid`, `use: "sig"` and `alg`.
- Keys are **EdDSA over Ed25519** (`kty: "OKP"`, `crv: "Ed25519"`) or **RS256**
  (`kty: "RSA"`, at least 2048 bits). Nothing else is accepted, and the token's
  `alg` is checked before its signature is looked at — `none`, the HMAC family
  and the EC family are refused outright.
- `kid` is **required**, in the JWKS and in the token header.
- **Publish the next key before signing with it.** Moov caches a key set for 10
  minutes and re-fetches on an unknown `kid` at most once every 60 seconds, so a
  key that appears at the same moment as the tokens signed by it produces a minute
  of refusals for no reason.
- **Keep the previous key for at least one token lifetime after rotating.** A
  token lives at most 5 minutes, so this costs nothing; skipping it refuses every
  token already in flight.
- **Never reuse a `kid`.** A repeated `kid` within one JWKS makes Moov refuse
  *both* keys, which is the safe reading of an ambiguous document.

If the JWKS cannot be fetched and no cached key matches, the exchange answers
`503` with `Retry-After` — the only case where the issuer's availability shows
through to a user. A `503` here means "go and look at the portal's JWKS
endpoint", not "go and look at Moov".

#### The token never reaches a log

The browser carries the JWT in the **URL fragment**, which is never sent to any
server: not to Moov, not to the fronting Caddy, not to a proxy in between. The PWA
reads it out of the fragment in JavaScript and POSTs it to
`/auth/delegated/exchange` in a body. The token therefore appears in no access log
by construction, and this deployment needs no new redaction rule for it.

**The one thing that would break that is letting an integration move the token
into the query string.** A query string is logged by Caddy whenever access logging
is on (see *Logs*), and it lands in browser history and `Referer` headers
besides. The fragment is the whole mechanism, not an implementation detail.

Session tokens are kept out of query strings too: one presented as `access_token=`
is refused by construction, because anything arriving without an `Authorization`
header is handed to the scoped push/blob verifier, which does not know the session
format. A test pins it.

A refused **token** is one `401` with one message, whatever failed — bad
signature, wrong audience, replayed `jti`, expired, unknown issuer. The reason goes
to a debug log line that never carries the token. A refused **account** is a `403`
with a machine-readable code (`notProvisioned`, `suspended`, `disabled`), because
the signature has already proved the caller is the issuer and the PWA renders a
different screen for each.

### Read-only, export and deletion

These three behave in ways that surprise people, so they are spelled out here
rather than left to the contract.

#### Read-only retention is permanent, and the mechanism looks indirect

`POST /admin/accounts/{address}/readonly` moves a mailbox into retention: still
readable, no longer able to send. **There is no transition back in this version.**
Not "not exposed yet" — there is no route, and a consumer that needs reversibility
should suspend and resume instead.

The mechanism is worth understanding because it looks roundabout: Moov **re-issues
the account's app password without SMTP** and deletes the old one. The order is
mint, store, then delete, so a failure at any step leaves the account holding a
credential that works.

It is built that way because **Mailcow's `smtp_access` flag alone does not block
sending — F0 measured it.** With `attr:{"smtp_access":0}` saved and visible in a
subsequent `GET`, AUTH on submission port 465 still answers `235 2.7.0
Authentication successful`: no Postfix SQL map consults the attribute. A flag that
is stored and displayed but not enforced is worse than no flag, and building
retention on it would have produced a lock that does not lock.

So the credential is the lock. Moov still clears `smtp_access` afterwards, as belt
and braces and so the Mailcow UI shows the intent, and it logs a warning rather
than failing if that write does not land — by then the account is already unable
to send. The JMAP layer's refusal of `EmailSubmission/set` and the PWA's hidden
compose are the *explaining* lock: the one that tells a user why.

One failure mode to watch for in the log: if the old app password cannot be
deleted, the line says so loudly and names its id. **That credential still permits
SMTP until someone removes it in the Mailcow UI.** It is the one gap in this
mechanism, and it is logged at `ERROR`.

#### Exports are files on disk with a 7-day life

An export is **one zip per account** — one `.eml` per message plus a
`manifest.json` — written under **`exports/` inside the blob root**. It rides the
blob root rather than taking a variable of its own because the requirements are
identical (a writable, persistent, sizeable directory the daemon owns) and an
operator who configured one has configured the other. It is not a blob and never
enters the blob tree.

**Budget disk for it: one export is a copy of the mailbox.** A queue of them is
that many copies. `moov_pending_exports` is how a queue forming becomes visible.

Two different clocks, and they are routinely confused:

- **The download URL is valid 24 hours.** It is signed, carries no identity, needs
  no credential, and grants exactly one zip. It is also bound to the origin it was
  minted for, so a multi-host installation mints a different URL per host.
- **The file lives 7 days.** After that a sweep deletes the zip and the row starts
  answering `410`.

So a consumer that stored a download URL and came back on day three has a dead URL
and a live export, and asks for a fresh URL. One that comes back on day eight has
neither, and asks for a new export.

The runner does at most one job per tick (every 5 s), which keeps a backlog from
starving the sweep. A daemon restart mid-export is harmless: the job row is still
claimable, and the next daemon produces the file again from the store, which is
the authority.

#### Deletion is asynchronous, and `deleting` is not a promise of speed

`DELETE` answers **202**, not 204, and the state becomes `deleting`. Synchronously:
sessions and tokens are revoked, the Mailcow mailbox is deleted (its app passwords
cascade with it), the row is marked. In the background, on a one-minute ticker:
Moov's own rows and blob references.

**Do not quote a deadline to a consumer, and do not alert on a `deleting` state
that persists.** The contract promises only that a `GET` eventually answers 404,
and it is worded that way for a measured reason: **F0 could not verify that
Mailcow removes the maildir from disk promptly for a mailbox that has received
mail.** The mailbox is gone from Mailcow's database and unreachable by its owner
immediately; whether the bytes have left the disk at that moment is unverified. If
disk reclamation matters for a compliance answer, verify it on your own
installation rather than quoting this document.

A second `DELETE` on an account already deleting is a `409`, not a second delete;
once the purge completes it is a `404`. Blobs are not unlinked by the purge:
dropping the message rows drops the references, and the blob GC collects what no
longer has any — the only correct path for content-addressed storage that another
account may share.

## Branding a hostname

One Moov install can serve several customers, each on its own hostname, each
with its own name, logo and colours. Nothing is duplicated to do it: the same
`moovd`, the same store, one directory of files per host.

**What branding covers.** The login split panel (logo, product name, tagline,
splash image or gradient, the "contact your administrator" link), the logo and
name in the top bar, the accent palette in **both** the light and the dark
theme, the browser tab's title and favicon, the PWA manifest's `name`,
`short_name` and `theme_color`, the icons of the installed app on a phone or a
desktop launcher, and the icon on a desktop notification.

**What it never covers.** Layout, behaviour and keyboard. Every shortcut, every
row, every menu is in the same place on every host — the Gmail muscle memory
this product is built on is not something a customer gets to move. Status
colours are not brand colours either: red still means destructive and green
still means sent, whatever the accent is.

### How a host is resolved

By the `Host` header of the request, and by nothing else. There is no
`?brand=` parameter and the email address being typed is never sent to find
out: either of those would turn the login page into an oracle for *which
customers exist on this server*, enumerable one guess at a time. The caller
already had to know the hostname to reach us.

**A hostname with no configuration is served Moov's own brand, and it is
indistinguishable from a hostname configured to look like Moov.** Existence is
never confirmed or denied. That is also why "the whole install is unbranded" is
a complete, correct configuration — it is what the pilot runs.

**Why your change is not visible yet.** Two caches sit in front of a brand:

| | Duration | Where |
|---|---|---|
| Resolved document, in process | 60 s (`brandingCacheTTL`) | `moovd` |
| `Cache-Control: public, max-age` | 300 s (`BrandingMaxAge`) | browsers, any shared proxy |

So a `moovctl branding set` takes effect **within a minute without a restart**,
and a browser that already loaded the old one may hold it for up to five more.
Both routes answer `If-None-Match` with a 304, so a reload is cheap, not free.

### The directory layout

`MOOV_BRANDING_DIR` (inside the container: `/etc/moov/branding`) is a root of
per-host directories:

```
/etc/moov/branding/
├── mail.acme.example/
│   ├── branding.json
│   ├── logo.png
│   ├── logo-dark.png
│   ├── icon.png
│   └── splash.jpg
└── correo.otracosa.example/
    ├── branding.json
    └── logo.png
```

The directory name is the **lowercase hostname without a port** —
`mail.acme.example`, never `Mail.Acme.Example:8443`. Only `[a-z0-9.-]` is
accepted; anything else (a path separator, `..`, a percent escape, an IPv6
literal) resolves to "no configuration" rather than to a filesystem read.
`moovctl` normalises what you pass to `-host` by the *same* rule the server
applies, so a host the CLI writes is a host the server will find.

Permissions: `0755` on the directories and `0644` on the files, which is what
`moovctl` writes. This is not laxity — `moovd` runs **distroless as a different,
unprivileged user** than the operator running the CLI, and it must be able to
traverse and read these. Every byte here is published to anonymous callers by
design; there is nothing secret to protect.

### `branding.json`

Written by `moovctl branding set`. Every field is optional, and this is all of
them:

```json
{
  "name": "Acme Mail",
  "shortName": "Acme",
  "tagline": "Correo corporativo de Acme S.A.",
  "supportUrl": "mailto:soporte@acme.example",
  "privacyUrl": "https://acme.example/privacidad",
  "termsUrl": "https://acme.example/terminos",
  "logo": "logo.png",
  "logoDark": "logo-dark.png",
  "icon": "icon.png",
  "splash": "splash.jpg",
  "colors": {
    "primary": "#0f766e",
    "onPrimary": "#ffffff",
    "splashFrom": "#042f2e",
    "splashTo": "#115e59"
  }
}
```

`name` is the product name in the UI and the browser tab (capped at 64
characters). `shortName` is the label under the installed icon on a home
screen; it is **at most 12 characters**, because that is where launchers
truncate, and `moovctl` *refuses* a longer one rather than cutting it silently
— an operator who typed "Correo Corporativo" should learn at the terminal that
the phone will say "Correo Corpo". Leave it unset and it is derived from
`name`: the name itself when it fits, otherwise its first word ("Acme Mail"
stays "Acme Mail"; "Correo Corporativo Acme" becomes "Correo").

`tagline` is the optional line under the name on the login panel (160
characters); empty renders nothing. `supportUrl` is where "contact your
administrator" points and accepts **only** `https://`, `http://` or `mailto:` —
so it can never become a `javascript:` URL on the page where passwords are
typed.

`privacyUrl` and `termsUrl` are **the operator's own** privacy policy and terms
of service. They appear in the legal footer — one muted line under the sign-in
form and at the foot of the message list — beside three links that are **not**
configurable: "Powered by Moov", the source code at the exact commit the bundle
was built from, and the AGPL-3.0 license. That trio is a **license obligation**,
not a credit: AGPL-3.0 §13 requires that anyone interacting with the program
over a network be offered the corresponding source, so no branding document can
remove or replace it. What a brand *can* do is add its own two, which are the
operator's obligations to their users rather than ours. Both accept exactly the
schemes `supportUrl` does (`https://`, `http://`, `mailto:`); an unset one
renders no link at all rather than a dead one.

`logo`, `logoDark`, `icon` and `splash` name **files sitting beside
`branding.json`**, never URLs: a customer-supplied external URL would be a
tracking pixel on our login page and a mixed-content risk.

`logo` is what the top bar and the login panel show — usually a wordmark, often
wide. `logoDark` is **the wordmark for dark backgrounds: used on the login panel
and in the dark theme; without it the app draws the light logo on a small light
plate** — legible, but a plate the customer did not design. It is the mirror of
the problem `icon` solves, and it was found on the same live gate: Areacorp's
wordmark is black, so on the dark login panel it was a black mark on a dark
ground. `logoDark` takes **no part in generating the PWA icons** — that chain is
`icon`, then `logo`, then Moov's own, and a second wordmark would only give the
home screen a way to disagree with the top bar. `icon` is the **optional square mark the installed app's icons and the
favicon are rendered from**, and it exists because those are not the same
picture: the maskable and Apple icons sit on an **opaque plate**,
so a brand whose primary is `#000000` and whose logo is a black wordmark ships
a black glyph on a black plate — invisible on a home screen. Most brand kits
already have the square glyph drawn for dark backgrounds; that file goes here.
Leave `icon` out and everything behaves exactly as before: the icons are
rendered from `logo`.

The four colours are seed tokens, and CSS derives hovers, borders and surfaces
from them — a customer configures four values, not forty. Each must be a CSS
hex literal, `#rgb` or `#rrggbb`; named colours, `rgb()` and `hsl()` are
refused, so the client can compare and contrast-check them without a CSS
parser. `primary` is the accent (buttons, links, focus rings); `onPrimary` is
the text drawn *on* the accent, its own token because guessing it is exactly
how a contrast failure gets shipped; `splashFrom`/`splashTo` are the two stops
of the login panel's gradient, used when there is no splash image and as its
backdrop while it loads.

**Leave `splashFrom`/`splashTo` out and they follow your `primary`** — derived
as the primary mixed 70% and 35% toward black, so a brand that configures one
colour gets a gradient in its own hue instead of Moov's violet; set either one
to pin it, and clear it (`""`) to go back to automatic. Without a `primary`
there is nothing to derive from, so Moov's gradient stands. And **a splash
image is shown exactly as you uploaded it**: the gradient sits behind it as the
backdrop while it loads and as the fallback if it fails, never as a tint over
it, with only a bottom scrim for the legibility of the mark and tagline.

**An invalid field falls back to Moov's value for that field only** — never to
an unstyled page, and never to a refusal to render. A malformed `#00ff0` gives
you Moov's indigo with the rest of your brand intact; a `branding.json` that is
not valid JSON at all gives you Moov's whole brand, and says so in the daemon
log so you learn the typo did not take effect. A login screen that refuses to
render is far worse than one that renders unbranded.

### The images

Accepted: **PNG, JPEG, WebP and GIF**, decided by the file's magic bytes, not
by its extension — a file called `logo.png` that contains HTML is refused, not
served as an image the browser then re-sniffs for itself.

**SVG is refused, deliberately, in both the CLI and the server.** An SVG is an
XML document that can carry `<script>`, external references and CSS; serving an
operator-supplied one from our own origin would hand anyone who can supply a
logo a stored-XSS primitive on the page that exists to receive passwords.
Sanitising SVG correctly is a project in itself. Export to PNG.

- **2 MiB per file** (`MaxBrandingAssetBytes`), enforced when the file is
  *read*, so dropping a bigger one into the directory later does not slip past.
- **4096 px maximum on each side** for the logo (`MaxBrandingLogoDimension`),
  checked from the image header before any pixels are decoded — a 40,000 px PNG
  that inflates to gigabytes costs us a few bytes to refuse.
- **A WebP image displays fine on the page but cannot become PWA icons.** Its
  decoder is not vendored, and the vendor tree is hermetic. The icons then come
  from the next link of the chain — `icon`, then `logo`, then **Moov's** — and
  ending up on Moov's would be a nasty surprise on a customer's phone, so it is
  declared twice, never silently: `moovctl branding set` prints `Note: the PWA
  icons will not be rendered from this icon — …` the moment you pass the file,
  `moovctl branding show` says in its `PWA ICONS` row which file is in use and
  why the one you asked for is not, and `moovd` logs one warning per host per
  cache TTL naming the file that failed. The same declaration covers an
  undecodable, oversized or missing image.

**Recommended logo:** PNG with an alpha channel, at least 512 px on the long
side. A wide wordmark is exactly right in the top bar and on the login panel.
**Recommended icon:** **square, at least 512 px, PNG with alpha**, and prefer
a mark with **some dark in it** — the favicon and the launcher "any" icons are
drawn on a transparent canvas, so an all-light glyph can vanish on a light tab
(you get a warning if yours is). You no longer need a special dark-background
variant for the plated icons: the plate turns **white** behind a dark mark by
itself. `moovctl` warns when an `-icon` is further than 10% from 1:1, because a
launcher shows a square and a wide image ends up small between bands of the
plate.
**Recommended splash:** a photograph at least 1600 px wide — it is rendered
`object-fit: cover`, so it is cropped to the panel, not letterboxed, and it is
shown **exactly as you uploaded it**: no gradient tint, no blend, and no scrim
over it. The name and tagline stay legible with a text-shadow instead, so the
only pixels darkened are the ones directly behind the lettering — and the ink
follows the picture: the panel measures the region behind the text and switches
to **dark lettering with a light halo** over a bright image. **`splashText:
false`** (or `moovctl branding set -splash-text=false`, or the switch in the
Marca panel) hides the name, tagline and logo altogether, for artwork that
already carries them; it is `true` by default and has no effect without an
image. With no splash
image the panel keeps its gradient and its scrim, which is where a plate behind
a dark logo still applies.

What the icon generator does, on demand and cached. Its source is `icon` when
there is a usable one, `logo` otherwise, and Moov's own mark when neither can
be rendered:

| Icon | Size | Padding each side | Plate |
|---|---|---|---|
| `icon-192`, `icon-512` | 192, 512 | 10% | **transparent** |
| `icon-maskable-192`, `icon-maskable-512` | 192, 512 | 20% | opaque, **chosen from the mark** |
| `apple-touch-icon` | 180 | 10% | opaque, **chosen from the mark** |
| `favicon-32` | 32 | none | **transparent** |

**The favicon and the "any" icons are the mark exactly as you uploaded it** —
transparent canvas, never plated, never tinted, whichever file they came from.
An earlier version plated a dedicated `icon` at every size to rescue a
white-on-dark glyph, and the cost was a frame around every other brand's tab
icon; a plate in the tab is chrome the operator did not draw. `favicon-32`
therefore keeps **no padding**: at 32 px every pixel counts, and the padding
floor existed only to frame a plate.

**Where there IS a plate, its colour comes from the mark, not from `primary`:**
the mean WCAG relative luminance of the icon's opaque pixels is measured
(alpha-weighted, so anti-aliased edges count in proportion and the transparent
canvas does not count at all); below 0.5 the mark is dark and the plate is
**white**, otherwise the plate is your **`primary`**. That is what lets a black
glyph be visible on a maskable icon *and* on the tab at the same time — which
the old single-colour plate could not do, because on a near-black `primary` it
painted a black mark onto a black plate.

The one case nothing here can fix honestly is a **light mark on the transparent
canvas**: plating the favicon is what this removed, and recolouring your mark
would wreck anything that is not a flat silhouette. So it is **declared**
instead — `moovctl branding show` and the Marca panel's warnings say *"a light
icon may vanish on light tabs; consider a version with a dark outline"*, and
you decide.

The source image is contained inside the padded square with its aspect ratio
preserved and centred — which is why a square `icon` fills it and a wide `logo`
does not. The maskable pair pads to 20% because Android adaptive icons keep
only the inner 80% circle, and paints the plate because a transparent one would
be masked onto whatever the launcher picks (usually white). `apple-touch-icon`
is opaque because iOS discards alpha and composites onto **black**. **Those two
plates are the reason `icon` exists:** a wordmark drawn for a light page needs
a square glyph when it has to sit on a plate.

### The `moovctl` workflow

`moovctl` writes to the **host** filesystem, and `moovd`'s image is distroless.
Two things that look like they should work do not, and both were verified on
the pilot: there is **no Go on the host**, so "run it from a checkout" is not
an option, and `docker compose run -v …` **cannot** override the service's
read-only branding bind — it fails with `read-only file system`.

Run a **fresh container from the same image** instead:

```bash
IMG=$(docker inspect moovd --format '{{.Config.Image}}')
docker run --rm --user 65532:65532 \
  -v /etc/moov/branding:/etc/moov/branding \
  -v /root/brand:/brand:ro \
  --entrypoint /usr/local/bin/moovctl "$IMG" \
  branding set -host mail.acme.example -name 'Acme Mail' \
    -logo /brand/acme/logo.png -icon /brand/acme/icon.png \
    -color-primary '#0f766e'
```

Each piece earns its place:

- **`IMG` from the running container** — the brand is written by exactly the
  binary that will serve it, whatever tag is deployed, with no second source of
  truth to drift.
- **`--user 65532:65532`** — the daemon's own unprivileged uid, so files the
  CLI writes stay writable by the Settings → Marca panel and vice versa (the
  host directory is owned by that uid, see "Granting brand admins").
- **`-v /etc/moov/branding:/etc/moov/branding`** — the same bind the service
  has, at the same path both sides so `MOOV_BRANDING_DIR`
  and the default agree without a `-dir`.
- **`-v /root/brand:/brand:ro`** — the source images, read-only: this container
  has no business writing to wherever the brand kit lives.
- **`--entrypoint /usr/local/bin/moovctl`** — the image's entrypoint is the
  daemon; this replaces it for one command.

`show` and `list` run the same way and can take the branding mount `:ro` too,
since they only read.

A full brand in one call — every flag `set` accepts, with the source images
under the read-only `/brand` mount:

```bash
docker run --rm --user 65532:65532 \
  -v /etc/moov/branding:/etc/moov/branding \
  -v /root/brand:/brand:ro \
  --entrypoint /usr/local/bin/moovctl "$IMG" \
  branding set \
    -host mail.acme.example \
    -name 'Acme Mail' \
    -short-name 'Acme' \
    -tagline 'Correo corporativo de Acme S.A.' \
    -support-url 'mailto:soporte@acme.example'     -privacy-url 'https://acme.example/privacidad'     -terms-url 'https://acme.example/terminos' \
    -logo /brand/acme/logo.png \
    -logo-dark /brand/acme/logo-on-dark.png \
    -icon /brand/acme/glyph-on-dark.png \
    -splash /brand/acme/office.jpg \
    -color-primary '#0f766e' \
    -color-on-primary '#ffffff' \
    -color-splash-from '#042f2e' \
    -color-splash-to '#115e59'
```

Every subcommand takes a `-dir` flag that overrides the root; without it the
CLI takes `MOOV_BRANDING_DIR`, then `/etc/moov/branding` — which is why the
mount above uses the same path on both sides and no `-dir` is needed.

**`set` is incremental: a flag you do not pass keeps its current value.** So
adjusting one colour does not re-upload the logo, and — the reason it works
this way — adjusting one colour cannot silently delete the customer's logo.
Passing a flag with an *empty* value clears that field (`-logo ''` stops
advertising the logo, `-logo-dark ''` returns the dark panel and theme to the
light logo on its plate, `-icon ''` returns the app icons to being rendered
from the logo; the file itself is left on disk, because deleting an operator's
file as a side effect of a config change would be a surprise). The stored
filename is always ours (`logo.png`, `logo-dark.png`, `icon.png`, `splash.jpg`,
from the sniffed type), never the source filename.

```bash
# Read-only, so the branding mount can be :ro. RUN=... is the prefix from above.
RUN="docker run --rm --user 65532:65532 -v /etc/moov/branding:/etc/moov/branding:ro \
  --entrypoint /usr/local/bin/moovctl $IMG"

$RUN branding show -host mail.acme.example   # every field, plus where the PWA icons come from
$RUN branding list                           # every configured host

# unset WRITES, so it needs the mount writable (drop the :ro):
docker run --rm --user 65532:65532 -v /etc/moov/branding:/etc/moov/branding \
  --entrypoint /usr/local/bin/moovctl "$IMG" \
  branding unset -host mail.acme.example     # back to Moov's defaults
```

`unset` removes `branding.json` and the image files it wrote (only those, by
their recorded names — never a blanket wipe of a directory an operator may have
put something else in); `-keep-assets` leaves the images.

### Granting brand admins (the Settings → Marca panel)

A domain's own administrator can edit their host's brand from the webmail
(Settings → Marca) through the authenticated **`/branding/admin` API**, without
shell access. Who may do that is **operator data**, granted here:

```bash
docker run --rm --user 65532:65532 -v /etc/moov/branding:/etc/moov/branding \
  --entrypoint /usr/local/bin/moovctl "$IMG" \
  branding grant -host mail.acme.example -user ana@acme.example
# ... revoke -host mail.acme.example -user ana@acme.example
```

`grant` records the mailbox (lowercased) in `brandAdmins` of that host's
`branding.json` — creating the file, with nothing but that list in it, when the
host has no brand yet (the host keeps serving Moov's brand until someone
configures one). `show` prints the list as `BRAND ADMINS`, `list` has a column
for it. The running server honours a grant or a revoke **within a minute**, no
restart. The public `GET /branding` **never** carries the list.

What the API enforces, so you do not have to: the admin edits **the host they
are logged in on** (there is no host in the URL — the `Host` header decides,
exactly as for `GET /branding`); a logged-in user who is *not* an admin of that
host gets the same generic **404** an unknown route gets, so they learn neither
that the host is configured nor that the feature exists; every write is
validated by the same rules as `moovctl branding set`, written atomically
through the same code, budgeted at **10 writes per minute per user**, and
leaves one audit line in the daemon log (`branding admin: write` with host,
actor, action, bytes and sha256 — never the image bytes, never a URL value).
"Volver a la marca de Moov" in the panel is like `unset` **except the admin
list survives**, so the panel stays reachable. A second role source — Mailcow's
domain admins — is designed to slot in behind the same interface and is not
built yet (`docs/specs/L2-brand-admin.md` §2).

**Two switches:**

- `MOOV_BRANDING_ADMIN=0` turns the whole API off: every `/branding/admin`
  route answers the generic 404, indistinguishable from "no such route". The
  default is on whenever `MOOV_BRANDING_DIR` is set; with no branding directory
  the routes answer 404 regardless.
- **The bind mount is writable, and the host directory must be owned by the
  daemon's uid.** `docker-compose.yml` mounts the branding root read-write so
  the panel can write `branding.json` and images. The container runs as the
  distroless `nonroot` user (uid 65532), so once, on the host:

  ```bash
  mkdir -p /etc/moov/branding && chown -R 65532:65532 /etc/moov/branding
  ```

  Without it every panel write fails with a `500` ("writing the brand failed",
  cause in the daemon log) while reads keep working. Both `moovctl` and the
  API write `0755`/`0644`; nothing here is secret. For a CLI-only deployment
  that prefers a read-only mount, add `:ro` back on that volume line and set
  `MOOV_BRANDING_ADMIN=0`.

### Deploy wiring

`MOOV_BRANDING_HOST_DIR` in `.env` is the **host** path; compose bind-mounts it
**read-only** at `/etc/moov/branding` (see the previous section for when to
make it writable) and sets `MOOV_BRANDING_DIR` to that container path for you. It defaults to `/etc/moov/branding` on both sides, so
the CLI and the daemon agree without configuration.

A missing directory is a valid configuration: Docker creates it empty on first
start, and an empty root means every host is served Moov's brand. An unreadable
one logs a warning and falls back to the same defaults — it never fails
startup, because refusing to boot a mail server over a logo directory would be
the wrong trade.

**The front must route `/branding*` to `moovd`.** `Caddyfile.public` and
`Caddyfile.pilot` already do, in the same `@jmap` matcher as `/jmap*`. Any
other reverse proxy in front of Moov must do the same, or the SPA's catch-all
answers `/branding`, `/branding/manifest.webmanifest` and `/branding/icons/*`
with `index.html`. **This exact failure was found live on 2026-09-09:** the
shell swallowed the endpoint, so every host installed as Moov, with Moov's name
and Moov's icons, and nothing in the daemon log said a word — moovd was never
asked.

### Verifying

```bash
# The resolved document, as the login page sees it. "default": false means
# your configuration was found; true means it was not (or it is Moov's).
curl -s https://mail.acme.example/branding | jq

# The manifest must come back as application/manifest+json, not text/html.
# text/html here is the SPA-fallback failure above.
curl -sI https://mail.acme.example/branding/manifest.webmanifest

# An icon must be image/png.
curl -s -o /dev/null -w '%{content_type}\n' \
  https://mail.acme.example/branding/icons/icon-192.png
```

Then in a browser, which is where the parts `curl` cannot see live:

- the tab shows the customer's name and favicon;
- the install prompt offers the customer's `short_name`;
- **DevTools → Application → Manifest** shows the name, the theme colour and
  every icon rendered from the customer's `icon` (or its logo when it has
  none).

Browsers cache a manifest and the icons of an **installed** app aggressively,
and far beyond our five minutes. To see new icons, **uninstall and reinstall
the PWA** — a reload will not do it, and neither will a hard reload.

### Choosing the colour

Pick **one** primary that reads as text on white, and let the app do the rest:
it derives both themes from it and **guarantees WCAG AA** — the accent clears
4.5:1 as text against both surfaces of its theme, and the label on a button
clears it against the accent's normal, hover and active steps. When the colour
as sent cannot satisfy that, it is **adjusted**: lightness is moved in OKLCH
toward the constraint with hue kept and chroma kept as far as the sRGB gamut
allows, so an adjusted brand still reads as the customer's colour. A primary
that already passes is returned **exactly** — Moov's own `#5b5bd6` never
shifts. `onPrimary` is treated as a hint: used when it clears the constraint
against the final accent, replaced (and declared) when it does not.

So a customer can never make the app unreadable. But a colour that needs heavy
adjustment **will not look like their brand** — a pale mint arrives as a much
deeper green — and the app tells you which: it logs one line in the browser
console, naming the theme and the reason:

```
[branding] Acme Mail: colours adjusted for WCAG AA — light: … | dark: …
```

If that line is there, check the result with the customer before calling it
done. Sending a darker or deeper variant of their brand colour is usually a
better answer than shipping the adjustment.

---

## Pointing a JMAP client at it

Browser clients need the JMAP server and the web app on **one origin** (spike S1
H7). `Caddyfile.pilot` in this directory is the front that does it, and it is
deployed at `/opt/moov-spike/Caddyfile` on the pilot host:

```
/jmap*, /.well-known/jmap*  ->  moovd:8620
everything else             ->  the webmail
```

`moovd` must be on the front's network for `moovd:8620` to resolve, and
**`docker-compose.yml` owns that attachment**: the `front` network is declared
external and listed among `moovd`'s networks, so `docker compose up -d`
re-establishes it every time the container is created.

This used to be a manual `docker network connect moov-spike moovd` run after
each deploy. It is not one any more, and the difference is not cosmetic: a
manual attachment does not survive container recreation, and `up -d` recreates
`moovd` on every image or configuration change. Each redeploy therefore dropped
the attachment and the front answered 502 until somebody remembered the command.

The network must already exist — compose joins it, it never creates it, exactly
as with Mailcow's. It is `moov-spike` by default (the front spike S1 stood up);
`MOOV_FRONT_NETWORK` points it elsewhere if the front ever moves into a compose
stack of its own.

**Rollback to the S1 `jmap-proxy` is one line** — change the single
`reverse_proxy` target in the `@jmap` handler and reload:

```bash
docker exec moov-caddy-spike caddy reload --config /etc/caddy/Caddyfile
```

The proxy stays running as the S1 oracle (L2 §2.5): when a mapping is in doubt,
its answer is the reference.

---

## Public exposure — `moov.atmosfera.cloud`

The pilot has two front doors. Opening the public one does not close the private
one, and either can be turned off without touching the other.

| | VPN entry | Public entry |
|---|---|---|
| Address | `http://100.123.119.124:8090` | `https://moov.atmosfera.cloud` |
| Listener | S1 Caddy, Tailscale interface | `caddy-public`, **217.216.85.211** only |
| Managed by | hand-run, `/opt/moov-spike/` | **compose** (`Caddyfile.public`) |
| TLS | none (VPN) | Let's Encrypt, HTTP-01 |
| Turned on by | already running | `--profile public` |

### Why the second IP, and why that is load-bearing

The host has two public addresses. The primary (**217.216.83.79**) is
**Mailcow's**: its nginx holds `:80`/`:443` there, along with `25/143/993/995/587/465/4190`.
CLAUDE.md's rule is that Mailcow is never touched, so Moov may not take a port on
that address — not even by adding a vhost to Mailcow's nginx.

The second (**217.216.85.211**) carries only Postal's SMTP on `:25`; its `:80`
and `:443` were verified free before anything was deployed. The compose `ports:`
mapping binds those two ports to **that address explicitly**, never `0.0.0.0`, so
the two stacks cannot contend for a listener even by accident.

> The public-IP restriction lives in `docker-compose.yml`, **not** in the
> Caddyfile. Caddy runs in a network namespace where the host address does not
> exist, so a `bind 217.216.85.211` directive there makes it fail to start
> outright. Docker owns host addresses; Caddy binds all interfaces inside its
> own namespace.

### Turning it on

```bash
cd /opt/moov/src/deploy
docker compose --profile public up -d caddy-public
```

The `public` profile is a safety catch. Without it, a routine `docker compose
up -d` — the command run for every unrelated redeploy — would silently publish
the pilot. Exposure has to be something a person typed.

Certificate issuance needs the DNS record to already point here, since the
HTTP-01 challenge is fetched over the public internet. Starting the service
before the A record exists is harmless: Caddy retries, and the only symptom is
`could not get certificate` in the log.

### What is NOT exposed, and what actually enforces that

`/metrics` and `/healthz` (`moovd:8080`) expose account ids and sync state, and
they stay unreachable from the internet. **The mechanism is the routing, not the
network topology** — worth stating plainly, because the obvious assumption is
wrong:

- `caddy-public` joins only the front network, so PostgreSQL is unreachable.
- But `moovd` is on that same front network and its `:8080` binds all interfaces
  inside its namespace, so **`moovd:8080` is dialable from the public front.**
  That was verified, not assumed.
- What keeps it private is that the only `reverse_proxy` to `moovd` targets
  **8620**, and no route names 8080. `/metrics` falls through to the Bulwark
  handler — confirmed by fetching it through the front and finding zero `moov_`
  series.

**Consequence for review:** adding a route to `moovd:8080` in
`Caddyfile.public` would publish the ops listener with nothing else to stop it.

### Rate limiting: what is honestly there

Stock `caddy:2-alpine` (v2.11.4) has **no `rate_limit` directive** — it is a
third-party plugin needing a custom `xcaddy` build. Verified with
`caddy list-modules`; no such module is present. Rather than pull an unvetted
plugin into the edge, the deployment relies on the limiter that already exists
in `internal/jmaphttp` and is stricter than a generic edge limit because it
counts the thing that matters (failed logins), not requests:

- **Per IP+account exponential lockout** — measured from a cold identity through
  this front: `Retry-After` 2 s → 7 s → 19 s over five wrong passwords.
- **Global failure budget** — a token bucket of upstream login failures, so an
  attacker rotating accounts still cannot make Moov the IP that Mailcow's
  netfilter bans.
- **Positive-result caching**, so a live user's traffic never pays a LOGIN.

One caveat the exposure makes real: `clientIP` is deliberately the TCP peer and
never `X-Forwarded-For` (a spoofable header would let an attacker rotate lockout
keys at will). Behind this front every client shares the proxy's container IP,
so the per-pair key **collapses to per-account**. That is strictly tighter, never
looser — one guessed account cannot be attacked faster by rotating source
addresses — but it also means one noisy client can lock an account for everyone
on that path. The global budget is what bounds the damage.

If a genuine per-source-IP edge limit is wanted later, it needs a custom Caddy
build with `caddy-ratelimit`, and the front must then parse a trusted
`X-Forwarded-For` — a change with its own spoofing surface. Not free, and not
done here.

### Rollback — back to VPN-only in about a minute

```bash
docker compose stop caddy-public      # the public door closes immediately
```

The VPN entry on `:8090` is untouched by that command and keeps serving. If the
exposure is meant to stay off, delete the DNS record too — otherwise the name
keeps resolving to a host with nothing listening:

```bash
curl -s -X DELETE "https://api.cloudflare.com/client/v4/zones/$ZONE_ID/dns_records/$RECORD_ID" \
  -H "Authorization: Bearer $CF_TOKEN"
```

Nothing about this rollback touches Mailcow, `moovd`, or the store; only the
front is stopped.

---

## Operating

### Health and metrics

Both are on `moovd`'s operational listener (`:8080`), which is **never** proxied
publicly — it exposes account ids and sync state.

```bash
# From a container on the internal network:
docker run --rm --network moov-internal curlimages/curl -s http://moovd:8080/healthz
docker run --rm --network moov-internal curlimages/curl -s http://moovd:8080/metrics
```

| Metric | Meaning |
|---|---|
| `moov_sync_lag_seconds{account}` | Seconds since that account last made **any** sync progress — the newest of `mailboxes.last_synced_at` (what every incremental pass writes) and `sync_log.last_success_at` (what the initial sync and the watcher's handshake write). It used to read the second alone and was therefore un-alertable; that is fixed. |
| `moov_sync_watcher_idle_seconds{account}` | Seconds since that account's push watcher last did anything observable — an event, a pass, a sweep, a heartbeat. **The alert for a silently stalled watcher**; see below. |
| `moov_sync_stuck_divergences_total{account}` | Divergences the reconciler found, tried to repair, **verified afterwards**, and could not fix. **The alert for a mailbox the engine cannot heal on its own**; see below. Labeled by account, not by mailbox — which mailbox is a question the WARN line answers by name, and a mailbox label is unbounded per account. |
| `moov_sync_unsynced_seconds{account}` | Seconds that account has been the supervisor's responsibility **without ever completing an initial sync**. Absent on a healthy account — the sample disappears the moment the initial sync finishes, so "present and rising" is the whole condition. **The alert for a mailbox that was created and never synced**; see below. |
| `moov_sync_breaker_open{account}` | 1 while an account's circuit breaker is open. The breaker is the anti-fail2ban control (ADR §4), so this answers "who is locked out of Dovecot right now". |
| `moov_jmap_http_requests_total{route,status}` | JMAP requests by route pattern and status class. |
| `moov_jmap_http_request_duration_seconds{route}` | Latency histogram, bucketed around the 100 ms Gmail-class bar (regla 1). |
| `moov_jmap_method_calls_total{method,outcome}` | Per-method outcomes. This is the one that answers "is `Email/query` erroring?" — JMAP returns HTTP 200 with an error *invocation*, so an HTTP-only view reports a healthy server while every call fails. |
| `moov_parse_results_total{stage}` | Which stage of the S4 parse cascade produced each result. A jump in failures means a new class of message in the wild. |
| `moov_submissions_total{result}` | Terminal outcomes of the outbox: `sent` (the SMTP 250 was read *and persisted*), `failed` (a permanent 5xx or the retry cap), `canceled` (an undo inside the window). A transient re-queue counts as none of them — the message may still go out, so counting it would make the failure rate report retries. |
| `moov_jmap_sse_connections` | Open EventSource streams. A leak shows up here and nowhere else, since these are long-lived by design. |
| `moov_admin_actions_total{action,result}` | Accounts-API **writes** by verb (`create`, `suspend`, `resume`, `readonly`, `export`, `delete`, …) and `ok`/`error`. Reads are not counted: `GET` writes no audit line and no sample. It carries **no actor label** — that question wants a record, not a rate, and the audit row already names the actor on every line. |
| `moov_pending_exports` | Export jobs pending or running right now, observed by the runner at each 5 s tick. |
| `moov_delegated_exchanges_total{result}` | Delegated token-for-session exchanges: `ok` (a session was issued), `invalid` (the **token** was refused — the single 401), `account` (the token verified but the account could not be signed in — the 403s). `invalid` is deliberately **not** split by cause; that split is precisely the oracle the contract refuses to give over HTTP, and the reason already goes to a debug log line. |
| `moov_delegated_sessions_active` | Delegated sessions that are live: neither revoked nor past either expiry. Collected from the store at scrape time. |
| `moov_build_info{version,commit,go}` | Always 1; the labels identify the running build. |

**What is worth alerting on among the new four**, and what each one honestly
measures:

- `rate(moov_admin_actions_total{result="error"}[5m])` — a consumer's
  integration breaking, or Mailcow refusing. Alert on the *rate*, not on any
  single error: one `error` line is a consumer sending a bad field, which is
  the API working.
- `moov_pending_exports` — **the one worth a real alert.** It is a gauge, not
  a counter, precisely because the failure mode is a queue that stops
  draining. Exports are minutes of work, so a brief non-zero value is normal
  and a number that stays high for an hour means the runner is stuck — which
  a counter of started jobs could never show. Pair it with disk: every pending
  job becomes a copy of a mailbox.
- `rate(moov_delegated_exchanges_total{result="invalid"}[5m])` — either the
  issuer's key rotation went wrong or someone is probing. It cannot tell you
  which, by design; the log line can.
- `moov_delegated_sessions_active` — capacity and curiosity, not an alert. It
  is a gauge collected from the store rather than a counter incremented on
  issue and decremented on logout, because sessions die three ways no code
  path observes (the sliding expiry lapses, the absolute lifetime is reached,
  a suspend or delete cascades them away); a counter pair would drift on the
  first of those and never recover. **A failed collection emits no series at
  all rather than a zero** — "no sessions" and "the database did not answer"
  are different facts, and a dashboard that renders the second as the first
  shows a healthy flat line straight through an outage. Alert on the series
  being *absent*, never on it reading zero.

Each of these four reads the thing its name claims. Where one is imprecise it is
said above rather than left to be discovered.

#### Alerting on a stalled watcher

`moov_sync_watcher_idle_seconds` exists because of the 2026-09-16 incident, in
which push stopped for **every** account and nothing said so: no error, no
warning, no log line, and no metric that moved. The process was healthy, the
IMAP connections were open in `doveadm who`, and the breaker was closed. Mail
was delivered, stored by Dovecot, and never appeared in Moov for two hours.

The watcher now probes its own session after `MOOV_SYNC_IDLE_HEARTBEAT` (default
**2 minutes**) of no events, which both repairs the stall and logs it. This
gauge is the external half of that: it rises for as long as a watcher is quiet
and drops to zero whenever one does anything — including the heartbeat. So a
healthy watcher, even on a completely silent mailbox, can never exceed the
heartbeat period by much, and the alert is simply:

```promql
# The watcher has seen nothing for three heartbeat periods.
max by (account) (moov_sync_watcher_idle_seconds) > 360
```

Alert on the series being **absent**, too: no series for an account means no
watcher is running for it at all, which is the same outage arrived at from the
other direction. The gauge is pushed from the running process rather than read
from a table on purpose — the incident was precisely a process whose in-memory
loop had stopped while every persisted row stayed plausible.

The second-order backstop is the reconciler (`MOOV_SYNC_RECONCILE_INTERVAL`,
default **15 minutes**, was 6 h). It re-derives every folder's state with one
`LIST-STATUS` per account per sweep, so a divergence the heartbeat cannot see —
a session that is live and answering but whose events are being lost upstream —
self-corrects within that window. Each one it finds logs at WARN with the
counters that moved.

#### Alerting on a mailbox that was created and never synced

`moov_sync_unsynced_seconds` exists because of the **stranded-account** defect
of 2026-09-17 (a different one from the stuck-divergence defect below, same
day), found the first time a mailbox was created through the accounts API
against a **running** daemon.

**What happened.** An organiser created `unidos@corppass.events` through
`POST /admin/accounts`. Everything reported success: Mailcow had the mailbox,
the credential validated against Dovecot with a real IMAP LOGIN, the app
password was minted and sealed, the account row read `active`/`active`, and the
delegated session opened the webmail. The inbox then showed loading skeletons
**forever**. Hours later `sync` was still `{state: "initial", lastSyncAt: null,
messages: 0}`, `mailboxes` had zero rows and `Mailbox/get` returned an empty
list. Mail delivered to the mailbox did not change anything either.

The cause was one line: the supervisor read the eligible accounts **once**, at
startup, and then blocked. The supervised set was frozen at the instant the
process started, so an account created afterwards was invisible until somebody
restarted moovd — which is exactly the case the accounts API exists for, since a
portal creates mailboxes with nobody watching a terminal.

**What it does now.** Discovery is periodic (every 30 s) and the accounts API
additionally nudges the supervisor the moment it finishes provisioning, so the
common case is immediate and the sweep is the backstop for everything else
(`moovctl`, an operator's SQL, a second daemon, a create whose caller died). The
log says `adopting an account that appeared since startup` with the account id
and address.

**Why a new metric was needed at all.** Every existing series was silent by
construction, which is what made this defect invisible rather than merely
broken:

- `moov_sync_lag_seconds` emits **no sample** for an account that has never
  synced — an absent series being more honest than a zero.
- `moov_sync_watcher_idle_seconds` needs a watcher, and a stranded account has
  none.
- `moov_sync_stuck_divergences_total` needs a reconciler, which runs inside the
  watcher that does not exist.

There was nothing an `absent()` rule could catch either: the account had never
appeared in any series, so there was no disappearance to detect.

**The alert.**

```promql
# An account the supervisor has owned for five minutes without finishing one
# initial sync.
max by (account) (moov_sync_unsynced_seconds) > 300
```

The gauge is emitted from the moment the supervisor takes charge of an account
and **stops being emitted** when that account's initial sync completes —
including the cheap "already complete" case a restarted daemon takes. So a
healthy deployment exports this for a few seconds after start and then not at
all, and any sustained value is either an account that cannot connect (the log
says why: `initial sync failed; will retry`) or one that is stuck. A failed
attempt deliberately does **not** reset the clock: the number is how long the
account has been stranded in total, and resetting on each retry would cap it at
`RetryDelay` and make a permanently broken account look fresh every five
minutes.

**The gap this metric does not close, and why that is fine.** An account the
supervisor never adopted at all still exports nothing here, because this gauge
only knows what the supervisor told it. That gap is closed by the fix rather
than by the metric — an eligible account *is* adopted now, and adoption is what
starts this clock. The metric proves the adoption happened and got somewhere; it
is not a substitute for it.

#### Alerting on a divergence the reconciler cannot repair

`moov_sync_stuck_divergences_total` exists because of the 2026-09-17 defect,
which the idle heartbeat above uncovered within a day of shipping.

**What happened.** The heartbeat probes a quiet session by running `Reconcile`.
Overnight it fired 906 times and reported **180 divergences, all on one account,
always INBOX, with zero errors**. The account had 24,147 messages in INBOX on
Dovecot and 24,146 in Moov; diffing the UID lists gave exactly one missing UID,
a message five weeks old that some one-off failure had dropped. The reconciler
found the mismatch on every sweep, ran an incremental pass, and logged
`repaired=1` — **which was false every single time**. Two bugs stacked:

1. The incremental pass is *structurally incapable* of fixing that divergence.
   It resumes from the stored cursor and applies the delta above it; a UID far
   below the cursor is never in any delta it will be shown.
2. The result counted a repair whenever the pass returned **no error**. It never
   looked at the store afterwards. A "repaired" count that does not verify the
   repair is worse than no count, because it is precisely the number an operator
   trusts when deciding whether to investigate.

**What it does now.** After every repair attempt the sweep re-derives the same
comparison that detected the divergence — a fresh `STATUS` of that one mailbox
against the freshly reloaded row, plus the stored-row count. Only a divergence
that is actually **gone** counts as repaired. One that survives escalates to a
full backfill walk of the mailbox, which is the repair that *can* close a gap
below the cursor. One that survives even that is counted here and logged at WARN
as `reconciler could not repair a divergence; it persists`, naming the mailbox
and the reason that is still true.

**The alert.** A counter rather than a gauge, because the condition worth paging
on is a rate that does **not** fall back to zero:

```promql
# A mailbox the engine has been unable to repair for an hour.
sum by (account) (increase(moov_sync_stuck_divergences_total[1h])) > 10
```

A handful of increments is normal and self-healing — a folder changing under the
sweep, a walk that had not finished. A line that keeps rising means a mailbox
that no repair in the engine can close, and the WARN line names it. Expect the
usual causes in that order: a message whose bytes the parser refuses (check
`moov_parse_results_total{stage="failed"}` for the same account), a UID Dovecot
reports in `STATUS` but will not `FETCH`, or a genuine engine bug.

**Two bounds keep the repair from becoming its own incident**, and an operator
should know both before reading the numbers:

- **Per mailbox**, exponential backoff on a *persisted* counter: the first
  failure walks immediately, the second waits 15 minutes, then 30, then an hour,
  to a ceiling of one a day. It is persisted deliberately — the production gap
  was five weeks old, and an in-memory counter would reset on every deploy and
  re-authorise the hammer. It is cleared the moment the mailbox looks healthy
  again, so a folder that had one bad afternoon pays nothing on its next
  incident.
- **Per sweep**, at most **one** backfill walk across all of an account's
  mailboxes. The account above has 24 folders and 26,869 messages; without this,
  a sweep that found several diverged would walk several 20k folders back to
  back. A mailbox that does not get the budget is still detected, still counted
  here, still logged by name, and gets it on the next sweep.

So the honest worst case for an account with many broken folders is **one folder
repaired per reconcile interval** — in a situation where the previous behaviour
was zero, forever, while reporting success.

**A backfill walk is safe to let run on a live account.** It re-fetches UIDs
through the same idempotent path the initial sync uses: a UID already present is
skipped, never rewritten. Nothing is deleted, nothing is reset, and `UIDVALIDITY`
is checked before any watermark is trusted. It costs IMAP fetches and changes
nothing a user can see.

`/healthz` is a **liveness** probe: it reports that the process and its HTTP
stack are up, and deliberately does *not* check the database. A health check
that failed on a PostgreSQL blip would have Docker restart a healthy daemon
during a database restart, turning a recoverable outage into a crash loop.
Store problems surface through `moov_sync_lag_seconds` rising and through the
logs, where an operator can act on them.

The container healthcheck is `moovd -health`, which probes its own `/healthz`.
The image is distroless — no shell, no curl — so the binary probes itself.

### Logs

Structured JSON, one record per event:

```bash
docker compose logs -f moovd
docker compose logs moovd | grep '"level":"ERROR"'
```

The JMAP request log records the path but never the query string and never a
header — the `Authorization` header passing through this server is a password,
and since the scoped-token change the query string can carry an
`access_token` (a short-lived, single-scope capability for EventSource and
downloads; `internal/jmaphttp/token.go`). A test pins the redaction
(`TestLogMiddlewareNeverLogsQueryString`).

**The fronting Caddy must observe the same rule.** Caddy's access log, when
enabled, records the full URI including the query. Either keep access logging
off for `/jmap/*` (the pilot's default vhost has no `log` directive, which is
Caddy's default: no access log), or accept that a VPN-only log briefly holds
capabilities that expire in 10 minutes and grant one scope each. Do not ship
a public deployment that writes `access_token` values to a log shipped
anywhere.

### Upgrading

The pilot host is **not** a git checkout — the tree is copied there — so an
upgrade is: back up the running tree, upload the new one beside it, restore
`deploy/.env` into it, build, then swap and start.

```bash
# On the host, with the new tree already uploaded to /opt/moov/src.new:
cp -a /opt/moov/src /opt/moov/src.pre-<name>        # rollback point
cp /opt/moov/src/deploy/.env /opt/moov/src.new/deploy/.env
cd /opt/moov/src.new/deploy && docker compose build moovd
mv /opt/moov/src /opt/moov/src.old && mv /opt/moov/src.new /opt/moov/src
cd /opt/moov/src/deploy && docker compose up -d moovd
```

The PWA is a bind mount of `web/dist`, so a web change additionally needs
`docker compose up -d --force-recreate caddy-public` — the container holds the
old directory's inode otherwise and keeps serving the previous bundle.

Rollback is the same swap in reverse: `mv` the backup back into `src` and
`docker compose up -d`.

Migrations apply on start. `moovd` drains in-flight requests within
`MOOV_SHUTDOWN_TIMEOUT` (30 s default) before exiting.

---

## Security notes

- `.env` is `chmod 600` and git-ignored. It holds the master key and the
  database password.
- The image runs as `nonroot` (uid 65532) with `no-new-privileges`, on
  `distroless/static`: no shell, no package manager, no libc to patch.
- `sslmode=disable` on the database URL is correct **here and only here**: that
  connection never leaves a private Docker bridge that only two containers join.
- Phase 1 serves **raw HTML** in `bodyValues` over the authenticated API. The
  three-layer sanitization (ADR §5) is a requirement of Moov's own PWA in phase
  2; a third-party client does its own. See `SECURITY.md`.
- Scoped access tokens (`/jmap/token`, `internal/jmaphttp/token.go`) need **no
  deployment work**: no new environment variable, no migration, no new route in
  the fronting proxy (they ride the existing `/jmap/*` paths). The signing key
  is random per process, so **restarting `moovd` invalidates every outstanding
  token** — by design; the PWA re-mints on its refresh cycle. The only operator
  concern is the access-log rule under *Logs* above.

---

## Troubleshooting

**`moovd` exits immediately with a configuration error.** A required variable is
missing. The message names it. The daemon refuses to start rather than run in a
degraded state — "started with the wrong database" is worse than "did not
start".

**Blob writes fail with `permission denied`.** The named volume was created
before the image declared the blob root's ownership. Docker seeds an empty
volume from the image's directory *including its owner*, so a volume created
against an older image stays root-owned:

```bash
docker compose down && docker volume rm moov-blobs && docker compose up -d --build
```

**Accounts do not sync after being added.** The supervisor enumerates accounts
at start: `docker compose restart moovd`.

**The stack refuses to start with `network … declared as external, but could not
be found`.** One of the two external networks is missing. Both are joined, never
created: Mailcow's comes from Mailcow, and the front's (`moov-spike`) from
whatever stood the front up. Create the front's network if it is genuinely gone
(`docker network create moov-spike`) and restart the front so it rejoins;
failing to start is the right behaviour here, since a moovd nothing can reach is
not a working deployment.

**The front answers 502 after a deploy.** Check the attachment survived —
`docker inspect moovd --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}'`
must list the front's network. Since compose owns it, the fix is
`docker compose up -d` and not a manual `docker network connect`; if a bare
`docker network connect` is what makes it work, this stack is running an old
`docker-compose.yml` that predates the declaration.

**Certificate errors dialing Dovecot.** `MOOV_IMAP_SERVER_NAME` must be the
hostname on Mailcow's certificate, not the container alias Moov dials (spike S1
H2). Moov verifies against the certificate's name rather than disabling
verification.

**A client gets `unknownCapability` for `urn:ietf:params:jmap:submission`.** Was
expected in phase 1, and is a real fault now: phase 2 implements submission and
the session advertises it whenever the daemon mounts the submission methods. If
a client still sees this, the deployment is running a phase-1 image.

**Migration 0004 takes tens of seconds on an existing store.** Expected, once.
It backfills `thread_id` over every message already synced, and the pilot's
26,869-message account took **29.5 s** (0005 adds ~0.2 s). The daemon does not
serve until migrations finish, so a redeploy onto a populated store is not the
sub-second restart an empty one is. A fresh deployment pays nothing: there are
no rows to backfill.

**`moov_sync_lag_seconds` reads high on a healthy system — FIXED.** This was a
real defect and the gauge is now honest: the collector reads
`store.AccountLastProgressAt`, which takes the newest of
`mailboxes.last_synced_at` (written by every incremental pass the watcher runs)
and `sync_log.last_success_at` (written by the initial sync and the watcher's
handshake). Previously it read the second alone, which only the initial sync
ever advances, so an account whose watcher was working perfectly reported days
of lag — 8.6 of them on the pilot — while mail arrived in seconds. The old
instruction "do not alert on this gauge" no longer applies; **alert on it.**

Per-mailbox freshness is a different question and is still worth asking
directly when investigating one account:

```sql
SELECT name, last_synced_at, now() - last_synced_at AS age
FROM mailboxes WHERE account_id = $1 ORDER BY last_synced_at DESC;
```

**A mailbox created through the accounts API never syncs — FIXED.** This was
the 2026-09-17 defect: the supervisor read its account list once at startup, so
anything created later stayed invisible until a restart. Discovery is now
periodic (30 s) and provisioning nudges it, and
`moov_sync_unsynced_seconds{account}` is the series that shows a mailbox stuck
in that state. If it recurs, check in this order: the account row is
`state='active'` **and** `credential_state='active'` (the supervisor skips
anything else on purpose, so it does not hand fail2ban a failed login); the log
carries `adopting an account that appeared since startup` for that id; and no
`discovering accounts failed` WARN is repeating, which would mean the sweep
itself cannot read the database and new accounts are silently not being picked
up.

**New mail stops appearing, with no error anywhere.** This was the 2026-09-16
incident and it now self-heals within `MOOV_SYNC_IDLE_HEARTBEAT` (2 min). If it
recurs, the log is no longer silent — look for `watcher idle; probing the
session` (INFO, every heartbeat on a quiet account), `watcher heartbeat found
divergence` (WARN — the session was alive but not current: events are being
lost), and `watcher heartbeat failed; the session is dead, reconnecting` (WARN,
followed by the ordinary reconnect and its sweep). An account with none of
those lines and a rising `moov_sync_watcher_idle_seconds` has no watcher at
all, which is a supervisor problem rather than a session one.
