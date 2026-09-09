import {
  forwardRef,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import {
  createDebouncer,
  isSearchable,
  normalizeQuery,
  SEARCH_DEBOUNCE_MS,
} from "../../mail/search";
import { buildSuggestions, type Suggestion } from "../../mail/searchSuggestions";
import type { IndexedAddress } from "../../mail/addressIndex";
import type { Label } from "../../mail/labelStore";
import type { FilterDraftFromSearch } from "../../mail/searchToFilter";
import type { Mailbox } from "../../mail/types";
import { SearchOptions } from "./SearchOptions";
import styles from "./SearchBar.module.css";

/**
 * The search field (P2 deliverable 5; extended in L3 epic E3).
 *
 * # Typing does not search (P0-3)
 *
 * `onChange` fires per character and updates the TEXT; it does not call
 * `onSearch`. The screen answers a search with a route change, so a debounced
 * search-per-keystroke meant navigating mid-word — most visibly on `from:`,
 * an operator with no value yet, which came back "no matches" under a warning
 * card while the user was still typing the name. Gmail holds the box until
 * Enter, and so does this.
 *
 * The debouncer survives for the paths that DO search but can repeat: the
 * advanced panel and an accepted suggestion. Its original reason — the
 * server's `maxConcurrentRequests` of 8 — is why it is a cancel-and-fire seam
 * rather than a bare call.
 *
 * # Escape leaves, X clears (P0-4, E-28)
 *
 * Escape closes the popup if one is open, and otherwise blurs. It does NOT
 * clear the query: that is the X's job. Blurring is load-bearing rather than
 * cosmetic — every global shortcut is suppressed while an input has focus, so
 * a field that never gave focus back was a trap in which `?` reached the
 * browser instead of the shortcuts dialog.
 *
 * # E3: the combobox, and how it coexists with the debounce
 *
 * The suggestion list is the APG **combobox with listbox popup** pattern: the
 * input carries `role="combobox"`, `aria-expanded`, `aria-controls` and
 * `aria-activedescendant`; the list is a `listbox` of `option`s; focus NEVER
 * leaves the input, and the arrow keys move a virtual cursor instead. That is
 * the pattern's whole point — a user typing must keep typing, so moving DOM
 * focus into the list would break the field.
 *
 * The debounce is untouched by it. Suggestions are computed synchronously from
 * data already in memory (recent searches, labels, the operator table), so
 * they appear on the keystroke while nothing is searched at all.
 */

export interface SearchBarProps {
  /** The current text, owned by the screen so the URL can drive it. */
  readonly value: string;
  /** Fires on every keystroke, for the controlled input. */
  readonly onChange: (value: string) => void;
  /**
   * Runs a search — the one that costs a request and moves the route.
   *
   * Fired by Enter, by accepting a suggestion, by the advanced panel and by
   * the clear button. NEVER by a keystroke (P0-3).
   */
  readonly onSearch: (value: string) => void;
  readonly isSearching: boolean;

  /** E3: past searches, newest first. Empty disables that section. */
  readonly recentSearches?: readonly string[];
  /** E3: the account's labels, for `label:` completion. */
  readonly labels?: readonly Label[];
  /** E3: the folders the options panel offers as a scope. */
  readonly mailboxes?: readonly Mailbox[];
  /** E-15: the folder on screen, for the panel's "En esta carpeta" row. */
  readonly currentMailbox?: Mailbox | undefined;
  /**
   * E-04: E7's address index, for completing `from:` / `to:` / `cc:` / `bcc:`.
   *
   * The soft dependency `searchSuggestions.ts` declared in E3 and named as the
   * highest-value missing source in the review (E-05). Absent when the user
   * opted out or nothing has been indexed, which produces no address rows
   * rather than an empty section — the same rule `AddressField` follows.
   */
  readonly addressSuggestions?: readonly IndexedAddress[];
  /** E3: clears the stored history. Absent hides the affordance. */
  readonly onClearRecent?: (() => void) | undefined;
  /**
   * A-11: the query text a VIRTUAL VIEW is equivalent to — `is:starred`,
   * `in:snoozed`, `label:Trabajo`.
   *
   * Gmail's box is never empty on those views: opening Destacados puts
   * `is:starred` in it with a ✕ beside it, and that single line is what tells
   * a user which of the rail's many entries they are looking at. Moov's box
   * went blank, so Destacados, Pospuestos and a label view were three
   * identical unlabelled lists.
   *
   * It is a DISPLAY value and not the `value`, which is the whole design:
   *
   *   - it is never persisted, never stored in recent searches, and never
   *     submitted — a virtual view is a ROUTE (`{kind:"starred"}`), not a text
   *     query, and pushing `is:starred` through `onSearch` would turn it into
   *     one, losing the route the rail highlights from;
   *   - the moment the user types, `value` takes over and this disappears,
   *     because they are now writing a real query and the view's label would
   *     be text they did not enter;
   *   - navigating to an ordinary folder clears it, because the caller stops
   *     passing it — there is no state here to go stale.
   *
   * Absent (the ordinary case: a folder, or a real search) changes nothing.
   */
  readonly viewQuery?: string | undefined;
  /**
   * A-11: the ✕ beside `viewQuery`. Leaves the virtual view.
   *
   * A separate callback from `onSearch("")` because leaving `is:starred` is a
   * NAVIGATION — back to the inbox — not the clearing of a query that was
   * never run.
   */
  readonly onClearView?: (() => void) | undefined;
  /**
   * E12/B7: opens the filter builder pre-filled from the advanced panel
   * (canon 07 §8).
   *
   * Passed straight through — this component knows nothing about filters and
   * should not: it owns a text box, a combobox popup and the panel's
   * placement. Absent removes the button, which is the case when the server
   * has no Sieve capability.
   */
  readonly onCreateFilter?: ((draft: FilterDraftFromSearch) => void) | undefined;
  /**
   * E-06: fetches the first few MATCHING MESSAGES for a query, without
   * navigating.
   *
   * This is the debouncer's proper job, and the reason it survived P0-3. The
   * debounce was written for the server's `maxConcurrentRequests` of 8 and then
   * misused to run the actual search on every keystroke, which navigated
   * mid-word; removing that left a timer with nothing to collapse. A preview is
   * exactly what it was built for: many keystrokes, one request, and no route
   * change to be wrong about.
   *
   * Absent — offline, or on a screen with no client — removes the message rows
   * entirely. The operator and recent suggestions are unaffected, because they
   * are computed from memory and never need a request.
   */
  readonly onPreviewSearch?: (
    query: string,
    signal: AbortSignal,
  ) => Promise<readonly SearchPreviewRow[]>;
  /** E-06: opens one previewed message. Absent disables the rows. */
  readonly onOpenPreview?: (row: SearchPreviewRow) => void;
}

/** One matching message in the dropdown (E-06). */
export interface SearchPreviewRow {
  readonly id: string;
  readonly sender: string;
  readonly subject: string;
  /** Already formatted by the caller, whose locale formatter this is. */
  readonly date: string;
}

/** The section heading each suggestion kind carries, as a lookup not a chain. */
const KIND_LABELS = {
  recent: "search.suggestions.recent",
  label: "search.suggestions.labels",
  operator: "search.suggestions.operators",
  /* E-04: a value for the operator the user has already finished typing. */
  value: "search.suggestions.values",
} as const;

const NO_LABELS: readonly Label[] = [];
const NO_MAILBOXES: readonly Mailbox[] = [];
const NO_RECENT: readonly string[] = [];
const NO_ADDRESSES: readonly IndexedAddress[] = [];
const NO_PREVIEWS: readonly SearchPreviewRow[] = [];

/**
 * How many matching messages the dropdown shows (E-06).
 *
 * Five, which is Gmail's own count and the right order of magnitude for a list
 * a person scans on the way to Enter. More would make the popup the page; fewer
 * would not answer "is the thing I am looking for already here".
 */
const MAX_PREVIEWS = 5;

/**
 * How long to wait before asking the server for the previews (E-06).
 *
 * 300 ms rather than the 180 ms `SEARCH_DEBOUNCE_MS` the removed as-you-type
 * search used, and the difference is deliberate: this request is a
 * CONVENIENCE, not the search, so it should cost the server less and it is
 * allowed to arrive a beat after the user stops. 180 ms was chosen to fire
 * DURING typing; this one is meant to fire after it.
 */
const PREVIEW_DEBOUNCE_MS = 300;

/**
 * E-07: the shortcuts offered when the box is focused and empty.
 *
 * Three, and each is a query a person actually wants and would otherwise have
 * to know the operator language to write. They emit ordinary grammar, so a chip
 * and a typed query are the same thing to everything downstream — which is the
 * invariant the whole search surface is built on.
 */
const QUICK_CHIPS = [
  { query: "has:attachment", labelKey: "search.chip.hasAttachment" },
  { query: "newer_than:7d", labelKey: "search.chip.last7" },
  /*
   * "Enviados por mí" is `in:sent`, not a `from:` on the user's own address.
   *
   * Gmail's chip means "mail I sent", and the Sent folder IS that set — every
   * message the server put there went out under this account. A `from:me` would
   * be worse in both directions: it over-matches (a mailing list that echoes
   * your own post back into the inbox) and it under-matches (a message sent
   * from a second identity). The folder is the truth, so the folder is the
   * query.
   */
  { query: "in:sent", labelKey: "search.quick.sentByMe" },
  /*
   * `as const` rather than a `StringKey` annotation, deliberately: `t` accepts
   * only the keys whose values are PLAIN strings (a formatted one needs
   * `format`), and that narrower type is not exported. Inferring the literal
   * keys lets the compiler check each one against the real table, which is
   * strictly stronger than annotating them as "some key".
   */
] as const;

/** One row of the popup: a suggestion, a matching message, or the Enter row. */
type PopupRow =
  | { readonly kind: "suggestion"; readonly suggestion: Suggestion }
  | { readonly kind: "preview"; readonly preview: SearchPreviewRow }
  | { readonly kind: "all" };

/** A stable React key per row, unique across the three kinds. */
function rowKey(row: PopupRow): string {
  if (row.kind === "suggestion") return `s:${row.suggestion.id}`;
  if (row.kind === "preview") return `p:${row.preview.id}`;
  return "all";
}

export const SearchBar = forwardRef<HTMLInputElement, SearchBarProps>(function SearchBar(
  {
    value,
    onChange,
    onSearch,
    isSearching,
    recentSearches = NO_RECENT,
    labels = NO_LABELS,
    mailboxes = NO_MAILBOXES,
    currentMailbox,
    addressSuggestions = NO_ADDRESSES,
    onClearRecent,
    viewQuery,
    onClearView,
    onCreateFilter,
    onPreviewSearch,
    onOpenPreview,
  },
  ref,
) {
  const { t, format } = useTranslation();
  const ids = useId();
  const listboxId = `${ids}-suggestions`;

  const [isOpen, setOpen] = useState(false);
  /** The virtual cursor: -1 means "no suggestion is active". */
  const [activeIndex, setActiveIndex] = useState(-1);
  const [panelOpen, setPanelOpen] = useState(false);
  const wrapperRef = useRef<HTMLDivElement | null>(null);

  // The debouncer is kept in a ref so it survives re-renders; recreating it on
  // each render would reset its timer on every keystroke and it would never
  // fire.
  const onSearchRef = useRef(onSearch);
  useEffect(() => {
    onSearchRef.current = onSearch;
  }, [onSearch]);

  const debouncer = useMemo(
    () =>
      createDebouncer((next: string) => {
        onSearchRef.current(next);
      }, SEARCH_DEBOUNCE_MS),
    [],
  );

  // A pending search must not fire after the field is gone.
  useEffect(() => () => { debouncer.cancel(); }, [debouncer]);

  const suggestions: readonly Suggestion[] = useMemo(
    () =>
      buildSuggestions({
        input: value,
        recent: recentSearches,
        labels,
        // E-04: a complete operator opens its VALUES, and these are where the
        // values come from. Empty arrays mean the corresponding rows are simply
        // not offered — never an empty section.
        mailboxes,
        addresses: addressSuggestions,
      }),
    [value, recentSearches, labels, mailboxes, addressSuggestions],
  );

  /*
   * E-06: the matching messages, fetched on a debounce and never navigating.
   *
   * The whole point of the effect running on `value` rather than being called
   * from `handleChange` is that it also covers the paths that set the text
   * WITHOUT a keystroke — the URL restoring a query, a chip rewriting one — so
   * the preview never shows the previous query's messages under the new text.
   *
   * `AbortController` per run, cancelled on the next keystroke, so an early
   * slow response cannot land after a later fast one and repaint the popup with
   * stale rows. That is a real hazard here and not a theoretical one: the
   * debounce collapses a burst but does not serialise what escapes it.
   */
  const [previews, setPreviews] = useState<readonly SearchPreviewRow[]>(NO_PREVIEWS);
  const previewFor = useRef("");
  useEffect(() => {
    const query = normalizeQuery(value);
    if (onPreviewSearch === undefined || !isOpen || !isSearchable(query)) {
      setPreviews(NO_PREVIEWS);
      previewFor.current = "";
      return undefined;
    }
    if (previewFor.current === query) return undefined;

    const controller = new AbortController();
    const timer = setTimeout(() => {
      void (async () => {
        try {
          const rows = await onPreviewSearch(query, controller.signal);
          if (controller.signal.aborted) return;
          previewFor.current = query;
          setPreviews(rows.slice(0, MAX_PREVIEWS));
        } catch {
          // A failed preview is not an error the user has to see: the search
          // itself has not been run, and Enter still works. Silence here is
          // the honest degradation, not a swallowed failure.
          if (!controller.signal.aborted) setPreviews(NO_PREVIEWS);
        }
      })();
    }, PREVIEW_DEBOUNCE_MS);

    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [value, isOpen, onPreviewSearch]);

  /*
   * The popup closes on an outside pointer down — the same dismissal
   * `PopupMenu` implements, and for the same reason: a popup that only closes
   * on Escape traps a mouse user.
   */
  useEffect(() => {
    if (!isOpen && !panelOpen) return undefined;
    const onPointerDown = (event: PointerEvent): void => {
      if (wrapperRef.current?.contains(event.target as Node) === true) return;
      setOpen(false);
      setPanelOpen(false);
    };
    document.addEventListener("pointerdown", onPointerDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
    };
  }, [isOpen, panelOpen]);

  /**
   * Every option in the popup, in render order (E-06, E-08).
   *
   * ONE array, because the arrow keys walk ONE virtual cursor. Keeping the
   * suggestions and the message rows as two lists would mean two cursors and a
   * hand-written hand-off between them, which is precisely where a combobox
   * stops matching the APG pattern the review credited it with.
   *
   * The "all results" row is last and is present only when there is something
   * to search for. It is the row that makes the message previews safe: without
   * it a user who wanted the whole result list would see five messages and have
   * no visible way to ask for the rest, and would conclude five is all there
   * are.
   */
  /** E-07: the chips show only for a focused, EMPTY box. */
  const showQuickChips = isOpen && value.trim() === "";

  const rows = useMemo((): readonly PopupRow[] => {
    const list: PopupRow[] = suggestions.map((suggestion) => ({
      kind: "suggestion" as const,
      suggestion,
    }));
    for (const preview of previews) list.push({ kind: "preview" as const, preview });
    if (isSearchable(normalizeQuery(value))) list.push({ kind: "all" as const });
    return list;
  }, [suggestions, previews, value]);

  const handleChange = useCallback(
    (event: React.ChangeEvent<HTMLInputElement>): void => {
      const next = event.target.value;
      onChange(next);
      /*
       * P0-3: typing does NOT search.
       *
       * `debouncer.run(next)` used to live here, and 180 ms after any pause it
       * called `onSearch` — which the screen answers with a route change. So
       * the user typing `from:ana` navigated on `f`, on `fr`, and finally on
       * `from:` — a half-typed operator, which the list answered with "no
       * matches" while the caret was still mid-word. Gmail does not do this:
       * the box holds text until Enter.
       *
       * The debouncer stays for the paths that DO search — Enter flushes it,
       * accepting a suggestion cancels and fires — so nothing else changes.
       */
      setOpen(true);
      // A new keystroke invalidates the cursor: the list under it has changed.
      setActiveIndex(-1);
    },
    [onChange],
  );

  const clear = useCallback((): void => {
    debouncer.cancel();
    onChange("");
    onSearchRef.current("");
    setOpen(false);
    setActiveIndex(-1);
  }, [onChange, debouncer]);

  const accept = useCallback(
    (suggestion: Suggestion): void => {
      onChange(suggestion.value);
      setOpen(false);
      setActiveIndex(-1);
      /*
       * An accepted suggestion searches IMMEDIATELY. The debounce exists to
       * collapse typing; a click or an Enter on a suggestion is a finished
       * intention, and making it wait 180 ms would be latency we chose to add.
       *
       * `cancel` first, so a keystroke still pending cannot fire afterwards
       * with the half-typed text and overwrite this result.
       */
      debouncer.cancel();
      onSearchRef.current(suggestion.value);
    },
    [onChange, debouncer],
  );

  /**
   * Runs the search the box currently holds (E-08's "all results" row).
   *
   * The same thing Enter does, extracted because the row and the key must not
   * be able to diverge: a row labelled "Enter" that did something Enter does
   * not would be worse than no row.
   */
  const runAll = useCallback((): void => {
    setOpen(false);
    setActiveIndex(-1);
    debouncer.cancel();
    onSearchRef.current(value);
  }, [debouncer, value]);

  /** Activates whichever kind of row the cursor is on (E-06, E-08). */
  const activate = useCallback(
    (row: PopupRow): void => {
      if (row.kind === "suggestion") {
        accept(row.suggestion);
        return;
      }
      if (row.kind === "preview") {
        /*
         * Opening a previewed message does NOT run the search, and does not
         * touch the list behind: the user found the one message they were
         * after, and replacing their inbox with a result list they never asked
         * for would be the screen doing something they did not.
         */
        setOpen(false);
        setActiveIndex(-1);
        onOpenPreview?.(row.preview);
        return;
      }
      runAll();
    },
    [accept, onOpenPreview, runAll],
  );

  const handleKeyDown = useCallback(
    (event: React.KeyboardEvent<HTMLInputElement>): void => {
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        if (rows.length === 0) return;
        event.preventDefault();
        setOpen(true);
        setActiveIndex((current) => {
          const step = event.key === "ArrowDown" ? 1 : -1;
          const next = current + step;
          // Wraps at both ends, which APG lists as the expected behaviour and
          // which saves a long list from a dead end at the bottom.
          if (next < 0) return rows.length - 1;
          if (next >= rows.length) return -1;
          return next;
        });
        return;
      }

      if (event.key === "Enter") {
        event.preventDefault();
        const active = activeIndex >= 0 ? rows[activeIndex] : undefined;
        if (active !== undefined) {
          activate(active);
          return;
        }
        /*
         * The user has finished. Since P0-3 no keystroke queues a search, so
         * `flush()` would have nothing to flush — Enter is now the thing that
         * SEARCHES, not the thing that hurries an already-pending one along.
         * `cancel()` first anyway: the panel and a suggestion still use the
         * debouncer, and a stale timer firing after this would replace this
         * result with an older query's.
         */
        setOpen(false);
        debouncer.cancel();
        onSearchRef.current(value);
        return;
      }

      if (event.key === "Escape") {
        /*
         * P0-4 + E-28: Escape LEAVES the field. The X clears it.
         *
         * Two bugs lived in the old three-branch version. First, no branch
         * ever called `blur()`, so after any search the caret stayed in the
         * box — and every global shortcut is suppressed while an INPUT has
         * focus (`isTypingTarget`), so `?` opened the browser's own field
         * history instead of the shortcuts dialog. The field was a focus trap
         * with no visible walls.
         *
         * Second, Escape used to CLEAR the query. That is not Gmail's shape:
         * there, Escape returns you to the list and the text survives, because
         * a user who dismisses a popup has not asked to lose what they typed —
         * they may well want to edit it. Clearing is the X's job, which is
         * beside the box and says so.
         *
         * So: the popup closes first (innermost affordance, as the menus do),
         * and the next Escape blurs. Focus goes to the document rather than to
         * a named element because the shortcut layer listens there; that is the
         * exact symmetric of `focusSearch`, which pulls focus IN from wherever
         * it was.
         */
        if (isOpen) {
          event.stopPropagation();
          setOpen(false);
          setActiveIndex(-1);
          return;
        }
        event.stopPropagation();
        setActiveIndex(-1);
        event.currentTarget.blur();
      }
    },
    [rows, activeIndex, activate, debouncer, isOpen, value],
  );

  const activeId = activeIndex >= 0 ? `${listboxId}-${String(activeIndex)}` : undefined;

  return (
    <div className={styles.wrapper} ref={wrapperRef}>
      <svg
        className={styles.icon}
        viewBox="0 0 20 20"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.7"
        strokeLinecap="round"
        aria-hidden="true"
        focusable="false"
      >
        <circle cx="9" cy="9" r="5.5" />
        <path d="M13.2 13.2l4 4" />
      </svg>

      {/*
        A-11: the virtual view's query, shown INSIDE the pill.

        It renders only while the box is otherwise empty. The moment the user
        types, `value` is non-empty and this disappears — they are writing a
        real query, and the view's label sitting beside it would read as text
        they had entered.

        # Why a chip and not the input's `value`

        The brief says "a display value of the SearchBar", and the honest way
        to build that is a chip rather than seeding the input. React controls
        the input, so a seeded `value` of "is:starred" means the first
        keystroke produces "is:starredf" — the user would have to clear text
        they never typed. Every fix for that is worse than the chip: making it
        `readOnly` breaks `/`; clearing on focus makes the label vanish when
        the user merely tabs past; stripping a known prefix in `onChange` fails
        the moment someone deliberately types `is:starred` themselves.

        The chip is also more truthful about what it is. `is:starred` here is
        not a query anyone ran — the route is `{kind:"starred"}`, a
        `hasKeyword` filter — and drawing it as an unfocusable label rather
        than as editable text says so. It sits in the input's own row with the
        input's own type scale, so it reads exactly as Gmail's does.
      */}
      {value === "" && viewQuery !== undefined && (
        <span className={styles.viewQuery}>
          <span className={styles.viewQueryText}>{viewQuery}</span>
          {onClearView !== undefined && (
            <button
              type="button"
              className={styles.viewQueryClear}
              onClick={onClearView}
              aria-label={t("search.clearView")}
              title={t("search.clearView")}
            >
              <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" aria-hidden="true" focusable="false">
                <path d="M5.5 5.5l9 9M14.5 5.5l-9 9" />
              </svg>
            </button>
          )}
        </span>
      )}

      <input
        ref={ref}
        type="search"
        className={styles.input}
        value={value}
        onChange={handleChange}
        onFocus={() => {
          setOpen(true);
        }}
        onKeyDown={handleKeyDown}
        /* APG combobox: the input owns the popup and points at the active
           option, while DOM focus stays here so typing is never interrupted. */
        role="combobox"
        aria-expanded={isOpen && suggestions.length > 0}
        aria-controls={listboxId}
        aria-autocomplete="list"
        {...(activeId !== undefined ? { "aria-activedescendant": activeId } : {})}
        /* A visible placeholder is not a label: it disappears on focus and is
         * not announced reliably. The real label is hidden but present. */
        aria-label={t("search.label")}
        /* A-11: the placeholder gives way to the view's chip. Showing "Buscar
         * correo" beside `is:starred` would offer two competing answers to
         * "what is in this box". */
        placeholder={
          value === "" && viewQuery !== undefined ? "" : t("search.placeholder")
        }
        /* The browser's own search history dropdown covers our results and is
         * shared across sites; a mail search box should not feed it. */
        autoComplete="off"
        spellCheck={false}
      />

      {isSearching && <span className={styles.spinner} aria-hidden="true" />}

      {value !== "" && !isSearching && (
        <button
          type="button"
          className={styles.clear}
          onClick={clear}
          aria-label={t("search.clear")}
          title={t("search.clear")}
        >
          <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" aria-hidden="true" focusable="false">
            <path d="M5.5 5.5l9 9M14.5 5.5l-9 9" />
          </svg>
        </button>
      )}

      <button
        type="button"
        className={styles.options}
        aria-expanded={panelOpen}
        aria-label={panelOpen ? t("search.options.close") : t("search.options.open")}
        title={panelOpen ? t("search.options.close") : t("search.options.open")}
        onClick={() => {
          setPanelOpen((open) => !open);
          setOpen(false);
        }}
      >
        {/*
          E-11: SLIDERS, not a funnel and not a caret.

          The old path was a three-line taper, which reads as a filter funnel —
          the icon for "narrow these results", a thing this button does not do.
          Gmail's is a pair of horizontal rails with a knob on each, and the
          knobs are the whole difference: they say the control OPENS SETTINGS
          you adjust, which is exactly what the advanced panel is. A caret would
          have said "there is more of this list below", which is what the
          suggestions popup does and this button does not.
        */}
        <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" aria-hidden="true" focusable="false">
          <path d="M3 7h4M11 7h6M3 13h8M15 13h2" />
          <circle cx="9" cy="7" r="1.9" />
          <circle cx="13" cy="13" r="1.9" />
        </svg>
      </button>

      {/*
        The popup. The listbox is ALWAYS rendered (hidden when empty) so
        `aria-controls` never points at a missing element — a dangling
        reference is an accessibility bug screen readers report as a broken
        widget.
      */}
      <div className={styles.popup} hidden={!isOpen || (rows.length === 0 && !showQuickChips)}>
      {/*
        E-07: the quick chips, INSIDE the popup and above the rows.

        They were a separate band under the box, which is not where Gmail puts
        them and — more to the point — meant a band of controls sitting over the
        list whether or not anyone was searching. Here they appear exactly when
        they are useful: the box has focus and is EMPTY, which is the one moment
        a person has no query and might take a suggested one.

        They are `<button>`s and not options of the listbox, deliberately: they
        do not complete what is being typed (nothing is), they start a search
        outright. Putting them in the listbox would make the arrow keys walk
        through three shortcuts before reaching the recent searches.
      */}
      {showQuickChips && (
        <div className={styles.quickChips} role="group" aria-label={t("search.quick.label")}>
          {QUICK_CHIPS.map((chip) => (
            <button
              key={chip.query}
              type="button"
              className={styles.quickChip}
              onMouseDown={(event) => {
                event.preventDefault();
                onChange(chip.query);
                setOpen(false);
                debouncer.cancel();
                onSearchRef.current(chip.query);
              }}
            >
              {t(chip.labelKey)}
            </button>
          ))}
        </div>
      )}

      <ul
        id={listboxId}
        role="listbox"
        aria-label={t("search.suggestions.label")}
        className={styles.suggestions}
      >
        {rows.map((row, index) => (
          <li
            key={rowKey(row)}
            id={`${listboxId}-${String(index)}`}
            role="option"
            aria-selected={index === activeIndex}
            className={[
              row.kind === "preview" ? styles.previewRow : styles.suggestion,
              row.kind === "all" ? styles.allRow : "",
              index === activeIndex ? styles.suggestionActive : "",
            ]
              .filter(Boolean)
              .join(" ")}
            /*
             * `onMouseDown` with preventDefault, NOT onClick: a click fires
             * after blur, and the blur would have closed the popup and
             * unmounted this element before the click landed. Preventing the
             * default keeps focus in the input, which is also what the APG
             * pattern requires.
             */
            onMouseDown={(event) => {
              event.preventDefault();
              activate(row);
            }}
          >
            {row.kind === "suggestion" ? (
              <>
                <span className={styles.suggestionKind}>
                  {t(KIND_LABELS[row.suggestion.kind])}
                </span>
                <span className={styles.suggestionText}>{row.suggestion.label}</span>
              </>
            ) : row.kind === "preview" ? (
              <>
                <span className={styles.previewSender}>{row.preview.sender}</span>
                <span className={styles.previewSubject}>{row.preview.subject}</span>
                <span className={styles.previewDate}>{row.preview.date}</span>
              </>
            ) : (
              /*
               * E-08: the row that makes the message previews safe.
               *
               * Without it a user who wanted the whole result list would see
               * five messages and have no visible way to ask for the rest —
               * and would reasonably conclude five is all there are. It names
               * the key as well as being clickable, because the key is what a
               * returning user will reach for.
               */
              <>
                <span className={styles.suggestionText}>
                  {format("search.allResults", normalizeQuery(value))}
                </span>
                <kbd className={styles.allKey}>{t("search.allResultsKey")}</kbd>
              </>
            )}
          </li>
        ))}
      </ul>

        {recentSearches.length > 0 && onClearRecent !== undefined && (
          <button
            type="button"
            className={styles.clearRecent}
            onMouseDown={(event) => {
              // Same reason as a suggestion row: a click lands after blur, by
              // which time this element is gone.
              event.preventDefault();
              onClearRecent();
            }}
          >
            {t("search.suggestions.clearRecent")}
          </button>
        )}
      </div>

      {panelOpen && (
        <SearchOptions
          query={value}
          mailboxes={mailboxes}
          currentMailbox={currentMailbox}
          onSubmit={(next) => {
            onChange(next);
            setPanelOpen(false);
            debouncer.cancel();
            onSearchRef.current(next);
          }}
          onClose={() => {
            setPanelOpen(false);
          }}
          onCreateFilter={
            onCreateFilter === undefined
              ? undefined
              : (draft) => {
                  // The panel closes as the builder opens: leaving an advanced
                  // search panel hanging over the settings page the builder
                  // lives on would be two surfaces stacked for no reason.
                  setPanelOpen(false);
                  onCreateFilter(draft);
                }
          }
        />
      )}
    </div>
  );
});
