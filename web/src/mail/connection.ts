/**
 * Connection honesty — the banner, and the stale-stream watchdog (L3 E9).
 *
 * # The gap this closes
 *
 * E2 shipped the SSE stream and recorded what it did NOT ship: when the stream
 * dies the app looks exactly like an app with no new mail. There is no spinner,
 * no error, no difference at all — the inbox simply stops updating, silently,
 * which is the single worst failure mode a mail client can have. A user who
 * knows the connection is down waits or reloads; a user who does not know
 * misses mail and blames themselves for not refreshing.
 *
 * So: a subtle, non-blocking pill that states the truth, and a watchdog that
 * heals the case a timer cannot.
 *
 * # Why `visibilitychange` and not just a timer (the G3 mechanism)
 *
 * A reconnect watchdog implemented as `setInterval` has a fatal blind spot on
 * exactly the platform that needs it most: **an iOS home-screen PWA freezes its
 * timers when backgrounded.** The watchdog itself sleeps, so the app wakes up
 * with a dead EventSource, a timer that never fired, and no reason to think
 * anything is wrong. Chrome's timer throttling is milder but has the same
 * shape.
 *
 * `visibilitychange` is the event that is guaranteed to fire when the tab comes
 * back, so it is the moment to ask "is this stream still alive?" and force a
 * reconnect if not — {@link shouldRecycleStream}. It is a complement to the
 * stream's own retry, not a replacement: EventSource reconnects transient drops
 * on its own while the tab is alive.
 */

/** What the pill says, or nothing at all. */
export type ConnectionState = "online" | "offline" | "reconnecting";

/** Everything {@link connectionState} decides from. */
export interface ConnectionInputs {
  /** `navigator.onLine`, kept live by the online/offline events. */
  readonly online: boolean;
  /**
   * True when the push stream is known to be down and being re-established —
   * the screen sets this in `onDead` and clears it when a state event arrives.
   */
  readonly streamDead: boolean;
}

/**
 * What to tell the user about the connection.
 *
 * The browser's own offline flag WINS over the stream's state, and the order
 * matters: with no network the stream is dead as a consequence, and reporting
 * "reconnecting" would understate the situation — nothing at all will work,
 * not just the live updates. "Sin conexión" is the honest, actionable word;
 * "Reconectando" is for the case where the network is fine and it is our stream
 * that fell over (a server restart, a proxy timeout, a revoked token), which
 * genuinely does heal itself.
 *
 * `navigator.onLine` is famously optimistic — true means "there is an
 * interface", not "the internet is reachable". That is fine here BECAUSE of
 * the ordering: a captive portal reports online, the stream then dies, and the
 * user gets "Reconectando", which is the truth of what our app is doing.
 */
export function connectionState(inputs: ConnectionInputs): ConnectionState {
  if (!inputs.online) return "offline";
  if (inputs.streamDead) return "reconnecting";
  return "online";
}

/** True when the pill should be on screen at all. */
export function showsConnectionPill(state: ConnectionState): boolean {
  return state !== "online";
}

/**
 * How long a stream may go without a heartbeat before a returning tab
 * distrusts it.
 *
 * The server's EventSource sends a `state` event on every change and nothing
 * in between, so a genuinely quiet mailbox produces a long silence that is not
 * a fault. This threshold is therefore NOT a liveness timeout — it is the point
 * past which, having just been backgrounded, we would rather pay one reconnect
 * than risk a silently dead stream. Two minutes is comfortably longer than any
 * proxy's idle window that would have killed the connection outright (Caddy's
 * default is minutes, not seconds) and short enough that a phone unlocked after
 * lunch reconnects before the user looks at it.
 */
export const STALE_STREAM_MS = 120_000;

/** What {@link shouldRecycleStream} needs to know. */
export interface RecycleInputs {
  /** The document's visibility AFTER the change. Only "visible" acts. */
  readonly visibility: "visible" | "hidden";
  /** True when `onDead` fired and no reconnection has been confirmed. */
  readonly streamDead: boolean;
  /** When the stream last proved it was alive, epoch ms. 0 = never. */
  readonly lastEventAt: number;
  readonly now: number;
  /** `navigator.onLine`: with no network a reconnect is guaranteed to fail. */
  readonly online: boolean;
}

/**
 * Whether a tab becoming visible should force a stream reconnect and a refresh
 * (the `recycleStaleSSE` mechanism).
 *
 * The conditions read in the order they matter:
 *
 *   - **going hidden never acts.** Reconnecting a stream on the way OUT is
 *     work for a tab nobody is looking at, and on mobile it is work the OS is
 *     about to freeze anyway.
 *   - **offline never acts.** A reconnect with no network burns a token mint
 *     and fails; the `online` event is the correct trigger for that case, and
 *     the pill has already told the user what is happening.
 *   - **a stream known to be dead always acts.** This is the frozen-timer case:
 *     `onDead` fired while the tab was backgrounded and nothing ran to heal it.
 *   - **otherwise, a stream silent for longer than {@link STALE_STREAM_MS}
 *     acts.** This is the case with no error at all — iOS quietly tore down the
 *     socket while the PWA was suspended, and the EventSource still reports
 *     itself as open.
 *
 * `lastEventAt === 0` means "never heard from" and is treated as stale as soon
 * as the threshold has passed since the epoch, which is always true for a real
 * clock — deliberately: a tab that was backgrounded before the stream ever
 * delivered anything has no evidence it works.
 */
export function shouldRecycleStream(inputs: RecycleInputs): boolean {
  if (inputs.visibility !== "visible") return false;
  if (!inputs.online) return false;
  if (inputs.streamDead) return true;
  return inputs.now - inputs.lastEventAt > STALE_STREAM_MS;
}
