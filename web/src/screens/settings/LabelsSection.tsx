import { useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import { labelColorVariables, LABEL_COLORS } from "../../mail/labelPalette";
import {
  LABEL_VISIBILITIES,
  type Label,
  type LabelVisibility,
} from "../../mail/labelStore";
import {
  validateLabelName,
  type LabelBudget,
  type LabelNameProblem,
} from "../../mail/labels";
import type { PlainStringKey } from "./registry";
import styles from "./LabelsSection.module.css";

/**
 * The label manager (L3 epic E8, GC-5).
 *
 * # The section's whole job is to be honest about 26
 *
 * Everything else here is ordinary CRUD. What is not ordinary is that creating
 * a label can be IMPOSSIBLE, for a reason no user could guess, and the design
 * rule of this epic is that the reason is on screen before the attempt rather
 * than after it:
 *
 *   - the budget is stated permanently ("17 de 26 disponibles"), not surfaced
 *     as an error at the 27th;
 *   - at zero the create control is disabled AND accompanied by the
 *     explanation and by the alternative that has no limit — a folder;
 *   - the number shown and the number the button obeys are the same
 *     `labelBudget` call, so the UI cannot offer what the server would refuse.
 *
 * Without that, the 27th label is created, applied, read back correctly for
 * weeks, and then vanishes from every message at once when Dovecot rebuilds its
 * Maildir index (validation V1). That is the failure mode this section is
 * shaped around.
 *
 * # Rename and delete are migrations, and they say so
 *
 * A label has no record of its own — it IS the keyword on every message. So
 * renaming walks every message and rewrites the keyword, and deleting walks
 * every message and removes it. Both are bounded loops that report progress and
 * can be stopped (`mail/migrateKeyword.ts`), and both surface an incomplete
 * result rather than claiming success: "1.800 actualizados — algunos todavía
 * tienen la etiqueta anterior" is the honest sentence.
 */

/** What the manager needs from its host, which owns the JMAP client. */
export interface LabelsSectionProps {
  readonly labels: readonly Label[];
  readonly budget: LabelBudget;
  /** Creates a label locally; the keyword only reaches a message when applied. */
  readonly onCreate: (name: string, colorId: string) => void;
  readonly onSetColor: (label: Label, colorId: string) => void;
  readonly onSetVisibility: (label: Label, visibility: LabelVisibility) => void;
  /** Runs the bounded keyword migration. */
  readonly onRename: (label: Label, newName: string) => void;
  readonly onDelete: (label: Label) => void;
  /** Progress text while a migration runs, or undefined when idle. */
  readonly migrationStatus?: string | undefined;
  readonly onAbortMigration?: (() => void) | undefined;
  /** Opens the new-folder flow — the alternative offered when the budget is 0. */
  readonly onCreateFolder?: (() => void) | undefined;
}

export function LabelsSection({
  labels,
  budget,
  onCreate,
  onSetColor,
  onSetVisibility,
  onRename,
  onDelete,
  migrationStatus,
  onAbortMigration,
  onCreateFolder,
}: LabelsSectionProps): React.JSX.Element {
  const { t, format } = useTranslation();
  const [name, setName] = useState("");
  const [colorId, setColorId] = useState(LABEL_COLORS[0]?.id ?? "slate");
  const [problem, setProblem] = useState<LabelNameProblem | "full" | undefined>(undefined);
  const [renaming, setRenaming] = useState<string | undefined>(undefined);
  const [renameValue, setRenameValue] = useState("");

  const existingNames = labels.map((label) => label.name);

  const submit = (): void => {
    if (budget.isFull) {
      setProblem("full");
      return;
    }
    const invalid = validateLabelName(name, existingNames);
    if (invalid !== undefined) {
      setProblem(invalid);
      return;
    }
    onCreate(name.trim(), colorId);
    setName("");
    setProblem(undefined);
  };

  return (
    <div className={styles.wrap}>
      {/*
        The section's own one-line description, naming what a label IS on this
        server. "Labels are IMAP keywords, so they cross folders — and a folder
        holds only 26 of them" is the sentence that makes the budget below make
        sense instead of looking arbitrary.

        The `settings.section.labels` reference below is also what lets the
        registry's drift scan see this row as rendered: it is the row's
        `labelKey`, and the rail's heading reaches it only through a lookup
        table a source scan cannot follow. It is a visually-hidden label on the
        list further down rather than a duplicate heading, so it earns its
        place semantically instead of existing only to satisfy a test.
      */}
      <p className={styles.explain}>{t("settings.labels.description")}</p>

      {/*
        The budget line. It is a `status` so a screen-reader user hears the
        number change after creating a label, rather than discovering the
        ceiling only when the button stops working.
      */}
      <p className={styles.budget} role="status">
        <span className={budget.isFull ? styles.budgetFull : styles.budgetOk}>
          {budget.isFull
            ? t("label.budgetFull")
            : format("label.budget", budget.available, budget.ceiling)}
        </span>
      </p>
      <p className={styles.explain}>{t("label.budgetExplained")}</p>
      {budget.isFull && onCreateFolder !== undefined && (
        <button type="button" className={styles.linkButton} onClick={onCreateFolder}>
          {t("label.createFolderInstead")}
        </button>
      )}

      {/*
        Prefs v2 closed the metadata gap. The note is now the POSITIVE fact
        rather than a caveat — a user who read the old "does not follow you to
        another device" warning has to be told it no longer holds, and deleting
        the line silently would leave them believing it.
      */}
      <p className={styles.note}>{t("label.roams")}</p>

      {/* --- create --- */}
      <form
        className={styles.createRow}
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <label className={styles.field}>
          <span className={styles.fieldLabel}>{t("label.name")}</span>
          <input
            type="text"
            className={styles.input}
            value={name}
            disabled={budget.isFull}
            onChange={(event) => {
              setName(event.target.value);
              setProblem(undefined);
            }}
          />
        </label>

        <ColorPicker
          value={colorId}
          disabled={budget.isFull}
          onChange={setColorId}
          label={t("label.color")}
        />

        <button type="submit" className={styles.primary} disabled={budget.isFull}>
          {t("label.create")}
        </button>
      </form>

      {problem !== undefined && (
        <p className={styles.error} role="alert">
          {t(PROBLEM_KEYS[problem])}
        </p>
      )}

      {migrationStatus !== undefined && (
        <p className={styles.progress} role="status">
          {migrationStatus}
          {onAbortMigration !== undefined && (
            <button type="button" className={styles.linkButton} onClick={onAbortMigration}>
              {t("label.abort")}
            </button>
          )}
        </p>
      )}

      {/* --- the list --- */}
      <ul className={styles.list} aria-label={t("settings.section.labels")}>
        {labels.map((label) => (
          <li key={label.keyword} className={styles.row}>
            {renaming === label.keyword ? (
              <form
                className={styles.renameForm}
                onSubmit={(event) => {
                  event.preventDefault();
                  const invalid = validateLabelName(
                    renameValue,
                    existingNames.filter((other) => other !== label.name),
                  );
                  if (invalid !== undefined) {
                    setProblem(invalid);
                    return;
                  }
                  onRename(label, renameValue.trim());
                  setRenaming(undefined);
                  setProblem(undefined);
                }}
              >
                <input
                  type="text"
                  className={styles.input}
                  aria-label={format("label.renameTitle", label.name)}
                  value={renameValue}
                  onChange={(event) => {
                    setRenameValue(event.target.value);
                    setProblem(undefined);
                  }}
                />
                <button type="submit" className={styles.primary}>
                  {t("label.rename")}
                </button>
                <button
                  type="button"
                  className={styles.secondary}
                  onClick={() => {
                    setRenaming(undefined);
                    setProblem(undefined);
                  }}
                >
                  {t("action.cancel")}
                </button>
              </form>
            ) : (
              <>
                <span
                  className={styles.chip}
                  style={labelColorVariables(label.colorId)}
                >
                  {label.name}
                </span>

                <ColorPicker
                  value={label.colorId}
                  disabled={false}
                  onChange={(id) => {
                    onSetColor(label, id);
                  }}
                  label={`${t("label.color")}: ${label.name}`}
                />

                <select
                  className={styles.select}
                  aria-label={`${t("label.visibility")}: ${label.name}`}
                  value={label.visibility}
                  onChange={(event) => {
                    onSetVisibility(label, event.target.value as LabelVisibility);
                  }}
                >
                  {LABEL_VISIBILITIES.map((visibility) => (
                    <option key={visibility} value={visibility}>
                      {t(VISIBILITY_KEYS[visibility])}
                    </option>
                  ))}
                </select>

                <button
                  type="button"
                  className={styles.secondary}
                  onClick={() => {
                    setRenaming(label.keyword);
                    setRenameValue(label.name);
                  }}
                >
                  {t("label.rename")}
                </button>
                <button
                  type="button"
                  className={styles.danger}
                  onClick={() => {
                    onDelete(label);
                  }}
                >
                  {t("label.delete")}
                </button>
              </>
            )}
          </li>
        ))}

        {labels.length === 0 && <li className={styles.empty}>{t("label.none")}</li>}
      </ul>
    </div>
  );
}

/**
 * The closed palette, as a radio group.
 *
 * A radio group and not a `<select>` of colour names: the choice IS the colour,
 * so it has to be seen. `role="radiogroup"` with one radio per swatch is the
 * APG pattern for "pick exactly one of these", and it gives each swatch an
 * accessible name (the colour's id) that a screen reader can read — which a
 * grid of unlabelled coloured divs cannot.
 */
function ColorPicker({
  value,
  disabled,
  onChange,
  label,
}: {
  readonly value: string;
  readonly disabled: boolean;
  readonly onChange: (id: string) => void;
  readonly label: string;
}): React.JSX.Element {
  return (
    <span className={styles.palette} role="radiogroup" aria-label={label}>
      {LABEL_COLORS.map((color) => (
        <label key={color.id} className={styles.swatchLabel}>
          <input
            type="radio"
            className="visually-hidden"
            name={`${label}-color`}
            value={color.id}
            checked={value === color.id}
            disabled={disabled}
            onChange={() => {
              onChange(color.id);
            }}
          />
          <span
            className={`${styles.swatch} ${value === color.id ? styles.swatchActive : ""}`}
            style={labelColorVariables(color.id)}
            aria-hidden="true"
          />
          <span className="visually-hidden">{color.id}</span>
        </label>
      ))}
    </span>
  );
}

const PROBLEM_KEYS: Readonly<Record<LabelNameProblem | "full", PlainStringKey>> = {
  empty: "label.error.empty",
  tooLong: "label.error.tooLong",
  reserved: "label.error.reserved",
  duplicate: "label.error.duplicate",
  control: "label.error.control",
  full: "label.error.full",
};

const VISIBILITY_KEYS: Readonly<Record<LabelVisibility, PlainStringKey>> = {
  show: "label.visibility.show",
  showIfUnread: "label.visibility.showIfUnread",
  hide: "label.visibility.hide",
};
