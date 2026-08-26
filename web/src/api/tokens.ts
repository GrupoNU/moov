/**
 * The token lifecycle: mint on session start, refresh before expiry, drop on
 * sign-out.
 *
 * # What this manages and why it exists
 *
 * The server's scoped tokens (internal/jmaphttp/token.go) let the two
 * header-less browser primitives authenticate: EventSource (`push` scope) and
 * `<a download>`/`<img>` (`blob` scope). They expire server-side in minutes,
 * so somebody has to re-mint before expiry, and somebody has to revoke them
 * when the user signs out. That somebody is this class, so no screen ever
 * schedules a timer or remembers to revoke.
 *
 * # The refresh discipline
 *
 * A refresh is scheduled at {@link REFRESH_FRACTION} of the shortest minted
 * lifetime — early enough that consumers are handed a replacement well before
 * the old token dies, so an EventSource reconnecting with the CURRENT token
 * never presents an expired one. A failed mint retries on a short fixed delay
 * ({@link RETRY_DELAY_MS}) while the old tokens, still valid for a few more
 * minutes, keep working — a transient server hiccup therefore degrades to
 * "push reconnects a little later", never to a signed-out user.
 *
 * A server RESTART invalidates all outstanding tokens at once (the signing
 * key is per-process — a deliberate trade documented in token.go). The
 * refresh cycle heals this within one interval, and `refreshNow` lets an
 * observer that notices earlier (a closed EventSource) heal it immediately.
 */

import type { JmapClient, MintedTokens, TokenScope } from "./jmap";

/** Refresh when this fraction of the shortest lifetime has elapsed. */
export const REFRESH_FRACTION = 0.75;

/** Delay before retrying a failed mint. */
export const RETRY_DELAY_MS = 15_000;

/** The narrow slice of JmapClient this class needs; tests stub exactly this. */
export interface TokenClient {
  mintTokens(scopes: readonly TokenScope[], signal?: AbortSignal): Promise<MintedTokens>;
  revokeTokens(tokens: readonly string[], signal?: AbortSignal): Promise<void>;
}

/** Injectable timer functions, for tests. */
export interface TokenManagerOptions {
  readonly scopes?: readonly TokenScope[];
  readonly setTimeoutImpl?: (fn: () => void, ms: number) => unknown;
  readonly clearTimeoutImpl?: (id: unknown) => void;
  /** Called after every successful mint with the fresh token set. */
  readonly onTokens?: (tokens: MintedTokens) => void;
}

export class TokenManager {
  private readonly client: TokenClient;
  private readonly scopes: readonly TokenScope[];
  private readonly setTimeoutImpl: (fn: () => void, ms: number) => unknown;
  private readonly clearTimeoutImpl: (id: unknown) => void;
  private readonly onTokens: ((tokens: MintedTokens) => void) | undefined;

  private tokens: MintedTokens = {};
  private timer: unknown;
  private stopped = false;
  private minting = false;

  constructor(client: TokenClient, options: TokenManagerOptions = {}) {
    this.client = client;
    this.scopes = options.scopes ?? ["push", "blob"];
    this.setTimeoutImpl = options.setTimeoutImpl ?? ((fn, ms) => setTimeout(fn, ms));
    this.clearTimeoutImpl = options.clearTimeoutImpl ?? ((id) => { clearTimeout(id as number); });
    this.onTokens = options.onTokens;
  }

  /**
   * Mints the initial tokens and starts the refresh cycle. Resolves once the
   * first mint settles (successfully or not — a failure schedules a retry
   * rather than rejecting, because the app must render either way).
   */
  async start(): Promise<void> {
    await this.mint();
  }

  /** The current token for a scope, or undefined when none is held. */
  getToken(scope: TokenScope): string | undefined {
    return this.tokens[scope]?.token;
  }

  /**
   * Forces an immediate re-mint — the healing path for an observer that
   * noticed staleness before the schedule did (a dead EventSource after a
   * server restart). Coalesces with an in-flight mint.
   */
  async refreshNow(): Promise<void> {
    await this.mint();
  }

  /**
   * Stops the cycle and revokes the held tokens server-side (best-effort:
   * the TTL bounds what an unreachable revocation leaves behind). This is
   * the sign-out path; it must be called while the client's credential is
   * still valid, which is why sign-out revokes BEFORE clearing the session.
   */
  async stop(): Promise<void> {
    this.stopped = true;
    if (this.timer !== undefined) {
      this.clearTimeoutImpl(this.timer);
      this.timer = undefined;
    }
    const held = Object.values(this.tokens)
      .map((t) => t?.token)
      .filter((t): t is string => typeof t === "string");
    this.tokens = {};
    if (held.length > 0) {
      try {
        await this.client.revokeTokens(held);
      } catch {
        // Unreachable server at sign-out: the tokens die at TTL anyway.
      }
    }
  }

  private async mint(): Promise<void> {
    if (this.stopped || this.minting) return;
    this.minting = true;
    try {
      const minted = await this.client.mintTokens(this.scopes);
      if (this.stopped) return;
      this.tokens = minted;
      this.schedule(this.nextRefreshMs(minted));
      this.onTokens?.(minted);
    } catch {
      if (this.stopped) return;
      // Keep whatever tokens are still unexpired and try again soon.
      this.schedule(RETRY_DELAY_MS);
    } finally {
      this.minting = false;
    }
  }

  private nextRefreshMs(minted: MintedTokens): number {
    let shortest = Number.POSITIVE_INFINITY;
    for (const t of Object.values(minted)) {
      if (t !== undefined && t.expiresIn > 0 && t.expiresIn < shortest) {
        shortest = t.expiresIn;
      }
    }
    if (!Number.isFinite(shortest)) return RETRY_DELAY_MS;
    return Math.max(1_000, Math.floor(shortest * 1000 * REFRESH_FRACTION));
  }

  private schedule(ms: number): void {
    if (this.timer !== undefined) {
      this.clearTimeoutImpl(this.timer);
    }
    this.timer = this.setTimeoutImpl(() => {
      this.timer = undefined;
      void this.mint();
    }, ms);
  }
}

/** A JmapClient satisfies TokenClient structurally; this alias documents it. */
export type { JmapClient as TokenCapableClient };
