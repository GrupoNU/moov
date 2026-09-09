import { useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import { formatFullDate } from "../../mail/format";
import { formatAddress, recipientSummary } from "../../mail/recipientSummary";
import { displaySubject } from "../../mail/threading";
import type { Email, EmailAddress } from "../../mail/types";
import styles from "./RecipientSummary.module.css";

/**
 * "para mí ▾" — the recipient line under a sender, Gmail's shape (C-14).
 *
 * One short phrase naming who the message went to (`mail/recipientSummary.ts`
 * decides the words), and a ▾ that reveals the full headers as a
 * description list: De, Para, Cc, Cco, Responder a, Fecha, Asunto. The
 * literal recipient list this replaces answered a question nobody asked on
 * every message; the headers are one click away for the message where it
 * matters.
 *
 * The toggle is a real `<button>` with `aria-expanded`, so a keyboard reaches
 * it and a screen reader knows there is more. It must NOT sit inside another
 * button — the conversation's expanded header is itself a collapse control —
 * which is why the callers render this component as a sibling of that
 * control, never a child.
 */
export function RecipientSummary({
  email,
  ownAddresses = [],
}: {
  readonly email: Email;
  /** The reader's own addresses, so a recipient can read as "mí". */
  readonly ownAddresses?: readonly string[] | undefined;
}): React.JSX.Element | null {
  const { t, locale } = useTranslation();
  const [open, setOpen] = useState(false);

  const summary = recipientSummary(email, ownAddresses, t("reader.me"));
  if (summary === undefined) return null;

  return (
    <div className={styles.summary}>
      <button
        type="button"
        className={styles.toggle}
        onClick={() => {
          setOpen((current) => !current);
        }}
        aria-expanded={open}
        title={open ? t("reader.hideDetails") : t("reader.showDetails")}
      >
        <span className={styles.phrase}>{`${t("reader.toPhrase")} ${summary}`}</span>
        <svg
          className={styles.caret}
          viewBox="0 0 20 20"
          fill="currentColor"
          aria-hidden="true"
          focusable="false"
        >
          <path d="M5.5 8l4.5 4.5L14.5 8z" />
        </svg>
      </button>

      {open && (
        <dl className={styles.details}>
          <DetailRow label={t("reader.from")} addresses={email.from} />
          <DetailRow label={t("reader.to")} addresses={email.to} />
          <DetailRow label={t("reader.cc")} addresses={email.cc} />
          <DetailRow label={t("reader.bcc")} addresses={email.bcc} />
          <DetailRow label={t("reader.replyTo")} addresses={email.replyTo} />
          {email.receivedAt !== undefined && (
            <div className={styles.row}>
              <dt className={styles.label}>{t("reader.date")}</dt>
              <dd className={styles.value}>{formatFullDate(email.receivedAt, locale)}</dd>
            </div>
          )}
          <div className={styles.row}>
            <dt className={styles.label}>{t("reader.subject")}</dt>
            <dd className={styles.value}>{displaySubject(email.subject) ?? t("list.noSubject")}</dd>
          </div>
        </dl>
      )}
    </div>
  );
}

/** One header row; absent headers (`null` on this server) render nothing. */
function DetailRow({
  label,
  addresses,
}: {
  readonly label: string;
  readonly addresses: readonly EmailAddress[] | null | undefined;
}): React.JSX.Element | null {
  if (addresses === null || addresses === undefined || addresses.length === 0) return null;
  return (
    <div className={styles.row}>
      <dt className={styles.label}>{label}</dt>
      <dd className={styles.value}>{addresses.map(formatAddress).join(", ")}</dd>
    </div>
  );
}
