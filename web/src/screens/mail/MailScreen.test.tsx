import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { AuthProvider } from "../../auth/AuthProvider";
import { I18nProvider } from "../../i18n/I18nProvider";
import { PrefsProvider } from "../../mail/PrefsProvider";
import { DEFAULT_PREFS } from "../../mail/prefs";
import { OfflineProvider } from "../../offline/OfflineProvider";
import { RouterProvider } from "../../router/RouterProvider";
import { MailScreen } from "./MailScreen";

/**
 * The shell's canary (E11).
 *
 * MailScreen is ~3,900 lines and, until this file, had ZERO component tests:
 * every one of its behaviours was covered only through the smaller components
 * it composes, or not at all. That is a gap no amount of unit coverage closes,
 * because the failures it admits are exactly the ones that only appear once
 * the pieces are wired together — a provider missing, a hook ordering change,
 * a client that never gets its session.
 *
 * # What this deliberately is and is not
 *
 * It is a CANARY, not a suite. It asserts that the shell mounts against a fake
 * server, renders the list, opens a message with Enter, and that the settings
 * gear walks its two-step path (E12: quick dock → full surface). Those cover
 * the shell's load-bearing seams: the JMAP plumbing, the keyboard layer's
 * connection to real state, and the settings surfaces.
 *
 * It is NOT an attempt to test the screen's behaviour exhaustively. The
 * behaviour lives in the components and hooks that already have their own
 * tests; duplicating them here through nine providers would be slow, brittle,
 * and would fail for reasons unrelated to what it claims to check.
 *
 * # Why the seam is `fetch`
 *
 * MailScreen builds its own `JmapClient` from stored credentials — deliberately
 * (P1: "which credential is this request using" must be answerable by
 * construction), which means there is no client prop to inject. The honest seam
 * is therefore the network itself: a fake server that answers the Session
 * request and the handful of JMAP methods the first paint makes.
 */

const ACCOUNT = "acct-1";

const SESSION = {
  capabilities: {
    "urn:ietf:params:jmap:core": { maxSizeUpload: 50_000_000 },
    "urn:ietf:params:jmap:mail": {},
  },
  accounts: {
    [ACCOUNT]: {
      name: "moov-test@example.test",
      isPersonal: true,
      isReadOnly: false,
      // Required by RFC 8620 §2 and always sent by our server. A first draft of
      // this fixture omitted it and the shell threw during render rather than
      // treating the feature as absent — the guard that now prevents that lives
      // in `mail/triage.ts`.
      accountCapabilities: { "urn:ietf:params:jmap:mail": {} },
    },
  },
  primaryAccounts: {
    "urn:ietf:params:jmap:mail": ACCOUNT,
    "urn:ietf:params:jmap:submission": ACCOUNT,
  },
  username: "moov-test@example.test",
  apiUrl: "http://localhost/jmap/api",
  downloadUrl: "http://localhost/jmap/download/{accountId}/{blobId}/{name}",
  uploadUrl: "http://localhost/jmap/upload/{accountId}",
  eventSourceUrl: "http://localhost/jmap/events",
  state: "s1",
};

const INBOX = {
  id: "mb-inbox",
  name: "Inbox",
  role: "inbox",
  parentId: null,
  sortOrder: 1,
  totalEmails: 2,
  unreadEmails: 1,
  totalThreads: 2,
  unreadThreads: 1,
  myRights: {},
  isSubscribed: true,
};

const EMAILS = [
  {
    id: "e1",
    threadId: "t1",
    mailboxIds: { "mb-inbox": true },
    keywords: {},
    subject: "The first message",
    preview: "Preview of the first",
    from: [{ name: "Ana", email: "ana@example.test" }],
    to: [{ name: null, email: "moov-test@example.test" }],
    receivedAt: "2026-08-30T10:00:00Z",
    size: 1024,
    hasAttachment: false,
  },
  {
    id: "e2",
    threadId: "t2",
    mailboxIds: { "mb-inbox": true },
    keywords: { $seen: true },
    subject: "The second message",
    preview: "Preview of the second",
    from: [{ name: "Beatriz", email: "bea@example.test" }],
    to: [{ name: null, email: "moov-test@example.test" }],
    receivedAt: "2026-08-30T09:00:00Z",
    size: 2048,
    hasAttachment: false,
  },
];

/** Answers one JMAP method call with something shaped like the real thing. */
function respond(name: string, args: Record<string, unknown>): Record<string, unknown> {
  switch (name) {
    case "Mailbox/get":
      return { accountId: ACCOUNT, state: "mb-1", list: [INBOX], notFound: [] };
    case "Email/query":
      return {
        accountId: ACCOUNT,
        queryState: "q-1",
        ids: EMAILS.map((email) => email.id),
        position: 0,
        total: EMAILS.length,
      };
    case "Email/get": {
      // Honour the requested ids so the reader's follow-up fetch resolves to
      // the message the list opened, not to the whole mailbox.
      const requested = args.ids;
      const ids = Array.isArray(requested) ? (requested as string[]) : undefined;
      const list =
        ids === undefined ? EMAILS : EMAILS.filter((email) => ids.includes(email.id));
      return {
        accountId: ACCOUNT,
        state: "e-1",
        list: list.map((email) => ({
          ...email,
          bodyValues: { 1: { value: "The body.", isEncodingProblem: false, isTruncated: false } },
          textBody: [{ partId: "1", type: "text/plain" }],
          htmlBody: [],
        })),
        notFound: [],
      };
    }
    case "Thread/get":
      return {
        accountId: ACCOUNT,
        state: "th-1",
        list: [
          { id: "t1", emailIds: ["e1"] },
          { id: "t2", emailIds: ["e2"] },
        ],
        notFound: [],
      };
    default:
      // Anything else the shell probes for (submission, vendor capabilities)
      // answers empty rather than erroring: an unimplemented extra must not
      // look like a broken server.
      return { accountId: ACCOUNT, state: "x", list: [], notFound: [] };
  }
}

function fakeFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;

  if (url.includes("/.well-known/jmap")) {
    return Promise.resolve(
      new Response(JSON.stringify(SESSION), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  }

  if (url.includes("/jmap/api")) {
    const raw = typeof init?.body === "string" ? init.body : "{}";
    const body = JSON.parse(raw) as {
      methodCalls?: [string, Record<string, unknown>, string][];
    };
    const methodResponses = (body.methodCalls ?? []).map(
      ([name, args, id]) => [name, respond(name, args), id] as const,
    );
    return Promise.resolve(
      new Response(JSON.stringify({ methodResponses, sessionState: "s1" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  }

  return Promise.resolve(new Response("{}", { status: 200 }));
}

/**
 * A no-op EventSource.
 *
 * The shell opens one for push. Left unstubbed, jsdom throws on construction
 * and the failure surfaces as an unrelated render error.
 */
class StubEventSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readonly readyState = 0;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  onopen: ((event: Event) => void) | null = null;
  // The shell only ever subscribes and closes; doing nothing is the whole
  // point, and each body says so rather than being empty.
  addEventListener(): void {
    return undefined;
  }
  removeEventListener(): void {
    return undefined;
  }
  close(): void {
    return undefined;
  }
}

function renderShell(): void {
  render(
    <I18nProvider>
      <AuthProvider>
        <RouterProvider>
          {/*
            `initialPrefs` is the provider's own documented test seam: it skips
            the prefs load, which is a JMAP round trip this canary is not about.
            The shell still gets a complete, valid set of preferences.
          */}
          <PrefsProvider
            client={undefined}
            session={undefined}
            accountId={ACCOUNT}
            initialPrefs={DEFAULT_PREFS}
          >
            <OfflineProvider accountId={ACCOUNT}>
              <MailScreen />
            </OfflineProvider>
          </PrefsProvider>
        </RouterProvider>
      </AuthProvider>
    </I18nProvider>,
  );
}

describe("MailScreen — the shell's canary", () => {
  beforeEach(() => {
    // The stored credential AuthProvider revalidates and MailScreen builds its
    // client from. Both read the same storage, which is what makes one write
    // here enough.
    window.sessionStorage.setItem(
      "moov.session.v1",
      JSON.stringify({ username: "moov-test@example.test", password: "secret" }),
    );
    vi.stubGlobal("fetch", vi.fn(fakeFetch));
    vi.stubGlobal("EventSource", StubEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    window.sessionStorage.clear();
  });

  it("mounts and renders the message list", async () => {
    renderShell();

    await waitFor(
      () => {
        expect(screen.getByText("The first message")).toBeInTheDocument();
      },
      { timeout: 5000 },
    );
    expect(screen.getByText("The second message")).toBeInTheDocument();
  });

  it("opens a message with Enter — the keyboard layer against real state", async () => {
    const user = userEvent.setup();
    renderShell();
    await waitFor(
      () => {
        expect(screen.getByText("The first message")).toBeInTheDocument();
      },
      { timeout: 5000 },
    );

    /*
     * Enter resolves through the SAME global handler the app installs, which
     * is the point: the resolver's own tests prove `Enter` means "open", and
     * this proves the shell is actually listening and has a selected row for
     * it to act on.
     */
    await user.keyboard("{Enter}");

    await waitFor(() => {
      // The reader shows the body; the list only ever showed the preview.
      expect(screen.getByText("The body.")).toBeInTheDocument();
    });
  });

  /**
   * E12: the gear is a TWO-STEP affordance now (canon 07 §1, §4).
   *
   * The gear opens the quick-settings dock; the dock's "See all settings" is
   * what opens the full surface. This walks both steps against the real shell,
   * because the wiring between them is the thing a unit test of either piece
   * alone cannot see — the panel does not know what a settings dialog is, and
   * the dialog does not know a panel exists.
   */
  it("opens quick settings from the gear, and the full surface from there", async () => {
    const user = userEvent.setup();
    renderShell();
    await waitFor(
      () => {
        expect(screen.getByText("The first message")).toBeInTheDocument();
      },
      { timeout: 5000 },
    );

    await user.click(screen.getByRole("button", { name: "Settings" }));

    const panel = await screen.findByRole("complementary", {
      name: "Quick settings",
    });
    /*
     * The dock is NOT a modal: the mail behind it must still be on screen,
     * which is the whole reason it is a docked panel rather than a dialog.
     *
     * `getAllBy`, because by this point in the file a message may be open and
     * the subject then appears in BOTH the list row and the reader's heading.
     * Asserting on exactly one would be asserting on the reading pane's state,
     * which is not what this test is about.
     */
    expect(screen.getAllByText("The first message").length).toBeGreaterThan(0);

    await user.click(
      within(panel).getByRole("button", { name: "See all settings" }),
    );

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Settings" })).toBeInTheDocument();
    });
    // The panel hands OFF rather than stacking: leaving a shrunken list behind
    // the sheet is a layout the user never asked for.
    expect(
      screen.queryByRole("complementary", { name: "Quick settings" }),
    ).not.toBeInTheDocument();
  });
});
