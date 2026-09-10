import { afterEach, describe, expect, it, vi } from "vitest";

import {
  CONTENT_SAMPLE_REGION,
  LIGHT_REGION_LUMINANCE_MIN,
  inkForLuminance,
  sampleContentLuminance,
} from "./splashInk";

/**
 * The ink rule, tested where it actually is: a pure function of one number.
 *
 * The canvas half is deliberately thin here — jsdom has no 2D context, so the
 * only honest thing to assert about `sampleContentLuminance` in this
 * environment is that it FAILS SAFELY. That is not a gap in the coverage, it is
 * the contract: every way of not being able to measure must answer undefined,
 * and the component then keeps the default ink.
 */

/**
 * Replaces the 2D context with a stub for one test.
 *
 * `vi.spyOn` rather than assigning the prototype property directly: reading a
 * method off a prototype to restore it later detaches it from its receiver,
 * which the lint rule correctly objects to — and `restoreAllMocks` below puts
 * it back even when a test fails partway.
 */
function stubContext(ctx: {
  readonly drawImage: () => void;
  readonly getImageData: (x: number, y: number, w: number, h: number) => {
    readonly data: Uint8ClampedArray;
  };
}): void {
  /*
   * The stub is typed by what the code under test USES, not by the full
   * CanvasRenderingContext2D — an accurate `Partial<>` would demand an ImageData
   * instance jsdom cannot construct, and stubbing forty unused methods to
   * satisfy a type would say nothing about behaviour.
   */
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(
    ctx as unknown as CanvasRenderingContext2D,
  );
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("inkForLuminance", () => {
  it("puts light ink on a dark region and dark ink on a light one", () => {
    // The two ends, well clear of the threshold.
    expect(inkForLuminance(0)).toBe("light");
    expect(inkForLuminance(0.1)).toBe("light");
    expect(inkForLuminance(0.9)).toBe("dark");
    expect(inkForLuminance(1)).toBe("dark");
  });

  it("switches exactly at the threshold, exclusive", () => {
    /*
     * Pinned as a boundary rather than "around 0.55", because the whole value
     * of a threshold is that it is one number and not a region — and because
     * `>` versus `>=` is the classic silent edit here.
     */
    expect(LIGHT_REGION_LUMINANCE_MIN).toBe(0.55);
    expect(inkForLuminance(LIGHT_REGION_LUMINANCE_MIN)).toBe("light");
    expect(inkForLuminance(LIGHT_REGION_LUMINANCE_MIN + 0.001)).toBe("dark");
  });

  it("biases the ambiguous band toward the DEFAULT treatment", () => {
    /*
     * 0.55 rather than 0.5, and the asymmetry is deliberate: light ink on a
     * mid-grey photograph still reads (it carries a dark halo, and a photograph
     * has texture a flat swatch does not), while dark ink on a mid-grey one is
     * the weaker of the two. So mid-grey keeps the treatment every panel had
     * before this existed, and the switch fires only on a genuinely bright
     * region.
     */
    expect(LIGHT_REGION_LUMINANCE_MIN).toBeGreaterThan(0.5);
    expect(inkForLuminance(0.5)).toBe("light");
    expect(inkForLuminance(0.54)).toBe("light");
  });

  it("is pure: same input, same answer, no environment", () => {
    for (const l of [0, 0.25, 0.55, 0.8, 1]) {
      expect(inkForLuminance(l)).toBe(inkForLuminance(l));
    }
  });
});

describe("the sampled region", () => {
  it("is the BOTTOM-LEFT, where the content block sits", () => {
    /*
     * Not the whole image, and that is the point. The panel is
     * `align-items: flex-end` with its content in the bottom-left, so a
     * photograph that is dark overall and bright exactly where the lettering
     * falls is the case an average over everything gets wrong — and it is not
     * rare: photographers put the sky at the top and the subject in the middle.
     */
    expect(CONTENT_SAMPLE_REGION.left).toBe(0);
    expect(CONTENT_SAMPLE_REGION.top).toBeGreaterThan(0.5);
    // Bounded inside the image, or the sample would read past its edge.
    expect(CONTENT_SAMPLE_REGION.top + CONTENT_SAMPLE_REGION.height).toBeLessThanOrEqual(1);
    expect(CONTENT_SAMPLE_REGION.left + CONTENT_SAMPLE_REGION.width).toBeLessThanOrEqual(1);
  });
});

describe("sampleContentLuminance fails safely", () => {
  /**
   * Every way of not being able to measure answers undefined, and the caller
   * treats them identically: keep the default ink. A login screen must never
   * fail to render because it could not measure a photograph.
   */
  it("answers undefined for an image with no intrinsic size", () => {
    const image = document.createElement("img");
    // jsdom never fetches, so naturalWidth/naturalHeight are 0 — which is also
    // exactly what a decode failure looks like in a real browser.
    expect(sampleContentLuminance(image)).toBeUndefined();
  });

  it("answers undefined rather than throwing when there is no 2D context", () => {
    /*
     * jsdom has no canvas implementation, so `getContext("2d")` returns null
     * (or throws, depending on the build). Either way this must come back
     * undefined — the same answer a hardened browser or a privacy extension
     * that blocks canvas readback produces.
     */
    const image = document.createElement("img");
    Object.defineProperty(image, "naturalWidth", { value: 800, configurable: true });
    Object.defineProperty(image, "naturalHeight", { value: 600, configurable: true });
    expect(() => sampleContentLuminance(image)).not.toThrow();
    expect(sampleContentLuminance(image)).toBeUndefined();
  });

  it("answers undefined when the canvas refuses readback", () => {
    /*
     * The tainted-canvas path, simulated. The splash asset is same-origin by
     * construction (mergeBranding accepts only root-relative paths), so this
     * should be unreachable — which is precisely why it is worth pinning that
     * it degrades rather than taking the login screen down if it ever is not.
     */
    const image = document.createElement("img");
    Object.defineProperty(image, "naturalWidth", { value: 800, configurable: true });
    Object.defineProperty(image, "naturalHeight", { value: 600, configurable: true });

    stubContext({
      drawImage: () => undefined,
      getImageData: () => {
        throw new Error("SecurityError: tainted canvas");
      },
    });

    expect(sampleContentLuminance(image)).toBeUndefined();
  });

  it("measures a stubbed context, so the arithmetic itself is exercised", () => {
    /*
     * The one case that DOES measure. With a real context stubbed to return a
     * known buffer, the mean luminance is checkable arithmetic rather than a
     * property of jsdom — white must come back at 1, black at 0, and the ink
     * rule must then agree.
     */
    const image = document.createElement("img");
    Object.defineProperty(image, "naturalWidth", { value: 800, configurable: true });
    Object.defineProperty(image, "naturalHeight", { value: 600, configurable: true });

    const withPixels = (value: number): number | undefined => {
      stubContext({
        drawImage: () => undefined,
        getImageData: (_x: number, _y: number, w: number, h: number) => ({
          data: new Uint8ClampedArray(w * h * 4).fill(value),
        }),
      });
      return sampleContentLuminance(image);
    };

    const white = withPixels(255);
    const black = withPixels(0);
    expect(white).toBeCloseTo(1, 5);
    expect(black).toBeCloseTo(0, 5);
    expect(inkForLuminance(white ?? 0)).toBe("dark");
    expect(inkForLuminance(black ?? 0)).toBe("light");
  });
});
