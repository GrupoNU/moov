import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { BrandMark } from "./BrandMark";
import type { Branding } from "../branding/branding";

/**
 * The brand mark, tested for the two things a white-label product cannot get
 * wrong: a customer's logo must arrive the shape they drew it, and it must be
 * VISIBLE wherever it lands.
 */

const here = dirname(fileURLToPath(import.meta.url));
const css = (): string => readFileSync(resolve(here, "BrandMark.module.css"), "utf8");

const LOGO = "/branding/assets/acme/logo.png";
const LOGO_DARK = "/branding/assets/acme/logo-dark.png";

/**
 * A branding fixture.
 *
 * Fully typed with no cast: `as Branding` would let a field be dropped from
 * the wire contract without a single test noticing, which is exactly the kind
 * of silence the branding types exist to prevent.
 */
function branding(overrides: Partial<Branding> = {}): Branding {
  return {
    name: "ACME Mail",
    logoUrl: "",
    logoDarkUrl: "",
    splashUrl: "",
    colors: {
      primary: "#5b5bd6",
      onPrimary: "#ffffff",
      splashFrom: "#5b5bd6",
      splashTo: "#8b5cf6",
    },
    tagline: "",
    supportUrl: "",
    isDefault: false,
    ...overrides,
  };
}

describe("a customer logo", () => {
  it("replaces the text name rather than accompanying it", () => {
    const { container } = render(<BrandMark branding={branding({ logoUrl: LOGO })} />);

    expect(container.querySelector("img")).not.toBeNull();
    // The name must appear EXACTLY once, as the image's accessible name — not
    // a second time as text, which is what truncated on the login panel.
    expect(screen.getByAltText("ACME Mail")).toBeInTheDocument();
    expect(screen.queryByText("ACME Mail")).not.toBeInTheDocument();
  });

  it("still names the brand for a screen reader, iconOnly or not", () => {
    // `iconOnly` is about the FALLBACK glyph's text label. With a logo the name
    // is always in the alt, so a screen reader hears the brand either way.
    for (const iconOnly of [true, false]) {
      const { unmount } = render(
        <BrandMark branding={branding({ logoUrl: LOGO })} iconOnly={iconOnly} />,
      );
      expect(screen.getByAltText("ACME Mail")).toBeInTheDocument();
      unmount();
    }
  });

  it("reserves the box by HEIGHT and leaves the width to the image", () => {
    /*
     * A `width` attribute beside the height is an aspect ratio the browser
     * enforces, so it squashed every wordmark — the common upload — into a
     * square. The height alone still reserves a line box of the right height
     * (the CLS guarantee) while letting the intrinsic ratio decide the width.
     */
    const { container } = render(<BrandMark branding={branding({ logoUrl: LOGO })} />);
    const logo = container.querySelector("img");
    expect(logo).not.toBeNull();
    expect(logo).toHaveAttribute("height");
    expect(logo).not.toHaveAttribute("width");
  });

  it("reserves a height that matches the size it was asked for", () => {
    const { container } = render(
      <BrandMark branding={branding({ logoUrl: LOGO })} size="lg" />,
    );
    expect(container.querySelector("img")).toHaveAttribute("height", "44");
  });
});

describe("the fallback glyph keeps the text name", () => {
  it("draws Moov's own glyph beside the name, and no broken image", () => {
    // The default case: most installations never configure a brand. An <img>
    // with an empty src would be a broken-image icon on every one of those
    // screens, and the glyph names nothing on its own — so here the text IS
    // the brand and must stay.
    const { container } = render(<BrandMark branding={branding({ name: "Moov Mail" })} />);
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("svg")).not.toBeNull();
    expect(screen.getByText("Moov Mail")).toBeInTheDocument();
  });

  it("drops the text name when asked for the icon alone", () => {
    render(<BrandMark branding={branding({ name: "Moov Mail" })} iconOnly />);
    expect(screen.queryByText("Moov Mail")).not.toBeInTheDocument();
  });
});

describe("a dark context picks the dark logo", () => {
  it("renders ONLY the dark variant on the brand panel", () => {
    /*
     * `onDark` is the login panel, whose gradient is dark in every theme — so
     * the dark variant is the only correct one and the light one must not be in
     * the DOM at all. A CSS-hidden sibling would still be fetched, which is a
     * wasted request on the app's first paint.
     */
    const { container } = render(
      <BrandMark branding={branding({ logoUrl: LOGO, logoDarkUrl: LOGO_DARK })} onDark />,
    );
    const images = [...container.querySelectorAll("img")];
    expect(images).toHaveLength(1);
    expect(images[0]).toHaveAttribute("src", LOGO_DARK);
    expect(images[0]).toHaveAttribute("alt", "ACME Mail");
  });

  it("emits BOTH variants for the theme swap, with only one announced", () => {
    // Off the brand panel the theme decides, and the theme is CSS's to know.
    // Both are in the DOM; the alt lives on exactly one, because the two are
    // the same information in different colours.
    const { container } = render(
      <BrandMark branding={branding({ logoUrl: LOGO, logoDarkUrl: LOGO_DARK })} />,
    );
    const images = [...container.querySelectorAll("img")];
    expect(images.map((img) => img.getAttribute("src"))).toEqual([LOGO, LOGO_DARK]);
    expect(screen.getAllByAltText("ACME Mail")).toHaveLength(1);
    expect(images[1]).toHaveAttribute("alt", "");
    expect(images[1]).toHaveAttribute("aria-hidden", "true");
  });

  it("swaps in CSS with the same three-state pattern tokens.css uses", () => {
    /*
     * The swap must survive an explicit theme choice in BOTH directions, and
     * the media query must not override a user who chose light on a dark OS.
     * jsdom applies no styles, so this is asserted against the stylesheet — the
     * defect it guards is only visible in a real browser.
     */
    const sheet = css();
    expect(sheet).toContain('@media (prefers-color-scheme: dark)');
    expect(sheet).toContain(':root:not([data-theme="light"]) .logoDark');
    expect(sheet).toContain(':root[data-theme="dark"] .logoDark');
    expect(sheet).toContain(':root[data-theme="dark"] .logoLight');
  });

  it("never reads the theme in JavaScript", () => {
    /*
     * A JS theme check would be a second source of truth for something the
     * cascade already knows, and it would paint the wrong logo for one frame on
     * every load — the exact class of bug the pre-paint script in index.html
     * exists to prevent for colours.
     *
     * Comments are stripped before matching, because the component's docs
     * legitimately DISCUSS `data-theme` while the code must never touch it.
     */
    const source = readFileSync(resolve(here, "BrandMark.tsx"), "utf8")
      .replace(/\/\*[\s\S]*?\*\//g, "")
      .replace(/\/\/.*$/gm, "");
    for (const forbidden of ["matchMedia", "data-theme", "prefers-color-scheme"]) {
      expect(source, `${forbidden} must not appear in the component's code`).not.toContain(
        forbidden,
      );
    }
  });
});

describe("the plate: a dark wordmark with no dark variant", () => {
  it("plates the logo when the customer supplied no dark variant", () => {
    /*
     * The case a real pilot brand hits today: one pure-black wordmark, which is
     * invisible on the dark gradient. It gets a light rectangle behind it — the
     * logo's own pixels are never touched, because recolouring (invert, blend)
     * wrecks any mark that is not a flat silhouette.
     */
    const { container } = render(
      <BrandMark branding={branding({ logoUrl: LOGO })} onDark />,
    );
    expect(container.firstElementChild?.className).toContain("plated");
  });

  it("does NOT plate when a dark variant exists", () => {
    // With a real dark asset the plate would be a box around a logo that is
    // already legible — visible chrome that should not be there.
    const { container } = render(
      <BrandMark branding={branding({ logoUrl: LOGO, logoDarkUrl: LOGO_DARK })} onDark />,
    );
    expect(container.firstElementChild?.className).not.toContain("plated");
  });

  it("does NOT plate the drawn fallback", () => {
    // The glyph inherits currentColor, so it is legible in every context by
    // construction and a plate would just be a stray box.
    const { container } = render(<BrandMark branding={branding()} onDark />);
    expect(container.firstElementChild?.className).not.toContain("plated");
  });

  it("paints the plate on the dark panel and in the dark theme", () => {
    const sheet = css();
    // Unconditional on the brand panel: that gradient is dark in every theme.
    expect(sheet).toContain(".plated.onDark .logo");
    // And theme-driven elsewhere, in both directions.
    expect(sheet).toContain(':root[data-theme="dark"] .plated .logo');
    expect(sheet).toContain(':root:not([data-theme="light"]) .plated .logo');
  });

  it("uses a plate colour that does not follow the theme", () => {
    /*
     * THE subtle one. `--surface-default` is itself dark under the dark theme,
     * so plating with it would paint a dark plate under a dark logo and change
     * nothing at all — a fix that looks right in the code and does nothing on
     * screen. The plate must be light in a dark context by definition.
     */
    const sheet = css();
    expect(sheet).toContain("--brandmark-plate: #f6f7fb");
    expect(sheet).toContain("background: var(--brandmark-plate)");
    expect(sheet).not.toContain("background: var(--surface-default)");
  });

  it("keeps the plated logo the same size as an unplated one", () => {
    // The padding sits inside the fixed height, so without content-box the
    // plated mark would render visibly smaller than every other brand's.
    expect(css()).toContain("box-sizing: content-box");
  });
});

describe("the size table", () => {
  it("agrees with the stylesheet", () => {
    /*
     * SIZE_HEIGHTS is duplicated in TypeScript because an HTML attribute
     * cannot read a CSS class, and it is the attribute that reserves the box
     * before any stylesheet loads. Nothing but this test stops the two copies
     * from drifting — and a drift is invisible: the layout would simply shift
     * on load, which nobody would attribute to a two-pixel disagreement.
     */
    const sheet = css();
    for (const [size, height] of [
      ["sm", 28],
      ["md", 32],
      ["lg", 44],
    ] as const) {
      expect(sheet, `${size} height`).toContain(`--brandmark-height: ${height}px`);
      const { container, unmount } = render(
        <BrandMark branding={branding({ logoUrl: LOGO })} size={size} />,
      );
      expect(container.querySelector("img")).toHaveAttribute("height", String(height));
      unmount();
    }
  });

  it("caps the logo's width at every size", () => {
    // Without a ceiling a pathological upload — a 20:1 banner — takes the
    // layout with it. In the top bar that would mean pushing the centred
    // search pill, which the fixed 244px grid track exists to prevent.
    const sheet = css();
    expect(sheet).toContain("max-width: var(--brandmark-max-width)");
    expect(sheet).toContain("--brandmark-max-width: 120px");
    expect(sheet).toContain("--brandmark-max-width: 160px");
    expect(sheet).toContain("--brandmark-max-width: 200px");
  });

  it("reserves horizontal space so a slow logo cannot shift what follows it", () => {
    expect(css()).toContain("min-width: var(--brandmark-height)");
  });
});
