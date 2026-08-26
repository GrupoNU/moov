import js from "@eslint/js";
import globals from "globals";
import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";
import jsxA11y from "eslint-plugin-jsx-a11y";
import tseslint from "typescript-eslint";

/**
 * ESLint for the Moov PWA.
 *
 * Two rule sets carry real weight here rather than being defaults:
 *
 *   - jsx-a11y, at ERROR. Accessibility is an acceptance criterion of this
 *     epic (L2-pwa §4, P1: "accesible (teclado, lectores de pantalla,
 *     contraste AA)"), and a criterion that is only checked by hand is a
 *     criterion that regresses. Contrast is not something a linter can see —
 *     that is pinned by a unit test — but labels, roles and keyboard handlers
 *     are, and those are the ones that rot silently.
 *
 *   - typescript-eslint's type-checked rules. The project's whole approach to
 *     the error taxonomy is "make the wrong thing not compile"; a lint that
 *     cannot see types would miss the floating promises and unsafe `any`
 *     flows that undermine it.
 */
export default tseslint.config(
  {
    // Build output and dependencies are never linted.
    ignores: ["dist", "node_modules", "coverage"],
  },
  js.configs.recommended,
  // The type-checked rule sets are scoped to TypeScript sources. Applied
  // globally they would also target this config file and any plain JS, which
  // are not in a tsconfig project — and a type-aware rule with no type
  // information fails to load rather than skipping.
  ...tseslint.configs.strictTypeChecked.map((config) => ({
    ...config,
    files: ["**/*.{ts,tsx}"],
  })),
  ...tseslint.configs.stylisticTypeChecked.map((config) => ({
    ...config,
    files: ["**/*.{ts,tsx}"],
  })),
  {
    files: ["**/*.{ts,tsx}"],
    languageOptions: {
      ecmaVersion: 2022,
      globals: globals.browser,
      parserOptions: {
        projectService: true,
        tsconfigRootDir: import.meta.dirname,
      },
    },
    plugins: {
      "react-hooks": reactHooks,
      "react-refresh": reactRefresh,
      "jsx-a11y": jsxA11y,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      ...jsxA11y.flatConfigs.recommended.rules,

      // Fast Refresh prefers a file to export only components. The three
      // provider files here also export their consumer hook (useAuth,
      // useBranding, useTranslation), which is the conventional React pairing
      // — splitting a hook from the context it reads produces two files that
      // can never be understood apart. `allowExportNames` grants exactly those
      // three names rather than switching the rule off, so an accidental
      // export of something else still gets flagged.
      "react-refresh/only-export-components": [
        "warn",
        {
          allowConstantExport: true,
          allowExportNames: ["useAuth", "useBranding", "useTranslation"],
        },
      ],

      // A promise nobody awaits is how an error disappears. The auth flow
      // deliberately uses `void` at its two fire-and-forget call sites, which
      // this rule accepts as an explicit acknowledgement.
      "@typescript-eslint/no-floating-promises": "error",
      "@typescript-eslint/no-misused-promises": "error",

      // Unused variables are errors, with the conventional underscore escape
      // for deliberately ignored parameters.
      "@typescript-eslint/no-unused-vars": [
        "error",
        {
          argsIgnorePattern: "^_",
          varsIgnorePattern: "^_",
          caughtErrors: "none",
        },
      ],

      // The codebase reads server JSON, which is genuinely `unknown` until it
      // is validated; the validators narrow it explicitly. Indexing into a
      // Record<string, unknown> is how they do it, and that is not unsafe.
      "@typescript-eslint/no-unnecessary-condition": "off",

      // Interfaces and type aliases are both used deliberately here (interface
      // for object shapes, type for unions), so the stylistic preference for
      // one is not enforced.
      "@typescript-eslint/consistent-type-definitions": "off",

      // Interpolating a NUMBER into a string is allowed. The rule's default
      // forbids it to catch `${someObject}` producing "[object Object]", which
      // is a real bug — but a number has exactly one sensible string form, and
      // the alternative (`String(n)` at every call site) is noise. Booleans,
      // objects, nullables and `any` stay forbidden, which is where the rule
      // earns its keep.
      "@typescript-eslint/restrict-template-expressions": [
        "error",
        { allowNumber: true },
      ],
    },
  },
  {
    // Tests may assert on values the type system cannot see, and may use
    // non-null assertions on fixtures they just created.
    files: ["**/*.test.{ts,tsx}", "**/test/**/*.{ts,tsx}"],
    rules: {
      "@typescript-eslint/no-non-null-assertion": "off",
      "@typescript-eslint/no-unsafe-assignment": "off",
      "@typescript-eslint/no-unsafe-member-access": "off",
      "@typescript-eslint/no-unsafe-argument": "off",
      "@typescript-eslint/no-unsafe-call": "off",
      "@typescript-eslint/no-unsafe-return": "off",
    },
  },
  {
    files: ["*.config.{js,ts}", "vite.config.ts", "vitest.config.ts"],
    languageOptions: {
      globals: globals.node,
    },
  },
);
