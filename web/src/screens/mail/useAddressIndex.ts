import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { addressesFromMessage, type IndexedAddress } from "../../mail/addressIndex";
import {
  addressAutocompleteEnabled,
  addressAutocompleteMode,
  loadAddressAutocomplete,
  saveAddressAutocomplete,
} from "../../mail/addressPrefs";
import { usePrefs } from "../../mail/PrefsProvider";
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
  const { prefs, isAvailable, setPref } = usePrefs();
  const [suggestions, setSuggestions] = useState<readonly IndexedAddress[]>([]);

  /*
   * E5 v2: the choice is `Prefs.addressAutocomplete` and it ROAMS. The local
   * state answers only until prefs resolve, and permanently on a server that
   * does not advertise the capability.
   *
   * The pre-load answer is the MIRROR rather than the default, and that
   * direction is load-bearing: the feeds below fire on the first render, and
   * defaulting to "collect" for that window would gather addresses from a user
   * who opted out — the exact failure this setting exists to prevent. Erring
   * toward the last known choice makes the worst case "a few sightings missed
   * on one boot" instead of "a privacy setting silently ignored on every boot".
   *
   * The local state is kept even though prefs win when available, because
   * without it the switch would be DEAD on an older server: `setPref` there is
   * a no-op that resolves false (the provider says so), so nothing would
   * re-render and the toggle would not move. P4 — never a control that does
   * nothing — applies to the degraded case too.
   */
  const [localEnabled, setLocalEnabled] = useState<boolean>(() => loadAddressAutocomplete());
  const enabled = isAvailable
    ? addressAutocompleteEnabled(prefs.addressAutocomplete)
    : localEnabled;

  /*
   * The own address, read through a ref.
   *
   * `record` is handed to effects that fire on every list render, and making it
   * change identity when the identity resolves would re-run them. A ref states
   * "read the latest" without becoming a dependency.
   */
  const ownRef = useRef(ownAddress);
  ownRef.current = ownAddress;

  /*
   * `enabled` is read through a ref for the SAME reason `ownAddress` is, and
   * the reason became load-bearing when the value started coming from prefs.
   *
   * It used to be plain `useState`, settled before the first paint, so listing
   * it as a dependency of `record` was harmless. Now it can change one render
   * after mount — the provider resolves and the account's answer replaces the
   * mirror's — which gave `record` a new identity mid-flight and re-ran the
   * write-through effect that holds it. That effect fires on the path that
   * paints the message list, so a re-run means the same window recorded twice
   * and, worse, a `refresh()` from the superseded call landing after the newer
   * one and overwriting the suggestions with a staler read.
   *
   * A ref states "read the latest" without becoming a dependency, which is
   * exactly the property the feeds need: they must observe the CURRENT choice
   * at the moment they fire, not re-subscribe every time it is re-derived.
   */
  const enabledRef = useRef(enabled);
  enabledRef.current = enabled;

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
      if (!enabledRef.current || store === undefined || emails.length === 0) return;

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
    [store, refresh],
  );

  const recordSent = useCallback(
    (recipients: readonly EmailAddress[]): void => {
      // Through the ref, like `record` above and for the same reason: a send
      // must observe the CURRENT choice, not pin a callback identity to it.
      if (!enabledRef.current || store === undefined || recipients.length === 0) return;
      const sightings = addressesFromMessage({ to: recipients }, ownRef.current);
      if (sightings.length === 0) return;
      // `sent` rather than `browsed`: this is the Gmail-model feed, and the
      // source is what the settings row can honestly say these came from.
      void store.record(sightings, "sent").then(refresh);
    },
    [store, refresh],
  );

  const setEnabled = useCallback(
    (next: boolean): void => {
      /*
       * The mirror is written FIRST and unconditionally, then prefs.
       *
       * Not belt-and-braces: the mirror is what the next cold boot reads, and
       * writing it before the round trip means an opt-out survives a reload
       * even if the save fails or the tab closes mid-flight. If the server then
       * refuses, the provider rolls the pref back and the write-through in
       * `App.tsx` restores the mirror from the authoritative value on the next
       * render — so the two converge without this call site owning the repair.
       *
       * `setPref` is optimistic, so the switch moves immediately and the feeds
       * below stop on the same tick; the promise is deliberately not awaited,
       * for the reason the provider gives (paint first, call second).
       */
      setLocalEnabled(next);
      saveAddressAutocomplete(next);
      void setPref("addressAutocomplete", addressAutocompleteMode(next));
    },
    [setPref],
  );

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
