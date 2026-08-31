/**
 * View chrome: the layout state that belongs to a DEVICE, not to an account
 * (E12).
 *
 * # Why these are localStorage and the preferences are not
 *
 * Every user-facing SETTING in this app roams: it lives in the server's `Prefs`
 * singleton so a person signing in on a second machine finds the product as
 * they left it (`mail/prefs.ts`). The three values here deliberately do NOT,
 * and the reason is that they are answers to a question about the SCREEN in
 * front of you:
 *
 *   - whether the folder rail is collapsed to icons — a 1280px laptop wants it
 *     collapsed and a 27" monitor does not, and roaming the choice would mean
 *     the laptop keeps re-collapsing what the desktop just expanded;
 *   - how wide the reading pane is, in pixels;
 *   - how tall it is, in the "below" layout.
 *
 * Roaming a PIXEL WIDTH between a laptop and a monitor is not a feature, it is
 * a bug with a sync service attached. So they stay local, and this module is
 * the one place that says so.
 *
 * # Why every read is bounded and every write is guarded
 *
 * localStorage is a string store that survives across versions, so its contents
 * are UNTRUSTED input in exactly the way a server response is: an old build, a
 * hand-edited value or a corrupted profile can all put a `NaN`, a negative
 * number or `"[object Object]"` under these keys. A divider that restored to
 * -400px would render a reading pane the user cannot see and cannot get back,
 * with no error to explain it. So each reader clamps into the same range the
 * drag handle enforces, and an unparseable value returns the default rather
 * than propagating.
 *
 * Storage access itself is wrapped because it THROWS rather than failing
 * quietly in two real cases: Safari's private mode (quota 0) and a browser with
 * site data blocked. A layout preference must never be able to break the app.
 */

/** The storage keys, prefixed so they cannot collide with another app's. */
const SIDEBAR_KEY = "moov.chrome.sidebarCollapsed";
const PANE_WIDTH_KEY = "moov.chrome.readerWidth";
const PANE_HEIGHT_KEY = "moov.chrome.readerHeight";

/**
 * The reading pane's size limits, in pixels.
 *
 * The MINIMA are not decoration. Below ~22rem a reading pane cannot show a
 * quoted line without wrapping every four words, and below ~20rem the LIST
 * becomes a column of ellipses — which is the failure the existing
 * `minmax(20rem, 30rem)` grid track was written to prevent, now expressed as a
 * clamp because the user can drag. The maxima are the mirror: a divider that
 * can be dragged to leave a 40px list is a divider that can lose the list.
 */
export const PANE_BOUNDS = {
  /** The reader's width in the "right" layout. */
  width: { min: 360, max: 1200, default: 520 },
  /** The reader's height in the "below" layout. */
  height: { min: 200, max: 900, default: 420 },
} as const;

/** Which axis a layout resizes along. */
export type PaneAxis = "width" | "height";

function readItem(key: string): string | undefined {
  try {
    if (typeof localStorage === "undefined") return undefined;
    return localStorage.getItem(key) ?? undefined;
  } catch {
    // Private mode, blocked site data. A layout preference is never worth an
    // exception escaping into a render.
    return undefined;
  }
}

function writeItem(key: string, value: string): void {
  try {
    if (typeof localStorage === "undefined") return;
    localStorage.setItem(key, value);
  } catch {
    // Same. The session keeps the value in React state either way; only the
    // persistence across reloads is lost, which is the honest degradation.
  }
}

/** Clamps a number into a bound, or returns the default when it is not one. */
export function clampPane(value: number, axis: PaneAxis): number {
  const bound = PANE_BOUNDS[axis];
  if (!Number.isFinite(value)) return bound.default;
  if (value < bound.min) return bound.min;
  if (value > bound.max) return bound.max;
  // Rounded so the stored value and the rendered pixel agree: a fractional
  // width persisted and restored drifts by a subpixel per reload.
  return Math.round(value);
}

/** The stored reader size for one axis, clamped, or its default. */
export function loadPaneSize(axis: PaneAxis): number {
  const raw = readItem(axis === "width" ? PANE_WIDTH_KEY : PANE_HEIGHT_KEY);
  if (raw === undefined) return PANE_BOUNDS[axis].default;
  const parsed = Number.parseFloat(raw);
  return clampPane(parsed, axis);
}

/** Persists a reader size, clamped on the way in. */
export function savePaneSize(axis: PaneAxis, value: number): void {
  writeItem(
    axis === "width" ? PANE_WIDTH_KEY : PANE_HEIGHT_KEY,
    String(clampPane(value, axis)),
  );
}

/**
 * Whether the folder rail was left collapsed.
 *
 * Only the exact string `"1"` is true. A permissive read (`raw !== null`) would
 * make a leftover `"false"` from some future build collapse the rail, which is
 * the class of bug that is impossible to reproduce from a description.
 */
export function loadSidebarCollapsed(): boolean {
  return readItem(SIDEBAR_KEY) === "1";
}

export function saveSidebarCollapsed(collapsed: boolean): void {
  writeItem(SIDEBAR_KEY, collapsed ? "1" : "0");
}
