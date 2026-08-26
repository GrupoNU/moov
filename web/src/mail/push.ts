/**
 * Real-time push over the server's EventSource endpoint (RFC 8620 §7.3).
 *
 * # Why this was impossible until now, and what changed
 *
 * P2 recorded it as gap 4: the endpoint required HTTP Basic and `EventSource`
 * cannot attach an Authorization header, so the browser got a 401 and a
 * native credential dialog. The server now accepts a scoped `push` token in
 * the query string (internal/jmaphttp/token.go), minted by TokenManager; the
 * stream authenticates with that and nothing else.
 *
 * # The connection's lifetime is the token's lifetime
 *
 * A connection is opened with the CURRENT push token and simply replaced when
 * TokenManager mints a fresh one (the caller closes and reopens — see
 * MailScreen's wiring). That keeps the failure surface tiny: there is no
 * "connection outlived its token" state to reason about, reconnection after a
 * server restart rides the ordinary refresh cycle, and the EventSource's own
 * auto-retry covers transient network drops in between. On a persistent error
 * (readyState CLOSED — the browser gave up, typically because the server
 * answered 403 to a token a restart invalidated) the handler asks for an
 * immediate token refresh instead of retrying a dead URL.
 *
 * # What a state event means
 *
 * The server pushes a §7.1 StateChange whose strings equal what /get would
 * return RIGHT NOW. This module does not diff them — the remedy for "state
 * changed" is the same regardless (refetch through the ordinary API), so the
 * callback receives the parsed object and the screen decides what to reload.
 */

/** The parsed payload of one `state` event (RFC 8620 §7.1). */
export interface StateChange {
  readonly changed: Readonly<Record<string, Readonly<Record<string, string>>>>;
}

/** The subset of EventSource this module uses; tests substitute a fake. */
export interface EventSourceLike {
  addEventListener(type: string, listener: (event: MessageEvent) => void): void;
  close(): void;
  readonly readyState: number;
}

/** EventSource.CLOSED without depending on the global in tests. */
const CLOSED = 2;

export interface PushOptions {
  /** The fully-expanded eventsource URL, token included. */
  readonly url: string;
  /** Called for every state event. */
  readonly onStateChange: (change: StateChange) => void;
  /**
   * Called when the browser abandons the connection (readyState CLOSED).
   * The wiring uses this to force a token refresh, which reconnects.
   */
  readonly onDead?: () => void;
  /** Injectable constructor, for tests and for environments without SSE. */
  readonly eventSourceImpl?: (url: string) => EventSourceLike;
}

export interface PushHandle {
  close(): void;
}

/** True when a parsed payload has the §7.1 StateChange shape. */
function isStateChange(value: unknown): value is StateChange {
  return (
    typeof value === "object" &&
    value !== null &&
    typeof (value as { changed?: unknown }).changed === "object" &&
    (value as { changed?: unknown }).changed !== null
  );
}

/**
 * Opens one push connection. The caller owns its lifetime: close() on token
 * refresh (then reconnect with the new token) and on unmount.
 */
export function connectPush(options: PushOptions): PushHandle {
  const makeSource =
    options.eventSourceImpl ?? ((url: string) => new EventSource(url));
  const source = makeSource(options.url);
  let closed = false;

  source.addEventListener("state", (event) => {
    /*
     * The payload is server-controlled, but parsing still guards: a proxy
     * that mangles a frame must not take the screen down with a TypeError.
     */
    try {
      const parsed: unknown = JSON.parse(String(event.data));
      if (isStateChange(parsed)) {
        options.onStateChange(parsed);
      }
    } catch {
      // A malformed frame is dropped; the next state event supersedes it.
    }
  });

  source.addEventListener("error", () => {
    /*
     * EventSource retries transient failures itself (readyState CONNECTING).
     * CLOSED means it gave up — with this server that is an auth refusal
     * (403 for a token a restart or a revocation killed), which retrying the
     * same URL can never fix. Hand the problem to the token layer.
     */
    if (!closed && source.readyState === CLOSED) {
      options.onDead?.();
    }
  });

  return {
    close(): void {
      closed = true;
      source.close();
    },
  };
}
