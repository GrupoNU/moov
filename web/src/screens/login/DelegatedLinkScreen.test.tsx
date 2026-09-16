import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { DelegatedFailure } from "../../auth/AuthProvider";
import { BrandingProvider } from "../../branding/BrandingProvider";
import { I18nProvider } from "../../i18n/I18nProvider";
import { en, es } from "../../i18n/strings";
import { DelegatedLinkScreen } from "./DelegatedLinkScreen";

/**
 * The dead-link screen (epic M2, contract §3.7).
 *
 * The assertion that carries the contract is the NEGATIVE one: this screen
 * must never show a password field. A user who arrived through a portal has
 * no password — the mailbox was provisioned with a random one that was
 * discarded — so a form would invite them to fail at something impossible,
 * and §3.7 forbids it by name.
 */

function renderScreen(reason: DelegatedFailure, locale: "en" | "es" = "es"): void {
  render(
    <I18nProvider locale={locale}>
      <BrandingProvider>
        <DelegatedLinkScreen reason={reason} />
      </BrandingProvider>
    </I18nProvider>,
  );
}

const ALL_REASONS: DelegatedFailure[] = [
  { kind: "invalid" },
  { kind: "not-provisioned" },
  { kind: "unusable", code: "suspended" },
  { kind: "not-configured" },
  { kind: "unavailable" },
  { kind: "unavailable", retryAfterSeconds: 30 },
];

describe("the dead-link screen", () => {
  it("never offers a password field or a sign-in button, for any reason", () => {
    for (const reason of ALL_REASONS) {
      const { unmount } = render(
        <I18nProvider locale="es">
          <BrandingProvider>
            <DelegatedLinkScreen reason={reason} />
          </BrandingProvider>
        </I18nProvider>,
      );
      expect(document.querySelector('input[type="password"]')).toBeNull();
      expect(document.querySelector("form")).toBeNull();
      unmount();
    }
  });

  it("says the link expired and points at the portal (Spanish)", () => {
    renderScreen({ kind: "invalid" });
    // The exact wording the contract's §3.7 asks for.
    expect(screen.getAllByText(es["delegated.expired.title"]).length).toBeGreaterThan(0);
    expect(screen.getAllByText(es["delegated.expired.body"]).length).toBeGreaterThan(0);
    expect(es["delegated.expired.body"]).toContain("portal");
  });

  it("says the same thing in English", () => {
    renderScreen({ kind: "invalid" }, "en");
    expect(screen.getAllByText(en["delegated.expired.title"]).length).toBeGreaterThan(0);
    expect(en["delegated.expired.body"]).toContain("portal");
  });

  it("gives an unconfigured host the same copy as an expired link", () => {
    // The user cannot tell the two apart and should not have to: the remedy
    // is identical.
    renderScreen({ kind: "not-configured" });
    expect(screen.getAllByText(es["delegated.expired.title"]).length).toBeGreaterThan(0);
  });

  it("reuses the existing not-provisioned copy rather than inventing new words", () => {
    renderScreen({ kind: "not-provisioned" });
    // The brand-bound string, resolved with the default brand name.
    expect(
      screen.getAllByText(/no está habilitado en/i).length,
    ).toBeGreaterThan(0);
  });

  it("names the retry delay when the server sent one", () => {
    renderScreen({ kind: "unavailable", retryAfterSeconds: 30 });
    expect(screen.getAllByText(/30 segundos/).length).toBeGreaterThan(0);
  });

  it("explains a suspended mailbox as an administrator's problem", () => {
    renderScreen({ kind: "unusable", code: "suspended" });
    expect(screen.getAllByText(es["delegated.unusable.title"]).length).toBeGreaterThan(0);
    // The administrator hint is offered exactly where a user cannot act alone.
    expect(screen.getAllByText(es["login.contactAdministrator"]).length).toBeGreaterThan(0);
  });

  it("does NOT offer the administrator hint for an expired link", () => {
    // There is nothing an administrator can do about a link that timed out,
    // and offering the hint everywhere trains people to ignore it.
    renderScreen({ kind: "invalid" });
    expect(screen.queryByText(es["login.contactAdministrator"])).toBeNull();
  });
});

describe("both string tables", () => {
  it("carry every delegated key", () => {
    const keys = [
      "delegated.expired.title",
      "delegated.expired.body",
      "delegated.unusable.title",
      "delegated.unusable.body",
      "delegated.unavailable.title",
      "delegated.unavailable.body",
      "delegated.unavailable.bodyWithSeconds",
    ] as const;
    for (const key of keys) {
      expect(en[key], `en is missing ${key}`).toBeDefined();
      expect(es[key], `es is missing ${key}`).toBeDefined();
    }
  });
});
