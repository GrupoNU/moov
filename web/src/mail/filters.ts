/**
 * Filters, blocked senders, forwarding, vacation and quota — the client half
 * of epic E6 (GC-4, canon §2.2 / §2.8 / §2.11 / §3).
 *
 * # The four surfaces this file consumes, read off the server
 *
 * E6 landed FIVE capabilities and this module speaks to four of them. Each is
 * feature-detected separately, because a deployment can legitimately have one
 * and not the others — `internal/jmaphttp/session.go` gates them on four
 * independent config fields (`Sieve`, `Vacation`, `Quota`, `Filters`).
 *
 *   - **{@link CAP_FILTERS}** (`https://moov.email/ns/filters`, vendor) —
 *     `FilterRule/get|set`, `Forwarding/get|set`, `ForwardingAddress/get|set`.
 *     This is the surface the Filtros, Bloqueados and Reenvío sections are
 *     built on. `internal/jmap/mail/filters.go` states the contract this file
 *     mirrors, including the one thing worth naming twice: **there are no
 *     `/changes` methods on this surface, by contract rather than omission**
 *     ("the SSE StateChange plus a cheap /get is the refresh path"). So every
 *     write here re-reads, and nothing polls.
 *   - **{@link CAP_VACATION}** (`urn:ietf:params:jmap:vacationresponse`, RFC
 *     8621 §8) — `VacationResponse/get|set` over the `"singleton"` id.
 *   - **{@link CAP_QUOTA}** (`urn:ietf:params:jmap:quota`, RFC 9425) —
 *     `Quota/get`.
 *   - **{@link CAP_SIEVE}** (`urn:ietf:params:jmap:sieve`, RFC 9661) — used
 *     for ONE thing only: activating the managed script when a foreign script
 *     holds the active slot. See {@link activateManagedScript}.
 *
 * # `scriptActive` — the honesty bit, and why it is not a boolean we invented
 *
 * `FilterRule/get` carries one extra TOP-LEVEL response property beyond the
 * standard `/get` shape:
 *
 * ```go
 * type filterGetResponse struct {
 *     getResponse
 *     ScriptActive bool `json:"scriptActive"`
 * }
 * ```
 *
 * and the server's own comment says exactly what it is for: "When another
 * script is active (hand-written, SOGo's, Bulwark's) the rules exist but do not
 * filter mail; hiding that would be pretending". The UI therefore reads it off
 * the `/get` response, not off any rule, and shows the banner
 * {@link FilterConfig.scriptActive} `=== false` demands.
 *
 * # Rule ORDER is meaningful and the wire preserves it
 *
 * Sieve is sequential and `Actions.stop` ends the script for that message, so
 * the order of {@link FilterRule}s is part of the configuration, not a display
 * choice. `FilterRule/set` applies create/update/destroy over the CURRENT list
 * and pushes ONE regenerated script, so a reorder is expressed as a full
 * rewrite of the affected rules' fields — see {@link reorderRules}, which is
 * why that function exists at all.
 */

import {
  CAP_CORE,
  type JmapClient,
  type JmapInvocation,
  type JmapSession,
} from "../api/jmap";
import { MailApiError, isMethodError } from "./api";
import { readSetResponse, type SetOutcome } from "./write";

/** The vendor capability the rule surface lives under (jmap.CapFilters). */
export const CAP_FILTERS = "https://moov.email/ns/filters";

/** RFC 8621 §1.3.3 (jmap.CapVacation). */
export const CAP_VACATION = "urn:ietf:params:jmap:vacationresponse";

/** RFC 9425 §2.1 (jmap.CapQuota). */
export const CAP_QUOTA = "urn:ietf:params:jmap:quota";

/** RFC 9661 §1.2.1 (jmap.CapSieve). */
export const CAP_SIEVE = "urn:ietf:params:jmap:sieve";

/** The `Forwarding` singleton's wire id (filters.go: `forwardingID`). */
export const FORWARDING_ID = "singleton";

/** The `VacationResponse` singleton's wire id (RFC 8621 §8). */
export const VACATION_ID = "singleton";

// ---------------------------------------------------------------------------
// feature detection — one probe, four capabilities
// ---------------------------------------------------------------------------

/**
 * True when a session advertises `uri` in either place RFC 8620 §2 puts a
 * capability.
 *
 * The same tolerant read `mail/prefs.ts` documents: the top-level
 * `capabilities` map says the SERVER implements it and `accountCapabilities`
 * says THIS account may use it, they can legitimately differ, and accepting
 * either is what keeps a working backend from being hidden behind a
 * technicality. The `accountCapabilities` lookup is guarded because a server
 * omitting the (required) map must cost a feature, never the whole shell —
 * exactly the crash `mail/triage.ts` records having hit.
 */
export function sessionHasCapability(
  session: JmapSession | undefined,
  accountId: string,
  uri: string,
): boolean {
  if (session === undefined) return false;
  if (uri in session.capabilities) return true;
  const capabilities = session.accounts[accountId]?.accountCapabilities;
  return capabilities !== undefined && uri in capabilities;
}

/** Which E6 sections this session can honestly offer. */
export interface E6Capabilities {
  /** Filtros, Bloqueados and Reenvío — all three ride CAP_FILTERS. */
  readonly filters: boolean;
  readonly vacation: boolean;
  readonly quota: boolean;
  /** Only needed to ACTIVATE the managed script (the scriptActive banner). */
  readonly sieve: boolean;
}

export function e6Capabilities(
  session: JmapSession | undefined,
  accountId: string,
): E6Capabilities {
  return {
    filters: sessionHasCapability(session, accountId, CAP_FILTERS),
    vacation: sessionHasCapability(session, accountId, CAP_VACATION),
    quota: sessionHasCapability(session, accountId, CAP_QUOTA),
    sieve: sessionHasCapability(session, accountId, CAP_SIEVE),
  };
}

// ---------------------------------------------------------------------------
// the objects
// ---------------------------------------------------------------------------

/**
 * A rule's type tag (`internal/sieve/model.go`: `RuleFilter`, `RuleBlocked`,
 * `RuleNeverSpam`).
 *
 * The tag is what lets ONE script back TWO settings sections: "Bloqueados" is
 * `FilterRule/get` filtered to `type === "blocked"`, exactly as the server's
 * own comment says ("The type tag is what lets the UI render 'Bloqueados' and
 * 'Reenvío' as their own settings sections while the storage is one script").
 */
export const RULE_TYPES = ["filter", "blocked", "neverSpam"] as const;
export type RuleType = (typeof RULE_TYPES)[number];

/**
 * One rule, as `FilterRule/get` renders it.
 *
 * Flat, not nested criteria/actions: the WIRE is flat (`filterRuleObject`
 * spreads both into one object) even though the Go model nests them, and this
 * mirror follows the wire so a round-trip test can compare literal JSON.
 */
export interface FilterRule {
  readonly id: string;
  readonly name: string;
  readonly type: RuleType;
  readonly enabled: boolean;

  // --- criteria (GC-4's closed set: no date, no free-text query) ---
  readonly from: readonly string[];
  readonly to: readonly string[];
  readonly subject: readonly string[];
  /** Bytes; 0 is "unset", which is how the server spells it. */
  readonly sizeOver: number;
  readonly sizeUnder: number;
  /** `null` is unset — the wire is `Boolean|null` (`nullableBool`). */
  readonly hasAttachment: boolean | null;

  // --- actions ---
  readonly moveTo: string;
  readonly labels: readonly string[];
  readonly markRead: boolean;
  readonly star: boolean;
  /** MUST be a verified ForwardingAddress; the server refuses anything else. */
  readonly forward: string;
  /** Files into Trash — never `discard`. The model has no way to say "lose it". */
  readonly delete: boolean;
  readonly stop: boolean;
}

/** An empty rule, for the builder's "new filter" state. */
export const EMPTY_RULE: Omit<FilterRule, "id"> = {
  name: "",
  type: "filter",
  enabled: true,
  from: [],
  to: [],
  subject: [],
  sizeOver: 0,
  sizeUnder: 0,
  hasAttachment: null,
  moveTo: "",
  labels: [],
  markRead: false,
  star: false,
  forward: "",
  delete: false,
  stop: false,
};

/** The forward-all singleton (`Forwarding/get`). */
export interface ForwardAll {
  readonly enabled: boolean;
  /** `null` on the wire when unset (`nullableString`). */
  readonly address: string | null;
  /** Canon's two dispositions; the server defaults an empty one to "keep". */
  readonly disposition: ForwardDisposition;
}

export const FORWARD_DISPOSITIONS = ["keep", "archive"] as const;
export type ForwardDisposition = (typeof FORWARD_DISPOSITIONS)[number];

/** The whole rule surface, as one `FilterRule/get` returns it. */
export interface FilterConfig {
  readonly rules: readonly FilterRule[];
  readonly forwardAll: ForwardAll;
  /**
   * Whether the Moov-managed script holds the account's active slot.
   *
   * `false` means the rules below EXIST and DO NOT RUN. The UI must say so —
   * that is the whole reason the server invented this property.
   */
  readonly scriptActive: boolean;
  readonly state: string;
}

/** One verified-or-pending destination (`ForwardingAddress/get`). */
export interface ForwardingAddress {
  readonly id: string;
  readonly email: string;
  readonly state: ForwardingState;
  /** UTCDate, or null while pending. */
  readonly verifiedAt: string | null;
}

export const FORWARDING_STATES = ["pending", "accepted"] as const;
export type ForwardingState = (typeof FORWARDING_STATES)[number];

/** The RFC 8621 §8 singleton. */
export interface VacationResponse {
  readonly isEnabled: boolean;
  /** UTCDate ("2026-09-01T00:00:00Z") or null for "no start bound". */
  readonly fromDate: string | null;
  readonly toDate: string | null;
  readonly subject: string | null;
  readonly textBody: string | null;
  /**
   * Server-sanitized HTML.
   *
   * Read but never WRITTEN by this build: E6's UI offers a plain-text body
   * only, which is the honest first step (a rich editor whose output the
   * server then rewrites through `sanitizeHTMLSignature` would show the user
   * one thing and send another). It is surfaced read-only so a response
   * configured elsewhere is not silently destroyed by our save.
   */
  readonly htmlBody: string | null;
}

/** The all-null, disabled object §8 describes for a never-configured account. */
export const EMPTY_VACATION: VacationResponse = {
  isEnabled: false,
  fromDate: null,
  toDate: null,
  subject: null,
  textBody: null,
  htmlBody: null,
};

/** One RFC 9425 §4.1 Quota object. */
export interface Quota {
  /** "storage" or "message" — literal ids the server pins (`quotaWireID`). */
  readonly id: string;
  /** "octets" (STORAGE) or "count" (MESSAGE) — §3.2. */
  readonly resourceType: string;
  readonly used: number;
  readonly hardLimit: number;
  readonly name: string;
}

// ---------------------------------------------------------------------------
// parsing — the server is trusted for shape, never for values
// ---------------------------------------------------------------------------

function str(value: unknown, fallback = ""): string {
  return typeof value === "string" ? value : fallback;
}

function strOrNull(value: unknown): string | null {
  return typeof value === "string" && value !== "" ? value : null;
}

function bool(value: unknown, fallback = false): boolean {
  return typeof value === "boolean" ? value : fallback;
}

function num(value: unknown, fallback = 0): number {
  return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}

function strList(value: unknown): readonly string[] {
  if (!Array.isArray(value)) return [];
  return value.filter((item): item is string => typeof item === "string");
}

function ruleType(value: unknown): RuleType {
  return typeof value === "string" && (RULE_TYPES as readonly string[]).includes(value)
    ? (value as RuleType)
    : "filter";
}

/** Reads one rule off the wire. */
export function parseFilterRule(raw: unknown): FilterRule | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const o = raw as Record<string, unknown>;
  if (typeof o.id !== "string" || o.id === "") return undefined;
  return {
    id: o.id,
    name: str(o.name),
    type: ruleType(o.type),
    enabled: bool(o.enabled, true),
    from: strList(o.from),
    to: strList(o.to),
    subject: strList(o.subject),
    sizeOver: num(o.sizeOver),
    sizeUnder: num(o.sizeUnder),
    hasAttachment: typeof o.hasAttachment === "boolean" ? o.hasAttachment : null,
    moveTo: str(o.moveTo),
    labels: strList(o.labels),
    markRead: bool(o.markRead),
    star: bool(o.star),
    forward: str(o.forward),
    delete: bool(o.delete),
    stop: bool(o.stop),
  };
}

/** Reads the forward-all singleton off the wire. */
export function parseForwardAll(raw: unknown): ForwardAll {
  if (typeof raw !== "object" || raw === null) {
    return { enabled: false, address: null, disposition: "keep" };
  }
  const o = raw as Record<string, unknown>;
  const disposition = o.disposition;
  return {
    enabled: bool(o.enabled),
    address: strOrNull(o.address),
    disposition:
      typeof disposition === "string" &&
      (FORWARD_DISPOSITIONS as readonly string[]).includes(disposition)
        ? (disposition as ForwardDisposition)
        : "keep",
  };
}

/** Reads one forwarding address off the wire. */
export function parseForwardingAddress(raw: unknown): ForwardingAddress | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const o = raw as Record<string, unknown>;
  if (typeof o.id !== "string" || typeof o.email !== "string") return undefined;
  /*
   * Unknown states default to "pending", never to "accepted": this value gates
   * whether an address may be a redirect target, so an unrecognised word must
   * fail CLOSED. The server would refuse the redirect anyway (GC-4's gate is
   * server-side and fails closed too), but the UI should not offer it first.
   */
  const state = o.state === "accepted" ? "accepted" : "pending";
  return {
    id: o.id,
    email: o.email,
    state,
    verifiedAt: strOrNull(o.verifiedAt),
  };
}

/** Reads the §8 singleton off the wire. */
export function parseVacation(raw: unknown): VacationResponse {
  if (typeof raw !== "object" || raw === null) return EMPTY_VACATION;
  const o = raw as Record<string, unknown>;
  return {
    isEnabled: bool(o.isEnabled),
    fromDate: strOrNull(o.fromDate),
    toDate: strOrNull(o.toDate),
    subject: strOrNull(o.subject),
    textBody: strOrNull(o.textBody),
    htmlBody: strOrNull(o.htmlBody),
  };
}

/** Reads one §4.1 Quota object off the wire. */
export function parseQuota(raw: unknown): Quota | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const o = raw as Record<string, unknown>;
  if (typeof o.id !== "string") return undefined;
  if (typeof o.used !== "number" || typeof o.hardLimit !== "number") return undefined;
  return {
    id: o.id,
    resourceType: str(o.resourceType, "octets"),
    used: o.used,
    hardLimit: o.hardLimit,
    name: str(o.name),
  };
}

// ---------------------------------------------------------------------------
// the calls
// ---------------------------------------------------------------------------

const FILTER_CAPS = [CAP_CORE, CAP_FILTERS];
const VACATION_CAPS = [CAP_CORE, CAP_VACATION];
const QUOTA_CAPS = [CAP_CORE, CAP_QUOTA];
const SIEVE_CAPS = [CAP_CORE, CAP_SIEVE];

/** Pulls one named call out of a batch response, as `mail/triage.ts` does. */
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

/**
 * Loads the whole rule surface: rules, forward-all, and the honesty bit.
 *
 * One request, two calls: `FilterRule/get` carries the rules AND `scriptActive`,
 * `Forwarding/get` carries the singleton. They share a state cursor (both read
 * `FiltersState`), so batching them is not just a round-trip saving — it is
 * what guarantees the two halves describe the same script.
 */
export async function fetchFilters(
  client: JmapClient,
  accountId: string,
  signal?: AbortSignal,
): Promise<FilterConfig> {
  const response = await client.call(
    [
      ["FilterRule/get", { accountId, ids: null }, "r"],
      ["Forwarding/get", { accountId, ids: null }, "f"],
    ],
    FILTER_CAPS,
    signal,
  );
  const ruleArgs = responseFor(response.methodResponses, "r");
  const forwardArgs = responseFor(response.methodResponses, "f");

  const rules: FilterRule[] = [];
  for (const item of (ruleArgs.list ?? []) as readonly unknown[]) {
    const rule = parseFilterRule(item);
    if (rule !== undefined) rules.push(rule);
  }
  const forwardList = (forwardArgs.list ?? []) as readonly unknown[];

  return {
    rules,
    forwardAll: parseForwardAll(forwardList[0]),
    /*
     * Absent defaults to TRUE, and that polarity is deliberate: `scriptActive`
     * is a warning bit, and a server that does not send it (an older build, a
     * property filter) has told us nothing — so the banner must not fire. A
     * false alarm claiming "your rules are not running" on a server where they
     * are is worse than a missing warning, because it teaches users to ignore
     * the banner that matters.
     */
    scriptActive: bool(ruleArgs.scriptActive, true),
    state: str(ruleArgs.state),
  };
}

/**
 * The fields a `FilterRule/set` create or update carries.
 *
 * `id` is excluded by TYPE, not by convention: the server refuses a create or
 * patch that names one ("id is server-set"), so making it unspellable here
 * turns a runtime `invalidProperties` into a compile error.
 */
export type FilterRuleDraft = Omit<FilterRule, "id">;

/** Strips the id, so a loaded rule can be resent as a patch. */
export function ruleDraft(rule: FilterRule): FilterRuleDraft {
  const { id: _id, ...draft } = rule;
  return draft;
}

/**
 * Creates one rule. Returns the outcome and, on success, the server's id.
 *
 * The creation id is `"n"` and the response's `created.n.id` is the rule's
 * permanent opaque id (`newFilterRuleID`: `"r"` + 12 hex).
 */
export async function createFilterRule(
  client: JmapClient,
  accountId: string,
  draft: FilterRuleDraft,
  signal?: AbortSignal,
): Promise<SetOutcome> {
  const response = await client.call(
    [["FilterRule/set", { accountId, create: { n: draft } }, "s"]],
    FILTER_CAPS,
    signal,
  );
  return readSetResponse(responseFor(response.methodResponses, "s"));
}

/**
 * Creates several rules in ONE `/set` (F-42's import path).
 *
 * # Why one call and not a loop over {@link createFilterRule}
 *
 * The server applies a `/set` as one unit and pushes ONE regenerated script, so
 * a batch either lands whole or does not land at all. A loop would push N
 * scripts, leave a partial rule set behind if the fifth call failed, and — for
 * a surface where ORDER is configuration — could interleave with the watcher's
 * own reload between calls. All-or-nothing is what the import promises the user
 * ("none were imported"), and this is what makes the promise true rather than
 * approximately true.
 *
 * The creation ids are positional (`n0`, `n1`, …) because the server appends
 * creates to the current list in order, so the array's order becomes the
 * script's order — which is exactly what the export document carries.
 */
export async function createFilterRules(
  client: JmapClient,
  accountId: string,
  drafts: readonly FilterRuleDraft[],
  signal?: AbortSignal,
): Promise<SetOutcome> {
  const create: Record<string, FilterRuleDraft> = {};
  drafts.forEach((draft, index) => {
    create[`n${String(index)}`] = draft;
  });
  const response = await client.call(
    [["FilterRule/set", { accountId, create }, "s"]],
    FILTER_CAPS,
    signal,
  );
  return readSetResponse(responseFor(response.methodResponses, "s"));
}

/** Updates one rule with a complete field set (the server patches over base). */
export async function updateFilterRule(
  client: JmapClient,
  accountId: string,
  id: string,
  patch: Partial<FilterRuleDraft>,
  signal?: AbortSignal,
): Promise<SetOutcome> {
  const response = await client.call(
    [["FilterRule/set", { accountId, update: { [id]: patch } }, "s"]],
    FILTER_CAPS,
    signal,
  );
  return readSetResponse(responseFor(response.methodResponses, "s"));
}

/** Destroys rules. */
export async function destroyFilterRules(
  client: JmapClient,
  accountId: string,
  ids: readonly string[],
  signal?: AbortSignal,
): Promise<SetOutcome> {
  const response = await client.call(
    [["FilterRule/set", { accountId, destroy: [...ids] }, "s"]],
    FILTER_CAPS,
    signal,
  );
  return readSetResponse(responseFor(response.methodResponses, "s"));
}

/**
 * Moves a rule one position and returns the reordered list.
 *
 * Pure, and separate from the call that persists it, because ORDER is the one
 * property of this surface with no field of its own: the wire has no `position`
 * and `FilterRule/get` returns the rules in the script's evaluation order. So a
 * reorder is expressed by rewriting the LIST, which {@link persistRuleOrder}
 * then does in one `/set`.
 *
 * Returns the input unchanged when the move is a no-op (either end), so a
 * caller can compare by identity to decide whether to issue a request at all.
 */
export function reorderRules(
  rules: readonly FilterRule[],
  id: string,
  direction: "up" | "down",
): readonly FilterRule[] {
  const index = rules.findIndex((rule) => rule.id === id);
  if (index < 0) return rules;
  const target = direction === "up" ? index - 1 : index + 1;
  if (target < 0 || target >= rules.length) return rules;
  const next = [...rules];
  const moved = next[index];
  const displaced = next[target];
  if (moved === undefined || displaced === undefined) return rules;
  next[index] = displaced;
  next[target] = moved;
  return next;
}

/**
 * Persists a reordering.
 *
 * # Why this is destroy-then-create and not an update
 *
 * The list's order IS the storage order, and `FilterRule/set` appends creates
 * to the end of the current list (`rules = append(rules, *rule)`). There is no
 * argument that says "put this one third". Rewriting the two swapped rules'
 * FIELDS in place is therefore the only way to express a swap without changing
 * the set of ids — and it is what this does: each affected position is updated
 * to carry the other rule's content.
 *
 * The cost, stated rather than hidden: **the ids follow the position, not the
 * rule**. After moving "facturas" up, the id that used to name "facturas" now
 * names whatever was above it. Nothing in the UI holds a rule id across a
 * reorder (the list re-reads after every write), and the alternative —
 * destroy-and-recreate — would mint new ids for every rule below the moved one
 * AND lose them entirely if the create half failed. Swapping content keeps the
 * operation atomic in one `/set`, which is what the server pushes as one script.
 */
export async function persistRuleOrder(
  client: JmapClient,
  accountId: string,
  before: readonly FilterRule[],
  after: readonly FilterRule[],
  signal?: AbortSignal,
): Promise<SetOutcome> {
  const update: Record<string, FilterRuleDraft> = {};
  before.forEach((rule, index) => {
    const next = after[index];
    if (next === undefined || next.id === rule.id) return;
    update[rule.id] = ruleDraft(next);
  });
  if (Object.keys(update).length === 0) {
    return { updated: [], destroyed: [], created: {}, failed: {}, newState: undefined };
  }
  const response = await client.call(
    [["FilterRule/set", { accountId, update }, "s"]],
    FILTER_CAPS,
    signal,
  );
  return readSetResponse(responseFor(response.methodResponses, "s"));
}

// ---------------------------------------------------------------------------
// import and export (review F-42)
// ---------------------------------------------------------------------------

/**
 * The export envelope, and the honest statement of what it is NOT.
 *
 * # This is Moov's format, not Gmail's
 *
 * Gmail exports filters as an Atom XML document whose entries carry
 * `apps:property` elements from Google's own namespace. We do not read or write
 * that, and pretending otherwise by naming the file `mailFilters.xml` would be
 * the worst of both: a user would hand it to Gmail and get an error with no
 * explanation. So the file is JSON, the envelope names itself, and the UI says
 * in one line that it round-trips with Moov and not with Gmail.
 *
 * Importing Gmail's XML is a real want and a real piece of work — the criteria
 * do not map one-to-one (Gmail's `hasTheWord` is a search query; ours is a
 * closed set of fields, GC-4) — and it is deliberately out of scope here rather
 * than half-done. What IS in scope is the thing the review called the
 * contradiction: Sieve is behind these rules, so exporting them is trivial, and
 * a webmail whose selling point is that your mail is yours should not be the
 * one place your rules are trapped.
 *
 * # Why the payload is the WIRE object
 *
 * `FilterRuleDraft` is exactly what `FilterRule/set` accepts, minus the
 * server-set id. Exporting that means the import path is the same `/set` every
 * other write uses — no translation layer, and nothing that could accept a file
 * the server would then refuse.
 */
export const FILTERS_EXPORT_KIND = "moov.filters";

/** Bumped only when the payload shape changes incompatibly. */
export const FILTERS_EXPORT_VERSION = 1;

export interface FiltersExport {
  readonly kind: typeof FILTERS_EXPORT_KIND;
  readonly version: number;
  /**
   * The rules, in evaluation order.
   *
   * Order is configuration here (Sieve runs top to bottom and `stop` ends the
   * script), so the ARRAY's order is the meaningful part of this file — there
   * is no position field to carry it, exactly as on the wire.
   */
  readonly rules: readonly FilterRuleDraft[];
}

/**
 * Serializes rules to the export document.
 *
 * The ids are stripped, deliberately: they are server-set and opaque, and a
 * file that carried them would look importable into the account it came from
 * and only that one. Without them the document is what it should be — a
 * portable description of the rules, importable anywhere.
 */
export function exportFilters(rules: readonly FilterRule[]): FiltersExport {
  return {
    kind: FILTERS_EXPORT_KIND,
    version: FILTERS_EXPORT_VERSION,
    rules: rules.map(ruleDraft),
  };
}

/** The suggested filename, dated so two exports do not overwrite each other. */
export function filtersExportFilename(now: Date = new Date()): string {
  const day = now.toISOString().slice(0, 10);
  return `moov-filtros-${day}.json`;
}

/** Why an import file was refused. The UI maps each to one sentence. */
export type ImportProblem =
  | "notJson"
  | "notOurFormat"
  | "futureVersion"
  | "noRules"
  | "badRule";

export interface ImportResult {
  readonly rules?: readonly FilterRuleDraft[];
  readonly problem?: ImportProblem;
}

/**
 * Reads an export document back, refusing anything it cannot vouch for.
 *
 * # Why this validates rather than trusting `JSON.parse`
 *
 * The input is a FILE THE USER CHOSE, which is the one input on this surface
 * that did not come from our server. Casting it to `FiltersExport` and handing
 * it to `FilterRule/set` would send arbitrary JSON to the mail server and turn
 * a mistyped filename into an `invalidProperties` the user cannot act on. So
 * every rule goes through {@link parseFilterRule} — the same reader the wire
 * uses — with a synthesised id, and anything that fails is a refusal WITH A
 * REASON rather than a partial import.
 *
 * All-or-nothing on purpose: a half-applied set of rules is a filtering
 * configuration nobody designed, and Sieve's order-dependence means the half
 * that landed can behave differently from the half that was meant to be there.
 */
export function parseFiltersExport(text: string): ImportResult {
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch {
    return { problem: "notJson" };
  }
  if (typeof raw !== "object" || raw === null) return { problem: "notOurFormat" };
  const doc = raw as Record<string, unknown>;
  if (doc.kind !== FILTERS_EXPORT_KIND) return { problem: "notOurFormat" };
  /*
   * A NEWER version is refused rather than read optimistically: a field this
   * build does not know is a rule that would import with part of its behaviour
   * silently missing, which is worse than not importing it.
   */
  if (typeof doc.version !== "number" || doc.version > FILTERS_EXPORT_VERSION) {
    return { problem: "futureVersion" };
  }
  if (!Array.isArray(doc.rules)) return { problem: "notOurFormat" };
  if (doc.rules.length === 0) return { problem: "noRules" };

  const rules: FilterRuleDraft[] = [];
  for (const item of doc.rules as readonly unknown[]) {
    if (typeof item !== "object" || item === null) return { problem: "badRule" };
    /*
     * `parseFilterRule` requires an id (the wire always has one), so a
     * placeholder is supplied and then dropped by `ruleDraft`. Reusing the wire
     * reader rather than writing a second one is what guarantees an imported
     * rule is shaped exactly like a fetched one — including its fallbacks for
     * a missing field.
     */
    const parsed = parseFilterRule({ ...(item as Record<string, unknown>), id: "import" });
    if (parsed === undefined) return { problem: "badRule" };
    rules.push(ruleDraft(parsed));
  }
  return { rules };
}

/** Patches the forward-all singleton. */
export async function saveForwardAll(
  client: JmapClient,
  accountId: string,
  patch: Partial<ForwardAll>,
  signal?: AbortSignal,
): Promise<SetOutcome> {
  const response = await client.call(
    [["Forwarding/set", { accountId, update: { [FORWARDING_ID]: patch } }, "s"]],
    FILTER_CAPS,
    signal,
  );
  return readSetResponse(responseFor(response.methodResponses, "s"));
}

// ---------------------------------------------------------------------------
// ForwardingAddress — the verification state machine
// ---------------------------------------------------------------------------

/** Every destination this account has registered, verified or not. */
export async function fetchForwardingAddresses(
  client: JmapClient,
  accountId: string,
  signal?: AbortSignal,
): Promise<readonly ForwardingAddress[]> {
  const response = await client.call(
    [["ForwardingAddress/get", { accountId, ids: null }, "a"]],
    FILTER_CAPS,
    signal,
  );
  const args = responseFor(response.methodResponses, "a");
  const out: ForwardingAddress[] = [];
  for (const item of (args.list ?? []) as readonly unknown[]) {
    const address = parseForwardingAddress(item);
    if (address !== undefined) out.push(address);
  }
  return out;
}

/**
 * Registers a destination and triggers the verification mail.
 *
 * The server's create is SYNCHRONOUS with the send ("If the mail cannot be sent
 * the row is not left behind"), which is why a failure here is reported to the
 * user as the create's own error rather than as a mysterious address that never
 * verifies.
 */
export async function createForwardingAddress(
  client: JmapClient,
  accountId: string,
  email: string,
  signal?: AbortSignal,
): Promise<SetOutcome> {
  const response = await client.call(
    [["ForwardingAddress/set", { accountId, create: { n: { email } } }, "s"]],
    FILTER_CAPS,
    signal,
  );
  return readSetResponse(responseFor(response.methodResponses, "s"));
}

/**
 * Removes a destination.
 *
 * One that a rule or the forward-all setting still redirects to answers
 * `forbidden` with the server's own sentence ("the address is still used by a
 * filter or the forwarding setting; remove that first"). That sentence is
 * surfaced verbatim — it names the fix, which is more than any wording we could
 * invent without knowing which rule.
 */
export async function destroyForwardingAddress(
  client: JmapClient,
  accountId: string,
  id: string,
  signal?: AbortSignal,
): Promise<SetOutcome> {
  const response = await client.call(
    [["ForwardingAddress/set", { accountId, destroy: [id] }, "s"]],
    FILTER_CAPS,
    signal,
  );
  return readSetResponse(responseFor(response.methodResponses, "s"));
}

/** The verification route (`jmaphttp.PathForwardingVerify`). */
export const FORWARDING_VERIFY_PATH = "/jmap/forwarding/verify";

/**
 * Builds the verification request URL.
 *
 * Exported so a test can pin the exact shape the server reads —
 * `GET /jmap/forwarding/verify?token=…`, authenticated — without standing up a
 * client.
 */
export function forwardingVerifyUrl(token: string): string {
  return `${FORWARDING_VERIFY_PATH}?token=${encodeURIComponent(token)}`;
}

/** What the verify route answers on success: `{"verified": "<address>"}`. */
export interface VerifyResult {
  readonly verified: string;
}

/**
 * Consumes a verification token.
 *
 * # Why this bypasses `JmapClient` and speaks HTTP directly
 *
 * It is not a JMAP method — it is an auxiliary REST route, like `/jmap/token`
 * and `/jmap/imgproxy/sign`, because RFC 8620 has no vocabulary for "consume
 * this one-shot capability". The client exposes no generic authenticated GET,
 * so the request is made through a caller-supplied `fetch`-shaped function that
 * already carries auth. In the app that is a small wrapper over the same
 * credentials; in a test it is a stub.
 *
 * Every failure is one refusal (403 with the server's single sentence): "bad
 * MAC, expired, foreign account, destroyed row" are deliberately
 * indistinguishable, the same no-oracle rule the access tokens follow. So this
 * function does not try to classify — it reports that the code did not work.
 */
export async function verifyForwardingToken(
  authedFetch: (url: string) => Promise<Response>,
  token: string,
): Promise<VerifyResult> {
  const response = await authedFetch(forwardingVerifyUrl(token));
  if (!response.ok) {
    throw new MailApiError(`forwarding verification failed (${String(response.status)})`);
  }
  const body = (await response.json()) as { verified?: unknown };
  return { verified: str(body.verified) };
}

// ---------------------------------------------------------------------------
// VacationResponse
// ---------------------------------------------------------------------------

export async function fetchVacation(
  client: JmapClient,
  accountId: string,
  signal?: AbortSignal,
): Promise<{ vacation: VacationResponse; state: string }> {
  const response = await client.call(
    [["VacationResponse/get", { accountId, ids: null }, "v"]],
    VACATION_CAPS,
    signal,
  );
  const args = responseFor(response.methodResponses, "v");
  const list = (args.list ?? []) as readonly unknown[];
  return { vacation: parseVacation(list[0]), state: str(args.state) };
}

/**
 * Saves the vacation response.
 *
 * The patch carries only what changed, per RFC 8620 §5.3 — the server reads,
 * patches, validates and writes the whole object, so sending all seven fields
 * would make every save a chance to clobber a field set in another tab.
 *
 * `htmlBody` is deliberately never in a patch this build sends. See
 * {@link VacationResponse.htmlBody}.
 */
export async function saveVacation(
  client: JmapClient,
  accountId: string,
  patch: Partial<Omit<VacationResponse, "htmlBody">>,
  signal?: AbortSignal,
): Promise<SetOutcome> {
  const response = await client.call(
    [["VacationResponse/set", { accountId, update: { [VACATION_ID]: patch } }, "s"]],
    VACATION_CAPS,
    signal,
  );
  return readSetResponse(responseFor(response.methodResponses, "s"));
}

// ---------------------------------------------------------------------------
// Quota
// ---------------------------------------------------------------------------

/**
 * Reads the account's quotas.
 *
 * An account with NO quota limits gets an EMPTY LIST, and that is the server's
 * deliberate shape: "§4.1 makes hardLimit required, and fabricating an infinite
 * one would be an invented number. No objects is the truthful shape of 'no
 * quota'." So an empty array here means "sin límite" in the UI — never a bar at
 * 0%, never a bar at 100%.
 */
export async function fetchQuota(
  client: JmapClient,
  accountId: string,
  signal?: AbortSignal,
): Promise<readonly Quota[]> {
  const response = await client.call(
    [["Quota/get", { accountId, ids: null }, "q"]],
    QUOTA_CAPS,
    signal,
  );
  const args = responseFor(response.methodResponses, "q");
  const out: Quota[] = [];
  for (const item of (args.list ?? []) as readonly unknown[]) {
    const quota = parseQuota(item);
    if (quota !== undefined) out.push(quota);
  }
  return out;
}

/** The storage quota, if the server reports one. */
export function storageQuota(quotas: readonly Quota[]): Quota | undefined {
  return quotas.find((quota) => quota.resourceType === "octets" || quota.id === "storage");
}

// ---------------------------------------------------------------------------
// SieveScript — activation only
// ---------------------------------------------------------------------------

/** One script, as `SieveScript/get` renders it (RFC 9661 §2.1). */
export interface SieveScript {
  readonly id: string;
  readonly name: string;
  readonly isActive: boolean;
}

/** The managed script's name (`sieve.ManagedScriptName`). */
export const MANAGED_SCRIPT_NAME = "moov";

export async function fetchSieveScripts(
  client: JmapClient,
  accountId: string,
  signal?: AbortSignal,
): Promise<readonly SieveScript[]> {
  const response = await client.call(
    [["SieveScript/get", { accountId, ids: null, properties: ["name", "isActive"] }, "s"]],
    SIEVE_CAPS,
    signal,
  );
  const args = responseFor(response.methodResponses, "s");
  const out: SieveScript[] = [];
  for (const item of (args.list ?? []) as readonly unknown[]) {
    if (typeof item !== "object" || item === null) continue;
    const o = item as Record<string, unknown>;
    if (typeof o.id !== "string") continue;
    out.push({ id: o.id, name: str(o.name), isActive: bool(o.isActive) });
  }
  return out;
}

/**
 * Activates the Moov-managed script — the fix the `scriptActive` banner offers.
 *
 * # Why activation is allowed when update and destroy are not
 *
 * RFC 9661 §4 forbids exactly two things on the script that materializes the
 * VacationResponse: it "MUST NOT ... be destroyed or have its content updated
 * by the SieveScript/set method". `sieve.go` implements precisely that pair —
 * `sieveUpdateOne` and `sieveDestroyOne` answer `forbidden` for the managed id —
 * and the ACTIVATION path does not consult the managed id at all:
 *
 * ```go
 * if extras.OnSuccessActivateScript != nil { … d.Sieve.ActivateScript(…) }
 * ```
 *
 * So "activar las reglas de Moov" is a legal operation on the server's own
 * terms, and the banner can offer a button instead of instructions. The call is
 * a `/set` with NO creations, updates or destructions — `allOK` is vacuously
 * true, which §2.4 requires before the activation argument applies — carrying
 * only `onSuccessActivateScript`.
 *
 * What this REPLACES is the previously active foreign script, and the honest
 * consequence is stated in the UI before the click: the other script stops
 * running. The server preserves it (origin partitioning keeps foreign content
 * verbatim inside the managed script when it was taken over), so nothing is
 * destroyed — but a user who has a hand-written script deserves to be told
 * which one wins.
 */
export async function activateManagedScript(
  client: JmapClient,
  accountId: string,
  scriptId: string,
  signal?: AbortSignal,
): Promise<SetOutcome> {
  const response = await client.call(
    [["SieveScript/set", { accountId, onSuccessActivateScript: scriptId }, "s"]],
    SIEVE_CAPS,
    signal,
  );
  return readSetResponse(responseFor(response.methodResponses, "s"));
}
