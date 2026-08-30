import { describe, expect, it } from "vitest";

import {
  DEFAULT_ROUTE,
  formatRoute,
  isRoleAlias,
  openMessageId,
  parseRoute,
  routesEqual,
  withMessage,
  type Route,
} from "./routes";

describe("parseRoute", () => {
  it("parses a mailbox route", () => {
    expect(parseRoute("/mail/inbox")).toEqual({ kind: "mailbox", mailboxId: "inbox" });
  });

  it("parses a mailbox route with an open message", () => {
    expect(parseRoute("/mail/inbox/e42")).toEqual({
      kind: "mailbox",
      mailboxId: "inbox",
      messageId: "e42",
    });
  });

  it("parses a search route with its query", () => {
    expect(parseRoute("/search?q=arquitectura")).toEqual({
      kind: "search",
      query: "arquitectura",
    });
  });

  it("decodes percent-encoded queries, including spaces and accents", () => {
    expect(parseRoute("/search?q=dise%C3%B1o%20de%20lista")).toEqual({
      kind: "search",
      query: "diseño de lista",
    });
  });

  it("parses a search route with an open message", () => {
    expect(parseRoute("/search/e7?q=hola")).toEqual({
      kind: "search",
      query: "hola",
      messageId: "e7",
    });
  });

  it("treats an opaque mailbox id as a mailbox", () => {
    expect(parseRoute("/mail/mc")).toEqual({ kind: "mailbox", mailboxId: "mc" });
  });

  // The whole point of a router that cannot fail: no URL produces a blank app.
  it.each(["", "/", "/nonsense", "/mail", "/mail/", "//", "/mail//e1"])(
    "falls back to the default route for %o",
    (url) => {
      expect(parseRoute(url)).toEqual(DEFAULT_ROUTE);
    },
  );

  it("treats a search with no q as an empty query rather than failing", () => {
    expect(parseRoute("/search")).toEqual({ kind: "search", query: "" });
  });
});

describe("formatRoute", () => {
  it("omits the query string for an empty search", () => {
    expect(formatRoute({ kind: "search", query: "" })).toBe("/search");
  });

  it("encodes a query with spaces and accents", () => {
    expect(formatRoute({ kind: "search", query: "diseño de lista" })).toBe(
      "/search?q=dise%C3%B1o%20de%20lista",
    );
  });
});

describe("round-tripping", () => {
  // This is the property that makes deep links and the back button work by
  // construction: whatever the app can express, it can also parse back.
  const routes: readonly Route[] = [
    { kind: "mailbox", mailboxId: "inbox" },
    { kind: "mailbox", mailboxId: "mc" },
    { kind: "mailbox", mailboxId: "inbox", messageId: "e42" },
    { kind: "mailbox", mailboxId: "m7", messageId: "ekxn" },
    { kind: "search", query: "" },
    { kind: "search", query: "arquitectura" },
    { kind: "search", query: "diseño de lista" },
    { kind: "search", query: "a b", messageId: "e1" },
  ];

  it.each(routes)("survives format → parse: %o", (route) => {
    expect(parseRoute(formatRoute(route))).toEqual(route);
  });
});

describe("withMessage", () => {
  it("opens a message without losing the mailbox", () => {
    const route: Route = { kind: "mailbox", mailboxId: "inbox" };
    expect(withMessage(route, "e9")).toEqual({
      kind: "mailbox",
      mailboxId: "inbox",
      messageId: "e9",
    });
  });

  it("closes a message without losing the search query", () => {
    const route: Route = { kind: "search", query: "hola", messageId: "e9" };
    expect(withMessage(route, undefined)).toEqual({ kind: "search", query: "hola" });
  });
});

describe("routesEqual", () => {
  it("is true for structurally identical routes", () => {
    expect(
      routesEqual({ kind: "mailbox", mailboxId: "inbox" }, { kind: "mailbox", mailboxId: "inbox" }),
    ).toBe(true);
  });

  it("distinguishes an open message from a closed one", () => {
    expect(
      routesEqual(
        { kind: "mailbox", mailboxId: "inbox" },
        { kind: "mailbox", mailboxId: "inbox", messageId: "e1" },
      ),
    ).toBe(false);
  });
});

describe("openMessageId", () => {
  it("returns undefined when no message is open", () => {
    expect(openMessageId({ kind: "mailbox", mailboxId: "inbox" })).toBeUndefined();
  });

  it("returns the id when one is", () => {
    expect(openMessageId({ kind: "search", query: "x", messageId: "e3" })).toBe("e3");
  });
});

describe("isRoleAlias", () => {
  it("accepts the six standard roles", () => {
    for (const role of ["inbox", "drafts", "sent", "archive", "junk", "trash"]) {
      expect(isRoleAlias(role)).toBe(true);
    }
  });

  it("rejects a mailbox id", () => {
    expect(isRoleAlias("mc")).toBe(false);
  });
});

/**
 * E8 — the label route.
 *
 * Its own kind rather than a search with a `label:` operator: the result set is
 * a `hasKeyword` filter, not a text query, and the operator form would make a
 * literal search for "label:work" ambiguous. Gmail's own `#label/work` is a
 * distinct hash for the same reason.
 */
describe("the label route", () => {
  it("parses /label/:name", () => {
    expect(parseRoute("/label/work")).toEqual({ kind: "label", name: "work" });
  });

  it("parses /label/:name/:messageId", () => {
    expect(parseRoute("/label/work/e1")).toEqual({
      kind: "label",
      name: "work",
      messageId: "e1",
    });
  });

  it("keeps a NESTED name whole rather than splitting it into two segments", () => {
    /*
     * The one that would silently break: "work/clients" un-encoded would parse
     * as label "work" with message id "clients" — a different destination that
     * looks plausible enough to ship.
     */
    const route = { kind: "label", name: "work/clients" } as const;
    expect(formatRoute(route)).toBe("/label/work%2Fclients");
    expect(parseRoute(formatRoute(route))).toEqual(route);
  });

  it("round-trips unicode names", () => {
    for (const name of ["Facturación", "日本語", "a b", "work/clients", "back~up"]) {
      const route = { kind: "label", name } as const;
      expect(parseRoute(formatRoute(route))).toEqual(route);
    }
  });

  it("falls back to the default route for an empty name", () => {
    expect(parseRoute("/label/")).toEqual(DEFAULT_ROUTE);
    expect(parseRoute("/label")).toEqual(DEFAULT_ROUTE);
  });

  it("keeps the label when a message is opened and closed", () => {
    const base = { kind: "label", name: "work" } as const;
    const opened = withMessage(base, "e1");
    expect(opened).toEqual({ kind: "label", name: "work", messageId: "e1" });
    expect(withMessage(opened, undefined)).toEqual(base);
  });
});
