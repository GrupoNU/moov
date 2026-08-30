import { useTranslation } from "../../i18n/I18nProvider";
import { labelColorVariables } from "../../mail/labelPalette";
import type { Label } from "../../mail/labelStore";
import { labelStateFor, toggleTargetFor, type LabelSelectionState } from "../../mail/labels";
import { PopupMenu } from "./PopupMenu";
import menuStyles from "./MoveMenu.module.css";
import styles from "./LabelMenu.module.css";

/**
 * "Label as" — Gmail's `l` menu (canon §2.7: "`v` move to · `l` label as").
 *
 * # Why checkboxes and not menu items
 *
 * Moving is exclusive and labelling is not: a conversation lives in ONE folder
 * and carries ANY number of labels. That difference is the whole reason this is
 * a separate component rather than a variant of `MoveMenu`, and it shows up in
 * three places:
 *
 *   - the role is `menuitemcheckbox`, not `menuitem`;
 *   - the menu stays OPEN after a tick, so several labels can be applied in one
 *     visit — closing after each one would make labelling a conversation with
 *     three labels three trips;
 *   - a partially-applied label renders `aria-checked="mixed"`, which is the
 *     real state and the one a `boolean` would have to lie about.
 *
 * # The mixed state, and what a click on it does
 *
 * Gmail's rule, copied (and the same one `resolveToggle` applies to read and
 * starred): if ANY selected conversation lacks the label, the click applies it
 * to all of them. Only when every one already carries it does the click remove
 * it. Toggling each message independently would leave the selection mixed after
 * an explicit click, which is never what anyone meant.
 */

export interface LabelMenuProps {
  readonly labels: readonly Label[];
  /**
   * The keyword maps of the messages the menu acts on.
   *
   * Keyword maps rather than `Email`s, deliberately: the menu needs exactly one
   * property, and passing the whole objects would make it re-render on every
   * unrelated field a refetch changes.
   */
  readonly selection: readonly (Readonly<Record<string, boolean>> | undefined)[];
  readonly disabled: boolean;
  /** Applies (`true`) or removes (`false`) one keyword across the selection. */
  readonly onToggle: (keyword: string, apply: boolean) => void;
  /** Opens the label manager — the only way out when there are no labels yet. */
  readonly onManage: () => void;
  readonly triggerClassName: string | undefined;
  readonly triggerContent: React.ReactNode;
  /** Publishes an `open()` so the `l` shortcut can raise this menu. */
  readonly onReady?: ((open: () => void) => void) | undefined;
}

export function LabelMenu({
  labels,
  selection,
  disabled,
  onToggle,
  onManage,
  triggerClassName,
  triggerContent,
  onReady,
}: LabelMenuProps): React.JSX.Element {
  const { t } = useTranslation();

  return (
    <PopupMenu
      label={t("label.labelAs")}
      disabled={disabled}
      triggerClassName={triggerClassName}
      triggerContent={triggerContent}
      onReady={onReady}
    >
      {(close) => (
        <>
          {labels.map((label, index) => {
            const state = labelStateFor(label.keyword, selection);
            return (
              <li key={label.keyword} role="none">
                <button
                  type="button"
                  role="menuitemcheckbox"
                  aria-checked={ariaChecked(state)}
                  className={styles.item}
                  /* APG: focus must move into the menu on open, or the menu is
                     unusable by keyboard. */
                  // eslint-disable-next-line jsx-a11y/no-autofocus
                  autoFocus={index === 0}
                  onClick={() => {
                    // The menu deliberately does NOT close: labelling is
                    // multi-select by nature.
                    onToggle(label.keyword, toggleTargetFor(state));
                  }}
                >
                  <span
                    className={styles.swatch}
                    style={labelColorVariables(label.colorId)}
                    aria-hidden="true"
                  />
                  <span className={styles.name}>{label.name}</span>
                  {/* The tick is decorative — `aria-checked` above is what a
                      screen reader announces, including the mixed state. */}
                  <span className={styles.mark} aria-hidden="true">
                    {state === "all" ? "✓" : state === "some" ? "–" : ""}
                  </span>
                </button>
              </li>
            );
          })}

          {labels.length === 0 && (
            <li role="none">
              <span className={menuStyles.menuEmpty}>{t("label.none")}</span>
            </li>
          )}

          <li role="none">
            <hr className={styles.separator} />
          </li>
          <li role="none">
            <button
              type="button"
              role="menuitem"
              className={menuStyles.menuItem}
              // With no labels yet this is the only item in the menu, so it
              // takes the focus the APG pattern requires on open.
              // eslint-disable-next-line jsx-a11y/no-autofocus
              autoFocus={labels.length === 0}
              onClick={() => {
                onManage();
                close();
              }}
            >
              {t("label.manage")}
            </button>
          </li>
        </>
      )}
    </PopupMenu>
  );
}

/** The tri-state `aria-checked` value for a label over a selection. */
function ariaChecked(state: LabelSelectionState): boolean | "mixed" {
  switch (state) {
    case "all":
      return true;
    case "some":
      return "mixed";
    case "none":
      return false;
  }
}
