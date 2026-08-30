import { useCallback, useEffect, useRef, useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import { withAccessToken, type JmapClient } from "../../api/jmap";
import { signImageProxyUrls } from "../../mail/api";
import { formatBytes, formatFullDate, initialsFor, machineDate } from "../../mail/format";
import { headerSection, unfoldHeaders } from "../../mail/rawMessage";
import { displaySubject, senderLabel } from "../../mail/threading";
import {
  isFlagged,
  type Email,
  type EmailAddress,
  type EmailBodyPart,
  type Mailbox,
  type Thread,
} from "../../mail/types";
import { listIdLabel, unsubscribeInfo, type UnsubscribeInfo } from "../../mail/unsubscribe";
import { MessageBody } from "./MessageBody";
import { MoveMenu } from "./MoveMenu";
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

  // --- E2: the completed reader ------------------------------------------
  /** Toggles the star on the open message. */
  readonly onToggleFlag: () => void;
  /** Moves the open message to a chosen folder. */
  readonly onMove: (mailboxId: string) => void;
  /** Marks the open message unread (and returns to the list, per Gmail). */
  readonly onMarkUnread: () => void;
  /** Reports spam, or — when already in Junk — takes it back out. */
  readonly onToggleSpam: () => void;
  /** Opens the composer prefilled from a `mailto:` unsubscribe URI. */
  readonly onUnsubscribeByMail: (to: string, subject: string | undefined, body: string | undefined) => void;
  readonly mailboxes: readonly Mailbox[];
  /** The folder being viewed, so the move menu can exclude it. */
  readonly currentMailboxId: string | undefined;
  /**
   * True when the open message is in the mailbox with role `junk`.
   *
   * Two consequences, both from canon §4.1.9: the reader shows the spam
   * banner, and remote images become UNLOADABLE rather than merely blocked —
   * the unblock control is not rendered at all.
   */
  readonly inJunk: boolean;
  /**
   * E5 / D-4: load remote images without asking (the `imagesPolicy: "always"`
   * pole). Junk still overrides it — see {@link MessageBody}.
   */
  readonly autoLoadImages?: boolean;
  /** Goes to the next/previous message in the list; absent when there is none. */
  readonly onNextMessage: (() => void) | undefined;
  readonly onPreviousMessage: (() => void) | undefined;
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
  onToggleFlag,
  onMove,
  onMarkUnread,
  onToggleSpam,
  onUnsubscribeByMail,
  mailboxes,
  currentMailboxId,
  inJunk,
  autoLoadImages = false,
  onNextMessage,
  onPreviousMessage,
}: ReadingPaneProps): React.JSX.Element {
  const { t, format, locale } = useTranslation();
  const [originalOpen, setOriginalOpen] = useState(false);

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
  const unsubscribe = unsubscribeInfo(email);

  return (
    <article
      /*
       * `printRoot` is what the print stylesheet keys on: at print time every
       * other column of the app is hidden and this element becomes the page.
       * Marking it in the markup rather than selecting it by position means a
       * layout change cannot silently break printing.
       */
      className={[styles.pane, styles.printRoot].join(" ")}
      /* A labelled region, so a screen reader user can jump straight to the
       * message they just opened. */
      aria-label={subject}
    >
      <header className={styles.header}>
        <div className={styles.headerTop}>
          <h1 className={styles.subject}>{subject}</h1>
          {/*
            Previous/next before close, in that reading order, because that is
            the order they are reached by Tab and the order they sit in every
            mail client's top-right corner. Each is DISABLED rather than hidden
            at the ends of the list: a control that vanishes moves the two
            beside it, and the close button must not jump under the pointer.
          */}
          <div className={styles.navGroup} role="group" aria-label={t("shortcuts.sectionNavigate")}>
            <button
              type="button"
              className={styles.close}
              onClick={onPreviousMessage}
              disabled={onPreviousMessage === undefined}
              aria-label={t("action.previous")}
              title={`${t("action.previous")} (k)`}
            >
              <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round">
                <path d="M13 15.5l-6-5.5 6-5.5" />
              </svg>
            </button>
            <button
              type="button"
              className={styles.close}
              onClick={onNextMessage}
              disabled={onNextMessage === undefined}
              aria-label={t("action.next")}
              title={`${t("action.next")} (j)`}
            >
              <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round">
                <path d="M7 4.5l6 5.5-6 5.5" />
              </svg>
            </button>
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
        </div>

        {/*
          The Spam banner (canon §4.1.9).
          `role="note"` rather than `alert`: it is a standing property of the
          message, not an event, and an alert would re-interrupt a screen
          reader every time the pane re-renders.
        */}
        {inJunk && (
          <div className={styles.spamBanner} role="note">
            <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
              <path d="M10 2.6l6.6 3.5v4c0 3.7-2.8 6.4-6.6 7.3-3.8-.9-6.6-3.6-6.6-7.3v-4z" />
              <path d="M10 7v4M10 13.6v.1" />
            </svg>
            <div>
              <p className={styles.spamBannerTitle}>{t("reader.spamBanner")}</p>
              <p className={styles.spamBannerBody}>{t("reader.spamBannerBody")}</p>
            </div>
            <button type="button" className={styles.primaryAction} onClick={onToggleSpam}>
              {t("action.notSpam")}
            </button>
          </div>
        )}

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

          {/*
            E2: the star. `aria-pressed` rather than a changing label, because
            it IS a toggle in one state and announcing it as a toggle is what
            tells a screen-reader user whether the message is starred right
            now — a label that flips only says what the next press will do.
          */}
          <button
            type="button"
            className={[styles.secondaryAction, isFlagged(email) ? styles.activeAction : ""]
              .filter(Boolean)
              .join(" ")}
            onClick={onToggleFlag}
            aria-pressed={isFlagged(email)}
          >
            {isFlagged(email) ? t("action.unflag") : t("action.flag")}
          </button>

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

          <button type="button" className={styles.secondaryAction} onClick={onToggleSpam}>
            {inJunk ? t("action.notSpam") : t("action.spam")}
          </button>

          <MoveMenu
            mailboxes={mailboxes}
            currentMailboxId={currentMailboxId}
            disabled={false}
            onMove={onMove}
            triggerClassName={styles.secondaryAction}
            triggerContent={t("action.move")}
          />

          {/*
            Mark-unread CLOSES the reader, and that is not a shortcut: leaving
            the message open would have the reading pane immediately re-mark it
            read, so the button would appear to do nothing. Gmail returns to
            the list for exactly this reason.
          */}
          <button type="button" className={styles.secondaryAction} onClick={onMarkUnread}>
            {t("action.markUnread")}
          </button>

          <button
            type="button"
            className={styles.secondaryAction}
            onClick={() => {
              window.print();
            }}
          >
            {t("action.print")}
          </button>

          <button
            type="button"
            className={styles.secondaryAction}
            onClick={() => {
              setOriginalOpen(true);
            }}
            aria-haspopup="dialog"
          >
            {t("action.viewOriginal")}
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
          {/* Canon §2.2: the control sits NEXT TO THE SENDER, not in the
              toolbar — it is a statement about who is writing, not an action
              on this one message. */}
          {unsubscribe !== undefined && (
            <UnsubscribeButton
              info={unsubscribe}
              listName={listIdLabel(email)}
              onUnsubscribeByMail={onUnsubscribeByMail}
            />
          )}
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
            opt-in above all — can never leak from one message to the next.

            Canon §4.1.9: in Spam the images are not merely blocked-with-an-
            offer, they are UNLOADABLE. `allowRemoteImages={false}` removes the
            unblock control entirely rather than disabling it, because a
            disabled "Show images" invites the click that the policy exists to
            prevent, and a remote fetch from a message in Spam is a delivery
            receipt to a spammer. */}
        <MessageBody
          key={email.id}
          email={email}
          signImageUrls={signImages}
          allowRemoteImages={!inJunk}
          autoLoadImages={autoLoadImages}
        />
        {inJunk && (
          <p className={styles.spamImagesNote} role="note">
            {t("reader.spamImagesBlocked")}
          </p>
        )}
      </div>

      <footer className={styles.footer}>
        <DownloadOriginalButton email={email} client={client} accountId={accountId} />
      </footer>

      <OriginalDialog
        isOpen={originalOpen}
        onClose={() => {
          setOriginalOpen(false);
        }}
        email={email}
        client={client}
        accountId={accountId}
      />
    </article>
  );
}

/**
 * The Unsubscribe control (canon §2.2).
 *
 * The `mailto:` path opens OUR composer prefilled, so the user sees exactly
 * what is about to be sent from their own address and can cancel. The http(s)
 * path is a link with `rel="noopener noreferrer"` and `target="_blank"` — a
 * real anchor rather than a button calling `window.open`, so middle-click and
 * "copy link" work and the user can see where it goes before committing.
 */
function UnsubscribeButton({
  info,
  listName,
  onUnsubscribeByMail,
}: {
  readonly info: UnsubscribeInfo;
  readonly listName: string | undefined;
  readonly onUnsubscribeByMail: (
    to: string,
    subject: string | undefined,
    body: string | undefined,
  ) => void;
}): React.JSX.Element | null {
  const { t, format } = useTranslation();

  // The accessible name says WHAT is being unsubscribed from when the message
  // told us (List-ID), which is the difference between "Unsubscribe" (from
  // what?) and "Unsubscribe from Moov News".
  const label =
    listName === undefined ? t("action.unsubscribe") : format("reader.unsubscribeFrom", listName);

  if (info.mailto !== undefined) {
    const { to, subject, body } = info.mailto;
    return (
      <button
        type="button"
        className={styles.unsubscribe}
        onClick={() => {
          onUnsubscribeByMail(to, subject, body);
        }}
        aria-label={label}
        title={t("reader.unsubscribeLatency")}
      >
        {t("action.unsubscribe")}
      </button>
    );
  }

  if (info.url === undefined) return null;

  /*
   * TODO(E-server): RFC 8058 one-click. `info.oneClick` says the sender
   * accepts a bare POST to this URI, which would spare the user the round
   * trip to a landing page — but the POST cannot be made from the browser
   * (cross-origin, and it would leak the reader's IP to the sender). It
   * belongs on the server, in a later epic; until then the link is the
   * honest path and the flag is parsed and carried for it.
   */
  return (
    <a
      className={styles.unsubscribe}
      href={info.url}
      target="_blank"
      rel="noopener noreferrer"
      aria-label={label}
      title={t("reader.unsubscribeOpensTab")}
    >
      {t("action.unsubscribe")}
    </a>
  );
}

/**
 * "Show original" — the raw RFC 822 headers (canon §2.2).
 *
 * The bytes come through the SAME authenticated blob path the download button
 * uses (the download route needs HTTP Basic, which a navigation cannot carry —
 * see {@link DownloadOriginalButton}), and only the header section is rendered:
 * a 4 MB message with a base64 attachment must not become 4 MB of DOM.
 *
 * Rendered as TEXT in a `<pre>`, never as HTML. The content is attacker-
 * controlled by definition, and this is one of the few places in the app that
 * shows it verbatim.
 */
function OriginalDialog({
  isOpen,
  onClose,
  email,
  client,
  accountId,
}: {
  readonly isOpen: boolean;
  readonly onClose: () => void;
  readonly email: Email;
  readonly client: JmapClient;
  readonly accountId: string;
}): React.JSX.Element {
  const { t } = useTranslation();
  const dialogRef = useRef<HTMLDialogElement | null>(null);
  const returnFocusRef = useRef<HTMLElement | null>(null);
  const [headers, setHeaders] = useState<string | undefined>(undefined);
  const [state, setState] = useState<"idle" | "loading" | "failed">("idle");
  const [copied, setCopied] = useState<"idle" | "done" | "failed">("idle");

  // The same <dialog> pattern as ShortcutsDialog: showModal() makes the rest
  // of the page inert, traps focus and handles Escape, none of which a
  // hand-rolled overlay gets right. Focus restoration is explicit because not
  // every browser does it.
  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) return;
    if (isOpen && !dialog.open) {
      returnFocusRef.current =
        document.activeElement instanceof HTMLElement ? document.activeElement : null;
      dialog.showModal();
    } else if (!isOpen && dialog.open) {
      dialog.close();
      returnFocusRef.current?.focus();
    }
  }, [isOpen]);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) return undefined;
    const handleClose = (): void => {
      returnFocusRef.current?.focus();
      onClose();
    };
    dialog.addEventListener("close", handleClose);
    return () => {
      dialog.removeEventListener("close", handleClose);
    };
  }, [onClose]);

  const blobId = email.blobId;

  useEffect(() => {
    if (!isOpen || blobId === undefined) return undefined;
    let cancelled = false;
    setState("loading");
    setCopied("idle");
    void (async () => {
      try {
        const blob = await client.downloadBlob(accountId, blobId, "message.eml", "message/rfc822");
        const raw = await blob.text();
        if (cancelled) return;
        setHeaders(unfoldHeaders(headerSection(raw)));
        setState("idle");
      } catch {
        if (!cancelled) setState("failed");
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [isOpen, client, accountId, blobId]);

  const copy = useCallback((): void => {
    if (headers === undefined) return;
    // `navigator.clipboard` is absent in insecure contexts and in jsdom; the
    // failure is REPORTED rather than swallowed, because a Copy button that
    // silently does nothing is the most confusing control in any UI.
    const clipboard = navigator.clipboard as Clipboard | undefined;
    if (clipboard === undefined) {
      setCopied("failed");
      return;
    }
    void clipboard.writeText(headers).then(
      () => {
        setCopied("done");
      },
      () => {
        setCopied("failed");
      },
    );
  }, [headers]);

  return (
    <dialog ref={dialogRef} className={styles.originalDialog} aria-labelledby="original-title">
      <div className={styles.originalContent}>
        <div className={styles.originalHeader}>
          <h2 className={styles.originalTitle} id="original-title">
            {t("reader.originalTitle")}
          </h2>
          <button
            type="button"
            className={styles.close}
            onClick={onClose}
            aria-label={t("shortcuts.close")}
          >
            <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" aria-hidden="true" focusable="false">
              <path d="M5.5 5.5l9 9M14.5 5.5l-9 9" />
            </svg>
          </button>
        </div>

        <p className={styles.originalSubtitle}>{t("reader.originalHeaders")}</p>

        {state === "loading" && <p className={styles.mutedText}>{t("reader.originalLoading")}</p>}
        {state === "failed" && <p className={styles.errorTitle}>{t("reader.originalFailed")}</p>}
        {state === "idle" && headers !== undefined && (
          <pre className={styles.originalPre}>{headers}</pre>
        )}

        <div className={styles.originalActions}>
          <button
            type="button"
            className={styles.secondaryAction}
            onClick={copy}
            disabled={headers === undefined}
          >
            {t("reader.copy")}
          </button>
          {/* Always present so the outcome is ANNOUNCED when it appears,
              rather than a live region being inserted with its own text. */}
          <span role="status" aria-live="polite" className={styles.downloadStatus}>
            {copied === "done" ? t("reader.copied") : ""}
            {copied === "failed" ? t("reader.copyFailed") : ""}
          </span>
        </div>
      </div>
    </dialog>
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
