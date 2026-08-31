import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { I18nProvider } from "../../i18n/I18nProvider";
import type { Quota } from "../../mail/filters";
import { QuotaRow } from "./QuotaRow";

/**
 * The storage bar (E6, RFC 9425).
 *
 * The state that matters is "no limit". `Quota/get` answers an EMPTY LIST for
 * an account without limits, on purpose — "fabricating an infinite one would be
 * an invented number". A bar drawn at 0% would be that invention rendered, so
 * the test that a bar is ABSENT is the one carrying the epic's honesty rule.
 */

function quota(overrides: Partial<Quota> = {}): Quota {
  return {
    id: "storage",
    resourceType: "octets",
    used: 1024 * 1024 * 1024,
    hardLimit: 5 * 1024 * 1024 * 1024,
    name: "User quota",
    ...overrides,
  };
}

function renderRow(props: React.ComponentProps<typeof QuotaRow>) {
  render(
    <I18nProvider locale="es">
      <QuotaRow {...props} />
    </I18nProvider>,
  );
}

describe("with a limit", () => {
  it("draws a real <progress>, so the value is announced without an aria scaffold", () => {
    renderRow({ quotas: [quota()] });
    const bar = screen.getByRole("progressbar", { name: "Almacenamiento" });
    expect(bar).toHaveValue(1024 * 1024 * 1024);
  });

  it("states the used and total figures, and the percentage", () => {
    renderRow({ quotas: [quota()] });
    expect(screen.getByText(/1\.0 GB de 5\.0 GB usados/)).toBeInTheDocument();
    expect(screen.getByText(/20% ocupado/)).toBeInTheDocument();
  });

  it("clamps a mailbox over its limit at 100% rather than showing 137%", () => {
    renderRow({ quotas: [quota({ used: 7 * 1024 * 1024 * 1024 })] });
    expect(screen.getByText(/100% ocupado/)).toBeInTheDocument();
  });
});

describe("with NO limit — the state that must not become a fake bar", () => {
  it("renders the sentence and no bar at all for an empty list", () => {
    renderRow({ quotas: [] });
    expect(screen.getByText("Este buzón no tiene límite de almacenamiento.")).toBeInTheDocument();
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
  });

  it("does the same for a zero hardLimit, which cannot be divided by", () => {
    renderRow({ quotas: [quota({ hardLimit: 0 })] });
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
  });

  it("ignores a message-count quota when there is no storage one", () => {
    // §3.2 defines both resource types; only STORAGE is what this bar means.
    renderRow({ quotas: [quota({ id: "message", resourceType: "count" })] });
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
  });
});

describe("the failure states", () => {
  it("says the figure could not be read, and offers a retry", async () => {
    const user = userEvent.setup();
    const onRefresh = vi.fn();
    renderRow({ quotas: undefined, error: "IMAP said no", onRefresh });
    expect(screen.getByRole("alert")).toHaveTextContent("No se pudo leer el almacenamiento");
    await user.click(screen.getByRole("button", { name: "Actualizar" }));
    expect(onRefresh).toHaveBeenCalledTimes(1);
  });

  it("shows a loading state rather than an empty bar while the read is in flight", () => {
    renderRow({ quotas: undefined });
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
    expect(screen.getByText("Cargando…")).toBeInTheDocument();
  });
});
