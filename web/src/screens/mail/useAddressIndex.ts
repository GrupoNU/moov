import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { addressesFromMessage, type IndexedAddress } from "../../mail/addressIndex";
import { loadAddressAutocomplete, saveAddressAutocomplete } from "../../mail/addressPrefs";
import type { Email, EmailAddress } from "../../mail/types";
import { useOffline } from "../../offline/OfflineProvider";

/**
 * The address index, as the mail screen uses it (L3 E7; canon §2.3).
 *
 * # The two feeds, and why both are needed
 *
 * Gmail's "Other contacts" fills itself from the mail you send. Ours fills from
 * two places, because a webmail that has just been signed into for the first
 * time would otherwise suggest nothing for days:
 *
 *   1. **Write-through from every message the app loads.** The same discipline
 *      `cache.ts` uses for headers — browsing IS the sync. It costs nothing: the
 *      messages are already in memory, and the write is fire-and-forget.
 *   2. **One bounded scan of Sent**, run once per browser. This is the feed that
 *      matches Gmail's model most closely (the addresses you have MAILED are the
 *      ones you will mail again), and it is what makes autocomplete useful on
 *      the first day rather than the third.
 *
 * # Why the scan is bounded, and bounded twice
 *
 * {@link SCAN_CAP} messages, in {@link SCAN_PAGE}-sized pages, newest first,
 * with the loop counting its own iterations. A mailbox in this pilot holds
 * 26,869 messages; a scan that walked all of them would issue hundreds of
 * requests against a server whose `maxConcurrentRequests` is 8, on the first
 * screen after sign-in, to learn about correspondents from four years ago that
 * rank below everyone recent anyway.
 *
 * The iteration guard is not redundant with the cap. A server that returns a
 * short page, or repeats a position, would otherwise spin forever; the counter
 * is what makes the loop terminate on the server's behaviour rather than on its
 * good manners. That is the bounded-loop discipline this codebase applies to
 * every paged read.
 *
 * # The opt-out really opts out
 *
 * When it is off, the write-through stops (nothing is recorded) AND no
 * suggestions are produced. It does not merely hide the popup: a setting
 * described as being about saving addresses has to stop saving addresses, or
 * the description is false.
 */

/** How many Sent messages the first-run scan reads at most. */
export const SCAN_CAP = 1000;

/** How many per page. Matches the app's other windowed reads. */
export const SCAN_PAGE = 200;

/** Remembers that the scan has run, so it is once per browser and not per load. */
const SCAN_DONE_KEY = "moov.addressScan.v1";

export interface AddressIndexApi {
  /** What the composer completes from. Empty when opted out or unavailable. */
  readonly suggestions: readonly IndexedAddress[];
  /** Whether auto-saving is on. */
  readonly enabled: boolean;
  setEnabled: (enabled: boolean) => void;
  /** Feeds the index from messages the app just loaded. A no-op when off. */
  record: (emails: readonly Email[]) => void;
  /** Feeds it from the recipients of a message that was just SENT. */
  recordSent: (addresses: readonly EmailAddress[]) => void;
  /** Erases the stored addresses — the opt-out's other half. */
  clear: () => Promise<void>;
  /** How many are stored, for the settings row's honest disclosure. */
  readonly count: number;
}

export interface AddressIndexOptions {
  /** The signed-in address, kept out of its own suggestions. */
  readonly ownAddress: string | undefined;
  /** The Sent mailbox, when there is one. Absent skips the scan. */
  readonly sentMailboxId: string | undefined;
  /**
   * Reads one page of Sent, newest first, returning the messages and whether
   * there are more.
   *
   * Injected rather than called directly so this hook does not depend on the
   * JMAP client, which keeps it testable and keeps the request shape owned by
   * the screen that already knows how to ask.
   */
  readonly fetchSentPage?:
    | ((mailboxId: string, position: number, limit: number) => Promise<readonly Email[]>)
    | undefined;
}

export function useAddressIndex({
  ownAddress,
  sentMailboxId,
  fetchSentPage,
}: AddressIndexOptions): AddressIndexApi {
  const { addresses: store } = useOffline();
  const [enabled, setEnabledState] = useState<boolean>(() => loadAddressAutocomplete());
  const [suggestions, setSuggestions] = useState<readonly IndexedAddress[]>([]);

  /*
   * The own address, read through a ref.
   *
   * `record` is handed to effects that fire on every list render, and making it
   * change identity when the identity resolves would re-run them. A ref states
   * "read the latest" without becoming a dependency.
   */
  const ownRef = useRef(ownAddress);
  ownRef.current = ownAddress;

  /** Re-reads the stored index into state. */
  const refresh = useCallback(async (): Promise<void> => {
    if (store === undefined) {
      setSuggestions([]);
      return;
    }
    setSuggestions(await store.all());
  }, [store]);

  // Load once the store exists. Without a store this resolves to empty, which
  // is the app's supported no-storage mode.
  useEffect(() => {
    void refresh();
  }, [refresh]);

  const record = useCallback(
    (emails: readonly Email[]): void => {
      if (!enabled || store === undefined || emails.length === 0) return;

      const sightings = emails.flatMap((email) =>
        addressesFromMessage(email, ownRef.current),
      );
      if (sightings.length === 0) return;

      /*
       * Fire-and-forget, for the reason `OfflineProvider` gives for its own
       * write-through helpers: this runs on the path that paints the message
       * list, and an IndexedDB transaction has no business on it. A dropped
       * write costs one sighting out of thousands.
       *
       * The refresh AFTER the write is what makes a newly-seen address
       * completable in the same session rather than after a reload.
       */
      void store.record(sightings, "browsed").then(refresh);
    },
    [enabled, store, refresh],
  );

  const recordSent = useCallback(
    (recipients: readonly EmailAddress[]): void => {
      if (!enabled || store === undefined || recipients.length === 0) return;
      const sightings = addressesFromMessage({ to: recipients }, ownRef.current);
      if (sightings.length === 0) return;
      // `sent` rather than `browsed`: this is the Gmail-model feed, and the
      // source is what the settings row can honestly say these came from.
      void store.record(sightings, "sent").then(refresh);
    },
    [enabled, store, refresh],
  );

  const setEnabled = useCallback((next: boolean): void => {
    setEnabledState(next);
    saveAddressAutocomplete(next);
  }, []);

  const clear = useCallback(async (): Promise<void> => {
    if (store === undefined) return;
    await store.clear();
    await refresh();
  }, [store, refresh]);

  /**
   * The first-run scan of Sent.
   *
   * Guarded four ways, because this is the one thing in E7 that issues requests
   * the user did not ask for: it needs the feature ON, a store, a Sent mailbox,
   * a fetcher, and a browser that has not already done it.
   */
  useEffect(() => {
    if (!enabled || store === undefined || sentMailboxId === undefined) return;
    if (fetchSentPage === undefined) return;

    let done = false;
    try {
      done = globalThis.localStorage?.getItem(SCAN_DONE_KEY) === "1";
    } catch {
      // A blocked storage means the scan may run again on the next load. That
      // is wasteful, not wrong: `record` merges rather than duplicating.
    }
    if (done) return;

    let cancelled = false;

    void (async () => {
      let position = 0;
      /*
       * The iteration guard. `SCAN_CAP / SCAN_PAGE` is the number of pages the
       * cap allows; counting them means a server that returns a full page
       * forever, or repeats a position, still terminates.
       */
      const maxPages = Math.ceil(SCAN_CAP / SCAN_PAGE);

      for (let page = 0; page < maxPages; page += 1) {
        if (cancelled) return;
        let emails: readonly Email[];
        try {
          emails = await fetchSentPage(sentMailboxId, position, SCAN_PAGE);
        } catch {
          // A failed scan is not an error the user needs to see: autocomplete
          // simply has less to work with, and the write-through feed keeps
          // filling it. Stop rather than retry — a loop that retries on failure
          // is the loop that hammers a server having a bad day.
          return;
        }
        if (emails.length === 0) break;

        const sightings = emails.flatMap((email) =>
          addressesFromMessage(email, ownRef.current),
        );
        if (sightings.length > 0) await store.record(sightings, "sent");

        // A short page means the mailbox is exhausted.
        if (emails.length < SCAN_PAGE) break;
        position += emails.length;
      }

      if (cancelled) return;
      try {
        globalThis.localStorage?.setItem(SCAN_DONE_KEY, "1");
      } catch {
        // See above: at worst it runs again and merges.
      }
      await refresh();
    })();

    return () => {
      cancelled = true;
    };
  }, [enabled, store, sentMailboxId, fetchSentPage, refresh]);

  return useMemo<AddressIndexApi>(
    () => ({
      // The opt-out hides the suggestions as well as stopping the feed: a
      // switched-off feature must not keep completing from what it collected
      // before it was switched off.
      suggestions: enabled ? suggestions : [],
      enabled,
      setEnabled,
      record,
      recordSent,
      clear,
      count: suggestions.length,
    }),
    [enabled, suggestions, setEnabled, record, recordSent, clear],
  );
}
