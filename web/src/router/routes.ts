/**
 * The route model (P2 deliverable 1).
 *
 * # Why a hand-written router
 *
 * W-A3 calls for a "light router". React Router is ~20 kB for a feature set
 * this app uses about 5% of: there are exactly three destinations, none of
 * them nested, none of them lazy. What the app actually needs is a parse
 * function, a format function, and a subscription to `popstate` — which is
 * this file plus {@link ../router/RouterProvider}.
 *
 * The important property is that parsing and formatting are PURE and
 * round-trip: `parseRoute(formatRoute(r))` deep-equals `r` for every route the
 * app can construct. That is a unit test rather than a claim, and it is what
 * makes deep links and the back button work by construction rather than by
 * inspection.
 *
 * # Why the mailbox appears in the path but the message id does too
 *
 * A message is always read IN a mailbox — the list behind the reading pane is
 * part of the state a link should restore. `/mail/:mailbox/:messageId` restores
 * both; `/mail/:mailbox` restores the list alone. Search is its own top-level
 * route because its result set is not a mailbox, and pretending otherwise
 * ("/mail/search") would make every mailbox-shaped code path special-case it.
 */

/**
 * The page a list route is showing, when it is not the first (B-12).
 *
 * # Why the pager belongs in the URL after all
 *
 * `MailScreen` held this in `useState` with a documented reason: a page number
 * is not shareable, because "their inbox's page 3 holds different mail, and
 * mine holds different mail an hour later". That is true and it is an argument
 * about SHARING — but it is not the only thing a URL does. Back and reload are
 * the other two, and the review caught both failing: paging to 3 and pressing
 * Back left the app entirely instead of stepping to page 2, and reloading
 * dropped the user to page 1 with no indication anything had moved.
 *
 * A URL is the history entry. Anything the user navigated to and expects Back
 * to return from has to be in it, whether or not it means the same thing to
 * somebody else — and Gmail's own `#inbox/p2` is exactly this shape.
 *
 * # Why page 1 has no parameter
 *
 * `/mail/inbox` and `/mail/inbox?p=1` would be two URLs for one destination,
 * and `routesEqual` is `formatRoute` equality — so the router would think a
 * navigation happened where none did, pushing a duplicate history entry that
 * Back would have to be pressed twice to escape. One canonical form removes the
 * question, the same way `formatRoute` always writes the settings tab.
 *
 * 1-based, because it is what the URL says and what a user reads: `?p=2` is the
 * second page. The conversion to the 0-based `position` the server pages by
 * happens at the one place that talks to the server.
 */
export type PageNumber = number;

/** The application's destinations. A closed union: adding one is a compile error everywhere it must be handled. */
export type Route =
  /** The message list for one mailbox, optionally with a message open. */
  | {
      readonly kind: "mailbox";
      /** A JMAP mailbox id ("mc"), or a role alias ("inbox") resolved at render time. */
      readonly mailboxId: string;
      readonly messageId?: string;
      /** B-12: the 1-based page, absent on page one. See {@link PageNumber}. */
      readonly page?: PageNumber;
    }
  /** Full-text search across the account. */
  | {
      readonly kind: "search";
      readonly query: string;
      readonly messageId?: string;
      readonly page?: PageNumber;
    }
  /**
   * E8: every message carrying one user label.
   *
   * Its own kind rather than a search with a `label:` operator, for the reason
   * the search route gave for not being a mailbox: a label view's result set is
   * a `hasKeyword` filter, not a text query, and dressing it as one would mean
   * parsing the operator back out on every load and would make an ordinary
   * search for the literal text "label:work" ambiguous. Gmail's own URL does
   * the same thing — `#label/work` is a distinct hash, not `#search/label:work`.
   *
   * The path carries the DISPLAY NAME (`/label/work`), not the keyword: the
   * `$label:` prefix is a wire detail no user should see in a URL they might
   * share, and it is recoverable by `encodeLabelKeyword` at render time.
   */
  | {
      readonly kind: "label";
      /** The label's display name, un-prefixed. */
      readonly name: string;
      readonly messageId?: string;
      readonly page?: PageNumber;
    }
  /**
   * E9: the Outbox — mail composed offline, waiting for a connection.
   *
   * Its own kind, and not a mailbox, because nothing in it is a server object:
   * a queued message has no JMAP id, no thread and no mailbox, so every code
   * path that resolves `mailboxId` against the fetched folder list would have
   * to special-case it. It carries no `messageId` for the same reason — there
   * is no message to open, only a queue entry to retry or discard.
   */
  | { readonly kind: "outbox" }
  /**
   * "Destacados" — the starred view (canon 07 §2, owner's finding 3).
   *
   * Gmail lists it second in the rail, right under Recibidos, and it is not a
   * folder: a star is the `$flagged` IMAP system flag on a message that stays
   * where it is. So this is a virtual view, its own route kind for the same
   * reason `label` is one — its result set is a `hasKeyword` condition, not a
   * mailbox, and every code path that resolves `mailboxId` against the fetched
   * folder list would otherwise have to special-case it.
   *
   * It carries a `messageId` (unlike the Outbox) because the rows ARE real
   * messages: opening one from here has to be a link a user can share, and
   * coming back has to land on the starred list rather than on the inbox.
   */
  | {
      readonly kind: "starred";
      readonly messageId?: string;
      readonly page?: PageNumber;
    }
  /**
   * "Pospuestos" before the Snoozed folder exists (owner's finding 3).
   *
   * GC-10 makes snoozing a real IMAP move and the folder is created on demand,
   * so an account that has never snoozed anything has no mailbox to route to —
   * while Gmail shows the entry always. This route is that gap: it renders an
   * empty state and creates nothing.
   *
   * It is deliberately NOT a `mailbox` route with a missing id. A mailbox route
   * whose folder cannot be resolved is an ERROR everywhere else in the app, and
   * making one legitimate here would weaken that check for every real folder.
   * It carries no `messageId` because there is nothing to open.
   */
  | { readonly kind: "snoozedEmpty" }
  /**
   * E4: the Scheduled view — messages with a future `sendAt` (canon §2.3).
   *
   * Its own kind for the same reason the Outbox is, and for a second one the
   * Outbox does not have. The rows are `EmailSubmission` records, not `Email`s,
   * so nothing that resolves a mailbox applies to them — and there deliberately
   * IS no Scheduled folder: `internal/jmap/mail/submission_query.go` refused
   * one because "a scheduled message must stay a DRAFT [...] moving it to a
   * second folder would make every other IMAP client show it outside Drafts".
   *
   * The Snoozed view is NOT here, and that asymmetry is the design: snooze is a
   * real MOVE to a real folder (GC-10), so it is an ordinary `mailbox` route
   * and every existing code path works on it unchanged.
   */
  | { readonly kind: "scheduled" }
  /**
   * E12: the settings PAGE (canon 07 §5).
   *
   * A route, not a dialog, and the reason is Gmail's own: full settings replace
   * the list area while the top bar and the rail stay put, which makes them a
   * DESTINATION — one you can deep-link ("send me the link to the filters
   * tab"), bookmark, reach with Back, and land on directly from the quick
   * panel's "See all settings". A `<dialog>` can be none of those things, and
   * this app's previous one could not: there was no URL for "settings, filters"
   * to point at.
   *
   * The price is real and paid deliberately. `showModal()` supplied inertness,
   * a focus trap and Escape for free; a routed page has none of them and must
   * not — it is a page, and trapping focus on a page is a bug. What replaces
   * them is what replaces them for the mail list: `u`/Back returns, and the
   * shell's Escape handler is not involved at all.
   *
   * `tab` is part of the path rather than a query parameter because it selects
   * WHICH settings you are looking at, exactly as a mailbox id selects which
   * mail — and `/settings/filters` is a link a human can read, where
   * `/settings?tab=filters` is a link a human has to parse.
   */
  | { readonly kind: "settings"; readonly tab: SettingsTab };

/**
 * The settings tabs, in the order the tab row lists them (canon 07 §5).
 *
 * This is Gmail's own IA with its stated exclusions applied: no
 * Complementos/Chat/Temas (Google-ecosystem chrome), and no POP/IMAP tab
 * (GC-9 — Dovecot IS the IMAP server, so porting Gmail's IMAP settings would
 * import Google's web-store-vs-IMAP impedance debt to solve a problem we do not
 * have).
 *
 * "Filtros y direcciones bloqueadas" is ONE tab holding two sections, which is
 * the fold Gmail uses: filters and blocked senders are the same Sieve script on
 * our server too, so the fold is honest here in a way it is only conventional
 * at Google.
 */
export const SETTINGS_TABS = [
  "general",
  "labels",
  "inbox",
  "account",
  "filters",
  "forwarding",
  "offline",
] as const;

export type SettingsTab = (typeof SETTINGS_TABS)[number];

/** Where `/settings` with no tab, or an unknown one, lands. */
export const DEFAULT_SETTINGS_TAB: SettingsTab = "general";

/** True when a path segment names a settings tab. */
export function isSettingsTab(value: string): value is SettingsTab {
  return (SETTINGS_TABS as readonly string[]).includes(value);
}

/** Where an unrecognised or empty URL lands. */
export const DEFAULT_ROUTE: Route = { kind: "mailbox", mailboxId: "inbox" };

/**
 * Role aliases accepted in a URL in place of an opaque mailbox id.
 *
 * `/mail/inbox` is a link a human can type, remember and share; `/mail/mc` is
 * not, and worse, the opaque id is only stable for one account — a bookmarked
 * `/mail/mc` would open a different folder for a different user. Roles are
 * stable across accounts and servers, so they are the canonical form for the
 * six mailboxes that have one. Custom folders have no role and therefore use
 * their id, which is the honest thing to do rather than inventing a slug that
 * could collide with a role name.
 */
export const ROLE_ALIASES = [
  "inbox",
  "drafts",
  "sent",
  "archive",
  "junk",
  "trash",
] as const;

export type RoleAlias = (typeof ROLE_ALIASES)[number];

/** True when a path segment names a role rather than a mailbox id. */
export function isRoleAlias(value: string): value is RoleAlias {
  return (ROLE_ALIASES as readonly string[]).includes(value);
}

/**
 * Parses a URL (path + search) into a route.
 *
 * Never throws and never returns undefined: an unparseable URL is the default
 * route. A router that can fail is a router that can show a blank screen after
 * a typo in a shared link.
 */
export function parseRoute(url: string): Route {
  // A relative URL needs a base to parse; the base is discarded immediately.
  let parsed: URL;
  try {
    parsed = new URL(url, "http://localhost");
  } catch {
    return DEFAULT_ROUTE;
  }

  /*
   * Segments are taken POSITIONALLY, with a single leading empty dropped —
   * they are deliberately NOT filtered for emptiness. Filtering would make
   * "/mail//e1" parse as mailbox "e1": the empty mailbox segment vanishes and
   * the message id slides into its place, opening a folder that does not
   * exist. Keeping the empty means the mailbox segment is "" and the URL
   * correctly falls back to the default route.
   */
  const raw = parsed.pathname.split("/");
  const withoutLeading = raw[0] === "" ? raw.slice(1) : raw;
  // A trailing slash is cosmetic ("/mail/inbox/" is "/mail/inbox"), so exactly
  // one trailing empty is dropped — unlike an INTERIOR empty, which is a
  // malformed path and must not be closed up.
  const segments =
    withoutLeading.length > 1 && withoutLeading[withoutLeading.length - 1] === ""
      ? withoutLeading.slice(0, -1)
      : withoutLeading;

  /*
   * B-12: `?p=N`, spread as `{ page: N }` or as nothing at all.
   *
   * Spread rather than assigned so page one produces a route with NO `page`
   * key — under `exactOptionalPropertyTypes` an explicit `page: undefined` is
   * a different object from an absent one, and `toEqual` in the round-trip
   * test sees the difference. That is the compiler enforcing the canonical
   * form the header argues for.
   */
  const page = parsePageParam(parsed.searchParams.get("p"));
  const pageProp = page === undefined ? {} : { page };

  if (segments[0] === "search") {
    const query = parsed.searchParams.get("q") ?? "";
    const messageId = segments[1];
    return messageId !== undefined && messageId !== ""
      ? { kind: "search", query, messageId: decodeURIComponent(messageId), ...pageProp }
      : { kind: "search", query, ...pageProp };
  }

  /*
   * `/label/:name` and `/label/:name/:messageId`. The name is percent-decoded,
   * so a label containing a slash — "work/clients", the nested convention —
   * arrives whole rather than splitting into two segments and losing its tail.
   * `formatRoute` encodes it for the same reason.
   */
  if (segments[0] === "label") {
    const name = segments[1];
    if (name === undefined || name === "") return DEFAULT_ROUTE;
    const messageId = segments[2];
    const decoded = decodeURIComponent(name);
    return messageId !== undefined && messageId !== ""
      ? { kind: "label", name: decoded, messageId: decodeURIComponent(messageId), ...pageProp }
      : { kind: "label", name: decoded, ...pageProp };
  }

  /*
   * "Destacados". `/starred` and `/starred/:messageId`, the same shape the
   * label route uses — the view is a list of real messages, so a link to one
   * of them has to restore the list it was opened from.
   */
  if (segments[0] === "starred") {
    const messageId = segments[1];
    return messageId !== undefined && messageId !== ""
      ? { kind: "starred", messageId: decodeURIComponent(messageId), ...pageProp }
      : { kind: "starred", ...pageProp };
  }

  // Like the Outbox: one fixed segment, nothing to parameterise, no message to
  // open. `snoozed` rather than `pospuestos` — paths are not localised.
  if (segments[0] === "snoozed") return { kind: "snoozedEmpty" };

  // E9: a single fixed segment; there is nothing to parameterise.
  if (segments[0] === "outbox") return { kind: "outbox" };
  // E4: likewise — the Scheduled view lists submissions, not messages.
  if (segments[0] === "scheduled") return { kind: "scheduled" };

  /*
   * E12: `/settings` and `/settings/:tab`.
   *
   * An UNKNOWN tab falls back to General rather than to the default route, and
   * that difference matters: a stale bookmark to a tab that has since been
   * renamed should land in settings, not silently in the inbox. The user asked
   * for settings; only the sub-destination was wrong.
   */
  if (segments[0] === "settings") {
    const tab = segments[1];
    if (tab === undefined || tab === "") {
      return { kind: "settings", tab: DEFAULT_SETTINGS_TAB };
    }
    const decoded = decodeURIComponent(tab);
    return {
      kind: "settings",
      tab: isSettingsTab(decoded) ? decoded : DEFAULT_SETTINGS_TAB,
    };
  }

  if (segments[0] === "mail") {
    const mailbox = segments[1];
    if (mailbox === undefined || mailbox === "") return DEFAULT_ROUTE;
    const messageId = segments[2];
    const mailboxId = decodeURIComponent(mailbox);
    return messageId !== undefined && messageId !== ""
      ? { kind: "mailbox", mailboxId, messageId: decodeURIComponent(messageId), ...pageProp }
      : { kind: "mailbox", mailboxId, ...pageProp };
  }

  return DEFAULT_ROUTE;
}

/**
 * `?p=` as a page number, or undefined for anything that is not one (B-12).
 *
 * Every rejection lands on undefined, which renders page one — the same
 * posture `parseRoute` takes overall: a URL a user typed, truncated or edited
 * by hand must never produce a blank screen or an error. "p=0", "p=-3",
 * "p=abc", "p=1.5" and "p=" are all simply "the first page".
 *
 * The upper bound is the pager's own reach: `mail/paging.ts` mirrors the
 * server's `MaxQueryReach` of 100,000 over pages of 50, so page 2,000 is the
 * last one that can be served. A hand-typed `?p=999999` clamps to page one
 * rather than requesting a position the server will answer with an empty list
 * that looks like lost mail.
 */
const MAX_PAGE = 2000;

function parsePageParam(raw: string | null): number | undefined {
  if (raw === null || raw === "") return undefined;
  // `Number` and not `parseInt`: `parseInt("2abc")` is 2, which would silently
  // accept a malformed parameter as a page.
  const value = Number(raw);
  if (!Number.isInteger(value)) return undefined;
  if (value <= 1 || value > MAX_PAGE) return undefined;
  return value;
}

/**
 * Formats a route as a root-relative URL.
 *
 * Every segment is percent-encoded. Mailbox ids are opaque server strings and
 * a custom folder name never reaches this function, but encoding is not
 * conditional on trusting the input — that is the habit that produces the one
 * unencoded call site.
 */
export function formatRoute(route: Route): string {
  switch (route.kind) {
    case "mailbox": {
      const base = `/mail/${encodeURIComponent(route.mailboxId)}`;
      const path =
        route.messageId !== undefined
          ? `${base}/${encodeURIComponent(route.messageId)}`
          : base;
      return `${path}${pageQuery(route.page)}`;
    }
    case "label": {
      // The name IS encoded, which is what keeps "work/clients" one segment.
      const base = `/label/${encodeURIComponent(route.name)}`;
      const path =
        route.messageId !== undefined
          ? `${base}/${encodeURIComponent(route.messageId)}`
          : base;
      return `${path}${pageQuery(route.page)}`;
    }
    case "search": {
      // The query lives in the search string rather than the path: it is
      // free text, it may be empty, and `?q=` is the form users recognise
      // and that search engines and browsers autocomplete sensibly.
      const params: string[] = [];
      if (route.query !== "") params.push(`q=${encodeURIComponent(route.query)}`);
      // B-12: `p` after `q`, always in this order — `formatRoute` equality IS
      // `routesEqual`, so two spellings of one destination would make the
      // router see a navigation where none happened.
      if (isPagedBeyondFirst(route.page)) params.push(`p=${String(route.page)}`);
      const suffix = params.length === 0 ? "" : `?${params.join("&")}`;
      const base =
        route.messageId !== undefined
          ? `/search/${encodeURIComponent(route.messageId)}`
          : "/search";
      return `${base}${suffix}`;
    }
    case "starred": {
      const path =
        route.messageId !== undefined
          ? `/starred/${encodeURIComponent(route.messageId)}`
          : "/starred";
      return `${path}${pageQuery(route.page)}`;
    }
    case "snoozedEmpty":
      return "/snoozed";
    case "outbox":
      return "/outbox";
    case "scheduled":
      return "/scheduled";
    /*
     * The tab is ALWAYS in the path, including for the default. `/settings`
     * and `/settings/general` would otherwise be two URLs for one destination,
     * and `routesEqual` — which is `formatRoute` equality — would then have to
     * decide which of them the route "is". One canonical form removes the
     * question.
     */
    case "settings":
      return `/settings/${encodeURIComponent(route.tab)}`;
  }
}

/** True when a page number is worth writing into a URL (B-12). */
function isPagedBeyondFirst(page: number | undefined): boolean {
  return page !== undefined && page > 1;
}

/** `?p=N`, or "" for page one — the canonical form. */
function pageQuery(page: number | undefined): string {
  return isPagedBeyondFirst(page) ? `?p=${String(page)}` : "";
}

/**
 * The page a route is showing — always a real 1-based number (B-12).
 *
 * Routes with no list (the Outbox, Scheduled, settings, the empty Pospuestos)
 * answer 1 rather than undefined, so a caller never has to ask "does this
 * destination page" before asking "which page". Those views simply do not call
 * it.
 */
export function pageOf(route: Route): number {
  if (
    route.kind === "outbox" ||
    route.kind === "scheduled" ||
    route.kind === "snoozedEmpty" ||
    route.kind === "settings"
  ) {
    return 1;
  }
  return route.page ?? 1;
}

/**
 * The same list at a different page (B-12).
 *
 * The counterpart to {@link withMessage}, and it exists for the same reason:
 * building a fresh route at each call site is how a pager loses the search
 * query, the label name or the open message it was paging underneath.
 *
 * Page one drops the key entirely rather than setting it to 1 — the canonical
 * form `PageNumber` documents, without which `/mail/inbox` and
 * `/mail/inbox?p=1` become two URLs for one destination and `routesEqual`
 * reports a navigation that did not happen.
 *
 * A non-list route absorbs the request unchanged, exactly as `withMessage`
 * does for a message: there is no list to page.
 */
export function withPage(route: Route, page: number): Route {
  if (
    route.kind === "outbox" ||
    route.kind === "scheduled" ||
    route.kind === "snoozedEmpty" ||
    route.kind === "settings"
  ) {
    return route;
  }
  const { page: _dropped, ...rest } = route;
  return page > 1 ? { ...rest, page } : rest;
}

/** True when two routes denote the same destination. */
export function routesEqual(a: Route, b: Route): boolean {
  return formatRoute(a) === formatRoute(b);
}

/**
 * The route for the same list with a different message open (or none).
 *
 * Opening and closing a message must not lose the list you are looking at,
 * which is exactly the bug produced by building a fresh route at each call
 * site.
 */
export function withMessage(route: Route, messageId: string | undefined): Route {
  /*
   * B-12: the PAGE survives opening and closing a message, for exactly the
   * reason this function exists at all. Opening a message from page 3 and
   * closing it again has to land back on page 3 — dropping the parameter here
   * would silently reset the list under the reader, which is the same class of
   * bug as losing the search query.
   */
  const current = pageOf(route);
  const pageProp = current > 1 ? { page: current } : {};
  if (route.kind === "mailbox") {
    return messageId === undefined
      ? { kind: "mailbox", mailboxId: route.mailboxId, ...pageProp }
      : { kind: "mailbox", mailboxId: route.mailboxId, messageId, ...pageProp };
  }
  if (route.kind === "label") {
    return messageId === undefined
      ? { kind: "label", name: route.name, ...pageProp }
      : { kind: "label", name: route.name, messageId, ...pageProp };
  }
  if (route.kind === "starred") {
    // A list of real messages, so it behaves like the mailbox and label views:
    // opening one keeps the starred list underneath it.
    return messageId === undefined
      ? { kind: "starred", ...pageProp }
      : { kind: "starred", messageId, ...pageProp };
  }
  /*
   * E9: the Outbox holds no messages the reader can open, so it absorbs the
   * request rather than inventing a route. Returning the outbox unchanged is
   * the honest answer to "open message X in this list": there is no such list.
   *
   * E12: the settings page absorbs it for the same reason — it is not a list of
   * mail at all. The alternative, navigating AWAY from settings to open a
   * message, would make a stray notification click silently discard whatever
   * the user was editing.
   */
  if (
    route.kind === "outbox" ||
    route.kind === "scheduled" ||
    // The empty Pospuestos state holds no messages either — there is no folder
    // behind it yet, so there is nothing for the reader to open.
    route.kind === "snoozedEmpty" ||
    route.kind === "settings"
  ) {
    return route;
  }
  return messageId === undefined
    ? { kind: "search", query: route.query }
    : { kind: "search", query: route.query, messageId };
}

/** The message currently open in a route, if any. */
export function openMessageId(route: Route): string | undefined {
  return route.kind === "outbox" ||
    route.kind === "scheduled" ||
    route.kind === "snoozedEmpty" ||
    route.kind === "settings"
    ? undefined
    : route.messageId;
}
