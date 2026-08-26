/**
 * The keyboard map (P2 deliverable 7).
 *
 * ADR §6: "Navegación completa por teclado con el vocabulario Gmail (j/k, e,
 * r, #, /, c) — COPIADO, no inventado." The vocabulary below is Gmail's,
 * verbatim, because a mail power user's fingers already know it and any
 * cleverness we add is a tax they pay forever.
 *
 * # Why resolution is a pure function
 *
 * The hard parts of a keyboard layer are not the bindings — they are the
 * refusals: not firing inside a text field, not stealing browser shortcuts,
 * and handling the `g` chord's timeout. Each of those is a branch, and a
 * branch in an event handler is a branch nobody tests. Here the whole decision
 * is `resolveShortcut(event, state) -> Action | undefined`, which a test can
 * enumerate.
 */

/** Everything the keyboard can ask the app to do. */
export type ShortcutAction =
  | { readonly kind: "next" }
  | { readonly kind: "previous" }
  | { readonly kind: "open" }
  | { readonly kind: "back" }
  | { readonly kind: "focusSearch" }
  | { readonly kind: "archive" }
  | { readonly kind: "delete" }
  | { readonly kind: "toggleRead" }
  | { readonly kind: "toggleFlag" }
  | { readonly kind: "goToMailbox"; readonly role: string }
  | { readonly kind: "help" }
  | { readonly kind: "closeOverlay" }
  // P3: composition. `c` is Gmail's compose key; `r`, `a` and `f` are its
  // reply, reply-all and forward — copied verbatim, per ADR §6.
  | { readonly kind: "compose" }
  | { readonly kind: "reply" }
  | { readonly kind: "replyAll" }
  | { readonly kind: "forward" }
  | { readonly kind: "selectRow" };

/** The chord state: `g` has been pressed and the app is awaiting its second key. */
export interface KeyboardState {
  /** True while a `g` prefix is live. */
  readonly pendingG: boolean;
}

export const INITIAL_KEYBOARD_STATE: KeyboardState = { pendingG: false };

/**
 * How long a `g` prefix stays live.
 *
 * Gmail uses roughly a second. Long enough that `g` then `i` is comfortable as
 * two deliberate presses; short enough that a stray `g` does not swallow the
 * next real keystroke and leave the user typing into a void.
 */
export const CHORD_TIMEOUT_MS = 1200;

/** The subset of a KeyboardEvent this module needs — so tests need no DOM. */
export interface KeyLike {
  readonly key: string;
  readonly ctrlKey: boolean;
  readonly metaKey: boolean;
  readonly altKey: boolean;
  readonly shiftKey: boolean;
  readonly target?: EventTarget | null;
}

/**
 * True when the event originated somewhere the user is typing.
 *
 * A mail client whose `e` archives a message while you are writing one is
 * broken, so this check comes before every binding. `isContentEditable`
 * matters as much as the tags: a rich-text composer (P3) is a `div`, and
 * forgetting it is how the compose window loses characters.
 */
export function isTypingTarget(target: EventTarget | null | undefined): boolean {
  if (target === null || target === undefined) return false;
  const element = target as Partial<HTMLElement> & { tagName?: string };
  const tag = element.tagName?.toUpperCase();
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return true;
  if (element.isContentEditable === true) return true;
  return false;
}

/**
 * Resolves a key event into an action, given the chord state.
 *
 * Returns `undefined` when the app should not act — which includes every key
 * it does not bind, so the browser keeps its own shortcuts. The rule that
 * makes that safe: **any event carrying Ctrl, Meta or Alt is ignored
 * outright.** Ctrl+R must reload, Cmd+K must reach the browser, and a mail
 * client that eats them is the reason people distrust web apps' keyboard
 * handling.
 */
export function resolveShortcut(
  event: KeyLike,
  state: KeyboardState = INITIAL_KEYBOARD_STATE,
): { readonly action: ShortcutAction | undefined; readonly nextState: KeyboardState } {
  // Escape works everywhere, including inside the search field — it is how you
  // get OUT of a text field, so it must be handled before the typing guard.
  if (event.key === "Escape") {
    return { action: { kind: "closeOverlay" }, nextState: INITIAL_KEYBOARD_STATE };
  }

  if (event.ctrlKey || event.metaKey || event.altKey) {
    return { action: undefined, nextState: state };
  }

  if (isTypingTarget(event.target)) {
    return { action: undefined, nextState: INITIAL_KEYBOARD_STATE };
  }

  // The `g` chord's second key. Checked first so a live prefix cannot be
  // shadowed by a single-key binding of the same letter.
  if (state.pendingG) {
    const role = CHORD_TARGETS[event.key.toLowerCase()];
    return {
      action: role !== undefined ? { kind: "goToMailbox", role } : undefined,
      nextState: INITIAL_KEYBOARD_STATE,
    };
  }

  switch (event.key) {
    case "g":
      return { action: undefined, nextState: { pendingG: true } };

    case "j":
    case "ArrowDown":
      return { action: { kind: "next" }, nextState: INITIAL_KEYBOARD_STATE };

    case "k":
    case "ArrowUp":
      return { action: { kind: "previous" }, nextState: INITIAL_KEYBOARD_STATE };

    case "Enter":
    case "o":
      return { action: { kind: "open" }, nextState: INITIAL_KEYBOARD_STATE };

    case "u":
      return { action: { kind: "back" }, nextState: INITIAL_KEYBOARD_STATE };

    case "/":
      return { action: { kind: "focusSearch" }, nextState: INITIAL_KEYBOARD_STATE };

    case "e":
      return { action: { kind: "archive" }, nextState: INITIAL_KEYBOARD_STATE };

    case "#":
      return { action: { kind: "delete" }, nextState: INITIAL_KEYBOARD_STATE };

    // Gmail's read/unread toggles are the `Shift`-less pair on the same keys
    // as the chord targets, which is why they are only reachable with no
    // pending `g`.
    case "I":
      return { action: { kind: "toggleRead" }, nextState: INITIAL_KEYBOARD_STATE };

    case "s":
      return { action: { kind: "toggleFlag" }, nextState: INITIAL_KEYBOARD_STATE };

    case "c":
      return { action: { kind: "compose" }, nextState: INITIAL_KEYBOARD_STATE };

    case "r":
      return { action: { kind: "reply" }, nextState: INITIAL_KEYBOARD_STATE };

    // Gmail's reply-all. Capital A, so it cannot be confused with the `g a`
    // chord's second key, which is a lowercase `a`.
    case "A":
      return { action: { kind: "replyAll" }, nextState: INITIAL_KEYBOARD_STATE };

    case "f":
      return { action: { kind: "forward" }, nextState: INITIAL_KEYBOARD_STATE };

    // Gmail toggles a row's checkbox with `x`.
    case "x":
      return { action: { kind: "selectRow" }, nextState: INITIAL_KEYBOARD_STATE };

    case "?":
      return { action: { kind: "help" }, nextState: INITIAL_KEYBOARD_STATE };

    default:
      return { action: undefined, nextState: INITIAL_KEYBOARD_STATE };
  }
}

/** The second key of a `g` chord, mapped to a mailbox role. */
const CHORD_TARGETS: Readonly<Record<string, string>> = {
  i: "inbox",
  s: "sent",
  d: "drafts",
  a: "archive",
  t: "trash",
};

/** One row of the shortcuts help, for rendering and for a completeness test. */
export interface ShortcutHelpEntry {
  /** The keys, already formatted for display. */
  readonly keys: readonly string[];
  /** The i18n key describing what it does. */
  readonly descriptionKey: string;
}

/**
 * The help sheet's contents.
 *
 * This list is the DOCUMENTATION of the map above, and a test asserts that
 * every action the resolver can produce appears here — a shortcut that exists
 * but is undiscoverable is a shortcut only its author uses.
 */
export const SHORTCUT_HELP: readonly ShortcutHelpEntry[] = [
  { keys: ["j"], descriptionKey: "shortcuts.next" },
  { keys: ["k"], descriptionKey: "shortcuts.previous" },
  { keys: ["Enter"], descriptionKey: "shortcuts.open" },
  { keys: ["u"], descriptionKey: "shortcuts.back" },
  { keys: ["/"], descriptionKey: "shortcuts.search" },
  { keys: ["e"], descriptionKey: "shortcuts.archive" },
  { keys: ["#"], descriptionKey: "shortcuts.delete" },
  { keys: ["s"], descriptionKey: "shortcuts.flag" },
  { keys: ["x"], descriptionKey: "shortcuts.selectRow" },
  { keys: ["c"], descriptionKey: "shortcuts.compose" },
  { keys: ["r"], descriptionKey: "shortcuts.reply" },
  { keys: ["Shift", "A"], descriptionKey: "shortcuts.replyAll" },
  { keys: ["f"], descriptionKey: "shortcuts.forward" },
  { keys: ["Shift", "I"], descriptionKey: "shortcuts.toggleRead" },
  { keys: ["g", "i"], descriptionKey: "shortcuts.goInbox" },
  { keys: ["g", "s"], descriptionKey: "shortcuts.goSent" },
  { keys: ["g", "d"], descriptionKey: "shortcuts.goDrafts" },
  { keys: ["g", "a"], descriptionKey: "shortcuts.goArchive" },
  { keys: ["g", "t"], descriptionKey: "shortcuts.goTrash" },
  { keys: ["?"], descriptionKey: "shortcuts.help" },
  { keys: ["Esc"], descriptionKey: "shortcuts.close" },
];
