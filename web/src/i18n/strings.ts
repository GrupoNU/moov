/**
 * The string table.
 *
 * # Why a table and not literals in components
 *
 * The pilot's language is Spanish and the product's language is English, and
 * both are true at once: the code, comments and identifiers are English (the
 * project convention), while Diego's pilot users read Spanish. Hardcoding
 * either one into components would force a rewrite to serve the other.
 *
 * So every user-visible string in the app comes from here, keyed by a
 * TypeScript-checked identifier. The shape is a flat record rather than a
 * nested one because a flat key ("login.submit") greps cleanly and cannot be
 * partially overridden by accident.
 *
 * # The completeness guarantee
 *
 * `Strings` is derived from the English table, so every other locale must
 * supply EXACTLY the same keys — a missing translation is a compile error, not
 * a key rendered raw on screen. This is the mechanism that makes "i18n-ready"
 * a property the type checker enforces rather than a promise.
 *
 * # Interpolation
 *
 * Values are either plain strings or functions of their parameters. A function
 * keeps word ORDER a property of the translation: "wait 30 seconds" and
 * "esperá 30 segundos" put the number in different places relative to the
 * verb, and a template with positional holes cannot express that.
 */

/** The English strings — the source of truth for the key set. */
export const en = {
  // --- application chrome ---
  "app.skipToContent": "Skip to main content",
  "app.loading": "Loading…",

  // --- login screen ---
  "login.heading": "Sign in",
  "login.subheading": "Use your full email address and its password.",
  "login.emailLabel": "Email address",
  "login.emailPlaceholder": "you@example.com",
  "login.passwordLabel": "Password",
  "login.submit": "Sign in",
  "login.submitting": "Signing in…",
  "login.showPassword": "Show password",
  "login.hidePassword": "Hide password",
  "login.passwordShown": "Password is showing",
  "login.passwordHidden": "Password is hidden",
  "login.needHelp": "Need help?",
  "login.contactAdministrator": "Contact your administrator",

  // Client-side validation. These fire before any request, so they must be
  // about the FORM, never about the credentials.
  "login.error.emailRequired": "Enter your email address.",
  "login.error.emailInvalid": "Enter a complete email address, including the domain.",
  "login.error.passwordRequired": "Enter your password.",

  // --- the error taxonomy (the pilot's lesson) ---
  //
  // Each of these corresponds to one ApiErrorKind. The rule every one of them
  // follows: say what happened, then say what to do. "An error occurred" is
  // exactly what this table exists to make impossible.
  "error.invalidCredentials.title": "That email and password did not match",
  "error.invalidCredentials.body":
    "Check the address and password and try again. Use the password for this mailbox, not the one for another service.",

  "error.notProvisioned.title": "This mailbox is not set up in Moov yet",
  "error.notProvisioned.body":
    "Your password was correct, but this mailbox has not been added to Moov. An administrator has to enable it before you can sign in.",

  "error.rateLimited.title": "Too many attempts",
  "error.rateLimited.body": "Wait a moment before trying again.",
  "error.rateLimited.bodyWithSeconds": (seconds: number): string =>
    `Wait about ${seconds} second${seconds === 1 ? "" : "s"} before trying again.`,

  "error.serverError.title": "Moov is not answering right now",
  "error.serverError.body":
    "The server is reachable but could not complete the request. This is not a problem with your account — try again shortly.",

  "error.network.title": "Could not reach the server",
  "error.network.body":
    "Check your connection. If you are on a VPN or a company network, confirm it is connected.",

  "error.unknown.title": "Something went wrong",
  "error.unknown.body": "Try again. If it keeps happening, contact your administrator.",

  // --- the authenticated shell (P1 lands here; P2 fills it) ---
  "shell.mailboxes": "Mailboxes",
  "shell.signOut": "Sign out",
  "shell.signedInAs": (email: string): string => `Signed in as ${email}`,
  "shell.comingSoon": "Your mail is on the way",
  "shell.comingSoonBody":
    "You are signed in. The message list arrives in the next release.",

  // --- theme control ---
  "theme.label": "Theme",
  "theme.light": "Light",
  "theme.dark": "Dark",
  "theme.system": "System",
} as const;

/**
 * The shape every locale must satisfy: exactly the English keys, with matching
 * value types.
 *
 * Literal string types are WIDENED to `string`, while function types are kept
 * exactly. Without the widening, `as const` on the English table would make
 * each value its own literal type — and a Spanish translation would then be a
 * type error for the crime of not being the English sentence. Function values
 * keep their precise signature, which is what makes `format("...", n)` check
 * its arguments.
 */
export type Strings = {
  [K in keyof typeof en]: (typeof en)[K] extends (...args: infer A) => string
    ? (...args: A) => string
    : string;
};

/** A translation key. */
export type StringKey = keyof Strings;

/**
 * Spanish — the pilot's language.
 *
 * Rioplatense register ("iniciá", "esperá"), matching how the product is
 * spoken about with its actual users. The typing above guarantees this object
 * is complete: removing a key here fails the build.
 */
export const es: Strings = {
  "app.skipToContent": "Saltar al contenido principal",
  "app.loading": "Cargando…",

  "login.heading": "Iniciá sesión",
  "login.subheading": "Usá tu dirección de correo completa y su contraseña.",
  "login.emailLabel": "Dirección de correo",
  "login.emailPlaceholder": "vos@ejemplo.com",
  "login.passwordLabel": "Contraseña",
  "login.submit": "Iniciar sesión",
  "login.submitting": "Iniciando sesión…",
  "login.showPassword": "Mostrar contraseña",
  "login.hidePassword": "Ocultar contraseña",
  "login.passwordShown": "La contraseña está visible",
  "login.passwordHidden": "La contraseña está oculta",
  "login.needHelp": "¿Necesitás ayuda?",
  "login.contactAdministrator": "Contactá a tu administrador",

  "login.error.emailRequired": "Ingresá tu dirección de correo.",
  "login.error.emailInvalid": "Ingresá una dirección de correo completa, con el dominio.",
  "login.error.passwordRequired": "Ingresá tu contraseña.",

  "error.invalidCredentials.title": "Ese correo y esa contraseña no coinciden",
  "error.invalidCredentials.body":
    "Revisá la dirección y la contraseña e intentá de nuevo. Usá la contraseña de este buzón, no la de otro servicio.",

  "error.notProvisioned.title": "Este buzón todavía no está habilitado en Moov",
  "error.notProvisioned.body":
    "Tu contraseña era correcta, pero este buzón no fue dado de alta en Moov. Un administrador tiene que habilitarlo antes de que puedas entrar.",

  "error.rateLimited.title": "Demasiados intentos",
  "error.rateLimited.body": "Esperá un momento antes de volver a intentar.",
  "error.rateLimited.bodyWithSeconds": (seconds: number): string =>
    `Esperá unos ${seconds} segundo${seconds === 1 ? "" : "s"} antes de volver a intentar.`,

  "error.serverError.title": "Moov no está respondiendo en este momento",
  "error.serverError.body":
    "El servidor está accesible pero no pudo completar el pedido. No es un problema de tu cuenta: intentá de nuevo en un momento.",

  "error.network.title": "No se pudo contactar al servidor",
  "error.network.body":
    "Revisá tu conexión. Si estás en una VPN o en la red de la empresa, confirmá que esté conectada.",

  "error.unknown.title": "Algo salió mal",
  "error.unknown.body":
    "Intentá de nuevo. Si sigue pasando, contactá a tu administrador.",

  "shell.mailboxes": "Carpetas",
  "shell.signOut": "Cerrar sesión",
  "shell.signedInAs": (email: string): string => `Sesión iniciada como ${email}`,
  "shell.comingSoon": "Tu correo está en camino",
  "shell.comingSoonBody":
    "Ya iniciaste sesión. La lista de mensajes llega en la próxima versión.",

  "theme.label": "Tema",
  "theme.light": "Claro",
  "theme.dark": "Oscuro",
  "theme.system": "Sistema",
};

/** The locales the app ships with. */
export const locales = { en, es } as const;

/** A supported locale tag. */
export type Locale = keyof typeof locales;

/** The pilot ships in Spanish; `en` is the fallback for everything else. */
export const DEFAULT_LOCALE: Locale = "es";

/**
 * Picks the best locale for a browser's language list.
 *
 * Matches on the PRIMARY subtag, so "es-AR", "es-419" and "es" all resolve to
 * Spanish — a user whose browser says es-AR must not fall back to English over
 * a region code.
 */
export function resolveLocale(
  languages: readonly string[],
  available: Readonly<Record<string, Strings>> = locales,
): Locale {
  for (const language of languages) {
    const primary = language.toLowerCase().split("-")[0];
    if (primary !== undefined && primary in available) {
      return primary as Locale;
    }
  }
  return DEFAULT_LOCALE;
}
