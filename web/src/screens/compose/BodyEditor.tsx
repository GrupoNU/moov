import { useCallback, useEffect, useRef, useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import { sanitizeEmailHtml } from "../../mail/html/sanitize";
import {
  applyCommand,
  applyLink,
  isCommandActive,
  insertPlainText,
  normalizeLinkUrl,
  prepareRichText,
  RICH_TEXT_BUTTONS,
  type RichTextCommand,
} from "../../mail/richtext";
import styles from "./BodyEditor.module.css";

/**
 * The message body: a plain `<textarea>` or a `contentEditable` rich surface.
 *
 * # The security rule this component exists to enforce
 *
 * **Everything the composer produces is untrusted input to the next reader.**
 * A reply quotes the original message's HTML, and that HTML came off the wire.
 * So the initial value of the rich surface is put through the SAME P2b
 * sanitizer the reading pane uses (`mail/html/sanitize.ts`) before it is ever
 * assigned to `innerHTML`, and the value handed back to the parent on every
 * change is sanitized again on the way out.
 *
 * That is two sanitizations of the same bytes, deliberately:
 *
 *   - **In** protects THIS document. Assigning unsanitized quoted HTML to a
 *     live `contentEditable` in the app's own origin is a stored XSS with full
 *     session access — strictly worse than the reading pane's iframe, which at
 *     least has an opaque origin and `default-src 'none'`.
 *   - **Out** protects the next reader and our own outbound message. The
 *     surface is a live DOM the user edits and the browser mutates; what comes
 *     out of it is not what went in.
 *
 * `dangerouslySetInnerHTML` is used exactly once, on the initial value only,
 * and only with a string that has just come back from the sanitizer. React
 * cannot own this subtree afterwards — a `contentEditable` React re-renders on
 * every keystroke would move the caret to the start of the document — so the
 * DOM node is written once and then read from, which is the standard
 * uncontrolled-editor pattern.
 *
 * # Why plain text is a first-class mode, not a fallback
 *
 * Plenty of mail is better as text: a short reply, a message to a list, a
 * paste of a log. Offering the choice costs one toggle and makes the HTML path
 * optional rather than imposed.
 */

export interface BodyEditorProps {
  /** True for the rich surface, false for the textarea. */
  readonly isRich: boolean;
  readonly onToggleRich: (isRich: boolean) => void;
  /** The plain-text body (the source of truth in text mode). */
  readonly text: string;
  readonly onTextChange: (text: string) => void;
  /** The HTML body (the source of truth in rich mode). */
  readonly html: string;
  readonly onHtmlChange: (html: string) => void;
  /**
   * Changes to this string re-seed the rich surface from scratch. It is the
   * message identity — a new reply, a resumed draft — NOT the html itself,
   * which would re-seed on every keystroke and destroy the caret.
   */
  readonly seedKey: string;
  readonly bodyRef?: React.MutableRefObject<HTMLElement | null>;
}

export function BodyEditor({
  isRich,
  onToggleRich,
  text,
  onTextChange,
  html,
  onHtmlChange,
  seedKey,
  bodyRef,
}: BodyEditorProps): React.JSX.Element {
  const { t } = useTranslation();
  const editableRef = useRef<HTMLDivElement | null>(null);
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const [activeCommands, setActiveCommands] = useState<readonly RichTextCommand[]>([]);
  const [linkError, setLinkError] = useState<string | undefined>(undefined);

  /*
   * Seeding. Runs when the MESSAGE changes, never on every render: writing
   * innerHTML while the user is typing moves the caret to offset 0, which
   * presents as "the composer types backwards".
   *
   * The html is sanitized on the way IN — see the file header. A reply's
   * quoted body is attacker-controlled markup, and this is the app's own DOM.
   */
  /*
   * The html to seed with, held in a ref rather than named as a dependency.
   *
   * This is the honest way to express "read the current value, but do not
   * re-run when it changes": listing `html` in the dependency array would
   * re-seed the surface on every keystroke, which writes innerHTML while the
   * user is typing and moves the caret to offset 0 — the composer would appear
   * to type backwards. Suppressing the lint rule instead would leave the same
   * hazard with a comment over it; a ref makes the dependency genuinely absent.
   */
  const htmlToSeed = useRef(html);
  htmlToSeed.current = html;

  useEffect(() => {
    if (!isRich) return;
    const element = editableRef.current;
    if (element === null) return;
    prepareRichText();
    const { html: safe } = sanitizeEmailHtml(htmlToSeed.current, {
      allowRemoteImages: false,
    });
    element.innerHTML = safe;
  }, [seedKey, isRich]);

  /** Reads the surface, sanitizes it, and reports it upward. */
  const publish = useCallback((): void => {
    const element = editableRef.current;
    if (element === null) return;
    // Sanitized on the way OUT: the surface is a live DOM the user edits and
    // the browser mutates, and what comes out is not what went in.
    const { html: safe } = sanitizeEmailHtml(element.innerHTML, {
      allowRemoteImages: true,
      // In the composer's own output there is no proxy to route through and
      // nothing to hide from: an image the USER inserted is theirs. The
      // reading pane's blocking posture applies to mail one RECEIVES.
      proxiedUrlFor: (url) => url,
    });
    onHtmlChange(safe);
  }, [onHtmlChange]);

  const refreshActive = useCallback((): void => {
    setActiveCommands(
      RICH_TEXT_BUTTONS.map((button) => button.command).filter(isCommandActive),
    );
  }, []);

  const runCommand = useCallback(
    (command: RichTextCommand): void => {
      editableRef.current?.focus();
      applyCommand(command);
      refreshActive();
      publish();
    },
    [publish, refreshActive],
  );

  /*
   * Ctrl/Cmd+B, I and U inside the editable. These are the ONLY place the app
   * handles a modified key — the global shortcut layer ignores every event
   * carrying Ctrl/Meta/Alt precisely so the browser keeps its shortcuts, and
   * these three are the browser's own editing shortcuts, which a
   * contentEditable is expected to implement.
   */
  const onEditableKeyDown = useCallback(
    (event: React.KeyboardEvent<HTMLDivElement>): void => {
      if (!(event.ctrlKey || event.metaKey) || event.altKey) return;
      const command: RichTextCommand | undefined =
        event.key === "b" ? "bold" : event.key === "i" ? "italic" : event.key === "u" ? "underline" : undefined;
      if (command === undefined) return;
      event.preventDefault();
      runCommand(command);
    },
    [runCommand],
  );

  /*
   * Paste is intercepted and re-inserted as TEXT.
   *
   * A native paste into a contentEditable inserts the clipboard's HTML
   * flavour, which is whatever the source page put there — including markup a
   * hostile page placed on the clipboard on purpose. Losing formatting on
   * paste is a real cost; authoring attacker-chosen markup into a message the
   * user sends under their own name is a worse one.
   */
  const onPaste = useCallback(
    (event: React.ClipboardEvent<HTMLDivElement>): void => {
      const plain = event.clipboardData.getData("text/plain");
      event.preventDefault();
      insertPlainText(plain);
      publish();
    },
    [publish],
  );

  const onInsertLink = useCallback((): void => {
    const raw = window.prompt(t("compose.linkPrompt"));
    if (raw === null) return;
    const normalized = normalizeLinkUrl(raw);
    if (normalized === undefined) {
      setLinkError(t("compose.linkInvalid"));
      return;
    }
    setLinkError(undefined);
    editableRef.current?.focus();
    applyLink(normalized);
    publish();
  }, [t, publish]);

  return (
    <div className={styles.wrapper}>
      <div className={styles.toolbar} role="toolbar" aria-label={t("compose.richText")}>
        <div className={styles.modeGroup}>
          <button
            type="button"
            className={[styles.modeButton, isRich ? "" : styles.modeActive].filter(Boolean).join(" ")}
            aria-pressed={!isRich}
            onClick={() => {
              onToggleRich(false);
            }}
          >
            {t("compose.plainText")}
          </button>
          <button
            type="button"
            className={[styles.modeButton, isRich ? styles.modeActive : ""].filter(Boolean).join(" ")}
            aria-pressed={isRich}
            onClick={() => {
              onToggleRich(true);
            }}
          >
            {t("compose.richText")}
          </button>
        </div>

        {isRich && (
          <div className={styles.commands}>
            {RICH_TEXT_BUTTONS.map(({ command, labelKey, shortcut }) => (
              <button
                key={command}
                type="button"
                className={[
                  styles.commandButton,
                  activeCommands.includes(command) ? styles.commandActive : "",
                ]
                  .filter(Boolean)
                  .join(" ")}
                /* aria-pressed carries the state; the visual highlight alone
                   would be invisible to a screen reader. */
                aria-pressed={activeCommands.includes(command)}
                aria-label={t(labelKey)}
                title={shortcut === undefined ? t(labelKey) : `${t(labelKey)} (${shortcut})`}
                /* mousedown, not click: click fires after the editable has
                   already lost focus and the selection has collapsed, so the
                   command would apply to nothing. */
                onMouseDown={(event) => {
                  event.preventDefault();
                }}
                onClick={() => {
                  runCommand(command);
                }}
              >
                <CommandIcon command={command} />
              </button>
            ))}
            <button
              type="button"
              className={styles.commandButton}
              aria-label={t("compose.link")}
              title={t("compose.link")}
              onMouseDown={(event) => {
                event.preventDefault();
              }}
              onClick={onInsertLink}
            >
              <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" aria-hidden="true" focusable="false">
                <path d="M8.5 11.5a3 3 0 0 0 4.2 0l2.3-2.3a3 3 0 0 0-4.2-4.2l-1 1" />
                <path d="M11.5 8.5a3 3 0 0 0-4.2 0L5 10.8a3 3 0 0 0 4.2 4.2l1-1" />
              </svg>
            </button>
          </div>
        )}
      </div>

      {linkError !== undefined && (
        <p className={styles.linkError} role="alert">
          {linkError}
        </p>
      )}

      {isRich ? (
        <div
          ref={(node) => {
            editableRef.current = node;
            if (bodyRef !== undefined) bodyRef.current = node;
          }}
          className={styles.editable}
          contentEditable
          suppressContentEditableWarning
          role="textbox"
          aria-multiline="true"
          aria-label={t("compose.body")}
          tabIndex={0}
          onInput={publish}
          onKeyUp={refreshActive}
          onMouseUp={refreshActive}
          onKeyDown={onEditableKeyDown}
          onPaste={onPaste}
          onBlur={publish}
        />
      ) : (
        <textarea
          ref={(node) => {
            textareaRef.current = node;
            if (bodyRef !== undefined) bodyRef.current = node;
          }}
          className={styles.textarea}
          aria-label={t("compose.body")}
          value={text}
          onChange={(event) => {
            onTextChange(event.target.value);
          }}
          spellCheck
        />
      )}
    </div>
  );
}

/** The toolbar icons. Inline SVG so no font or network request is involved. */
function CommandIcon({ command }: { readonly command: RichTextCommand }): React.JSX.Element {
  switch (command) {
    case "bold":
      return (
        <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false">
          <text x="10" y="15" textAnchor="middle" fontSize="13" fontWeight="800" fill="currentColor">
            B
          </text>
        </svg>
      );
    case "italic":
      return (
        <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false">
          <text x="10" y="15" textAnchor="middle" fontSize="13" fontStyle="italic" fill="currentColor">
            I
          </text>
        </svg>
      );
    case "underline":
      return (
        <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false">
          <text x="10" y="14" textAnchor="middle" fontSize="13" fill="currentColor">
            U
          </text>
          <path d="M6 16.5h8" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
        </svg>
      );
    case "bulletList":
      return (
        <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" aria-hidden="true" focusable="false">
          <circle cx="4" cy="6" r="1.1" fill="currentColor" stroke="none" />
          <circle cx="4" cy="10" r="1.1" fill="currentColor" stroke="none" />
          <circle cx="4" cy="14" r="1.1" fill="currentColor" stroke="none" />
          <path d="M8 6h8M8 10h8M8 14h8" />
        </svg>
      );
    case "orderedList":
      return (
        <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" aria-hidden="true" focusable="false">
          <path d="M8 6h8M8 10h8M8 14h8" />
          <text x="2" y="8" fontSize="6" fill="currentColor" stroke="none">
            1
          </text>
          <text x="2" y="12" fontSize="6" fill="currentColor" stroke="none">
            2
          </text>
          <text x="2" y="16" fontSize="6" fill="currentColor" stroke="none">
            3
          </text>
        </svg>
      );
  }
}
