import { readdir, readFile } from "node:fs/promises";
import { join } from "node:path";

import { describe, expect, it, beforeAll } from "vitest";

import { sanitizeEmailHtml } from "./sanitize";

/**
 * The project's own pathological-MIME corpus (testdata/mime-corpus, S4) fed
 * through the renderer.
 *
 * Those .eml files were built to break the PARSER; several carry text/html
 * parts, and a body that survives the parser still has to survive the
 * sanitizer. Running the real HTML-bearing corpus proves the renderer does
 * not panic, hang, or emit executable/fetching surface on inputs shaped by a
 * different threat model than sanitize.test.ts's crafted cases.
 *
 * The HTML is extracted crudely — everything after the first blank line of a
 * text/html section — because the point is to feed the sanitizer REALISTIC,
 * possibly-malformed bytes, not to re-implement a MIME parser. A file with no
 * recoverable HTML is skipped rather than failed.
 */

const CORPUS_ROOT = join(process.cwd(), "..", "testdata", "mime-corpus");

interface HtmlFixture {
  readonly path: string;
  readonly html: string;
}

async function walkEml(dir: string): Promise<string[]> {
  const out: string[] = [];
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) {
      out.push(...(await walkEml(full)));
    } else if (entry.name.endsWith(".eml")) {
      out.push(full);
    }
  }
  return out;
}

/** Pulls the body of the first text/html section out of a raw message. */
function extractHtml(raw: string): string | undefined {
  const idx = raw.search(/content-type:\s*text\/html/i);
  if (idx < 0) return undefined;
  const afterHeader = raw.slice(idx);
  const blank = afterHeader.search(/\r?\n\r?\n/);
  if (blank < 0) return undefined;
  let body = afterHeader.slice(blank + 2);
  // Cut at the next MIME boundary if one follows.
  const boundary = body.search(/\r?\n--[-A-Za-z0-9]+/);
  if (boundary > 0) body = body.slice(0, boundary);
  return body.trim().includes("<") ? body : undefined;
}

let fixtures: HtmlFixture[] = [];

beforeAll(async () => {
  const files = await walkEml(CORPUS_ROOT);
  const collected: HtmlFixture[] = [];
  for (const path of files) {
    const raw = await readFile(path, "utf8");
    const html = extractHtml(raw);
    if (html !== undefined) collected.push({ path, html });
  }
  fixtures = collected;
});

describe("the pathological-MIME corpus through the renderer", () => {
  it("finds HTML-bearing cases to exercise", () => {
    // A guard: if the corpus layout changes and this finds nothing, the
    // suite must fail loudly rather than silently pass on zero cases.
    expect(fixtures.length).toBeGreaterThan(0);
  });

  it("neutralizes every HTML-bearing case without throwing", () => {
    for (const { path, html } of fixtures) {
      const label = path.slice(path.indexOf("mime-corpus"));
      expect(() => sanitizeEmailHtml(html, { allowRemoteImages: false }), label)
        .not.toThrow();
      const { html: clean } = sanitizeEmailHtml(html, { allowRemoteImages: false });
      const doc = new DOMParser().parseFromString(clean, "text/html");
      expect(doc.querySelectorAll("script,style,iframe,object,svg,form").length, label)
        .toBe(0);
      for (const el of doc.querySelectorAll("*")) {
        for (const attr of el.attributes) {
          expect(attr.name.startsWith("on"), `${label}: handler ${attr.name}`).toBe(false);
        }
      }
    }
  });
});
