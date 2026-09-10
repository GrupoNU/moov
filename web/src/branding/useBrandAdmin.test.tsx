import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { BrandingProvider, useBranding } from "./BrandingProvider";
import { useBrandAdmin } from "./useBrandAdmin";

/**
 * The brand-admin controller and the provider refresh it drives.
 *
 * Two behaviours are tested here rather than in the section, because neither is
 * visible from inside a component that only renders:
 *
 *   1. **The probe decides whether the tab exists, and is asked once.** A
 *      request on the path of every visit to Settings, for an answer that is
 *      "no" for almost everyone and changes only when an operator runs a
 *      command, is a cost with no buyer.
 *   2. **A successful write repaints the app.** Leaving the old accent on
 *      screen after a save is indistinguishable from the save having failed —
 *      and this is the one screen in the app where a user changes what the app
 *      LOOKS like, so "it worked" has to be visible.
 */

const ADMIN_DOC = {
  host: "mail.acme.example",
  default: false,
  name: "Acme Mail",
  shortName: "Acme",
  tagline: "",
  supportUrl: "",
  privacyUrl: "",
  termsUrl: "",
  colors: { primary: "#5b5bd6", onPrimary: "", splashFrom: "#1e1b4b", splashTo: "#4c1d95" },
  assets: { logo: null, logoDark: null, icon: null, splash: null },
  iconSource: "default",
  iconIssue: "",
  brandAdmins: [],
  warnings: [],
  publicUrl: "/branding",
  manifestUrl: "/manifest.webmanifest",
  iconUrls: {},
  version: 1,
};

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/**
 * A fetch that answers each route from a table, and counts the calls.
 *
 * A table rather than a queue: the hook issues a probe, a read and then writes,
 * and asserting on ORDER would make every test brittle to a change that
 * reorders two independent requests.
 */
function routedFetch(
  routes: Readonly<Record<string, () => Response>>,
): { readonly fetchImpl: typeof fetch; readonly calls: string[] } {
  const calls: string[] = [];
  const fetchImpl = vi.fn((input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    // Every caller here passes a string path; `Request` and `URL` are spelled
    // out so the union cannot be stringified as "[object Object]" by accident.
    const url =
      typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    calls.push(`${init?.method ?? "GET"} ${url}`);
    // The method-qualified key wins, so a route can answer a GET and a PUT
    // differently without every other test having to spell out the method.
    const handler = routes[`${init?.method ?? "GET"} ${url}`] ?? routes[url];
    return Promise.resolve(handler === undefined ? new Response("", { status: 404 }) : handler());
  }) as unknown as typeof fetch;
  return { fetchImpl, calls };
}

/** Renders the hook and prints what the app is wearing beside it. */
function Probe({
  authorization,
  fetchImpl,
}: {
  readonly authorization: string;
  readonly fetchImpl: typeof fetch;
}): React.JSX.Element {
  const admin = useBrandAdmin({ authorization, fetchImpl });
  const branding = useBranding();
  return (
    <div>
      <span data-testid="doc">{admin.doc === undefined ? "none" : admin.doc.name}</span>
      <span data-testid="applied">{branding.name}</span>
      <span data-testid="error">{admin.error?.kind ?? "none"}</span>
      <button
        type="button"
        onClick={() => {
          void admin.save({ name: "Área Mail" });
        }}
      >
        save
      </button>
    </div>
  );
}

function renderProbe(fetchImpl: typeof fetch, authorization = "Basic dGVzdA=="): void {
  render(
    <BrandingProvider fetchImpl={fetchImpl}>
      <Probe authorization={authorization} fetchImpl={fetchImpl} />
    </BrandingProvider>,
  );
}

describe("the probe", () => {
  it("loads the document when the server says this user may edit", async () => {
    const { fetchImpl } = routedFetch({
      "/branding/admin": () => json({ host: "mail.acme.example", canEdit: true }),
      "/branding/admin/brand": () => json(ADMIN_DOC),
      "/branding": () => json({ name: "Acme Mail", default: false }),
    });
    renderProbe(fetchImpl);
    await waitFor(() => {
      expect(screen.getByTestId("doc")).toHaveTextContent("Acme Mail");
    });
  });

  it("answers 'no document' on a 404, and calls it no error", async () => {
    const { fetchImpl, calls } = routedFetch({
      "/branding": () => json({ name: "Moov Mail", default: true }),
    });
    renderProbe(fetchImpl);
    await waitFor(() => {
      expect(screen.getByTestId("error")).toHaveTextContent("none");
    });
    expect(screen.getByTestId("doc")).toHaveTextContent("none");
    // The read is never attempted after a refused probe.
    expect(calls.some((call) => call.includes("/branding/admin/brand"))).toBe(false);
  });

  it("does NOTHING at all without a credential — never on the login screen", async () => {
    const { fetchImpl, calls } = routedFetch({
      "/branding": () => json({ name: "Moov Mail", default: true }),
    });
    renderProbe(fetchImpl, "");
    await waitFor(() => {
      expect(screen.getByTestId("applied")).toHaveTextContent("Moov Mail");
    });
    /*
     * An unauthenticated probe of an admin route is an enumeration oracle for
     * "which hosts have brand administrators", so the absence of the request
     * is the property, not a saved round trip.
     */
    expect(calls.some((call) => call.includes("/branding/admin"))).toBe(false);
  });

  it("asks ONCE per credential, not once per mount of a settings screen", async () => {
    const { fetchImpl, calls } = routedFetch({
      "/branding/admin": () => json({ host: "h", canEdit: true }),
      "/branding/admin/brand": () => json(ADMIN_DOC),
      "/branding": () => json({ name: "Acme Mail", default: false }),
    });
    renderProbe(fetchImpl);
    await waitFor(() => {
      expect(screen.getByTestId("doc")).toHaveTextContent("Acme Mail");
    });
    expect(calls.filter((call) => call === "GET /branding/admin")).toHaveLength(1);
  });

  it("reports a real failure rather than reading a broken server as 'no access'", async () => {
    const { fetchImpl } = routedFetch({
      "/branding/admin": () => new Response("", { status: 500 }),
      "/branding": () => json({ name: "Moov Mail", default: true }),
    });
    renderProbe(fetchImpl);
    await waitFor(() => {
      expect(screen.getByTestId("error")).toHaveTextContent("network");
    });
  });
});

describe("a successful write repaints the app", () => {
  it("re-reads GET /branding so the new brand is worn without a reload", async () => {
    const user = userEvent.setup();
    let publicName = "Acme Mail";
    const { fetchImpl } = routedFetch({
      "/branding/admin": () => json({ host: "h", canEdit: true }),
      "/branding/admin/brand": () => {
        // The PUT answers with the WHOLE document, and the public one follows.
        publicName = "Área Mail";
        return json({ ...ADMIN_DOC, name: "Área Mail" });
      },
      "/branding": () => json({ name: publicName, default: false }),
    });
    renderProbe(fetchImpl);
    await waitFor(() => {
      expect(screen.getByTestId("applied")).toHaveTextContent("Acme Mail");
    });

    await user.click(screen.getByRole("button", { name: "save" }));

    await waitFor(() => {
      // Both halves: the editor's copy AND what the rest of the app is wearing.
      expect(screen.getByTestId("doc")).toHaveTextContent("Área Mail");
      expect(screen.getByTestId("applied")).toHaveTextContent("Área Mail");
    });
  });

  it("holds a failed write as state rather than rejecting at the call site", async () => {
    const user = userEvent.setup();
    const { fetchImpl } = routedFetch({
      "/branding/admin": () => json({ host: "h", canEdit: true }),
      "GET /branding/admin/brand": () => json(ADMIN_DOC),
      "PUT /branding/admin/brand": () => json({ field: "name", reason: "too long" }, 400),
      "/branding": () => json({ name: "Acme Mail", default: false }),
    });
    renderProbe(fetchImpl);
    await waitFor(() => {
      expect(screen.getByTestId("doc")).toHaveTextContent("Acme Mail");
    });

    await user.click(screen.getByRole("button", { name: "save" }));

    await waitFor(() => {
      expect(screen.getByTestId("error")).toHaveTextContent("invalidField");
    });
    // The document is untouched: a refused write changed nothing.
    expect(screen.getByTestId("doc")).toHaveTextContent("Acme Mail");
  });
});
