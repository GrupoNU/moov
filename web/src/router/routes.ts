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

/** The application's destinations. A closed union: adding one is a compile error everywhere it must be handled. */
export type Route =
  /** The message list for one mailbox, optionally with a message open. */
  | {
      readonly kind: "mailbox";
      /** A JMAP mailbox id ("mc"), or a role alias ("inbox") resolved at render time. */
      readonly mailboxId: string;
      readonly messageId?: string;
    }
  /** Full-text search across the account. */
  | {
      readonly kind: "search";
      readonly query: string;
      readonly messageId?: string;
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
    };

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

  if (segments[0] === "search") {
    const query = parsed.searchParams.get("q") ?? "";
    const messageId = segments[1];
    return messageId !== undefined && messageId !== ""
      ? { kind: "search", query, messageId: decodeURIComponent(messageId) }
      : { kind: "search", query };
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
      ? { kind: "label", name: decoded, messageId: decodeURIComponent(messageId) }
      : { kind: "label", name: decoded };
  }

  if (segments[0] === "mail") {
    const mailbox = segments[1];
    if (mailbox === undefined || mailbox === "") return DEFAULT_ROUTE;
    const messageId = segments[2];
    const mailboxId = decodeURIComponent(mailbox);
    return messageId !== undefined && messageId !== ""
      ? { kind: "mailbox", mailboxId, messageId: decodeURIComponent(messageId) }
      : { kind: "mailbox", mailboxId };
  }

  return DEFAULT_ROUTE;
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
      return route.messageId !== undefined
        ? `${base}/${encodeURIComponent(route.messageId)}`
        : base;
    }
    case "label": {
      // The name IS encoded, which is what keeps "work/clients" one segment.
      const base = `/label/${encodeURIComponent(route.name)}`;
      return route.messageId !== undefined
        ? `${base}/${encodeURIComponent(route.messageId)}`
        : base;
    }
    case "search": {
      // The query lives in the search string rather than the path: it is
      // free text, it may be empty, and `?q=` is the form users recognise
      // and that search engines and browsers autocomplete sensibly.
      const suffix = route.query === "" ? "" : `?q=${encodeURIComponent(route.query)}`;
      const base =
        route.messageId !== undefined
          ? `/search/${encodeURIComponent(route.messageId)}`
          : "/search";
      return `${base}${suffix}`;
    }
  }
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
  if (route.kind === "mailbox") {
    return messageId === undefined
      ? { kind: "mailbox", mailboxId: route.mailboxId }
      : { kind: "mailbox", mailboxId: route.mailboxId, messageId };
  }
  if (route.kind === "label") {
    return messageId === undefined
      ? { kind: "label", name: route.name }
      : { kind: "label", name: route.name, messageId };
  }
  return messageId === undefined
    ? { kind: "search", query: route.query }
    : { kind: "search", query: route.query, messageId };
}

/** The message currently open in a route, if any. */
export function openMessageId(route: Route): string | undefined {
  return route.messageId;
}
