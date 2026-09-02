import { useTranslation } from "../../i18n/I18nProvider";
import { labelColorVariables } from "../../mail/labelPalette";
import type { Label } from "../../mail/labelStore";
import styles from "./LabelList.module.css";

/**
 * The sidebar's "Etiquetas" section (E8).
 *
 * # Why it is a separate list from the folders, and not more of the tree
 *
 * GC-5 draws the line the whole epic is built on: **folders carry the
 * organizational load** and labels are the few cross-cutting flags. Rendering
 * labels as more rows in the mailbox tree would erase exactly that distinction
 * — the user would see fifteen indistinguishable rows and would reasonably
 * expect to file mail into any of them, which a keyword cannot do. A separate,
 * titled group says what these are before the user clicks one.
 *
 * The tree above is an ARIA `tree` because mailboxes nest. Labels do not nest
 * structurally (a `/` in a name is a naming convention, not a hierarchy the
 * server knows about), so this is a plain `list` — the honest role for a flat
 * set, and one less traversal for a screen reader to walk.
 *
 * # The heading is ALWAYS there (canon 07 §2)
 *
 * E8 rendered nothing at all until the first label existed, reasoning that an
 * empty heading is a permanent reminder of an unused feature. Against the
 * benchmark that governs this epic that is the wrong trade, and the owner hit
 * it directly: on an account with no labels the "Etiquetas" section was simply
 * missing, so there was no way to discover labels FROM the place labels live.
 * The feature was reachable only by already knowing it was in Settings.
 *
 * Gmail keeps the heading and its `+` in the rail whether or not any label
 * exists — the header IS the affordance, and a section that appears only once
 * you have used it cannot teach you it is there.
 *
 * The empty state is therefore just the header: no placeholder row, no
 * explanatory paragraph. There is nothing to say that the `+` does not already
 * say, and prose in a rail is noise on every future render.
 *
 * One exception, and it is structural rather than a second opinion: with the
 * rail COLLAPSED to icons the header is hidden (there is no room for a heading
 * or a `+` at 68px). A collapsed rail with zero labels would then be an empty
 * bordered group, so in exactly that combination the section renders nothing.
 */

export interface LabelListProps {
  readonly labels: readonly Label[];
  /** The label currently being viewed, by keyword. */
  readonly selectedKeyword: string | undefined;
  readonly onSelect: (label: Label) => void;
  /**
   * E12: the `+` beside the "Etiquetas" heading (canon 07 §2).
   *
   * It NAVIGATES to the labels settings rather than opening an inline creator,
   * and that is deliberate: the label manager is where the 26-keyword ceiling
   * is explained and where colour and visibility are chosen, and a bare "name
   * it" prompt in the sidebar would create labels that then have to be found
   * and configured somewhere else. Gmail's own `+` opens its label dialog for
   * the same reason.
   *
   * Absent hides the button — a caller with no settings surface wired must not
   * render a control that leads nowhere.
   */
  readonly onCreate?: (() => void) | undefined;
  /** E12: the rail is collapsed to icons; see `MailboxListProps.collapsed`. */
  readonly collapsed?: boolean;
}

export function LabelList({
  labels,
  selectedKeyword,
  onSelect,
  onCreate,
  collapsed = false,
}: LabelListProps): React.JSX.Element | null {
  const { t } = useTranslation();
  /*
   * The ONLY case that renders nothing: collapsed AND empty. The header — the
   * whole point of always rendering — is hidden at icon width, so what would
   * be left is a group with no content and no way to act on it.
   */
  if (labels.length === 0 && collapsed) return null;

  return (
    <div
      className={[styles.group, collapsed ? styles.groupCollapsed : ""]
        .filter(Boolean)
        .join(" ")}
    >
      <div className={styles.header}>
        <h2 className={styles.title} id="sidebar-labels">
          {t("label.plural")}
        </h2>
        {onCreate !== undefined && (
          <button
            type="button"
            className={styles.create}
            onClick={onCreate}
            aria-label={t("label.create")}
            title={t("label.create")}
          >
            <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true" focusable="false">
              <path d="M10 4.5v11M4.5 10h11" />
            </svg>
          </button>
        )}
      </div>
      {/*
        The list is omitted entirely when there are no labels, rather than
        rendered empty. An empty `list` is announced as "list, 0 items", which
        is a statement about a structure the user did not ask about; the header
        alone already says everything the empty state has to say.
      */}
      {labels.length > 0 && (
      <ul className={styles.list} aria-labelledby="sidebar-labels">
        {labels.map((label) => {
          const isSelected = label.keyword === selectedKeyword;
          return (
            <li key={label.keyword}>
              <button
                type="button"
                className={[styles.row, isSelected ? styles.rowActive : ""]
                  .filter(Boolean)
                  .join(" ")}
                aria-current={isSelected ? "page" : undefined}
                onClick={() => {
                  onSelect(label);
                }}
                /* E12: same reason as the folder rows — with the rail
                   collapsed, the name has to stay reachable by a pointer. */
                title={label.name}
              >
                {/*
                  The swatch is the ONLY coloured thing in the row: the row
                  itself is never tinted (canon §4.2 — Gmail never tints), so
                  the selected state stays legible whatever colour the label is.
                */}
                <span
                  className={styles.swatch}
                  style={labelColorVariables(label.colorId)}
                  aria-hidden="true"
                />
                <span className={styles.name}>{label.name}</span>
              </button>
            </li>
          );
        })}
      </ul>
      )}
    </div>
  );
}
