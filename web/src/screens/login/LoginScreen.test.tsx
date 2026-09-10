import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { ApiError } from "../../api/errors";
import { AuthProvider } from "../../auth/AuthProvider";
import { BrandingProvider } from "../../branding/BrandingProvider";
import { MOOV_DEFAULT_BRANDING, type Branding } from "../../branding/branding";
import { I18nProvider } from "../../i18n/I18nProvider";
import { brandName, en } from "../../i18n/strings";
import { LoginScreen } from "./LoginScreen";
import type { JmapSession } from "../../api/jmap";

/**
 * Component tests for the login screen.
 *
 * These cover the ACs a unit test can honestly verify — labels, roles, focus
 * order, keyboard operation, and the actionable-error mapping reaching the
 * screen. What they cannot verify (that the split screen visually splits, that
 * a brand asset really loads over the network) is verified with Playwright
 * against the live pilot.
 */

/** A minimal Session, enough for the authenticated transition. */
const fakeSession: JmapSession = {
  capabilities: {},
  accounts: {},
  primaryAccounts: {},
  username: "moov-test@atmosfera.cloud",
  apiUrl: "/jmap/api",
  downloadUrl: "",
  uploadUrl: "",
  eventSourceUrl: "",
  state: "s1",
};

/** In-memory storage so tests never touch the real sessionStorage. */
function memoryStorage(): Storage {
  const map = new Map<string, string>();
  return {
    getItem: (key: string) => map.get(key) ?? null,
    setItem: (key: string, value: string) => void map.set(key, value),
    removeItem: (key: string) => void map.delete(key),
    clear: () => { map.clear(); },
    key: () => null,
    length: 0,
  };
}

interface RenderOptions {
  readonly authenticateImpl?: () => Promise<{ client: never; session: JmapSession }>;
  readonly branding?: Branding;
}

function renderLogin(options: RenderOptions = {}) {
  const authenticateImpl =
    options.authenticateImpl ??
    (() => Promise.resolve({ client: undefined as never, session: fakeSession }));

  return render(
    <I18nProvider locale="en">
      <BrandingProvider branding={options.branding ?? MOOV_DEFAULT_BRANDING}>
        <AuthProvider
          skipRestore
          storage={memoryStorage()}
          authenticateImpl={authenticateImpl as never}
        >
          <LoginScreen />
        </AuthProvider>
      </BrandingProvider>
    </I18nProvider>,
  );
}

/** Rejects with the given ApiError, as a failed sign-in would. */
function rejectingAuth(error: ApiError) {
  return () => Promise.reject(error);
}

describe("the login form", () => {
  it("labels both fields so they are reachable by their accessible name", () => {
    renderLogin();
    // getByLabelText fails unless the label is properly associated, so this
    // asserts the association rather than the mere presence of text.
    expect(screen.getByLabelText(en["login.emailLabel"])).toBeInTheDocument();
    expect(screen.getByLabelText(en["login.passwordLabel"])).toBeInTheDocument();
  });

  it("uses the autocomplete tokens a password manager keys on", () => {
    renderLogin();
    // "username" — not "email" — is the token the spec defines for a sign-in
    // identifier; getting it wrong is why some managers refuse to fill.
    expect(screen.getByLabelText(en["login.emailLabel"])).toHaveAttribute(
      "autocomplete",
      "username",
    );
    // "current-password" tells a manager this is a sign-in, not a
    // registration, so it offers to fill rather than to generate.
    expect(screen.getByLabelText(en["login.passwordLabel"])).toHaveAttribute(
      "autocomplete",
      "current-password",
    );
  });

  it("starts with the password masked", () => {
    renderLogin();
    expect(screen.getByLabelText(en["login.passwordLabel"])).toHaveAttribute(
      "type",
      "password",
    );
  });

  it("focuses the email field on a clean arrival", async () => {
    renderLogin();
    await waitFor(() => {
      expect(screen.getByLabelText(en["login.emailLabel"])).toHaveFocus();
    });
  });

  it("is fully operable by keyboard from the email field to submit", async () => {
    const user = userEvent.setup();
    renderLogin();

    const email = screen.getByLabelText(en["login.emailLabel"]);
    await waitFor(() => {
      expect(email).toHaveFocus();
    });

    await user.keyboard("moov-test@atmosfera.cloud");
    expect(email).toHaveValue("moov-test@atmosfera.cloud");

    // Tab reaches the password field, then the visibility toggle, then submit.
    await user.tab();
    expect(screen.getByLabelText(en["login.passwordLabel"])).toHaveFocus();

    await user.tab();
    expect(screen.getByRole("button", { name: en["login.showPassword"] })).toHaveFocus();

    await user.tab();
    expect(screen.getByRole("button", { name: en["login.submit"] })).toHaveFocus();
  });
});

describe("the password visibility toggle", () => {
  it("reveals and re-masks the password", async () => {
    const user = userEvent.setup();
    renderLogin();

    const password = screen.getByLabelText(en["login.passwordLabel"]);
    const toggle = screen.getByRole("button", { name: en["login.showPassword"] });

    // aria-pressed states the toggle's state for a screen reader.
    expect(toggle).toHaveAttribute("aria-pressed", "false");

    await user.click(toggle);
    expect(password).toHaveAttribute("type", "text");

    // The accessible name always says what the NEXT press will do.
    const pressed = screen.getByRole("button", { name: en["login.hidePassword"] });
    expect(pressed).toHaveAttribute("aria-pressed", "true");

    await user.click(pressed);
    expect(password).toHaveAttribute("type", "password");
  });

  it("does not submit the form", async () => {
    const user = userEvent.setup();
    const authenticateImpl = vi.fn(() =>
      Promise.resolve({ client: undefined as never, session: fakeSession }),
    );
    renderLogin({ authenticateImpl });

    await user.type(
      screen.getByLabelText(en["login.emailLabel"]),
      "moov-test@atmosfera.cloud",
    );
    await user.type(screen.getByLabelText(en["login.passwordLabel"]), "secret");

    // A bare <button> inside a <form> defaults to type="submit"; if that were
    // the case here, looking at your password would sign you in.
    await user.click(screen.getByRole("button", { name: en["login.showPassword"] }));
    expect(authenticateImpl).not.toHaveBeenCalled();
  });
});

describe("client-side validation", () => {
  it("refuses an empty email and moves focus to it", async () => {
    const user = userEvent.setup();
    const authenticateImpl = vi.fn();
    renderLogin({ authenticateImpl: authenticateImpl as never });

    await user.click(screen.getByRole("button", { name: en["login.submit"] }));

    // The message appears TWICE by design: once beside the field, and once in
    // the polite live region that announces it to a screen reader. Both are
    // wanted, so the query allows for both rather than the test asserting a
    // single occurrence that would forbid the announcement.
    expect(screen.getAllByText(en["login.error.emailRequired"]).length).toBeGreaterThan(0);
    expect(screen.getByLabelText(en["login.emailLabel"])).toHaveFocus();
    // Nothing was sent: a request with an empty username would burn a lockout
    // strike for a mistake the client can see.
    expect(authenticateImpl).not.toHaveBeenCalled();
  });

  it("catches a bare username before it becomes a wrong-password error", async () => {
    const user = userEvent.setup();
    const authenticateImpl = vi.fn();
    renderLogin({ authenticateImpl: authenticateImpl as never });

    await user.type(screen.getByLabelText(en["login.emailLabel"]), "moov-test");
    await user.type(screen.getByLabelText(en["login.passwordLabel"]), "secret");
    await user.click(screen.getByRole("button", { name: en["login.submit"] }));

    expect(screen.getAllByText(en["login.error.emailInvalid"]).length).toBeGreaterThan(0);
    expect(authenticateImpl).not.toHaveBeenCalled();
  });

  it("marks the offending field invalid for assistive technology", async () => {
    const user = userEvent.setup();
    renderLogin();

    await user.click(screen.getByRole("button", { name: en["login.submit"] }));

    const email = screen.getByLabelText(en["login.emailLabel"]);
    expect(email).toHaveAttribute("aria-invalid", "true");
    // The message is linked to the field, so a screen reader reads it when the
    // field takes focus rather than leaving it orphaned on screen.
    const describedBy = email.getAttribute("aria-describedby");
    expect(describedBy).toBeTruthy();
    expect(document.getElementById(describedBy ?? "")).toHaveTextContent(
      en["login.error.emailRequired"],
    );
  });

  it("clears the message as soon as the user corrects the field", async () => {
    const user = userEvent.setup();
    renderLogin();

    await user.click(screen.getByRole("button", { name: en["login.submit"] }));
    expect(screen.getAllByText(en["login.error.emailRequired"]).length).toBeGreaterThan(0);

    await user.type(screen.getByLabelText(en["login.emailLabel"]), "a");
    expect(screen.queryAllByText(en["login.error.emailRequired"])).toHaveLength(0);
  });
});

describe("server errors reach the screen with their meaning intact", () => {
  /** Signs in with credentials that will be rejected as `error`. */
  async function submitAndFail(error: ApiError): Promise<void> {
    const user = userEvent.setup();
    renderLogin({ authenticateImpl: rejectingAuth(error) });

    await user.type(
      screen.getByLabelText(en["login.emailLabel"]),
      "moov-test@atmosfera.cloud",
    );
    await user.type(screen.getByLabelText(en["login.passwordLabel"]), "wrong");
    await user.click(screen.getByRole("button", { name: en["login.submit"] }));
  }

  it("shows the wrong-credentials message for a 401", async () => {
    await submitAndFail(new ApiError("invalid-credentials", "", { status: 401 }));

    await waitFor(() => {
      expect(screen.getByRole("alert")).toHaveTextContent(
        en["error.invalidCredentials.title"],
      );
    });
    expect(screen.getByRole("alert")).toHaveTextContent(en["error.invalidCredentials.body"]);
  });

  /**
   * THE PILOT'S LESSON, as a test.
   *
   * Bulwark turned this exact server response into "an error occurred". Here
   * the user must be told that their password was right, that the mailbox is
   * not enabled, and that an administrator has to act.
   */
  it("tells an unprovisioned user exactly what is wrong and who can fix it", async () => {
    await submitAndFail(
      new ApiError("not-provisioned", "not provisioned in Moov", { status: 403 }),
    );

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en["error.notProvisioned.title"](brandName(MOOV_DEFAULT_BRANDING.name)));
    expect(alert).toHaveTextContent(en["error.notProvisioned.body"](brandName(MOOV_DEFAULT_BRANDING.name)));
    // The remedy is named.
    expect(alert.textContent?.toLowerCase()).toContain("administrator");
    // And it is NOT the generic message.
    expect(alert.textContent).not.toMatch(/^an error occurred\.?$/i);
  });

  it("names the wait for a 429", async () => {
    await submitAndFail(
      new ApiError("rate-limited", "", { status: 429, retryAfterSeconds: 30 }),
    );

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en["error.rateLimited.title"]);
    expect(alert).toHaveTextContent("30");
  });

  it("blames the server, not the account, for a 5xx", async () => {
    await submitAndFail(new ApiError("server-error", "", { status: 503 }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en["error.serverError.title"](brandName(MOOV_DEFAULT_BRANDING.name)));
    expect(alert.textContent?.toLowerCase()).toContain("account");
  });

  it("points at the connection for a transport failure", async () => {
    await submitAndFail(new ApiError("network", "The server could not be reached."));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en["error.network.title"]);
  });

  it("announces the failure through a live region", async () => {
    await submitAndFail(new ApiError("invalid-credentials", "", { status: 401 }));

    // role="alert" is itself an assertive live region: an announcement is what
    // tells a screen-reader user the submit they just made has failed.
    const alert = await screen.findByRole("alert");
    expect(alert).toBeInTheDocument();
  });

  it("keeps what the user typed so they can correct it", async () => {
    const user = userEvent.setup();
    renderLogin({
      authenticateImpl: rejectingAuth(
        new ApiError("invalid-credentials", "", { status: 401 }),
      ),
    });

    await user.type(
      screen.getByLabelText(en["login.emailLabel"]),
      "moov-test@atmosfera.cloud",
    );
    await user.type(screen.getByLabelText(en["login.passwordLabel"]), "wrong");
    await user.click(screen.getByRole("button", { name: en["login.submit"] }));

    await screen.findByRole("alert");
    // Clearing the email on failure would make the user retype it every time.
    expect(screen.getByLabelText(en["login.emailLabel"])).toHaveValue(
      "moov-test@atmosfera.cloud",
    );
  });
});

describe("branding", () => {
  const acme: Branding = {
    name: "Acme Mail",
    logoUrl: "/branding/assets/mail.acme.test/logo.png",
    logoDarkUrl: "",
    splashUrl: "/branding/assets/mail.acme.test/splash.jpg",
    colors: {
      primary: "#c0ffee",
      onPrimary: "#000000",
      splashFrom: "#102030",
      splashTo: "#405060",
    },
    tagline: "Correo de Acme",
    supportUrl: "https://support.acme.test",
    privacyUrl: "",
    termsUrl: "",
    isDefault: false,
  };

  it("shows the customer's logo and tagline", () => {
    renderLogin({ branding: acme });

    /*
     * The logo REPLACES the text name rather than sitting beside it: a
     * wordmark already says the name graphically, and rendering both printed
     * the brand twice — which then ellipsised inside the panel's content
     * column ("[LOGO] Acme …"). So the name is the image's accessible name and
     * appears nowhere as text.
     */
    // The screen renders the mark twice (the form header at `sm`, the brand
    // panel at `lg`), so the assertion is on the ABSENCE of a text duplicate
    // rather than on a single image.
    expect(screen.getAllByAltText("Acme Mail").length).toBeGreaterThan(0);
    expect(screen.queryByText("Acme Mail")).not.toBeInTheDocument();

    // The tagline is genuine content and stays.
    expect(screen.getByText("Correo de Acme")).toBeInTheDocument();

    const sources = Array.from(document.querySelectorAll("img")).map((img) =>
      img.getAttribute("src"),
    );
    expect(sources).toContain("/branding/assets/mail.acme.test/logo.png");
    expect(sources).toContain("/branding/assets/mail.acme.test/splash.jpg");
  });

  it("keeps the heading and tagline layout when the brand has a logo", () => {
    /*
     * The brand panel is imagery plus the tagline; the FORM owns the heading.
     * The logo change must not have moved either — a brand may change identity
     * and colour, never layout or behaviour.
     */
    renderLogin({ branding: acme });
    expect(
      screen.getByRole("heading", { name: en["login.heading"] }),
    ).toBeInTheDocument();
    expect(screen.getByLabelText(en["login.emailLabel"])).toBeInTheDocument();
    expect(screen.getByLabelText(en["login.passwordLabel"])).toBeInTheDocument();
    expect(screen.getByRole("button", { name: en["login.submit"] })).toBeInTheDocument();
  });

  it("shows Moov's own brand when nothing is configured", () => {
    renderLogin({ branding: MOOV_DEFAULT_BRANDING });
    expect(screen.getAllByText("Moov Mail").length).toBeGreaterThan(0);
  });

  it("offers the support link only when the brand configured one", async () => {
    const user = userEvent.setup();

    // With a support URL, the not-provisioned error offers it as a link.
    const { unmount } = render(
      <I18nProvider locale="en">
        <BrandingProvider branding={acme}>
          <AuthProvider
            skipRestore
            storage={memoryStorage()}
            authenticateImpl={
              rejectingAuth(new ApiError("not-provisioned", "", { status: 403 })) as never
            }
          >
            <LoginScreen />
          </AuthProvider>
        </BrandingProvider>
      </I18nProvider>,
    );

    await user.type(
      screen.getByLabelText(en["login.emailLabel"]),
      "moov-test@atmosfera.cloud",
    );
    await user.type(screen.getByLabelText(en["login.passwordLabel"]), "x");
    await user.click(screen.getByRole("button", { name: en["login.submit"] }));

    const link = await screen.findByRole("link", {
      name: en["login.contactAdministrator"],
    });
    expect(link).toHaveAttribute("href", "https://support.acme.test");
    // An off-origin link must not give the target access to window.opener.
    expect(link).toHaveAttribute("rel", expect.stringContaining("noopener"));

    unmount();
  });
});

describe("the submitting state", () => {
  it("disables the form and says what it is doing", async () => {
    const user = userEvent.setup();
    // A sign-in that never settles, so the pending state can be observed.
    renderLogin({ authenticateImpl: (() => new Promise(() => undefined)) as never });

    await user.type(
      screen.getByLabelText(en["login.emailLabel"]),
      "moov-test@atmosfera.cloud",
    );
    await user.type(screen.getByLabelText(en["login.passwordLabel"]), "secret");
    await user.click(screen.getByRole("button", { name: en["login.submit"] }));

    const submit = await screen.findByRole("button", { name: en["login.submitting"] });
    expect(submit).toBeDisabled();
    // Disabling the inputs is what stops a second submit from racing the first.
    expect(screen.getByLabelText(en["login.emailLabel"])).toBeDisabled();
    expect(screen.getByLabelText(en["login.passwordLabel"])).toBeDisabled();
  });
});
