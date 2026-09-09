import { useCallback, useEffect, useRef, useState } from "react";

/**
 * Per-row save confirmation (review F-38).
 *
 * # Why autosave needs a receipt at all
 *
 * Every control on the settings page saves on the gesture — no Save button, and
 * that is right: a switch has two states and flipping one IS the decision, so a
 * button asking the user to confirm what they just did is ceremony. The review
 * agreed with the autosave and named what it lacks: nothing on screen says the
 * save HAPPENED. The control moved, and the control would have moved either way
 * — a failed write and a successful one look identical until the page reloads.
 *
 * Gmail's answer is a toast. Ours is smaller and better placed: a "Guardado ✓"
 * beside the row that saved, for two seconds. A toast for a setting change is a
 * notification about a thing the user is looking straight at, and it appears
 * somewhere else on the screen to say so.
 *
 * # Why it is per row and not one shared flag
 *
 * Changing density and then theme in quick succession must confirm BOTH, in
 * their own places. One flag would move a single tick from row to row, which
 * reads as the previous confirmation being retracted.
 *
 * # Why only a SUCCESS is transient
 *
 * A failure is already reported permanently, at the top of the page, by the
 * provider's own error strip — and it must be, because a failed preference is a
 * state the user has to act on. A tick that disappears is right for "this
 * worked"; a cross that disappears would be a problem the user is allowed to
 * miss.
 */

/** How long the tick stays. Long enough to notice, short enough not to linger. */
export const SAVED_FEEDBACK_MS = 2000;

export interface SaveFeedback {
  /** True while this row's tick should be on screen. */
  readonly isSaved: boolean;
  /**
   * Wraps a save, showing the tick when it resolves true.
   *
   * The promise is not awaited by the caller — every control here is
   * fire-and-forget — so this returns void and swallows nothing: a rejection
   * still reaches the provider, which owns the error strip.
   */
  readonly report: (save: Promise<boolean>) => void;
}

export function useSaveFeedback(): SaveFeedback {
  const [isSaved, setSaved] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  /*
   * Guards a `setState` after unmount — a user who changes a setting and
   * immediately leaves the tab would otherwise get React's warning, and in a
   * test the update would land outside `act`.
   */
  const alive = useRef(true);

  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      if (timer.current !== undefined) clearTimeout(timer.current);
    };
  }, []);

  const report = useCallback((save: Promise<boolean>): void => {
    void save.then((ok) => {
      if (!ok || !alive.current) return;
      setSaved(true);
      /*
       * The previous timer is cleared rather than left to fire: changing one
       * setting twice in a second must extend the tick, not have the first
       * change's timeout hide the second change's confirmation.
       */
      if (timer.current !== undefined) clearTimeout(timer.current);
      timer.current = setTimeout(() => {
        if (alive.current) setSaved(false);
      }, SAVED_FEEDBACK_MS);
    });
  }, []);

  return { isSaved, report };
}
