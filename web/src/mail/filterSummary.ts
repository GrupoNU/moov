/**
 * Rule summaries and client-side validation (E6, GC-4).
 *
 * # Why the summary is built here and not in JSX
 *
 * A rule's list row has to answer "what does this do" in one line, and that
 * line is a JOIN over up to five criteria and eight actions with the locale's
 * own separators. Building it in the component would put a dozen conditional
 * fragments in the middle of a `<li>`, and would make "does an attachment-only
 * rule read correctly" a question only a rendered DOM can answer. As data, it
 * is a pure function of a rule and a translator.
 *
 * # Why validation is duplicated from the server
 *
 * `internal/sieve/model.go`'s `Validate` is the authority and runs on every
 * push — this is not a replacement for it. It exists so the builder can refuse
 * BEFORE the round trip, with the problem attached to the field that caused it,
 * rather than showing the server's flattened problem list under the Save
 * button. The rules mirrored are exactly the ones a user can hit by filling the
 * form:
 *
 *   - a filter needs at least one criterion (`Criteria.empty()`);
 *   - a filter needs at least one action (`Actions.empty()`);
 *   - `moveTo` and `delete` are mutually exclusive;
 *   - a forward target must be a VERIFIED address (GC-4's whole point);
 *   - no control characters anywhere (`safeScriptString`);
 *   - no negative size bounds.
 *
 * Deliberately NOT mirrored: the extension-availability check
 * (`caps.HasExtension`). The client cannot see the server's advertised SIEVE
 * extension list, and guessing at it would produce refusals the server would
 * have accepted. That one stays server-side, where its answer is real.
 */

import { isBlockableAddress } from "./blockedSenders";
import type { FilterRule, FilterRuleDraft } from "./filters";

/** One problem, named by the field that owns it. */
export type FilterProblem =
  | "noCriteria"
  | "noActions"
  | "moveAndDelete"
  | "forwardUnverified"
  | "forwardNotAddress"
  | "blockedNeedsAddress"
  | "blockedNotAddress"
  | "controlCharacters"
  | "negativeSize";

/** The server's `safeScriptString`: no C0 controls, no DEL. */
export function isSafeScriptString(value: string): boolean {
  for (const char of value) {
    const code = char.codePointAt(0) ?? 0;
    if (code < 0x20 || code === 0x7f) return false;
  }
  return true;
}

/** True when a draft names no criterion at all (`Criteria.empty()`). */
export function hasNoCriteria(draft: FilterRuleDraft): boolean {
  return (
    draft.from.length === 0 &&
    draft.to.length === 0 &&
    draft.subject.length === 0 &&
    draft.sizeOver === 0 &&
    draft.sizeUnder === 0 &&
    draft.hasAttachment === null
  );
}

/** True when a draft names no action at all (`Actions.empty()`). */
export function hasNoActions(draft: FilterRuleDraft): boolean {
  return (
    draft.moveTo === "" &&
    draft.labels.length === 0 &&
    !draft.markRead &&
    !draft.star &&
    draft.forward === "" &&
    !draft.delete &&
    !draft.stop
  );
}

/**
 * Every problem with a draft, given the addresses the server has verified.
 *
 * `verified` is the set of ACCEPTED forwarding addresses, lowercased. The
 * builder's forward picker only lists those, so this check fires mainly when a
 * rule is edited after its destination was removed — which is precisely the
 * case the server's `ErrForwardingInUse` exists to prevent and this one exists
 * to explain.
 */
export function validateFilterRule(
  draft: FilterRuleDraft,
  verified: ReadonlySet<string>,
): readonly FilterProblem[] {
  const problems: FilterProblem[] = [];

  if (draft.type === "blocked") {
    if (draft.from.length === 0) problems.push("blockedNeedsAddress");
    else if (!draft.from.every(isBlockableAddress)) problems.push("blockedNotAddress");
  } else {
    if (hasNoCriteria(draft)) problems.push("noCriteria");
    // `neverSpam` needs a criterion but no action — the action IS the type.
    if (draft.type === "filter" && hasNoActions(draft)) problems.push("noActions");
  }

  if (draft.moveTo !== "" && draft.delete) problems.push("moveAndDelete");

  if (draft.forward !== "") {
    if (!isBlockableAddress(draft.forward)) problems.push("forwardNotAddress");
    else if (!verified.has(draft.forward.trim().toLowerCase())) {
      problems.push("forwardUnverified");
    }
  }

  const strings = [
    ...draft.from,
    ...draft.to,
    ...draft.subject,
    draft.moveTo,
    ...draft.labels,
  ];
  if (!strings.every(isSafeScriptString)) problems.push("controlCharacters");

  if (draft.sizeOver < 0 || draft.sizeUnder < 0) problems.push("negativeSize");

  return problems;
}

// ---------------------------------------------------------------------------
// summaries
// ---------------------------------------------------------------------------

/** How the summary builder asks for words, so it stays locale-agnostic. */
export interface SummaryWords {
  readonly from: string;
  readonly to: string;
  readonly subject: string;
  readonly sizeOver: (bytes: string) => string;
  readonly sizeUnder: (bytes: string) => string;
  readonly hasAttachment: string;
  readonly noAttachment: string;
  readonly moveTo: (folder: string) => string;
  readonly label: (name: string) => string;
  readonly markRead: string;
  readonly star: string;
  readonly forward: (address: string) => string;
  readonly deleteAction: string;
  readonly neverSpam: string;
  readonly stop: string;
  readonly separator: string;
  readonly empty: string;
  readonly formatBytes: (bytes: number) => string;
}

/** The criteria half of a rule's one-line description. */
export function criteriaSummary(rule: FilterRule, words: SummaryWords): string {
  const parts: string[] = [];
  if (rule.from.length > 0) parts.push(`${words.from}: ${rule.from.join(", ")}`);
  if (rule.to.length > 0) parts.push(`${words.to}: ${rule.to.join(", ")}`);
  if (rule.subject.length > 0) parts.push(`${words.subject}: ${rule.subject.join(", ")}`);
  if (rule.sizeOver > 0) parts.push(words.sizeOver(words.formatBytes(rule.sizeOver)));
  if (rule.sizeUnder > 0) parts.push(words.sizeUnder(words.formatBytes(rule.sizeUnder)));
  if (rule.hasAttachment === true) parts.push(words.hasAttachment);
  if (rule.hasAttachment === false) parts.push(words.noAttachment);
  return parts.length === 0 ? words.empty : parts.join(words.separator);
}

/**
 * The actions half.
 *
 * A `neverSpam` rule has no action fields set — its type IS its action — so the
 * type is rendered as the action rather than leaving the row blank.
 */
export function actionsSummary(rule: FilterRule, words: SummaryWords): string {
  const parts: string[] = [];
  if (rule.type === "neverSpam") parts.push(words.neverSpam);
  if (rule.moveTo !== "") parts.push(words.moveTo(rule.moveTo));
  for (const label of rule.labels) parts.push(words.label(label));
  if (rule.markRead) parts.push(words.markRead);
  if (rule.star) parts.push(words.star);
  if (rule.forward !== "") parts.push(words.forward(rule.forward));
  if (rule.delete) parts.push(words.deleteAction);
  if (rule.stop) parts.push(words.stop);
  return parts.length === 0 ? words.empty : parts.join(words.separator);
}

/**
 * Bytes as a human size.
 *
 * Binary units (KiB-sized steps labelled KB), which is what every mail client
 * shows and what the size operators in search already mean. One decimal above
 * a kilobyte, none below — "1.5 MB" is useful, "1536.0 B" is noise.
 */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "0 B";
  if (bytes < 1024) return `${String(Math.round(bytes))} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(1)} ${units[unit] ?? "KB"}`;
}

/**
 * Parses a size the user typed, in the unit they chose, into bytes.
 *
 * Returns `undefined` for anything that is not a non-negative number, which the
 * builder renders as "leave it empty" rather than silently storing 0 — 0 is the
 * server's spelling of "unset", so a typo becoming 0 would silently drop the
 * criterion the user meant to add.
 */
export function parseSize(value: string, unit: "B" | "KB" | "MB"): number | undefined {
  const trimmed = value.trim();
  if (trimmed === "") return 0;
  const parsed = Number(trimmed);
  if (!Number.isFinite(parsed) || parsed < 0) return undefined;
  const multiplier = unit === "MB" ? 1024 * 1024 : unit === "KB" ? 1024 : 1;
  return Math.round(parsed * multiplier);
}
