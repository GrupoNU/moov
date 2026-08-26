# Moov Mail — the PWA

The product's own face: a React + TypeScript progressive web app that speaks
JMAP to Moov's server. Epics **P1** (foundations, branding, login), **P2**
(reading), **P2b** (the secure HTML renderer) and **P3** (writing and sending)
of [`docs/specs/L2-pwa.md`][spec] have landed.

The reading pane renders HTML through the three-layer pipeline in
`src/mail/html/`. P3 adds optimistic actions with rollback, multi-select, the
composer, drafts, attachments, send-with-undo and the identity signature.

[spec]: ../docs/specs/L2-pwa.md

```
npm install
npm run dev        # http://localhost:5173, proxying the API to the pilot
npm run typecheck  # tsc -b
npm run lint       # eslint, --max-warnings 0 (jsx-a11y at error level)
npm run test       # vitest
npm run build      # tsc -b && vite build
```

`npm run dev` proxies `/.well-known/jmap`, `/jmap*` and `/branding` to
`MOOV_DEV_API` (default `https://moov.atmosfera.cloud`) so the app is developed
against the same *same-origin* topology it ships in — the production Caddy
fronts the built assets and the API together, and CORS rules that only appear
in one of the two environments are a class of bug worth designing out.

---

## What P1 contains

| Area | Where |
|---|---|
| Design tokens (W-A2) | `src/styles/tokens.css` |
| Branding client (W-A1) | `src/branding/` |
| Typed JMAP client (W-A3) | `src/api/jmap.ts` |
| Error taxonomy | `src/api/errors.ts`, `src/api/errorMessages.ts` |
| String table (i18n) | `src/i18n/strings.ts` |
| Session persistence | `src/auth/session.ts` |
| Login screen | `src/screens/login/` |
| Authenticated shell | `src/screens/shell/` |

The server half lives in `internal/jmaphttp/branding.go` (the public
`GET /branding`) and `cmd/moovctl/branding.go` (the CLI that writes it).

---

## Design rationale

### The token system: three layers, and only one is brandable

Everything visual resolves through `src/styles/tokens.css`, which is built in
three layers:

1. **Seeds** (`--brand-*`) — four colours. The *only* values a customer
   controls, and the only ones JavaScript ever writes (`applyBranding` sets
   exactly four properties; a test asserts `style.length === 4`).
2. **Semantic tokens** (`--color-*`, `--surface-*`, `--text-*`) — what
   components actually consume. Derived from the seeds *in CSS* with
   `color-mix()`.
3. **Scales** (`--space-*`, `--radius-*`, `--text-*`, `--shadow-*`) — the
   product's own craft. Not brandable, because a customer changing them could
   only make the app worse.

The payoff is that four values restyle the entire application, in both themes,
with no JavaScript computing a palette. That is what the Areacorp screenshot
demonstrates: one document, and the gradient, the button, the focus ring, the
selection colour and the browser tab all move together.

Dark mode redefines **only layer 2**. A customer supplies one accent and gets a
coherent light *and* dark app — there is no second palette to configure, and
therefore no way to configure only half of one. The accent is lifted toward
white in dark mode (`color-mix(... 72%, #ffffff)`) because a mid-tone that
reads well on white drops to ~3:1 on near-black, below AA for text.

Three theme states are handled explicitly: an explicit choice stamps
`data-theme`, and "system" *removes* the attribute so `prefers-color-scheme`
decides. The dark media block is guarded with `:not([data-theme="light"])` so
an explicit light choice beats a dark OS, and every dark token is defined in
**both** the media block and the attribute block — a token defined only inside
a media query has no value when the attribute is what applies, which is how
theme toggles end up working in one direction only.

### Why the layout works

**Split screen** (product decision P2). Moov serves *one company per domain*,
which is precisely the case where a split screen is canonical: the image means
something to everyone who sees it. Google's single column is a consequence of
serving billions of unrelated users; copying it would discard the one
contextual advantage this product has.

The brand half gets the *larger* share (`1.1fr` vs `1fr`). The form column caps
its card at `26rem`, so without the bias the leftover whitespace makes the split
look accidental rather than composed. ~26rem is also about as wide as a login
form should ever be — an email field the width of a desktop screen makes a typo
genuinely hard to spot.

**The unbranded panel had to be good, not empty.** Most installations never
configure a brand, so the default is what most people will see. It is a
corner-to-corner gradient plus two pure-CSS layers — a soft radial "aurora" and
a fine grid faded out by a mask. No request, no bytes, and it reads as designed
rather than as a placeholder waiting for an image.

**A scrim sits under the panel's content** so the contrast of the mark and
tagline is a *fixed, testable* value rather than a property of whatever
photograph a customer uploaded.

### The mobile collapse — and why it is a band, not a background

P2 requires the split to collapse and the form never to fall below the fold.
The obvious option is to turn the imagery into a background behind the form
with an overlay. **This app does not do that**, deliberately:

> A background image makes text contrast a property of the customer's upload.
> No overlay opacity is safe for every image — a light photo defeats dark text,
> a dark one defeats light text — so the accessibility of the login screen would
> depend on an asset we do not control.

Instead the panel becomes a short **brand band** across the top: full-bleed
gradient, the mark, nothing else. The brand stays visible, the form sits on a
plain surface where contrast is fixed and measurable, and because the band is
`flex: none`, the form always owns the remaining viewport.

There are **two** collapse triggers, and the second one was added because a
measurement caught a failure:

- `max-width: 900px` — the ordinary phone/tablet collapse.
- `max-height: 560px` — **height**, not width. At 740×360 (a phone in
  landscape) the two-column grid survived and the submit button sat **125px
  below the fold**. A width breakpoint could never have fixed it. The short
  block collapses the grid, drops the duplicate compact mark and the
  subheading, and tightens spacing — but deliberately does **not** shrink the
  inputs or the button, because trading touch-target size for fold position
  fixes one accessibility problem by creating another.

Verified across 9 viewports × 2 themes (320×568 through 1440×900): the submit
button is above the fold and nothing scrolls horizontally, everywhere.

### The two-step seam

P1 ships a **single-step** form (email + password together). Identity-first
routing exists to choose between SSO providers, passkeys and multiple accounts;
Moov has none of those — auth is an IMAP `LOGIN` and the domain already
identifies the company — so a second step would be friction with no function.
Fastmail and Superhuman, the actual benchmark, are single-step.

The component is nonetheless built to split. The seam is documented inline in
`LoginScreen.tsx`, and it is a real seam rather than a comment:

- the two fields are already **independent components** with their own labels,
  validation and refs (`PasswordField` is a separate, `forwardRef` component);
- the submit handler validates them **in sequence** rather than as one blob, so
  the email branch already stands alone;
- nothing in the layout, branding or error handling knows how many fields
  exist.

Introducing two steps is: add a `step` state, render the email field alone with
a *Continue* button, then render the password field with the email read-only.
The form element, the error plumbing, the brand panel and every style are
reused unchanged.

### Actionable errors — the pilot's lesson, made structural

The pilot's failure: our server answers an unprovisioned mailbox with a precise
403 — *"this mailbox authenticated correctly but is not provisioned in Moov; an
administrator must add it"* — and Bulwark rendered **"an error occurred"**. The
information was on the wire and the client threw it away.

Three mechanisms make that impossible here:

1. `ApiErrorKind` is a **closed union**. `kindForStatus` maps each status our
   server actually sends to its own member; 401 and 403 can never collapse.
2. `messageForError` is an **exhaustive switch with no `default` branch**.
   Adding a kind without writing its message is a *compile error*.
3. `unknown` is a real kind with real copy ("try again; contact your
   administrator if it persists"), not a fallback — honest when we genuinely do
   not know, and still never "an error occurred".

Each message follows one shape: **say what happened, then say what to do.** The
"contact your administrator" hint is offered *only* for `not-provisioned`,
because that is the one state a user cannot resolve alone; showing it everywhere
would train people to ignore it exactly where it matters. `Retry-After` is read
from the 429 and rendered as a number, turning "wait" into an instruction.

A test asserts that every kind, in every locale, produces a non-generic title
and a body longer than 20 characters.

### Accessibility

Held as an acceptance criterion, not a review item:

- **`jsx-a11y` at error level** with `--max-warnings 0`, so labels, roles and
  keyboard handlers cannot regress silently.
- **Contrast is measured, not eyeballed.** A unit test pins the seed pairings
  from the WCAG formula; a Playwright run measures the *computed* styles in a
  real browser (resolving `color-mix()` through a canvas) — 10/10 pairs clear
  AA in both themes, with the tightest at 5.37:1.
- **`autoFocus` is not used.** It fires during initial render, before a screen
  reader has announced the page. Focus is set in an effect instead, and is
  *skipped* when an error notice is present so its `role="alert"` announcement
  is not cut short.
- **The focus ring is an `outline`**, not a `box-shadow`: it follows the
  element's shape, survives forced-colors mode, and cannot be clipped by an
  ancestor's overflow. Nothing sets `outline: none`.
- **Live regions are always in the DOM**, never inserted alongside their text —
  a region that appears at the same moment as its content is frequently not
  announced at all.
- The password toggle is a `<button type="button">` (a bare `<button>` in a
  `<form>` submits — looking at your password would sign you in), carries
  `aria-pressed`, and its accessible name states what the *next* press does.
- `prefers-reduced-motion` neutralises every animation; `forced-colors` gets
  explicit system-colour rules.

---

## How a client's brand changes it

The server resolves branding by **request Host** — never by anything the user
types, which would make the endpoint an oracle for "which domains exist here".
A host with no configuration gets Moov's defaults, indistinguishable from a host
deliberately branded to look like Moov.

```bash
moovctl branding set \
  -host mail.acme.com \
  -name "Acme Mail" \
  -tagline "Correo corporativo" \
  -support-url "mailto:it@acme.com" \
  -logo ./acme-logo.png \
  -splash ./acme-office.jpg \
  -color-primary "#0f7b6c" \
  -color-on-primary "#ffffff"
```

`set` is **incremental**: flags you do not pass keep their current value, so
adjusting one colour cannot silently delete a customer's logo.

The running server picks the change up within a minute — no restart.

**SVG is refused**, by both the CLI and the server. An SVG is an XML document
that can carry `<script>`, and these assets are served from the origin the login
page runs on; accepting one would hand any customer who can upload a logo a
stored-XSS primitive on the page that receives passwords. The server validates
by **content**, not extension — an SVG named `logo.png` is rejected too — and
sniffs only PNG/JPEG/WebP/GIF, never `text/html`.

The client re-validates everything the server sends: colours must be hex
literals before they reach `setProperty`, asset URLs must be same-origin
root-relative (a customer's absolute URL would be a tracking pixel on the login
page), and `supportUrl` is restricted to `https`/`http`/`mailto`.

If `/branding` is slow, broken, or returns garbage, the app silently uses Moov's
brand. **There is no state in which the login screen fails to render.**

---

## Testing

61 unit tests (`npm run test`) cover the branding merge and its hostile inputs,
the full error taxonomy in both locales, and the login screen's behaviour —
labels, autocomplete tokens, keyboard order, the toggle, validation, focus
management, and every error state reaching the DOM with the right words.

What a jsdom test cannot honestly verify is verified with Playwright against
the **live pilot**: a real sign-in with `moov-test@atmosfera.cloud`, session
persistence across reload, sign-out clearing the credential, real 401 copy from
a real wrong password, computed contrast, keyboard operation with a painted
focus ring, and the responsive fold check that caught the landscape bug.

Statuses the live server cannot be made to produce without changing its state
(403 not-provisioned, 429) are exercised against a local stand-in whose bodies
are copied **verbatim** from `internal/jmaphttp/auth.go`, so the strings under
test are the strings the server sends.

---

## What P2 contains

| Area | Where |
|---|---|
| URL router (routes, parsing, history) | `src/router/` |
| JMAP Mail types (verified against the live server) | `src/mail/types.ts` |
| Mail API calls, batched with back-references | `src/mail/api.ts` |
| Mailbox ordering, nesting, badges | `src/mail/mailboxes.ts` |
| Thread grouping | `src/mail/threading.ts` |
| Virtual-list windowing maths | `src/mail/windowing.ts` |
| Search normalisation, debounce, refusals | `src/mail/search.ts` |
| Date/size/initials formatting | `src/mail/format.ts` |
| Keyboard map (Gmail vocabulary) | `src/keyboard/shortcuts.ts` |
| The screens | `src/screens/mail/` |

`src/screens/shell/AppShell.tsx` (P1's placeholder landing) is no longer routed
to; `MailScreen` replaces it. It is left in the tree because it is still the
honest reference for the shell's proportions.

---

## The HTML-renderer seam

> **Status (W-A4, landed):** this seam is now FILLED. `HtmlBodyPlaceholder` is
> gone; `MessageBody` renders HTML through `SecureHtmlBody` and the three-layer
> pipeline in `src/mail/html/` (`policy.ts` → `sanitize.ts` → `srcdoc.ts`), and
> `fetchMessageDetail` sets `fetchHTMLBodyValues: true`. The server grew an
> HMAC image proxy (`internal/jmaphttp/imgproxy.go`). The contract below is kept
> as the record of what the epic was asked to satisfy; see those files' header
> comments for what was built.

**The single file that was changed is `src/screens/mail/MessageBody.tsx`.** Its
header comment is the normative contract; this section is the summary.

### What P2 deliberately does not do

P2 renders `text/plain` bodies and nothing else. There is **no
`dangerouslySetInnerHTML` anywhere in this codebase, no `<iframe>`, and no HTML
string is ever handed to the DOM.** `fetchMessageDetail` does not even ask the
server for HTML — it sets `fetchTextBodyValues: true` and omits
`fetchHTMLBodyValues` — so hostile markup never enters the client's memory.

That is a security boundary, not an unfinished feature. Rendering mail HTML is
the largest attack surface in a mail client (ADR §5, L2-pwa risk 2), and a
half-safe renderer is worse than none: it ships a product that looks like it
works while leaking the session of everyone who opens a crafted message.

When a message has an HTML part and no usable text part, the pane renders
`HtmlBodyPlaceholder` — one greppable symbol — which says plainly that a
formatted version exists and is not being shown.

### The contract for the renderer epic

Replace **only** the `HtmlBodyPlaceholder` branch. The metadata header, the
attachment list, the thread context, the download path and every style are
finished and should not need to change.

```ts
interface SecureHtmlBodyProps {
  /** The raw, UNTRUSTED bodyValue for the text/html part. */
  html: string;
  /** Start with remote images blocked. */
  blockRemoteImages: boolean;
  /** Called when the user explicitly opts in to loading them. */
  onShowRemoteImages: () => void;
}
```

Requirements, none of them optional (ADR §5's three layers):

1. The server sanitises (bluemonday) — already true for what it stores.
2. The client sanitises with **DOMPurify** before the string reaches the DOM.
3. The result renders in `<iframe sandbox>` **without `allow-scripts` and
   without `allow-same-origin`** — granting both is equivalent to no sandbox —
   carrying CSP `default-src 'none'`.
4. Remote images blocked by default, loaded only on an explicit user action and
   only through the HMAC image proxy (a direct fetch leaks the reader's IP to
   the sender).
5. `target="_blank"` links also carry `rel="noopener noreferrer"`.

**Where to turn it on:** `fetchMessageDetail` in `src/mail/api.ts`. Adding
`fetchHTMLBodyValues: true` is the deliberate switch that starts delivering HTML
to the client; flip it in the same change that lands the renderer, never before.

Two facts the renderer will need:

- `bodyValues` is keyed by **`partId`, which is a decimal index as a string**
  (`"0"`, `"2"`) — not a content id. `MessageBody`'s `valueFor` already does
  this lookup.
- **Per-part `blobId` is real now** (gap 5 closed server-side, 2026-08-26):
  every leaf part advertises a derived id (`<messageBlob>-<partIndex>`) the
  download route serves, so `cid:` inline images can be fetched individually —
  with a `blob`-scoped token in the query string, since an `<img>` carries no
  header (`src/api/tokens.ts` holds the token, `withAccessToken` attaches it).

---

## Design rationale (P2)

### The list is virtualized, and the rows are a fixed height

One scroll container of the full content height, two spacer divs, and only the
visible slice of rows between them, positioned with `transform: translateY`.
The maths lives in `src/mail/windowing.ts` and is unit-tested there, including
the invariant that **`paddingTop + rendered + paddingBottom` always equals the
total height** — the property that keeps the scrollbar honest.

Rows are a fixed 72px. Variable heights require measuring after render, which
makes the scrollbar change length as you scroll and turns `scrollTop` to index
into a search. Gmail, Fastmail and Superhuman all use fixed rows for the same
reason. `ROW_HEIGHT` and the CSS `--row-height` are **pinned together by a
test** (`src/mail/rowHeight.test.ts`), because a silent divergence between them
is a list that drifts away from its own scrollbar.

Scroll position resets on a change of **list**, not of contents: a refresh that
adds a message must not jump you to the top, while switching folders must not
leave you scrolled to row 400 of a folder with six. `listKey` distinguishes the
two, and the reset runs in `useLayoutEffect` so no frame paints at the stale
offset.

Two bugs the unit tests caught before a browser ever ran: a negative
`scrollTop` (real — iOS and macOS rubber-band) producing negative indices, and
an **inverted range** when the list shrinks under a stale scroll position,
which rendered an empty list at a valid offset and looked exactly like a data
failure.

### Accessibility: a grid, with the true row numbers

The list is a `role="grid"` whose rows carry `role="row"` and cells
`role="gridcell"`, so a screen reader announces "sender, subject, date" as one
row instead of three unrelated strings.

Virtualization breaks the assumption that every row is in the DOM, so
**`aria-rowcount` carries the true total and each row carries its true
`aria-rowindex`**. Without them a screen reader says "row 3 of 20" while the
user is on message 400 — the most common accessibility failure in virtualized
lists. Verified in the browser: 17 rows rendered, `aria-rowcount="31"`.

Focus uses a **roving tabindex** (exactly one row is tabbable), so Tab moves
past the list rather than through 200 rows. The sidebar is a `role="tree"` with
`aria-level` and `aria-expanded`, and every folder is a real `<a href>` — so
middle-click and Cmd-click open tabs, which a `div` with an `onClick` silently
takes away.

### The keyboard never breaks the browser

`resolveShortcut` is a pure function of `(event, chordState)`, so the refusals —
the hard part — are enumerable in tests rather than buried in a handler:

- **any event carrying Ctrl, Meta or Alt is ignored outright**, so Ctrl+R
  reloads and Cmd+K reaches the browser;
- nothing fires while focus is in an `input`, `textarea`, `select` or a
  `contentEditable` (the P3 composer);
- `Escape` is handled *before* the typing guard, because it is how you leave a
  text field;
- `preventDefault()` is called only *after* an action has been resolved, so
  unbound keys reach the browser untouched.

The `g` prefix expires after 1.2 s so a stray press cannot swallow the next
keystroke. `SHORTCUT_HELP` is pinned to the resolver by a test: a shortcut that
is not discoverable in the `?` sheet fails the build.

### Search says what the server can and cannot do

Debounced at 180 ms — not for our latency (the server answers in tens of
milliseconds) but because `maxConcurrentRequests` is **8, enforced with a
429**, and an un-debounced 12-character query is 12 requests. `Enter` flushes
rather than waiting.

The server's search repertoire is deliberately bounded (S3: unbounded work is
what sinks the instance under concurrency), so **`unsupportedFilter` is a normal
answer, not a failure**. It is mapped to a *refusal* with its own banner — "this
server cannot answer that search", plus what search does cover. The one thing it
must never become is a silent empty list, which would be a lie: results may
exist.

---

## Measured against the live pilot

`moov-test@atmosfera.cloud` only. The mailbox holds 11 messages, too few to
prove virtualization, so it was seeded over IMAP with **620 synthetic messages
(626 in INBOX, 92 threads, threads up to 14 messages)** and **purged afterwards
— INBOX is back to its original 6, Trash to 0**, verified over IMAP.

### The latency finding: measure the baseline, not just the operation

Naively, every operation looked like ~1 s. The number that explains it is
**`Core/echo` — a call that does no work — costing 528 ms** on a warm
connection from Argentina to the Frankfurt VPS. A cold connection costs ~700 ms
before the server does anything (TCP 178 ms, TLS handshake 527 ms).

Subtracting that baseline gives the server's real cost:

| Operation | Warm total | Minus echo baseline | ADR §6 bar |
|---|---|---|---|
| `Core/echo` (does nothing) | 528 ms | — | — |
| Folder list, 200 rows (`Email/query` + `Email/get`) | 548 ms | **~20 ms** | <100 ms ✅ |
| Search, 200 rows | 562 ms | **~34 ms** | <100 ms ✅ |
| `Mailbox/get` (all folders) | 540 ms | **~12 ms** | <100 ms ✅ |
| Message detail (`Email/get` + `Thread/get`) | 583 ms | **~54 ms** | <100 ms ✅ |

**The product is inside the bar; the transatlantic path is not.** On the LAN the
pilot actually serves, the wall-clock figures are the right-hand column. This is
worth stating precisely rather than reporting the 1 s number, which would be
true and useless.

Client-side rendering was verified separately in the browser: the window slides
correctly while scrolling (rows 1-17 to 15-31) and **never renders the whole
list**.

### Verified in a real browser (Playwright, live pilot)

Deep links (`/mail/inbox`, `/mail/inbox/:id`, `/search?q=`) restore state,
including through the login screen — an unauthenticated deep link lands exactly
where it pointed after signing in. Browser back/forward walks the real trail
(`inbox` to `sent` to `archive`, then back, back, forward) and the sidebar
re-renders to match. Folder navigation, threading (a 14-message conversation
with its participant list), the `?` sheet (a true `:modal` dialog with focus
trapped inside), keyboard `j`/`Enter`, the empty state and both themes were all
exercised.

**Contrast is measured, not eyeballed**, on real rendered elements with
`color-mix()` resolved through a canvas: **16/16 pairs clear AA in both
themes**, tightest 5.37:1.

**Console is free of errors from this app.** The only errors present are the
`/branding` 404 described below, which is a deployment gap and not the client's.

---

## Server and deployment gaps found

Recorded for the director to schedule. The standing playbook applies: closed
server-side with a test, never worked around with a client hack.

1. **`Email/query` cannot page past 200 rows — the folder view is capped.** The
   server fetches at most `DefaultSearchWindow = 200` matches and slices
   `position` out of *that* window. Verified against a 626-message INBOX:
   `position: 200` and `position: 400` both return **zero ids**. Worse, the
   obvious cursor does not work either: `before` is applied **in Go, after the
   SQL `LIMIT`** (`internal/jmap/mail/adapter_query.go`), so it can only shrink
   the same window — a `before`-cursor walk returned 78 ids that were all
   already in page 1. The server's own comment names the fix
   (`SearchQuery.Until *time.Time`, so the bound is index-served). Until then
   **messages 201+ of a folder are unreachable**, and the client says so
   (`list.truncated`) rather than pretending 200 is all of them.

2. **`GET /branding` is shadowed at the pilot by a Next.js app.** It answers
   `307` to `/en/branding` with `Set-Cookie: NEXT_LOCALE`, and that path 404s
   with `X-Powered-By: Next.js`. W-A1's endpoint never reaches `moovd`, so every
   install behind that host silently gets Moov's default brand. The JMAP paths
   are unaffected (401 challenges correctly). This is a routing/deployment fix,
   not a code one. P1's design degrades correctly, which is why it presents as
   two console 404s rather than a broken page.

3. **CLOSED (2026-08-26): the Session's `downloadUrl` template names the
   parameter the handler reads.** It advertised `?accept={type}` while the
   handler read `type`; the template now says `?type={type}`
   (`internal/jmaphttp/session.go`). `downloadUrlFor` still normalises the
   query client-side, so it also works against an older server.

4. **CLOSED (2026-08-26): scoped short-lived tokens open both routes to the
   browser's native primitives.** The server mints single-scope, account-bound
   tokens at `POST /jmap/token` (10-minute TTL, revoked at sign-out via
   `POST /jmap/token/revoke`, refused everywhere but their own route — a push
   token at `/jmap/api` is a 401, pinned by test). `EventSource` connects with
   `?access_token=` (scope `push`); `<a download>` uses the same form with
   scope `blob`. Client side: `src/api/tokens.ts` (mint/refresh/revoke
   lifecycle), `src/mail/push.ts` (the stream), wired in `MailScreen`. The
   full threat model is in `internal/jmaphttp/token.go`.

5. **CLOSED (2026-08-26): per-part `blobId` is served.** Every leaf part now
   advertises a derived id (`<messageBlobSha256>-<partIndex>`,
   `internal/jmap/mail/partblob.go`) that the download route serves by
   re-parsing the message blob on demand — same ownership rule, same parser
   limits as `bodyValues`. The reading pane renders each attachment as a
   native `<a download>` with a `blob` token. Residual: `message/rfc822`
   parts keep a null `blobId` (the parser does not retain their raw bytes).

6. **Expunges are not reconciled into the store (observed, unresolved).** After
   moving the 620 seeded messages out of INBOX and expunging them, IMAP reports
   `INBOX = 6` and `Trash = 0` — but Moov's store still reports
   `INBOX.totalEmails = 626` and `Email/query` **still returns the deleted
   messages**, stable across roughly four minutes of polling. The Trash side
   reconciled (620 to 0); the INBOX side did not. Reproducible with a bulk
   `UID MOVE` + `EXPUNGE`. Not a client issue — worth a sync-engine look, since
   the store is meant to be a reconstructible cache of Dovecot.

Already known and unchanged: `EmailSubmission/query` is unregistered;
`collapseThreads`, `OR`/`NOT`, `hasAttachment` and a bare `hasKeyword` are
refused with `unsupportedFilter`; `canCalculateChanges` is always `false`.

---

## Two client bugs the browser caught that jsdom could not

Worth recording because both argue for the Playwright step existing at all.

**The Session's absolute URLs broke CORS.** `apiUrl` is
`https://moov.atmosfera.cloud/jmap/api`, and using it verbatim works in
production — where the app is served from that origin — and fails everywhere
else. It presented as a *half*-working app: the message list loaded (its request
went out before the Session resolved) and opening a message did not. The client
now honours the server's **path** and always uses **our** origin, which also
means a response can never redirect Basic credentials to another host. Pinned by
`src/api/sameOrigin.test.ts`.

**`new URL().pathname` percent-encodes URI-Template braces.** `{accountId}`
comes back as `%7BaccountId%7D`, so the later `.replace("{accountId}", ...)`
matched nothing and shipped the placeholder to the server. Caught by the test
written for the fix above.

---

## Testing

**203 unit tests.** P2 adds coverage for the router (including a format-then-
parse round-trip over every route the app can construct), mailbox ordering and
nesting, thread grouping, the windowing maths, the keyboard map's refusals, the
search debounce, and the same-origin rule.

Three of them were written because the test failed first and the code was wrong:
`/mail//e1` parsed as mailbox `e1` (an interior empty segment was being filtered
away, sliding the message id into the mailbox slot), the inverted window range,
and the two browser-caught bugs above.

---

## Notes for the next epics

### For the HTML-renderer epic (Fable)

- The seam is `src/screens/mail/MessageBody.tsx`; the contract is above and in
  the file's header comment. `HtmlBodyPlaceholder` is the only symbol to delete.
- Turn HTML on in **one** place: `fetchHTMLBodyValues` in `fetchMessageDetail`
  (`src/mail/api.ts`). Nothing else needs to change to start receiving it.
- `bodyValues` keys are decimal part indices as strings; `valueFor` in
  `MessageBody.tsx` already resolves them.
- Body-part `blobId` is always `null` (gap 5), so inline `cid:` images have no
  source today.
- The message body is capped at `68ch` in `MessageBody.module.css`; an iframe is
  a separate document and inherits none of these styles, which is part of the
  isolation rather than an oversight.
- A parse-failed message (E4's cascade ends at "raw blob") arrives with empty
  `textBody`/`htmlBody` and is already handled with its own notice plus the
  download — do not treat it as an error state.


---

## What P3 contains

| Area | Where |
|---|---|
| Optimistic patches + rollback (pure) | `src/mail/actions.ts` |
| The optimistic controller (effects) | `src/screens/mail/useMessageActions.ts` |
| Multi-select with shift/ctrl ranges | `src/mail/selection.ts` |
| Address parsing, validation, chips | `src/mail/addresses.ts` |
| Reply/reply-all/forward + quoting | `src/mail/quoting.ts` |
| Composer initial state per intent | `src/screens/compose/composerState.ts` |
| The write API (`/set`, upload, submission, identity) | `src/mail/write.ts` |
| Rich-text commands | `src/mail/richtext.ts` |
| Autosave debounce + undo countdown | `src/mail/drafts.ts` |
| The composer UI | `src/screens/compose/` |
| The bulk action bar and move menu | `src/screens/mail/ActionBar.tsx` |
| Shared folder labels | `src/screens/mail/mailboxLabels.ts` |

## Design rationale (P3)

### Optimistic updates: a patch overlay, never a snapshot

ADR §6 asks for actions under 100 ms *perceived*. The server is fast — W1
measured flags at 19-44 ms — but the pilot is served across the Atlantic,
where the round trip alone is ~530 ms (P2's `Core/echo` baseline). No server
can win that. Only the client can, by painting first.

That makes **rollback** the load-bearing part, and rollback is where optimistic
UIs fail. Two designs were rejected:

- **Snapshot the list, restore it on failure.** This also undoes everything
  that landed in between — a message that arrived by SSE, another action that
  succeeded. Wrong by construction.
- **Flip the patch back.** Restoring `seen: false` is only correct if the
  message was genuinely unread before. Marking an already-read message as read
  and then failing would leave it *unread*, corrupting state the user never
  touched.

So `planAction` captures, from ONE snapshot, both the forward patch and the
**inverse derived from each message's actual prior state**. `MailScreen` keeps
the server's list as the single source of truth and overlays a
`Map<id, patch>`; `applyOverlay` is pure and unit-tested, including the
ordering properties.

On success the patch is **dropped**, not kept — the server's data now says what
the patch was pretending, and holding it would mask a later legitimate change
arriving by SSE. The exception is a removal, which stays until the refetch so
an archived row does not flash back for a moment.

Because RFC 8620 §5.3 gives per-record errors, a batch can half succeed: the
half that worked is left alone and only the failed ids are restored, each
reported with **the server's own `description`**. Never a silent revert.

**Measured in a real browser with the network delayed to 600 ms** (roughly the
pilot's real round trip): flagging three messages painted in **10 ms**, and
archiving — which removes the row — in **11 ms**. Both clear the ADR §6 bar
with two orders of magnitude to spare, and both would be ~600 ms without the
overlay.

### Delete says which of the two things it is about to do

Server arbitration W-A2 makes `Email/set destroy` a **move to Trash** unless
the message is already in Trash, in which case it is a real expunge. The client
does **not** re-implement that rule — it would drift — but it does have to say
which one is happening: the button reads *"Move to Trash"* or *"Delete
permanently"*, and only the irreversible case is styled as destructive and
asks for confirmation. A confirm on every delete trains people to dismiss it,
which is how the one that mattered gets dismissed too.

### The composer's HTML: no editor framework, and why

**No dependency was added.** The bundle is 314 kB (102 kB gzipped) and the only
runtime dependencies remain React and DOMPurify.

The bar the composer has to clear is bold, italic, underline, lists and links —
five commands, all implemented natively by `document.execCommand`. The
alternatives each cost more than they are worth:

- **ProseMirror/TipTap** (~130 kB) model a document as a schema-validated tree
  and re-render the DOM from it. That is right for a collaborative editor and
  wrong for mail, whose output must be *mail HTML* — inline styles,
  `<blockquote type="cite">`, and whatever markup the quoted original brought.
  All schema-foreign, so a quoted reply is either normalised (destroying the
  original) or needs a custom schema plus node views — more bespoke code than
  what is written here, on top of the 130 kB.
- **Quill** (~110 kB) owns its own Delta format; round-tripping a quoted
  message through it is lossy by construction.
- **Slate/Lexical** are frameworks for *building* editors. The work would still
  have to be written, in their idiom.

`execCommand` is deprecated on paper and has **no successor** — the Editing API
meant to replace it was abandoned, and every browser still implements it. The
lint exemption is scoped to `src/mail/richtext.ts` alone and argued in the
file, not switched off globally. `styleWithCSS` is turned **off** so commands
emit `<b>` rather than `<span style="font-weight:bold">`: semantic tags survive
other clients' sanitizers, and Outlook in particular strips enough CSS that a
styled-span "bold" arrives as plain text. Verified in a real browser: bolding a
selection produced `Cuerpo <b>en negrita</b>`.

### Compose output is untrusted input to the next reader

A reply quotes the original message's HTML, and that HTML came off the wire. So
the rich surface sanitizes through the **P2b pipeline twice**:

- **In**, before the quoted HTML is ever assigned to `innerHTML`. Assigning
  unsanitized markup to a live `contentEditable` in the app's own origin is a
  stored XSS with full session access — strictly *worse* than the reading
  pane's iframe, which at least has an opaque origin and `default-src 'none'`.
- **Out**, on every change. The surface is a live DOM the user edits and the
  browser mutates; what comes out is not what went in.

**Paste is intercepted** and re-inserted as plain text: a native paste inserts
the clipboard's HTML flavour, which is whatever the source page put there.
Losing formatting on paste is a real cost; authoring attacker-chosen markup
into a message the user sends under their own name is a worse one. Link URLs
are validated to http/https/mailto **before** insertion rather than relying on
the sanitizer downstream — a composer that authors hostile markup and trusts
the next layer to remove it is building a defect on purpose. Attribution lines
are escaped, so a display name of `<img onerror=...>` cannot inject markup
(pinned by a test).

### Sending: one request, and one message

The draft creation and the submission ride in **one** request, per RFC 8621
§7.5's canonical flow (`emailId: "#draft"`). That means the draft cannot exist
without its submission, the move to Sent is the server's atomic business via
`onSuccessUpdateEmail`, and the whole send costs one round trip.

**The double-send guard is a `ref`, not state.** `setState` is asynchronous, so
two clicks in the same tick both read the old value — which is exactly how
double-send bugs ship. Verified in a real browser: a genuine double-click
produced **exactly one `EmailSubmission/set create` and one draft create**.

The undo countdown is derived from the server's own `sendAt`, never a hardcoded
10 — the server clamps its window to 5-30 s per account, so a hardcoded number
would either offer undo after the mail left (a lie) or stop offering it while
the server still would. Verified in a browser: the banner opened at
`Enviando en 10s` and ticked to `8s` over 2.2 s.

**`cannotUnsend` is surfaced, not swallowed.** It is a true statement — the
mail is going out — and a user who believes a send was canceled and later finds
it in Sent has been lied to. Cancel sends the RFC's own spelling, verified on
the wire: `{"sub1": {"undoStatus": "canceled"}}`.

### Drafts: create-then-destroy, because a message is immutable

RFC 8621 §4.6 makes every Email property except `keywords` and `mailboxIds`
immutable — a message IS its bytes. Editing a draft is therefore a new message
plus removal of the old one, in **one request, create first**, so a failed
create leaves the previous revision intact.

Autosave is a debounce **with a maximum wait**. A pure debounce never fires
during continuous typing: someone composing for four minutes has nothing saved
when the tab crashes. The max-wait converts it into a "save at least every 30 s"
guarantee. Closing the composer — by button or by Escape — **flushes** first:
closing must never lose a draft. An empty composer saves nothing, so opening
and closing does not litter Drafts.

### Attachments: XHR, because fetch reports no upload progress

`fetch` has no upload-progress event; the Streams workaround needs HTTP/2 and
is unsupported in Safari. For a 20 MB attachment a progress bar is the
difference between "working" and "frozen", so this one call uses
`XMLHttpRequest` and everything else in the app uses fetch.

The size gate reads **`maxSizeUpload` from the session** (J1's declared ==
applied rule), never a hardcoded 50 MB — a hardcoded client breaks the day an
operator lowers the limit and refuses files the server would take the day one
raises it. A **failed attachment stays visible**, marked, carrying the server's
own problem detail; dropping it would let the user click Send believing it was
attached.

### Accessibility

- The composer is a real `<dialog>` opened with `showModal()`, so the browser
  owns the focus trap and the top-layer stacking. Verified in a browser:
  `dialog.open === true` and `:modal` matches, with focus landing inside.
- `autoFocus` is used in exactly three places, each a WAI-ARIA APG requirement
  (dialog and menu patterns must move focus inside on open) rather than the
  load-time focus theft the lint rule exists to prevent. Each is disabled
  individually with its reason.
- Chips are a real list with a per-chip `<button>` whose accessible name says
  **which** address it removes, not a bare "Remove" repeated five times.
  Backspace on an empty input removes the last chip.
- **A real a11y bug was caught by a test**: the chip `<ul>` carried the same
  `aria-label` as its input, making "the To field" ambiguous to a screen reader
  (and to any test looking it up by name). The list's label was removed; the
  `<label>` names the input and the count is announced by a live region.
- The row checkbox and the row itself are separate controls with separate
  names; a click on the box does not also open the message.
- Toolbar buttons carry `aria-pressed` for their active state, and the
  select-all box uses `indeterminate` for "some but not all" — a plain
  unchecked box would claim nothing is selected.
- Live regions are always in the DOM, so their content is announced when it
  changes rather than being inserted alongside its own text.
- Disabled bulk actions use the real `disabled` attribute, not `aria-disabled`:
  an inert control should also be unfocusable.

### Keyboard

The four keys P2 left **bound but deliberately inert** (`e`, `#`, `Shift+I`,
`s`) are now real, and P3 adds Gmail's `c` (compose), `r` (reply), `Shift+A`
(reply-all), `f` (forward) and `x` (toggle row selection). With a selection an
action is a bulk action; with none it applies to the focused row — Gmail's rule,
which is what makes `e` archive "the message I am looking at" with no selection
ceremony.

While the composer is open the global handler does not act on the list behind
it: `e` must not archive a message the user cannot see.

**The discoverability test was strengthened and it mattered.** It previously
iterated a hand-kept list of keys, so a *new* binding could never fail it —
exactly the regression it exists to prevent. It now sweeps the printable-ASCII
keyspace, asks the resolver what it binds, and requires each to appear in the
help sheet. Confirmed by deliberately removing one entry and watching it go red.

## Bugs this epic's own tests caught

Worth recording, because each was silent:

1. **`delete` was a complete no-op.** `patchFor` routed it through the move
   branch, which returns `{}` when no `mailboxId` is given — and a delete
   carries none on purpose, since the server owns W-A2. `planAction` skips
   empty patches, so the key painted nothing *and never issued the request*.
   Fixed, and pinned by two regression tests.
2. **A dangling selection anchor.** `pruneSelection` returned early when the
   selected set had not shrunk, leaving an anchor pointing at a message that
   was gone; the next shift-click would silently fall back to a plain click and
   lose the selection being built.
3. **The ambiguous `aria-label`** described under Accessibility above.

## Testing

**478 unit tests** (24 files). P3 adds coverage for the optimistic reducer and
its rollback orderings, the selection ranges (including the anchor rule that is
invisible until it is wrong), address parsing against header-injection and
Outlook's `"Last, First"` paste, quoting and attribution, the draft's exact
wire shape against `email_create.go`'s rules, the autosave debounce and
max-wait on an injected clock, the undo timer, the attachment size gate, and
the composer's own wiring — including that a double-click sends once and that
`cannotUnsend` reaches the screen.

### Live verification, and its honest limit

**The live send/undo against the pilot was NOT performed.** The credentials for
`moov-test@atmosfera.cloud` are supplied through `MOOV_TEST_USER` /
`MOOV_TEST_PASSWORD`, and **neither variable was present in this agent's
environment** (verified in both shells). Per the standing rule, no credentials
file was opened. The pilot was confirmed reachable and correctly challenging
(`401` on `/.well-known/jmap`), but nothing was signed into and **nothing was
deployed**.

What WAS verified in a real browser (Chromium via Playwright, against the dev
server with a stubbed JMAP layer — no credentials involved):

- the action bar, per-row checkboxes and Spanish strings render;
- **shift-click range selection** across three rows, announcing
  "3 seleccionados" and enabling every bulk action;
- **perceived latency under a 600 ms simulated round trip: flag 10 ms,
  archive 11 ms** (the ADR §6 bar is 100 ms);
- `c` opens the composer as a true `:modal` dialog with focus inside;
- rich text produces semantic `<b>` with `aria-pressed` tracking;
- a **double-click on Send produced exactly one submission**;
- the undo countdown opened at the server's 10 s and ticked to 8 s;
- **cancel sent `{undoStatus: "canceled"}`** and announced
  "Envío cancelado — el mensaje no se transmitió".

Console is free of errors from this app; the only two are the pre-existing
`/branding` 404s (server gap 2 above), which predate P3.

Screenshots: `docs/evidence/P3/`.

**The director should schedule the live pilot run** — send to `moov-test`
itself, prove undo cancels within the window, prove a completed send lands in
Sent exactly once — with credentials supplied and with the owner's approval.
Every server contract that run would exercise is pinned by a unit test against
the exact wire shape, so the residual risk is in the environment, not in the
shapes.

## Server and deployment gaps (P3's additions)

The P2 list above stands unchanged. P3 found:

7. **CLOSED (2026-08-26): `npm run typecheck` works.** The script is now
   `tsc -b --pretty` — the invocation `npm run build` already used — instead
   of the `--noEmit false` flags that conflicted with
   `allowImportingTsExtensions` (`TS5096`) and emitted `.js` files next to
   sources.

8. **`EmailSubmission/query` is still unregistered.** P3 does not need it: the
   composer holds the submission id it just created and cancels by that id, so
   nothing enumerates submissions. It will be needed by any future outbox view.

9. **CLOSED server-side (2026-08-26) with P2 gap 5:** part blobIds are real,
   and `Email/set create` reads attachment blobIds through the same
   `OpenBlob`, so a forward CAN now re-attach the original's parts. The
   composer wiring for that is still pending (it needs to seed the draft's
   attachment list from the original message).

## Notes for P4 (offline, PWA install, Bulwark replacement)

- **The optimistic overlay is the natural seam for offline queueing.** It
  already models "the UI shows X while the server has not confirmed X", with a
  per-message inverse for undo. An offline queue is the same structure with a
  durable backing store and a replay on reconnect; `useMessageActions` is the
  one file to extend.
- **SSE works now** (P2 gap 4 closed, 2026-08-26): `MailScreen` opens an
  `EventSource` with a `push`-scoped token, and a pushed `StateChange` bumps
  the same refresh cycle writes use (with a short debounce). The next step
  P4 owns is turning that blunt refetch into a targeted `Email/changes`.
- **Drafts are the offline case that matters most.** `createAutosaveScheduler`
  already separates *when* to save from *how*, so an IndexedDB-first save with
  a background flush to the server slots in without touching the composer.
- **`refreshToken` in `MailScreen` is a blunt refetch.** With SSE working it
  should become a targeted `Email/changes`; the write path already returns
  `newState` from every `/set` for exactly that.
- **Before replacing Bulwark**, the live run described above must pass, and the
  `/branding` routing gap (2) should be fixed or every install silently shows
  Moov's default brand.
