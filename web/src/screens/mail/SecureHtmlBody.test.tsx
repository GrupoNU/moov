import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { I18nProvider } from "../../i18n/I18nProvider";
import { SecureHtmlBody } from "./SecureHtmlBody";

/**
 * The quoted-text trimming, end to end through the real pipeline (E1, canon
 * §2.1).
 *
 * `mail/html/quotedTail.test.ts` proves the heuristic and
 * `mail/html/srcdoc.test.ts` proves the document assembly. What is proved HERE
 * is the composition: that real, unsanitized email HTML goes through DOMPurify
 * and comes out with its quote behind a toggle, and — the security-relevant
 * assertion — that a HIDDEN quote is genuinely absent from the frame's
 * document rather than merely styled away.
 *
 * The iframe's `srcdoc` is read directly, because that string IS the contract:
 * it is what the browser will parse, and nothing else about the frame is
 * observable from outside it (no allow-same-origin, by design).
 */

const REPLY_WITH_QUOTE =
  "<p>Thanks Ana, that works for me. I have updated the notes and pushed.</p>" +
  '<div dir="ltr">On Mon, Aug 4, 2026 at 10:11 AM Ana wrote:</div>' +
  "<blockquote><p>Can you review the plan before Thursday?</p></blockquote>";

function renderBody(html: string) {
  render(
    <I18nProvider locale="es">
      <SecureHtmlBody
        html={html}
        blockRemoteImages={false}
        onShowRemoteImages={vi.fn()}
        signImageUrls={vi.fn().mockResolvedValue(new Map())}
      />
    </I18nProvider>,
  );
  const frame = document.querySelector("iframe");
  return { frame };
}

/** The frame's current document. */
function srcDoc(): string {
  return document.querySelector("iframe")?.getAttribute("srcdoc") ?? "";
}

describe("quoted-text trimming in the reader", () => {
  it("hides the quote and offers the toggle", async () => {
    renderBody(REPLY_WITH_QUOTE);

    // The reply is shown...
    expect(srcDoc()).toContain("Thanks Ana");
    // ...and the quote is NOT in the document at all. This is the assertion
    // that distinguishes "trimmed" from "styled invisible": a select-all
    // inside the frame cannot copy what was never emitted.
    expect(srcDoc()).not.toContain("Can you review the plan");
    expect(srcDoc()).not.toContain("Ana wrote:");

    expect(
      await screen.findByRole("button", { name: /mostrar el contenido recortado/i }),
    ).toBeInTheDocument();
  });

  it("reveals the quote when the toggle is pressed, and hides it again", async () => {
    const user = userEvent.setup();
    renderBody(REPLY_WITH_QUOTE);

    await user.click(screen.getByRole("button", { name: /mostrar el contenido recortado/i }));

    expect(srcDoc()).toContain("Can you review the plan");
    // The reply is still there — revealing the tail appends, never replaces.
    expect(srcDoc()).toContain("Thanks Ana");

    await user.click(screen.getByRole("button", { name: /ocultar el contenido recortado/i }));
    expect(srcDoc()).not.toContain("Can you review the plan");
  });

  it("keeps the sandbox and the CSP intact in both states", async () => {
    const user = userEvent.setup();
    const { frame } = renderBody(REPLY_WITH_QUOTE);

    const assertIsolated = (): void => {
      expect(frame?.getAttribute("sandbox")).not.toContain("allow-scripts");
      expect(frame?.getAttribute("sandbox")).not.toContain("allow-same-origin");
      expect(srcDoc()).toContain("default-src 'none'");
      expect(srcDoc()).not.toContain("script-src");
    };

    assertIsolated();
    await user.click(screen.getByRole("button", { name: /mostrar el contenido recortado/i }));
    // Revealing a quote must not be a way to relax the frame — the whole
    // mechanism exists because it cannot.
    assertIsolated();
  });

  it("offers no toggle for a message with no quoted tail", () => {
    renderBody("<p>A short note with nothing quoted underneath it at all.</p>");
    expect(
      screen.queryByRole("button", { name: /contenido recortado/i }),
    ).not.toBeInTheDocument();
  });

  it("still sanitizes the quoted half — the split runs AFTER DOMPurify", () => {
    /*
     * The load-bearing ordering. If the tail were ever split off the RAW html
     * and re-attached after sanitization, this script would ride into the
     * document. The split operates on sanitizer output, so it cannot.
     */
    const hostile =
      "<p>Thanks Ana, that works for me and I have pushed the branch.</p>" +
      "<div>On Mon, Ana wrote:</div>" +
      "<blockquote><p>hi</p><script>alert(1)</script><img src=x onerror=alert(2)></blockquote>";
    renderBody(hostile);

    expect(srcDoc()).not.toContain("<script");
    expect(srcDoc()).not.toContain("onerror");
  });
});
