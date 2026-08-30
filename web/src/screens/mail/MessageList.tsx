import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import { formatListDate, initialsFor, machineDate } from "../../mail/format";
import { usePrefs } from "../../mail/PrefsProvider";
import { labelsFor, type Label } from "../../mail/labelStore";
import { rowHeightFor } from "../../mail/prefs";
import { displaySubject, senderLabel, type ThreadGroup } from "../../mail/threading";
import { computeWindow, scrollOffsetToReveal, totalHeight } from "../../mail/windowing";
import { LabelChips } from "./LabelChips";
import styles from "./MessageList.module.css";

/**
 * A stable empty array for the labels prop.
 *
 * A fresh `[]` at the call site would be a new reference on every render, which
 * would defeat any memo a row grows later — and this list renders 200 rows.
 */
const EMPTY_LABELS: readonly Label[] = [];

/**
 * The virtualized message list (P2 deliverable 3) — the biggest piece.
 *
 * # How the virtualization works
 *
 * One scroll container of the full content height (`itemCount * ROW_HEIGHT`),
 * containing two spacer divs and only the visible slice of rows between them.
 * The maths is in `mail/windowing.ts` and unit-tested there, including the
 * invariant that the spacers plus the rendered rows always equal the total —
 * which is what keeps the scrollbar honest.
 *
 * Rows are absolutely positioned by `transform: translateY` rather than laid
 * out in flow, so that changing the window does not trigger layout for the
 * rows that did not move.
 *
 * # Accessibility: a grid, not a list of links
 *
 * The rows are a `grid` with `row` and `gridcell` children. That is the ARIA
 * pattern for a table-shaped list with multiple columns per row, and it lets a
 * screen reader announce "sender, subject, date" as one row rather than as
 * three unrelated strings.
 *
 * Virtualization breaks the usual assumption that every row is in the DOM, so
 * `aria-rowcount` (the TRUE total) and `aria-rowindex` (each row's true
 * position) are set explicitly. Without them, a screen reader announces "row 3
 * of 20" while the user is at message 400 — the single most common
 * accessibility failure in virtualized lists.
 *
 * Focus follows selection with a roving tabindex: exactly one row is tabbable,
 * so Tab moves past the list rather than through 200 rows.
 */

export interface MessageListProps {
  readonly groups: readonly ThreadGroup[];
  readonly selectedId: string | undefined;
  readonly onSelect: (group: ThreadGroup) => void;
  readonly onOpen: (group: ThreadGroup) => void;
  readonly isLoading: boolean;
  /** Rendered when the list is empty and not loading. */
  readonly empty: React.ReactNode;
  /** Shown above the list when the server's window truncated the results. */
  readonly notice?: React.ReactNode;
  /** Identifies the current list, so a folder change resets the scroll. */
  readonly listKey: string;
  /** P3: the multi-selected rows (independent of the focused row). */
  readonly selectedIds?: ReadonlySet<string>;
  /** P3: a click on a row's checkbox, or on the row with a modifier held. */
  readonly onToggleSelect?: (
    group: ThreadGroup,
    modifiers: { readonly toggle: boolean; readonly range: boolean },
  ) => void;

  /*
   * E2 item 5: the hover actions.
   *
   * Gmail ships FOUR — archive, delete, snooze, mark-read. Snooze is epic E4's
   * (it needs the Snoozed mailbox and the engine's return-to-inbox job, per
   * GC-10), and a dead fourth button that greys out or does nothing would be
   * worse than three that work. E4 adds it here as a fourth prop.
   *
   * Each acts on the row it sits in, never on the selection: the pointer has
   * already named its target.
   */
  readonly onRowArchive?: (group: ThreadGroup) => void;
  readonly onRowDelete?: (group: ThreadGroup) => void;
  readonly onRowToggleRead?: (group: ThreadGroup) => void;

  /**
   * E8: the labels a row may carry, so each row can resolve its own chips.
   *
   * The whole KNOWN set is passed once rather than per-row chips being computed
   * upstream: a row renders its chips from its own keywords, and passing the
   * lookup table keeps that a pure function of data the row already has.
   */
  readonly labels?: readonly Label[];
  /** Clicking a chip navigates to that label's view. */
  readonly onSelectLabel?: (label: Label) => void;
}

export function MessageList({
  groups,
  selectedId,
  onSelect,
  onOpen,
  isLoading,
  empty,
  notice,
  listKey,
  selectedIds,
  onToggleSelect,
  onRowArchive,
  onRowDelete,
  onRowToggleRead,
  labels,
  onSelectLabel,
}: MessageListProps): React.JSX.Element {
  const { t, locale } = useTranslation();
  const { prefs } = usePrefs();
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const [scrollTop, setScrollTop] = useState(0);
  const [viewportHeight, setViewportHeight] = useState(600);

  /*
   * E5: the row height is DERIVED from the density preference, not a constant.
   *
   * This is the number the virtualizer divides by AND the number the
   * stylesheet draws with (`--row-height`, stamped on the root by
   * MailScreen from the same source). If the two ever disagree the list drifts
   * away from its scrollbar — the defect `rowHeight.test.ts` exists for — so
   * both sides read `rowHeightFor(density)` and neither has its own copy.
   */
  const rowHeight = rowHeightFor(prefs.density);

  // `now` is captured once per render pass rather than per row, so 200 rows do
  // not construct 200 Dates, and so every row in one paint agrees about what
  // "today" means.
  const now = useMemo(() => new Date(), []);

  /*
   * Scroll position resets when the LIST changes, not when its contents do.
   *
   * This is the "stable scroll position across data updates" requirement: a
   * refresh that adds a message to the current folder must NOT jump the user
   * to the top, while switching folders must not leave them scrolled to
   * message 400 of a folder with six. Keying on `listKey` distinguishes the
   * two, and `useLayoutEffect` runs the reset before paint so no frame is
   * drawn at the stale offset.
   */
  useLayoutEffect(() => {
    const element = scrollRef.current;
    if (element !== null) element.scrollTop = 0;
    setScrollTop(0);
  }, [listKey]);

  useEffect(() => {
    const element = scrollRef.current;
    if (element === null) return undefined;

    const measure = (): void => {
      setViewportHeight(element.clientHeight);
    };
    measure();

    // ResizeObserver rather than a window resize listener: the list also
    // changes height when the reading pane opens beside it, which no window
    // event reports.
    if (typeof ResizeObserver === "undefined") return undefined;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => {
      observer.disconnect();
    };
  }, []);

  const handleScroll = useCallback((event: React.UIEvent<HTMLDivElement>): void => {
    // Read synchronously from the event target. Deferring to rAF here would
    // render the window one frame behind the scroll, which is exactly the
    // blank-space-while-scrolling artefact overscan exists to hide.
    setScrollTop(event.currentTarget.scrollTop);
  }, []);

  const range = useMemo(
    () =>
      computeWindow({
        scrollTop,
        viewportHeight,
        itemCount: groups.length,
        rowHeight,
      }),
    [scrollTop, viewportHeight, groups.length, rowHeight],
  );

  // Keep the selected row in view when selection moves by keyboard.
  useEffect(() => {
    if (selectedId === undefined) return;
    const element = scrollRef.current;
    if (element === null) return;
    const index = groups.findIndex((group) => group.id === selectedId);
    if (index < 0) return;
    const offset = scrollOffsetToReveal(
      index,
      element.scrollTop,
      element.clientHeight,
      rowHeight,
    );
    if (offset !== undefined) element.scrollTop = offset;
  }, [selectedId, groups, rowHeight]);

  const selectedIndex = useMemo(
    () => groups.findIndex((group) => group.id === selectedId),
    [groups, selectedId],
  );

  const visible = groups.slice(range.start, range.end);
  const isEmpty = groups.length === 0 && !isLoading;

  return (
    <div className={styles.container}>
      {notice}
      <div
        className={styles.scroller}
        ref={scrollRef}
        onScroll={handleScroll}
        // The scroll container is the region a screen-reader user lands in.
        aria-busy={isLoading}
      >
        {isEmpty ? (
          empty
        ) : (
          <div
            role="grid"
            aria-label={t("list.label")}
            /* The TRUE total, not the rendered count — this is what makes a
             * virtualized list announce "row 400 of 626" correctly. */
            aria-rowcount={groups.length}
            className={styles.grid}
            style={{ height: `${totalHeight(groups.length, rowHeight)}px` }}
          >
            {visible.map((group, offset) => {
              const index = range.start + offset;
              return (
                <MessageRow
                  key={group.id}
                  group={group}
                  index={index}
                  isSelected={group.id === selectedId}
                  isTabbable={
                    // Roving tabindex: the selected row, or the first row when
                    // nothing is selected, so Tab enters the list once.
                    selectedIndex >= 0 ? index === selectedIndex : index === 0
                  }
                  top={index * rowHeight}
                  locale={locale}
                  now={now}
                  onSelect={onSelect}
                  onOpen={onOpen}
                  isChecked={selectedIds?.has(group.id) === true}
                  onToggleSelect={onToggleSelect}
                  /*
                   * E5: hover actions are gated by their preference (Gmail's
                   * single "Disable hover actions" setting, canon §2.2). The
                   * props are dropped entirely rather than passed with the
                   * buttons hidden by CSS — the cell is not rendered at all,
                   * so there is nothing for Tab to reach and no invisible
                   * control in the accessibility tree.
                   */
                  onRowArchive={prefs.hoverActions ? onRowArchive : undefined}
                  onRowDelete={prefs.hoverActions ? onRowDelete : undefined}
                  onRowToggleRead={prefs.hoverActions ? onRowToggleRead : undefined}
                  showSnippet={prefs.showSnippets}
                  labels={labels ?? EMPTY_LABELS}
                  onSelectLabel={onSelectLabel}
                />
              );
            })}
          </div>
        )}

        {isLoading && groups.length === 0 && <ListSkeleton />}
      </div>
    </div>
  );
}

interface MessageRowProps {
  readonly group: ThreadGroup;
  readonly index: number;
  readonly isSelected: boolean;
  readonly isTabbable: boolean;
  readonly top: number;
  readonly locale: string;
  readonly now: Date;
  readonly onSelect: (group: ThreadGroup) => void;
  readonly onOpen: (group: ThreadGroup) => void;
  readonly isChecked: boolean;
  readonly onToggleSelect:
    | ((
        group: ThreadGroup,
        modifiers: { readonly toggle: boolean; readonly range: boolean },
      ) => void)
    | undefined;
  readonly onRowArchive: ((group: ThreadGroup) => void) | undefined;
  readonly onRowDelete: ((group: ThreadGroup) => void) | undefined;
  readonly onRowToggleRead: ((group: ThreadGroup) => void) | undefined;
  /** E5: the `showSnippets` preference — the preview line next to the subject. */
  readonly showSnippet: boolean;
  /** E8: the known labels, for resolving this row's chips and their colours. */
  readonly labels: readonly Label[];
  readonly onSelectLabel: ((label: Label) => void) | undefined;
}

function MessageRow({
  group,
  index,
  isSelected,
  isTabbable,
  top,
  locale,
  now,
  onSelect,
  onOpen,
  isChecked,
  onToggleSelect,
  onRowArchive,
  onRowDelete,
  onRowToggleRead,
  showSnippet,
  labels,
  onSelectLabel,
}: MessageRowProps): React.JSX.Element {
  const { t, format } = useTranslation();
  const rowRef = useRef<HTMLDivElement | null>(null);
  const { latest } = group;

  // A thread shows its participants; a single message shows its sender.
  const sender =
    group.size > 1 && group.participants.length > 1
      ? group.participants.join(", ")
      : senderLabel(latest) ?? t("list.unknownSender");
  const subject = displaySubject(latest.subject) ?? t("list.noSubject");
  // E5: the snippet is dropped from the DOM when the preference is off, not
  // hidden — a screen reader must not read a preview the sighted user turned
  // off, and the row's own text is what the setting is about.
  const preview = showSnippet ? (latest.preview ?? "") : "";

  /*
   * E8: the row's chips.
   *
   * Read off the NEWEST message of the thread, the same one whose subject and
   * date the row shows. A thread whose messages carry different labels is real
   * but rare, and a union across the thread would make a row claim a label the
   * conversation's current state does not have — the row already commits to
   * "the latest message represents this conversation", and the chips follow
   * that same rule rather than inventing a second one.
   */
  const rowLabels = labelsFor(latest.keywords, labels);
  const date = formatListDate(latest.receivedAt, locale, now);
  const isoDate = machineDate(latest.receivedAt);

  // The focus follows selection, but ONLY when the row is already the active
  // element's list — never steal focus from the search box.
  useEffect(() => {
    if (!isSelected) return;
    const element = rowRef.current;
    if (element === null) return;
    const active = document.activeElement;
    const insideList = active !== null && element.parentElement?.contains(active) === true;
    if (insideList && active !== element) element.focus();
  }, [isSelected]);

  return (
    <div
      ref={rowRef}
      role="row"
      aria-rowindex={index + 1}
      aria-selected={isSelected}
      tabIndex={isTabbable ? 0 : -1}
      className={[
        styles.row,
        isSelected ? styles.selected : "",
        isChecked ? styles.checked : "",
        group.hasUnread ? styles.unread : "",
      ]
        .filter(Boolean)
        .join(" ")}
      style={{ transform: `translateY(${top}px)` }}
      onClick={(event) => {
        /*
         * A modified click is a SELECTION gesture, not a navigation one:
         * ctrl/cmd toggles the row and shift extends the range from the
         * anchor. Opening the message as well would replace the reading pane
         * on every click of a multi-select, which is exactly what makes
         * "select five and archive them" impossible in a client that gets
         * this wrong.
         */
        const toggle = event.ctrlKey || event.metaKey;
        const range = event.shiftKey;
        if ((toggle || range) && onToggleSelect !== undefined) {
          event.preventDefault();
          onToggleSelect(group, { toggle, range });
          return;
        }
        onSelect(group);
        onOpen(group);
      }}
      onKeyDown={(event) => {
        // Enter and Space activate, which is what a `row` with an action must
        // do; j/k and the rest are handled globally so they work from anywhere.
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          onSelect(group);
          onOpen(group);
        }
      }}
      onFocus={() => {
        // Focusing a row (by Tab or by click) selects it, so the visible
        // selection and the focus ring never disagree.
        if (!isSelected) onSelect(group);
      }}
    >
      {onToggleSelect !== undefined && (
        <span role="gridcell" className={styles.checkboxCell}>
          <input
            type="checkbox"
            className={styles.checkbox}
            checked={isChecked}
            aria-label={t("action.selectRow")}
            /* The checkbox is its own control with its own accessible name;
               stopping propagation keeps a click on it from ALSO opening the
               message, which would make the box unusable. */
            onClick={(event) => {
              event.stopPropagation();
            }}
            onChange={(event) => {
              onToggleSelect(group, {
                toggle: true,
                range: (event.nativeEvent as MouseEvent).shiftKey,
              });
            }}
            /* Enter/Space on the row must not reach the checkbox and vice
               versa; the row handles its own keys. */
            onKeyDown={(event) => {
              event.stopPropagation();
            }}
          />
        </span>
      )}

      <span className={styles.avatar} aria-hidden="true">
        {initialsFor(senderLabel(latest))}
      </span>

      <span role="gridcell" className={styles.sender}>
        {/* The unread dot is decorative; the row's state is announced by the
            visually-hidden text below, so it is not colour-only information. */}
        {group.hasUnread && <span className={styles.unreadDot} aria-hidden="true" />}
        <span className={styles.senderText}>{sender}</span>
        {/*
          E1: the conversation's size.

          The TOOLTIP depends on where the number came from. With server-side
          collapse it is the thread's real total (`Thread/get` rode the list's
          batch) and may be stated as a fact. On the client-grouped fallback it
          counts only the messages inside the fetched window, so the wording
          says so — a "3" that silently means "3 of maybe 24" is the kind of
          small lie that makes a whole list untrustworthy.
        */}
        {group.size > 1 && (
          <span
            className={styles.threadCount}
            title={
              group.sizeIsExact
                ? format("list.threadSize", group.size)
                : format("list.threadSizeInWindow", group.size)
            }
          >
            {group.size}
          </span>
        )}
      </span>

      <span role="gridcell" className={styles.body}>
        {/*
          E8: the chips come BEFORE the subject, which is where Gmail puts them
          and is the only position that works: after the subject they would be
          pushed off the row by any long subject and never seen, which is the
          same as not rendering them. The row itself is never tinted (canon
          §4.2) — the chips carry the colour and the row keeps its own states.
        */}
        <LabelChips labels={rowLabels} onSelect={onSelectLabel} />
        <span className={styles.subject}>{subject}</span>
        {preview !== "" && (
          <>
            <span className={styles.separator} aria-hidden="true">
              —
            </span>
            <span className={styles.preview}>{preview}</span>
          </>
        )}
      </span>

      {/*
        E2 item 5: the hover actions.

        They live in a `gridcell` of their own so the grid semantics stay
        valid, and they are FOCUSABLE (`tabIndex={0}` — not the row's roving
        -1), so a keyboard user reaches them with Tab from the focused row
        instead of them being a mouse-only feature. The cell is present in the
        DOM at all times and revealed by CSS on hover/focus-within: rendering
        it conditionally on a hover state would re-mount three buttons on every
        pointer move across a virtualized list.

        Every handler stops propagation — without it the click would also open
        the message, and "archive" would archive-then-open.
      */}
      {(onRowArchive !== undefined ||
        onRowDelete !== undefined ||
        onRowToggleRead !== undefined) && (
        <span role="gridcell" className={styles.hoverActions}>
          {onRowArchive !== undefined && (
            <RowAction
              label={t("action.archive")}
              onActivate={() => {
                onRowArchive(group);
              }}
            >
              <path d="M2.6 3.6h14.8v3.6H2.6z" />
              <path d="M4 7.2v8a1.4 1.4 0 0 0 1.4 1.4h9.2a1.4 1.4 0 0 0 1.4-1.4v-8M8 10.4h4" />
            </RowAction>
          )}
          {onRowDelete !== undefined && (
            <RowAction
              label={t("action.delete")}
              onActivate={() => {
                onRowDelete(group);
              }}
            >
              <path d="M3.6 5.6h12.8M8 5.6V4.2a1 1 0 0 1 1-1h2a1 1 0 0 1 1 1v1.4M5.4 5.6l.7 10a1.4 1.4 0 0 0 1.4 1.3h5a1.4 1.4 0 0 0 1.4-1.3l.7-10" />
            </RowAction>
          )}
          {onRowToggleRead !== undefined && (
            <RowAction
              /* The label says what the CLICK will do, which depends on the
                 row's own state — "Mark as read" on an unread row. */
              label={group.hasUnread ? t("action.markRead") : t("action.markUnread")}
              onActivate={() => {
                onRowToggleRead(group);
              }}
            >
              {group.hasUnread ? (
                <>
                  <path d="M2.8 6.2l7.2 5 7.2-5" />
                  <rect x="2.8" y="4.5" width="14.4" height="11" rx="1.6" />
                </>
              ) : (
                <>
                  <rect x="2.8" y="4.5" width="14.4" height="11" rx="1.6" />
                  <circle cx="15.4" cy="5.6" r="2.6" fill="currentColor" stroke="none" />
                </>
              )}
            </RowAction>
          )}
        </span>
      )}

      <span role="gridcell" className={styles.meta}>
        {group.hasFlagged && (
          <svg
            className={styles.flagIcon}
            viewBox="0 0 20 20"
            aria-hidden="true"
            focusable="false"
          >
            <path
              d="M10 2.6l2.3 4.7 5.2.8-3.8 3.7.9 5.2-4.6-2.4-4.6 2.4.9-5.2L2.5 8.1l5.2-.8z"
              fill="currentColor"
            />
          </svg>
        )}
        {group.hasAttachment && (
          <svg
            className={styles.attachIcon}
            viewBox="0 0 20 20"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.6"
            strokeLinecap="round"
            aria-hidden="true"
            focusable="false"
          >
            <path d="M14.5 9.2l-5 5a3.1 3.1 0 0 1-4.4-4.4l6-6a2.1 2.1 0 1 1 3 3l-6 6a1.1 1.1 0 0 1-1.5-1.5l5.3-5.3" />
          </svg>
        )}
        {isoDate !== undefined ? (
          <time className={styles.date} dateTime={isoDate}>
            {date}
          </time>
        ) : (
          <span className={styles.date}>{date}</span>
        )}
      </span>

      {/* The row's state, spelled out for assistive technology. Icons and
          boldness carry it visually; this carries it otherwise. */}
      <span className="visually-hidden">
        {group.hasUnread ? t("list.unread") : ""}
        {group.hasFlagged ? ` ${t("list.flagged")}` : ""}
        {group.hasAttachment ? ` ${t("list.attachment")}` : ""}
        {group.size > 1
          ? ` ${
              group.sizeIsExact
                ? format("list.threadSize", group.size)
                : format("list.threadSizeInWindow", group.size)
            }`
          : ""}
      </span>
    </div>
  );
}

/**
 * One hover action inside a row (E2 item 5).
 *
 * The three event handlers are not defensive noise — each blocks a specific
 * way the row's own handlers would otherwise fire:
 *
 *   - `onClick` stopping propagation, or "archive" archives AND opens;
 *   - `onKeyDown` stopping propagation, or the row's Enter/Space handler opens
 *     the message on top of the button's own activation;
 *   - `onFocus` stopping propagation, because the row selects itself on focus
 *     and tabbing to a button must not silently change the selection.
 */
function RowAction({
  label,
  onActivate,
  children,
}: {
  readonly label: string;
  readonly onActivate: () => void;
  readonly children: React.ReactNode;
}): React.JSX.Element {
  return (
    <button
      type="button"
      className={styles.rowAction}
      aria-label={label}
      title={label}
      /* Reachable by Tab from the focused row — the roving tabindex governs
         the ROWS, not the controls inside the one the user is on. */
      tabIndex={0}
      onClick={(event) => {
        event.stopPropagation();
        event.preventDefault();
        onActivate();
      }}
      onKeyDown={(event) => {
        event.stopPropagation();
      }}
      onFocus={(event) => {
        event.stopPropagation();
      }}
    >
      <svg
        viewBox="0 0 20 20"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden="true"
        focusable="false"
      >
        {children}
      </svg>
    </button>
  );
}

/** The list's loading state. */
function ListSkeleton(): React.JSX.Element {
  const { t } = useTranslation();
  return (
    <div className={styles.skeleton} aria-busy="true" aria-label={t("list.loading")}>
      {Array.from({ length: 8 }, (_, index) => (
        <div key={index} className={styles.skeletonRow} aria-hidden="true">
          <span className={styles.skeletonAvatar} />
          <span className={styles.skeletonLines}>
            <span className={styles.skeletonBar} style={{ width: `${[30, 24, 34, 26][index % 4] ?? 28}%` }} />
            <span className={styles.skeletonBar} style={{ width: `${[70, 84, 62, 78][index % 4] ?? 72}%` }} />
          </span>
        </div>
      ))}
    </div>
  );
}
