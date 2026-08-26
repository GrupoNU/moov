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

  // --- P2: mailboxes ---
  "mailbox.inbox": "Inbox",
  "mailbox.drafts": "Drafts",
  "mailbox.sent": "Sent",
  "mailbox.archive": "Archive",
  "mailbox.junk": "Junk",
  "mailbox.trash": "Trash",
  "mailbox.all": "All mail",
  "mailbox.flagged": "Starred",
  "mailbox.unreadCount": (count: number): string =>
    `${count} unread message${count === 1 ? "" : "s"}`,
  "mailbox.itemCount": (count: number): string =>
    `${count} item${count === 1 ? "" : "s"}`,
  "mailbox.collapse": "Collapse folder",
  "mailbox.expand": "Expand folder",
  "mailbox.loadFailed": "Could not load your folders",
  "mailbox.retry": "Try again",

  // --- P2: the message list ---
  "list.loading": "Loading messages…",
  "list.empty": "Nothing here",
  "list.emptyBody": "This folder has no messages.",
  "list.emptySearch": "No matches",
  "list.emptySearchBody": (query: string): string =>
    `Nothing matched “${query}”. Try fewer or different words.`,
  "list.label": "Message list",
  "list.selectMessage": "Select a message to read it",
  "list.selectMessageBody":
    "Choose a conversation from the list, or press j and k to move through it.",
  "list.attachment": "Has an attachment",
  "list.flagged": "Starred",
  "list.unread": "Unread",
  "list.threadSize": (count: number): string => `${count} messages in this conversation`,
  "list.noSubject": "(no subject)",
  "list.unknownSender": "(unknown sender)",
  // The honest ceiling message. The server answers at most 200 rows and has no
  // working offset, so a longer folder genuinely cannot be paged through yet.
  //
  // Two variants, because the advice has to differ: in a folder, searching IS
  // the way to reach older mail; inside a search, telling the user to search
  // is advice they have already taken, so the honest thing is to say the
  // result set is capped and suggest narrowing it.
  "list.truncated": (shown: number): string =>
    `Showing the ${shown} most recent conversations. This server cannot page further yet — use search to find older mail.`,
  "list.truncatedSearch": (shown: number): string =>
    `Showing the ${shown} most recent matches. There may be more — add words to narrow the search.`,
  "list.loadFailed": "Could not load these messages",

  // --- P2: search ---
  "search.label": "Search mail",
  "search.placeholder": "Search mail",
  "search.clear": "Clear search",
  "search.searching": "Searching…",
  "search.resultCount": (count: number): string =>
    `${count} result${count === 1 ? "" : "s"}`,
  "search.inMailbox": "In this folder",
  "search.everywhere": "All mail",
  // The graceful degradation the brief requires: never a silent empty list.
  "search.unsupported": "This server cannot answer that search",
  "search.unsupportedBody":
    "Moov's search covers text, sender, recipient and subject, and can be narrowed to one folder and a date range. Other conditions are not available yet.",

  // --- P2: the reading pane ---
  "reader.from": "From",
  "reader.to": "To",
  "reader.cc": "Cc",
  "reader.bcc": "Bcc",
  "reader.replyTo": "Reply to",
  "reader.date": "Date",
  "reader.close": "Back to the list",
  "reader.loading": "Loading the message…",
  "reader.loadFailed": "Could not load this message",
  "reader.attachments": (count: number): string =>
    `${count} attachment${count === 1 ? "" : "s"}`,
  "reader.download": "Download",
  "reader.downloadMessage": "Download the original message",
  "reader.downloading": "Preparing the download…",
  "reader.downloadFailed": "The download did not start. Try again.",
  "reader.threadContext": (count: number): string =>
    `Conversation with ${count} messages`,
  "reader.showThread": "Show the whole conversation",
  "reader.hideThread": "Hide the conversation",
  "reader.emptyBody": "This message has no text content.",
  "reader.bodyTruncated":
    "This message is long and has been shortened. Download the original to read all of it.",
  // --- the secure HTML renderer (W-A4) ---
  // The iframe's accessible name: what the region IS, for a screen-reader
  // user landing on it.
  "reader.htmlFrameTitle": "Message content",
  // Remote images: blocked by default (they leak the reader's IP and the
  // moment of opening to the sender), loadable through the privacy proxy on
  // an explicit action. The banner explains the WHY in one clause, because a
  // bare "images blocked" reads as a malfunction.
  "reader.imagesBlocked": (count: number): string =>
    count === 1
      ? "1 remote image is hidden to protect your privacy."
      : `${count} remote images are hidden to protect your privacy.`,
  "reader.showImages": "Show images",
  "reader.imagesLoading": "Loading images through the privacy proxy…",
  "reader.imagesFailed":
    "The images could not be loaded through the privacy proxy, so they stay hidden. Try again later.",
  "reader.inlineImagesUnavailable": (count: number): string =>
    count === 1
      ? "1 embedded image cannot be displayed yet."
      : `${count} embedded images cannot be displayed yet.`,
  // The honest fallback: sanitization refused the whole document. Never
  // rendered silently — the user is told a formatted version exists.
  "reader.htmlSanitizeFailed": "The formatted version cannot be shown safely",
  "reader.htmlSanitizeFailedBody":
    "This message's formatting could not be made safe to display, so Moov is not showing it. The plain-text version, when the sender included one, is shown below; the original message can be downloaded in full.",
  "reader.parseFailed": "Moov could not read this message's contents",
  "reader.parseFailedBody":
    "The message is stored safely and can be downloaded in full, but its structure could not be parsed.",

  // --- P2: keyboard ---
  "shortcuts.title": "Keyboard shortcuts",
  "shortcuts.close": "Close",
  "shortcuts.open": "Open the message",
  "shortcuts.next": "Next message",
  "shortcuts.previous": "Previous message",
  "shortcuts.back": "Back to the list",
  "shortcuts.search": "Search",
  "shortcuts.archive": "Archive",
  "shortcuts.delete": "Delete",
  "shortcuts.flag": "Star",
  "shortcuts.toggleRead": "Mark read or unread",
  "shortcuts.goInbox": "Go to Inbox",
  "shortcuts.goSent": "Go to Sent",
  "shortcuts.goDrafts": "Go to Drafts",
  "shortcuts.goArchive": "Go to Archive",
  "shortcuts.goTrash": "Go to Trash",
  "shortcuts.help": "Show this help",
  "shortcuts.sectionNavigate": "Moving around",
  "shortcuts.sectionActions": "Acting on mail",
  "shortcuts.sectionJump": "Jumping to a folder",
  // P3 wires the actions behind e and #; saying so is more honest than a
  // shortcut that silently does nothing.
  "shortcuts.comingSoon": "Arriving in the next release",
  "action.notYet": "This action arrives in the next release",
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

  "mailbox.inbox": "Bandeja de entrada",
  "mailbox.drafts": "Borradores",
  "mailbox.sent": "Enviados",
  "mailbox.archive": "Archivo",
  "mailbox.junk": "Spam",
  "mailbox.trash": "Papelera",
  "mailbox.all": "Todo el correo",
  "mailbox.flagged": "Destacados",
  "mailbox.unreadCount": (count: number): string =>
    `${count} mensaje${count === 1 ? "" : "s"} sin leer`,
  "mailbox.itemCount": (count: number): string =>
    `${count} elemento${count === 1 ? "" : "s"}`,
  "mailbox.collapse": "Contraer carpeta",
  "mailbox.expand": "Expandir carpeta",
  "mailbox.loadFailed": "No se pudieron cargar tus carpetas",
  "mailbox.retry": "Reintentar",

  "list.loading": "Cargando mensajes…",
  "list.empty": "No hay nada acá",
  "list.emptyBody": "Esta carpeta no tiene mensajes.",
  "list.emptySearch": "Sin coincidencias",
  "list.emptySearchBody": (query: string): string =>
    `Nada coincidió con «${query}». Probá con menos palabras u otras distintas.`,
  "list.label": "Lista de mensajes",
  "list.selectMessage": "Elegí un mensaje para leerlo",
  "list.selectMessageBody":
    "Elegí una conversación de la lista, o usá j y k para recorrerla.",
  "list.attachment": "Tiene un adjunto",
  "list.flagged": "Destacado",
  "list.unread": "Sin leer",
  "list.threadSize": (count: number): string => `${count} mensajes en esta conversación`,
  "list.noSubject": "(sin asunto)",
  "list.unknownSender": "(remitente desconocido)",
  "list.truncated": (shown: number): string =>
    `Se muestran las ${shown} conversaciones más recientes. Este servidor todavía no puede paginar más allá: usá la búsqueda para encontrar correo más viejo.`,
  "list.truncatedSearch": (shown: number): string =>
    `Se muestran las ${shown} coincidencias más recientes. Puede haber más: agregá palabras para acotar la búsqueda.`,
  "list.loadFailed": "No se pudieron cargar estos mensajes",

  "search.label": "Buscar correo",
  "search.placeholder": "Buscar correo",
  "search.clear": "Limpiar la búsqueda",
  "search.searching": "Buscando…",
  "search.resultCount": (count: number): string =>
    `${count} resultado${count === 1 ? "" : "s"}`,
  "search.inMailbox": "En esta carpeta",
  "search.everywhere": "Todo el correo",
  "search.unsupported": "Este servidor no puede responder esa búsqueda",
  "search.unsupportedBody":
    "La búsqueda de Moov cubre texto, remitente, destinatario y asunto, y se puede acotar a una carpeta y a un rango de fechas. Otras condiciones todavía no están disponibles.",

  "reader.from": "De",
  "reader.to": "Para",
  "reader.cc": "Cc",
  "reader.bcc": "Cco",
  "reader.replyTo": "Responder a",
  "reader.date": "Fecha",
  "reader.close": "Volver a la lista",
  "reader.loading": "Cargando el mensaje…",
  "reader.loadFailed": "No se pudo cargar este mensaje",
  "reader.attachments": (count: number): string =>
    `${count} adjunto${count === 1 ? "" : "s"}`,
  "reader.download": "Descargar",
  "reader.downloadMessage": "Descargar el mensaje original",
  "reader.downloading": "Preparando la descarga…",
  "reader.downloadFailed": "La descarga no se inició. Intentá de nuevo.",
  "reader.threadContext": (count: number): string =>
    `Conversación con ${count} mensajes`,
  "reader.showThread": "Ver toda la conversación",
  "reader.hideThread": "Ocultar la conversación",
  "reader.emptyBody": "Este mensaje no tiene contenido de texto.",
  "reader.bodyTruncated":
    "Este mensaje es largo y se acortó. Descargá el original para leerlo completo.",
  "reader.htmlFrameTitle": "Contenido del mensaje",
  "reader.imagesBlocked": (count: number): string =>
    count === 1
      ? "1 imagen remota está oculta para proteger tu privacidad."
      : `${count} imágenes remotas están ocultas para proteger tu privacidad.`,
  "reader.showImages": "Mostrar imágenes",
  "reader.imagesLoading": "Cargando imágenes a través del proxy de privacidad…",
  "reader.imagesFailed":
    "Las imágenes no se pudieron cargar a través del proxy de privacidad, así que siguen ocultas. Probá más tarde.",
  "reader.inlineImagesUnavailable": (count: number): string =>
    count === 1
      ? "1 imagen incrustada todavía no se puede mostrar."
      : `${count} imágenes incrustadas todavía no se pueden mostrar.`,
  "reader.htmlSanitizeFailed": "La versión con formato no se puede mostrar de forma segura",
  "reader.htmlSanitizeFailedBody":
    "El formato de este mensaje no se pudo hacer seguro para mostrar, así que Moov no lo muestra. La versión de texto plano, cuando el remitente incluyó una, se muestra abajo; el mensaje original se puede descargar completo.",
  "reader.parseFailed": "Moov no pudo leer el contenido de este mensaje",
  "reader.parseFailedBody":
    "El mensaje está guardado a salvo y se puede descargar completo, pero no se pudo interpretar su estructura.",

  "shortcuts.title": "Atajos de teclado",
  "shortcuts.close": "Cerrar",
  "shortcuts.open": "Abrir el mensaje",
  "shortcuts.next": "Mensaje siguiente",
  "shortcuts.previous": "Mensaje anterior",
  "shortcuts.back": "Volver a la lista",
  "shortcuts.search": "Buscar",
  "shortcuts.archive": "Archivar",
  "shortcuts.delete": "Eliminar",
  "shortcuts.flag": "Destacar",
  "shortcuts.toggleRead": "Marcar como leído o sin leer",
  "shortcuts.goInbox": "Ir a la Bandeja de entrada",
  "shortcuts.goSent": "Ir a Enviados",
  "shortcuts.goDrafts": "Ir a Borradores",
  "shortcuts.goArchive": "Ir a Archivo",
  "shortcuts.goTrash": "Ir a la Papelera",
  "shortcuts.help": "Mostrar esta ayuda",
  "shortcuts.sectionNavigate": "Moverse",
  "shortcuts.sectionActions": "Actuar sobre el correo",
  "shortcuts.sectionJump": "Saltar a una carpeta",
  "shortcuts.comingSoon": "Llega en la próxima versión",
  "action.notYet": "Esta acción llega en la próxima versión",
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
