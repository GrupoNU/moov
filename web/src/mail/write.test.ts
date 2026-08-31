import { describe, expect, it, vi } from "vitest";

import { CAP_CORE, CAP_MAIL, JmapClient, type JmapResponse } from "../api/jmap";
import {
  destroyMessages,
  draftObject,
  firstFailureMessage,
  hasFailures,
  maxAttachmentsSize,
  maxUploadSize,
  moveMessages,
  saveDraft,
  sendDraft,
  setKeyword,
  setKeywords,
  uploadUrlFor,
  type DraftSpec,
} from "./write";

/** A client whose every call returns a scripted response and records the request. */
function scriptedClient(response: JmapResponse): {
  client: JmapClient;
  requests: { using: string[]; methodCalls: unknown[] }[];
} {
  const requests: { using: string[]; methodCalls: unknown[] }[] = [];
  const fetchImpl = vi.fn((_url: string, init?: RequestInit) => {
    // The body is always the JSON string the client just serialized; typing it
    // as such keeps the parse honest instead of stringifying an object.
    const body = typeof init?.body === "string" ? init.body : "{}";
    requests.push(JSON.parse(body));
    return Promise.resolve(
      new Response(JSON.stringify(response), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  }) as unknown as typeof fetch;

  return {
    client: new JmapClient({ username: "u", password: "p" }, { fetchImpl }),
    requests,
  };
}

function response(...calls: [string, Record<string, unknown>, string][]): JmapResponse {
  return { methodResponses: calls, sessionState: "s" };
}

describe("setKeyword", () => {
  it("sends a PatchObject, never the whole keyword set", async () => {
    const { client, requests } = scriptedClient(
      response(["Email/set", { updated: { e1: null }, newState: "2" }, "s"]),
    );
    await setKeyword(client, "a", ["e1"], "$seen", true);

    const call = (requests[0]?.methodCalls as [string, Record<string, unknown>, string][])[0];
    expect(call?.[0]).toBe("Email/set");
    /*
     * The whole-set form would ERASE keywords the client never fetched —
     * $answered, $forwarded, another client's label. A patch touches exactly
     * the property named.
     */
    expect((call?.[1].update as Record<string, unknown>).e1).toEqual({
      "keywords/$seen": true,
    });
  });

  it("clears with false, RFC 8620 §5.3's 'remove this key'", async () => {
    const { client, requests } = scriptedClient(response(["Email/set", { updated: {} }, "s"]));
    await setKeyword(client, "a", ["e1"], "$flagged", false);

    const call = (requests[0]?.methodCalls as [string, Record<string, unknown>, string][])[0];
    expect((call?.[1].update as Record<string, unknown>).e1).toEqual({
      "keywords/$flagged": false,
    });
  });

  it("batches many ids into one request", async () => {
    const { client, requests } = scriptedClient(response(["Email/set", { updated: {} }, "s"]));
    await setKeyword(client, "a", ["e1", "e2", "e3"], "$seen", true);
    expect(requests).toHaveLength(1);
    const call = (requests[0]?.methodCalls as [string, Record<string, unknown>, string][])[0];
    expect(Object.keys(call?.[1].update as Record<string, unknown>)).toEqual(["e1", "e2", "e3"]);
  });

  it("makes no request at all for an empty id list", async () => {
    const { client, requests } = scriptedClient(response(["Email/set", {}, "s"]));
    const outcome = await setKeyword(client, "a", [], "$seen", true);
    expect(requests).toHaveLength(0);
    expect(outcome.updated).toEqual([]);
  });

  /*
   * §5.3 gives every /set per-record errors so one bad id does not fail the
   * batch. Throwing on the first would discard the successes and roll back
   * messages the server DID change.
   */
  it("reports per-record failures as values, keeping the successes", async () => {
    const { client } = scriptedClient(
      response([
        "Email/set",
        {
          updated: { e1: null },
          notUpdated: {
            e2: { type: "notFound" },
            e3: {
              type: "invalidProperties",
              properties: ["keywords"],
              description: "this mailbox is at the Maildir durable-keyword ceiling",
            },
          },
        },
        "s",
      ]),
    );
    const outcome = await setKeyword(client, "a", ["e1", "e2", "e3"], "$seen", true);
    expect(outcome.updated).toEqual(["e1"]);
    expect(hasFailures(outcome)).toBe(true);
    expect(Object.keys(outcome.failed)).toEqual(["e2", "e3"]);
  });

  it("surfaces the server's OWN description, not a generic word", () => {
    const outcome = {
      updated: [],
      destroyed: [],
      created: {},
      failed: {
        e1: {
          type: "invalidProperties",
          description: "this mailbox is at the Maildir durable-keyword ceiling",
        },
      },
      newState: undefined,
    };
    expect(firstFailureMessage(outcome)).toBe(
      "this mailbox is at the Maildir durable-keyword ceiling",
    );
  });

  it("falls back to the SetError type when the server gave no description", () => {
    const outcome = {
      updated: [],
      destroyed: [],
      created: {},
      failed: { e1: { type: "notFound" } },
      newState: undefined,
    };
    // Still a precise machine word, never "an error occurred".
    expect(firstFailureMessage(outcome)).toBe("notFound");
  });
});

describe("moveMessages", () => {
  /*
   * A patch that only ADDS the destination would resolve to two mailboxes and
   * be refused with invalidProperties — this server holds a message in exactly
   * one mailbox.
   */
  it("sends the whole mailboxIds set, not an add-patch", async () => {
    const { client, requests } = scriptedClient(response(["Email/set", { updated: {} }, "s"]));
    await moveMessages(client, "a", ["e1"], "mbArchive");

    const call = (requests[0]?.methodCalls as [string, Record<string, unknown>, string][])[0];
    expect((call?.[1].update as Record<string, unknown>).e1).toEqual({
      mailboxIds: { mbArchive: true },
    });
  });
});

describe("destroyMessages", () => {
  it("sends a destroy list and reports what was destroyed", async () => {
    const { client, requests } = scriptedClient(
      response(["Email/set", { destroyed: ["e1"], notDestroyed: { e2: { type: "notFound" } } }, "s"]),
    );
    const outcome = await destroyMessages(client, "a", ["e1", "e2"]);

    const call = (requests[0]?.methodCalls as [string, Record<string, unknown>, string][])[0];
    expect(call?.[1].destroy).toEqual(["e1", "e2"]);
    expect(outcome.destroyed).toEqual(["e1"]);
    expect(Object.keys(outcome.failed)).toEqual(["e2"]);
  });
});

// ---------------------------------------------------------------------------

const baseSpec: DraftSpec = {
  mailboxId: "mbDrafts",
  from: [{ name: "Me", email: "me@moov.test" }],
  to: [{ name: null, email: "you@x.com" }],
  cc: [],
  bcc: [],
  subject: "Hola",
  text: "cuerpo",
  attachments: [],
};

describe("draftObject", () => {
  it("builds the shape email_create.go accepts", () => {
    const object = draftObject(baseSpec);
    expect(object.mailboxIds).toEqual({ mbDrafts: true });
    expect(object.keywords).toEqual({ $draft: true, $seen: true });
    expect(object.textBody).toEqual([{ partId: "text", type: "text/plain" }]);
    expect(object.bodyValues).toEqual({ text: { value: "cuerpo" } });
  });

  /*
   * The server REFUSES every server-set property with invalidProperties —
   * receivedAt included, explicitly, in email_create.go's header.
   */
  it("sends no server-set property", () => {
    const object = draftObject(baseSpec);
    for (const forbidden of [
      "id",
      "blobId",
      "threadId",
      "size",
      "preview",
      "hasAttachment",
      "receivedAt",
    ]) {
      expect(object, forbidden).not.toHaveProperty(forbidden);
    }
  });

  /*
   * Every client that omits $seen leaves Drafts with a permanent unread badge.
   */
  it("marks the draft seen, not just $draft", () => {
    expect(draftObject(baseSpec).keywords).toHaveProperty("$seen", true);
  });

  it("omits empty cc/bcc rather than sending empty arrays", () => {
    const object = draftObject(baseSpec);
    expect(object).not.toHaveProperty("cc");
    expect(object).not.toHaveProperty("bcc");
  });

  /*
   * GC-6, the composer half (E10; canon §4.1.2): outgoing mail NEVER carries
   * a read-receipt request. The creation object is the assembly layer on this
   * side of the wire — `header:{Name}` is the only way a header this client
   * does not model could reach the server — so the pin is structural: no
   * `header:*` key of ANY kind is emitted, which subsumes the specific ban.
   * The server enforces its own half (email_create.go refuses the receipt
   * family with `forbidden`), so both layers hold independently.
   */
  it("emits no header:* key at all — read-receipt requests are unmintable (GC-6)", () => {
    const object = draftObject({
      ...baseSpec,
      cc: [{ name: null, email: "cc@x.com" }],
      bcc: [{ name: null, email: "bcc@x.com" }],
      html: "<p>hola</p>",
      attachments: [{ blobId: "b1", name: "a.pdf", type: "application/pdf", size: 3 }],
      inReplyTo: ["<m1@x>"],
      references: ["<m0@x>"],
      keywords: ["$label:x"],
      replyTo: [{ name: null, email: "r@x.com" }],
    });
    for (const key of Object.keys(object)) {
      expect(key.startsWith("header:"), key).toBe(false);
    }
    expect(JSON.stringify(object).toLowerCase()).not.toContain(
      "disposition-notification",
    );
  });

  it("includes cc and bcc when present", () => {
    const object = draftObject({
      ...baseSpec,
      cc: [{ name: null, email: "c@x.com" }],
      bcc: [{ name: null, email: "b@x.com" }],
    });
    expect(object.cc).toEqual([{ name: null, email: "c@x.com" }]);
    expect(object.bcc).toEqual([{ name: null, email: "b@x.com" }]);
  });

  /*
   * §4.6: textBody and htmlBody carry AT MOST ONE part each, referenced by
   * partId into bodyValues. A multi-part list is refused, not concatenated.
   */
  it("adds exactly one html part when the body is rich", () => {
    const object = draftObject({ ...baseSpec, html: "<p>cuerpo</p>" });
    expect(object.htmlBody).toEqual([{ partId: "html", type: "text/html" }]);
    expect(object.bodyValues).toEqual({
      text: { value: "cuerpo" },
      html: { value: "<p>cuerpo</p>" },
    });
  });

  it("omits htmlBody for an empty html string", () => {
    expect(draftObject({ ...baseSpec, html: "" })).not.toHaveProperty("htmlBody");
  });

  it("references attachments by blobId with an explicit disposition", () => {
    const object = draftObject({
      ...baseSpec,
      attachments: [{ blobId: "b1", name: "a.pdf", type: "application/pdf", size: 10 }],
    });
    expect(object.attachments).toEqual([
      { blobId: "b1", type: "application/pdf", name: "a.pdf", disposition: "attachment" },
    ]);
  });

  it("carries the threading headers a reply needs", () => {
    const object = draftObject({
      ...baseSpec,
      inReplyTo: ["<p@x.com>"],
      references: ["<r@x.com>", "<p@x.com>"],
    });
    expect(object.inReplyTo).toEqual(["<p@x.com>"]);
    expect(object.references).toEqual(["<r@x.com>", "<p@x.com>"]);
  });
});

describe("saveDraft", () => {
  /*
   * §4.6 makes every Email property except keywords and mailboxIds immutable —
   * a message IS its bytes. Editing a draft is a create plus a destroy, and
   * the order matters: create FIRST, so a failed create leaves the previous
   * revision intact.
   */
  it("creates the new revision and destroys the old one in ONE request, create first", async () => {
    const { client, requests } = scriptedClient(
      response(["Email/set", { created: { draft: { id: "e9", blobId: "b9", size: 42 } }, destroyed: ["e8"] }, "s"]),
    );
    const { draft } = await saveDraft(client, "a", baseSpec, "e8");

    expect(requests).toHaveLength(1);
    const call = (requests[0]?.methodCalls as [string, Record<string, unknown>, string][])[0];
    expect(call?.[1]).toHaveProperty("create");
    expect(call?.[1].destroy).toEqual(["e8"]);
    expect(draft?.id).toBe("e9");
  });

  it("sends no destroy for a first save", async () => {
    const { client, requests } = scriptedClient(
      response(["Email/set", { created: { draft: { id: "e1" } } }, "s"]),
    );
    await saveDraft(client, "a", baseSpec, undefined);
    const call = (requests[0]?.methodCalls as [string, Record<string, unknown>, string][])[0];
    expect(call?.[1]).not.toHaveProperty("destroy");
  });

  it("returns no draft when the create failed, with the server's reason", async () => {
    const { client } = scriptedClient(
      response([
        "Email/set",
        { notCreated: { draft: { type: "tooLarge", description: "attachments exceed maxSizeAttachmentsPerEmail (25000000 bytes)" } } },
        "s",
      ]),
    );
    const { draft, outcome } = await saveDraft(client, "a", baseSpec, undefined);
    expect(draft).toBeUndefined();
    expect(firstFailureMessage(outcome)).toContain("maxSizeAttachmentsPerEmail");
  });
});

describe("sendDraft", () => {
  it("creates the draft and submits it in ONE request, per §7.5's flow", async () => {
    const { client, requests } = scriptedClient(
      response(
        ["Email/set", { created: { draft: { id: "e1" } } }, "c"],
        [
          "EmailSubmission/set",
          { created: { sendIt: { id: "s1", undoStatus: "pending", sendAt: "2026-08-26T12:00:10Z" } } },
          "s",
        ],
      ),
    );
    const result = await sendDraft(client, "a", baseSpec, {
      identityId: "primary",
      sentMailboxId: "mbSent",
    });

    expect(requests).toHaveLength(1);
    const calls = requests[0]?.methodCalls as [string, Record<string, unknown>, string][];
    expect(calls.map((call) => call[0])).toEqual(["Email/set", "EmailSubmission/set"]);

    // §7.5: emailId "may be a creation id reference, prefixed with #".
    const submission = calls[1]?.[1].create as Record<string, Record<string, unknown>>;
    expect(submission.sendIt?.emailId).toBe("#draft");
    expect(submission.sendIt?.identityId).toBe("primary");

    expect(result.submission?.id).toBe("s1");
    expect(result.submission?.undoStatus).toBe("pending");
    expect(result.emailId).toBe("e1");
  });

  it("asks the server to file the sent copy and drop $draft, atomically", async () => {
    const { client, requests } = scriptedClient(
      response(
        ["Email/set", { created: { draft: { id: "e1" } } }, "c"],
        ["EmailSubmission/set", { created: { sendIt: { id: "s1", undoStatus: "pending", sendAt: "" } } }, "s"],
      ),
    );
    await sendDraft(client, "a", baseSpec, { identityId: "primary", sentMailboxId: "mbSent" });

    const calls = requests[0]?.methodCalls as [string, Record<string, unknown>, string][];
    expect(calls[1]?.[1].onSuccessUpdateEmail).toEqual({
      "#sendIt": { mailboxIds: { mbSent: true }, "keywords/$draft": null },
    });
  });

  it("uses the submission capability — the server refuses the method without it", async () => {
    const { client, requests } = scriptedClient(
      response(
        ["Email/set", { created: { draft: { id: "e1" } } }, "c"],
        ["EmailSubmission/set", { created: {} }, "s"],
      ),
    );
    await sendDraft(client, "a", baseSpec, { identityId: "primary", sentMailboxId: undefined });
    expect(requests[0]?.using).toContain("urn:ietf:params:jmap:submission");
  });

  /*
   * A failed create means the submission never ran. Reporting "no submission"
   * would hide the real reason, which is on the create's own SetError.
   */
  it("reports the CREATE's error when the draft could not be made", async () => {
    const { client } = scriptedClient(
      response(
        ["Email/set", { notCreated: { draft: { type: "blobNotFound", description: 'blobId "b1" is not available to this account' } } }, "c"],
        ["EmailSubmission/set", { notCreated: {} }, "s"],
      ),
    );
    const result = await sendDraft(client, "a", baseSpec, {
      identityId: "primary",
      sentMailboxId: "mbSent",
    });
    expect(result.submission).toBeUndefined();
    expect(result.emailId).toBeUndefined();
    expect(firstFailureMessage(result.outcome)).toContain("blobNotFound".slice(0, 4));
  });

  it("surfaces a forbiddenFrom refusal with the server's sentence", async () => {
    const { client } = scriptedClient(
      response(
        ["Email/set", { created: { draft: { id: "e1" } } }, "c"],
        [
          "EmailSubmission/set",
          {
            notCreated: {
              sendIt: {
                type: "forbiddenFrom",
                description: "the message's From (x@y.com) is not the identity's address (me@moov.test)",
              },
            },
          },
          "s",
        ],
      ),
    );
    const result = await sendDraft(client, "a", baseSpec, {
      identityId: "primary",
      sentMailboxId: "mbSent",
    });
    expect(result.submission).toBeUndefined();
    expect(firstFailureMessage(result.outcome)).toContain("is not the identity's address");
  });
});

describe("maxUploadSize / maxAttachmentsSize", () => {
  /*
   * The server's rule is declared == applied. Hardcoding 50 MB would break the
   * day an operator lowers the limit, and would refuse files the server would
   * accept the day one raises it.
   */
  it("reads maxSizeUpload from the session's core capability", () => {
    expect(maxUploadSize({ [CAP_CORE]: { maxSizeUpload: 50_000_000 } })).toBe(50_000_000);
  });

  it("reads maxSizeAttachmentsPerEmail from the mail capability", () => {
    expect(maxAttachmentsSize({ [CAP_MAIL]: { maxSizeAttachmentsPerEmail: 25_000_000 } })).toBe(
      25_000_000,
    );
  });

  it("returns undefined rather than a guess when the server said nothing", () => {
    expect(maxUploadSize(undefined)).toBeUndefined();
    expect(maxUploadSize({})).toBeUndefined();
    expect(maxUploadSize({ [CAP_CORE]: {} })).toBeUndefined();
    expect(maxUploadSize({ [CAP_CORE]: { maxSizeUpload: "big" } })).toBeUndefined();
    expect(maxUploadSize({ [CAP_CORE]: { maxSizeUpload: 0 } })).toBeUndefined();
  });
});

describe("uploadUrlFor", () => {
  it("expands the session's template", () => {
    expect(uploadUrlFor("/jmap/upload/{accountId}", "a1")).toBe("/jmap/upload/a1");
  });

  /*
   * The same same-origin rule the whole client follows: honour the server's
   * PATH, never its origin — a JMAP client that follows an origin handed to it
   * by a response is one redirect away from sending Basic credentials
   * elsewhere.
   */
  it("reduces an absolute advertised URL to its path", () => {
    expect(uploadUrlFor("https://moov.atmosfera.cloud/jmap/upload/{accountId}", "a1")).toBe(
      "/jmap/upload/a1",
    );
  });

  it("falls back to the known path when the template is absent", () => {
    expect(uploadUrlFor(undefined, "a1")).toBe("/jmap/upload/a1");
  });

  it("percent-encodes the account id", () => {
    expect(uploadUrlFor("/jmap/upload/{accountId}", "a/1")).toBe("/jmap/upload/a%2F1");
  });
});

/**
 * E8 — the JSON-Pointer escaping, asserted at the WIRE.
 *
 * `labels.test.ts` proves `keywordPatchKey` escapes correctly. These prove the
 * write path USES it, which is the half that regresses: the escaping helper can
 * be perfect and a new call site can still interpolate the key by hand, which
 * is exactly how the Bulwark bug (research 05 §5.0) reached production.
 */
describe("setKeyword escapes the patch key (E8, mechanism G3)", () => {
  it("sends the ESCAPED pointer for a nested label — not the silently-lost form", async () => {
    const { client, requests } = scriptedClient(response(["Email/set", { updated: {} }, "s"]));
    await setKeyword(client, "a", ["e1"], "$label:work/clients", true);

    const call = (requests[0]?.methodCalls as [string, Record<string, unknown>, string][])[0];
    const patch = (call?.[1].update as Record<string, Record<string, unknown>>).e1;

    /*
     * The bug shape, stated as the thing that must NOT be sent: an unescaped
     * `keywords/$label:work/clients` is a pointer to the `clients` MEMBER of
     * `$label:work`. The server accepts it, nothing errors, and the label never
     * lands.
     */
    expect(patch).not.toHaveProperty("keywords/$label:work/clients");
    expect(patch).toEqual({ "keywords/$label:work~1clients": true });
  });

  it("escapes a tilde", async () => {
    const { client, requests } = scriptedClient(response(["Email/set", { updated: {} }, "s"]));
    await setKeyword(client, "a", ["e1"], "$label:back~up", false);

    const call = (requests[0]?.methodCalls as [string, Record<string, unknown>, string][])[0];
    expect((call?.[1].update as Record<string, unknown>).e1).toEqual({
      "keywords/$label:back~0up": false,
    });
  });

  it("leaves a plain system flag untouched — no gratuitous escaping", async () => {
    const { client, requests } = scriptedClient(response(["Email/set", { updated: {} }, "s"]));
    await setKeyword(client, "a", ["e1"], "$seen", true);

    const call = (requests[0]?.methodCalls as [string, Record<string, unknown>, string][])[0];
    expect((call?.[1].update as Record<string, unknown>).e1).toEqual({
      "keywords/$seen": true,
    });
  });
});

describe("setKeywords — the atomic swap a rename needs", () => {
  it("adds and removes in ONE patch, so no message can carry both labels", async () => {
    const { client, requests } = scriptedClient(response(["Email/set", { updated: {} }, "s"]));
    await setKeywords(client, "a", ["e1", "e2"], {
      "$label:work": false,
      "$label:trabajo": true,
    });

    expect(requests).toHaveLength(1);
    const call = (requests[0]?.methodCalls as [string, Record<string, unknown>, string][])[0];
    const update = call?.[1].update as Record<string, Record<string, unknown>>;
    for (const id of ["e1", "e2"]) {
      expect(update[id]).toEqual({
        "keywords/$label:work": false,
        "keywords/$label:trabajo": true,
      });
    }
  });

  it("escapes every key it builds", async () => {
    const { client, requests } = scriptedClient(response(["Email/set", { updated: {} }, "s"]));
    await setKeywords(client, "a", ["e1"], {
      "$label:a/b": false,
      "$label:c/d": true,
    });

    const call = (requests[0]?.methodCalls as [string, Record<string, unknown>, string][])[0];
    expect((call?.[1].update as Record<string, unknown>).e1).toEqual({
      "keywords/$label:a~1b": false,
      "keywords/$label:c~1d": true,
    });
  });

  it("makes no request for an empty id list or an empty patch", async () => {
    const { client, requests } = scriptedClient(response(["Email/set", { updated: {} }, "s"]));
    await setKeywords(client, "a", [], { "$label:x": true });
    await setKeywords(client, "a", ["e1"], {});
    expect(requests).toHaveLength(0);
  });
});
