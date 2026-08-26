# Moov Mail — the PWA

The product's own face: a React + TypeScript progressive web app that speaks
JMAP to Moov's server. This is epic **P1** of [`docs/specs/L2-pwa.md`][spec] —
foundations, branding, and the login screen.

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

## Notes for P2

- `src/api/jmap.ts` already carries batching and back-references (`backRef`),
  written but unused — P2 adds methods, not plumbing. The canonical
  `Email/query` → `Email/get` pair is what `backRef` exists for.
- The shell's sidebar renders an honest skeleton with `aria-busy`; replacing it
  with the real mailbox list is a data source, not a layout change. The 260px
  column is sized for the longest folder names Dovecot produces.
- The "router" in `App.tsx` is an auth-state switch, not a URL router. A second
  destination (a mailbox in the path) plugs in below `AppShell`.
- `credentials: "omit"` is set on every request and the `Authorization` header
  is attached explicitly — keep it that way; the wildcard-with-credentials
  combination is what `internal/jmaphttp/cors.go` makes unrepresentable.
- Session persistence is `sessionStorage` and deliberately tab-scoped. When the
  server grows bearer tokens (phase 2 of `L2-jmap-server` §2.2),
  `src/auth/session.ts` is the only file that changes.
