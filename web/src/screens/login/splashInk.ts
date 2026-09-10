/**
 * Which ink the login panel's text takes over a customer's splash photograph.
 *
 * # Why this exists
 *
 * The panel used to darken the photograph so white text would read on it. The
 * owner's finding killed that: a scrim over somebody's picture IS a tint, and
 * they can see it. So the picture is now shown untouched — and the very next
 * image proved the other half of the problem, exactly as predicted: white text
 * on a LIGHT photograph is illegible, and there is no overlay left to fix it.
 *
 * The remaining lever is the text itself. Measure what is actually behind the
 * lettering and pick ink that reads on it: light ink over a dark region, dark
 * ink over a light one, each with a halo in the opposite direction.
 *
 * # Why measured rather than configured
 *
 * The same reasoning as the logo's aspect ratio. An operator would have to
 * describe a picture they can see, a wrong answer would be an illegible login
 * screen nobody could explain, and the browser already has the pixels. What the
 * operator gets instead is the switch that matters — `splashText`, for artwork
 * that already carries the brand and needs no overlay text at all.
 *
 * # The split
 *
 * {@link inkForLuminance} is pure arithmetic and is where the rule lives.
 * {@link sampleContentLuminance} is the part that touches a canvas, isolated so
 * a test can stub it: jsdom has no 2D context, and a rule worth pinning should
 * not be untestable because of where it happens to read its input.
 */

/** Which of the two ink treatments the panel is wearing. */
export type SplashInk = "light" | "dark";

/**
 * Above this mean luminance the region is light enough to need dark ink.
 *
 * 0.55 rather than 0.5: the two outcomes are not symmetric. Light ink on a
 * mid-grey photograph still reads (it carries a dark halo, and photographs have
 * texture that a flat swatch does not), while dark ink on a mid-grey one is the
 * weaker of the two. Biasing the threshold up keeps the default treatment — the
 * one every panel had before this — in the ambiguous band, and switches only
 * when the region is genuinely bright.
 */
export const LIGHT_REGION_LUMINANCE_MIN = 0.55;

/**
 * The ink for a measured mean luminance. THE RULE, and the whole of it.
 *
 * Pure: same number in, same answer out, no DOM, no canvas, no environment.
 */
export function inkForLuminance(luminance: number): SplashInk {
  return luminance > LIGHT_REGION_LUMINANCE_MIN ? "dark" : "light";
}

/**
 * Where the content block sits, as fractions of the panel, for sampling.
 *
 * The panel is `align-items: flex-end` with the content in the bottom-left, so
 * that is the region measured — NOT the whole image. A photograph that is dark
 * overall and bright exactly where the lettering falls is the case an average
 * over everything gets wrong, and it is not a rare one: photographers put the
 * sky at the top and the subject in the middle.
 *
 * The box is generous on purpose. Sampling the exact text bounds would make the
 * answer flip as a brand name wraps to a second line; a fixed, slightly larger
 * region is stable and is what the eye takes in around the words anyway.
 */
export const CONTENT_SAMPLE_REGION = {
  left: 0,
  top: 0.55,
  width: 0.75,
  height: 0.45,
} as const;

/** How many pixels wide the offscreen canvas is. */
const SAMPLE_CANVAS_EDGE = 32;

/**
 * The mean WCAG relative luminance of the region behind the content block, or
 * undefined when it cannot be measured.
 *
 * Undefined is a real answer, not a failure to report: no 2D context (jsdom, a
 * hardened browser, a privacy extension), an image that never decoded, a canvas
 * the browser refuses to read back. Every one of those means "keep the default
 * ink", and the caller treats them identically.
 *
 * # Why this cannot taint the canvas
 *
 * The splash asset is same-origin by construction — `mergeBranding` accepts only
 * root-relative paths, and the server serves it from our own origin — so
 * `getImageData` is allowed. A cross-origin image would throw a SecurityError
 * here; it is caught with everything else and answers undefined rather than
 * taking the login screen down.
 */
export function sampleContentLuminance(image: HTMLImageElement): number | undefined {
  try {
    const w = image.naturalWidth;
    const h = image.naturalHeight;
    if (!(w > 0) || !(h > 0)) return undefined;

    const canvas = document.createElement("canvas");
    canvas.width = SAMPLE_CANVAS_EDGE;
    canvas.height = SAMPLE_CANVAS_EDGE;
    const ctx = canvas.getContext("2d");
    if (ctx === null) return undefined;

    /*
     * The image is drawn `object-fit: cover`, so the panel shows a CROP of it,
     * not the whole thing. Sampling the source rectangle directly would measure
     * pixels the viewer never sees — but the panel's aspect ratio is a layout
     * fact this module has no business knowing, and getting it wrong is worse
     * than not modelling it. So the region is taken from the image's own
     * geometry, which is right whenever the crop is not severe and is never
     * catastrophically wrong: it is still the bottom-left of the same picture.
     */
    const sx = Math.floor(w * CONTENT_SAMPLE_REGION.left);
    const sy = Math.floor(h * CONTENT_SAMPLE_REGION.top);
    const sw = Math.max(1, Math.floor(w * CONTENT_SAMPLE_REGION.width));
    const sh = Math.max(1, Math.floor(h * CONTENT_SAMPLE_REGION.height));

    // Downscaling to 32x32 in one draw is what makes this cheap AND stable: the
    // browser's own resampler averages for us, so a single bright speck cannot
    // move the answer the way a sparse pixel walk would.
    ctx.drawImage(image, sx, sy, sw, sh, 0, 0, SAMPLE_CANVAS_EDGE, SAMPLE_CANVAS_EDGE);
    const { data } = ctx.getImageData(0, 0, SAMPLE_CANVAS_EDGE, SAMPLE_CANVAS_EDGE);

    let sum = 0;
    let count = 0;
    for (let i = 0; i < data.length; i += 4) {
      const r = data[i] ?? 0;
      const g = data[i + 1] ?? 0;
      const b = data[i + 2] ?? 0;
      sum += relativeLuminance(r, g, b);
      count += 1;
    }
    if (count === 0) return undefined;
    return sum / count;
  } catch {
    /*
     * Deliberately swallowed, and the reason is the contract above: every cause
     * — no context, a tainted canvas, a browser that refuses readback — means
     * the same thing to the caller, and a login screen must never fail to
     * render because it could not measure a photograph.
     */
    return undefined;
  }
}

/**
 * WCAG 2.x relative luminance for 0-255 channels.
 *
 * The same formula `internal/branding` uses to pick an icon plate, written out
 * rather than imported from the palette module: this one takes channels, that
 * one takes a hex string, and a conversion in between would be arithmetic for
 * its own sake in a loop that runs 1,024 times.
 */
function relativeLuminance(r: number, g: number, b: number): number {
  const lin = (c: number): number => {
    const s = c / 255;
    return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}
