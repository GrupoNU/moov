import { describe, expect, it, vi } from "vitest";

import {
  applyBranding,
  applyBrandingToDocument,
  fetchBranding,
  mergeBranding,
  MOOV_DEFAULT_BRANDING,
  type Branding,
} from "./branding";

/**
 * Tests for the branding client.
 *
 * The theme throughout: a login screen must ALWAYS render. Every test below
 * that feeds garbage to the merge is asserting that garbage degrades to Moov's
 * brand rather than to a broken page.
 */

/** A complete, valid server document. */
const validDocument = {
  name: "Acme Mail",
  logoUrl: "/branding/assets/mail.acme.test/logo.png",
  splashUrl: "/branding/assets/mail.acme.test/splash.jpg",
  colors: {
    primary: "#C0FFEE",
    onPrimary: "#000000",
    splashFrom: "#102030",
    splashTo: "#405060",
  },
  tagline: "Correo de Acme",
  supportUrl: "mailto:it@acme.test",
  default: false,
};

/** Builds a Response-like object for the fetch stub. */
function jsonResponse(body: unknown, init: { ok?: boolean; status?: number } = {}): Response {
  return {
    ok: init.ok ?? true,
    status: init.status ?? 200,
    json: () => Promise.resolve(body),
    headers: new Headers({ "Content-Type": "application/json" }),
  } as unknown as Response;
}

describe("mergeBranding", () => {
  it("accepts a complete valid document", () => {
    const brand = mergeBranding(validDocument);
    expect(brand.name).toBe("Acme Mail");
    expect(brand.logoUrl).toBe("/branding/assets/mail.acme.test/logo.png");
    expect(brand.tagline).toBe("Correo de Acme");
    expect(brand.supportUrl).toBe("mailto:it@acme.test");
    expect(brand.isDefault).toBe(false);
    // Colours are normalised to lowercase, matching the server.
    expect(brand.colors.primary).toBe("#c0ffee");
  });

  it("falls back to the Moov defaults for a non-object", () => {
    for (const input of [null, undefined, "a string", 42, [], true]) {
      expect(mergeBranding(input)).toEqual(MOOV_DEFAULT_BRANDING);
    }
  });

  it("keeps the default for each field the document omits", () => {
    const brand = mergeBranding({ name: "Only A Name", default: false });
    expect(brand.name).toBe("Only A Name");
    // A partially configured brand must not end up with empty colours, which
    // would render an unstyled page.
    expect(brand.colors).toEqual(MOOV_DEFAULT_BRANDING.colors);
    expect(brand.logoUrl).toBe("");
  });

  it("rejects a colour that is not a hex literal, field by field", () => {
    const brand = mergeBranding({
      ...validDocument,
      colors: {
        primary: "rebeccapurple",
        onPrimary: "#fff",
        splashFrom: "url(https://evil.test/x)",
        splashTo: "#123456",
      },
    });
    // The invalid ones fall back...
    expect(brand.colors.primary).toBe(MOOV_DEFAULT_BRANDING.colors.primary);
    expect(brand.colors.splashFrom).toBe(MOOV_DEFAULT_BRANDING.colors.splashFrom);
    // ...and the valid ones survive alongside them.
    expect(brand.colors.onPrimary).toBe("#fff");
    expect(brand.colors.splashTo).toBe("#123456");
  });

  it("refuses an off-origin or protocol-relative asset URL", () => {
    // A customer-supplied absolute URL on the login page would be a tracking
    // pixel and a mixed-content risk. The server never sends one; the client
    // refuses one anyway.
    for (const hostile of [
      "https://evil.test/logo.png",
      "//evil.test/logo.png",
      "http://evil.test/logo.png",
      "javascript:alert(1)",
      "data:image/svg+xml,<svg onload=alert(1)>",
      "\\\\evil.test\\logo.png",
      "logo.png",
    ]) {
      const brand = mergeBranding({ ...validDocument, logoUrl: hostile });
      expect(brand.logoUrl, `logoUrl ${hostile} must be refused`).toBe("");
    }
  });

  it("refuses a support URL that could execute script", () => {
    for (const hostile of [
      "javascript:alert(document.cookie)",
      "data:text/html,<script>alert(1)</script>",
      "vbscript:msgbox(1)",
      "file:///etc/passwd",
    ]) {
      const brand = mergeBranding({ ...validDocument, supportUrl: hostile });
      expect(brand.supportUrl, `supportUrl ${hostile} must be refused`).toBe("");
    }
    for (const safe of ["https://s.test", "http://s.test", "mailto:a@s.test"]) {
      expect(mergeBranding({ ...validDocument, supportUrl: safe }).supportUrl).toBe(safe);
    }
  });

  it("treats a missing default flag as a configured brand", () => {
    // Only an explicit `true` means "this is Moov's own brand"; anything else
    // is read as a customer brand, which is the conservative direction (it
    // only affects whether Moov's wordmark may be shown).
    expect(mergeBranding({ name: "X" }).isDefault).toBe(false);
    expect(mergeBranding({ name: "X", default: true }).isDefault).toBe(true);
    expect(mergeBranding({ name: "X", default: "yes" }).isDefault).toBe(false);
  });
});

describe("applyBranding", () => {
  it("writes exactly the four seed properties and nothing else", () => {
    const root = document.createElement("div");
    applyBranding(mergeBranding(validDocument), root);

    expect(root.style.getPropertyValue("--brand-primary")).toBe("#c0ffee");
    expect(root.style.getPropertyValue("--brand-on-primary")).toBe("#000000");
    expect(root.style.getPropertyValue("--brand-splash-from")).toBe("#102030");
    expect(root.style.getPropertyValue("--brand-splash-to")).toBe("#405060");

    // The contract of W-A2: JavaScript writes SEEDS, CSS derives the rest.
    // A semantic token written from here would mean the palette had leaked
    // back into script.
    expect(root.style.length).toBe(4);
    expect(root.style.getPropertyValue("--color-accent")).toBe("");
    expect(root.style.getPropertyValue("--surface-canvas")).toBe("");
  });
});

describe("applyBrandingToDocument", () => {
  it("sets the title and the theme-color meta", () => {
    const doc = document.implementation.createHTMLDocument("initial");
    applyBrandingToDocument(mergeBranding(validDocument), doc);

    expect(doc.title).toBe("Acme Mail");
    const meta = doc.querySelector('meta[name="theme-color"]');
    expect(meta?.getAttribute("content")).toBe("#c0ffee");
  });

  it("reuses an existing theme-color meta rather than adding a second", () => {
    const doc = document.implementation.createHTMLDocument("initial");
    const existing = doc.createElement("meta");
    existing.name = "theme-color";
    existing.content = "#000000";
    doc.head.appendChild(existing);

    applyBrandingToDocument(mergeBranding(validDocument), doc);
    expect(doc.querySelectorAll('meta[name="theme-color"]')).toHaveLength(1);
    expect(existing.content).toBe("#c0ffee");
  });
});

describe("fetchBranding", () => {
  it("returns the merged document on success", async () => {
    const fetchImpl = vi.fn().mockResolvedValue(jsonResponse(validDocument));
    const brand = await fetchBranding({ fetchImpl: fetchImpl as unknown as typeof fetch });

    expect(brand.name).toBe("Acme Mail");
    expect(fetchImpl).toHaveBeenCalledWith(
      "/branding",
      expect.objectContaining({ credentials: "omit" }),
    );
  });

  it("never rejects — every failure resolves to the defaults", async () => {
    const failures: (() => Promise<Response>)[] = [
      () => Promise.reject(new TypeError("Failed to fetch")),
      () => Promise.resolve(jsonResponse({}, { ok: false, status: 500 })),
      () => Promise.resolve(jsonResponse({}, { ok: false, status: 404 })),
      () =>
        Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.reject(new SyntaxError("not json")),
          headers: new Headers(),
        } as unknown as Response),
    ];

    for (const failure of failures) {
      const brand = await fetchBranding({
        fetchImpl: failure as unknown as typeof fetch,
      });
      expect(brand).toEqual(MOOV_DEFAULT_BRANDING);
    }
  });

  it("gives up after the timeout rather than leaving the screen blank", async () => {
    vi.useFakeTimers();
    try {
      // A fetch that never settles until aborted.
      const fetchImpl = vi.fn(
        (_url: string, init?: RequestInit) =>
          new Promise<Response>((_resolve, reject) => {
            init?.signal?.addEventListener("abort", () => {
              reject(new DOMException("Aborted", "AbortError"));
            });
          }),
      );

      const pending = fetchBranding({
        fetchImpl: fetchImpl as unknown as typeof fetch,
        timeoutMs: 100,
      });
      await vi.advanceTimersByTimeAsync(150);

      expect(await pending).toEqual(MOOV_DEFAULT_BRANDING);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("the default brand", () => {
  /**
   * The Moov palette is duplicated in three places by design — Go's
   * DefaultBranding(), the seed block of tokens.css, and MOOV_DEFAULT_BRANDING
   * — because each serves a consumer the others cannot reach. This test is the
   * pin that keeps the CSS copy and the TypeScript copy identical; a Go test
   * (TestBrandingDefaultColorsAreValid) pins its own.
   */
  it("matches the seed values in tokens.css", async () => {
    // Read from disk rather than through a `?raw` import: the test config
    // enables CSS-module processing, under which Vite resolves `?raw` on a
    // stylesheet to an empty string. `process.cwd()` is the web/ project root
    // when Vitest runs, which is a stable base that does not depend on
    // `import.meta.url` (an http URL under Vite, not a file one).
    const { readFile } = await import("node:fs/promises");
    const { join } = await import("node:path");
    const css = await readFile(join(process.cwd(), "src/styles/tokens.css"), "utf8");

    const seed = (name: string): string | undefined =>
      new RegExp(`--brand-${name}:\\s*(#[0-9a-fA-F]{3,6});`).exec(css)?.[1]?.toLowerCase();

    expect(seed("primary")).toBe(MOOV_DEFAULT_BRANDING.colors.primary);
    expect(seed("on-primary")).toBe(MOOV_DEFAULT_BRANDING.colors.onPrimary);
    expect(seed("splash-from")).toBe(MOOV_DEFAULT_BRANDING.colors.splashFrom);
    expect(seed("splash-to")).toBe(MOOV_DEFAULT_BRANDING.colors.splashTo);
  });

  it("is a real brand rather than a placeholder", () => {
    // Most installations never configure a brand, so these values ARE the
    // product for most users.
    expect(MOOV_DEFAULT_BRANDING.name).toBe("Moov Mail");
    expect(MOOV_DEFAULT_BRANDING.isDefault).toBe(true);
    for (const value of Object.values(MOOV_DEFAULT_BRANDING.colors)) {
      expect(value).toMatch(/^#(?:[0-9a-f]{3}|[0-9a-f]{6})$/);
    }
  });

  /** WCAG AA, computed rather than asserted by eye. */
  it("meets WCAG AA contrast on the primary pairing", () => {
    const luminance = (hex: string): number => {
      const full =
        hex.length === 4
          ? `#${hex[1] ?? ""}${hex[1] ?? ""}${hex[2] ?? ""}${hex[2] ?? ""}${hex[3] ?? ""}${hex[3] ?? ""}`
          : hex;
      const channel = (offset: number): number => {
        const value = Number.parseInt(full.slice(offset, offset + 2), 16) / 255;
        return value <= 0.04045 ? value / 12.92 : Math.pow((value + 0.055) / 1.055, 2.4);
      };
      return 0.2126 * channel(1) + 0.7152 * channel(3) + 0.0722 * channel(5);
    };
    const contrast = (a: string, b: string): number => {
      const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
      return ((hi ?? 0) + 0.05) / ((lo ?? 0) + 0.05);
    };

    const { primary, onPrimary, splashFrom, splashTo } = MOOV_DEFAULT_BRANDING.colors;

    // The submit button: its label on its background.
    expect(contrast(onPrimary, primary)).toBeGreaterThanOrEqual(4.5);
    // The accent used as text/links on the light surface.
    expect(contrast(primary, "#ffffff")).toBeGreaterThanOrEqual(4.5);
    // White overlay text on the brand panel, at large sizes (AA is 3:1).
    expect(contrast("#ffffff", splashFrom)).toBeGreaterThanOrEqual(3);
    expect(contrast("#ffffff", splashTo)).toBeGreaterThanOrEqual(3);
  });
});

describe("the Branding type", () => {
  it("is satisfied by the defaults", () => {
    // A compile-time assertion made visible at runtime: if the interface and
    // the constant drift, this file stops compiling.
    const brand: Branding = MOOV_DEFAULT_BRANDING;
    expect(brand).toBeDefined();
  });
});
