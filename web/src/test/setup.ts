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
