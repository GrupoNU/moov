/**
 * The write half of the Mail API: `Email/set`, uploads, `EmailSubmission` and
 * `Identity` (P3).
 *
 * Kept separate from `api.ts` — which is P2's read path — because the read and
 * write surfaces fail differently and that difference is the whole design. A
 * read that fails shows a message and the user retries. A write that fails has
 * already been PAINTED as done (the optimistic overlay), so its failure has to
 * carry enough information to both undo the paint and tell the user what the
 * server said.
 *
 * # `SetError` is a value, not an exception
 *
 * RFC 8620 §5.3 gives every `/set` per-record errors: one id can fail while
 * the rest of the batch succeeds. Throwing on the first failure would discard
 * the successes and roll back messages the server did change. So every
 * function here returns a {@link SetOutcome} naming exactly which ids
 * succeeded and which did not, with the server's own `description` intact —
 * the pilot's lesson, applied to writes.
 *
 * # `ifInState` is deliberately NOT sent
 *
 * §5.3's `ifInState` aborts the whole call if the account's state moved. That
 * is right for a client doing read-modify-write on a whole object; it is wrong
 * for flag toggles, because the state moves every time mail arrives. A user
 * flagging a message while a newsletter lands would get `stateMismatch` and a
 * rollback for no reason. The operations here are all idempotent patches
 * (`keywords/$seen: true` is the same instruction whatever else changed), so
 * last-writer-wins on that one property is both correct and what every real
 * client does.
 */

import {
  backRef,
  CAP_CORE,
  CAP_MAIL,
  CAP_SUBMISSION,
  type JmapClient,
  type JmapInvocation,
} from "../api/jmap";
import { MailApiError, isMethodError, type MethodError } from "./api";
import { keywordPatchKey } from "./labels";
import type { Email, EmailAddress } from "./types";

/** The three capabilities a write request needs. */
const WRITE_CAPS = [CAP_CORE, CAP_MAIL, CAP_SUBMISSION];

/** RFC 8620 §5.3 SetError, as it arrives. */
export interface SetError {
  readonly type: string;
  readonly description?: string;
  readonly properties?: readonly string[];
}

/** The outcome of a `/set`: what landed and what did not, per record. */
export interface SetOutcome {
  readonly updated: readonly string[];
  readonly destroyed: readonly string[];
  readonly created: Readonly<Record<string, Record<string, unknown>>>;
  /** id (or creation id) to the server's own error. */
  readonly failed: Readonly<Record<string, SetError>>;
  readonly newState: string | undefined;
}

/** True when the outcome has at least one per-record failure. */
export function hasFailures(outcome: SetOutcome): boolean {
  return Object.keys(outcome.failed).length > 0;
}

/**
 * The first failure's human-readable text.
 *
 * Prefers the server's `description` — that is the whole point of surfacing
 * it — and falls back to the SetError `type`, which is at least a precise
 * machine word rather than "an error occurred".
 */
export function firstFailureMessage(outcome: SetOutcome): string | undefined {
  for (const error of Object.values(outcome.failed)) {
    return error.description ?? error.type;
  }
  return undefined;
}

/**
 * Reads a `/set` response object into a {@link SetOutcome}.
 *
 * Exported since E4: `mail/triage.ts` speaks the same `/set` grammar to the
 * vendor triage methods (`Snooze/set`, `Mute/set`), and re-deriving
 * created/notCreated/destroyed/notDestroyed there would be a second reading of
 * RFC 8620 §5.3 that could drift from this one.
 */
export function readSetResponse(args: Record<string, unknown>): SetOutcome {
  const failed: Record<string, SetError> = {};
  for (const key of ["notUpdated", "notDestroyed", "notCreated"] as const) {
    const map = args[key];
    if (typeof map !== "object" || map === null) continue;
    for (const [id, error] of Object.entries(map as Record<string, unknown>)) {
      if (isSetError(error)) failed[id] = error;
    }
  }

  const updatedMap = args.updated;
  const updated =
    typeof updatedMap === "object" && updatedMap !== null
      ? Object.keys(updatedMap)
      : [];

  const createdMap = args.created;
  const created: Record<string, Record<string, unknown>> = {};
  if (typeof createdMap === "object" && createdMap !== null) {
    for (const [cid, value] of Object.entries(createdMap as Record<string, unknown>)) {
      if (typeof value === "object" && value !== null) {
        created[cid] = value as Record<string, unknown>;
      }
    }
  }

  return {
    updated,
    destroyed: Array.isArray(args.destroyed) ? (args.destroyed as string[]) : [],
    created,
    failed,
    newState: typeof args.newState === "string" ? args.newState : undefined,
  };
}

function isSetError(value: unknown): value is SetError {
  return (
    typeof value === "object" &&
    value !== null &&
    typeof (value as { type?: unknown }).type === "string"
  );
}

/**
 * Pulls one named call's arguments out of a response.
 *
 * Unlike the read path's version, a method-level `error` here is turned into a
 * `MailApiError` carrying the method error — the caller needs the server's
 * words for the failure banner.
 */
function responseFor(
  responses: readonly JmapInvocation[],
  clientId: string,
): Record<string, unknown> {
  for (const [name, args, id] of responses) {
    if (id !== clientId) continue;
    if (name === "error") {
      const err: MethodError | undefined = isMethodError(args) ? args : undefined;
      throw new MailApiError(err?.description ?? `JMAP method error: ${err?.type ?? "unknown"}`, err);
    }
    return args;
  }
  throw new MailApiError(`no response for call "${clientId}"`);
}

// ---------------------------------------------------------------------------
// Email/set — flags, moves, destroy
// ---------------------------------------------------------------------------

/**
 * Sets or clears one keyword on many messages in a single request.
 *
 * The PatchObject form (`"keywords/$seen": true`) rather than the whole-set
 * form, deliberately: a whole-set write would erase keywords the client did
 * not fetch — `$answered`, `$forwarded`, a label another client set — and the
 * list row properties do not include all of them. A patch touches exactly the
 * one property named.
 *
 * Clearing uses `false`, which RFC 8620 §5.3 defines as "remove this key",
 * rather than `null`; the server's `boolPatchValue` accepts both, but `false`
 * is what §4.1.1's object-as-set grammar means.
 *
 * # The patch key is BUILT, never interpolated (E8, mechanism G3)
 *
 * The key goes through {@link keywordPatchKey}, which applies RFC 6901
 * escaping. A template literal here was correct for `$seen` and silently wrong
 * for `$label:work/clients`: the `/` is a JSON-Pointer SEPARATOR, so the patch
 * would address the `clients` member of `$label:work` — a location the server
 * accepts and that never carries the label. The label simply never lands, with
 * no error anywhere (research 05 §5.0). There is now exactly one place in this
 * client that composes a `keywords/…` key, and it is escaped.
 */
export async function setKeyword(
  client: JmapClient,
  accountId: string,
  ids: readonly string[],
  keyword: string,
  value: boolean,
  signal?: AbortSignal,
): Promise<SetOutcome> {
  if (ids.length === 0) return emptyOutcome();
  const update: Record<string, Record<string, unknown>> = {};
  for (const id of ids) update[id] = { [keywordPatchKey(keyword)]: value ? true : false };

  const response = await client.call(
    [["Email/set", { accountId, update }, "s"]],
    WRITE_CAPS,
    signal,
  );
  return readSetResponse(responseFor(response.methodResponses, "s"));
}

/**
 * Sets and clears SEVERAL keywords on many messages in one request (E8).
 *
 * The rename migration needs this and `setKeyword` cannot express it: a rename
 * must add the new keyword and remove the old one in the SAME `Email/set`
 * update, or a failure between two calls leaves the message carrying both
 * labels — which reads as a duplicated label in every client, ours included.
 * One patch object, both keys, one server-side transaction per record.
 *
 * Every key goes through {@link keywordPatchKey}, so a label containing `/`
 * survives the trip. That is the whole reason this function does not build its
 * keys inline.
 */
export async function setKeywords(
  client: JmapClient,
  accountId: string,
  ids: readonly string[],
  keywords: Readonly<Record<string, boolean>>,
  signal?: AbortSignal,
): Promise<SetOutcome> {
  if (ids.length === 0 || Object.keys(keywords).length === 0) return emptyOutcome();

  const patch: Record<string, unknown> = {};
  for (const [keyword, value] of Object.entries(keywords)) {
    patch[keywordPatchKey(keyword)] = value ? true : false;
  }

  const update: Record<string, Record<string, unknown>> = {};
  // The SAME patch object for every id: it is read-only here and the server
  // sees one identical instruction per record, which is what makes the whole
  // batch idempotent under a retry.
  for (const id of ids) update[id] = patch;

  const response = await client.call(
    [["Email/set", { accountId, update }, "s"]],
    WRITE_CAPS,
    signal,
  );
  return readSetResponse(responseFor(response.methodResponses, "s"));
}

/**
 * Moves messages into a mailbox.
 *
 * The whole-set form for `mailboxIds` — `{ [destination]: true }` — because a
 * move IS a replacement: this server holds a message in exactly one mailbox
 * (email_set.go's `resolveMailbox`), so a patch that only adds the
 * destination would resolve to two mailboxes and be refused with
 * `invalidProperties`. Sending the set states the intent the server accepts.
 */
export async function moveMessages(
  client: JmapClient,
  accountId: string,
  ids: readonly string[],
  mailboxId: string,
  signal?: AbortSignal,
): Promise<SetOutcome> {
  if (ids.length === 0) return emptyOutcome();
  const update: Record<string, Record<string, unknown>> = {};
  for (const id of ids) update[id] = { mailboxIds: { [mailboxId]: true } };

  const response = await client.call(
    [["Email/set", { accountId, update }, "s"]],
    WRITE_CAPS,
    signal,
  );
  return readSetResponse(responseFor(response.methodResponses, "s"));
}

/**
 * Destroys messages (server arbitration W-A2).
 *
 * The server decides what destroy MEANS: a message outside Trash is MOVED
 * there, and only a message already in Trash is expunged. The client does not
 * re-implement that rule — it would drift — but it does have to TELL the user
 * which of the two is about to happen, which `deleteIsPermanent` in
 * `actions.ts` answers for the confirmation copy.
 */
export async function destroyMessages(
  client: JmapClient,
  accountId: string,
  ids: readonly string[],
  signal?: AbortSignal,
): Promise<SetOutcome> {
  if (ids.length === 0) return emptyOutcome();
  const response = await client.call(
    [["Email/set", { accountId, destroy: [...ids] }, "s"]],
    WRITE_CAPS,
    signal,
  );
  return readSetResponse(responseFor(response.methodResponses, "s"));
}

function emptyOutcome(): SetOutcome {
  return { updated: [], destroyed: [], created: {}, failed: {}, newState: undefined };
}

// ---------------------------------------------------------------------------
// Blob upload (RFC 8620 §6.1)
// ---------------------------------------------------------------------------

/** What the upload endpoint answers (RFC 8620 §6.1). */
export interface UploadedBlob {
  readonly blobId: string;
  readonly type: string;
  readonly size: number;
}

/**
 * Uploads one file, reporting progress.
 *
 * # Why XMLHttpRequest and not fetch
 *
 * `fetch` has no upload-progress event. The Streams-based workaround
 * (`ReadableStream` request bodies) requires HTTP/2, `duplex: "half"`, and is
 * unsupported in Safari — for an attachment of 20 MB on a slow link, a
 * progress bar is not a nicety, it is the difference between "working" and
 * "frozen". XHR reports `upload.onprogress` everywhere, so this one call uses
 * it and everything else in the app uses fetch.
 *
 * The Authorization header is attached explicitly for the same reason the
 * download path does it (see `JmapClient.downloadBlob`): a browser navigation
 * or ambient-credential request would trigger the native Basic dialog.
 */
export function uploadBlob(
  uploadUrl: string,
  authorization: string,
  file: File,
  options: {
    readonly onProgress?: (loaded: number, total: number) => void;
    readonly signal?: AbortSignal;
  } = {},
): Promise<UploadedBlob> {
  return new Promise((resolve, reject) => {
    const request = new XMLHttpRequest();
    request.open("POST", uploadUrl, true);
    request.setRequestHeader("Authorization", authorization);
    // §6.1: "The Content-Type MUST be set to the media type of the file."
    request.setRequestHeader("Content-Type", file.type === "" ? "application/octet-stream" : file.type);
    request.setRequestHeader("Accept", "application/json");
    request.responseType = "json";

    if (options.onProgress !== undefined) {
      request.upload.onprogress = (event): void => {
        if (event.lengthComputable) options.onProgress?.(event.loaded, event.total);
      };
    }

    request.onload = (): void => {
      if (request.status >= 200 && request.status < 300) {
        const body = request.response as Partial<UploadedBlob> | null;
        if (body === null || typeof body.blobId !== "string") {
          reject(new Error("the upload response carried no blobId"));
          return;
        }
        resolve({
          blobId: body.blobId,
          type: typeof body.type === "string" ? body.type : "application/octet-stream",
          size: typeof body.size === "number" ? body.size : file.size,
        });
        return;
      }
      // The server answers RFC 7807 problem details; its `detail` is the
      // precise sentence we want the user to read (e.g. the maxSizeUpload
      // refusal), not a status code.
      const problem = request.response as { detail?: unknown } | null;
      const detail =
        problem !== null && typeof problem.detail === "string"
          ? problem.detail
          : `upload failed with status ${request.status}`;
      reject(new UploadError(detail, request.status));
    };

    request.onerror = (): void => {
      reject(new UploadError("the upload could not reach the server", 0));
    };
    request.onabort = (): void => {
      reject(new UploadError("the upload was canceled", 0));
    };

    options.signal?.addEventListener("abort", () => {
      request.abort();
    });

    request.send(file);
  });
}

/** An upload failure carrying the server's own problem detail. */
export class UploadError extends Error {
  readonly status: number;
  constructor(message: string, status: number) {
    super(message);
    this.name = "UploadError";
    this.status = status;
  }
}

/**
 * The upload URL for an account, from the Session's template (RFC 8620 §2).
 *
 * Same same-origin discipline as every other advertised URL: the server's PATH
 * is honoured, the origin is always ours.
 */
export function uploadUrlFor(template: string | undefined, accountId: string): string {
  const base = template ?? "/jmap/upload/{accountId}";
  const path = base.startsWith("/") ? base : safePath(base);
  return path.replace("{accountId}", encodeURIComponent(accountId));
}

function safePath(advertised: string): string {
  try {
    const parsed = new URL(advertised);
    return decodeURIComponent(`${parsed.pathname}${parsed.search}`);
  } catch {
    return "/jmap/upload/{accountId}";
  }
}

/**
 * The advertised `maxSizeUpload`, read from the Session — never hardcoded.
 *
 * The server's rule is "declared == applied" (J1): the number in the session
 * IS the number `MaxBytesReader` enforces. Hardcoding 50 MB here would be a
 * client that silently breaks the day an operator lowers it, and would refuse
 * files the server would have taken the day one raises it.
 */
export function maxUploadSize(
  capabilities: Readonly<Record<string, unknown>> | undefined,
): number | undefined {
  const core = capabilities?.[CAP_CORE];
  if (typeof core !== "object" || core === null) return undefined;
  const value = (core as Record<string, unknown>).maxSizeUpload;
  return typeof value === "number" && value > 0 ? value : undefined;
}

/** The advertised per-message attachment ceiling (RFC 8621 §1.5.2). */
export function maxAttachmentsSize(
  capabilities: Readonly<Record<string, unknown>> | undefined,
): number | undefined {
  const mail = capabilities?.[CAP_MAIL];
  if (typeof mail !== "object" || mail === null) return undefined;
  const value = (mail as Record<string, unknown>).maxSizeAttachmentsPerEmail;
  return typeof value === "number" && value > 0 ? value : undefined;
}

// ---------------------------------------------------------------------------
// Email/set create — drafts
// ---------------------------------------------------------------------------

/** One attachment as a draft references it. */
export interface DraftAttachment {
  readonly blobId: string;
  readonly name: string;
  readonly type: string;
  readonly size: number;
}

/** Everything a draft carries. The server assembles the MIME from this. */
export interface DraftSpec {
  readonly mailboxId: string;
  readonly from: readonly EmailAddress[];
  readonly to: readonly EmailAddress[];
  readonly cc: readonly EmailAddress[];
  readonly bcc: readonly EmailAddress[];
  readonly replyTo?: readonly EmailAddress[];
  readonly subject: string;
  readonly text: string;
  /** Omitted for a plain-text-only message. */
  readonly html?: string;
  readonly attachments: readonly DraftAttachment[];
  readonly inReplyTo?: readonly string[];
  readonly references?: readonly string[];
  /** The keywords the draft carries; `$draft` is added by {@link draftObject}. */
  readonly keywords?: readonly string[];
}

/**
 * Builds the `Email/set` creation object for a draft.
 *
 * # Respecting the server's contract exactly
 *
 * `email_create.go` is strict on purpose, and three of its rules shape this:
 *
 *   - **`textBody`/`htmlBody` carry AT MOST ONE part each**, referenced by
 *     `partId` into `bodyValues`. A multi-part list is refused rather than
 *     concatenated wrongly.
 *   - **`mailboxIds` must resolve to exactly one mailbox.**
 *   - **Server-set properties are refused**, `receivedAt` included. So this
 *     sends none of `id`, `blobId`, `threadId`, `size`, `preview`,
 *     `hasAttachment` or `receivedAt`.
 *
 * `$draft` and `$seen` are both set: a draft the user is writing is not
 * "unread mail", and every client that omits `$seen` produces a Drafts folder
 * with a permanent unread badge.
 */
export function draftObject(spec: DraftSpec): Record<string, unknown> {
  const bodyValues: Record<string, { value: string }> = { text: { value: spec.text } };
  const object: Record<string, unknown> = {
    mailboxIds: { [spec.mailboxId]: true },
    keywords: Object.fromEntries(
      ["$draft", "$seen", ...(spec.keywords ?? [])].map((keyword) => [keyword, true]),
    ),
    from: [...spec.from],
    to: [...spec.to],
    subject: spec.subject,
    textBody: [{ partId: "text", type: "text/plain" }],
  };
  if (spec.cc.length > 0) object.cc = [...spec.cc];
  if (spec.bcc.length > 0) object.bcc = [...spec.bcc];
  if (spec.replyTo !== undefined && spec.replyTo.length > 0) object.replyTo = [...spec.replyTo];

  if (spec.html !== undefined && spec.html !== "") {
    bodyValues.html = { value: spec.html };
    object.htmlBody = [{ partId: "html", type: "text/html" }];
  }
  object.bodyValues = bodyValues;

  if (spec.attachments.length > 0) {
    object.attachments = spec.attachments.map((attachment) => ({
      blobId: attachment.blobId,
      type: attachment.type,
      name: attachment.name,
      disposition: "attachment",
    }));
  }
  if (spec.inReplyTo !== undefined && spec.inReplyTo.length > 0) {
    object.inReplyTo = [...spec.inReplyTo];
  }
  if (spec.references !== undefined && spec.references.length > 0) {
    object.references = [...spec.references];
  }
  return object;
}

/** The creation id used for a draft within one request. */
const DRAFT_CREATION_ID = "draft";

/** What a successful draft save returns. */
export interface SavedDraft {
  readonly id: string;
  readonly blobId: string | undefined;
  readonly threadId: string | undefined;
  readonly size: number | undefined;
}

/**
 * Creates a draft, optionally destroying the previous revision.
 *
 * # Why create-then-destroy rather than update
 *
 * RFC 8621 §4.6 makes every Email property except `keywords` and
 * `mailboxIds` immutable — a message IS its bytes. Editing a draft's body is
 * therefore not an update at all: it is a new message plus the removal of the
 * old one, which is exactly what every JMAP client does and what the server's
 * `Email/set` supports.
 *
 * The two halves ride in ONE request, in the order create-then-destroy, so a
 * failure of the create leaves the previous revision intact. The reverse order
 * would open a window in which the user's draft exists nowhere.
 */
export async function saveDraft(
  client: JmapClient,
  accountId: string,
  spec: DraftSpec,
  previousId: string | undefined,
  signal?: AbortSignal,
): Promise<{ readonly draft: SavedDraft | undefined; readonly outcome: SetOutcome }> {
  const args: Record<string, unknown> = {
    accountId,
    create: { [DRAFT_CREATION_ID]: draftObject(spec) },
  };
  if (previousId !== undefined) args.destroy = [previousId];

  const response = await client.call([["Email/set", args, "s"]], WRITE_CAPS, signal);
  const outcome = readSetResponse(responseFor(response.methodResponses, "s"));
  const created = outcome.created[DRAFT_CREATION_ID];
  if (created === undefined) return { draft: undefined, outcome };

  return {
    draft: {
      id: String(created.id),
      blobId: typeof created.blobId === "string" ? created.blobId : undefined,
      threadId: typeof created.threadId === "string" ? created.threadId : undefined,
      size: typeof created.size === "number" ? created.size : undefined,
    },
    outcome,
  };
}

// ---------------------------------------------------------------------------
// EmailSubmission — sending, with the server's undo window
// ---------------------------------------------------------------------------

/** A submission as the server reports it (RFC 8621 §7.1). */
export interface Submission {
  readonly id: string;
  readonly undoStatus: "pending" | "final" | "canceled";
  /** UTC ISO — the instant the server will actually transmit. */
  readonly sendAt: string;
}

/** The result of asking the server to send. */
export interface SendResult {
  readonly submission: Submission | undefined;
  readonly outcome: SetOutcome;
  /** The draft's id, once the create in the same request resolved it. */
  readonly emailId: string | undefined;
}

/**
 * Creates the draft AND submits it in ONE request (RFC 8621 §7.5's canonical
 * flow), then files the sent copy.
 *
 * # Why all of it is one request
 *
 * §7.5 defines `emailId` as accepting a creation reference (`"#draft"`), and
 * `onSuccessUpdateEmail` as an implicit `Email/set` the server runs after the
 * submission. Doing it in one request means:
 *
 *   - the draft cannot exist without its submission (no orphan drafts if the
 *     network drops between two calls);
 *   - the move to Sent and the removal of `$draft` are the SERVER's atomic
 *     business, not a second client round trip that could be lost;
 *   - and the whole send costs one RTT, which on this pilot's transatlantic
 *     path is the difference between 0.5 s and 1.5 s.
 *
 * `onSuccessUpdateEmail` is keyed by `"#sendIt"` — the submission's creation
 * id, per §7.5 — and moves the message into Sent while dropping `$draft`. The
 * server's own executor also appends to `\Sent` after the SMTP 250; W3's
 * verification proved the copy lands EXACTLY ONCE, which is why this client
 * does not attempt any Sent bookkeeping of its own.
 */
export async function sendDraft(
  client: JmapClient,
  accountId: string,
  spec: DraftSpec,
  options: {
    readonly identityId: string;
    readonly sentMailboxId: string | undefined;
    readonly previousDraftId?: string | undefined;
    /**
     * E4: a future release instant (RFC 8621 §7.1's `sendAt` on create,
     * canon §2.3's schedule send).
     *
     * Its presence changes TWO things, and the second is the subtle one:
     *
     *   - `sendAt` goes on the creation object, gated server-side by the
     *     advertised `maxDelayedSend` (30 days) and by the cap of 100
     *     simultaneously scheduled sends, refused with `overQuota`.
     *   - `onSuccessUpdateEmail` is OMITTED. The server already suppresses it
     *     for a scheduled submission (`holdsItsDraft` in submission.go — a
     *     message scheduled for Friday must not sit in Sent from Tuesday, and
     *     must stay a DRAFT the user can still edit), so sending it would be
     *     harmless; it is omitted anyway because sending an instruction the
     *     server is required to ignore states an intent this client does not
     *     have, and the next reader of this code would have to go find the
     *     suppression to know it was safe.
     */
    readonly sendAt?: string | undefined;
    readonly signal?: AbortSignal;
  },
): Promise<SendResult> {
  const { identityId, sentMailboxId, previousDraftId, sendAt, signal } = options;
  const isScheduled = sendAt !== undefined && sendAt !== "";

  const createArgs: Record<string, unknown> = {
    accountId,
    create: { [DRAFT_CREATION_ID]: draftObject(spec) },
  };
  if (previousDraftId !== undefined) createArgs.destroy = [previousDraftId];

  const submissionCreate: Record<string, unknown> = {
    identityId,
    // §7.5: "may be a creation id reference, prefixed with #".
    emailId: `#${DRAFT_CREATION_ID}`,
  };
  if (isScheduled) submissionCreate.sendAt = sendAt;

  const submissionArgs: Record<string, unknown> = {
    accountId,
    create: { sendIt: submissionCreate },
  };
  if (!isScheduled && sentMailboxId !== undefined) {
    // §7.5's implicit Email/set: file the message in Sent and stop calling it
    // a draft, atomically with the submission succeeding.
    submissionArgs.onSuccessUpdateEmail = {
      "#sendIt": {
        mailboxIds: { [sentMailboxId]: true },
        "keywords/$draft": null,
      },
    };
  }

  const response = await client.call(
    [
      ["Email/set", createArgs, "c"],
      ["EmailSubmission/set", submissionArgs, "s"],
    ],
    WRITE_CAPS,
    signal,
  );

  const createOutcome = readSetResponse(responseFor(response.methodResponses, "c"));
  const created = createOutcome.created[DRAFT_CREATION_ID];
  const emailId = created === undefined ? undefined : String(created.id);

  // The create failing means the submission never ran; report the create's
  // own error rather than a confusing "no submission".
  if (emailId === undefined) {
    return { submission: undefined, outcome: createOutcome, emailId: undefined };
  }

  const submitOutcome = readSetResponse(responseFor(response.methodResponses, "s"));
  const submissionObject = submitOutcome.created.sendIt;
  const submission =
    submissionObject === undefined
      ? undefined
      : {
          id: String(submissionObject.id),
          undoStatus: readUndoStatus(submissionObject.undoStatus),
          sendAt: typeof submissionObject.sendAt === "string" ? submissionObject.sendAt : "",
        };

  return { submission, outcome: submitOutcome, emailId };
}

function readUndoStatus(value: unknown): Submission["undoStatus"] {
  return value === "final" || value === "canceled" ? value : "pending";
}

/**
 * Cancels a pending submission — the undo (RFC 8621 §7.5).
 *
 * `update … undoStatus: "canceled"` is the RFC's own spelling, and the
 * server's `CancelSendIntent` does an atomic compare-and-set against the
 * executor's claim: after the window, the first mover wins and the client gets
 * `cannotUnsend`. That refusal is a TRUE statement — the mail is going out —
 * and it must be shown as such rather than swallowed, because a user who
 * believes a send was canceled and finds it in Sent has been lied to.
 */
export async function cancelSubmission(
  client: JmapClient,
  accountId: string,
  submissionId: string,
  signal?: AbortSignal,
): Promise<SetOutcome> {
  const response = await client.call(
    [
      [
        "EmailSubmission/set",
        { accountId, update: { [submissionId]: { undoStatus: "canceled" } } },
        "s",
      ],
    ],
    WRITE_CAPS,
    signal,
  );
  return readSetResponse(responseFor(response.methodResponses, "s"));
}

/**
 * Sends a scheduled message NOW (L3 E4, canon §2.3's Scheduled view).
 *
 * # Why this is cancel-then-resubmit and not an update
 *
 * The obvious implementation is `update: {id: {sendAt: <now>}}`. The server
 * refuses it, and its refusal is deliberate: `applySubmissionUpdate` accepts
 * "only undoStatus [...] and only to 'canceled'" (RFC 8621 §7.5 defines exactly
 * that one transition; §7.1 makes `sendAt` immutable on an existing
 * submission). So "send now" is two operations, and they are ordered:
 *
 *   1. cancel the scheduled submission. The draft SURVIVES — the server never
 *      filed it into Sent (`holdsItsDraft`) and never retires it for a
 *      cancellation, so the message is still a draft with its id intact;
 *   2. submit that same draft again, with no `sendAt`, which gives it the
 *      ordinary undo window.
 *
 * Both ride ONE request, in that order, because §3.2 processes method calls
 * sequentially: if the cancel fails (`cannotUnsend` — the mail is already
 * going out) the create still runs, which would send the message TWICE. So the
 * cancel is checked first and the resubmit is a second request, deliberately
 * paying a round trip to make a double send impossible.
 *
 * The `onSuccessUpdateEmail` returns here, unlike the scheduled create: this
 * submission is an ordinary immediate one, so the message SHOULD move to Sent
 * and stop being a draft on success.
 */
export async function sendScheduledNow(
  client: JmapClient,
  accountId: string,
  submissionId: string,
  emailId: string,
  options: {
    readonly identityId: string;
    readonly sentMailboxId: string | undefined;
    readonly signal?: AbortSignal;
  },
): Promise<{ readonly canceled: SetOutcome; readonly resubmitted: SetOutcome | undefined }> {
  const canceled = await cancelSubmission(client, accountId, submissionId, options.signal);
  if (hasFailures(canceled)) {
    // The schedule could not be lifted, so the message is going out on the
    // server's own terms. Resubmitting would send it twice.
    return { canceled, resubmitted: undefined };
  }

  const submissionArgs: Record<string, unknown> = {
    accountId,
    create: { sendNow: { identityId: options.identityId, emailId } },
  };
  if (options.sentMailboxId !== undefined) {
    submissionArgs.onSuccessUpdateEmail = {
      "#sendNow": {
        mailboxIds: { [options.sentMailboxId]: true },
        "keywords/$draft": null,
      },
    };
  }
  const response = await client.call(
    [["EmailSubmission/set", submissionArgs, "s"]],
    WRITE_CAPS,
    options.signal,
  );
  return {
    canceled,
    resubmitted: readSetResponse(responseFor(response.methodResponses, "s")),
  };
}

// ---------------------------------------------------------------------------
// Identity (RFC 8621 §6) — the signature
// ---------------------------------------------------------------------------

/** An Identity as this client uses it. */
export interface Identity {
  readonly id: string;
  readonly name: string;
  readonly email: string;
  readonly replyTo: readonly EmailAddress[] | null;
  readonly bcc: readonly EmailAddress[] | null;
  readonly textSignature: string;
  readonly htmlSignature: string;
  readonly mayDelete: boolean;
}

/** Fetches the account's identities (`ids: null` — §6.1 permits it). */
export async function fetchIdentities(
  client: JmapClient,
  accountId: string,
  signal?: AbortSignal,
): Promise<readonly Identity[]> {
  const response = await client.call(
    [["Identity/get", { accountId, ids: null }, "i"]],
    [CAP_CORE, CAP_SUBMISSION],
    signal,
  );
  const args = responseFor(response.methodResponses, "i");
  const list = Array.isArray(args.list) ? (args.list as Record<string, unknown>[]) : [];
  return list.map((raw) => ({
    id: String(raw.id),
    name: typeof raw.name === "string" ? raw.name : "",
    email: typeof raw.email === "string" ? raw.email : "",
    replyTo: Array.isArray(raw.replyTo) ? (raw.replyTo as EmailAddress[]) : null,
    bcc: Array.isArray(raw.bcc) ? (raw.bcc as EmailAddress[]) : null,
    textSignature: typeof raw.textSignature === "string" ? raw.textSignature : "",
    htmlSignature: typeof raw.htmlSignature === "string" ? raw.htmlSignature : "",
    mayDelete: raw.mayDelete === true,
  }));
}

/**
 * Updates an identity's plain-text signature (RFC 8621 §6.3, L3 E5).
 *
 * Only `textSignature` is written. `htmlSignature` is deliberately left alone:
 * it is HTML the server stores and the composer may render, so setting it from
 * a plain textarea would either mean escaping the user's text into markup they
 * did not write, or shipping a rich editor for one field. Gmail's own signature
 * model is rich and multi-signature (canon §2.3, up to 10,000 chars, named
 * signatures with new-vs-reply defaults) — that is epic E7's scope, and this is
 * the honest subset: the one field the composer already reads.
 *
 * The per-record `notUpdated` entry is returned rather than thrown, matching
 * every other /set wrapper here, so a caller can name the server's own reason.
 */
export async function setIdentitySignature(
  client: JmapClient,
  accountId: string,
  identityId: string,
  textSignature: string,
  signal?: AbortSignal,
): Promise<SetOutcome> {
  const response = await client.call(
    [
      [
        "Identity/set",
        { accountId, update: { [identityId]: { textSignature } } },
        "i",
      ],
    ],
    [CAP_CORE, CAP_SUBMISSION],
    signal,
  );
  return readSetResponse(responseFor(response.methodResponses, "i"));
}

/** The longest sender name this client will send (RFC 8621 §6 sets no cap). */
export const MAX_IDENTITY_NAME_LENGTH = 128;

/**
 * Normalizes a typed sender name into the value `Identity/set` should carry.
 *
 * Trimmed, because leading or trailing whitespace in a display name is
 * invisible in every input and produces `" Diego " <a@b>` on the wire; and
 * capped, because the name rides in every outgoing From header and a
 * pathological one would be an unbounded string in a message header.
 *
 * An empty result stays `""` rather than becoming `null`: §6 types `name` as
 * "String" with a default of `""`, and `identity.go`'s `patchString` accepts a
 * string — a `null` would be refused as a non-string. The FALLBACK to the
 * address is a rendering rule (an identity whose name is empty shows its
 * email), not a storage one.
 */
export function normalizeIdentityName(name: string): string {
  return name.trim().slice(0, MAX_IDENTITY_NAME_LENGTH);
}

/**
 * Updates an identity's display name — the name recipients see (RFC 8621 §6.3).
 *
 * Sibling of {@link setIdentitySignature}, and separate from it for the same
 * reason each `/set` wrapper here names exactly one property: a PatchObject
 * that carried both would make saving a name also rewrite the signature the
 * user did not touch, and a single failure would then be ambiguous about which
 * of the two the server refused.
 *
 * `name` is one of §6's MUTABLE properties, so the server accepts it as-is;
 * `email` is not, which is why the address remains a read-only display.
 */
export async function setIdentityName(
  client: JmapClient,
  accountId: string,
  identityId: string,
  name: string,
  signal?: AbortSignal,
): Promise<SetOutcome> {
  const response = await client.call(
    [
      [
        "Identity/set",
        { accountId, update: { [identityId]: { name: normalizeIdentityName(name) } } },
        "i",
      ],
    ],
    [CAP_CORE, CAP_SUBMISSION],
    signal,
  );
  return readSetResponse(responseFor(response.methodResponses, "i"));
}

// ---------------------------------------------------------------------------
// Mailbox/set — creating a folder from the move menu
// ---------------------------------------------------------------------------

/** Creates a mailbox and returns its new id. */
export async function createMailbox(
  client: JmapClient,
  accountId: string,
  name: string,
  parentId: string | null,
  signal?: AbortSignal,
): Promise<{ readonly id: string | undefined; readonly outcome: SetOutcome }> {
  const response = await client.call(
    [
      [
        "Mailbox/set",
        { accountId, create: { box: { name, parentId } } },
        "m",
      ],
    ],
    WRITE_CAPS,
    signal,
  );
  const outcome = readSetResponse(responseFor(response.methodResponses, "m"));
  const created = outcome.created.box;
  return { id: created === undefined ? undefined : String(created.id), outcome };
}

/**
 * Fetches a single message's full detail after a write, so the list can be
 * reconciled from the server's truth rather than from the optimistic patch.
 */
export async function refetchEmails(
  client: JmapClient,
  accountId: string,
  ids: readonly string[],
  properties: readonly string[],
  signal?: AbortSignal,
): Promise<readonly Email[]> {
  if (ids.length === 0) return [];
  const response = await client.call(
    [["Email/get", { accountId, ids: [...ids], properties: [...properties] }, "g"]],
    WRITE_CAPS,
    signal,
  );
  const args = responseFor(response.methodResponses, "g");
  return Array.isArray(args.list) ? (args.list as Email[]) : [];
}

/** Re-exported so callers import errors from one place. */
export { MailApiError };

/** Kept for the batching helper's type-checking. */
export type { JmapInvocation };
export { backRef };
