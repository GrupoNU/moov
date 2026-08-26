import { useCallback, useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import { withAccessToken, type JmapClient } from "../../api/jmap";
import { signImageProxyUrls } from "../../mail/api";
import { formatBytes, formatFullDate, initialsFor, machineDate } from "../../mail/format";
import { displaySubject, senderLabel } from "../../mail/threading";
import type { Email, EmailAddress, EmailBodyPart, Thread } from "../../mail/types";
import { MessageBody } from "./MessageBody";
import styles from "./ReadingPane.module.css";

/**
 * The reading pane (P2 deliverable 6, partial — the body renderer is a seam).
 *
 * Everything except the body is finished here: metadata, the attachment list
 * with sizes and working downloads, thread context, and the keyboard path back
 * to the list. The body goes through {@link MessageBody}, which is the single
 * documented seam the HTML epic replaces — see its header comment for the
 * contract.
 */

export interface ReadingPaneProps {
  readonly email: Email | undefined;
  readonly thread: Thread | undefined;
  readonly isLoading: boolean;
  readonly error: string | undefined;
  readonly onClose: () => void;
  /** Used for attachment and raw-message downloads, which need auth headers. */
  readonly client: JmapClient;
  readonly accountId: string;
  /**
   * The current `blob`-scoped download token, when one is held. It turns each
   * attachment into a NATIVE `<a download>` — the browser streams the bytes
   * itself instead of the app buffering them through fetch+objectURL. Absent
   * (still minting, or the feature failed), the per-attachment links simply
   * do not render and the whole-message download below still works.
   */
  readonly blobToken?: string | undefined;
  // --- P3: acting on the open message --------------------------------------
  readonly onReply: () => void;
  readonly onReplyAll: () => void;
  readonly onForward: () => void;
  readonly onArchive: () => void;
  readonly onDelete: () => void;
  /** True when delete ERASES rather than moves to Trash (server rule W-A2). */
  readonly deleteIsPermanent: boolean;
}

export function ReadingPane({
  email,
  thread,
  isLoading,
  error,
  onClose,
  client,
  accountId,
  onReply,
  onReplyAll,
  onForward,
  onArchive,
  onDelete,
  deleteIsPermanent,
  blobToken,
}: ReadingPaneProps): React.JSX.Element {
  const { t, format, locale } = useTranslation();

  // The remote-image signer the secure HTML renderer uses (W-A4): the ONLY
  // path by which a message's remote image can ever be fetched, and it goes
  // through our authenticated sign endpoint plus the HMAC proxy.
  const signImages = useCallback(
    (urls: readonly string[]) => signImageProxyUrls(client, urls),
    [client],
  );

  if (isLoading && email === undefined) {
    return (
      <div className={styles.pane}>
        <div className={styles.centered} role="status" aria-live="polite">
          <span className={styles.spinner} aria-hidden="true" />
          <p className={styles.mutedText}>{t("reader.loading")}</p>
        </div>
      </div>
    );
  }

  if (error !== undefined) {
    return (
      <div className={styles.pane}>
        <div className={styles.centered}>
          <p className={styles.errorTitle}>{t("reader.loadFailed")}</p>
          <p className={styles.mutedText}>{error}</p>
        </div>
      </div>
    );
  }

  if (email === undefined) {
    return (
      <div className={styles.pane}>
        <div className={styles.centered}>
          <p className={styles.errorTitle}>{t("reader.loadFailed")}</p>
        </div>
      </div>
    );
  }

  const subject = displaySubject(email.subject) ?? t("list.noSubject");
  const attachments = email.attachments ?? [];
  const threadSize = thread?.emailIds.length ?? 1;
  const isoDate = machineDate(email.receivedAt);

  return (
    <article
      className={styles.pane}
      /* A labelled region, so a screen reader user can jump straight to the
       * message they just opened. */
      aria-label={subject}
    >
      <header className={styles.header}>
        <div className={styles.headerTop}>
          <h1 className={styles.subject}>{subject}</h1>
          <button
            type="button"
            className={styles.close}
            onClick={onClose}
            /* The accessible name says where it goes, not what it looks like. */
            aria-label={t("reader.close")}
            title={t("reader.close")}
          >
            <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round">
              <path d="M5.5 5.5l9 9M14.5 5.5l-9 9" />
            </svg>
          </button>
        </div>

        {/*
          The action row. Reply is the primary action of a mail client and is
          styled as such; the rest are equal-weight secondary actions. Each is
          a real <button> with a text label, not an icon alone — this pane has
          the room, and an icon-only toolbar is a guessing game the first time
          someone uses it.
        */}
        <div className={styles.actions} role="group" aria-label={t("action.more")}>
          <button type="button" className={styles.primaryAction} onClick={onReply}>
            {t("action.reply")}
          </button>
          <button type="button" className={styles.secondaryAction} onClick={onReplyAll}>
            {t("action.replyAll")}
          </button>
          <button type="button" className={styles.secondaryAction} onClick={onForward}>
            {t("action.forward")}
          </button>
          <span className={styles.actionSpacer} />
          <button type="button" className={styles.secondaryAction} onClick={onArchive}>
            {t("action.archive")}
          </button>
          {/* The LABEL states which of the two semantics applies (W-A2). */}
          <button
            type="button"
            className={[styles.secondaryAction, deleteIsPermanent ? styles.dangerAction : ""]
              .filter(Boolean)
              .join(" ")}
            onClick={onDelete}
          >
            {deleteIsPermanent ? t("action.deleteForever") : t("action.delete")}
          </button>
        </div>

        {threadSize > 1 && (
          <p className={styles.threadContext}>{format("reader.threadContext", threadSize)}</p>
        )}

        <div className={styles.identity}>
          <span className={styles.avatar} aria-hidden="true">
            {initialsFor(senderLabel(email))}
          </span>
          <div className={styles.identityText}>
            <p className={styles.fromLine}>
              <span className={styles.fromName}>{senderLabel(email) ?? t("list.unknownSender")}</span>
              {email.from?.[0]?.name !== null && email.from?.[0] !== undefined && (
                <span className={styles.fromAddress}>{`<${email.from[0].email}>`}</span>
              )}
            </p>
            {isoDate !== undefined && (
              <time className={styles.date} dateTime={isoDate}>
                {formatFullDate(email.receivedAt, locale)}
              </time>
            )}
          </div>
        </div>

        {/*
          Recipients as a description list: each label is programmatically tied
          to its addresses, which is what lets a screen reader say "To: Ana,
          Carlos" instead of reading five names with no idea which field they
          belong to.
        */}
        <dl className={styles.recipients}>
          <AddressRow label={t("reader.to")} addresses={email.to} />
          <AddressRow label={t("reader.cc")} addresses={email.cc} />
          <AddressRow label={t("reader.bcc")} addresses={email.bcc} />
        </dl>
      </header>

      {attachments.length > 0 && (
        <AttachmentList
          attachments={attachments}
          email={email}
          client={client}
          accountId={accountId}
          blobToken={blobToken}
        />
      )}

      <div className={styles.bodyRegion}>
        {/* Keyed by message id so per-message state — the remote-images
            opt-in above all — can never leak from one message to the next. */}
        <MessageBody key={email.id} email={email} signImageUrls={signImages} />
      </div>

      <footer className={styles.footer}>
        <DownloadOriginalButton email={email} client={client} accountId={accountId} />
      </footer>
    </article>
  );
}

function AddressRow({
  label,
  addresses,
}: {
  readonly label: string;
  readonly addresses: readonly EmailAddress[] | null | undefined;
}): React.JSX.Element | null {
  // Absent headers are `null` on this server, never `[]` — either way there is
  // nothing to render, and an empty row would be noise.
  if (addresses === null || addresses === undefined || addresses.length === 0) return null;
  return (
    <div className={styles.recipientRow}>
      <dt className={styles.recipientLabel}>{label}</dt>
      <dd className={styles.recipientValue}>
        {addresses.map((address) => address.name ?? address.email).join(", ")}
      </dd>
    </div>
  );
}

/**
 * The attachment list.
 *
 * Per-attachment download is real now: the server advertises a derived
 * per-part `blobId` (P2 gap 5, closed), and a `blob`-scoped token turns each
 * attachment into a native `<a download>` — the browser streams the bytes,
 * nothing is buffered in the page. The token rides the query string because
 * a navigation can carry no header; it is single-scope, account-bound and
 * expires in minutes (see the server's token.go for the full threat model).
 *
 * When either half is missing — an old message row without part ids, or the
 * token not yet minted — the attachment is listed without a link, exactly as
 * before, and the whole-message download below still always works.
 */
function AttachmentList({
  attachments,
  email,
  client,
  accountId,
  blobToken,
}: {
  readonly attachments: readonly EmailBodyPart[];
  readonly email: Email;
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
    <section className={styles.attachments} aria-label={format("reader.attachments", attachments.length)}>
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
              <span className={styles.attachmentMeta}>
                {formatBytes(part.size, locale)}
              </span>
            </li>
          );
        })}
      </ul>
      <DownloadOriginalButton email={email} client={client} accountId={accountId} />
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
 */
function DownloadOriginalButton({
  email,
  client,
  accountId,
}: {
  readonly email: Email;
  readonly client: JmapClient;
  readonly accountId: string;
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
