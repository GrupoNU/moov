import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { JmapClient } from "../../api/jmap";
import { BrandingProvider } from "../../branding/BrandingProvider";
import { I18nProvider } from "../../i18n/I18nProvider";
import { makeChip } from "../../mail/addresses";
import { PrefsProvider } from "../../mail/PrefsProvider";
import { DEFAULT_PREFS, type Prefs } from "../../mail/prefs";
import type { Identity } from "../../mail/write";
import { Composer } from "./Composer";
import { newDraft } from "./composerState";

/**
 * The composer's signature precedence (prefs v2 `signatures`, E7).
 *
 * The RULE itself is pinned in `mail/prefs.test.ts` against the table quoted
 * from `store.Prefs.Signatures`. What only a mounted composer can prove — and
 * what the gate found missing — is that the composer CONSULTS it: before this
 * wiring the six v2 keys had no client consumer at all, and the composer went
 * on using the single per-identity signature.
 *
 * The rule, restated so this file can be read on its own:
 *
 *   this PWA, new mail : prefs.signatures.forNew, else the Identity's own
 *   this PWA, a reply  : prefs.signatures.forReply, else the Identity's own
 *   any other client   : the Identity's own, always
 */

const identity: Identity = {
  id: "primary",
  name: "Moov Test",
  email: "moov-test@atmosfera.cloud",
  replyTo: null,
  bcc: null,
  textSignature: "-- \nFrom the identity",
  htmlSignature: "",
  mayDelete: false,
};

/** Captures every `Email/set` the composer autosaves, so the BODY is readable. */
function harness() {
  const bodies: string[] = [];
  const fetchImpl = vi.fn((_url: string, init?: RequestInit) => {
    const raw = typeof init?.body === "string" ? init.body : "{}";
    bodies.push(raw);
    return Promise.resolve(
      new Response(
        JSON.stringify({
          methodResponses: [["Email/set", { created: { draft: { id: "e-new" } } }, "c"]],
          sessionState: "x",
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
  }) as unknown as typeof fetch;

  const client = new JmapClient({ username: "u", password: "p" }, { fetchImpl });
  return { client, bodies };
}

const SIGNATURES: Prefs["signatures"] = {
  items: {
    work: { name: "Work", textBody: "-- \nWork signature", htmlBody: "" },
    brief: { name: "Brief", textBody: "-- \nBrief signature", htmlBody: "" },
  },
  forNew: "work",
  forReply: "brief",
};

function renderComposer(
  client: JmapClient,
  prefs: Prefs,
  draft: React.ComponentProps<typeof Composer>["draft"],
) {
  render(
    <I18nProvider locale="en">
      <BrandingProvider>
        <PrefsProvider
          client={undefined}
          session={undefined}
          accountId="a1"
          initialPrefs={prefs}
        >
          <Composer
            draft={draft}
            client={client}
            accountId="a1"
            identity={identity}
            draftsMailboxId="mbDrafts"
            sentMailboxId="mbSent"
            sessionCapabilities={{
              "urn:ietf:params:jmap:core": { maxSizeUpload: 1000 },
              "urn:ietf:params:jmap:mail": { maxSizeAttachmentsPerEmail: 2000 },
            }}
            uploadUrlTemplate="/jmap/upload/{accountId}"
            authorization="Basic xxx"
            onClose={vi.fn()}
            onNotify={vi.fn()}
            onChanged={vi.fn()}
          />
        </PrefsProvider>
      </BrandingProvider>
    </I18nProvider>,
  );
}

/** The body of the draft the composer saved, once it has saved one. */
async function savedBody(bodies: readonly string[]): Promise<string> {
  await waitFor(() => {
    expect(bodies.length).toBeGreaterThan(0);
  });
  return bodies.join("\n");
}

/**
 * Sends, which is what puts the assembled body on the wire.
 *
 * Send rather than autosave: autosave is debounced behind a scheduler this test
 * would have to drive with fake timers, whereas Send is a click and produces
 * the same `Email/set` from the same `spec` memo — which is the value carrying
 * the signature, and therefore the thing under test.
 */
async function send(user: ReturnType<typeof userEvent.setup>): Promise<void> {
  await user.click(await screen.findByRole("button", { name: "Send" }));
}

/** A ready-to-send draft with the given intent. */
function draftWith(intent: "new" | "reply") {
  const base = {
    ...newDraft(false),
    to: [makeChip("destino@example.test")],
    subject: "Hola",
    text: "cuerpo",
  };
  return intent === "new" ? base : { ...base, intent: "reply" as const };
}

describe("which signature the composer pre-fills", () => {
  it("uses forNew for a NEW message", async () => {
    const user = userEvent.setup();
    const { client, bodies } = harness();
    renderComposer(client, { ...DEFAULT_PREFS, signatures: SIGNATURES }, draftWith("new"));
    await send(user);

    const body = await savedBody(bodies);
    expect(body).toContain("Work signature");
    expect(body).not.toContain("From the identity");
  });

  it("uses forReply for a REPLY", async () => {
    const user = userEvent.setup();
    const { client, bodies } = harness();
    renderComposer(client, { ...DEFAULT_PREFS, signatures: SIGNATURES }, draftWith("reply"));
    await send(user);

    const body = await savedBody(bodies);
    expect(body).toContain("Brief signature");
    expect(body).not.toContain("Work signature");
  });

  it("falls back to the IDENTITY signature when no named one is selected", async () => {
    /*
     * The default state of every account, and the behaviour every other JMAP
     * client sees (RFC 8621 §6). A regression here would silently strip the
     * signature from every message of every user who has not created a named
     * one — which is all of them, on day one.
     */
    const user = userEvent.setup();
    const { client, bodies } = harness();
    renderComposer(
      client,
      { ...DEFAULT_PREFS, signatures: { ...SIGNATURES, forNew: null } },
      draftWith("new"),
    );
    await send(user);

    const body = await savedBody(bodies);
    expect(body).toContain("From the identity");
    expect(body).not.toContain("Work signature");
  });

  it("falls back when the account has no named signatures at all", async () => {
    // The day-one state, and the one `parsePrefs` also produces from a dangling
    // reference: `resolveSignature` returning undefined IS the cue to use the
    // identity's own, never to send a signature-less message.
    const user = userEvent.setup();
    const { client, bodies } = harness();
    renderComposer(client, DEFAULT_PREFS, draftWith("new"));
    await send(user);

    expect(await savedBody(bodies)).toContain("From the identity");
  });
});
