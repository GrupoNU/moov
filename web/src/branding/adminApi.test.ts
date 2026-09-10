import { describe, expect, it, vi } from "vitest";

import {
  ACCEPTED_IMAGE_TYPES,
  BrandAdminClient,
  BrandAdminError,
  MAX_ASSET_BYTES,
  checkImageFile,
  parseBrandAdminDoc,
} from "./adminApi";

/**
 * The brand-admin client.
 *
 * Two properties matter here and the rest is plumbing:
 *
 *   1. **Nothing from the wire is trusted.** The fields of this document become
 *      CSS custom properties, `<img src>` attributes and `href`s on a page an
 *      administrator is looking at, so a hostile or merely broken value must
 *      fall back rather than reach the DOM. The parse tests are written as
 *      attacks, not as shape checks.
 *   2. **The status IS the taxonomy.** The UI removes a whole tab on one status
 *      and marks one field on another, so mapping a status to the wrong kind is
 *      a visible product bug rather than a cosmetic one.
 */

/** A minimal well-formed document, for the tests that vary one field. */
const DOC = {
  host: "mail.acme.example",
  default: false,
  name: "Acme Mail",
  shortName: "Acme",
  tagline: "Correo de Acme",
  supportUrl: "https://acme.example/help",
  privacyUrl: "",
  termsUrl: "",
  colors: {
    primary: "#5B5BD6",
    onPrimary: "#ffffff",
    splashFrom: "#1e1b4b",
    splashTo: "#4c1d95",
  },
  assets: {
    logo: { url: "/branding/assets/logo.png?v=7", bytes: 2048, width: 320, height: 80 },
    logoDark: null,
    icon: null,
    splash: null,
  },
  iconSource: "logo",
  iconIssue: "",
  brandAdmins: ["admin@acme.example"],
  warnings: [],
  publicUrl: "/branding",
  manifestUrl: "/manifest.webmanifest",
  iconUrls: { "icon-192": "/branding/icons/icon-192.png?v=7" },
  version: 7,
};

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function clientWith(fetchImpl: typeof fetch): BrandAdminClient {
  return new BrandAdminClient({ authorization: "Basic dGVzdA==", fetchImpl });
}

describe("parsing refuses what it cannot render safely", () => {
  it("keeps a well-formed document intact", () => {
    const doc = parseBrandAdminDoc(DOC);
    expect(doc.name).toBe("Acme Mail");
    expect(doc.colors.primary).toBe("#5b5bd6"); // lower-cased
    expect(doc.assets.logo?.url).toBe("/branding/assets/logo.png?v=7");
    expect(doc.iconSource).toBe("logo");
    expect(doc.version).toBe(7);
  });

  it("drops a colour that is not a hex literal rather than passing it through", () => {
    /*
     * The attack this closes: a value like `red; background: url(...)` reaching
     * `style.setProperty` on the preview container. An empty string renders no
     * colour; a poisoned one renders somebody else's CSS.
     */
    const doc = parseBrandAdminDoc({
      ...DOC,
      colors: { ...DOC.colors, primary: "red; --x: url(https://evil.example)" },
    });
    expect(doc.colors.primary).toBe("");
  });

  it.each([
    ["https://evil.example/pixel.png", "an absolute off-origin URL"],
    ["//evil.example/pixel.png", "a protocol-relative URL"],
    ["javascript:alert(1)", "a script URL"],
    ["\\\\evil.example\\pixel.png", "a UNC path"],
  ])("refuses %s as an asset URL (%s)", (url) => {
    const doc = parseBrandAdminDoc({
      ...DOC,
      assets: { ...DOC.assets, logo: { url, bytes: 10, width: 1, height: 1 } },
    });
    // An asset whose URL did not survive is an EMPTY SLOT, not half an asset:
    // there is nothing to draw and nothing to remove.
    expect(doc.assets.logo).toBeNull();
  });

  it("drops a poisoned generated-icon URL without losing the good ones", () => {
    const doc = parseBrandAdminDoc({
      ...DOC,
      iconUrls: {
        "icon-192": "/branding/icons/icon-192.png?v=7",
        "icon-512": "https://evil.example/icon.png",
      },
    });
    expect(doc.iconUrls["icon-192"]).toBe("/branding/icons/icon-192.png?v=7");
    expect(doc.iconUrls["icon-512"]).toBeUndefined();
  });

  it("falls back to the EMPTY value, never to Moov's defaults", () => {
    /*
     * The difference from `mergeBranding`, and it is deliberate: this is an
     * EDITOR. Showing an administrator a colour their server did not send
     * invites them to "keep" a value that is not theirs.
     */
    const doc = parseBrandAdminDoc({ host: "mail.acme.example" });
    expect(doc.name).toBe("");
    expect(doc.colors.primary).toBe("");
    expect(doc.assets.logo).toBeNull();
  });

  it("reads an unknown iconSource as the stock icons", () => {
    expect(parseBrandAdminDoc({ ...DOC, iconSource: "wat" }).iconSource).toBe("default");
  });

  it("keeps only the strings out of a warnings array of mixed junk", () => {
    const doc = parseBrandAdminDoc({ ...DOC, warnings: ["not square", 42, null, "cropped"] });
    expect(doc.warnings).toEqual(["not square", "cropped"]);
  });

  it("throws on a body that is not a document at all", () => {
    expect(() => parseBrandAdminDoc([DOC])).toThrow(BrandAdminError);
    expect(() => parseBrandAdminDoc("nope")).toThrow(BrandAdminError);
  });
});

describe("the pre-check answers the common mistakes without a round trip", () => {
  const fileOf = (name: string, type: string, size: number): File => {
    const file = new File(["x"], name, { type });
    Object.defineProperty(file, "size", { value: size });
    return file;
  };

  it("accepts every type the server accepts", () => {
    for (const type of ACCEPTED_IMAGE_TYPES) {
      expect(checkImageFile(fileOf("logo.png", type, 1024))).toBeUndefined();
    }
  });

  it("refuses an SVG — the one refusal that needs a sentence on screen", () => {
    expect(checkImageFile(fileOf("logo.svg", "image/svg+xml", 1024))).toBe("unsupportedType");
  });

  it("refuses a file over the ceiling", () => {
    expect(checkImageFile(fileOf("big.png", "image/png", MAX_ASSET_BYTES + 1))).toBe("tooLarge");
  });

  it("accepts a typeless file whose EXTENSION is right", () => {
    // Dragged out of some applications, a file arrives with an empty type.
    // Refusing on one missing signal would block uploads the server would take.
    expect(checkImageFile(fileOf("logo.png", "", 1024))).toBeUndefined();
    expect(checkImageFile(fileOf("logo", "", 1024))).toBe("unsupportedType");
  });
});

describe("the probe decides whether a tab exists", () => {
  it("answers the host when the server says canEdit", async () => {
    const fetchImpl = vi.fn(() => Promise.resolve(jsonResponse({ host: "mail.acme.example", canEdit: true }))) as unknown as typeof fetch;
    await expect(clientWith(fetchImpl).probe()).resolves.toEqual({
      host: "mail.acme.example",
      canEdit: true,
    });
  });

  it("answers undefined on 404 rather than throwing — not being an admin is normal", async () => {
    const fetchImpl = vi.fn(() => Promise.resolve(new Response("", { status: 404 }))) as unknown as typeof fetch;
    await expect(clientWith(fetchImpl).probe()).resolves.toBeUndefined();
  });

  it("answers undefined when the body says canEdit is false", async () => {
    const fetchImpl = vi.fn(() => Promise.resolve(jsonResponse({ host: "mail.acme.example", canEdit: false }))) as unknown as typeof fetch;
    await expect(clientWith(fetchImpl).probe()).resolves.toBeUndefined();
  });

  it("still THROWS a real failure, so a broken server is not read as 'no access'", async () => {
    const fetchImpl = vi.fn(() => Promise.resolve(new Response("", { status: 500 }))) as unknown as typeof fetch;
    await expect(clientWith(fetchImpl).probe()).rejects.toMatchObject({ kind: "network" });
  });
});

describe("every request carries the credential explicitly and no ambient one", () => {
  it("sends Authorization and omits cookies", async () => {
    const fetchImpl = vi.fn(() => Promise.resolve(jsonResponse(DOC))) as unknown as typeof fetch;
    await clientWith(fetchImpl).get();
    const [url, init] = (fetchImpl as unknown as ReturnType<typeof vi.fn>).mock.calls[0] as [
      string,
      RequestInit,
    ];
    expect(url).toBe("/branding/admin/brand");
    expect((init.headers as Record<string, string>).Authorization).toBe("Basic dGVzdA==");
    // The header is sent explicitly, so the browser must not also attach
    // ambient credentials — the rule cors.go depends on.
    expect(init.credentials).toBe("omit");
  });
});

describe("writes send only what changed", () => {
  it("PUTs a partial body, and an empty string means CLEAR", async () => {
    const fetchImpl = vi.fn(() => Promise.resolve(jsonResponse(DOC))) as unknown as typeof fetch;
    await clientWith(fetchImpl).update({ tagline: "" });
    const [url, init] = (fetchImpl as unknown as ReturnType<typeof vi.fn>).mock.calls[0] as [
      string,
      RequestInit,
    ];
    expect(url).toBe("/branding/admin/brand");
    expect(init.method).toBe("PUT");
    // Only the changed field. Sending the whole document on every blur would
    // make two administrators editing at once overwrite each other.
    expect(JSON.parse(init.body as string)).toEqual({ tagline: "" });
  });

  it("PUTs a nested colours patch with one key", async () => {
    const fetchImpl = vi.fn(() => Promise.resolve(jsonResponse(DOC))) as unknown as typeof fetch;
    await clientWith(fetchImpl).update({ colors: { primary: "#123456" } });
    const [, init] = (fetchImpl as unknown as ReturnType<typeof vi.fn>).mock.calls[0] as [
      string,
      RequestInit,
    ];
    expect(JSON.parse(init.body as string)).toEqual({ colors: { primary: "#123456" } });
  });

  it("PUTs an asset as raw bytes with the file's own Content-Type", async () => {
    const fetchImpl = vi.fn(() => Promise.resolve(jsonResponse(DOC))) as unknown as typeof fetch;
    const file = new File(["bytes"], "logo.png", { type: "image/png" });
    await clientWith(fetchImpl).putAsset("logoDark", file);
    const [url, init] = (fetchImpl as unknown as ReturnType<typeof vi.fn>).mock.calls[0] as [
      string,
      RequestInit,
    ];
    expect(url).toBe("/branding/admin/assets/logoDark");
    expect((init.headers as Record<string, string>)["Content-Type"]).toBe("image/png");
    expect(init.body).toBe(file);
  });

  it("DELETEs an asset and POSTs a reset at their own paths", async () => {
    const fetchImpl = vi.fn(() => Promise.resolve(jsonResponse(DOC))) as unknown as typeof fetch;
    const client = clientWith(fetchImpl);
    await client.deleteAsset("icon");
    await client.reset();
    const calls = (fetchImpl as unknown as ReturnType<typeof vi.fn>).mock.calls as [
      string,
      RequestInit,
    ][];
    expect(calls[0]?.[0]).toBe("/branding/admin/assets/icon");
    expect(calls[0]?.[1].method).toBe("DELETE");
    expect(calls[1]?.[0]).toBe("/branding/admin/reset");
    expect(calls[1]?.[1].method).toBe("POST");
  });
});

describe("the status is the taxonomy", () => {
  it.each([
    [404, "notAdmin"],
    [413, "tooLarge"],
    [415, "unsupportedType"],
    [400, "invalidField"],
    [500, "network"],
    [503, "network"],
  ])("maps %i to %s", async (status, kind) => {
    const fetchImpl = vi.fn(() => Promise.resolve(new Response("{}", { status, headers: { "Content-Type": "application/json" } }))) as unknown as typeof fetch;
    await expect(clientWith(fetchImpl).get()).rejects.toMatchObject({ kind });
  });

  it("keeps the FIELD and the reason a 400 named, so one control can be marked", async () => {
    const fetchImpl = vi.fn(() => Promise.resolve(jsonResponse({ field: "shortName", reason: "use 12 characters at most" }, 400))) as unknown as typeof fetch;
    await expect(clientWith(fetchImpl).update({ shortName: "a very long one" })).rejects.toMatchObject({
      kind: "invalidField",
      field: "shortName",
      message: "use 12 characters at most",
    });
  });

  it("keeps a 415's reason, and survives a 415 with no JSON body", async () => {
    const withReason = vi.fn(() => Promise.resolve(jsonResponse({ reason: "SVG is not accepted" }, 415))) as unknown as typeof fetch;
    const file = new File(["x"], "logo.svg", { type: "image/svg+xml" });
    await expect(clientWith(withReason).putAsset("logo", file)).rejects.toMatchObject({
      message: "SVG is not accepted",
    });

    const noBody = vi.fn(() => Promise.resolve(new Response("<html>", { status: 415 }))) as unknown as typeof fetch;
    await expect(clientWith(noBody).putAsset("logo", file)).rejects.toMatchObject({
      kind: "unsupportedType",
    });
  });

  it("turns a transport failure into a network error, never letting it escape raw", async () => {
    const fetchImpl = vi.fn((): Promise<Response> => {
      throw new TypeError("Failed to fetch");
    }) as unknown as typeof fetch;
    await expect(clientWith(fetchImpl).get()).rejects.toMatchObject({ kind: "network" });
  });

  it("lets an ABORT through unchanged, so a cancelled load is not shown as an error", async () => {
    const fetchImpl = vi.fn((): Promise<Response> => {
      throw new DOMException("aborted", "AbortError");
    }) as unknown as typeof fetch;
    await expect(clientWith(fetchImpl).get()).rejects.toBeInstanceOf(DOMException);
  });

  it("reports a 200 whose body is not JSON rather than rendering a blank brand", async () => {
    const fetchImpl = vi.fn(() => Promise.resolve(new Response("<html>", { status: 200 }))) as unknown as typeof fetch;
    await expect(clientWith(fetchImpl).get()).rejects.toMatchObject({ kind: "network" });
  });
});
