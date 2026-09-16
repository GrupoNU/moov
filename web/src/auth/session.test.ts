import { describe, expect, it } from "vitest";

import {
  clearSession,
  isBeyondAbsoluteLifetime,
  isDueForRenewal,
  isReadOnlySession,
  loadSession,
  saveBearerSession,
  saveSession,
  type SessionStorageLike,
  type StoredBearerSession,
} from "./session";

/**
 * Session persistence, now a discriminated union (M2, contract §3.7).
 *
 * The tests that carry weight are the ones about the SEAM between the two
 * schemes: a record written by the previous build has no `kind` and must
 * still read as Basic, and a bearer record must never be mistaken for one —
 * because that mistake would end with the app trying to send a session token
 * as a password.
 */

function memoryStorage(seed?: string): SessionStorageLike & { value: string | undefined } {
  const box = {
    value: seed,
    getItem: (): string | null => box.value ?? null,
    setItem: (_key: string, value: string): void => {
      box.value = value;
    },
    removeItem: (): void => {
      box.value = undefined;
    },
  };
  return box;
}

const BEARER: StoredBearerSession = {
  kind: "bearer",
  username: "expo@eventos.example.test",
  token: "mds1_9vXk2Qm7Lp4Rt8Wz1Yc3Nb6Hd0Jf5Sg2Va7Ke4Mu9Xq1Zr",
  expiresAt: "2026-10-22T21:40:55.310Z",
  renewAfter: "2026-10-22T20:40:55.310Z",
  absoluteExpiresAt: "2026-10-29T09:40:55.310Z",
  readOnly: false,
  displayName: "Expo",
};

describe("the stored session union", () => {
  it("round-trips a Basic credential", () => {
    const storage = memoryStorage();
    saveSession({ username: "a@example.test", password: "pw" }, storage);
    const back = loadSession(storage);
    expect(back).toEqual({ kind: "basic", username: "a@example.test", password: "pw" });
  });

  it("round-trips a bearer session with all of its timestamps", () => {
    const storage = memoryStorage();
    saveBearerSession(BEARER, storage);
    expect(loadSession(storage)).toEqual(BEARER);
  });

  it("reads a record written by the previous build as Basic", () => {
    // The old shape had no `kind` at all. A tab left open across a deploy
    // must not be signed out by a schema change.
    const storage = memoryStorage(
      JSON.stringify({ username: "a@example.test", password: "pw" }),
    );
    expect(loadSession(storage)).toEqual({
      kind: "basic",
      username: "a@example.test",
      password: "pw",
    });
  });

  it("discards a record whose kind it does not know", () => {
    // A future scheme written by a newer build, read by an older one.
    const storage = memoryStorage(JSON.stringify({ kind: "passkey", handle: "x" }));
    expect(loadSession(storage)).toBeUndefined();
  });

  it("discards a bearer record missing any of its required fields", () => {
    const required = ["token", "expiresAt", "renewAfter", "absoluteExpiresAt", "username"];
    for (const drop of required) {
      const partial = Object.fromEntries(
        Object.entries(BEARER).filter(([key]) => key !== drop),
      );
      const storage = memoryStorage(JSON.stringify(partial));
      expect(loadSession(storage), `missing ${drop}`).toBeUndefined();
    }
  });

  it("reads a missing readOnly as false, never undefined", () => {
    const { readOnly: _dropped, ...rest } = BEARER;
    const storage = memoryStorage(JSON.stringify(rest));
    const back = loadSession(storage);
    // A restriction that arrives as undefined would be read as "allowed" by
    // one call site and "forbidden" by another.
    expect(back?.kind === "bearer" && back.readOnly).toBe(false);
  });

  it("discards corrupt JSON rather than throwing", () => {
    expect(loadSession(memoryStorage("{not json"))).toBeUndefined();
  });

  it("survives a storage that throws on every operation", () => {
    const hostile: SessionStorageLike = {
      getItem: () => {
        throw new Error("blocked");
      },
      setItem: () => {
        throw new Error("blocked");
      },
      removeItem: () => {
        throw new Error("blocked");
      },
    };
    expect(loadSession(hostile)).toBeUndefined();
    expect(() => {
      saveBearerSession(BEARER, hostile);
    }).not.toThrow();
    expect(() => {
      clearSession(hostile);
    }).not.toThrow();
  });

  it("clearSession erases either kind", () => {
    const storage = memoryStorage();
    saveBearerSession(BEARER, storage);
    clearSession(storage);
    expect(loadSession(storage)).toBeUndefined();
  });
});

describe("the bearer session's clocks", () => {
  const renewAt = Date.parse(BEARER.renewAfter);
  const absoluteAt = Date.parse(BEARER.absoluteExpiresAt);

  it("is due for renewal once renewAfter passes", () => {
    expect(isDueForRenewal(BEARER, renewAt - 1_000)).toBe(false);
    expect(isDueForRenewal(BEARER, renewAt + 1_000)).toBe(true);
  });

  it("is beyond the absolute lifetime once absoluteExpiresAt passes", () => {
    expect(isBeyondAbsoluteLifetime(BEARER, absoluteAt - 1_000)).toBe(false);
    expect(isBeyondAbsoluteLifetime(BEARER, absoluteAt + 1_000)).toBe(true);
  });

  it("treats an unparseable absolute expiry as expired", () => {
    // The safe direction: one extra trip through the portal beats an app that
    // keeps retrying a credential that can never work.
    expect(isBeyondAbsoluteLifetime({ ...BEARER, absoluteExpiresAt: "soon" })).toBe(true);
  });

  it("treats an unparseable renewal deadline as due now", () => {
    // Also the safe direction: a renewal that was not needed costs one
    // request; one that never fires costs the session.
    expect(isDueForRenewal({ ...BEARER, renewAfter: "later" })).toBe(true);
  });
});

describe("the readOnly seam", () => {
  it("is false for a Basic session, which has no retention phase", () => {
    expect(
      isReadOnlySession({ kind: "basic", username: "a@example.test", password: "pw" }),
    ).toBe(false);
  });

  it("is false when there is no session at all", () => {
    expect(isReadOnlySession(undefined)).toBe(false);
  });

  it("is false for a bearer session the server did not mark", () => {
    // The truth before M1 lands: nothing sets the flag, so nothing is hidden.
    expect(isReadOnlySession(BEARER)).toBe(false);
  });

  it("is true once the server marks the account read-only", () => {
    expect(isReadOnlySession({ ...BEARER, readOnly: true })).toBe(true);
  });
});
