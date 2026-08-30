import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import type { JmapClient } from "../../api/jmap";
import { queryEmails } from "../../mail/api";
import {
  encodeLabelKeyword,
  labelBudget,
  type LabelBudget,
} from "../../mail/labels";
import {
  deriveLabels,
  loadLabelState,
  renamedLabel,
  saveLabelState,
  withLabel,
  withoutLabel,
  type Label,
  type LabelMetadata,
  type LabelState,
  type LabelVisibility,
} from "../../mail/labelStore";
import {
  MIGRATE_BATCH_SIZE,
  shouldContinueMigration,
  summarizeMigration,
  type MigrateResult,
  type MigrateRound,
} from "../../mail/migrateKeyword";
import { firstFailureMessage, setKeywords } from "../../mail/write";
import type { Email } from "../../mail/types";

/**
 * The label controller (L3 epic E8).
 *
 * # What it owns
 *
 * The local metadata (colour, sidebar visibility) and the two operations that
 * are DATA MIGRATIONS rather than writes — rename and delete. Everything else
 * about labels is already expressible with machinery that exists: applying one
 * is an optimistic `label` action through `useMessageActions`, and viewing one
 * is a `hasKeyword` filter through `queryEmails`.
 *
 * # Why rename and delete live here and not in the settings sheet
 *
 * They need a JMAP client, they run for up to forty rounds, and they must
 * survive the settings dialog being closed mid-flight — a user who starts a
 * rename over 4,000 messages and closes the sheet has not cancelled it. Putting
 * the loop in the component that renders the button would tie the migration's
 * lifetime to that component's, which is how a half-migrated label happens.
 */

export interface LabelsApi {
  readonly labels: readonly Label[];
  readonly budget: LabelBudget;
  readonly create: (name: string, colorId: string) => void;
  readonly setColor: (label: Label, colorId: string) => void;
  readonly setVisibility: (label: Label, visibility: LabelVisibility) => void;
  readonly rename: (label: Label, newName: string) => Promise<MigrateResult>;
  readonly remove: (label: Label) => Promise<MigrateResult>;
  /** True while a migration is running. */
  readonly isMigrating: boolean;
  /** How many messages the running migration has updated so far. */
  readonly migratedCount: number;
  /** Stops the running migration between rounds. */
  readonly abort: () => void;
}

export interface UseLabelsOptions {
  readonly client: JmapClient | undefined;
  readonly accountId: string;
  /** The messages currently loaded — where labels are DISCOVERED from. */
  readonly emails: readonly Email[];
  /** Called after a migration so the list re-reads the changed messages. */
  readonly onChanged: () => void;
}

export function useLabels({
  client,
  accountId,
  emails,
  onChanged,
}: UseLabelsOptions): LabelsApi {
  const [state, setState] = useState<LabelState>(() => loadLabelState());
  const [isMigrating, setMigrating] = useState(false);
  const [migratedCount, setMigratedCount] = useState(0);
  const abortRef = useRef(false);

  // Every state change is persisted immediately: the alternative is a save on
  // unmount, and a tab closed with the X never unmounts cleanly.
  useEffect(() => {
    saveLabelState(state);
  }, [state]);

  /**
   * Every keyword seen on the loaded messages.
   *
   * This is what makes label discovery work at all — a label created in
   * Bulwark, or applied by a Sieve rule, has no record on this browser and
   * would otherwise be invisible. It is also the input to the budget, which
   * must count the keywords OTHER clients set (`$Forwarded`, `NonJunk`)
   * because they spend from the same 26.
   *
   * It is bounded by what is loaded, and that is a real limitation stated
   * rather than hidden: a label that exists only on messages outside the
   * current window is not discovered, so the budget can UNDERSTATE how full the
   * folder is. The server enforces the true ceiling regardless
   * (`checkKeywordCeiling`), so the failure mode is an honest server refusal,
   * never a silently lost label.
   */
  const observedKeywords = useMemo<readonly string[]>(() => {
    const seen = new Set<string>();
    for (const email of emails) {
      for (const [keyword, value] of Object.entries(email.keywords ?? {})) {
        if (value) seen.add(keyword);
      }
    }
    for (const keyword of state.known) seen.add(keyword);
    return [...seen];
  }, [emails, state.known]);

  const labels = useMemo(() => deriveLabels(observedKeywords, state), [observedKeywords, state]);

  const budget = useMemo(
    () =>
      labelBudget(
        observedKeywords,
        labels.map((label) => label.keyword),
      ),
    [observedKeywords, labels],
  );

  const create = useCallback((name: string, colorId: string): void => {
    const keyword = encodeLabelKeyword(name);
    const metadata: LabelMetadata = { colorId, visibility: "show" };
    setState((current) => withLabel(current, keyword, metadata));
  }, []);

  const setColor = useCallback((label: Label, colorId: string): void => {
    setState((current) =>
      withLabel(current, label.keyword, { colorId, visibility: label.visibility }),
    );
  }, []);

  const setVisibility = useCallback((label: Label, visibility: LabelVisibility): void => {
    setState((current) =>
      withLabel(current, label.keyword, { colorId: label.colorId, visibility }),
    );
  }, []);

  const abort = useCallback((): void => {
    abortRef.current = true;
  }, []);

  /**
   * The bounded walk that both rename and delete are.
   *
   * `to === undefined` is a delete: the old keyword is cleared and nothing is
   * added. Otherwise both keys travel in ONE patch per message, so a failure
   * can never leave a message carrying both labels.
   */
  const migrate = useCallback(
    async (from: string, to: string | undefined): Promise<MigrateResult> => {
      if (client === undefined || accountId === "") {
        return summarizeMigration([], { failureMessage: "not connected" });
      }

      abortRef.current = false;
      setMigrating(true);
      setMigratedCount(0);

      const rounds: MigrateRound[] = [];
      let failureMessage: string | undefined;

      try {
        for (;;) {
          if (abortRef.current) break;

          /*
           * "The first N messages that STILL carry the old keyword." Re-queried
           * rather than paged: each successful round removes the keyword from
           * what it touched, so the set shrinks and an offset would skip
           * messages as the list moved underneath it.
           */
          const page = await queryEmails(
            client,
            accountId,
            { kind: "label", keyword: from },
            { limit: MIGRATE_BATCH_SIZE },
          );

          const ids = page.ids;
          if (ids.length === 0) {
            rounds.push({ attempted: 0, migrated: 0 });
            break;
          }

          const patch: Record<string, boolean> = { [from]: false };
          if (to !== undefined) patch[to] = true;

          const outcome = await setKeywords(client, accountId, ids, patch);
          const migratedNow = outcome.updated.length;
          const round: MigrateRound = { attempted: ids.length, migrated: migratedNow };
          rounds.push(round);

          failureMessage ??= firstFailureMessage(outcome);

          setMigratedCount((count) => count + migratedNow);

          if (!shouldContinueMigration(round, rounds.length, abortRef.current)) break;
        }
      } catch (error) {
        failureMessage = error instanceof Error ? error.message : String(error);
      }

      const result = summarizeMigration(rounds, {
        aborted: abortRef.current,
        failureMessage,
      });
      setMigrating(false);
      abortRef.current = false;
      onChanged();
      return result;
    },
    [client, accountId, onChanged],
  );

  const rename = useCallback(
    async (label: Label, newName: string): Promise<MigrateResult> => {
      const to = encodeLabelKeyword(newName);
      const result = await migrate(label.keyword, to);
      /*
       * The local metadata moves even when the migration was INCOMPLETE, and
       * that is deliberate: the new label now exists on some messages, so it
       * must have a colour and a sidebar row. The old keyword survives on the
       * remainder and is re-discovered from those messages as a separate,
       * default-styled label — which is the honest picture of a half-finished
       * rename, and exactly what the "run it again to finish" copy refers to.
       */
      setState((current) => renamedLabel(current, label.keyword, to));
      return result;
    },
    [migrate],
  );

  const remove = useCallback(
    async (label: Label): Promise<MigrateResult> => {
      const result = await migrate(label.keyword, undefined);
      /*
       * Forgotten locally only when the keyword is really gone. Dropping it
       * after a partial run would hide a label that is still on hundreds of
       * messages — the "labels that exist only in the DB, silently" failure of
       * L2 §2.3, in its mirror form.
       */
      if (!result.incomplete) {
        setState((current) => withoutLabel(current, label.keyword));
      }
      return result;
    },
    [migrate],
  );

  return {
    labels,
    budget,
    create,
    setColor,
    setVisibility,
    rename,
    remove,
    isMigrating,
    migratedCount,
    abort,
  };
}
