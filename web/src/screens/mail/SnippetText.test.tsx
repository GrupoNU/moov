import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { SnippetText } from "./SnippetText";

/**
 * The rendered snippet (L3 epic E3).
 *
 * `snippet.test.ts` proves the PARSE is inert; this proves the RENDER is —
 * that the segments reach the DOM as text nodes and the only element the
 * component ever creates is its own `<mark>`.
 */

describe("SnippetText", () => {
  it("renders the fallback when there is no snippet", () => {
    render(<SnippetText raw={undefined} fallback={<span>original subject</span>} />);
    expect(screen.getByText("original subject")).toBeInTheDocument();
  });

  it("renders a mark element around the matched run", () => {
    const { container } = render(
      <SnippetText raw="the <mark>arquitectura</mark> doc" fallback={null} />,
    );
    const marks = container.querySelectorAll("mark");
    expect(marks).toHaveLength(1);
    expect(marks[0]?.textContent).toBe("arquitectura");
    expect(container.textContent).toBe("the arquitectura doc");
  });

  it("SECURITY: a script tag renders as visible text, not as an element", () => {
    const { container } = render(
      <SnippetText raw="<script>alert(1)</script>" fallback={null} />,
    );
    // The tag is TEXT: no element was created…
    expect(container.querySelector("script")).toBeNull();
    // …and the characters are on screen exactly as they arrived.
    expect(container.textContent).toBe("<script>alert(1)</script>");
  });

  it("SECURITY: an img onerror renders as visible text, not as an element", () => {
    const { container } = render(
      <SnippetText raw={'<img src=x onerror="alert(1)">'} fallback={null} />,
    );
    expect(container.querySelector("img")).toBeNull();
    expect(container.textContent).toBe('<img src=x onerror="alert(1)">');
  });

  it("SECURITY: the ONLY element in the output is the component's own mark", () => {
    /*
     * The strongest form of the assertion: whatever the server sends, the
     * element set of the rendered subtree is a subset of {mark}. This holds
     * because segments are React children, and React emits children as text
     * nodes — there is no parse step for a payload to exploit.
     */
    const hostile =
      '<mark>hit</mark> <b>bold</b> <a href="javascript:alert(1)">x</a> ' +
      '<iframe src="x"></iframe><svg onload="alert(1)"></svg>';
    const { container } = render(<SnippetText raw={hostile} fallback={null} />);

    const tags = new Set(
      Array.from(container.querySelectorAll("*")).map((el) => el.tagName.toLowerCase()),
    );
    expect(tags).toEqual(new Set(["mark"]));
    expect(container.querySelector("a")).toBeNull();
    expect(container.querySelector("iframe")).toBeNull();
  });

  it("SECURITY: a mark carrying attributes creates no element", () => {
    const { container } = render(
      <SnippetText raw={'<mark onmouseover="alert(1)">x</mark>'} fallback={null} />,
    );
    /*
     * Only the exact literal token splits, so the scanner never opens a run —
     * and never looks for a close either. The whole string, closing tag
     * included, is one text node, and the component created no element at all.
     * The attacker's `onmouseover` is a character sequence on screen.
     */
    expect(container.querySelectorAll("mark")).toHaveLength(0);
    expect(container.textContent).toBe('<mark onmouseover="alert(1)">x</mark>');
  });

  it("decodes the server's entities for display", () => {
    const { container } = render(<SnippetText raw="R&amp;D team" fallback={null} />);
    expect(container.textContent).toBe("R&D team");
  });
});
