import { describe, expect, it, vi } from "vitest";

import {
  authorizationHeader,
  encodeBasicCredentials,
  isBearerCredential,
  JmapClient,
  SESSION_ENDPOINT,
} from "./jmap";
import {
  authOf,
  jsonResponse,
  recordingFetch,
  type RecordedRequest,
} from "../test/delegatedFetch";

/**
 * The client under a delegated session (epic M2, contract §3.4).
 *
 * §3.4: "The token is accepted wherever Basic is accepted today:
 * /.well-known/jmap, /jmap/api, /jmap/upload/*, /jmap/token (so push and
 * downloads keep working) …". The Go suite pins the SERVER'S half of that set
 * (TestBearerAcceptedExactlyWhereBasicIs). These tests pin the CLIENT'S: that
 * the same client, constructed from a session token, sends `Bearer` on every
 * one of those routes — in particular on /jmap/token, because if minting
 * stops working under a bearer session then push and attachment downloads
 * break SILENTLY, with no error the user could report.
 */

const SESSION_TOKEN = "mds1_9vXk2Qm7Lp4Rt8Wz1Yc3Nb6Hd0Jf5Sg2Va7Ke4Mu9Xq1Zr";

function first(seen: readonly RecordedRequest[]): RecordedRequest {
  const call = seen[0];
  if (call === undefined) throw new Error("no request was recorded");
  return call;
}

describe("authorizationHeader", () => {
  it("builds Basic for a password pair and Bearer for a session token", () => {
    expect(authorizationHeader({ username: "a@example.test", password: "pw" })).toBe(
      encodeBasicCredentials({ username: "a@example.test", password: "pw" }),
    );
    expect(authorizationHeader({ token: SESSION_TOKEN })).toBe(`Bearer ${SESSION_TOKEN}`);
  });

  it("discriminates the two without inspecting the header it would build", () => {
    expect(isBearerCredential({ token: SESSION_TOKEN })).toBe(true);
    expect(isBearerCredential({ username: "a@example.test", password: "pw" })).toBe(false);
  });

  it("never encodes a session token as a password", () => {
    // The failure this rules out is quiet and total: base64 of ":mds1_…"
    // would be accepted by no server and rejected as a bad password, sending
    // the user to a login form they cannot satisfy.
    const header = authorizationHeader({ token: SESSION_TOKEN });
    expect(header.startsWith("Bearer ")).toBe(true);
    expect(header).not.toContain("Basic");
  });
});

describe("JmapClient under a bearer credential", () => {
  it("sends Bearer on the Session endpoint", async () => {
    const { fetchImpl, seen } = recordingFetch(() =>
      jsonResponse(200, JSON.stringify({ state: "s1" })),
    );
    await new JmapClient({ token: SESSION_TOKEN }, { fetchImpl }).fetchSession();

    const call = first(seen);
    expect(call.url).toBe(SESSION_ENDPOINT);
    expect(authOf(call)).toBe(`Bearer ${SESSION_TOKEN}`);
    // Never in the URL: the token-in-query set is exactly the scoped push and
    // blob tokens, and a session token there is refused by construction.
    expect(call.url).not.toContain(SESSION_TOKEN);
  });

  it("keeps minting push and blob tokens — the half that fails silently", async () => {
    const { fetchImpl, seen } = recordingFetch(() =>
      jsonResponse(200, JSON.stringify({ tokens: { push: { token: "p1", expiresIn: 600 } } })),
    );
    const client = new JmapClient({ token: SESSION_TOKEN }, { fetchImpl });

    const tokens = await client.mintTokens(["push", "blob"]);

    expect(tokens.push?.token).toBe("p1");
    expect(first(seen).url).toBe("/jmap/token");
    expect(authOf(first(seen))).toBe(`Bearer ${SESSION_TOKEN}`);
  });

  it("sends Bearer on the JMAP API route too", async () => {
    const { fetchImpl, seen } = recordingFetch(() =>
      jsonResponse(200, JSON.stringify({ methodResponses: [], sessionState: "s1" })),
    );
    const client = new JmapClient({ token: SESSION_TOKEN }, { fetchImpl });

    await client.call([["Mailbox/get", { accountId: "a1" }, "c0"]]);

    const api = seen.find((s) => s.url.includes("/jmap/api"));
    expect(api).toBeDefined();
    if (api === undefined) return;
    expect(authOf(api)).toBe(`Bearer ${SESSION_TOKEN}`);
  });

  it("never attaches ambient cookies, whichever scheme it carries", async () => {
    for (const credential of [
      { token: SESSION_TOKEN },
      { username: "a@example.test", password: "pw" },
    ]) {
      const { fetchImpl, seen } = recordingFetch(() =>
        jsonResponse(200, JSON.stringify({ state: "s1" })),
      );
      await new JmapClient(credential, { fetchImpl }).fetchSession();
      expect(first(seen).init?.credentials).toBe("omit");
    }
  });
});

describe("the 401 hook", () => {
  function refusingFetch(status: number): typeof fetch {
    return recordingFetch(() => jsonResponse(status, JSON.stringify({ status, detail: "no" })))
      .fetchImpl;
  }

  it("fires once on a 401, before the error is thrown", async () => {
    const onUnauthorized = vi.fn();
    const client = new JmapClient(
      { token: SESSION_TOKEN },
      { fetchImpl: refusingFetch(401), onUnauthorized },
    );

    await expect(client.fetchSession()).rejects.toThrow();
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
  });

  it("does NOT fire on a 403 — not-provisioned has its own screen", async () => {
    const onUnauthorized = vi.fn();
    const client = new JmapClient(
      { token: SESSION_TOKEN },
      { fetchImpl: refusingFetch(403), onUnauthorized },
    );

    await expect(client.fetchSession()).rejects.toThrow();
    // Tearing the session down here would replace a precise explanation with
    // a vaguer one.
    expect(onUnauthorized).not.toHaveBeenCalled();
  });

  it("does not fire on success", async () => {
    const onUnauthorized = vi.fn();
    const { fetchImpl } = recordingFetch(() =>
      jsonResponse(200, JSON.stringify({ state: "s1" })),
    );
    await new JmapClient({ token: SESSION_TOKEN }, { fetchImpl, onUnauthorized }).fetchSession();
    expect(onUnauthorized).not.toHaveBeenCalled();
  });
});
