import { useCallback, useEffect, useRef, useState } from "react";

import styles from "./MoveMenu.module.css";

/**
 * The shared menu-button machinery (extracted in E8).
 *
 * # Why this exists
 *
 * `MoveMenu` was already the second copy of the hardest sixty lines in this
 * screen — outside-click dismissal, Escape handling, focus return to the
 * trigger — and E8's "Label as" menu would have been the third. The comment at
 * the top of `MoveMenu` predicted the outcome exactly: two copies "would have
 * drifted the first time one was fixed". So the mechanism moves here once, and
 * both menus become their own CONTENT and nothing else.
 *
 * What stays with each menu is what genuinely differs: the move menu is a list
 * of `menuitem`s that closes on choice, and the label menu is a list of
 * `menuitemcheckbox`es that deliberately stays OPEN so several labels can be
 * ticked in one visit (Gmail's behaviour, and the reason its label menu has an
 * "Apply" affordance where its move menu does not).
 *
 * # The APG contract this implements
 *
 * The menu-button pattern (WAI-ARIA APG): the trigger carries
 * `aria-haspopup="menu"` and `aria-expanded`; the popup is a `menu` with an
 * accessible name; focus moves INTO the menu on open and RETURNS to the trigger
 * on close. Both dismissals are implemented, because a menu that only closes on
 * Escape traps a mouse user and one that only closes on an outside click traps
 * a keyboard user.
 */

export interface PopupMenuProps {
  /** The menu's accessible name, and the trigger's label and title. */
  readonly label: string;
  readonly disabled: boolean;
  /** The trigger's class, so each caller keeps its own button styling. */
  readonly triggerClassName: string | undefined;
  /** Rendered inside the trigger: an icon in the bar, a word in the reader. */
  readonly triggerContent: React.ReactNode;
  /**
   * The menu's contents, given a `close` it can call.
   *
   * A render prop rather than plain children because the item that closes the
   * menu is the item's own business: a move closes, a label tick does not.
   */
  readonly children: (close: () => void) => React.ReactNode;
  /** Called when the menu opens, for a caller that must refresh something. */
  readonly onOpen?: (() => void) | undefined;
  /**
   * Receives an `open()` the caller can invoke — how a KEYBOARD shortcut opens
   * this menu.
   *
   * A ref to the trigger element and a synthetic `.click()` would have worked
   * too, and is what a first version did; it was replaced because a synthetic
   * click on a disabled button silently does nothing, and the failure looked
   * like a broken shortcut rather than like an empty selection. An explicit
   * imperative handle makes the disabled case visible at the call site.
   */
  readonly onReady?: ((open: () => void) => void) | undefined;
}

export function PopupMenu({
  label,
  disabled,
  triggerClassName,
  triggerContent,
  children,
  onOpen,
  onReady,
}: PopupMenuProps): React.JSX.Element {
  const [isOpen, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement | null>(null);
  const buttonRef = useRef<HTMLButtonElement | null>(null);

  const close = useCallback((): void => {
    setOpen(false);
    buttonRef.current?.focus();
  }, []);

  /*
   * The imperative `open` is published through a ref-stable callback, so the
   * host can call it from a key handler without this component re-rendering the
   * host on every open.
   */
  useEffect(() => {
    if (onReady === undefined) return;
    onReady(() => {
      if (disabled) return;
      onOpen?.();
      setOpen(true);
    });
  }, [onReady, disabled, onOpen]);

  useEffect(() => {
    if (!isOpen) return undefined;
    const onPointerDown = (event: PointerEvent): void => {
      if (containerRef.current?.contains(event.target as Node) !== true) setOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key === "Escape") {
        /*
         * Stopped, so the app's global handler does not ALSO read this Escape
         * as "close the reading pane". One Escape, one dismissal — the
         * innermost one.
         */
        event.stopPropagation();
        close();
      }
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown, true);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown, true);
    };
  }, [isOpen, close]);

  return (
    <div className={styles.menuWrap} ref={containerRef}>
      <button
        ref={buttonRef}
        type="button"
        className={triggerClassName}
        disabled={disabled}
        aria-haspopup="menu"
        aria-expanded={isOpen}
        aria-label={label}
        title={label}
        onClick={() => {
          setOpen((open) => {
            if (!open) onOpen?.();
            return !open;
          });
        }}
      >
        {triggerContent}
      </button>

      {isOpen && (
        <ul className={styles.menu} role="menu" aria-label={label}>
          {children(close)}
        </ul>
      )}
    </div>
  );
}
