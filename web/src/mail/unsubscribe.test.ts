import { describe, expect, it } from "vitest";

import type { Email, EmailHeader } from "./types";
import {
  headerValue,
  listIdLabel,
  parseMailtoUri,
  parseUnsubscribeUris,
  unsubscribeInfo,
} from "./unsubscribe";

function withHeaders(headers: readonly EmailHeader[]): Email {
  return { id: "m1", headers };
}

describe("finding the header", () => {
  it("matches the name case-insensitively, as senders write it every way", () => {
    const headers = [{ name: "list-UNSUBSCRIBE", value: "<mailto:a@b.c>" }];
    expect(headerValue(headers, "List-Unsubscribe")).toBe("<mailto:a@b.c>");
  });

  it("returns undefined rather than throwing when there are no headers", () => {
    expect(headerValue(undefined, "List-Unsubscribe")).toBeUndefined();
  });
});

describe("parsing the URI list", () => {
  it("takes both URIs of the common two-option header", () => {
    expect(
      parseUnsubscribeUris("<mailto:u@list.example?subject=unsub>, <https://list.example/u?k=1>"),
    ).toEqual(["mailto:u@list.example?subject=unsub", "https://list.example/u?k=1"]);
  });

  it("ignores RFC 2822 comments outside the brackets", () => {
    expect(parseUnsubscribeUris("<https://x.example/u> (Click here to unsubscribe)")).toEqual([
      "https://x.example/u",
    ]);
  });

  it("unfolds a header split across lines", () => {
    expect(parseUnsubscribeUris("<https://x.example/very/\r\n long/path>")).toEqual([
      "https://x.example/very/long/path",
    ]);
  });

  /*
   * The security-relevant case. This header is attacker-controlled and the UI
   * turns it into a clickable control, so a scheme that can execute must never
   * survive the parser.
   */
  it.each([
    ["javascript:alert(1)"],
    ["data:text/html,<script>alert(1)</script>"],
    ["vbscript:msgbox"],
    ["file:///etc/passwd"],
  ])("refuses the %s scheme outright", (uri) => {
    expect(parseUnsubscribeUris(`<${uri}>`)).toEqual([]);
  });

  it("keeps the safe URI when a hostile one sits beside it", () => {
    expect(parseUnsubscribeUris("<javascript:alert(1)>, <https://ok.example/u>")).toEqual([
      "https://ok.example/u",
    ]);
  });

  it("returns nothing for an absent or empty header", () => {
    expect(parseUnsubscribeUris(undefined)).toEqual([]);
    expect(parseUnsubscribeUris("")).toEqual([]);
    expect(parseUnsubscribeUris("no brackets here")).toEqual([]);
  });
});

describe("splitting a mailto: URI", () => {
  it("reads the address and the prefilled subject", () => {
    expect(parseMailtoUri("mailto:unsub@list.example?subject=Unsubscribe%20me")).toEqual({
      to: "unsub@list.example",
      subject: "Unsubscribe me",
      body: undefined,
    });
  });

  it("decodes + as a space in the query, per form encoding", () => {
    expect(parseMailtoUri("mailto:u@l.example?subject=stop+now")?.subject).toBe("stop now");
  });

  it("reads a body when the sender prefilled one", () => {
    expect(parseMailtoUri("mailto:u@l.example?body=UNSUBSCRIBE")?.body).toBe("UNSUBSCRIBE");
  });

  it("refuses a mailto with no usable recipient", () => {
    expect(parseMailtoUri("mailto:?subject=x")).toBeUndefined();
    expect(parseMailtoUri("mailto:notanaddress")).toBeUndefined();
  });

  it("survives a broken percent escape instead of throwing", () => {
    expect(parseMailtoUri("mailto:u@l.example?subject=100%25%zz")?.to).toBe("u@l.example");
  });
});

describe("what the reader gets", () => {
  it("prefers the mailto path while keeping the URL available", () => {
    const info = unsubscribeInfo(
      withHeaders([
        {
          name: "List-Unsubscribe",
          value: "<https://l.example/u>, <mailto:u@l.example>",
        },
      ]),
    );
    expect(info?.mailto?.to).toBe("u@l.example");
    expect(info?.url).toBe("https://l.example/u");
  });

  it("reports RFC 8058 one-click when the sender advertised it", () => {
    const info = unsubscribeInfo(
      withHeaders([
        { name: "List-Unsubscribe", value: "<https://l.example/u>" },
        { name: "List-Unsubscribe-Post", value: "List-Unsubscribe=One-Click" },
      ]),
    );
    expect(info?.oneClick).toBe(true);
  });

  it("does not claim one-click without the header", () => {
    const info = unsubscribeInfo(
      withHeaders([{ name: "List-Unsubscribe", value: "<https://l.example/u>" }]),
    );
    expect(info?.oneClick).toBe(false);
  });

  /*
   * Nothing usable must mean NO control. A dead Unsubscribe button teaches the
   * user the feature does not work, which is worse than not offering it.
   */
  it("returns undefined when the header offers nothing usable", () => {
    expect(unsubscribeInfo(withHeaders([]))).toBeUndefined();
    expect(
      unsubscribeInfo(withHeaders([{ name: "List-Unsubscribe", value: "<javascript:x>" }])),
    ).toBeUndefined();
    expect(unsubscribeInfo({ id: "m1" })).toBeUndefined();
  });
});

describe("the list's display name", () => {
  it("prefers the human half of List-ID", () => {
    expect(
      listIdLabel(withHeaders([{ name: "List-ID", value: 'Moov News <news.moov.example>' }])),
    ).toBe("Moov News");
  });

  it("falls back to the bracketed identifier when there is no name", () => {
    expect(listIdLabel(withHeaders([{ name: "List-ID", value: "<news.moov.example>" }]))).toBe(
      "news.moov.example",
    );
  });

  it("returns undefined when there is no List-ID at all", () => {
    expect(listIdLabel(withHeaders([]))).toBeUndefined();
  });
});
