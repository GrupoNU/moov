import { describe, expect, it } from "vitest";

import type { JmapClient, JmapResponse, JmapSession } from "../api/jmap";
import {
  CAP_PREFS,
  DEFAULT_PREFS,
  densityMetrics,
  densityVariables,
  DENSITIES,
  fetchPrefs,
  paneLayout,
  parsePrefs,
  PREFS_ID,
  PREFS_V2_KEYS,
  READING_PANES,
  resolveSignature,
  rowHeightFor,
  savePrefs,
  servesPrefsV2,
  servesPrefsV3,
  sessionHasPrefs,
  sortForInboxType,
  UNDO_SEND_SECONDS,
  type Prefs,
} from "./prefs";

/**
 * A JmapClient stand-in.
 *
 * Only `call` is exercised: everything else on the real client is transport
 * the preference methods do not touch, and faking it would be faking code this
 * test is not about.
 */
function fakeClient(
  respond: (methodCalls: readonly unknown[]) => JmapResponse,
): { client: JmapClient; calls: unknown[][] } {
  const calls: unknown[][] = [];
  const client = {
    call: (methodCalls: readonly unknown[], using: readonly string[]) => {
      calls.push([methodCalls, using]);
      return Promise.resolve(respond(methodCalls));
    },
  } as unknown as JmapClient;
  return { client, calls };
}

const FULL_WIRE = {
  id: PREFS_ID,
  undoSendSeconds: 30,
  imagesPolicy: "ask",
  conversationView: false,
  hoverActions: false,
  autoAdvance: "newer",
  density: "compact",
  showSnippets: false,
  keyboardShortcuts: false,
  language: "en",
  readingPane: "bottom",
  inboxType: "unread_first",
  notifications: "new",
  theme: "dark",
  // v2, in the exact shape `prefsObject` renders: the two maps always present
  // (never null), the two id references as String|null.
  labels: { "$label:work": { color: "amber", visibility: "showIfUnread" } },
  offlineDepth: { headersPerMailbox: 500, bodies: 250 },
  addressAutocomplete: "manual",
  sendAndArchive: false,
  defaultReplyBehavior: "replyAll",
  signatures: {
    items: { work: { name: "Work", textBody: "-- \nD", htmlBody: "<p>D</p>" } },
    forNew: "work",
    forReply: null,
  },
  // v3 (P0-5): a flat map keyed by mailbox DISPLAY NAME. Rendered as `{}` when
  // empty rather than null, the same as `labels`, so a client never has to
  // tell "no choices" from "unknown".
  folderVisibility: { Calendario: "hide", Avisos: "showIfUnread" },
};

describe("parsePrefs", () => {
  it("reads every field the server sends", () => {
    expect(parsePrefs(FULL_WIRE)).toEqual({
      undoSendSeconds: 30,
      imagesPolicy: "ask",
      conversationView: false,
      hoverActions: false,
      autoAdvance: "newer",
      density: "compact",
      showSnippets: false,
      keyboardShortcuts: false,
      language: "en",
      readingPane: "bottom",
      inboxType: "unread_first",
      notifications: "new",
      theme: "dark",
      labels: { "$label:work": { color: "amber", visibility: "showIfUnread" } },
      offlineDepth: { headersPerMailbox: 500, bodies: 250 },
      addressAutocomplete: "manual",
      sendAndArchive: false,
      defaultReplyBehavior: "replyAll",
      signatures: {
        items: { work: { name: "Work", textBody: "-- \nD", htmlBody: "<p>D</p>" } },
        forNew: "work",
        forReply: null,
      },
      folderVisibility: { Calendario: "hide", Avisos: "showIfUnread" },
    } satisfies Prefs);
  });

  it("reads null language as follow-the-browser", () => {
    expect(parsePrefs({ ...FULL_WIRE, language: null }).language).toBeNull();
  });

  it("falls back per FIELD, not per object, on a partial response", () => {
    /*
     * The mid-deploy case: a server older than this client omits two keys.
     * Everything else it DID send must survive — the whole point of defaulting
     * field by field rather than discarding the response.
     */
    const partial = { ...FULL_WIRE } as Record<string, unknown>;
    delete partial.density;
    delete partial.theme;

    const parsed = parsePrefs(partial);
    expect(parsed.density).toBe(DEFAULT_PREFS.density);
    expect(parsed.theme).toBe(DEFAULT_PREFS.theme);
    // …and the eleven that did arrive are not lost.
    expect(parsed.readingPane).toBe("bottom");
    expect(parsed.inboxType).toBe("unread_first");
    expect(parsed.undoSendSeconds).toBe(30);
  });

  it("rejects values outside each closed domain", () => {
    const parsed = parsePrefs({
      ...FULL_WIRE,
      density: "enormous",
      undoSendSeconds: 7,
      inboxType: "important_first",
      language: "fr",
    });
    expect(parsed.density).toBe(DEFAULT_PREFS.density);
    expect(parsed.undoSendSeconds).toBe(DEFAULT_PREFS.undoSendSeconds);
    expect(parsed.inboxType).toBe(DEFAULT_PREFS.inboxType);
    // An unknown tag is "follow the browser", not a locale we have no strings
    // for.
    expect(parsed.language).toBeNull();
  });

  it("survives a non-object", () => {
    expect(parsePrefs(null)).toEqual(DEFAULT_PREFS);
    expect(parsePrefs("nope")).toEqual(DEFAULT_PREFS);
    expect(parsePrefs(undefined)).toEqual(DEFAULT_PREFS);
  });
});

describe("sessionHasPrefs — feature detection", () => {
  const withCapabilities = (
    capabilities: Record<string, unknown>,
    accountCapabilities: Record<string, unknown> = {},
  ): JmapSession => ({
    capabilities,
    accounts: { a1: { name: "a", isPersonal: true, isReadOnly: false, accountCapabilities } },
    primaryAccounts: {},
    username: "u",
    apiUrl: "",
    downloadUrl: "",
    uploadUrl: "",
    eventSourceUrl: "",
    state: "",
  });

  it("is false without a session — the pre-load state must never call", () => {
    expect(sessionHasPrefs(undefined)).toBe(false);
  });

  it("is false when the server does not advertise the capability", () => {
    expect(sessionHasPrefs(withCapabilities({ "urn:ietf:params:jmap:mail": {} }), "a1")).toBe(
      false,
    );
  });

  it("is true from the session's top-level capabilities", () => {
    expect(sessionHasPrefs(withCapabilities({ [CAP_PREFS]: {} }), "a1")).toBe(true);
  });

  it("is true from the account's capabilities alone", () => {
    expect(sessionHasPrefs(withCapabilities({}, { [CAP_PREFS]: {} }), "a1")).toBe(true);
  });

  it("does not consult an account it was not given", () => {
    expect(sessionHasPrefs(withCapabilities({}, { [CAP_PREFS]: {} }))).toBe(false);
  });
});

describe("fetchPrefs", () => {
  it("asks for the singleton under the vendor capability", async () => {
    const { client, calls } = fakeClient(() => ({
      methodResponses: [["Prefs/get", { state: "s1", list: [FULL_WIRE] }, "p"]],
      sessionState: "x",
    }) as unknown as JmapResponse);

    const result = await fetchPrefs(client, "a1");

    const [methodCalls, using] = calls[0] as [readonly unknown[], readonly string[]];
    expect(methodCalls).toEqual([["Prefs/get", { accountId: "a1", ids: null }, "p"]]);
    // The vendor capability MUST be in `using`, or a conformant server refuses
    // the method it did not see requested (RFC 8620 §3.3).
    expect(using).toContain(CAP_PREFS);
    expect(result.prefs.theme).toBe("dark");
    expect(result.state).toBe("s1");
  });

  it("throws the server's own words on a method error", async () => {
    const { client } = fakeClient(() => ({
      methodResponses: [
        ["error", { type: "unknownCapability", description: "no such capability" }, "p"],
      ],
      sessionState: "x",
    }) as unknown as JmapResponse);

    await expect(fetchPrefs(client, "a1")).rejects.toThrow(/unknownCapability.*no such capability/);
  });

  it("defaults when the server returns an empty list", async () => {
    const { client } = fakeClient(() => ({
      methodResponses: [["Prefs/get", { state: "s", list: [] }, "p"]],
      sessionState: "x",
    }) as unknown as JmapResponse);
    expect((await fetchPrefs(client, "a1")).prefs).toEqual(DEFAULT_PREFS);
  });
});

describe("savePrefs", () => {
  it("sends only the changed keys, as an update on the singleton", async () => {
    const { client, calls } = fakeClient(() => ({
      methodResponses: [
        ["Prefs/set", { updated: { [PREFS_ID]: null } }, "p"],
        ["Prefs/get", { state: "s2", list: [{ ...FULL_WIRE, density: "comfortable" }] }, "g"],
      ],
      sessionState: "x",
    }) as unknown as JmapResponse);

    const result = await savePrefs(client, "a1", { density: "comfortable" });

    const [methodCalls] = calls[0] as [readonly unknown[]];
    expect(methodCalls[0]).toEqual([
      "Prefs/set",
      { accountId: "a1", update: { [PREFS_ID]: { density: "comfortable" } } },
      "p",
    ]);
    // The read-back rides the SAME request, so the returned state describes
    // the data in hand.
    expect((methodCalls[1] as unknown[])[0]).toBe("Prefs/get");
    expect(result.prefs.density).toBe("comfortable");
    expect(result.state).toBe("s2");
  });

  it("throws when the server refuses the update — the caller must roll back", async () => {
    const { client } = fakeClient(() => ({
      methodResponses: [
        [
          "Prefs/set",
          {
            notUpdated: {
              [PREFS_ID]: {
                type: "invalidProperties",
                properties: ["density"],
                description: "density must be one of default, comfortable, compact",
              },
            },
          },
          "p",
        ],
        ["Prefs/get", { state: "s", list: [FULL_WIRE] }, "g"],
      ],
      sessionState: "x",
    }) as unknown as JmapResponse);

    await expect(
      savePrefs(client, "a1", { density: "enormous" as never }),
    ).rejects.toThrow(/invalidProperties.*density must be one of/);
  });
});

describe("density", () => {
  it("gives every density a distinct row height", () => {
    const heights = DENSITIES.map((density) => rowHeightFor(density));
    expect(new Set(heights).size).toBe(DENSITIES.length);
  });

  it("keeps the default at the height P2 shipped and the stylesheet draws", () => {
    // Changing this is a product decision, not a refactor: it is the number
    // `--row-height` and the virtualizer both start from.
    expect(rowHeightFor("default")).toBe(72);
  });

  it("orders the three densities as their names promise", () => {
    expect(rowHeightFor("compact")).toBeLessThan(rowHeightFor("default"));
    expect(rowHeightFor("default")).toBeLessThan(rowHeightFor("comfortable"));
  });

  it("emits custom properties whose row height matches the metric", () => {
    for (const density of DENSITIES) {
      const vars = densityVariables(density);
      expect(vars["--row-height"]).toBe(`${densityMetrics(density).rowHeight}px`);
      // The other two must exist, or the stylesheet falls back mid-theme.
      expect(vars["--row-padding-x"]).toMatch(/^\d+px$/);
      expect(vars["--row-gap"]).toMatch(/^\d+px$/);
    }
  });
});

describe("sortForInboxType — the polarity the server documents", () => {
  it("sends no sort for the default inbox", () => {
    // The server's own default is newest-first; a redundant comparator pair
    // would route every plain inbox load through the partition path.
    expect(sortForInboxType("default")).toBeUndefined();
  });

  it("puts UNREAD on top with $seen ASCENDING", () => {
    /*
     * query.go: `keywordFirst: !sort[0].ascending()`, and an absent
     * isAscending defaults to true. So ascending:true means "those that LACK
     * the keyword come first" — which for $seen is the unread ones.
     */
    expect(sortForInboxType("unread_first")).toEqual([
      { property: "hasKeyword", keyword: "$seen", isAscending: true },
      { property: "receivedAt", isAscending: false },
    ]);
  });

  it("puts STARRED on top with $flagged DESCENDING", () => {
    // The inverse case, and the reason the two branches are not symmetrical:
    // here we want the messages that HAVE the keyword first.
    expect(sortForInboxType("starred_first")).toEqual([
      { property: "hasKeyword", keyword: "$flagged", isAscending: false },
      { property: "receivedAt", isAscending: false },
    ]);
  });

  it("only ever emits the [hasKeyword, receivedAt] pair the server accepts", () => {
    // translateKeywordSort refuses any other shape with unsupportedSort, which
    // the UI would render as an empty inbox.
    for (const type of ["unread_first", "starred_first"] as const) {
      const sort = sortForInboxType(type);
      expect(sort).toHaveLength(2);
      expect(sort?.[0]?.property).toBe("hasKeyword");
      expect(sort?.[0]?.keyword).toBeTruthy();
      expect(sort?.[1]?.property).toBe("receivedAt");
    }
  });
});

describe("the domains", () => {
  it("offers Gmail's exact undo-send set", () => {
    // Canon §2.3 (/mail/answer/2819488). Not a range, not a slider.
    expect([...UNDO_SEND_SECONDS]).toEqual([5, 10, 20, 30]);
  });

  it("defaults to a value inside every domain it belongs to", () => {
    expect(UNDO_SEND_SECONDS).toContain(DEFAULT_PREFS.undoSendSeconds);
    expect(DENSITIES).toContain(DEFAULT_PREFS.density);
  });

  it("keeps keyboard shortcuts ON — decision D-3, a signed divergence", () => {
    // Gmail's default is OFF; D-3 signed ON. If this ever flips, it is a
    // product decision that must reopen the arbitration, not a tidy-up.
    expect(DEFAULT_PREFS.keyboardShortcuts).toBe(true);
  });

  it("keeps images on 'always' — decision D-4, which the HMAC proxy earns", () => {
    expect(DEFAULT_PREFS.imagesPolicy).toBe("always");
  });
});

describe("no request escapes without the capability", () => {
  it("fetchPrefs and savePrefs both name it in `using`", async () => {
    const respond = () =>
      ({
        methodResponses: [
          ["Prefs/get", { state: "s", list: [FULL_WIRE] }, "p"],
          ["Prefs/set", { updated: {} }, "p"],
          ["Prefs/get", { state: "s", list: [FULL_WIRE] }, "g"],
        ],
        sessionState: "x",
      }) as unknown as JmapResponse;

    const { client, calls } = fakeClient(respond);
    await fetchPrefs(client, "a1");
    await savePrefs(client, "a1", { theme: "dark" });

    // BOTH calls, not just the first: a save that forgot the capability would
    // be refused by a conformant server with unknownCapability.
    expect(calls).toHaveLength(2);
    for (const [, using] of calls as [unknown, readonly string[]][]) {
      expect(using).toContain(CAP_PREFS);
    }
  });
});

/**
 * The reading-pane layout (canon §2.4 — /9499937).
 *
 * This is the pure half of a layout change that jsdom cannot otherwise reach:
 * MailScreen needs auth, a router, a JMAP client and an EventSource before it
 * renders one pixel, so the decision lives here where a test can enumerate it.
 */
describe("paneLayout", () => {
  it("shows only the list when nothing is open, whatever the setting says", () => {
    // "No split" is not a permanently different shell — it is a different
    // answer to "what happens when you open something".
    for (const pane of READING_PANES) {
      const layout = paneLayout(pane, false);
      expect(layout.mode).toBe("list");
      expect(layout.showsReader).toBe(false);
      expect(layout.listHidden).toBe(false);
    }
  });

  it("splits to the right — the layout the PWA shipped with", () => {
    expect(paneLayout("right", true)).toEqual({
      isSplit: true,
      listHidden: false,
      showsReader: true,
      mode: "right",
    });
  });

  it("stacks the reader below the list", () => {
    expect(paneLayout("bottom", true)).toEqual({
      isSplit: true,
      listHidden: false,
      showsReader: true,
      mode: "bottom",
    });
  });

  it("UNMOUNTS the list in 'no split', rather than hiding it", () => {
    /*
     * The invariant, and the reason this function exists as a testable unit: a
     * virtualized list inside a zero-height container measures a viewport of 0
     * and computes a window of nothing. Hiding it with CSS would mean
     * returning to an empty list at a scroll offset that no longer means
     * anything — a bug that looks exactly like a failed fetch.
     */
    const layout = paneLayout("none", true);
    expect(layout.listHidden).toBe(true);
    expect(layout.isSplit).toBe(false);
    expect(layout.showsReader).toBe(true);
    expect(layout.mode).toBe("full");
  });

  it("never hides the list while it is also splitting", () => {
    // The two are contradictory: a split needs both panes on screen.
    for (const pane of READING_PANES) {
      for (const reading of [true, false]) {
        const layout = paneLayout(pane, reading);
        expect(layout.isSplit && layout.listHidden).toBe(false);
      }
    }
  });

  it("always shows at least one pane", () => {
    // A state with no list AND no reader would be a blank shell.
    for (const pane of READING_PANES) {
      for (const reading of [true, false]) {
        const layout = paneLayout(pane, reading);
        expect(layout.showsReader || !layout.listHidden).toBe(true);
      }
    }
  });
});

// ---------------------------------------------------------------------------
// prefs v2 — the six roaming keys
// ---------------------------------------------------------------------------

describe("parsePrefs — the v2 keys", () => {
  it("tolerates their complete absence, which is a v1 server mid-deploy", () => {
    /*
     * The realistic failure this guards: a PWA newer than the moovd it is
     * talking to. Every v2 key must fall back to its own default while the
     * v1 keys the old server DID send survive intact.
     */
    // Built by FILTERING rather than by copy-then-delete: the same discipline
    // `labelStore.withoutLabel` uses, and it avoids a dynamic `delete`.
    const v2 = new Set<string>(PREFS_V2_KEYS);
    const v1 = Object.fromEntries(
      Object.entries(FULL_WIRE).filter(([key]) => !v2.has(key)),
    );

    const parsed = parsePrefs(v1);
    expect(parsed.labels).toEqual({});
    expect(parsed.offlineDepth).toEqual(DEFAULT_PREFS.offlineDepth);
    expect(parsed.addressAutocomplete).toBe(DEFAULT_PREFS.addressAutocomplete);
    expect(parsed.sendAndArchive).toBe(DEFAULT_PREFS.sendAndArchive);
    expect(parsed.defaultReplyBehavior).toBe(DEFAULT_PREFS.defaultReplyBehavior);
    expect(parsed.signatures).toEqual(DEFAULT_PREFS.signatures);
    // …and the v1 half is untouched.
    expect(parsed.density).toBe("compact");
    expect(parsed.undoSendSeconds).toBe(30);
  });

  it("feature-detects a v2 server from the object, not from a version number", () => {
    /*
     * The server publishes no schema version on the wire — deliberately, since
     * it is metadata about the STORED document (`encodePrefs`) and RFC 8621 has
     * no place for it. Structural detection is therefore the honest answer.
     */
    expect(servesPrefsV2(FULL_WIRE)).toBe(true);

    // One missing key is enough: a v2 server renders all six unconditionally.
    const { signatures: _absent, ...withoutSignatures } = FULL_WIRE;
    expect(servesPrefsV2(withoutSignatures)).toBe(false);
    expect(servesPrefsV2(null)).toBe(false);
  });

  it("detects v3 at its OWN granularity, not v2's (P0-5)", () => {
    /*
     * A deploy window can serve v2 and not v3, and the difference is
     * user-visible: the folder-visibility table must render as unavailable
     * rather than offering a switch whose save comes back `unknownProperty`
     * and silently reverts. Detection folded into `servesPrefsV2` would have
     * offered it.
     */
    expect(servesPrefsV3(FULL_WIRE)).toBe(true);

    const { folderVisibility: _absent, ...v2Only } = FULL_WIRE;
    expect(servesPrefsV3(v2Only)).toBe(false);
    // ...and the v2 keys are still all there, so a v2 server is still a v2
    // server: the two detections are independent, which is the point.
    expect(servesPrefsV2(v2Only)).toBe(true);
  });

  it("hands a folder back to the POLICY when its stored value is nonsense", () => {
    /*
     * Dropped, not defaulted to "show". Defaulting would reveal a folder the
     * user had hidden — the wrong way for a parse failure to fall — while
     * dropping restores the state before anyone chose.
     */
    const parsed = parsePrefs({
      ...FULL_WIRE,
      folderVisibility: { Calendario: "maybe", Avisos: "hide", Otra: 7 },
    });
    expect(parsed.folderVisibility).toEqual({ Avisos: "hide" });
  });

  it("treats an absent folderVisibility as an empty map, never undefined", () => {
    const { folderVisibility: _absent, ...v2Only } = FULL_WIRE;
    expect(parsePrefs(v2Only).folderVisibility).toEqual({});
  });

  it("drops a half-written label entry rather than inventing a colour", () => {
    /*
     * The opposite of how scalars degrade, and deliberately: a scalar has one
     * honest fallback, while a half-written label would render a chip in a
     * colour the user never picked. Dropping it returns the label to the
     * default swatch, which is exactly what "no metadata" already means.
     */
    const parsed = parsePrefs({
      ...FULL_WIRE,
      labels: {
        "$label:ok": { color: "teal", visibility: "hide" },
        "$label:noColor": { visibility: "show" },
        "$label:badVisibility": { color: "teal", visibility: "sometimes" },
      },
    });
    expect(parsed.labels).toEqual({ "$label:ok": { color: "teal", visibility: "hide" } });
  });

  it("clamps each offline depth independently, out-of-range falling to the default", () => {
    const parsed = parsePrefs({
      ...FULL_WIRE,
      // Below the floor and inside the range — the floor is what the server
      // also refuses, and the valid half must not be collateral damage.
      offlineDepth: { headersPerMailbox: 10, bodies: 300 },
    });
    expect(parsed.offlineDepth.headersPerMailbox).toBe(
      DEFAULT_PREFS.offlineDepth.headersPerMailbox,
    );
    expect(parsed.offlineDepth.bodies).toBe(300);
  });

  it("treats a signature reference that names no item as none", () => {
    /*
     * The server refuses a dangling reference on WRITE, for a stated reason:
     * the fallback it would silently produce is a different signature going out
     * under the user's name. The read path honours the same rule.
     */
    const parsed = parsePrefs({
      ...FULL_WIRE,
      signatures: { items: {}, forNew: "ghost", forReply: null },
    });
    expect(parsed.signatures.forNew).toBeNull();
  });
});

describe("resolveSignature — the precedence rule, quoted from store.Prefs", () => {
  const signatures = {
    items: {
      work: { name: "Work", textBody: "-- \nWork", htmlBody: "<p>Work</p>" },
      plain: { name: "Plain", textBody: "-- \nPlain", htmlBody: "" },
    },
    forNew: "work",
    forReply: "plain",
  };

  it("uses forNew for new mail and forReply for replies", () => {
    expect(resolveSignature(signatures, "new")?.text).toBe("-- \nWork");
    expect(resolveSignature(signatures, "reply")?.text).toBe("-- \nPlain");
  });

  it("falls back to the identity signature when the reference is none", () => {
    // `undefined` is the caller's cue to use the Identity's own — the RFC 8621
    // §6 behaviour every other JMAP client sees.
    const none = { items: signatures.items, forNew: null, forReply: null };
    expect(resolveSignature(none, "new")).toBeUndefined();
    expect(resolveSignature(none, "reply")).toBeUndefined();
  });

  it("uses the text body for HTML when a named signature has no html", () => {
    // Every signature this UI can create has an empty htmlBody, so without
    // this the rich composer's footer would blank out on choosing one.
    expect(resolveSignature(signatures, "reply")?.html).toBe("-- \nPlain");
    // A signature that DOES carry html keeps it.
    expect(resolveSignature(signatures, "new")?.html).toBe("<p>Work</p>");
  });
});

describe("the wire shape of a v2 set — the Go↔TS seam no compiler spans", () => {
  /*
   * The gate's finding 2 named this precisely: "nothing pins the Go↔TS seam".
   * Each case below asserts the EXACT JSON a set of one v2 key puts on the
   * wire, against the shape `internal/jmap/mail/prefs.go` parses:
   * `applyLabelsPatch`, `applyOfflineDepthPatch`, `applySignaturesPatch` and
   * the scalar `prefsPatchEnum`/`prefsPatchBool` arms.
   *
   * A rename on either side now fails here instead of at a user's settings
   * screen, which is the whole point — the two schemas are the same object
   * written twice, across a language boundary.
   */
  function capture(patch: Partial<Prefs>): Record<string, unknown> {
    const { client, calls } = fakeClient(() => ({
      methodResponses: [
        ["Prefs/set", { updated: { [PREFS_ID]: null } }, "p"],
        ["Prefs/get", { state: "s2", list: [FULL_WIRE] }, "g"],
      ],
      sessionState: "x",
    }) as unknown as JmapResponse);
    void savePrefs(client, "a1", patch);
    const [methodCalls] = calls[0] as [readonly unknown[]];
    const [, args] = methodCalls[0] as [string, Record<string, unknown>, string];
    const update = args.update as Record<string, Record<string, unknown>>;
    return update[PREFS_ID] ?? {};
  }

  it("labels: an object keyed by keyword, each with color and visibility", () => {
    expect(
      capture({ labels: { "$label:work": { color: "amber", visibility: "showIfUnread" } } }),
    ).toEqual({
      // NOT `colorId` — the wire name is `color`, and the server's
      // `parseLabelPrefs` refuses any other nested key by name.
      labels: { "$label:work": { color: "amber", visibility: "showIfUnread" } },
    });
  });

  it("offlineDepth: an object with headersPerMailbox and bodies", () => {
    expect(capture({ offlineDepth: { headersPerMailbox: 500, bodies: 250 } })).toEqual({
      offlineDepth: { headersPerMailbox: 500, bodies: 250 },
    });
  });

  it("addressAutocomplete: the enum string, never a boolean", () => {
    // The client speaks booleans internally; the wire is "auto"/"manual", and
    // a boolean here would be refused with invalidProperties.
    expect(capture({ addressAutocomplete: "manual" })).toEqual({
      addressAutocomplete: "manual",
    });
  });

  it("folderVisibility: a flat map keyed by mailbox NAME (v3, P0-5)", () => {
    /*
     * Keys are display names, not ids, matching `store.Prefs.FolderVisibility`.
     * Sent as the WHOLE map, which is the server's whole-map replacement arm —
     * the per-folder `folderVisibility/<name>` pointer form exists too and is
     * deliberately unused here, because a folder name may contain a slash and
     * would then need RFC 6901 escaping on the way out.
     */
    expect(
      capture({ folderVisibility: { Calendario: "hide", Avisos: "showIfUnread" } }),
    ).toEqual({ folderVisibility: { Calendario: "hide", Avisos: "showIfUnread" } });
  });

  it("folderVisibility: a name with a slash rides the whole map untouched", () => {
    // The case that would have forced an escaping step if this used pointers.
    expect(capture({ folderVisibility: { "Problemas/Conflictos": "hide" } })).toEqual({
      folderVisibility: { "Problemas/Conflictos": "hide" },
    });
  });

  it("sendAndArchive: a bare boolean", () => {
    expect(capture({ sendAndArchive: false })).toEqual({ sendAndArchive: false });
  });

  it("defaultReplyBehavior: the enum string", () => {
    expect(capture({ defaultReplyBehavior: "replyAll" })).toEqual({
      defaultReplyBehavior: "replyAll",
    });
  });

  it("signatures: items plus forNew/forReply, with null (not empty string) for none", () => {
    const patch = capture({
      signatures: {
        items: { work: { name: "Work", textBody: "-- \nD", htmlBody: "" } },
        forNew: "work",
        forReply: null,
      },
    });
    expect(patch).toEqual({
      signatures: {
        items: { work: { name: "Work", textBody: "-- \nD", htmlBody: "" } },
        forNew: "work",
        forReply: null,
      },
    });
    /*
     * The null is load-bearing and worth its own assertion: `prefsOptionalID`
     * emits null and `parseSignatureRef` reads it as "none". An empty string
     * happens to be accepted too, but relying on that would be relying on a
     * tolerance rather than on the contract.
     */
    const signatures = patch.signatures as Record<string, unknown>;
    expect(signatures.forReply).toBeNull();
  });

  it("sends ONLY the named key, never the whole object", () => {
    // A save that sent every key would overwrite whatever another tab changed
    // between our read and our write.
    expect(Object.keys(capture({ sendAndArchive: true }))).toEqual(["sendAndArchive"]);
  });
});
