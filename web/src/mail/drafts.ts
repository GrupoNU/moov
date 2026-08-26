/**
 * Draft autosave and the undo-send countdown (P3 deliverables 3 and 5).
 *
 * Both are timing logic, and timing logic hidden inside a component is timing
 * logic nobody tests. Each is a pure state machine here, driven by an injected
 * clock, so every edge — a save that lands after the user typed again, an undo
 * window that expires while the tab is backgrounded — is enumerable.
 */

/** How long after the last keystroke a draft is saved. */
export const AUTOSAVE_DEBOUNCE_MS = 2_000;

/**
 * The longest a draft may go unsaved while the user keeps typing.
 *
 * A pure debounce never fires during continuous typing: someone composing a
 * long message for four minutes has nothing saved when the tab crashes. The
 * max-wait converts the debounce into a "save at least every N seconds"
 * guarantee, which is the property that actually protects the user's work.
 */
export const AUTOSAVE_MAX_WAIT_MS = 30_000;

/** What the composer shows about the draft's saved state. */
export type DraftStatus =
  | { readonly kind: "idle" }
  | { readonly kind: "dirty" }
  | { readonly kind: "saving" }
  | { readonly kind: "saved"; readonly at: number }
  | { readonly kind: "failed"; readonly message: string };

/**
 * A debounce with a maximum wait.
 *
 * Distinct from `search.ts`'s debouncer, which deliberately has no max-wait:
 * for search, a keystroke that never settles genuinely should not fire. For a
 * draft, the opposite is true — see AUTOSAVE_MAX_WAIT_MS.
 */
export interface AutosaveScheduler {
  /** Records a change; schedules a save. */
  readonly touch: () => void;
  /** Runs any pending save immediately (blur, close, send). */
  readonly flush: () => void;
  /** Drops a pending save without running it (discard). */
  readonly cancel: () => void;
  /** True when a save is scheduled but has not run. */
  readonly isPending: () => boolean;
}

/**
 * Builds a scheduler.
 *
 * The timer functions and the clock are injectable, and the handle type is a
 * type PARAMETER rather than `ReturnType<typeof setTimeout>`: that type is
 * `number` in a DOM lib and `NodeJS.Timeout` under @types/node, so pinning it
 * makes the module compile against whichever happens to win and makes an
 * injected fake — which returns neither — a type error for no reason.
 */
export function createAutosaveScheduler<THandle = ReturnType<typeof setTimeout>>(
  save: () => void,
  options: {
    readonly debounceMs?: number;
    readonly maxWaitMs?: number;
    readonly setTimer?: (fn: () => void, ms: number) => THandle;
    readonly clearTimer?: (handle: THandle) => void;
    readonly now?: () => number;
  } = {},
): AutosaveScheduler {
  const debounceMs = options.debounceMs ?? AUTOSAVE_DEBOUNCE_MS;
  const maxWaitMs = options.maxWaitMs ?? AUTOSAVE_MAX_WAIT_MS;
  const setTimer =
    options.setTimer ?? ((fn: () => void, ms: number): THandle => setTimeout(fn, ms) as THandle);
  const clearTimer =
    options.clearTimer ?? ((handle: THandle): void => { clearTimeout(handle as never); });
  const now = options.now ?? Date.now;

  let handle: THandle | undefined;
  let firstTouchAt: number | undefined;

  const run = (): void => {
    if (handle !== undefined) clearTimer(handle);
    handle = undefined;
    firstTouchAt = undefined;
    save();
  };

  return {
    touch(): void {
      const at = now();
      firstTouchAt ??= at;
      if (handle !== undefined) clearTimer(handle);

      // The max-wait: if the first unsaved change is already older than the
      // ceiling, save NOW instead of extending the debounce again.
      const waited = at - firstTouchAt;
      const delay = waited >= maxWaitMs ? 0 : Math.min(debounceMs, maxWaitMs - waited);
      handle = setTimer(run, delay);
    },
    flush(): void {
      if (handle === undefined) return;
      run();
    },
    cancel(): void {
      if (handle !== undefined) clearTimer(handle);
      handle = undefined;
      firstTouchAt = undefined;
    },
    isPending(): boolean {
      return handle !== undefined;
    },
  };
}

// ---------------------------------------------------------------------------
// the undo-send countdown
// ---------------------------------------------------------------------------

/** The state of an in-flight send, as the undo banner renders it. */
export type SendState =
  | { readonly kind: "idle" }
  | { readonly kind: "submitting" }
  | {
      readonly kind: "undoable";
      readonly submissionId: string;
      /** Seconds left, already floored — what the countdown shows. */
      readonly secondsLeft: number;
      readonly sendAt: number;
    }
  | { readonly kind: "sent" }
  | { readonly kind: "canceled" }
  | { readonly kind: "failed"; readonly message: string };

/**
 * Seconds remaining until `sendAt`, floored at zero.
 *
 * `Math.ceil` rather than `floor`: a window with 4.2 s left should read "5",
 * not "4", because a countdown that shows 0 while the button still works is a
 * countdown nobody trusts. The last visible number is 1, and the window closes
 * when it would show 0.
 */
export function secondsUntil(sendAt: number, now: number): number {
  const remaining = Math.ceil((sendAt - now) / 1000);
  return remaining > 0 ? remaining : 0;
}

/**
 * Parses the server's `sendAt` (a UTC ISO string) into epoch milliseconds.
 *
 * Returns undefined for anything unparseable rather than `NaN`, so a
 * malformed value cannot silently produce an undo window of `NaN` seconds that
 * renders as blank and never expires.
 */
export function parseSendAt(value: string | undefined): number | undefined {
  if (value === undefined || value === "") return undefined;
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : undefined;
}

/**
 * The client-side undo window in seconds, derived from the server's own
 * `sendAt` — never a hardcoded 10.
 *
 * The server clamps the window to 5-30 s per account (`clampUndoWindow`), so
 * the only correct source is what it just told us. A hardcoded countdown would
 * either offer undo after the mail left (a lie) or stop offering it while the
 * server still would (a lost feature).
 */
export function undoWindowSeconds(
  sendAt: string | undefined,
  now: number,
): number | undefined {
  const at = parseSendAt(sendAt);
  if (at === undefined) return undefined;
  return secondsUntil(at, now);
}
