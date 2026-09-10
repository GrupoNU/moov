import type { PlainKey } from "../../i18n/strings";
import type { PrefKey } from "../../mail/prefs";
import type { SettingsTab } from "../../router/routes";

/**
 * The settings registry: what rows exist, where they live, and what finds them.
 *
 * # Why this is data and the screen is JSX
 *
 * Each row appears twice — once here, once as JSX in `SettingsDialog`. The
 * alternative, generating the controls from this table, was rejected on
 * purpose: every control has different wiring (a select, a switch, a radio
 * group, a textarea with its own explicit save), and a generator able to
 * express all of them would be a worse abstraction than the duplication. What
 * the duplication costs is drift, and drift is what a test can catch — so
 * `registry.test.ts` asserts the two lists agree, which is the guarantee
 * generation would have bought at a much higher price.
 *
 * # Why it is its own module
 *
 * The screen is a component file, and Fast Refresh wants component files to
 * export only components. This table is the non-component half, so it lives
 * where a search test can import it without pulling a `<dialog>` into scope.
 */

/**
 * The keys these rows may use: the ones `t` resolves on its own.
 *
 * That includes the brand-parameterised strings — from a row's point of view
 * `t(key)` still returns a finished label, and excluding them would push four
 * settings descriptions out of the registry the moment they learned the host's
 * name.
 */
export type PlainStringKey = PlainKey;

/**
 * The sections a row can belong to.
 *
 * # E12: sections and TABS are now two different things
 *
 * Through E11 these were the same list: one rail item per section, one section
 * per rail item. Canon 07 §5 breaks that, and correctly — Gmail's settings tabs
 * are COARSER than its sections, because a tab is a destination you deep-link
 * to and a section is a heading inside one. "Filtros y direcciones bloqueadas"
 * is one tab holding two sections; "Cuenta" holds the identity, the signatures
 * and the storage bar.
 *
 * So this list stays the FINE-grained one — it is what `showRow` and the
 * settings search resolve against, and splitting a search hit down to its
 * section is what lets the page scroll to the right heading rather than to the
 * top of a long tab. {@link SETTINGS_TABS} in `router/routes.ts` is the coarse
 * one, and {@link SECTION_TAB} is the map between them.
 *
 * "appearance" is GONE as a section, and that is a real regrouping rather than
 * a rename: its three rows moved where canon 07 §5 puts them. Theme and density
 * live in the quick panel (B2), which is where Gmail keeps them and where a
 * live preview is possible; the reading pane joins the inbox tab, because
 * "where an open message appears" is a fact about the inbox, not about colour.
 */
export const SECTION_IDS = [
  "general",
  "inbox",
  "account",
  /*
   * E8. Its own TAB in Gmail's IA (second, right after General), because it is
   * the only section here that manages server-side objects rather than
   * preferences — a label is a thing you create, not a value you pick.
   */
  "labels",
  /*
   * E6. Filters, Blocked, Forwarding and Vacation are all backed by ONE Sieve
   * script on the mail server; they are four sections because they are four
   * user-facing jobs, and because a blocked rule has no visible action to show
   * in a filter list (the Junk filing is compiled from its type tag).
   *
   * E12 folds the first two into ONE TAB, which is Gmail's own fold and is
   * honest here in a way it is only conventional at Google: they are literally
   * the same script on our server. Vacation joins Forwarding for the weaker but
   * still real reason that both are "what happens to mail you do not read".
   */
  "filters",
  "blocked",
  "forwarding",
  "vacation",
  "offline",
] as const;

export type SectionId = (typeof SECTION_IDS)[number];

export const SECTION_TITLES: Readonly<Record<SectionId, PlainStringKey>> = {
  general: "settings.section.general",
  inbox: "settings.section.inbox",
  account: "settings.section.account",
  labels: "settings.section.labels",
  filters: "settings.section.filters",
  blocked: "settings.section.blocked",
  forwarding: "settings.section.forwarding",
  vacation: "settings.section.vacation",
  offline: "settings.section.offline",
};

/**
 * Which TAB each section is rendered under (canon 07 §5).
 *
 * A total map, so a section added without a home is a compile error rather than
 * a section that silently renders nowhere — which is exactly the failure mode
 * of a page that picks its sections with a `switch` and a default case.
 */
export const SECTION_TAB: Readonly<Record<SectionId, SettingsTab>> = {
  general: "general",
  inbox: "inbox",
  account: "account",
  labels: "labels",
  filters: "filters",
  // Gmail's fold: "Filters and blocked addresses" is one tab.
  blocked: "filters",
  forwarding: "forwarding",
  // "What happens to mail you do not read" — the weaker of the two folds, but
  // a vacation responder alone does not earn a tab of its own.
  vacation: "forwarding",
  offline: "offline",
};

/** The sections a tab renders, in order. Derived, never a second hand-kept list. */
export function sectionsOfTab(tab: SettingsTab): readonly SectionId[] {
  return SECTION_IDS.filter((id) => SECTION_TAB[id] === tab);
}

/** The tab a section lives under — what a settings-search hit navigates to. */
export function tabOfSection(section: SectionId): SettingsTab {
  return SECTION_TAB[section];
}

/** The label the tab row shows for each tab. */
export const TAB_TITLES: Readonly<Record<SettingsTab, PlainStringKey>> = {
  general: "settings.section.general",
  labels: "settings.section.labels",
  inbox: "settings.section.inbox",
  account: "settings.section.account",
  // The FOLDED name, not "Filters": a tab that hides the blocked list behind a
  // label that does not mention it is a tab nobody looks in for it.
  filters: "settings.tab.filters",
  forwarding: "settings.section.forwarding",
  offline: "settings.section.offline",
};

/**
 * Rows whose CONTROL lives in the quick-settings panel, not on this page
 * (E12/B2, canon 07 §4).
 *
 * They keep their registry entries — so the settings search still finds
 * "densidad" and "tema", which is the whole point of D-5 — and NOTHING ELSE.
 * Two live controls over one preference is the drift this codebase avoids
 * everywhere else, and the quick panel is where Gmail puts these two because
 * it is the only surface where the change is visible as you make it.
 *
 * P0-7 corrected what the page does with them. They used to render on the
 * Recibidos tab as pointer rows ("Abrir los ajustes rápidos") — rows that look
 * like settings and settle nothing, which Gmail never has and which the review
 * called dead rows. The page now renders them ONLY when a search surfaced
 * them, and what it renders is a way into the panel rather than an empty
 * anchor. `SettingsPage`'s `isSearchHit` is the predicate; a test pins both
 * halves (absent while browsing, present and clickable under a search).
 */
export const QUICK_PANEL_ROWS: ReadonlySet<string> = new Set(["theme", "density"]);

/**
 * Gmail's "Tamaño máximo de la página" is DELIBERATELY not a row here.
 *
 * Gmail's General tab offers "Mostrar [50] conversaciones por página", and it
 * is the one confirmed General row this page does not mirror. The reason is not
 * scope: Moov's list does not paginate BY A PREFERENCE. B4's pager is fixed at
 * 50 and the list under it is virtualized, so the number a user picked would
 * change nothing they can see — the rows they scroll past are rendered on
 * demand either way.
 *
 * A control that writes a preference nothing reads is exactly the dead control
 * principle P4 forbids, and it is worse than a missing one: it invites the user
 * to tune something and then silently ignores them. If the pager ever becomes
 * configurable this constant is where the row goes, with a preference behind it.
 */
/*
 * F-22: there is no "Avanzadas" tab, and there should not be an empty one.
 *
 * Gmail's Advanced tab is where its opt-in experiments live (plantillas,
 * auto-advance, multiple inboxes). Ours would be empty today — every setting
 * this build has belongs on a tab that already exists — and an empty tab is the
 * dead control principle P4 forbids, one level up.
 *
 * This comment is the marker, not a placeholder: when a deferred feature from
 * plan §6 lands behind an opt-in (superstars, the GC-3 splits, the queue
 * operators), THIS is where its section id goes, with "advanced" added to
 * SETTINGS_TABS and SECTION_TAB in the same change.
 */

export const PAGE_SIZE_ROW_OMITTED =
  "Gmail's page-size preference has no reader in Moov: the pager is fixed at 50 " +
  "and the list beneath it is virtualized, so the setting would change nothing.";

/** One row: its strings and the words that should find it. */
export interface RowSpec {
  readonly id: string;
  readonly sectionId: SectionId;
  readonly labelKey: PlainStringKey;
  readonly descriptionKey?: PlainStringKey;
  /**
   * Extra words that should find this row (D-5).
   *
   * The label is what we called it; the query is what the user calls it.
   * Someone looking for the undo-send window types "cancelar", "deshacer" or
   * "undo"; someone worried about tracking types "privacidad", not "imágenes
   * remotas". Matching only the rendered label makes the search look broken
   * for exactly the people who needed it — the ones who could not find the row
   * by eye.
   *
   * Both languages appear on every row, deliberately: a Spanish-speaking user
   * knowing a setting by its English name is the norm in this market, not an
   * edge case. The words are stored ACCENT-FREE because the matcher folds both
   * sides anyway, and an unaccented keyword is one less thing to get wrong.
   */
  readonly keywords: readonly string[];
}

export const SETTINGS_ROWS: readonly RowSpec[] = [
  // --- General ---
  {
    id: "language",
    sectionId: "general",
    labelKey: "settings.language.label",
    descriptionKey: "settings.language.description",
    keywords: ["language", "idioma", "locale", "espanol", "spanish", "english", "ingles"],
  },
  {
    id: "undoSend",
    sectionId: "general",
    labelKey: "settings.undoSend.label",
    descriptionKey: "settings.undoSend.description",
    keywords: [
      "undo",
      "deshacer",
      "cancelar",
      "cancel",
      "send",
      "enviar",
      "envio",
      "seconds",
      "segundos",
    ],
  },
  {
    id: "images",
    sectionId: "general",
    labelKey: "settings.images.label",
    descriptionKey: "settings.images.description",
    keywords: [
      "images",
      "imagenes",
      "remote",
      "remotas",
      "proxy",
      "privacidad",
      "privacy",
      "tracking",
      "pixel",
    ],
  },
  {
    id: "conversationView",
    sectionId: "general",
    labelKey: "settings.conversation.label",
    descriptionKey: "settings.conversation.description",
    keywords: ["conversation", "conversacion", "thread", "hilo", "grouping", "agrupar"],
  },
  {
    id: "hoverActions",
    sectionId: "general",
    labelKey: "settings.hover.label",
    descriptionKey: "settings.hover.description",
    keywords: ["hover", "cursor", "acciones", "actions", "buttons", "botones", "row", "fila"],
  },
  {
    id: "autoAdvance",
    sectionId: "general",
    labelKey: "settings.autoAdvance.label",
    descriptionKey: "settings.autoAdvance.description",
    keywords: [
      "auto",
      "advance",
      "avance",
      "siguiente",
      "next",
      "after",
      "despues",
      "archive",
      "archivar",
    ],
  },
  {
    id: "keyboardShortcuts",
    sectionId: "general",
    labelKey: "settings.keyboard.label",
    descriptionKey: "settings.keyboard.description",
    keywords: ["keyboard", "teclado", "shortcuts", "atajos", "keys", "teclas", "gmail"],
  },
  {
    id: "showSnippets",
    sectionId: "general",
    labelKey: "settings.snippets.label",
    descriptionKey: "settings.snippets.description",
    keywords: ["snippets", "fragmentos", "preview", "vista previa", "primera linea", "first line"],
  },
  {
    /* E7 / prefs v2: Gmail's "Show 'Send & Archive' button in reply". */
    id: "sendAndArchive",
    sectionId: "general",
    labelKey: "settings.sendAndArchive.label",
    descriptionKey: "settings.sendAndArchive.description",
    keywords: [
      "send",
      "enviar",
      "archive",
      "archivar",
      "reply",
      "responder",
      "button",
      "boton",
    ],
  },
  {
    /* E5 v2: which reply the button and `r` open (canon §2.3). */
    id: "replyBehavior",
    sectionId: "general",
    labelKey: "settings.replyBehavior.label",
    descriptionKey: "settings.replyBehavior.description",
    keywords: [
      "reply",
      "responder",
      "reply all",
      "responder a todos",
      "default",
      "predeterminado",
      "por defecto",
      "r",
    ],
  },

  // --- Inbox (canon 07 §5's "Recibidos") ---
  //
  // E12 moved `readingPane` here from the deleted "appearance" section: "where
  // an open message appears" is a fact about the inbox, not about colour.
  // `theme` and `density` moved OUT of the page entirely — to the quick panel
  // (B2), which is where Gmail keeps them and the only surface where the live
  // preview that makes them choosable is possible. Their registry rows moved
  // WITH them rather than being deleted, so the settings search still finds
  // them; the page renders a pointer at the panel instead of a duplicate
  // control (see `QUICK_PANEL_ROWS` below).
  {
    id: "readingPane",
    sectionId: "inbox",
    labelKey: "settings.readingPane.label",
    descriptionKey: "settings.readingPane.description",
    keywords: [
      "reading",
      "lectura",
      "pane",
      "panel",
      "split",
      "dividir",
      "layout",
      "right",
      "derecha",
      "bottom",
      "abajo",
    ],
  },
  {
    id: "theme",
    sectionId: "inbox",
    labelKey: "theme.label",
    descriptionKey: "settings.theme.description",
    keywords: [
      "theme",
      "tema",
      "dark",
      "oscuro",
      "light",
      "claro",
      "system",
      "sistema",
      "colors",
      "colores",
    ],
  },
  {
    id: "density",
    sectionId: "inbox",
    labelKey: "settings.density.label",
    descriptionKey: "settings.density.description",
    keywords: [
      "density",
      "densidad",
      "compact",
      "compacta",
      "comfortable",
      "comoda",
      "rows",
      "filas",
      "spacing",
      "espacio",
    ],
  },
  {
    id: "inboxType",
    sectionId: "inbox",
    labelKey: "settings.inboxType.label",
    descriptionKey: "settings.inboxType.description",
    keywords: [
      "inbox",
      "bandeja",
      "recibidos",
      "unread",
      "no leidos",
      "starred",
      "destacados",
      "sort",
      "orden",
    ],
  },
  {
    id: "notifications",
    sectionId: "inbox",
    labelKey: "settings.notifications.label",
    descriptionKey: "settings.notifications.description",
    keywords: [
      "notifications",
      "notificaciones",
      "desktop",
      "escritorio",
      "alerts",
      "avisos",
      "permission",
      "permiso",
    ],
  },

  // --- Account ---
  {
    id: "identity",
    sectionId: "account",
    labelKey: "settings.identity.label",
    descriptionKey: "settings.identity.description",
    keywords: ["identity", "identidad", "from", "remitente", "address", "direccion", "email", "correo"],
  },
  {
    id: "signature",
    sectionId: "account",
    labelKey: "settings.signature.label",
    descriptionKey: "settings.signature.description",
    keywords: ["signature", "firma", "footer", "pie"],
  },
  {
    id: "addressAutocomplete",
    sectionId: "account",
    labelKey: "settings.addressAutocomplete.label",
    descriptionKey: "settings.addressAutocomplete.description",
    keywords: [
      "autocomplete",
      "autocompletado",
      "addresses",
      "direcciones",
      "contacts",
      "contactos",
      "suggestions",
      "sugerencias",
      "recipients",
      "destinatarios",
      // Someone looking for this row because they are worried about what is
      // stored searches for the worry, not for the feature's name.
      "privacy",
      "privacidad",
      "delete",
      "borrar",
    ],
  },
  {
    /* E7 / prefs v2: the named signatures, with the new/reply defaults. */
    id: "signatures",
    sectionId: "account",
    labelKey: "settings.signatures.label",
    descriptionKey: "settings.signatures.description",
    keywords: [
      "signature",
      "signatures",
      "firma",
      "firmas",
      "footer",
      "pie",
      "reply",
      "respuesta",
    ],
  },
  {
    /* E6: the storage bar, read live from Dovecot (RFC 9425, canon §2.11). */
    id: "quota",
    sectionId: "account",
    labelKey: "quota.label",
    descriptionKey: "quota.description",
    keywords: [
      "quota",
      "cuota",
      "storage",
      "almacenamiento",
      "space",
      "espacio",
      "full",
      "lleno",
      "size",
      "tamano",
      "gb",
      "mb",
    ],
  },

  // --- Labels (E8) ---
  {
    id: "labels",
    sectionId: "labels",
    labelKey: "settings.section.labels",
    descriptionKey: "settings.labels.description",
    keywords: [
      "labels",
      "etiquetas",
      "label",
      "etiqueta",
      "tags",
      "tag",
      "keywords",
      "color",
      "colores",
      "colours",
      "chips",
    ],
  },

  // --- E6: the four Sieve-backed sections ---
  //
  // These were skeletons through E5 ("llega con la épica de Sieve"). The epic
  // landed, so the rows now name what they DO. The skeleton strings survive for
  // the one honest case left: a server that does not advertise the capability,
  // where the section renders the absence instead of a control that cannot work.
  {
    id: "filters",
    sectionId: "filters",
    labelKey: "settings.section.filters",
    descriptionKey: "filters.description",
    keywords: [
      "filters",
      "filtros",
      "rules",
      "reglas",
      "sieve",
      "label",
      "etiquetar",
      "archive",
      "archivar",
      "forward",
      "reenviar",
    ],
  },
  {
    id: "blocked",
    sectionId: "blocked",
    labelKey: "settings.section.blocked",
    descriptionKey: "blocked.description",
    keywords: [
      "blocked",
      "bloqueados",
      "block",
      "bloquear",
      "sender",
      "remitente",
      "spam",
      "unsubscribe",
      "baja",
    ],
  },
  {
    id: "forwarding",
    sectionId: "forwarding",
    labelKey: "settings.section.forwarding",
    descriptionKey: "forwarding.description",
    keywords: [
      "forwarding",
      "reenvio",
      "forward",
      "reenviar",
      "redirect",
      "copy",
      "copia",
      "verify",
      "verificar",
    ],
  },
  {
    id: "vacation",
    sectionId: "vacation",
    labelKey: "settings.section.vacation",
    descriptionKey: "vacation.description",
    keywords: [
      "vacation",
      "vacaciones",
      "away",
      "ausencia",
      "out of office",
      "responder",
      "auto reply",
      "respuesta automatica",
      "sieve",
    ],
  },
  /*
   * E9b / prefs v2: the offline depth.
   *
   * These two REPLACE the "offline" skeleton row, which pointed at
   * `settings.offline.soon` while the string that actually described the gap
   * (`offline.depthPending`) had no render site at all — the gate found it
   * defined in both locales and shown nowhere. Real controls are the fix.
   */
  {
    id: "offlineHeaders",
    sectionId: "offline",
    labelKey: "settings.offlineHeaders.label",
    descriptionKey: "settings.offlineHeaders.description",
    keywords: [
      "offline",
      "sin conexion",
      "sync",
      "sincronizacion",
      "pwa",
      "storage",
      "almacenamiento",
      "depth",
      "profundidad",
      "messages",
      "mensajes",
    ],
  },
  {
    id: "offlineBodies",
    sectionId: "offline",
    labelKey: "settings.offlineBodies.label",
    descriptionKey: "settings.offlineBodies.description",
    keywords: [
      "offline",
      "sin conexion",
      "bodies",
      "cuerpos",
      "storage",
      "almacenamiento",
      "depth",
      "profundidad",
      "attachments",
      "adjuntos",
    ],
  },
];

/**
 * The preference each row writes, by row id.
 *
 * Rows that write no preference (the identity display, the signature, the four
 * skeletons) are absent rather than mapped to a sentinel — "this row has no
 * pref" is exactly what an absent key means, and a sentinel would have to be
 * excluded from the coverage check below anyway.
 *
 * The test that consumes this asserts the map is TOTAL over `PrefKey`: every
 * preference the server serves has a row that sets it. That is the check which
 * catches the failure this epic exists to prevent — a preference that lives in
 * the schema, is validated by the server, and has no way for a user to change
 * it.
 */
export const ROW_PREF_KEYS: Readonly<Record<string, PrefKey>> = {
  language: "language",
  undoSend: "undoSendSeconds",
  images: "imagesPolicy",
  conversationView: "conversationView",
  hoverActions: "hoverActions",
  autoAdvance: "autoAdvance",
  keyboardShortcuts: "keyboardShortcuts",
  showSnippets: "showSnippets",
  theme: "theme",
  density: "density",
  readingPane: "readingPane",
  inboxType: "inboxType",
  notifications: "notifications",
  // --- prefs v2 ---
  sendAndArchive: "sendAndArchive",
  replyBehavior: "defaultReplyBehavior",
  signatures: "signatures",
  addressAutocomplete: "addressAutocomplete",
  /*
   * `offlineDepth` is ONE preference behind TWO rows — the header count and the
   * body count are independent settings inside one object, and a user looking
   * for either searches for that one, not for "depth".
   *
   * Only the header row is mapped, and that is not a shortcut: this table's
   * "no two rows write one key" invariant is real (two controls writing one key
   * would race and disagree on screen), and a structured preference edited
   * field-by-field is precisely the case it was not written for. Mapping one
   * row keeps the coverage check honest — `offlineDepth` reaches a control —
   * without asserting a one-to-one shape that does not hold. `offlineBodies`
   * is named in the sibling test as the documented second half.
   */
  offlineHeaders: "offlineDepth",
  /*
   * `labels` is deliberately ABSENT, and it is the one entry worth explaining
   * rather than adding. It has a real, fully-wired UI — the Labels section's
   * per-label colour and visibility pickers (`LabelsSection.tsx`) — but that
   * section writes it through `useLabels`, not through a `SettingRow` whose
   * control calls `setPref` with this key. Mapping the "labels" row would claim
   * a shape this table describes and that the section does not have, and the
   * drift test would then be pinning a fiction.
   *
   * The coverage test names it as the single documented exemption, so the
   * absence is asserted rather than merely tolerated.
   */
};

/**
 * Preferences whose control exists but is NOT a `ROW_PREF_KEYS` row, with the
 * reason each is exempt.
 *
 * It is a table rather than a hard-coded list in the test so the exemption and
 * its justification live beside the mapping they are an exception to. A key
 * added here without a real control is the failure this whole file guards
 * against, so the reasons are load-bearing prose, not decoration.
 */
export const PREF_ROWS_BY_OTHER_MEANS: Readonly<Record<string, string>> = {
  labels:
    "The Labels section's per-label colour and visibility pickers write it through " +
    "useLabels, not through a SettingRow control.",
  folderVisibility:
    "The Labels tab's Carpetas table (P0-5c) writes one entry per folder from a " +
    "select beside each name, not through a SettingRow control — a row per folder " +
    "would be a settings page as long as the account's folder list.",
};
