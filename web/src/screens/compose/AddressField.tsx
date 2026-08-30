import { useCallback, useId, useMemo, useRef, useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import { isCommitKey, makeChip, parseAddressList, type AddressChip } from "../../mail/addresses";
import { suggestAddresses, type IndexedAddress } from "../../mail/addressIndex";
import styles from "./AddressField.module.css";

/**
 * One recipient field (To, Cc, Bcc) with address chips.
 *
 * # Accessibility, which is the hard part of a chip field
 *
 * A chip field is a list of removable things plus a text input, and the naive
 * implementation — `<div>`s with click handlers — is unusable by keyboard and
 * silent to a screen reader. This one:
 *
 *   - is a `role="list"` of `role="listitem"` chips followed by the input, so
 *     the recipients are announced as a list with a count rather than as loose
 *     text;
 *   - gives every chip a real `<button>` to remove it, reachable by Tab, with
 *     an accessible name that says WHICH address it removes ("Remove
 *     ana@x.com") rather than a bare "Remove" repeated five times;
 *   - handles Backspace on an empty input by removing the last chip, which is
 *     the interaction everyone expects and which no amount of ARIA substitutes
 *     for;
 *   - marks invalid chips with `aria-invalid` AND a visible treatment, never
 *     colour alone.
 *
 * # Why blur commits
 *
 * A user who types an address and clicks Send has not pressed Enter. Without a
 * blur commit their recipient is discarded and the send fails with "add a
 * recipient" while the address is visibly on screen — the single most
 * infuriating composer bug. `onBlur` commits, which also covers clicking Send:
 * the button's mousedown blurs the input, and the commit runs before the
 * submit handler reads the recipients.
 *
 * # E7: the combobox, laid over all of that without disturbing it
 *
 * With an address index supplied, the field becomes the APG **combobox with
 * listbox popup** — the same pattern `SearchBar` implements, and deliberately
 * the same implementation choices, so the two behave identically:
 *
 *   - the input carries `role="combobox"`, `aria-expanded`, `aria-controls` and
 *     `aria-activedescendant`; DOM focus NEVER leaves it, and the arrows move a
 *     virtual cursor. Moving real focus into the list would break typing, which
 *     is the entire point of a type-ahead;
 *   - the listbox is always in the DOM (hidden when empty) so `aria-controls`
 *     can never dangle;
 *   - options commit on `onMouseDown` with `preventDefault`, not `onClick` — a
 *     click fires after blur, and blur has already committed the pending text
 *     and closed the popup, so an `onClick` handler would land on an element
 *     that no longer exists.
 *
 * The existing behaviours are preserved exactly, and that is the constraint
 * that shaped the key handling: Enter and Tab COMMIT A SUGGESTION when one is
 * active, and otherwise fall through to the chip-commit path they always had.
 * Backspace-to-remove, paste-splitting, blur-commit and the invalid-chip
 * treatment are untouched — the suggestion layer sits above them and takes over
 * only when there is an active option to take over for.
 */

export interface AddressFieldProps {
  readonly label: string;
  readonly chips: readonly AddressChip[];
  readonly onChange: (chips: readonly AddressChip[]) => void;
  /** Autofocus this field when the composer opens (the To field of a new message). */
  readonly autoFocusField?: boolean;
  /** Rendered after the input — the Cc/Bcc toggles live here on the To row. */
  readonly trailing?: React.ReactNode;
  readonly inputRef?: React.MutableRefObject<HTMLInputElement | null>;
  /**
   * E7: the address index to complete from (canon §2.3).
   *
   * Absent or empty means no suggestions and no combobox semantics at all — not
   * an empty popup. A user who opted out, or whose index has not been fed yet,
   * gets exactly the field that shipped before this epic, `role="combobox"`
   * included: announcing a combobox that can never suggest anything is a lie
   * told to a screen reader.
   */
  readonly suggestions?: readonly IndexedAddress[];
}

const NO_SUGGESTIONS: readonly IndexedAddress[] = [];

export function AddressField({
  label,
  chips,
  onChange,
  autoFocusField = false,
  trailing,
  inputRef,
  suggestions: index = NO_SUGGESTIONS,
}: AddressFieldProps): React.JSX.Element {
  const { t, format } = useTranslation();
  const [pending, setPending] = useState("");
  const fallbackRef = useRef<HTMLInputElement | null>(null);
  const field = inputRef ?? fallbackRef;
  const inputId = useId();
  const listId = useId();

  /** The virtual cursor: -1 means "no suggestion is active". */
  const [activeIndex, setActiveIndex] = useState(-1);
  const [isOpen, setOpen] = useState(false);

  /**
   * The offered completions.
   *
   * Recomputed synchronously per keystroke, from an array already in memory —
   * no debounce is involved, because nothing here costs a request. The
   * already-chipped addresses are excluded so the list never offers a recipient
   * the field already has.
   */
  const matches = useMemo(
    () =>
      index.length === 0
        ? NO_SUGGESTIONS
        : suggestAddresses(
            index,
            pending,
            chips.map((chip) => chip.email),
          ),
    [index, pending, chips],
  );

  const popupOpen = isOpen && matches.length > 0;

  const commit = useCallback(
    (text: string): void => {
      const parsed = parseAddressList(text);
      if (parsed.length === 0) return;
      onChange([...chips, ...parsed]);
      setPending("");
      setActiveIndex(-1);
      setOpen(false);
    },
    [chips, onChange],
  );

  /**
   * Turns a suggestion into a chip.
   *
   * The display name rides along, so the header reads `Ana Gómez <ana@x.com>`
   * rather than a bare address — that is the whole reason the index stores a
   * name, and dropping it here would make the feature look like nothing more
   * than a history of strings.
   */
  const acceptSuggestion = useCallback(
    (suggestion: IndexedAddress): void => {
      onChange([...chips, makeChip(suggestion.email, suggestion.displayName)]);
      setPending("");
      setActiveIndex(-1);
      setOpen(false);
      field.current?.focus();
    },
    [chips, onChange, field],
  );

  const removeAt = useCallback(
    (key: string): void => {
      onChange(chips.filter((chip) => chip.key !== key));
      field.current?.focus();
    },
    [chips, onChange, field],
  );

  const onKeyDown = useCallback(
    (event: React.KeyboardEvent<HTMLInputElement>): void => {
      /*
       * E7: the arrows drive the virtual cursor. Handled BEFORE the commit
       * keys, and only when there is something to move through, so a field
       * with no index behaves exactly as it did before this epic.
       */
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        if (matches.length === 0) return;
        event.preventDefault();
        setOpen(true);
        setActiveIndex((current) => {
          const step = event.key === "ArrowDown" ? 1 : -1;
          const next = current + step;
          // Wraps at both ends, as APG expects and as `SearchBar` already does,
          // so a long list has no dead end at the bottom.
          if (next < 0) return matches.length - 1;
          if (next >= matches.length) return -1;
          return next;
        });
        return;
      }

      if (event.key === "Escape" && popupOpen) {
        /*
         * The popup is dismissed and the event is STOPPED, so the composer
         * dialog does not also read this Escape as "close me". One Escape, one
         * dismissal, innermost first — the same rule `PopupMenu` and
         * `SearchBar` follow.
         */
        event.stopPropagation();
        setOpen(false);
        setActiveIndex(-1);
        return;
      }

      if (isCommitKey(event.key)) {
        /*
         * An ACTIVE suggestion wins over the typed text: the user arrowed to it
         * deliberately, and committing what they half-typed instead would be
         * the field ignoring the selection it is displaying as selected.
         *
         * Tab is included, which is what makes the field feel like every other
         * type-ahead — and it still moves focus afterwards, exactly as the
         * plain-commit path below has always done.
         */
        const active = activeIndex >= 0 ? matches[activeIndex] : undefined;
        if (active !== undefined) {
          if (event.key !== "Tab") event.preventDefault();
          acceptSuggestion(active);
          return;
        }
      }

      if (isCommitKey(event.key) && pending.trim() !== "") {
        // Tab still moves focus after committing — the address is captured AND
        // the user's intent to leave the field is honoured.
        if (event.key !== "Tab") event.preventDefault();
        commit(pending);
        return;
      }
      if (event.key === "Backspace" && pending === "" && chips.length > 0) {
        event.preventDefault();
        const last = chips[chips.length - 1];
        if (last !== undefined) removeAt(last.key);
      }
    },
    [pending, chips, commit, removeAt, matches, activeIndex, acceptSuggestion, popupOpen],
  );

  /*
   * Paste is intercepted so a pasted list becomes chips immediately rather
   * than one long string the user then has to break up by hand.
   */
  const onPaste = useCallback(
    (event: React.ClipboardEvent<HTMLInputElement>): void => {
      const text = event.clipboardData.getData("text/plain");
      if (!/[,;<]/.test(text)) return;
      event.preventDefault();
      commit(`${pending}${text}`);
    },
    [pending, commit],
  );

  const invalidCount = chips.filter((chip) => !chip.isValid).length;
  const activeId = activeIndex >= 0 ? `${listId}-${String(activeIndex)}` : undefined;

  return (
    <div className={styles.row}>
      <label className={styles.label} htmlFor={inputId}>
        {label}
      </label>

      <div
        className={styles.field}
        onClick={(event) => {
          // Clicking the whitespace of the field focuses the input, which is
          // what the whole box looks like it should do. Clicks that landed on
          // a chip's remove button are excluded by the target check.
          if (event.target === event.currentTarget) field.current?.focus();
        }}
        /* Not interactive itself; the input inside it is. The handler above is
           a convenience, and a11y rules are satisfied by the real control. */
        role="presentation"
      >
        {/* The list is NOT labelled with the field name: the <label> above
            already names the input, and a second element with the same
            accessible name makes "the To field" ambiguous to a screen
            reader (and to any test that looks it up by name). The chip
            count is announced by the live region below instead. */}
        <ul className={styles.chips} id={listId}>
          {chips.map((chip) => (
            <li
              key={chip.key}
              className={[styles.chip, chip.isValid ? "" : styles.invalid]
                .filter(Boolean)
                .join(" ")}
            >
              <span className={styles.chipText} title={chip.email}>
                {chip.name ?? chip.email}
              </span>
              <button
                type="button"
                className={styles.chipRemove}
                onClick={() => {
                  removeAt(chip.key);
                }}
                aria-label={format("compose.removeRecipient", chip.email)}
              >
                <svg viewBox="0 0 16 16" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round">
                  <path d="M4.5 4.5l7 7M11.5 4.5l-7 7" />
                </svg>
              </button>
            </li>
          ))}

          <li className={styles.inputItem}>
            <input
              id={inputId}
              ref={field}
              className={styles.input}
              type="text"
              value={pending}
              // `email` rather than a bare text input: it gets the right
              // on-screen keyboard on mobile.
              inputMode="email"
              /*
               * E7: the browser's own autofill dropdown is turned OFF once we
               * have suggestions of our own. Two popups anchored to the same
               * input fight for the same pixels, and the browser's — shared
               * across every site, and unaware of which addresses this mailbox
               * actually corresponds with — would cover ours. With no index we
               * keep `email`, because the browser's list is better than nothing.
               */
              autoComplete={index.length > 0 ? "off" : "email"}
              spellCheck={false}
              /* The composer is a modal dialog opened by an explicit user
                 action; the WAI-ARIA APG dialog pattern REQUIRES focus to move
                 inside it, or a keyboard user is stranded behind it. The rule's
                 concern — focus stolen on page load — does not apply. */
              // eslint-disable-next-line jsx-a11y/no-autofocus
              autoFocus={autoFocusField}
              aria-describedby={invalidCount > 0 ? `${inputId}-error` : undefined}
              aria-invalid={invalidCount > 0}
              /*
               * E7: combobox semantics, applied ONLY when there is an index to
               * complete from. A field that can never suggest anything must not
               * announce itself as a combobox — see the `suggestions` prop.
               */
              {...(index.length > 0
                ? {
                    role: "combobox",
                    "aria-expanded": popupOpen,
                    "aria-controls": listId,
                    "aria-autocomplete": "list" as const,
                    ...(activeId !== undefined ? { "aria-activedescendant": activeId } : {}),
                  }
                : {})}
              onChange={(event) => {
                setPending(event.target.value);
                setOpen(true);
                // A new keystroke invalidates the cursor: the list under it has
                // changed, and keeping the index would highlight a different row
                // than the one the user was looking at.
                setActiveIndex(-1);
              }}
              onKeyDown={onKeyDown}
              onPaste={onPaste}
              onBlur={() => {
                // See the file header: a typed-but-not-committed address must
                // survive clicking Send.
                setOpen(false);
                setActiveIndex(-1);
                if (pending.trim() !== "") commit(pending);
              }}
            />
          </li>
        </ul>
        {trailing}

        {/*
          E7: the suggestion popup.

          Rendered only when there IS an index — see the `suggestions` prop for
          why an empty combobox is worse than none. Within that, the listbox is
          always present and merely `hidden` when there is nothing to show, so
          `aria-controls` never points at a missing element.
        */}
        {index.length > 0 && (
          <ul
            id={listId}
            role="listbox"
            aria-label={t("compose.suggestions.label")}
            className={styles.suggestions}
            hidden={!popupOpen}
          >
            {matches.map((suggestion, position) => (
              <li
                key={suggestion.email}
                id={`${listId}-${String(position)}`}
                role="option"
                aria-selected={position === activeIndex}
                className={[
                  styles.suggestion,
                  position === activeIndex ? styles.suggestionActive : "",
                ]
                  .filter(Boolean)
                  .join(" ")}
                /*
                 * `onMouseDown` with preventDefault, NOT onClick: a click fires
                 * after blur, and blur has already closed this popup and
                 * unmounted the row. Preventing the default also keeps focus in
                 * the input, which is what the APG pattern requires.
                 */
                onMouseDown={(event) => {
                  event.preventDefault();
                  acceptSuggestion(suggestion);
                }}
              >
                {suggestion.displayName !== undefined && (
                  <span className={styles.suggestionName}>{suggestion.displayName}</span>
                )}
                <span className={styles.suggestionEmail}>{suggestion.email}</span>
              </li>
            ))}
          </ul>
        )}
      </div>

      {invalidCount > 0 && (
        <p className={styles.error} id={`${inputId}-error`} role="alert">
          {format(
            "compose.addressInvalid",
            chips.find((chip) => !chip.isValid)?.email ?? "",
          )}
        </p>
      )}
      {/* Always in the DOM, so its content is ANNOUNCED when it changes — a
          live region inserted alongside its own text frequently is not. */}
      <span className="visually-hidden" aria-live="polite">
        {chips.length > 0 ? format("compose.recipientCount", chips.length) : ""}
      </span>
    </div>
  );
}
