import { useEffect, useRef } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import type { AddressChip } from "../../mail/addresses";
import styles from "./Composer.module.css";

/**
 * The composer's CHROME — the surface a draft is written on, and nothing about
 * the draft itself.
 *
 * # Why this exists
 *
 * Gmail writes a new message in a floating card, bottom-right (canon 07 §7),
 * and a REPLY at the foot of the conversation it answers, with the thread
 * still visible above it. Those are two hosts for one form: the fields, the
 * editor, the toolbar, the autosave, the undo window and the send path are
 * identical, and the only differences are the box around them and the way that
 * box is dismissed.
 *
 * Extracting the box rather than the form is the direction that keeps the
 * product honest. `Composer` holds a great deal of interlocked state — the
 * body editor's selection, the attachments' upload progress, the undo
 * countdown, the draft id the next save destroys — and the file header already
 * argues at length that none of it may be remounted. Splitting the FORM into
 * a second component would have meant lifting all of that or threading it, and
 * either would have put the send path at risk to move a border. Splitting the
 * BOX costs nothing: it holds no state at all.
 *
 * # The two hosts
 *
 * `floating` is exactly what shipped in E12 and is unchanged: a non-modal
 * `<dialog>` opened with `show()` (for the top layer, not for a focus trap —
 * see `Composer.module.css`), a title bar that toggles minimise, and the
 * minimise / maximise / close trio on the right.
 *
 * `inline` is a plain `<section>` in the document flow, mounted by the reader
 * at the foot of the conversation. It has no minimise and no maximise —
 * neither means anything for a box that is already part of the page — and its
 * header is Gmail's collapsed recipient line ("Responder a Ana Pérez ▾")
 * rather than a window title, because in an inline reply the question a person
 * asks of the header is "who is this going to", not "what is this window".
 * Its right-hand control is the POP-OUT, which hands the same draft to the
 * floating host.
 *
 * # What the inline host deliberately does NOT have
 *
 * A close ✕. Gmail's inline reply is dismissed by discarding it (the bin in
 * the footer, which this composer already has) or by sending it. A ✕ that
 * merely hid a box holding unsent words would be a fourth way to lose a draft,
 * and the one thing this component's family is most careful about is never
 * losing one.
 */

export type ComposerHost = "floating" | "inline";

export interface ComposerShellProps {
  readonly host: ComposerHost;
  /** The card's title — "Mensaje nuevo", "Responder", "Reenviar". */
  readonly title: string;
  /**
   * Who an inline reply is addressed to, for the collapsed header line.
   *
   * Ignored by the floating host, which shows the title instead. Empty (a
   * forward, which starts with no recipient) falls back to the title, so the
   * header never reads "Responder a" with nothing after it.
   */
  readonly recipients: readonly AddressChip[];
  /**
   * Whether the inline header's recipient line is expanded into the real
   * address fields below. Gmail collapses them and expands on click; the
   * fields themselves live in the form, so the shell only reports the press.
   */
  readonly fieldsExpanded: boolean;
  readonly onToggleFields: () => void;
  /** Floating only: the card's size state and the control that changes it. */
  readonly cardSize: "normal" | "minimized" | "maximized";
  readonly onCardSize: (next: "normal" | "minimized" | "maximized") => void;
  /** Floating only: the ✕, which flushes the autosave before it closes. */
  readonly onClose: () => void;
  /** Inline only: moves this draft to the floating host, content intact. */
  readonly onPopOut?: (() => void) | undefined;
  /**
   * Bound to the shell's outer element, so every key pressed inside the
   * composer reaches it and nothing outside can.
   *
   * A `<form>` is not an interactive element, so a React `onKeyDown` on it is
   * a jsx-a11y error and the rule is right — hence the listener is attached
   * imperatively to the boundary element here, exactly as it was on the
   * `<dialog>` before this split.
   */
  readonly onKeyDown: (event: KeyboardEvent) => void;
  /** Floating only: Escape, intercepted so the draft is flushed first. */
  readonly onCancel: (event: React.SyntheticEvent<HTMLDialogElement>) => void;
  readonly onSubmit: () => void;
  readonly children: React.ReactNode;
  /** Rendered outside the form — the discard confirmation. */
  readonly trailing?: React.ReactNode;
}

export function ComposerShell({
  host,
  title,
  recipients,
  fieldsExpanded,
  onToggleFields,
  cardSize,
  onCardSize,
  onClose,
  onPopOut,
  onKeyDown,
  onCancel,
  onSubmit,
  children,
  trailing,
}: ComposerShellProps): React.JSX.Element {
  const { t, format } = useTranslation();
  const boundaryRef = useRef<HTMLElement | null>(null);

  /*
   * `show()`, not `showModal()` — floating host only (canon 07 §7).
   *
   * The card is NON-MODAL: the mail behind it stays live, which is what lets
   * someone look up an address or re-read the message they are answering
   * without abandoning the draft. It stays a `<dialog>` for the TOP LAYER,
   * which is the one property of the pair worth keeping — a hand-rolled
   * overlay eventually loses a `z-index` argument with a menu.
   *
   * `show()` focuses nothing by itself, so the composer's own autofocus on the
   * first empty field is what puts the caret where the user expects it.
   */
  useEffect(() => {
    if (host !== "floating") return undefined;
    const dialog = boundaryRef.current;
    if (!(dialog instanceof HTMLDialogElement)) return undefined;
    if (!dialog.open) dialog.show();
    return () => {
      if (dialog.open) dialog.close();
    };
  }, [host]);

  useEffect(() => {
    const element = boundaryRef.current;
    if (element === null) return undefined;
    element.addEventListener("keydown", onKeyDown);
    return () => {
      element.removeEventListener("keydown", onKeyDown);
    };
  }, [onKeyDown]);

  /*
   * Scroll the inline box into view when it appears.
   *
   * It mounts at the FOOT of a conversation the user may have read only the
   * top of, so without this the reply opens off screen and pressing Reply
   * looks like it did nothing. The floating host needs no equivalent: it is
   * fixed to the corner and is always already in view.
   *
   * `block: "nearest"` rather than `center`: if the box is already visible,
   * nothing moves, and the reader keeps the position the user scrolled to.
   */
  useEffect(() => {
    if (host !== "inline") return;
    boundaryRef.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [host]);

  const form = (
    <form
      className={styles.form}
      onSubmit={(event) => {
        event.preventDefault();
        onSubmit();
      }}
    >
      {host === "floating" ? (
        <FloatingHeader
          title={title}
          cardSize={cardSize}
          onCardSize={onCardSize}
          onClose={onClose}
        />
      ) : (
        <header className={styles.inlineHeader}>
          {/*
            Gmail's collapsed recipient line. It is a BUTTON because pressing
            it is what reveals the real Para/Cc/Cco fields below — a person
            reading "Responder a Ana Pérez ▾" and clicking the caret expects
            the addresses to open, and a div with a handler would deny that to
            a keyboard.
          */}
          <button
            type="button"
            className={styles.inlineRecipients}
            onClick={onToggleFields}
            aria-expanded={fieldsExpanded}
          >
            <span>
              {recipients.length === 0
                ? title
                : format("compose.inlineTo", summarize(recipients))}
            </span>
            <span className={styles.inlineCaret} aria-hidden="true">
              ▾
            </span>
          </button>

          {onPopOut !== undefined && (
            <button
              type="button"
              className={styles.iconButton}
              onClick={onPopOut}
              aria-label={t("compose.popOut")}
              title={t("compose.popOut")}
            >
              <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
                {/* The arrow OUT of a frame: this leaves the conversation. */}
                <path d="M11 4h5v5M16 4l-6.5 6.5" />
                <path d="M15 12v3.4a1.2 1.2 0 0 1-1.2 1.2H4.6a1.2 1.2 0 0 1-1.2-1.2V6.2A1.2 1.2 0 0 1 4.6 5H8" />
              </svg>
            </button>
          )}
        </header>
      )}

      {children}
    </form>
  );

  if (host === "inline") {
    return (
      <section
        ref={boundaryRef}
        className={styles.inline}
        aria-label={title}
      >
        {form}
        {trailing}
      </section>
    );
  }

  return (
    <dialog
      ref={boundaryRef as React.RefObject<HTMLDialogElement>}
      className={[
        styles.dialog,
        cardSize === "minimized" ? styles.minimized : "",
        cardSize === "maximized" ? styles.maximized : "",
      ]
        .filter(Boolean)
        .join(" ")}
      aria-label={title}
      onCancel={onCancel}
    >
      {form}
      {/*
        E11: the discard confirmation. Inside the composer's own <dialog>, and
        correct there: a nested `showModal()` goes into the top layer ABOVE its
        parent, so it is not covered by the composer it is asking about.
      */}
      {trailing}
    </dialog>
  );
}

/**
 * The floating card's title bar — unchanged from E12/B6.
 *
 * The BAR itself toggles minimise on click, which is what makes a collapsed
 * strip expand again by clicking anywhere on it rather than by finding a 2rem
 * button. It is a <button> for that reason and not a div with a handler: it is
 * genuinely a control, and making it one gives it keyboard operation and a
 * role for free.
 */
function FloatingHeader({
  title,
  cardSize,
  onCardSize,
  onClose,
}: {
  readonly title: string;
  readonly cardSize: "normal" | "minimized" | "maximized";
  readonly onCardSize: (next: "normal" | "minimized" | "maximized") => void;
  readonly onClose: () => void;
}): React.JSX.Element {
  const { t } = useTranslation();
  const minimizeLabel = cardSize === "minimized" ? t("compose.expand") : t("compose.minimize");
  const toggleMinimize = (): void => {
    onCardSize(cardSize === "minimized" ? "normal" : "minimized");
  };

  return (
    <header className={styles.header}>
      <button
        type="button"
        className={styles.titleBar}
        onClick={toggleMinimize}
        /* The heading's text is the accessible name; what the press DOES is
           the label, so the two together read as "Mensaje nuevo, minimise". */
        aria-label={`${title} — ${minimizeLabel}`}
        aria-expanded={cardSize !== "minimized"}
      >
        <span className={styles.title}>{title}</span>
      </button>

      <div className={styles.headerActions}>
        <button
          type="button"
          className={styles.iconButton}
          onClick={toggleMinimize}
          aria-label={minimizeLabel}
          title={minimizeLabel}
        >
          <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round">
            <path d="M5 14h10" />
          </svg>
        </button>
        <button
          type="button"
          className={styles.iconButton}
          onClick={() => {
            onCardSize(cardSize === "maximized" ? "normal" : "maximized");
          }}
          aria-label={cardSize === "maximized" ? t("compose.restore") : t("compose.maximize")}
          title={cardSize === "maximized" ? t("compose.restore") : t("compose.maximize")}
          aria-pressed={cardSize === "maximized"}
        >
          {cardSize === "maximized" ? (
            <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
              {/* Arrows pointing IN: this collapses back to the card. */}
              <path d="M9 4v5H4M11 16v-5h5" />
            </svg>
          ) : (
            <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
              {/* Arrows pointing OUT: this grows to the full panel. */}
              <path d="M12 4h4v4M8 16H4v-4" />
            </svg>
          )}
        </button>
        <button
          type="button"
          className={styles.iconButton}
          onClick={onClose}
          aria-label={t("compose.close")}
          title={t("compose.close")}
        >
          <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round">
            <path d="M5.5 5.5l9 9M14.5 5.5l-9 9" />
          </svg>
        </button>
      </div>
    </header>
  );
}

/**
 * How the collapsed header names its recipients.
 *
 * The first name, and a count for the rest — Gmail's own shape. A full list
 * would defeat the collapse, and the name rather than the address because that
 * is what the person writing the reply is thinking of.
 */
function summarize(recipients: readonly AddressChip[]): string {
  const first = recipients[0];
  if (first === undefined) return "";
  const name = first.name ?? first.email;
  if (recipients.length === 1) return name;
  return `${name} +${String(recipients.length - 1)}`;
}
