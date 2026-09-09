import { useTranslation } from "../../i18n/I18nProvider";
import type { SelectionScope } from "../../keyboard/shortcuts";
import {
  hasNextPage,
  hasPreviousPage,
  pageLabel,
  type PageState,
} from "../../mail/paging";
import { PopupMenu } from "./PopupMenu";
import styles from "./ListToolbar.module.css";

/**
 * The row above the message list (E12/B4, canon 07 §3).
 *
 * # What it is, and why it is not the ActionBar
 *
 * Gmail has TWO strips over its list and they do different jobs. The one this
 * component draws is the CHROME: a select-all checkbox with its scope dropdown,
 * a refresh, an overflow `⋮`, and — right-aligned — the pager. None of it acts
 * on a selection; all of it acts on the LIST.
 *
 * `ActionBar` is the other one: archive, delete, spam, labels, snooze — the
 * verbs, every one of which needs a selection and disables without it. Merging
 * the two would have produced a strip where half the controls grey out and half
 * do not, which is the layout that makes people stop reading a toolbar.
 *
 * # The pager is the only thing here that can lie, so it is the only thing here
 * with real logic
 *
 * Everything else is a callback. The pager's arrows and its sentence come from
 * `mail/paging.ts`, which is pure and tested — including the two facts that
 * make it delicate: the server's `total` is exact or ABSENT (never an
 * estimate), and its reach ceiling is real, so "older" must disappear at the
 * wall rather than page into a permanently empty list.
 */

export interface ListToolbarProps {
  /**
   * Selects by scope — Gmail's six dropdown options.
   *
   * This is the SAME callback the `* a`/`* n`/`* r`/`* u`/`* s`/`* t` chords
   * resolve to (canon §2.4), so the menu is a second surface over one reducer
   * rather than a second implementation of the same six rules. A menu that
   * disagreed with the keyboard about what "unread" selects would be the worst
   * kind of bug: invisible until someone used both.
   */
  readonly onSelectBy: (scope: SelectionScope) => void;
  /** The plain checkbox's two states — the dropdown covers the other four. */
  readonly allSelected: boolean;
  readonly someSelected: boolean;
  /** Rows on the current page; zero disables the checkbox and the menu. */
  readonly totalCount: number;
  readonly onRefresh: () => void;
  readonly isRefreshing: boolean;

  /**
   * The pager's state, or absent to draw no pager at all.
   *
   * Absent for the views that genuinely do not page: the Outbox is a local
   * queue, and Scheduled lists submissions. A pager over either would be
   * chrome describing a thing that does not exist.
   */
  readonly page?: PageState | undefined;
  readonly onNewerPage?: (() => void) | undefined;
  readonly onOlderPage?: (() => void) | undefined;

  /**
   * The `⋮` overflow's contents, rendered by the caller.
   *
   * A render prop for the same reason `MessageList` takes one for snooze: the
   * bulk actions behind it need the dispatcher, the mailbox list and the
   * confirm dialog, none of which this component has or should acquire to draw
   * a strip.
   */
  readonly renderOverflow?: ((close: () => void) => React.ReactNode) | undefined;
}

/** The six scopes, with their labels, in Gmail's own order. */
const SCOPES: readonly {
  readonly scope: SelectionScope;
  readonly key:
    | "action.select.all"
    | "action.select.none"
    | "action.select.read"
    | "action.select.unread"
    | "action.select.starred"
    | "action.select.unstarred";
}[] = [
  { scope: "all", key: "action.select.all" },
  { scope: "none", key: "action.select.none" },
  { scope: "read", key: "action.select.read" },
  { scope: "unread", key: "action.select.unread" },
  { scope: "starred", key: "action.select.starred" },
  { scope: "unstarred", key: "action.select.unstarred" },
];

export function ListToolbar({
  onSelectBy,
  allSelected,
  someSelected,
  totalCount,
  onRefresh,
  isRefreshing,
  page,
  onNewerPage,
  onOlderPage,
  renderOverflow,
}: ListToolbarProps): React.JSX.Element {
  const { t, format } = useTranslation();
  const isEmpty = totalCount === 0;

  const label =
    page === undefined
      ? undefined
      : pageLabel(
          page,
          (first, last, total) => format("list.page.range", first, last, total),
          (first, last) => format("list.page.rangeUnknown", first, last),
          /*
           * B-01: the bound the client can assert when the server declined to
           * count — "1–50 de más de 50". `pageBound` decides WHICH of the three
           * sentences is true; this only supplies the wording for one of them.
           */
          (first, last, atLeast) =>
            format("list.page.rangeAtLeast", first, last, atLeast),
        );

  return (
    <div className={styles.bar}>
      {/*
        The checkbox and its dropdown are ONE control visually and TWO in the
        accessibility tree, which is the right split: the box toggles (a
        checkbox), the caret opens a menu (a menu button). Fusing them into one
        widget would leave a screen-reader user with a control whose role
        cannot describe both behaviours.
      */}
      <div className={styles.selectGroup}>
        <label className={styles.selectAll}>
          <input
            type="checkbox"
            checked={allSelected && !isEmpty}
            /* The indeterminate box is the honest state for "some but not
               all"; a plain unchecked box would claim nothing is selected. */
            ref={(node) => {
              if (node !== null) node.indeterminate = someSelected && !allSelected;
            }}
            disabled={isEmpty}
            onChange={(event) => {
              onSelectBy(event.target.checked ? "all" : "none");
            }}
          />
          <span className="visually-hidden">{t("action.selectAll")}</span>
        </label>

        <PopupMenu
          label={t("action.selectMenu")}
          disabled={isEmpty}
          triggerClassName={styles.caret}
          triggerContent={
            <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
              <path d="M6 8.5l4 4 4-4" />
            </svg>
          }
        >
          {(close) => (
            <>
              {SCOPES.map(({ scope, key }) => (
                <button
                  key={scope}
                  type="button"
                  role="menuitem"
                  className={styles.menuItem}
                  onClick={() => {
                    onSelectBy(scope);
                    // A scope choice CLOSES: unlike the label menu (where
                    // ticking several in one visit is the point), picking a
                    // second scope replaces the first, so leaving the menu open
                    // would invite a click that undoes the previous one.
                    close();
                  }}
                >
                  {t(key)}
                </button>
              ))}
            </>
          )}
        </PopupMenu>
      </div>

      <button
        type="button"
        className={styles.iconButton}
        onClick={onRefresh}
        aria-label={t("list.refresh")}
        title={t("list.refresh")}
        /* Disabled WHILE refreshing, not merely spun: a second refresh in
           flight would race the first and could paint the older answer last. */
        disabled={isRefreshing}
      >
        <svg
          className={isRefreshing ? styles.spinning : undefined}
          viewBox="0 0 20 20"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.6"
          strokeLinecap="round"
          strokeLinejoin="round"
          aria-hidden="true"
          focusable="false"
        >
          <path d="M16.4 8.4a6.6 6.6 0 1 0 .3 3.4" />
          <path d="M16.8 4.4v4.2h-4.2" />
        </svg>
      </button>

      {renderOverflow !== undefined && (
        <PopupMenu
          label={t("action.more")}
          disabled={false}
          triggerClassName={styles.iconButton}
          triggerContent={
            <svg viewBox="0 0 20 20" fill="currentColor" aria-hidden="true" focusable="false">
              <circle cx="10" cy="4.6" r="1.5" />
              <circle cx="10" cy="10" r="1.5" />
              <circle cx="10" cy="15.4" r="1.5" />
            </svg>
          }
        >
          {(close) => renderOverflow(close)}
        </PopupMenu>
      )}

      {/*
        The pager, right-aligned — Gmail's placement, and the only piece of
        this strip that is INFORMATION rather than a control. It is the one
        place in the product that says how much mail a folder holds.
      */}
      {label !== undefined && (
        <div className={styles.pager}>
          {/*
            A live region, because the range changes as a RESULT of the arrows
            rather than as a label on them: without it a screen-reader user
            presses "older" and hears nothing, with no way to know whether the
            page moved. `polite`, so it waits for a pause rather than
            interrupting the row the user may be reading.
          */}
          <span className={styles.range} aria-live="polite">
            {label}
          </span>
          <button
            type="button"
            className={styles.iconButton}
            onClick={onNewerPage}
            aria-label={t("list.page.newer")}
            title={t("list.page.newer")}
            disabled={onNewerPage === undefined || page === undefined || !hasPreviousPage(page)}
          >
            <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
              <path d="M12 4.5L6.5 10l5.5 5.5" />
            </svg>
          </button>
          <button
            type="button"
            className={styles.iconButton}
            onClick={onOlderPage}
            aria-label={t("list.page.older")}
            title={t("list.page.older")}
            /*
             * This is the arrow that must not over-promise. `hasNextPage`
             * refuses it for a short page, for a total that says we are at the
             * end, AND at the server's reach ceiling — where an arrow would
             * page the user into a permanently empty list that looks like lost
             * mail rather than like a boundary.
             */
            disabled={onOlderPage === undefined || page === undefined || !hasNextPage(page)}
          >
            <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
              <path d="M8 4.5l5.5 5.5L8 15.5" />
            </svg>
          </button>
        </div>
      )}
    </div>
  );
}
