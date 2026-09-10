import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { BrandingProvider } from "../branding/BrandingProvider";
import { MOOV_DEFAULT_BRANDING, mergeBranding, type Branding } from "../branding/branding";
import { I18nProvider } from "../i18n/I18nProvider";
import { en } from "../i18n/strings";
import { LegalFooter } from "./LegalFooter";
import { MOOV_LICENSE_URL, MOOV_REPO_URL, sourceUrlForCommit } from "./legalLinks";

/**
 * The legal footer.
 *
 * The three fixed links are a LICENSE obligation (AGPL-3.0 §13), so the tests
 * that matter most here are the ones asserting a brand cannot make them
 * disappear — a passing "renders on the default brand" would say nothing about
 * the case the clause exists for, which is a customer who rebranded Moov.
 */

const here = dirname(fileURLToPath(import.meta.url));
const css = (): string => readFileSync(resolve(here, "LegalFooter.module.css"), "utf8");

function brand(overrides: Partial<Branding> = {}): Branding {
  return { ...MOOV_DEFAULT_BRANDING, name: "ACME Mail", isDefault: false, ...overrides };
}

function renderFooter(branding: Branding = MOOV_DEFAULT_BRANDING) {
  return render(
    <I18nProvider locale="en">
      <BrandingProvider branding={branding}>
        <LegalFooter placement="login" />
      </BrandingProvider>
    </I18nProvider>,
  );
}

describe("the source offer (AGPL-3.0 §13)", () => {
  it("shows attribution, source and license on Moov's own brand", () => {
    renderFooter();

    expect(screen.getByRole("link", { name: en["legal.poweredBy"] })).toHaveAttribute(
      "href",
      MOOV_REPO_URL,
    );
    expect(screen.getByRole("link", { name: en["legal.sourceCode"] })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: en["legal.license"] })).toHaveAttribute(
      "href",
      MOOV_LICENSE_URL,
    );
  });

  it("shows the same three on a fully custom brand", () => {
    // The case the clause is FOR: an operator who rebranded the product still
    // owes their users the source. Nothing in the branding document can
    // suppress these three.
    renderFooter(
      brand({
        name: "ACME Mail",
        logoUrl: "/branding/assets/acme/logo.png",
        supportUrl: "mailto:it@acme.test",
        privacyUrl: "https://acme.test/privacy",
        termsUrl: "https://acme.test/terms",
      }),
    );

    expect(screen.getByRole("link", { name: en["legal.poweredBy"] })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: en["legal.sourceCode"] })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: en["legal.license"] })).toBeInTheDocument();
  });

  it("points the source link at the commit that built the bundle", () => {
    renderFooter();

    const link = screen.getByRole("link", { name: en["legal.sourceCode"] });
    const href = link.getAttribute("href") ?? "";
    // `__MOOV_COMMIT__` is inlined by Vite; under Vitest it is whatever the
    // same config produced, so the assertion is on the SHAPE rather than on a
    // literal hash that would change with every commit.
    expect(href === MOOV_REPO_URL || /^https:\/\/github\.com\/GrupoNU\/moov\/tree\/[0-9a-f]{7,40}$/.test(href)).toBe(
      true,
    );
    // The commit is evidence, and evidence has to be readable: it rides in the
    // title, not in the link text.
    expect(link.getAttribute("title")).toMatch(/^Build /);
  });

  it("builds a tree URL for a commit and falls back to the repo for anything else", () => {
    expect(sourceUrlForCommit("a1b2c3d")).toBe(`${MOOV_REPO_URL}/tree/a1b2c3d`);
    expect(sourceUrlForCommit("  a1b2c3d4e5  ")).toBe(`${MOOV_REPO_URL}/tree/a1b2c3d4e5`);
    // A source tarball built outside a checkout: a /tree/dev link would 404,
    // and a 404 is a worse offer than the repository root.
    expect(sourceUrlForCommit("dev")).toBe(MOOV_REPO_URL);
    expect(sourceUrlForCommit("")).toBe(MOOV_REPO_URL);
    // Nothing that is not a git object name reaches the URL path.
    expect(sourceUrlForCommit("../../evil")).toBe(MOOV_REPO_URL);
  });

  it("opens every external link without handing over window.opener", () => {
    renderFooter(brand({ privacyUrl: "https://acme.test/privacy", termsUrl: "https://acme.test/terms" }));

    for (const link of screen.getAllByRole("link")) {
      expect(link).toHaveAttribute("target", "_blank");
      expect(link.getAttribute("rel")).toContain("noopener");
      expect(link.getAttribute("rel")).toContain("noreferrer");
    }
  });
});

describe("the operator's own links", () => {
  it("shows neither Privacy nor Terms when the brand configured none", () => {
    renderFooter();

    expect(screen.queryByRole("link", { name: en["legal.privacy"] })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: en["legal.terms"] })).not.toBeInTheDocument();
  });

  it("shows each one only when it is configured", () => {
    const { unmount } = renderFooter(brand({ privacyUrl: "https://acme.test/privacy" }));
    expect(screen.getByRole("link", { name: en["legal.privacy"] })).toHaveAttribute(
      "href",
      "https://acme.test/privacy",
    );
    expect(screen.queryByRole("link", { name: en["legal.terms"] })).not.toBeInTheDocument();
    unmount();

    renderFooter(brand({ termsUrl: "https://acme.test/terms" }));
    expect(screen.getByRole("link", { name: en["legal.terms"] })).toHaveAttribute(
      "href",
      "https://acme.test/terms",
    );
    expect(screen.queryByRole("link", { name: en["legal.privacy"] })).not.toBeInTheDocument();
  });

  it("drops a hostile URL at the merge, so it never reaches an href", () => {
    // The defence lives in mergeBranding rather than in this component, which
    // is why it is asserted there AND here: the footer trusts what the merge
    // produced, so the merge is what has to be total.
    const merged = mergeBranding({
      name: "ACME",
      privacyUrl: "javascript:alert(1)",
      termsUrl: "data:text/html,<script>alert(1)</script>",
    });
    expect(merged.privacyUrl).toBe("");
    expect(merged.termsUrl).toBe("");

    renderFooter(merged);
    expect(screen.queryByRole("link", { name: en["legal.privacy"] })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: en["legal.terms"] })).not.toBeInTheDocument();
  });

  it("keeps a legitimate http, https or mailto policy link", () => {
    expect(mergeBranding({ privacyUrl: "https://acme.test/p" }).privacyUrl).toBe(
      "https://acme.test/p",
    );
    expect(mergeBranding({ termsUrl: "http://acme.test/t" }).termsUrl).toBe("http://acme.test/t");
    expect(mergeBranding({ privacyUrl: "mailto:legal@acme.test" }).privacyUrl).toBe(
      "mailto:legal@acme.test",
    );
  });
});

describe("the height it costs the list", () => {
  /*
   * A CSS invariant rather than a rendered measurement: jsdom computes no
   * layout, so the only honest place to assert "this line never grows into a
   * message row" is the stylesheet that promises it.
   */
  it("caps the list placement at one row and never lets it flex", () => {
    const sheet = css();
    const listBlock = sheet.slice(sheet.indexOf(".list {"), sheet.indexOf("}", sheet.indexOf(".list {")));
    expect(listBlock).toMatch(/min-height:\s*24px/);
    // `flex: none` is on the shared `.footer` block; without it the footer
    // would be a flex item that grows, which is exactly how a footer eats a
    // list.
    const footerBlock = sheet.slice(
      sheet.indexOf(".footer {"),
      sheet.indexOf("}", sheet.indexOf(".footer {")),
    );
    expect(footerBlock).toMatch(/flex:\s*none/);
    expect(footerBlock).toMatch(/font-size:\s*var\(--text-xs\)/);
    expect(footerBlock).toMatch(/color:\s*var\(--text-muted\)/);
  });
});
