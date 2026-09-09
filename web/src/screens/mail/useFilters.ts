import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import type { JmapClient, JmapSession } from "../../api/jmap";
import {
  activateManagedScript,
  createFilterRule,
  createFilterRules,
  createForwardingAddress,
  destroyFilterRules,
  destroyForwardingAddress,
  e6Capabilities,
  fetchFilters,
  fetchForwardingAddresses,
  fetchQuota,
  fetchSieveScripts,
  fetchVacation,
  forwardingVerifyUrl,
  MANAGED_SCRIPT_NAME,
  persistRuleOrder,
  reorderRules,
  saveForwardAll,
  saveVacation,
  updateFilterRule,
  type E6Capabilities,
  type FilterConfig,
  type FilterRule,
  type FilterRuleDraft,
  type ForwardAll,
  type ForwardingAddress,
  type Quota,
  type VacationResponse,
} from "../../mail/filters";
import { EMPTY_VACATION } from "../../mail/filters";
import { firstFailureMessage, hasFailures, type SetOutcome } from "../../mail/write";

/**
 * The E6 controller: filters, blocked senders, forwarding, vacation and quota
 * (L3 epic E6).
 *
 * # Why one hook for five surfaces
 *
 * Because four of them are ONE Sieve script on the server, and the fifth is
 * read at the same moment. Blocking a sender writes a `FilterRule`; removing a
 * forwarding address can be refused BECAUSE a filter redirects to it; the
 * forward picker in the filter builder lists rows from `ForwardingAddress/get`.
 * Splitting them into five hooks would mean five caches of the same script that
 * have to be invalidated together — which is one cache with extra steps and
 * more places to forget.
 *
 * # No polling, and no `/changes`
 *
 * The vendor surface has no `/changes` methods, by contract:
 *
 * > No /changes methods exist on this surface, by contract rather than
 * > omission: the capability is Moov's own, its one client is Moov's UI, and
 * > the SSE StateChange plus a cheap /get is the refresh path.
 *
 * So every write here RE-READS, and nothing is optimistic. That is a deliberate
 * departure from the mail list's optimistic-with-rollback idiom, and the reason
 * is that these writes regenerate and re-push an entire Sieve script: the
 * server may normalize, reorder or refuse the whole batch, and a UI showing a
 * predicted result would be showing a script that does not exist. A settings
 * write is also a once-in-a-while action where 200 ms of honesty costs nothing,
 * unlike archiving a message.
 *
 * # Feature detection gates each section separately
 *
 * `session.go` puts the filter, vacation and quota capabilities behind three
 * independent config fields, so this hook reports three independent booleans
 * and the settings sheet renders a skeleton for each absent one.
 */

export interface FiltersApi {
  readonly capabilities: E6Capabilities;

  // --- filters and blocked senders (one surface) ---
  readonly rules: readonly FilterRule[];
  readonly scriptActive: boolean;
  readonly createRule: (draft: FilterRuleDraft) => void;
  /**
   * F-42: applies an imported rule set, appended to whatever is there.
   *
   * APPENDED rather than replacing: destroying the user's existing filters
   * because they imported a file is a destructive action nobody asked for, and
   * the export names no account it came from. What the user gets is both sets,
   * with the imported ones at the end — which they can then reorder, since
   * order is the one thing this surface makes explicit.
   */
  readonly importRules: (drafts: readonly FilterRuleDraft[]) => void;
  readonly updateRule: (id: string, draft: FilterRuleDraft) => void;
  readonly deleteRule: (rule: FilterRule) => void;
  readonly moveRule: (id: string, direction: "up" | "down") => void;
  /** Activates the Moov script; undefined when the server has no Sieve. */
  readonly activate: (() => void) | undefined;
  readonly isActivating: boolean;

  // --- forwarding ---
  readonly forwardingAddresses: readonly ForwardingAddress[];
  readonly forwardAll: ForwardAll;
  readonly addForwardingAddress: (email: string) => Promise<boolean>;
  readonly verifyForwarding: (token: string) => Promise<boolean>;
  readonly removeForwardingAddress: (address: ForwardingAddress) => void;
  readonly saveForwarding: (patch: Partial<ForwardAll>) => void;

  // --- vacation ---
  readonly vacation: VacationResponse;
  readonly saveVacationResponse: (
    patch: Partial<Omit<VacationResponse, "htmlBody">>,
  ) => Promise<boolean>;

  // --- quota ---
  readonly quotas: readonly Quota[] | undefined;
  readonly refreshQuota: () => void;

  readonly isBusy: boolean;
  readonly error: string | undefined;
  readonly quotaError: string | undefined;
}

export interface UseFiltersOptions {
  readonly client: JmapClient | undefined;
  readonly session: JmapSession | undefined;
  readonly accountId: string | undefined;
  /**
   * An authenticated GET, for the forwarding-verify aux route.
   *
   * Passed in rather than derived here because `JmapClient` exposes no generic
   * authenticated fetch, and adding one to it for a single route would widen a
   * class whose narrowness is the point (one client, one credential, an
   * enumerated set of endpoints).
   */
  readonly authedFetch: ((url: string) => Promise<Response>) | undefined;
}

const NO_FORWARD_ALL: ForwardAll = { enabled: false, address: null, disposition: "keep" };

export function useFilters({
  client,
  session,
  accountId,
  authedFetch,
}: UseFiltersOptions): FiltersApi {
  const capabilities = useMemo(
    () => e6Capabilities(session, accountId ?? ""),
    [session, accountId],
  );

  const [config, setConfig] = useState<FilterConfig | undefined>(undefined);
  const [addresses, setAddresses] = useState<readonly ForwardingAddress[]>([]);
  const [vacation, setVacation] = useState<VacationResponse>(EMPTY_VACATION);
  const [quotas, setQuotas] = useState<readonly Quota[] | undefined>(undefined);
  const [isBusy, setBusy] = useState(false);
  const [isActivating, setActivating] = useState(false);
  const [error, setError] = useState<string | undefined>(undefined);
  const [quotaError, setQuotaError] = useState<string | undefined>(undefined);

  /*
   * A generation counter, not an AbortController per call.
   *
   * The reads here are cheap and infrequent; what actually goes wrong is an
   * older response landing after a newer one and reinstating stale rules (the
   * classic settings-panel flicker). Comparing a generation at the moment of
   * commit is the smallest thing that makes that unrepresentable.
   */
  const generation = useRef(0);

  /**
   * Re-reads every surface this session has.
   *
   * `clearError` is false when the reload FOLLOWS a refused write, and that
   * distinction is not a nicety: a refusal is re-read immediately (so the list
   * shows what the server actually has), and a reload that cleared the error
   * unconditionally would wipe the server's sentence before the user could
   * read it — the write would look like it silently did nothing. Caught by a
   * test, which is why the flag exists rather than a comment.
   */
  const reload = useCallback(
    async (clearError = true): Promise<void> => {
      if (client === undefined || accountId === undefined) return;
      const mine = ++generation.current;
      try {
        if (capabilities.filters) {
          const [next, rows] = await Promise.all([
            fetchFilters(client, accountId),
            fetchForwardingAddresses(client, accountId),
          ]);
          if (generation.current !== mine) return;
          setConfig(next);
          setAddresses(rows);
        }
        if (capabilities.vacation) {
          const { vacation: next } = await fetchVacation(client, accountId);
          if (generation.current !== mine) return;
          setVacation(next);
        }
        if (generation.current === mine && clearError) setError(undefined);
      } catch (cause) {
        if (generation.current !== mine) return;
        // A READ failure always wins: it is the newer, more relevant fact.
        setError(cause instanceof Error ? cause.message : String(cause));
      }
    },
    [client, accountId, capabilities.filters, capabilities.vacation],
  );

  useEffect(() => {
    void reload();
  }, [reload]);

  const refreshQuota = useCallback((): void => {
    if (client === undefined || accountId === undefined || !capabilities.quota) return;
    void fetchQuota(client, accountId)
      .then((next) => {
        setQuotas(next);
        setQuotaError(undefined);
      })
      .catch((cause: unknown) => {
        // The quota is read live over IMAP and can fail while everything else
        // works; it gets its OWN error so a failed read does not blank the
        // filter list.
        setQuotaError(cause instanceof Error ? cause.message : String(cause));
      });
  }, [client, accountId, capabilities.quota]);

  /**
   * Runs a write and re-reads, reporting the server's own refusal sentence.
   *
   * `firstFailureMessage` prefers the server's `description`, which is the
   * whole reason the per-record errors are surfaced at all: "the address is
   * still used by a filter or the forwarding setting; remove that first" names
   * the fix, and no wording invented here could.
   */
  const run = useCallback(
    async (write: () => Promise<SetOutcome>): Promise<boolean> => {
      setBusy(true);
      try {
        const outcome = await write();
        if (hasFailures(outcome)) {
          setError(firstFailureMessage(outcome));
          // Re-read WITHOUT clearing: the list must show what the server has,
          // and the refusal must stay on screen while it does.
          await reload(false);
          return false;
        }
        setError(undefined);
        await reload();
        return true;
      } catch (cause) {
        setError(cause instanceof Error ? cause.message : String(cause));
        return false;
      } finally {
        setBusy(false);
      }
    },
    [reload],
  );

  const rules = config?.rules ?? [];

  const createRule = useCallback(
    (draft: FilterRuleDraft): void => {
      if (client === undefined || accountId === undefined) return;
      void run(() => createFilterRule(client, accountId, draft));
    },
    [client, accountId, run],
  );

  const importRules = useCallback(
    (drafts: readonly FilterRuleDraft[]): void => {
      if (client === undefined || accountId === undefined) return;
      if (drafts.length === 0) return;
      // ONE /set, so the import is atomic — see `createFilterRules`.
      void run(() => createFilterRules(client, accountId, drafts));
    },
    [client, accountId, run],
  );

  const updateRule = useCallback(
    (id: string, draft: FilterRuleDraft): void => {
      if (client === undefined || accountId === undefined) return;
      void run(() => updateFilterRule(client, accountId, id, draft));
    },
    [client, accountId, run],
  );

  const deleteRule = useCallback(
    (rule: FilterRule): void => {
      if (client === undefined || accountId === undefined) return;
      void run(() => destroyFilterRules(client, accountId, [rule.id]));
    },
    [client, accountId, run],
  );

  const moveRule = useCallback(
    (id: string, direction: "up" | "down"): void => {
      if (client === undefined || accountId === undefined) return;
      const before = config?.rules ?? [];
      const after = reorderRules(before, id, direction);
      // Identity means the move was a no-op at either end; the pure function
      // returns the same array precisely so this check can skip the request.
      if (after === before) return;
      void run(() => persistRuleOrder(client, accountId, before, after));
    },
    [client, accountId, config, run],
  );

  const activate = useCallback((): void => {
    if (client === undefined || accountId === undefined) return;
    setActivating(true);
    void (async () => {
      try {
        /*
         * The script's id is looked up rather than remembered: SieveScript ids
         * are the ledger's, not ours, and the managed script may not have
         * existed the last time this account was read (it is created on the
         * first push of a rule or a vacation response).
         */
        const scripts = await fetchSieveScripts(client, accountId);
        const managed = scripts.find((script) => script.name === MANAGED_SCRIPT_NAME);
        if (managed === undefined) {
          setError(undefined);
          return;
        }
        const outcome = await activateManagedScript(client, accountId, managed.id);
        const failed = hasFailures(outcome);
        if (failed) setError(firstFailureMessage(outcome));
        else setError(undefined);
        await reload(!failed);
      } catch (cause) {
        setError(cause instanceof Error ? cause.message : String(cause));
      } finally {
        setActivating(false);
      }
    })();
  }, [client, accountId, reload]);

  const addForwardingAddress = useCallback(
    async (email: string): Promise<boolean> => {
      if (client === undefined || accountId === undefined) return false;
      return await run(() => createForwardingAddress(client, accountId, email));
    },
    [client, accountId, run],
  );

  const removeForwardingAddress = useCallback(
    (address: ForwardingAddress): void => {
      if (client === undefined || accountId === undefined) return;
      void run(() => destroyForwardingAddress(client, accountId, address.id));
    },
    [client, accountId, run],
  );

  const verifyForwarding = useCallback(
    async (token: string): Promise<boolean> => {
      if (authedFetch === undefined) return false;
      setBusy(true);
      try {
        const response = await authedFetch(forwardingVerifyUrl(token));
        if (!response.ok) return false;
        await reload();
        return true;
      } catch {
        // The route answers ONE refusal for every internal reason, so there is
        // nothing to classify here either — a failed verification is a failed
        // verification, and the UI says exactly that.
        return false;
      } finally {
        setBusy(false);
      }
    },
    [authedFetch, reload],
  );

  const saveForwarding = useCallback(
    (patch: Partial<ForwardAll>): void => {
      if (client === undefined || accountId === undefined) return;
      void run(() => saveForwardAll(client, accountId, patch));
    },
    [client, accountId, run],
  );

  const saveVacationResponse = useCallback(
    async (patch: Partial<Omit<VacationResponse, "htmlBody">>): Promise<boolean> => {
      if (client === undefined || accountId === undefined) return false;
      return await run(() => saveVacation(client, accountId, patch));
    },
    [client, accountId, run],
  );

  return {
    capabilities,
    rules,
    // Absent config means "not loaded yet", and the banner must NOT fire on an
    // unknown — the same fail-quiet polarity `fetchFilters` applies to a server
    // that omits the property.
    scriptActive: config?.scriptActive ?? true,
    createRule,
    importRules,
    updateRule,
    deleteRule,
    moveRule,
    activate: capabilities.sieve ? activate : undefined,
    isActivating,
    forwardingAddresses: addresses,
    forwardAll: config?.forwardAll ?? NO_FORWARD_ALL,
    addForwardingAddress,
    verifyForwarding,
    removeForwardingAddress,
    saveForwarding,
    vacation,
    saveVacationResponse,
    quotas,
    refreshQuota,
    isBusy,
    error,
    quotaError,
  };
}
