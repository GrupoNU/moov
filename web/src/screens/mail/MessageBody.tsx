import { useCallback, useMemo, useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import type { JmapClient } from "../../api/jmap";
import type { Email, EmailBodyPart, EmailBodyValue } from "../../mail/types";
import { SecureHtmlBody, type SignImageUrls } from "./SecureHtmlBody";
import styles from "./MessageBody.module.css";

/**
 * THE BODY RENDERER — where a message's body becomes pixels, and
 * deliberately the ONLY place.
 *
 * P2 rendered plain text exclusively; the W-A4 epic filled the documented
 * seam with {@link SecureHtmlBody}. The security architecture is layered
 * and each layer documents itself where it lives:
 *
 *   1. server-side sanitization (its own epic; the client assumes NOTHING
 *      from it — layers must fail independently);
 *   2. client-side DOMPurify under the explicit policy of
 *      `mail/html/policy.ts` — allowlisted tags/attributes/URL schemes,
 *      filtered inline CSS, stripped stylesheets, forced link hygiene,
 *      remote images stripped or rewritten to the HMAC proxy;
 *   3. a sandboxed, opaque-origin iframe under `default-src 'none'`
 *      (`mail/html/srcdoc.ts`) — the layer that holds when 1 and 2 fail.
 *
 * HTML is preferred when present (it is what the sender designed); the
 * text/plain alternative renders when there is no HTML, or as the honest
 * fallback when sanitization refuses the document. A message with neither
 * readable part gets the parse-failure notice and the raw download.
 *
 * Remote images: this component owns the per-message opt-in state. It
 * starts blocked for every message; the SecureHtmlBody instance is keyed by
 * the caller (ReadingPane keys MessageBody on email.id), so the state can
 * never leak from one message to the next.
 */

export interface MessageBodyProps {
  readonly email: Email;
  /** Signs remote-image URLs for the proxy (mail/api.ts). */
  readonly signImageUrls: SignImageUrls;
  /**
   * E2 / canon §4.1.9: whether the user may unblock remote images AT ALL.
   *
   * `false` in the Junk mailbox. It is stronger than the default blocked
   * state: the opt-in control is not rendered, so there is no path — not even
   * a deliberate one — by which a message in Spam fetches from its sender's
   * server. Defaults to `true`, so every existing caller keeps the offer.
   */
  readonly allowRemoteImages?: boolean;
  /**
   * E5 / D-4: load remote images WITHOUT asking, for every message.
   *
   * This is the `imagesPolicy: "always"` pole, and the reason it is
   * defensible is the proxy: every remote image is fetched through Moov's
   * HMAC-signed, anti-SSRF proxy, so the sender's server sees our host and
   * learns nothing about the reader — no IP, no user agent, no read receipt
   * (canon §4.1.1, which is exactly the condition Gmail states for its own
   * display-by-default).
   *
   * It is deliberately SUBORDINATE to {@link allowRemoteImages}: in Junk the
   * images stay unloadable no matter what this says. A preference must not be
   * able to override a security stance.
   */
  readonly autoLoadImages?: boolean;
  /**
   * C-11: needed to fetch the message's inline (`cid:`) image parts through
   * the authenticated blob path. Absent (an old caller), inline images stay
   * unresolved and the honest notice remains.
   */
  readonly client?: JmapClient | undefined;
  readonly accountId?: string | undefined;
}

/** Picks the body value for a part, if the server sent one. */
function valueFor(
  email: Email,
  part: EmailBodyPart | undefined,
): EmailBodyValue | undefined {
  if (part?.partId === undefined || part.partId === null) return undefined;
  return email.bodyValues?.[part.partId];
}

export function MessageBody({
  email,
  signImageUrls,
  allowRemoteImages = true,
  autoLoadImages = false,
  client,
  accountId,
}: MessageBodyProps): React.JSX.Element {
  const { t } = useTranslation();
  const [showImages, setShowImages] = useState(false);
  /*
   * C-11: the loader for inline images — the same authenticated path the
   * attachment cards use. Stable across renders so the body's effect does not
   * refetch on every keystroke elsewhere in the pane.
   */
  const loadInlineImage = useCallback(
    (part: EmailBodyPart): Promise<Blob> => {
      if (client === undefined || accountId === undefined || part.blobId === null) {
        return Promise.reject(new Error("inline image not loadable"));
      }
      return client.downloadBlob(accountId, part.blobId, part.name ?? "image", part.type);
    },
    [client, accountId],
  );
  /*
   * Memoized on the attachments array: the renderer's fetch effect keys on
   * this reference, and a fresh `.filter()` per render would re-run it — and
   * re-fetch every inline image — on every render of the pane.
   */
  const inlineParts = useMemo(
    () => (email.attachments ?? []).filter((part) => part.cid !== null),
    [email.attachments],
  );
  /*
   * Belt AND braces: even if the opt-in state were somehow set (a stale value
   * surviving a re-key, a future caller flipping the prop), the images stay
   * blocked. The policy is enforced here rather than only by hiding a button.
   *
   * `allowRemoteImages` is the OUTER conjunct for both routes, which is what
   * makes the Junk rule absolute: the "always" preference can turn the opt-in
   * into an automatic yes, and it still cannot turn a forbidden fetch into a
   * permitted one.
   */
  const imagesShown = (showImages || autoLoadImages) && allowRemoteImages;

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

  // Collect every part that actually has a value.
  const renderedText = textParts
    .map((part) => ({ part, value: valueFor(email, part) }))
    .filter((entry): entry is { part: EmailBodyPart; value: EmailBodyValue } =>
      entry.value !== undefined,
    );
  const renderedHtml = htmlParts
    .map((part) => valueFor(email, part))
    .filter((value): value is EmailBodyValue => value !== undefined);

  /*
   * The plain-text rendering — the whole body when there is no HTML, and the
   * fallback SecureHtmlBody shows when sanitization refuses the document.
   */
  const textFallback =
    renderedText.length > 0 ? (
      <>
        {renderedText.map(({ part, value }, index) => (
          <PlainTextBody key={part.partId ?? index} value={value} />
        ))}
      </>
    ) : (
      <p className={styles.emptyBody} role="note">
        {t("reader.emptyBody")}
      </p>
    );

  if (renderedHtml.length > 0) {
    /*
     * Multiple text/html parts are joined into one document: they are
     * sequential fragments of one body (RFC 8621 §4.1.4 orders htmlBody),
     * and one frame with one policy beats a stack of frames.
     */
    const rawHtml = renderedHtml.map((value) => value.value).join("\n");
    const anyTruncated = renderedHtml.some((value) => value.isTruncated);

    return (
      <div className={styles.htmlBody}>
        <SecureHtmlBody
          html={rawHtml}
          blockRemoteImages={!imagesShown}
          allowUnblock={allowRemoteImages}
          onShowRemoteImages={() => {
            setShowImages(true);
          }}
          signImageUrls={signImageUrls}
          fallback={textFallback}
          /* C-11: the parts with a content-id; the renderer fetches only
             the ones the body references. */
          inlineParts={inlineParts}
          loadInlineImage={client === undefined ? undefined : loadInlineImage}
        />
        {anyTruncated && (
          <p className={styles.truncated} role="note">
            {t("reader.bodyTruncated")}
          </p>
        )}
      </div>
    );
  }

  return <div className={styles.body}>{textFallback}</div>;
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
