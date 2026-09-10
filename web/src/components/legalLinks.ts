/**
 * The fixed links of the legal footer, and the rule that builds the source
 * offer's URL.
 *
 * Separate from the component so the component file exports only a component
 * (which is what keeps fast refresh working), and so the URL rule can be
 * tested without rendering anything.
 */

/** Where Moov's source lives. */
export const MOOV_REPO_URL = "https://github.com/GrupoNU/moov";

/** The license text, in the repository. */
export const MOOV_LICENSE_URL = `${MOOV_REPO_URL}/blob/main/LICENSE`;

/**
 * The repository at the revision that produced THIS bundle.
 *
 * A link to `main` would satisfy nobody: the program a user is interacting
 * with is the one that was built, and AGPL-3.0 §13's offer is for the
 * *corresponding* source. `__MOOV_COMMIT__` is inlined by Vite at build time
 * (vite.config.ts); outside a git checkout it is "dev", and then a tree link
 * would 404 — so that case falls back to the repository root, which is a
 * better offer than a dead page.
 */
export function sourceUrlForCommit(commit: string): string {
  const trimmed = commit.trim();
  // Only a plausible git object name may be pasted into a URL path; anything
  // else (an empty string, "dev", a value someone injected into the build
  // environment) falls back to the repository root.
  if (!/^[0-9a-f]{7,40}$/i.test(trimmed)) return MOOV_REPO_URL;
  return `${MOOV_REPO_URL}/tree/${trimmed}`;
}
