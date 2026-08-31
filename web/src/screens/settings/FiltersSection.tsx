import { useId, useState } from "react";

import { useConfirm } from "../../components/useConfirm";
import { useTranslation } from "../../i18n/I18nProvider";
import {
  actionsSummary,
  criteriaSummary,
  formatBytes,
  parseSize,
  validateFilterRule,
  type FilterProblem,
  type SummaryWords,
} from "../../mail/filterSummary";
import {
  EMPTY_RULE,
  type FilterRule,
  type FilterRuleDraft,
  type ForwardingAddress,
} from "../../mail/filters";
import type { Label } from "../../mail/labelStore";
import type { Mailbox } from "../../mail/types";
import type { PlainStringKey } from "./registry";
import styles from "./FiltersSection.module.css";

/**
 * The filter manager (L3 epic E6, GC-4).
 *
 * # The section's whole job is order and honesty
 *
 * Two things make this more than CRUD, and both are consequences of what a
 * filter IS on this server — a section of a Sieve script Dovecot executes:
 *
 *   1. **Order is configuration.** Sieve runs top to bottom and `stop` ends the
 *      script for that message, so the position of a rule changes what the mail
 *      server does. The list therefore states the position, offers up/down, and
 *      says so in prose — a list that looked like an unordered set would be
 *      lying about the mechanism.
 *   2. **The rules can exist and not run.** When another Sieve script holds the
 *      account's active slot, `FilterRule/get` reports `scriptActive: false` and
 *      these rules filter nothing. That is the {@link ForeignScriptBanner}, and
 *      it is the reason the server invented a non-standard response property.
 *
 * # What the builder deliberately does not offer (GC-4)
 *
 * No date condition and no free-text search condition. Gmail has neither
 * (canon §2.6: "no date criterion — filters run forward in time; dates belong
 * to search"), and Sieve has no equivalent for the second. The builder does not
 * render them disabled, does not render them at all, and says why in one line —
 * "restrict, don't fake" made visible rather than left in a spec.
 *
 * # The forward picker lists ONLY verified addresses
 *
 * Because the server refuses anything else, and it fails CLOSED
 * (`CheckRedirectPolicy`: "content whose redirects cannot be read is refused").
 * Offering an unverified address would be building a control whose only outcome
 * is a refusal. With none verified, the picker is replaced by the hint that
 * names where to get one.
 */

export interface FiltersSectionProps {
  readonly rules: readonly FilterRule[];
  /** `false` puts the honest banner at the top of the section. */
  readonly scriptActive: boolean;
  /** Verified destinations, for the forward picker. */
  readonly forwardingAddresses: readonly ForwardingAddress[];
  /** Folders, for the "move to" picker. */
  readonly mailboxes: readonly Mailbox[];
  /** E8's labels, for the "apply labels" picker. */
  readonly labels: readonly Label[];
  readonly onCreate: (draft: FilterRuleDraft) => void;
  readonly onUpdate: (id: string, draft: FilterRuleDraft) => void;
  readonly onDelete: (rule: FilterRule) => void;
  readonly onMove: (id: string, direction: "up" | "down") => void;
  /**
   * Activates the Moov script. Absent when the server has no Sieve capability —
   * which removes the button and leaves the banner's explanation, because the
   * situation is still true and only the remedy is unavailable.
   */
  readonly onActivate?: (() => void) | undefined;
  readonly isActivating?: boolean;
  readonly isBusy?: boolean;
  readonly error?: string | undefined;
}

export function FiltersSection({
  rules,
  scriptActive,
  forwardingAddresses,
  mailboxes,
  labels,
  onCreate,
  onUpdate,
  onDelete,
  onMove,
  onActivate,
  isActivating = false,
  isBusy = false,
  error,
}: FiltersSectionProps): React.JSX.Element {
  const { t, format } = useTranslation();
  const { confirm, dialog: confirmDialog } = useConfirm();
  /** The rule being edited, `"new"` for a fresh one, undefined when closed. */
  const [editing, setEditing] = useState<FilterRule | "new" | undefined>(undefined);

  /*
   * "Bloqueados" has its own section, so the FILTERS list shows only what a
   * user would call a filter. A blocked rule appearing here as a nameless rule
   * whose action is invisible (the Junk filing is compiled from the type, not
   * stored in a field) would look like a broken row.
   */
  const visible = rules.filter((rule) => rule.type !== "blocked");
  const verified = new Set(
    forwardingAddresses
      .filter((address) => address.state === "accepted")
      .map((address) => address.email.toLowerCase()),
  );

  const words = summaryWords(t);

  return (
    <div className={styles.wrap}>
      {confirmDialog}

      {!scriptActive && (
        <ForeignScriptBanner
          onActivate={onActivate}
          isActivating={isActivating}
        />
      )}

      <p className={styles.explain}>{t("filters.description")}</p>

      {error !== undefined && (
        <p className={styles.error} role="alert">
          {error}
        </p>
      )}

      <ul className={styles.list} aria-label={t("settings.section.filters")}>
        {visible.map((rule, index) => (
          <li key={rule.id} className={styles.row}>
            <div className={styles.rowText}>
              <span className={styles.rowName}>
                {rule.name === "" ? t("filters.unnamed") : rule.name}
                {!rule.enabled && (
                  <span className={styles.paused}>{t("filters.disabled")}</span>
                )}
              </span>
              {/*
                The position, stated. A user reasoning about why their "stop"
                rule swallowed a later one needs the number, and reading it off
                the visual order is exactly the inference an accessible list
                must not require.
              */}
              <span className={styles.rowOrder}>
                {format("filters.order", index + 1, visible.length)}
              </span>
              <span className={styles.rowSummary}>
                <strong>{t("filters.criteria")}</strong> {criteriaSummary(rule, words)}
              </span>
              <span className={styles.rowSummary}>
                <strong>{t("filters.actions")}</strong> {actionsSummary(rule, words)}
              </span>
            </div>

            <div className={styles.rowActions}>
              <button
                type="button"
                className={styles.iconButton}
                disabled={index === 0 || isBusy}
                aria-label={`${t("filters.moveUp")}: ${rule.name}`}
                title={t("filters.moveUp")}
                onClick={() => {
                  onMove(rule.id, "up");
                }}
              >
                <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
                  <path d="M10 15V5M5.5 9.5L10 5l4.5 4.5" />
                </svg>
              </button>
              <button
                type="button"
                className={styles.iconButton}
                disabled={index === visible.length - 1 || isBusy}
                aria-label={`${t("filters.moveDown")}: ${rule.name}`}
                title={t("filters.moveDown")}
                onClick={() => {
                  onMove(rule.id, "down");
                }}
              >
                <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
                  <path d="M10 5v10M5.5 10.5L10 15l4.5-4.5" />
                </svg>
              </button>
              <button
                type="button"
                className={styles.secondary}
                disabled={isBusy}
                onClick={() => {
                  setEditing(rule);
                }}
              >
                {t("filters.edit")}
              </button>
              <button
                type="button"
                className={styles.danger}
                disabled={isBusy}
                onClick={() => {
                  void (async () => {
                    const name = rule.name === "" ? t("filters.unnamed") : rule.name;
                    if (
                      !(await confirm({
                        message: format("filters.deleteConfirm", name),
                        destructive: true,
                        confirmLabel: t("filters.delete"),
                      }))
                    ) {
                      return;
                    }
                    onDelete(rule);
                  })();
                }}
              >
                {t("filters.delete")}
              </button>
            </div>
          </li>
        ))}

        {visible.length === 0 && <li className={styles.empty}>{t("filters.none")}</li>}
      </ul>

      <button
        type="button"
        className={styles.primary}
        disabled={isBusy}
        onClick={() => {
          setEditing("new");
        }}
      >
        {t("filters.create")}
      </button>

      {editing !== undefined && (
        <FilterBuilder
          rule={editing === "new" ? undefined : editing}
          mailboxes={mailboxes}
          labels={labels}
          verified={verified}
          hasVerifiedAddress={verified.size > 0}
          onCancel={() => {
            setEditing(undefined);
          }}
          onSave={(draft) => {
            if (editing === "new") onCreate(draft);
            else onUpdate(editing.id, draft);
            setEditing(undefined);
          }}
        />
      )}
    </div>
  );
}

/**
 * The banner that fires on `scriptActive: false`.
 *
 * `role="alert"` and not `note`, unlike the reader's spam banner: this IS an
 * event from the user's point of view — they configured rules and the rules are
 * not running — and it is the one thing on this screen that must interrupt.
 *
 * The wording states the preservation guarantee explicitly, because the obvious
 * fear when a button says "activate mine" is "what happens to the other one".
 * The server's origin partitioning keeps foreign content verbatim, so the
 * honest answer is "it stays stored, it stops being active", and saying it is
 * what makes the button clickable by someone who has a script they care about.
 */
function ForeignScriptBanner({
  onActivate,
  isActivating,
}: {
  readonly onActivate: (() => void) | undefined;
  readonly isActivating: boolean;
}): React.JSX.Element {
  const { t } = useTranslation();
  return (
    <div className={styles.banner} role="alert">
      <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
        <path d="M10 2.8l7.2 12.4H2.8z" />
        <path d="M10 8v3.4M10 13.6v.1" />
      </svg>
      <div className={styles.bannerText}>
        <p className={styles.bannerTitle}>{t("filters.foreignScript")}</p>
        <p className={styles.bannerBody}>{t("filters.foreignScriptBody")}</p>
      </div>
      {onActivate !== undefined && (
        <button
          type="button"
          className={styles.primary}
          disabled={isActivating}
          onClick={onActivate}
        >
          {isActivating ? t("filters.activating") : t("filters.activate")}
        </button>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// the builder
// ---------------------------------------------------------------------------

/** The builder's own draft state, in the shapes the inputs hold. */
interface BuilderState {
  readonly name: string;
  readonly enabled: boolean;
  /** Textareas, one value per line — the shape the wire's arrays come from. */
  readonly from: string;
  readonly to: string;
  readonly subject: string;
  readonly sizeOver: string;
  readonly sizeUnder: string;
  readonly sizeUnit: "B" | "KB" | "MB";
  readonly hasAttachment: "any" | "yes" | "no";
  readonly moveTo: string;
  readonly labels: readonly string[];
  readonly markRead: boolean;
  readonly star: boolean;
  readonly forward: string;
  readonly delete: boolean;
  readonly neverSpam: boolean;
  readonly stop: boolean;
}

/** Splits a textarea into the wire's array, dropping blank lines. */
function lines(value: string): readonly string[] {
  return value
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "");
}

function stateFromRule(rule: FilterRule | undefined): BuilderState {
  const base = rule ?? { ...EMPTY_RULE, id: "" };
  return {
    name: base.name,
    enabled: base.enabled,
    from: base.from.join("\n"),
    to: base.to.join("\n"),
    subject: base.subject.join("\n"),
    // Bytes are shown in bytes when they came from the server: re-deriving the
    // unit the user originally typed is not possible, and guessing "5 MB" for
    // 5_000_000 would silently change the value on the next save.
    sizeOver: base.sizeOver === 0 ? "" : String(base.sizeOver),
    sizeUnder: base.sizeUnder === 0 ? "" : String(base.sizeUnder),
    sizeUnit: "B",
    hasAttachment: base.hasAttachment === null ? "any" : base.hasAttachment ? "yes" : "no",
    moveTo: base.moveTo,
    labels: base.labels,
    markRead: base.markRead,
    star: base.star,
    forward: base.forward,
    delete: base.delete,
    neverSpam: base.type === "neverSpam",
    stop: base.stop,
  };
}

/** Turns builder state into the draft the wire carries. */
function draftFromState(state: BuilderState): FilterRuleDraft | undefined {
  const sizeOver = parseSize(state.sizeOver, state.sizeUnit);
  const sizeUnder = parseSize(state.sizeUnder, state.sizeUnit);
  if (sizeOver === undefined || sizeUnder === undefined) return undefined;
  return {
    name: state.name.trim(),
    type: state.neverSpam ? "neverSpam" : "filter",
    enabled: state.enabled,
    from: lines(state.from),
    to: lines(state.to),
    subject: lines(state.subject),
    sizeOver,
    sizeUnder,
    hasAttachment:
      state.hasAttachment === "any" ? null : state.hasAttachment === "yes",
    moveTo: state.moveTo,
    labels: state.labels,
    markRead: state.markRead,
    star: state.star,
    forward: state.forward,
    delete: state.delete,
    stop: state.stop,
  };
}

function FilterBuilder({
  rule,
  mailboxes,
  labels,
  verified,
  hasVerifiedAddress,
  onCancel,
  onSave,
}: {
  readonly rule: FilterRule | undefined;
  readonly mailboxes: readonly Mailbox[];
  readonly labels: readonly Label[];
  readonly verified: ReadonlySet<string>;
  readonly hasVerifiedAddress: boolean;
  readonly onCancel: () => void;
  readonly onSave: (draft: FilterRuleDraft) => void;
}): React.JSX.Element {
  const { t } = useTranslation();
  const [state, setState] = useState<BuilderState>(() => stateFromRule(rule));
  const [problems, setProblems] = useState<readonly FilterProblem[]>([]);
  const idPrefix = useId();
  const patch = (next: Partial<BuilderState>): void => {
    setState((current) => ({ ...current, ...next }));
    setProblems([]);
  };

  const submit = (): void => {
    const draft = draftFromState(state);
    if (draft === undefined) {
      setProblems(["negativeSize"]);
      return;
    }
    const found = validateFilterRule(draft, verified);
    if (found.length > 0) {
      setProblems(found);
      return;
    }
    onSave(draft);
  };

  return (
    <form
      className={styles.builder}
      aria-label={rule === undefined ? t("filters.builder.newTitle") : t("filters.builder.editTitle")}
      onSubmit={(event) => {
        event.preventDefault();
        submit();
      }}
    >
      <h4 className={styles.builderTitle}>
        {rule === undefined ? t("filters.builder.newTitle") : t("filters.builder.editTitle")}
      </h4>

      <label className={styles.field}>
        <span className={styles.fieldLabel}>{t("filters.builder.name")}</span>
        <input
          type="text"
          className={styles.input}
          value={state.name}
          placeholder={t("filters.builder.namePlaceholder")}
          onChange={(event) => {
            patch({ name: event.target.value });
          }}
        />
      </label>

      <fieldset className={styles.group}>
        <legend className={styles.legend}>{t("filters.builder.criteriaLegend")}</legend>

        <MultiField
          id={`${idPrefix}-from`}
          label={t("filters.builder.from")}
          value={state.from}
          onChange={(from) => {
            patch({ from });
          }}
        />
        <MultiField
          id={`${idPrefix}-to`}
          label={t("filters.builder.to")}
          value={state.to}
          onChange={(to) => {
            patch({ to });
          }}
        />
        <MultiField
          id={`${idPrefix}-subject`}
          label={t("filters.builder.subject")}
          value={state.subject}
          onChange={(subject) => {
            patch({ subject });
          }}
        />

        <div className={styles.sizeRow}>
          <label className={styles.field}>
            <span className={styles.fieldLabel}>{t("filters.builder.sizeOver")}</span>
            <input
              type="text"
              inputMode="numeric"
              className={styles.inputShort}
              value={state.sizeOver}
              onChange={(event) => {
                patch({ sizeOver: event.target.value });
              }}
            />
          </label>
          <label className={styles.field}>
            <span className={styles.fieldLabel}>{t("filters.builder.sizeUnder")}</span>
            <input
              type="text"
              inputMode="numeric"
              className={styles.inputShort}
              value={state.sizeUnder}
              onChange={(event) => {
                patch({ sizeUnder: event.target.value });
              }}
            />
          </label>
          <select
            className={styles.select}
            aria-label={`${t("filters.builder.sizeOver")} / ${t("filters.builder.sizeUnder")}`}
            value={state.sizeUnit}
            onChange={(event) => {
              patch({ sizeUnit: event.target.value as BuilderState["sizeUnit"] });
            }}
          >
            <option value="B">B</option>
            <option value="KB">KB</option>
            <option value="MB">MB</option>
          </select>
        </div>

        <label className={styles.field}>
          <span className={styles.fieldLabel}>{t("filters.builder.attachment")}</span>
          <select
            className={styles.select}
            value={state.hasAttachment}
            onChange={(event) => {
              patch({ hasAttachment: event.target.value as BuilderState["hasAttachment"] });
            }}
          >
            <option value="any">{t("filters.builder.attachmentAny")}</option>
            <option value="yes">{t("filters.builder.attachmentYes")}</option>
            <option value="no">{t("filters.builder.attachmentNo")}</option>
          </select>
        </label>

        {/*
          GC-4, on screen. The absent conditions are NAMED rather than silently
          missing: a user who came looking for "mail older than a year" needs to
          know it is a deliberate restriction with a working alternative, not a
          field they failed to find.
        */}
        <p className={styles.note}>{t("filters.builder.noDateNote")}</p>
      </fieldset>

      <fieldset className={styles.group}>
        <legend className={styles.legend}>{t("filters.builder.actionsLegend")}</legend>

        <label className={styles.field}>
          <span className={styles.fieldLabel}>{t("filters.builder.moveTo")}</span>
          <select
            className={styles.select}
            value={state.moveTo}
            onChange={(event) => {
              patch({ moveTo: event.target.value });
            }}
          >
            <option value="">{t("filters.builder.moveToNone")}</option>
            {mailboxes.map((mailbox) => (
              <option key={mailbox.id} value={mailbox.name}>
                {mailbox.name}
              </option>
            ))}
          </select>
        </label>

        {labels.length > 0 && (
          <fieldset className={styles.checkGroup}>
            <legend className={styles.fieldLabel}>{t("filters.builder.labels")}</legend>
            {labels.map((label) => (
              <label key={label.keyword} className={styles.check}>
                <input
                  type="checkbox"
                  checked={state.labels.includes(label.name)}
                  onChange={(event) => {
                    patch({
                      labels: event.target.checked
                        ? [...state.labels, label.name]
                        : state.labels.filter((name) => name !== label.name),
                    });
                  }}
                />
                <span>{label.name}</span>
              </label>
            ))}
          </fieldset>
        )}

        <label className={styles.check}>
          <input
            type="checkbox"
            checked={state.markRead}
            onChange={(event) => {
              patch({ markRead: event.target.checked });
            }}
          />
          <span>{t("filters.builder.markRead")}</span>
        </label>

        <label className={styles.check}>
          <input
            type="checkbox"
            checked={state.star}
            onChange={(event) => {
              patch({ star: event.target.checked });
            }}
          />
          <span>{t("filters.builder.star")}</span>
        </label>

        {/*
          The forward picker. With nothing verified it is REPLACED by the hint
          rather than rendered empty — an empty picker offering only "do not
          forward" is a control that cannot do its job, and the hint names where
          the missing precondition comes from.
        */}
        {hasVerifiedAddress ? (
          <label className={styles.field}>
            <span className={styles.fieldLabel}>{t("filters.builder.forward")}</span>
            <select
              className={styles.select}
              value={state.forward}
              onChange={(event) => {
                patch({ forward: event.target.value });
              }}
            >
              <option value="">{t("filters.builder.forwardNone")}</option>
              {[...verified].map((address) => (
                <option key={address} value={address}>
                  {address}
                </option>
              ))}
            </select>
          </label>
        ) : (
          <p className={styles.note}>{t("filters.builder.forwardHint")}</p>
        )}

        <label className={styles.check}>
          <input
            type="checkbox"
            checked={state.delete}
            onChange={(event) => {
              patch({ delete: event.target.checked });
            }}
          />
          <span>{t("filters.builder.delete")}</span>
        </label>

        <label className={styles.check}>
          <input
            type="checkbox"
            checked={state.neverSpam}
            onChange={(event) => {
              patch({ neverSpam: event.target.checked });
            }}
          />
          <span>{t("filters.builder.neverSpam")}</span>
        </label>

        <label className={styles.check}>
          <input
            type="checkbox"
            checked={state.stop}
            onChange={(event) => {
              patch({ stop: event.target.checked });
            }}
          />
          <span>{t("filters.builder.stop")}</span>
        </label>
      </fieldset>

      {problems.length > 0 && (
        <ul className={styles.problems} role="alert">
          {problems.map((problem) => (
            <li key={problem}>{t(PROBLEM_KEYS[problem])}</li>
          ))}
        </ul>
      )}

      <div className={styles.builderActions}>
        <button type="submit" className={styles.primary}>
          {t("filters.builder.save")}
        </button>
        <button type="button" className={styles.secondary} onClick={onCancel}>
          {t("filters.builder.cancel")}
        </button>
      </div>
    </form>
  );
}

/**
 * A multi-value criterion.
 *
 * A textarea, one value per line, rather than a chip editor: Gmail's own filter
 * fields take a comma-separated string, the wire is an array of substrings, and
 * a chip editor would add a whole interaction model (backspace semantics, paste
 * splitting, focus management) for a field most users fill with one value.
 */
function MultiField({
  id,
  label,
  value,
  onChange,
}: {
  readonly id: string;
  readonly label: string;
  readonly value: string;
  readonly onChange: (value: string) => void;
}): React.JSX.Element {
  const { t } = useTranslation();
  return (
    <div className={styles.field}>
      <label className={styles.fieldLabel} htmlFor={id}>
        {label}
      </label>
      <textarea
        id={id}
        className={styles.textarea}
        rows={2}
        value={value}
        onChange={(event) => {
          onChange(event.target.value);
        }}
      />
      <span className={styles.hint}>{t("filters.builder.multiHint")}</span>
    </div>
  );
}

const PROBLEM_KEYS: Readonly<Record<FilterProblem, PlainStringKey>> = {
  noCriteria: "filters.problem.noCriteria",
  noActions: "filters.problem.noActions",
  moveAndDelete: "filters.problem.moveAndDelete",
  forwardUnverified: "filters.problem.forwardUnverified",
  forwardNotAddress: "filters.problem.forwardNotAddress",
  blockedNeedsAddress: "filters.problem.blockedNeedsAddress",
  blockedNotAddress: "filters.problem.blockedNotAddress",
  controlCharacters: "filters.problem.controlCharacters",
  negativeSize: "filters.problem.negativeSize",
};

/**
 * The summary vocabulary, bound to the active locale.
 *
 * Built here rather than inside `filterSummary.ts` so that module stays free of
 * React and of the string table — it is pure logic a test drives with its own
 * words, which is what let the summary rules be tested without a provider.
 */
function summaryWords(t: (key: PlainStringKey) => string): SummaryWords {
  return {
    from: t("filters.builder.from"),
    to: t("filters.builder.to"),
    subject: t("filters.builder.subject"),
    sizeOver: (bytes) => `${t("filters.builder.sizeOver")} ${bytes}`,
    sizeUnder: (bytes) => `${t("filters.builder.sizeUnder")} ${bytes}`,
    hasAttachment: t("filters.builder.attachmentYes"),
    noAttachment: t("filters.builder.attachmentNo"),
    moveTo: (folder) => `${t("filters.builder.moveTo")}: ${folder}`,
    label: (name) => `${t("filters.builder.labels")}: ${name}`,
    markRead: t("filters.builder.markRead"),
    star: t("filters.builder.star"),
    forward: (address) => `${t("filters.builder.forward")} ${address}`,
    deleteAction: t("filters.builder.delete"),
    neverSpam: t("filters.builder.neverSpam"),
    stop: t("filters.builder.stop"),
    separator: " · ",
    empty: t("filters.empty"),
    formatBytes,
  };
}
