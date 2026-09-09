import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { COMPOSE_PARAM, parseComposeRequest } from "./mailto";
import { SERVICE_WORKER_URL } from "./register";

/**
 * The manifest and the worker, checked as the CONTRACTS they are.
 *
 * These are static files in `public/`, so no type checker and no bundler ever
 * looks at them: a typo in the manifest is discovered by a user whose install
 * button never appears, and a mistake in the worker's deny list is discovered
 * by a user reading stale mail. Both are exactly the kind of silent failure
 * the module-graph guard was written for, applied to files the module graph
 * cannot see.
 */

const here = dirname(fileURLToPath(import.meta.url));
const publicDir = resolve(here, "../../public");

const readPublic = (name: string): string => readFileSync(join(publicDir, name), "utf8");

describe("the web app manifest", () => {
  const raw = readPublic("manifest.webmanifest");
  const manifest = JSON.parse(raw) as Record<string, unknown>;

  it("is valid JSON with the fields an install prompt requires", () => {
    // Chromium refuses to offer installation without all of these.
    expect(manifest.name).toBe("Moov Mail");
    expect(manifest.short_name).toBe("Moov");
    expect(manifest.start_url).toBe("/");
    expect(manifest.display).toBe("standalone");
    expect(manifest.background_color).toBe("#f6f7fb");
  });

  it("uses the app's own theme colour", () => {
    /*
     * Pinned against index.html rather than asserted as a literal alone: the
     * installed window's title bar and the browser's address bar are painted
     * from these two values, and they drifting apart is a visible seam nobody
     * would think to look for.
     */
    const indexHtml = readFileSync(resolve(here, "../../index.html"), "utf8");
    expect(manifest.theme_color).toBe("#5b5bd6");
    expect(indexHtml).toContain('content="#5b5bd6"');
  });

  it("offers both an any-purpose and a maskable icon at 192 and 512", () => {
    const icons = manifest.icons as { src: string; sizes: string; purpose: string }[];
    for (const purpose of ["any", "maskable"]) {
      for (const size of ["192x192", "512x512"]) {
        const found = icons.find(
          (icon) => icon.purpose === purpose && icon.sizes === size,
        );
        expect(found, `missing ${purpose} icon at ${size}`).toBeDefined();
      }
    }
  });

  it("ships every icon file it declares", () => {
    // A manifest that names a missing icon fails installation with an error
    // only visible in devtools.
    const icons = manifest.icons as { src: string }[];
    const shortcuts = (manifest.shortcuts ?? []) as { icons?: { src: string }[] }[];
    const sources = [
      ...icons.map((icon) => icon.src),
      ...shortcuts.flatMap((shortcut) => (shortcut.icons ?? []).map((icon) => icon.src)),
    ];
    for (const src of sources) {
      expect(() => readFileSync(join(publicDir, src.replace(/^\//, ""))), src).not.toThrow();
    }
  });

  it("registers a mailto handler the app can actually parse", () => {
    /*
     * The end-to-end pin: the template the MANIFEST declares, filled in the way
     * a BROWSER fills it, must be understood by the app's own parser. Three
     * places have to agree, and nothing but this test makes them.
     */
    const handlers = manifest.protocol_handlers as { protocol: string; url: string }[];
    const mailtoHandler = handlers.find((handler) => handler.protocol === "mailto");
    expect(mailtoHandler).toBeDefined();
    expect(mailtoHandler?.url).toContain(`${COMPOSE_PARAM}=%s`);

    const navigated = (mailtoHandler?.url ?? "").replace(
      "%s",
      encodeURIComponent("mailto:ana@example.com?subject=Hola"),
    );
    expect(parseComposeRequest(navigated)).toEqual({
      to: "ana@example.com",
      subject: "Hola",
      body: undefined,
    });
  });

  it("declares only shortcuts that lead somewhere real", () => {
    // A shortcut whose URL the app ignores is a dead control on the user's
    // launcher. Every one must be a route the router resolves.
    const shortcuts = (manifest.shortcuts ?? []) as { url: string }[];
    for (const shortcut of shortcuts) {
      expect(shortcut.url.startsWith("/")).toBe(true);
      // No shortcut may rely on a compose parameter the parser would reject.
      if (shortcut.url.includes(COMPOSE_PARAM)) {
        expect(parseComposeRequest(shortcut.url)).toBeDefined();
      }
    }
  });
});

describe("the app shell's brand-resolved links", () => {
  const indexHtml = readFileSync(resolve(here, "../../index.html"), "utf8");

  it("links the manifest, the favicon and the apple-touch-icon under /branding", () => {
    /*
     * The installed app is the one surface that cannot be rebranded after the
     * fact: the name under the home-screen icon and the icon itself are frozen
     * at install time. All three links must therefore resolve through the
     * server, which answers them per Host header.
     *
     * A regression here is invisible in development (an unbranded host serves
     * Moov's own bytes either way) and only shows up as a customer installing
     * an app with somebody else's icon.
     */
    expect(indexHtml).toContain('href="/branding/manifest.webmanifest"');
    expect(indexHtml).toContain('href="/branding/icons/favicon-32.png"');
    expect(indexHtml).toContain('href="/branding/icons/apple-touch-icon.png"');
  });

  it("no longer links any icon straight out of /public", () => {
    // The complement of the test above: a leftover static link would silently
    // win for the favicon, because a browser honours the first one it parses.
    expect(indexHtml).not.toContain('href="/favicon.svg"');
    expect(indexHtml).not.toContain('href="/manifest.webmanifest"');
    expect(indexHtml).not.toContain('href="/icons/');
  });

  it("keeps the static originals on disk as the server's embedded defaults", () => {
    // moovd embeds these and serves them for an unbranded host; a Go test pins
    // byte-equality against exactly these paths. Deleting them because the
    // shell stopped linking them would break the unbranded install.
    expect(() => readPublic("manifest.webmanifest")).not.toThrow();
    expect(() => readPublic("favicon.svg")).not.toThrow();
    expect(() => readPublic("icons/apple-touch-icon.png")).not.toThrow();
  });
});

describe("the service worker", () => {
  const source = readPublic(SERVICE_WORKER_URL.replace(/^\//, ""));

  it("bypasses every dynamic route", () => {
    /*
     * THE test of this epic. A worker that cached a JMAP response would show a
     * user mail that no longer exists, and nothing else in the build would
     * catch it. Each of these prefixes must appear in the worker's deny list.
     */
    for (const prefix of [
      "/jmap",
      "/.well-known/jmap",
      "/session",
      "/raw/",
      "/upload/",
      "/eventsource",
      "/branding",
    ]) {
      expect(source, `${prefix} must be bypassed`).toContain(`"${prefix}"`);
    }
  });

  it("only ever caches GET", () => {
    expect(source).toContain('request.method !== "GET"');
  });

  it("serves navigations from the network first", () => {
    // Cache-first navigation would pin a stale shell against a moved server.
    expect(source).toContain('request.mode === "navigate"');
    expect(source).toContain("handleNavigation");
  });

  it("precaches the offline page it falls back to", () => {
    expect(source).toContain("/offline.html");
    expect(() => readPublic("offline.html")).not.toThrow();
  });

  it("cleans old caches on activate, scoped to its own prefix", () => {
    // Unscoped deletion would evict a co-hosted app's caches.
    expect(source).toContain("caches.keys()");
    expect(source).toContain("startsWith(CACHE_PREFIX)");
    expect(source).toContain("caches.delete");
  });

  it("treats only Vite's hashed output as immutable", () => {
    expect(source).toContain('"/assets/"');
  });

  it("bypasses the shell's branded manifest and icons by prefix", () => {
    /*
     * The shell links /branding/manifest.webmanifest and /branding/icons/*.
     * Those are resolved per Host by the server, so a cached copy would serve
     * one customer's icon on another customer's host. `/branding` is in the
     * deny list and matched by PREFIX, which is what covers the sub-paths —
     * this test is the pin on that reasoning, not a second copy of the list.
     */
    const bypassed = (pathname: string): boolean =>
      source.includes('"/branding"') && pathname.startsWith("/branding");
    expect(bypassed("/branding/manifest.webmanifest")).toBe(true);
    expect(bypassed("/branding/icons/favicon-32.png")).toBe(true);
    expect(source).toContain("startsWith(prefix)");
  });
});

describe("the offline page", () => {
  const html = readPublic("offline.html");

  it("is self-contained: it must render with no network at all", () => {
    /*
     * The one property that matters. This page is shown precisely when nothing
     * can be fetched, so a stylesheet link, a font URL or an <img src> would
     * be the one asset missing exactly when it is needed.
     */
    expect(html).not.toMatch(/<link[^>]+rel=["']stylesheet/i);
    expect(html).not.toMatch(/<script[^>]+\ssrc=/i);
    expect(html).not.toMatch(/<img[^>]+src=/i);
    expect(html).not.toMatch(/url\(\s*["']?https?:/i);
  });

  it("says what is wrong in both of the app's languages", () => {
    expect(html).toContain("Estás sin conexión");
    expect(html).toContain("Moov needs a connection until offline mode lands.");
  });

  it("carries the same pre-paint theme script as the app shell", () => {
    // Without it, a dark-mode user gets a white flash on the one screen that
    // appears when things are already going wrong.
    expect(html).toContain("moov.theme.v1");
  });
});
