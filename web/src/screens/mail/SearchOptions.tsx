import { useId, useMemo, useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import { mailboxSegment } from "../../mail/mailboxes";
import { SCOPE_ANYWHERE } from "../../mail/searchFilter";
import { formatQuery, parseSearchQuery, type QueryGroup } from "../../mail/searchQuery";
import {
  DROPPED_CRITERION_LABELS,
  searchToFilterDraft,
  type FilterDraftFromSearch,
} from "../../mail/searchToFilter";
import type { Mailbox } from "../../mail/types";
import styles from "./SearchOptions.module.css";

/**
 * Gmail's search-options panel (L3 epic E3; canon §2.5).
 *
 * # What it builds, and why it builds a STRING
 *
 * The panel does not construct a filter. It composes a QUERY STRING and hands
 * it to the box, which parses it like any typed query. That is the round trip
 * the whole epic is built on: one grammar, one source of truth, and a panel
 * that can never produce a search the user could not have typed — or that the
 * chips would then disagree about.
 *
 * # The two Gmail fields that are deliberately absent
 *
 * **"Doesn't have the words" is NOT here, and it is not coming back.** The
 * server refuses the NOT operator on principle: `translateOperator` explains
 * that a complement is a set no index can produce, so answering it means
 * visiting every message in the account. A field that produced an error on
 * submit — or worse, one that silently returned results including the excluded
 * words — is exactly the dead control P4 forbids. It is omitted entirely
 * rather than disabled, because a greyed-out field still advertises a feature
 * that does not exist.
 *
 * # "Crear filtro" landed in E12/B7, exactly as the TODO predicted
 *
 * E3 left a typed TODO here saying the button "takes THIS panel's PanelState —
 * the criteria map 1:1 onto GC-4's algebra {from, to, subject, hasAttachment,
 * size} — and hands it to the filter builder as the new rule's condition.
 * Nothing here needs to change but the button." That held: `PanelState` is
 * structurally `searchToFilter.ts`'s `SearchCriteria`, so there is no
 * translation layer, only a call.
 *
 * Two things the button does that the TODO did not anticipate, both about
 * honesty. It is ABSENT when the server has no Sieve capability, because a
 * button that opens nothing is the dead control P4 forbids; and it is DISABLED
 * when the criteria map to no rule condition at all, because a rule with no
 * conditions matches every message — a search of pure free text would otherwise
 * offer to build a filter that files the whole inbox. What a filter cannot
 * carry over is NAMED under the buttons rather than dropped in silence.
 */

export interface SearchOptionsProps {
  /** The current query, so the panel opens showing what is already searched. */
  readonly query: string;
  readonly mailboxes: readonly Mailbox[];
  /**
   * E-15: the folder the list is currently showing, so "En esta carpeta" can be
   * a real option rather than a label on the default.
   *
   * Absent — on a search route, a label view, the Outbox — removes that row.
   * There is no "this folder" when the user is not in one, and offering it
   * anyway would be a scope with nothing behind it.
   */
  readonly currentMailbox?: Mailbox | undefined;
  /** Runs the composed query. */
  readonly onSubmit: (query: string) => void;
  readonly onClose: () => void;
  /**
   * E12/B7: opens the filter builder pre-filled from these criteria
   * (canon 07 §8).
   *
   * Absent removes the button entirely rather than disabling it — which is the
   * case when the server does not advertise the Sieve capability. There is
   * nothing to create then, and a control that opens nothing is exactly the
   * dead affordance P4 forbids.
   *
   * It receives the whole {@link FilterDraftFromSearch}, not only the rule, so
   * the caller can also tell the user what the search expressed that a filter
   * cannot carry.
   */
  readonly onCreateFilter?: ((draft: FilterDraftFromSearch) => void) | undefined;
}

/** The panel's own form state — transient, discarded on submit. */
interface PanelState {
  from: string;
  to: string;
  subject: string;
  words: string;
  sizeMode: "larger" | "smaller";
  sizeValue: string;
  sizeUnit: "K" | "M" | "G";
  /** A `newer_than:` day count as a string, or "" for any time. */
  within: string;
  hasAttachment: boolean;
  /** A mailbox id, `SCOPE_ANYWHERE`, or "" for the server's default scope. */
  scope: string;
}

/** Gmail's "Date within" presets, as day counts. */
const WITHIN_OPTIONS = [
  { value: "1", key: "search.within.1d" },
  { value: "3", key: "search.within.3d" },
  { value: "7", key: "search.within.1w" },
  { value: "14", key: "search.within.2w" },
  { value: "30", key: "search.within.1m" },
  { value: "60", key: "search.within.2m" },
  { value: "180", key: "search.within.6m" },
  { value: "365", key: "search.within.1y" },
] as const;

/** Seeds the form from the query already in the box, so it never lies. */
function stateFromQuery(query: string, mailboxes: readonly Mailbox[]): PanelState {
  const parsed = parseSearchQuery(query);
  const group: QueryGroup | undefined = parsed.groups[0];

  const scope = ((): string => {
    if (group?.inMailbox === undefined) return "";
    if (group.inMailbox === SCOPE_ANYWHERE) return SCOPE_ANYWHERE;
    const match = mailboxes.find(
      (mailbox) => mailboxSegment(mailbox).toLowerCase() === group.inMailbox,
    );
    return match?.id ?? "";
  })();

  const size = group?.larger ?? group?.smaller;
  return {
    from: group?.fields.from ?? "",
    to: group?.fields.to ?? "",
    subject: group?.fields.subject ?? "",
    words: group?.text ?? "",
    sizeMode: group?.smaller !== undefined ? "smaller" : "larger",
    // Shown in MB, which is the unit a person types; the exact octets are
    // recomposed on submit.
    sizeValue: size !== undefined ? String(Math.round(size / (1024 * 1024))) : "",
    sizeUnit: "M",
    within: "",
    hasAttachment: group?.hasAttachment === true,
    scope,
  };
}

/** Composes the query string. The parser is the only thing that reads it back. */
function queryFromState(state: PanelState, mailboxes: readonly Mailbox[]): string {
  const parts: string[] = [];
  const quote = (value: string): string => (/\s/.test(value) ? `"${value}"` : value);

  if (state.from.trim() !== "") parts.push(`from:${quote(state.from.trim())}`);
  if (state.to.trim() !== "") parts.push(`to:${quote(state.to.trim())}`);
  if (state.subject.trim() !== "") parts.push(`subject:${quote(state.subject.trim())}`);
  if (state.hasAttachment) parts.push("has:attachment");

  if (state.sizeValue.trim() !== "" && Number(state.sizeValue) > 0) {
    parts.push(`${state.sizeMode}:${Number(state.sizeValue)}${state.sizeUnit}`);
  }

  /*
   * "Date within" is Gmail's date ± window. The plan asks for an after+before
   * PAIR, and that is what a window around a point means — but the panel has
   * no anchor date field (Gmail's sits next to it and is a free-form date),
   * so the anchor is NOW and the window is a `newer_than:`. That yields the
   * `after` half; the `before` half of "within N of today" is the future,
   * which no message has. Stated here rather than left as a silent
   * simplification.
   */
  if (state.within !== "") parts.push(`newer_than:${state.within}d`);

  if (state.scope === SCOPE_ANYWHERE) {
    parts.push(`in:${SCOPE_ANYWHERE}`);
  } else if (state.scope !== "") {
    const mailbox = mailboxes.find((candidate) => candidate.id === state.scope);
    if (mailbox !== undefined) parts.push(`in:${quote(mailboxSegment(mailbox))}`);
  }

  if (state.words.trim() !== "") parts.push(state.words.trim());

  /*
   * Normalised through the parser before it leaves. It costs one parse and it
   * guarantees the panel can only ever emit a string the grammar accepts —
   * which is the invariant that makes the box, the chips and this panel three
   * views of ONE value rather than three sources of truth.
   */
  return formatQuery(parseSearchQuery(parts.join(" ")));
}

export function SearchOptions({
  query,
  mailboxes,
  currentMailbox,
  onSubmit,
  onClose,
  onCreateFilter,
}: SearchOptionsProps): React.JSX.Element {
  const { t, format } = useTranslation();
  const [state, setState] = useState<PanelState>(() => stateFromQuery(query, mailboxes));
  const ids = useId();
  const field = (name: string): string => `${ids}-${name}`;

  const set = <K extends keyof PanelState>(key: K, value: PanelState[K]): void => {
    setState((current) => ({ ...current, [key]: value }));
  };

  /*
   * E12/B7: the panel's criteria as a filter rule.
   *
   * Recomputed on every change rather than on the click, so the button's
   * disabled state and the "this will not carry over" note both track what is
   * on screen. Computing it in the handler would let the user press a button
   * that was enabled for criteria they have since cleared.
   *
   * `PanelState` is structurally the module's `SearchCriteria`, which is the
   * 1:1 mapping E3's TODO predicted — so there is nothing to translate here.
   */
  const filterDraft = useMemo<FilterDraftFromSearch>(
    () => searchToFilterDraft(state),
    [state],
  );

  return (
    /*
     * The Escape handler sits on the form, and the a11y rule that flags it is
     * guarding against a different situation than this one.
     *
     * `jsx-a11y/no-noninteractive-element-interactions` exists to catch
     * handlers on elements a keyboard user can never reach — a div that only
     * responds to a mouse. Here the handler is a KEY handler on a container
     * whose every child is focusable, and it does the thing the APG asks a
     * dismissible popup to do: Escape closes it. Without it the panel would be
     * the one control in the header a keyboard user cannot back out of, which
     * is the accessibility defect, not the fix.
     */
    // eslint-disable-next-line jsx-a11y/no-noninteractive-element-interactions
    <form
      className={styles.panel}
      aria-label={t("search.options")}
      onSubmit={(event) => {
        event.preventDefault();
        onSubmit(queryFromState(state, mailboxes));
      }}
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          event.stopPropagation();
          onClose();
        }
      }}
    >
      <div className={styles.grid}>
        <label className={styles.row} htmlFor={field("from")}>
          <span className={styles.label}>{t("search.options.from")}</span>
          <input
            id={field("from")}
            className={styles.input}
            value={state.from}
            onChange={(event) => {
              set("from", event.target.value);
            }}
            autoComplete="off"
          />
        </label>

        <label className={styles.row} htmlFor={field("to")}>
          <span className={styles.label}>{t("search.options.to")}</span>
          <input
            id={field("to")}
            className={styles.input}
            value={state.to}
            onChange={(event) => {
              set("to", event.target.value);
            }}
            autoComplete="off"
          />
        </label>

        <label className={styles.row} htmlFor={field("subject")}>
          <span className={styles.label}>{t("search.options.subject")}</span>
          <input
            id={field("subject")}
            className={styles.input}
            value={state.subject}
            onChange={(event) => {
              set("subject", event.target.value);
            }}
            autoComplete="off"
          />
        </label>

        <label className={styles.row} htmlFor={field("words")}>
          <span className={styles.label}>{t("search.options.words")}</span>
          <input
            id={field("words")}
            className={styles.input}
            value={state.words}
            onChange={(event) => {
              set("words", event.target.value);
            }}
            autoComplete="off"
          />
        </label>

        {/*
          Gmail's "Doesn't have the words" would sit HERE. It is absent by
          decision, not by oversight: the server refuses NOT on principle
          (query.go translateOperator), so the field could only ever produce an
          error. See this component's doc comment.
        */}

        <div className={styles.row}>
          <span className={styles.label} id={field("size-label")}>
            {t("search.options.size")}
          </span>
          <div className={styles.inline} role="group" aria-labelledby={field("size-label")}>
            <select
              className={styles.select}
              value={state.sizeMode}
              aria-label={t("search.options.size")}
              onChange={(event) => {
                set("sizeMode", event.target.value === "smaller" ? "smaller" : "larger");
              }}
            >
              <option value="larger">{t("search.options.sizeLarger")}</option>
              <option value="smaller">{t("search.options.sizeSmaller")}</option>
            </select>
            <input
              className={styles.number}
              type="number"
              min="0"
              inputMode="numeric"
              value={state.sizeValue}
              aria-label={t("search.options.size")}
              onChange={(event) => {
                set("sizeValue", event.target.value);
              }}
            />
            <select
              className={styles.select}
              value={state.sizeUnit}
              aria-label={t("search.options.sizeUnit")}
              onChange={(event) => {
                const unit = event.target.value;
                set("sizeUnit", unit === "K" || unit === "G" ? unit : "M");
              }}
            >
              <option value="K">KB</option>
              <option value="M">MB</option>
              <option value="G">GB</option>
            </select>
          </div>
        </div>

        <label className={styles.row} htmlFor={field("within")}>
          <span className={styles.label}>{t("search.options.dateWithin")}</span>
          <select
            id={field("within")}
            className={styles.select}
            value={state.within}
            onChange={(event) => {
              set("within", event.target.value);
            }}
          >
            <option value="">{t("search.chip.anyTime")}</option>
            {WITHIN_OPTIONS.map((option) => (
              <option key={option.value} value={option.value}>
                {t(option.key)}
              </option>
            ))}
          </select>
        </label>

        <label className={styles.row} htmlFor={field("scope")}>
          <span className={styles.label}>{t("search.options.scope")}</span>
          <select
            id={field("scope")}
            className={styles.select}
            value={state.scope}
            onChange={(event) => {
              set("scope", event.target.value);
            }}
          >
            {/*
              E-15 (owner's decision 2, 2026-09-09): the DEFAULT is the whole
              account, and the first option now says so.

              The wire never changed — an empty scope sends no `in:` at all and
              the server applies Gmail's own exclusion of Spam and Trash
              (`applyDefaultExclusion`), which is exactly "all mail" in Gmail's
              sense. What was wrong was the LABEL: this option read "En esta
              carpeta", so a user reading the panel believed every search was
              folder-scoped when none of them were. A control that misdescribes
              what it does is worse than a missing one, because the user acts on
              the description.

              "En esta carpeta" survives as a real option below, resolving to
              the folder the user is actually looking at — which is the thing
              the old label promised and never delivered.
            */}
            <option value="">{t("search.options.scopeDefault")}</option>
            {currentMailbox !== undefined && (
              <option value={currentMailbox.id}>{t("search.inMailbox")}</option>
            )}
            <option value={SCOPE_ANYWHERE}>{t("search.options.scopeAll")}</option>
            {/* The current folder is omitted here: it already has its own row
                above, and two options carrying the same value would make the
                select's rendered label depend on which one the browser matched
                first. */}
            {mailboxes
              .filter((mailbox) => mailbox.id !== currentMailbox?.id)
              .map((mailbox) => (
                <option key={mailbox.id} value={mailbox.id}>
                  {mailbox.name}
                </option>
              ))}
          </select>
        </label>

        <label className={styles.checkboxRow} htmlFor={field("attach")}>
          <input
            id={field("attach")}
            type="checkbox"
            checked={state.hasAttachment}
            onChange={(event) => {
              set("hasAttachment", event.target.checked);
            }}
          />
          <span>{t("search.options.hasAttachment")}</span>
        </label>
      </div>

      <div className={styles.actions}>
        <button
          type="button"
          className={styles.secondary}
          onClick={() => {
            setState(stateFromQuery("", mailboxes));
          }}
        >
          {t("search.options.reset")}
        </button>

        {/*
          E12/B7: "Crear filtro" (canon 07 §8), which E3's TODO predicted
          exactly — "nothing here needs to change but the button".

          It is present only when the caller wired a builder, which is only
          when the server advertises the Sieve capability: without it there is
          nothing to create, and a button that opened nothing would be the dead
          control P4 forbids.

          It is DISABLED when the panel's criteria map to no rule condition at
          all. That is not defensive styling — a rule with no conditions
          matches EVERY message, so a search of pure free text would otherwise
          offer to build a filter that files the whole inbox. The title says
          why, so a greyed button is never a mystery.
        */}
        {onCreateFilter !== undefined && (
          <button
            type="button"
            className={styles.secondary}
            disabled={!filterDraft.usable}
            title={
              filterDraft.usable
                ? t("search.options.createFilter")
                : t("search.options.createFilterUnusable")
            }
            onClick={() => {
              onCreateFilter(filterDraft);
            }}
          >
            {t("search.options.createFilter")}
          </button>
        )}

        <button type="submit" className={styles.primary}>
          {t("search.options.submit")}
        </button>
      </div>

      {/*
        What the filter will NOT carry over, said BEFORE the builder opens.

        A search can express things a delivery-time filter cannot — free text,
        a date range, a folder scope — and dropping them silently would give
        the user a filter that matches far more mail than the search they built
        it from. They would discover it weeks later, as archived mail they
        wanted.
      */}
      {onCreateFilter !== undefined && filterDraft.dropped.length > 0 && (
        <p className={styles.droppedNote} role="status">
          {format(
            "search.options.filterDrops",
            filterDraft.dropped.map((code) => t(DROPPED_CRITERION_LABELS[code])).join(", "),
          )}
        </p>
      )}
    </form>
  );
}
