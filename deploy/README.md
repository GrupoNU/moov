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

`moovd` must be on the front's network for that to resolve:

```bash
docker network connect moov-spike moovd
```

**Rollback to the S1 `jmap-proxy` is one line** — change the single
`reverse_proxy` target in the `@jmap` handler and reload:

```bash
docker exec moov-caddy-spike caddy reload --config /etc/caddy/Caddyfile
```

The proxy stays running as the S1 oracle (L2 §2.5): when a mapping is in doubt,
its answer is the reference.

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

**Certificate errors dialing Dovecot.** `MOOV_IMAP_SERVER_NAME` must be the
hostname on Mailcow's certificate, not the container alias Moov dials (spike S1
H2). Moov verifies against the certificate's name rather than disabling
verification.

**A client gets `unknownCapability` for `urn:ietf:params:jmap:submission`.**
Expected in phase 1: submission is not implemented and therefore not advertised.
Clients that poll `Identity/get` (Bulwark does) log this repeatedly and read mail
regardless.
