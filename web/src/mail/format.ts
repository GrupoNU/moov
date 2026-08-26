/**
 * Formatting for the message list and reading pane.
 *
 * Locale-aware through `Intl`, which is why these take a locale rather than
 * hardcoding a pattern: "20 ago" and "Aug 20" are the same information, and
 * the pilot reads the first.
 */

/**
 * The date a list row shows.
 *
 * Gmail's rule, copied: today shows a time, this year shows a day and month,
 * older shows a year. The reason it works is that the list is sorted by date,
 * so a row's date is only ever compared with its neighbours — the precision
 * you need is "how far from now", and that varies with distance.
 *
 * `now` is a parameter rather than `Date.now()` so the function is pure and a
 * test does not have to mock the clock to check the boundaries.
 */
export function formatListDate(
  isoDate: string | undefined,
  locale: string,
  now: Date = new Date(),
): string {
  if (isoDate === undefined) return "";
  const date = new Date(isoDate);
  if (Number.isNaN(date.getTime())) return "";

  const sameDay =
    date.getFullYear() === now.getFullYear() &&
    date.getMonth() === now.getMonth() &&
    date.getDate() === now.getDate();

  if (sameDay) {
    return new Intl.DateTimeFormat(locale, {
      hour: "2-digit",
      minute: "2-digit",
    }).format(date);
  }

  if (date.getFullYear() === now.getFullYear()) {
    return new Intl.DateTimeFormat(locale, { day: "numeric", month: "short" }).format(date);
  }

  return new Intl.DateTimeFormat(locale, {
    day: "numeric",
    month: "short",
    year: "numeric",
  }).format(date);
}

/** The full, unambiguous date for a reading pane and for a row's tooltip. */
export function formatFullDate(isoDate: string | undefined, locale: string): string {
  if (isoDate === undefined) return "";
  const date = new Date(isoDate);
  if (Number.isNaN(date.getTime())) return "";
  return new Intl.DateTimeFormat(locale, {
    dateStyle: "full",
    timeStyle: "short",
  }).format(date);
}

/**
 * A machine-readable date for `<time datetime>`.
 *
 * Returns undefined rather than a broken string for an unparseable input: an
 * invalid `datetime` attribute is worse than none, because assistive
 * technology may read it aloud.
 */
export function machineDate(isoDate: string | undefined): string | undefined {
  if (isoDate === undefined) return undefined;
  const date = new Date(isoDate);
  return Number.isNaN(date.getTime()) ? undefined : date.toISOString();
}

/**
 * A human file size.
 *
 * Binary units (KiB semantics) presented with the familiar KB/MB labels, which
 * is what mail clients have always shown. Sizes are rounded to one decimal
 * above a kilobyte and to none below, because "1.0 KB" reads as false
 * precision for something that is 1,024 bytes.
 */
export function formatBytes(bytes: number | undefined, locale: string): string {
  if (bytes === undefined || !Number.isFinite(bytes) || bytes < 0) return "";
  if (bytes < 1024) {
    return `${new Intl.NumberFormat(locale).format(bytes)} B`;
  }
  const units = ["KB", "MB", "GB", "TB"];
  let value = bytes / 1024;
  let unitIndex = 0;
  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024;
    unitIndex += 1;
  }
  const formatted = new Intl.NumberFormat(locale, {
    maximumFractionDigits: value < 10 ? 1 : 0,
  }).format(value);
  return `${formatted} ${units[unitIndex] ?? "KB"}`;
}

/**
 * The first user-perceived character of a string.
 *
 * `str[0]` and `[...str][0]` are both wrong here in ways that show up in real
 * mail: the first takes a UTF-16 code unit and splits any non-BMP character
 * (an emoji display name renders as a replacement box), the second takes a
 * code point and still splits a grapheme cluster (a flag emoji, or a letter
 * followed by a combining accent, becomes half of itself).
 *
 * `Intl.Segmenter` with `granularity: "grapheme"` is the only correct answer,
 * and it is available in every browser this app targets. The fallback exists
 * for jsdom and other non-browser environments, where a code point is a
 * reasonable approximation.
 */
function firstGrapheme(value: string): string {
  if (value === "") return "";
  if (typeof Intl.Segmenter === "function") {
    const segmenter = new Intl.Segmenter(undefined, { granularity: "grapheme" });
    for (const { segment } of segmenter.segment(value)) return segment;
    return "";
  }
  return Array.from(value)[0] ?? "";
}

/**
 * The initial(s) shown in a row's avatar.
 *
 * Takes the first grapheme of the first two words, which handles "Ana Gómez" →
 * "AG" and "soporte@example.com" → "S".
 */
export function initialsFor(label: string | undefined): string {
  if (label === undefined) return "";
  const cleaned = label.trim();
  if (cleaned === "") return "";
  const words = cleaned.split(/[\s.@_-]+/).filter((w) => w !== "");
  const joined = words.slice(0, 2).map(firstGrapheme).join("");
  return joined === ""
    ? firstGrapheme(cleaned).toLocaleUpperCase()
    : joined.toLocaleUpperCase();
}
