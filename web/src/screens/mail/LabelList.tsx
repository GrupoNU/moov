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
 * # Rendered only when there is something to render
 *
 * An empty "Etiquetas" heading over nothing is a permanent reminder of a
 * feature the user is not using. The section appears with the first label and
 * disappears with the last, and creation lives in Settings where the ceiling
 * can be explained.
 */

export interface LabelListProps {
  readonly labels: readonly Label[];
  /** The label currently being viewed, by keyword. */
  readonly selectedKeyword: string | undefined;
  readonly onSelect: (label: Label) => void;
}

export function LabelList({
  labels,
  selectedKeyword,
  onSelect,
}: LabelListProps): React.JSX.Element | null {
  const { t } = useTranslation();
  if (labels.length === 0) return null;

  return (
    <div className={styles.group}>
      <h2 className={styles.title} id="sidebar-labels">
        {t("label.plural")}
      </h2>
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
    </div>
  );
}
