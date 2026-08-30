/**
 * Booting offline: what to render before any request can be made (L3 E9).
 *
 * # The decision this file isolates
 *
 * When the app starts there are three possible worlds, and telling them apart
 * wrongly produces the two worst offline bugs:
 *
 *   - showing an empty inbox when a cache existed (the user believes their mail
 *     is gone); or
 *   - showing cached mail as if it were live when the network is fine and the
 *     server simply has not answered yet (the user acts on stale state).
 *
 * The rule below therefore never guesses: cached data is rendered only when the
 * browser says there is no network, or a real request has already failed. A
 * slow server is not an offline server.
 */

/** What the shell should render right now. */
export type BootMode =
  /** Normal: fetch, and render what comes back. */
  | "online"
  /** Render from the cache, with the banner explaining why. */
  | "cached"
  /** No network AND nothing cached: an honest, explanatory empty state. */
  | "empty";

/** Everything {@link bootMode} decides from. */
export interface BootInputs {
  /** `navigator.onLine` at the moment of the decision. */
  readonly online: boolean;
  /** True when a live request has already failed with a network error. */
  readonly requestFailed: boolean;
  /** True when the cache holds at least a mailbox list for this account. */
  readonly hasCache: boolean;
}

/**
 * What to render.
 *
 * Note the asymmetry between the two failure signals, which is deliberate:
 * `online === false` is trusted immediately (the browser knows there is no
 * interface, and waiting for a request to time out would leave the user
 * watching a spinner for thirty seconds), while `online === true` is NOT
 * trusted on its own — `navigator.onLine` is famously optimistic, so a captive
 * portal reaches "cached" through `requestFailed` instead.
 */
export function bootMode(inputs: BootInputs): BootMode {
  const reachable = inputs.online && !inputs.requestFailed;
  if (reachable) return "online";
  return inputs.hasCache ? "cached" : "empty";
}

/**
 * Whether a message the user asked for can be shown from cache.
 *
 * Split out from {@link bootMode} because it answers a per-message question the
 * list-level mode cannot: the list may render happily from cache while the one
 * message the user clicks has no cached body, and that case needs its own
 * honest explanation ("this message was not saved for offline reading") rather
 * than an empty reading pane or a spinner that never resolves.
 */
export function canReadOffline(hasCachedBody: boolean, mode: BootMode): boolean {
  return mode === "online" || hasCachedBody;
}
