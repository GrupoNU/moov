import { useCallback, useEffect, useRef } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import { clampPane, PANE_BOUNDS, type PaneAxis } from "../../mail/viewChrome";
import styles from "./PaneDivider.module.css";

/**
 * The draggable divider between the list and the reading pane (E12/B5,
 * canon 07 §6 and §33).
 *
 * # Why a `separator` and not a button or a plain div
 *
 * WAI-ARIA defines exactly this widget: a `separator` that is FOCUSABLE is a
 * window splitter, with `aria-valuenow`/`aria-valuemin`/`aria-valuemax`
 * describing its position and the arrow keys moving it. That is not decoration
 * — it is the only way a keyboard user can resize at all, and a resizer that is
 * pointer-only is a resizer half the audience does not have.
 *
 * A `<button>` would have been easier and is wrong: a button announces "press
 * me" and has no position to report, so a screen reader would describe a
 * control that does one thing when it actually does a hundred.
 *
 * # Why the drag is on the DOCUMENT and not on this element
 *
 * A pointer moving faster than the browser repaints leaves the divider behind,
 * and `mousemove` handlers bound to the element itself then stop firing — the
 * drag "sticks" the instant the user moves decisively, which is exactly when
 * they meant it. Listening on the document (and capturing the pointer) is what
 * makes a drag survive leaving the 6px strip it started on.
 *
 * # Why it reports pixels and not a percentage
 *
 * The pane's stored size is a pixel width (see `mail/viewChrome.ts` on why that
 * must not roam between devices), and `aria-valuenow` has to describe the same
 * quantity the arrows change or the announcement drifts from the behaviour.
 * A percentage would also make the arrow step mean a different number of pixels
 * on every viewport.
 */

export interface PaneDividerProps {
  /** Which dimension this divider resizes: the "right" split or the "below" one. */
  readonly axis: PaneAxis;
  /** The pane's current size in pixels. */
  readonly size: number;
  /** Called with a new size, already clamped by this component. */
  readonly onResize: (size: number) => void;
  /** Restores the default size — the double-click, and Enter from the keyboard. */
  readonly onReset: () => void;
  /**
   * An extra class from the shell, for the grid placement.
   *
   * The "below" layout places its panes by named `grid-template-areas`, and a
   * grid area can only be assigned by a rule in the GRID's own stylesheet —
   * which cannot name a class from this module. One passed-down class is a
   * smaller price than exporting a class across modules or giving this
   * component a `gridArea` prop it has no business knowing about.
   */
  readonly className?: string | undefined;
}

/**
 * How far one arrow press moves the divider.
 *
 * 16px, not 1: a 1px step would need forty presses to make a visible
 * difference, and a resizer that appears not to respond is a resizer people
 * stop using. Shift multiplies it, which is the convention every other
 * fine/coarse keyboard adjustment uses.
 */
const STEP = 16;
const COARSE_STEP = 64;

export function PaneDivider({
  axis,
  size,
  onResize,
  onReset,
  className,
}: PaneDividerProps): React.JSX.Element {
  const { t, format } = useTranslation();
  const bounds = PANE_BOUNDS[axis];

  /**
   * The drag's origin, held in a ref.
   *
   * A ref rather than state because it changes on every pointer event and
   * NOTHING renders from it: putting it in state would re-render the whole
   * shell — a virtualized list included — on every mouse move of a drag.
   */
  const dragRef = useRef<{ readonly origin: number; readonly startSize: number } | undefined>(
    undefined,
  );

  /**
   * The latest `onResize`, so the document listeners can call it without being
   * re-bound on every render.
   *
   * Re-binding them per render would detach and re-attach two document
   * listeners on every frame of a drag, which is both wasteful and a real
   * source of dropped events at the moment of detachment.
   */
  const onResizeRef = useRef(onResize);
  onResizeRef.current = onResize;

  const handlePointerDown = useCallback(
    (event: React.PointerEvent<HTMLDivElement>): void => {
      // Only the primary button drags; a right-click on a divider is a context
      // menu, not a resize.
      if (event.button !== 0) return;
      event.preventDefault();
      dragRef.current = {
        origin: axis === "width" ? event.clientX : event.clientY,
        startSize: size,
      };
      /*
       * Capturing the pointer is what makes a TOUCH drag work at all: without
       * it the browser may claim the gesture for a scroll partway through, and
       * the divider stops following the finger with no event to explain it.
       */
      event.currentTarget.setPointerCapture(event.pointerId);
    },
    [axis, size],
  );

  useEffect(() => {
    const onPointerMove = (event: PointerEvent): void => {
      const drag = dragRef.current;
      if (drag === undefined) return;
      const current = axis === "width" ? event.clientX : event.clientY;
      /*
       * The delta is NEGATED for width and not for height, and that is not an
       * arbitrary sign: the reading pane is to the RIGHT of the divider, so
       * dragging left (a negative delta) makes it BIGGER — while in the "below"
       * layout the pane is under the divider, so dragging down makes it
       * smaller. Getting this backwards produces a divider that runs away from
       * the pointer, which is the classic splitter bug.
       */
      const delta = axis === "width" ? drag.origin - current : current - drag.origin;
      onResizeRef.current(clampPane(drag.startSize + delta, axis));
    };
    const onPointerUp = (): void => {
      dragRef.current = undefined;
    };

    document.addEventListener("pointermove", onPointerMove);
    document.addEventListener("pointerup", onPointerUp);
    // `pointercancel` is not optional on touch: the OS can revoke a gesture
    // (an incoming call, a system swipe) and without this the divider would
    // stay in drag mode and follow the next unrelated pointer move.
    document.addEventListener("pointercancel", onPointerUp);
    return () => {
      document.removeEventListener("pointermove", onPointerMove);
      document.removeEventListener("pointerup", onPointerUp);
      document.removeEventListener("pointercancel", onPointerUp);
    };
  }, [axis]);

  const handleKeyDown = useCallback(
    (event: React.KeyboardEvent<HTMLDivElement>): void => {
      const step = event.shiftKey ? COARSE_STEP : STEP;
      /*
       * BOTH axes answer BOTH arrow pairs, deliberately.
       *
       * A vertical splitter "should" only take Left/Right, but a user who has
       * just tabbed to a thin grey strip does not know which kind it is — and
       * pressing the pair that does nothing reads as the control being broken.
       * Accepting all four costs nothing and removes the guess. The SIGN still
       * follows the layout: for the right-hand pane, Left grows it.
       */
      let next: number | undefined;
      switch (event.key) {
        case "ArrowLeft":
        case "ArrowUp":
          next = axis === "width" ? size + step : size - step;
          break;
        case "ArrowRight":
        case "ArrowDown":
          next = axis === "width" ? size - step : size + step;
          break;
        case "Home":
          next = bounds.min;
          break;
        case "End":
          next = bounds.max;
          break;
        case "Enter":
          // The keyboard equivalent of the double-click, on the key that means
          // "activate" for every other widget.
          event.preventDefault();
          onReset();
          return;
        default:
          return;
      }
      event.preventDefault();
      /*
       * Stopped, so the shell's global key handler does not ALSO see this. The
       * arrows are unbound there today, but `Home`/`End` are the kind of key a
       * list grows a binding for — and a divider that jumped the list to its
       * last row while resizing would be a genuinely confusing bug.
       */
      event.stopPropagation();
      onResize(clampPane(next, axis));
    },
    [axis, size, bounds, onResize, onReset],
  );

  return (
    /*
     * The two suppressions below are for ONE rule the linter cannot express,
     * and they are narrow on purpose.
     *
     * `jsx-a11y` classifies `separator` as non-interactive, and for the common
     * case it is right — a separator is usually a decorative rule. But WAI-ARIA
     * defines a second, explicitly interactive form: "a focusable separator is
     * a window splitter", whose position is reported by
     * `aria-valuenow`/`min`/`max` and moved by the arrow keys. That widget is
     * what this is, and every attribute the pattern requires is present below.
     *
     * The alternative the linter would accept is a `<button>`, and it is
     * genuinely worse: a button announces "press me" and has no position to
     * report, so a screen reader would describe a control that does one thing
     * when it actually does a hundred. Making the resizer keyboard-operable at
     * all requires exactly this shape.
     */
    /* eslint-disable-next-line jsx-a11y/no-noninteractive-element-interactions */
    <div
      className={[
        styles.divider,
        axis === "width" ? styles.vertical : styles.horizontal,
        className ?? "",
      ]
        .filter(Boolean)
        .join(" ")}
      role="separator"
      /* Focusable: this is what promotes an ARIA separator from a decorative
         rule to a window splitter, and it is the only reason the arrow keys
         below can ever be reached. */
      // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex
      tabIndex={0}
      aria-label={t("shell.resizePane")}
      aria-orientation={axis === "width" ? "vertical" : "horizontal"}
      aria-valuenow={size}
      aria-valuemin={bounds.min}
      aria-valuemax={bounds.max}
      /* The size in pixels, spoken. `aria-valuenow` alone is announced as a
         bare number, which for a splitter is meaningless without its unit. */
      aria-valuetext={format("shell.resizePaneValue", size)}
      onPointerDown={handlePointerDown}
      onKeyDown={handleKeyDown}
      onDoubleClick={onReset}
    >
      {/* A visible grip. The hit area is the whole strip (padded in CSS well
          beyond the 1px rule it draws), because a 1px drag target is a target
          most people miss. */}
      <span className={styles.grip} aria-hidden="true" />
    </div>
  );
}
