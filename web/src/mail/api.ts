/**
 * The Mail API: the JMAP calls P2 makes, and the shapes it gets back.
 *
 * Every list fetch is ONE request containing an `Email/query` and an
 * `Email/get` joined by a back-reference — the idiomatic JMAP batching S1
 * validated and what P1's `backRef` helper was written for. Two round trips
 * for a list would double the latency of the single most frequent operation in
 * a mail client.
 *
 * # The 200-row ceiling, and why this file surfaces it instead of hiding it
 *
 * `Email/query` on this server fetches at most 200 matches and slices
 * `position` out of THAT window (verified against the live pilot: a mailbox of
 * 626 messages answers `position:200` with zero ids). There is no offset and
 * no working cursor — `before` is applied in Go AFTER the SQL LIMIT, so it can
 * only shrink the same window, never advance past it. Deep paging is therefore
 * not expressible today, and {@link QueryPage.truncated} says so out loud so
 * the UI can tell the user the truth rather than presenting 200 of 626
 * messages as if they were all of them.
 */

import {
  backRef,
  CAP_CORE,
  CAP_MAIL,
  type JmapClient,
  type JmapInvocation,
} from "../api/jmap";
import { ApiError } from "../api/errors";
import {
  DETAIL_PROPERTIES,
  LIST_PROPERTIES,
  type Email,
  type Mailbox,
  type Thread,
} from "./types";

/** The server's hard search window (mail.DefaultSearchWindow). */
export const SEARCH_WINDOW = 200;

/**
 * A JMAP-level error carried as a value.
 *
 * Method errors are NOT thrown as exceptions: a batch can have one call fail
 * and another succeed, and an exception would discard the successful half.
 * More importantly, `unsupportedFilter` is a NORMAL outcome for this server's
 * bounded repertoire — it is a fact about the query, not a failure of the app,
 * and the UI has to render it as an explanation rather than as a crash.
 */
export interface MethodError {
  readonly type: string;
  readonly description?: string;
}

/** True when a value is a JMAP method-level error response. */
export function isMethodError(value: unknown): value is MethodError {
  return (
    typeof value === "object" &&
    value !== null &&
    typeof (value as { type?: unknown }).type === "string"
  );
}

/** Thrown when a batch's response does not contain a call we asked for. */
export class MailApiError extends Error {
  readonly methodError: MethodError | undefined;

  constructor(message: string, methodError?: MethodError) {
    super(message);
    this.name = "MailApiError";
    this.methodError = methodError;
  }
}

/**
 * Pulls the arguments of one named call out of a response.
 *
 * Takes `JmapInvocation[]` directly — the same tuple the client returns — so
 * no call site needs a cast to hand it a response.
 */
function responseFor(
  responses: readonly JmapInvocation[],
  clientId: string,
): Record<string, unknown> {
  for (const [name, args, id] of responses) {
    if (id !== clientId) continue;
    if (name === "error") {
      const err = isMethodError(args) ? args : undefined;
      throw new MailApiError(
        `JMAP method error: ${err?.type ?? "unknown"}`,
        err,
      );
    }
    return args;
  }
  throw new MailApiError(`no response for call "${clientId}"`);
}

/** Fetches every mailbox in the account. */
export async function fetchMailboxes(
  client: JmapClient,
  accountId: string,
  signal?: AbortSignal,
): Promise<readonly Mailbox[]> {
  const response = await client.call(
    [["Mailbox/get", { accountId, ids: null }, "m"]],
    [CAP_CORE, CAP_MAIL],
    signal,
  );
  const args = responseFor(response.methodResponses, "m");
  return (args.list ?? []) as readonly Mailbox[];
}

/** What a list query returned. */
export interface QueryPage {
  readonly emails: readonly Email[];
  readonly ids: readonly string[];
  /** The server's opaque query state, for detecting staleness. */
  readonly queryState: string;
  /** Exact count when the server could give one; undefined when it declined. */
  readonly total: number | undefined;
  /**
   * True when the result filled the server's window, so there are almost
   * certainly more messages that this API cannot reach.
   */
  readonly truncated: boolean;
}

/** A filter the server's repertoire accepts. */
export type MailFilter =
  | { readonly kind: "mailbox"; readonly mailboxId: string }
  | { readonly kind: "search"; readonly text: string; readonly mailboxId?: string }
  | { readonly kind: "all" };

/** Builds the JMAP filter object for one of our filters. */
export function toJmapFilter(filter: MailFilter): Record<string, unknown> | null {
  switch (filter.kind) {
    case "all":
      // `null`, NOT `{}`: an empty filter object is refused by this server
      // ("an empty filter condition matches the whole account, which this
      // server cannot enumerate") while an explicit null is the supported
      // account-wide enumeration.
      return null;
    case "mailbox":
      return { inMailbox: filter.mailboxId };
    case "search":
      return filter.mailboxId !== undefined
        ? {
            operator: "AND",
            conditions: [{ inMailbox: filter.mailboxId }, { text: filter.text }],
          }
        : { text: filter.text };
  }
}

/**
 * Fetches one page of a list: ids and their row data, in ONE request.
 *
 * The `Email/get` names its ids with a back-reference into the `Email/query`
 * that precedes it, so the server resolves both without a round trip in
 * between. `properties` is the narrow list row set — asking for `bodyValues`
 * or `headers` here would make the server open and re-parse every message's
 * raw blob to paint a list.
 */
export async function queryEmails(
  client: JmapClient,
  accountId: string,
  filter: MailFilter,
  options: {
    readonly limit?: number;
    readonly position?: number;
    readonly signal?: AbortSignal;
    /**
     * The comparators to sort by (RFC 8620 §5.5), or undefined for the
     * server's own default of newest-first.
     *
     * This server accepts a single comparator or the PAIR
     * `[hasKeyword, receivedAt]` and refuses anything else with
     * `unsupportedSort` — see `mail/prefs.ts` `sortForInboxType`, which is the
     * only thing that builds one, and which documents the polarity read out of
     * `internal/jmap/mail/query.go`.
     */
    readonly sort?: readonly Record<string, unknown>[] | undefined;
  } = {},
): Promise<QueryPage> {
  const { limit = SEARCH_WINDOW, position = 0, signal, sort } = options;

  const queryArgs: Record<string, unknown> = {
    accountId,
    filter: toJmapFilter(filter),
    limit,
    calculateTotal: true,
  };
  if (position > 0) queryArgs.position = position;
  // Omitted rather than sent as null: a `sort` key the server has to parse and
  // reject is a round trip spent on a question we did not need to ask.
  if (sort !== undefined && sort.length > 0) queryArgs.sort = sort;

  const response = await client.call(
    [
      ["Email/query", queryArgs, "q"],
      [
        "Email/get",
        {
          accountId,
          ...backRef("ids", { resultOf: "q", name: "Email/query", path: "/ids" }),
          properties: LIST_PROPERTIES,
        },
        "g",
      ],
    ],
    [CAP_CORE, CAP_MAIL],
    signal,
  );

  const responses = response.methodResponses;

  const queryResult = responseFor(responses, "q");
  const getResult = responseFor(responses, "g");

  const ids = (queryResult.ids ?? []) as readonly string[];
  const total = typeof queryResult.total === "number" ? queryResult.total : undefined;
  const emails = (getResult.list ?? []) as readonly Email[];

  // `Email/get` does not promise the order of `list` — RFC 8620 §5.1 lets a
  // server return records in any order. Restoring the query's order here is
  // what keeps the list sorted; trusting `list` would produce a subtly
  // shuffled inbox that looks like a sorting bug in the server.
  const byId = new Map(emails.map((email) => [email.id, email]));
  const ordered = ids
    .map((id) => byId.get(id))
    .filter((email): email is Email => email !== undefined);

  return {
    emails: ordered,
    ids,
    queryState: typeof queryResult.queryState === "string" ? queryResult.queryState : "",
    total,
    truncated: ids.length >= SEARCH_WINDOW,
  };
}

/**
 * Fetches one message with everything the reading pane needs, plus its thread,
 * in ONE request.
 *
 * `fetchHTMLBodyValues` is ON — this is the deliberate switch the P2 seam
 * contract reserved for the renderer epic (W-A4), flipped in the same change
 * that landed SecureHtmlBody. The HTML that arrives is UNTRUSTED and is
 * handled exclusively by the three-layer pipeline documented in
 * `mail/html/policy.ts`; nothing else in the client may touch it.
 */
export async function fetchMessageDetail(
  client: JmapClient,
  accountId: string,
  emailId: string,
  options: { readonly signal?: AbortSignal; readonly maxBodyValueBytes?: number } = {},
): Promise<{ readonly email: Email | undefined; readonly thread: Thread | undefined }> {
  const { signal, maxBodyValueBytes = 512 * 1024 } = options;

  const response = await client.call(
    [
      [
        "Email/get",
        {
          accountId,
          ids: [emailId],
          properties: DETAIL_PROPERTIES,
          fetchTextBodyValues: true,
          fetchHTMLBodyValues: true,
          maxBodyValueBytes,
        },
        "e",
      ],
      [
        "Thread/get",
        {
          accountId,
          ...backRef("ids", { resultOf: "e", name: "Email/get", path: "/list/*/threadId" }),
        },
        "t",
      ],
    ],
    [CAP_CORE, CAP_MAIL],
    signal,
  );

  const responses = response.methodResponses;

  const emailResult = responseFor(responses, "e");
  const email = ((emailResult.list ?? []) as readonly Email[])[0];

  // The thread is a nicety, not a requirement: a message must still open if
  // Thread/get fails, so its error is swallowed rather than propagated.
  let thread: Thread | undefined;
  try {
    const threadResult = responseFor(responses, "t");
    thread = ((threadResult.list ?? []) as readonly Thread[])[0];
  } catch {
    thread = undefined;
  }

  return { email, thread };
}

/** Fetches list rows for an explicit set of ids (a thread's messages). */
export async function fetchEmailsByIds(
  client: JmapClient,
  accountId: string,
  ids: readonly string[],
  signal?: AbortSignal,
): Promise<readonly Email[]> {
  if (ids.length === 0) return [];
  const response = await client.call(
    [["Email/get", { accountId, ids, properties: LIST_PROPERTIES }, "g"]],
    [CAP_CORE, CAP_MAIL],
    signal,
  );
  const args = responseFor(response.methodResponses, "g");
  return (args.list ?? []) as readonly Email[];
}

/**
 * Stage 1 of the conversation reader (L3 epic E1): a thread's ROWS.
 *
 * The narrow property set — the one a list row uses — for every message in the
 * thread. It is what makes the collapsed rows renderable (sender, preview,
 * date, keywords) at the cost of ONE request, and it deliberately carries no
 * `bodyValues`: the pilot's largest real thread has 24 messages, and fetching
 * 24 bodies to render the one the user opened is the most expensive possible
 * way to be wrong about this feature.
 *
 * This is `fetchEmailsByIds` with a name that says which stage it is; they
 * share a body because they ask the same question. The alias exists so the
 * call site reads as the two-stage design rather than as a generic fetch, and
 * so a future change to the row set for conversations does not silently change
 * every other caller.
 */
export async function fetchThreadRows(
  client: JmapClient,
  accountId: string,
  ids: readonly string[],
  signal?: AbortSignal,
): Promise<readonly Email[]> {
  return fetchEmailsByIds(client, accountId, ids, signal);
}

/**
 * Stage 2 of the conversation reader: FULL messages for the expanded ones.
 *
 * The same properties and body-value switches `fetchMessageDetail` uses for a
 * single open message, but for a batch of ids and WITHOUT the `Thread/get` —
 * the caller already has the thread, which is how it knew to ask for these.
 *
 * The batch size is the caller's (ConversationView caps it), which is Bulwark's
 * `batched()` lesson: a request whose size is a function of someone else's data
 * is a request that eventually arrives too large.
 */
export async function fetchConversationMessages(
  client: JmapClient,
  accountId: string,
  ids: readonly string[],
  signal?: AbortSignal,
  maxBodyValueBytes = 512 * 1024,
): Promise<readonly Email[]> {
  if (ids.length === 0) return [];
  const response = await client.call(
    [
      [
        "Email/get",
        {
          accountId,
          ids,
          properties: DETAIL_PROPERTIES,
          fetchTextBodyValues: true,
          fetchHTMLBodyValues: true,
          maxBodyValueBytes,
        },
        "g",
      ],
    ],
    [CAP_CORE, CAP_MAIL],
    signal,
  );
  const args = responseFor(response.methodResponses, "g");
  return (args.list ?? []) as readonly Email[];
}

/**
 * Signs remote-image URLs for the proxy, re-validating what comes back.
 *
 * The map's VALUES are re-checked client-side (the same paranoia the
 * branding client applies to server responses): a signed path must be a
 * relative `/jmap/imgproxy?` path — never an absolute URL, which could
 * point an <img> at another origin. Anything else is discarded, and its
 * image stays blocked.
 */
export async function signImageProxyUrls(
  client: JmapClient,
  urls: readonly string[],
  signal?: AbortSignal,
): Promise<ReadonlyMap<string, string>> {
  const signed = await client.signImageProxyUrls(urls, signal);
  const map = new Map<string, string>();
  for (const [original, path] of Object.entries(signed)) {
    if (typeof path === "string" && path.startsWith("/jmap/imgproxy?")) {
      map.set(original, path);
    }
  }
  return map;
}

/**
 * Downloads a blob as an object URL.
 *
 * A plain `<a download href=...>` CANNOT work against this server: the
 * download route requires HTTP Basic and a browser navigation sends no
 * Authorization header — verified against the live pilot, which answers 401
 * with `WWW-Authenticate: Basic`, popping a native credential dialog. So the
 * bytes are fetched with the header attached and handed to the anchor as a
 * blob: URL.
 *
 * The caller MUST revoke the returned URL (`URL.revokeObjectURL`) once the
 * download has started, or the whole message stays in memory for the life of
 * the document.
 */
export async function downloadBlobUrl(
  client: JmapClient,
  accountId: string,
  blobId: string,
  name: string,
  type: string,
): Promise<string> {
  const blob = await client.downloadBlob(accountId, blobId, name, type);
  return URL.createObjectURL(blob);
}

export { ApiError };
