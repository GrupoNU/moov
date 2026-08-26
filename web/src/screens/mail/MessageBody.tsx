import { useTranslation } from "../../i18n/I18nProvider";
import type { Email, EmailBodyPart, EmailBodyValue } from "../../mail/types";
import styles from "./MessageBody.module.css";

/**
 * THE HTML-RENDERER SEAM.
 *
 * ============================================================================
 * READ THIS BEFORE ADDING HTML RENDERING. This component is the single place
 * where a message's body becomes pixels, and it is deliberately the ONLY one.
 * ============================================================================
 *
 * # What P2 does, and why it does so little
 *
 * P2 renders `text/plain` bodies and NOTHING else. There is no
 * `dangerouslySetInnerHTML` anywhere in this codebase, no `<iframe>`, and no
 * HTML string is ever passed to the DOM — `Email/get` is not even asked for
 * `fetchHTMLBodyValues` (see `mail/api.ts`), so hostile markup does not enter
 * the client's memory at all.
 *
 * That is not laziness or an oversight. Rendering mail HTML is the largest
 * attack surface in a mail client (ADR §5, L2-pwa risk 2), and a half-safe
 * renderer is worse than none: it produces a product that appears to work
 * while leaking the session of every user who opens a crafted message. The
 * epic that adds it (W-A4) runs on a stronger model for exactly that reason.
 *
 * # The contract for the epic that fills this in
 *
 * Replace ONLY the `HtmlBodyPlaceholder` branch below. Everything else — the
 * metadata header, the attachment list, the thread context, the download path
 * — is finished and must not need to change. The new component must satisfy:
 *
 *   PROPS (stable, do not widen):
 *     { html: string;          // the raw, UNTRUSTED bodyValue
 *       blockRemoteImages: boolean;
 *       onShowRemoteImages: () => void; }
 *
 *   REQUIREMENTS (ADR §5's three layers, none optional):
 *     1. The server sanitises (bluemonday) — already true for what it stores.
 *     2. The client sanitises with DOMPurify before the string reaches the DOM.
 *     3. The result renders in `<iframe sandbox>` WITHOUT `allow-scripts` and
 *        WITHOUT `allow-same-origin` — the two together are equivalent to no
 *        sandbox at all — carrying CSP `default-src 'none'`.
 *     4. Remote images are blocked by default and only loaded on an explicit
 *        user action, through the HMAC image proxy (never a direct fetch,
 *        which leaks the reader's IP to the sender).
 *     5. `target="_blank"` links additionally carry `rel="noopener noreferrer"`.
 *
 *   WHERE TO TURN IT ON: `fetchMessageDetail` in `mail/api.ts` currently sets
 *   `fetchTextBodyValues: true` only. Adding `fetchHTMLBodyValues: true` there
 *   is the deliberate switch that begins delivering HTML to the client, and it
 *   should be flipped in the same change that lands the renderer — not before.
 *
 * Until then this component shows the plain-text alternative and says plainly
 * that a formatted version exists but is not being shown, which is honest
 * rather than silently degrading.
 */

export interface MessageBodyProps {
  readonly email: Email;
}

/** Picks the body value for a part, if the server sent one. */
function valueFor(
  email: Email,
  part: EmailBodyPart | undefined,
): EmailBodyValue | undefined {
  if (part?.partId === undefined || part.partId === null) return undefined;
  return email.bodyValues?.[part.partId];
}

export function MessageBody({ email }: MessageBodyProps): React.JSX.Element {
  const { t } = useTranslation();

  const textParts = email.textBody ?? [];
  const htmlParts = email.htmlBody ?? [];

  /*
   * A parse failure is a real, expected state, not an error: the sync engine
   * stores messages it cannot parse (E4's cascade ends in "raw blob"), and
   * `Email/get` renders them with an empty body list. Saying so — and offering
   * the download, which always works because the raw blob is intact — is much
   * better than an empty pane that looks like a bug.
   */
  const hasNoParts = textParts.length === 0 && htmlParts.length === 0;
  if (hasNoParts) {
    return (
      <div className={styles.notice} role="note">
        <p className={styles.noticeTitle}>{t("reader.parseFailed")}</p>
        <p className={styles.noticeBody}>{t("reader.parseFailedBody")}</p>
      </div>
    );
  }

  // Collect every text part that actually has a value.
  const rendered = textParts
    .map((part) => ({ part, value: valueFor(email, part) }))
    .filter((entry): entry is { part: EmailBodyPart; value: EmailBodyValue } =>
      entry.value !== undefined,
    );

  const hasHtmlOnly = rendered.length === 0 && htmlParts.length > 0;

  return (
    <div className={styles.body}>
      {/*
        The HTML seam. When a message is HTML-only there is no text to show,
        so the placeholder is all the user gets — which is why it explains
        itself rather than rendering an empty pane.
      */}
      {hasHtmlOnly && <HtmlBodyPlaceholder />}

      {rendered.map(({ part, value }, index) => (
        <PlainTextBody
          key={part.partId ?? index}
          value={value}
        />
      ))}

      {/*
        A message with BOTH parts renders its text and notes that a formatted
        version exists. multipart/alternative is the common case, and the text
        alternative is usually the same content.
      */}
      {!hasHtmlOnly && htmlParts.length > 0 && rendered.length > 0 && (
        <p className={styles.htmlHint}>{t("reader.htmlNotRendered")}</p>
      )}
    </div>
  );
}

/**
 * A plain-text body.
 *
 * Rendered as text inside a `<pre>`-like element rather than as HTML: React
 * escapes the string, and `white-space: pre-wrap` preserves the sender's line
 * breaks without a single tag being constructed. There is no parsing step and
 * therefore no parser to exploit.
 *
 * URLs are deliberately NOT auto-linked. Turning text into anchors means
 * finding URL boundaries in hostile input, which is its own well-supplied
 * source of bugs (and of phishing: `http://good.com@evil.com`). The text is
 * selectable and copyable, which is the safe 95% of the value.
 */
function PlainTextBody({ value }: { readonly value: EmailBodyValue }): React.JSX.Element {
  const { t } = useTranslation();
  return (
    <>
      <div className={styles.text}>{value.value}</div>
      {value.isTruncated && (
        <p className={styles.truncated} role="note">
          {t("reader.bodyTruncated")}
        </p>
      )}
    </>
  );
}

/**
 * The placeholder that the secure HTML renderer replaces.
 *
 * Named and documented so it is trivially greppable: `HtmlBodyPlaceholder` is
 * the one symbol the next epic deletes.
 */
function HtmlBodyPlaceholder(): React.JSX.Element {
  const { t } = useTranslation();
  return (
    <div className={styles.notice} role="note">
      <p className={styles.noticeTitle}>{t("reader.htmlNotRendered")}</p>
      <p className={styles.noticeBody}>{t("reader.htmlNotRenderedBody")}</p>
    </div>
  );
}
