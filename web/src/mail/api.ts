/**
 * The Mail API: the JMAP calls P2 makes, and the shapes it gets back.
 *
 * Every list fetch is ONE request containing an `Email/query` and an
 * `Email/get` joined by a back-reference — the idiomatic JMAP batching S1
 * validated and what P1's `backRef` helper was written for. Two round trips
 * for a list would double the latency of the single most frequent operation in
 * a mail client.
 *
 * # Paging, and why this file still surfaces truncation instead of hiding it
 *
 * HISTORY: this header once documented a real 200-row ceiling (`position`
 * beyond the first window answered zero ids). That server limitation was
 * fixed in E3/D-7 (2026-08-31): `Email/query` pages with a keyset cursor to
 * `MaxQueryReach` = 100,000 rows, measured through the real paging code
 * (internal/jmap/mail/search.go, query_paging_test.go). The E12 pager
 * (mail/paging.ts) consumes exactly that, mirroring the 100k ceiling
 * client-side. {@link QueryPage.truncated} remains, because a query CAN
 * still exceed the reach — the UI keeps telling the user the truth when
 * what it shows is not everything that matched.
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
  /**
   * E1: each returned message's thread, when the query collapsed threads.
   *
   * `Thread/get` rides the same batch as the query and the row fetch, so a
   * collapsed list costs ONE request and the rows arrive knowing their TRUE
   * conversation size — not the size within the fetched window, which is all
   * client-side grouping can ever know (see `mail/threading.ts`).
   *
   * Empty when the query did not collapse, which is what makes the caller's
   * two paths distinguishable without a second flag.
   */
  readonly threads: readonly Thread[];
}

/** A filter the server's repertoire accepts. */
export type MailFilter =
  | { readonly kind: "mailbox"; readonly mailboxId: string }
  | { readonly kind: "search"; readonly text: string; readonly mailboxId?: string }
  /**
   * E8: every message carrying one user label, across the whole account.
   *
   * This is a `hasKeyword` condition, and the server serves it —
   * `internal/jmap/mail/query.go`'s `applyHasKeyword` sends everything that is
   * not a system flag to the keywords array, "which is where the store keeps
   * user keywords AND where arbitration A6 puts labels — so a label filter is a
   * keyword filter, by design".
   *
   * The four system flags are NOT expressible here and this type cannot carry
   * them by accident: `$seen` is refused (the repertoire exposes only its
   * negation, `notKeyword:$seen`), and `$flagged`/`$answered`/`$draft` live in
   * a bitmask the repertoire has no predicate for. Since every keyword this
   * kind ever carries is a `$label:` one, the refusals are unreachable — which
   * `api.test.ts` pins, so a future caller passing `$flagged` fails a test
   * rather than shipping an `unsupportedFilter` to a user.
   */
  | { readonly kind: "label"; readonly keyword: string; readonly mailboxId?: string }
  /**
   * E3: a filter already composed by the operator grammar.
   *
   * `mail/searchFilter.ts` maps a parsed Gmail-style query onto the server's
   * §4.4.1 conditions, checking each one against `internal/jmap/mail/query.go`
   * and refusing — with a named reason — anything the repertoire cannot
   * answer. By the time a filter reaches here it has already been validated
   * against that grammar, so this kind carries it through verbatim.
   *
   * It does NOT subsume the kinds above. Those are the shapes the app builds
   * from a route (a folder, a label, the whole account), and keeping them
   * named means a folder view cannot accidentally become an arbitrary filter
   * because someone edited a string. This kind is only ever produced by the
   * parser, which is the one place the wire shape is pinned by tests.
   *
   * `null` is the account-wide enumeration (§5.5's `filter: null`), which is
   * why the type admits it — it is NOT "no filter", which the planner
   * expresses as `undefined` and never sends.
   */
  | { readonly kind: "query"; readonly filter: Record<string, unknown> | null }
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
    case "query":
      // Already in the server's grammar, validated by `planFilter`.
      return filter.filter;
    case "label":
      /*
       * The AND with `inMailbox` is the SAME two-condition shape the search
       * filter uses, and the server's `searchFilter` accepts a conjunction of
       * one mailbox and one keyword condition. Without a mailbox it is the bare
       * `hasKeyword`, which is what a label view wants: a label is
       * cross-cutting by definition, so scoping it to a folder by default would
       * hide exactly the messages the user filed away.
       */
      return filter.mailboxId !== undefined
        ? {
            operator: "AND",
            conditions: [
              { inMailbox: filter.mailboxId },
              { hasKeyword: filter.keyword },
            ],
          }
        : { hasKeyword: filter.keyword };
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
    /**
     * E1: collapse the result to one row per CONVERSATION (RFC 8621 §4.4.3).
     *
     * The server serves this for every filter shape this client sends —
     * inMailbox, the `[hasKeyword, receivedAt]` pair, `filter:null` and
     * full-text search — collapsing in the database inside a bounded window
     * (`store.ListCollapsedMessages`). It refuses exactly ONE combination,
     * collapse together with the `relevance` sort, because that sort ranks a
     * bounded recent window rather than an index order and so has no cursor to
     * page a collapsed result with. This client never sends that pair.
     *
     * Collapsing SERVER-side is categorically better than the client-side
     * grouping it replaces, and for a reason worth stating: a client can only
     * group the messages inside the window it fetched, so a thread's row shows
     * the count IN THAT WINDOW rather than the thread's real size. The server
     * knows the whole thread.
     */
    readonly collapseThreads?: boolean;
  } = {},
): Promise<QueryPage> {
  const { limit = SEARCH_WINDOW, position = 0, signal, sort, collapseThreads = false } = options;

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
  // Likewise omitted when false: `collapseThreads:false` is the RFC default,
  // so sending it says nothing the server did not already assume.
  if (collapseThreads) queryArgs.collapseThreads = true;

  /*
   * The batch: query → rows → threads, joined by back-references so the server
   * resolves all three without a round trip between them.
   *
   * The `Thread/get` is added ONLY for a collapsed query. On an uncollapsed
   * list it would fetch one thread per message — hundreds of them — to answer
   * a question the list does not ask.
   */
  const invocations: JmapInvocation[] = [
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
  ];
  if (collapseThreads) {
    invocations.push([
      "Thread/get",
      {
        accountId,
        ...backRef("ids", { resultOf: "g", name: "Email/get", path: "/list/*/threadId" }),
      },
      "th",
    ]);
  }

  const response = await client.call(invocations, [CAP_CORE, CAP_MAIL], signal);

  const responses = response.methodResponses;

  const queryResult = responseFor(responses, "q");
  const getResult = responseFor(responses, "g");

  /*
   * The threads are a NICETY, exactly as in `fetchMessageDetail`: they carry
   * the conversation sizes the rows display, and a list that renders without
   * them is a list showing "1" where it should show "4" — degraded, never
   * broken. So a failure here is swallowed rather than failing the whole list.
   */
  let threads: readonly Thread[] = [];
  if (collapseThreads) {
    try {
      threads = (responseFor(responses, "th").list ?? []) as readonly Thread[];
    } catch {
      threads = [];
    }
  }

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
    threads,
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
