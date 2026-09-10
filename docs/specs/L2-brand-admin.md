# L2 — Brand administration panel ("Marca") with domain roles

> Status: **BA-1 BUILT (2026-09-10) with one change to §2: the FIRST role source is an
> operator-granted list (`moovctl branding grant`); Mailcow domain admins are a SECOND
> provider behind the same interface, not built yet.** Design direction (option A) approved
> by the owner 2026-09-10; BA-2 (web) in progress, BA-0 and the Mailcow provider pending.
> Size: L (3 epics, ~2 weeks incl. a 1-day spike). Depends on the per-host branding
> shipped 2026-09-09 (`internal/jmaphttp/branding*.go`, `web/src/branding/*`).

## 1. Goal

A domain administrator opens Settings → **Marca** on their own host, uploads logo /
dark logo / square icon / splash, picks the primary colour with a live preview of
the real app palette (light and dark), edits name, short name, tagline, support,
privacy and terms URLs, and saves. The result is exactly what `moovctl branding set`
writes today; the CLI remains the operator's tool and the source of truth format
(`<MOOV_BRANDING_DIR>/<host>/branding.json` + images) does not change.

Non-goals: per-user themes, per-mailbox branding, removing the "Powered by Moov"
attribution (commercial licence topic, out of scope), SVG uploads (still refused).

## 2. Who may administer a host — option A (chosen), built as two providers

**As built (BA-1):** authorization is a `BrandAdminSource` interface with one method,
`IsBrandAdmin(ctx, host, mailbox)`, behind a composite that ORs its providers in order
(first "yes" wins; the first error DENIES — a provider that cannot answer is never
skipped to ask the next).

- **Provider 1 (built): the operator-granted list.** `branding.json` gains
  `brandAdmins: ["u@d", ...]` (lowercased mailboxes), written by
  `moovctl branding grant -host H -user u@d` / `revoke`, shown by `show` (row BRAND
  ADMINS) and `list`. It is read through the branding store, so it rides the same 60 s
  cache as the document and is invalidated immediately by every write through the API.
  The public `GET /branding` NEVER carries it (pinned by test): a stranger learns no
  mailbox, a non-admin learns no admin.
- **Provider 2 (designed, not built): Mailcow domain admins**, exactly as described
  below. It slots in as `jmaphttp.Config.BrandAdminSources` without touching the routes;
  a test already drives the composite with a fake second provider. Everything below
  about the key, its scope and deny-on-unavailable still applies to it when it is built.

**Mailcow is the source of truth for "who administers a domain", exactly as it is for
mail** — for provider 2. Moov has no role table of its own beyond the per-host grant
list, which is operator data in the same file as the brand.

- A user is a **brand admin of host H** when (a) they are logged into Moov with a
  mailbox `u@D`, and (b) Mailcow lists a *domain admin* whose username equals `u@D`
  OR whose domains include `D` and whose username equals the mailbox local part
  (convention to be confirmed by the spike, §6), AND (c) host H is *bound* to domain
  D (see §3). Mailcow *global* admins are not mapped: they administer through
  `moovctl`, not through a mailbox session.
- Authorization is evaluated **server-side on every write**, from a cached read of
  `GET /api/v1/get/domain-admin/all` (TTL 60 s, same shape as the branding cache),
  never from a client claim. Reads of the current brand are public already.
- **New trust boundary:** today `moovd` holds no Mailcow API key; only `moovctl
  account add` uses one at provisioning time. Option A puts a key into the daemon.
  Mitigations, all mandatory: a **read-only** Mailcow API key (Mailcow supports RO
  keys) scoped by allowed IP to the moovd container; stored via the existing
  `MOOV_MASTER_KEY` envelope like app passwords; the single call above and nothing
  else (an interface with one method, pinned by test); a startup log line naming the
  key's role; the feature is **off** when the key is absent (Settings hides Marca
  and the API answers 404, indistinguishable from "no such route").

Fallback when Mailcow cannot answer (timeout, 5xx): **deny**, with a clear message.
A branding write is never urgent enough to guess.

## 3. Host ↔ domain binding (provider 2 only)

> Not needed by provider 1: a grant names the host directly. The binding below is what
> the Mailcow provider will need to map the domain of a mailbox to the host it may edit.

Branding is keyed by **host** (mail.acme.example); Mailcow roles are keyed by
**domain** (acme.example). The binding is operator data, not a guess:

```
<MOOV_BRANDING_DIR>/<host>/branding.json  →  "adminDomains": ["acme.example", "acme.com.ar"]
```

Written by `moovctl branding bind -host mail.acme.example -domain acme.example`.
A host with no `adminDomains` has no brand admins (CLI only). This keeps the
anti-enumeration property: the public document never exposes `adminDomains`.

## 4. API (authenticated, same session as JMAP) — as built

All under `/branding/admin`, authenticated by the route table's default (the same
`Authorization` header every JMAP call carries; there are no cookies in this server, so
there is no CSRF surface and no token to carry), then authorized per host. **There is NO
`{host}` in the URL** — the design's `/branding/admin/{host}` paths are superseded: an
admin edits the brand of the host they are ON, resolved from the `Host` header exactly as
`GET /branding` resolves it. That removes a whole class of cross-host bugs (a body for
host A written under host B) and makes the authorization question a single one.

| Method | Path | Notes |
|---|---|---|
| GET | `/branding/admin` | `200 {"host","canEdit":true}` for an admin of this host; **404** (the generic problem body, identical to an unknown route) otherwise |
| GET | `/branding/admin/brand` | `200 BrandAdminDoc`; 404 otherwise |
| PUT | `/branding/admin/brand` | partial JSON `{name?, shortName?, tagline?, supportUrl?, privacyUrl?, termsUrl?, colors?: {primary?, onPrimary?, splashFrom?, splashTo?}}`; absent = unchanged, `""` = clear (a cleared colour is Moov's again); validated in full by the SAME rules the CLI applies (`NormalizeHexColor`, `SafeURL`, shortName ≤ 12, name ≤ 64, tagline ≤ 160, no control characters, unknown fields refused) — `400 {"field","reason"}` names the FIRST invalid field and nothing is written; `415` on a non-JSON Content-Type; `413` past 64 KiB; `200 BrandAdminDoc` |
| PUT | `/branding/admin/assets/{logo\|logoDark\|icon\|splash}` | raw image body, `Content-Type: image/*` (else 415), ≤ 2 MiB (else 413), sniffed like the CLI — SVG → `415 {"reason"}` naming SVG, HTML-as-PNG → 415, empty → 400; stored under the CLI's names (`logo.png`, `logo-dark.jpg`, ...); `200 BrandAdminDoc` whose `warnings` carry the non-square-icon / not-renderable notes in the CLI's exact wording |
| DELETE | same | file removed, field cleared; idempotent; `200 BrandAdminDoc` |
| POST | `/branding/admin/reset` | back to Moov's brand like `moovctl branding unset`, **`brandAdmins` preserved** so the caller keeps access; `200 BrandAdminDoc` |

The preview endpoint of the original table is not built (the client derives the palette).

`BrandAdminDoc`: `host`, `default`, the six text fields **as configured** (empty when
unset), `colors` as **effectively served**, `assets.{logo,logoDark,icon,splash}` as
`{url, bytes, width?, height?}` or `null` — `url` carries `?v=<sha256 prefix>` as a
cache-buster the public asset route ignores — `iconSource` (`icon`/`logo`/`default`),
`iconIssue`, `brandAdmins`, `warnings[]`, `publicUrl`, `manifestUrl`,
`iconUrls{name: path}` (with their own buster once a source exists) and `version` (the
public document's ETag, unquoted).

Writes: **rate-limited per actor** (token bucket, 10/min, `429` + `Retry-After`),
**serialized per host** (a mutex around read-modify-write of `branding.json`), and
**switched off wholesale** by `MOOV_BRANDING_ADMIN=0` (every route 404, indistinguishable;
also 404 whenever `MOOV_BRANDING_DIR` is empty).

Writes are **atomic per file** (temp + rename) through **one writer, `internal/branding`**,
which `moovctl` now calls too — a test in `cmd/moovctl` runs the same scenario through
both and diffs the two directories byte for byte. Every write invalidates the in-process
document AND icon cache for that host immediately (the 60 s TTL is for the CLI path; the
UI sees its own save on the next paint, and the icons re-render).

Audit: every write logs `host, actor, field/asset, bytes, sha256` — the file
directory is the state, the log is the history.

## 5. UI (Settings → Marca)

- Reuses the settings page chrome (E12 B3), one tab "Marca", visible only when
  `GET /branding/admin` answers 200 on the current host (404 = not an admin here, or the API is off).
- **Live preview** without saving: the page applies `derivePalette(primary)` to a
  scoped preview container (a mini top bar + a list row + a primary button, light
  and dark side by side) using the existing seeds; the "adjusted for AA" note the
  console prints today becomes a visible inline notice ("Tu color se ajustó en el
  tema oscuro a #… para seguir legible").
- Uploads show the generated icons (`/branding/icons/*` are re-fetched with a
  cache-busting query after save) and the login split panel thumbnail.
- Danger zone: "Volver a la marca de Moov" = `POST /branding/admin/reset` (like `unset`, but the admin list survives so the panel stays reachable).
- Everything keyboard-reachable; no layout change elsewhere (Gmail rule).

## 6. Epics and sizes

| # | Epic | Size | Notes |
|---|---|---|---|
| BA-0 | **Spike (1 day):** confirm on our Mailcow the domain-admin API shape, RO key support and IP allowlist, and the username↔mailbox convention (§2b). Deliverable: `docs/spikes/S5-mailcow-domain-admins.md`. | S | gates BA-1 |
| BA-1 | **Built 2026-09-10.** Server: `BrandAdminSource` + composite, provider 1 = operator-granted list (`moovctl branding grant/revoke`), admin API (no `{host}` in the URL) with atomic writes through the shared `internal/branding` writer, cache invalidation, audit log, per-actor budget, per-host mutex, `MOOV_BRANDING_ADMIN` kill switch; tests for the whole AC list. **Not built:** the Mailcow provider (BA-0 spike + `adminDomains` binding), which slots into `Config.BrandAdminSources`. | M | Fable (security boundary) |
| BA-2 | Web: Marca tab, live preview reusing `palette.ts`, uploads, warnings, unset; tests; live gate on the pilot with Areacorp's admin | M | Opus |
| BA-3 | Docs: operator guide (bind, key scope), admin guide (what the panel does), SECURITY.md note on the new key | S | Opus |

## 7. Decisions requested from the owner (answered)

- **D-BA1** Role source: Mailcow domain admins (option A). **Approved 2026-09-10.**
- **D-BA2** Mailcow key in moovd: accepted with the mitigations of §2 (RO key, IP scope,
  envelope encryption, feature off without key). *Proposed; confirm at BA-0 exit.*
- **D-BA3** Deny-on-Mailcow-unavailable for writes. *Proposed.*
