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
import { htmlToText, textToHtml, type ComposeIntent } from "../../mail/quoting";
import { usePrefs } from "../../mail/PrefsProvider";
import { resolveSignature } from "../../mail/prefs";
import type { IndexedAddress } from "../../mail/addressIndex";
import { isBlockedAttachment } from "../../mail/blockedExtensions";
import { loadBodyMode, saveBodyMode } from "../../mail/composePrefs";
import { loadComposeFormatBar, saveComposeFormatBar } from "../../mail/viewChrome";
import { useConfirm } from "../../components/useConfirm";
import { AddressField } from "./AddressField";
import { AttachmentList, type ComposerAttachment } from "./AttachmentList";
import { BodyEditor } from "./BodyEditor";
import type { ComposerDraft } from "./composerState";
import { PopupMenu } from "../mail/PopupMenu";
import { ScheduleMenu } from "./ScheduleMenu";
import styles from "./Composer.module.css";

/**
 * The composer: a floating card, bottom-right, that writes, saves and sends a
 * message (canon 07 §7).
 *
 * # It was a centred modal until E12, and the reversal is the point
 *
 * P3 opened this with `showModal()`, for three real properties: the browser's
 * own focus trap, its own Escape, and top-layer stacking no `z-index` fight can
 * lose. Canon 07 §7 overrules it on the only axis this epic cares about —
 * Gmail's composer is a NON-MODAL card in the bottom-right corner, and the mail
 * behind it stays live. That is not cosmetic: it is what lets a person look up
 * an address, re-read the message they are answering, or start a second draft
 * without abandoning the first. A modal makes all three impossible.
 *
 * So it is still a `<dialog>` — for the top layer, which is the one property
 * worth keeping and the one that is genuinely hard to reproduce — but opened
 * with `show()` rather than `showModal()`. What that gives up:
 *
 *   - **the focus trap**, correctly. Trapping focus in a surface that leaves
 *     the page clickable is a lie about the page's state.
 *   - **the backdrop**, correctly. There is nothing to dim; the list behind is
 *     not inert.
 *   - **native Escape**, which this component already intercepted anyway (see
 *     below), so nothing changes there.
 *
 * # Minimise, and why it is a state and not a second component
 *
 * Gmail's card collapses to its own title bar, keeping the draft mounted and
 * the autosave running. Unmounting and remounting would be visibly different:
 * the body editor's selection, the attachment upload progress and the undo
 * countdown all live in this component's state, and a remount would discard
 * them. So minimising hides the FORM below the header with CSS and nothing
 * else changes — the draft is still there, still saving, still sending.
 *
 * `Escape` is intercepted rather than allowed to close: **closing a composer
 * must never lose a draft** (deliverable 7). The handler flushes the autosave
 * first, so what the user wrote is on the server before the card goes away.
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
  /**
   * E7: the address index the recipient fields complete from (canon §2.3).
   *
   * Absent means no autocomplete at all — the user opted out, or nothing has
   * been indexed yet. See `AddressField`: an empty index does not produce an
   * empty popup, it produces the field exactly as it was before E7.
   */
  readonly addressSuggestions?: readonly IndexedAddress[];
  /**
   * E7: records the addresses this message was sent to.
   *
   * Called on a SUCCESSFUL send only, which is the whole difference between
   * this and indexing what was typed: an address that failed to send is not one
   * you corresponded with, and it should not be promoted in a list you will
   * pick from tomorrow. Absent when the user has opted out.
   */
  readonly onRecordAddresses?: (addresses: readonly { name: string | null; email: string }[]) => void;
  /**
   * E7: archives the conversation this is a reply to (canon §2.3, "Send &
   * Archive").
   *
   * Present only for a reply that HAS a conversation to archive; its absence is
   * what removes the button. Resolves false when the archive failed, which the
   * composer surfaces — the message still went out, and saying only "sent"
   * would hide half of what the button promised.
   */
  readonly onSendAndArchive?: () => Promise<boolean>;
  /**
   * E7: undoes that archive when the send is cancelled inside the undo window.
   *
   * The archive happens IMMEDIATELY on send, Gmail-style, so the conversation
   * leaves the inbox while the undo window is still open. If the user then
   * undoes, the message never goes out — and a conversation archived for a
   * message that was never sent is a conversation the user has to go find. This
   * is the inverse, and it is why the archive is allowed to be immediate.
   */
  readonly onUndoArchive?: () => Promise<void>;
  /** E7: attachments the composer opens with — a forwarded `.eml`, say. */
  readonly initialAttachments?: readonly ComposerAttachment[];
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
  addressSuggestions,
  onRecordAddresses,
  onSendAndArchive,
  onUndoArchive,
  initialAttachments,
}: ComposerProps): React.JSX.Element {
  const { t, format, locale } = useTranslation();
  /*
   * E5 v2: the named signatures. Read from the provider rather than threaded
   * as a prop for the same reason the reader reads its own preference — this is
   * the only thing here that needs it, and `usePrefs` falls back to the
   * defaults outside a provider, so every existing composer test keeps working
   * unchanged and gets the Identity signature it always got.
   */
  const { prefs } = usePrefs();
  const dialogRef = useRef<HTMLDialogElement | null>(null);
  const fileInputRef = useRef<HTMLInputElement | null>(null);

  /**
   * E12: the card's size state (canon 07 §7).
   *
   * Three, not two, because Gmail has three and each does something the others
   * cannot: the default card, MINIMISED to its title bar (so the mail behind
   * is fully visible while a draft stays open and saving), and MAXIMISED to a
   * large centred panel (for a long message, where the corner card is a
   * letterbox).
   *
   * It is one state on one component rather than three components, and the
   * reason is what would be LOST by remounting: the body editor's selection,
   * the attachments' upload progress and the undo countdown all live in this
   * component. Minimising hides the form below the header with CSS; the draft
   * is still mounted, still autosaving, still able to finish sending.
   */
  const [cardSize, setCardSize] = useState<"normal" | "minimized" | "maximized">(
    "normal",
  );

  /** E11: the app's own confirm, replacing `window.confirm` for discard. */
  const { confirm, dialog: confirmDialog } = useConfirm();

  const [to, setTo] = useState<readonly AddressChip[]>(draft.to);
  const [cc, setCc] = useState<readonly AddressChip[]>(draft.cc);
  const [bcc, setBcc] = useState<readonly AddressChip[]>(draft.bcc);
  const [showCc, setShowCc] = useState(draft.cc.length > 0);
  const [showBcc, setShowBcc] = useState(draft.bcc.length > 0);
  const [subject, setSubject] = useState(draft.subject);
  const [text, setText] = useState(draft.text);
  const [html, setHtml] = useState(draft.html ?? "");
  /*
   * E7: the body mode.
   *
   * The stored preference applies ONLY to a composition that has no HTML of its
   * own — a brand-new message. A reply, a forward or a resumed draft arrives
   * carrying formatted content, and letting a remembered "plain" flatten it
   * would destroy the quoted material the user is replying to. The preference
   * is a default for new writing, never a filter over existing content.
   */
  const [isRich, setRich] = useState(
    draft.html !== undefined && (draft.html !== "" || loadBodyMode() === "rich"),
  );
  const [attachments, setAttachments] = useState<readonly ComposerAttachment[]>(
    initialAttachments ?? [],
  );
  /** E7: the honest refusal shown when a file is blocked outright. */
  const [blockedNotice, setBlockedNotice] = useState<string | undefined>(undefined);

  /*
   * D-01: the footer's formatting row, and the `Aa` that reveals it.
   *
   * The row is portalled into `formatHost` by `BodyEditor` — see that file's
   * header for why a portal rather than lifted state. The HOST is held in
   * component state rather than a ref because a ref assignment does not
   * re-render, and the portal cannot be created until the node exists: with a
   * ref the first render would portal into `null` and the row would never
   * appear until something else happened to re-render the composer.
   */
  const [formatHost, setFormatHost] = useState<HTMLDivElement | null>(null);
  const [showFormatBar, setShowFormatBar] = useState(() => loadComposeFormatBar());

  const [draftId, setDraftId] = useState<string | undefined>(draft.existingDraftId);
  const [saveState, setSaveState] = useState<"idle" | "saving" | "saved" | "failed">("idle");
  const [saveError, setSaveError] = useState<string | undefined>(undefined);
  const [sendError, setSendError] = useState<string | undefined>(undefined);
  const [pending, setPending] = useState<
    {
      readonly submissionId: string;
      readonly sendAt: number;
      /** E7: this send also archived the conversation, so an undo must restore it. */
      readonly archived: boolean;
    } | undefined
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
    /*
     * E12: `show()`, not `showModal()` (canon 07 §7).
     *
     * The card is NON-MODAL — the mail behind it stays live, which is what
     * lets someone look up an address or re-read the message they are
     * answering without abandoning the draft. It stays a `<dialog>` for the
     * TOP LAYER, which is the one property of the pair worth keeping: a
     * hand-rolled overlay eventually loses a `z-index` argument with a menu.
     *
     * `show()` does not focus anything by itself, so the composer's own
     * autofocus on the first empty field is what puts the caret where the user
     * expects it — the same behaviour `showModal()` produced, now explicit.
     */
    if (!dialog.open) dialog.show();
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

    /*
     * E5 v2 — the named signature (E7), resolved by the precedence rule
     * documented on `store.Prefs.Signatures` and mirrored on
     * `SignaturePrefs` / `resolveSignature`:
     *
     *   this PWA, new mail : prefs.signatures.forNew, else the Identity's own
     *   this PWA, a reply  : prefs.signatures.forReply, else the Identity's own
     *   any other client   : the Identity's own, always
     *
     * A FORWARD counts as a reply here, matching `signatureIntent`'s comment:
     * Gmail's setting is worded "on reply/forward", and the two share the case
     * because both are a message the user is continuing rather than starting.
     *
     * Nothing is injected by the server — this is a composer pre-fill, exactly
     * as RFC 8621 §6 says a client SHOULD do with the Identity's signature — so
     * two clients can only ever disagree about what was PRE-FILLED, never about
     * what was sent.
     */
    const named = resolveSignature(prefs.signatures, signatureIntent(draft.intent));
    const textSignature = named?.text ?? identity.textSignature;
    const htmlSignature = named?.html ?? identity.htmlSignature;

    const bodyHtml = isRich ? withSignature(html, htmlSignature, true) : undefined;
    const bodyText = isRich
      ? htmlToText(bodyHtml ?? "")
      : withSignature(text, textSignature, false);

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
    // E5 v2: which named signature the body starts with.
    draft.intent,
    prefs.signatures,
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
         * E7: the executable-extension block (canon §2.3, /mail/answer/6590).
         *
         * FIRST, before the size gates and before any byte is uploaded. This
         * is a hard refusal, not a warning: the file is not attached, does not
         * appear in the list even as "failed", and nothing is sent to the
         * server. A "failed" row would leave a blocked executable's NAME
         * sitting in the composer, which invites the user to try renaming it —
         * exactly the behaviour the block exists to prevent.
         */
        if (isBlockedAttachment(file.name)) {
          setBlockedNotice(format("compose.blockedExtension", file.name));
          continue;
        }

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

  /**
   * Sends the message, optionally archiving the conversation it replies to.
   *
   * # Why the archive happens immediately (E7, canon §2.3)
   *
   * Gmail's Send & Archive archives the moment you press it — the conversation
   * leaves the inbox while the undo window is still counting down. That looks
   * wrong until you consider the alternative: deferring the archive until the
   * window closes means the button's second promise is invisible for up to
   * thirty seconds, and the user, seeing the thread still in their inbox,
   * concludes the button did not work and archives it by hand.
   *
   * What makes immediacy safe is that the inverse exists. An undo inside the
   * window cancels the send AND restores the conversation, so the pair is
   * atomic from the user's point of view. That inverse is `onUndoArchive`, and
   * the test for it is the one that matters in this file.
   */
  const send = useCallback(async (alsoArchive = false): Promise<void> => {
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

      /*
       * E7: feed the address index from what was actually SENT.
       *
       * After the submission succeeded, never before: an address the server
       * refused is not one you corresponded with, and indexing it would promote
       * a typo into tomorrow's suggestions. Bcc is deliberately excluded here
       * too — see `addressesFromMessage` for that reasoning.
       */
      onRecordAddresses?.([...current.to, ...current.cc]);

      /*
       * The archive, immediate and before the undo window opens. See this
       * callback's header for why immediacy is the correct behaviour, and
       * `undo` below for the inverse that makes it safe.
       *
       * A failed archive does NOT fail the send: the message is gone, and the
       * only honest thing left is to say that the archive did not happen.
       */
      let archived = false;
      if (alsoArchive && onSendAndArchive !== undefined) {
        archived = await onSendAndArchive();
        if (!archived) setSendError(t("send.archiveFailed"));
      }

      const sendAt = parseSendAt(result.submission.sendAt);
      if (sendAt === undefined || secondsUntil(sendAt, Date.now()) === 0) {
        // No window (or one already elapsed): the message is on its way.
        onNotify(archived ? t("send.sentAndArchived") : t("send.sent"));
        onChanged();
        onClose();
        return;
      }
      setPending({ submissionId: result.submission.id, sendAt, archived });
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
    onRecordAddresses,
    onSendAndArchive,
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
        // The window closed: the server is transmitting. Nothing to undo, and
        // the archive that rode along is now permanent too.
        setPending(undefined);
        onNotify(pending.archived ? t("send.sentAndArchived") : t("send.sent"));
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
    const { submissionId, archived } = pending;
    setPending(undefined);
    try {
      const outcome = await cancelSubmission(client, accountId, submissionId);
      if (hasFailures(outcome)) {
        /*
         * `cannotUnsend` is a TRUE statement: the mail is going out. A user
         * who believes a send was canceled and later finds it in Sent has been
         * lied to, so the refusal is surfaced rather than swallowed.
         *
         * The archive is deliberately NOT undone here. The message is being
         * delivered, so the conversation genuinely has been replied to and
         * archived — restoring it would contradict what actually happened.
         */
        setSendError(firstFailureMessage(outcome) ?? t("send.cannotUnsend"));
        onChanged();
        return;
      }

      /*
       * E7: the inverse of Send & Archive.
       *
       * The cancel SUCCEEDED, which means the message provably never went out
       * (that is exactly what `canceled` means on this server — W3's undo is
       * proven by an empty Sent). A conversation archived on behalf of a
       * message that was never sent has to come back, or the user is left
       * hunting for a thread that left their inbox for no reason.
       *
       * Failures here are swallowed on purpose: `onChanged()` refetches, so a
       * conversation that failed to un-archive shows its true state
       * immediately, and the toast that matters is the one about the send.
       */
      if (archived && onUndoArchive !== undefined) {
        try {
          await onUndoArchive();
        } catch {
          // The refresh below tells the truth about where the thread is.
        }
      }

      onNotify(archived ? t("send.canceledUnarchived") : t("send.canceled"));
      onChanged();
      onClose();
    } catch (error) {
      setSendError(error instanceof Error ? error.message : String(error));
    }
  }, [pending, client, accountId, t, onNotify, onChanged, onClose, onUndoArchive]);

  // --- discard and close ---------------------------------------------------

  const discard = useCallback(async (): Promise<void> => {
    // E11: our own dialog, not the browser's. Same shape as the `window.confirm`
    // it replaces — one `await` longer — so the flow below is untouched.
    if (!(await confirm({ message: t("draft.discardConfirm"), destructive: true }))) return;
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
  }, [client, accountId, scheduler, t, onNotify, onChanged, onClose, confirm]);

  /**
   * Switches the body between the rich surface and the textarea.
   *
   * Extracted in E7 because there are now TWO controls that do it — the
   * editor's own mode buttons and the ⋯ menu's "Plain text mode" — and two
   * copies of the conversion would be two chances to drop the user's words.
   *
   * The conversion itself is unchanged from P3: going rich turns the text into
   * paragraphs, going plain renders the HTML down. Either way the words
   * survive, which is the only acceptable behaviour for a toggle sitting next
   * to a message someone wrote. What E7 adds is the persistence, so the choice
   * outlives this composer.
   */
  const toggleRich = useCallback(
    (next: boolean): void => {
      if (next && html === "") setHtml(textToHtml(text));
      if (!next && text.trim() === "" && html !== "") setText(htmlToText(html));
      setRich(next);
      saveBodyMode(next ? "rich" : "plain");
      touched();
    },
    [html, text, touched],
  );

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

  /*
   * E11 — the composer's own keys (canon §2.7's compose row).
   *
   * They live HERE rather than in the global resolver because the global one
   * refuses everything typed inside a text field — correctly, or `e` would
   * archive a message while you write one. That refusal is exactly why the
   * composer has to own its own keys.
   *
   * They are also the one place modifiers are ours rather than the browser's:
   * Ctrl+Enter, Ctrl+Shift+C and Ctrl+Shift+B are Gmail's, and none collides
   * with a browser default worth keeping inside a modal composer.
   */
  const onComposerKeyDown = useCallback(
    (event: KeyboardEvent): void => {
      const accel = event.ctrlKey || event.metaKey;
      if (!accel || event.altKey) return;

      // Ctrl+Enter sends — the one key every mail client agrees on.
      if (event.key === "Enter" && !event.shiftKey) {
        event.preventDefault();
        if (canSend) void send(false);
        return;
      }

      if (!event.shiftKey) return;

      /*
       * Ctrl+Shift+C / Ctrl+Shift+B reveal Cc/Bcc AND focus them.
       *
       * Resolved by `event.code`, for the same reason the global map is: on a
       * non-QWERTY layout `event.key` here would be a Cyrillic glyph and the
       * shortcut would be unreachable. `KeyC`/`KeyB` are the same physical
       * keys everywhere.
       *
       * Revealing without focusing would be the wrong half of the job: the
       * user pressed a key to type an address, not to look at a field.
       */
      if (event.code === "KeyC") {
        event.preventDefault();
        setShowCc(true);
        setFocusField("cc");
      } else if (event.code === "KeyB") {
        event.preventDefault();
        setShowBcc(true);
        setFocusField("bcc");
      }
    },
    [canSend, send],
  );

  /*
   * Which optional address field to focus once it has rendered.
   *
   * A state flag rather than a direct `.focus()` because the field may not be
   * in the DOM yet — `setShowCc(true)` in the same handler is what puts it
   * there, and focusing before React commits would hit nothing.
   */
  /*
   * Bound to the <dialog> element, not to the <form>.
   *
   * A `<form>` is not an interactive element, so a React `onKeyDown` on it is
   * a jsx-a11y error and the rule is right: the handler would be describing
   * behaviour on a node that cannot be focused. The dialog is the composer's
   * actual boundary — every key pressed inside it bubbles here, and nothing
   * outside it can reach this listener.
   */
  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) return undefined;
    dialog.addEventListener("keydown", onComposerKeyDown);
    return () => {
      dialog.removeEventListener("keydown", onComposerKeyDown);
    };
  }, [onComposerKeyDown]);

  const [focusField, setFocusField] = useState<"cc" | "bcc" | undefined>(undefined);
  const ccInputRef = useRef<HTMLInputElement | null>(null);
  const bccInputRef = useRef<HTMLInputElement | null>(null);
  useEffect(() => {
    if (focusField === undefined) return;
    const target = focusField === "cc" ? ccInputRef.current : bccInputRef.current;
    target?.focus();
    setFocusField(undefined);
  }, [focusField, showCc, showBcc]);

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
      className={[
        styles.dialog,
        cardSize === "minimized" ? styles.minimized : "",
        cardSize === "maximized" ? styles.maximized : "",
      ]
        .filter(Boolean)
        .join(" ")}
      aria-label={title}
      onCancel={onDialogCancel}
    >
      <form
        className={styles.form}
        onSubmit={(event) => {
          event.preventDefault();
          void send(false);
        }}
      >
        {/*
          E12 (canon 07 §7): the card's title bar — the name on the left, then
          minimise / maximise / close on the right, in Gmail's order.

          The BAR itself toggles minimise on click, which is what makes a
          collapsed strip expand again by clicking anywhere on it rather than
          by finding a 2rem button. It is a <button> for that reason and not a
          div with a handler: it is genuinely a control, and making it one gives
          it keyboard operation and a role for free.
        */}
        <header className={styles.header}>
          <button
            type="button"
            className={styles.titleBar}
            onClick={() => {
              setCardSize((size) => (size === "minimized" ? "normal" : "minimized"));
            }}
            /* The heading's text is the accessible name; what the press DOES is
               the label, so the two together read as "Mensaje nuevo, minimise". */
            aria-label={`${title} — ${
              cardSize === "minimized" ? t("compose.expand") : t("compose.minimize")
            }`}
            aria-expanded={cardSize !== "minimized"}
          >
            <span className={styles.title}>{title}</span>
          </button>

          <div className={styles.headerActions}>
            <button
              type="button"
              className={styles.iconButton}
              onClick={() => {
                setCardSize((size) => (size === "minimized" ? "normal" : "minimized"));
              }}
              aria-label={
                cardSize === "minimized" ? t("compose.expand") : t("compose.minimize")
              }
              title={cardSize === "minimized" ? t("compose.expand") : t("compose.minimize")}
            >
              <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round">
                <path d="M5 14h10" />
              </svg>
            </button>
            <button
              type="button"
              className={styles.iconButton}
              onClick={() => {
                setCardSize((size) => (size === "maximized" ? "normal" : "maximized"));
              }}
              aria-label={
                cardSize === "maximized" ? t("compose.restore") : t("compose.maximize")
              }
              title={cardSize === "maximized" ? t("compose.restore") : t("compose.maximize")}
              aria-pressed={cardSize === "maximized"}
            >
              {cardSize === "maximized" ? (
                <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
                  {/* Arrows pointing IN: this collapses back to the card. */}
                  <path d="M9 4v5H4M11 16v-5h5" />
                </svg>
              ) : (
                <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
                  {/* Arrows pointing OUT: this grows to the full panel. */}
                  <path d="M12 4h4v4M8 16H4v-4" />
                </svg>
              )}
            </button>
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
          </div>
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
          {...(addressSuggestions !== undefined ? { suggestions: addressSuggestions } : {})}
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
            inputRef={ccInputRef}
            onChange={(next) => {
              setCc(next);
              touched();
            }}
            {...(addressSuggestions !== undefined ? { suggestions: addressSuggestions } : {})}
          />
        )}
        {showBcc && (
          <AddressField
            label={t("compose.bcc")}
            chips={bcc}
            inputRef={bccInputRef}
            onChange={(next) => {
              setBcc(next);
              touched();
            }}
            /* Bcc completes from the index like the others: the index is never
               FED from Bcc (that would surface a hidden recipient), but
               completing INTO it is just the user picking someone they already
               know — the asymmetry is deliberate. */
            {...(addressSuggestions !== undefined ? { suggestions: addressSuggestions } : {})}
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
          toolbarHost={formatHost}
          showToolbar={showFormatBar}
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

        {/*
          E7: the refusal for a blocked file.

          `role="alert"` because it IS an event — the user just did something
          and this is the answer to it — and because the file silently not
          appearing in the list is otherwise indistinguishable from a bug. It
          names the file and says why, then says what to do instead, which is
          the same shape every error in this app takes.
        */}
        {blockedNotice !== undefined && (
          <div className={styles.errorBanner} role="alert">
            <strong>{blockedNotice}</strong>
            <span>{t("compose.blockedExtensionHint")}</span>
          </div>
        )}

        {(sendError !== undefined || saveError !== undefined) && (
          <div className={styles.errorBanner} role="alert">
            <strong>{sendError !== undefined ? t("send.failedTitle") : t("draft.saveFailed")}</strong>
            {/* The SERVER's sentence, verbatim — the pilot's lesson. */}
            <span>{sendError ?? saveError}</span>
          </div>
        )}

        <footer className={styles.footer}>
          {/*
            D-01: the formatting row's home (canon 07 §7).

            An empty div until `Aa` is pressed and `BodyEditor` portals its
            toolbar in. It is the FIRST child of the footer, so the row lands
            directly above Send exactly as Gmail's does — and it is rendered
            unconditionally so the portal target exists before the toggle is
            ever pressed.
          */}
          <div ref={setFormatHost} className={styles.formatRow} />

          <div className={styles.actionRow}>
          <div className={styles.footerLeft}>
            <button
              type="submit"
              className={styles.send}
              disabled={!canSend || isSending || pending !== undefined}
            >
              {isSending ? t("compose.sending") : t("compose.send")}
            </button>

            {/*
              E7: Send & Archive (canon §2.3, /a/users/answer/9282734).

              # The named gap, stated where it lives

              Gmail gates this button behind a setting ("Show 'Send & Archive'
              button in reply"). Our prefs v1 has no key for it, and inventing
              one client-side is exactly what `labelStore` explains we do not
              do — the server refuses unknown keys, and the wire shape is the
              server's business.

              So the button is SHOWN, always, in a reply that has something to
              archive. That is the honest end of the trade: a visible, working,
              useful control is not a dead one, whereas hiding it behind a
              preference we cannot persist would mean either a setting that
              forgets itself or a feature nobody can reach. The hiding
              preference arrives with prefs v2, named in the deliverable.

              It renders only for a reply (`onSendAndArchive` is absent
              otherwise), because archiving is about the conversation being
              replied to and a new message has none.
            */}
            {onSendAndArchive !== undefined && (
              <button
                type="button"
                className={styles.sendAndArchive}
                disabled={!canSend || isSending || pending !== undefined}
                title={t("send.andArchiveHint")}
                onClick={() => {
                  void send(true);
                }}
              >
                {t("send.andArchive")}
              </button>
            )}

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

            {/*
              D-01/D-04: `Aa` — the formatting row's toggle.

              Gmail's composer keeps its formatting controls behind exactly this
              button, collapsed by default, and that is what makes a corner card
              usable for a two-line reply. It is `aria-expanded`, not
              `aria-pressed`: it discloses a region rather than latching a mode.

              It is also the surviving INDICATOR of rich vs plain now that the
              segmented control is gone (D-04) — pressing it in plain mode
              switches to rich first, because revealing a bold button over a
              textarea would be four controls that do nothing. Going the other
              way is the ⋯ menu's `menuitemcheckbox`, which is the canonical
              control for that state and always was.
            */}
            <button
              type="button"
              className={[styles.iconButton, showFormatBar && isRich ? styles.iconButtonOn : ""]
                .filter(Boolean)
                .join(" ")}
              aria-expanded={showFormatBar && isRich}
              aria-label={t("compose.formatOptions")}
              title={t("compose.formatOptions")}
              onClick={() => {
                const next = !(showFormatBar && isRich);
                if (next && !isRich) toggleRich(true);
                setShowFormatBar(next);
                saveComposeFormatBar(next);
              }}
            >
              <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false">
                <text x="10" y="14.5" textAnchor="middle" fontSize="12" fontWeight="600" fill="currentColor">
                  Aa
                </text>
              </svg>
            </button>

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

            {/*
              E7: the composer's ⋯ menu, holding the plain-text toggle
              (canon §2.3 — behaviourally real in Gmail, UNSOURCED as a
              documented row, which is recorded in `composePrefs.ts`).

              A `menuitemcheckbox` rather than two items or a switch: it is one
              mode with two states, and `aria-checked` is what tells a screen
              reader which one is active right now. The menu closes on choice,
              because unlike the label menu there is nothing else in it to tick.
            */}
            <PopupMenu
              label={t("compose.more")}
              disabled={false}
              triggerClassName={styles.iconButton}
              triggerContent={
                <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false" fill="currentColor">
                  <circle cx="4.5" cy="10" r="1.4" />
                  <circle cx="10" cy="10" r="1.4" />
                  <circle cx="15.5" cy="10" r="1.4" />
                </svg>
              }
            >
              {(close) => (
                <li role="none">
                  <button
                    type="button"
                    role="menuitemcheckbox"
                    aria-checked={!isRich}
                    className={styles.menuItem}
                    onClick={() => {
                      toggleRich(isRich ? false : true);
                      close();
                    }}
                  >
                    {t("compose.plainTextMode")}
                  </button>
                </li>
              )}
            </PopupMenu>

          </div>

          {/* The draft status. Always in the DOM so its changes are announced. */}
          <p className={styles.status} role="status" aria-live="polite">
            {saveState === "saving"
              ? t("draft.saving")
              : saveState === "saved"
                ? t("draft.saved")
                : ""}
          </p>

          {/*
            D-02: discard is a TRASH ICON alone at the far right (canon 07 §7).

            It was a word — "Descartar" — sitting immediately beside the ⋯, in
            the same run as attach and the overflow. That is the one place it
            must not be: the row's other controls add to the message, this one
            destroys it, and a destructive verb rendered like its neighbours is
            a destructive verb someone eventually hits by reflex. Gmail isolates
            it at the opposite end of the footer, and the distance IS the
            affordance.

            The confirmation for a non-empty draft is unchanged (E11's own
            dialog): the icon reduces the chance of an accidental press, it does
            not replace the guard behind it.
          */}
          <button
            type="button"
            className={styles.discard}
            onClick={() => void discard()}
            aria-label={t("compose.discard")}
            title={t("compose.discard")}
          >
            <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
              <path d="M4.5 6h11M8 6V4.6h4V6M6 6l.7 9.2a1 1 0 0 0 1 .8h4.6a1 1 0 0 0 1-.8L14 6" />
              <path d="M8.6 8.8v4.6M11.4 8.8v4.6" />
            </svg>
          </button>
          </div>
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
      {/*
        E11: the discard confirmation. Inside the composer's own <dialog>, and
        correct there: a nested `showModal()` goes into the top layer ABOVE its
        parent, so it is not covered by the composer it is asking about.
      */}
      {confirmDialog}
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
/**
 * Which of the two signature defaults a compose intent uses (canon §2.3).
 *
 * Gmail's pair is worded "for new emails" and "on reply/forward", so a FORWARD
 * takes the reply signature: both continue a message rather than start one, and
 * a user who wrote a shorter footer "for replies" means it for forwards too.
 *
 * `draft` — resuming a saved draft — takes the NEW signature, which is the
 * conservative choice of the two: the body already carries whatever signature
 * was appended when it was first composed, and `withSignature` is idempotent by
 * substring check, so in the common case nothing is appended at all. Where they
 * differ (a draft saved before the setting changed) "new" is the reading that
 * matches how the draft was started.
 */
function signatureIntent(intent: ComposeIntent): "new" | "reply" {
  switch (intent) {
    case "reply":
    case "replyAll":
    case "forward":
      return "reply";
    case "new":
    case "draft":
      return "new";
  }
}

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
