import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { I18nProvider } from "../../i18n/I18nProvider";
import { ConnectionPill } from "./ConnectionPill";
import { OutboxView } from "./OutboxView";
import type { OutboxItem } from "../../offline/outbox";
import type { DraftSpec } from "../../mail/write";

/**
 * The two surfaces E9 adds to the mail screen, in a real DOM.
 *
 * The state machine behind the Outbox is enumerated in `offline/outbox.test.ts`;
 * what is proved HERE is what a pure module cannot be — that a failure is
 * actually VISIBLE with the server's own words, that the buttons appear only
 * where they should, and that the connection pill is a live region whose
 * content changes rather than one inserted together with its own text.
 */

const spec: DraftSpec = {
  mailboxId: "drafts",
  from: [{ name: null, email: "me@example.com" }],
  to: [{ name: null, email: "you@example.com" }],
  cc: [],
  bcc: [],
  subject: "Presupuesto",
  text: "…",
  attachments: [],
};

function item(id: string, overrides: Partial<OutboxItem> = {}): OutboxItem {
  return {
    id,
    accountId: "acc",
    state: "queued",
    spec,
    identityId: "primary",
    sentMailboxId: "sent",
    queuedAt: 1,
    attempts: 0,
    lastError: undefined,
    subject: "Presupuesto",
    recipients: ["you@example.com"],
    ...overrides,
  };
}

function renderOutbox(
  items: readonly OutboxItem[],
  handlers: {
    onRetry?: (item: OutboxItem) => void;
    onDiscard?: (item: OutboxItem) => void;
  } = {},
): void {
  render(
    <I18nProvider locale="es">
      <OutboxView
        items={items}
        onRetry={handlers.onRetry ?? (() => undefined)}
        onDiscard={handlers.onDiscard ?? (() => undefined)}
      />
    </I18nProvider>,
  );
}

describe("OutboxView", () => {
  it("shows the recipient and subject of a queued message", () => {
    renderOutbox([item("1")]);

    expect(screen.getByText("you@example.com")).toBeInTheDocument();
    expect(screen.getByText("Presupuesto")).toBeInTheDocument();
    expect(screen.getByText("Esperando para enviarse")).toBeInTheDocument();
  });

  it("offers NO actions on a message that is going out on its own", () => {
    // Offering "try again" for a queued message invites a second send of one
    // already in flight.
    renderOutbox([item("1")]);

    expect(screen.queryByRole("button", { name: "Reintentar" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Descartar" })).not.toBeInTheDocument();
  });

  it("shows the server's OWN sentence on a failure, not a paraphrase", () => {
    /*
     * "Could not be sent" alone leaves the user with nothing to act on;
     * "550 no such user" tells them the address is wrong.
     */
    renderOutbox([item("1", { state: "failed", lastError: "550 no such user" })]);

    expect(screen.getByText("No se pudo enviar")).toBeInTheDocument();
    expect(screen.getByText("550 no such user")).toBeInTheDocument();
  });

  it("offers retry and discard on a failed message", async () => {
    const onRetry = vi.fn();
    const onDiscard = vi.fn();
    const failed = item("1", { state: "failed", lastError: "timeout" });
    renderOutbox([failed], { onRetry, onDiscard });

    await userEvent.click(screen.getByRole("button", { name: "Reintentar" }));
    expect(onRetry).toHaveBeenCalledWith(failed);

    await userEvent.click(screen.getByRole("button", { name: "Descartar" }));
    expect(onDiscard).toHaveBeenCalledWith(failed);
  });

  it("reports a message that is on its way", () => {
    renderOutbox([item("1", { state: "sending" })]);
    expect(screen.getByText("Enviando…")).toBeInTheDocument();
  });

  it("names a subject-less message rather than rendering a blank row", () => {
    renderOutbox([item("1", { subject: "" })]);
    expect(screen.getByText("(sin asunto)")).toBeInTheDocument();
  });

  it("explains itself when empty", () => {
    renderOutbox([]);
    expect(screen.getByText("No hay nada esperando para enviarse.")).toBeInTheDocument();
  });

  it("lists every queued message", () => {
    renderOutbox([item("1"), item("2"), item("3", { state: "failed" })]);
    expect(screen.getAllByText("Presupuesto")).toHaveLength(3);
  });
});

describe("ConnectionPill", () => {
  const renderPill = (state: "online" | "offline" | "reconnecting"): void => {
    render(
      <I18nProvider locale="es">
        <ConnectionPill state={state} />
      </I18nProvider>,
    );
  };

  it("says nothing while the connection works", () => {
    renderPill("online");
    expect(screen.queryByText(/Sin conexión|Reconectando/)).not.toBeInTheDocument();
  });

  it("keeps the live region mounted even when empty", () => {
    /*
     * The property that makes it announce at all: a live region inserted
     * TOGETHER with its own text is frequently never announced, because the
     * assistive tech did not observe it changing.
     */
    renderPill("online");
    const region = screen.getByRole("status");
    expect(region).toBeInTheDocument();
    expect(region).toBeEmptyDOMElement();
  });

  it("states the offline case in terms of what the user is seeing", () => {
    renderPill("offline");
    expect(screen.getByRole("status")).toHaveTextContent(
      "Sin conexión — mostrando datos guardados",
    );
  });

  it("promises recovery when it is only our stream that fell over", () => {
    renderPill("reconnecting");
    expect(screen.getByRole("status")).toHaveTextContent("Reconectando…");
  });

  it("announces politely rather than interrupting", () => {
    // `assertive` would cut a screen-reader user off mid-sentence for a
    // condition that is usually transient.
    renderPill("offline");
    expect(screen.getByRole("status")).toHaveAttribute("aria-live", "polite");
  });
});
