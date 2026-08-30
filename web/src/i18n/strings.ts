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

  // --- settings ---
  "settings.title": "Settings",
  "settings.open": "Settings",
  "settings.close": "Close",
  "settings.section.appearance": "Appearance",
  "settings.theme.description": "Choose how Moov looks, or follow your system.",

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
  "shortcuts.selectRow": "Select this conversation",
  "shortcuts.compose": "Write a new message",
  "shortcuts.reply": "Reply",
  "shortcuts.replyAll": "Reply to everyone",
  "shortcuts.forward": "Forward",
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
  "shortcuts.comingSoon": "Arriving in the next release",
  "action.notYet": "This action arrives in the next release",

  // --- E2: the rest of Gmail's triage vocabulary ---
  "shortcuts.spam": "Report spam, or mark as not spam",
  "shortcuts.undo": "Undo the last action",
  "shortcuts.archiveNext": "Archive and go to the next message",
  "shortcuts.archivePrevious": "Archive and go to the previous message",
  "shortcuts.markUnreadFromHere": "Mark unread from here down",
  "shortcuts.selectAll": "Select every conversation",
  "shortcuts.selectNone": "Clear the selection",
  "shortcuts.selectRead": "Select the read ones",
  "shortcuts.selectUnread": "Select the unread ones",
  "shortcuts.selectStarred": "Select the starred ones",
  "shortcuts.selectUnstarred": "Select the ones without a star",

  // --- P3: actions on messages ---
  //
  // The wording of delete is load-bearing. Server arbitration W-A2 makes
  // `destroy` a MOVE to Trash unless the message is already there, in which
  // case it really is erased. One word for both promises would be a lie in one
  // of the two cases, so the UI asks which one it is (see deleteIsPermanent).
  "action.markRead": "Mark as read",
  "action.markUnread": "Mark as unread",
  "action.flag": "Star",
  "action.unflag": "Remove star",
  "action.archive": "Archive",
  "action.delete": "Move to Trash",
  "action.deleteForever": "Delete permanently",
  "action.move": "Move to",
  "action.moveTo": "Move to folder",
  "action.reply": "Reply",
  "action.replyAll": "Reply all",
  "action.forward": "Forward",
  "action.more": "More actions",
  "action.selectAll": "Select all",
  "action.clearSelection": "Clear the selection",
  "action.selected": (count: number): string => `${count} selected`,
  "action.selectRow": "Select this conversation",
  "action.undo": "Undo",
  "action.confirmDeleteForever": (count: number): string =>
    count === 1
      ? "Delete this message permanently? This cannot be undone."
      : `Delete these ${count} messages permanently? This cannot be undone.`,
  "action.confirm": "Delete permanently",
  "action.cancel": "Cancel",
  // Every failure names what failed AND restores the prior state â never a
  // silent revert.
  "action.failedTitle": "That action did not go through",
  "action.failedRestored": "Nothing changed on the server; the list has been put back.",
  "action.partialFailure": (done: number, failed: number): string =>
    `${done} succeeded, ${failed} failed. The failed ones have been put back.`,
  "action.doneArchived": (count: number): string =>
    count === 1 ? "Archived" : `${count} archived`,
  "action.doneDeleted": (count: number): string =>
    count === 1 ? "Moved to Trash" : `${count} moved to Trash`,
  "action.doneDeletedForever": (count: number): string =>
    count === 1 ? "Deleted permanently" : `${count} deleted permanently`,
  "action.doneMoved": (folder: string): string => `Moved to ${folder}`,

  // --- E2: spam, undo, the completed reader, and emptying the trash ---
  //
  // "Report spam" rather than "Move to Junk": the user's intent is a verdict
  // about the message, and the folder it lands in is an implementation detail
  // of that verdict. Inside Junk the same control means the opposite, so it
  // gets its own label rather than a toggled state on one word.
  "action.spam": "Report spam",
  "action.notSpam": "Not spam",
  "action.doneSpam": (count: number): string =>
    count === 1 ? "Reported as spam" : `${count} reported as spam`,
  "action.doneNotSpam": (count: number): string =>
    count === 1 ? "Moved back to the inbox" : `${count} moved back to the inbox`,
  "action.undoDone": "The action was undone",
  "action.undoFailed": "That could not be undone",
  "action.undoExpired": "There is nothing to undo",
  "action.emptyTrash": "Empty trash now",
  "action.emptyTrashConfirm": (count: number): string =>
    count === 1
      ? "Delete the 1 message in Trash permanently? This cannot be undone."
      : `Delete all ${count} messages in Trash permanently? This cannot be undone.`,
  "action.emptyTrashEmpty": "The Trash is already empty",
  "action.emptyTrashDone": (count: number): string =>
    count === 1 ? "1 message deleted permanently" : `${count} messages deleted permanently`,
  "action.emptyTrashWorking": "Emptying the Trash…",
  "action.print": "Print",
  "action.viewOriginal": "Show original",
  "action.next": "Next message",
  "action.previous": "Previous message",
  "action.unsubscribe": "Unsubscribe",

  // --- E2: the reader's new surfaces ---
  "reader.spamBanner": "This message is in Spam",
  "reader.spamBannerBody":
    "Moov shows it because you asked for it, and keeps its images and links inert. If it does not belong here, mark it as not spam.",
  "reader.spamImagesBlocked":
    "Images are never loaded for a message in Spam.",
  "reader.originalTitle": "Original message",
  "reader.originalHeaders": "Headers, exactly as they arrived",
  "reader.originalLoading": "Loading the original…",
  "reader.originalFailed": "Could not load the original message",
  "reader.copy": "Copy to clipboard",
  "reader.copied": "Copied",
  "reader.copyFailed": "Could not copy. Select the text and copy it manually.",
  "reader.unsubscribeFrom": (list: string): string => `Unsubscribe from ${list}`,
  "reader.unsubscribeOpensTab": "Opens the sender's page in a new tab",
  "reader.unsubscribeLatency":
    "It can take a few days for the sender to stop sending.",

  // --- P3: the composer ---
  "compose.new": "Write",
  "compose.title": "New message",
  "compose.titleReply": "Reply",
  "compose.titleForward": "Forward",
  "compose.titleDraft": "Draft",
  "compose.from": "From",
  "compose.to": "To",
  "compose.cc": "Cc",
  "compose.bcc": "Bcc",
  "compose.showCc": "Add Cc",
  "compose.showBcc": "Add Bcc",
  "compose.subject": "Subject",
  "compose.subjectPlaceholder": "Subject",
  "compose.body": "Message",
  "compose.send": "Send",
  "compose.sending": "Sending…",
  "compose.discard": "Discard",
  "compose.close": "Close the composer",
  "compose.attach": "Attach a file",
  "compose.attachments": (count: number): string =>
    count === 1 ? "1 attachment" : `${count} attachments`,
  "compose.removeAttachment": (name: string): string => `Remove ${name}`,
  "compose.removeRecipient": (address: string): string => `Remove ${address}`,
  "compose.recipientCount": (count: number): string =>
    count === 1 ? "1 recipient" : `${count} recipients`,
  "compose.uploading": (percent: number): string => `Uploading… ${percent}%`,
  "compose.uploadFailed": "This file could not be attached",
  "compose.plainText": "Plain text",
  "compose.richText": "Formatting",
  "compose.bold": "Bold",
  "compose.italic": "Italic",
  "compose.underline": "Underline",
  "compose.bulletList": "Bulleted list",
  "compose.orderedList": "Numbered list",
  "compose.link": "Insert a link",
  "compose.linkPrompt": "Address of the link",
  "compose.linkInvalid": "A link must be a web address (http, https) or an email address.",
  "compose.addressInvalid": (address: string): string =>
    `${address} is not a complete email address.`,
  "compose.noRecipients": "Add at least one recipient before sending.",
  "compose.attributionLine": (date: string, sender: string): string =>
    `On ${date}, ${sender} wrote:`,
  "compose.forwardedHeader": "---------- Forwarded message ----------",
  "compose.forwardedFrom": "From",
  "compose.forwardedDate": "Date",
  "compose.forwardedSubject": "Subject",
  "compose.forwardedTo": "To",

  // --- P3: drafts ---
  "draft.saving": "Saving…",
  "draft.saved": "Draft saved",
  "draft.unsaved": "Unsaved changes",
  "draft.saveFailed": "The draft could not be saved",
  "draft.discardConfirm": "Discard this draft? What you wrote will be lost.",
  "draft.discarded": "Draft discarded",
  "draft.discardFailed": "The draft could not be discarded",

  // --- P3: sending, with undo ---
  "send.undoWindow": (seconds: number): string => `Sending in ${seconds}s`,
  "send.undo": "Undo",
  "send.sent": "Message sent",
  "send.canceled": "Send canceled — the message was not transmitted",
  "send.failedTitle": "The message was not sent",
  "send.cannotUnsend": "Too late to undo — the message has already gone out.",
  "send.sizeExceeded": (limit: string): string =>
    `This file is larger than the ${limit} this server accepts.`,
  "send.attachmentsExceeded": (limit: string): string =>
    `The attachments add up to more than the ${limit} one message may carry.`,

  // --- P3: folders ---
  "folder.create": "New folder",
  "folder.name": "Folder name",
  "folder.createFailed": "The folder could not be created",
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

  "settings.title": "Configuración",
  "settings.open": "Configuración",
  "settings.close": "Cerrar",
  "settings.section.appearance": "Apariencia",
  "settings.theme.description": "Elegí cómo se ve Moov, o seguí el sistema.",

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
  "shortcuts.selectRow": "Seleccionar esta conversación",
  "shortcuts.compose": "Escribir un mensaje nuevo",
  "shortcuts.reply": "Responder",
  "shortcuts.replyAll": "Responder a todos",
  "shortcuts.forward": "Reenviar",
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

  // --- E2: el resto del vocabulario de triage de Gmail ---
  "shortcuts.spam": "Marcar como spam, o quitar el spam",
  "shortcuts.undo": "Deshacer la última acción",
  "shortcuts.archiveNext": "Archivar e ir al mensaje siguiente",
  "shortcuts.archivePrevious": "Archivar e ir al mensaje anterior",
  "shortcuts.markUnreadFromHere": "Marcar como no leídos de acá para abajo",
  "shortcuts.selectAll": "Seleccionar todas las conversaciones",
  "shortcuts.selectNone": "Limpiar la selección",
  "shortcuts.selectRead": "Seleccionar los leídos",
  "shortcuts.selectUnread": "Seleccionar los no leídos",
  "shortcuts.selectStarred": "Seleccionar los destacados",
  "shortcuts.selectUnstarred": "Seleccionar los que no están destacados",

  // --- P3: acciones sobre mensajes ---
  "action.markRead": "Marcar como leído",
  "action.markUnread": "Marcar como no leído",
  "action.flag": "Destacar",
  "action.unflag": "Quitar el destaque",
  "action.archive": "Archivar",
  "action.delete": "Mover a la Papelera",
  "action.deleteForever": "Eliminar definitivamente",
  "action.move": "Mover a",
  "action.moveTo": "Mover a una carpeta",
  "action.reply": "Responder",
  "action.replyAll": "Responder a todos",
  "action.forward": "Reenviar",
  "action.more": "Más acciones",
  "action.selectAll": "Seleccionar todo",
  "action.clearSelection": "Limpiar la selección",
  "action.selected": (count: number): string =>
    count === 1 ? "1 seleccionado" : `${count} seleccionados`,
  "action.selectRow": "Seleccionar esta conversación",
  "action.undo": "Deshacer",
  "action.confirmDeleteForever": (count: number): string =>
    count === 1
      ? "¿Eliminar este mensaje definitivamente? No se puede deshacer."
      : `¿Eliminar estos ${count} mensajes definitivamente? No se puede deshacer.`,
  "action.confirm": "Eliminar definitivamente",
  "action.cancel": "Cancelar",
  "action.failedTitle": "Esa acción no se aplicó",
  "action.failedRestored": "No cambió nada en el servidor; la lista quedó como estaba.",
  "action.partialFailure": (done: number, failed: number): string =>
    `${done} se aplicaron y ${failed} fallaron. Los que fallaron quedaron como estaban.`,
  "action.doneArchived": (count: number): string =>
    count === 1 ? "Archivado" : `${count} archivados`,
  "action.doneDeleted": (count: number): string =>
    count === 1 ? "Movido a la Papelera" : `${count} movidos a la Papelera`,
  "action.doneDeletedForever": (count: number): string =>
    count === 1 ? "Eliminado definitivamente" : `${count} eliminados definitivamente`,
  "action.doneMoved": (folder: string): string => `Movido a ${folder}`,

  // --- E2: spam, deshacer, el lector completo y vaciar la papelera ---
  "action.spam": "Marcar como spam",
  "action.notSpam": "No es spam",
  "action.doneSpam": (count: number): string =>
    count === 1 ? "Marcado como spam" : `${count} marcados como spam`,
  "action.doneNotSpam": (count: number): string =>
    count === 1 ? "Devuelto a la bandeja de entrada" : `${count} devueltos a la bandeja de entrada`,
  "action.undoDone": "Se deshizo la acción",
  "action.undoFailed": "No se pudo deshacer",
  "action.undoExpired": "No hay nada para deshacer",
  "action.emptyTrash": "Vaciar la papelera",
  "action.emptyTrashConfirm": (count: number): string =>
    count === 1
      ? "¿Eliminar definitivamente el mensaje de la Papelera? No se puede deshacer."
      : `¿Eliminar definitivamente los ${count} mensajes de la Papelera? No se puede deshacer.`,
  "action.emptyTrashEmpty": "La Papelera ya está vacía",
  "action.emptyTrashDone": (count: number): string =>
    count === 1 ? "1 mensaje eliminado definitivamente" : `${count} mensajes eliminados definitivamente`,
  "action.emptyTrashWorking": "Vaciando la Papelera…",
  "action.print": "Imprimir",
  "action.viewOriginal": "Ver original",
  "action.next": "Mensaje siguiente",
  "action.previous": "Mensaje anterior",
  "action.unsubscribe": "Cancelar la suscripción",

  // --- E2: las superficies nuevas del lector ---
  "reader.spamBanner": "Este mensaje está en Spam",
  "reader.spamBannerBody":
    "Moov lo muestra porque lo pediste, y mantiene sus imágenes y enlaces inertes. Si no corresponde que esté acá, marcalo como que no es spam.",
  "reader.spamImagesBlocked":
    "Las imágenes nunca se cargan en un mensaje que está en Spam.",
  "reader.originalTitle": "Mensaje original",
  "reader.originalHeaders": "Encabezados, tal como llegaron",
  "reader.originalLoading": "Cargando el original…",
  "reader.originalFailed": "No se pudo cargar el mensaje original",
  "reader.copy": "Copiar al portapapeles",
  "reader.copied": "Copiado",
  "reader.copyFailed": "No se pudo copiar. Seleccioná el texto y copialo a mano.",
  "reader.unsubscribeFrom": (list: string): string => `Cancelar la suscripción a ${list}`,
  "reader.unsubscribeOpensTab": "Abre la página del remitente en una pestaña nueva",
  "reader.unsubscribeLatency":
    "El remitente puede tardar unos días en dejar de enviar.",

  // --- P3: el compositor ---
  "compose.new": "Escribir",
  "compose.title": "Mensaje nuevo",
  "compose.titleReply": "Responder",
  "compose.titleForward": "Reenviar",
  "compose.titleDraft": "Borrador",
  "compose.from": "De",
  "compose.to": "Para",
  "compose.cc": "Cc",
  "compose.bcc": "Cco",
  "compose.showCc": "Agregar Cc",
  "compose.showBcc": "Agregar Cco",
  "compose.subject": "Asunto",
  "compose.subjectPlaceholder": "Asunto",
  "compose.body": "Mensaje",
  "compose.send": "Enviar",
  "compose.sending": "Enviando…",
  "compose.discard": "Descartar",
  "compose.close": "Cerrar el compositor",
  "compose.attach": "Adjuntar un archivo",
  "compose.attachments": (count: number): string =>
    count === 1 ? "1 adjunto" : `${count} adjuntos`,
  "compose.removeAttachment": (name: string): string => `Quitar ${name}`,
  "compose.removeRecipient": (address: string): string => `Quitar ${address}`,
  "compose.recipientCount": (count: number): string =>
    count === 1 ? "1 destinatario" : `${count} destinatarios`,
  "compose.uploading": (percent: number): string => `Subiendo… ${percent}%`,
  "compose.uploadFailed": "Este archivo no se pudo adjuntar",
  "compose.plainText": "Texto plano",
  "compose.richText": "Formato",
  "compose.bold": "Negrita",
  "compose.italic": "Cursiva",
  "compose.underline": "Subrayado",
  "compose.bulletList": "Lista con viñetas",
  "compose.orderedList": "Lista numerada",
  "compose.link": "Insertar un enlace",
  "compose.linkPrompt": "Dirección del enlace",
  "compose.linkInvalid":
    "Un enlace tiene que ser una dirección web (http, https) o una dirección de correo.",
  "compose.addressInvalid": (address: string): string =>
    `${address} no es una dirección de correo completa.`,
  "compose.noRecipients": "Agregá al menos un destinatario antes de enviar.",
  "compose.attributionLine": (date: string, sender: string): string =>
    `El ${date}, ${sender} escribió:`,
  "compose.forwardedHeader": "---------- Mensaje reenviado ----------",
  "compose.forwardedFrom": "De",
  "compose.forwardedDate": "Fecha",
  "compose.forwardedSubject": "Asunto",
  "compose.forwardedTo": "Para",

  // --- P3: borradores ---
  "draft.saving": "Guardando…",
  "draft.saved": "Borrador guardado",
  "draft.unsaved": "Cambios sin guardar",
  "draft.saveFailed": "El borrador no se pudo guardar",
  "draft.discardConfirm": "¿Descartar este borrador? Se pierde lo que escribiste.",
  "draft.discarded": "Borrador descartado",
  "draft.discardFailed": "El borrador no se pudo descartar",

  // --- P3: envío, con deshacer ---
  "send.undoWindow": (seconds: number): string => `Enviando en ${seconds}s`,
  "send.undo": "Deshacer",
  "send.sent": "Mensaje enviado",
  "send.canceled": "Envío cancelado — el mensaje no se transmitió",
  "send.failedTitle": "El mensaje no se envió",
  "send.cannotUnsend": "Ya es tarde para deshacer: el mensaje ya salió.",
  "send.sizeExceeded": (limit: string): string =>
    `Este archivo supera los ${limit} que acepta este servidor.`,
  "send.attachmentsExceeded": (limit: string): string =>
    `Los adjuntos suman más de los ${limit} que puede llevar un mensaje.`,

  // --- P3: carpetas ---
  "folder.create": "Carpeta nueva",
  "folder.name": "Nombre de la carpeta",
  "folder.createFailed": "La carpeta no se pudo crear",
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
