import { useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import {
  DEFAULT_LABEL_COLOR_ID,
  labelColorVariables,
  LABEL_COLORS,
  type LabelColorStep,
} from "../../mail/labelPalette";
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
import { FOLDER_VISIBILITIES, type FolderVisibility } from "../../mail/prefs";
import type { Mailbox } from "../../mail/types";
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
  /**
   * P0-5c: the folder-visibility table, below the labels.
   *
   * Optional so the section keeps working where there is no mailbox list to
   * show — the tests, and any host that has not loaded folders yet. Absent
   * renders no table at all rather than an empty one, because a heading over
   * nothing is worse than silence.
   */
  readonly folders?: FoldersTableProps | undefined;
}

/** What the folder table needs. Exported so the settings page can build it. */
export interface FoldersTableProps {
  /** Every folder in the account, in the rail's own order. */
  readonly folders: readonly Mailbox[];
  /** The visibility in force — the stored choice, or the policy's default. */
  readonly visibilityOf: (folder: Mailbox) => FolderVisibility;
  readonly onSetVisibility: (folder: Mailbox, visibility: FolderVisibility) => void;
  /** False on a server that does not serve prefs v3 — the table goes read-only. */
  readonly isAvailable: boolean;
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
  folders,
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

        {labels.length === 0 && (
          /*
            F-30: an actionable empty state, not a full stop.

            "Todavía no hay etiquetas" is true and useless — it tells a user who
            arrived looking for labels that they are in the right place and
            offers them nothing. What replaces it names the first thing to do,
            says in one line what a label IS on this server (it crosses folders,
            which is the whole reason to want one), and offers three examples.

            The examples PREFILL the name box rather than creating anything.
            Creating a label from a click would be a decision the user did not
            make, and the block here is not the button — it is "what would I
            even call one", which a name they can then edit answers.
          */
          <li className={styles.empty}>
            <strong className={styles.emptyTitle}>{t("label.emptyTitle")}</strong>
            <span className={styles.emptyBody}>{t("label.emptyBody")}</span>
            <span className={styles.emptyExamples}>
              <span className={styles.fieldLabel}>{t("label.emptyExamples")}</span>
              {EXAMPLE_LABEL_KEYS.map((key, index) => {
                const name = t(key);
                return (
                  <button
                    key={key}
                    type="button"
                    className={styles.exampleChip}
                    // The example's own colour, so the chips also demonstrate
                    // what a coloured label looks like.
                    style={labelColorVariables(EXAMPLE_COLOR_IDS[index])}
                    aria-label={format("label.useExample", name)}
                    onClick={() => {
                      setName(name);
                      setColorId(EXAMPLE_COLOR_IDS[index] ?? DEFAULT_LABEL_COLOR_ID);
                      setProblem(undefined);
                    }}
                  >
                    {name}
                  </button>
                );
              })}
            </span>
          </li>
        )}
      </ul>

      {folders !== undefined && <FoldersTable {...folders} />}
    </div>
  );
}

/**
 * The system-folder table (P0-5c, review F-28).
 *
 * # Why this lives in the LABELS tab
 *
 * Gmail's Etiquetas tab opens with a "System labels" table — Recibidos,
 * Destacados, Enviados… each with mostrar / ocultar — above the user's own
 * labels. This is that table, for the thing Moov has that Gmail does not: an
 * IMAP account whose folder list includes Calendario, Diario, Fuentes RSS and
 * Problemas de sincronización, none of which hold mail.
 *
 * It is the OTHER HALF of the rail's curation, and the half that makes the
 * curation defensible. The rail hides those folders by a NAME heuristic, which
 * can be wrong — someone's real folder might be called "Notas". Nothing is
 * deleted, and this is where a user sees every folder the account has, sees
 * which are hidden, and changes their mind. Without it the heuristic would be
 * an unexplained disappearance.
 *
 * # The three states, and why they are the label list's three
 *
 * "Mostrar / Ocultar / Mostrar si hay sin leer" — the same triple, the same
 * words, in a table one tab-stop away from the label list that already uses
 * them. A user who learned them once should not learn them twice.
 *
 * # Unavailable, rather than broken, on an older server
 *
 * The choice is stored server-side (prefs v3) so it roams. Against a v2 server
 * a switch would be accepted by the UI and refused by `Prefs/set` with
 * `unknownProperty` — silently reverting on reload, which is the worst kind of
 * failure. `isAvailable: false` renders the table read-only with a sentence
 * saying why, exactly as the other v2-gated rows do. The RAIL still curates
 * itself either way: the policy needs no preference to run.
 */
function FoldersTable({
  folders,
  visibilityOf,
  onSetVisibility,
  isAvailable,
}: FoldersTableProps): React.JSX.Element {
  const { t } = useTranslation();

  return (
    <section className={styles.folders} aria-labelledby="settings-folders-heading">
      <h3 id="settings-folders-heading" className={styles.foldersHeading}>
        {t("folders.heading")}
      </h3>
      <p className={styles.foldersHelp}>{t("folders.help")}</p>
      {!isAvailable && <p className={styles.foldersHelp}>{t("folders.unavailable")}</p>}

      <ul className={styles.list}>
        {folders.map((folder) => (
          <li key={folder.id} className={styles.row}>
            <span className={styles.folderName}>{folder.name}</span>
            <select
              className={styles.select}
              aria-label={`${t("folders.visibility")}: ${folder.name}`}
              value={visibilityOf(folder)}
              disabled={!isAvailable}
              onChange={(event) => {
                onSetVisibility(folder, event.target.value as FolderVisibility);
              }}
            >
              {FOLDER_VISIBILITIES.map((visibility) => (
                <option key={visibility} value={visibility}>
                  {t(FOLDER_VISIBILITY_KEYS[visibility])}
                </option>
              ))}
            </select>
          </li>
        ))}

        {folders.length === 0 && <li className={styles.empty}>{t("folders.none")}</li>}
      </ul>
    </section>
  );
}

/**
 * The closed palette, as a radio group.
 *
 * A radio group and not a `<select>` of colour names: the choice IS the colour,
 * so it has to be seen. `role="radiogroup"` with one radio per swatch is the
 * APG pattern for "pick exactly one of these", and it gives each swatch an
 * accessible name a screen reader can read — which a grid of unlabelled
 * coloured divs cannot.
 *
 * # F-31: the swatches are NAMED, and there are twice as many
 *
 * They were reading out their raw ids ("slate", "amber") and showing no tooltip
 * at all, so a sighted user hovering a pastel learned nothing and a screen
 * reader user heard a word from our source code. Each swatch now carries the
 * name a person would use — "Ámbar · Suave" — as BOTH its `title` and its
 * accessible name, so the tooltip and the announcement agree.
 *
 * The grid is laid out one row per hue (`--palette-columns: 2`), so a hue's
 * pale and bold steps sit beside each other and the twenty-four squares read as
 * twelve colours at two strengths rather than as twenty-four unrelated ones.
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
  const { t, format } = useTranslation();
  return (
    <span className={styles.palette} role="radiogroup" aria-label={label}>
      {LABEL_COLORS.map((color) => {
        const name = format(
          "label.colorName",
          t(HUE_KEYS[color.hue] ?? "label.color"),
          t(STEP_KEYS[color.step]),
        );
        return (
          <label key={color.id} className={styles.swatchLabel} title={name}>
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
            <span className="visually-hidden">{name}</span>
          </label>
        );
      })}
    </span>
  );
}

/**
 * The hue names, as a lookup rather than a total record.
 *
 * The palette's `hue` is a plain string (it is derived, not a closed union that
 * would force this table to be total), so an unknown hue falls back to the
 * generic "Color" rather than rendering a raw key. A palette entry added
 * without a name is then a swatch that says "Color · Intenso" — imprecise, but
 * never a string from our source code on a user's screen.
 */
const HUE_KEYS: Readonly<Record<string, PlainStringKey | undefined>> = {
  slate: "label.hue.slate",
  red: "label.hue.red",
  orange: "label.hue.orange",
  amber: "label.hue.amber",
  lime: "label.hue.lime",
  green: "label.hue.green",
  teal: "label.hue.teal",
  cyan: "label.hue.cyan",
  blue: "label.hue.blue",
  indigo: "label.hue.indigo",
  purple: "label.hue.purple",
  pink: "label.hue.pink",
};

const STEP_KEYS: Readonly<Record<LabelColorStep, PlainStringKey>> = {
  pale: "label.step.pale",
  bold: "label.step.bold",
};

const PROBLEM_KEYS: Readonly<Record<LabelNameProblem | "full", PlainStringKey>> = {
  empty: "label.error.empty",
  tooLong: "label.error.tooLong",
  reserved: "label.error.reserved",
  duplicate: "label.error.duplicate",
  control: "label.error.control",
  full: "label.error.full",
};

/**
 * The three examples the empty state offers (F-30).
 *
 * Three, and these three, because they are the categories a person recognises
 * without thinking: a bill, a trip, a thing you owe someone. The point is not
 * that a user wants exactly these — it is that seeing them answers "what would
 * I even call one", which is the real block a bare "no labels yet" leaves in
 * place.
 */
const EXAMPLE_LABEL_KEYS: readonly PlainStringKey[] = [
  "label.example.invoices",
  "label.example.travel",
  "label.example.followUp",
];

/** One colour per example, so the chips also show what a label looks like. */
const EXAMPLE_COLOR_IDS: readonly string[] = ["amber", "teal-bold", "red"];

const VISIBILITY_KEYS: Readonly<Record<LabelVisibility, PlainStringKey>> = {
  show: "label.visibility.show",
  showIfUnread: "label.visibility.showIfUnread",
  hide: "label.visibility.hide",
};

/* The SAME three words the label list uses, deliberately: one vocabulary. */
const FOLDER_VISIBILITY_KEYS: Readonly<Record<FolderVisibility, PlainStringKey>> = {
  show: "label.visibility.show",
  showIfUnread: "label.visibility.showIfUnread",
  hide: "label.visibility.hide",
};
