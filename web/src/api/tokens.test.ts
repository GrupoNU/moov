import { describe, expect, it, vi } from "vitest";

import {
  REFRESH_FRACTION,
  RETRY_DELAY_MS,
  TokenManager,
  type TokenClient,
} from "./tokens";
import type { MintedTokens, TokenScope } from "./jmap";

/**
 * TokenManager owns the mint → refresh → revoke lifecycle. These tests drive
 * it with a stub client and injected timers, so every timing claim in the
 * design — refresh at 75% of the shortest lifetime, retry on failure, revoke
 * on stop — is pinned rather than assumed.
 */

interface FakeTimers {
  readonly pending: { fn: () => void; ms: number }[];
  fire(): Promise<void>;
}

function fakeTimers(): FakeTimers & {
  setTimeoutImpl: (fn: () => void, ms: number) => unknown;
  clearTimeoutImpl: (id: unknown) => void;
} {
  const pending: { fn: () => void; ms: number }[] = [];
  return {
    pending,
    async fire(): Promise<void> {
      const next = pending.shift();
      next?.fn();
      // Let the mint promise chain settle.
      await Promise.resolve();
      await Promise.resolve();
    },
    setTimeoutImpl(fn, ms) {
      const entry = { fn, ms };
      pending.push(entry);
      return entry;
    },
    clearTimeoutImpl(id) {
      const index = pending.indexOf(id as { fn: () => void; ms: number });
      if (index >= 0) pending.splice(index, 1);
    },
  };
}

function mintedPair(suffix: string, expiresIn = 600): MintedTokens {
  return {
    push: { token: `push-${suffix}`, expiresIn },
    blob: { token: `blob-${suffix}`, expiresIn },
  };
}

function stubClient(
  mint: (scopes: readonly TokenScope[]) => Promise<MintedTokens>,
): TokenClient & { revoked: string[][] } {
  const revoked: string[][] = [];
  return {
    revoked,
    mintTokens: (scopes) => mint(scopes),
    revokeTokens: (tokens) => {
      revoked.push([...tokens]);
      return Promise.resolve();
    },
  };
}

describe("TokenManager", () => {
  it("mints both scopes on start and exposes them", async () => {
    const client = stubClient(() => Promise.resolve(mintedPair("1")));
    const timers = fakeTimers();
    const manager = new TokenManager(client, { ...timers });

    await manager.start();

    expect(manager.getToken("push")).toBe("push-1");
    expect(manager.getToken("blob")).toBe("blob-1");
  });

  it("notifies onTokens with every fresh set", async () => {
    const seen: MintedTokens[] = [];
    const client = stubClient(() => Promise.resolve(mintedPair("1")));
    const timers = fakeTimers();
    const manager = new TokenManager(client, {
      ...timers,
      onTokens: (tokens) => seen.push(tokens),
    });

    await manager.start();
    expect(seen).toHaveLength(1);
    expect(seen[0]?.push?.token).toBe("push-1");
  });

  it("schedules the refresh at REFRESH_FRACTION of the shortest lifetime", async () => {
    const client = stubClient(() =>
      Promise.resolve({
        push: { token: "p", expiresIn: 600 },
        blob: { token: "b", expiresIn: 300 }, // the shorter one governs
      }),
    );
    const timers = fakeTimers();
    const manager = new TokenManager(client, { ...timers });

    await manager.start();

    expect(timers.pending).toHaveLength(1);
    expect(timers.pending[0]?.ms).toBe(Math.floor(300 * 1000 * REFRESH_FRACTION));
  });

  it("re-mints when the refresh timer fires", async () => {
    let round = 0;
    const client = stubClient(() => {
      round += 1;
      return Promise.resolve(mintedPair(String(round)));
    });
    const timers = fakeTimers();
    const manager = new TokenManager(client, { ...timers });

    await manager.start();
    expect(manager.getToken("push")).toBe("push-1");

    await timers.fire();
    expect(manager.getToken("push")).toBe("push-2");
    // And the next refresh is scheduled in turn.
    expect(timers.pending).toHaveLength(1);
  });

  it("keeps the old tokens and retries soon when a mint fails", async () => {
    let fail = false;
    let round = 0;
    const client = stubClient(() => {
      if (fail) return Promise.reject(new Error("server restarting"));
      round += 1;
      return Promise.resolve(mintedPair(String(round)));
    });
    const timers = fakeTimers();
    const manager = new TokenManager(client, { ...timers });

    await manager.start();
    fail = true;
    await timers.fire(); // the refresh attempt fails

    // The old token is still held — a transient failure must not sign the
    // user out of push — and a retry is scheduled at the short delay.
    expect(manager.getToken("push")).toBe("push-1");
    expect(timers.pending).toHaveLength(1);
    expect(timers.pending[0]?.ms).toBe(RETRY_DELAY_MS);

    fail = false;
    await timers.fire();
    expect(manager.getToken("push")).toBe("push-2");
  });

  it("refreshNow mints immediately (the dead-EventSource healing path)", async () => {
    let round = 0;
    const client = stubClient(() => {
      round += 1;
      return Promise.resolve(mintedPair(String(round)));
    });
    const timers = fakeTimers();
    const manager = new TokenManager(client, { ...timers });

    await manager.start();
    await manager.refreshNow();
    expect(manager.getToken("push")).toBe("push-2");
    // The stale scheduled refresh was replaced, not duplicated.
    expect(timers.pending).toHaveLength(1);
  });

  it("stop revokes the held tokens and cancels the cycle", async () => {
    const client = stubClient(() => Promise.resolve(mintedPair("1")));
    const timers = fakeTimers();
    const manager = new TokenManager(client, { ...timers });

    await manager.start();
    await manager.stop();

    expect(client.revoked).toEqual([["push-1", "blob-1"]]);
    expect(manager.getToken("push")).toBeUndefined();
    expect(timers.pending).toHaveLength(0);
  });

  it("stop swallows a failed revocation (TTL bounds the residue)", async () => {
    const client: TokenClient = {
      mintTokens: () => Promise.resolve(mintedPair("1")),
      revokeTokens: () => Promise.reject(new Error("offline")),
    };
    const timers = fakeTimers();
    const manager = new TokenManager(client, { ...timers });

    await manager.start();
    await expect(manager.stop()).resolves.toBeUndefined();
  });

  it("never mints again after stop", async () => {
    const mint = vi.fn(() => Promise.resolve(mintedPair("1")));
    const client = stubClient(mint);
    const timers = fakeTimers();
    const manager = new TokenManager(client, { ...timers });

    await manager.start();
    await manager.stop();
    await manager.refreshNow();

    expect(mint).toHaveBeenCalledTimes(1);
  });
});
