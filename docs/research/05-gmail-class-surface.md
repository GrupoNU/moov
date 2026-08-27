# 05 — The Gmail-class surface: what a complete webmail actually consists of

> **Status:** research / evidence base. Not a plan, not a decision. Uncommitted by instruction.
> **Date:** 2026-08-26 · **Repo state:** `main` @ `542250c`
> **Purpose.** The product owner's verdict on the live PWA was: *"it has NOTHING in settings — when I say nothing I mean absolutely nothing. Bulwark had a very complete settings panel. It doesn't do even 20% of what Bulwark does."* This document exists so the director can plan against a **verified inventory** rather than against memory. It is deliberately exhaustive.
> **Governing rule.** ADR-001 and `CLAUDE.md` rule 1: the benchmark is Gmail/Fastmail/Superhuman. Bulwark is the **floor to exceed**, not the ceiling.

---

## 0. Method, sources, and what could NOT be verified

### 0.1 What was done

| Workstream | Method | Confidence |
|---|---|---|
| Bulwark source | **Full clone of `github.com/bulwarkmail/webmail`** (v1.9.2, `5dcef71`), read exhaustively: `lib/jmap/client.ts` (320 KB / 8,042 lines), `stores/email-store.ts` (210 KB / 4,690 lines), all 33 `components/settings/*.tsx`, `stores/settings-store.ts` (63 KB), `lib/sieve/{generator,parser}.ts`, all hooks, `public/sw.js`, `locales/`, `e2e/`, `integration/`, `CHANGELOG.md`, `FEATURES.md` | **High** — direct source |
| Bulwark runtime | Playwright against `https://bulwark.atmosfera.cloud` + `curl` of `/api/config` | **Partial** — see 0.2 |
| Gmail | Google's own help documentation (`support.google.com/mail`), fetched live | **High** where cited; gaps marked |
| Moov PWA | Full read of `web/src` (90 TS/TSX files, 20,377 lines incl. tests; 14,015 non-test) | **High** — direct source |
| Moov server | `internal/jmap/**`, `internal/jmaphttp/**`, `internal/store/migrations`, `docs/specs/L2-*.md` | **High** — direct source |

### 0.2 What could NOT be verified, and why — stated plainly

1. **I could not log into Bulwark's authenticated UI.** The brief said credentials would be in `MOOV_TEST_USER` / `MOOV_TEST_PASSWORD`. **They are absent from the environment** (verified: `env | grep -i moov` returns nothing; 86 env vars, none matching). Per the brief's own instruction I did **not** read any credentials file and did **not** touch the three real accounts (`diego@gruponu.com`, `comercial@areacorp.com.ar`, `info@areacorp.com.ar`).
   - **Consequence:** every Bulwark settings screenshot of an *authenticated* screen is missing. `https://bulwark.atmosfera.cloud/es/settings` **redirects to `/es/login`** when unauthenticated (verified).
   - **Demo mode is off** — `GET /api/config` returns `"demoMode":false`, so the fixture-data path that would have rendered the UI without a server is unavailable.
   - **Mitigation:** the entire settings inventory in §1 is derived from **source code**, which is strictly more complete than a UI walk (it includes conditionally-gated tabs a walk would never reveal). What is genuinely lost is *visual* evidence and *observed network traffic*. Network traffic was recovered analytically by reading the JMAP client's call sites.
2. **The deployed Bulwark is v1.8.1; the source I read is v1.9.2.** The login page self-reports `Version: 1.8.1 · Build: a066108 · Latest: 1.9.2` and displays a **"Security update available"** banner for **GHSA-24w9-8r42-8jwm** (a DNS-rebinding SSRF in `/api/fetch-ical`, fixed in 1.9.2). Deltas 1.8.1→1.9.2 that affect this inventory are noted inline. *(Operational note, not part of the brief: our pilot is running a Bulwark with a published advisory.)*
3. **No production deploys, no writes to real accounts, nothing committed** — as instructed.
4. **Gmail's UI was not walked** — it is behind a Google account. Everything in §2 is from Google's published documentation, with per-cluster URLs. Items I could not source are explicitly marked `unsourced`.

### 0.3 Evidence files

`docs/research/evidence/bulwark/` — `00-login.jpeg` (Bulwark login, showing the version/advisory banner and the PWA install prompt), `10-moov-login-for-contrast.jpeg` (Moov login, same viewport). Kept small (JPEG, ~50 KB each) as reference material.

---

## 1. Bulwark, exhaustively

**What it is.** Next.js 16 + React 19 + TypeScript + Tailwind v4 + zustand 5, AGPL-3.0, built for **Stalwart**. ~12,300 lines in `app/components/contexts/hooks/lib/stores` plus a 320 KB JMAP client. **25 locales × 2,951 leaf strings each — 100 % key parity, verified by leaf-key diff (README and FEATURES.md both say 24; `mn` Mongolian ships unlisted).** For scale: Moov's PWA has **2 locales, 228 keys**.

**The single most important architectural fact:** Bulwark is a **stateless proxy over a capable server**. It has no sync engine, no local index, no delta sync, and no offline store. Its speed is borrowed from Stalwart. This is exactly the contraposition ADR-001 §2 relied on — and it means **several of its weaknesses are ours to beat, not ours to copy.**

### 1.1 The settings IA — 26 tabs in 6 groups

Registry: `components/settings/settings-app.tsx` (1,193 lines). Group order: `general` → `appearance` → `mail` → `privacy` → `apps` → `advanced`. Default tab: `appearance`.

| # | Tab id | Group | Gate | Backend dependency |
|---|---|---|---|---|
| 1 | `account` | general | always | `Quota/get` for the storage bar |
| 2 | `language` | general | always | none (client) |
| 3 | `notifications` | general | always | `PushSubscription/set` + external relay |
| 4 | `protocol_handlers` | general | always | none (`navigator.registerProtocolHandler`) |
| 5 | `appearance` | appearance | always **(default)** | none |
| 6 | `layout` | appearance | always | none |
| 7 | `themes` | appearance | `themesEnabled` | Bulwark admin API |
| 8 | `reading` | mail | always | none |
| 9 | `composing` | mail | always | none |
| 10 | `downloads` | mail | always | none |
| 11 | `identities` | mail | always | **`Identity/get` + `Identity/set`** |
| 12 | `vacation` | mail | **`urn:ietf:params:jmap:vacationresponse`** | **`VacationResponse/get` + `/set`** |
| 13 | `filters` | mail | **`urn:ietf:params:jmap:sieve`** | **`SieveScript/get` `/set` `/validate`** + blob upload |
| 14 | `templates` | mail | `templatesEnabled` | none (localStorage + settings sync) |
| 15 | `folders` | mail | always | **`Mailbox/set`** |
| 16 | `keywords` (Tags) | mail | `customKeywordsEnabled` | `Email/query`+`Email/set`; optional vendor `Keyword/get` |
| 17 | `security` | privacy | **`stalwartFeaturesEnabled`** | ⚠️ **Stalwart-proprietary `x:*`** |
| 18 | `content_senders` | privacy | always | optional `AddressBook`/`ContactCard` |
| 19 | `calendar` | apps | `urn:ietf:params:jmap:calendars` | `Calendar/*`, `CalendarEvent/*` |
| 20 | `contacts` | apps | `contactsEnabled` | `AddressBook/*`, `ContactCard/*` |
| 21 | `files` | apps | `urn:ietf:params:jmap:filenode` | `FileNode/*` |
| 22 | `sidebar_apps` | apps | `sidebarAppsEnabled` | none |
| 23 | `plugin:<id>` | apps | dynamic, one per plugin | plugin sandbox |
| 24 | `about_data` | advanced | always | Bulwark `/api/settings` |
| 25 | `plugins` | advanced | `pluginsEnabled` (**default false**) | Bulwark admin API |
| 26 | `debug` | advanced | `debugModeEnabled` | none |

**Capability→tab unlock map** (this is the actionable part for us — advertising a URI *turns a whole tab on*):

| Capability URI | Unlocks | RFC |
|---|---|---|
| `urn:ietf:params:jmap:vacationresponse` | Vacation tab | RFC 8621 §8 |
| `urn:ietf:params:jmap:sieve` | Filters tab | RFC 9661 |
| `urn:ietf:params:jmap:quota` | storage bar in Account | RFC 9425 |
| `urn:ietf:params:jmap:calendars` | Calendar tab | draft |
| `urn:ietf:params:jmap:contacts` | Contacts | draft |
| `urn:ietf:params:jmap:filenode` | Files tab | draft (unadopted) |
| `urn:ietf:params:jmap:principals` | sharing dialogs | RFC 9670 |
| `urn:ietf:params:jmap:emailpush` | spam-filtered push | draft-ietf-jmap-emailpush |
| ⚠️ `urn:stalwart:jmap` | entire Security tab | **vendor** |
| ⚠️ `https://bulwarkmail.com/ns/jmap/keywords` | fast tag counts | **Bulwark vendor** |

Moov advertises **exactly three**: `core`, `mail`, `submission`. That is *why* a Bulwark pointed at Moov would show a reduced settings panel — but note it would still show **~20 tabs**, because most are client-local.

**An admin policy layer sits on top:** 22 `FeatureGates` flags from `/api/admin/policy`, with three independent mechanisms — `isFeatureEnabled` (hide tab), `isSettingLocked` (render at 60 % opacity with a lock icon, `pointer-events-none`), `isSettingHidden` (omit row). The server **re-validates on write** (`/api/settings` POST strips locked keys, enforces `allowedValues`/`min`/`max`) — client-side locking is not the only defense.

**Settings has its own full-text search** — genuinely differentiating and *entirely client-side*: per-tab haystack from i18n subtree paths plus hardcoded English synonym keywords (so a Spanish user searching "password" still finds the security tab); renders **sub-results** (individual settings) indented under each tab; clicking one switches tab, finds `[data-search-label="…"]`, `scrollIntoView({behavior:'smooth',block:'center'})` and applies a 1,800 ms highlight animation — **retrying every 80 ms for up to 2 s** because some tabs only render after a fetch.

### 1.2 Settings persistence — the architecture (and why we must NOT copy it verbatim)

**Settings are not in JMAP.** Two tiers:

1. **localStorage**, zustand `persist`, key `settings-storage`, **version 7** with a real migration chain.
2. **Optional sync to Bulwark's own Next.js server** — `POST /api/settings` → `lib/settings-sync.ts` writes `SETTINGS_DATA_DIR/<sha256(username:serverUrl)>.enc`, **AES-256-GCM**, key = `sha256(SESSION_SECRET)`, layout `[12B IV][16B tag][ciphertext]`, atomic `.tmp`+`rename`, path-traversal guarded. Debounce 2,000 ms; identity verified by re-reading httpOnly session cookies and matching username+server; 403/404 permanently disables sync for the session.

**~95 keys** are in the `exportSettings()` allowlist (which doubles as the file-export format). `proInterface` is explicitly device-local. Theme and locale ride along from separate stores and **ignore the user's sync opt-out**.

**The subtle bit worth stealing (bug #507):** `preferredIdentityIds` and `allMailFolderIds` are `Record<accountId, …>` maps that exist in *every* account's blob. On a **server** load only that account's own key is merged; otherwise the last account to log in clobbers the map and the composer picks the wrong sender. File imports replace wholesale.

**Migration chain** (`migrateSettings`, →v7) handles renames (`listDensity`→`density`), repurposed enums (`dateFormat`), a global→per-account restructure, and value sanitization. `onRehydrateStorage` defensively coerces non-record maps to `{}` and **re-sanitizes `messageListOrder`** — because an unknown sort criterion triggers `unsupportedSort` and **empties the folder**. That is exactly the class of bug worth pre-empting.

> **Judgment for Moov:** the encrypted-file-per-user scheme is a *workaround for not having a server*. We have PostgreSQL. A `settings` JSONB keyed by account is strictly better — queryable, backed up, transactional, no `SESSION_SECRET`-derived key, no 403-disables-sync failure mode. **Copy the IA and the per-account merge semantics; reject the storage mechanism.**

### 1.3 Full settings inventory

Legend — **LS** = `localStorage['settings-storage']`; **LS+sync** = also in the export allowlist → encrypted server file. Rows marked LS/LS+sync have **no backend dependency at all**.

#### Account (`account`) — read-mostly
| Setting | Type | Persisted | Backend |
|---|---|---|---|
| Name / Email / Username / Auth method / Server | read-only | — | JMAP session, `/api/auth/session` |
| **Storage used** | progress bar + % | — | **`Quota/get`** |
| Accounts list | list, drag-reorder, switch, set default, add | `auth-storage` | multi-slot cookies |
| Shared accounts | list | — | `urn:ietf:params:jmap:principals` |

#### Language & Region (`language`)
| Setting | Values (default) | Persisted |
|---|---|---|
| Language | 25 locales | `locale-storage` + sync |
| `dateFormat` | `smart`(d) / `relative` / `full` | LS+sync |
| `dateLocale` | `auto`(d) / `iso` / `en-GB` / `en-US` | LS+sync |
| `timeFormat` | `12h` / **`24h`**(d) | LS+sync |
| `timeZone` | `auto`(d) or IANA id | LS+sync |
| `firstDayOfWeek` | Sun / **Mon**(d) / Sat | LS+sync |

#### Notifications (`notifications`)
| Setting | Type | Persisted | Backend |
|---|---|---|---|
| Enable Web Push | toggle + status | subscription id in LS | **`PushSubscription/set`** + relay VAPID |
| Re-register | button (`forceRecreate`) | — | relay + JMAP (#841) |
| `pushRelayUrl` | select from admin list (never free text) | LS+sync | — |
| Device list | list + revoke, active/inactive/unknown, "this device" | — | relay endpoints |
| `emailNotificationsEnabled` / `emailNotificationSound` | toggles (both default true) | LS+sync | — |
| `notificationSoundChoice` | select + ▶ preview | LS+sync | — |
| `calendarNotificationsEnabled` / `calendarNotificationSound` | toggles | LS+sync | — |
| `calendarInvitationParsingEnabled` | toggle (true) | LS+sync | — |

#### Protocol handlers (`protocol_handlers`)
`mailto` registration button; `webcal` registration (calendar only); `protocolOpenMode` = `active-session` / **`new-tab`**(d). All client-side.

#### Appearance (`appearance`)
| Setting | Values (default) | Persisted |
|---|---|---|
| `theme` | light / dark / system | `theme-storage` + sync |
| `fontSize` 🔒 | small 14 / **medium 16**(d) / large 18 → CSS var | LS+sync |
| `density` 🔒 | extra-compact / compact / **regular**(d) / comfortable → 6 CSS vars, **live preview** | LS+sync |
| `animationsEnabled` 🔒 | true(d) → `--transition-duration: 0s` when off | LS+sync |
| `senderFavicons` | true(d) | LS+sync |
| `showAvatarsInJunk` | false(d) | LS+sync |
| Restart tour / `showOnboardingOnNewDevices` | button / toggle | LS+sync |
| **`messageListOrder`** 🔒 | preset + up to 3 sortable levels → **`Email/query` `sort`** | LS+sync |
| `messageListOrderScope` 🔒 | `inbox`(d) / `all` | LS+sync |

#### Layout (`layout`) — 17 settings, all client-side
`mailLayout` (split(d)/focus/horizontal) · `toolbarPosition` · `showToolbarLabels` · `hideAccountSwitcher` · `showRailAccountList` · `colorfulSidebarIcons` · `tintListRowsByTag` · `showFolderTotalCount` · `faviconUnreadBadge` · `enableUnifiedMailbox` · `unifiedCrossAccount` · `includeGroupInUnified` · `enableCrossUnreadView` · `enableCrossStarredView` · `enableCrossAllView` · `allMailFolderIds` (per-account folder multi-select) · `proInterface` (device-local).

#### Reading (`reading`) — 18 settings, all client-side
`markAsReadDelay` (0 instant(d)/3 s/5 s/never) · `messageSpacing` · `plainTextFont` (mono/**sans**(d)) · `deleteAction` (trash(d)/trash-and-read/permanent) · `archiveMode` (single(d)/year/month) · `permanentlyDeleteJunk` · `returnToListAfterAction` · `swipeRightAction` / `swipeLeftAction` (mobile) · `showPreview` · **`disableThreading`** · `hideInlineImageAttachments` · `attachmentImagePreviewsEnabled` · `hoverActions` (multi-select chips) · `hoverActionsMode` · `hoverActionsCorner` · `mailAttachmentAction` · `attachmentPosition` · **`emailsPerPage`** (10/25/**50**(d)/100).

#### Composing (`composing`)
| Setting | Values (default) | Persisted |
|---|---|---|
| `autoSelectReplyIdentity` | false(d) | LS+sync |
| `plainTextMode` | false(d) | LS+sync |
| `rtlEditingSupport` | per-paragraph LTR/RTL, false(d) | LS+sync |
| **`sendDelaySeconds`** (undo send) | 0 off(d) / 10 / 30 / 60 | LS+sync |
| `signaturePosition` | above_quote / **below_quote**(d) | LS+sync |
| `signatureSeparatorEnabled` | RFC 3676 `"-- "`, true(d) | LS+sync |
| `requestReadReceiptDefault` | false(d) | LS+sync |
| `readReceiptResponse` | **ask**(d) / always / never | LS+sync |
| `subAddressDelimiter` | `+` etc., custom validated | LS+sync |
| `emptySubjectWarningEnabled` | true(d) | LS+sync |
| `attachmentReminderEnabled` | true(d) | LS+sync |
| `attachmentReminderKeywords` | chips, **~30 defaults across 13 languages** | LS+sync |

#### Downloads (`downloads`)
Three filename templates (email / attachment / bundle) with token chips and **live preview**; tokens `{date} {date_short} {time} {year} {month} {day} {from} {from_email} {from_name} {to} {to_email} {to_name} {subject} {filename} {count}`. Plus `filenameSpaceReplacement`, `filenameLowercase`, `filenameStripDiacritics`, `filenameCollapseSeparators`, `postExportAction` (keep(d)/archive/trash). All client-side.

#### Identities (`identities`) — **standard JMAP**
`Identity/get`; `Identity/set` create (`name,email,replyTo,bcc,textSignature,htmlSignature`), update (same minus `email` — not updatable), destroy. **Signature is dual: `textSignature` (plain) + `htmlSignature` (HTML, edited in the rich-text editor).** Placement is a *global* setting, not per-identity. Default-identity selection is a three-layer fallback (settings map → identity-store local → re-applied on every login/switch).

#### Vacation (`vacation`) — **RFC 8621 §8, not Sieve**
`VacationResponse/get` / `/set` on singleton id `"singleton"`, `using: [core, mail, vacationresponse]`. Fields `isEnabled`, `fromDate`, `toDate` (both `datetime-local`), `subject`, `textBody` (`htmlBody` in model, not exposed). Validation warnings for end-before-start, start-in-past, empty body. Renders a "not supported" state when the capability is absent. **`filter-store` explicitly skips a script named `vacation`** — RFC 9661 §4 makes it server-managed and modifiable only via `VacationResponse/set`.

#### Filters (`filters`) — **RFC 9661 SieveScript, and the best idea in the codebase**
Wire surface: `SieveScript/get` · read content by **HTTP GET on the blob download URL** · upload content by **POST to `session.uploadUrl`** with `Content-Type: application/sieve` → blobId · `SieveScript/set {create:{…blobId}, onSuccessActivateScript:"#new-script"}` · update · destroy · activate/deactivate via `onSuccessActivateScript: id|null` · `SieveScript/validate`. **Every content write is two steps: upload blob, then `/set` referencing it. No inline script text anywhere.**

**Round-trip strategy — a JSON metadata comment header.** The generator emits rules twice: once machine-readable, once as real Sieve.

```
/* @metadata:begin
{"version":1,"rules":[…],"vacation":{…}}
@metadata:end */
require ["fileinto","imap4flags"];      ← computed from used features, sorted, deduped
# Rule: <name>
if allof(header :contains "From" "x") { fileinto "Folder"; stop; }
# --- External rules (managed outside Bulwark) ---
<verbatim rawBlock>
```

`parseScript()` prefers the metadata block, else falls back to a real ~800-line hand-written Sieve tokenizer. **Three rule origins:** `bulwark` (editable), `external` (parsed, read-only — even detects and labels **Nextcloud**-generated blocks), `opaque` (preserved verbatim). Unparseable script → raw-text editor, saved untouched. `externalRequires` preserved and merged.

> **This is directly aligned with our "Dovecot is the source of truth / never destroy what you didn't write" posture. Steal it.**

Rule model — fields `from|to|cc|subject|header|size|body|attachment`; comparators `contains|not_contains|is|not_is|starts_with|ends_with|matches|greater_than|less_than|has_any|has_type`; actions `move`(fileinto) · `copy`(fileinto :copy) · `forward`(redirect) · `mark_read`(addflag `\\Seen`) · `star`(addflag `\\Flagged`) · **`add_label`(addflag `$label:<id>`** — same convention as our A6 keywords) · `discard` · `reject` · `keep` · `stop`. Values may be arrays → Sieve list literal = OR-within-condition. Attachment matching uses RFC 5703 `:mime :anychild` and checks the filename in **both** `Content-Disposition` and `Content-Type` (older senders only set the latter).

#### Folders (`folders`)
Drag-reorder + drag-to-nest → `Mailbox/set` (`sortOrder`, `parentId`); create/subfolder; rename; delete with distinct errors for has-children / has-email; **standard-role assignment** per mailbox; `folderIcons` per mailbox (client-side, gated).

#### Tags / Keywords (`keywords`)
`nestedTags` (treat `/` as hierarchy) · drag-reorder · add/edit dialog with **39-color palette** (3 shades × 13 hues) · per-tag sidebar visibility (`show`(d)/`unread`/`hide`) · **rename tag → `migrateKeyword()`** which walks `Email/query`+`Email/set` across every matching message (bounded to 3 `queryState`-drift restarts) · **discover unrecognized keywords** via vendor `Keyword/get` if available, else a paged `Email/query` scan. Tag id is the JMAP keyword suffix `$label:<id>`. 7 default tags.

> **Constraint we have and Bulwark does not:** our Maildir keyword ceiling is **26 durable keywords per mailbox** (`internal/imap/metadata.go: MaxDurableKeywordsPerMailbox = 26`; keywords past the 26th live only in Dovecot's in-memory index). A 39-colour palette assumes a freedom we do not have. Any label design must be built around that number.

#### ⚠️ Security (`security`) — **Stalwart-proprietary, must be rebuilt for Mailcow**
Transport is a **Bulwark server-side passthrough** (`POST /api/account/stalwart/jmap`) that injects basic auth from an httpOnly cookie, `using: [core, urn:stalwart:jmap]` — deliberately never from the browser. Sub-blocks: change password (`x:AccountPassword/set`) · display name (`x:Account`) · **TOTP 2FA** (QR + verify, requires current password) · **app passwords** (`x:AppPassword/*` with `expiresAt`, `allowedIps`, one-time secret reveal) · API keys (`x:ApiKey/*`) · at-rest encryption (`x:PublicKey/*` + `x:AccountSettings/*`) · email-client setup info · **link device** (QR pairing, countdown expiry).

> Moov already provisions app passwords through the Mailcow API, so 1 of these 7 is effectively done. The rest map to Mailcow's API or have no Dovecot analogue.

#### Content & senders (`content_senders`)
`externalContentPolicy` (**ask**(d)/block/allow) · `emailAlwaysLightMode` · `trustedSenders` list · `trustedSendersAddressBook` — a **tri-state `boolean|null`** that resolves to `true` on first connect if contacts are supported, then never auto-changes, storing trusted senders in a real JMAP address book so they sync.

#### Calendar / Contacts / Files / Sidebar apps / About & data / Themes / Plugins / Debug
Calendar: default view, `showTimeInMonthView`, `showWeekNumbers`, hover-preview delay, birthday calendar + colour, tasks toggles, `sharedCalendarColors` (per-viewer override, #345); management = CRUD + CalDAV URL + **sharing via `Principal/query`+`Principal/get`** + iCal subscriptions. Contacts: `groupContactsByLetter`, import/export vCard, address-book CRUD + sharing. **Files: all 8 settings live in a *separate* raw-JSON `localStorage['files-settings']`, not synced, not exported — this isolation looks accidental.** Sidebar apps: user-defined iframe/tab apps with lucide icon picker. About & data: sync opt-out, export/import settings JSON, refresh cache, reset to defaults, and a hidden **Spam Siege** mini-game. Themes: admin-deployed + built-in, CSS in **IndexedDB** when large. Plugins: schema-driven config UI + approval workflow. Debug: 8 per-category log toggles (**latent bug: `contacts` is in `ALL_DEBUG_CATEGORIES` but missing from the defaults object**).

### 1.4 The JMAP client layer — mechanism

**No BFF for mail.** The browser speaks JMAP directly. `proxy.ts` is Next middleware for CSP/i18n only; the single server-side passthrough exists solely for Stalwart's `x:` management namespace.

**Complete method inventory** (`✳` non-mail, `⚠` vendor):

| Group | Methods |
|---|---|
| Core | `Core/echo` (30 s keep-alive) |
| Mail | `Mailbox/get` `Mailbox/set` · `Email/query` `Email/get` `Email/set` `Email/import` `Email/copy` · `Thread/get` · `Identity/get` `Identity/set` |
| Submission | `EmailSubmission/set` `/get` **`/query`** |
| Push | `PushSubscription/get` `/set` |
| Other std | `Quota/get` · `VacationResponse/get` `/set` · ✳`SieveScript/get` `/set` `/validate` · ✳`Principal/query` `Principal/get` |
| ✳ Calendar/Contacts/Files | `Calendar/get` `/set` · `CalendarEvent/get` `/set` `/query` `/parse` · `AddressBook/get` `/set` · `ContactCard/get` `/set` `/query` · `FileNode/get` `/set` `/query` |
| ⚠ Vendor | `Keyword/get` (Bulwark's own) · `x:Account*`, `x:AppPassword/*`, `x:ApiKey/*`, `x:PublicKey/*`, `x:OAuthClient/*` (Stalwart, via BFF) |
| **Never called** | **`SearchSnippet/get`** (no search highlighting) · **`Email/changes`, `Mailbox/changes`, any `/queryChanges`** (grep count: 0) · any RFC 9404 `Blob/*` |

**Batching and back-references** — three distinct mechanisms:

1. **`#ids` ResultReference** — the universal list idiom. *Opening a mailbox sends exactly one HTTP request:*
   ```js
   ["Email/query", {accountId, filter, sort, limit, position, calculateTotal:true}, "0"],
   ["Email/get",   {accountId, "#ids":{resultOf:"0",name:"Email/query",path:"/ids"},
                    properties: EMAIL_LIST_PROPERTIES}, "1"]
   ```
   `EMAIL_LIST_PROPERTIES` = `id, threadId, mailboxIds, keywords, size, receivedAt, from, to, cc, subject, preview, hasAttachment, blobId` (`blobId` is there so list rows can be dragged out as `.eml`).
2. **Creation ids `#cid`** — `batchArchiveEmails` creates year *and* month folders and files mail into them in **one** request, with a **nested** creation-ref (`parentId:'#year-2026'`). Creation ids are request-scoped (RFC 8620 §3.3), so later batches substitute harvested real ids.
3. **`onSuccess*` server-side chaining** — `onSuccessUpdateEmail`, `onSuccessActivateScript`, `onSuccessDestroyOriginal`.

**N-call fan-out is budgeted:** `itemsPerRequest(maxCallsInRequest, callsPerItem)` + `batched()` at **17 call sites**, because *"Stalwart defaults to 16 method calls… **nine tags is already 18 calls**"* and the ceiling fails the **entire** request.

**Send — the crown jewel:**
```js
["Email/set", {accountId, create:{[emailId]: emailCreate}}, "0"],
["EmailSubmission/set", {accountId, create:{"1":{emailId:`#${emailId}`, identityId, envelope?}},
   onSuccessUpdateEmail:{"#1":{mailboxIds:{[sentId]:true}, "keywords/$draft":null}}}, "1"]
```
The message is created **in Drafts** and the **server** moves it to Sent *after* successful submission — *"avoiding issues with servers that encrypt on append"* (#188). Also: `messageId` is generated client-side because otherwise the server synthesizes one from its OS hostname, leaking internal names (`@ip-10-0-12-97.ec2.internal`) — an anti-spam signal and an information disclosure. And `cc: cc?.length ? … : undefined`, because sending `cc:[]` emits a literal empty `Cc:` header, which is malformed and a spam signal.

**Deliberate anti-batching:** draft save/send **splits the destroy into a second request** (#849) — a combined `Email/set {create, destroy}` processes both independently, so when the create fails with `blobNotFound` the destroy still deletes the last good copy of the draft.

**Resilience, layered:**
- 30 s header deadline (300 s for blobs), timer cleared once headers arrive so SSE streams freely — motivated by iOS reusing pooled connections the network already tore down.
- Network error → **one** retry after 1 s. **`RequestTimeoutError` is NEVER retried** — *"replaying an `EmailSubmission/set` would send the mail twice."* Correct idempotency call.
- **429** → parse `Retry-After` (numeric or HTTP-date, cap 5 min) → set `rateLimitedUntil`, tear down push, short-circuit subsequent calls **before** the network.
- **401** → bearer refreshes token and retries once; basic re-runs `refreshSession()`, and if that fails and TOTP is configured, prompts for a fresh code. Excluded for `/.well-known/jmap` to avoid recursion.
- **`maxConcurrentRequests` replay** with delays `[200,400,800]` + jitter, detected precisely (HTTP 400 **and** body `{type:'urn:ietf:params:jmap:error:limit', limit:'maxConcurrentRequests'}`). Safe to replay *even a `/set`* because *"the refusal comes before any method runs."*
- **No placeholder fallbacks — a hard rule.** `getMailboxes` throws rather than returning a synthetic Inbox, because a refused request once replaced the real folder tree with a lone fake "Inbox" until reload (#780).

**Three small modules encoding hard-won lessons:**

| Module | Lines | Problem it solves |
|---|---|---|
| `request-limits.ts` | 26 | RFC 8620 ceilings fail the *whole* request; 9 tags = 18 calls > Stalwart's 16 |
| `patch-pointer.ts` | 29 | PatchObject keys are **JSON Pointers**: a nested tag `$label:work/clients` makes `keywords/$label:work/clients` address the `clients` member of `$label:work` — **the tag silently never lands.** RFC 6901 escaping (`~`→`~0`, `/`→`~1`, decoded in that exact order) |
| `first-touch-gate.ts` | 101 | Stalwart lazily creates a default calendar guarded only by a per-node cache; Bulwark's login fans out `Calendar/get` + `CalendarEvent/query` within milliseconds, so clustered deployments create **two undeletable "Personal" calendars** (#907) |

**And a related trap:** `mailboxIdsReplacement()` never patches `mailboxIds` by pointer, because Stalwart rejects a pointer token that is purely numeric (it reads the digits as a JSON-Pointer array index), which **silently stranded delivered mail in Drafts** for accounts whose mailbox id happened to be all digits. *Moov is immune by construction* — our ids are prefixed (`m`/`e`/`t`/`a` + base36, `internal/jmap/mail/id.go`) precisely so they never start with a digit, per RFC 8620 §1.2.

**Capability adaptation** — the most sophisticated piece is the **`hasKeyword` sort polarity probe** (#718): servers disagree on how `isAscending` reads for a `hasKeyword` comparator, so Bulwark fires 3 calls in one request (asc, desc, and a back-referenced `Email/get`) and infers polarity from which top id wins; inconclusive results are cached with a 5-minute retry. Three-layer degradation: check advertised `emailQuerySortOptions` → honour a cached `unsupportedSort` refusal → **retry the whole query without keyword comparators** rather than showing an empty folder.

> Note: this independently corroborates our own J4 finding that real clients open every folder with `hasKeyword`+`receivedAt`. Bulwark goes further and *probes* for semantics.

### 1.5 State, caching, offline, push — where Bulwark is genuinely weak

**State:** zustand v5, plain `create()`, **no immer**. `email-store.ts` is 4,690 lines with ~90 actions. The store is **not normalized**: `emails: Email[]` is literally *the list you are looking at*, every lookup is a linear `find()`, and there is **no entity map and no query-result cache**. Selecting a different mailbox **destroys** the list — Inbox → Archive → Inbox is two full round-trips. Opening a message you read ten seconds ago **refetches 256 KB of bodies**.

**Offline: there is none, by design.** `public/sw.js` line 36 is the entire fetch handler:
```js
self.addEventListener("fetch", () => {});
```
with the comment *"network-only fetch handler, no caching — so we never serve stale chunks after a deployment."* No workbox, no next-pwa, no runtime caching. IndexedDB exists only for **plugin/theme bundles** — zero mail data. The only mail persistence is a **50-email localStorage boot snapshot** (debounced 1 s, restored at module load only if the last session ended authenticated, refusing search/keyword/unified/secondary-account views after #847) whose stated purpose is *"so a returning visit paints rows the moment auth resolves"* — **a paint-latency trick, not offline support.** Offline events are a plugin hook and nothing more: no queue, no outbox, no retry, no offline UI.

**Optimistic updates: they are not optimistic.** Every mutation is uniformly **await-then-patch**:
```js
await actionClient.toggleStar(emailId, !isFlagged, accountId);   // network FIRST
set((state) => ({ emails: state.emails.map(...) }));             // patch AFTER
```
Verified identical for `markAsRead`, `deleteEmail`, `moveToMailbox`, `markAsSpam`, `setEmailKeywords` and all four batch variants. There is **no rollback code because there is nothing to roll back.** What *is* clever: mailbox **counters** are patched locally in the same `set()` while the row change waits.

**Push:** not `EventSource` — a manual `fetch` + `ReadableStream` reader, because `EventSource` cannot send an `Authorization` header. Subscribes to `{types}=*`. **On any `StateChange` it refetches everything** — `Email/changes` is never called. Survivability comes from a careful merge whose cutoff derives from **page size, not list length** (`appendFromIndex = max(emailsPerPage - insertedCount, 0)`), because a length-based cutoff re-appended deleted rows from stale state and produced ghost drafts that **re-sent mail when clicked** (#592).

**The standout piece of engineering — the per-tab SSE socket budget (#702):** `MAX_SSE_STREAMS = 2`, static `sseHolders`/`sseWaiters`, FIFO promotion on a **microtask** (not inline — an account switch rebuilds every client, and synchronous promotion would open a stream about to be closed). *"Browsers allow only 6 HTTP/1.1 connections per host, so from the sixth login on there is no socket left for an ordinary JMAP POST: sends grey out and never complete."* Plus `recycleStaleSSE()` on `visibilitychange`, because an iOS home-screen PWA **freezes its timers** and the watchdog is itself asleep. Reconnect is a flat 3 s — no exponential backoff.

**`coalesceRefresh`** — a WeakMap keyed per client × per operation; late callers share the in-flight promise and at most **one** follow-up is queued. It exists because a single push event fans out into several refreshes, times open tabs, and Stalwart's `maxConcurrentRequests` refusal **blanked the sidebar folder tree** (#780).

**Undo send is real and server-side** — `EmailSubmission` with a future `sendAt`; the toast offers **"Undo send"** (cancel → restore to Drafts → reopen composer) and **"Send now"** (reschedule 1 s out). `undoStatus` is genuinely consulted. **There is no undo for move/delete/archive** (only a spam-undo cache that remembers the exact original folder).

**Threading is hybrid and honestly limited:** server-assigned `threadId`, **client-side grouping of the current page only**, with `Thread/get` supplying counts. Two messages of one thread on pages 1 and 3 render as **two separate rows** until page 3 loads. `Thread/get` corrects the count, not the grouping.

**Multi-account is core (not pro-gated) and architecturally expensive:** a module-level `Map<string, JMAPClient>` outside the store; **eight** resolver helpers exist solely to route each mutation; mailbox ids are namespaced `${accountId}:${mailboxId}`; cross-account move is fetch-blob → import → delete because `onSuccessDestroyOriginal` is broken upstream (stalw.art #1150). The silent-failure class is nasty: Stalwart answers a misrouted `Email/set` with `updated: null` and **no error**, so the change is simply lost on reload (#281, #847, #874).

### 1.6 Main UI, keyboard, composer, security, i18n

**Keyboard map** (`hooks/use-keyboard-shortcuts.ts`, 310 lines):

| Key | Action | Key | Action |
|---|---|---|---|
| `j` / `↓` | next | `c` | compose |
| `k` / `↑` | previous | `/` | focus search |
| `Enter` / `o` | open | `?` | help |
| `Esc` | close + deselect | `Shift+G` | refresh |
| `r` / `Shift+R` | reply / reply-all | `x` | toggle thread expansion |
| `a` | reply-all | `Ctrl/Cmd+A` | select all |
| `f` | forward | `u` | mark unread |
| `s` | toggle star | `Shift+I` | mark read |
| `e` | archive | `!` | toggle spam |
| `#` / `Del` / `Backspace` | delete | | |

**A detail worth stealing:** shortcuts resolve from the **physical key** (`event.code`, `KeyA`..`KeyZ`, plus `Slash`/`Digit1`/`Digit3` for `/ ? ! #`) *"so they stay reachable on non-Latin layouts"* (Cyrillic, Greek). The typing guard is event-based (`composedPath`), not `document.activeElement`, because a shadow-root editor retargets `activeElement` to its host (#654).

**Composer:** TipTap rich text with inline images, drag-and-drop embedding and **tables**; plain-text mode; drafts autosave preserving identity/HTML/`In-Reply-To`/`References`; attachments upload/download/drag-out/inline preview, `.eml` parts rendered as nested email, **TNEF (`winmail.dat`) extraction**; attachment-mention reminder; empty-subject confirmation; **scheduled send**; read receipts (MDN, RFC 8098); quoted text in an editable island; per-identity signature above/below quote; From override and alias auto-fill; templates with placeholders; contact autocomplete on To/Cc/Bcc from JMAP contacts.

**Security rendering:** DOMPurify sanitization; external content blocked until allowed with remembered trusted senders; CSP with a **per-request nonce**; SSRF redirect validation; sandboxed PDF iframe; `data:` allowlist applied to media tags **and `srcset` candidates**; S/MIME sign/encrypt/decrypt/verify; SPF/DKIM/DMARC indicators that surface the most severe SPF result and drop the "via" badge on spoofed mail. **Notably: no image proxy** — remote images rely on CSP plus iframe sandbox. Only favicons go through a server route.

**i18n/a11y:** 25 locales × 2,951 leaf strings, **100 % parity**, ICU MessageFormat with correct Slavic `one/few/other` plurals, EN deep-merged as fallback so an untranslated key shows English rather than a raw key path. **RTL for `ar`/`he`/`fa`** is unusually thorough — measured across the codebase: **~440 logical-property usages (`me-` 123, `ms-` 89, `text-start` 137, `border-s/e` 74…) against just 4 physical leaks**, plus a TipTap `TextDirection` extension defaulting to `dir="auto"` per block so each paragraph detects direction as you type, and the attribute round-trips into the sent mail. a11y counts: `aria-label` 247, `role=` 187, `aria-modal` 31, `inert` 8 (the off-screen mobile sidebar stayed in the a11y tree without it, #721), live regions 10; global `prefers-reduced-motion` zeroing.

**Three corrections to Bulwark's own claims**, found by reading the source: (1) **25 locales ship, not 24** — `mn` is unlisted; (2) **the search UI never emits `operator:"OR"`** — the filter builder only ANDs, so FEATURES.md's "OR conditions" describes JMAP's capability, not the UI's; (3) **there is no image proxy in the mail path** — FEATURES.md's HMAC/anti-SSRF proxy applies to *server-side* fetches (iCal, favicons); remote mail images are either blocked or loaded **directly**, never proxied. *We proxy; they do not.*

**e2e:** the root `e2e/` suite is nearly vestigial — 8 tests, **6 skipped**, only 2 running and both on the login page: **there is effectively no UI-level e2e coverage of the mail experience.** The real coverage is `integration/` — **14 specs / ~50 tests against a containerized Stalwart**, dominated by counter accuracy under every mutation path, multi-account and shared-account isolation, draft fidelity, and cross-account blobs. One `describe` is pinned `test.fail(true, 'blocked by Stalwart #1150')` — an upstream server bug encoded as an expected failure. **Untested anywhere:** keyboard shortcuts, context menus, swipe/drag, the search UI, TipTap behaviour, sanitization/external-content blocking, RTL, a11y.

---

## 2. Gmail's surface, from authoritative sources

### 2.0 A methodological finding that changes how to use this section

**Google does not publish a settings specification.** `support.google.com/mail/answer/6562` ("Change your Gmail settings") is a hub that names three tabs and links out to ~22 per-feature articles. Documentary coverage of the real UI is roughly **60–65 %**. Specifically:

- **The Advanced tab is almost entirely undocumented** — only Templates and custom keyboard shortcuts have real articles.
- **Filter criteria and actions are enumerated nowhere in Help prose.** The only exhaustive Google-authored enumeration is the **Gmail API reference**, which models the same settings as typed enums with legal value domains.
- Several strings in common circulation (delegation read/unread radios, "Reply from the same address…", the offline 7/30/90 presets, spam 30-day auto-delete) **could not be sourced to Google at all** and are flagged individually below.

> **Implication for us:** when we model a settings schema, the **Gmail API reference** — not the Help centre — is the correct source. It supplies real value domains (`maxFolderSize ∈ {0,1000,2000,5000,10000}`, four POP dispositions, three expunge behaviours, nine filter criteria) that Help omits. It also reveals Gmail's actual data model: **filter actions are label mutations (`addLabelIds`/`removeLabelIds`/`forward`), not discrete operations.** That composes far better than a flat action list and is worth mirroring.

Everything below is cited to a Google-authored page. Anything that could not be sourced is marked **unsourced** rather than presented as verified.

### 2.1 Settings taxonomy

#### General — [/mail/answer/6562](https://support.google.com/mail/answer/6562)

| Setting | Options (exact where sourced) | What it does |
|---|---|---|
| Language | dropdown; "Enable input tools"; "Right-to-left editing support on" | UI language, input tools, RTL ([/17091](https://support.google.com/mail/answer/17091)) |
| Phone numbers (country code) | dropdown | **unsourced** |
| Default text style | font / size / colour + "Remove formatting" | Default formatting ([/8260](https://support.google.com/mail/answer/8260)) |
| **Conversation View** | on / off | Groups responses; **splits on subject change or >100 emails** ([/5900](https://support.google.com/mail/answer/5900)) |
| **Images** | "Always display external images" / "Ask before displaying external images" | Ask mode shows a "Display images below" button; Google proxies images to prevent open-tracking ([/145919](https://support.google.com/mail/answer/145919)) |
| Dynamic email | "Enable dynamic email" | AMP for Email; **requires images always-display** ([/9266768](https://support.google.com/mail/answer/9266768)) |
| Grammar / Spelling / Autocorrect | on / off each | Inline correction ([/7987](https://support.google.com/mail/answer/7987)) |
| Smart Compose (+ personalization) | "Writing suggestions on" / off | Predictive completion; account-level ([/9116836](https://support.google.com/mail/answer/9116836)) |
| **Nudges** | "Suggest emails to reply to" · "Suggest emails to follow up on" | Surfaces forgotten threads ([/6585](https://support.google.com/mail/answer/6585)) |
| Smart Reply | on / off | Reply chips |
| Package tracking | "Turn on package tracking" | Requires smart features ([/13073650](https://support.google.com/mail/answer/13073650)) |
| **Desktop notifications** | "New mail" / "Important mail" / "Mail notifications off" + sound | **With categories on, new-mail notifications fire only for Primary.** Requires Gmail open in a tab ([/1075549](https://support.google.com/mail/answer/1075549)) |
| **Stars** | presets "1 star" / "4 stars" / "all stars"; drag between "Not in use" / "In use" | **12 star types**; click cycles ([/5904](https://support.google.com/mail/answer/5904)) |
| **Keyboard shortcuts** | on / off — **off by default** | ([/6594](https://support.google.com/mail/answer/6594)) |
| Button labels | "Icons" / "Text" | ([/2473038](https://support.google.com/mail/answer/2473038)) |
| Create contacts for auto-complete | — | **unsourced** |
| **Signature** | multiple named sigs; per-alias; defaults for new vs reply/forward | **10,000-char limit** sourced; the three control labels **unsourced** ([/8395](https://support.google.com/mail/answer/8395)) |
| Personal level indicators | "No indicators" / "Show indicators" | `›` to you+group, `››` to you only ([/14582217](https://support.google.com/mail/answer/14582217)) |
| Snippets | "Show snippets" / "No snippets" | Body preview in list |
| **Vacation responder** | on/off, date range, subject, message, contacts-only, org-only | See §2.2 |
| Maximum page size | 25 / 50 / 100 per page | **unsourced on General** (documented only inside Multiple Inboxes) |
| **Undo Send** | **5, 10, 20 or 30 seconds** | Fully sourced discrete set ([/2819488](https://support.google.com/mail/answer/2819488)) |
| Default reply behaviour | "Reply" / "Reply all" | |
| Hover actions | enable / disable | Archive, delete, snooze, mark-read on row hover |
| Send and Archive | "Show 'Send & Archive' button in reply" | ([/a/users/9282734](https://support.google.com/a/users/answer/9282734)) |
| Smart features | three independent master toggles | See §2.4 ([/10079371](https://support.google.com/mail/answer/10079371)) |

#### Labels — [/mail/answer/118708](https://support.google.com/mail/answer/118708)
Label list visibility show/hide ✅ · "show if unread" ❌ unsourced · show/hide in message list ❌ unsourced · **"Show in IMAP"** per label ✅ · "Nest label under" ✅ · label colour swatches + custom, **up to 100 custom colours** ✅ · **limit 5,000 labels** ✅ · **system-label show/hide ❌ unsourced — Google never enumerates system labels** (a real documentation gap).

#### Inbox — [/mail/answer/18522](https://support.google.com/mail/answer/18522)
Six inbox types, verbatim: **Default** (tabs) · **Important first** · **Unread first** · **Starred first** · **Priority Inbox** ("Important and unread", "Starred", "Everything else") · **Multiple Inboxes**.
Categories ([/3055016](https://support.google.com/mail/answer/3055016)): Primary, Social, Promotions, Updates, Forums; Reservations is search-only; **Purchases ❌ unsourced as a tab**. "You can't create your own categories." **⚠️ Users with over 250,000 emails cannot use the Default tabbed inbox.**
Also: Importance markers ✅ partial · Filtered mail override ❌ unsourced · **Reading pane** — "Enable reading pane" + **"No split" / "Right of inbox" / "Below inbox"** ✅ ([/9499937](https://support.google.com/mail/answer/9499937)) — **note Google calls it "Reading pane" and puts it on the Inbox tab, not "Preview Pane" on Advanced** · Multiple Inboxes config (section name, query, max page size, position) ✅.

#### Accounts and Import
**Send mail as** — verification flow, "Make default", "Specify a different reply-to address", **limit 99 addresses**; ⚠️ *"Beginning January 2027, Gmail will discontinue support for 'Send as' with non-Google addresses"* ([/22370](https://support.google.com/mail/answer/22370)).
**"Treat as an alias"** — checked: same inbox, reply auto-fills To: with the original To: addresses; unchecked: "send on behalf of", mail does not land in your inbox ([Workspace KB](https://knowledge.workspace.google.com/admin/users/should-i-uncheck-treat-as-an-alias-in-gmail)).
"When replying to a message" radios — ❌ **UNSOURCED anywhere**.
**Check mail from other accounts** — POP, **limit 5**, "Email folders and labels cannot be imported" ([/21289](https://support.google.com/mail/answer/21289)). Gmailify. Import mail and contacts.
**Grant access (delegation)** — **10 delegates personal / 1,000 Workspace**, same-org only; ⚠️ documented gap: *"Autocomplete isn't available to delegated users when using the Gmail search bar"* ([/138350](https://support.google.com/mail/answer/138350)). Delegate read/unread radios ❌ unsourced.
Storage: **15 GB shared across Gmail, Drive, Photos; counts Spam and Trash** ([/9312312](https://support.google.com/mail/answer/9312312)).

#### Filters and Blocked Addresses — [API reference](https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.settings.filters)
**Criteria:** `from`, `to` (includes cc/bcc), `subject`, `query`, `negatedQuery`, `hasAttachment`, `excludeChats`, `size`, `sizeComparison ∈ {smaller, larger}`. **Size is bytes only, and there is NO date criterion** — "Date within" is a search-UI feature that does not persist into a filter.
**Actions — only three fields:** `addLabelIds[]`, `removeLabelIds[]`, `forward`. Skip Inbox = remove `INBOX`; Mark read = remove `UNREAD`; Star = add `STARRED`; Delete = add `TRASH`; Never spam = remove `SPAM`.
Export/import as **`.xml`** ([/6579](https://support.google.com/mail/answer/6579)). Blocked addresses with bulk "Unblock selected addresses" ([/8151](https://support.google.com/mail/answer/8151)).

#### Forwarding and POP/IMAP
Forwarding with address verification; disable; selective forwarding via a filter's "Forward it" ([/10957](https://support.google.com/mail/answer/10957)).
**POP** ([/7104828](https://support.google.com/mail/answer/7104828)): "Enable POP for all mail" / "…for mail that arrives from now on" / disable; **four dispositions verbatim** — "Keep Gmail's copy in the Inbox", "Mark Gmail's copy as read", "Archive Gmail's copy", "Delete Gmail's copy"; `recent:` prefix covers the last 30 days.
**IMAP** ([/7126229](https://support.google.com/mail/answer/7126229)): ⚠️ *"Starting January 2025, the option to choose 'Enable IMAP' or 'Disable IMAP' won't be available. IMAP access is always turned on."* `autoExpunge`, `expungeBehavior ∈ {ARCHIVE, TRASH, DELETE_FOREVER}`, `maxFolderSize ∈ {0,1000,2000,5000,10000}`.

#### Add-ons · Chat and Meet · Advanced · Themes · Offline
**Add-ons:** "Enable developer add-ons" ❌ unsourced; Marketplace ✅.
**Chat and Meet:** Chat on/off; **Chat position** left/right; "Hide the Meet section in the main menu"; sidebar apps.
**Advanced:** ⚠️ **no reference article exists.** Custom keyboard shortcuts ✅ (adds a whole "Keyboard Shortcuts" settings tab) · Templates ✅ ("After you delete a template, you can't recover it") · Auto-advance ⚠️ semantics sourced, labels not · Right-side chat / Unread message icon / Auto-label ❌ unsourced · Preview Pane **obsolete** · Multiple Inboxes now an Inbox type.
**Themes** ([/112508](https://support.google.com/mail/answer/112508)): Default, Dark, presets, "My photos" (must be in Google Photos), plus **Text background / Vignette / Blur**.
**Offline** ([/1306849](https://support.google.com/mail/answer/1306849)): "Enable offline mail" ✅ · sync-period **values 7/30/90 ❌ UNSOURCED** · "Download attachments" ✅ · keep/remove offline data ❌ unsourced · **Chrome-only, non-Incognito** ✅ · **"can't preview attachments" offline** ✅.

### 2.2 Vacation responder — the behaviour that matters
[API `VacationSettings`](https://developers.google.com/workspace/gmail/api/reference/rest/v1/VacationSettings): `enableAutoReply`, `responseSubject`, `responseBodyPlainText`/`responseBodyHtml` ("Gmail will sanitize the HTML before storing it"; HTML wins), `restrictToContacts`, `restrictToDomain` (**Workspace only**), `startTime`/`endTime`.
**Re-send cadence, verbatim:** *"In most cases, your reply is only sent to people the first time they message you."* Exceptions that **do** re-send: the same person writes again **after four days** while it is still on; or you **edit** the reply. Active responder shows a banner with **"End now"** ([/25922](https://support.google.com/mail/answer/25922)).

### 2.3 Main-UI features — core vs peripheral

| Feature | Behaviour (documented) | Tier |
|---|---|---|
| Conversation view / threading | Splits on subject change or >100 emails | **CORE** |
| Labels vs folders | *"Labels are different from folders… only you can access your labels"*; delete removes from **every** label | **CORE** |
| Archive | Leaves inbox, stays in All Mail; **⚠️ "If someone replies to a message you archive, it returns to your inbox"**; `in:archive` | **CORE** |
| Delete / Trash | 30-day retention; **drafts exempt — a deleted draft is unrecoverable** | **CORE** |
| Snooze | Returns to **top of inbox**; `in:snoozed`; key `b` | **CORE** |
| Schedule send | **Max 100 scheduled**; cancel reverts to **draft** | **CORE** |
| Undo Send | 5/10/20/30 s; key `z` | **CORE** |
| **Mute** | Replies *"skip your inbox and go directly to your archive"*; **three escape hatches: sent only to you, sent to a Google Group you belong to, or you are added to To/Cc**; `is:muted` ([/16594169](https://support.google.com/mail/answer/16594169)) | **CORE** |
| Report spam / phishing / block sender | Spam trains the classifier; block routes to Spam | **CORE** |
| Unsubscribe | Shown next to sender name; may show a **list ID** instead of a name (implies `List-Unsubscribe`/`List-ID` parsing — Google never names the RFC) | **CORE** |
| Manage subscriptions | Shows recent send volume; unsubscribes from all lists for a sender | PERIPHERAL |
| Attachments | **25 MB send limit**; over limit Gmail **auto-substitutes a Drive link**; `.exe` blocked; up to **500 attachments** | **CORE** |
| Confidential mode | Disables forward/copy/print/download, expiry, SMS passcode; Google's own caveat: screenshots still possible | PERIPHERAL |
| Templates | **Web-only**; deletion unrecoverable; filters can "Send template" | PERIPHERAL |
| Print / Download `.eml` / Show original | Show original gives full headers + "Copy to clipboard" | **CORE** |
| **Translate message** | ⚠️ *"exclusively for Google Accounts and doesn't extend to messages in IMAP accounts"* ([/13846620](https://support.google.com/mail/answer/13846620)) | PERIPHERAL |
| Stars / superstars | **12 types**: yellow/orange/red/purple/blue/green-star, red-bang, orange-guillemet, yellow-bang, green-check, blue-info, purple-question; `has:red-bang` etc. | **CORE** (basic) / PERIPHERAL (super) |
| Importance markers | Signals Google names: whom you email and how often, which you open, reply to, star, archive, delete | PERIPHERAL |
| Categories / tabs | **⚠️ unavailable above 250,000 emails** | PERIPHERAL |
| Display density | **"Default" / "Comfortable" / "Compact"**; Default previews attachments | **CORE** |
| Reading pane | No split / Right of inbox / Below inbox | **CORE** |
| Search options panel + chips | From, To, Subject, Has the words, Doesn't have, Size, Date within, Search scope, Has attachment, Don't include chats → "Create filter". Chips: From, To, Any time, Has attachment, Exclude calendar updates, Is unread, Is encrypted | **CORE** |
| Select-all-matching-search + bulk actions | ❌ unsourced first-party; behaviourally real | **CORE** |
| Undo for actions (`z`) | Undoes archive/delete/label | **CORE** |
| Offline | Chrome-only; **no PWA install flow documented — Google recommends bookmarking** | **CORE** |
| Delegation / send-as | 10 or 1,000 delegates; 99 send-as addresses; per-alias signatures | PERIPHERAL / **CORE** |

### 2.4 Proprietary/AI vs generic — the sharpest finding

Per [/10079371](https://support.google.com/mail/answer/10079371), the "Smart features" master toggle gates **exactly four** experiences: **automatic categorization**, **Smart Compose**, **Smart Reply**, and **summary cards** (travel, package tracking).

Features that are predictive but have **their own independent controls, outside that umbrella**: **importance markers / Priority Inbox**, **Nudges**, Smart Compose personalization, spam-classifier learning.

Proprietary but **not** AI (platform lock-in): Confidential mode, Drive large-file substitution, Dynamic email/AMP, Gmailify, Chat/Meet/Tasks/Keep panels, Photos themes, Marketplace add-ons, `has:drive|document|…` operators.

> **Roadmap implication: only four features are genuinely AI-gated.** Everything else in §2.3 — archive, snooze, schedule send, undo send, mute, labels, stars, filters, templates, the full search language, vacation, signatures, delegation, forwarding, density, reading pane, shortcuts, offline, bulk actions — is deterministic capability a well-built client matches with **no model in the loop**. Any claim that Gmail-class parity requires AI is unsupported by Google's own documentation.

### 2.5 Gmail's full keyboard map ([/6594](https://support.google.com/mail/answer/6594)) — off by default

**Actions:** `x` select · `s` star/cycle superstars · `e` archive · `m` mute · `!` spam · `#` delete · `r`/`Shift+R` reply · `a`/`Shift+A` reply all · `f`/`Shift+F` forward · `]`/`[` archive+prev/next · `z` undo · `Shift+I`/`Shift+U` read/unread · `_` mark unread from here · `+`/`-` important · `b` snooze · `;`/`:` expand/collapse thread · `Shift+T` add to Tasks · `,` focus toolbar.
**Navigation:** `g i/s/b/t/d/a/k/l` inbox/starred/snoozed/sent/drafts/all/tasks/label · `g n`/`g p` page · `u` back · `k`/`j` newer/older · `o`/`Enter` open · `` ` ``/`~` inbox section.
**Selection:** `* a`/`* n` all/none · `* r`/`* u` read/unread · `* s`/`* t` starred/unstarred.
**Application:** `c`/`d` compose (new tab) · `/` search · `q` chat search · `.` more actions · `v` move to · `l` label as · `?` help.
**Compose:** `Ctrl+Enter` send · `Ctrl+Shift+c/b` cc/bcc · `Ctrl+Shift+f` custom from · `Ctrl+k` link · `p`/`n` prev/next message in thread · `Esc` focus compose.
**Formatting:** bold/italic/underline, font size, lists, quote, indent, align, `Ctrl+\` remove formatting.

### 2.6 Gmail's full search operator language ([/7190](https://support.google.com/mail/answer/7190))

`from:` `to:` `cc:` `bcc:` `subject:` · `after:` `before:` `older:` `newer:` `older_than:` `newer_than:` (d/m/y) · `OR` / `{ }` / `AND` / `-` / `AROUND n` · `label:` `category:` (primary|social|promotions|updates|forums|reservations|purchases) · `has:attachment` `has:youtube` `has:drive` `has:document` `has:spreadsheet` `has:presentation` `has:userlabels` `has:nouserlabels` `has:<color>-<icon>` · `list:` `filename:` · `" "` exact · `( )` group · `in:anywhere` `in:archive` `in:snoozed` · `is:muted` `is:important` `is:starred` `is:unread` `is:read` · `deliveredto:` · `size:` `larger:` `smaller:` · `+word` exact · `rfc822msgid:` · `header:` · `label:encryptedmail` · `recent:` (POP, 30 days).

> **Two claims to verify empirically before writing either into a spec:** the spam 30-day auto-delete, and the offline 7/30/90-day sync presets. Both are widely repeated; neither is sourceable to Google.

---

## 3. Moov today, honestly

### 3.1 The PWA — three destinations, one setting

**Scale:** 90 TS/TSX files, 20,377 lines (14,015 non-test), **3 npm dependencies** (`react`, `react-dom`, `dompurify`), 2 locales × 228 keys.

**Routes — there are three, total:** `/mail/:mailboxId[/:messageId]`, `/search[/:messageId]?q=`, and a fallback to inbox. Screens: `LoginScreen`, `MailScreen` (1,217 lines — the whole app), plus three modals (`Composer`, `SettingsDialog`, `ShortcutsDialog`).

| Feature | Status | Notes |
|---|---|---|
| Login, single-step, split-screen, per-domain branding | ✅ | 7-kind actionable error taxonomy. No remember-me, no reset, no SSO, no multi-account |
| Mailbox sidebar | ✅ read-only | ARIA `tree`, unread/total badges. **Cannot create/rename/delete/reorder/subscribe** |
| Virtualized list | ✅ | `ROW_HEIGHT=72`, `OVERSCAN=6`, `role="grid"` with true `aria-rowcount`/`aria-rowindex` |
| Thread grouping | ⚠️ partial | Groups adjacent same-thread rows; **no expandable conversation view** — the reader shows ONE message plus a "N messages in this thread" text line |
| Multi-select | ✅ | click / shift-range / ctrl-toggle / select-all, `pruneSelection` on refresh |
| Actions | ✅ 7 | compose, select-all, mark read, mark unread, flag, archive, delete, move-to-folder. **`unflag` exists in the model but has no button** — only the `s` key toggles |
| **Optimistic actions + inverse-patch rollback** | ✅ **better than Bulwark** | Patch-map overlay, never a list snapshot |
| Reading pane | ⚠️ thin | from/to/cc/replyTo, attachments with sizes, raw download, reply/reply-all/forward/archive/delete. **No print, no view-source, no mark-unread, no move, no next/prev** |
| **Secure HTML (3 layers)** | ✅ **best-in-class** | Allowlist tags/attrs/CSS props+functions → scheme classification on the *parsed* scheme → `<iframe srcdoc sandbox>` + CSP `default-src 'none'`. Remote images blocked, unblocked via **HMAC-signed imgproxy**. Attack corpus in tests |
| Search | ⚠️ | As-you-type, 180 ms debounce. **Plain text only, zero operators** |
| Compose | ✅ core | to/cc/bcc chips, subject, plain+rich body, attachments, drafts with 2 s/30 s autosave, reply/forward quoting, signature from Identity, **undo send countdown**. Rich text = 5 commands + link via `execCommand` |
| Push (SSE) | ✅ | Scoped `push` token; measured **671–942 ms** end-to-end |
| Keyboard | ✅ | See below |
| **Settings** | ❌ | **One section, one row, three radios** |
| Theme | ✅ | light(d)/dark/system, pre-paint inline script |

**Keyboard** (`web/src/keyboard/shortcuts.ts`) — architecturally *better* than Bulwark's (a pure `resolveShortcut(event, state) → Action` function, fully enumerable by tests, with real `g`-chords and a 1,200 ms timeout):

`j`/`↓` next · `k`/`↑` prev · `Enter`/`o` open · `u` back · `/` search · `e` archive · `#` delete · `Shift+I` toggle read · `s` toggle flag · `c` compose · `r` reply · `Shift+A` reply-all · `f` forward · `x` select row · `?` help · `Esc` close · `g` + `i`/`s`/`d`/`a`/`t` go to mailbox.

> **But one real regression vs Bulwark:** we resolve on `event.key`, which is **layout-dependent**. A Cyrillic or Greek keyboard loses every letter shortcut. Bulwark resolves on `event.code`. This is a small, cheap fix.

**Settings — the owner's claim, verified literally.** `SettingsDialog.tsx` contains exactly one `<SettingsSection titleKey="settings.section.appearance">` holding exactly one `<SettingRow labelKey="theme.label">` whose control is `<ThemeToggle/>` — three radios. Line 133 is a comment reading `THE NEXT SETTING GOES HERE`.

Absent: signature editor, display name, reply-to, vacation, filters, forwarding, aliases/identities, notifications, language picker, density, reading-pane position, conversation toggle, image policy, undo-send window, default compose mode, shortcuts on/off, timezone/date format, password change, quota, blocked senders, labels, import/export, sessions.

**It is not a PWA.** No service worker, no `manifest.webmanifest`, no workbox, no `vite-plugin-pwa`, no IndexedDB, no Cache API, no Notification API. `web/public/` contains one file (`favicon.svg`). Verified empirically too: `https://moov.atmosfera.cloud/manifest.webmanifest` and `/sw.js` both return **the SPA's `index.html`** (a 200 that is not a manifest). **Epic P4 of `L2-pwa.md` — "Service worker, IndexedDB con los últimos N mensajes, instalable, funciona sin red" — is entirely unstarted** (git history: P1, P2, P2b, P3 shipped; no P4 commit).

**Accessibility is genuinely strong:** skip link, `role="grid"` with true row indices, ARIA `tree` sidebar, native `<dialog>`+`showModal()` with focus restoration, APG menu-button, `role="toolbar"`, live regions, `eslint-plugin-jsx-a11y` at `--max-warnings 0`. Gaps: no reduced-motion handling, no focus-visible audit.

**Tests:** 30 vitest files, excellent on pure logic; **only three component tests**, and `MailScreen.tsx` (1,217 lines, the entire shell) **has none**. `moduleGraph.test.ts` enforces layering and catches orphaned modules.

### 3.2 The server — 17 registered methods

| Method | Supports | Notable limits |
|---|---|---|
| `Core/echo` | — | — |
| `Mailbox/get` · `/changes` · `/set` | roles, counts, `myRights`, create/rename/move/subscribe/destroy | `onDestroyRemoveEmails` → Trash, not destroy; role mailboxes refuse rename **and** destroy; **delete measured 1.8–6.1 s — misses the <100 ms bar** (documented, unpatched) |
| `Mailbox/queryChanges` | always `cannotCalculateChanges` | registered deliberately so clients don't see `unknownMethod` |
| `Thread/get` | ids + emailIds | **`Thread/changes` not registered** |
| `Email/get` · `/changes` · `/query` · `/queryChanges` · `/set` | see below | |
| `Identity/get` · `/changes` · **`/set`** | update `name`, `replyTo`, `bcc`, `textSignature`, `htmlSignature` | create → `forbiddenFrom`; destroy → `forbidden`; `email` immutable. Default id is the literal `"primary"` |
| `EmailSubmission/get` · `/set` · `/changes` | queue with undo window, `onSuccessUpdateEmail` | `maxDelayedSend: 0`; undo clamped [5 s, 30 s], default 10 s. **`/query` not registered** |

**`Email/query` filters accepted:** `inMailbox`, `text`, **`from`, `to`, `subject`** (all four map to the *same* single tsvector — deliberately over-matching, documented against §4.4.1), `after`, `before`, `hasKeyword` (custom keywords only), `notKeyword` (`$seen` — the unread filter), `null`, and `AND` of the above.
**Refused (`unsupportedFilter`):** `OR`, `NOT`, `inMailboxOtherThan`, `minSize`/`maxSize`, the three in-thread keyword conditions, **`cc`, `bcc`, `body`, `header`, `hasAttachment`**, `hasKeyword` on system flags, `collapseThreads`.
**Sorts:** `receivedAt`, `relevance` (bounded 200-row window), `hasKeyword`. Window `DefaultSearchWindow=200`, reach cap `MaxQueryReach=10000`.

> The code names the cheapest next wins itself: *"hasAttachment and cc/bcc are the ones worth closing first: the store HAS a has_attachments column and a cc column, so they are a store-method away rather than an index away."*

**Advertised capabilities: exactly three** — `core`, `mail`, `submission`. Account capability: `maxMailboxesPerEmail:1`, `maxSizeMailboxName:255`, `maxSizeAttachmentsPerEmail:25 MB`, `emailQuerySortOptions:[receivedAt, relevance, hasKeyword]`. Declared == enforced, pinned by test.

**Store schema (6 migrations):** `accounts` · `mailboxes` · `messages` (+`thread_id`, GIN `tsv`) · `message_state` · `blobs`/`blob_refs` · `sync_log` · `intents` (outbox) · `thread_subject_keys` · `identities`. **Nothing for** Sieve, vacation, quota, contacts, calendar, **or per-user preferences**.

**Where preferences could live today:** only the `identities` table (name, reply_to, bcc, signatures) via the conforming `Identity/set`, plus one `localStorage` key (`moov.theme.v1`). Everything else needs new design.

---

## 4. Consolidated gap table

Category: **(a)** server has it, PWA doesn't expose it · **(b)** server lacks it but an RFC/draft defines it · **(c)** neither — needs new design.
Size: **XS** ≤ ½ day · **S** 1–2 d · **M** 3–5 d · **L** 1–2 wk · **XL** > 2 wk. Sizes are rough engineering judgement, not commitments.

### 4.1 Category (a) — the server already does it; only the PWA is missing. **The cheapest wins in the product.**

| Feature | Bulwark | Gmail | Moov PWA | Moov server | Cat | Size | Exact server surface |
|---|---|---|---|---|---|---|---|
| **Signature editing (text + HTML)** | ✅ | ✅ | ❌ | ✅ | **a** | **XS** | `Identity/set` update `textSignature`/`htmlSignature` — sanitized server-side. PWA already *reads* it to prefill compose |
| **Display name / Reply-To / default Bcc** | ✅ | ✅ | ❌ | ✅ | **a** | **XS** | `Identity/set` `name`, `replyTo`, `bcc` |
| **Unread-only filter/view** | ✅ | ✅ | ❌ | ✅ | **a** | **XS** | `notKeyword:"$seen"` already implemented |
| **Date-range search (`before:`/`after:`)** | ✅ | ✅ | ❌ | ✅ | **a** | **XS** | `after`/`before` RFC 3339 conditions |
| **`from:`/`to:`/`subject:` operators** | ✅ | ✅ | ❌ | ✅ (broad) | **a** | **S** | Accepted, but all map to the shared tsvector — over-matching, documented against §4.4.1 |
| **Sort by starred/unread first** | ✅ | ✅ | ❌ | ✅ | **a** | **XS** | `emailQuerySortOptions` advertises `hasKeyword`; PWA never sends `sort` |
| **Folder create / rename / move / delete / subscribe** | ✅ | ✅ | ❌ | ✅ | **a** | **S** | Full `Mailbox/set`; `myRights` served and currently ignored. ⚠️ delete measured 1.8–6.1 s |
| **Pagination / infinite scroll past 200** | ✅ | ✅ | ❌ | ✅ | **a** | **S** | `position`/`limit`, keyset cursor, `MaxQueryReach=10000` |
| **Outbox / delivery status / failed sends** | ✅ | ✅ | ❌ | ✅ | **a** | **S** | `EmailSubmission/get` + `/changes` with `deliveryStatus`, `undoStatus`, `sendAt` |
| **Configurable undo-send window** | ✅ (0/10/30/60) | ✅ (5/10/20/30) | ❌ | ✅ | **a** | **XS** | Clamped [5 s,30 s] server-side; no user control |
| **Keyword/label filtering** | ✅ | ✅ | ❌ | ✅ | **a** | **S** | `hasKeyword` + `message_state.keywords` GIN. ⚠️ bounded by the **26-keyword** ceiling |
| **Whole-thread actions (archive/delete thread)** | ✅ | ✅ | ❌ | ✅ | **a** | **M** | `Thread/get` returns all `emailIds`; `Email/set` takes many ids |
| **`ifInState` optimistic concurrency** | ❌ | — | ❌ | ✅ | **a** | **XS** | `stateMismatch` supported, never sent |
| **Incremental refresh via `Email/changes`** | ❌ | — | ❌ | ✅ | **a** | **S** | Registered and tombstone-correct; PWA refetches the window instead. **We'd beat Bulwark, which never calls `/changes`** |
| **Reply-To on outgoing mail** | ✅ | ✅ | ❌ | ✅ | **a** | **XS** | Already serialized in `draftObject` |

> **This block alone is most of the owner's complaint, and none of it needs protocol work, migrations, or new dependencies.** Seven items are XS.

### 4.2 Category (b) — an RFC/draft defines it; our server does not implement it

| Feature | Bulwark | Gmail | Moov PWA | Moov server | Cat | Size | Spec |
|---|---|---|---|---|---|---|---|
| **Vacation / auto-responder** | ✅ | ✅ | ❌ | ❌ | **b** | **M** | **RFC 8621 §8** `VacationResponse/get`+`/set`, singleton. Advertising `urn:…:vacationresponse` alone unlocks Bulwark's tab |
| **Filters / rules (server-side)** | ✅ | ✅ | ❌ | ❌ | **b** | **XL** | **RFC 9661** `SieveScript/get`/`set`/`validate` + blob upload. Needs ManageSieve/Dovecot plumbing |
| **Forwarding** | ✅ (via Sieve) | ✅ | ❌ | ❌ | **b** | — | Sieve `redirect` — rides on the above |
| **Blocked senders** | ✅ | ✅ | ❌ | ❌ | **b** | — | Sieve `discard`/`fileinto Junk` — rides on the above |
| **Quota / storage display** | ✅ | ✅ | ❌ | ❌ | **b** | **S** | **RFC 9425** `Quota/get`. Mailcow already exposes quota; we simply never read it |
| **Search-result highlighting** | ❌ | ✅ | ❌ | ❌ | **b** | **M** | **RFC 8621 §5** `SearchSnippet/get`. **Bulwark never calls it either — a chance to beat both** |
| **Read receipts (MDN)** | ✅ | ❌ | ❌ | ❌ | **b** | **M** | RFC 8098 / RFC 9007. We serve `mdnBlobIds` but nothing produces or consumes MDNs |
| **Web Push (background notifications)** | ✅ | ✅ | ❌ | ❌ | **b** | **L** | RFC 8620 §7.2 `PushSubscription/get`+`/set` + a relay. We have SSE (foreground only) |
| **Spam-filtered push** | ✅ | — | ❌ | ❌ | **b** | — | draft-ietf-jmap-emailpush `emailPush` delivery filter |
| **Multiple identities / aliases** | ✅ | ✅ (99) | ❌ | ⚠️ partial | **b** | **M** | `Identity/set` **create** returns `forbiddenFrom` — needs alias verification against Mailcow |
| **`hasAttachment` / `cc` / `bcc` search** | ✅ | ✅ | ❌ | ❌ | **b** | **S** | §4.4.1 conditions. The code names these first: *"the store HAS a has_attachments column and a cc column"* |
| **`OR` / `NOT` in search** | ✅ | ✅ | ❌ | ❌ | **b** | **M** | §4.4.1 `FilterOperator` |
| **Contacts + autocomplete** | ✅ | ✅ | ❌ | ❌ | **b** | **XL** | JMAP Contacts draft / RFC 9553. No contacts store at all today |
| **Cross-account move / import `.eml`** | ✅ | ✅ | ❌ | ❌ | **b** | **M** | `Email/copy`, `Email/import`, `Email/parse` |
| **Delegation / shared mailboxes** | ✅ | ✅ | ❌ | ❌ | **b** | **XL** | RFC 9670 `Principal/*`; today `isPersonal:true` always |
| **Calendar / Files** | ✅ | ✅ | ❌ | ❌ | **b** | **XL** | Out of ADR scope — listed for completeness only |

### 4.3 Category (c) — neither has it; needs new design

| Feature | Bulwark | Gmail | Moov | Cat | Size | Note |
|---|---|---|---|---|---|---|
| **A settings persistence mechanism at all** | ✅ (localStorage + encrypted file BFF) | ✅ | ❌ | **c** | **M** | **The foundational gap.** JMAP has no generic preferences object. We have PostgreSQL — a `settings` JSONB on `accounts` beats Bulwark's scheme outright |
| **Settings IA (sections, rows, primitives)** | ✅ 26 tabs | ✅ 10 tabs | ❌ 1 row | **c** | **M** | The `SettingsSection`/`SettingRow` scaffold already exists — literally one row and a "next setting goes here" comment |
| **Settings search with sub-results** | ✅ | ❌ | ❌ | **c** | **S** | Client-side only. Genuinely differentiating; Gmail lacks it |
| **Conversation/thread view (expandable)** | ✅ | ✅ | ❌ | **c** | **L** | Server has `Thread/get` + real threading, so it is *enabled*; the UI is entirely new design. **The single most defining modern-mail feature we lack** |
| **Offline (service worker + IndexedDB + outbox)** | ❌ **none** | ✅ (Chrome-only) | ❌ | **c** | **L** | **P4, unstarted.** Bulwark's SW is a deliberate no-op. **Pure differentiation territory** |
| **PWA installability (manifest + SW)** | ✅ | ❌ (Google says bookmark it) | ❌ | **c** | **S** | We ship neither manifest nor SW; the product's name currently overstates it |
| **Density / reading-pane position / layout prefs** | ✅ | ✅ | ❌ | **c** | **S** | Depends on the settings mechanism |
| **Desktop notifications** | ✅ | ✅ | ❌ | **c** | **S** | Notification API, foreground; Web Push is the (b) item above |
| **Snooze** | ❌ | ✅ | ❌ | **c** | **L** | No JMAP standard. Needs a store-side timer + a hidden mailbox or state column |
| **Schedule send (user-chosen time)** | ✅ | ✅ | ❌ | **c** | **M** | `maxDelayedSend:0` is truthful — Postfix submission offers no FUTURERELEASE. Needs an outbox-side scheduler (the `intents` table already exists) |
| **Mute conversation** | ❌ | ✅ | ❌ | **c** | **M** | Gmail's three escape hatches are a precise spec; needs a thread-level flag |
| **Templates / canned responses** | ✅ | ✅ | ❌ | **c** | **S** | Pure client + settings storage |
| **Language picker** | ✅ 25 | ✅ | ❌ (auto only) | **c** | **XS** | i18n exists; no override control |
| **RTL support** | ✅ | ✅ | ❌ | **c** | **M** | No RTL anywhere today |
| **Multi-account in one tab** | ✅ | ✅ | ❌ | **c** | **XL** | Bulwark's own experience says this is where the cost lives (8 resolver helpers, SSE budget, namespaced ids) |
| **Print / show-original / view headers** | ✅ | ✅ | ❌ | **c** | **XS** | Raw blob already downloadable — mostly a button |
| **Keyboard layout independence** | ✅ `event.code` | — | ❌ `event.key` | **c** | **XS** | We break on Cyrillic/Greek layouts. Cheap fix |
| **Bulk "select all matching search"** | ✅ | ✅ | ❌ | **c** | **M** | Needs a server-side "apply to query" path or a bounded client loop |
| **Sender avatars / hover actions / swipe** | ✅ | ✅ | ❌ | **c** | **S–M** | Polish tier |
| **Undo for move/delete/archive** | ❌ | ✅ (`z`) | ⚠️ rollback only | **c** | **S** | We have inverse patches already — a toast + timer would surface it. **Bulwark lacks this entirely** |

### 4.4 The shape of the gap, summarized

| Category | Count | Character |
|---|---|---|
| **(a)** server ready, UI missing | **15** | 7 are XS. **No protocol work, no migrations, no new deps.** |
| **(b)** RFC exists, server missing | **16** | Two cheap and high-value (`Quota/get`, `hasAttachment`/`cc`/`bcc`); Sieve and Contacts are the XL anchors |
| **(c)** needs new design | **20** | Dominated by one foundational item (**settings persistence**) plus conversation view and offline |

**The critical path is short and specific:** a settings persistence mechanism (c, M) unlocks most of the (a) block, which is where the owner's complaint actually lives. Conversation view and offline are the two large items that decide whether we are Gmail-class or merely competent.

---

## 5. What Bulwark does better than us today — concretely

1. **Settings: 26 tabs vs 1 row.** ~95 synced preferences vs one theme choice. Most of Bulwark's need **no backend at all** — this gap is UI work, not protocol work.
2. **Conversation view.** Bulwark expands threads inline with per-message actions; we show a text line saying how many messages exist. This is the single most defining feature of modern mail UX.
3. **Search.** Filter panel, chips, wildcards, OR conditions, cross-mailbox queries, recent-search suggestions and contact autocomplete — vs our plain text box with zero operators.
4. **Folder management.** Full CRUD, drag-to-nest, role assignment, per-folder icons — **against a server that already supports all of it in our case too**.
5. **Filters/rules and vacation responder.** Visual builder over RFC 9661 Sieve with origin-preserving round-trip; RFC 8621 §8 vacation. We have neither, at any layer.
6. **Identities and signature editing.** Full CRUD with dual text/HTML signatures — **and our server already implements `Identity/set` with editable signatures; we just never built the form.**
7. **Pagination.** Infinite scroll with deduped offset paging; we load one 200-row window and show a "truncated" notice.
8. **i18n scale.** 25 locales × 3,222 keys with enforced parity and true RTL, vs 2 × 228 and no RTL.
9. **Keyboard layout independence** (`event.code`) — we break on non-Latin layouts.
10. **Transport discipline.** Request coalescing, request-limit batching, JSON-Pointer escaping, first-touch gating, the SSE socket budget, stale-stream recycling on `visibilitychange`. Each encodes a specific production bug.
11. **Breadth we have not attempted:** calendar, contacts, files, templates, plugins/themes, multi-account, S/MIME, TOTP, Web Push, protocol handlers, sub-addressing, TNEF, `.eml` import/export, print.

### 5.0 Mechanisms worth copying outright (each encodes a specific production bug)

These are the parts of Bulwark that are *engineering floor*, not feature list. Each is small, self-contained, and encodes a failure we would otherwise ship and then hunt.

| Mechanism | Size | What it prevents | Applies to us? |
|---|---|---|---|
| **`patch-pointer.ts`** — RFC 6901 escaping of JSON Pointers in PatchObject keys | 29 lines | A nested tag `$label:work/clients` makes `keywords/$label:work/clients` address the *`clients` member of `$label:work`*. **The tag silently never lands.** | **Yes, the moment nested labels ship.** Fails silently |
| **`request-limits.ts`** — `batched()` + `itemsPerRequest(maxCalls, callsPerItem)` | 26 lines | RFC 8620 ceilings fail the **entire** request, not the surplus. "Nine tags is already 18 calls" | Yes — we declare `maxCallsInRequest`/`maxObjectsInGet` and will hit them |
| **`coalesceRefresh`** — WeakMap per client × Map per operation, share in-flight, exactly one rerun | ~25 lines | One push event fans out into several refreshes × open tabs → `maxConcurrentRequests` refusal **blanked the sidebar folder tree** (#780) | Yes — our SSE refresh has the same fan-out shape |
| **SSE socket budget** (`MAX_SSE_STREAMS=2`, microtask promotion) | ~60 lines | 6 HTTP/1.1 connections per host, shared across tabs: from the 6th login, **sends grey out and hang forever** | Only if multi-account/multi-tab lands — but the failure is opaque |
| **`recycleStaleSSE()` on `visibilitychange`** | ~15 lines | iOS home-screen PWA **freezes timers**, so a watchdog `setInterval` is itself asleep and cannot notice a dead socket | **Yes — directly relevant to P4 offline/PWA** |
| **Page-size-derived merge cutoff** (`appendFromIndex = max(emailsPerPage − insertedCount, 0)`) | ~10 lines | A length-based cutoff re-appended deleted rows from stale state → ghost drafts that **re-sent mail when clicked** (#592) | Yes — same merge problem on push refresh |
| **`first-touch-gate.ts`** | 101 lines | Concurrent first-touch creates **two undeletable default calendars** in clustered deployments (#907) | Not today (no calendar), but the pattern generalizes to lazy server-side creation |
| **`event.code` physical-key shortcuts** | ~15 lines | Every letter shortcut dies on Cyrillic/Greek layouts | **Yes — we have this bug now** |
| **Split draft destroy from create** (#849) | — | A combined `Email/set {create, destroy}` runs both independently; a `blobNotFound` create still lets the destroy **delete the last good draft** | Yes — our draft path does create+destroy |
| **`retainedInViewIds`** | ~20 lines | In self-filtering views (unread/starred), acting on a row makes it stop matching, so it **vanishes under the cursor** on the next refresh | Yes, once we add an unread view (a §4.1 XS item) |
| **Favicon-link discipline** | ~20 lines | Mutating React-owned `<link rel=icon>` throws; and **clearing a badge must be an insertion**, because Firefox re-evaluates favicons only on insertion | Yes if we add an unread badge |
| **No placeholder fallbacks** (throw, never synthesize) | policy | A refused `Mailbox/get` once replaced the real folder tree with a lone fake "Inbox" until reload (#780) | Yes — a posture, not code |
| **`RequestTimeoutError` is never retried** | policy | Replaying `EmailSubmission/set` **sends the mail twice** | **Yes — our outbox must honour the same rule** |
| **Sieve `@metadata:begin` JSON header + origin partitioning** (`bulwark`/`external`/`opaque`) | ~800 lines total | Destroying a hand-written or Nextcloud-written Sieve script on round-trip | Yes, when filters land — and it matches our "Dovecot is the source of truth" posture exactly |
| **Client-generated `Message-ID`** | ~10 lines | Server-synthesized ids leak internal hostnames (`@ip-10-0-12-97.ec2.internal`) — an anti-spam signal and an information disclosure | Worth checking our W3 assembly |
| **Omit empty `Cc`** | 1 line | `cc:[]` emits a literal empty `Cc:` header — malformed and a spam signal | Worth checking our W3 assembly |
| **`isEditableEventTarget()` via `composedPath()`** | ~10 lines | Inside a shadow root, `activeElement` and `event.target` are **retargeted to the host**, so a `contentEditable` island looks like a plain `<div>` and single-key shortcuts fire while typing — **up to deleting the open email on Backspace** (#654) | Only if we adopt shadow DOM; the `composedPath` principle is right regardless |
| **`QuotedHtml` as an atomic node with a Shadow-Root NodeView** | ~200 lines | The quoted original is held **verbatim, never parsed into the editor schema**, so nested tables/MJML/Outlook markup survive reply-forward 1:1 — and the shadow root stops Tailwind preflight from cascading in and destroying the quote's layout | **Yes** — this is the correct answer to "quotes get mangled", and our composer will hit it |
| **Rebuild `srcDoc` on image-unblock (never restore URLs in place)** | policy | **A document's CSP is fixed at load** — the strict CSP keeps refusing restored URLs, so in-place restoration silently fails | **Yes — we use the same sandboxed-iframe + CSP approach** |
| **DOMPurify `DATA_URI_TAGS` re-check + `srcset` per-candidate** | ~40 lines | DOMPurify's data-URI carve-out **short-circuits `ALLOWED_URI_REGEXP`** for `IMG/VIDEO/AUDIO/SOURCE/TRACK`, and the set can only be extended, never trimmed; `srcset` is URI-tested as one string, so an allowed first candidate waves every later one through | **Yes — check our sanitizer against both** |
| **C0-control URL normalization + CSS-escape decoding of the whole stylesheet** | ~30 lines | `"\n\nhttps://t"`, `"h\ttps://t"`, protocol-relative `//host`, and `\75\72\6C(` → `url(` are real Email-Privacy-Tester bypasses (#457) | **Yes — directly applicable to our three-layer sanitizer** |
| **Raster-GIF blocked-pixel placeholder (not SVG)** | 1 line | The placeholder must survive the data-URI restrictor running in the same hook pass, which has no ordering guarantee — an SVG placeholder gets stripped | Yes |
| **Single chokepoint for body sanitization** | policy | Plugin-decrypted PGP/S-MIME bodies took a sanitize-only path and **fetched tracking pixels regardless of the user's preference** (#797) | Yes — one function no render path can bypass |
| **Swipe tuning constants** (12 px claim, 96 px commit, 140 px max, 0.3× damping, 0.15× on an unconfigured side) + `touch-action: pan-y` | ~80 lines | Gesture fights with the list scroller; no `preventDefault` needed | Yes, when mobile lands |

### 5.1 …and where we are already better

1. **Optimistic UI with real rollback.** Bulwark is uniformly await-then-patch; every action waits on the network. We compute an inverse patch and roll back on failure. Directly serves rule 1's <100 ms bar.
2. **Server-side threading over the whole store** — 1,168 multi-message threads on real data, the largest 24 deep. Bulwark groups only the loaded page and mis-splits threads across pagination boundaries.
3. **Instant FTS over the entire account** (tsvector+GIN, S3-validated to 5 M messages) — Bulwark delegates search entirely to Stalwart.
4. **A three-layer HTML sanitizer** (allowlist → parsed-scheme classification → sandboxed `srcdoc` + CSP) **plus an HMAC image proxy with anti-SSRF**. Bulwark relies on DOMPurify + CSP and has **no image proxy**.
5. **Per-scope short-lived tokens** for SSE and blobs; Bulwark pins `Authorization` on a long-lived stream.
6. **ID design immune to the JSON-Pointer numeric trap** that silently stranded Bulwark users' mail.
7. **Delta sync exists server-side** (`Email/changes`, `Mailbox/changes` with tombstones). Bulwark never calls `/changes` at all — a full page refetch per push event.
8. **A real sync engine and local store**, so we are not limited by what the upstream server indexes. Bulwark's speed is borrowed; ours is owned.
9. **Keyboard resolution as a pure, enumerable function** with `g`-chords — cleaner than Bulwark's 200-line switch.
10. **Conformance discipline:** a suite cited clause-by-clause to RFC 8620/8621, 12 green + 9 explicit skips, in CI.
11. **We proxy remote images; Bulwark does not.** Its mail path blocks or loads directly — the HMAC/anti-SSRF proxy in its FEATURES.md covers server-side fetches (iCal, favicons) only.

### 5.2 Where Bulwark is *also* weak — open ground, not just catch-up

Three of the gaps in §4 are places where **nobody in this comparison is strong**, so the work buys differentiation rather than parity:

| Area | Bulwark | Gmail | The opening |
|---|---|---|---|
| **Search query language** | **No operator syntax at all** — a free-text box plus an AND-only filter panel; the builder never emits `OR`. No saved searches | Full operator language (§2.6) | Our S3 engine (tsvector+GIN, 8/10 shapes at 4–30× margin, 607 qps) is already ahead of what Bulwark's UI can express. A real operator language is cheap on top of an engine we already have — and it is the difference between "search box" and Gmail-class |
| **Offline** | **None. The service worker's fetch handler is `() => {}` by design** | Chrome-only; **Google recommends bookmarking rather than installing** | Genuine offline (SW + IndexedDB + outbox) beats *both* references. This is P4 |
| **Keyboard depth** | ~24 bindings, **no chords** (no `g i`/`g s`, no `[`/`]`, no `z` undo, no `n`/`p` in-thread) | Full chorded map (§2.5) | We already have `g`-chords and a pure resolver. Adding `z`, `[`/`]`, `v`, `l` puts us past Bulwark on our existing architecture |
| **Interaction-layer testing** | **Zero e2e coverage** of shortcuts, menus, swipe/drag, search, sanitization, RTL, a11y | — | Our policy already requires tests per AC; this is a place to be strictly better rather than merely equal |

---

## 6. Appendix — verification notes

- Bulwark source: `github.com/bulwarkmail/webmail` @ `5dcef71`, `VERSION` = 1.9.2, AGPL-3.0-only. Cloned to a scratch dir, not vendored.
- Deployed Bulwark: **v1.8.1**, build `a066108`, with an unpatched **GHSA-24w9-8r42-8jwm** advisory shown on its own login page.
- Bulwark runtime config observed: `jmapServerUrl: https://moov.atmosfera.cloud`, `stalwartFeaturesEnabled: true`, `settingsSyncEnabled: false`, `demoMode: false`, `oauthEnabled: false`.
- Moov PWA/server facts are from the working tree at `542250c`; no files were modified.
