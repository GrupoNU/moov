import { act, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "../api/errors";
import type { AuthCredential, JmapClient, JmapSession } from "../api/jmap";
import { AuthProvider, useAuth } from "./AuthProvider";
import { DELEGATED_ROUTE } from "./delegated";
import { jsonResponse, recordingFetch } from "../test/delegatedFetch";
import { loadSession, type SessionStorageLike } from "./session";

/**
 * Delegated sign-in, end to end through the provider (epic M2, contract §3.7
 * and M2 acceptance (i)).
 *
 * These are the tests that pin the CLIENT'S half of the contract, the half no
 * Go test can reach:
 *
 *   - the fragment is gone before any network call, and the token is in no
 *     request URL (acceptance (i));
 *   - a 401 under a bearer session NEVER renders the login form, which is the
 *     one thing §3.7 forbids by name;
 *   - a 403 notProvisioned stays distinct, so the existing screen can render;
 *   - the JMAP layer is handed the session token, never a password.
 */

const TOKEN = "eyJhbGciOiJFZERTQSJ9.payload.sig";
const SESSION_TOKEN = "mds1_9vXk2Qm7Lp4Rt8Wz1Yc3Nb6Hd0Jf5Sg2Va7Ke4Mu9Xq1Zr";

const FAKE_SESSION = {
  capabilities: {},
  accounts: {},
  primaryAccounts: { "urn:ietf:params:jmap:mail": "a1" },
  username: "expo@eventos.example.test",
  apiUrl: "/jmap/api",
  downloadUrl: "/jmap/download/{accountId}/{blobId}",
  uploadUrl: "/jmap/upload/{accountId}",
  eventSourceUrl: "/jmap/eventsource",
  state: "s1",
} as unknown as JmapSession;

function memoryStorage(seed?: string): SessionStorageLike {
  let value = seed;
  return {
    getItem: () => value ?? null,
    setItem: (_key: string, next: string) => {
      value = next;
    },
    removeItem: () => {
      value = undefined;
    },
  };
}

function exchangeBody(overrides: Record<string, unknown> = {}): string {
  return JSON.stringify({
    tokenType: "Bearer",
    sessionToken: SESSION_TOKEN,
    expiresAt: new Date(Date.now() + 12 * 3_600_000).toISOString(),
    renewAfter: new Date(Date.now() + 11 * 3_600_000).toISOString(),
    absoluteExpiresAt: new Date(Date.now() + 7 * 24 * 3_600_000).toISOString(),
    account: { address: "expo@eventos.example.test", name: "Expo" },
    readOnly: false,
    ...overrides,
  });
}

/** Renders the auth state as text, plus a button a test can press. */
function Probe(): React.JSX.Element {
  const { state, onUnauthorized, credential, readOnly } = useAuth();
  return (
    <div>
      <span data-testid="status">{state.status}</span>
      <span data-testid="reason">{state.status === "link-dead" ? state.reason.kind : ""}</span>
      <span data-testid="readonly">{String(readOnly)}</span>
      <span data-testid="credential">
        {credential === undefined
          ? "none"
          : "token" in credential
            ? `bearer:${credential.token}`
            : `basic:${credential.username}`}
      </span>
      <button type="button" onClick={onUnauthorized}>
        force401
      </button>
    </div>
  );
}

function land(hash: string): void {
  window.history.replaceState({}, "", `${DELEGATED_ROUTE}${hash}`);
}

/**
 * An `authenticate` stub that records the credential it was handed.
 *
 * Typed as `never` at the prop, which is how the existing login tests pass a
 * stub: the real signature returns a live `JmapClient`, and building one here
 * would test the client rather than the provider.
 */
function capturing(seen: AuthCredential[]): never {
  const impl = (credential: AuthCredential): Promise<unknown> => {
    seen.push(credential);
    return Promise.resolve({ client: {} as JmapClient, session: FAKE_SESSION });
  };
  return impl as never;
}

/** An `authenticate` that always succeeds. */
function succeeding(): never {
  return (() =>
    Promise.resolve({ client: {} as JmapClient, session: FAKE_SESSION })) as never;
}

describe("the delegated landing route", () => {
  beforeEach(() => {
    window.history.replaceState({}, "", "/");
  });

  it("erases the fragment before any network call, and never sends the token in a URL", async () => {
    land(`#token=${TOKEN}`);

    let hashWhenFetched: string | undefined;
    const { fetchImpl, seen } = recordingFetch(() => {
      hashWhenFetched ??= window.location.hash;
      return jsonResponse(200, exchangeBody());
    });

    render(
      <AuthProvider
        storage={memoryStorage()}
        fetchImpl={fetchImpl}
        authenticateImpl={succeeding()}
      >
        <Probe />
      </AuthProvider>,
    );

    await waitFor(() => {
      expect(screen.getByTestId("status").textContent).toBe("authenticated");
    });

    // Acceptance (i), both halves.
    expect(hashWhenFetched).toBe("");
    expect(window.location.hash).toBe("");
    for (const request of seen) expect(request.url).not.toContain(TOKEN);
  });

  it("stores the session and hands the bearer credential to the HTTP layer", async () => {
    land(`#token=${TOKEN}`);
    const storage = memoryStorage();
    const { fetchImpl } = recordingFetch(() => jsonResponse(200, exchangeBody()));

    render(
      <AuthProvider storage={storage} fetchImpl={fetchImpl} authenticateImpl={succeeding()}>
        <Probe />
      </AuthProvider>,
    );

    await waitFor(() => {
      expect(screen.getByTestId("credential").textContent).toBe(`bearer:${SESSION_TOKEN}`);
    });

    const stored = loadSession(storage);
    expect(stored?.kind).toBe("bearer");
    expect(stored?.kind === "bearer" && stored.token).toBe(SESSION_TOKEN);
  });

  it("authenticates JMAP with the session token, not with a password", async () => {
    land(`#token=${TOKEN}`);
    const seen: AuthCredential[] = [];
    const { fetchImpl } = recordingFetch(() => jsonResponse(200, exchangeBody()));

    render(
      <AuthProvider
        storage={memoryStorage()}
        fetchImpl={fetchImpl}
        authenticateImpl={capturing(seen)}
      >
        <Probe />
      </AuthProvider>,
    );

    await waitFor(() => {
      expect(seen).toHaveLength(1);
    });
    expect(seen[0]).toEqual({ token: SESSION_TOKEN });
  });

  it("carries readOnly from the exchange response", async () => {
    land(`#token=${TOKEN}`);
    const { fetchImpl } = recordingFetch(() =>
      jsonResponse(200, exchangeBody({ readOnly: true })),
    );

    render(
      <AuthProvider
        storage={memoryStorage()}
        fetchImpl={fetchImpl}
        authenticateImpl={succeeding()}
      >
        <Probe />
      </AuthProvider>,
    );

    await waitFor(() => {
      expect(screen.getByTestId("readonly").textContent).toBe("true");
    });
  });

  it("shows the dead-link state — never the login form — for a refused token", async () => {
    land(`#token=${TOKEN}`);
    const { fetchImpl } = recordingFetch(() =>
      jsonResponse(401, JSON.stringify({ status: 401, detail: "invalid delegated token" })),
    );

    render(
      <AuthProvider storage={memoryStorage()} fetchImpl={fetchImpl}>
        <Probe />
      </AuthProvider>,
    );

    await waitFor(() => {
      expect(screen.getByTestId("status").textContent).toBe("link-dead");
    });
    // The thing §3.7 forbids by name.
    expect(screen.getByTestId("status").textContent).not.toBe("anonymous");
    expect(screen.getByTestId("reason").textContent).toBe("invalid");
  });

  it("reports notProvisioned distinctly, so the existing screen can render", async () => {
    land(`#token=${TOKEN}`);
    const { fetchImpl } = recordingFetch(() =>
      jsonResponse(403, JSON.stringify({ status: 403, code: "notProvisioned" })),
    );

    render(
      <AuthProvider storage={memoryStorage()} fetchImpl={fetchImpl}>
        <Probe />
      </AuthProvider>,
    );

    await waitFor(() => {
      expect(screen.getByTestId("reason").textContent).toBe("not-provisioned");
    });
  });

  it("reports a suspended account as unusable", async () => {
    land(`#token=${TOKEN}`);
    const { fetchImpl } = recordingFetch(() =>
      jsonResponse(403, JSON.stringify({ status: 403, code: "suspended" })),
    );

    render(
      <AuthProvider storage={memoryStorage()} fetchImpl={fetchImpl}>
        <Probe />
      </AuthProvider>,
    );

    await waitFor(() => {
      expect(screen.getByTestId("reason").textContent).toBe("unusable");
    });
  });

  it("treats the landing route with no token as a dead link, not as a login page", async () => {
    window.history.replaceState({}, "", DELEGATED_ROUTE);
    render(
      <AuthProvider storage={memoryStorage()}>
        <Probe />
      </AuthProvider>,
    );
    await waitFor(() => {
      expect(screen.getByTestId("status").textContent).toBe("link-dead");
    });
  });
});

describe("a 401 during a live session", () => {
  beforeEach(() => {
    window.history.replaceState({}, "", "/");
  });

  it("drops a bearer session to the dead-link screen, never to the form", async () => {
    land(`#token=${TOKEN}`);
    const storage = memoryStorage();
    const { fetchImpl } = recordingFetch(() => jsonResponse(200, exchangeBody()));

    render(
      <AuthProvider storage={storage} fetchImpl={fetchImpl} authenticateImpl={succeeding()}>
        <Probe />
      </AuthProvider>,
    );
    await waitFor(() => {
      expect(screen.getByTestId("status").textContent).toBe("authenticated");
    });

    act(() => {
      screen.getByRole("button", { name: "force401" }).click();
    });

    await waitFor(() => {
      expect(screen.getByTestId("status").textContent).toBe("link-dead");
    });
    // The credential is gone from storage too, so a reload does not retry it.
    expect(loadSession(storage)).toBeUndefined();
  });

  it("drops a Basic session to the login form, as it always did", async () => {
    const storage = memoryStorage(
      JSON.stringify({ username: "a@example.test", password: "pw" }),
    );

    render(
      <AuthProvider storage={storage} authenticateImpl={succeeding()}>
        <Probe />
      </AuthProvider>,
    );
    await waitFor(() => {
      expect(screen.getByTestId("status").textContent).toBe("authenticated");
    });

    act(() => {
      screen.getByRole("button", { name: "force401" }).click();
    });

    await waitFor(() => {
      expect(screen.getByTestId("status").textContent).toBe("anonymous");
    });
  });
});

describe("restoring a stored bearer session", () => {
  beforeEach(() => {
    window.history.replaceState({}, "", "/");
  });

  function storedBearer(overrides: Record<string, unknown> = {}): SessionStorageLike {
    return memoryStorage(
      JSON.stringify({
        kind: "bearer",
        username: "expo@eventos.example.test",
        token: SESSION_TOKEN,
        expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
        renewAfter: new Date(Date.now() + 1_800_000).toISOString(),
        absoluteExpiresAt: new Date(Date.now() + 6 * 24 * 3_600_000).toISOString(),
        readOnly: false,
        ...overrides,
      }),
    );
  }

  it("revalidates it against JMAP rather than trusting it", async () => {
    const seen: AuthCredential[] = [];
    render(
      <AuthProvider storage={storedBearer()} authenticateImpl={capturing(seen)}>
        <Probe />
      </AuthProvider>,
    );
    await waitFor(() => {
      expect(screen.getByTestId("status").textContent).toBe("authenticated");
    });
    expect(seen[0]).toEqual({ token: SESSION_TOKEN });
  });

  it("goes straight to the dead-link screen past the absolute lifetime, with no round trip", async () => {
    const authenticateImpl = vi.fn();
    const { fetchImpl, seen } = recordingFetch(() => jsonResponse(200, exchangeBody()));
    render(
      <AuthProvider
        storage={storedBearer({
          absoluteExpiresAt: new Date(Date.now() - 1_000).toISOString(),
        })}
        fetchImpl={fetchImpl}
        authenticateImpl={authenticateImpl as never}
      >
        <Probe />
      </AuthProvider>,
    );
    await waitFor(() => {
      expect(screen.getByTestId("status").textContent).toBe("link-dead");
    });
    // No request whose only possible answer is a 401.
    expect(authenticateImpl).not.toHaveBeenCalled();
    expect(seen).toHaveLength(0);
  });

  it("renews before adopting when the window lapsed while the tab was closed", async () => {
    const renewed = SESSION_TOKEN.replace("9vXk", "FRESH");
    const { fetchImpl, seen: requests } = recordingFetch(() =>
      jsonResponse(200, exchangeBody({ sessionToken: renewed })),
    );

    const seen: AuthCredential[] = [];
    render(
      <AuthProvider
        storage={storedBearer({ renewAfter: new Date(Date.now() - 1_000).toISOString() })}
        fetchImpl={fetchImpl}
        authenticateImpl={capturing(seen)}
      >
        <Probe />
      </AuthProvider>,
    );

    await waitFor(() => {
      expect(screen.getByTestId("status").textContent).toBe("authenticated");
    });
    expect(requests.map((r) => r.url)).toContain("/auth/delegated/renew");
    // The adopted credential is the RENEWED one, not the one that was due.
    expect(seen[0]).toEqual({ token: renewed });
  });

  it("keeps the old token when a renewal is merely unavailable", async () => {
    // A transient hiccup must not sign anyone out: the old token still has
    // life in it.
    const { fetchImpl } = recordingFetch(() => jsonResponse(503, JSON.stringify({})));

    const seen: AuthCredential[] = [];
    render(
      <AuthProvider
        storage={storedBearer({ renewAfter: new Date(Date.now() - 1_000).toISOString() })}
        fetchImpl={fetchImpl}
        authenticateImpl={capturing(seen)}
      >
        <Probe />
      </AuthProvider>,
    );

    await waitFor(() => {
      expect(screen.getByTestId("status").textContent).toBe("authenticated");
    });
    expect(seen[0]).toEqual({ token: SESSION_TOKEN });
  });

  it("shows the dead-link screen when the revalidation itself is refused", async () => {
    const refusing = (() =>
      Promise.reject(new ApiError("invalid-credentials", "nope", { status: 401 }))) as never;
    render(
      <AuthProvider storage={storedBearer()} authenticateImpl={refusing}>
        <Probe />
      </AuthProvider>,
    );
    await waitFor(() => {
      expect(screen.getByTestId("status").textContent).toBe("link-dead");
    });
  });
});
