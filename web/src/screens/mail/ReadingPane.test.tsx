import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { JmapClient } from "../../api/jmap";
import { I18nProvider } from "../../i18n/I18nProvider";
import { KEYWORD_FLAGGED, type Email, type Mailbox } from "../../mail/types";
import { PrefsProvider } from "../../mail/PrefsProvider";
import { DEFAULT_PREFS, type Prefs } from "../../mail/prefs";
import { ReadingPane, type ReadingPaneProps } from "./ReadingPane";

/**
 * The reader's E2 surfaces.
 *
 * What is proved here is the behaviour a pure module cannot: that the Spam
 * banner appears exactly in Junk, that the remote-image opt-in is UNREACHABLE
 * there (canon §4.1.9 — the acceptance criterion with a security consequence),
 * that the unsubscribe control routes a `mailto:` to our composer and an http
 * URL to a hardened new tab, and that the completed toolbar exists and is
 * wired.
 *
 * jsdom implements neither `<dialog>`'s modal behaviour nor `window.print`;
 * both are stubbed with the one behaviour the component logic reads, and what
 * only a browser can verify (the focus trap, that printing prints one message)
 * is left to a browser.
 */
if (typeof HTMLDialogElement !== "undefined") {
  HTMLDialogElement.prototype.showModal = function showModal(this: HTMLDialogElement) {
    this.open = true;
  };
  HTMLDialogElement.prototype.close = function close(this: HTMLDialogElement) {
    if (!this.open) return;
    this.open = false;
    this.dispatchEvent(new Event("close"));
  };
}

function mailbox(id: string, role: Mailbox["role"], name: string): Mailbox {
  return {
    id,
    name,
    parentId: null,
    role,
    sortOrder: 0,
    totalEmails: 1,
    unreadEmails: 0,
    totalThreads: 1,
    unreadThreads: 0,
    isSubscribed: true,
    myRights: {
      mayReadItems: true,
      mayAddItems: true,
      mayRemoveItems: true,
      maySetSeen: true,
      maySetKeywords: true,
      mayCreateChild: true,
      mayRename: true,
      mayDelete: true,
      maySubmit: true,
    },
  };
}

const MAILBOXES = [
  mailbox("inbox", "inbox", "Inbox"),
  mailbox("junk", "junk", "Junk"),
  mailbox("archive", "archive", "Archive"),
];

function message(overrides: Partial<Email> = {}): Email {
  return {
    id: "m1",
    blobId: "b1",
    mailboxIds: { inbox: true },
    keywords: {},
    subject: "A subject",
    from: [{ name: "Ana", email: "ana@example.com" }],
    receivedAt: "2026-08-20T10:00:00Z",
    // A body with a remote image, so the block/unblock path is exercised.
    htmlBody: [
      {
        partId: "1",
        blobId: null,
        size: 20,
        name: null,
        type: "text/html",
        charset: "utf-8",
        disposition: null,
        cid: null,
        language: null,
        location: null,
      },
    ],
    bodyValues: {
      "1": {
        value: '<p>hi</p><img src="https://tracker.example/pixel.gif" alt="">',
        isEncodingProblem: false,
        isTruncated: false,
      },
    },
    ...overrides,
  };
}

function renderPane(
  overrides: Partial<ReadingPaneProps> = {},
  /*
   * prefs v2: the reader reads `defaultReplyBehavior` from the provider rather
   * than from a prop, so a test that cares about it seeds one. Omitted, there
   * is NO provider and `usePrefs` returns its documented fallback — which is
   * why every case below keeps working unchanged, at Gmail's own default.
   */
  prefs?: Prefs,
) {
  const props: ReadingPaneProps = {
    email: message(),
    thread: undefined,
    isLoading: false,
    error: undefined,
    onClose: vi.fn(),
    client: new JmapClient({ username: "u", password: "p" }),
    accountId: "a",
    onReply: vi.fn(),
    onReplyAll: vi.fn(),
    onForward: vi.fn(),
    onArchive: vi.fn(),
    onDelete: vi.fn(),
    deleteIsPermanent: false,
    onToggleFlag: vi.fn(),
    onMove: vi.fn(),
    onMarkUnread: vi.fn(),
    onToggleSpam: vi.fn(),
    onUnsubscribeByMail: vi.fn(),
    mailboxes: MAILBOXES,
    currentMailboxId: "inbox",
    inJunk: false,
    onNextMessage: vi.fn(),
    onPreviousMessage: vi.fn(),
    /*
     * E1: these cases exercise the SINGLE-MESSAGE reader, so conversation view
     * is off by default here. The conversation path has its own suite
     * (ConversationView.test.tsx); leaving it on would make every assertion
     * below depend on a thread fetch this suite does not stub.
     */
    conversationView: false,
    onReplyToMessage: vi.fn(),
    onForwardMessage: vi.fn(),
    onMarkMessagesRead: vi.fn(),
    onConversationControls: vi.fn(),
    ...overrides,
  };
  /*
   * The locale is PINNED to Spanish rather than left to jsdom's navigator.
   * Spanish is the pilot's language and the primary locale, and an assertion
   * that silently followed the environment's default would pass on a machine
   * where nobody had checked the Spanish strings existed at all.
   */
  const pane = <ReadingPane {...props} />;
  render(
    <I18nProvider locale="es">
      {prefs === undefined ? (
        pane
      ) : (
        <PrefsProvider
          client={undefined}
          session={undefined}
          accountId="a"
          initialPrefs={prefs}
        >
          {pane}
        </PrefsProvider>
      )}
    </I18nProvider>,
  );
  return props;
}

describe("the Spam banner and its consequences (canon §4.1.9)", () => {
  it("shows no banner in an ordinary folder", () => {
    renderPane();
    expect(screen.queryByText(/en Spam|in Spam/i)).not.toBeInTheDocument();
  });

  it("shows the banner and a Not-spam action in Junk", () => {
    renderPane({ inJunk: true, currentMailboxId: "junk" });
    expect(screen.getByText("Este mensaje está en Spam")).toBeInTheDocument();
    // Twice on purpose: once in the banner (where the user is looking) and
    // once in the toolbar (where the verb lives in every other folder).
    expect(screen.getAllByRole("button", { name: /no es spam/i })).toHaveLength(2);
  });

  /*
   * THE security-relevant acceptance criterion. In Spam the images are not
   * merely blocked-with-an-offer: there must be NO control at all, because a
   * remote fetch from a message in Spam is a delivery receipt to a spammer.
   */
  it("offers the remote-image unblock outside Junk", () => {
    renderPane();
    expect(screen.getByRole("button", { name: /mostrar imágenes/i })).toBeInTheDocument();
  });

  it("removes the remote-image unblock entirely inside Junk", () => {
    renderPane({ inJunk: true, currentMailboxId: "junk" });
    expect(screen.queryByRole("button", { name: /mostrar imágenes/i })).not.toBeInTheDocument();
  });

  it("still SAYS the images are hidden in Junk, rather than going silent", () => {
    renderPane({ inJunk: true, currentMailboxId: "junk" });
    // Silence would read as a rendering bug; the count and the reason stay.
    expect(screen.getByText(/nunca se cargan/i)).toBeInTheDocument();
  });

  it("labels the spam control 'Not spam' in Junk and 'Report spam' outside", async () => {
    const user = userEvent.setup();
    const props = renderPane();
    await user.click(screen.getByRole("button", { name: /^marcar como spam$/i }));
    expect(props.onToggleSpam).toHaveBeenCalledTimes(1);
  });
});

describe("the completed toolbar (E2 item 3)", () => {
  /*
   * P0-6 rebuilt this row as icons with the rest in a ⋮ (canon 07 §6), so
   * where each verb LIVES is part of what these tests pin: the eight the canon
   * names are one click away, and nothing was lost in the move.
   */
  it("puts the canonical verbs in the icon row", () => {
    renderPane();
    // "Back" is the header's ✕, not a second control in this row — see the
    // comment where the canon's ← would have gone.
    expect(screen.getByRole("button", { name: /^archivar$/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /spam/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /mover a la papelera|eliminar/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /marcar como no leído/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /mover a una carpeta/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /más acciones/i })).toBeInTheDocument();
  });

  it("keeps star, print and view-original — one click away, in the ⋮", async () => {
    const user = userEvent.setup();
    renderPane();
    // Not in the row: an icon for "view original" is a glyph nobody
    // recognises, which is what the menu is for.
    expect(screen.queryByRole("button", { name: /ver original/i })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /más acciones/i }));
    expect(screen.getByRole("menuitem", { name: /^destacar$/i })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /^imprimir$/i })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /ver original/i })).toBeInTheDocument();
  });

  it("keeps the reply verbs as WORDS — they are the reader's primary actions", () => {
    renderPane();
    expect(screen.getByRole("button", { name: /^responder$/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /responder a todos/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^reenviar$/i })).toBeInTheDocument();
  });

  it("keeps the row SHORT — the whole point of the change", () => {
    /*
     * The defect was sixteen text buttons wrapping into two rows in a 520px
     * reader. A count is a crude assertion, but it is the one that fails if
     * someone adds "just one more" verb back into the row — which is exactly
     * how the sixteen accumulated.
     */
    renderPane({ onSnooze: vi.fn(), onToggleMute: vi.fn(), onBlockSender: vi.fn() });
    const bar = screen.getByRole("toolbar", { name: /más acciones/i });
    expect(within(bar).getAllByRole("button").length).toBeLessThanOrEqual(9);
  });

  it("does not repeat the download-original under each message (C-04)", () => {
    /*
     * It used to render under EVERY message's attachment list, so a
     * six-message conversation put a diagnostic six times between the reader
     * and the next message. It lives once now, in the ⋮.
     */
    renderPane();
    expect(
      screen.queryByRole("button", { name: /descargar el mensaje original/i }),
    ).not.toBeInTheDocument();
  });

  /*
   * `aria-pressed` rather than only a flipped label: it is what tells a screen
   * reader whether the message is starred RIGHT NOW, where a label alone only
   * says what the next press would do.
   */
  it("announces the star as a toggle in its current state", async () => {
    const user = userEvent.setup();
    renderPane({ email: message({ keywords: { [KEYWORD_FLAGGED]: true } }) });
    await user.click(screen.getByRole("button", { name: /más acciones/i }));
    const star = screen.getByRole("menuitem", { name: /quitar el destaque/i });
    expect(star).toHaveAttribute("aria-pressed", "true");
  });

  it("calls window.print for the print item", async () => {
    const user = userEvent.setup();
    const print = vi.fn();
    vi.stubGlobal("print", print);
    renderPane();
    await user.click(screen.getByRole("button", { name: /más acciones/i }));
    await user.click(screen.getByRole("menuitem", { name: /^imprimir$/i }));
    expect(print).toHaveBeenCalledTimes(1);
    vi.unstubAllGlobals();
  });

  it("disables next/previous at the ends rather than hiding them", () => {
    renderPane({ onNextMessage: undefined, onPreviousMessage: undefined });
    expect(screen.getByRole("button", { name: /mensaje siguiente/i })).toBeDisabled();
    expect(screen.getByRole("button", { name: /mensaje anterior/i })).toBeDisabled();
  });

  it("navigates with the next/previous buttons when there is somewhere to go", async () => {
    const user = userEvent.setup();
    const props = renderPane();
    await user.click(screen.getByRole("button", { name: /mensaje siguiente/i }));
    expect(props.onNextMessage).toHaveBeenCalledTimes(1);
  });
});

describe("unsubscribe (E2 item 6)", () => {
  it("renders nothing when the message offers no List-Unsubscribe", () => {
    renderPane();
    expect(screen.queryByRole("button", { name: /cancelar la suscripción/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /cancelar la suscripción/i })).not.toBeInTheDocument();
  });

  it("opens the composer prefilled from a mailto: URI", async () => {
    const user = userEvent.setup();
    const props = renderPane({
      email: message({
        headers: [
          {
            name: "List-Unsubscribe",
            value: "<mailto:leave@list.example?subject=stop>",
          },
        ],
      }),
    });
    await user.click(screen.getByRole("button", { name: /cancelar la suscripción/i }));
    expect(props.onUnsubscribeByMail).toHaveBeenCalledWith("leave@list.example", "stop", undefined);
  });

  /*
   * A cross-origin unsubscribe link opened without `noopener` hands the
   * sender's page a handle on ours. Both tokens are asserted because either
   * one alone is insufficient in some engines.
   */
  it("opens an http-only unsubscribe in a hardened new tab", () => {
    renderPane({
      email: message({
        headers: [{ name: "List-Unsubscribe", value: "<https://list.example/u?k=1>" }],
      }),
    });
    const link = screen.getByRole("link", { name: /cancelar la suscripción/i });
    expect(link).toHaveAttribute("href", "https://list.example/u?k=1");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link.getAttribute("rel")).toContain("noopener");
    expect(link.getAttribute("rel")).toContain("noreferrer");
  });

  it("names the list in the accessible label when List-ID says so", () => {
    renderPane({
      email: message({
        headers: [
          { name: "List-Unsubscribe", value: "<https://l.example/u>" },
          { name: "List-ID", value: "Moov News <news.l.example>" },
        ],
      }),
    });
    expect(
      screen.getByRole("link", { name: /cancelar la suscripción a moov news/i }),
    ).toBeInTheDocument();
  });
});

/**
 * E4 — the reader's triage controls and the muted badge (canon §2.2).
 *
 * The badge and the button's wording read from the SAME flag, which is the
 * property worth pinning: a header saying "Silenciada" beside a button offering
 * to mute is the kind of contradiction a user cannot resolve.
 */
describe("E4: snooze and mute in the reader", () => {
  it("shows no triage controls when the server has no capability", () => {
    renderPane();
    expect(screen.queryByRole("button", { name: /posponer/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /silenciar/i })).toBeNull();
  });

  it("offers snooze and mute once the capability is wired", async () => {
    const user = userEvent.setup();
    renderPane({ onSnooze: vi.fn(), onToggleMute: vi.fn() });
    // P0-6: snooze keeps a slot in the icon row (canon 07 §6 lists it); mute
    // moved to the ⋮, where the verbs without a recognisable glyph live.
    expect(screen.getByRole("button", { name: /posponer hasta/i })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /más acciones/i }));
    expect(screen.getByRole("menuitem", { name: /^silenciar$/i })).toBeInTheDocument();
  });

  it("badges a muted conversation in WORDS, with the consequence spelled out", () => {
    renderPane({ onToggleMute: vi.fn(), isMuted: true });
    const badge = screen.getByText(/^silenciada$/i);
    // Not "this conversation is quiet" — the fact that matters is that its
    // replies skip the inbox entirely, and the title says exactly that.
    expect(badge).toHaveAttribute("title", expect.stringMatching(/saltean la bandeja/i));
  });

  it("keeps the badge and the menu item agreeing about the same fact", async () => {
    const user = userEvent.setup();
    renderPane({ onToggleMute: vi.fn(), isMuted: true });
    expect(screen.getByText(/^silenciada$/i)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /más acciones/i }));
    // The item says what the CLICK will do, which is the opposite of the state
    // the badge reports.
    expect(screen.getByRole("menuitem", { name: /dejar de silenciar/i })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /^silenciar$/i })).toBeNull();
  });

  it("shows no badge on an unmuted conversation", () => {
    renderPane({ onToggleMute: vi.fn(), isMuted: false });
    expect(screen.queryByText(/^silenciada$/i)).toBeNull();
  });

  it("offers unsnooze only where there is something snoozed", () => {
    renderPane({ onSnooze: vi.fn() });
    expect(screen.queryByRole("button", { name: /traer ahora/i })).toBeNull();

    renderPane({ onSnooze: vi.fn(), onUnsnooze: vi.fn() });
    expect(screen.getByRole("button", { name: /traer ahora/i })).toBeInTheDocument();
  });

  it("mutes the conversation on click", async () => {
    const user = userEvent.setup();
    const onToggleMute = vi.fn();
    renderPane({ onToggleMute });
    await user.click(screen.getByRole("button", { name: /más acciones/i }));
    await user.click(screen.getByRole("menuitem", { name: /^silenciar$/i }));
    expect(onToggleMute).toHaveBeenCalledTimes(1);
  });
});

describe("blocking the sender (E6, canon §2.2)", () => {
  it("is absent when the server has no filter capability", async () => {
    // A block writes a Sieve rule, so without the capability the control has
    // nothing to write — it is removed, not disabled. Checked INSIDE the open
    // menu (P0-6 moved it there), because an absence in a closed menu would
    // pass whatever the code did.
    const user = userEvent.setup();
    renderPane();
    await user.click(screen.getByRole("button", { name: /más acciones/i }));
    expect(
      screen.queryByRole("menuitem", { name: "Bloquear al remitente" }),
    ).not.toBeInTheDocument();
  });

  it("blocks the FROM address, which is the identity the reader shows", async () => {
    const user = userEvent.setup();
    const onBlockSender = vi.fn();
    renderPane({ onBlockSender });
    // P0-6: the block moved into the ⋮ — a verb with no glyph anyone reads.
    await user.click(screen.getByRole("button", { name: /más acciones/i }));
    await user.click(screen.getByRole("menuitem", { name: "Bloquear al remitente" }));
    expect(onBlockSender).toHaveBeenCalledWith("ana@example.com");
  });

  it("prefers From over Sender — a list posting must not block every author", async () => {
    const user = userEvent.setup();
    const onBlockSender = vi.fn();
    renderPane({
      onBlockSender,
      email: message({
        from: [{ name: "Ana", email: "Ana@Example.com" }],
        sender: [{ name: "La lista", email: "bounces@list.example" }],
      }),
    });
    // P0-6: the block moved into the ⋮ — a verb with no glyph anyone reads.
    await user.click(screen.getByRole("button", { name: /más acciones/i }));
    await user.click(screen.getByRole("menuitem", { name: "Bloquear al remitente" }));
    // Lowercased, because a header's casing is not identity.
    expect(onBlockSender).toHaveBeenCalledWith("ana@example.com");
  });

  it("falls back to Sender when there is no From at all", async () => {
    const user = userEvent.setup();
    const onBlockSender = vi.fn();
    renderPane({
      onBlockSender,
      email: message({ from: null, sender: [{ name: null, email: "s@example.com" }] }),
    });
    // P0-6: the block moved into the ⋮ — a verb with no glyph anyone reads.
    await user.click(screen.getByRole("button", { name: /más acciones/i }));
    await user.click(screen.getByRole("menuitem", { name: "Bloquear al remitente" }));
    expect(onBlockSender).toHaveBeenCalledWith("s@example.com");
  });

  it("renders nothing when neither header carries an address", async () => {
    // A block with nothing to block is not an action.
    const user = userEvent.setup();
    renderPane({ onBlockSender: vi.fn(), email: message({ from: null, sender: null }) });
    await user.click(screen.getByRole("button", { name: /más acciones/i }));
    expect(
      screen.queryByRole("menuitem", { name: "Bloquear al remitente" }),
    ).not.toBeInTheDocument();
  });
});

describe("suspicious mail outside Junk (E10, canon §4.1.15)", () => {
  const suspicious = () => message({ "moov:suspicious": true });

  it("shows the warning banner with a report-spam action", () => {
    renderPane({ email: suspicious() });
    expect(screen.getByText("Este mensaje parece spam")).toBeInTheDocument();
    // Twice on purpose, like Junk's pair: the banner (where the user is
    // looking) and the toolbar (where the verb always lives).
    expect(screen.getAllByRole("button", { name: /marcar como spam/i })).toHaveLength(2);
  });

  it("keeps every other affordance — the banner degrades, it does not hide", () => {
    renderPane({ email: suspicious() });
    // Reply stays primary; archive, delete, move all remain. Spot-check the
    // representative pair rather than the whole toolbar.
    expect(screen.getByRole("button", { name: "Responder" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Archivar" })).toBeInTheDocument();
  });

  it("removes the remote-image unblock entirely, exactly like Junk", () => {
    renderPane({ email: suspicious() });
    expect(screen.queryByRole("button", { name: /mostrar imágenes/i })).not.toBeInTheDocument();
    // The count is still stated — silence would read as a rendering bug.
    expect(screen.getByText(/imagen/i)).toBeInTheDocument();
  });

  it("wins over imagesPolicy 'always': the preference cannot override the stance", () => {
    renderPane({ email: suspicious(), autoLoadImages: true });
    const frame = document.querySelector("iframe");
    expect(frame).not.toBeNull();
    // The tracker URL from the fixture must be unfetchable: not in the doc.
    expect(frame?.getAttribute("srcdoc") ?? "").not.toContain("tracker.example");
  });

  it("does not double-banner inside Junk — the spam banner already says it", () => {
    renderPane({ email: suspicious(), inJunk: true, currentMailboxId: "junk" });
    expect(screen.getByText("Este mensaje está en Spam")).toBeInTheDocument();
    expect(screen.queryByText("Este mensaje parece spam")).not.toBeInTheDocument();
  });

  it("treats an absent verdict as clean — old cache entries and list rows", () => {
    renderPane(); // the fixture has no moov:suspicious at all
    expect(screen.queryByText("Este mensaje parece spam")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /mostrar imágenes/i })).toBeInTheDocument();
  });
});

describe("the sanitize chokepoint holds for cache-shaped bodies (E10 / D-4, E9b)", () => {
  /*
   * An offline body re-renders from IndexedDB as an Email object fed through
   * the SAME props as a network response — there is no separate "cached"
   * render path. This test feeds the reader a poisoned Email of exactly that
   * shape (script, event handler, external image) and pins that the rendered
   * frame contains none of it: a poisoned cache entry is still sanitized ON
   * RENDER, never trusted for having been stored by us.
   */
  it("sanitizes a poisoned body on render rather than trusting the cache", () => {
    renderPane({
      email: message({
        bodyValues: {
          "1": {
            value:
              '<p>ok</p><script>document.title="pwned"</script>' +
              '<img src="https://evil.example/x.gif" onerror="fetch(\'https://evil.example/c\')">' +
              '<div style="background: url(https://evil.example/css)">t</div>',
            isEncodingProblem: false,
            isTruncated: false,
          },
        },
      }),
    });
    const frame = document.querySelector("iframe");
    expect(frame).not.toBeNull();
    const doc = frame?.getAttribute("srcdoc") ?? "";
    expect(doc).not.toContain("<script");
    expect(doc).not.toContain("onerror");
    expect(doc).not.toContain("evil.example");
    expect(doc).toContain("ok");
    // The frame's sandbox is the layer that holds if the sanitizer fails.
    expect(frame?.getAttribute("sandbox")).toBe("allow-popups allow-popups-to-escape-sandbox");
  });
});

/**
 * `defaultReplyBehavior` (prefs v2, canon §2.3).
 *
 * Gmail's shape exactly: the preference chooses which reply is PRIMARY and the
 * other stays on screen as a secondary. The `r` key follows the same value in
 * `MailScreen`, so the button the eye lands on and the key the hand reaches for
 * always agree.
 */
describe("the default reply behaviour", () => {
  /** The two reply buttons, in DOM order, with the primary flagged. */
  function replyButtons(): readonly { label: string; isPrimary: boolean }[] {
    return screen
      .getAllByRole("button")
      .filter((button) => /^Responder( a todos)?$/.test(button.textContent ?? ""))
      .map((button) => ({
        label: button.textContent ?? "",
        // `classNameStrategy: "non-scoped"` keeps module class names literal in
        // the test environment, so the styling role is readable here.
        isPrimary: button.className.includes("primaryAction"),
      }));
  }

  it("makes plain reply primary by default — Gmail's own choice", () => {
    renderPane();
    expect(replyButtons()).toEqual([
      { label: "Responder", isPrimary: true },
      { label: "Responder a todos", isPrimary: false },
    ]);
  });

  it("promotes reply-all to primary when the preference says so", () => {
    renderPane({}, { ...DEFAULT_PREFS, defaultReplyBehavior: "replyAll" });
    expect(replyButtons()).toEqual([
      { label: "Responder a todos", isPrimary: true },
      { label: "Responder", isPrimary: false },
    ]);
  });

  it("NEVER removes the other verb — the setting moves emphasis, not controls", async () => {
    /*
     * P4: a preference must not make a control disappear when the action is
     * still available. Both handlers stay reachable in either configuration,
     * which is why this is an order swap and not a conditional render.
     */
    const user = userEvent.setup();
    const props = renderPane({}, { ...DEFAULT_PREFS, defaultReplyBehavior: "replyAll" });

    await user.click(screen.getByRole("button", { name: "Responder" }));
    expect(props.onReply).toHaveBeenCalledTimes(1);

    await user.click(screen.getByRole("button", { name: "Responder a todos" }));
    expect(props.onReplyAll).toHaveBeenCalledTimes(1);
  });
});

/**
 * C-06: the thread header — Gmail's double chevron and "N de M".
 *
 * The chevron is drawn from what the conversation PUBLISHES, so these run
 * the real ConversationView under the pane with a stubbed client answering
 * the thread's rows, the way ConversationView.test does.
 */
describe("C-06: the thread header", () => {
  function threadClient(rows: readonly Email[]): JmapClient {
    const client = new JmapClient({ username: "u", password: "p" });
    vi.spyOn(client, "call").mockImplementation((invocations) => {
      const [name, args, id] = invocations[0] as [string, Record<string, unknown>, string];
      const ids = (args.ids ?? []) as readonly string[];
      const list = rows.filter((row) => ids.includes(row.id));
      return Promise.resolve({ methodResponses: [[name, { list }, id]] } as never);
    });
    return client;
  }

  const older: Email = {
    ...message({ id: "m0", receivedAt: "2026-08-19T10:00:00Z", keywords: { $seen: true } }),
    threadId: "t1",
  };
  const newest: Email = { ...message({ keywords: { $seen: true } }), threadId: "t1" };

  it("offers the double chevron for a real conversation, flipping aria-expanded", async () => {
    const user = userEvent.setup();
    renderPane({
      conversationView: true,
      email: newest,
      thread: { id: "t1", emailIds: ["m0", "m1"] },
      client: threadClient([older, newest]),
    });

    const chevron = await screen.findByRole("button", { name: /^expandir todo$/i });
    expect(chevron).toHaveAttribute("aria-expanded", "false");
    // The old text strip is gone.
    expect(screen.queryByText(/conversación con/i)).not.toBeInTheDocument();

    await user.click(chevron);
    const collapse = await screen.findByRole("button", { name: /^contraer todo$/i });
    expect(collapse).toHaveAttribute("aria-expanded", "true");
  });

  it("grows no chevron for a single message", async () => {
    renderPane({
      conversationView: true,
      email: newest,
      thread: { id: "t1", emailIds: ["m1"] },
      client: threadClient([newest]),
    });
    await screen.findByText("Ana");
    expect(screen.queryByRole("button", { name: /expandir todo/i })).not.toBeInTheDocument();
  });

  it("states the position as 'N de M' beside the arrows", () => {
    renderPane({ listPosition: { index: 8, total: 15287 } });
    expect(screen.getByText("8 de 15.287")).toBeInTheDocument();
  });

  it("states the index alone when the server did not count", () => {
    renderPane({ listPosition: { index: 8 } });
    expect(screen.getByText("8")).toBeInTheDocument();
    expect(screen.queryByText(/8 de/)).not.toBeInTheDocument();
  });
});
