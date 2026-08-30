/**
 * Parsed query → the server's JMAP filter grammar (L3 epic E3).
 *
 * # Why this is a separate module from the parser
 *
 * `searchQuery.ts` knows Gmail's language. This module knows what
 * `internal/jmap/mail/query.go` will ACCEPT, which is a strictly smaller set,
 * and the difference between the two is the honest refusal list the UI shows.
 * Keeping them apart means the grammar does not shrink every time the server's
 * repertoire is discussed, and the repertoire's limits are all in one file
 * where they can be cited line by line.
 *
 * Every decision below is cross-checked against `query.go` at the commit this
 * was written. The citations are not decoration: each one is a rule that, if
 * broken, produces either an `unsupportedFilter` the user cannot act on, or —
 * far worse — a result list that silently ignored a condition.
 *
 * # The five rules that shape everything here
 *
 * **1. There is exactly ONE text field.** `query.go`'s `translateCondition`
 * folds `text`, `from`, `to` and `subject` into a single `f.text`, and
 * `mergeFilters` refuses "two different text conditions in one filter". So
 * `from:ana subject:report` CANNOT be sent as two conditions — it would be
 * refused outright. They are concatenated into one text term instead, which
 * the server answers with `websearch_to_tsquery` (it ANDs the words), and the
 * over-match this causes is the same one the server already documents for
 * `from`: "answering it with a whole-message match returns messages that
 * merely MENTION the address". The alternative was refusing the two most
 * common searches a person types. Named in {@link FilterPlan.approximations}
 * so the UI can say the search was broadened rather than pretending precision.
 *
 * **2. `cc:` and `bcc:` are EXACT and separate.** Same file: migration 0008
 * gave each its own trigram index, "so `cc:ana@x.test` matches the Cc header
 * and only the Cc header". They are therefore sent as their own conditions and
 * are NOT folded into the text — folding them would make them worse.
 *
 * **3. A filter needs a text or an inMailbox to be answerable.** `answerable()`
 * refuses anything else: "this filter needs an inMailbox or a text condition".
 * So `is:unread` alone, or `has:attachment` alone, is unanswerable — and the
 * honest thing is to say so BEFORE sending, naming what to add, rather than
 * relaying a server error the user cannot parse.
 *
 * **4. `hasKeyword` needs a text.** `answerable()` again: "the folder view has
 * no keyword predicate". So `label:x in:inbox` is refused while `label:x` alone
 * (whole-account) and `label:x report` (text path) are served.
 *
 * **5. An OR nested inside an AND is refused.** `mergeFilters` says so and
 * names the remedy: "distribute it into one OR of complete conditions". Since
 * every branch of our OR is a complete condition already, we never build that
 * shape — but the branch bound (4) and the per-branch answerability rule are
 * enforced here, before the request, for the same reason as rule 3.
 */

import {
  MAX_OR_BRANCHES,
  isEmptyGroup,
  type ParsedQuery,
  type QueryGroup,
  type UnsupportedTerm,
} from "./searchQuery";
import { SYSTEM_FLAG_KEYWORDS, encodeLabelKeyword } from "./labels";
import { KEYWORD_FLAGGED, KEYWORD_SEEN, type Mailbox } from "./types";

/**
 * The scope keyword that means "including Spam and Trash".
 *
 * The server excludes both by default from any search carrying a condition
 * (`applyDefaultExclusion`, citing canon §2.5), and the documented escape
 * hatch is an EMPTY `inMailboxOtherThan`: "a client that has never heard of
 * this behavior and wants everything sends an empty inMailboxOtherThan and
 * gets everything".
 */
export const SCOPE_ANYWHERE = "anywhere";

/** Why the mapper could not send part of a query. */
export interface FilterProblem {
  /** A stable code the UI turns into a translated sentence. */
  readonly code:
    /** No text and no folder: `answerable()` would refuse it. */
    | "needsTextOrFolder"
    /** A label filter scoped to a folder: no keyword predicate on that path. */
    | "labelNeedsText"
    /** `in:<name>` named a folder this account does not have. */
    | "unknownMailbox"
    /** More OR branches than the server's `maxOrBranches`. */
    | "tooManyBranches"
    /** A branch of an OR that would not stand on its own. */
    | "branchNotAnswerable";
  /** The operator or value at fault, for quoting back. */
  readonly detail?: string;
}

/** A widening the server's repertoire forces, which the UI states plainly. */
export interface FilterApproximation {
  readonly code: "fieldsFoldedIntoText";
  /** The field names that were folded, e.g. ["from", "subject"]. */
  readonly fields: readonly string[];
}

/** Everything the UI needs to run one search and explain it. */
export interface FilterPlan {
  /** The JMAP filter to send, or undefined when nothing can be sent. */
  readonly filter: Record<string, unknown> | null | undefined;
  /** Terms the grammar recognised but excluded (from the parser). */
  readonly unsupported: readonly UnsupportedTerm[];
  /** Reasons the whole search cannot run as typed. */
  readonly problems: readonly FilterProblem[];
  /** Ways the search was broadened to fit the repertoire. */
  readonly approximations: readonly FilterApproximation[];
  /**
   * True when the query asked for `in:anywhere` — i.e. Spam and Trash are IN.
   * Surfaced so the UI can say which scope produced the results.
   */
  readonly includesEverything: boolean;
  /** The mailbox the query scoped itself to, when it named one. */
  readonly scopedMailboxId?: string;
  /**
   * E4: `is:muted` / `-is:muted` — a narrowing applied AFTER the results
   * arrive, not part of the filter.
   *
   * `true` keeps only muted conversations, `false` keeps only unmuted ones,
   * `undefined` means the term was not used. It is on the plan rather than
   * inside `filter` because it is deliberately not a wire condition: the server
   * refused a vendor `inMutedThread` on measured grounds and offers `Mute/get`
   * instead, so the predicate is answered against the cached set of thread ids.
   *
   * The consequence is real and must be SAID, not hidden: the term narrows the
   * page that came back, not the search. A muted conversation outside the
   * server's 200-row window is not found by `is:muted` — which is why the UI
   * labels the result as a view over what was loaded.
   */
  readonly mutedOnly?: boolean;
}

/**
 * Resolves an `in:<name>` to a mailbox id.
 *
 * Roles first, then name (case-insensitively): a user typing `in:inbox` means
 * the mailbox WITH THE INBOX ROLE, which may be named "Bandeja de entrada" on
 * a Spanish server. Falling back to the literal name is what makes
 * `in:Proyectos` work for a folder that has no role at all.
 */
export function resolveScope(
  scope: string,
  mailboxes: readonly Mailbox[],
): Mailbox | undefined {
  const wanted = scope.toLowerCase();
  const roleAliases: Readonly<Record<string, string>> = {
    inbox: "inbox",
    spam: "junk",
    junk: "junk",
    trash: "trash",
    bin: "trash",
    sent: "sent",
    drafts: "drafts",
    draft: "drafts",
    archive: "archive",
    starred: "flagged",
  };
  const role = roleAliases[wanted];
  if (role !== undefined) {
    const byRole = mailboxes.find((mailbox) => mailbox.role === role);
    if (byRole !== undefined) return byRole;
  }
  return mailboxes.find((mailbox) => mailbox.name.toLowerCase() === wanted);
}

/** One group's conditions, plus what it could not express. */
interface GroupPlan {
  readonly conditions: Record<string, unknown>[];
  readonly problems: FilterProblem[];
  readonly approximations: FilterApproximation[];
  readonly includesEverything: boolean;
  readonly mailboxId?: string;
}

/**
 * Maps one conjunctive group onto §4.4.1 conditions.
 *
 * The output is a LIST of conditions to be ANDed, never a pre-built operator,
 * because the caller decides whether one group becomes a bare condition (the
 * flat, common case) or a branch of an OR.
 */
function planGroup(group: QueryGroup, mailboxes: readonly Mailbox[]): GroupPlan {
  const conditions: Record<string, unknown>[] = [];
  const problems: FilterProblem[] = [];
  const approximations: FilterApproximation[] = [];
  let includesEverything = false;
  let mailboxId: string | undefined;

  /*
   * RULE 1: from / to / subject and the free text share ONE tsvector, and the
   * server refuses a second, different text condition. So they are joined into
   * one `text`, in the order Gmail's own options panel lists them.
   *
   * `cc` and `bcc` are deliberately NOT in this fold — RULE 2.
   */
  const foldedFields: string[] = [];
  const textParts: string[] = [];
  for (const field of ["from", "to", "subject"] as const) {
    const value = group.fields[field];
    if (value === undefined) continue;
    foldedFields.push(field);
    // A value containing spaces is quoted so `websearch_to_tsquery` reads it as
    // a phrase rather than as loose ANDed words.
    textParts.push(/\s/.test(value) ? `"${value}"` : value);
  }
  if (group.text !== "") textParts.push(group.text);

  const text = textParts.join(" ").trim();
  if (text !== "") conditions.push({ text });
  if (foldedFields.length > 0 && (foldedFields.length > 1 || group.text !== "")) {
    // Only worth stating when something was actually MERGED — a lone `from:ana`
    // is folded too, but the server's own from/to/subject handling would have
    // over-matched identically, so there is nothing new to disclose.
    approximations.push({ code: "fieldsFoldedIntoText", fields: foldedFields });
  }

  // RULE 2: the exact address conditions, sent as themselves.
  for (const field of ["cc", "bcc"] as const) {
    const value = group.fields[field];
    if (value !== undefined) conditions.push({ [field]: value });
  }

  if (group.hasAttachment !== undefined) {
    conditions.push({ hasAttachment: group.hasAttachment });
  }

  /*
   * The system flags, through the two predicates the server actually has.
   *
   * `is:unread` is `notKeyword:$seen` — the server's own words: "The unread
   * filter keeps its own field ... spelled as the literal the partial index is
   * built on". `is:read` is its `hasKeyword` twin, which `applyHasKeyword`
   * serves through the flag bitmask E3 added.
   *
   * `is:starred` / `-is:starred` are `hasKeyword`/`notKeyword` on `$flagged`,
   * which is the pair that made this a CORE Gmail operator answerable at all.
   */
  if (group.unread === true) conditions.push({ notKeyword: KEYWORD_SEEN });
  if (group.unread === false) conditions.push({ hasKeyword: KEYWORD_SEEN });
  if (group.starred === true) conditions.push({ hasKeyword: KEYWORD_FLAGGED });
  if (group.starred === false) conditions.push({ notKeyword: KEYWORD_FLAGGED });

  if (group.after !== undefined) conditions.push({ after: group.after });
  if (group.before !== undefined) conditions.push({ before: group.before });
  if (group.larger !== undefined) {
    // §4.4.1 minSize is ">= this number" while Gmail's `larger:` is strictly
    // greater. The off-by-one octet is not worth a second condition the server
    // would have to merge; the inclusive bound is used and the difference is
    // invisible at the sizes anyone types.
    conditions.push({ minSize: group.larger });
  }
  if (group.smaller !== undefined) conditions.push({ maxSize: group.smaller });

  /*
   * Scope. Three shapes, and the middle one is the whole point of the
   * server's `applyDefaultExclusion`:
   *
   *   in:anywhere      -> inMailboxOtherThan: []  (the documented escape hatch)
   *   in:<name>        -> inMailbox: <id>
   *   nothing          -> NOTHING IS SENT. The server applies Gmail's default
   *                       exclusion of Spam and Trash itself, and a client that
   *                       sent its own inMailboxOtherThan here would SUPPRESS
   *                       that default ("an explicit inMailboxOtherThan ...
   *                       suppresses the server's default one"). Sending the
   *                       exclusion by hand is how a client accidentally puts
   *                       Spam back into every search.
   */
  if (group.inMailbox === SCOPE_ANYWHERE) {
    conditions.push({ inMailboxOtherThan: [] });
    includesEverything = true;
  } else if (group.inMailbox !== undefined) {
    const mailbox = resolveScope(group.inMailbox, mailboxes);
    if (mailbox === undefined) {
      problems.push({ code: "unknownMailbox", detail: group.inMailbox });
    } else {
      conditions.push({ inMailbox: mailbox.id });
      mailboxId = mailbox.id;
    }
  }

  /*
   * RULE 4: a label is a `hasKeyword`, and `answerable()` serves it only on the
   * text path or as a bare whole-account filter — "the folder view has no
   * keyword predicate". So a label combined with a FOLDER is refused here,
   * before the request, naming what to remove.
   */
  if (group.label !== undefined) {
    const keyword = encodeLabelKeyword(group.label);
    if (text === "" && (mailboxId !== undefined || includesEverything)) {
      problems.push({ code: "labelNeedsText", detail: group.label });
    } else {
      conditions.push({ hasKeyword: keyword });
    }
  }

  return { conditions, problems, approximations, includesEverything, ...(mailboxId !== undefined ? { mailboxId } : {}) };
}

/**
 * RULE 3, applied to one group's conditions.
 *
 * The server's `answerable()` needs a `text` or an `inMailbox`. A bare
 * `hasKeyword` ALSO passes — it is the whole-account label view E8 ships — but
 * only when the keyword is a USER LABEL, and that distinction is the bug this
 * function was written wrong once already.
 *
 * `applyHasKeyword` splits on the keyword's identity: a SYSTEM flag
 * (`$seen`, `$flagged`, `$answered`, `$draft`) becomes a bit in `flagsAll` and
 * never touches `f.keyword`, while everything else goes to the keyword array.
 * `answerable()` then tests `f.keyword != ""` — so `hasKeyword:$flagged` alone
 * leaves BOTH `text` and `keyword` empty and is refused with "this filter needs
 * an inMailbox or a text condition".
 *
 * Counting every `hasKeyword` as answerable therefore let `is:starred` through
 * as a standalone filter that the server would reject — the exact
 * round-trip-and-fail this module exists to avoid.
 */
function isAnswerable(conditions: readonly Record<string, unknown>[]): boolean {
  return conditions.some((condition) => {
    if ("text" in condition || "inMailbox" in condition) return true;
    const keyword = condition.hasKeyword;
    return typeof keyword === "string" && !isSystemFlagKeyword(keyword);
  });
}

/** True for the four IMAP system flags the server keeps in its bitmask. */
function isSystemFlagKeyword(keyword: string): boolean {
  return SYSTEM_FLAG_KEYWORDS.some(
    (flag) => flag.toLowerCase() === keyword.toLowerCase(),
  );
}

/** Folds a condition list into the one JMAP filter node the server expects. */
function toNode(
  conditions: readonly Record<string, unknown>[],
): Record<string, unknown> | undefined {
  if (conditions.length === 0) return undefined;
  if (conditions.length === 1) return conditions[0];
  return { operator: "AND", conditions: [...conditions] };
}

/**
 * Builds the request plan for a parsed query.
 *
 * Returns `filter: undefined` when nothing can be sent — which is different
 * from `filter: null`, the server's account-wide enumeration. The caller must
 * not confuse them: `undefined` means "do not search", `null` means "search
 * everything".
 */
export function planFilter(
  query: ParsedQuery,
  mailboxes: readonly Mailbox[],
): FilterPlan {
  const groups = query.groups.filter((group) => !isEmptyGroup(group));

  if (groups.length === 0) {
    return {
      filter: undefined,
      unsupported: query.unsupported,
      problems: [],
      approximations: [],
      includesEverything: false,
    };
  }

  // RULE 5's sibling: the branch bound, enforced client-side with a message the
  // user can act on rather than relayed from the server after a round trip.
  if (groups.length > MAX_OR_BRANCHES) {
    return {
      filter: undefined,
      unsupported: query.unsupported,
      problems: [{ code: "tooManyBranches", detail: String(groups.length) }],
      approximations: [],
      includesEverything: false,
    };
  }

  const plans = groups.map((group) => planGroup(group, mailboxes));
  const problems = plans.flatMap((plan) => plan.problems);
  const approximations = plans.flatMap((plan) => plan.approximations);
  const includesEverything = plans.some((plan) => plan.includesEverything);
  const scopedMailboxId = plans.length === 1 ? plans[0]?.mailboxId : undefined;

  /*
   * E4: `is:muted`, taken off the groups and carried on the plan.
   *
   * Only a SINGLE-branch query can honour it. Across an OR the term belongs to
   * ONE branch, and a post-filter runs over the merged result set — so
   * `is:muted OR from:ana` would become "muted AND (muted or from ana)", a
   * different search than the one typed. It is therefore REFUSED by name in
   * that case rather than dropped: a term that silently does nothing is the
   * failure mode this whole module exists to prevent.
   */
  const mutedOnly = groups.length === 1 ? groups[0]?.muted : undefined;
  const mutedAcrossOr: readonly UnsupportedTerm[] =
    groups.length > 1 && groups.some((group) => group.muted !== undefined)
      ? [{ operator: "is", raw: "is:muted", reason: "deferredOperator" as const }]
      : [];
  const unsupported = [...query.unsupported, ...mutedAcrossOr];

  /*
   * THE OR RULE, mirrored from `translateOr`: "A BRANCH OF AN OR MUST BE A
   * FILTER THIS SERVER WOULD SERVE ON ITS OWN. ... a disjunction never narrows,
   * it only widens, so a branch that would scan the account alone scans the
   * account here too."
   *
   * Checked per branch here so the message names WHICH branch, which the
   * server's own error also does — but without spending the round trip.
   */
  for (const [index, plan] of plans.entries()) {
    if (plans.length > 1 && !isAnswerable(plan.conditions)) {
      problems.push({ code: "branchNotAnswerable", detail: String(index + 1) });
    }
  }

  if (problems.length > 0) {
    return {
      filter: undefined,
      unsupported,
      problems,
      approximations,
      includesEverything,
    };
  }

  if (plans.length === 1) {
    const only = plans[0];
    if (only === undefined) {
      return {
        filter: undefined,
        unsupported,
        problems: [],
        approximations,
        includesEverything,
      };
    }
    if (!isAnswerable(only.conditions)) {
      return {
        filter: undefined,
        unsupported,
        problems: [{ code: "needsTextOrFolder" }],
        approximations,
        includesEverything,
      };
    }
    return {
      filter: toNode(only.conditions) ?? null,
      unsupported,
      problems: [],
      approximations,
      includesEverything,
      ...(scopedMailboxId !== undefined ? { scopedMailboxId } : {}),
      // E4: only a single-branch query carries it; across an OR it was refused
      // by name above rather than applied to the wrong set.
      ...(mutedOnly !== undefined ? { mutedOnly } : {}),
    };
  }

  return {
    filter: {
      operator: "OR",
      conditions: plans.map((plan) => toNode(plan.conditions) ?? {}),
    },
    unsupported,
    problems: [],
    approximations,
    includesEverything,
  };
}
