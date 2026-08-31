import { useCallback, useEffect, useId, useRef, useState } from "react";

import type { Branding } from "../../branding/branding";
import { BrandMark } from "../../components/BrandMark";
import { useTranslation } from "../../i18n/I18nProvider";
import { initialsFor } from "../../mail/format";
import styles from "./TopBar.module.css";

/**
 * The application top bar (E12, canon 07 §1).
 *
 * # What this is, and what it is not
 *
 * It is a LAYOUT component: a one-row grid that puts the hamburger, the brand,
 * the search box and the right cluster where Gmail puts them, and owns exactly
 * one piece of state — whether the avatar menu is open. Every control it draws
 * is a callback the shell passes in, and the search box is passed as a CHILD
 * rather than constructed here, because `SearchBar` needs the screen's
 * suggestion sources, its debounce and its input ref. Rebuilding it here would
 * have meant threading nine props through a bar whose job is placement.
 *
 * # Why the gear moved here from the sidebar's bottom-left
 *
 * The old placement had a stated rationale (Slack/Linear/VS Code put app-level
 * controls bottom-left) and it was defensible in isolation. Canon 07 §1 settles
 * it against the only benchmark that governs this epic: Gmail's gear is
 * top-right, and "most people can use Gmail blind" is a claim about WHERE their
 * hand goes. A defensible-but-different placement is exactly the kind of thing
 * that costs a migrating user a manual, so it moves.
 *
 * # The avatar menu, and why it is not a `PopupMenu`
 *
 * `PopupMenu` is built for menus of ACTIONS on a selection: it takes a
 * disabled state, it publishes an imperative `open()` for the keyboard map, and
 * its items are `menuitem`s. This one shows an identity — an email address that
 * is not a control — above a single action. Reusing PopupMenu would have meant
 * putting a non-interactive line inside a `role="menu"`, which is precisely the
 * structure screen readers announce wrongly. So it is a small disclosure with
 * the same two dismissals (Escape, outside pointer) and the same focus return,
 * written out rather than borrowed.
 */

export interface TopBarProps {
  readonly branding: Branding;
  /** The signed-in address, shown in the avatar menu. */
  readonly username: string;
  readonly onSignOut: () => void;
  /** True when the left rail is collapsed to its icon width. */
  readonly sidebarCollapsed: boolean;
  readonly onToggleSidebar: () => void;
  /** Opens the shortcuts dialog — Gmail's `?` help affordance. */
  readonly onOpenHelp: () => void;
  /** Opens the quick-settings docked panel (B2). */
  readonly onOpenQuickSettings: () => void;
  /** True while that panel is open, for `aria-expanded` on the gear. */
  readonly quickSettingsOpen: boolean;
  /**
   * The search box, rendered by the caller.
   *
   * A child rather than props: `SearchBar` is a combobox with its own debounce,
   * its own suggestion sources and an imperative ref the `/` shortcut focuses.
   * Passing those through here would make this component the middleman for a
   * widget it has no opinions about.
   */
  readonly children: React.ReactNode;
}

export function TopBar({
  branding,
  username,
  onSignOut,
  sidebarCollapsed,
  onToggleSidebar,
  onOpenHelp,
  onOpenQuickSettings,
  quickSettingsOpen,
  children,
}: TopBarProps): React.JSX.Element {
  const { t } = useTranslation();
  const menuId = useId();
  const [menuOpen, setMenuOpen] = useState(false);
  const avatarRef = useRef<HTMLButtonElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);

  const closeMenu = useCallback((): void => {
    setMenuOpen(false);
    // Focus returns to the trigger, or a keyboard user is dropped at the top of
    // the document with no idea where they were.
    avatarRef.current?.focus();
  }, []);

  /*
   * Both dismissals. A popup that only closes on Escape traps a mouse user and
   * one that only closes on an outside click traps a keyboard user — the same
   * pair `PopupMenu` implements, for the same reason.
   */
  useEffect(() => {
    if (!menuOpen) return undefined;
    const onPointerDown = (event: PointerEvent): void => {
      const target = event.target as Node;
      if (menuRef.current?.contains(target) === true) return;
      if (avatarRef.current?.contains(target) === true) return;
      // No focus return here: the pointer has already named where it is going,
      // and yanking focus back to the avatar would fight the click.
      setMenuOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key !== "Escape") return;
      // Stopped so the shell's global Escape (which closes panels and the
      // reader) does not also fire on the same press.
      event.stopPropagation();
      closeMenu();
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown, true);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown, true);
    };
  }, [menuOpen, closeMenu]);

  return (
    <header className={styles.bar}>
      <div className={styles.left}>
        <button
          type="button"
          className={styles.iconButton}
          onClick={onToggleSidebar}
          /* The label says what the press will DO. `aria-expanded` carries the
             current state, so the two together are unambiguous. */
          aria-label={
            sidebarCollapsed ? t("shell.expandSidebar") : t("shell.collapseSidebar")
          }
          title={sidebarCollapsed ? t("shell.expandSidebar") : t("shell.collapseSidebar")}
          aria-expanded={!sidebarCollapsed}
        >
          <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" aria-hidden="true" focusable="false">
            <path d="M3 5.5h14M3 10h14M3 14.5h14" />
          </svg>
        </button>
        <BrandMark branding={branding} size="sm" />
      </div>

      {/* The pill box. Centred-left and wide, which is where Gmail's is and
          what makes search the visual centre of the bar rather than an
          afterthought at one end. */}
      <div className={styles.search}>{children}</div>

      <div className={styles.right}>
        <button
          type="button"
          className={styles.iconButton}
          onClick={onOpenHelp}
          aria-label={t("shell.help")}
          title={`${t("shortcuts.title")} (?)`}
        >
          <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" aria-hidden="true" focusable="false">
            <circle cx="10" cy="10" r="7.5" />
            <path d="M7.8 7.7a2.2 2.2 0 1 1 2.9 2.1c-.5.2-.8.6-.8 1.1v.4M10 14.2v.1" />
          </svg>
        </button>

        {/*
          THE settings entry, top-right (canon 07 §1). It opens the QUICK
          panel, not the full page: Gmail's gear is a two-step affordance, and
          the panel's own "Ver todos los ajustes" is the second step.
        */}
        <button
          type="button"
          className={styles.iconButton}
          onClick={onOpenQuickSettings}
          aria-label={t("settings.open")}
          title={t("settings.open")}
          aria-expanded={quickSettingsOpen}
        >
          <svg className={styles.gear} viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
            <circle cx="10" cy="10" r="2.6" />
            <path d="M10 2.6l1 2 2.2-.5 1.2 1.9-1.4 1.7.9 2 2.1.7v2.2l-2.1.7-.9 2 1.4 1.7-1.2 1.9-2.2-.5-1 2h-2l-1-2-2.2.5-1.2-1.9 1.4-1.7-.9-2-2.1-.7V10.4l2.1-.7.9-2L3.6 6l1.2-1.9 2.2.5 1-2z" />
          </svg>
        </button>

        <div className={styles.account}>
          <button
            ref={avatarRef}
            type="button"
            className={styles.avatar}
            onClick={() => {
              setMenuOpen((open) => !open);
            }}
            aria-haspopup="true"
            aria-expanded={menuOpen}
            aria-controls={menuId}
            aria-label={t("shell.accountMenu")}
            title={username}
          >
            {/* The initial, not a favicon or a gravatar. A signed decision:
                nothing is fetched from a third party to draw a user's face. */}
            <span aria-hidden="true">{initialsFor(username)}</span>
          </button>

          {menuOpen && (
            <div ref={menuRef} id={menuId} className={styles.menu}>
              {/*
                The address is a STATEMENT, not a control, so it is a plain
                paragraph outside any menu semantics — which is why this popup
                is not a `role="menu"`: a non-interactive line inside one is
                announced as an item that cannot be activated.
              */}
              <p className={styles.menuAddress}>{username}</p>
              <button
                type="button"
                className={styles.menuAction}
                onClick={() => {
                  setMenuOpen(false);
                  onSignOut();
                }}
              >
                {t("shell.signOut")}
              </button>
            </div>
          )}
        </div>
      </div>
    </header>
  );
}
