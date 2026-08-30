import { useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import {
  schedulePresets,
  withinDelayHorizon,
} from "../../mail/scheduled";
import {
  datetimeLocalValue,
  parseDatetimeLocal,
  toUntilString,
} from "../../mail/snoozePresets";
import { PopupMenu } from "../mail/PopupMenu";
import menuStyles from "../mail/MoveMenu.module.css";
import styles from "../mail/SnoozeMenu.module.css";

/**
 * "Send later" — the composer's schedule-send menu (L3 E4, canon §2.3).
 *
 * # Why it reuses the snooze menu's machinery and its stylesheet
 *
 * The two are the same interaction asking a different question: pick one of a
 * few named times, or open a picker. `PopupMenu` already owns the APG
 * menu-button contract (focus in on open, focus back on close, both
 * dismissals), and `SnoozeMenu.module.css` already styles a list of
 * name-plus-time rows with an inline `datetime-local`. A second copy of either
 * is the drift `PopupMenu` was extracted to prevent — and it would let the two
 * pickers diverge visually for no reason a user could name.
 *
 * What genuinely differs is the BOUND: a snooze may be years out (the server
 * allows five), while a scheduled send is capped by the advertised
 * `maxDelayedSend` of 30 days. So this menu checks `withinDelayHorizon` and
 * refuses past it, in the user's language, one round trip before the server
 * would say the same thing in English.
 */
export function ScheduleMenu({
  disabled,
  onSchedule,
  maxDelayedSendSeconds,
  triggerClassName,
  triggerContent,
  now = () => new Date(),
}: {
  readonly disabled: boolean;
  /** Called with the wire `sendAt` (UTCDate) once a time is chosen. */
  readonly onSchedule: (sendAt: string) => void;
  readonly maxDelayedSendSeconds: number;
  readonly triggerClassName: string | undefined;
  readonly triggerContent: React.ReactNode;
  /** The clock, injectable — every preset is a calendar edge from a different answer. */
  readonly now?: () => Date;
}): React.JSX.Element {
  const { t } = useTranslation();
  const [openedAt, setOpenedAt] = useState<Date>(() => now());

  return (
    <PopupMenu
      label={t("schedule.menuLabel")}
      disabled={disabled}
      triggerClassName={triggerClassName}
      triggerContent={triggerContent}
      onOpen={() => {
        setOpenedAt(now());
      }}
    >
      {(close) => (
        <ScheduleMenuBody
          openedAt={openedAt}
          maxDelayedSendSeconds={maxDelayedSendSeconds}
          onChoose={(sendAt) => {
            onSchedule(sendAt);
            close();
          }}
        />
      )}
    </PopupMenu>
  );
}

function ScheduleMenuBody({
  openedAt,
  maxDelayedSendSeconds,
  onChoose,
}: {
  readonly openedAt: Date;
  readonly maxDelayedSendSeconds: number;
  readonly onChoose: (sendAt: string) => void;
}): React.JSX.Element {
  const { t, format } = useTranslation();
  const [picking, setPicking] = useState(false);
  const [value, setValue] = useState("");
  const [error, setError] = useState<string | undefined>(undefined);

  const presets = schedulePresets(openedAt);
  const horizonDays = Math.floor(maxDelayedSendSeconds / (24 * 60 * 60));

  const confirm = (): void => {
    const now = Date.now();
    const at = parseDatetimeLocal(value, new Date(now));
    if (at === undefined) {
      setError(t("schedule.pickDateInvalid"));
      return;
    }
    if (!withinDelayHorizon(at, now, maxDelayedSendSeconds)) {
      // The advertised limit, enforced client-side so the refusal names the
      // real number rather than repeating the server's English.
      setError(format("schedule.tooFarAhead", horizonDays));
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
            // eslint-disable-next-line jsx-a11y/no-autofocus
            autoFocus={index === 0}
            onClick={() => {
              onChoose(toUntilString(preset.at));
            }}
          >
            <span className={styles.presetName}>{t(preset.labelKey)}</span>
            <span className={styles.presetTime}>
              {preset.at.toLocaleString(undefined, {
                weekday: "short",
                hour: "2-digit",
                minute: "2-digit",
              })}
            </span>
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
              const seed = presets[presets.length - 1]?.at ?? openedAt;
              setValue(datetimeLocalValue(seed));
            }}
          >
            {t("schedule.pickDate")}
          </button>
        </li>
      ) : (
        <li role="none" className={styles.picker}>
          <label className={styles.pickerLabel}>
            <span>{t("schedule.pickDateLabel")}</span>
            <input
              type="datetime-local"
              className={styles.pickerInput}
              value={value}
              min={datetimeLocalValue(new Date())}
              /* The advertised horizon, on the input itself: the browser's own
                 picker then cannot offer a date the server would refuse. */
              max={datetimeLocalValue(new Date(Date.now() + maxDelayedSendSeconds * 1000))}
              // eslint-disable-next-line jsx-a11y/no-autofocus
              autoFocus
              onChange={(event) => {
                setValue(event.target.value);
                setError(undefined);
              }}
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  event.preventDefault();
                  confirm();
                }
              }}
            />
          </label>
          {error !== undefined && (
            <p className={styles.pickerError} role="alert">
              {error}
            </p>
          )}
          <button type="button" className={styles.pickerConfirm} onClick={confirm}>
            {t("schedule.pickDateConfirm")}
          </button>
        </li>
      )}
    </>
  );
}
