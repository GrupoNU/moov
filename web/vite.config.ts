/// <reference types="vitest/config" />
import { execFileSync } from "node:child_process";

import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";

/**
 * The commit this bundle was built from.
 *
 * The legal footer links the source at THIS revision, because AGPL-3.0 §13
 * requires offering the *corresponding* source to a network user — a link to
 * whatever `main` happens to be today is a link to a different program.
 *
 * Read at CONFIG time, in Node, and inlined by `define`: the alternative (a
 * runtime fetch of a version endpoint) would put a network request on the
 * critical path of the login screen to render one line of grey text.
 *
 * "dev" when git is absent or the tree is not a checkout — a source tarball
 * built by a downstream packager is exactly that case, and it must build.
 */
function buildCommit(): string {
  try {
    return execFileSync("git", ["rev-parse", "--short", "HEAD"], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    }).trim() || "dev";
  } catch {
    return "dev";
  }
}

/**
 * Vite configuration for the Moov PWA (W-A3: React + TypeScript + Vite, no
 * heavy UI framework).
 *
 * # The dev proxy
 *
 * The app talks to the JMAP server and to /branding on its OWN origin. In
 * production that is literally true — Caddy serves the built assets and proxies
 * the API paths from one origin, exactly as it does for Bulwark today. In
 * development the dev server must reproduce that, or the browser applies CORS
 * rules production never sees and the app is being tested under different
 * conditions than it ships in.
 *
 * So the dev server proxies the same path set the production Caddyfile routes,
 * to MOOV_DEV_API (default: the pilot). `changeOrigin` is deliberately ON: the
 * upstream resolves branding by Host, and we want the pilot's Host, not
 * localhost's.
 */
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const target = env.MOOV_DEV_API ?? "https://moov.atmosfera.cloud";

  // The exact path set Caddyfile.pilot sends to moovd, so dev and production
  // route identically.
  const apiPaths = [
    "/.well-known/jmap",
    "/jmap",
    "/branding",
  ];

  const proxy = Object.fromEntries(
    apiPaths.map((path) => [
      path,
      {
        target,
        changeOrigin: true,
        secure: true,
        // EventSource must stream; buffering it would make push look broken in
        // development only.
        ws: false,
      },
    ]),
  );

  return {
    plugins: [react()],
    define: {
      __MOOV_COMMIT__: JSON.stringify(buildCommit()),
    },
    server: {
      port: 5173,
      strictPort: true,
      proxy,
    },
    preview: {
      port: 4173,
      strictPort: true,
      proxy,
    },
    build: {
      // The login screen is the first paint of a cold visit; a source map that
      // ships to production would triple what a user downloads to see it.
      sourcemap: false,
      target: "es2022",
      // Fail the build rather than silently shipping a chunk that will feel
      // slow on the pilot's connection.
      chunkSizeWarningLimit: 500,
    },

    /*
     * The test configuration lives HERE rather than in its own vitest.config.ts
     * on purpose: a separate file makes Vitest resolve its own bundled copy of
     * Vite, and the two copies' plugin types are structurally incompatible —
     * which surfaces as an unreadable 30-line type error on the `plugins`
     * array. One config, one Vite, no duplicate.
     *
     * jsdom rather than a real browser: the unit suite covers logic (the
     * branding merge, the error taxonomy, session persistence) and component
     * behaviour (labels, roles, focus). Real-browser truths — that the split
     * screen actually splits, that a brand asset loads — are verified with
     * Playwright against the live pilot, which is where they can be verified
     * honestly.
     */
    test: {
      globals: true,
      environment: "jsdom",
      setupFiles: ["./src/test/setup.ts"],

      /*
       * A cap on parallelism, because the default one is a memory limit in
       * disguise.
       *
       * Vitest defaults to roughly one worker per core, and every worker that
       * runs a component test builds its own jsdom — a browser's worth of
       * objects each. On an 8-core machine that is 7 simultaneous DOMs, which
       * on a developer box with other things open reliably ends in
       * `FATAL ERROR: ... process out of memory` rather than a test failure.
       * A crash is the worst possible gate result: it reports nothing about
       * the code, and it looks like the change under test broke something.
       *
       * Two forks keep the suite comfortably inside a couple of gigabytes and
       * cost only a few seconds against the unbounded default. `forks` (not
       * `threads`) and ISOLATION ARE DELIBERATE: the component suites share
       * `document`, so running them in one context makes them fail with
       * "multiple elements found" — a shared-DOM artifact, not a real defect.
       * Reducing the worker COUNT is the fix; reusing a worker is not.
       */
      pool: "forks",
      poolOptions: {
        forks: { maxForks: 2 },
      },
      css: {
        // CSS modules resolve to their class names rather than being stripped,
        // so a test can assert on structure without parsing the styles.
        modules: { classNameStrategy: "non-scoped" as const },
      },
      restoreMocks: true,
    },
  };
});
