import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { BrandPanel } from "./BrandPanel";
import type { Branding } from "../../branding/branding";

/**
 * The login brand panel, tested for the rule an owner's first real splash
 * image broke: A CUSTOMER'S PHOTOGRAPH IS SHOWN AS THEY UPLOADED IT.
 *
 * The panel used to blend the image into the brand gradient — `mix-blend-mode:
 * overlay` at 85% opacity — which came out as a violet wash over their picture,
 * violet because the gradient stops still defaulted to Moov's. Two separate
 * faults, and this file pins the half that lives here: no tint, ever.
 *
 * Most of it is asserted against the STYLESHEET rather than against rendered
 * boxes, because jsdom applies no styles and a blend mode is invisible to it —
 * the defect was only ever visible in a real browser, so the text of the rule
 * is what has to be pinned.
 */

const here = dirname(fileURLToPath(import.meta.url));
const css = (): string => readFileSync(resolve(here, "BrandPanel.module.css"), "utf8");

const SPLASH = "/branding/assets/acme/splash.jpg";
/** A logo with NO dark variant — the case the plate exists for. */
const LOGO = "/branding/assets/acme/logo.png";

function branding(overrides: Partial<Branding> = {}): Branding {
  return {
    name: "Acme Mail",
    logoUrl: "",
    logoDarkUrl: "",
    splashUrl: "",
    colors: {
      primary: "#b8faff",
      onPrimary: "#ffffff",
      splashFrom: "#374b4d",
      splashTo: "#78a3a6",
    },
    tagline: "Correo de Acme",
    supportUrl: "",
    privacyUrl: "",
    termsUrl: "",
    isDefault: false,
    ...overrides,
  };
}

/**
 * The body of a rule whose selector list is EXACTLY `selector`.
 *
 * A bare `.image {` search hits the grouped positioning rule
 * (`.gradient,\n.image,\n.aurora,\n.grid,\n.scrim {`) and the scoped
 * `.panel[data-has-image="true"] .image {` as well — neither of which is the
 * rule under test. So the match requires the selector to be the WHOLE list: a
 * line start, the selector, then the brace. That also fails loudly if the rule
 * is ever renamed, which is the point of reading CSS from a test at all.
 */
function ruleBody(sheet: string, selector: string): string {
  const escaped = selector.replace(/[.[\]"=]/g, (c) => "\\" + c);
  /*
   * The lookbehind is what makes "the whole selector list" precise: the
   * selector must follow a closing brace or a comment, i.e. START a rule
   * rather than merely start a LINE. The grouped positioning rule puts
   * `.scrim {` on a line of its own as its last member, so a line anchor
   * matched that instead — and matched it FIRST, returning `inset: 0`.
   *
   * No `m` flag, deliberately: with it, `^` inside the lookbehind would match
   * every line start and reintroduce exactly that bug.
   */
  const match = new RegExp(String.raw`(?:\}|\*/)\s*${escaped}\s*\{([^}]*)\}`).exec(sheet);
  expect(match, `the ${selector} rule`).not.toBeNull();
  return match?.[1] ?? "";
}

describe("a customer's splash image", () => {
  it("is never tinted, blended or dimmed", () => {
    /*
     * THE RULE. An operator who uploads a picture has already made the visual
     * decision; tinting it is the product overriding them with an effect they
     * cannot see in advance, turn off, or predict — the result depends on their
     * pixels. `object-fit: cover` and nothing else.
     */
    const rules = ruleBody(css(), ".image");

    expect(rules).toContain("object-fit: cover");
    expect(rules).not.toContain("mix-blend-mode");
    expect(rules).not.toContain("opacity");
    expect(rules).not.toContain("filter");
  });

  it("keeps the image untinted on the collapsed mobile band too", () => {
    /*
     * The band used to drop the image to 55% opacity "because at that height it
     * adds noise rather than atmosphere" — the same override in a second place,
     * and the place a phone user meets first.
     */
    // Anywhere in the sheet, including inside a media query: every rule whose
    // selector ends in `.image` must be free of an opacity declaration.
    for (const [, body] of css().matchAll(/\.image\s*\{([^}]*)\}/g)) {
      expect(body).not.toContain("opacity");
      expect(body).not.toContain("mix-blend-mode");
    }
  });

  it("paints the image OVER the gradient, by declaration and not by DOM order", () => {
    /*
     * The gradient stays as the backdrop while the image decodes and as the
     * fallback if it never arrives — but it must never contribute to the pixels
     * once the picture is there. DOM order alone would deliver that today and
     * lose it to the first edit that reorders the layers, so the stacking is
     * written down.
     */
    const sheet = css();
    expect(sheet).toContain('.panel[data-has-image="true"] .gradient');
    expect(sheet).toContain('.panel[data-has-image="true"] .image');
  });

  it("paints NOTHING over the photograph — not even a scrim", () => {
    /*
     * The second half of the same finding. Removing the blend and keeping the
     * scrim was half a fix: the owner read the bottom darkening as a tint on
     * their own picture, which is exactly what it is. With an image the panel
     * paints nothing over it at all.
     */
    const { container } = render(<BrandPanel branding={branding({ splashUrl: SPLASH })} />);
    expect(container.querySelector('[class*="scrim"]')).toBeNull();
    expect(container.querySelector('[class*="aurora"]')).toBeNull();
    expect(container.querySelector('[class*="grid"]')).toBeNull();
    /*
     * Stated as the WHOLE decorative stack rather than as a count, so the
     * assertion names what may be there: the gradient (the backdrop, fully
     * covered once the photograph decodes) and the photograph. Any third layer
     * is by definition something painted over the customer's picture.
     */
    const panel = container.querySelector("[data-brand-panel]");
    // Direct children only: the brand mark's own glyph is aria-hidden too, but
    // it lives INSIDE the content and is the brand, not a layer over it.
    const layers = [...(panel?.children ?? [])].filter(
      (el) => el.getAttribute("aria-hidden") === "true",
    );
    expect(layers.map((el) => el.tagName)).toEqual(["DIV", "IMG"]);
    expect(layers[0]?.className).toContain("gradient");
  });

  it("buys legibility with a text-shadow instead, on the content", () => {
    /*
     * The honest trade, stated where it can be read: a scrim makes contrast a
     * FIXED value regardless of the image and a shadow does not. It is right
     * here because the operator chose the photograph, can see the result, and
     * can change it — what they could not do was turn off an effect the
     * product applied without asking.
     *
     * On the CONTENT rather than on each text node, so it covers the mark's
     * name and the tagline together and inherits to anything added later.
     */
    const shadow = ruleBody(css(), '.panel[data-has-image="true"] .content');
    expect(shadow).toContain("text-shadow");
    // Two layers: a tight offset that separates a glyph from what is directly
    // under it, and a wider halo that lifts it off a busy region.
    expect(shadow).toContain("0 1px 2px rgba(0, 0, 0, 0.6)");
    expect(shadow).toContain("0 0 12px rgba(0, 0, 0, 0.35)");
  });

  it("KEEPS the scrim when there is no photograph to protect", () => {
    // Nothing of the customer's to preserve there, the two stops are the
    // brand's own colours, and the scrim is what keeps the mark legible
    // against a light gradient.
    const { container } = render(<BrandPanel branding={branding()} />);
    expect(container.querySelector('[class*="scrim"]')).not.toBeNull();
    expect(ruleBody(css(), ".scrim")).toContain("linear-gradient(");
  });

  it("drops the light PLATE behind the logo when there is an image", () => {
    /*
     * The plate is a light rectangle the product puts behind a dark logo so it
     * stays legible — right wherever WE chose the ground, wrong when the
     * operator did. They picked this photograph AND this logo and can see
     * whether the pair works.
     */
    const withImage = render(
      <BrandPanel branding={branding({ splashUrl: SPLASH, logoUrl: LOGO })} />,
    );
    expect(withImage.container.querySelector('[class*="mark"]')?.className ?? "").not.toContain(
      "plated",
    );
    withImage.unmount();

    // And keeps it on the gradient, where the plate is the product's own fix
    // for a background the product chose.
    const gradientOnly = render(<BrandPanel branding={branding({ logoUrl: LOGO })} />);
    expect(
      gradientOnly.container.querySelector('[class*="mark"]')?.className ?? "",
    ).toContain("plated");
  });

  it("never composites the photograph over anything at less than full opacity", () => {
    /*
     * The whole chain, not just `.image`: an opacity or a blend ANYWHERE
     * between the photograph and the viewer would let the gradient behind it
     * show through and tint it, which is the bug in its original form. The
     * image is `object-fit: cover` and fully opaque, so the gradient it sits on
     * contributes nothing once the bytes arrive.
     */
    const sheet = css();
    for (const [, body] of sheet.matchAll(/\.image\s*\{([^}]*)\}/g)) {
      expect(body).not.toContain("opacity");
      expect(body).not.toContain("mix-blend-mode");
      expect(body).not.toContain("filter");
    }
    // The panel and the content must not dim the layers under them either.
    for (const selector of [".panel", ".content"]) {
      expect(ruleBody(sheet, selector)).not.toContain("opacity:");
    }
    // `cover` is what guarantees the gradient is fully covered rather than
    // letterboxed with the backdrop showing at the edges.
    expect(ruleBody(sheet, ".image")).toContain("object-fit: cover");
  });
});

describe("the name and the tagline share the panel without colliding", () => {
  it("stacks them as flex siblings with a real gap", () => {
    /*
     * The guarantee is structural rather than a measurement: the mark and the
     * tagline are siblings in a flex COLUMN with a gap, so the tagline sits
     * below the mark however many lines the brand name wraps to. Overlap would
     * need one of them out of normal flow, which is what this pins against.
     */
    const content = ruleBody(css(), ".content");
    expect(content).toContain("flex-direction: column");
    expect(content).toContain("gap: var(--space-4)");
  });

  it("gives the mark the content column to wrap INSIDE", () => {
    /*
     * BrandMark is an inline-flex, so on its own it is as wide as its contents
     * want to be — and a name with nothing to wrap against does not wrap, it
     * runs off the panel. The content column is the bound, and `min-width: 0` is
     * what stops the flex item from refusing to shrink to it.
     */
    const sheet = css();
    expect(ruleBody(sheet, ".content")).toContain("max-width: min(72%, 760px)");
    expect(ruleBody(sheet, ".content")).toContain("min-width: 0");
    expect(ruleBody(sheet, ".content > :first-child")).toContain("max-width: 100%");
  });

  it("renders both, in that order, with the tagline as its own paragraph", () => {
    const { container } = render(<BrandPanel branding={branding()} />);
    const content = container.querySelector('[class*="content"]');
    const children = [...(content?.children ?? [])];
    // The mark first, the tagline second — and the tagline is a <p>, not text
    // inside the mark, which is what keeps the gap between them real.
    expect(children).toHaveLength(2);
    expect(children[1]?.tagName).toBe("P");
    expect(children[1]).toHaveTextContent("Correo de Acme");
  });

  it("drops the tagline where there is no room for it, rather than squeezing it", () => {
    // On the collapsed band and on a short viewport the form must keep the
    // rest of the screen, so the tagline goes rather than the mark shrinking.
    const sheet = css();
    const collapsed = sheet.slice(sheet.indexOf("@media (max-width: 900px)"));
    expect(ruleBody(collapsed, ".tagline")).toContain("display: none");
  });
});

describe("a real brand name, at the width the panel actually has", () => {
  /**
   * The live measurement that forced the stacked lockup: the content column is
   * 380px, and a square mark beside its name left the name a 176px box. The
   * owner's own brand — 26 characters — needs three-plus lines in 176px at a
   * heading size, so it rendered "NU / Desarroll…".
   *
   * jsdom measures nothing (every box is 0x0), so none of this can be asserted
   * by rendering and reading a width. What CAN be asserted is that the name
   * element is present, unabbreviated in the DOM, and governed by the rules
   * that give it the full column — which is what these tests do.
   */
  const NAME = "NU Desarrollos Conscientes";
  const SQUARE_LOGO = "/branding/assets/nu/logo.png";

  /** Renders the panel and reports the square logo as loaded. */
  function renderWithSquareLogo(): HTMLElement {
    const { container } = render(
      <BrandPanel branding={branding({ name: NAME, logoUrl: SQUARE_LOGO })} />,
    );
    const image = container.querySelector("img");
    expect(image).not.toBeNull();
    if (image !== null) {
      Object.defineProperty(image, "naturalWidth", { value: 512, configurable: true });
      Object.defineProperty(image, "naturalHeight", { value: 512, configurable: true });
      fireEvent.load(image);
    }
    return container;
  }

  it("puts the whole name in the DOM, unabbreviated", () => {
    renderWithSquareLogo();
    /*
     * The DOM carries the full string in every version of this bug — the
     * abbreviation was always visual (an ellipsis, then a line clamp). So this
     * is the floor, not the proof: it fails only if the name stops being
     * rendered at all, and the CSS assertions below are what cover the rest.
     */
    expect(screen.getByText(NAME)).toBeInTheDocument();
  });

  it("renders the name INSIDE the mark, under the logo, above the tagline", () => {
    const container = renderWithSquareLogo();
    const mark = container.querySelector('[class*="mark"]');
    const name = screen.getByText(NAME);

    // Inside the mark, so the stacked lockup's own gap separates it from the
    // logo — not the content column's larger one.
    expect(mark).toContainElement(name);
    // And the logo comes first: the stack is mark-then-name, top to bottom.
    const marked = [...(mark?.children ?? [])];
    expect(marked[0]?.tagName).toBe("IMG");
    expect(marked[1]).toBe(name);

    // The tagline is still a sibling of the whole mark, below it.
    const content = container.querySelector('[class*="content"]');
    const children = [...(content?.children ?? [])];
    expect(children[0]).toBe(mark);
    expect(children[1]?.tagName).toBe("P");
  });

  it("carries the class the stacked rules are written against", () => {
    /*
     * The bridge between this file and BrandMark.module.css. The CSS tests
     * pin what `.lg.square` DOES; this pins that a square logo on the login
     * panel actually gets that class — without which those rules are correct
     * and unreachable.
     */
    const container = renderWithSquareLogo();
    const className = container.querySelector('[class*="mark"]')?.className ?? "";
    expect(className).toContain("lg");
    expect(className).toContain("square");
  });

  it("gives that name the full content column, not the remainder beside a logo", () => {
    /*
     * The rules themselves, since jsdom cannot measure. `width: 100%` inside a
     * mark that BrandPanel bounds to the content column is what makes the name's
     * box the whole 380px rather than the 176px left over beside an 80px mark.
     */
    const markCss = readFileSync(
      resolve(here, "../../components/BrandMark.module.css"),
      "utf8",
    );
    const name = ruleBody(markCss, ".lg.square .name");
    expect(name).toContain("width: 100%");
    expect(name).toContain("font-size: var(--text-2xl)");
    expect(name).toContain("overflow-wrap: anywhere");
    expect(name).toContain("text-overflow: clip");
    expect(name).not.toContain("ellipsis");
    // Two lines is the cap, and 26 characters across 380px at --text-2xl fits
    // inside it — which is the whole point of the step down from --text-3xl.
    expect(name).toContain("line-clamp: 2");

    // And the column the width is a percentage OF.
    expect(ruleBody(css(), ".content")).toContain("max-width: min(72%, 760px)");
    expect(ruleBody(css(), ".content > :first-child")).toContain("max-width: 100%");
  });
});

describe("what the panel renders", () => {
  it("marks itself as having an image, and renders the photograph", () => {
    const { container } = render(<BrandPanel branding={branding({ splashUrl: SPLASH })} />);
    const panel = container.querySelector("[data-brand-panel]");
    expect(panel).toHaveAttribute("data-has-image", "true");

    const image = container.querySelector("img");
    expect(image).toHaveAttribute("src", SPLASH);
    // Decorative: a mood image the customer chose carries nothing a user needs.
    expect(image).toHaveAttribute("alt", "");
    // It IS the largest element on the screen, so it is the LCP candidate.
    expect(image).toHaveAttribute("fetchpriority", "high");
  });

  it("draws the generated texture only when there is NO photograph", () => {
    /*
     * The aurora and the grid exist to make an UNCONFIGURED gradient look
     * composed. Over a photograph they are noise on top of somebody's picture —
     * the same fault as the wash, in cheaper form.
     */
    const withImage = render(<BrandPanel branding={branding({ splashUrl: SPLASH })} />);
    expect(withImage.container.querySelector('[class*="aurora"]')).toBeNull();
    expect(withImage.container.querySelector('[class*="grid"]')).toBeNull();
    withImage.unmount();

    const without = render(<BrandPanel branding={branding()} />);
    expect(without.container.querySelector('[class*="aurora"]')).not.toBeNull();
    expect(without.container.querySelector('[class*="grid"]')).not.toBeNull();
    expect(without.container.querySelector("img")).toBeNull();
    expect(without.container.querySelector("[data-brand-panel]")).toHaveAttribute(
      "data-has-image",
      "false",
    );
  });
});
