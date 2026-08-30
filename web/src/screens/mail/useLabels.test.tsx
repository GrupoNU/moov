import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { JmapClient } from "../../api/jmap";
import { MAX_MIGRATE_ROUNDS } from "../../mail/migrateKeyword";
import type { Email } from "../../mail/types";
import { useLabels } from "./useLabels";

/**
 * The label controller (E8) — and above all the bounded, abortable migration.
 *
 * A rename or a delete rewrites a keyword on every message that carries it, in
 * rounds of 100 against a 200-row query window. The failure modes are the ones
 * `emptyTrash` was written for and they are worse here, because the loop is
 * driven by a set that a failing server never shrinks: a runaway loop hammering
 * the server from a browser tab, and a partial run reported as a success.
 */

const ACCOUNT = "a";

/**
 * A client whose `Email/query` yields the given pages in order and whose
 * `Email/set` confirms whatever it is given.
 *
 * `pages` is a list of id-arrays: one per round. Running out means "no more
 * matches", which is how a real migration ends.
 */
function scriptedClient(
  pages: readonly (readonly string[])[],
  options: { readonly confirm?: (ids: readonly string[]) => readonly string[] } = {},
): { client: JmapClient; queries: number; sets: string[][] } {
  const state = { queries: 0, sets: [] as string[][] };
  const client = new JmapClient({ username: "u", password: "p" });
  vi.spyOn(client, "call").mockImplementation((invocations) => {
    const calls = invocations as [string, Record<string, unknown>, string][];
    const responses = calls.map(([name, args, id]) => {
      if (name === "Email/query") {
        const ids = pages[state.queries] ?? [];
        state.queries += 1;
        return [name, { ids, queryState: "q", total: ids.length }, id];
      }
      if (name === "Email/get") return [name, { list: [] }, id];
      if (name === "Email/set") {
        const update = args.update as Record<string, unknown>;
        const ids = Object.keys(update);
        state.sets.push(ids);
        const confirmed = options.confirm?.(ids) ?? ids;
        return [
          name,
          { updated: Object.fromEntries(confirmed.map((entry) => [entry, null])) },
          id,
        ];
      }
      return [name, {}, id];
    });
    return Promise.resolve({ methodResponses: responses } as never);
  });
  return {
    client,
    get queries() {
      return state.queries;
    },
    get sets() {
      return state.sets;
    },
  };
}

function setup(
  client: JmapClient | undefined,
  emails: readonly Email[] = [],
  onChanged = vi.fn(),
) {
  return renderHook(() => useLabels({ client, accountId: ACCOUNT, emails, onChanged }));
}

beforeEach(() => {
  globalThis.localStorage.clear();
});

describe("discovery and the budget", () => {
  it("discovers a label from a message's keywords, even one we never created", () => {
    // A label applied in Bulwark or by a Sieve rule has no local record; without
    // discovery it would be invisible here.
    const emails: Email[] = [{ id: "e1", keywords: { $seen: true, "$label:work": true } }];
    const { result } = setup(undefined, emails);
    expect(result.current.labels.map((label) => label.name)).toEqual(["work"]);
  });

  it("charges the semi-system keywords other clients set against the same 26", () => {
    const emails: Email[] = [
      { id: "e1", keywords: { $seen: true, $Forwarded: true, NonJunk: true, "$label:work": true } },
    ];
    const { result } = setup(undefined, emails);
    // $seen is free (Maildir flag field); $Forwarded, NonJunk and the label all
    // take a letter.
    expect(result.current.budget.available).toBe(23);
  });

  it("keeps a created label before any message carries it", () => {
    const { result } = setup(undefined, []);
    act(() => {
      result.current.create("Clientes", "blue");
    });
    expect(result.current.labels.map((label) => label.name)).toEqual(["Clientes"]);
    // Counted as spent the moment it exists — the 27th must be refused BEFORE
    // it is applied and silently lost.
    expect(result.current.budget.available).toBe(25);
  });

  it("persists across a remount, so a new label does not vanish on refresh", () => {
    const first = setup(undefined, []);
    act(() => {
      first.result.current.create("Clientes", "teal");
    });
    first.unmount();

    const second = setup(undefined, []);
    expect(second.result.current.labels[0]?.name).toBe("Clientes");
    expect(second.result.current.labels[0]?.colorId).toBe("teal");
  });

  it("stores the colour and the visibility", () => {
    const { result } = setup(undefined, []);
    act(() => {
      result.current.create("work", "red");
    });
    act(() => {
      const label = result.current.labels[0];
      if (label !== undefined) result.current.setVisibility(label, "showIfUnread");
    });
    expect(result.current.labels[0]?.visibility).toBe("showIfUnread");
    expect(result.current.labels[0]?.colorId).toBe("red");
  });
});

describe("the migration is bounded", () => {
  it("walks page after page until the query comes back empty", async () => {
    const scripted = scriptedClient([["e1", "e2"], ["e3"], []]);
    const { result } = setup(scripted.client, []);

    let outcome;
    await act(async () => {
      outcome = await result.current.rename(
        { keyword: "$label:work", name: "work", colorId: "slate", visibility: "show" },
        "trabajo",
      );
    });

    expect(outcome).toMatchObject({ migrated: 3, incomplete: false, aborted: false });
    // Three queries: two productive rounds and the one that proved emptiness.
    expect(scripted.queries).toBe(3);
  });

  it("STOPS when a round changes nothing, instead of re-failing the same page forever", async () => {
    // The runaway case: a server refusing this set answers identically every
    // time, so the loop must not ask again.
    const scripted = scriptedClient([["e1"], ["e1"], ["e1"]], { confirm: () => [] });
    const { result } = setup(scripted.client, []);

    let outcome;
    await act(async () => {
      outcome = await result.current.remove({
        keyword: "$label:work",
        name: "work",
        colorId: "slate",
        visibility: "show",
      });
    });

    expect(scripted.queries).toBe(1);
    expect(outcome).toMatchObject({ migrated: 0, incomplete: true });
  });

  it("never exceeds the round ceiling even when every round succeeds", async () => {
    // A server that keeps returning the same page AND confirming it would spin
    // forever without this.
    const pages = Array.from({ length: MAX_MIGRATE_ROUNDS + 10 }, () => ["e1"]);
    const scripted = scriptedClient(pages);
    const { result } = setup(scripted.client, []);

    let outcome;
    await act(async () => {
      outcome = await result.current.remove({
        keyword: "$label:work",
        name: "work",
        colorId: "slate",
        visibility: "show",
      });
    });

    expect(scripted.queries).toBeLessThanOrEqual(MAX_MIGRATE_ROUNDS);
    expect(outcome).toMatchObject({ incomplete: true });
  });

  it("reports a transport failure with the server's own words", async () => {
    const client = new JmapClient({ username: "u", password: "p" });
    vi.spyOn(client, "call").mockRejectedValue(new Error("over quota"));
    const { result } = setup(client, []);

    let outcome;
    await act(async () => {
      outcome = await result.current.remove({
        keyword: "$label:work",
        name: "work",
        colorId: "slate",
        visibility: "show",
      });
    });

    expect(outcome).toMatchObject({ failureMessage: "over quota", migrated: 0 });
  });

  it("does nothing at all without a client, rather than claiming success", async () => {
    const { result } = setup(undefined, []);
    let outcome;
    await act(async () => {
      outcome = await result.current.remove({
        keyword: "$label:work",
        name: "work",
        colorId: "slate",
        visibility: "show",
      });
    });
    expect(outcome).toMatchObject({ migrated: 0, failureMessage: "not connected" });
  });
});

describe("the local half follows the remote half honestly", () => {
  it("moves the metadata to the new keyword on rename", async () => {
    const scripted = scriptedClient([["e1"], []]);
    const { result } = setup(scripted.client, []);
    act(() => {
      result.current.create("work", "purple");
    });
    await act(async () => {
      await result.current.rename(
        { keyword: "$label:work", name: "work", colorId: "purple", visibility: "show" },
        "trabajo",
      );
    });
    await waitFor(() => {
      expect(result.current.labels.map((label) => label.name)).toContain("trabajo");
    });
    expect(result.current.labels.find((label) => label.name === "trabajo")?.colorId).toBe(
      "purple",
    );
  });

  it("forgets a deleted label only when the keyword is really gone", async () => {
    const scripted = scriptedClient([["e1"], []]);
    const { result } = setup(scripted.client, []);
    act(() => {
      result.current.create("work", "blue");
    });
    await act(async () => {
      await result.current.remove({
        keyword: "$label:work",
        name: "work",
        colorId: "blue",
        visibility: "show",
      });
    });
    await waitFor(() => {
      expect(result.current.labels).toHaveLength(0);
    });
  });

  it("KEEPS a partially-deleted label, so it is not hidden while still on messages", async () => {
    /*
     * The mirror of L2 §2.3's "no labels that exist only in the DB, silently":
     * dropping it locally after a partial run would hide a label that is still
     * on hundreds of messages.
     */
    /*
     * The run is stopped from INSIDE the first round's `Email/set`, which is
     * where a real abort lands: the user clicks "Detener" while a request is in
     * flight, and the loop notices between rounds. Aborting before `remove` is
     * called would not work and should not — `migrate` clears the flag on
     * entry so a stale abort cannot cancel the next migration.
     */
    // Assigned once, after the hook renders; the closure below reads it later.
    // eslint-disable-next-line prefer-const
    let stop: (() => void) | undefined;
    const scripted = scriptedClient([["e1"], ["e2"], []], {
      confirm: (ids) => {
        stop?.();
        return ids;
      },
    });
    const { result } = setup(scripted.client, []);
    act(() => {
      result.current.create("work", "blue");
    });
    stop = result.current.abort;

    let outcome;
    await act(async () => {
      outcome = await result.current.remove({
        keyword: "$label:work",
        name: "work",
        colorId: "blue",
        visibility: "show",
      });
    });

    expect(outcome).toMatchObject({ aborted: true, incomplete: true });
    expect(result.current.labels.map((label) => label.name)).toContain("work");
  });

  it("tells the host to refetch when the migration ends", async () => {
    const scripted = scriptedClient([[]]);
    const onChanged = vi.fn();
    const { result } = setup(scripted.client, [], onChanged);
    await act(async () => {
      await result.current.remove({
        keyword: "$label:work",
        name: "work",
        colorId: "slate",
        visibility: "show",
      });
    });
    expect(onChanged).toHaveBeenCalled();
  });
});
