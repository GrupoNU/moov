import type { Strings } from "../../i18n/strings";
import type { PrefKey } from "../../mail/prefs";

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

/** The keys these rows may use: the plain-string ones. */
export type PlainStringKey = {
  [K in keyof Strings]: Strings[K] extends string ? K : never;
}[keyof Strings];

/** The sections, in the order the rail lists them (Gmail's IA, canon §3). */
export const SECTION_IDS = [
  "general",
  "appearance",
  "inbox",
  "account",
  /*
   * E8. It sits after "account" and before the skeletons because it is a REAL
   * section — Gmail's own IA puts Labels second, right after General, but ours
   * earns its place by being the only section here that manages server-side
   * objects rather than preferences, and grouping it with the account is the
   * honest reading of what it is.
   */
  "labels",
  "filters",
  "forwarding",
  "vacation",
  "offline",
] as const;

export type SectionId = (typeof SECTION_IDS)[number];

export const SECTION_TITLES: Readonly<Record<SectionId, PlainStringKey>> = {
  general: "settings.section.general",
  appearance: "settings.section.appearance",
  inbox: "settings.section.inbox",
  account: "settings.section.account",
  labels: "settings.section.labels",
  filters: "settings.section.filters",
  forwarding: "settings.section.forwarding",
  vacation: "settings.section.vacation",
  offline: "settings.section.offline",
};

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

  // --- Appearance ---
  {
    id: "theme",
    sectionId: "appearance",
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
    sectionId: "appearance",
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
    id: "readingPane",
    sectionId: "appearance",
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

  // --- Inbox ---
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

  // --- the honest skeletons (P4: a named absence, never a dead control) ---
  {
    id: "filters",
    sectionId: "filters",
    labelKey: "settings.filters.soon",
    descriptionKey: "settings.filters.soonBody",
    keywords: ["filters", "filtros", "rules", "reglas", "sieve", "block", "bloquear", "spam"],
  },
  {
    id: "forwarding",
    sectionId: "forwarding",
    labelKey: "settings.forwarding.soon",
    descriptionKey: "settings.forwarding.soonBody",
    keywords: ["forwarding", "reenvio", "forward", "reenviar", "redirect", "sieve", "block", "bloquear"],
  },
  {
    id: "vacation",
    sectionId: "vacation",
    labelKey: "settings.vacation.soon",
    descriptionKey: "settings.vacation.soonBody",
    keywords: [
      "vacation",
      "vacaciones",
      "away",
      "ausencia",
      "out of office",
      "responder",
      "auto reply",
      "respuesta automatica",
    ],
  },
  {
    id: "offline",
    sectionId: "offline",
    labelKey: "settings.offline.soon",
    descriptionKey: "settings.offline.soonBody",
    keywords: [
      "offline",
      "sin conexion",
      "outbox",
      "bandeja de salida",
      "sync",
      "sincronizacion",
      "pwa",
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
};
