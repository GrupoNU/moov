import type {
  AutoAdvance,
  Density,
  ImagesPolicy,
  InboxType,
  ReadingPane,
  ReplyBehavior,
  Theme,
} from "../../mail/prefs";
import type { PlainStringKey } from "./registry";

/**
 * The string key for every option of every radio group, in one place.
 *
 * # Why these live in a module of their own
 *
 * Two surfaces render the same options — the quick panel and the settings page
 * (F-34/F-35) — and both need these tables. Kept in `QuickSettingsPanel.tsx`
 * they made it a file exporting constants as well as a component, which the
 * Fast Refresh rule rightly rejects: a module that mixes the two loses hot
 * reloading for the component. Here, both surfaces import from a file that has
 * no component in it and nothing to reload.
 *
 * # Why they are written out per value rather than templated
 *
 * `` `settings.density.${option}` `` would need an `as PlainStringKey` to
 * compile, and that cast is exactly the thing these tables exist to avoid: it
 * would let a renamed or deleted string key pass the type checker and reach a
 * user as a raw key on screen. Written as literals, each one is checked against
 * `Strings` — which is derived from the English table, so a missing SPANISH
 * translation is a compile error too. That guarantee is the whole point of the
 * i18n module's design, and a template literal quietly opts out of it.
 *
 * Every table is a TOTAL record over its union, so adding a value to
 * `READING_PANES` without adding its label is a compile error rather than a
 * radio that renders with no name.
 */

export const DENSITY_LABELS: Readonly<Record<Density, PlainStringKey>> = {
  default: "settings.density.default",
  comfortable: "settings.density.comfortable",
  compact: "settings.density.compact",
};

export const THEME_LABELS: Readonly<Record<Theme, PlainStringKey>> = {
  light: "theme.light",
  dark: "theme.dark",
  system: "theme.system",
};

export const INBOX_TYPE_LABELS: Readonly<Record<InboxType, PlainStringKey>> = {
  default: "settings.inboxType.default",
  unread_first: "settings.inboxType.unread_first",
  starred_first: "settings.inboxType.starred_first",
};

export const READING_PANE_LABELS: Readonly<Record<ReadingPane, PlainStringKey>> = {
  none: "settings.readingPane.none",
  right: "settings.readingPane.right",
  bottom: "settings.readingPane.bottom",
};

/*
 * F-26's three groups: the settings-page rows that were `<select>`s over two or
 * three values. Each carries a NOTES table beside its labels — Gmail's inline
 * explanation, which is what makes radios worth more than the select they
 * replace: the sentence answers "what does this one do" at the moment of
 * comparison, which a collapsed select cannot.
 */

export const IMAGES_LABELS: Readonly<Record<ImagesPolicy, PlainStringKey>> = {
  always: "settings.images.always",
  ask: "settings.images.ask",
};

export const IMAGES_NOTES: Readonly<Record<ImagesPolicy, PlainStringKey>> = {
  always: "settings.images.alwaysNote",
  ask: "settings.images.askNote",
};

export const AUTO_ADVANCE_LABELS: Readonly<Record<AutoAdvance, PlainStringKey>> = {
  list: "settings.autoAdvance.list",
  newer: "settings.autoAdvance.newer",
  older: "settings.autoAdvance.older",
};

export const AUTO_ADVANCE_NOTES: Readonly<Record<AutoAdvance, PlainStringKey>> = {
  list: "settings.autoAdvance.listNote",
  newer: "settings.autoAdvance.newerNote",
  older: "settings.autoAdvance.olderNote",
};

export const REPLY_BEHAVIOR_LABELS: Readonly<Record<ReplyBehavior, PlainStringKey>> = {
  reply: "settings.replyBehavior.reply",
  replyAll: "settings.replyBehavior.replyAll",
};

export const REPLY_BEHAVIOR_NOTES: Readonly<Record<ReplyBehavior, PlainStringKey>> = {
  reply: "settings.replyBehavior.replyNote",
  replyAll: "settings.replyBehavior.replyAllNote",
};
