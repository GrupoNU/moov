import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { MOOV_DEFAULT_BRANDING, mergeBranding, type Branding } from "./branding";
import { BrandingProvider, useBranding } from "./BrandingProvider";
import { derivePalette } from "./palette";
import { usePalette } from "./paletteContext";

/**
 * The provider's two new duties: the palette reaches the tree, and an
 * adjusted brand is DECLARED — once, in the console, naming theme and reason.
 */

function Probe(): React.JSX.Element {
  const brand = useBranding();
  const palette = usePalette();
  return (
    <output>
      {brand.name}|{palette.light.accent}|{palette.dark.accent}
    </output>
  );
}

/** A brand whose primary cannot be text on white. */
const mint: Branding = mergeBranding({
  name: "Mint Corp",
  colors: { primary: "#c0ffee", onPrimary: "#000000" },
  default: false,
});

afterEach(() => {
  vi.restoreAllMocks();
  document.documentElement.removeAttribute("style");
});

describe("BrandingProvider", () => {
  it("exposes the derived palette to the tree", () => {
    render(
      <BrandingProvider branding={mint}>
        <Probe />
      </BrandingProvider>,
    );
    const expected = derivePalette("#c0ffee", "#000000");
    expect(screen.getByRole("status")).toHaveTextContent(
      `Mint Corp|${expected.light.accent}|${expected.dark.accent}`,
    );
  });

  it("writes the per-theme seeds — and never --color-accent — onto <html>", () => {
    render(
      <BrandingProvider branding={mint}>
        <Probe />
      </BrandingProvider>,
    );
    const style = document.documentElement.style;
    const expected = derivePalette("#c0ffee", "#000000");
    expect(style.getPropertyValue("--brand-primary")).toBe("#c0ffee");
    expect(style.getPropertyValue("--brand-accent-light")).toBe(expected.light.accent);
    expect(style.getPropertyValue("--brand-accent-dark")).toBe("#c0ffee");
    // An inline --color-accent on :root would beat both theme blocks and
    // freeze the accent in one theme; the provider must never write it.
    expect(style.getPropertyValue("--color-accent")).toBe("");
  });

  it("declares an adjusted brand ONCE in the console, naming theme and reason", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const { rerender } = render(
      <BrandingProvider branding={mint}>
        <Probe />
      </BrandingProvider>,
    );
    // A re-render with the same brand must not repeat the declaration.
    rerender(
      <BrandingProvider branding={mint}>
        <Probe />
      </BrandingProvider>,
    );

    expect(warn).toHaveBeenCalledTimes(1);
    const message = String(warn.mock.calls[0]?.[0]);
    expect(message).toContain("Mint Corp");
    expect(message).toContain("light:");
    expect(message).toContain("#c0ffee reads at");
    expect(message).toContain("WCAG AA");
  });

  it("stays quiet for Moov's own brand, whose dark lift is the product's own design", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    render(
      <BrandingProvider branding={MOOV_DEFAULT_BRANDING}>
        <Probe />
      </BrandingProvider>,
    );
    expect(warn).not.toHaveBeenCalled();
  });
});
