import { useTranslation } from "../../i18n/I18nProvider";
import { formatBytes } from "../../mail/filterSummary";
import { storageQuota, type Quota } from "../../mail/filters";
import styles from "./FiltersSection.module.css";

/**
 * The storage bar (L3 epic E6, RFC 9425 · canon §2.11).
 *
 * # Three states, and the one that is usually got wrong
 *
 *   - **a limit** — a bar with the used/limit figures beside it;
 *   - **no limit** — the SENTENCE "this mailbox has no storage limit", and NO
 *     bar. This is the state that matters: `Quota/get` returns an EMPTY LIST
 *     for an account without limits, deliberately, because "§4.1 makes
 *     hardLimit required, and fabricating an infinite one would be an invented
 *     number". A bar drawn at 0% would be exactly that invention — it implies a
 *     ceiling that does not exist;
 *   - **unreadable** — the quota is read live over IMAP, so it can fail while
 *     the rest of the sheet works. Saying so beats a bar of unknown meaning.
 *
 * # The bar is a `<progress>`
 *
 * Not a div with a width. `<progress>` carries the value, the maximum and the
 * role for free, so a screen reader announces "63%" without an `aria-*`
 * scaffold we would have to keep in sync with the CSS width.
 */

export interface QuotaRowProps {
  readonly quotas: readonly Quota[] | undefined;
  readonly error?: string | undefined;
  /** Re-reads the figure; the section calls it on open. */
  readonly onRefresh?: (() => void) | undefined;
}

export function QuotaRow({ quotas, error, onRefresh }: QuotaRowProps): React.JSX.Element {
  const { t, format } = useTranslation();

  if (error !== undefined) {
    return (
      <div className={styles.wrap}>
        <p className={styles.error} role="alert">
          {t("quota.loadFailed")}
        </p>
        {onRefresh !== undefined && (
          <button type="button" className={styles.secondary} onClick={onRefresh}>
            {t("quota.refresh")}
          </button>
        )}
      </div>
    );
  }

  if (quotas === undefined) {
    return <span className={styles.hint}>{t("app.loading")}</span>;
  }

  const storage = storageQuota(quotas);
  if (storage === undefined || storage.hardLimit <= 0) {
    // The honest shape of "no quota": a sentence, not a bar at zero.
    return <span className={styles.hint}>{t("quota.noLimit")}</span>;
  }

  const percent = Math.min(100, Math.round((storage.used / storage.hardLimit) * 100));

  return (
    <div className={styles.quotaWrap}>
      <progress
        className={styles.quotaBar}
        value={storage.used}
        max={storage.hardLimit}
        aria-label={t("quota.label")}
      />
      <span className={styles.hint}>
        {format(
          "quota.used",
          formatBytes(storage.used),
          formatBytes(storage.hardLimit),
        )}
        {" · "}
        {format("quota.percent", percent)}
      </span>
    </div>
  );
}
