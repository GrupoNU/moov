import { describe, expect, it, vi } from "vitest";

import { JmapClient, withAccessToken, type JmapSession } from "./jmap";

/**
 * The client half of the scoped-token contract: the wire shapes of mint and
 * revoke, the eventsource template expansion, and the one place the token is
 * attached to a URL.
 */

function sessionWith(overrides: Partial<JmapSession> = {}): JmapSession {
  return {
    capabilities: {},
    accounts: {},
    primaryAccounts: {},
    username: "moov-test@atmosfera.cloud",
    apiUrl: "https://moov.atmosfera.cloud/jmap/api",
    downloadUrl:
      "https://moov.atmosfera.cloud/jmap/download/{accountId}/{blobId}/{name}?type={type}",
    uploadUrl: "https://moov.atmosfera.cloud/jmap/upload/{accountId}",
    eventSourceUrl:
      "https://moov.atmosfera.cloud/jmap/eventsource?types={types}&closeafter={closeafter}&ping={ping}",
    state: "abc",
    ...overrides,
  };
}

interface RecordedCall {
  url: string;
  body: unknown;
}

function clientWith(
  responseBody: unknown,
  calls: RecordedCall[],
): JmapClient {
  const fetchImpl: typeof fetch = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({
      url: input instanceof Request ? input.url : String(input),
      body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
    });
    return Promise.resolve(
      new Response(JSON.stringify(responseBody), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  });
  return new JmapClient({ username: "u@example.com", password: "p" }, { fetchImpl });
}

describe("mintTokens", () => {
  it("POSTs the scopes and returns the minted map", async () => {
    const calls: RecordedCall[] = [];
    const client = clientWith(
      {
        tokens: {
          push: { token: "mt1.p.sig", expiresIn: 600 },
          blob: { token: "mt1.b.sig", expiresIn: 600 },
        },
      },
      calls,
    );

    const tokens = await client.mintTokens(["push", "blob"]);

    expect(calls[0]?.url).toBe("/jmap/token");
    expect(calls[0]?.body).toEqual({ scopes: ["push", "blob"] });
    expect(tokens.push?.token).toBe("mt1.p.sig");
    expect(tokens.blob?.expiresIn).toBe(600);
  });

  it("returns an empty map for a malformed response", async () => {
    const client = clientWith({}, []);
    await expect(client.mintTokens(["push"])).resolves.toEqual({});
  });
});

describe("revokeTokens", () => {
  it("POSTs the tokens to the revoke endpoint", async () => {
    const calls: RecordedCall[] = [];
    const client = clientWith({}, calls);

    await client.revokeTokens(["mt1.a.b", "mt1.c.d"]);

    expect(calls[0]?.url).toBe("/jmap/token/revoke");
    expect(calls[0]?.body).toEqual({ tokens: ["mt1.a.b", "mt1.c.d"] });
  });

  it("skips the request entirely for an empty list", async () => {
    const calls: RecordedCall[] = [];
    const client = clientWith({}, calls);
    await client.revokeTokens([]);
    expect(calls).toHaveLength(0);
  });
});

describe("eventSourceUrlFor", () => {
  it("expands the advertised template same-origin with the three §7.3 variables", async () => {
    const calls: RecordedCall[] = [];
    const client = clientWith(sessionWith(), calls);
    await client.fetchSession();

    const url = client.eventSourceUrlFor();

    // Same-origin: the advertised absolute URL is reduced to its path — an
    // EventSource must never carry a token to an origin a response named.
    expect(url).toBe("/jmap/eventsource?types=*&closeafter=no&ping=30");
  });

  it("falls back to the known path when no session is loaded", () => {
    const client = clientWith({}, []);
    expect(client.eventSourceUrlFor({ types: "Email", ping: 60 })).toBe(
      "/jmap/eventsource?types=Email&closeafter=no&ping=60",
    );
  });
});

describe("withAccessToken", () => {
  it("appends with ? or & as the URL requires, encoded", () => {
    expect(withAccessToken("/jmap/eventsource", "a.b+c")).toBe(
      "/jmap/eventsource?access_token=a.b%2Bc",
    );
    expect(withAccessToken("/jmap/download/a/b/c?type=image%2Fpng", "tok")).toBe(
      "/jmap/download/a/b/c?type=image%2Fpng&access_token=tok",
    );
  });
});
