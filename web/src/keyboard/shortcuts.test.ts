import { describe, expect, it } from "vitest";

import {
  INITIAL_KEYBOARD_STATE,
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
    const result = resolveShortcut(key("z"), armed);
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
  it("documents every key the resolver binds", () => {
    const documented = new Set(SHORTCUT_HELP.flatMap((entry) => entry.keys));
    for (const k of ["j", "k", "Enter", "u", "/", "e", "#", "s", "?", "g"]) {
      const shown = k === "Enter" ? "Enter" : k;
      expect(
        documented.has(shown),
        `"${k}" is bound but missing from SHORTCUT_HELP`,
      ).toBe(true);
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
