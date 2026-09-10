/**
 * Ambient module declarations.
 *
 * # Why `vite/client` is NOT referenced here
 *
 * Vite ships its own `declare module "*.module.css"`, typed as
 * `Record<string, string>` with an index signature. Under
 * `noUncheckedIndexedAccess` — which this project turns on deliberately, to
 * make a missing translation key a compile error — an index signature makes
 * every `styles.foo` a `string | undefined`. That is technically honest and
 * practically useless: it turns every className in the app into a nullable,
 * and the noise would push someone to disable the flag that is protecting the
 * data code.
 *
 * A module can only be declared once, so the only way to get a different type
 * is not to load Vite's. That costs the `import.meta.env` typings, which this
 * app does not use (the dev proxy reads env at CONFIG time, in Node, where
 * loadEnv provides it), and nothing else.
 *
 * The declaration below is intentionally NOT an index signature: `styles.typo`
 * is a plain `string`, so a misspelled class silently produces an undefined
 * class name at runtime rather than a type error. That tradeoff is accepted
 * because a wrong class name is a visible styling bug caught immediately by
 * eye, whereas a nullable string propagates into template literals and
 * function arguments across the whole codebase.
 */
declare module "*.module.css" {
  const classes: Record<string, string>;
  export default classes;
}

declare module "*.svg" {
  const src: string;
  export default src;
}

/**
 * Raw text imports (`import css from "./x.css?raw"`).
 *
 * Used by the test that pins the CSS seed values against the TypeScript ones.
 * Declared here because dropping `vite/client` (see above) also dropped this.
 */
declare module "*?raw" {
  const content: string;
  export default content;
}

/**
 * The git commit this bundle was built from, inlined by Vite's `define`
 * (see vite.config.ts). "dev" outside a git checkout.
 *
 * Declared as a global rather than read from `import.meta.env` because this
 * file deliberately does not load `vite/client` — see the note at the top.
 */
declare const __MOOV_COMMIT__: string;
