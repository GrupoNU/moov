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
  | { readonly kind: "selectRow" }
  // E2: the triage verbs Gmail binds that P3 left unbound.
  | { readonly kind: "toggleSpam" }
  | { readonly kind: "undo" }
  /** `]` / `[`: archive, then move to the next/previous conversation. */
  | { readonly kind: "archiveAndAdvance"; readonly direction: "next" | "previous" }
  /** `_`: mark unread from the focused row downward. */
  | { readonly kind: "markUnreadFromHere" }
  /** `* a` and friends: bulk selection over the whole visible list. */
  | { readonly kind: "selectBy"; readonly scope: SelectionScope }
  // E1: the conversation keys (canon §2.1, /mail/answer/6594).
  /** `;` expands every message of the open conversation; `:` collapses them. */
  | { readonly kind: "expandConversation"; readonly expand: boolean }
  /**
   * `p` / `n`: move between messages INSIDE the open conversation.
   *
   * Deliberately distinct from `next`/`previous` (`j`/`k`), which move between
   * CONVERSATIONS. Gmail draws that line and it is the reason both pairs
   * exist: in a 24-message thread you need to walk the thread without leaving
   * it, and to leave it without walking it.
   */
  | { readonly kind: "conversationMessage"; readonly direction: "next" | "previous" }
  /**
   * E8 — `l`: open the "Label as" menu (canon §2.7's application keys:
   * "`v` move to · `l` label as").
   *
   * It OPENS a menu rather than applying anything, which is why it carries no
   * payload. A single key cannot name one of up to 26 labels, and Gmail's `l`
   * does exactly this: it opens the picker.
   */
  | { readonly kind: "labelAs" };

/**
 * The six selection scopes Gmail's `*` chord offers, verbatim (canon §2.4):
 * all, none, read, unread, starred, unstarred.
 */
export type SelectionScope = "all" | "none" | "read" | "unread" | "starred" | "unstarred";

/**
 * The chord state.
 *
 * Two independent prefixes, not one enum: `g` (go to a folder) and `*`
 * (select by). They are mutually exclusive in practice — the resolver clears
 * both on every resolution — but modelling them as separate booleans keeps
 * each branch's condition readable, and makes "is any chord live" a plain OR
 * rather than a comparison against a sentinel.
 */
export interface KeyboardState {
  /** True while a `g` prefix is live. */
  readonly pendingG: boolean;
  /** True while a `*` prefix is live. */
  readonly pendingStar: boolean;
}

export const INITIAL_KEYBOARD_STATE: KeyboardState = { pendingG: false, pendingStar: false };

/** True when any chord prefix is awaiting its second key. */
export function hasPendingChord(state: KeyboardState): boolean {
  return state.pendingG || state.pendingStar;
}

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
 * How much of the keyboard map is live (L3 E5, the `keyboardShortcuts` pref).
 *
 * The setting is Gmail's "Keyboard shortcuts on/off" (canon §2.7), whose
 * DEFAULT we diverge from — decision D-3 signed shortcuts ON, where Gmail
 * ships them off. The off state still has to exist, and this is what it means.
 */
export interface ShortcutOptions {
  /**
   * When false, only the ALWAYS-ON keys resolve (see {@link isAlwaysOnKey}).
   *
   * "Off" cannot mean "the resolver is not called": Escape has to keep
   * dismissing dialogs and `/` has to keep reaching the search box, or turning
   * shortcuts off would strand a keyboard user inside a modal with no way out
   * and no way to search. Gmail keeps the same two reachable for the same
   * reason.
   */
  readonly enabled?: boolean;
}

/**
 * The keys that work even with shortcuts turned off.
 *
 * Escape, because it is the universal "get me out of here" and a dialog that
 * cannot be dismissed from the keyboard is an accessibility defect, not a
 * preference. `/`, because search must stay reachable — it is the one action
 * with no equivalent affordance a keyboard user can reach without it, and it
 * is a NAVIGATION key rather than a destructive one.
 *
 * Everything else — every key that changes mail — obeys the setting. That is
 * the line: the off state removes the keys that ACT, never the keys that
 * navigate out of a corner.
 */
export function isAlwaysOnKey(key: string): boolean {
  return key === "Escape" || key === "/";
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
  options: ShortcutOptions = {},
): { readonly action: ShortcutAction | undefined; readonly nextState: KeyboardState } {
  const enabled = options.enabled ?? true;

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

  /*
   * The off state, applied AFTER the modifier and typing guards so those keep
   * their meaning, and after Escape so a dialog stays dismissable.
   *
   * The chord state is cleared on the way out: a `g` pressed just before the
   * user turned shortcuts off must not sit live waiting for a second key that
   * can no longer resolve.
   */
  if (!enabled && !isAlwaysOnKey(event.key)) {
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

  /*
   * The `*` chord's second key, same precedence rule as `g`.
   *
   * Note that `a`, `r`, `s` and `u` all have single-key meanings of their own
   * (reply-all is `A`, reply is `r`, star is `s`, back is `u`). Resolving the
   * chord BEFORE the single-key switch is what keeps `* u` from being read as
   * "select-by prefix, then go back to the list" — which would both leave the
   * reader and select nothing.
   */
  if (state.pendingStar) {
    const scope = STAR_CHORD_TARGETS[event.key.toLowerCase()];
    return {
      action: scope !== undefined ? { kind: "selectBy", scope } : undefined,
      nextState: INITIAL_KEYBOARD_STATE,
    };
  }

  switch (event.key) {
    case "g":
      return { action: undefined, nextState: { ...INITIAL_KEYBOARD_STATE, pendingG: true } };

    case "*":
      return { action: undefined, nextState: { ...INITIAL_KEYBOARD_STATE, pendingStar: true } };

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

    // Gmail's report-spam key. It TOGGLES: pressing it on a message already in
    // Junk is "not spam", which is the only sensible reading of "!" in that
    // folder and what Gmail does.
    case "!":
      return { action: { kind: "toggleSpam" }, nextState: INITIAL_KEYBOARD_STATE };

    case "z":
      return { action: { kind: "undo" }, nextState: INITIAL_KEYBOARD_STATE };

    // Archive-and-advance. `]` goes newer, `[` goes older — Gmail's own
    // direction, which is the opposite of what the bracket shapes suggest and
    // therefore the exact thing to copy rather than to reason about.
    case "]":
      return {
        action: { kind: "archiveAndAdvance", direction: "next" },
        nextState: INITIAL_KEYBOARD_STATE,
      };

    case "[":
      return {
        action: { kind: "archiveAndAdvance", direction: "previous" },
        nextState: INITIAL_KEYBOARD_STATE,
      };

    // Gmail's `_`: mark unread from the focused conversation downward. It is
    // the "I'll deal with the rest later" key, and there is no other way to
    // express it without selecting every row by hand.
    case "_":
      return { action: { kind: "markUnreadFromHere" }, nextState: INITIAL_KEYBOARD_STATE };

    /*
     * E1 — the conversation keys (canon §2.1).
     *
     * `;` and `:` are the same physical key with and without Shift on a US
     * layout, and Gmail gives them opposite meanings: expand all, collapse
     * all. They are matched on the CHARACTER rather than the physical key, so
     * a layout that puts `:` elsewhere still resolves it correctly.
     */
    case ";":
      return {
        action: { kind: "expandConversation", expand: true },
        nextState: INITIAL_KEYBOARD_STATE,
      };

    case ":":
      return {
        action: { kind: "expandConversation", expand: false },
        nextState: INITIAL_KEYBOARD_STATE,
      };

    /*
     * `n` / `p` — the NEXT and PREVIOUS message inside the open conversation.
     *
     * They do not shadow `j`/`k`: those move between conversations and keep
     * doing so. The two pairs coexist because a conversation view needs both
     * axes, which is why Gmail binds four keys rather than two.
     */
    case "n":
      return {
        action: { kind: "conversationMessage", direction: "next" },
        nextState: INITIAL_KEYBOARD_STATE,
      };

    case "p":
      return {
        action: { kind: "conversationMessage", direction: "previous" },
        nextState: INITIAL_KEYBOARD_STATE,
      };

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

    // E8 — Gmail's "label as". Lowercase `l`; it does not shadow the `g l`
    // chord's second key, which is only reachable with a pending `g`.
    case "l":
      return { action: { kind: "labelAs" }, nextState: INITIAL_KEYBOARD_STATE };

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

/**
 * The second key of a `*` chord, mapped to a selection scope (canon §2.4).
 *
 * `t` is Gmail's "starred" and `s` its "unstarred", which reads backwards
 * until you know that `t` is for "s*t*arred" — the mnemonic is not ours to
 * fix. Copied verbatim, per ADR §6: a power user's fingers already know it.
 */
const STAR_CHORD_TARGETS: Readonly<Record<string, SelectionScope>> = {
  a: "all",
  n: "none",
  r: "read",
  u: "unread",
  s: "unstarred",
  t: "starred",
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
  // E1: the conversation keys. They sit next to j/k precisely because the
  // distinction between them is the thing a reader has to learn — j/k move
  // between conversations, n/p move inside one.
  { keys: ["n"], descriptionKey: "shortcuts.conversationNext" },
  { keys: ["p"], descriptionKey: "shortcuts.conversationPrevious" },
  { keys: [";"], descriptionKey: "shortcuts.expandAll" },
  { keys: [":"], descriptionKey: "shortcuts.collapseAll" },
  { keys: ["/"], descriptionKey: "shortcuts.search" },
  { keys: ["e"], descriptionKey: "shortcuts.archive" },
  { keys: ["#"], descriptionKey: "shortcuts.delete" },
  { keys: ["!"], descriptionKey: "shortcuts.spam" },
  { keys: ["z"], descriptionKey: "shortcuts.undo" },
  { keys: ["]"], descriptionKey: "shortcuts.archiveNext" },
  { keys: ["["], descriptionKey: "shortcuts.archivePrevious" },
  { keys: ["_"], descriptionKey: "shortcuts.markUnreadFromHere" },
  { keys: ["s"], descriptionKey: "shortcuts.flag" },
  { keys: ["l"], descriptionKey: "shortcuts.labelAs" },
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
  { keys: ["*", "a"], descriptionKey: "shortcuts.selectAll" },
  { keys: ["*", "n"], descriptionKey: "shortcuts.selectNone" },
  { keys: ["*", "r"], descriptionKey: "shortcuts.selectRead" },
  { keys: ["*", "u"], descriptionKey: "shortcuts.selectUnread" },
  { keys: ["*", "t"], descriptionKey: "shortcuts.selectStarred" },
  { keys: ["*", "s"], descriptionKey: "shortcuts.selectUnstarred" },
  { keys: ["?"], descriptionKey: "shortcuts.help" },
  { keys: ["Esc"], descriptionKey: "shortcuts.close" },
];
