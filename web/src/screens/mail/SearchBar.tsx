import { forwardRef, useCallback, useEffect, useMemo, useRef } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import { createDebouncer, SEARCH_DEBOUNCE_MS } from "../../mail/search";
import styles from "./SearchBar.module.css";

/**
 * The search field (P2 deliverable 5).
 *
 * Debouncing lives here rather than in the screen because the field is what
 * knows about keystrokes: `onChange` fires per character, and the debouncer
 * collapses a burst into one request. The reason is not our latency (the
 * server answers well inside the bar) but the server's `maxConcurrentRequests`
 * of 8 — typing a 12-character query un-debounced would fire 12 requests and
 * earn a 429.
 *
 * `Enter` FLUSHES rather than waiting: a user who has stopped typing and
 * pressed Enter has told us they are done, and making them wait out a timer
 * they cannot see is the cheapest kind of sluggishness.
 */

export interface SearchBarProps {
  /** The current text, owned by the screen so the URL can drive it. */
  readonly value: string;
  /** Fires on every keystroke, for the controlled input. */
  readonly onChange: (value: string) => void;
  /** Fires debounced — this is the one that costs a request. */
  readonly onSearch: (value: string) => void;
  readonly isSearching: boolean;
}

export const SearchBar = forwardRef<HTMLInputElement, SearchBarProps>(function SearchBar(
  { value, onChange, onSearch, isSearching },
  ref,
) {
  const { t } = useTranslation();

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

  const handleChange = useCallback(
    (event: React.ChangeEvent<HTMLInputElement>): void => {
      const next = event.target.value;
      onChange(next);
      debouncer.run(next);
    },
    [onChange, debouncer],
  );

  const clear = useCallback((): void => {
    debouncer.cancel();
    onChange("");
    onSearchRef.current("");
  }, [onChange, debouncer]);

  return (
    <div className={styles.wrapper}>
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
        onKeyDown={(event) => {
          if (event.key === "Enter") {
            event.preventDefault();
            // The user has finished; do not make them wait out the debounce.
            debouncer.flush();
          }
          if (event.key === "Escape" && value !== "") {
            // Escape clears before it closes anything — the field's own
            // affordance takes priority while there is text in it.
            event.stopPropagation();
            clear();
          }
        }}
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
    </div>
  );
});
