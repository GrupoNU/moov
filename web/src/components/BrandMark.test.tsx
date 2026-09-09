import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { BrandMark } from "./BrandMark";
import type { Branding } from "../branding/branding";

/**
 * The brand mark, tested for the one thing a white-label product cannot get
 * wrong: a customer's logo must arrive on screen the shape they drew it.
 */

const here = dirname(fileURLToPath(import.meta.url));

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

describe("BrandMark with a customer logo", () => {
  it("reserves the box by HEIGHT and leaves the width to the image", () => {
    /*
     * THE test of this change. A `width` attribute beside the height is an
     * aspect ratio the browser enforces, so it squashed every wordmark — the
     * common upload — into a square. The height alone still reserves a line box
     * of the right height (the CLS guarantee the attributes were added for)
     * while letting the intrinsic ratio decide the width.
     */
    const { container } = render(
      <BrandMark branding={branding({ logoUrl: "/branding/logo.png" })} />,
    );
    const logo = container.querySelector("img");
    expect(logo).not.toBeNull();
    expect(logo).toHaveAttribute("height");
    expect(logo).not.toHaveAttribute("width");
  });

  it("reserves a height that matches the size it was asked for", () => {
    const { container } = render(
      <BrandMark branding={branding({ logoUrl: "/branding/logo.png" })} size="lg" />,
    );
    expect(container.querySelector("img")).toHaveAttribute("height", "44");
  });

  it("names the brand for a screen reader only when the name is not beside it", () => {
    // Unchanged behaviour, pinned because it is easy to break while editing
    // the attributes next to it: with the name rendered as text, a described
    // image would make a screen reader say the brand twice.
    const { rerender } = render(
      <BrandMark branding={branding({ logoUrl: "/branding/logo.png" })} iconOnly />,
    );
    expect(screen.getByAltText("ACME Mail")).toBeInTheDocument();

    rerender(<BrandMark branding={branding({ logoUrl: "/branding/logo.png" })} />);
    expect(screen.queryByAltText("ACME Mail")).not.toBeInTheDocument();
    expect(screen.getByText("ACME Mail")).toBeInTheDocument();
  });
});

describe("BrandMark without a logo", () => {
  it("draws Moov's own glyph, and no broken image", () => {
    // The default case: most installations never configure a brand, so the
    // fallback is what MOST people see. An <img> with an empty src would be a
    // broken-image icon on every one of those screens.
    const { container } = render(<BrandMark branding={branding({ name: "Moov Mail" })} />);
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("svg")).not.toBeNull();
    expect(screen.getByText("Moov Mail")).toBeInTheDocument();
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
    const css = readFileSync(resolve(here, "BrandMark.module.css"), "utf8");
    for (const [size, height] of [
      ["sm", 28],
      ["md", 32],
      ["lg", 44],
    ] as const) {
      expect(css, `${size} height`).toContain(`--brandmark-height: ${height}px`);
      const { container } = render(
        <BrandMark branding={branding({ logoUrl: "/l.png" })} size={size} />,
      );
      expect(container.querySelector("img")).toHaveAttribute("height", String(height));
    }
  });

  it("caps the logo's width at every size", () => {
    // Without a ceiling a pathological upload — a 20:1 banner — takes the
    // layout with it. In the top bar that would mean pushing the centred
    // search pill, which the fixed 244px grid track exists to prevent.
    const css = readFileSync(resolve(here, "BrandMark.module.css"), "utf8");
    expect(css).toContain("max-width: var(--brandmark-max-width)");
    expect(css).toContain("--brandmark-max-width: 120px");
    expect(css).toContain("--brandmark-max-width: 160px");
    expect(css).toContain("--brandmark-max-width: 200px");
  });

  it("reserves horizontal space so a slow logo cannot shift what follows it", () => {
    expect(readFileSync(resolve(here, "BrandMark.module.css"), "utf8")).toContain(
      "min-width: var(--brandmark-height)",
    );
  });
});
