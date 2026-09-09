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
typed. `logo`, `logoDark`, `icon` and `splash` name **files sitting beside
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
picture: the maskable and Apple icons sit on an **opaque plate of `primary`**,
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
**Recommended icon:** **square, at least 512 px, PNG with alpha** — and **use
the glyph your kit draws for dark backgrounds if your primary is dark**, since
the maskable and Apple icons sit on a plate of the primary color. `moovctl`
warns when an `-icon` is further than 10% from 1:1, because a launcher shows a
square and a wide image ends up small between bands of that plate.
**Recommended splash:** a photograph at least 1600 px wide — it is rendered
`object-fit: cover`, so it is cropped to the panel, not letterboxed.

What the icon generator does, on demand and cached. Its source is `icon` when
there is a usable one, `logo` otherwise, and Moov's own mark when neither can
be rendered:

| Icon | Size | Padding each side | Plate, from `logo` | Plate, from `icon` |
|---|---|---|---|---|
| `icon-192`, `icon-512` | 192, 512 | 10% | transparent | **opaque, the primary** |
| `icon-maskable-192`, `icon-maskable-512` | 192, 512 | 20% | opaque, the primary | opaque, the primary |
| `apple-touch-icon` | 180 | 10% | opaque, the primary | opaque, the primary |
| `favicon-32` | 32 | none (10% when plated) | transparent | **opaque, the primary** |

**When the source is a dedicated `icon`, every size is plated.** An operator
who supplies one supplies a mark drawn *for* that plate — and the alternative
was found live on the pilot: Areacorp's white glyph rendered correctly on the
maskable pair and then **vanished** on `icon-192`, `icon-512` and `favicon-32`,
which were transparent, on a light desktop launcher and a light browser tab.
`favicon-32` also picks up a 10% padding floor when plated, because its own
spec has none and a square mark would otherwise cover the plate edge to edge —
the same white-square-on-a-light-tab failure. **When the source is the `logo`,
nothing changed:** the transparent column is exactly what it always was.

The source image is contained inside the padded square with its aspect ratio
preserved and centred — which is why a square `icon` fills it and a wide `logo`
does not. The maskable pair pads to 20% because Android adaptive icons keep
only the inner 80% circle, and paints the plate because a transparent one would
be masked onto whatever the launcher picks (usually white). `apple-touch-icon`
is opaque because iOS discards alpha and composites onto **black**. **Those
three plates are the reason `icon` exists:** on a dark primary, a dark mark
disappears into them.

### The `moovctl` workflow

`moovctl` writes to the **host** filesystem, and `moovd`'s image is distroless.
Two things that look like they should work do not, and both were verified on
the pilot: there is **no Go on the host**, so "run it from a checkout" is not
an option, and `docker compose run -v …` **cannot** override the service's
read-only branding bind — it fails with `read-only file system`.

Run a **fresh container from the same image** instead:

```bash
IMG=$(docker inspect moovd --format '{{.Config.Image}}')
docker run --rm --user root \
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
- **`--user root`** — the branding directory is root-owned on the host, and the
  image runs non-root. Without it the write fails on permissions.
- **`-v /etc/moov/branding:/etc/moov/branding`** — writable, unlike the
  service's own bind, and at the same path both sides so `MOOV_BRANDING_DIR`
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
docker run --rm --user root \
  -v /etc/moov/branding:/etc/moov/branding \
  -v /root/brand:/brand:ro \
  --entrypoint /usr/local/bin/moovctl "$IMG" \
  branding set \
    -host mail.acme.example \
    -name 'Acme Mail' \
    -short-name 'Acme' \
    -tagline 'Correo corporativo de Acme S.A.' \
    -support-url 'mailto:soporte@acme.example' \
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
RUN="docker run --rm --user root -v /etc/moov/branding:/etc/moov/branding:ro \
  --entrypoint /usr/local/bin/moovctl $IMG"

$RUN branding show -host mail.acme.example   # every field, plus where the PWA icons come from
$RUN branding list                           # every configured host

# unset WRITES, so it needs the mount writable (drop the :ro):
docker run --rm --user root -v /etc/moov/branding:/etc/moov/branding \
  --entrypoint /usr/local/bin/moovctl "$IMG" \
  branding unset -host mail.acme.example     # back to Moov's defaults
```

`unset` removes `branding.json` and the image files it wrote (only those, by
their recorded names — never a blanket wipe of a directory an operator may have
put something else in); `-keep-assets` leaves the images.

### Deploy wiring

`MOOV_BRANDING_HOST_DIR` in `.env` is the **host** path; compose bind-mounts it
**read-only** at `/etc/moov/branding` and sets `MOOV_BRANDING_DIR` to that
container path for you. It defaults to `/etc/moov/branding` on both sides, so
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
