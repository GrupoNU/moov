import { describe, expect, it } from "vitest";

import { ATTACK_CORPUS } from "./corpus";
import {
  COLLAPSED_MAX_PX,
  DEFAULT_FRAME_WIDTH_PX,
  EXPANDED_MAX_PX,
  MIN_FRAME_PX,
  estimateFrameHeight,
  frameSizing,
} from "./frameHeight";
import { sanitizeEmailHtml } from "./sanitize";

/**
 * The content-derived height estimate (C-03). It is an estimate by design —
 * the sandbox refuses every exact measure — so what is pinned here is its
 * BOUNDS: a short mail gets no empty box, a long one grows with its content,
 * a huge one is clamped and flagged for the expander, and hostile markup can
 * neither crash the scan nor buy itself an unbounded frame.
 */

const WIDTH = DEFAULT_FRAME_WIDTH_PX;

function paragraphs(count: number, chars: number): string {
  const sentence = "Lorem ipsum dolor sit amet, consectetur adipiscing elit sed do. ";
  const text = sentence.repeat(Math.ceil(chars / sentence.length)).slice(0, chars);
  return Array.from({ length: count }, () => `<p>${text}</p>`).join("");
}

describe("the frame height estimate", () => {
  it("gives a one-line message a frame, not a box — well under the old 320px floor", () => {
    const height = estimateFrameHeight("<p>Hola Diego</p>", WIDTH);
    expect(height).toBeGreaterThanOrEqual(MIN_FRAME_PX);
    expect(height).toBeLessThan(120);
  });

  it("grows with the content — a long message estimates to roughly its lines", () => {
    // 40 paragraphs of ~300 characters at ~87 chars/line ≈ 4 lines each,
    // 23px a line, plus margins: on the order of 4000px, never a few hundred.
    const height = estimateFrameHeight(paragraphs(40, 300), WIDTH);
    expect(height).toBeGreaterThan(3000);
    expect(height).toBeLessThan(7000);
  });

  it("is monotonic: more content never means a shorter frame", () => {
    const short = estimateFrameHeight(paragraphs(5, 200), WIDTH);
    const longer = estimateFrameHeight(paragraphs(10, 200), WIDTH);
    expect(longer).toBeGreaterThan(short);
  });

  it("reserves height for a shown quote and none for a hidden one", () => {
    const visible = "<p>Thanks, that works.</p>";
    const quoted = `<blockquote>${paragraphs(6, 250)}</blockquote>`;
    expect(estimateFrameHeight(visible + quoted, WIDTH)).toBeGreaterThan(
      estimateFrameHeight(visible, WIDTH),
    );
  });

  it("narrower frames wrap more and estimate taller", () => {
    const html = paragraphs(10, 400);
    expect(estimateFrameHeight(html, 320)).toBeGreaterThan(estimateFrameHeight(html, 900));
  });

  it("counts a declared image height, scaled down when it is wider than the frame", () => {
    const base = estimateFrameHeight("<p>x</p>", WIDTH);
    const small = estimateFrameHeight('<p>x</p><img src="data:image/png;base64,AA" width="200" height="100">', WIDTH);
    const wide = estimateFrameHeight('<p>x</p><img src="data:image/png;base64,AA" width="1280" height="1000">', WIDTH);
    expect(small - base).toBeGreaterThanOrEqual(100);
    expect(small - base).toBeLessThan(160);
    // 1280 wide in a 640 frame renders at half: ~500px, not 1000.
    expect(wide - base).toBeGreaterThan(450);
    expect(wide - base).toBeLessThan(650);
  });

  it("does not let a sender's height attribute dictate the frame", () => {
    // `height="99999"` is content, not an instruction: per-image cap, then
    // the hard ceiling.
    const html = '<img src="data:image/png;base64,AA" height="99999">'.repeat(20);
    expect(estimateFrameHeight(html, WIDTH)).toBeLessThanOrEqual(EXPANDED_MAX_PX);
    expect(estimateFrameHeight(html, WIDTH)).toBeGreaterThanOrEqual(MIN_FRAME_PX);
  });

  it("charges a blocked image only its placeholder, not an undeclared image's default", () => {
    const blocked = estimateFrameHeight('<p>x</p><img alt="">', WIDTH);
    const undeclared = estimateFrameHeight('<p>x</p><img src="data:image/png;base64,AA">', WIDTH);
    expect(undeclared).toBeGreaterThan(blocked + 100);
  });

  it("falls back to the default width for a nonsense width", () => {
    const html = paragraphs(3, 200);
    expect(estimateFrameHeight(html, 0)).toBe(estimateFrameHeight(html, WIDTH));
    expect(estimateFrameHeight(html, Number.NaN)).toBe(estimateFrameHeight(html, WIDTH));
  });

  it("stays finite and inside the clamp for every case of the attack corpus", () => {
    // The scan runs on the SANITIZED output, as it does in the component;
    // hostile markup must be unable to crash it or escape the bounds.
    for (const attack of ATTACK_CORPUS) {
      const { html } = sanitizeEmailHtml(attack.html, { allowRemoteImages: false });
      const height = estimateFrameHeight(html, WIDTH);
      expect(Number.isFinite(height), attack.id).toBe(true);
      expect(height, attack.id).toBeGreaterThanOrEqual(MIN_FRAME_PX);
      expect(height, attack.id).toBeLessThanOrEqual(EXPANDED_MAX_PX);
    }
  });
});

describe("the cap", () => {
  it("passes a modest estimate through untouched", () => {
    expect(frameSizing(500, false)).toEqual({ heightPx: 500, isClipped: false });
  });

  it("clips a huge estimate at the collapsed maximum and flags it", () => {
    const sizing = frameSizing(9000, false);
    expect(sizing.heightPx).toBe(COLLAPSED_MAX_PX);
    expect(sizing.isClipped).toBe(true);
  });

  it("lets the expander raise the cap, but never past the hard ceiling", () => {
    expect(frameSizing(9000, true).heightPx).toBe(9000);
    expect(frameSizing(50_000, true).heightPx).toBe(EXPANDED_MAX_PX);
    // Still flagged: the control flips to "show less" rather than vanishing.
    expect(frameSizing(9000, true).isClipped).toBe(true);
  });

  it("never goes below the floor", () => {
    expect(frameSizing(1, false).heightPx).toBe(MIN_FRAME_PX);
  });

  it("keeps a huge message clipped even when the sender is generous with pixels", () => {
    // The end-to-end shape: a 2000-paragraph mail estimates past the
    // collapsed cap, so the pane shows the cap and offers the whole thing.
    const estimate = estimateFrameHeight(paragraphs(2000, 120), WIDTH);
    expect(estimate).toBeGreaterThan(COLLAPSED_MAX_PX);
    expect(frameSizing(estimate, false).heightPx).toBe(COLLAPSED_MAX_PX);
  });
});
