import { useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import type { VacationResponse } from "../../mail/filters";
import { formatFullDate } from "../../mail/format";
import { isVacationActive } from "../../mail/vacationWindow";
import styles from "./VacationBanner.module.css";

/**
 * "Your vacation reply is on" — the bar across the top of the mail screen
 * (L3 epic E6, canon §2.8: a banner with "End now").
 *
 * # Why it is a banner and not a settings row
 *
 * Because the failure it prevents happens where the mail is, not where the
 * settings are. The auto-reply going out to every correspondent is invisible
 * from the inbox — Gmail's own answer is a bar the user cannot miss with the
 * off switch built into it, and the reason it works is that "I forgot it was
 * on" is the only way this feature goes wrong.
 *
 * # Visible when ENABLED **and** inside the window
 *
 * Not merely when `isEnabled`. A responder configured to start next Monday is
 * not responding today, and a banner claiming otherwise would be the UI
 * disagreeing with what the generated Sieve actually does. The window test lives
 * in `mail/vacationWindow.ts` with the rest of the date semantics, so the banner
 * and the form agree by construction.
 *
 * `role="status"` rather than `alert`: it is a standing condition, not an event,
 * and it re-renders whenever the mail list does — an alert would re-interrupt a
 * screen reader every time.
 */

export interface VacationBannerProps {
  readonly vacation: VacationResponse;
  /** Sets `isEnabled: false`. Resolves true when the server accepted it. */
  readonly onEndNow: () => Promise<boolean>;
  /** Injectable for tests; the window test needs a clock. */
  readonly now?: Date;
}

export function VacationBanner({
  vacation,
  onEndNow,
  now,
}: VacationBannerProps): React.JSX.Element | null {
  const { t, format, locale } = useTranslation();
  const [state, setState] = useState<"idle" | "ending" | "failed">("idle");

  if (!isVacationActive(vacation, now ?? new Date())) return null;

  return (
    <div className={styles.banner} role="status">
      <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
        <circle cx="10" cy="10" r="7.2" />
        <path d="M10 6v4.2l2.6 1.6" />
      </svg>
      <span className={styles.text}>
        {/*
          The end date is named when there is one: "until Friday 14" is what
          turns the banner from a nag into information, and it is also the
          sentence that tells a user whether they need to act at all.
        */}
        {vacation.toDate !== null
          ? format("vacation.bannerUntil", formatFullDate(vacation.toDate, locale))
          : t("vacation.banner")}
      </span>
      <button
        type="button"
        className={styles.endNow}
        disabled={state === "ending"}
        onClick={() => {
          setState("ending");
          void onEndNow().then((ok) => {
            setState(ok ? "idle" : "failed");
          });
        }}
      >
        {state === "ending" ? t("vacation.ending") : t("vacation.endNow")}
      </button>
      {state === "failed" && (
        <span className={styles.failed} role="alert">
          {t("vacation.endFailed")}
        </span>
      )}
    </div>
  );
}
