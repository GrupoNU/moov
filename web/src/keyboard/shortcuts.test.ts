import { describe, expect, it } from "vitest";

import {
  hasPendingChord,
  INITIAL_KEYBOARD_STATE,
  isAlwaysOnKey,
  isTypingTarget,
  resolveShortcut,
  SHORTCUT_HELP,
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
