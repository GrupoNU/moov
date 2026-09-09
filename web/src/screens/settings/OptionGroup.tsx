import { useId } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import type { PlainStringKey } from "./registry";
import styles from "./OptionGroup.module.css";

/**
 * The radio group both settings surfaces render (review F-26, F-34, F-35).
 *
 * # Why this exists as ONE component used by two screens
 *
 * The quick panel had radios with thumbnails; the settings page had a
 * `<select>` over the same three values. So "Panel de lectura" was a picture
 * you could compare in the dock and a one-line dropdown on the page — two
 * different answers to the same question, and the page's was the worse one:
 * a collapsed select shows ONE option, which is exactly the wrong shape for a
 * choice whose whole difficulty is telling three similar options apart.
 *
 * Extracting the group rather than copying it is what makes that permanent.
 * The two surfaces cannot drift into different labels, different orders or
 * different pictures, because there is one implementation and one set of
 * thumbnails ({@link ./QuickThumbnails}) behind both.
 *
 * # Why radios and not a select, restated because it is the finding
 *
 * Gmail uses radios with an inline explanation for its two- and three-option
 * settings and reserves `<select>` for long lists (the language list, the
 * undo-send seconds). A select hides every option but one behind a click, which
 * is the right trade for twenty and the wrong one for three: with three, the
 * comparison IS the decision, and hiding two of them to save a line of vertical
 * space costs the user the only thing that would let them choose.
 *
 * The keyboard behaviour — arrows move within the group, the group is ONE tab
 * stop, focus enters at the checked option — comes from the browser's native
 * radio group and is subtly wrong in every hand-rolled version.
 *
 * # Two densities, one component
 *
 * `variant="thumb"` is the quick panel's: label left, 48×32 preview pushed to
 * the right edge so the three pictures line up in a column the eye can scan.
 * `variant="inline"` is the settings page's for options with no picture: the
 * radio, the label, and a grey sentence beside it saying what the option does
 * — Gmail's own shape for "Comportamiento de respuesta" and "Avance
 * automático".
 */

export interface OptionGroupProps<T extends string> {
  /** Names the group for a screen reader; also the visible legend when shown. */
  readonly legendKey: PlainStringKey;
  /**
   * False hides the legend visually but keeps it as the group's name.
   *
   * The settings page's rows already carry the setting's name in their left
   * column, so a visible legend there would print it twice — the same
   * duplicate F-47 removed one level up.
   */
  readonly showLegend?: boolean;
  readonly value: T;
  readonly options: readonly T[];
  readonly labelKey: (option: T) => PlainStringKey;
  /** An option's one-line explanation, rendered beside its label (F-26). */
  readonly describeKey?: ((option: T) => PlainStringKey | undefined) | undefined;
  readonly onChange: (next: T) => void;
  /** The preview beside an option, when the choice is about how something LOOKS. */
  readonly renderThumb?: ((option: T) => React.ReactNode) | undefined;
  readonly variant?: "thumb" | "inline";
}

export function OptionGroup<T extends string>({
  legendKey,
  showLegend = true,
  value,
  options,
  labelKey,
  describeKey,
  onChange,
  renderThumb,
  variant = "thumb",
}: OptionGroupProps<T>): React.JSX.Element {
  const { t } = useTranslation();
  /*
   * The radio NAME is per-instance, so two groups over the same values on one
   * screen (the quick panel's reading pane and the page's, if both were ever
   * mounted) do not become one group that unchecks the other.
   */
  const groupName = useId();

  return (
    <fieldset className={variant === "thumb" ? styles.group : styles.groupInline}>
      <legend className={showLegend ? styles.legend : "visually-hidden"}>
        {t(legendKey)}
      </legend>
      {options.map((option) => {
        const description = describeKey?.(option);
        const noteId = `${groupName}-${option}-note`;
        return (
          /*
           * The row is a `<div>` and the `<label>` wraps only the radio and its
           * NAME — the explanation sits outside it, as a sibling.
           *
           * Kept inside the label, the sentence becomes part of the radio's
           * accessible name: a screen reader would announce "Reply answers the
           * sender only, radio button, 1 of 2", a name that no longer matches
           * the words a user would say to a colleague. `aria-describedby` puts
           * it where a description belongs — announced after the name, at the
           * verbosity the user chose.
           *
           * Clicking the sentence therefore does not select the option, which
           * is the honest trade: it is prose ABOUT the option, and a paragraph
           * that turns out to be a button is its own small surprise.
           */
          <div
            key={option}
            className={variant === "thumb" ? styles.option : styles.optionInline}
          >
            <label className={styles.optionMain}>
              <input
                type="radio"
                className={styles.radio}
                name={groupName}
                value={option}
                checked={value === option}
                {...(description !== undefined ? { "aria-describedby": noteId } : {})}
                onChange={() => {
                  onChange(option);
                }}
              />
              <span className={styles.optionLabel}>{t(labelKey(option))}</span>
              {renderThumb?.(option)}
            </label>
            {description !== undefined && (
              <span className={styles.optionNote} id={noteId}>
                {t(description)}
              </span>
            )}
          </div>
        );
      })}
    </fieldset>
  );
}
