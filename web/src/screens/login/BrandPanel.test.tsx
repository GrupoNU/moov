import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
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

  it("keeps the scrim, because legibility is its job and only its job", () => {
    /*
     * With the wash gone the scrim is the ONLY overlay, and it has to stay: the
     * mark and the tagline sit on whatever the customer uploaded, and their
     * contrast must be a fixed value rather than a property of a photograph.
     * It is bottom-weighted precisely so it buys that without dimming the
     * picture — the top two-thirds are untouched.
     */
    const { container } = render(<BrandPanel branding={branding({ splashUrl: SPLASH })} />);
    const scrim = container.querySelector('[class*="scrim"]');
    expect(scrim).not.toBeNull();

    const scrimRules = ruleBody(css(), ".scrim");
    expect(scrimRules).toContain("linear-gradient(");
    // Transparent at the top: a flat wash would dim the whole photograph.
    expect(scrimRules).toContain("rgba(6, 8, 18, 0)");
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
