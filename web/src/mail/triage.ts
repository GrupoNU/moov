/**
 * Snooze and mute — the client half of Moov's vendor triage capability
 * (L3 E4, canon §2.2, arbitration GC-10).
 *
 * # The contract this file consumes, read off the server
 *
 * `internal/jmap/mail/triage.go` serves four methods under one vendor URI:
 * `Snooze/get`, `Snooze/set`, `Mute/get`, `Mute/set`. Its header states the
 * two facts that shape everything here:
 *
 *   - **A Snooze's id IS the Email's id**, and a Mute's id IS the Thread's id.
 *     The objects are addressable by what a client already holds, and `/set` is
 *     naturally idempotent — snoozing the same message twice names the same
 *     object.
 *   - **There is no `Snooze/query`, no `Mute/query` and no filter condition for
 *     either.** `in:snoozed` IS the Snoozed mailbox (GC-10 makes snoozing a
 *     MOVE), answered by the `inMailbox` condition that already exists; and
 *     `is:muted` is answered by `Mute/get` — "a client renders the badge from a
 *     set of thread ids it already holds". This module is therefore a fetch of
 *     two small whole sets, not a query surface, and {@link fetchMutedThreadIds}
 *     is what `is:muted` is built out of.
 *
 * # Feature detection is mandatory
 *
 * A vendor capability is by definition something a server may not have, and
 * `capabilities.go` is explicit that a client which never names the URI must
 * "behave as though it does not implement anything else" (RFC 8620 §1.8). So
 * every call here goes out with {@link CAP_TRIAGE} in `using`, and the UI asks
 * {@link sessionHasTriage} before offering a single control — the same rule
 * `mail/prefs.ts` applies to its own capability.
 *
 * # The Snoozed folder's NAME comes from the session, never from a constant
 *
 * There is no RFC 6154 SPECIAL-USE role for snoozed mail — `internal/sync/
 * snooze.go` refused to invent one — so the folder cannot be resolved by role
 * the way Trash can. `triageCapability()` publishes `snoozeMailboxName` for
 * exactly this reason: "if the name ever changes, or a role appears, the
 * session tells the truth without a client release". {@link snoozeMailboxName}
 * reads it; nothing here hard-codes "Snoozed".
 */

import { CAP_CORE, type JmapClient, type JmapInvocation, type JmapSession } from "../api/jmap";
import { MailApiError, isMethodError } from "./api";
import { readSetResponse, type SetOutcome } from "./write";

/** The vendor capability the triage methods live under (jmap.CapTriage). */
export const CAP_TRIAGE = "https://moov.email/ns/triage";

/** The `using` array every triage call sends. */
const TRIAGE_CAPS = [CAP_CORE, CAP_TRIAGE];

/**
 * The server's fallback folder name.
 *
 * Used ONLY when a session advertises the capability without the name, which
 * `triageCapability()` never does — so this is a defensive default for a server
 * older than the field, not the value the app expects to use.
 */
const SNOOZE_MAILBOX_FALLBACK = "Snoozed";

/** One pending snooze, as `Snooze/get` renders it. */
export interface SnoozeRecord {
  /** The Email id — which is also the Snooze's own id. */
  readonly id: string;
  /** The wake instant, UTCDate ("2026-09-01T08:00:00Z"). */
  readonly until: string;
  /**
   * The IMAP name of the folder it returns to, or `null` for the inbox.
   *
   * The server sends `null` rather than `""` deliberately: the empty string is
   * the STORE's spelling of "the inbox" and "a client should not have to know
   * that".
   */
  readonly originMailboxName: string | null;
}

/** True when the session advertises the triage capability. */
export function sessionHasTriage(
  session: JmapSession | undefined,
  accountId: string,
): boolean {
  if (session === undefined) return false;
  if (CAP_TRIAGE in session.capabilities) return true;
  /*
   * `accountCapabilities` is REQUIRED by RFC 8620 §2, and our own server always
   * sends it — but a capability probe must not be able to take down the whole
   * shell if a server omits it. Before this guard the bare `in` threw
   * "Cannot use 'in' operator ... in undefined" during render, which surfaces
   * as a blank app rather than as a missing feature. Absent capabilities mean
   * the feature is absent; that is the only reading, and it is a safe one.
   */
  const capabilities = session.accounts[accountId]?.accountCapabilities;
  return capabilities !== undefined && CAP_TRIAGE in capabilities;
}

/**
 * The name of the Snoozed folder this server uses.
 *
 * Returns `undefined` when the capability is absent — which is the caller's
 * signal that there is no Snoozed view to offer at all, distinct from "there is
 * one and it is called Snoozed".
 */
export function snoozeMailboxName(session: JmapSession | undefined): string | undefined {
  if (session === undefined) return undefined;
  const capability = session.capabilities[CAP_TRIAGE];
  if (typeof capability !== "object" || capability === null) return undefined;
  const name = (capability as { snoozeMailboxName?: unknown }).snoozeMailboxName;
  return typeof name === "string" && name !== "" ? name : SNOOZE_MAILBOX_FALLBACK;
}

/**
 * The account's schedule-send limits, from the triage account capability.
 *
 * `triageAccountCapability()` carries BOTH numbers deliberately: "a client
 * building a 'schedule for...' picker needs both and reading one of them out of
 * a different capability's object would be a cross-capability dependency §1.8
 * does not promise". So both are read from here, and neither is guessed.
 */
export interface ScheduleLimits {
  /** The cap on simultaneously scheduled sends (canon §2.3's 100). */
  readonly maxScheduledSends: number;
  /** How far ahead a send may be scheduled, in seconds (30 days). */
  readonly maxDelayedSendSeconds: number;
}

/** The canon's own numbers, for a session that does not carry them. */
export const DEFAULT_SCHEDULE_LIMITS: ScheduleLimits = {
  maxScheduledSends: 100,
  maxDelayedSendSeconds: 30 * 24 * 60 * 60,
};

export function scheduleLimits(
  session: JmapSession | undefined,
  accountId: string,
): ScheduleLimits {
  const account = session?.accounts[accountId];
  const capability = account?.accountCapabilities[CAP_TRIAGE];
  if (typeof capability !== "object" || capability === null) {
    return DEFAULT_SCHEDULE_LIMITS;
  }
  const raw = capability as { maxScheduledSends?: unknown; maxDelayedSendSeconds?: unknown };
  return {
    maxScheduledSends:
      typeof raw.maxScheduledSends === "number" && raw.maxScheduledSends > 0
        ? raw.maxScheduledSends
        : DEFAULT_SCHEDULE_LIMITS.maxScheduledSends,
    maxDelayedSendSeconds:
      typeof raw.maxDelayedSendSeconds === "number" && raw.maxDelayedSendSeconds > 0
        ? raw.maxDelayedSendSeconds
        : DEFAULT_SCHEDULE_LIMITS.maxDelayedSendSeconds,
  };
}

/** Pulls one named call out of a batch response, as `mail/api.ts` does. */
function responseFor(
  responses: readonly JmapInvocation[],
  clientId: string,
): Record<string, unknown> {
  for (const [name, args, id] of responses) {
    if (id !== clientId) continue;
    if (name === "error") {
      const err = isMethodError(args) ? args : undefined;
      throw new MailApiError(`JMAP method error: ${err?.type ?? "unknown"}`, err);
    }
    return args;
  }
  throw new MailApiError(`no response for call "${clientId}"`);
}

// ---------------------------------------------------------------------------
// Snooze
// ---------------------------------------------------------------------------

/**
 * Every pending snooze in the account (`ids: null` — §5.1's "all records").
 *
 * The whole set, because the server's own header says it is bounded: "both
 * objects are small, bounded sets an account holds a handful of, and both /get
 * calls return the WHOLE set in one indexed read". There is no /changes to poll
 * and none is needed.
 */
export async function fetchSnoozes(
  client: JmapClient,
  accountId: string,
  signal?: AbortSignal,
): Promise<readonly SnoozeRecord[]> {
  const response = await client.call(
    [["Snooze/get", { accountId, ids: null }, "s"]],
    TRIAGE_CAPS,
    signal,
  );
  const args = responseFor(response.methodResponses, "s");
  const list = (args.list ?? []) as readonly Record<string, unknown>[];
  const out: SnoozeRecord[] = [];
  for (const item of list) {
    const id = item.id;
    const until = item.until;
    if (typeof id !== "string" || typeof until !== "string") continue;
    out.push({
      id,
      until,
      originMailboxName: typeof item.originMailboxName === "string" ? item.originMailboxName : null,
    });
  }
  return out;
}

/**
 * Snoozes messages: one `Snooze/set` create per id, in ONE request.
 *
 * A CONVERSATION is snoozed as a whole — Gmail's unit is the thread, and a
 * thread with three of its messages asleep and one awake is a conversation the
 * user cannot reason about. The caller passes every message id of the thread;
 * this batches them into a single `create` map because the server iterates
 * creates "in a deterministic order" within one call, so N messages cost one
 * round trip rather than N.
 *
 * The creation ids are positional (`s0`, `s1`, …) and the outcome is keyed by
 * them; {@link SetOutcome.failed} therefore names which message could not be
 * snoozed, which is what a partial failure has to be able to say.
 */
export async function snoozeMessages(
  client: JmapClient,
  accountId: string,
  ids: readonly string[],
  until: string,
  signal?: AbortSignal,
): Promise<SetOutcome> {
  if (ids.length === 0) return emptyOutcome();
  const create: Record<string, unknown> = {};
  ids.forEach((id, index) => {
    create[`s${String(index)}`] = { emailId: id, until };
  });
  const response = await client.call(
    [["Snooze/set", { accountId, create }, "s"]],
    TRIAGE_CAPS,
    signal,
  );
  return readSetResponse(responseFor(response.methodResponses, "s"));
}

/**
 * Un-snoozes: `destroy`, which the server documents as "the message returns to
 * its origin NOW".
 *
 * This is the honest inverse of a snooze and the reason `z` can offer one. The
 * asymmetry worth naming: un-snoozing BEFORE the wake is a move back, and the
 * message keeps its id; after the wake, `internal/sync/snooze.go` re-APPENDs it
 * with a fresh INTERNALDATE (so it "returns to the top of your inbox") and the
 * id CHANGES. So an undo offered inside the 8-second window is always the
 * before-wake case — the only one where the ids the client is holding are still
 * the server's.
 */
export async function unsnoozeMessages(
  client: JmapClient,
  accountId: string,
  ids: readonly string[],
  signal?: AbortSignal,
): Promise<SetOutcome> {
  if (ids.length === 0) return emptyOutcome();
  const response = await client.call(
    [["Snooze/set", { accountId, destroy: [...ids] }, "s"]],
    TRIAGE_CAPS,
    signal,
  );
  return readSetResponse(responseFor(response.methodResponses, "s"));
}

// ---------------------------------------------------------------------------
// Mute
// ---------------------------------------------------------------------------

/**
 * The account's muted THREAD ids.
 *
 * This is the entire `is:muted` surface. The server refused a vendor filter
 * condition on `Email/query` and said why: it "would make every mail search
 * join against the mute table for a predicate whose whole result set is, in
 * practice, a few dozen ids a client can cache". So the client caches them, and
 * a row is badged by set membership.
 */
export async function fetchMutedThreadIds(
  client: JmapClient,
  accountId: string,
  signal?: AbortSignal,
): Promise<ReadonlySet<string>> {
  const response = await client.call(
    [["Mute/get", { accountId, ids: null }, "m"]],
    TRIAGE_CAPS,
    signal,
  );
  const args = responseFor(response.methodResponses, "m");
  const list = (args.list ?? []) as readonly Record<string, unknown>[];
  const out = new Set<string>();
  for (const item of list) {
    if (typeof item.id === "string") out.add(item.id);
  }
  return out;
}

/**
 * Mutes or unmutes conversations.
 *
 * Mute is `create` and unmute is `destroy` — the server has no update path,
 * because "a Mute has no mutable property. Muting is a binary fact". Creating a
 * mute that already exists SUCCEEDS (the store's `ON CONFLICT DO NOTHING`),
 * which is what a client retrying a request whose response it lost needs, and
 * what lets this function be called without first checking the cached set.
 */
export async function setThreadsMuted(
  client: JmapClient,
  accountId: string,
  threadIds: readonly string[],
  muted: boolean,
  signal?: AbortSignal,
): Promise<SetOutcome> {
  if (threadIds.length === 0) return emptyOutcome();
  const args: Record<string, unknown> = { accountId };
  if (muted) {
    const create: Record<string, unknown> = {};
    threadIds.forEach((id, index) => {
      create[`m${String(index)}`] = { threadId: id };
    });
    args.create = create;
  } else {
    args.destroy = [...threadIds];
  }
  const response = await client.call([["Mute/set", args, "m"]], TRIAGE_CAPS, signal);
  return readSetResponse(responseFor(response.methodResponses, "m"));
}

function emptyOutcome(): SetOutcome {
  return { updated: [], destroyed: [], created: {}, failed: {}, newState: undefined };
}
