/**
 * Service worker registration (E9, decision D-1).
 *
 * # Why this is its own module and this small
 *
 * Registration has exactly three requirements and they are all about NOT
 * breaking things: it must be feature-detected (jsdom has no
 * `navigator.serviceWorker`, and neither does a browser on an insecure
 * origin), it must never block or delay the app, and it must never throw into
 * a caller. A rejected registration promise that nobody catches is an
 * unhandled rejection in the console of a working app.
 *
 * The whole install-prompt / update-toast surface is deliberately absent. This
 * slice ships installability, not an update UI, and a half-built "a new
 * version is available" banner is worse than none.
 */

/** The path the worker is served from. Root scope, so it can serve navigations. */
export const SERVICE_WORKER_URL = "/sw.js";

/** True when this environment can host a service worker at all. */
export function serviceWorkerSupported(scope: {
  readonly navigator?: { readonly serviceWorker?: unknown };
  readonly isSecureContext?: boolean;
} = globalThis): boolean {
  /*
   * BOTH conditions, not just the first. `navigator.serviceWorker` is absent
   * in jsdom (which is what keeps `npm test` green) but it is also absent on
   * an insecure origin — except that some environments expose the property and
   * then reject at register() time. Checking the secure context too turns that
   * rejection into a branch not taken.
   *
   * `isSecureContext` is true for https AND for localhost, so development over
   * plain http on localhost still registers, exactly as production does.
   */
  if (typeof scope.navigator?.serviceWorker !== "object") return false;
  if (scope.navigator.serviceWorker === null) return false;
  return scope.isSecureContext !== false;
}

/**
 * Registers the service worker, if this environment has one.
 *
 * Resolves to `true` when a registration was actually made. Never rejects: a
 * failed registration costs offline detection and nothing else, so it is
 * logged once and swallowed rather than propagated into the render path.
 */
export async function registerServiceWorker(): Promise<boolean> {
  if (!serviceWorkerSupported()) return false;
  try {
    await navigator.serviceWorker.register(SERVICE_WORKER_URL, { scope: "/" });
    return true;
  } catch (error) {
    // Once, and not as an error: an app that works fine without a worker
    // should not paint a red line in a user's console on every load.
    console.warn("Moov: service worker registration failed", error);
    return false;
  }
}
