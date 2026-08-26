import { describe, expect, it, vi } from "vitest";

import { JmapClient, type JmapSession } from "./jmap";

/**
 * Regression tests for the same-origin rule.
 *
 * THE BUG THIS PINS: the Session object advertises ABSOLUTE URLs
 * (`https://moov.atmosfera.cloud/jmap/api`). The client originally used them
 * verbatim, which worked in production — where the app is served from that
 * origin — and failed everywhere else with a CORS preflight rejection. It was
 * caught in a real browser: the message list loaded (its request went out
 * before the Session resolved) and opening a message did not, which is exactly
 * the kind of half-working failure a jsdom test would have missed.
 *
 * The rule is that the server's PATH is honoured and the ORIGIN is always
 * ours — which also means a response can never redirect Basic credentials to
 * another host.
 */

function sessionWith(overrides: Partial<JmapSession>): JmapSession {
  return {
    capabilities: {},
    accounts: {},
    primaryAccounts: {},
    username: "moov-test@atmosfera.cloud",
    apiUrl: "https://moov.atmosfera.cloud/jmap/api",
    downloadUrl:
      "https://moov.atmosfera.cloud/jmap/download/{accountId}/{blobId}/{name}?accept={type}",
    uploadUrl: "https://moov.atmosfera.cloud/jmap/upload/{accountId}",
    eventSourceUrl: "https://moov.atmosfera.cloud/jmap/eventsource",
    state: "abc",
    ...overrides,
  };
}

/**
 * A client whose session is already populated.
 *
 * The session is loaded through the real `fetchSession`, so the fixture takes
 * exactly the path a live session does — a test that reached into the private
 * field would still pass if `fetchSession` stopped storing it.
 */
async function clientWithSession(
  session: JmapSession,
  fetchImpl: typeof fetch,
): Promise<JmapClient> {
  let served = false;
  const seeded: typeof fetch = (input, init) => {
    if (!served) {
      served = true;
      return Promise.resolve(
        new Response(JSON.stringify(session), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    }
    return fetchImpl(input, init);
  };

  const client = new JmapClient(
    { username: "u@example.com", password: "p" },
    { fetchImpl: seeded },
  );
  await client.fetchSession();
  return client;
}

/** A fetch spy that records the URLs it is called with. */
function recordingFetch(calls: string[]): typeof fetch {
  return vi.fn((input: RequestInfo | URL) => {
    calls.push(input instanceof Request ? input.url : String(input));
    return Promise.resolve(
      new Response(JSON.stringify({ methodResponses: [], sessionState: "x" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  });
}

describe("the API URL stays same-origin", () => {
  it("does not send requests to the absolute origin the Session advertises", async () => {
    const calls: string[] = [];
    const fetchImpl = recordingFetch(calls);

    const client = await clientWithSession(sessionWith({}), fetchImpl);
    await client.call([["Core/echo", {}, "e"]]);

    expect(calls).toHaveLength(1);
    // The path is honoured...
    expect(calls[0]).toContain("/jmap/api");
    // ...but never the origin, which would be a cross-origin request.
    expect(calls[0]).not.toContain("https://moov.atmosfera.cloud");
  });

  it("honours a path the server changes", async () => {
    const calls: string[] = [];
    const fetchImpl = recordingFetch(calls);

    const client = await clientWithSession(
      sessionWith({ apiUrl: "https://moov.atmosfera.cloud/v2/jmap/api" }),
      fetchImpl,
    );
    await client.call([["Core/echo", {}, "e"]]);
    expect(calls[0]).toContain("/v2/jmap/api");
  });
});

describe("downloadUrlFor", () => {
  it("builds a same-origin URL and expands every template variable", async () => {
    const fetchImpl = recordingFetch([]);
    const client = await clientWithSession(sessionWith({}), fetchImpl);

    const url = client.downloadUrlFor("a1", "deadbeef", "informe.pdf", "application/pdf");

    expect(url).not.toContain("https://moov.atmosfera.cloud");
    expect(url).toContain("/jmap/download/a1/deadbeef/informe.pdf");
    expect(url).not.toContain("{");
  });

  /*
   * The server advertises `?accept={type}` but its handler reads the `type`
   * query parameter. Emitting `accept=` would make every download
   * application/octet-stream. Recorded as a server gap; corrected here.
   */
  it("emits the `type` parameter the server actually reads, not `accept`", async () => {
    const fetchImpl = recordingFetch([]);
    const client = await clientWithSession(sessionWith({}), fetchImpl);

    const url = client.downloadUrlFor("a1", "deadbeef", "x.pdf", "application/pdf");

    expect(url).toContain("type=application%2Fpdf");
    expect(url).not.toContain("accept=");
  });

  it("percent-encodes a filename that would otherwise break the path", async () => {
    const fetchImpl = recordingFetch([]);
    const client = await clientWithSession(sessionWith({}), fetchImpl);

    const url = client.downloadUrlFor("a1", "b1", "informe final/2026.pdf", "text/plain");
    expect(url).not.toContain("final/2026");
  });
});
