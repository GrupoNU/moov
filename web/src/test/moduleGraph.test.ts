import { describe, expect, it } from "vitest";
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

/**
 * The dead-component guard.
 *
 * # The defect this exists to prevent
 *
 * `ThemeToggle` and `AppShell` were both built with care, styles and (for the
 * toggle) a place in the design — and `AppShell` was mounted by nothing. It
 * had passed review, passed typecheck, passed lint and passed the test suite,
 * because none of those tools ask the one question that mattered: *is anyone
 * rendering this?* An unreferenced module is not a type error and not a lint
 * error; it is simply absent from the product while looking, in the repo, like
 * a feature that exists.
 *
 * That is a CLASS of defect, not a one-off, and it gets more likely as the app
 * grows: every epic adds screens, and a screen that loses its last import
 * during a refactor fails silently and permanently.
 *
 * # What this test actually checks
 *
 * Every non-test module under `src/` must be reachable from an entry point by
 * following static `import` specifiers. The graph is walked from `main.tsx`
 * (the real entry) and from every `*.test.*` file, so a module that only tests
 * reference — the exact shape of this defect — is NOT counted as reachable.
 * That distinction is the whole point: a component with tests and no callers
 * is precisely what slipped through.
 *
 * # Why a text scan rather than a bundler
 *
 * Reading `import`/`export ... from` specifiers with a regex is crude, but it
 * is crude in the SAFE direction: it over-approximates reachability (it will
 * follow a specifier inside a comment) and therefore only ever produces false
 * NEGATIVES, never a false accusation that fails CI on a Friday. A real module
 * graph would mean adding a bundler dependency and a build step to answer a
 * question this answers in ~20 lines.
 *
 * # If this fails
 *
 * Delete the module, or import it. Do not add it to the allowlist below unless
 * it is genuinely an entry point — the allowlist is for files the app is
 * loaded THROUGH, not for files nobody got round to wiring up.
 */

const here = dirname(fileURLToPath(import.meta.url));
const srcRoot = resolve(here, "..");

/**
 * Entry points: the module the browser actually loads, plus config-referenced
 * files. Everything else must earn its place by being imported.
 */
const ENTRY_POINTS = [
  "main.tsx",
  // Loaded by name from vite.config.ts (`setupFiles`), not by an import.
  "test/setup.ts",
];

/**
 * Modules that are legitimately not part of the app graph.
 *
 * This list is deliberately SHORT and every entry states why. It is not a
 * parking space for code someone means to wire up later — that code is exactly
 * what this test is for. Two kinds qualify:
 *
 *   - ambient declarations, which have no runtime existence to import;
 *   - test FIXTURES, whose entire purpose is to be consumed by a test.
 *
 * A fixture is distinguishable from an orphan component by intent, which a
 * regex cannot read — so it is named here, once, and reviewed when it changes.
 */
const NOT_APP_CODE = [
  // Ambient `declare module` blocks; there is nothing to import.
  "vite-env.d.ts",
  // The adversarial HTML corpus: a fixture, imported by sanitize.test.ts. It
  // is data for a test, not a feature that lost its caller.
  "mail/html/corpus.ts",
  /*
   * E9b's in-memory IndexedDB. jsdom ships none, and the epic may add no npm
   * dependency, so the offline cache's tests need a shim — which is a test
   * FIXTURE by construction: shipping it to a browser that has the real API
   * would be a bug. Same category as the corpus above.
   */
  "test/fakeIndexedDB.ts",
];

/** Extensions a bare specifier may resolve to, in the order Vite tries them. */
const EXTENSIONS = [".ts", ".tsx", ".js", ".jsx", ".css"];

function listModules(dir: string, acc: string[] = []): string[] {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) {
      listModules(full, acc);
    } else if (/\.(ts|tsx|css)$/.test(entry.name)) {
      acc.push(relative(srcRoot, full).split(sep).join("/"));
    }
  }
  return acc;
}

const isTestFile = (id: string): boolean => /\.test\.(ts|tsx)$/.test(id);

/** Every relative specifier in a module's source text. */
function specifiersOf(id: string): string[] {
  let text: string;
  try {
    text = readFileSync(join(srcRoot, id), "utf8");
  } catch {
    return [];
  }
  // `import x from "./y"`, `import "./y.css"`, `export * from "./y"`, and the
  // dynamic `import("./y")` form.
  const pattern = /(?:import|export)\s*(?:[\w*{}\n\r\t,\s]+from\s*)?["']([^"']+)["']|import\s*\(\s*["']([^"']+)["']\s*\)/g;
  const found: string[] = [];
  for (const match of text.matchAll(pattern)) {
    const specifier = match[1] ?? match[2];
    // Bare specifiers are dependencies, not our modules.
    if (specifier?.startsWith(".") === true) found.push(specifier);
  }
  return found;
}

/** Resolves a relative specifier, as seen from `fromId`, to a module id. */
function resolveSpecifier(fromId: string, specifier: string, all: Set<string>): string | undefined {
  const base = join(dirname(fromId), specifier).split(sep).join("/");
  if (all.has(base)) return base;
  for (const extension of EXTENSIONS) {
    if (all.has(base + extension)) return base + extension;
  }
  for (const extension of EXTENSIONS) {
    const asIndex = `${base}/index${extension}`;
    if (all.has(asIndex)) return asIndex;
  }
  return undefined;
}

/** Modules reachable from `roots` by following static imports. */
function reachableFrom(roots: readonly string[], all: Set<string>): Set<string> {
  const seen = new Set<string>();
  const queue = [...roots];
  while (queue.length > 0) {
    const id = queue.pop();
    if (id === undefined || seen.has(id)) continue;
    seen.add(id);
    for (const specifier of specifiersOf(id)) {
      const resolved = resolveSpecifier(id, specifier, all);
      if (resolved !== undefined && !seen.has(resolved)) queue.push(resolved);
    }
  }
  return seen;
}

describe("the module graph", () => {
  const modules = listModules(srcRoot);
  const all = new Set(modules);

  it("has the entry points it claims to have", () => {
    // A typo in ENTRY_POINTS would silently make everything unreachable and
    // turn the real test below into noise.
    for (const entry of ENTRY_POINTS) expect(all.has(entry)).toBe(true);
  });

  it("contains no module that only its own tests import", () => {
    /*
     * Reachability is computed from the APP entry points only. Test files are
     * then walked separately, so a module imported exclusively by a `.test.`
     * file lands in `testOnly` rather than in `reachable` — which is exactly
     * the AppShell/ThemeToggle situation.
     */
    const reachable = reachableFrom(ENTRY_POINTS, all);

    const orphans = modules.filter((id) => {
      if (isTestFile(id)) return false;
      if (ENTRY_POINTS.includes(id)) return false;
      if (NOT_APP_CODE.includes(id)) return false;
      return !reachable.has(id);
    });

    // The message names the files, so a failure is actionable without reading
    // this test.
    expect(
      orphans,
      orphans.length === 0
        ? ""
        : `These modules are imported by nothing the app loads. Either mount them or delete them:\n  ${orphans.join("\n  ")}`,
    ).toEqual([]);
  });

  it("keeps its allowlist honest", () => {
    // An allowlist entry for a deleted file is a comment pretending to be a
    // rule, and it is how the list grows until it covers the whole app.
    for (const id of NOT_APP_CODE) expect(all.has(id)).toBe(true);
  });

  it("detects an orphan when one is introduced", () => {
    /*
     * The guard guarding the guard. A reachability test that silently stopped
     * walking — a broken regex, a resolver that returns undefined for
     * everything — would pass forever while checking nothing. This asserts the
     * machinery still has teeth by running it against a graph with a known
     * orphan in it.
     */
    const withOrphan = new Set([...all, "components/Orphan.tsx"]);
    const reachable = reachableFrom(ENTRY_POINTS, withOrphan);
    expect(reachable.has("components/Orphan.tsx")).toBe(false);

    // ...and that it does NOT cry wolf about a module that IS imported.
    expect(reachable.has("theme/theme.ts")).toBe(true);
    expect(reachable.has("screens/settings/SettingsDialog.tsx")).toBe(true);
  });
});
