import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, sep } from "node:path";
import { describe, expect, it } from "vitest";

/**
 * E10's source-tree invariants — the client-side counterpart of the server's
 * `mdn_invariant_test.go`. A behavioral test cannot pin the ABSENCE of a
 * subsystem, so these walk the source the way the Go side walks the module:
 * any new file that crosses one of the lines below trips the test and forces
 * a conscious decision in review.
 *
 * # Invariant 1 — GC-6: no MDN machinery, ever
 *
 * The composer never sets `Disposition-Notification-To` (or its legacy
 * relatives) on outgoing mail, and nothing in the client ever auto-answers
 * one. The server refuses the header at the assembly layer too
 * (email_create.go), so this is defence in depth: the header name simply
 * does not occur in the client source.
 *
 * # Invariant 2 — D-4: ONE sanitize+srcdoc chokepoint
 *
 * Every render path for message HTML — reader, conversation view, print (the
 * print stylesheet re-flows the same DOM), offline bodies replayed from the
 * E9b cache — must pass through SecureHtmlBody's sanitize → buildSrcDoc
 * assembly. The way a second path appears is someone writing `srcDoc=` or
 * `dangerouslySetInnerHTML` somewhere new; this test makes that a visible,
 * deliberate act instead of a quiet one.
 */

// Vitest runs with cwd = web/ (the package root); jsdom rewrites
// import.meta.url to a non-file scheme, so the path is anchored on cwd.
const SRC_ROOT = join(process.cwd(), "src") + sep;

function walkSource(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      walkSource(full, out);
    } else if (/\.(ts|tsx)$/.test(entry)) {
      out.push(full);
    }
  }
  return out;
}

const rel = (path: string): string =>
  path.slice(SRC_ROOT.length).replaceAll("\\", "/");

describe("GC-6: no MDN machinery in the client source", () => {
  const markers = [
    "disposition-notification",
    "return-receipt-to",
    "x-confirm-reading-to",
  ];
  // Sanctioned mentions: this test, and the draftObject pin that asserts the
  // header is ABSENT from every creation object (write.test.ts) — the
  // behavioral half of the same invariant.
  const allowed = new Set(["mail/trustInvariants.test.ts", "mail/write.test.ts"]);

  it("no file names a read-receipt request header", () => {
    const violations: string[] = [];
    for (const file of walkSource(SRC_ROOT)) {
      if (allowed.has(rel(file))) continue;
      const lower = readFileSync(file, "utf8").toLowerCase();
      for (const marker of markers) {
        if (lower.includes(marker)) violations.push(`${rel(file)} mentions ${marker}`);
      }
    }
    expect(violations, "GC-6: read receipts are never requested or answered").toEqual([]);
  });

  it("the matcher is live", () => {
    // This very file contains the markers, so a walk that skipped the
    // allowlist would have found them; prove the needle exists.
    const self = readFileSync(join(SRC_ROOT, "mail", "trustInvariants.test.ts"), "utf8");
    expect(self.toLowerCase()).toContain("disposition-notification");
  });
});

describe("D-4: the sanitize chokepoint is the ONLY raw-HTML sink", () => {
  /*
   * Each allowlisted file, with its reason — the list is the review record:
   *
   *   - SecureHtmlBody.tsx: THE chokepoint (sanitize → buildSrcDoc → sandboxed
   *     iframe srcDoc). The only place message HTML becomes a document.
   *   - SecureHtmlBody.test.tsx: reads the srcDoc attribute back to assert on
   *     it.
   *   - ReadingPane.test.tsx: same read-back, for the cache-poisoning pin.
   *   - compose/BodyEditor.tsx: `dangerouslySetInnerHTML` for the composer's
   *     OWN draft html (initial value only) — content this client authored,
   *     never a message body. The reply-quote path feeds it through the
   *     quoting builder, which escapes; that property is pinned in
   *     quoting.test.ts.
   */
  const srcDocAllowed = new Set(["screens/mail/SecureHtmlBody.tsx"]);
  const innerHtmlAllowed = new Set(["screens/compose/BodyEditor.tsx"]);

  /*
   * The SINK forms, not mere mentions: many files legitimately reference
   * "srcdoc.ts" in comments, and tests READ the attribute back with
   * getAttribute — neither mounts a document. What mounts one is the JSX
   * attribute or a DOM assignment, and those shapes are what this matches.
   */
  const srcDocSink = /srcDoc\s*=\s*\{|\.srcdoc\s*=|setAttribute\(\s*["']srcdoc/;

  it("the srcDoc SINK exists only in the chokepoint", () => {
    const violations: string[] = [];
    for (const file of walkSource(SRC_ROOT)) {
      if (rel(file) === "mail/trustInvariants.test.ts") continue;
      const text = readFileSync(file, "utf8");
      if (srcDocSink.test(text) && !srcDocAllowed.has(rel(file))) {
        violations.push(rel(file));
      }
    }
    expect(violations, "a second srcDoc sink bypasses the sanitize chokepoint").toEqual([]);
    // Prove the matcher is live: the chokepoint itself must match it.
    const chokepoint = readFileSync(
      join(SRC_ROOT, "screens", "mail", "SecureHtmlBody.tsx"),
      "utf8",
    );
    expect(srcDocSink.test(chokepoint)).toBe(true);
  });

  it("dangerouslySetInnerHTML appears only in the composer's own editor", () => {
    const violations: string[] = [];
    for (const file of walkSource(SRC_ROOT)) {
      const text = readFileSync(file, "utf8");
      if (!text.includes("dangerouslySetInnerHTML")) continue;
      if (innerHtmlAllowed.has(rel(file))) continue;
      // Files that mention it in comments only (documented refusals) are
      // fine as long as the JSX attribute form is absent.
      if (!/dangerouslySetInnerHTML\s*=/.test(text)) continue;
      violations.push(rel(file));
    }
    expect(violations, "a new dangerouslySetInnerHTML sink appeared").toEqual([]);
  });

  it("the chokepoint still exists and still sanitizes", () => {
    const body = readFileSync(
      join(SRC_ROOT, "screens", "mail", "SecureHtmlBody.tsx"),
      "utf8",
    );
    expect(body).toContain("sanitizeEmailHtml");
    expect(body).toContain("buildSrcDoc");
    expect(body).toContain("MESSAGE_SANDBOX");
  });
});
