import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { JmapClient } from "../../api/jmap";
import { I18nProvider } from "../../i18n/I18nProvider";
import { KEYWORD_SEEN, type Email, type Thread } from "../../mail/types";
import { ConversationView, type ConversationControls } from "./ConversationView";

/**
 * The conversation reader in a real DOM (L3 epic E1, canon §2.1).
 *
 * The state machine's rules are enumerated in `mail/conversation.test.ts`;
 * what is proved HERE is what a pure module cannot be: that the thread is
 * actually fetched in two stages, that a collapsed message costs no body
 * request, that expanding one fetches exactly its own, and that read-marking
 * reaches the caller with only the expanded ids.
 *
 * The JMAP client is stubbed at `call`, which is the seam every fetch in this
 * component goes through — stubbing the api module's functions instead would
 * verify that the component calls a mock rather than that it makes the right
 * requests.
 */

const ACCOUNT = "a";

function email(id: string, receivedAt: string, overrides: Partial<Email> = {}): Email {
  return {
    id,
    threadId: "t1",
    mailboxIds: { inbox: true },
    keywords: { [KEYWORD_SEEN]: true },
    from: [{ name: `Sender ${id}`, email: `${id}@example.com` }],
    subject: "A subject",
    preview: `preview of ${id}`,
    receivedAt,
    ...overrides,
  };
}

/** A message with a body, as `Email/get` with bodyValues returns it. */
function withBody(base: Email, text: string): Email {
  return {
    ...base,
    textBody: [
      {
        partId: "1",
        blobId: null,
        size: text.length,
        name: null,
        type: "text/plain",
        charset: "utf-8",
        disposition: null,
        cid: null,
        language: null,
        location: null,
      },
    ],
    bodyValues: { "1": { value: text, isEncodingProblem: false, isTruncated: false } },
  };
}

const M1 = email("m1", "2026-08-01T10:00:00Z");
const M2 = email("m2", "2026-08-02T10:00:00Z");
const M3 = email("m3", "2026-08-03T10:00:00Z");
const THREAD: Thread = { id: "t1", emailIds: ["m1", "m2", "m3"] };

/**
 * A JMAP client whose `call` answers from a canned thread, recording every
 * request so a test can assert on what was ASKED, not only on what rendered.
 */
function stubClient(rows: readonly Email[], bodies: Record<string, Email>) {
  const calls: { method: string; args: Record<string, unknown> }[] = [];
  const client = new JmapClient({ username: "u", password: "p" });
  vi.spyOn(client, "call").mockImplementation((invocations) => {
    const [name, args, id] = invocations[0] as [string, Record<string, unknown>, string];
    calls.push({ method: name, args });
    const ids = (args.ids ?? []) as readonly string[];
    // The two stages differ by whether body values were requested — exactly
    // the distinction the component's two fetch functions encode.
    const wantsBodies = args.fetchTextBodyValues === true;
    const list = wantsBodies
      ? ids.map((wanted) => bodies[wanted]).filter((m): m is Email => m !== undefined)
      : rows.filter((row) => ids.includes(row.id));
    return Promise.resolve({ methodResponses: [[name, { list }, id]] } as never);
  });
  return { client, calls };
}

function renderConversation(
  overrides: {
    readonly openEmail?: Email;
    readonly thread?: Thread;
    readonly rows?: readonly Email[];
    readonly bodies?: Record<string, Email>;
    readonly onMarkRead?: (ids: readonly string[]) => void;
    readonly onControls?: (c: ConversationControls | undefined) => void;
  } = {},
) {
  const openEmail = overrides.openEmail ?? withBody(M3, "the newest message");
  const rows = overrides.rows ?? [M1, M2, M3];
  const bodies = overrides.bodies ?? {
    m1: withBody(M1, "the oldest message"),
    m2: withBody(M2, "the middle message"),
    m3: withBody(M3, "the newest message"),
  };
  const { client, calls } = stubClient(rows, bodies);

  const props = {
    openEmail,
    thread: overrides.thread ?? THREAD,
    client,
    accountId: ACCOUNT,
    signImageUrls: vi.fn().mockResolvedValue(new Map()),
    allowRemoteImages: true,
    autoLoadImages: false,
    onReply: vi.fn(),
    onForward: vi.fn(),
    onMarkRead: overrides.onMarkRead ?? vi.fn(),
    ...(overrides.onControls !== undefined ? { onControls: overrides.onControls } : {}),
  };

  render(
    <I18nProvider locale="es">
      <ConversationView {...props} />
    </I18nProvider>,
  );
  return { calls, props };
}

/** The ids whose FULL bodies were requested, across every recorded call. */
function bodyRequests(calls: { method: string; args: Record<string, unknown> }[]): string[] {
  return calls
    .filter((call) => call.args.fetchTextBodyValues === true)
    .flatMap((call) => (call.args.ids ?? []) as readonly string[]);
}

/**
 * The MESSAGE toggles that are open.
 *
 * Scoped to the message rows deliberately: the conversation bar's own
 * expand-all control also carries `aria-expanded`, so a bare role query counts
 * it too and every "how many are open" assertion is off by one. Scoping by the
 * article each toggle lives in is what makes the count mean what it says.
 */
function expandedMessages(): HTMLElement[] {
  return screen
    .getAllByRole("button", { expanded: true })
    .filter((button) => button.closest("[data-message-id]") !== null);
}

function collapsedMessages(): HTMLElement[] {
  return screen
    .getAllByRole("button", { expanded: false })
    .filter((button) => button.closest("[data-message-id]") !== null);
}

describe("the conversation reader", () => {
  it("renders every message of the thread, oldest first", async () => {
    renderConversation();
    // Canon §2.1: "the latest email at the bottom of a conversation thread".
    await waitFor(() => {
      expect(screen.getByText("Sender m1")).toBeInTheDocument();
    });
    const senders = screen.getAllByText(/^Sender m/).map((node) => node.textContent);
    expect(senders).toEqual(["Sender m1", "Sender m2", "Sender m3"]);
  });

  it("collapses everything except the newest", async () => {
    renderConversation();
    await waitFor(() => {
      expect(screen.getByText("Sender m1")).toBeInTheDocument();
    });
    // Each message's toggle carries its expanded state; only the newest is open.
    const toggles = collapsedMessages();
    expect(toggles.length).toBeGreaterThanOrEqual(2);
    expect(expandedMessages()).toHaveLength(1);
  });

  it("expands an unread message wherever it sits in the thread", async () => {
    const unreadMiddle = email("m2", "2026-08-02T10:00:00Z", { keywords: {} });
    renderConversation({ rows: [M1, unreadMiddle, M3] });
    await waitFor(() => {
      // Two expanded: the newest, and the unread one in the middle.
      expect(expandedMessages()).toHaveLength(2);
    });
  });

  it("does NOT fetch bodies for collapsed messages", async () => {
    // THE performance rule: the pilot's largest real thread has 24 messages.
    const { calls } = renderConversation();
    await waitFor(() => {
      expect(screen.getByText("Sender m1")).toBeInTheDocument();
    });
    // m3 arrives with its body already (the reader's own Email/get), so a
    // correct implementation requests NOTHING here.
    expect(bodyRequests(calls)).toEqual([]);
  });

  it("fetches exactly one body when one message is expanded", async () => {
    const user = userEvent.setup();
    const { calls } = renderConversation();
    await waitFor(() => {
      expect(screen.getByText("Sender m1")).toBeInTheDocument();
    });

    await user.click(screen.getByText("Sender m1"));

    await waitFor(() => {
      expect(bodyRequests(calls)).toEqual(["m1"]);
    });
    // And not the other collapsed one.
    expect(bodyRequests(calls)).not.toContain("m2");
  });

  it("asks the server for the thread's rows without body values", async () => {
    const { calls } = renderConversation();
    await waitFor(() => {
      expect(screen.getByText("Sender m1")).toBeInTheDocument();
    });
    const rowCall = calls.find((call) => call.args.fetchTextBodyValues !== true);
    expect(rowCall?.method).toBe("Email/get");
    expect(rowCall?.args.ids).toEqual(["m1", "m2", "m3"]);
  });
});

describe("read-marking", () => {
  it("marks only the messages that expanded, never the whole thread", async () => {
    const onMarkRead = vi.fn();
    const unreadThread = [
      email("m1", "2026-08-01T10:00:00Z", { keywords: {} }),
      email("m2", "2026-08-02T10:00:00Z", { keywords: {} }),
      email("m3", "2026-08-03T10:00:00Z", { keywords: {} }),
    ];
    renderConversation({
      openEmail: withBody(unreadThread[2]!, "newest"),
      rows: unreadThread,
      onMarkRead,
    });

    await waitFor(() => {
      expect(onMarkRead).toHaveBeenCalled();
    });
    /*
     * Every message here is unread, so `initialExpanded` opens all three and
     * all three are marked — which is correct AND is exactly why the next
     * test matters: with a read thread, only the newest is touched.
     */
    const marked = onMarkRead.mock.calls.flatMap((call) => call[0] as readonly string[]);
    expect(new Set(marked)).toEqual(new Set(["m1", "m2", "m3"]));
  });

  it("marks nothing when the whole thread is already read", async () => {
    const onMarkRead = vi.fn();
    renderConversation({ onMarkRead });
    await waitFor(() => {
      expect(screen.getByText("Sender m1")).toBeInTheDocument();
    });
    expect(onMarkRead).not.toHaveBeenCalled();
  });

  it("issues no write when an ALREADY-READ message is expanded", async () => {
    const user = userEvent.setup();
    const onMarkRead = vi.fn();
    // The complement of the rule above: read-marking is driven by expansion,
    // but it must still be a no-op for a message that is already read, or
    // every click in a thread would cost a pointless round trip.
    renderConversation({ onMarkRead });
    await waitFor(() => {
      expect(screen.getByText("Sender m1")).toBeInTheDocument();
    });
    await user.click(screen.getByText("Sender m1"));
    await waitFor(() => {
      expect(expandedMessages().length).toBeGreaterThan(1);
    });
    // Already read: expanding must not issue a pointless write.
    expect(onMarkRead).not.toHaveBeenCalled();
  });
});

describe("the conversation bar", () => {
  it("states the message count and expands everything on demand", async () => {
    const user = userEvent.setup();
    renderConversation();
    await waitFor(() => {
      expect(screen.getByText("Sender m1")).toBeInTheDocument();
    });

    const expandAll = screen.getByRole("button", { name: /expandir todo/i });
    await user.click(expandAll);

    await waitFor(() => {
      expect(expandedMessages()).toHaveLength(3);
    });
    // ...and the same control now collapses, leaving the newest open.
    await user.click(screen.getByRole("button", { name: /contraer todo/i }));
    await waitFor(() => {
      expect(expandedMessages()).toHaveLength(1);
    });
  });

  it("does not appear for a single-message conversation", async () => {
    renderConversation({
      openEmail: withBody(M3, "alone"),
      thread: { id: "t1", emailIds: ["m3"] },
      rows: [M3],
    });
    await waitFor(() => {
      expect(screen.getByText("Sender m3")).toBeInTheDocument();
    });
    // A control that says "1 message" and a button that does nothing.
    expect(screen.queryByRole("button", { name: /expandir todo/i })).not.toBeInTheDocument();
  });
});

describe("the keyboard controls it publishes", () => {
  it("hands the caller expandAll, collapseAll and goToMessage", async () => {
    let controls: ConversationControls | undefined;
    renderConversation({
      onControls: (next) => {
        if (next !== undefined) controls = next;
      },
    });
    await waitFor(() => {
      expect(controls).toBeDefined();
    });
    expect(typeof controls?.expandAll).toBe("function");
    expect(typeof controls?.collapseAll).toBe("function");
    expect(typeof controls?.goToMessage).toBe("function");
  });
});

describe("per-message actions", () => {
  it("replies to the message whose button was pressed, not to the thread", async () => {
    const user = userEvent.setup();
    const { props } = renderConversation();
    await waitFor(() => {
      expect(screen.getByText("Sender m1")).toBeInTheDocument();
    });

    // Expand the OLDEST message and reply from it.
    await user.click(screen.getByText("Sender m1"));
    await waitFor(() => {
      expect(screen.getAllByRole("button", { name: /^responder$/i }).length).toBeGreaterThan(1);
    });

    const replyButtons = screen.getAllByRole("button", { name: /^responder$/i });
    await user.click(replyButtons[0]!);

    // The FIRST reply button belongs to m1 — the oldest, rendered at the top.
    expect(props.onReply).toHaveBeenCalledWith(
      expect.objectContaining({ id: "m1" }),
      false,
    );
  });
});

describe("degrading honestly", () => {
  it("still shows the opened message when the thread's rows fail to load", async () => {
    const client = new JmapClient({ username: "u", password: "p" });
    vi.spyOn(client, "call").mockRejectedValue(new Error("network down"));

    render(
      <I18nProvider locale="es">
        <ConversationView
          openEmail={withBody(M3, "the newest message")}
          thread={THREAD}
          client={client}
          accountId={ACCOUNT}
          signImageUrls={vi.fn().mockResolvedValue(new Map())}
          allowRemoteImages
          autoLoadImages={false}
          onReply={vi.fn()}
          onForward={vi.fn()}
          onMarkRead={vi.fn()}
        />
      </I18nProvider>,
    );

    // Never a broken pane: the message the user opened is there, and the
    // failure is stated rather than presented as a one-message thread.
    await waitFor(() => {
      expect(screen.getByText("Sender m3")).toBeInTheDocument();
    });
    await waitFor(() => {
      expect(screen.getByText(/no se pudo cargar el resto/i)).toBeInTheDocument();
    });
  });
});
