import "@testing-library/jest-dom/vitest";

/**
 * Test environment setup.
 *
 * jsdom does not implement `matchMedia`, which the theme code reads. A minimal
 * stub is installed rather than a full polyfill: the tests that care about
 * theming assert on the `data-theme` attribute, which is what the CSS actually
 * keys on, and pretending to simulate a media query would test the stub.
 */
if (typeof globalThis.matchMedia !== "function") {
  Object.defineProperty(globalThis, "matchMedia", {
    writable: true,
    value: (query: string): MediaQueryList =>
      ({
        matches: false,
        media: query,
        onchange: null,
        addListener: () => undefined,
        removeListener: () => undefined,
        addEventListener: () => undefined,
        removeEventListener: () => undefined,
        dispatchEvent: () => false,
      }),
  });
}

/**
 * jsdom does not implement `<dialog>`'s modal behaviour, so `showModal`/`close`
 * are given the ONE behaviour component logic depends on: flip `.open` and fire
 * `close`.
 *
 * Deliberately NOT simulated: the focus trap and page inertness. Those are the
 * browser's job, and a stub asserting on itself would only pretend to test
 * them — they are verified in a real browser instead.
 *
 * Installed globally in E11, when the third and fourth components to use a
 * `<dialog>` arrived. Test files that already define it locally keep working:
 * they simply overwrite this with the identical implementation.
 */
if (typeof HTMLDialogElement !== "undefined") {
  HTMLDialogElement.prototype.showModal = function showModal(this: HTMLDialogElement) {
    this.open = true;
  };
  HTMLDialogElement.prototype.close = function close(this: HTMLDialogElement) {
    if (!this.open) return;
    this.open = false;
    this.dispatchEvent(new Event("close"));
  };
}
