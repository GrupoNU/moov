import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import type { JmapClient } from "../../api/jmap";
import { useTranslation } from "../../i18n/I18nProvider";
import { chipsToWire, hasValidRecipients, type AddressChip } from "../../mail/addresses";
import { createAutosaveScheduler, secondsUntil, parseSendAt } from "../../mail/drafts";
import { formatBytes, formatFullDate } from "../../mail/format";
import {
  cancelSubmission,
  destroyMessages,
  firstFailureMessage,
  hasFailures,
  maxAttachmentsSize,
  maxUploadSize,
  saveDraft,
  sendDraft,
  uploadBlob,
  uploadUrlFor,
  UploadError,
  type DraftSpec,
  type Identity,
} from "../../mail/write";
import { htmlToText, textToHtml } from "../../mail/quoting";
import { AddressField } from "./AddressField";
import { AttachmentList, type ComposerAttachment } from "./AttachmentList";
import { BodyEditor } from "./BodyEditor";
import type { ComposerDraft } from "./composerState";
import { ScheduleMenu } from "./ScheduleMenu";
import styles from "./Composer.module.css";

/**
 * The composer: a modal dialog that writes, saves and sends a message.
 *
 * # Focus and the dialog contract
 *
 * This is a real `<dialog>` opened with `showModal()`, which gives the
 * browser's own focus trap, its own `Escape` handling, and the top-layer
 * stacking that no `z-index` fight can lose. Reimplementing those in JavaScript
 * is the classic source of "Tab escapes the dialog and lands in the message
 * list behind it".
 *
 * `Escape` is intercepted rather than allowed to close: **closing a composer
 * must never lose a draft** (deliverable 7). The handler flushes the autosave
 * first, so what the user wrote is on the server before the dialog goes away.
 *
 * # Sending, and the one thing that must never happen
 *
 * A double-click on Send must not produce two messages. The guard is a REF,
 * not state: `setState` is asynchronous and two clicks in the same tick both
 * read the old value, which is exactly how double-send bugs ship. `sendingRef`
 * is set synchronously before the first `await`.
 */

export interface ComposerProps {
  readonly draft: ComposerDraft;
  readonly client: JmapClient;
  readonly accountId: string;
  readonly identity: Identity | undefined;
  readonly draftsMailboxId: string | undefined;
  readonly sentMailboxId: string | undefined;
  readonly sessionCapabilities: Readonly<Record<string, unknown>> | undefined;
  readonly uploadUrlTemplate: string | undefined;
  readonly authorization: string;
  readonly onClose: () => void;
  /** Announced by the parent's live region: a send, a cancel, a discard. */
  readonly onNotify: (message: string) => void;
  /** Called after a send or discard so the list can refresh. */
  readonly onChanged: () => void;
  /**
   * E9: queues this message for later instead of sending it now.
   *
   * Present only when the browser has durable storage for the Outbox. Resolves
   * FALSE when the queue write did not commit, which is treated as a refusal to
   * close — see {@link Composer}'s send path. Absent means there is no Outbox,
   * and an offline send fails honestly rather than promising a queue that
   * cannot hold anything.
   */
  readonly onQueueOffline?: (spec: DraftSpec) => Promise<boolean>;
  /** E9: `navigator.onLine`, so Send can queue rather than fail. */
  readonly isOnline?: boolean;
  /**
   * E4: how far ahead this server accepts a scheduled send, in seconds
   * (the session's `maxDelayedSendSeconds` — 30 days on this one).
   *
   * `undefined` means the server does not advertise schedule send, and the
   * control is not rendered at all. Read from the session rather than assumed,
   * because "declared == applied" is the server's own rule (J1) and a picker
   * offering a date the server will refuse is the client half of breaking it.
   */
  readonly maxDelayedSendSeconds?: number | undefined;
}

export function Composer({
  draft,
  client,
  accountId,
  identity,
  draftsMailboxId,
  sentMailboxId,
  sessionCapabilities,
  uploadUrlTemplate,
  authorization,
  onClose,
  onNotify,
  onChanged,
  onQueueOffline,
  isOnline = true,
  maxDelayedSendSeconds,
}: ComposerProps): React.JSX.Element {
  const { t, format, locale } = useTranslation();
  const dialogRef = useRef<HTMLDialogElement | null>(null);
  const fileInputRef = useRef<HTMLInputElement | null>(null);

  const [to, setTo] = useState<readonly AddressChip[]>(draft.to);
  const [cc, setCc] = useState<readonly AddressChip[]>(draft.cc);
  const [bcc, setBcc] = useState<readonly AddressChip[]>(draft.bcc);
  const [showCc, setShowCc] = useState(draft.cc.length > 0);
  const [showBcc, setShowBcc] = useState(draft.bcc.length > 0);
  const [subject, setSubject] = useState(draft.subject);
  const [text, setText] = useState(draft.text);
  const [html, setHtml] = useState(draft.html ?? "");
  const [isRich, setRich] = useState(draft.html !== undefined);
  const [attachments, setAttachments] = useState<readonly ComposerAttachment[]>([]);

  const [draftId, setDraftId] = useState<string | undefined>(draft.existingDraftId);
  const [saveState, setSaveState] = useState<"idle" | "saving" | "saved" | "failed">("idle");
  const [saveError, setSaveError] = useState<string | undefined>(undefined);
  const [sendError, setSendError] = useState<string | undefined>(undefined);
  const [pending, setPending] = useState<
    { readonly submissionId: string; readonly sendAt: number } | undefined
  >(undefined);
  const [secondsLeft, setSecondsLeft] = useState(0);
  const [isSending, setSending] = useState(false);

  /** Set SYNCHRONOUSLY before the first await — see the file header. */
  const sendingRef = useRef(false);
  /** The id of the draft on the server, readable from callbacks without a stale closure. */
  const draftIdRef = useRef<string | undefined>(draft.existingDraftId);
  useEffect(() => {
    draftIdRef.current = draftId;
  }, [draftId]);

  const uploadLimit = useMemo(() => maxUploadSize(sessionCapabilities), [sessionCapabilities]);
  const attachmentsLimit = useMemo(
    () => maxAttachmentsSize(sessionCapabilities),
    [sessionCapabilities],
  );

  // --- the dialog ----------------------------------------------------------

  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) return undefined;
    if (!dialog.open) dialog.showModal();
    return () => {
      if (dialog.open) dialog.close();
    };
  }, []);

  // --- the message the server will receive ---------------------------------

  /**
   * The draft spec, rebuilt on every relevant change.
   *
   * In rich mode BOTH parts are sent: the HTML the user composed and a
   * plain-text rendering of it. A multipart/alternative with a real text part
   * is what makes a message readable in a text client, in a notification
   * preview, and in the search index — an HTML-only message is a message that
   * reads as blank in all three.
   */
  const spec = useMemo<DraftSpec | undefined>(() => {
    if (draftsMailboxId === undefined || identity === undefined) return undefined;

    const readyAttachments = attachments
      .filter((attachment): attachment is Extract<ComposerAttachment, { kind: "ready" }> =>
        attachment.kind === "ready",
      )
      .map((attachment) => ({
        blobId: attachment.blobId,
        name: attachment.name,
        type: attachment.type,
        size: attachment.size,
      }));

    const bodyHtml = isRich ? withSignature(html, identity.htmlSignature, true) : undefined;
    const bodyText = isRich
      ? htmlToText(bodyHtml ?? "")
      : withSignature(text, identity.textSignature, false);

    return {
      mailboxId: draftsMailboxId,
      from: [{ name: identity.name === "" ? null : identity.name, email: identity.email }],
      to: chipsToWire(to),
      cc: chipsToWire(cc),
      bcc: chipsToWire(bcc),
      subject,
      text: bodyText,
      ...(bodyHtml !== undefined ? { html: bodyHtml } : {}),
      attachments: readyAttachments,
      ...(draft.inReplyTo !== undefined ? { inReplyTo: draft.inReplyTo } : {}),
      ...(draft.references !== undefined ? { references: draft.references } : {}),
    };
  }, [
    draftsMailboxId,
    identity,
    attachments,
    isRich,
    html,
    text,
    to,
    cc,
    bcc,
    subject,
    draft.inReplyTo,
    draft.references,
  ]);

  const specRef = useRef(spec);
  useEffect(() => {
    specRef.current = spec;
  }, [spec]);

  // --- autosave ------------------------------------------------------------

  const persist = useCallback(async (): Promise<void> => {
    const current = specRef.current;
    if (current === undefined) return;
    // Never save an empty shell: a composer opened and closed immediately must
    // not litter Drafts with a blank message.
    if (isEmptyDraft(current)) return;

    setSaveState("saving");
    try {
      const { draft: saved, outcome } = await saveDraft(
        client,
        accountId,
        current,
        draftIdRef.current,
      );
      if (saved === undefined) {
        setSaveState("failed");
        setSaveError(firstFailureMessage(outcome) ?? t("draft.saveFailed"));
        return;
      }
      draftIdRef.current = saved.id;
      setDraftId(saved.id);
      setSaveState("saved");
      setSaveError(undefined);
    } catch (error) {
      setSaveState("failed");
      // The server's own words, never a generic sentence.
      setSaveError(error instanceof Error ? error.message : String(error));
    }
  }, [client, accountId, t]);

  const scheduler = useMemo(
    () =>
      createAutosaveScheduler(() => {
        void persist();
      }),
    [persist],
  );

  useEffect(
    () => () => {
      scheduler.cancel();
    },
    [scheduler],
  );

  /** Every edit marks the draft dirty and schedules the save. */
  const touched = useCallback((): void => {
    setSaveState((current) => (current === "saved" ? "idle" : current));
    scheduler.touch();
  }, [scheduler]);

  // --- attachments ---------------------------------------------------------

  const attachFiles = useCallback(
    (files: FileList | null): void => {
      if (files === null) return;
      for (const file of Array.from(files)) {
        const key = `att-${String(Date.now())}-${file.name}-${String(Math.random()).slice(2, 8)}`;

        /*
         * The client-side gate reads the SERVER's advertised maxSizeUpload
         * (declared == applied, J1's rule). Refusing here is a courtesy —
         * the server enforces it regardless — but it saves the user
         * uploading 40 MB before being told no.
         */
        if (uploadLimit !== undefined && file.size > uploadLimit) {
          setAttachments((current) => [
            ...current,
            {
              kind: "failed",
              key,
              name: file.name,
              size: file.size,
              type: file.type,
              message: format("send.sizeExceeded", formatBytes(uploadLimit, locale)),
            },
          ]);
          continue;
        }

        const alreadyAttached = attachments
          .filter((item) => item.kind !== "failed")
          .reduce((sum, item) => sum + item.size, 0);
        if (attachmentsLimit !== undefined && alreadyAttached + file.size > attachmentsLimit) {
          setAttachments((current) => [
            ...current,
            {
              kind: "failed",
              key,
              name: file.name,
              size: file.size,
              type: file.type,
              message: format("send.attachmentsExceeded", formatBytes(attachmentsLimit, locale)),
            },
          ]);
          continue;
        }

        setAttachments((current) => [
          ...current,
          { kind: "uploading", key, name: file.name, size: file.size, type: file.type, percent: 0 },
        ]);

        void (async () => {
          try {
            const uploaded = await uploadBlob(
              uploadUrlFor(uploadUrlTemplate, accountId),
              authorization,
              file,
              {
                onProgress: (loaded, total) => {
                  const percent = total > 0 ? Math.round((loaded / total) * 100) : 0;
                  setAttachments((current) =>
                    current.map((item) =>
                      item.key === key && item.kind === "uploading"
                        ? { ...item, percent }
                        : item,
                    ),
                  );
                },
              },
            );
            setAttachments((current) =>
              current.map((item) =>
                item.key === key
                  ? {
                      kind: "ready",
                      key,
                      name: file.name,
                      size: uploaded.size,
                      type: uploaded.type,
                      blobId: uploaded.blobId,
                    }
                  : item,
              ),
            );
            touched();
          } catch (error) {
            // The upload endpoint answers RFC 7807 problem details; its own
            // sentence ("the uploaded file exceeds maxSizeUpload") is what the
            // user must read.
            const message =
              error instanceof UploadError
                ? error.message
                : error instanceof Error
                  ? error.message
                  : t("compose.uploadFailed");
            setAttachments((current) =>
              current.map((item) =>
                item.key === key
                  ? { kind: "failed", key, name: file.name, size: file.size, type: file.type, message }
                  : item,
              ),
            );
          }
        })();
      }
    },
    [
      uploadLimit,
      attachmentsLimit,
      attachments,
      accountId,
      authorization,
      uploadUrlTemplate,
      touched,
      format,
      locale,
      t,
    ],
  );

  const removeAttachment = useCallback(
    (key: string): void => {
      setAttachments((current) => current.filter((item) => item.key !== key));
      touched();
    },
    [touched],
  );

  // --- sending -------------------------------------------------------------

  const canSend =
    hasValidRecipients(to, cc, bcc) &&
    !attachments.some((item) => item.kind === "uploading") &&
    spec !== undefined &&
    identity !== undefined;

  const send = useCallback(async (): Promise<void> => {
    // The double-send guard: a REF, set before any await. Two clicks in one
    // tick both read stale state, which is how double-send bugs ship.
    if (sendingRef.current) return;
    const current = specRef.current;
    if (current === undefined || identity === undefined) return;

    sendingRef.current = true;
    setSending(true);
    setSendError(undefined);
    // A scheduled autosave must not create a second copy of the message we are
    // about to send as a draft.
    scheduler.cancel();

    /*
     * E9: with no network, Send means "put it in the Outbox".
     *
     * Checked BEFORE the request rather than as a catch on its failure, and
     * that is deliberate: a `sendDraft` that fails mid-flight may or may not
     * have reached the server, and queueing a message that might already be
     * sent is how a recipient gets it twice. `navigator.onLine === false` is
     * the one signal that means the request provably never left.
     */
    if (!isOnline && onQueueOffline !== undefined) {
      try {
        const queued = await onQueueOffline(current);
        if (!queued) {
          // The write did not commit. Do NOT close: the text on screen is the
          // only copy left, and the message says so.
          setSendError(t("outbox.queueFailed"));
          return;
        }
        onChanged();
        onClose();
      } catch (error) {
        setSendError(error instanceof Error ? error.message : String(error));
      } finally {
        sendingRef.current = false;
        setSending(false);
      }
      return;
    }

    try {
      const result = await sendDraft(client, accountId, current, {
        identityId: identity.id,
        sentMailboxId,
        previousDraftId: draftIdRef.current,
      });

      if (result.submission === undefined) {
        setSendError(firstFailureMessage(result.outcome) ?? t("send.failedTitle"));
        return;
      }

      // The draft that the send created now belongs to the submission; it must
      // not be destroyed by a later save.
      draftIdRef.current = undefined;
      setDraftId(undefined);

      const sendAt = parseSendAt(result.submission.sendAt);
      if (sendAt === undefined || secondsUntil(sendAt, Date.now()) === 0) {
        // No window (or one already elapsed): the message is on its way.
        onNotify(t("send.sent"));
        onChanged();
        onClose();
        return;
      }
      setPending({ submissionId: result.submission.id, sendAt });
      setSecondsLeft(secondsUntil(sendAt, Date.now()));
    } catch (error) {
      setSendError(error instanceof Error ? error.message : String(error));
    } finally {
      sendingRef.current = false;
      setSending(false);
    }
  }, [
    client,
    accountId,
    identity,
    sentMailboxId,
    scheduler,
    t,
    onNotify,
    onChanged,
    onClose,
    isOnline,
    onQueueOffline,
  ]);

  /**
   * E4: sends this message at a chosen future instant (canon §2.3).
   *
   * # Why it is a separate path and not `send` with an argument
   *
   * Everything after the request differs. An ordinary send opens the undo
   * window and holds the composer open with a countdown; a scheduled one has
   * NO undo window — its `sendAt` is days out, so the "undo" is the Scheduled
   * view's own "Cancel send", available for as long as the schedule lasts. So
   * this closes the composer immediately and says when the message goes out.
   *
   * # What the server does that this must not fight
   *
   * The message STAYS A DRAFT. `sendDraft` omits `onSuccessUpdateEmail` for a
   * scheduled send and the server suppresses it anyway (`holdsItsDraft`),
   * because a message scheduled for Friday sitting in Sent from Tuesday would
   * tell the user it was already sent and would not be a draft they could
   * edit. `draftIdRef` is therefore NOT cleared: the draft is still this
   * composer's, and a later save must still replace rather than duplicate it.
   *
   * The cap of 100 comes back as `overQuota` with the server's own sentence,
   * which is shown verbatim — it already names the number and says what to do.
   */
  const schedule = useCallback(
    async (until: string): Promise<void> => {
      if (sendingRef.current) return;
      const current = specRef.current;
      if (current === undefined || identity === undefined) return;

      sendingRef.current = true;
      setSending(true);
      setSendError(undefined);
      scheduler.cancel();

      try {
        const result = await sendDraft(client, accountId, current, {
          identityId: identity.id,
          sentMailboxId,
          previousDraftId: draftIdRef.current,
          sendAt: until,
        });
        if (result.submission === undefined) {
          setSendError(firstFailureMessage(result.outcome) ?? t("send.failedTitle"));
          return;
        }
        onNotify(format("schedule.scheduled", formatScheduled(until, locale)));
        onChanged();
        onClose();
      } catch (error) {
        setSendError(error instanceof Error ? error.message : String(error));
      } finally {
        sendingRef.current = false;
        setSending(false);
      }
    },
    [
      client,
      accountId,
      identity,
      sentMailboxId,
      scheduler,
      t,
      format,
      locale,
      onNotify,
      onChanged,
      onClose,
    ],
  );

  /** The countdown. One interval, cleared on every exit path. */
  useEffect(() => {
    if (pending === undefined) return undefined;
    const tick = (): void => {
      const left = secondsUntil(pending.sendAt, Date.now());
      setSecondsLeft(left);
      if (left === 0) {
        // The window closed: the server is transmitting. Nothing to undo.
        setPending(undefined);
        onNotify(t("send.sent"));
        onChanged();
        onClose();
      }
    };
    const timer = setInterval(tick, 250);
    return () => {
      clearInterval(timer);
    };
  }, [pending, onNotify, onChanged, onClose, t]);

  const undo = useCallback(async (): Promise<void> => {
    if (pending === undefined) return;
    const { submissionId } = pending;
    setPending(undefined);
    try {
      const outcome = await cancelSubmission(client, accountId, submissionId);
      if (hasFailures(outcome)) {
        /*
         * `cannotUnsend` is a TRUE statement: the mail is going out. A user
         * who believes a send was canceled and later finds it in Sent has been
         * lied to, so the refusal is surfaced rather than swallowed.
         */
        setSendError(firstFailureMessage(outcome) ?? t("send.cannotUnsend"));
        onChanged();
        return;
      }
      onNotify(t("send.canceled"));
      onChanged();
      onClose();
    } catch (error) {
      setSendError(error instanceof Error ? error.message : String(error));
    }
  }, [pending, client, accountId, t, onNotify, onChanged, onClose]);

  // --- discard and close ---------------------------------------------------

  const discard = useCallback(async (): Promise<void> => {
    if (!window.confirm(t("draft.discardConfirm"))) return;
    scheduler.cancel();
    const id = draftIdRef.current;
    if (id !== undefined) {
      try {
        const outcome = await destroyMessages(client, accountId, [id]);
        if (hasFailures(outcome)) {
          setSaveError(firstFailureMessage(outcome) ?? t("draft.discardFailed"));
          return;
        }
      } catch (error) {
        setSaveError(error instanceof Error ? error.message : String(error));
        return;
      }
    }
    onNotify(t("draft.discarded"));
    onChanged();
    onClose();
  }, [client, accountId, scheduler, t, onNotify, onChanged, onClose]);

  /** Closing flushes the autosave first — closing must never lose a draft. */
  const closeWithSave = useCallback((): void => {
    scheduler.flush();
    onClose();
  }, [scheduler, onClose]);

  // Escape is intercepted so the flush happens; letting the dialog close
  // natively would discard whatever had not been saved yet.
  const onDialogCancel = useCallback(
    (event: React.SyntheticEvent<HTMLDialogElement>): void => {
      event.preventDefault();
      closeWithSave();
    },
    [closeWithSave],
  );

  const title =
    draft.intent === "reply" || draft.intent === "replyAll"
      ? t("compose.titleReply")
      : draft.intent === "forward"
        ? t("compose.titleForward")
        : draft.intent === "draft"
          ? t("compose.titleDraft")
          : t("compose.title");

  return (
    <dialog
      ref={dialogRef}
      className={styles.dialog}
      aria-label={title}
      onCancel={onDialogCancel}
    >
      <form
        className={styles.form}
        onSubmit={(event) => {
          event.preventDefault();
          void send();
        }}
      >
        <header className={styles.header}>
          <h2 className={styles.title}>{title}</h2>
          <button
            type="button"
            className={styles.iconButton}
            onClick={closeWithSave}
            aria-label={t("compose.close")}
            title={t("compose.close")}
          >
            <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round">
              <path d="M5.5 5.5l9 9M14.5 5.5l-9 9" />
            </svg>
          </button>
        </header>

        {identity !== undefined && (
          <p className={styles.fromLine}>
            <span className={styles.fromLabel}>{t("compose.from")}</span>
            <span>{identity.name === "" ? identity.email : `${identity.name} <${identity.email}>`}</span>
          </p>
        )}

        <AddressField
          label={t("compose.to")}
          chips={to}
          onChange={(next) => {
            setTo(next);
            touched();
          }}
          autoFocusField={draft.focusField === "to"}
          trailing={
            <div className={styles.ccToggles}>
              {!showCc && (
                <button
                  type="button"
                  className={styles.linkButton}
                  onClick={() => {
                    setShowCc(true);
                  }}
                >
                  {t("compose.showCc")}
                </button>
              )}
              {!showBcc && (
                <button
                  type="button"
                  className={styles.linkButton}
                  onClick={() => {
                    setShowBcc(true);
                  }}
                >
                  {t("compose.showBcc")}
                </button>
              )}
            </div>
          }
        />

        {showCc && (
          <AddressField
            label={t("compose.cc")}
            chips={cc}
            onChange={(next) => {
              setCc(next);
              touched();
            }}
          />
        )}
        {showBcc && (
          <AddressField
            label={t("compose.bcc")}
            chips={bcc}
            onChange={(next) => {
              setBcc(next);
              touched();
            }}
          />
        )}

        <div className={styles.subjectRow}>
          <label className={styles.subjectLabel} htmlFor="composer-subject">
            {t("compose.subject")}
          </label>
          <input
            id="composer-subject"
            className={styles.subjectInput}
            type="text"
            value={subject}
            placeholder={t("compose.subjectPlaceholder")}
            /* See AddressField: a modal dialog must place focus inside itself
               (WAI-ARIA APG), which is the opposite of the load-time focus
               theft this rule exists to prevent. */
            // eslint-disable-next-line jsx-a11y/no-autofocus
            autoFocus={draft.focusField === "subject"}
            onChange={(event) => {
              setSubject(event.target.value);
              touched();
            }}
          />
        </div>

        <BodyEditor
          isRich={isRich}
          onToggleRich={(next) => {
            /*
             * Switching modes CONVERTS rather than discards. Going rich turns
             * the text into paragraphs; going plain renders the HTML down.
             * Either way the user's words survive, which is the only
             * acceptable behaviour for a toggle next to a message they wrote.
             */
            if (next && html === "") setHtml(textToHtml(text));
            if (!next && text.trim() === "" && html !== "") setText(htmlToText(html));
            setRich(next);
            touched();
          }}
          text={text}
          onTextChange={(next) => {
            setText(next);
            touched();
          }}
          html={html}
          onHtmlChange={(next) => {
            setHtml(next);
            touched();
          }}
          seedKey={draft.seedKey}
        />

        <AttachmentList attachments={attachments} onRemove={removeAttachment} />

        {(sendError !== undefined || saveError !== undefined) && (
          <div className={styles.errorBanner} role="alert">
            <strong>{sendError !== undefined ? t("send.failedTitle") : t("draft.saveFailed")}</strong>
            {/* The SERVER's sentence, verbatim — the pilot's lesson. */}
            <span>{sendError ?? saveError}</span>
          </div>
        )}

        <footer className={styles.footer}>
          <div className={styles.footerLeft}>
            <button
              type="submit"
              className={styles.send}
              disabled={!canSend || isSending || pending !== undefined}
            >
              {isSending ? t("compose.sending") : t("compose.send")}
            </button>

            {/*
              E4: "Schedule send" — a SECONDARY action beside Send, not a
              variant of it (canon §2.3).

              Two reasons it is its own control rather than a dropdown ON the
              send button. First, Send must stay a single unambiguous click:
              splitting it means a user aiming for "send" can land on a caret
              and open a menu instead, which is the one place in a mail client
              where a mis-click is expensive. Second, it is UNAVAILABLE offline
              — a schedule is a promise only the server can keep, and there is
              no queue for it — so it has to be able to disappear without
              taking Send with it.
            */}
            {maxDelayedSendSeconds !== undefined && isOnline && (
              <ScheduleMenu
                disabled={!canSend || isSending || pending !== undefined}
                maxDelayedSendSeconds={maxDelayedSendSeconds}
                onSchedule={(sendAt) => {
                  void schedule(sendAt);
                }}
                triggerClassName={styles.iconButton}
                triggerContent={
                  <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
                    <circle cx="10" cy="11" r="6.3" />
                    <path d="M10 7.6v3.6l2.4 1.4" />
                    <path d="M16.2 4.4l-2.6 2.6m2.6-2.6h-2.4m2.4 0v2.4" />
                  </svg>
                }
              />
            )}

            <input
              ref={fileInputRef}
              className={styles.fileInput}
              type="file"
              multiple
              aria-label={t("compose.attach")}
              onChange={(event) => {
                attachFiles(event.target.files);
                // Reset, so attaching the same file twice in a row fires again.
                event.target.value = "";
              }}
            />
            <button
              type="button"
              className={styles.iconButton}
              onClick={() => {
                fileInputRef.current?.click();
              }}
              aria-label={t("compose.attach")}
              title={t("compose.attach")}
            >
              <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" aria-hidden="true" focusable="false">
                <path d="M14.5 9.2l-5 5a3.1 3.1 0 0 1-4.4-4.4l6-6a2.1 2.1 0 1 1 3 3l-6 6a1.1 1.1 0 0 1-1.5-1.5l5.3-5.3" />
              </svg>
            </button>

            <button type="button" className={styles.discard} onClick={() => void discard()}>
              {t("compose.discard")}
            </button>
          </div>

          {/* The draft status. Always in the DOM so its changes are announced. */}
          <p className={styles.status} role="status" aria-live="polite">
            {saveState === "saving"
              ? t("draft.saving")
              : saveState === "saved"
                ? t("draft.saved")
                : ""}
          </p>
        </footer>

        {pending !== undefined && (
          <div className={styles.undoBar} role="status" aria-live="assertive">
            <span>{format("send.undoWindow", secondsLeft)}</span>
            <button type="button" className={styles.undoButton} onClick={() => void undo()}>
              {t("send.undo")}
            </button>
          </div>
        )}

        <p className="visually-hidden">
          {draft.reference !== undefined
            ? formatFullDate(draft.reference, locale)
            : ""}
        </p>
      </form>
    </dialog>
  );
}

/**
 * Appends the identity's signature, if any and if not already present.
 *
 * The idempotence check matters for a resumed draft: the signature was already
 * appended when the draft was first composed, and appending it again on every
 * save would grow a message with one signature per autosave.
 */
function withSignature(body: string, signature: string, isHtml: boolean): string {
  if (signature === "") return body;
  if (body.includes(signature)) return body;
  // "-- " on its own line is the RFC 3676 §4.3 signature delimiter that mail
  // clients recognise and collapse.
  return isHtml
    ? `${body}<div><br></div><div class="moov-signature">${signature}</div>`
    : `${body}\n\n-- \n${signature}`;
}

/** True when there is nothing worth saving. */
function isEmptyDraft(spec: DraftSpec): boolean {
  return (
    spec.subject.trim() === "" &&
    spec.text.trim() === "" &&
    (spec.html ?? "").trim() === "" &&
    spec.to.length === 0 &&
    spec.cc.length === 0 &&
    spec.bcc.length === 0 &&
    spec.attachments.length === 0
  );
}

/**
 * The scheduled instant, for the toast that confirms it (E4).
 *
 * Absolute rather than relative: "in 3 days" is not something a user can check
 * against a calendar, and the whole value of the confirmation is that they can.
 */
function formatScheduled(sendAt: string, locale: string): string {
  const at = new Date(sendAt);
  if (Number.isNaN(at.getTime())) return sendAt;
  return at.toLocaleString(locale, {
    weekday: "short",
    day: "numeric",
    month: "short",
    hour: "2-digit",
    minute: "2-digit",
  });
}
