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
import { createDebouncer, SEARCH_DEBOUNCE_MS } from "../../mail/search";
import { buildSuggestions, type Suggestion } from "../../mail/searchSuggestions";
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
  /** E3: clears the stored history. Absent hides the affordance. */
  readonly onClearRecent?: (() => void) | undefined;
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
}

/** The section heading each suggestion kind carries, as a lookup not a chain. */
const KIND_LABELS = {
  recent: "search.suggestions.recent",
  label: "search.suggestions.labels",
  operator: "search.suggestions.operators",
} as const;

const NO_LABELS: readonly Label[] = [];
const NO_MAILBOXES: readonly Mailbox[] = [];
const NO_RECENT: readonly string[] = [];

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
    onClearRecent,
    onCreateFilter,
  },
  ref,
) {
  const { t } = useTranslation();
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
    () => buildSuggestions({ input: value, recent: recentSearches, labels }),
    [value, recentSearches, labels],
  );

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

  const handleKeyDown = useCallback(
    (event: React.KeyboardEvent<HTMLInputElement>): void => {
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        if (suggestions.length === 0) return;
        event.preventDefault();
        setOpen(true);
        setActiveIndex((current) => {
          const step = event.key === "ArrowDown" ? 1 : -1;
          const next = current + step;
          // Wraps at both ends, which APG lists as the expected behaviour and
          // which saves a long list from a dead end at the bottom.
          if (next < 0) return suggestions.length - 1;
          if (next >= suggestions.length) return -1;
          return next;
        });
        return;
      }

      if (event.key === "Enter") {
        event.preventDefault();
        const active = activeIndex >= 0 ? suggestions[activeIndex] : undefined;
        if (active !== undefined) {
          accept(active);
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
    [suggestions, activeIndex, accept, debouncer, isOpen, value],
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
        placeholder={t("search.placeholder")}
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
        <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" aria-hidden="true" focusable="false">
          <path d="M4 6h12M6.5 10h7M9 14h2" />
        </svg>
      </button>

      {/*
        The popup. The listbox is ALWAYS rendered (hidden when empty) so
        `aria-controls` never points at a missing element — a dangling
        reference is an accessibility bug screen readers report as a broken
        widget.
      */}
      <div className={styles.popup} hidden={!isOpen || suggestions.length === 0}>
      <ul
        id={listboxId}
        role="listbox"
        aria-label={t("search.suggestions.label")}
        className={styles.suggestions}
      >
        {suggestions.map((suggestion, index) => (
          <li
            key={suggestion.id}
            id={`${listboxId}-${String(index)}`}
            role="option"
            aria-selected={index === activeIndex}
            className={`${styles.suggestion} ${index === activeIndex ? styles.suggestionActive : ""}`}
            /*
             * `onMouseDown` with preventDefault, NOT onClick: a click fires
             * after blur, and the blur would have closed the popup and
             * unmounted this element before the click landed. Preventing the
             * default keeps focus in the input, which is also what the APG
             * pattern requires.
             */
            onMouseDown={(event) => {
              event.preventDefault();
              accept(suggestion);
            }}
          >
            <span className={styles.suggestionKind}>{t(KIND_LABELS[suggestion.kind])}</span>
            <span className={styles.suggestionText}>{suggestion.label}</span>
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
