import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";

import { BrandingProvider } from "../branding/BrandingProvider";
import { MOOV_DEFAULT_BRANDING, type Branding } from "../branding/branding";
import { I18nProvider, useTranslation } from "./I18nProvider";

/**
 * `t` binds the host's brand, so that a call site writing
 * `t("filters.activate")` gets "Activar las reglas de Área Mail" without ever
 * having heard of branding.
 *
 * These tests exercise that seam through a real render, not through the table:
 * the point is that the brand reaches the string via the CONTEXT the app
 * actually mounts, and a unit test on `strings.ts` could not prove that.
 */

const AREA: Branding = {
  ...MOOV_DEFAULT_BRANDING,
  name: "Área Mail",
  shortName: "Área",
  isDefault: false,
};

function Probe({ testId = "out" }: { readonly testId?: string }): React.JSX.Element {
  const { t } = useTranslation();
  return (
    <div>
      <span data-testid={testId}>{t("error.serverError.title")}</span>
      <span data-testid="rules">{t("filters.activate")}</span>
      <span data-testid="plain">{t("login.submit")}</span>
    </div>
  );
}

function renderIn(branding: Branding, locale: "en" | "es"): void {
  render(
    <I18nProvider locale={locale}>
      <BrandingProvider branding={branding}>
        <Probe />
      </BrandingProvider>
    </I18nProvider>,
  );
}

describe("t is bound to the host's brand", () => {
  it("says the customer's name in Spanish, not Moov", () => {
    renderIn(AREA, "es");

    expect(screen.getByTestId("out")).toHaveTextContent(
      "Área Mail no está respondiendo en este momento",
    );
    expect(screen.getByTestId("rules")).toHaveTextContent(
      "Activar las reglas de Área Mail",
    );
    expect(screen.getByTestId("out").textContent).not.toContain("Moov");
  });

  it("says the customer's name in English, not Moov", () => {
    renderIn(AREA, "en");

    expect(screen.getByTestId("out")).toHaveTextContent("Área Mail is not answering right now");
    expect(screen.getByTestId("rules")).toHaveTextContent("Activate the Área Mail rules");
  });

  it("leaves plain strings untouched", () => {
    renderIn(AREA, "en");
    expect(screen.getByTestId("plain")).toHaveTextContent("Sign in");
  });

  /**
   * The login screen's outer `I18nProvider` renders ABOVE `BrandingProvider`,
   * so `t` has to work with no brand in scope. It resolves to Moov's own name
   * there, which is right: an unbranded install IS Moov.
   */
  it("falls back to Moov's own name when no brand is mounted", () => {
    render(
      <I18nProvider locale="en">
        <Probe />
      </I18nProvider>,
    );
    expect(screen.getByTestId("out")).toHaveTextContent("Moov Mail is not answering right now");
  });

  it("re-renders the strings when the brand changes", () => {
    const { rerender } = render(
      <I18nProvider locale="en">
        <BrandingProvider branding={MOOV_DEFAULT_BRANDING}>
          <Probe />
        </BrandingProvider>
      </I18nProvider>,
    );
    expect(screen.getByTestId("out")).toHaveTextContent("Moov Mail");

    rerender(
      <I18nProvider locale="en">
        <BrandingProvider branding={AREA}>
          <Probe />
        </BrandingProvider>
      </I18nProvider>,
    );
    expect(screen.getByTestId("out")).toHaveTextContent("Área Mail");
    expect(screen.getByTestId("out").textContent).not.toContain("Moov");
  });
});
