import { createContext, useContext, useMemo, type ReactNode } from "react";
import {
  DEFAULT_LOCALE,
  locales,
  resolveLocale,
  type Locale,
  type Strings,
} from "./strings";

/**
 * The translation context.
 *
 * `t` is typed so that `t("login.submit")` is checked against the key set and
 * `t("typo")` does not compile. Keys whose value is a function are called
 * through {@link useFormat} instead, which keeps `t`'s return type a plain
 * string — a `t` that could return a function would push that union into every
 * call site.
 */
interface I18nContextValue {
  readonly locale: Locale;
  readonly strings: Strings;
}

const I18nContext = createContext<I18nContextValue | undefined>(undefined);

export interface I18nProviderProps {
  readonly children: ReactNode;
  /** Overrides detection; used by tests and by a future user preference. */
  readonly locale?: Locale;
  /** Injected for tests; defaults to the browser's list. */
  readonly languages?: readonly string[];
}

export function I18nProvider({
  children,
  locale,
  languages,
}: I18nProviderProps): React.JSX.Element {
  const value = useMemo<I18nContextValue>(() => {
    const resolved =
      locale ??
      resolveLocale(
        languages ?? (typeof navigator !== "undefined" ? navigator.languages : []) ?? [],
      );
    return { locale: resolved, strings: locales[resolved] ?? locales[DEFAULT_LOCALE] };
  }, [locale, languages]);

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

function useI18nContext(): I18nContextValue {
  const context = useContext(I18nContext);
  if (context === undefined) {
    // A hard failure rather than a silent English fallback: rendering outside
    // the provider is a wiring bug, and a fallback would hide it until a user
    // saw untranslated text.
    throw new Error("useTranslation must be used inside an <I18nProvider>");
  }
  return context;
}

/** Keys whose value is a plain string. */
type PlainKey = {
  [K in keyof Strings]: Strings[K] extends string ? K : never;
}[keyof Strings];

/** Keys whose value is a formatting function. */
type FormatKey = {
  [K in keyof Strings]: Strings[K] extends (...args: never[]) => string ? K : never;
}[keyof Strings];

export interface Translation {
  /** Looks up a plain string. */
  readonly t: (key: PlainKey) => string;
  /** Calls a formatting string with its parameters. */
  readonly format: <K extends FormatKey>(
    key: K,
    ...args: Strings[K] extends (...args: infer A) => string ? A : never
  ) => string;
  readonly locale: Locale;
}

/** Access to the string table. */
export function useTranslation(): Translation {
  const { locale, strings } = useI18nContext();
  return useMemo<Translation>(
    () => ({
      locale,
      t: (key) => strings[key],
      // The double cast is confined to this one line, and it is the standard
      // consequence of indexing a mapped type with a generic key: TypeScript
      // cannot prove that `strings[K]` is the function whose parameters `K`
      // implies, even though the Strings mapping guarantees it. The call sites
      // are fully checked — `format("shell.signedInAs", 42)` is an error —
      // which is where the safety needed to be.
      format: (key, ...args) =>
        (strings[key] as unknown as (...a: unknown[]) => string)(...args),
    }),
    [locale, strings],
  );
}
