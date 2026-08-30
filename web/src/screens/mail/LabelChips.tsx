import { useTranslation } from "../../i18n/I18nProvider";
import { labelColorVariables } from "../../mail/labelPalette";
import type { Label } from "../../mail/labelStore";
import styles from "./LabelChips.module.css";

/**
 * The label chips on a list row and in the reader header (E8, canon §4.2).
 *
 * # Chips, and NEVER a row tint
 *
 * "Chips on rows — Gmail never tints rows" (canon §2.6). The rule is not
 * aesthetic. A tinted row encodes one label in a property the row already uses
 * for state — unread, selected, focused — so a starred unread message in a red
 * label has three colours competing for the same pixels and the user can read
 * none of them. A chip is additive: it takes its own space, it can be plural,
 * and it leaves the row's own states legible. This component therefore renders
 * INTO a row and never styles the row.
 *
 * # The overflow, and why the count is not a lie
 *
 * Three chips at most, then "+N". Not because three is magic, but because a row
 * is one line and a fourth chip pushes the subject out of it — and the subject
 * is what the row is for. The `+N` counts what is HIDDEN (`total - shown`),
 * which the `title` spells out in full so nothing is unreachable: a user who
 * needs the fourth label hovers, or opens the conversation, where the reader
 * shows them all.
 */

/** How many chips a row shows before collapsing the rest into "+N". */
export const MAX_ROW_CHIPS = 3;

export interface LabelChipsProps {
  readonly labels: readonly Label[];
  /**
   * How many to show before the overflow marker. Defaults to
   * {@link MAX_ROW_CHIPS}; the reader passes `Infinity` because it has room and
   * because the open conversation is exactly where the full set belongs.
   */
  readonly max?: number;
  /** Called when a chip is clicked — navigates to that label's view. */
  readonly onSelect?: ((label: Label) => void) | undefined;
}

export function LabelChips({
  labels,
  max = MAX_ROW_CHIPS,
  onSelect,
}: LabelChipsProps): React.JSX.Element | null {
  const { t, format } = useTranslation();
  if (labels.length === 0) return null;

  const shown = labels.slice(0, max);
  const hidden = labels.length - shown.length;

  return (
    <span className={styles.chips}>
      {shown.map((label) =>
        onSelect === undefined ? (
          <span
            key={label.keyword}
            className={styles.chip}
            style={labelColorVariables(label.colorId)}
          >
            {label.name}
          </span>
        ) : (
          <button
            key={label.keyword}
            type="button"
            className={`${styles.chip} ${styles.clickable}`}
            style={labelColorVariables(label.colorId)}
            title={format("label.openLabel", label.name)}
            onClick={(event) => {
              /*
               * Stopped: a chip lives inside a row whose own click opens the
               * conversation. Without this, clicking a chip would navigate to
               * the label AND open a message — two destinations from one click.
               */
              event.stopPropagation();
              onSelect(label);
            }}
          >
            {label.name}
          </button>
        ),
      )}
      {hidden > 0 && (
        <span
          className={styles.overflow}
          title={labels
            .slice(max)
            .map((label) => label.name)
            .join(", ")}
        >
          {format("label.more", hidden)}
        </span>
      )}
      {/* The chips are visual shorthand; a screen reader gets the full list as
          one phrase rather than as N unlabelled fragments. */}
      <span className="visually-hidden">
        {`${t("label.plural")}: ${labels.map((label) => label.name).join(", ")}`}
      </span>
    </span>
  );
}
