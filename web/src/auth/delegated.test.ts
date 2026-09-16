import { beforeEach, describe, expect, it } from "vitest";

import {
  DELEGATED_ROUTE,
  exchangeDelegatedToken,
  logoutDelegatedSession,
  renewDelegatedSession,
  takeDelegatedToken,
} from "./delegated";
import {
  authOf,
  bodyOf,
  failingFetch,
  jsonResponse,
  recordingFetch,
} from "../test/delegatedFetch";

/**
 * The delegated sign-in client (epic M2; contract §3, M2 acceptance (i)).
 *
 * The test that matters most is the first one: the token must be out of the
 * URL before any request is made, and it must never appear in a request URL.
 * That is the entire security property of carrying it in a fragment — a
 * fragment reaches no server, so the moment it ends up in a `fetch` URL or
 * lingers in an address bar the design has been defeated, silently.
 */

const TOKEN = "eyJhbGciOiJFZERTQSIsImtpZCI6ImNwLTIwMjYtMDkifQ.payload.signature";

/** A jsdom window positioned on the delegated landing route. */
function landOn(hash: string, search = ""): void {
  window.history.replaceState({}, "", `${DELEGATED_ROUTE}${search}${hash}`);
}

function sessionBody(overrides: Record<string, unknown> = {}): string {
  return JSON.stringify({
    tokenType: "Bearer",
    sessionToken: "mds1_9vXk2Qm7Lp4Rt8Wz1Yc3Nb6Hd0Jf5Sg2Va7Ke4Mu9Xq1Zr",
    expiresAt: "2026-10-22T21:40:55.310Z",
    renewAfter: "2026-10-22T20:40:55.310Z",
    absoluteExpiresAt: "2026-10-29T09:40:55.310Z",
    account: { address: "expo@eventos.example.test", name: "Expo" },
    readOnly: false,
    jmap: { sessionUrl: "/.well-known/jmap" },
    ...overrides,
  });
}

describe("takeDelegatedToken", () => {
  beforeEach(() => {
    window.history.replaceState({}, "", "/");
  });

  it("reads the token and erases the fragment", () => {
    landOn(`#token=${TOKEN}`);
    expect(window.location.hash).not.toBe("");

    const token = takeDelegatedToken(window);

    expect(token).toBe(TOKEN);
    // Acceptance (i), first half.
    expect(window.location.hash).toBe("");
    expect(window.location.href).not.toContain(TOKEN);
  });

  it("erases the fragment even when it carries no token", () => {
    landOn("#something-else");
    expect(takeDelegatedToken(window)).toBeUndefined();
    expect(window.location.hash).toBe("");
  });

  it("keeps the path and query, dropping only the fragment", () => {
    landOn(`#token=${TOKEN}`, "?next=%2Fmail%2Finbox");
    takeDelegatedToken(window);
    expect(window.location.pathname).toBe(DELEGATED_ROUTE);
    expect(window.location.search).toBe("?next=%2Fmail%2Finbox");
  });

  it("replaces the history entry rather than pushing one", () => {
    landOn(`#token=${TOKEN}`);
    const before = window.history.length;
    takeDelegatedToken(window);
    // A pushState would grow history and leave Back pointing at the URL that
    // still carries the token.
    expect(window.history.length).toBe(before);
  });

  it("still returns the token when replaceState is refused", () => {
    landOn(`#token=${TOKEN}`);
    const fake = {
      location: window.location,
      history: {
        state: null,
        replaceState: () => {
          throw new Error("blocked");
        },
      },
    } as unknown as Window;
    // Refusing to sign the user in because the address bar could not be
    // tidied would be the worse failure.
    expect(takeDelegatedToken(fake)).toBe(TOKEN);
  });
});

describe("exchangeDelegatedToken", () => {
  it("never puts the token in a URL — it goes in the body (acceptance (i))", async () => {
    const { fetchImpl, seen } = recordingFetch(() => jsonResponse(200, sessionBody()));

    await exchangeDelegatedToken(TOKEN, fetchImpl);

    expect(seen).toHaveLength(1);
    const call = seen[0];
    if (call === undefined) throw new Error("no request was recorded");
    // The whole point: not in the path, not in a query parameter.
    expect(call.url).not.toContain(TOKEN);
    expect(call.url).toBe("/auth/delegated/exchange");
    expect(bodyOf(call)).toContain(TOKEN);
    expect(call.init?.method).toBe("POST");
    // Explicit credentials only; no ambient cookies.
    expect(call.init?.credentials).toBe("omit");
  });

  it("returns the session on 200", async () => {
    const { fetchImpl } = recordingFetch(() => jsonResponse(200, sessionBody()));

    const outcome = await exchangeDelegatedToken(TOKEN, fetchImpl);

    expect(outcome.kind).toBe("ok");
    if (outcome.kind !== "ok") return;
    expect(outcome.session.sessionToken).toMatch(/^mds1_/);
    expect(outcome.session.account.address).toBe("expo@eventos.example.test");
    expect(outcome.session.readOnly).toBe(false);
  });

  it("carries readOnly through when the server sets it", async () => {
    const { fetchImpl } = recordingFetch(() =>
      jsonResponse(200, sessionBody({ readOnly: true })),
    );
    const outcome = await exchangeDelegatedToken(TOKEN, fetchImpl);
    expect(outcome.kind === "ok" && outcome.session.readOnly).toBe(true);
  });

  it("treats a 200 with no session token as a broken server, not a bad link", async () => {
    const { fetchImpl } = recordingFetch(() =>
      jsonResponse(200, JSON.stringify({ tokenType: "Bearer" })),
    );
    expect((await exchangeDelegatedToken(TOKEN, fetchImpl)).kind).toBe("unavailable");
  });

  it("maps 401 to the single invalid outcome, whatever the body says", async () => {
    for (const body of [
      JSON.stringify({ type: "about:blank", status: 401, detail: "invalid delegated token" }),
      "",
    ]) {
      const { fetchImpl } = recordingFetch(() => jsonResponse(401, body));
      expect((await exchangeDelegatedToken(TOKEN, fetchImpl)).kind).toBe("invalid");
    }
  });

  it("splits the 403s by the problem document's code", async () => {
    const cases: { code: string | undefined; want: string }[] = [
      { code: "notProvisioned", want: "not-provisioned" },
      { code: "suspended", want: "unusable" },
      { code: "disabled", want: "unusable" },
      // A code we have never seen must not produce a blank screen.
      { code: "somethingNew", want: "unusable" },
      { code: undefined, want: "unusable" },
    ];
    for (const c of cases) {
      const body = JSON.stringify({
        type: "about:blank",
        status: 403,
        ...(c.code === undefined ? {} : { code: c.code }),
        detail: "no",
      });
      const { fetchImpl } = recordingFetch(() => jsonResponse(403, body));
      expect((await exchangeDelegatedToken(TOKEN, fetchImpl)).kind).toBe(c.want);
    }
  });

  it("maps 404 to not-configured — the host has no issuer", async () => {
    const { fetchImpl } = recordingFetch(() =>
      jsonResponse(404, JSON.stringify({ type: "about:blank", status: 404, detail: "not found" })),
    );
    expect((await exchangeDelegatedToken(TOKEN, fetchImpl)).kind).toBe("not-configured");
  });

  it("reads Retry-After off a 429 and a 503", async () => {
    for (const status of [429, 503]) {
      const { fetchImpl } = recordingFetch(() =>
        jsonResponse(status, JSON.stringify({ reason: "wait" }), { "Retry-After": "30" }),
      );
      const outcome = await exchangeDelegatedToken(TOKEN, fetchImpl);
      expect(outcome.kind).toBe("unavailable");
      if (outcome.kind !== "unavailable") return;
      expect(outcome.retryAfterSeconds).toBe(30);
    }
  });

  it("treats a transport failure as unavailable, never as a bad link", async () => {
    expect((await exchangeDelegatedToken(TOKEN, failingFetch())).kind).toBe("unavailable");
  });
});

describe("renewDelegatedSession", () => {
  it("authenticates with the current token in the header, not in the URL", async () => {
    const { fetchImpl, seen } = recordingFetch(() => jsonResponse(200, sessionBody()));

    await renewDelegatedSession("mds1_old", fetchImpl);

    const call = seen[0];
    if (call === undefined) throw new Error("no request was recorded");
    expect(call.url).toBe("/auth/delegated/renew");
    expect(call.url).not.toContain("mds1_old");
    expect(authOf(call)).toBe("Bearer mds1_old");
    // No body, and therefore no Content-Type claiming there is one.
    expect(call.init?.body).toBeUndefined();
    expect((call.init?.headers as Record<string, string>)["Content-Type"]).toBeUndefined();
  });

  it("is 401 past the absolute lifetime (acceptance (e), client half)", async () => {
    const { fetchImpl } = recordingFetch(() =>
      jsonResponse(401, JSON.stringify({ status: 401, detail: "invalid delegated token" })),
    );
    expect((await renewDelegatedSession("mds1_old", fetchImpl)).kind).toBe("invalid");
  });
});

describe("logoutDelegatedSession", () => {
  it("posts the session token as a header and never throws", async () => {
    const { fetchImpl, seen } = recordingFetch(() => new Response(null, { status: 204 }));

    await logoutDelegatedSession("mds1_live", fetchImpl);

    const call = seen[0];
    if (call === undefined) throw new Error("no request was recorded");
    expect(call.url).toBe("/auth/delegated/logout");
    expect(call.url).not.toContain("mds1_live");
    expect(authOf(call)).toBe("Bearer mds1_live");
  });

  it("swallows a transport failure — sign-out must not fail on the user", async () => {
    await expect(logoutDelegatedSession("mds1_live", failingFetch())).resolves.toBeUndefined();
  });
});
