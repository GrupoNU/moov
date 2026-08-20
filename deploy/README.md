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
| `moov_sync_lag_seconds{account}` | Seconds since that account's oldest scope last synced. The **oldest** scope, so a single stalled folder cannot hide behind a busy one. |
| `moov_sync_breaker_open{account}` | 1 while an account's circuit breaker is open. The breaker is the anti-fail2ban control (ADR §4), so this answers "who is locked out of Dovecot right now". |
| `moov_jmap_http_requests_total{route,status}` | JMAP requests by route pattern and status class. |
| `moov_jmap_http_request_duration_seconds{route}` | Latency histogram, bucketed around the 100 ms Gmail-class bar (regla 1). |
| `moov_jmap_method_calls_total{method,outcome}` | Per-method outcomes. This is the one that answers "is `Email/query` erroring?" — JMAP returns HTTP 200 with an error *invocation*, so an HTTP-only view reports a healthy server while every call fails. |
| `moov_parse_results_total{stage}` | Which stage of the S4 parse cascade produced each result. A jump in failures means a new class of message in the wild. |
| `moov_submissions_total{result}` | Terminal outcomes of the outbox: `sent` (the SMTP 250 was read *and persisted*), `failed` (a permanent 5xx or the retry cap), `canceled` (an undo inside the window). A transient re-queue counts as none of them — the message may still go out, so counting it would make the failure rate report retries. |
| `moov_jmap_sse_connections` | Open EventSource streams. A leak shows up here and nowhere else, since these are long-lived by design. |
| `moov_build_info{version,commit,go}` | Always 1; the labels identify the running build. |

`/healthz` is a **liveness** probe: it reports that the process and its HTTP
stack are up, and deliberately does *not* check the database. A health check
that failed on a PostgreSQL blip would have Docker restart a healthy daemon
during a database restart, turning a recoverable outage into a crash loop.
Store problems surface through `moov_sync_lag_seconds` going stale and through
the logs, where an operator can act on them.

The container healthcheck is `moovd -health`, which probes its own `/healthz`.
The image is distroless — no shell, no curl — so the binary probes itself.

### Logs

Structured JSON, one record per event:

```bash
docker compose logs -f moovd
docker compose logs moovd | grep '"level":"ERROR"'
```

The JMAP request log records the path but never the query string and never a
header — the `Authorization` header passing through this server is a password.

### Upgrading

```bash
cd /opt/moov/src && git pull
cd deploy && docker compose up -d --build
```

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

**`moov_sync_lag_seconds` reads high on a healthy system.** Known limitation of
E8-lite. The gauge is computed from `sync_log.last_success_at`, which the
*initial* sync writes; the steady-state watcher records its progress on
`mailboxes.last_synced_at` instead. An account whose watcher is working
perfectly therefore reports a lag measured from its last full pass — days, on
the pilot — while mail arrives in seconds. Until the collector reads the
mailbox column, **do not alert on this gauge**; `mailboxes.last_synced_at` is
the honest freshness signal:

```sql
SELECT name, last_synced_at, now() - last_synced_at AS age
FROM mailboxes WHERE account_id = $1 ORDER BY last_synced_at DESC;
```
