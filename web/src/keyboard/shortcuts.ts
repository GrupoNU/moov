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
  /**
   * `Shift+I` / `Shift+U` — mark the selection read or unread (canon §2.7).
   *
   * DIRECTIONAL, not a toggle: Gmail binds two keys, and with a mixed
   * selection a toggle has no defined meaning. See the resolver's `case "I"`.
   */
  | { readonly kind: "markRead"; readonly read: boolean }
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
  | { readonly kind: "labelAs" }
  /**
   * E4 — `b`: open the snooze menu (canon §2.2, /mail/answer/7622010).
   *
   * Like `l`, it OPENS a menu rather than applying anything, and for the same
   * reason: a single key cannot name one of five wake times. Gmail's `b` does
   * exactly this.
   */
  | { readonly kind: "snooze" }
  /**
   * E4 — `m`: mute the conversation (canon §2.2, /mail/answer/16594169).
   *
   * Unlike `b` this ACTS immediately, because mute is binary — there is
   * nothing to pick. It toggles: pressing `m` on an already-muted conversation
   * unmutes it, which is the only sensible reading of the key in a view that
   * shows the muted badge.
   */
  | { readonly kind: "toggleMute" }
  /**
   * E4 — `g b`: go to Snoozed (canon §2.2 names the chord explicitly).
   *
   * Its OWN action rather than `{kind:"goToMailbox", role:"snoozed"}`, because
   * there is no such role: RFC 6154 defines no SPECIAL-USE attribute for
   * snoozed mail and `internal/sync/snooze.go` refused to invent one, so the
   * folder is found by the NAME the session capability publishes. Passing
   * "snoozed" through the role channel would make the resolver look for
   * something that cannot exist, and the failure would be a silently dead
   * chord.
   */
  | { readonly kind: "goToSnoozed" }
  /**
   * E11 — `,`: move focus into the action toolbar (canon §2.7).
   *
   * Gmail's "move focus to toolbar". It is the keyboard's way INTO the row of
   * controls that the mouse reaches by pointing, and without it every toolbar
   * button is only reachable by tabbing past everything above it.
   */
  | { readonly kind: "focusToolbar" }
  /**
   * E11 — `.`: open the "more actions" (⋯) menu for the selection.
   *
   * Like `l` and `b` it OPENS rather than applies, for the same reason: the
   * overflow menu is a list, and one key cannot name one of its items.
   */
  | { readonly kind: "moreActions" };

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
  /**
   * The PHYSICAL key (`KeyboardEvent.code`), which is what letter and symbol
   * bindings resolve from — see {@link physicalKey}.
   *
   * Optional so that the pre-E11 call shape still compiles and still works:
   * when absent, resolution falls back to `key`, which is correct on a US
   * layout and is what every existing test exercises.
   */
  readonly code?: string;
  readonly ctrlKey: boolean;
  readonly metaKey: boolean;
  readonly altKey: boolean;
  readonly shiftKey: boolean;
  readonly target?: EventTarget | null;
}

/**
 * The physical-key glyph a binding resolves from (E11).
 *
 * # The bug this fixes
 *
 * Before E11 every binding read `event.key` — the CHARACTER the layout
 * produces. On a Cyrillic layout the physical `J` key produces `о`, so `j`
 * never matched and the ENTIRE map was dead: not degraded, dead. The same held
 * for Greek, Hebrew, Dvorak and every non-QWERTY layout. A keyboard-first mail
 * client that only works in one alphabet is not keyboard-first.
 *
 * # The rule
 *
 * Letters and symbols resolve from `code` (the physical position), which is
 * layout-INDEPENDENT: `KeyJ` is the same key on every layout, whatever it
 * prints. Named keys — Enter, Escape, the arrows — keep resolving from `key`,
 * because there the character IS the semantics and `code` would only add
 * numpad/main-row duplication for nothing.
 *
 * This is what Gmail does, and it is why Gmail's shortcuts work on a Russian
 * layout while the naive implementation does not.
 *
 * # The documented consequence
 *
 * Bindings are pinned to the US-QWERTY PHYSICAL POSITION. On a layout where
 * `?` is not Shift+Slash, `?` is still the key in the Slash position — the
 * glyph printed on the user's keycap may differ from the glyph in the help
 * sheet. That is the trade every implementation makes, Gmail included: the
 * alternative (resolving symbols by glyph) is what breaks letters, because a
 * layout moves them all at once.
 *
 * Returns `undefined` when the event carries no physical key we bind, so the
 * caller falls back to `key`.
 */
export function physicalKey(event: KeyLike): string | undefined {
  const code = event.code;
  if (code === undefined || code === "") return undefined;

  // Letters: `KeyA`…`KeyZ`. Shift selects the upper-case glyph, which is how
  // the map distinguishes `a` (chord target) from `A` (reply all) and `i`
  // (chord target) from `I` (mark read).
  if (code.length === 4 && code.startsWith("Key")) {
    const letter = code.charAt(3);
    if (letter >= "A" && letter <= "Z") {
      return event.shiftKey ? letter : letter.toLowerCase();
    }
  }

  const symbol = PHYSICAL_SYMBOLS[code];
  if (symbol !== undefined) return event.shiftKey ? symbol.shifted : symbol.plain;

  return undefined;
}

/**
 * The physical symbol keys the map binds, with and without Shift.
 *
 * Only the positions this app actually binds are listed — a full US-layout
 * table would invite bindings to be added here rather than in the resolver,
 * where the reasoning lives.
 *
 * The unshifted glyphs are the US-QWERTY legends; per {@link physicalKey} that
 * is the canonical position, not a claim about the user's keycaps.
 */
const PHYSICAL_SYMBOLS: Readonly<Record<string, { readonly plain: string; readonly shifted: string }>> = {
  // `/` focuses search; Shift+/ is `?`, the help sheet.
  Slash: { plain: "/", shifted: "?" },
  // `;` expands a conversation, `:` collapses it — one physical key, and Gmail
  // gives its two glyphs opposite meanings, which only works via `code`.
  Semicolon: { plain: ";", shifted: ":" },
  // `,` focuses the toolbar, `.` opens the more-actions menu (E11).
  Comma: { plain: ",", shifted: "<" },
  Period: { plain: ".", shifted: ">" },
  BracketLeft: { plain: "[", shifted: "{" },
  BracketRight: { plain: "]", shifted: "}" },
  // Shift+3 is `#` (delete) and Shift+8 is `*` (the selection chord) on a US
  // layout; on most others those glyphs live elsewhere entirely, which is the
  // whole reason they resolve by position.
  Digit3: { plain: "3", shifted: "#" },
  Digit8: { plain: "8", shifted: "*" },
  // Shift+1 is `!` — report spam.
  Digit1: { plain: "1", shifted: "!" },
  // Shift+- is `_`, mark-unread-from-here.
  Minus: { plain: "-", shifted: "_" },
  Equal: { plain: "=", shifted: "+" },
  Backquote: { plain: "`", shifted: "~" },
};

/**
 * The glyph a binding matches on: the physical key when we have one, the
 * character otherwise.
 *
 * Named keys (Enter, Escape, ArrowUp/Down) bypass this entirely — see
 * {@link physicalKey} — so they are returned untouched from `key`.
 */
function bindingKey(event: KeyLike): string {
  if (NAMED_KEYS.has(event.key)) return event.key;
  return physicalKey(event) ?? event.key;
}

/**
 * Keys whose SEMANTICS are the character, not the position.
 *
 * Enter is Enter on every layout; so are Escape and the arrows. Resolving them
 * by `code` would gain nothing and would split Enter from NumpadEnter.
 */
const NAMED_KEYS: ReadonlySet<string> = new Set([
  "Enter",
  "Escape",
  "ArrowUp",
  "ArrowDown",
  "ArrowLeft",
  "ArrowRight",
  "Tab",
]);

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

  /*
   * E11: from here down the map matches on the PHYSICAL key, so the whole
   * vocabulary survives a non-QWERTY layout. Computed once — every branch
   * below, chords included, reads this instead of `event.key`.
   */
  const pressed = bindingKey(event);

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
  if (!enabled && !isAlwaysOnKey(pressed)) {
    return { action: undefined, nextState: INITIAL_KEYBOARD_STATE };
  }

  // The `g` chord's second key. Checked first so a live prefix cannot be
  // shadowed by a single-key binding of the same letter.
  if (state.pendingG) {
    const key = pressed.toLowerCase();
    // E4: `g b` is the one chord target that is not a role — the Snoozed
    // folder is found by name, so it gets its own action.
    if (key === "b") {
      return { action: { kind: "goToSnoozed" }, nextState: INITIAL_KEYBOARD_STATE };
    }
    const role = CHORD_TARGETS[key];
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
    const scope = STAR_CHORD_TARGETS[pressed.toLowerCase()];
    return {
      action: scope !== undefined ? { kind: "selectBy", scope } : undefined,
      nextState: INITIAL_KEYBOARD_STATE,
    };
  }

  switch (pressed) {
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

    /*
     * E11 — Gmail's read/unread PAIR (canon §2.7: `Shift+I/U`).
     *
     * Before E11 `Shift+I` toggled and `Shift+U` was unbound, which is not what
     * Gmail does and is worse than it looks in bulk: with a mixed selection a
     * toggle has no defined meaning, so "mark these fourteen read" was a
     * coin-flip per row. Gmail binds two keys precisely because the operation
     * is directional. The test that pinned the toggle was updated to Gmail
     * semantics rather than preserved — it pinned our bug, not a contract.
     */
    case "I":
      return { action: { kind: "markRead", read: true }, nextState: INITIAL_KEYBOARD_STATE };

    case "U":
      return { action: { kind: "markRead", read: false }, nextState: INITIAL_KEYBOARD_STATE };

    /*
     * E11 — the two application keys of canon §2.7 that had no binding.
     *
     * `,` moves focus to the toolbar and `.` opens the overflow menu. Both are
     * NAVIGATION into controls that already exist and were mouse-only.
     */
    case ",":
      return { action: { kind: "focusToolbar" }, nextState: INITIAL_KEYBOARD_STATE };

    case ".":
      return { action: { kind: "moreActions" }, nextState: INITIAL_KEYBOARD_STATE };

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

    /*
     * E4 — Gmail's triage pair (canon §2.2).
     *
     * `b` does not shadow the `g b` chord's second key: that one is only
     * reachable with a pending `g`, which the branch at the top of this
     * function resolves before ever entering this switch. The same
     * relationship `l` has with `g l`.
     */
    case "b":
      return { action: { kind: "snooze" }, nextState: INITIAL_KEYBOARD_STATE };

    case "m":
      return { action: { kind: "toggleMute" }, nextState: INITIAL_KEYBOARD_STATE };

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
  /**
   * Which section of the sheet the row belongs to (E11).
   *
   * Gmail's cheat sheet is grouped, and for a map this size the grouping is
   * the difference between a reference and a wall: forty rows in press order
   * is not something anyone reads twice.
   */
  readonly section: ShortcutSection;
}

/**
 * The sheet's sections, in render order (E11, modelled on Gmail's own sheet).
 *
 * "jump" is kept separate from "navigate" because the `g` chords are a
 * different MOTION — moving between folders rather than within a list — and
 * grouping them together is what made the old flat list unreadable.
 */
export type ShortcutSection = "navigate" | "actions" | "selection" | "compose" | "jump";

export const SHORTCUT_SECTIONS: readonly ShortcutSection[] = [
  "navigate",
  "actions",
  "selection",
  "compose",
  "jump",
];

/** The i18n key for a section's heading. */
export const SECTION_TITLE_KEYS: Readonly<Record<ShortcutSection, string>> = {
  navigate: "shortcuts.sectionNavigate",
  actions: "shortcuts.sectionActions",
  selection: "shortcuts.sectionSelection",
  compose: "shortcuts.sectionCompose",
  jump: "shortcuts.sectionJump",
};

/**
 * The help sheet's contents.
 *
 * This list is the DOCUMENTATION of the map above, and a test asserts that
 * every action the resolver can produce appears here — a shortcut that exists
 * but is undiscoverable is a shortcut only its author uses.
 */
export const SHORTCUT_HELP: readonly ShortcutHelpEntry[] = [
  // --- Moving around ---
  { keys: ["j"], descriptionKey: "shortcuts.next", section: "navigate" },
  { keys: ["k"], descriptionKey: "shortcuts.previous", section: "navigate" },
  { keys: ["Enter"], descriptionKey: "shortcuts.open", section: "navigate" },
  { keys: ["u"], descriptionKey: "shortcuts.back", section: "navigate" },
  // E1: the conversation keys. They sit next to j/k precisely because the
  // distinction between them is the thing a reader has to learn — j/k move
  // between conversations, n/p move inside one.
  { keys: ["n"], descriptionKey: "shortcuts.conversationNext", section: "navigate" },
  { keys: ["p"], descriptionKey: "shortcuts.conversationPrevious", section: "navigate" },
  { keys: [";"], descriptionKey: "shortcuts.expandAll", section: "navigate" },
  { keys: [":"], descriptionKey: "shortcuts.collapseAll", section: "navigate" },
  { keys: ["/"], descriptionKey: "shortcuts.search", section: "navigate" },
  /*
   * E-32 — the keys INSIDE the search box.
   *
   * Documented here but not resolved by `resolveShortcut`, exactly like the
   * composer's Ctrl+Enter row above: the typing guard refuses every key while
   * an input has focus — correctly, or `e` would archive a message while you
   * typed one — so the box owns them itself. A user does not care which module
   * implements a key, and the sheet's whole job is to answer "what can I press
   * here". Leaving the box's three keys out made the one surface a person is
   * most likely to get stuck in the one surface the sheet said nothing about.
   */
  { keys: ["Esc"], descriptionKey: "shortcuts.searchLeave", section: "navigate" },
  { keys: ["Enter"], descriptionKey: "shortcuts.searchRun", section: "navigate" },
  { keys: ["↑", "↓"], descriptionKey: "shortcuts.searchSuggestions", section: "navigate" },
  // E11: the two application keys that reach the toolbar and its overflow.
  { keys: [","], descriptionKey: "shortcuts.focusToolbar", section: "navigate" },
  { keys: ["."], descriptionKey: "shortcuts.moreActions", section: "navigate" },
  { keys: ["?"], descriptionKey: "shortcuts.help", section: "navigate" },
  { keys: ["Esc"], descriptionKey: "shortcuts.close", section: "navigate" },

  // --- Acting on mail ---
  { keys: ["e"], descriptionKey: "shortcuts.archive", section: "actions" },
  { keys: ["#"], descriptionKey: "shortcuts.delete", section: "actions" },
  { keys: ["!"], descriptionKey: "shortcuts.spam", section: "actions" },
  { keys: ["z"], descriptionKey: "shortcuts.undo", section: "actions" },
  { keys: ["]"], descriptionKey: "shortcuts.archiveNext", section: "actions" },
  { keys: ["["], descriptionKey: "shortcuts.archivePrevious", section: "actions" },
  { keys: ["_"], descriptionKey: "shortcuts.markUnreadFromHere", section: "actions" },
  { keys: ["s"], descriptionKey: "shortcuts.flag", section: "actions" },
  { keys: ["l"], descriptionKey: "shortcuts.labelAs", section: "actions" },
  // E4: the triage pair, next to the other verbs that make a row leave the
  // list — which is what they have in common with archive and delete.
  { keys: ["b"], descriptionKey: "shortcuts.snooze", section: "actions" },
  { keys: ["m"], descriptionKey: "shortcuts.mute", section: "actions" },
  // E11: the directional read/unread pair, replacing the old single toggle.
  { keys: ["Shift", "I"], descriptionKey: "shortcuts.markRead", section: "actions" },
  { keys: ["Shift", "U"], descriptionKey: "shortcuts.markUnread", section: "actions" },

  // --- Selecting ---
  { keys: ["x"], descriptionKey: "shortcuts.selectRow", section: "selection" },
  { keys: ["*", "a"], descriptionKey: "shortcuts.selectAll", section: "selection" },
  { keys: ["*", "n"], descriptionKey: "shortcuts.selectNone", section: "selection" },
  { keys: ["*", "r"], descriptionKey: "shortcuts.selectRead", section: "selection" },
  { keys: ["*", "u"], descriptionKey: "shortcuts.selectUnread", section: "selection" },
  { keys: ["*", "t"], descriptionKey: "shortcuts.selectStarred", section: "selection" },
  { keys: ["*", "s"], descriptionKey: "shortcuts.selectUnstarred", section: "selection" },

  // --- Writing ---
  { keys: ["c"], descriptionKey: "shortcuts.compose", section: "compose" },
  { keys: ["r"], descriptionKey: "shortcuts.reply", section: "compose" },
  { keys: ["Shift", "A"], descriptionKey: "shortcuts.replyAll", section: "compose" },
  { keys: ["f"], descriptionKey: "shortcuts.forward", section: "compose" },
  /*
   * E11 — the in-composer keys (canon §2.7's compose row).
   *
   * They are documented here but NOT resolved by `resolveShortcut`: inside a
   * text field the typing guard refuses everything, correctly, so the composer
   * handles them itself. Documenting them anyway is the point of a cheat sheet
   * — the user does not care which module implements the key.
   */
  { keys: ["Ctrl", "Enter"], descriptionKey: "shortcuts.send", section: "compose" },
  { keys: ["Ctrl", "Shift", "C"], descriptionKey: "shortcuts.focusCc", section: "compose" },
  { keys: ["Ctrl", "Shift", "B"], descriptionKey: "shortcuts.focusBcc", section: "compose" },

  // --- Jumping to a folder ---
  { keys: ["g", "i"], descriptionKey: "shortcuts.goInbox", section: "jump" },
  { keys: ["g", "s"], descriptionKey: "shortcuts.goSent", section: "jump" },
  { keys: ["g", "d"], descriptionKey: "shortcuts.goDrafts", section: "jump" },
  { keys: ["g", "a"], descriptionKey: "shortcuts.goArchive", section: "jump" },
  { keys: ["g", "t"], descriptionKey: "shortcuts.goTrash", section: "jump" },
  { keys: ["g", "b"], descriptionKey: "shortcuts.goSnoozed", section: "jump" },
];
