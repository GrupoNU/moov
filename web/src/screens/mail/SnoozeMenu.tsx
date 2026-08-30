import { useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import {
  datetimeLocalValue,
  parseDatetimeLocal,
  snoozePresets,
  toUntilString,
} from "../../mail/snoozePresets";
import { PopupMenu } from "./PopupMenu";
import menuStyles from "./MoveMenu.module.css";
import styles from "./SnoozeMenu.module.css";

/**
 * "Snooze until" — Gmail's `b` menu (L3 E4, canon §2.2).
 *
 * # Why a menu and not a button
 *
 * Snoozing without a time is meaningless, and a single key cannot name one of
 * five. So `b` opens this, exactly as `l` opens the label menu, and the trigger
 * is reused in three places (the action bar, the reader, and a row's hover
 * strip) by handing it a different `triggerClassName` and `triggerContent` —
 * the same split `MoveMenu` and `LabelMenu` already use.
 *
 * # The times, and where they come from
 *
 * `mail/snoozePresets.ts` computes them from a `now` passed in, and its header
 * records that the LABELS are Gmail's while the HOURS are ours (canon §5 puts
 * Gmail's own preset times in the UNSOURCED register). `now` is captured when
 * the menu OPENS rather than at render, because a menu mounted at 19:29 and
 * opened at 19:31 must offer the options that are true at 19:31 — and because
 * reading the clock during render would make this component impossible to test
 * deterministically.
 *
 * # The custom picker stays inside the menu
 *
 * "Pick date & time" expands a `datetime-local` in place instead of opening a
 * dialog. A dialog would need its own focus trap, its own Escape and its own
 * return-focus — all three of which `PopupMenu` already implements for this
 * menu, and a second copy of them is the exact drift `PopupMenu` was extracted
 * to prevent.
 */

export interface SnoozeMenuProps {
  readonly disabled: boolean;
  /** Called with the wire `until` (UTCDate) once a time is chosen. */
  readonly onSnooze: (until: string) => void;
  readonly triggerClassName: string | undefined;
  readonly triggerContent: React.ReactNode;
  /** Publishes an `open()` so the `b` shortcut can raise this menu. */
  readonly onReady?: ((open: () => void) => void) | undefined;
  /**
   * The clock, injectable for tests.
   *
   * Defaults to the real one. Every preset is a calendar edge away from a
   * different answer, so a component test that could not fix `now` would be a
   * test of whatever day it ran on.
   */
  readonly now?: () => Date;
}

export function SnoozeMenu({
  disabled,
  onSnooze,
  triggerClassName,
  triggerContent,
  onReady,
  now = () => new Date(),
}: SnoozeMenuProps): React.JSX.Element {
  const { t } = useTranslation();
  const [openedAt, setOpenedAt] = useState<Date>(() => now());

  return (
    <PopupMenu
      label={t("snooze.menuLabel")}
      disabled={disabled}
      triggerClassName={triggerClassName}
      triggerContent={triggerContent}
      onReady={onReady}
      onOpen={() => {
        // Re-read the clock on every open: a menu mounted before 20:00 and
        // opened after it must have withdrawn "later today".
        setOpenedAt(now());
      }}
    >
      {(close) => (
        <SnoozeMenuBody
          openedAt={openedAt}
          onChoose={(until) => {
            onSnooze(until);
            close();
          }}
        />
      )}
    </PopupMenu>
  );
}

/**
 * The menu's contents.
 *
 * Split out so the custom-picker state resets every time the menu is closed and
 * reopened: `PopupMenu` unmounts its children on close, so an expanded picker
 * with a half-typed date cannot come back on the next open.
 */
function SnoozeMenuBody({
  openedAt,
  onChoose,
}: {
  readonly openedAt: Date;
  readonly onChoose: (until: string) => void;
}): React.JSX.Element {
  const { t } = useTranslation();
  const [picking, setPicking] = useState(false);
  const [value, setValue] = useState("");
  const [invalid, setInvalid] = useState(false);

  const presets = snoozePresets(openedAt);

  const confirm = (): void => {
    const at = parseDatetimeLocal(value, new Date());
    if (at === undefined) {
      // Refused HERE rather than at the server, which would answer the same
      // `invalidProperties` one round trip later and in English.
      setInvalid(true);
      return;
    }
    onChoose(toUntilString(at));
  };

  return (
    <>
      {presets.map((preset, index) => (
        <li key={preset.id} role="none">
          <button
            type="button"
            role="menuitem"
            className={menuStyles.menuItem}
            /* APG: focus must move into the menu on open, or the menu is
               unusable by keyboard. */
            // eslint-disable-next-line jsx-a11y/no-autofocus
            autoFocus={index === 0}
            onClick={() => {
              onChoose(toUntilString(preset.at));
            }}
          >
            <span className={styles.presetName}>{t(preset.labelKey)}</span>
            <span className={styles.presetTime}>{formatWhen(preset.at)}</span>
          </button>
        </li>
      ))}

      <li role="none">
        <hr className={styles.separator} />
      </li>

      {!picking ? (
        <li role="none">
          <button
            type="button"
            role="menuitem"
            className={menuStyles.menuItem}
            onClick={() => {
              setPicking(true);
              // Seeded with tomorrow morning so the input opens on a plausible
              // value rather than on an empty field the user must fill twice.
              const seed = presets[presets.length - 1]?.at ?? openedAt;
              setValue(datetimeLocalValue(seed));
            }}
          >
            {t("snooze.pickDate")}
          </button>
        </li>
      ) : (
        <li role="none" className={styles.picker}>
          <label className={styles.pickerLabel}>
            <span>{t("snooze.pickDateLabel")}</span>
            <input
              type="datetime-local"
              className={styles.pickerInput}
              value={value}
              /* The server refuses a wake in the past; so does the input. */
              min={datetimeLocalValue(new Date())}
              // eslint-disable-next-line jsx-a11y/no-autofocus
              autoFocus
              onChange={(event) => {
                setValue(event.target.value);
                setInvalid(false);
              }}
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  event.preventDefault();
                  confirm();
                }
              }}
            />
          </label>
          {invalid && (
            <p className={styles.pickerError} role="alert">
              {t("snooze.pickDateInvalid")}
            </p>
          )}
          <button type="button" className={styles.pickerConfirm} onClick={confirm}>
            {t("snooze.pickDateConfirm")}
          </button>
        </li>
      )}
    </>
  );
}

/**
 * The preset's time as a short hint beside its name.
 *
 * Gmail shows one too, and it is what makes an ambiguous label unambiguous:
 * "This weekend" means nothing until it says "Sat, 08:00".
 */
function formatWhen(at: Date): string {
  return at.toLocaleString(undefined, {
    weekday: "short",
    hour: "2-digit",
    minute: "2-digit",
  });
}
