import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { I18nProvider } from "../../i18n/I18nProvider";
import type { Mailbox } from "../../mail/types";
import { MailboxList } from "./MailboxList";

/** The sidebar's E2 "Empty trash now" affordance (item 7). */

function mailbox(
  id: string,
  role: Mailbox["role"],
  name: string,
  totalEmails = 4,
): Mailbox {
  return {
    id,
    name,
    parentId: null,
    role,
    sortOrder: role === "inbox" ? 1 : 2,
    totalEmails,
    unreadEmails: 0,
    totalThreads: totalEmails,
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
  mailbox("trash", "trash", "Trash"),
];

function renderSidebar(overrides: Record<string, unknown> = {}) {
  const onEmptyTrash = vi.fn();
  render(
    <I18nProvider locale="es">
      <MailboxList
        mailboxes={MAILBOXES}
        selectedId="trash"
        onSelect={vi.fn()}
        onEmptyTrash={onEmptyTrash}
        {...overrides}
      />
    </I18nProvider>,
  );
  return { onEmptyTrash };
}

describe("empty trash (E2 item 7)", () => {
  it("puts the affordance on the Trash row and nowhere else", () => {
    renderSidebar();
    const buttons = screen.getAllByRole("button", { name: /vaciar la papelera/i });
    expect(buttons).toHaveLength(1);
  });

  /*
   * The caller passes the handler only while Trash is on screen. A
   * permanently visible irreversible bulk destroy in a sidebar is a mis-click
   * waiting to happen, so its ABSENCE has to be as testable as its presence.
   */
  it("renders nothing when the caller does not offer it", () => {
    renderSidebar({ onEmptyTrash: undefined });
    expect(screen.queryByRole("button", { name: /vaciar la papelera/i })).not.toBeInTheDocument();
  });

  it("hands the Trash mailbox itself to the caller, which owns the confirmation", async () => {
    const user = userEvent.setup();
    const { onEmptyTrash } = renderSidebar();
    await user.click(screen.getByRole("button", { name: /vaciar la papelera/i }));
    expect(onEmptyTrash).toHaveBeenCalledWith(expect.objectContaining({ id: "trash" }));
  });

  it("is disabled on an already-empty Trash", () => {
    render(
      <I18nProvider locale="es">
        <MailboxList
          mailboxes={[mailbox("inbox", "inbox", "Inbox"), mailbox("trash", "trash", "Trash", 0)]}
          selectedId="trash"
          onSelect={vi.fn()}
          onEmptyTrash={vi.fn()}
        />
      </I18nProvider>,
    );
    expect(screen.getByRole("button", { name: /vaciar la papelera/i })).toBeDisabled();
  });

  it("says what it is doing, and refuses a second press, while it runs", () => {
    renderSidebar({ isEmptyingTrash: true });
    const button = screen.getByRole("button", { name: /vaciando la papelera/i });
    expect(button).toBeDisabled();
  });

  /*
   * The button is a SIBLING of the folder link, not a child of it: a button
   * inside an anchor is invalid HTML, and its click would also navigate.
   */
  it("does not nest the button inside the folder link", () => {
    renderSidebar();
    const button = screen.getByRole("button", { name: /vaciar la papelera/i });
    expect(button.closest("a")).toBeNull();
  });

  it("keeps the tree semantics intact", () => {
    renderSidebar();
    expect(screen.getByRole("tree")).toBeInTheDocument();
    /*
     * Three, not two: P0-5's "Más" is a treeitem like every other row, and it
     * is drawn here because Trash fell behind the collapse. That is the shape
     * the disclosure has to have — a row in the same list, at the same rhythm,
     * as Gmail's own.
     */
    expect(screen.getAllByRole("treeitem")).toHaveLength(3);
  });
});

/**
 * E4 — the three outgoing/deferred destinations, and why they stay three
 * (canon §2.2 and §2.3).
 */
describe("E4: the Snoozed folder and the Scheduled entry", () => {
  it("labels the Snoozed folder and does NOT need a role to find it", () => {
    // Dovecot supplies the name in English and RFC 6154 has no SPECIAL-USE
    // attribute for snoozed mail, so the sidebar recognises it by the NAME the
    // session capability published — never by a role that does not exist.
    renderSidebar({
      mailboxes: [...MAILBOXES, mailbox("snz", null, "Snoozed")],
      snoozedMailboxName: "Snoozed",
    });
    expect(screen.getByRole("link", { name: /pospuestos/i })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /^Snoozed$/ })).toBeNull();
  });

  it("leaves a folder alone when the session names a different one", () => {
    // A server that renamed its folder must not have some OTHER folder called
    // "Snoozed" relabelled and re-iconed as if it were the real one.
    renderSidebar({
      mailboxes: [...MAILBOXES, mailbox("snz", null, "Snoozed")],
      snoozedMailboxName: "Zzz",
    });
    expect(screen.getByRole("link", { name: /^Snoozed$/ })).toBeInTheDocument();
  });

  it("draws the Scheduled entry only when something is scheduled", () => {
    renderSidebar();
    expect(screen.queryByRole("button", { name: /programados/i })).toBeNull();

    renderSidebar({ scheduled: { count: 2, isSelected: false, onSelect: vi.fn() } });
    expect(screen.getByRole("button", { name: /programados/i })).toBeInTheDocument();
  });

  it("keeps Outbox and Scheduled as TWO entries, never merged", () => {
    /*
     * Both list mail that has not gone out, and merging them would make "why is
     * this still here?" have two different answers: the Outbox is local to this
     * browser and drains when the network returns, while a scheduled send is a
     * server-side submission other devices can see and cancel.
     */
    renderSidebar({
      outbox: { count: 1, hasFailures: false, isSelected: false, onSelect: vi.fn() },
      scheduled: { count: 2, isSelected: false, onSelect: vi.fn() },
    });
    expect(screen.getByRole("button", { name: /bandeja de salida/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /programados/i })).toBeInTheDocument();
  });

  it("navigates to Scheduled on click", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    renderSidebar({ scheduled: { count: 2, isSelected: false, onSelect } });
    await user.click(screen.getByRole("button", { name: /programados/i }));
    expect(onSelect).toHaveBeenCalledTimes(1);
  });
});

/**
 * "Destacados" and "Pospuestos" — the two rail entries the owner found missing
 * (canon 07 §2, finding 3).
 *
 * What is worth pinning is not that they render, but WHEN and WHERE: both are
 * always-visible entries whose position under Recibidos is the muscle memory,
 * and Pospuestos must not double up once its real folder exists.
 */
describe("the always-visible virtual entries", () => {
  it("shows Destacados, and immediately after Recibidos", () => {
    renderSidebar({ starred: { isSelected: false, onSelect: vi.fn() } });

    const names = screen.getAllByRole("treeitem").map((item) => item.textContent ?? "");
    // Moov's own name for the inbox is "Bandeja de entrada"; Gmail says
    // "Recibidos". That difference is not this test's subject — the ORDER is.
    const inbox = names.findIndex((name) => /bandeja de entrada/i.test(name));
    const starred = names.findIndex((name) => /destacados/i.test(name));
    expect(inbox).toBeGreaterThanOrEqual(0);
    expect(starred).toBeGreaterThanOrEqual(0);
    /*
     * Adjacency, not mere presence. Gmail puts Destacados directly under the
     * inbox, and "the one under Recibidos" is how a migrating user finds it —
     * appending it at the end of the rail would render the same row somewhere
     * the hand does not go.
     */
    expect(starred).toBe(inbox + 1);
  });

  it("navigates to Destacados on click", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    renderSidebar({ starred: { isSelected: false, onSelect } });
    await user.click(screen.getByRole("button", { name: /destacados/i }));
    expect(onSelect).toHaveBeenCalledTimes(1);
  });

  it("shows Pospuestos even though no Snoozed folder exists yet", () => {
    /*
     * The gap the placeholder exists for: GC-10 creates the Snoozed folder on
     * the first real snooze, so before then the tree has nothing to draw and
     * the entry was simply absent — where Gmail shows it always.
     */
    renderSidebar({ snoozedPlaceholder: { isSelected: false, onSelect: vi.fn() } });
    expect(screen.getByRole("button", { name: /pospuestos/i })).toBeInTheDocument();
  });

  it("draws Pospuestos ONCE when the real folder exists", () => {
    /*
     * The caller stops passing the placeholder as soon as the folder is in the
     * tree. Two rows both labelled "Pospuestos" — one routing to a real folder
     * and one to an empty state — is the failure this guards.
     */
    renderSidebar({
      mailboxes: [...MAILBOXES, mailbox("snz", null, "Snoozed")],
      snoozedMailboxName: "Snoozed",
    });
    /*
     * Counted across BOTH roles on purpose. A real folder row is a `link` (it
     * has a URL a middle-click can open) and the placeholder is a `button`, so
     * querying either role alone would miss exactly the duplicate this guards.
     */
    const rows = [
      ...screen.queryAllByRole("link", { name: /pospuestos/i }),
      ...screen.queryAllByRole("button", { name: /pospuestos/i }),
    ];
    expect(rows).toHaveLength(1);
  });

  it("omits both entries when the caller does not pass them", () => {
    // They are the caller's decision, not the list's: nothing here invents a
    // destination the shell has not wired.
    renderSidebar();
    expect(screen.queryByRole("button", { name: /destacados/i })).not.toBeInTheDocument();
    // Neither role: the fixture has no Snoozed folder either, so nothing at all
    // should name Pospuestos.
    expect(screen.queryByRole("button", { name: /pospuestos/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /pospuestos/i })).not.toBeInTheDocument();
  });
});

/**
 * P0-5 — the rail is curated, not dumped (canon 07 §2).
 *
 * The state these tests prevent is the one in the owner's screenshot: about
 * twenty-five IMAP folders in the sidebar, Calendario and Diario and Fuentes
 * RSS among them, "Problemas de sincronización" whose Conflictos child carried
 * a badge of 26 demanding attention about a folder holding no mail, and — with
 * the rail collapsed — twenty identical grey rectangles.
 *
 * The POLICY itself is tested in `mail/railCuration.test.ts`, on its own, as
 * data. What is tested here is what the component does with it.
 */
describe("P0-5: the curated rail", () => {
  const CURATED = [
    mailbox("inbox", "inbox", "Inbox"),
    mailbox("sent", "sent", "Sent"),
    mailbox("drafts", "drafts", "Drafts"),
    mailbox("archive", "archive", "Archive"),
    mailbox("trash", "trash", "Trash"),
    mailbox("cal", null, "Calendario"),
    mailbox("work", null, "Trabajo"),
  ];

  function renderCurated(overrides: Record<string, unknown> = {}) {
    render(
      <I18nProvider locale="es">
        <MailboxList
          mailboxes={CURATED}
          selectedId="inbox"
          onSelect={vi.fn()}
          {...overrides}
        />
      </I18nProvider>,
    );
  }

  it("shows the canonical rows and hides the rest behind Más", () => {
    renderCurated();
    expect(screen.getByRole("link", { name: /bandeja de entrada/i })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /^enviados/i })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /^borradores/i })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /^archivo/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /^papelera/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /trabajo/i })).not.toBeInTheDocument();
  });

  it("does not draw the system folders at all — they are not even in Más", async () => {
    const user = userEvent.setup();
    renderCurated();
    await user.click(screen.getByRole("button", { name: /^más$/i }));
    expect(screen.getByRole("link", { name: /^archivo/i })).toBeInTheDocument();
    // Calendario stays hidden: this is the whole point of the policy.
    expect(screen.queryByRole("link", { name: /calendario/i })).not.toBeInTheDocument();
  });

  it("opens Más on demand and closes it again", async () => {
    const user = userEvent.setup();
    renderCurated();
    const toggle = screen.getByRole("button", { name: /^más$/i });
    expect(toggle).toHaveAttribute("aria-expanded", "false");

    await user.click(toggle);
    expect(screen.getByRole("link", { name: /trabajo/i })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /^menos$/i }));
    expect(screen.queryByRole("link", { name: /trabajo/i })).not.toBeInTheDocument();
  });

  it("opens itself when the folder you are IN is behind the collapse", () => {
    /*
     * Otherwise navigating to Papelera — by keyboard, deep link, or deleting a
     * message — leaves no row marked current and the folder you are standing
     * in nowhere on screen.
     */
    renderCurated({ selectedId: "trash" });
    expect(screen.getByRole("link", { name: /^papelera/i })).toBeInTheDocument();
  });

  it("honours a stored choice over the policy, in both directions", async () => {
    const user = userEvent.setup();
    renderCurated({ folderVisibility: { Calendario: "show", Trabajo: "hide" } });
    await user.click(screen.getByRole("button", { name: /^más$/i }));
    expect(screen.getByRole("link", { name: /calendario/i })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /trabajo/i })).not.toBeInTheDocument();
  });

  it("keeps Pospuestos in Gmail's place — third, above Enviados", () => {
    /*
     * The Snoozed folder has no RFC 6154 role, so nothing structural puts it
     * there: the tree sorts unroled folders after every roled one, which would
     * land it below Borradores. Canon 07 §2 lists it third.
     */
    render(
      <I18nProvider locale="es">
        <MailboxList
          mailboxes={[...CURATED, mailbox("snz", null, "Snoozed")]}
          selectedId="inbox"
          onSelect={vi.fn()}
          snoozedMailboxName="Snoozed"
          starred={{ isSelected: false, onSelect: vi.fn() }}
        />
      </I18nProvider>,
    );
    const rows = screen
      .getAllByRole("treeitem")
      .map((item) => item.textContent ?? "");
    const index = (needle: RegExp): number => rows.findIndex((row) => needle.test(row));
    expect(index(/pospuestos/i)).toBeGreaterThan(index(/destacados/i));
    expect(index(/pospuestos/i)).toBeLessThan(index(/enviados/i));
  });

  it("keeps the outgoing rows ABOVE Más — they are urgent when they exist", () => {
    renderCurated({
      outbox: { count: 2, hasFailures: false, isSelected: false, onSelect: vi.fn() },
    });
    // Drawn without opening anything: mail that has not gone out must not be
    // one click further away than it was.
    expect(screen.getByRole("button", { name: /salida|outbox/i })).toBeInTheDocument();
  });

  it("collapses to icons WITHOUT unfolding Más into a wall of them", () => {
    // Canon 07 §2: the collapsed rail is a handful of distinct icons. A dozen
    // identical generic folder glyphs is the state being fixed.
    renderCurated({ collapsed: true, moreOpen: true });
    expect(screen.queryByRole("link", { name: /trabajo/i })).not.toBeInTheDocument();
  });

  it("disambiguates a custom folder colliding with a role's label", async () => {
    const user = userEvent.setup();
    renderCurated({ mailboxes: [...CURATED, mailbox("dup", null, "Archivo")] });
    await user.click(screen.getByRole("button", { name: /^más$/i }));
    // Two rows both reading exactly "Archivo" is the defect: the role keeps the
    // plain label, the custom one is qualified.
    expect(screen.getByRole("link", { name: /archivo \(carpeta\)/i })).toBeInTheDocument();
  });
});

/**
 * P0-5a — the virtual rows must not look like buttons.
 *
 * Four rail entries (Destacados, Pospuestos, Programados, Salida) have nothing
 * to link to, so they are `<button>`s among anchors — and a bare button brings
 * a border, a grey fill, the platform control font and centred text. The
 * owner's screenshot showed exactly that: grey boxes in a list of plain rows.
 *
 * Asserted against the STYLESHEET, because jsdom applies no UA stylesheet and
 * resolves no cascade: a render test would pass with the reset deleted.
 */
describe("P0-5a: the row reset", () => {
  it("neutralises the UA button chrome on the class both row kinds share", () => {
    const css = readFileSync(
      resolve(process.cwd(), "src/screens/mail/MailboxList.module.css"),
      "utf8",
    );
    const rule = /\.row \{([\s\S]*?)\}/.exec(css)?.[1] ?? "";
    expect(rule).toMatch(/border:\s*0/);
    expect(rule).toMatch(/background:\s*transparent/);
    expect(rule).toMatch(/font:\s*inherit/);
    expect(rule).toMatch(/text-align:\s*left/);
    expect(rule).toMatch(/width:\s*100%/);
  });
});

/**
 * A-09 — the counter is plain text, and the accent belongs to the active row.
 *
 * The reviewed rail drew a filled accent capsule beside every folder holding
 * unread mail, so a dozen rows carried a solid block of brand colour at once
 * and the one row that actually meant something — the folder you are in — had
 * no colour left to be distinguished by. Gmail draws a right-aligned grey
 * number and spends its accent exactly once.
 *
 * Asserted against the stylesheet for the reason the row reset above is: jsdom
 * resolves no cascade, so a render test cannot tell a capsule from a number.
 */
describe("A-09: the rail counters", () => {
  const css = readFileSync(
    resolve(process.cwd(), "src/screens/mail/MailboxList.module.css"),
    "utf8",
  );
  const badge = /\.badge \{([\s\S]*?)\}/.exec(css)?.[1] ?? "";
  const unreadBadge = /\.unread \.badge \{([\s\S]*?)\}/.exec(css)?.[1] ?? "";

  it("draws a right-aligned tabular grey number, with no pill behind it", () => {
    expect(badge).toMatch(/color:\s*var\(--text-muted\)/);
    expect(badge).toMatch(/text-align:\s*right/);
    // Tabular figures are what make the column a straight right edge rather
    // than a shimmering one as digit widths change.
    expect(badge).toMatch(/font-variant-numeric:\s*tabular-nums/);
    // The capsule: a fill and a full radius. Neither may come back.
    expect(badge).not.toMatch(/background/);
    expect(badge).not.toMatch(/border-radius/);
  });

  it("marks unread by WEIGHT only — never by an accent fill", () => {
    expect(unreadBadge).toMatch(/font-weight:\s*var\(--weight-semibold\)/);
    expect(unreadBadge).not.toMatch(/background/);
  });

  it("reserves the accent for the row you are on", () => {
    // The rule that painted a selected row's counter in the accent is gone
    // outright: `.selected` already carries a tint AND a spine, and a third
    // accented thing in the same row diluted the one signal that says "here".
    expect(css).not.toMatch(/\.selected \.badge \{/);
  });
});

/**
 * A-04 — one row looks active at a time.
 *
 * The review saw TWO rows reading as active and named a persistent focus ring
 * on the last-pressed row as the cause. The diagnosis had two halves and they
 * are fixed in different places, so both are pinned here.
 *
 * The ring: it is drawn by exactly one rule, `:focus-visible` in `base.css`,
 * which a pointer press never triggers. The regression to guard against is a
 * bare `:focus` appearing in this stylesheet — a single one would restore the
 * defect, because a mouse-clicked row would keep an outline until something
 * else was clicked, and it would sit beside a genuinely selected row.
 *
 * The tint: `.selected` was `--color-accent-tint`, ten per cent of the brand
 * over *transparent*, which against the rail's own surface was barely louder
 * than a hover — the review's "tenue". A state whose whole job is to answer
 * "where am I" cannot be a whisper, so it is a filled pill now.
 */
describe("A-04: the active row", () => {
  /*
   * Comments are stripped before anything is counted. This file explains its
   * own focus policy IN a comment, so a naive scan finds `:focus-visible`
   * twice and reports two rules where there is one — a test that fails on its
   * subject's documentation is a test nobody will trust the next time it goes
   * red.
   */
  const css = readFileSync(
    resolve(process.cwd(), "src/screens/mail/MailboxList.module.css"),
    "utf8",
  ).replace(/\/\*[\s\S]*?\*\//g, "");

  it("declares no focus styling outside `:focus-visible`", () => {
    /*
     * Matches `:focus` only when it is NOT the start of `:focus-visible` or
     * `:focus-within`. A plain `:focus` rule anywhere in this file re-creates
     * the sticky ring the review photographed.
     */
    expect(css).not.toMatch(/:focus(?!-visible|-within)/);
  });

  it("leaves the ONE ring rule to base.css rather than drawing its own", () => {
    // Two competing rings are how a ring ends up outliving its element's
    // focus. `.emptyTrash:focus-visible` is the file's only focus rule and is
    // deliberately narrow — it is a nested control, not a row.
    const focusRules = css.match(/:focus-visible/g) ?? [];
    expect(focusRules).toHaveLength(1);
    expect(css).toMatch(/\.emptyTrash:focus-visible/);
  });

  it("draws the selected row as a filled, opaque pill", () => {
    const selected = /\.selected \{([\s\S]*?)\}/.exec(css)?.[1] ?? "";
    // Mixed against a SURFACE, so the fill is opaque and its contrast does not
    // depend on what happens to scroll behind it — the same reason the compose
    // pill does it. The old `--color-accent-tint` was alpha over transparent.
    expect(selected).toMatch(
      /background:\s*color-mix\(in srgb,\s*var\(--color-accent\)\s*16%,\s*var\(--surface-default\)\)/,
    );
    expect(selected).not.toMatch(/var\(--color-accent-tint\)/);
    // A pill, not the rounded rectangle every other row gets — Gmail's shape,
    // and a second signal beyond the fill.
    expect(selected).toMatch(/border-radius:\s*var\(--radius-full\)/);
    expect(selected).toMatch(/font-weight:\s*var\(--weight-semibold\)/);
  });
});
