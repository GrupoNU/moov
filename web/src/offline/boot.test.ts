import { describe, expect, it } from "vitest";

import { bootMode, canReadOffline } from "./boot";

describe("bootMode", () => {
  it("is online when the network is up and nothing has failed", () => {
    expect(bootMode({ online: true, requestFailed: false, hasCache: true })).toBe("online");
    expect(bootMode({ online: true, requestFailed: false, hasCache: false })).toBe("online");
  });

  it("renders the cache when the browser says there is no network", () => {
    // Trusted immediately: waiting for a request to time out would leave the
    // user watching a spinner for thirty seconds.
    expect(bootMode({ online: false, requestFailed: false, hasCache: true })).toBe("cached");
  });

  it("renders the cache when a real request failed despite navigator.onLine", () => {
    // The captive-portal path: `onLine` is optimistic, so a failed request is
    // what actually promotes us to "cached".
    expect(bootMode({ online: true, requestFailed: true, hasCache: true })).toBe("cached");
  });

  it("is an honest empty state when there is no network AND no cache", () => {
    // Never an empty inbox pretending to be the truth.
    expect(bootMode({ online: false, requestFailed: false, hasCache: false })).toBe("empty");
    expect(bootMode({ online: true, requestFailed: true, hasCache: false })).toBe("empty");
  });

  it("does not treat a slow server as an offline server", () => {
    // No request has failed yet, so nothing licenses showing stale mail.
    expect(bootMode({ online: true, requestFailed: false, hasCache: true })).toBe("online");
  });
});

describe("canReadOffline", () => {
  it("always allows a read while online", () => {
    expect(canReadOffline(false, "online")).toBe(true);
  });

  it("allows a cached body while the list renders from cache", () => {
    expect(canReadOffline(true, "cached")).toBe(true);
  });

  it("refuses a message whose body was never cached", () => {
    // Its own honest explanation, rather than an empty pane or an endless spinner.
    expect(canReadOffline(false, "cached")).toBe(false);
    expect(canReadOffline(false, "empty")).toBe(false);
  });
});
