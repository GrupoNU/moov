import { useCallback } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import {
  firstGroup,
  withGroupPatch,
  type GroupPatch,
  type QueryGroup,
} from "../../mail/searchQuery";
import { PopupMenu } from "./PopupMenu";
import menuStyles from "./MoveMenu.module.css";
import styles from "./SearchChips.module.css";

/**
 * The chips row under an active search (L3 epic E3; canon §2.5 "Chips: From,
 * To, Any time, Has attachment, Is unread").
 *
 * # The one rule that makes this component correct
 *
 * A chip holds NO STATE. It reads its value out of the query string with
 * `firstGroup`, and it writes by handing a new query string up. There is no
 * "is unread checked" boolean anywhere, because a second copy of the truth is
 * how a chip ends up disagreeing with the box — and the user has no way to
 * tell which one the server was actually given.
 *
 * Every toggle therefore round-trips through the parser, which makes the
 * canonical output order of `formatQuery` load-bearing: pressing a chip twice
 * must return the box to the exact string it started with, and a test in
 * `searchQuery.test.ts` pins that.
 *
 * # Why "Any time" is a menu and the rest are toggles
 *
 * Gmail's own chip is a menu with presets, and a date range has more than two
 * states. The presets map to `newer_than:`, which the parser resolves to an
 * absolute instant — so the chip and the typed operator produce the SAME
 * query, and the server never sees a relative expression to interpret.
 */

export interface SearchChipsProps {
  /** The current query string — the ONLY state. */
  readonly query: string;
  /** Hands up the new query string; the screen re-runs the search. */
  readonly onChange: (query: string) => void;
}

/** The presets Gmail's "Any time" chip offers, as day counts. */
const TIME_PRESETS = [
  { days: 7, key: "search.chip.last7" },
  { days: 30, key: "search.chip.last30" },
  { days: 90, key: "search.chip.last90" },
] as const;

export function SearchChips({ query, onChange }: SearchChipsProps): React.JSX.Element {
  const { t, format } = useTranslation();
  const group: QueryGroup = firstGroup(query);

  const patch = useCallback(
    (next: GroupPatch): void => {
      onChange(withGroupPatch(query, next));
    },
    [query, onChange],
  );

  /*
   * Which preset, if any, the current `after:` corresponds to.
   *
   * Matched with a one-day tolerance rather than exactly: the instant was
   * computed when the chip was pressed, and by the time the user looks again
   * `Date.now()` has moved. An exact comparison would make the active preset
   * stop being highlighted a second after it was chosen.
   */
  const activePreset = TIME_PRESETS.find((preset) => {
    if (group.after === undefined) return false;
    const expected = Date.now() - preset.days * 86_400_000;
    return Math.abs(new Date(group.after).getTime() - expected) < 86_400_000;
  });

  const timeLabel =
    activePreset !== undefined ? t(activePreset.key) : t("search.chip.anyTime");

  return (
    <div className={styles.row} role="group" aria-label={t("search.chips.label")}>
      <Chip
        label={t("search.chip.hasAttachment")}
        active={group.hasAttachment === true}
        onToggle={() => {
          patch({ hasAttachment: group.hasAttachment === true ? undefined : true });
        }}
      />
      <Chip
        label={t("search.chip.isUnread")}
        active={group.unread === true}
        onToggle={() => {
          patch({ unread: group.unread === true ? undefined : true });
        }}
      />

      <PopupMenu
        label={timeLabel}
        disabled={false}
        triggerClassName={`${styles.chip} ${group.after !== undefined ? styles.active : ""}`}
        triggerContent={
          <>
            {timeLabel}
            <svg
              className={styles.caret}
              viewBox="0 0 12 12"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.6"
              strokeLinecap="round"
              aria-hidden="true"
              focusable="false"
            >
              <path d="M3 4.5L6 7.5L9 4.5" />
            </svg>
          </>
        }
      >
        {(close) => (
          <>
            <li role="none">
              <button
                type="button"
                role="menuitem"
                className={menuStyles.menuItem}
                /* The APG menu-button pattern requires focus to move into the
                   menu when it opens; without it the menu is unusable by
                   keyboard. Same exemption MoveMenu takes, for the same
                   reason. */
                // eslint-disable-next-line jsx-a11y/no-autofocus
                autoFocus
                onClick={() => {
                  patch({ after: undefined, before: undefined });
                  close();
                }}
              >
                {t("search.chip.anyTime")}
              </button>
            </li>
            {TIME_PRESETS.map((preset) => (
              <li key={preset.days} role="none">
                <button
                  type="button"
                  role="menuitem"
                  className={menuStyles.menuItem}
                  onClick={() => {
                    /*
                     * An absolute instant, exactly as the parser resolves
                     * `newer_than:Nd`. `before` is cleared so the two halves of
                     * a range cannot contradict each other.
                     */
                    patch({
                      after: new Date(
                        Date.now() - preset.days * 86_400_000,
                      ).toISOString(),
                      before: undefined,
                    });
                    close();
                  }}
                >
                  {t(preset.key)}
                </button>
              </li>
            ))}
          </>
        )}
      </PopupMenu>

      {/*
        From and To appear only when they carry a value.

        Gmail renders them as always-present menus fed by its contact index;
        ours has no such index until epic E7 (the plan's declared soft
        dependency), and a chip that opens an empty picker is a control that
        does nothing — P4. So they are REMOVABLE chips once a value exists,
        which is the half of the behaviour that works today; they gain their
        picker with E7 rather than shipping hollow now.
      */}
      {(["from", "to"] as const).map((field) => {
        const value = group.fields[field];
        if (value === undefined) return null;
        const name = t(field === "from" ? "search.chip.from" : "search.chip.to");
        const text = `${name}: ${value}`;
        return (
          <button
            key={field}
            type="button"
            className={`${styles.chip} ${styles.active}`}
            onClick={() => {
              /*
               * Rebuilt without the key rather than `delete`d out of a copy:
               * the patch's REMOVAL signal is an absent property, and filtering
               * says that directly instead of mutating an object into shape.
               */
              const fields = Object.fromEntries(
                Object.entries(group.fields).filter(([name]) => name !== field),
              );
              patch({ fields });
            }}
            aria-label={format("search.chip.remove", text)}
            title={format("search.chip.remove", text)}
          >
            {text}
            <svg
              className={styles.caret}
              viewBox="0 0 12 12"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.6"
              strokeLinecap="round"
              aria-hidden="true"
              focusable="false"
            >
              <path d="M3.5 3.5l5 5M8.5 3.5l-5 5" />
            </svg>
          </button>
        );
      })}
    </div>
  );
}

function Chip({
  label,
  active,
  onToggle,
}: {
  readonly label: string;
  readonly active: boolean;
  readonly onToggle: () => void;
}): React.JSX.Element {
  return (
    <button
      type="button"
      className={`${styles.chip} ${active ? styles.active : ""}`}
      /*
       * `aria-pressed` rather than a checkbox role: this is a toggle BUTTON,
       * and a screen reader announcing "pressed" / "not pressed" matches what
       * the control looks like. A checkbox role would promise a form field.
       */
      aria-pressed={active}
      onClick={onToggle}
    >
      {label}
    </button>
  );
}
