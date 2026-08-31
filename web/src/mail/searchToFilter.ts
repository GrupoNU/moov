import { EMPTY_RULE, type FilterRuleDraft } from "./filters";

/**
 * "Crear filtro" — turning a search into a filter rule (E12/B7, canon 07 §8).
 *
 * # The affordance this completes
 *
 * Gmail's advanced-search panel has two buttons: "Buscar" and "Crear filtro".
 * The second takes the criteria you just typed and opens the filter builder
 * pre-filled with them, which is how most people ever create a filter — they
 * search for the mail they are tired of, then ask for it to be handled
 * automatically from now on.
 *
 * `SearchOptions` has carried a typed TODO for it since E3, saying exactly
 * this: "It takes THIS panel's PanelState — the criteria map 1:1 onto GC-4's
 * algebra {from, to, subject, hasAttachment, size} — and hands it to the filter
 * builder as the new rule's condition. Nothing here needs to change but the
 * button." That prediction held; this module is the mapping it described.
 *
 * # Why the mapping is PARTIAL, and why that is the honest shape
 *
 * A search can express things a filter cannot, and this is the whole reason
 * the function returns what it drops rather than silently discarding it:
 *
 *   - **Free text** ("contains the words") has no home. GC-4's rule algebra is
 *     a closed set of header and size predicates — there is no full-text
 *     condition in Sieve's `header` test, and inventing one would produce a
 *     rule the server refuses or, worse, one that matches the words in a
 *     SUBJECT and quietly ignores the body.
 *   - **The date range** has none either, and could not: a filter runs at
 *     delivery, so "newer than 7 days" is either always true or meaningless
 *     depending on how you read it. Gmail drops it the same way.
 *   - **The scope** (`in:`) is a search's own idea; a filter acts on mail
 *     ARRIVING, which is in exactly one place.
 *
 * Dropping any of them silently would be the failure this whole codebase
 * avoids: the user would get a filter that matches far more mail than the
 * search they built it from, and would discover it weeks later by finding
 * archived mail they wanted. So {@link searchToFilterDraft} names every
 * dropped criterion, and the UI says so before the builder opens.
 */

/** A criterion the filter algebra cannot express, and why. */
export type DroppedCriterion = "words" | "dateRange" | "scope";

/**
 * How each dropped criterion is NAMED to the user.
 *
 * Here rather than in a component, because TWO surfaces say it: the panel,
 * before the click, and the toast after the navigation. A second copy would be
 * two places for the wording to drift, and the two sentences would then
 * describe the same loss differently on the same journey.
 *
 * They reuse the panel's own FIELD labels deliberately — the user is being
 * told which of the boxes they just filled in will not survive, so naming them
 * anything other than what those boxes are called would make the sentence a
 * puzzle.
 */
export const DROPPED_CRITERION_LABELS: Readonly<
  Record<DroppedCriterion, "search.options.words" | "search.options.dateWithin" | "search.options.scope">
> = {
  words: "search.options.words",
  dateRange: "search.options.dateWithin",
  scope: "search.options.scope",
};

export interface FilterDraftFromSearch {
  /** The rule, pre-filled with everything that DID map. */
  readonly draft: FilterRuleDraft;
  /**
   * What could not be carried over. Empty means the filter matches exactly
   * what the search did.
   */
  readonly dropped: readonly DroppedCriterion[];
  /**
   * False when nothing mapped at all — a search of pure free text, say.
   *
   * The caller must not open the builder in that case: a rule with no
   * conditions matches EVERY message, and pre-filling one from a search that
   * expressed nothing it understands is how a user ends up archiving their
   * whole inbox with one click.
   */
  readonly usable: boolean;
}

/** The panel's criteria, as `SearchOptions` holds them. */
export interface SearchCriteria {
  readonly from: string;
  readonly to: string;
  readonly subject: string;
  readonly words: string;
  readonly sizeMode: "larger" | "smaller";
  /** The raw number the user typed, as a string. */
  readonly sizeValue: string;
  readonly sizeUnit: "K" | "M" | "G";
  /** A `newer_than:` day count as a string, or "" for any time. */
  readonly within: string;
  readonly hasAttachment: boolean;
  /** A mailbox id, the anywhere sentinel, or "" for the default scope. */
  readonly scope: string;
}

const UNIT_BYTES: Readonly<Record<"K" | "M" | "G", number>> = {
  K: 1024,
  M: 1024 * 1024,
  G: 1024 * 1024 * 1024,
};

/**
 * A criterion's value as a list, or an empty list.
 *
 * Trimmed, and empty strings dropped: a rule condition of `""` matches every
 * message, and the difference between "no from condition" and "a from
 * condition that always matches" is the difference between a filter that files
 * a newsletter and one that files everything.
 */
function terms(value: string): readonly string[] {
  const trimmed = value.trim();
  return trimmed === "" ? [] : [trimmed];
}

/** The size in BYTES, or 0 for "unset" — which is how the server spells it. */
function sizeBytes(value: string, unit: "K" | "M" | "G"): number {
  const parsed = Number.parseFloat(value);
  if (!Number.isFinite(parsed) || parsed <= 0) return 0;
  return Math.round(parsed * UNIT_BYTES[unit]);
}

/**
 * Builds a filter draft from a search panel's criteria.
 *
 * Pure, so the mapping — including everything it refuses to carry — is a unit
 * test rather than something a reviewer has to reason about from the JSX of a
 * form.
 */
export function searchToFilterDraft(criteria: SearchCriteria): FilterDraftFromSearch {
  const from = terms(criteria.from);
  const to = terms(criteria.to);
  const subject = terms(criteria.subject);
  const bytes = sizeBytes(criteria.sizeValue, criteria.sizeUnit);

  const dropped: DroppedCriterion[] = [];
  if (criteria.words.trim() !== "") dropped.push("words");
  if (criteria.within !== "") dropped.push("dateRange");
  if (criteria.scope !== "") dropped.push("scope");

  /*
   * `hasAttachment` is `null` when the box is UNTICKED, not `false`.
   *
   * The wire type is `Boolean|null` and the three values mean three different
   * things: true is "must have one", false is "must NOT have one", and null is
   * "do not care". An unticked box in a search panel means the user did not
   * ask about attachments — it does not mean they want messages WITHOUT them,
   * which is what `false` would file.
   */
  const hasAttachment = criteria.hasAttachment ? true : null;

  const usable =
    from.length > 0 ||
    to.length > 0 ||
    subject.length > 0 ||
    bytes > 0 ||
    hasAttachment !== null;

  return {
    draft: {
      ...EMPTY_RULE,
      /*
       * The name is left EMPTY on purpose, for the builder to fill or the user
       * to write. Deriving one from the criteria ("from:boletin@…") would put a
       * machine-generated string in the one field whose whole job is to be
       * what the user recognises this rule by six months from now.
       */
      from,
      to,
      subject,
      ...(criteria.sizeMode === "larger" ? { sizeOver: bytes } : { sizeUnder: bytes }),
      hasAttachment,
    },
    dropped,
    usable,
  };
}
