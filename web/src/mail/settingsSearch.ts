/**
 * Settings search (decision D-5, signed 2026-08-30).
 *
 * Gmail does NOT have this — and the canon records (§5) that no reason for its
 * absence is sourceable, which is why D-5 could be signed as a divergence at
 * all: P1 only forbids diverging from a REASONED omission. It costs nothing in
 * trust, and a settings surface that scales to eight sections needs it.
 *
 * # Client-side, and why that is the correct answer here
 *
 * The whole corpus is the settings screen's own labels: a few dozen short
 * strings that are already in memory as translations. Sending them to the
 * server would add a round trip, a query language and a relevance problem to
 * something a `filter` answers in microseconds — and it would break the moment
 * the user switches language, because the haystack IS the translation.
 *
 * # Why synonyms are part of the row and not an afterthought
 *
 * The label is what we called it; the query is what the user calls it. Someone
 * looking for the undo-send window types "cancelar", "deshacer" or "undo";
 * someone looking for density types "compacto", "espaciado" or "rows". Matching
 * only the rendered label makes the search look broken for exactly the people
 * who needed it — the ones who could not find the row by eye. So each row
 * carries keywords in BOTH languages, deliberately: a Spanish user who knows
 * the English name of a setting (very common in this market) still finds it.
 *
 * # Matching: normalized substring, no fuzziness
 *
 * Accents and case are folded (`NFD` + combining-mark strip), which is what
 * makes "imagenes" find "Imágenes" — the single most likely near-miss in a
 * Spanish UI. Beyond that it is a plain substring test on every term: a query
 * of several words matches a row that contains ALL of them, anywhere. No
 * fuzzy/edit-distance matching, because a settings list of this size has no
 * room for a false positive — showing the wrong row under a confident query is
 * worse than showing none, and "no results" is a state this screen renders
 * honestly.
 */

/** One searchable settings row, as the screen registers it. */
export interface SearchableRow {
  /** Stable identity — the row's key in the screen. */
  readonly id: string;
  /** The section it lives in, so a match can reveal its group. */
  readonly sectionId: string;
  /** The rendered label, already translated. */
  readonly label: string;
  /** The rendered description, already translated. May be empty. */
  readonly description?: string;
  /**
   * Extra words that should find this row.
   *
   * Hardcoded per row rather than derived, because a synonym is a product
   * decision ("does 'privacidad' find the images setting?") and not something
   * a stemmer can infer.
   */
  readonly keywords?: readonly string[];
}

/**
 * Folds a string for comparison: lowercase, accent-stripped, whitespace-tidy.
 *
 * `normalize("NFD")` decomposes "á" into "a" + U+0301, and the range strip
 * removes the combining mark — the standard accent-fold that needs no table
 * and no dependency. `\p{Diacritic}` would be tidier but requires the `u` flag
 * plus a property escape that older Safari mis-handles; the explicit
 * combining-marks block is the portable form.
 */
export function foldForSearch(value: string): string {
  return value
    .normalize("NFD")
    .replace(/[̀-ͯ]/g, "")
    .toLowerCase()
    .trim();
}

/** The words a query asks for. Empty query → no terms → everything matches. */
export function queryTerms(query: string): readonly string[] {
  const folded = foldForSearch(query);
  if (folded === "") return [];
  return folded.split(/\s+/).filter((term) => term !== "");
}

/** The folded text one row is searchable by. */
export function haystackFor(row: SearchableRow): string {
  return foldForSearch(
    [row.label, row.description ?? "", ...(row.keywords ?? [])].join(" "),
  );
}

/** True when a row satisfies every term of the query. */
export function rowMatches(row: SearchableRow, terms: readonly string[]): boolean {
  if (terms.length === 0) return true;
  const haystack = haystackFor(row);
  return terms.every((term) => haystack.includes(term));
}

/** What a search resolved to. */
export interface SearchResult {
  /** The ids of the rows to show. */
  readonly rowIds: ReadonlySet<string>;
  /** The ids of the sections that still have at least one visible row. */
  readonly sectionIds: ReadonlySet<string>;
  /** True when the user typed something — i.e. the screen is filtered. */
  readonly isFiltering: boolean;
  /** True when a real query matched nothing. */
  readonly isEmpty: boolean;
}

/**
 * Filters the settings surface by a query.
 *
 * Returns SETS of ids rather than a filtered array of rows, because the screen
 * renders its rows declaratively (each with its own control, wiring and
 * accessible name) and rebuilding that tree from a search result would mean
 * describing every control twice. The screen keeps its JSX and asks "should
 * this row show?" — which also means a filtered row keeps its React identity
 * and its control keeps its focus while the user is still typing.
 */
export function searchSettings(
  rows: readonly SearchableRow[],
  query: string,
): SearchResult {
  const terms = queryTerms(query);
  const isFiltering = terms.length > 0;

  const rowIds = new Set<string>();
  const sectionIds = new Set<string>();
  for (const row of rows) {
    if (!rowMatches(row, terms)) continue;
    rowIds.add(row.id);
    sectionIds.add(row.sectionId);
  }

  return {
    rowIds,
    sectionIds,
    isFiltering,
    isEmpty: isFiltering && rowIds.size === 0,
  };
}
