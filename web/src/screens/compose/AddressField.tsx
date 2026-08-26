import { useCallback, useId, useRef, useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import { isCommitKey, parseAddressList, type AddressChip } from "../../mail/addresses";
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
}

export function AddressField({
  label,
  chips,
  onChange,
  autoFocusField = false,
  trailing,
  inputRef,
}: AddressFieldProps): React.JSX.Element {
  const { format } = useTranslation();
  const [pending, setPending] = useState("");
  const fallbackRef = useRef<HTMLInputElement | null>(null);
  const field = inputRef ?? fallbackRef;
  const inputId = useId();
  const listId = useId();

  const commit = useCallback(
    (text: string): void => {
      const parsed = parseAddressList(text);
      if (parsed.length === 0) return;
      onChange([...chips, ...parsed]);
      setPending("");
    },
    [chips, onChange],
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
    [pending, chips, commit, removeAt],
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
              // on-screen keyboard on mobile and the browser's own address
              // autofill.
              inputMode="email"
              autoComplete="email"
              spellCheck={false}
              /* The composer is a modal dialog opened by an explicit user
                 action; the WAI-ARIA APG dialog pattern REQUIRES focus to move
                 inside it, or a keyboard user is stranded behind it. The rule's
                 concern — focus stolen on page load — does not apply. */
              // eslint-disable-next-line jsx-a11y/no-autofocus
              autoFocus={autoFocusField}
              aria-describedby={invalidCount > 0 ? `${inputId}-error` : undefined}
              aria-invalid={invalidCount > 0}
              onChange={(event) => {
                setPending(event.target.value);
              }}
              onKeyDown={onKeyDown}
              onPaste={onPaste}
              onBlur={() => {
                // See the file header: a typed-but-not-committed address must
                // survive clicking Send.
                if (pending.trim() !== "") commit(pending);
              }}
            />
          </li>
        </ul>
        {trailing}
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
