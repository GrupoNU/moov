import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { JmapClient } from "../../api/jmap";
import { I18nProvider } from "../../i18n/I18nProvider";
import { KEYWORD_FLAGGED, type Email, type Mailbox } from "../../mail/types";
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

function renderPane(overrides: Partial<ReadingPaneProps> = {}) {
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
    ...overrides,
  };
  /*
   * The locale is PINNED to Spanish rather than left to jsdom's navigator.
   * Spanish is the pilot's language and the primary locale, and an assertion
   * that silently followed the environment's default would pass on a machine
   * where nobody had checked the Spanish strings existed at all.
   */
  render(
    <I18nProvider locale="es">
      <ReadingPane {...props} />
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
  it("offers star, move, mark-unread, print and view-original", () => {
    renderPane();
    expect(screen.getByRole("button", { name: /^destacar$/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /mover a una carpeta/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /marcar como no leído/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^imprimir$/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /ver original/i })).toBeInTheDocument();
  });

  /*
   * `aria-pressed` rather than only a flipped label: it is what tells a screen
   * reader whether the message is starred RIGHT NOW, where a label alone only
   * says what the next press would do.
   */
  it("announces the star as a toggle in its current state", () => {
    renderPane({ email: message({ keywords: { [KEYWORD_FLAGGED]: true } }) });
    const star = screen.getByRole("button", { name: /quitar el destaque/i });
    expect(star).toHaveAttribute("aria-pressed", "true");
  });

  it("calls window.print for the print button", async () => {
    const user = userEvent.setup();
    const print = vi.fn();
    vi.stubGlobal("print", print);
    renderPane();
    await user.click(screen.getByRole("button", { name: /^imprimir$/i }));
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
