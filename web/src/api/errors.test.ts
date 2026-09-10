import { describe, expect, it } from "vitest";

import {
  ApiError,
  apiErrorFromResponse,
  apiErrorFromThrown,
  kindForStatus,
  parseRetryAfter,
  readProblemDetail,
  type ApiErrorKind,
} from "./errors";
import { messageForError } from "./errorMessages";
import { brandName, en, es, type Strings } from "../i18n/strings";
import { MOOV_DEFAULT_BRANDING } from "../branding/branding";
import type { Translation } from "../i18n/I18nProvider";

/**
 * Tests for the error taxonomy.
 *
 * # What is actually being defended here
 *
 * The pilot's failure: our server answered an unprovisioned mailbox with a
 * precise, actionable 403 and Bulwark rendered "an error occurred". These
 * tests assert that (a) each status keeps its distinct meaning all the way to
 * the UI, and (b) NO reachable path produces a generic message.
 */

/** Builds a Response-like object with the headers a case needs. */
function response(
  status: number,
  body?: unknown,
  headers: Record<string, string> = {},
): Response {
  const isJson = body !== undefined;
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: new Headers({
      ...(isJson ? { "Content-Type": "application/problem+json" } : {}),
      ...headers,
    }),
    json: () =>
      isJson ? Promise.resolve(body) : Promise.reject(new SyntaxError("no body")),
  } as unknown as Response;
}

/** A translation harness over a real locale table. */
function translationFor(strings: Strings): Translation {
  return {
    locale: "en",
    brand: MOOV_DEFAULT_BRANDING.name,
    t: (key) => {
      const value = strings[key];
      return typeof value === "function"
        ? (value)(brandName(MOOV_DEFAULT_BRANDING.name))
        : value;
    },
    format: (key, ...args) =>
      (strings[key] as unknown as (...a: unknown[]) => string)(...args),
  };
}

describe("kindForStatus", () => {
  it("maps each status our server sends to its own kind", () => {
    // The inverse of internal/jmaphttp/auth.go. Each line names the server
    // function that produces the status.
    expect(kindForStatus(401)).toBe("invalid-credentials"); // challenge()
    expect(kindForStatus(403)).toBe("not-provisioned"); // requireProvisioned()
    expect(kindForStatus(429)).toBe("rate-limited"); // tooMany()
    expect(kindForStatus(503)).toBe("server-error"); // auth backend unavailable
    expect(kindForStatus(500)).toBe("server-error");
  });

  it("keeps 401 and 403 distinct — the distinction the pilot lost", () => {
    expect(kindForStatus(401)).not.toBe(kindForStatus(403));
  });
});

describe("readProblemDetail", () => {
  it("reads the detail from an RFC 7807 body", async () => {
    // The server's real 403 text.
    const detail =
      "this mailbox authenticated correctly but is not provisioned in Moov; " +
      "an administrator must add it with `moovctl account add` first";
    const problem = { type: "about:blank", status: 403, detail };
    expect(await readProblemDetail(response(403, problem))).toBe(detail);
  });

  it("returns undefined rather than throwing on a body it cannot read", async () => {
    expect(await readProblemDetail(response(500))).toBeUndefined();
    expect(await readProblemDetail(response(403, { type: "about:blank" }))).toBeUndefined();
    expect(await readProblemDetail(response(403, { detail: "   " }))).toBeUndefined();
    expect(await readProblemDetail(response(403, "not an object"))).toBeUndefined();
  });
});

describe("parseRetryAfter", () => {
  it("reads the delta-seconds form the server sends", () => {
    expect(parseRetryAfter(response(429, undefined, { "Retry-After": "30" }))).toBe(30);
    expect(parseRetryAfter(response(429, undefined, { "Retry-After": " 5 " }))).toBe(5);
    expect(parseRetryAfter(response(429, undefined, { "Retry-After": "0" }))).toBe(0);
  });

  it("ignores a value it cannot use", () => {
    expect(parseRetryAfter(response(429))).toBeUndefined();
    expect(
      parseRetryAfter(response(429, undefined, { "Retry-After": "not a number" })),
    ).toBeUndefined();
    expect(parseRetryAfter(response(429, undefined, { "Retry-After": "-5" }))).toBeUndefined();
  });
});

describe("apiErrorFromResponse", () => {
  it("carries the server's own detail through", async () => {
    const detail = "this account is disabled in Moov";
    const error = await apiErrorFromResponse(response(403, { detail }));

    expect(error.kind).toBe("not-provisioned");
    expect(error.status).toBe(403);
    expect(error.detail).toBe(detail);
  });

  it("carries Retry-After on a 429", async () => {
    const error = await apiErrorFromResponse(
      response(429, { detail: "too many failed attempts" }, { "Retry-After": "45" }),
    );
    expect(error.kind).toBe("rate-limited");
    expect(error.retryAfterSeconds).toBe(45);
  });
});

describe("apiErrorFromThrown", () => {
  it("classifies a fetch transport failure as network", () => {
    // fetch() rejects with TypeError for offline, DNS, TLS and CORS alike —
    // indistinguishable to script by design.
    expect(apiErrorFromThrown(new TypeError("Failed to fetch")).kind).toBe("network");
  });

  it("classifies an abort as its own kind, not as a failure", () => {
    const error = apiErrorFromThrown(new DOMException("Aborted", "AbortError"));
    expect(error.kind).toBe("aborted");
  });

  it("passes an ApiError through unchanged", () => {
    const original = new ApiError("not-provisioned", "x", { status: 403 });
    expect(apiErrorFromThrown(original)).toBe(original);
  });

  it("never throws, whatever it is given", () => {
    for (const value of [undefined, null, "a string", 42, {}, new Error("boom")]) {
      expect(() => apiErrorFromThrown(value)).not.toThrow();
    }
  });
});

describe("messageForError", () => {
  const kinds: ApiErrorKind[] = [
    "invalid-credentials",
    "not-provisioned",
    "rate-limited",
    "server-error",
    "network",
    "aborted",
    "jmap",
    "unknown",
  ];

  it.each([
    ["en", en],
    ["es", es],
  ])("produces a real title and body for every kind in %s", (_name, strings) => {
    const translation = translationFor(strings);

    for (const kind of kinds) {
      const message = messageForError(new ApiError(kind, "raw"), translation);

      expect(message.title, `${kind} title`).toBeTruthy();
      expect(message.body, `${kind} body`).toBeTruthy();
      // The body must be a sentence that says something, not a label.
      expect(message.body.length, `${kind} body length`).toBeGreaterThan(20);

      // THE REGRESSION GUARD. These are the phrasings that carry no
      // information — the class of message this whole module exists to make
      // impossible.
      const generic = /^(an error occurred|error|something failed|failed)\.?$/i;
      expect(message.title, `${kind} title is generic`).not.toMatch(generic);
      expect(message.body, `${kind} body is generic`).not.toMatch(generic);
    }
  });

  it("tells an unprovisioned user that an administrator must act", () => {
    // The exact case Bulwark flattened.
    const message = messageForError(
      new ApiError("not-provisioned", "not provisioned", { status: 403 }),
      translationFor(en),
    );

    expect(message.body.toLowerCase()).toContain("administrator");
    // The remedy is offered only here — see errorMessages.ts on why.
    expect(message.suggestsAdministrator).toBe(true);
    // Retrying the same credential cannot help; the form should say so by not
    // inviting another attempt.
    expect(message.retryable).toBe(false);
  });

  it("distinguishes a wrong password from an unprovisioned mailbox", () => {
    const translation = translationFor(en);
    const wrongPassword = messageForError(
      new ApiError("invalid-credentials", "", { status: 401 }),
      translation,
    );
    const notProvisioned = messageForError(
      new ApiError("not-provisioned", "", { status: 403 }),
      translation,
    );

    expect(wrongPassword.title).not.toBe(notProvisioned.title);
    expect(wrongPassword.body).not.toBe(notProvisioned.body);
    // A wrong password IS worth retrying; an unprovisioned mailbox is not.
    expect(wrongPassword.retryable).toBe(true);
    expect(wrongPassword.suggestsAdministrator).toBe(false);
  });

  it("names the wait in seconds when the server supplied one", () => {
    const translation = translationFor(en);

    const withSeconds = messageForError(
      new ApiError("rate-limited", "", { status: 429, retryAfterSeconds: 30 }),
      translation,
    );
    expect(withSeconds.body).toContain("30");

    // Without Retry-After it still says to wait, just without a number.
    const withoutSeconds = messageForError(
      new ApiError("rate-limited", "", { status: 429 }),
      translation,
    );
    expect(withoutSeconds.body).not.toContain("30");
    expect(withoutSeconds.body.toLowerCase()).toContain("wait");
  });

  it("pluralises the wait correctly", () => {
    const translation = translationFor(en);
    const one = messageForError(
      new ApiError("rate-limited", "", { retryAfterSeconds: 1 }),
      translation,
    );
    expect(one.body).toContain("1 second");
    expect(one.body).not.toContain("1 seconds");
  });

  it("tells a network failure apart from a server failure", () => {
    const translation = translationFor(en);
    const network = messageForError(new ApiError("network", ""), translation);
    const server = messageForError(new ApiError("server-error", ""), translation);

    expect(network.title).not.toBe(server.title);
    // The network message must point at the user's own connectivity, which is
    // the only thing they can act on.
    expect(network.body.toLowerCase()).toMatch(/connection|vpn|network/);
    // The server message must reassure that the account is fine.
    expect(server.body.toLowerCase()).toContain("account");
  });
});

describe("the locale tables", () => {
  it("have identical key sets", () => {
    // The type system already enforces this; the test states it in a form that
    // fails loudly rather than as a wall of assignability errors.
    expect(Object.keys(es).sort()).toEqual(Object.keys(en).sort());
  });

  it("agree on which keys are functions", () => {
    for (const key of Object.keys(en) as (keyof Strings)[]) {
      expect(typeof es[key], `${key} kind`).toBe(typeof en[key]);
    }
  });

  it("has no empty translation", () => {
    for (const [locale, table] of Object.entries({ en, es })) {
      for (const [key, value] of Object.entries(table)) {
        if (typeof value === "string") {
          expect(value.trim(), `${locale}.${key}`).not.toBe("");
        }
      }
    }
  });
});
