import { useCallback, useEffect, useId, useRef, useState } from "react";

import { useTranslation } from "../i18n/I18nProvider";
import styles from "./ModalDialog.module.css";

/**
 * The app's own confirm and prompt (E11).
 *
 * # Why the native ones had to go
 *
 * `window.confirm` and `window.prompt` were still deciding three things: the
 * destructive-delete confirmation, the discard-draft confirmation, and the
 * link URL in the rich-text editor. They are wrong for all three:
 *
 * - **Unstyleable.** They render in the browser's chrome, in the OS font, with
 *   the OS button order — in the middle of a product whose whole claim is
 *   Gmail-class polish. Nothing about them can be made to match.
 * - **Inconsistent.** Chrome, Firefox and Safari lay them out differently and
 *   put the buttons in different orders, so "the second button" is not a thing
 *   a user can learn.
 * - **No validation affordance.** `window.prompt` can only accept or reject a
 *   string after the fact. The link prompt could not show "that is not a valid
 *   URL" next to the field the way any real form does; it had to swallow the
 *   input and surface an error somewhere else entirely.
 * - **They block the main thread**, and browsers increasingly suppress them —
 *   in a cross-origin iframe they may not appear at all, which turns a
 *   confirmation into a silent no-op. For a DESTRUCTIVE action that is a
 *   correctness bug, not a styling complaint.
 *
 * # The focus discipline, copied from SettingsDialog
 *
 * A real `<dialog>` opened with `showModal()`, which gives three things for
 * free and correctly: the rest of the page becomes inert (not merely covered),
 * focus is trapped for as long as it is open, and Escape closes it. Focus
 * return is done explicitly on close, because not every browser restores it.
 */

interface BaseProps {
  readonly isOpen: boolean;
  /** The question. Already translated by the caller — it is usually formatted. */
  readonly message: string;
  /** Optional title; falls back to the app's generic "Confirm". */
  readonly title?: string | undefined;
  readonly onCancel: () => void;
}

/**
 * Shared `<dialog>` mechanics: open/close, focus return, backdrop dismissal.
 *
 * Extracted rather than duplicated because these are the same sixty lines the
 * shortcuts sheet and the settings sheet already each own once — a third and
 * fourth copy would have drifted the first time one was fixed.
 */
function useModalDialog(
  isOpen: boolean,
  onCancel: () => void,
): React.MutableRefObject<HTMLDialogElement | null> {
  const dialogRef = useRef<HTMLDialogElement | null>(null);
  const returnFocusRef = useRef<HTMLElement | null>(null);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) return;
    if (isOpen && !dialog.open) {
      returnFocusRef.current =
        document.activeElement instanceof HTMLElement ? document.activeElement : null;
      dialog.showModal();
    } else if (!isOpen && dialog.open) {
      dialog.close();
      returnFocusRef.current?.focus();
    }
  }, [isOpen]);

  /*
   * Escape and the backdrop both dismiss, and both must reach the caller's
   * `onCancel` — a dialog that closes visually while the caller still believes
   * it is open is how a screen ends up with an invisible modal blocking input.
   *
   * `cancel` rather than `close`, because `close` also fires on our own
   * programmatic close above and would re-enter the caller's handler.
   */
  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) return undefined;
    const handleCancel = (event: Event): void => {
      event.preventDefault();
      returnFocusRef.current?.focus();
      onCancel();
    };
    const handleBackdrop = (event: MouseEvent): void => {
      if (event.target === dialog) onCancel();
    };
    dialog.addEventListener("cancel", handleCancel);
    dialog.addEventListener("click", handleBackdrop);
    return () => {
      dialog.removeEventListener("cancel", handleCancel);
      dialog.removeEventListener("click", handleBackdrop);
    };
  }, [onCancel]);

  return dialogRef;
}

export interface ConfirmDialogProps extends BaseProps {
  readonly onConfirm: () => void;
  /** The confirming button's label; defaults to a generic "Confirm". */
  readonly confirmLabel?: string | undefined;
  /**
   * Styles the confirming button as destructive.
   *
   * Deleting forever and emptying the Trash look different from renaming a
   * label, because the cost of a mis-click is different.
   */
  readonly destructive?: boolean;
}

export function ConfirmDialog({
  isOpen,
  message,
  title,
  confirmLabel,
  destructive = false,
  onConfirm,
  onCancel,
}: ConfirmDialogProps): React.JSX.Element {
  const { t } = useTranslation();
  const dialogRef = useModalDialog(isOpen, onCancel);
  const titleId = useId();

  return (
    <dialog ref={dialogRef} className={styles.dialog} aria-labelledby={titleId}>
      <div className={styles.content}>
        <h2 className={styles.title} id={titleId}>
          {title ?? t("dialog.confirmTitle")}
        </h2>
        <p className={styles.message}>{message}</p>
        <div className={styles.actions}>
          <button type="button" className={styles.secondary} onClick={onCancel}>
            {t("dialog.cancel")}
          </button>
          {/*
            The confirming button is autofocused: the user asked for this
            action and the dialog is a checkpoint, so Enter should complete it.
            The APG dialog pattern requires focus INSIDE the dialog on open —
            the lint rule's concern is focus stolen on page load, which a modal
            opened by an explicit user action is not.
          */}
          <button
            type="button"
            className={destructive ? styles.destructive : styles.primary}
            onClick={onConfirm}
            // eslint-disable-next-line jsx-a11y/no-autofocus
            autoFocus
          >
            {confirmLabel ?? t("dialog.confirm")}
          </button>
        </div>
      </div>
    </dialog>
  );
}

/**
 * A promise-shaped confirm, so callers keep the linear code they had.
 *
 * `window.confirm` returned a boolean synchronously, which is exactly why the
 * call sites read well: `if (!confirm(...)) return;` in the middle of an async
 * action. A React dialog is inherently asynchronous — it renders, then the
 * user answers — and rewriting every caller into a state machine with a
 * "pending action" field would be a far larger and more error-prone change
 * than the one this epic is making.
 *
 * So the promise is the bridge: `if (!(await confirm(message))) return;` is
 * the same shape, one `await` longer, and the dialog it renders is ours.
 *
 * Returns the element to render plus the asker. The element must be mounted
 * for the promise to ever settle.
 */
export interface ConfirmRequest {
  readonly message: string;
  readonly title?: string | undefined;
  readonly confirmLabel?: string | undefined;
  readonly destructive?: boolean;
}

export function useConfirm(): {
  readonly confirm: (request: ConfirmRequest) => Promise<boolean>;
  readonly dialog: React.JSX.Element;
} {
  const [request, setRequest] = useState<ConfirmRequest | undefined>(undefined);
  const resolveRef = useRef<((answer: boolean) => void) | undefined>(undefined);

  const confirm = useCallback((next: ConfirmRequest): Promise<boolean> => {
    // A second ask while one is live resolves the first as cancelled, so no
    // caller is left awaiting a promise that can never settle.
    resolveRef.current?.(false);
    setRequest(next);
    return new Promise<boolean>((resolve) => {
      resolveRef.current = resolve;
    });
  }, []);

  const settle = useCallback((answer: boolean): void => {
    const resolve = resolveRef.current;
    resolveRef.current = undefined;
    setRequest(undefined);
    resolve?.(answer);
  }, []);

  /*
   * Nothing is rendered until there is something to ask.
   *
   * An always-mounted `<dialog>` is not inert: it still participates in the
   * document, and a host that renders this handle near the top of its tree
   * gets a closed dialog sitting in front of its own content. That is not
   * theoretical — mounting it unconditionally inside the settings sheet broke
   * that sheet's search box, because the element intercepted the interaction
   * before the input ever saw it.
   */
  const dialog =
    request === undefined ? (
      <></>
    ) : (
      <ConfirmDialog
        isOpen
        message={request.message}
        title={request.title}
        confirmLabel={request.confirmLabel}
        destructive={request.destructive ?? false}
        onConfirm={() => {
          settle(true);
        }}
        onCancel={() => {
          settle(false);
        }}
      />
    );

  return { confirm, dialog };
}

export interface PromptDialogProps extends BaseProps {
  /** Called with the accepted value; never called with an invalid one. */
  readonly onSubmit: (value: string) => void;
  readonly placeholder?: string | undefined;
  readonly initialValue?: string | undefined;
  readonly submitLabel?: string | undefined;
  /**
   * Validates as the user types, returning an error message or `undefined`.
   *
   * This is the thing `window.prompt` structurally cannot do, and the reason
   * the link prompt was the worst of the three natives it replaces: the field
   * can now say "http:// or https:// only" NEXT TO the input, before the user
   * commits, instead of swallowing the value and reporting failure elsewhere.
   */
  readonly validate?: ((value: string) => string | undefined) | undefined;
}

export function PromptDialog({
  isOpen,
  message,
  title,
  placeholder,
  initialValue = "",
  submitLabel,
  validate,
  onSubmit,
  onCancel,
}: PromptDialogProps): React.JSX.Element {
  const { t } = useTranslation();
  const dialogRef = useModalDialog(isOpen, onCancel);
  const titleId = useId();
  const inputId = useId();
  const errorId = useId();
  const [value, setValue] = useState(initialValue);
  const [error, setError] = useState<string | undefined>(undefined);
  const [touched, setTouched] = useState(false);

  // Each opening starts clean: a dialog that reopens showing the last
  // rejection is telling the user about a mistake they already abandoned.
  useEffect(() => {
    if (isOpen) {
      setValue(initialValue);
      setError(undefined);
      setTouched(false);
    }
  }, [isOpen, initialValue]);

  const submit = useCallback((): void => {
    const problem = validate?.(value);
    setTouched(true);
    if (problem !== undefined) {
      setError(problem);
      return;
    }
    if (value.trim() === "") return;
    onSubmit(value);
  }, [validate, value, onSubmit]);

  return (
    <dialog ref={dialogRef} className={styles.dialog} aria-labelledby={titleId}>
      <form
        className={styles.content}
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <h2 className={styles.title} id={titleId}>
          {title ?? t("dialog.promptTitle")}
        </h2>
        <label className={styles.label} htmlFor={inputId}>
          {message}
        </label>
        <input
          id={inputId}
          className={styles.input}
          type="text"
          value={value}
          placeholder={placeholder}
          // eslint-disable-next-line jsx-a11y/no-autofocus
          autoFocus
          aria-invalid={error !== undefined}
          aria-describedby={error !== undefined ? errorId : undefined}
          onChange={(event) => {
            setValue(event.target.value);
            // Re-validate only once the user has already been told off, so the
            // error clears as they fix it but never appears mid-first-word.
            if (touched) setError(validate?.(event.target.value));
          }}
        />
        {error !== undefined && (
          <p className={styles.error} id={errorId} role="alert">
            {error}
          </p>
        )}
        <div className={styles.actions}>
          <button type="button" className={styles.secondary} onClick={onCancel}>
            {t("dialog.cancel")}
          </button>
          <button type="submit" className={styles.primary} disabled={value.trim() === ""}>
            {submitLabel ?? t("dialog.ok")}
          </button>
        </div>
      </form>
    </dialog>
  );
}
