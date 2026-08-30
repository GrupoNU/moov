# 06 — The Gmail canon: what Gmail-class actually is, verified

> **Status:** research / evidence base — the foundation for the L3 v3 plan. Supersedes §2 of `05-gmail-class-surface.md` as the Gmail reference; 05 remains valid only as the Bulwark mechanism catalogue (§1, §5.0) and is corrected by §6 below.
> **Date:** 2026-08-30 · **Retrieval date for every live citation: 2026-08-30.**
> **Method:** four parallel research agents (2× Fable 5 for product criterion, 2× Opus for schema/inventory), every report audited by the director with spot-checks against the live sources (`users.labels` API page, `/mail/answer/5900`) and against the code (`query.go`, `thread.go`). Every claim cites a Google-authored URL fetched on the retrieval date; what could not be sourced is in §5, never silently asserted.
> **Mandate (owner, 2026-08-30):** Gmail defines WHAT exists and WHY. Bulwark, where it coincides with Gmail, contributes only HOW. The previous plan copied Bulwark's surface and broke on first contrast (the favicon case); this document exists so that never happens again.

---

## 1. The Gmail filter — mandatory for every item

1. **Does Gmail have it?** If yes → adopt the mechanics Gmail chose (they encode 20 years of abuse data), verified against Google-authored documentation, never from memory.
2. **If Gmail doesn't have it — why not?** The reason is almost always one of four:
   **(a) trust/impersonation** — anything lending unearned credibility to a sender (favicons, unverified logos);
   **(b) privacy/tracking** — anything leaking recipient behavior to senders (read receipts, unproxied images, AMP);
   **(c) consent for data use** — anything reading message content to power a feature (the smart-features toggle);
   **(d) surface-area discipline** — anything Gmail killed or never built because complexity is a cost (Labs, Inbox bundles, Basic HTML, debug UIs, games).
3. **Divergence is legitimate only when the reason found in (2) is a Google business artifact** (Chrome-only offline, no PWA install), never when it is a security/privacy stance — and it is never decided at feature level: every divergence is a director arbitration recorded in the plan, signed by the owner.

Template case (decided 2026-08-27, plan v2 §5 bis): sender logos. Bulwark fetches DuckDuckGo favicons — attacker-controlled identity. Gmail: logos **only** via BIMI — DMARC `p=quarantine|reject` + VMC/CMC certificate, trademark required; VMC senders additionally get a checkmark; everyone else gets initials ([BIMI setup](https://knowledge.workspace.google.com/admin/security/set-up-bimi)). Moov keeps initials.

## 2. The core interaction canon

Tier legend: **CORE** = a daily-driver webmail must match it · **SEC** = expected, survivable without. Quotes are verbatim from the cited page.

### 2.1 Conversation view — CORE, the defining behavior

- Grouping: replies grouped "with the latest email at the bottom of a conversation thread". Criteria: "the same recipients, senders, or subjects as a previous message" **or** "a reference header with the same IDs", **and** "sent within one week of a previous message". Split rule: "when the subject line changes, or the conversation gets to more than 100 emails". On/off setting exists; turning it off hides nudges. ([/5900](https://support.google.com/mail/answer/5900) — director-verified live)
- Older messages render collapsed; quoted/repeated material hides behind "Show trimmed content" ([/11468381](https://support.google.com/mail/answer/11468381)). `;`/`:` expand/collapse the whole conversation; `p`/`n` navigate messages inside it ([/6594](https://support.google.com/mail/answer/6594)).
- Reply/reply-all/forward are per-message; toolbar archive/delete/label act on the conversation. API nuance: "labels are only added to a message, and not an entire conversation"; later messages don't inherit thread labels ([labels guide](https://developers.google.com/workspace/gmail/api/guides/labels)).
- **Moov note:** our JWZ threading (References both directions + normalized-subject fallback) is a superset of Gmail's criteria except the one-week window and the >100/subject-change split — both are presentation-split decisions, not storage decisions.

### 2.2 Triage verbs — the complete set

| Verb | Canonical behavior | Tier | Source |
|---|---|---|---|
| Archive | Leaves inbox, stays in All Mail; **"if someone replies to a message you archive, it returns to your inbox"**; `in:archive` | CORE | [/6576](https://support.google.com/mail/answer/6576) |
| Delete/Trash | 30-day retention, then permanent (doubly sourced); **deleted drafts unrecoverable**; "Delete forever" / "Empty Trash now" | CORE | [/7401](https://support.google.com/mail/answer/7401) |
| Snooze | "Removed from your inbox temporarily"; returns "to the **top of your inbox**"; `in:snoozed`; key `b`; `g b` goes to Snoozed | CORE | [/7622010](https://support.google.com/mail/answer/7622010) |
| Mute | Replies "skip your inbox and go directly to your archive"; **three escape hatches**: sent only to you / sent to a group you're in / you're added to To or Cc; `is:muted`; key `m` | CORE | [/16594169](https://support.google.com/mail/answer/16594169) |
| Report spam / not spam | Trains the classifier; "Google receives a copy of the email"; not-spam whitelists the sender; key `!` | CORE | [/1366858](https://support.google.com/mail/answer/1366858) |
| Block sender | "All future emails from them go to Spam"; does NOT unsubscribe | CORE | [/8151](https://support.google.com/mail/answer/8151) |
| Unsubscribe | Button next to sender name; list-ID fallback; "Go to website" fallback; days of latency disclosed | CORE | [/15433283](https://support.google.com/mail/answer/15433283) |
| Manage subscriptions | Left nav; recent send volume per sender; unsubscribe-all per sender; then "any new emails from them go to your spam folder" | SEC | [/15621070](https://support.google.com/mail/answer/15621070) |
| Star | `s`; 12 superstar types with `has:<color>-<icon>` search names; cycle-on-click; presets 1/4/all | CORE (basic) / SEC (super) | [/5904](https://support.google.com/mail/answer/5904) |
| Important | ML markers with named signals, manual toggle trains it, two opt-outs | SEC (AI-gated in spirit) | [/186543](https://support.google.com/mail/answer/186543) |
| Read/unread | `Shift+I` / `Shift+U` / `_` mark-unread-from-here | CORE | [/6594](https://support.google.com/mail/answer/6594) |
| Move / Label | `v` move-to menu, `l` label-as menu | CORE | [/6594](https://support.google.com/mail/answer/6594) |
| **Undo action** | `z` "Undo last action" (documented as shortcut; undoable set and toast duration unsourced) | CORE | [/6594](https://support.google.com/mail/answer/6594) |
| Archive-and-advance | `]` / `[` | SEC | [/6594](https://support.google.com/mail/answer/6594) |
| Hover actions | Exactly four — "archive, delete, snooze, or mark a message as read" — ON by default, one "Disable hover actions" setting | CORE | [/2473038](https://support.google.com/mail/answer/2473038) |
| Auto-advance | OFF by default (back to list); opt-in "older messages, newer messages, or the conversation list" | SEC | [/6562 Android](https://support.google.com/mail/answer/6562?co=GENIE.Platform%3DAndroid) |

### 2.3 Sending

- **Undo send: 5 / 10 / 20 / 30 seconds** exactly ([/2819488](https://support.google.com/mail/answer/2819488)). Moov's clamp [5,30] is compatible; the offered values become Gmail's four.
- **Schedule send:** max **100** scheduled; **cancel reverts to draft**; sent per scheduling timezone; "may be sent a few minutes after" ([/9214606](https://support.google.com/mail/answer/9214606)).
- **Send & Archive** button-in-reply setting ([/a/users/9282734](https://support.google.com/a/users/answer/9282734)).
- **Signatures:** up to 10,000 chars; **multiple named signatures**; per send-as address; **separate defaults for new mail vs replies/forwards**; images count ([/8395](https://support.google.com/mail/answer/8395)).
- **Attachments:** 25 MB; over-limit auto-substitutes a Drive link (the substitution is N/A-Google; the limit matches our advertised 25 MB); blocked executable/archive extension list published, error string included ([/6584](https://support.google.com/mail/answer/6584), [/6590](https://support.google.com/mail/answer/6590)).
- **Contact autocomplete without a contacts app:** Gmail auto-saves addresses you email to "Other contacts" and autocompletes from them; user opt-out "I'll add contacts myself" ([/contacts/1069522](https://support.google.com/contacts/answer/1069522)). **This is Gmail's own blessing of the address-index-without-contacts-subsystem design.**
- Forward as attachment → `.eml` ([/9337672](https://support.google.com/mail/answer/9337672)). Default reply behavior reply/reply-all. Plain-text mode (unsourced as a row; behaviorally real).

### 2.4 Inbox models, reading pane, density

- **Six inbox types** ([/186531](https://support.google.com/mail/answer/186531)): Default (tabs), Important first, Unread first, Starred first, Priority Inbox (sections, customizable), Multiple Inboxes (per-section query + name + page size + position, "computer only", [/9694882](https://support.google.com/mail/answer/9694882)). Sectioned types are "X at the top, 'Everything else' at the bottom".
- Categories: 5 fixed tabs, no custom; Reservations/Purchases search-only; **>250,000 emails → Default type unavailable** ([/3055016](https://support.google.com/mail/answer/3055016)). AI-gated → out of Moov's deterministic scope (IA phase, consent-gated).
- **Reading pane:** "No split" / "Right of inbox" / "Below inbox" + enable checkbox, also in Quick settings ([/9499937](https://support.google.com/mail/answer/9499937)).
- Density: Default / Comfortable / Compact (labels unsourced on a fetchable page; behaviorally real). Snippets on/off. Button labels icons/text.
- Selection chords `* a` / `* n` / `* r` / `* u` / `* s` / `* t` sourced; the "select all conversations that match this search" banner is real in-product but unsourced — cite the in-product string.

### 2.5 Search — the operator language ([/7190](https://support.google.com/mail/answer/7190), panel and chips [/6593](https://support.google.com/mail/answer/6593))

`from: to: cc: bcc: deliveredto: subject:` · `" "` `( )` `+word` `AROUND n` · `OR { } AND -` · `after:/before:` (formats `2004/04/16` and `04/16/2004`), `older_than:/newer_than:` (d/m/y) · `label: category: list: filename: has:userlabels has:nouserlabels label:encryptedmail` · `has:attachment` (+ Google-content `has:` variants, N/A) · `in:anywhere in:archive in:snoozed` · `is:muted is:important is:starred is:unread is:read` · 12 star names · `size: larger: smaller:` · `rfc822msgid:` · **`header:` confirmed with example**. Spam/Trash excluded by default; `in:anywhere` includes them. Search options panel: From, To, Subject, Has the words, Doesn't have, Size, Date within, scope, Has attachment → **"Create filter"**. Chips: From, To, Any time, Has attachment, Is unread. Suggestions from contacts/labels/messages/past searches.

### 2.6 Labels — the model and its limits

- One primitive: **filter actions are exactly `addLabelIds[]` / `removeLabelIds[]` / `forward`** — archive = remove INBOX, mark-read = remove UNREAD, star = add STARRED, delete = add TRASH, never-spam = remove SPAM ([filter guide](https://developers.google.com/workspace/gmail/api/guides/filter_settings)). Criteria: from, to (incl. cc/bcc), subject, query, negatedQuery, hasAttachment, excludeChats, size(bytes)+comparison — **no date criterion** (filters run forward in time; dates belong to search). Forward only to **verified** addresses.
- `labelListVisibility`: `labelShow` / **`labelShowIfUnread`** / `labelHide` (API-confirmed; director-verified live). `messageListVisibility`: show/hide. System labels enumerated (13 + "not exhaustive"). Limits: 5,000 user-creatable (Help) / 10,000 mailbox ceiling (API). Colors: closed ~98-value palette + "up to 100 custom colors" as background/text pairs (Help) — **a closed palette guaranteeing contrast, never a free picker**. Nesting via "Nest label under". Chips on rows — Gmail never tints rows.
- **Moov structural constraint:** Maildir's **26 durable keywords per mailbox** (measured, `internal/imap/metadata.go:52`; system-ish keywords consume from the same 26) makes Gmail's everything-is-a-label impossible. Consequence: **folders carry the organizational load** (unlimited), keywords are reserved for the few cross-cutting flags (starred, unread, a handful of user labels), and the label UI must be designed around that number — honestly, in the UI.

### 2.7 Keyboard ([/6594](https://support.google.com/mail/answer/6594) — full map in report, verified current)

Off by default (rationale unsourced — default state is a director arbitration for Moov). The complete Gmail map is the target vocabulary: actions (`x s e m ! # r a f z b ; : ] [ Shift+I/U _ + -`), chords (`* a/n/r/u/s/t`, `g i/s/b/t/d/a/l`, `g n/p`), application (`c d / . v l ?` ), compose (`p n Ctrl+Enter Ctrl+Shift+C/B/F Ctrl+K`), formatting. Custom shortcuts = Advanced opt-in, "one key can refer to only one action".

### 2.8 Vacation responder — CORE, exact anti-annoyance spec

Date range (starts 12:00 AM, ends 11:59 PM), subject, message, contacts-only option; **re-send only after 4 days** or when edited; **never replies to spam or mailing lists**; signature appended; banner with "End now" ([/25922](https://support.google.com/mail/answer/25922)). API: subject-or-body required, HTML sanitized server-side, html wins, `restrictToDomain` Workspace-only ([VacationSettings](https://developers.google.com/workspace/gmail/api/reference/rest/v1/VacationSettings)). **Maps 1:1 to Sieve `vacation` (RFC 5230 `:days 4`, `:subject`, `:addresses`) on Dovecot.**

### 2.9 Notifications — CORE, and a scope-changing fact

Exact modes: "New mail notifications on" / "Important mail notifications on" / "Mail notifications off" + sound picker ([/1075549](https://support.google.com/mail/answer/1075549)). **"You get email notifications … after you sign in to Gmail and open it in your browser."** — **Gmail web parity does NOT require background Web Push; tab-open Notification API over our existing SSE is the parity bar.** Web Push/VAPID is beyond-Gmail territory, not a parity gap.

### 2.10 Offline — CORE concept, Google-artifact implementation

Chrome-only, non-Incognito, **bookmark not install**; sync-depth setting ("how many days"; 7/30/90 presets unsourced); "Download attachments" toggle; offline you can "read, search, and reply"; **offline sends queue in an 'Outbox' folder**; attachments not previewable ([/1306849](https://support.google.com/mail/answer/1306849)). The Chrome coupling is ecosystem strategy → **sanctioned divergence candidate: standards-based SW + IndexedDB, browser-agnostic** (owner signature required, expected yes per ADR §6).

### 2.11 The rest (settings-adjacent behaviors)

Show original with full headers + copy-to-clipboard ([/29436](https://support.google.com/mail/answer/29436)) · storage bar (15 GB shared, counts Spam+Trash, [/9312312](https://support.google.com/mail/answer/9312312)) · Trash 30-day auto-clean (doubly sourced) · Spam 30-day auto-clean (in-product banner only — cite as such) · Templates: web-only, unrecoverable deletion, filter action "Send template" ([/14864208](https://support.google.com/mail/answer/14864208)) · Send-as: 99 addresses, verification link, **third-party send-as retired January 2027** — Gmail is abandoning exactly the use case a Mailcow webmail serves ([/22370](https://support.google.com/mail/answer/22370)) · Forwarding: verified addresses, spam excluded, 1-week security notice ([/10957](https://support.google.com/mail/answer/10957)) · Delegation: 10 personal / 1,000 Workspace, "can't chat or change your password" ([/138350](https://support.google.com/mail/answer/138350)).

## 3. The settings schema (from the Gmail API — the only machine-readable contract)

Google publishes no settings spec; the API reference supplies exact types and value domains. Full extraction in the research record; the classification that governs the plan:

**DIRECT (adopt as-is):** signature model (multiple named, per-identity, new-vs-reply defaults, 10k cap) · vacation (8 fields → Sieve) · reply-to · undo-send {5,10,20,30} · conversation view toggle · display language (RFC 3066) · images policy (always/ask — the toggle that gates our proxy) · keyboard on/off · default reply behavior · density/page-size/snippets/hover/stars/notifications (concepts direct, exact values ours where Google's are unsourced) · `labelListVisibility` incl. `labelShowIfUnread` · closed color palette.

**ADAPT (concept yes, mechanism ours):** labels → 26-keyword ceiling + A6 hybrid, folders carry the load · filters → Sieve (criteria map well; `query`/`negatedQuery` have no Sieve equivalent — restrict, don't fake) · forwarding → Sieve `redirect` + **the verification flow copied as security design** (token mail, pending→accepted) · blocked senders → Sieve recipe · quota → Mailcow API (`Quota/get` RFC 9425 as transport) · vacation `restrictToDomain` → own-domain Sieve check · delegation → Dovecot ACL, deferred · S/MIME portable, out of scope.

**N/A-GOOGLE (category errors — deliberately NOT built):** the whole IMAP/POP settings block (Dovecot IS the IMAP server; those settings paper over Gmail's web-store-vs-IMAP impedance mismatch — porting them imports Google's architectural debt) · Gmailify/POP-fetch · Chat/Meet · Add-ons · AMP dynamic email · smart-features AI set · Drive/Photos · confidential mode (Google escrow) · CSE · category classifiers.

## 4. The deliberate-omissions register and the Bulwark verdicts

### 4.1 Omissions/gates with their reasons (all cited in the research record)

1. **Images display-by-default is CONDITIONAL on the proxy** — "Gmail uses Google's secure proxy servers… senders can't use image loading to get information about your computer or location… can't set or read cookies"; suspicious mail → images withheld; "Ask before displaying" is framed as bandwidth, the proxy is the privacy mechanism. **Consequence: Moov's HMAC+anti-SSRF proxy (already built) is the precondition; default-display + suspicious-mail suppression is the Gmail-shape target.**
2. **Read receipts:** consumer Gmail refuses MDNs entirely; Workspace admin-gates them with mandatory per-recipient prompts. **Moov: never auto-answer `Disposition-Notification-To`, never request by default.**
3. **No link wrapping** — hover-to-verify depends on untouched URLs. **Moov: never rewrite links.**
4. **Smart features:** exactly 4 features behind one consent toggle ("you agree to let…"), **default OFF in EEA/JP/CH/UK**. Everything else in this canon is deterministic — **no AI is required for Gmail-class parity** (re-confirmed). Moov's IA phase goes behind an equivalent master consent toggle, opt-in.
5. **Filter minimalism:** label algebra + verified forward, no dates, no scripts, no per-filter auto-replies. A principled "no" for every exotic filter request.
6. **Keyboard off by default** (rationale unsourced) — default state is an arbitration.
7. **PWA/offline Chrome coupling** — business artifacts, the two sanctioned divergences.
8. **Removal discipline:** Inbox by Gmail killed (only snooze/Smart-Reply/nudges/priority-notifications/Smart-Compose migrated; bundles/pinning/reminders died); Basic HTML killed 2024. **Every epic must pass the "would Gmail migrate this?" test.**
9. Vacation 4-day throttle, hover = exactly 4 actions, auto-advance off, quiet web (no sounds beyond the notification setting), spam UI degrades affordances (banner + image suppression) instead of hiding mail, confidential-mode-style honesty about limits, 250k-email capacity honesty.

### 4.2 Bulwark features judged (the kill/adopt/flag list)

- **REJECT (fail the filter, with citable reasons):** sender favicons (decided) · debug tab · Spam Siege game · row tinting by tag · per-folder icons · attachment filename templates · post-export actions · sub-address delimiter *setting* (adopt the behavior from Dovecot's `recipient_delimiter`, reject the setting) · read-receipt request default toggle · plugins/sidebar-apps/admin-CSS (XSS-by-design; Gmail's model is governed sandboxed add-ons).
- **ADOPT Gmail's shape instead of Bulwark's:** label colors (presets + custom pairs ≤100 — Gmail is *more* capable than Bulwark's fixed 39) · `mailto:` protocol handler (+ manifest `protocol_handlers`, which Gmail can't have) · mobile swipe = Gmail's five-action per-direction vocabulary · settings persistence server-side (our PostgreSQL, never localStorage).
- **FLAG for owner arbitration (genuine divergence candidates):** settings search with sub-results (cheap, zero trust cost, Gmail lacks it) · TNEF/winmail.dat extraction (real value for the Outlook-heavy Mailcow audience vs parser attack surface; fits the E4 cascade+fuzzing discipline) · keyboard default ON vs Gmail's OFF · emails-per-page setting (moot with virtualization) · unified inbox (mobile-only pattern in Gmail; not MVP).
- **Bulwark mechanisms that survive as HOW** (05 §5.0, unchanged in value): JSON-Pointer escaping, `batched()` request-limits, `coalesceRefresh`, page-size merge cutoff, `retainedInViewIds`, `event.code` shortcuts, split draft destroy, no-placeholder-fallbacks, `RequestTimeoutError` never retried, `QuotedHtml` atomic node, srcDoc rebuild on unblock, DOMPurify data-URI/srcset re-checks, C0/CSS-escape URL normalization, single sanitization chokepoint, Sieve origin partitioning, client Message-ID, omit empty Cc, `recycleStaleSSE`, error boundaries per pane. Each enters the plan attached to the Gmail-mandated feature that triggers its bug class.

## 5. UNSOURCED register (nothing below founds a decision)

Gmail defaults for most settings (domains documented, defaults not) · plain-text compose mode as a documented row · snippets and button-labels settings rows (behaviorally real, no fetchable article) · web auto-advance option labels (semantics sourced from the **Android** article only) · max filters 1,000 · 500-attachment cap · max page size values · density labels on a fetchable page · snooze preset times · select-all-matching banner string and cap · undo toast duration · drafts autosave cadence · recipient-chip behavior · collapsed-message rules · offline 7/30/90 presets · keep/remove offline data labels · spam 30-day auto-delete (in-product banner is the ceiling of evidence) · "reply from same address" radios · delegate read/unread radios · filtered-mail importance override · phone country-code setting · keyboard-default rationale · settings-search absence rationale · unread favicon icon setting (community only) · TNEF non-extraction (community only) · Gmail-PWA negative (no install doc + community; unauthenticated manifest check impossible — **decision does not hinge on it**: our PWA divergence stands on browser-agnostic standards regardless) · `older:`/`newer:` operator synonyms · "Show in IMAP" per label (needs re-verification — live page didn't surface it this pass).

## 6. Corrections to `05-gmail-class-surface.md`

1. Inbox-types citation is [/186531](https://support.google.com/mail/answer/186531), not /18522 (content identical).
2. "Up to 500 attachments" → downgraded to UNSOURCED (absent from live /6584).
3. "Show in IMAP" per label → needs re-verification (was marked ✅).
4. Label limit: record **both** 5,000 (user-creatable, Help) and 10,000 (mailbox ceiling, API).
5. Send-as deprecation copy updated; live text wins.
6. `older:`/`newer:` unverified on the current /7190.
7. Forwarding has 2 copy dispositions, POP has 4 — never conflate.
8. **05 §3 (Moov inventory) confirmed accurate at HEAD `b99d934` (no code drift), with four omissions:** `Mailbox/query` unregistered (RFC 8621 §2.3) · `SearchSnippet/get` entirely absent · **the public deployment is missing** — Moov's own PWA is the shipped client on 4 production hostnames since 2026-08-26 (`deploy/Caddyfile.public`), Bulwark demoted to side-by-side reference · `env.example` is the de-facto (operator-only) settings surface.
9. **New finding, director-verified in code:** the `collapseThreads` refusal (`query.go:146-149`, "this server has no thread index yet") is **stale** — migration 0004 created `messages.thread_id` NOT NULL + trigger + index, and `thread.go:13-35` documents the gap as closed. Conversation-list collapsing is a store-repertoire decision away (S3 window discipline must bless the query shape), not a schema migration away.
10. PWA detail (supplementary audit, corrected by the adversarial pass): the reader is missing star/move/mark-unread **as props** (`runToggleFlag`/`runMove`/`runToggleRead` exist in `MailScreen` and are simply not passed down); **spam is different — no spam action kind exists anywhere in the PWA** (`web/src/mail/actions.ts` kinds: markRead/markUnread/flag/unflag/archive/delete/move), so the spam button is new-build UI+wiring, not prop plumbing; `ShortcutsDialog.tsx:133` still says "coming soon: e, #" though both work; no locale switcher (browser-detect only); persistent SSE death is silent (no disconnected indicator).

## 7. Design consequences (what the plan is built on)

1. **The proxy we already have is the unlock** for Gmail's image model: default-display + suspicious-mail suppression, "ask" as the setting's other pole.
2. **Filters are label algebra over Sieve** — criteria {from, to, subject, hasAttachment, size}, actions {±keyword/folder, verified forward, markRead, star, delete, never-spam}; no dates, no free-text query criterion (no Sieve equivalent — restricted honestly).
3. **Two trust invariants:** sender identity = initials (BIMI-only future); URLs untouched.
4. **MDN policy in one stroke:** never auto-answer, never request by default.
5. **Notifications parity = tab-open Notification API over existing SSE.** Web Push is beyond-Gmail, separately prioritized.
6. **PWA install + browser-agnostic offline are the two sanctioned divergences**, each recorded with its arbitration.
7. **Snooze, mute, schedule send are CORE** in Gmail — they enter the plan as first-class epics (the v2 plan deferred snooze/mute; Gmail-first reverses that).
8. **Conversation view is unblocked server-side** (correction 9) — the presentation split rules (subject change, >100, one-week window) are ours to implement Gmail-shaped.
9. **The autocomplete model is Gmail's own:** an address index auto-fed by sent mail ("Other contacts"), with the "I'll add contacts myself" opt-out — no contacts subsystem required for parity.
10. **All AI features sit behind one consent toggle, opt-in** — and none are needed for parity.
