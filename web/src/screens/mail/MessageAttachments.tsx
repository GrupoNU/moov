import { useCallback, useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import { withAccessToken, type JmapClient } from "../../api/jmap";
import { formatBytes } from "../../mail/format";
import { displaySubject } from "../../mail/threading";
import type { Email, EmailBodyPart } from "../../mail/types";
import styles from "./MessageAttachments.module.css";

/**
 * A message's attachments, and the download of the message itself.
 *
 * # Why this is its own module
 *
 * Both components lived inside `ReadingPane` while there was exactly one
 * message on screen. E1 put a whole conversation there, and attachments are a
 * property of ONE message: hoisting the opened message's list above six others
 * would attribute files to the wrong sender, which is a correctness problem
 * rather than a layout one. So they moved here, unchanged, and both the
 * single-message reader and each expanded message in a conversation render
 * their own.
 */

/**
 * The attachment list.
 *
 * Per-attachment download is real: the server advertises a derived per-part
 * `blobId` (P2 gap 5, closed), and a `blob`-scoped token turns each attachment
 * into a native `<a download>` — the browser streams the bytes, nothing is
 * buffered in the page. The token rides the query string because a navigation
 * can carry no header; it is single-scope, account-bound and expires in
 * minutes (see the server's token.go for the full threat model).
 *
 * When either half is missing — an old message row without part ids, or the
 * token not yet minted — the attachment is listed without a link, exactly as
 * before, and the reader toolbar's "download original" still always works.
 *
 * P0-6 / C-04 removed the `email` prop: it existed only to feed the
 * whole-message download button that used to sit under this list, which now
 * lives once in the reader's overflow menu instead of once per message.
 */
export function AttachmentList({
  attachments,
  client,
  accountId,
  blobToken,
}: {
  readonly attachments: readonly EmailBodyPart[];
  readonly client: JmapClient;
  readonly accountId: string;
  readonly blobToken?: string | undefined;
}): React.JSX.Element {
  const { t, format, locale } = useTranslation();

  const hrefFor = (part: EmailBodyPart): string | undefined => {
    if (part.blobId === null || blobToken === undefined) return undefined;
    const name = part.name ?? "attachment";
    return withAccessToken(
      client.downloadUrlFor(accountId, part.blobId, name, part.type),
      blobToken,
    );
  };

  return (
    <section
      className={styles.attachments}
      aria-label={format("reader.attachments", attachments.length)}
    >
      <p className={styles.attachmentsTitle}>{format("reader.attachments", attachments.length)}</p>
      <ul className={styles.attachmentList}>
        {attachments.map((part, index) => {
          const href = hrefFor(part);
          return (
            <li key={part.partId ?? index} className={styles.attachment}>
              <svg
                className={styles.attachmentIcon}
                viewBox="0 0 20 20"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.5"
                aria-hidden="true"
                focusable="false"
              >
                <path d="M11.5 2.5H5.8a1 1 0 0 0-1 1v13a1 1 0 0 0 1 1h8.4a1 1 0 0 0 1-1V6.2z" />
                <path d="M11.5 2.5v3.7h3.7" />
              </svg>
              {href !== undefined ? (
                <a
                  className={styles.attachmentName}
                  href={href}
                  download={part.name ?? "attachment"}
                  title={t("reader.download")}
                >
                  {part.name ?? part.type}
                </a>
              ) : (
                <span className={styles.attachmentName}>{part.name ?? part.type}</span>
              )}
              <span className={styles.attachmentMeta}>{formatBytes(part.size, locale)}</span>
            </li>
          );
        })}
      </ul>
      {/*
        P0-6 / C-04: "Descargar el mensaje original" is GONE from here.

        It rendered under EVERY message's attachment list, which in a
        six-message conversation put a developer's affordance six times
        between the reader and the next message. It is not a per-message
        action a person reaches for — it is a diagnostic — and it now lives
        once, in the reader toolbar's ⋮, beside "Ver original", which is the
        same file seen a different way.

        `DownloadOriginalButton` itself stays exported and unchanged: the
        toolbar's menu item calls the same download path.
      */}
    </section>
  );
}

/**
 * Downloads the original message.
 *
 * This CANNOT be a plain `<a download href>`: the download route requires HTTP
 * Basic and a browser navigation sends no Authorization header — the live
 * pilot answers 401 with `WWW-Authenticate: Basic`, which makes the browser
 * pop its own credential dialog at the user. So the bytes are fetched with the
 * header attached, handed to a temporary anchor as a blob: URL, and the URL is
 * revoked immediately afterwards so the message does not stay in memory.
 *
 * # Two shapes, one implementation (P0-6 / C-04)
 *
 * It used to render under every message's attachment list. It now renders
 * ONCE, as an item in the reader toolbar's overflow menu — so the same
 * component has to be able to be a menu item as well as a standalone row.
 *
 * `variant` does that rather than a copy, and the reason is the state machine:
 * the fetch has idle / working / failed states and a `role="status"` live
 * region that must be present BEFORE the failure text appears (an element
 * inserted together with its own text is not announced). A second
 * implementation for the menu would be a second place for that to be got
 * wrong, in the branch nobody looks at.
 *
 * In the menu the live region rides along inside the item, which is why the
 * item is a `<li role="none">` wrapping a `role="menuitem"` button plus the
 * status span — the shape `PopupMenu` expects.
 */
export function DownloadOriginalButton({
  email,
  client,
  accountId,
  variant = "row",
  onDone,
}: {
  readonly email: Email;
  readonly client: JmapClient;
  readonly accountId: string;
  /** "menu" renders it as an overflow-menu item instead of a standalone row. */
  readonly variant?: "row" | "menu";
  /** Called after a download STARTS, so a menu can close itself. */
  readonly onDone?: (() => void) | undefined;
}): React.JSX.Element | null {
  const { t } = useTranslation();
  const [state, setState] = useState<"idle" | "working" | "failed">("idle");

  const filename = `${(displaySubject(email.subject) ?? "message")
    .replace(/[^\p{L}\p{N} ._-]/gu, "")
    .slice(0, 60)
    .trim()}.eml`;

  const download = useCallback(async (): Promise<void> => {
    if (email.blobId === undefined) return;
    setState("working");
    let url: string | undefined;
    try {
      const blob = await client.downloadBlob(
        accountId,
        email.blobId,
        filename,
        "application/octet-stream",
      );
      url = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = filename;
      document.body.appendChild(anchor);
      anchor.click();
      anchor.remove();
      setState("idle");
    } catch {
      setState("failed");
    } finally {
      // Revoked on a macrotask so the click has certainly been dispatched;
      // revoking synchronously cancels the download in some browsers.
      if (url !== undefined) {
        const toRevoke = url;
        setTimeout(() => {
          URL.revokeObjectURL(toRevoke);
        }, 30_000);
      }
    }
  }, [client, accountId, email.blobId, filename]);

  if (email.blobId === undefined) return null;

  if (variant === "menu") {
    return (
      <li role="none">
        <button
          type="button"
          role="menuitem"
          className={styles.downloadMenuItem}
          onClick={() => {
            void download().then(() => {
              onDone?.();
            });
          }}
          disabled={state === "working"}
        >
          {state === "working" ? t("reader.downloading") : t("reader.downloadMessage")}
        </button>
        {/* Always present, so its message is ANNOUNCED when it appears rather
            than being inserted alongside its own text. */}
        <span role="status" aria-live="polite" className={styles.downloadStatus}>
          {state === "failed" ? t("reader.downloadFailed") : ""}
        </span>
      </li>
    );
  }

  return (
    <div className={styles.downloadRow}>
      <button
        type="button"
        className={styles.downloadButton}
        onClick={() => {
          void download();
        }}
        disabled={state === "working"}
      >
        {state === "working" ? t("reader.downloading") : t("reader.downloadMessage")}
      </button>
      {/* The live region is always present, so its message is announced when
          it appears rather than being inserted alongside its own text. */}
      <span role="status" aria-live="polite" className={styles.downloadStatus}>
        {state === "failed" ? t("reader.downloadFailed") : ""}
      </span>
    </div>
  );
}
