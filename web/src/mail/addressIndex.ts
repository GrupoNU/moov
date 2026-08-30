/**
 * The address index — recipient autocomplete without a contacts subsystem
 * (L3 E7; canon §2.3, §7.9).
 *
 * # Why this design, and whose blessing it has
 *
 * Gmail does not require a contacts app to autocomplete a recipient. It
 * auto-saves the addresses you mail to into "Other contacts" and completes from
 * those, with one user-facing escape hatch — "I'll add contacts myself"
 * (/contacts/answer/1069522). The canon calls this out explicitly (§7.9) as
 * **Gmail's own blessing of the address-index-without-contacts design**, which
 * is what lets E7 deliver full autocomplete parity while a real contacts
 * subsystem stays deferred (plan §6, XL).
 *
 * So the model is copied rather than invented:
 *
 *   - **Auto-fed, never hand-curated.** No "add contact" UI exists, because
 *     Gmail's does not either at this layer. The index is a by-product of using
 *     the mailbox.
 *   - **Two feeds, both bounded.** Write-through from every message the app
 *     loads (the same discipline `cache.ts` uses for headers — browsing IS the
 *     sync), plus one explicit, windowed scan of Sent on first enable.
 *   - **One opt-out that stops the feeding AND can erase what was collected.**
 *     A privacy control that only hides the data is not a privacy control.
 *
 * # Ranking: frequency first, recency to break ties
 *
 * `timesSeen` descending, then `lastSeenAt` descending. That order is
 * deliberate and is the one every mail client converges on: the person you mail
 * weekly should outrank the one you mailed once this morning, because the
 * frequent correspondent is who you are usually typing. Pure recency puts a
 * one-off address at the top of your list all day; pure frequency never lets a
 * new colleague surface. Frequency-then-recency gets both.
 *
 * # What is stored, and what is deliberately not
 *
 * `{email, displayName, lastSeenAt, timesSeen, source}` — no message ids, no
 * subjects, no bodies, nothing that reconstructs WHO WROTE WHAT to whom. The
 * index answers "is this an address you have seen" and nothing else. It also
 * never leaves the browser: there is no prefs key for it (see the named gap in
 * `addressPrefs`), which means it does not roam and, more to the point, is
 * never uploaded anywhere.
 *
 * # Why this module has no IndexedDB in it
 *
 * Everything here is pure: merging an observation into a row, ranking, matching
 * a query. `offline/addressStore.ts` holds the persistence. The split is the
 * same one `labelStore.ts` makes and for the same reason — the interesting
 * behaviour (does `ana` match `Ana Gómez <a.gomez@x.com>`?) is testable as a
 * function, and a bug in it should not require a database to reproduce.
 */

import { isValidEmail } from "./addresses";
import type { EmailAddress } from "./types";

/**
 * Where an address was first seen. Kept for honesty in the settings UI ("these
 * came from your sent mail") and because it costs one short string.
 */
export type AddressSource = "browsed" | "sent";

/** One address the index knows about. */
export interface IndexedAddress {
  /** Lowercased — the identity. Comparison is case-insensitive everywhere. */
  readonly email: string;
  /**
   * The most recent NON-EMPTY display name seen for this address.
   *
   * "Most recent non-empty" rather than "first": people change their display
   * name (marriage, a new employer's convention, fixing a typo), and the newest
   * one they sent is the one they want seen. An empty name never overwrites a
   * good one — a message whose header carried only the bare address should not
   * erase a name learned from another.
   */
  readonly displayName: string | undefined;
  /** Epoch ms of the most recent sighting. */
  readonly lastSeenAt: number;
  /** How many sightings. Drives the ranking. */
  readonly timesSeen: number;
  readonly source: AddressSource;
}

/**
 * Folds one sighting into an existing row (or creates it).
 *
 * Pure, and the single place the merge rules live, so the write-through path
 * and the Sent scan cannot drift on them.
 */
export function mergeSighting(
  existing: IndexedAddress | undefined,
  sighting: {
    readonly email: string;
    readonly displayName?: string | undefined;
    readonly seenAt: number;
    readonly source: AddressSource;
  },
): IndexedAddress {
  const email = sighting.email.trim().toLowerCase();
  const name = sighting.displayName?.trim();
  const cleanName = name === undefined || name === "" ? undefined : name;

  if (existing === undefined) {
    return {
      email,
      displayName: cleanName,
      lastSeenAt: sighting.seenAt,
      timesSeen: 1,
      source: sighting.source,
    };
  }

  return {
    email,
    // A newer non-empty name wins; an empty one never erases what we have.
    displayName: cleanName ?? existing.displayName,
    // `Math.max`, not "the new one": the two feeds interleave, and the Sent
    // scan walks BACKWARDS through old mail. A scan must never make an address
    // look staler than the message the user just opened.
    lastSeenAt: Math.max(existing.lastSeenAt, sighting.seenAt),
    timesSeen: existing.timesSeen + 1,
    // The first source is kept: "where did this come from" is about origin.
    source: existing.source,
  };
}

/**
 * Extracts the addresses worth indexing from one message's headers.
 *
 * `from`, `to` and `cc` — never `bcc`. A Bcc on a message in YOUR mailbox is
 * either your own address (useless) or, on a sent message, someone you
 * deliberately hid from the other recipients; surfacing them in an autocomplete
 * that can be seen over your shoulder is a small betrayal of what Bcc means.
 * Gmail indexes recipients you mail, and a Bcc'd recipient is one you mailed —
 * so this is a deliberate divergence, in the more conservative direction, and
 * it is recorded here rather than left to be discovered.
 *
 * Invalid addresses are dropped: the index exists to be typed into a recipient
 * field, and an entry that would produce an invalid chip is worse than absent.
 * `ownAddress` is dropped too — completing your own address to yourself is
 * noise in every single suggestion list.
 */
export function addressesFromMessage(
  message: {
    readonly from?: readonly EmailAddress[] | null | undefined;
    readonly to?: readonly EmailAddress[] | null | undefined;
    readonly cc?: readonly EmailAddress[] | null | undefined;
  },
  ownAddress?: string,
): readonly { readonly email: string; readonly displayName: string | undefined }[] {
  const own = ownAddress?.trim().toLowerCase();
  const out: { email: string; displayName: string | undefined }[] = [];
  const seen = new Set<string>();

  for (const list of [message.from, message.to, message.cc]) {
    if (list === null || list === undefined) continue;
    for (const address of list) {
      const email = address.email.trim().toLowerCase();
      if (email === "" || email === own) continue;
      if (!isValidEmail(email)) continue;
      // One sighting per address per message: a message addressed to the same
      // person in To and Cc is one interaction, not two.
      if (seen.has(email)) continue;
      seen.add(email);
      const name = address.name?.trim();
      out.push({ email, displayName: name === undefined || name === "" ? undefined : name });
    }
  }
  return out;
}

/**
 * Orders the index: most-used first, most-recent to break ties.
 *
 * The final tie-break is the address itself, so the order is TOTAL. Two
 * addresses with identical counts and timestamps would otherwise sort
 * differently depending on the store's iteration order, and a suggestion list
 * that reshuffles between keystrokes is one nobody can click.
 */
export function rankAddresses(
  addresses: readonly IndexedAddress[],
): readonly IndexedAddress[] {
  return [...addresses].sort((a, b) => {
    if (a.timesSeen !== b.timesSeen) return b.timesSeen - a.timesSeen;
    if (a.lastSeenAt !== b.lastSeenAt) return b.lastSeenAt - a.lastSeenAt;
    return a.email.localeCompare(b.email);
  });
}

/**
 * Folds a string for comparison: lowercase, accents removed.
 *
 * "Gomez" must find "Gómez". A Spanish-speaking user typing on a keyboard
 * without dead keys — or simply in a hurry — types the unaccented form, and an
 * autocomplete that refuses it is one that looks broken precisely for the
 * people whose names carry the accents. `NFD` splits the base letter from its
 * mark and the range strips the marks; this is the same normalisation the
 * server does with `unaccent` on the search side, kept consistent on purpose.
 */
export function foldForMatch(value: string): string {
  return value
    .normalize("NFD")
    .replace(/[̀-ͯ]/gu, "")
    .toLowerCase();
}

/**
 * True when `query` should suggest this address.
 *
 * The rule, in order of what a person expects:
 *
 *   - a match anywhere in the ADDRESS (`gom` finds `a.gomez@x.com`), because
 *     people remember fragments of addresses, especially the domain;
 *   - a match at the start of the display name OR of any of its WORDS
 *     (`gom` finds "Ana Gómez"; `ana` finds it too), because a name is a set of
 *     words and nobody thinks of a surname as being "in the middle" of one.
 *
 * Substring-anywhere on the display name is deliberately NOT the rule: it
 * matches "man" against "Fernanda Mansilla" and fills the list with entries the
 * user cannot see the reason for. Word-prefix is the behaviour that reads as
 * intelligent; substring reads as random.
 */
export function matchesQuery(address: IndexedAddress, query: string): boolean {
  const needle = foldForMatch(query.trim());
  if (needle === "") return false;

  if (foldForMatch(address.email).includes(needle)) return true;

  const name = address.displayName;
  if (name === undefined) return false;
  const folded = foldForMatch(name);
  if (folded.startsWith(needle)) return true;
  // Any word of the name, so a surname is reachable without typing the first
  // name. Split on whitespace and the punctuation names actually contain.
  return folded.split(/[\s,._-]+/u).some((word) => word !== "" && word.startsWith(needle));
}

/** How many suggestions the field offers at once. */
export const SUGGESTION_LIMIT = 6;

/**
 * The suggestions for a query: matching, ranked, capped.
 *
 * Capped at {@link SUGGESTION_LIMIT} because a dropdown taller than the
 * composer's recipient row covers the message being written, and because a list
 * of thirty is one nobody reads — they retype instead. Six is what fits without
 * the popup becoming the page.
 *
 * `exclude` removes addresses already chipped in the SAME field: offering to
 * add a recipient who is already there is a dead row that pushes a useful one
 * off the bottom of the list.
 */
export function suggestAddresses(
  index: readonly IndexedAddress[],
  query: string,
  exclude: readonly string[] = [],
  limit: number = SUGGESTION_LIMIT,
): readonly IndexedAddress[] {
  if (query.trim() === "") return [];
  const taken = new Set(exclude.map((address) => address.trim().toLowerCase()));
  const matched = index.filter(
    (address) => !taken.has(address.email) && matchesQuery(address, query),
  );
  return rankAddresses(matched).slice(0, limit);
}
