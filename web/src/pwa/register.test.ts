import { describe, expect, it, vi } from "vitest";

import { registerServiceWorker, serviceWorkerSupported, SERVICE_WORKER_URL } from "./register";

/**
 * Registration's guard rails.
 *
 * The single most important assertion in this file is the first one: jsdom has
 * no `navigator.serviceWorker`, and if the guard were wrong the entire unit
 * suite would start throwing in `main.tsx`. That is the failure mode this
 * module exists to prevent, so it is checked against the real environment the
 * suite runs in rather than a mock.
 */

describe("serviceWorkerSupported", () => {
  it("is false in the test environment (jsdom has no service worker)", () => {
    // Not a mock: this is the actual global the suite runs against.
    expect(serviceWorkerSupported()).toBe(false);
  });

  it("is false when the API is missing", () => {
    expect(serviceWorkerSupported({ navigator: {}, isSecureContext: true })).toBe(false);
  });

  it("is false on an insecure origin even when the API is present", () => {
    // Some environments expose the property and then reject at register().
    expect(
      serviceWorkerSupported({
        navigator: { serviceWorker: {} },
        isSecureContext: false,
      }),
    ).toBe(false);
  });

  it("is true with the API present on a secure origin", () => {
    expect(
      serviceWorkerSupported({
        navigator: { serviceWorker: {} },
        isSecureContext: true,
      }),
    ).toBe(true);
  });
});

describe("registerServiceWorker", () => {
  it("resolves false without touching anything when unsupported", async () => {
    // jsdom: the real branch taken by `npm test`.
    await expect(registerServiceWorker()).resolves.toBe(false);
  });

  it("registers at the root scope when supported", async () => {
    const register = vi.fn().mockResolvedValue({});
    vi.stubGlobal("navigator", { serviceWorker: { register } });
    vi.stubGlobal("isSecureContext", true);

    await expect(registerServiceWorker()).resolves.toBe(true);
    expect(register).toHaveBeenCalledWith(SERVICE_WORKER_URL, { scope: "/" });

    vi.unstubAllGlobals();
  });

  it("swallows a failed registration rather than rejecting", async () => {
    /*
     * An unhandled rejection here would print a red error in the console of an
     * app that is working perfectly well without a worker.
     */
    const register = vi.fn().mockRejectedValue(new Error("no"));
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    vi.stubGlobal("navigator", { serviceWorker: { register } });
    vi.stubGlobal("isSecureContext", true);

    await expect(registerServiceWorker()).resolves.toBe(false);
    expect(warn).toHaveBeenCalled();

    vi.unstubAllGlobals();
  });
});
