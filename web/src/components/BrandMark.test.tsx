import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { BrandMark } from "./BrandMark";
import { ASPECT_WIDE_MIN, SIZE_HEIGHTS, logoShapeOf } from "./brandMarkShape";
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
    privacyUrl: "",
    termsUrl: "",
    isDefault: false,
    ...overrides,
  };
}

/**
 * Fires a `load` on an image with a defined intrinsic size.
 *
 * jsdom never fetches, so `naturalWidth`/`naturalHeight` are 0 for every image
 * and the component would classify nothing. Defining them on the element and
 * then firing the event is the only way to exercise the measurement at all —
 * and it is the measurement, not the network, that the rule is about.
 */
function loadWith(image: HTMLImageElement | null, width: number, height: number): void {
  expect(image).not.toBeNull();
  if (image === null) return;
  Object.defineProperty(image, "naturalWidth", { value: width, configurable: true });
  Object.defineProperty(image, "naturalHeight", { value: height, configurable: true });
  fireEvent.load(image);
}

/**
 * The `--brandmark-max-width` a size/shape block declares, in px.
 *
 * Read out of the stylesheet rather than mirrored in TS, because the point of
 * the two tests that use it is to catch a CEILING that moved in CSS — a mirror
 * would move with the edit and assert nothing.
 */
function maxWidthOf(sheet: string, size: string, shape: string): number {
  const block = new RegExp(
    String.raw`\.${size}\.${shape}\s*\{[^}]*--brandmark-max-width:\s*(\d+)px`,
  ).exec(sheet);
  expect(block, `${size}.${shape} declares a max-width`).not.toBeNull();
  return Number(block?.[1] ?? 0);
}

/**
 * The body of a rule whose selector list is EXACTLY `selector`.
 *
 * A bare `.name {` search would also hit `.lg .name {` and `.onDark .name {`,
 * and the point of these assertions is precisely to tell those apart — the bug
 * being pinned is one rule inheriting the other's treatment. So the match
 * requires the selector to START a rule (following a closing brace or a
 * comment) rather than merely start a line.
 */
function ruleBody(sheet: string, selector: string): string {
  const escaped = selector.replace(/[.[\]"=]/g, (c) => "\\" + c);
  const match = new RegExp(String.raw`(?:\}|\*/)\s*${escaped}\s*\{([^}]*)\}`).exec(sheet);
  expect(match, `the ${selector} rule`).not.toBeNull();
  return match?.[1] ?? "";
}

describe("a customer logo", () => {
  it("lets a WIDE mark replace the text name rather than accompany it", () => {
    const { container } = render(<BrandMark branding={branding({ logoUrl: LOGO })} />);
    loadWith(container.querySelector("img"), 320, 80);

    expect(container.querySelector("img")).not.toBeNull();
    // The name must appear EXACTLY once, as the image's accessible name — not
    // a second time as text, which is what truncated on the login panel.
    expect(screen.getByAltText("ACME Mail")).toBeInTheDocument();
    expect(screen.queryByText("ACME Mail")).not.toBeInTheDocument();
  });

  it("brings the text name BACK for a square mark", () => {
    /*
     * The owner's finding, and the reason the rule is by shape rather than
     * absolute: a square glyph names nothing. Shown alone it leaves the product
     * unnamed anywhere on the screen. Gmail's own bar is the reference — an "M"
     * glyph WITH the word "Gmail" beside it.
     */
    const { container } = render(<BrandMark branding={branding({ logoUrl: LOGO })} />);
    loadWith(container.querySelector("img"), 512, 512);

    expect(screen.getByText("ACME Mail")).toBeInTheDocument();
    /*
     * And exactly ONCE: with the name rendered as text the image becomes
     * decorative, or a screen reader would hear the brand twice — the same
     * fault the wordmark rule avoids, in the other direction.
     */
    expect(screen.queryByAltText("ACME Mail")).not.toBeInTheDocument();
    expect(container.querySelector("img")).toHaveAttribute("aria-hidden", "true");
  });

  it("still hides the name for a square mark when asked for the icon alone", () => {
    const { container } = render(
      <BrandMark branding={branding({ logoUrl: LOGO })} iconOnly />,
    );
    loadWith(container.querySelector("img"), 512, 512);
    expect(screen.queryByText("ACME Mail")).not.toBeInTheDocument();
  });

  it("truncates a long name in the TOP BAR, where the track cannot grow", () => {
    // The 244px track's guarantee only holds while the name can give way, so
    // the default treatment is one line, ellipsised.
    expect(css()).toMatch(/\.name \{[^}]*text-overflow: ellipsis/);
    expect(css()).toMatch(/\.name \{[^}]*white-space: nowrap/);
    expect(css()).toMatch(/\.name \{[^}]*min-width: 0/);
  });

  it("STACKS the square lockup on the login panel, name under the mark", () => {
    /*
     * Measured on the live panel: the content column is 380px, and a square
     * mark beside its name leaves the name a 176px box. "NU Desarrollos
     * Conscientes" needs three-plus lines in 176px at a heading size, so the
     * previous side-by-side wrap rendered "NU / Desarroll…" — the same
     * abbreviation it was written to remove, reached by a different route.
     *
     * Wrapping cannot fix a box that narrow. Giving the name the whole column
     * can, which is where a real lockup puts a name that does not fit beside
     * the mark.
     */
    expect(ruleBody(css(), ".lg.square")).toContain("flex-direction: column");

    const name = ruleBody(css(), ".lg.square .name");
    // The whole column, not the 176px remainder beside an 80px logo.
    expect(name).toContain("width: 100%");
    /*
     * A step down from the row's --text-3xl: with the full width the name no
     * longer needs the heading size to carry presence, and the smaller step is
     * what lets a 26-character brand land in two lines instead of three.
     */
    expect(name).toContain("font-size: var(--text-2xl)");
    expect(name).not.toContain("--text-3xl");
  });

  it("keeps the stacked name wrapping, clipped, and capped at two lines", () => {
    const name = ruleBody(css(), ".lg.square .name");

    expect(name).toContain("white-space: normal");
    // Clip, not an ellipsis: a name that still overruns is CUT rather than
    // decorated with a "…" that falsely implies the rest was fetched.
    expect(name).toContain("text-overflow: clip");
    expect(name).not.toContain("ellipsis");
    // The guarantee for a name that is one unbroken token longer than the
    // column: without it the panel overflows sideways rather than wrapping.
    expect(name).toContain("overflow-wrap: anywhere");
    // Two lines. Past that a lockup becomes a paragraph, and the tagline below
    // is where prose belongs.
    expect(name).toContain("line-clamp: 2");
  });

  it("leaves a WIDE mark on the panel exactly as it was", () => {
    /*
     * A wordmark already says the name graphically, so it renders alone and
     * has nothing to stack. Only the square case changed, and this says so —
     * the stacking must not leak into the shape that never had the problem.
     */
    const sheet = css();
    expect(ruleBody(sheet, ".lg.wide")).not.toContain("flex-direction");
    expect(sheet).not.toContain(".lg.wide .name");

    const { container } = render(
      <BrandMark branding={branding({ logoUrl: LOGO })} size="lg" onDark />,
    );
    loadWith(container.querySelector("img"), 320, 80);
    // The name is not rendered at all beside a wordmark.
    expect(screen.queryByText("ACME Mail")).not.toBeInTheDocument();
    expect(container.firstElementChild?.className).toContain("wide");
  });

  it("keeps the ellipsis OUT of the login panel and IN the top bar", () => {
    /*
     * The two rules must not converge. Stated as a single assertion because
     * the failure mode is a later edit "simplifying" one of them into the
     * other — and either direction is a bug: an abbreviated brand on the
     * panel, or a top bar whose name wraps and grows the bar.
     */
    expect(ruleBody(css(), ".name")).toContain("text-overflow: ellipsis");
    expect(ruleBody(css(), ".lg.square .name")).not.toContain("ellipsis");
  });

  it("left-aligns the stacked lockup rather than centring the mark over it", () => {
    // In a column `align-items` is the CROSS axis, so this is what puts the
    // logo flush with the left edge of the name below it instead of centring
    // an 80px mark over a 380px block.
    expect(ruleBody(css(), ".mark")).toContain("align-items: center");
    expect(ruleBody(css(), ".lg")).toContain("align-items: flex-start");
  });


  it("still names the brand for a screen reader beside a wordmark, iconOnly or not", () => {
    // `iconOnly` is about the TEXT label. Beside a wordmark the name is always
    // in the alt, so a screen reader hears the brand either way.
    for (const iconOnly of [true, false]) {
      const { container, unmount } = render(
        <BrandMark branding={branding({ logoUrl: LOGO })} iconOnly={iconOnly} />,
      );
      loadWith(container.querySelector("img"), 320, 80);
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
  it("agrees with the stylesheet, per size AND per shape", () => {
    /*
     * SIZE_HEIGHTS is duplicated in TypeScript because an HTML attribute
     * cannot read a CSS class, and it is the attribute that reserves the box
     * before any stylesheet loads. Nothing but this test stops the two copies
     * from drifting — and a drift is invisible: the layout would simply shift
     * on load, which nobody would attribute to a two-pixel disagreement.
     */
    const sheet = css();
    for (const size of ["sm", "md", "lg"] as const) {
      for (const shape of ["wide", "square"] as const) {
        expect(sheet, `${size}.${shape}`).toContain(
          `.${size}.${shape} {
  --brandmark-height: ${String(SIZE_HEIGHTS[size][shape])}px;`,
        );
      }
    }
  });

  it("renders a wide mark at the wide height, before and after it loads", () => {
    /*
     * WIDE is the pre-load assumption, and this is why: a wordmark — the
     * common upload — must render at its final size on the first frame and
     * never move. Assuming square would shrink every one of them on load, a
     * shift on the most-visited screen in the product.
     */
    const { container } = render(
      <BrandMark branding={branding({ logoUrl: LOGO })} size="sm" />,
    );
    const logo = container.querySelector("img");
    expect(logo).toHaveAttribute("height", String(SIZE_HEIGHTS.sm.wide));
    expect(container.firstElementChild?.className).toContain("wide");

    loadWith(logo, 320, 80);
    expect(container.querySelector("img")).toHaveAttribute(
      "height",
      String(SIZE_HEIGHTS.sm.wide),
    );
    expect(container.firstElementChild?.className).toContain("wide");
  });

  it("grows a SQUARE mark once it has measured it", () => {
    // The finding: a square logo at a wordmark's height is a postage stamp.
    const { container } = render(
      <BrandMark branding={branding({ logoUrl: LOGO })} size="sm" />,
    );
    loadWith(container.querySelector("img"), 512, 512);

    expect(container.querySelector("img")).toHaveAttribute(
      "height",
      String(SIZE_HEIGHTS.sm.square),
    );
    expect(container.firstElementChild?.className).toContain("square");
    expect(SIZE_HEIGHTS.sm.square).toBeGreaterThan(SIZE_HEIGHTS.sm.wide);
  });

  it("takes the login panel to 56 / 80, not 44", () => {
    for (const [w, h, want] of [
      [320, 80, SIZE_HEIGHTS.lg.wide],
      [512, 512, SIZE_HEIGHTS.lg.square],
    ] as const) {
      const { container, unmount } = render(
        <BrandMark branding={branding({ logoUrl: LOGO })} size="lg" onDark />,
      );
      loadWith(container.querySelector("img"), w, h);
      expect(container.querySelector("img")).toHaveAttribute("height", String(want));
      unmount();
    }
  });

  it("puts the aspect line at 1.6, on the square side of a lockup", () => {
    /*
     * A 1.5:1 lockup has no room for a readable word and needs the name beside
     * it exactly as a square mark does; a 2:1 strip is a wordmark. The failure
     * of calling a square mark wide (a stamp, with the product unnamed) is far
     * worse than the failure the other way, so the line is generous toward
     * square.
     */
    expect(ASPECT_WIDE_MIN).toBe(1.6);
    expect(logoShapeOf(150, 100)).toBe("square");
    expect(logoShapeOf(200, 100)).toBe("wide");
    // An image whose intrinsic size the browser does not know (a decode
    // failure, or jsdom's 0x0) classifies as nothing, and the caller keeps its
    // wide assumption rather than jumping to a measurement that is not one.
    expect(logoShapeOf(0, 0)).toBeUndefined();
    expect(logoShapeOf(100, 0)).toBeUndefined();
  });

  it("caps the logo's width at every size and shape", () => {
    // Without a ceiling a pathological upload — a 20:1 banner — takes the
    // layout with it. In the top bar that would mean pushing the centred
    // search pill, which the fixed 244px grid track exists to prevent.
    const sheet = css();
    expect(sheet).toContain("max-width: var(--brandmark-max-width)");
    for (const size of ["sm", "md", "lg"] as const) {
      for (const shape of ["wide", "square"] as const) {
        expect(maxWidthOf(sheet, size, shape), `${size}.${shape}`).toBeGreaterThan(0);
      }
    }
  });

  it("keeps the top bar's 244px brand track intact at every shape", () => {
    /*
     * THE INVARIANT E12 pinned the whole muscle-memory layout to: the brand
     * track is a FIXED 244px grid column, and the centred search pill's
     * position is only stable while everything in that track fits inside it. A
     * logo that grew past the track would push the pill — the one piece of
     * chrome that must never move.
     *
     * jsdom applies no layout, so the arithmetic is checked against the
     * declared numbers: the hamburger, the gaps, and the widest the mark may
     * be. The NAME is excluded on purpose — it ellipsises (`min-width: 0`,
     * pinned above), so it cannot contribute to overflow.
     */
    const bar = readFileSync(resolve(here, "../screens/mail/TopBar.module.css"), "utf8");
    expect(bar).toContain("grid-template-columns: 244px minmax(0, 1fr) auto");

    const sheet = css();
    // The hamburger button plus the two gaps in the left cluster, generously.
    const CHROME = 36 + 8 + 12;
    for (const shape of ["wide", "square"] as const) {
      expect(
        CHROME + maxWidthOf(sheet, "sm", shape),
        `sm.${shape} inside the 244px track`,
      ).toBeLessThan(244);
    }
  });

  it("reserves horizontal space so a slow logo cannot shift what follows it", () => {
    expect(css()).toContain("min-width: var(--brandmark-height)");
  });
});
