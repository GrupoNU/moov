import { describe, expect, it } from "vitest";

import {
  hasPendingChord,
  INITIAL_KEYBOARD_STATE,
  isAlwaysOnKey,
  isTypingTarget,
  physicalKey,
  resolveShortcut,
  SECTION_TITLE_KEYS,
  SHORTCUT_HELP,
  SHORTCUT_SECTIONS,
  type KeyLike,
} from "./shortcuts";

function key(k: string, overrides: Partial<KeyLike> = {}): KeyLike {
  return {
    key: k,
    ctrlKey: false,
    metaKey: false,
    altKey: false,
    shiftKey: false,
    target: null,
    ...overrides,
  };
}

/**
 * A press on a US-QWERTY layout: `code` and `key` agree.
 *
 * The `key(...)` helper above deliberately omits `code` — that is the
 * pre-E11 call shape, and every existing test keeps using it to prove the
 * fallback still resolves the whole map. This helper is the layout-aware one.
 */
function usKey(k: string, overrides: Partial<KeyLike> = {}): KeyLike {
  return key(k, { code: US_CODES[k] ?? `Key${k.toUpperCase()}`, shiftKey: SHIFTED.has(k), ...overrides });
}

/**
 * The same PHYSICAL press on a Cyrillic (ЙЦУКЕН) layout.
 *
 * `code` is the physical key — identical to the US press — while `key` is the
 * Cyrillic glyph that physical key actually produces. This is exactly the
 * event a Russian-layout user generates, and before E11 it resolved to
 * NOTHING: the entire map was dead outside the Latin alphabet.
 */
function cyrillicKey(k: string, overrides: Partial<KeyLike> = {}): KeyLike {
  const code = US_CODES[k] ?? `Key${k.toUpperCase()}`;
  const glyph = CYRILLIC_GLYPHS[k.toLowerCase()] ?? k;
  return key(SHIFTED.has(k) ? glyph.toUpperCase() : glyph, {
    code,
    shiftKey: SHIFTED.has(k),
    ...overrides,
  });
}

/** The physical position of each non-letter glyph the map binds. */
const US_CODES: Readonly<Record<string, string>> = {
  "/": "Slash",
  "?": "Slash",
  ";": "Semicolon",
  ":": "Semicolon",
  ",": "Comma",
  ".": "Period",
  "[": "BracketLeft",
  "]": "BracketRight",
  "#": "Digit3",
  "*": "Digit8",
  "!": "Digit1",
  _: "Minus",
  Enter: "Enter",
  Escape: "Escape",
  ArrowUp: "ArrowUp",
  ArrowDown: "ArrowDown",
};

/** The glyphs that require Shift on a US layout. */
const SHIFTED: ReadonlySet<string> = new Set(["?", ":", "#", "*", "!", "_", "I", "U", "A"]);

/** ЙЦУКЕН: what each US letter position actually prints. */
const CYRILLIC_GLYPHS: Readonly<Record<string, string>> = {
  a: "ф",
  b: "и",
  c: "с",
  e: "у",
  f: "а",
  g: "п",
  i: "ш",
  j: "о",
  k: "л",
  l: "д",
  m: "ь",
  n: "т",
  o: "щ",
  p: "з",
  r: "к",
  s: "ы",
  t: "е",
  u: "г",
  x: "ч",
  z: "я",
};

describe("the Gmail vocabulary", () => {
  it.each([
    ["j", "next"],
    ["k", "previous"],
    ["Enter", "open"],
    ["o", "open"],
    ["u", "back"],
    ["/", "focusSearch"],
    ["e", "archive"],
    ["#", "delete"],
    ["?", "help"],
  ])("maps %o to %o", (pressed, expected) => {
    expect(resolveShortcut(key(pressed)).action?.kind).toBe(expected);
  });

  it("maps the arrow keys alongside j/k", () => {
    expect(resolveShortcut(key("ArrowDown")).action?.kind).toBe("next");
    expect(resolveShortcut(key("ArrowUp")).action?.kind).toBe("previous");
  });
});

/**
 * E1: the conversation keys (canon §2.1).
 *
 * The point of these tests is the DISTINCTION. `j`/`k` and `n`/`p` look like
 * duplicates and are not: one pair moves between conversations, the other
 * inside one. If a refactor ever collapses them, these go red.
 */
describe("the conversation keys", () => {
  it("maps ; to expand-all and : to collapse-all", () => {
    expect(resolveShortcut(key(";")).action).toEqual({
      kind: "expandConversation",
      expand: true,
    });
    expect(resolveShortcut(key(":")).action).toEqual({
      kind: "expandConversation",
      expand: false,
    });
  });

  it("maps n and p to movement INSIDE the conversation", () => {
    expect(resolveShortcut(key("n")).action).toEqual({
      kind: "conversationMessage",
      direction: "next",
    });
    expect(resolveShortcut(key("p")).action).toEqual({
      kind: "conversationMessage",
      direction: "previous",
    });
  });

  it("keeps j/k on conversations — the two pairs are different axes", () => {
    expect(resolveShortcut(key("j")).action?.kind).toBe("next");
    expect(resolveShortcut(key("k")).action?.kind).toBe("previous");
    expect(resolveShortcut(key("n")).action?.kind).not.toBe("next");
    expect(resolveShortcut(key("p")).action?.kind).not.toBe("previous");
  });

  it("does not fire n/p or ;/: while typing", () => {
    // `n` and `p` are ordinary letters: firing them in the composer would eat
    // characters, which is the defect the typing guard exists for.
    const target = { tagName: "INPUT" } as unknown as EventTarget;
    for (const k of ["n", "p", ";", ":"]) {
      expect(resolveShortcut(key(k, { target })).action).toBeUndefined();
    }
  });

  it("obeys the shortcuts-off preference", () => {
    for (const k of ["n", "p", ";", ":"]) {
      expect(resolveShortcut(key(k), undefined, { enabled: false }).action).toBeUndefined();
    }
  });
});

describe("the g chord", () => {
  it("does not act on g alone, but arms the prefix", () => {
    const result = resolveShortcut(key("g"));
    expect(result.action).toBeUndefined();
    expect(result.nextState.pendingG).toBe(true);
  });

  it.each([
    ["i", "inbox"],
    ["s", "sent"],
    ["d", "drafts"],
    ["a", "archive"],
    ["t", "trash"],
  ])("g then %o goes to %o", (second, role) => {
    const armed = resolveShortcut(key("g")).nextState;
    const result = resolveShortcut(key(second), armed);
    expect(result.action).toEqual({ kind: "goToMailbox", role });
    expect(result.nextState.pendingG).toBe(false);
  });

  it("clears the prefix on an unbound second key rather than leaving it armed", () => {
    const armed = resolveShortcut(key("g")).nextState;
    const result = resolveShortcut(key("q"), armed);
    expect(result.action).toBeUndefined();
    expect(result.nextState.pendingG).toBe(false);
  });

  it("lets the chord shadow a single-key binding of the same letter", () => {
    // `s` alone flags; `g s` must go to Sent, not flag.
    const armed = resolveShortcut(key("g")).nextState;
    expect(resolveShortcut(key("s"), armed).action).toEqual({
      kind: "goToMailbox",
      role: "sent",
    });
    expect(resolveShortcut(key("s")).action?.kind).toBe("toggleFlag");
  });
});

describe("never breaking the browser or the user's typing", () => {
  it.each(["INPUT", "TEXTAREA", "SELECT"])("ignores keys typed in a %s", (tagName) => {
    const target = { tagName } as unknown as EventTarget;
    expect(resolveShortcut(key("e", { target })).action).toBeUndefined();
    expect(resolveShortcut(key("j", { target })).action).toBeUndefined();
  });

  it("ignores keys typed in a contentEditable (the P3 composer)", () => {
    const target = { tagName: "DIV", isContentEditable: true } as unknown as EventTarget;
    expect(resolveShortcut(key("#", { target })).action).toBeUndefined();
  });

  /*
   * The rule that keeps the browser usable: any modifier means the event is
   * not ours. Ctrl+R must reload, Cmd+K must reach the browser.
   */
  it.each([
    ["ctrlKey", { ctrlKey: true }],
    ["metaKey", { metaKey: true }],
    ["altKey", { altKey: true }],
  ])("ignores %s combinations so browser shortcuts survive", (_label, modifier) => {
    expect(resolveShortcut(key("j", modifier)).action).toBeUndefined();
    expect(resolveShortcut(key("e", modifier)).action).toBeUndefined();
  });

  it("still handles Escape inside a text field, because that is how you leave one", () => {
    const target = { tagName: "INPUT" } as unknown as EventTarget;
    expect(resolveShortcut(key("Escape", { target })).action?.kind).toBe("closeOverlay");
  });

  it("clears a pending chord when focus moves into a text field", () => {
    const armed = resolveShortcut(key("g")).nextState;
    const target = { tagName: "INPUT" } as unknown as EventTarget;
    expect(resolveShortcut(key("i", { target }), armed).nextState.pendingG).toBe(false);
  });

  it("returns no action for unbound keys, leaving them to the browser", () => {
    for (const k of ["q", "w", "F5", "Tab", "1"]) {
      expect(resolveShortcut(key(k)).action).toBeUndefined();
    }
  });
});

describe("isTypingTarget", () => {
  it("is false for null and for a plain div", () => {
    expect(isTypingTarget(null)).toBe(false);
    expect(isTypingTarget({ tagName: "DIV" } as unknown as EventTarget)).toBe(false);
  });
});

describe("discoverability", () => {
  /*
   * A shortcut that exists but is not in the help sheet is one only its author
   * uses. This pins the two together.
   */
  /*
   * DERIVED from the resolver rather than from a hand-kept list.
   *
   * The previous version of this test iterated a literal array of keys, which
   * meant a NEW binding could never fail it — exactly the regression the test
   * exists to prevent. Sweeping the printable-ASCII keyspace plus the named
   * keys and asking the resolver what it binds makes the check real: add a
   * shortcut and forget the help sheet, and this goes red.
   */
  it("documents every key the resolver binds", () => {
    const documented = new Set(SHORTCUT_HELP.flatMap((entry) => entry.keys));

    const candidates: string[] = ["Enter", "ArrowUp", "ArrowDown"];
    for (let code = 0x21; code <= 0x7e; code += 1) {
      candidates.push(String.fromCharCode(code));
    }

    const bound: string[] = [];
    for (const candidate of candidates) {
      const { action, nextState } = resolveShortcut(key(candidate));
      // `g` and `*` bind no action of their own; they open a chord, and their
      // targets are documented as the two-key entries.
      if (action !== undefined || hasPendingChord(nextState)) bound.push(candidate);
    }

    // Sanity: the sweep must actually find the vocabulary, or a broken sweep
    // would make this test vacuously pass.
    expect(bound).toEqual(expect.arrayContaining(["j", "k", "e", "#", "c", "r", "f", "x"]));

    /*
     * ArrowUp/ArrowDown are aliases of k/j and o is an alias of Enter; a help
     * sheet listing every alias is noise, so the aliases are exempt and the
     * canonical key of each pair must be documented.
     */
    const aliases = new Set(["ArrowUp", "ArrowDown", "o"]);
    for (const k of bound) {
      if (aliases.has(k)) continue;
      expect(documented.has(k), `"${k}" is bound but missing from SHORTCUT_HELP`).toBe(true);
    }
  });

  /*
   * E11: the same completeness check, swept by PHYSICAL key.
   *
   * The glyph sweep above cannot see a binding that is only reachable by
   * position — and after the `event.code` migration that is how every letter
   * and symbol resolves. Sweeping KeyA-KeyZ and the bound symbol positions,
   * both shifted and not, is what makes the check real for the new resolver.
   */
  it("documents every binding reachable by physical position", () => {
    const documented = new Set(SHORTCUT_HELP.flatMap((entry) => entry.keys));

    const codes: string[] = [];
    for (let i = 0; i < 26; i += 1) codes.push(`Key${String.fromCharCode(65 + i)}`);
    codes.push(
      "Slash",
      "Semicolon",
      "Comma",
      "Period",
      "BracketLeft",
      "BracketRight",
      "Digit1",
      "Digit3",
      "Digit8",
      "Minus",
      "Equal",
      "Backquote",
    );

    // Aliases of a documented canonical key, exempt for the same reason the
    // glyph sweep exempts them: a sheet listing every alias is noise.
    const aliases = new Set(["o"]);

    for (const code of codes) {
      for (const shiftKey of [false, true]) {
        // A deliberately non-Latin glyph, so a binding that still leaked
        // through `event.key` would be invisible here and fail the assertion.
        const { action, nextState } = resolveShortcut(key("§", { code, shiftKey }));
        if (action === undefined && !hasPendingChord(nextState)) continue;

        const glyph = physicalKey(key("§", { code, shiftKey }));
        expect(glyph, `${code}${shiftKey ? "+shift" : ""} resolved but has no glyph`).toBeDefined();
        if (glyph === undefined || aliases.has(glyph)) continue;

        // Shifted letters are written "Shift"+"X" in the sheet; unshifted and
        // symbols appear as the glyph itself.
        const isShiftedLetter = shiftKey && /^[A-Z]$/.test(glyph);
        const found = isShiftedLetter
          ? SHORTCUT_HELP.some(
              (entry) => entry.keys.length === 2 && entry.keys[0] === "Shift" && entry.keys[1] === glyph,
            )
          : documented.has(glyph);
        expect(found, `"${glyph}" (${code}) is bound but missing from SHORTCUT_HELP`).toBe(true);
      }
    }
  });

  it("gives every help entry a description key", () => {
    for (const entry of SHORTCUT_HELP) {
      expect(entry.descriptionKey).toMatch(/^shortcuts\./);
      expect(entry.keys.length).toBeGreaterThan(0);
    }
  });
});

describe("initial state", () => {
  it("starts with no pending chord", () => {
    expect(INITIAL_KEYBOARD_STATE.pendingG).toBe(false);
  });
});

describe("E2: the rest of Gmail's triage vocabulary", () => {
  it.each([
    ["!", "toggleSpam"],
    ["z", "undo"],
    ["_", "markUnreadFromHere"],
  ])("binds %s to %s", (k, kind) => {
    expect(resolveShortcut(key(k)).action?.kind).toBe(kind);
  });

  /*
   * Gmail's own direction, which is the OPPOSITE of what the bracket shapes
   * suggest — copied rather than reasoned about, per ADR §6.
   */
  it("archives and advances with ] and retreats with [", () => {
    expect(resolveShortcut(key("]")).action).toEqual({
      kind: "archiveAndAdvance",
      direction: "next",
    });
    expect(resolveShortcut(key("[")).action).toEqual({
      kind: "archiveAndAdvance",
      direction: "previous",
    });
  });
});

describe("E2: the * selection chord", () => {
  it("arms on * without acting", () => {
    const result = resolveShortcut(key("*"));
    expect(result.action).toBeUndefined();
    expect(result.nextState.pendingStar).toBe(true);
    expect(result.nextState.pendingG).toBe(false);
  });

  it.each([
    ["a", "all"],
    ["n", "none"],
    ["r", "read"],
    ["u", "unread"],
    ["s", "unstarred"],
    ["t", "starred"],
  ])("resolves * %s to the %s scope", (k, scope) => {
    const armed = resolveShortcut(key("*")).nextState;
    const result = resolveShortcut(key(k), armed);
    expect(result.action).toEqual({ kind: "selectBy", scope });
    expect(hasPendingChord(result.nextState)).toBe(false);
  });

  /*
   * The precedence that makes the chord usable at all: `u` alone leaves the
   * reader and `r` alone replies, so a `*` prefix that did not shadow them
   * would make `* u` and `* r` unreachable.
   */
  it("shadows the single-key meanings of its target letters", () => {
    const armed = resolveShortcut(key("*")).nextState;
    expect(resolveShortcut(key("u"), armed).action?.kind).toBe("selectBy");
    expect(resolveShortcut(key("u")).action?.kind).toBe("back");
    expect(resolveShortcut(key("r"), armed).action?.kind).toBe("selectBy");
    expect(resolveShortcut(key("r")).action?.kind).toBe("reply");
  });

  it("clears on an unbound second key rather than staying armed", () => {
    const armed = resolveShortcut(key("*")).nextState;
    const result = resolveShortcut(key("q"), armed);
    expect(result.action).toBeUndefined();
    expect(hasPendingChord(result.nextState)).toBe(false);
  });

  it("does not let the two chords be armed at once", () => {
    const starred = resolveShortcut(key("*")).nextState;
    // `g` while `*` is armed is read as the chord's second key (unbound) and
    // clears everything — it must NOT leave both prefixes live.
    const after = resolveShortcut(key("g"), starred).nextState;
    expect(hasPendingChord(after)).toBe(false);
  });

  it("clears the * prefix when focus moves into a text field", () => {
    const armed = resolveShortcut(key("*")).nextState;
    const target = { tagName: "INPUT" } as unknown as EventTarget;
    expect(hasPendingChord(resolveShortcut(key("a", { target }), armed).nextState)).toBe(false);
  });
});

/**
 * The `keyboardShortcuts` preference (L3 E5, decision D-3).
 *
 * Turning shortcuts off must not turn the app into a trap: the two keys that
 * get a keyboard user OUT of somewhere — Escape and `/` — stay live, and
 * everything that ACTS on mail goes quiet.
 */
describe("the shortcuts-off gate", () => {
  const off = { enabled: false };

  it("resolves nothing for the acting keys", () => {
    for (const k of ["e", "#", "!", "z", "s", "c", "r", "f", "x", "j", "k", "u", "o", "_", "]", "["]) {
      expect(resolveShortcut(key(k), INITIAL_KEYBOARD_STATE, off).action).toBeUndefined();
    }
  });

  it("keeps Escape live — a modal must stay dismissable", () => {
    expect(resolveShortcut(key("Escape"), INITIAL_KEYBOARD_STATE, off).action).toEqual({
      kind: "closeOverlay",
    });
  });

  it("keeps `/` live — search must stay reachable", () => {
    expect(resolveShortcut(key("/"), INITIAL_KEYBOARD_STATE, off).action).toEqual({
      kind: "focusSearch",
    });
  });

  it("refuses to arm a chord, so `g` cannot swallow the next key", () => {
    const after = resolveShortcut(key("g"), INITIAL_KEYBOARD_STATE, off);
    expect(after.action).toBeUndefined();
    expect(hasPendingChord(after.nextState)).toBe(false);
  });

  it("does not resolve a chord that was armed before the setting changed", () => {
    // Armed while ON…
    const armed = resolveShortcut(key("g")).nextState;
    expect(hasPendingChord(armed)).toBe(true);
    // …and now OFF: the pending prefix must not complete into a navigation.
    const result = resolveShortcut(key("i"), armed, off);
    expect(result.action).toBeUndefined();
    expect(hasPendingChord(result.nextState)).toBe(false);
  });

  it("still ignores modified keys, so the browser keeps Ctrl+R", () => {
    expect(
      resolveShortcut(key("r", { ctrlKey: true }), INITIAL_KEYBOARD_STATE, off).action,
    ).toBeUndefined();
  });

  it("still refuses to fire inside a text field", () => {
    const target = { tagName: "INPUT" } as unknown as EventTarget;
    expect(resolveShortcut(key("e", { target }), INITIAL_KEYBOARD_STATE, off).action).toBeUndefined();
  });

  it("is ON by default — decision D-3, the signed divergence from Gmail", () => {
    // Omitting the options object must not silently disable the map.
    expect(resolveShortcut(key("e")).action).toEqual({ kind: "archive" });
    expect(resolveShortcut(key("e"), INITIAL_KEYBOARD_STATE, {}).action).toEqual({ kind: "archive" });
  });

  it("names exactly the two always-on keys", () => {
    expect(isAlwaysOnKey("Escape")).toBe(true);
    expect(isAlwaysOnKey("/")).toBe(true);
    expect(isAlwaysOnKey("e")).toBe(false);
    expect(isAlwaysOnKey("?")).toBe(false);
  });
});

/**
 * E11 — layout independence (the `event.code` migration).
 *
 * The regression this suite exists for: before E11 every binding matched on
 * `event.key`, the CHARACTER the layout produces. On a Cyrillic layout the
 * physical `J` key produces `о`, so `j` never matched and the map was not
 * degraded but DEAD — for Russian, Greek, Hebrew and every non-QWERTY layout.
 */
describe("E11: the map resolves from the PHYSICAL key", () => {
  const letterBindings: readonly [string, string][] = [
    ["j", "next"],
    ["k", "previous"],
    ["o", "open"],
    ["u", "back"],
    ["e", "archive"],
    ["z", "undo"],
    ["s", "toggleFlag"],
    ["c", "compose"],
    ["r", "reply"],
    ["f", "forward"],
    ["l", "labelAs"],
    ["b", "snooze"],
    ["m", "toggleMute"],
    ["x", "selectRow"],
    ["n", "conversationMessage"],
    ["p", "conversationMessage"],
  ];

  it.each(letterBindings)("resolves %s on a US layout", (pressed, expected) => {
    expect(resolveShortcut(usKey(pressed)).action?.kind).toBe(expected);
  });

  /* The heart of the fix: same physical keys, Cyrillic glyphs. */
  it.each(letterBindings)("resolves the %s POSITION on a Cyrillic layout", (pressed, expected) => {
    const event = cyrillicKey(pressed);
    // Guard the fixture itself: if `key` were still Latin the test would pass
    // for the wrong reason and prove nothing.
    expect(event.key).not.toBe(pressed);
    expect(resolveShortcut(event).action?.kind).toBe(expected);
  });

  it("resolves the g chord from positions, not glyphs", () => {
    // `g` prints `п` and `i` prints `ш`; the chord must still reach Inbox.
    const armed = resolveShortcut(cyrillicKey("g")).nextState;
    expect(armed.pendingG).toBe(true);
    expect(resolveShortcut(cyrillicKey("i"), armed).action).toEqual({
      kind: "goToMailbox",
      role: "inbox",
    });
  });

  it("resolves the * chord from positions, not glyphs", () => {
    const armed = resolveShortcut(usKey("*")).nextState;
    expect(armed.pendingStar).toBe(true);
    // `a` prints `ф` on ЙЦУКЕН; `* a` must still select everything.
    expect(resolveShortcut(cyrillicKey("a"), armed).action).toEqual({
      kind: "selectBy",
      scope: "all",
    });
  });

  it("distinguishes shifted from unshifted on the SAME physical key", () => {
    // Semicolon: `;` expands, `:` collapses. One position, two meanings —
    // which only works because shift state is read alongside the code.
    expect(resolveShortcut(usKey(";")).action).toEqual({
      kind: "expandConversation",
      expand: true,
    });
    expect(resolveShortcut(usKey(":")).action).toEqual({
      kind: "expandConversation",
      expand: false,
    });
  });

  it("resolves the shifted symbol bindings by position on ANY layout", () => {
    // These are the ones that move most between layouts: on a German keyboard
    // `#` is its own key and `/` is Shift+7. Resolving by position is what
    // keeps them reachable at all.
    const shifted: readonly [string, string, string][] = [
      ["Digit3", "#", "delete"],
      ["Digit1", "!", "toggleSpam"],
      ["Minus", "_", "markUnreadFromHere"],
      ["Slash", "?", "help"],
    ];
    for (const [code, glyph, expected] of shifted) {
      // The glyph the layout prints is deliberately NOT the US one.
      const event = key("§", { code, shiftKey: true });
      expect(resolveShortcut(event).action?.kind, `${code} (${glyph})`).toBe(expected);
    }
  });

  it("resolves the unshifted symbol bindings by position", () => {
    const plain: readonly [string, string][] = [
      ["Slash", "focusSearch"],
      ["BracketLeft", "archiveAndAdvance"],
      ["BracketRight", "archiveAndAdvance"],
      ["Comma", "focusToolbar"],
      ["Period", "moreActions"],
    ];
    for (const [code, expected] of plain) {
      expect(resolveShortcut(key("щ", { code })).action?.kind, code).toBe(expected);
    }
  });

  it("keeps Enter, Escape and the arrows on `key`, where the character IS the semantics", () => {
    // No `code` at all: these must still resolve, because binding them to a
    // position would only split Enter from NumpadEnter for nothing.
    expect(resolveShortcut(key("Enter")).action?.kind).toBe("open");
    expect(resolveShortcut(key("Escape")).action?.kind).toBe("closeOverlay");
    expect(resolveShortcut(key("ArrowDown")).action?.kind).toBe("next");
    expect(resolveShortcut(key("ArrowUp")).action?.kind).toBe("previous");
  });

  it("still refuses modifiers and typing targets when resolving by code", () => {
    // The guards must not have been bypassed by the new resolution path.
    expect(resolveShortcut(cyrillicKey("e", { ctrlKey: true })).action).toBeUndefined();
    const target = { tagName: "INPUT" } as unknown as EventTarget;
    expect(resolveShortcut(cyrillicKey("e", { target })).action).toBeUndefined();
  });

  it("honours the shortcuts-off gate for the always-on keys by POSITION", () => {
    const off = { enabled: false };
    // `/` must stay reachable on a Cyrillic layout too, or turning shortcuts
    // off strands a non-Latin user with no way to search.
    expect(resolveShortcut(key(".", { code: "Slash" }), INITIAL_KEYBOARD_STATE, off).action).toEqual(
      { kind: "focusSearch" },
    );
    expect(resolveShortcut(cyrillicKey("e"), INITIAL_KEYBOARD_STATE, off).action).toBeUndefined();
  });

  it("falls back to `key` when the event carries no code", () => {
    // The pre-E11 call shape, which every other test in this file uses. It has
    // to keep working: a synthetic event without `code` is still an event.
    expect(resolveShortcut(key("j")).action?.kind).toBe("next");
    expect(resolveShortcut(key("j", { code: "" })).action?.kind).toBe("next");
  });

  it("ignores codes it does not bind rather than guessing", () => {
    expect(resolveShortcut(key("q", { code: "KeyQ" })).action).toBeUndefined();
    expect(resolveShortcut(key("F5", { code: "F5" })).action).toBeUndefined();
  });
});

/**
 * E11 — the Gmail map gaps that have a referent in our architecture.
 *
 * Canon §2.7. Skipped deliberately: Tasks, chat, tabs/sections and multiple
 * inboxes, none of which exist here.
 */
describe("E11: the closed map gaps", () => {
  it("splits Shift+I and Shift+U into a DIRECTIONAL pair, as Gmail does", () => {
    /*
     * This replaces a `toggleRead` binding that was our invention, not
     * Gmail's. The bug it hid: with a mixed selection a toggle has no defined
     * meaning, so "mark these fourteen read" was decided by whichever row
     * happened to be first.
     */
    expect(resolveShortcut(usKey("I")).action).toEqual({ kind: "markRead", read: true });
    expect(resolveShortcut(usKey("U")).action).toEqual({ kind: "markRead", read: false });
  });

  it("does not let the unshifted letters shadow the read pair", () => {
    // `u` alone leaves the reader and `i` alone is unbound; only the SHIFTED
    // presses mark read/unread.
    expect(resolveShortcut(usKey("u")).action?.kind).toBe("back");
    expect(resolveShortcut(usKey("i")).action).toBeUndefined();
  });

  it("binds , to the toolbar and . to the more-actions menu", () => {
    expect(resolveShortcut(usKey(",")).action).toEqual({ kind: "focusToolbar" });
    expect(resolveShortcut(usKey(".")).action).toEqual({ kind: "moreActions" });
  });

  it("makes . OPEN a menu rather than apply anything — it carries no payload", () => {
    expect(Object.keys(resolveShortcut(usKey(".")).action ?? {})).toEqual(["kind"]);
  });

  it("obeys the shortcuts-off setting for the new keys", () => {
    const off = { enabled: false };
    for (const k of [",", ".", "I", "U"]) {
      expect(resolveShortcut(usKey(k), INITIAL_KEYBOARD_STATE, off).action).toBeUndefined();
    }
  });

  it("does not fire the new keys while typing", () => {
    const target = { tagName: "INPUT" } as unknown as EventTarget;
    for (const k of [",", ".", "I", "U"]) {
      expect(resolveShortcut(usKey(k, { target })).action).toBeUndefined();
    }
  });
});

/** E11 — the sheet's grouping (canon §2.7: Gmail's cheat sheet is grouped). */
describe("E11: the help sheet's sections", () => {
  it("gives every entry a section from the declared set", () => {
    for (const entry of SHORTCUT_HELP) {
      expect(SHORTCUT_SECTIONS, entry.descriptionKey).toContain(entry.section);
    }
  });

  it("names every section it renders", () => {
    for (const section of SHORTCUT_SECTIONS) {
      expect(SECTION_TITLE_KEYS[section]).toMatch(/^shortcuts\.section/);
    }
  });

  it("leaves no section empty — an empty heading is a lie about the map", () => {
    for (const section of SHORTCUT_SECTIONS) {
      expect(
        SHORTCUT_HELP.filter((entry) => entry.section === section).length,
        section,
      ).toBeGreaterThan(0);
    }
  });

  it("documents the new bindings", () => {
    const rows = SHORTCUT_HELP.map((entry) => entry.keys.join(" "));
    expect(rows).toContain(",");
    expect(rows).toContain(".");
    expect(rows).toContain("Shift I");
    expect(rows).toContain("Shift U");
    // The composer keys are documented even though the global resolver does
    // not own them — the user does not care which module implements a key.
    expect(rows).toContain("Ctrl Enter");
    expect(rows).toContain("Ctrl Shift C");
    expect(rows).toContain("Ctrl Shift B");
  });
});

describe("E8: the label key", () => {
  it("resolves `l` to labelAs", () => {
    // Gmail's application keys (canon §2.7): "`v` move to · `l` label as".
    expect(resolveShortcut(key("l")).action).toEqual({ kind: "labelAs" });
  });

  it("OPENS a menu rather than applying anything — it carries no payload", () => {
    // One key cannot name one of up to 26 labels, so `l` raises the picker.
    // Gmail's does the same.
    const action = resolveShortcut(key("l")).action;
    expect(Object.keys(action ?? {})).toEqual(["kind"]);
  });

  it("is not shadowed by, and does not shadow, the g chord", () => {
    // `g l` has no target in CHORD_TARGETS, so it resolves to nothing rather
    // than falling through to the single-key label binding.
    const armed = resolveShortcut(key("g")).nextState;
    expect(resolveShortcut(key("l"), armed).action).toBeUndefined();
    expect(resolveShortcut(key("l")).action?.kind).toBe("labelAs");
  });

  it("obeys the shortcuts-off setting — it is a key that ACTS", () => {
    expect(resolveShortcut(key("l"), INITIAL_KEYBOARD_STATE, { enabled: false }).action)
      .toBeUndefined();
  });

  it("does not fire while the user is typing", () => {
    const target = { tagName: "INPUT" } as unknown as EventTarget;
    expect(resolveShortcut({ ...key("l"), target }).action).toBeUndefined();
  });

  it("appears in the help sheet, or it is a shortcut only its author uses", () => {
    expect(SHORTCUT_HELP.some((entry) => entry.keys.join("") === "l")).toBe(true);
  });
});

/**
 * E4 — the triage keys (canon §2.2, /mail/answer/7622010 and /16594169).
 *
 * The canon names all three explicitly: `b` for snooze, `m` for mute, `g b` to
 * reach the Snoozed view. What these tests protect is the interaction between
 * the two `b` bindings, which is exactly the shape that broke for `l`/`g l`.
 */
describe("E4: b, m and g b", () => {
  it("binds b to opening the snooze menu, not to applying a time", () => {
    // A single key cannot name one of five wake times, so `b` opens a picker —
    // the same shape as `l`.
    expect(resolveShortcut(key("b")).action).toEqual({ kind: "snooze" });
  });

  it("binds m to the mute TOGGLE", () => {
    // Mute is binary, so unlike `b` it acts immediately; and it toggles,
    // because pressing `m` on a conversation showing the muted badge can only
    // sensibly mean "stop muting it".
    expect(resolveShortcut(key("m")).action).toEqual({ kind: "toggleMute" });
  });

  it("lets the g chord shadow b, so `g b` is never read as snooze", () => {
    const armed = resolveShortcut(key("g")).nextState;
    expect(resolveShortcut(key("b"), armed).action).toEqual({ kind: "goToSnoozed" });
    expect(resolveShortcut(key("b")).action?.kind).toBe("snooze");
  });

  it("gives g b its OWN action rather than a role that does not exist", () => {
    // RFC 6154 has no SPECIAL-USE attribute for snoozed mail and the sync
    // engine refused to invent one, so the folder is found by the name the
    // session publishes. A `{kind:"goToMailbox", role:"snoozed"}` would make
    // the resolver look for something that cannot exist.
    const armed = resolveShortcut(key("g")).nextState;
    const action = resolveShortcut(key("b"), armed).action;
    expect(action).not.toHaveProperty("role");
  });

  it("clears the g prefix after b, like every other chord target", () => {
    const armed = resolveShortcut(key("g")).nextState;
    expect(resolveShortcut(key("b"), armed).nextState.pendingG).toBe(false);
  });

  it("obeys the shortcuts-off setting — both keys ACT on mail", () => {
    const off = { enabled: false };
    expect(resolveShortcut(key("b"), INITIAL_KEYBOARD_STATE, off).action).toBeUndefined();
    expect(resolveShortcut(key("m"), INITIAL_KEYBOARD_STATE, off).action).toBeUndefined();
  });

  it("does not fire while the user is typing", () => {
    const target = { tagName: "TEXTAREA" } as unknown as EventTarget;
    expect(resolveShortcut({ ...key("b"), target }).action).toBeUndefined();
    expect(resolveShortcut({ ...key("m"), target }).action).toBeUndefined();
  });

  it("does not shadow n/p — m is not a conversation-navigation key", () => {
    expect(resolveShortcut(key("n")).action?.kind).toBe("conversationMessage");
    expect(resolveShortcut(key("p")).action?.kind).toBe("conversationMessage");
  });

  it("documents all three in the help sheet", () => {
    const entries = SHORTCUT_HELP.map((entry) => entry.keys.join(" "));
    expect(entries).toContain("b");
    expect(entries).toContain("m");
    expect(entries).toContain("g b");
  });
});

/**
 * E-32 — the sheet documents the SEARCH BOX's own keys.
 *
 * They are the box's, not the resolver's: the typing guard refuses every global
 * key while an input has focus — correctly, or `e` would archive a message
 * while you typed one — so `SearchBar` handles them itself. Leaving them out of
 * the sheet made the one surface a person is most likely to get stuck in the
 * one surface the sheet said nothing about, which is exactly backwards.
 *
 * A user does not care which module implements a key. The composer's
 * Ctrl+Enter row is documented on the same footing and for the same reason.
 */
describe("E-32 — the search box's keys are on the cheat sheet", () => {
  const described = new Set(SHORTCUT_HELP.map((entry) => entry.descriptionKey));

  it("documents leaving the box, running the search, and the suggestions", () => {
    expect(described.has("shortcuts.searchLeave")).toBe(true);
    expect(described.has("shortcuts.searchRun")).toBe(true);
    expect(described.has("shortcuts.searchSuggestions")).toBe(true);
  });

  it("says the arrows in a way a person recognises", () => {
    const entry = SHORTCUT_HELP.find(
      (item) => item.descriptionKey === "shortcuts.searchSuggestions",
    );
    expect(entry?.keys).toEqual(["↑", "↓"]);
  });
});
