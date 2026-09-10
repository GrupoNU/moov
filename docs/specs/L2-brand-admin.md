# L2 — Brand administration panel ("Marca") with domain roles

> Status: **DESIGN ONLY — approved direction (option A), not built.** Owner decision 2026-09-10.
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

## 2. Who may administer a host — option A (chosen)

**Mailcow is the source of truth for "who administers a domain", exactly as it is for
mail.** Moov has no role table of its own.

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

## 3. Host ↔ domain binding

Branding is keyed by **host** (mail.acme.example); Mailcow roles are keyed by
**domain** (acme.example). The binding is operator data, not a guess:

```
<MOOV_BRANDING_DIR>/<host>/branding.json  →  "adminDomains": ["acme.example", "acme.com.ar"]
```

Written by `moovctl branding bind -host mail.acme.example -domain acme.example`.
A host with no `adminDomains` has no brand admins (CLI only). This keeps the
anti-enumeration property: the public document never exposes `adminDomains`.

## 4. API (authenticated, same session as JMAP)

All under `/branding/admin`, JSON, CSRF-safe (same-origin + custom header + the
existing session cookie rules), rate-limited like uploads:

| Method | Path | Notes |
|---|---|---|
| GET | `/branding/admin/{host}` | full file view (incl. asset names, `adminDomains`), 404 unless admin |
| PUT | `/branding/admin/{host}` | text fields + colours; validated by the SAME functions the CLI uses (`normalizeHexColor`, `safeSupportURL`, short-name ≤12) |
| PUT | `/branding/admin/{host}/assets/{logo\|logoDark\|icon\|splash}` | raw image body ≤ 2 MiB, sniffed; SVG refused; icon non-square → 200 with `warnings[]` |
| DELETE | same | removes the asset |
| GET | `/branding/admin/{host}/preview?primary=%23hex` | returns the derived palette (server-side port of `palette.ts` is NOT built: the client derives; this endpoint only exists if we later need parity for e-mail templates) |

Writes are **atomic per file** (temp + rename, as `moovctl` does) and invalidate the
in-process branding cache for that host immediately (today's 60 s TTL is for the
CLI path; the UI must see its own save on the next paint).

Audit: every write logs `host, actor, field/asset, bytes, sha256` — the file
directory is the state, the log is the history.

## 5. UI (Settings → Marca)

- Reuses the settings page chrome (E12 B3), one tab "Marca", visible only when
  `GET /branding/admin/{host}` answers 200 for the current host.
- **Live preview** without saving: the page applies `derivePalette(primary)` to a
  scoped preview container (a mini top bar + a list row + a primary button, light
  and dark side by side) using the existing seeds; the "adjusted for AA" note the
  console prints today becomes a visible inline notice ("Tu color se ajustó en el
  tema oscuro a #… para seguir legible").
- Uploads show the generated icons (`/branding/icons/*` are re-fetched with a
  cache-busting query after save) and the login split panel thumbnail.
- Danger zone: "Volver a la marca de Moov" = `unset`.
- Everything keyboard-reachable; no layout change elsewhere (Gmail rule).

## 6. Epics and sizes

| # | Epic | Size | Notes |
|---|---|---|---|
| BA-0 | **Spike (1 day):** confirm on our Mailcow the domain-admin API shape, RO key support and IP allowlist, and the username↔mailbox convention (§2b). Deliverable: `docs/spikes/S5-mailcow-domain-admins.md`. | S | gates BA-1 |
| BA-1 | Server: Mailcow RO client (one method), authorizer, `adminDomains` binding + `moovctl branding bind/unbind`, admin API with atomic writes, cache invalidation, audit log, tests (authz matrix, hostile uploads, anti-enumeration of the admin routes) | M | Fable (security boundary) |
| BA-2 | Web: Marca tab, live preview reusing `palette.ts`, uploads, warnings, unset; tests; live gate on the pilot with Areacorp's admin | M | Opus |
| BA-3 | Docs: operator guide (bind, key scope), admin guide (what the panel does), SECURITY.md note on the new key | S | Opus |

## 7. Decisions requested from the owner (answered)

- **D-BA1** Role source: Mailcow domain admins (option A). **Approved 2026-09-10.**
- **D-BA2** Mailcow key in moovd: accepted with the mitigations of §2 (RO key, IP scope,
  envelope encryption, feature off without key). *Proposed; confirm at BA-0 exit.*
- **D-BA3** Deny-on-Mailcow-unavailable for writes. *Proposed.*
