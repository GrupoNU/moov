import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { I18nProvider } from "../../i18n/I18nProvider";
import type { Email, EmailBodyPart } from "../../mail/types";
import { MessageBody } from "./MessageBody";

/**
 * Inline (`cid:`) images in REAL mail, end to end through the reader.
 *
 * The shape below is not invented: it is the dominant structure of the
 * pilot's migrated Outlook/Microsoft 365 corpus — `multipart/mixed >
 * multipart/related > multipart/alternative(text, html)` with the images as
 * later children of the `related` container, `Content-Disposition: inline`
 * and a `Content-ID` the HTML references. The server puts those parts in
 * `attachments` (RFC 8621 §4.1.4), and this component is what has to turn
 * them into pixels.
 */

function part(overrides: Partial<EmailBodyPart> & { partId: string }): EmailBodyPart {
  return {
    blobId: `${"a".repeat(64)}-${overrides.partId}`,
    size: 1024,
    name: null,
    type: "application/octet-stream",
    charset: null,
    disposition: null,
    cid: null,
    language: null,
    location: null,
    ...overrides,
  };
}

const HTML_PART = part({
  partId: "4",
  type: "text/html",
  charset: "windows-1252",
  size: 5693,
});

const INLINE_IMAGE = part({
  partId: "5",
  type: "image/png",
  size: 71143,
  name: "image.png",
  disposition: "inline",
  cid: "b700b481-6b2b-49f1-84cb-73e9fdd001f6",
});

/** A 1x1 PNG — real bytes, so the data: URL the sanitizer accepts is real. */
const PNG_BYTES = Uint8Array.from(
  atob(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==",
  ),
  (c) => c.charCodeAt(0),
);

function outlookEmail(): Email {
  return {
    id: "m1",
    attachments: [INLINE_IMAGE],
    htmlBody: [HTML_PART],
    textBody: [],
    bodyValues: {
      "4": {
        value:
          '<p>Saludos,</p><img src="cid:b700b481-6b2b-49f1-84cb-73e9fdd001f6" width="200">',
        isEncodingProblem: false,
        isTruncated: false,
      },
    },
  };
}

function srcDoc(): string {
  return document.querySelector("iframe")?.getAttribute("srcdoc") ?? "";
}

/** The frame document's BODY. The CSP in the head names the proxy path
 * whatever the image policy is, so an assertion about what actually renders
 * has to look past it. */
function srcDocBody(): string {
  return srcDoc().split("<body>")[1] ?? "";
}

describe("inline cid: images from real Outlook mail", () => {
  it("fetches the referenced part and inlines it into the frame", async () => {
    const downloadBlob = vi.fn().mockResolvedValue(new Blob([PNG_BYTES], { type: "image/png" }));
    const client = { downloadBlob } as never;

    render(
      <I18nProvider locale="es">
        <MessageBody
          email={outlookEmail()}
          signImageUrls={vi.fn().mockResolvedValue(new Map())}
          client={client}
          accountId="a1"
        />
      </I18nProvider>,
    );

    // The part the body references is the one fetched — nothing else.
    await waitFor(() => {
      expect(downloadBlob).toHaveBeenCalledTimes(1);
    });
    expect(downloadBlob).toHaveBeenCalledWith("a1", INLINE_IMAGE.blobId, "image.png", "image/png");

    // And it reaches the document as a data: URL the frame's CSP admits.
    await waitFor(() => {
      expect(srcDoc()).toContain("data:image/png;base64,");
    });
  });
});

describe("remote images in real mail", () => {
  it("rewrites a remote <img> to the signed proxy path once unblocked", async () => {
    const remote = "https://cdn.example.com/logo.png";
    const signImageUrls = vi
      .fn()
      .mockResolvedValue(new Map([[remote, "/jmap/imgproxy?u=abc&e=1&s=xyz"]]));

    const email: Email = {
      id: "m2",
      attachments: [],
      htmlBody: [HTML_PART],
      textBody: [],
      bodyValues: {
        "4": {
          value: `<p>Hola</p><img src="${remote}" width="200">`,
          isEncodingProblem: false,
          isTruncated: false,
        },
      },
    };

    render(
      <I18nProvider locale="es">
        <MessageBody email={email} signImageUrls={signImageUrls} autoLoadImages />
      </I18nProvider>,
    );

    await waitFor(() => {
      expect(signImageUrls).toHaveBeenCalledWith([remote]);
    });
    await waitFor(() => {
      expect(srcDoc()).toContain("/jmap/imgproxy?u=abc");
    });
  });
});

/**
 * The regression that motivated this file.
 *
 * The signing effect used to depend on the very state it set, so React ran
 * its cleanup — cancelling the in-flight request — between the call and its
 * answer. Nothing threw; the signed map was simply dropped and every remote
 * image in every message stayed blocked. This pins the mechanism rather than
 * the symptom: the map must survive a re-render that happens WHILE the
 * signing request is in flight.
 */
describe("signing survives a re-render in flight", () => {
  it("applies a map that resolves after the component re-rendered", async () => {
    const remote = "https://cdn.example.com/hero.png";
    let release: ((map: ReadonlyMap<string, string>) => void) | undefined;
    const signImageUrls = vi.fn().mockReturnValue(
      new Promise<ReadonlyMap<string, string>>((resolve) => {
        release = resolve;
      }),
    );

    const email: Email = {
      id: "m3",
      attachments: [],
      htmlBody: [HTML_PART],
      textBody: [],
      bodyValues: {
        "4": {
          value: `<img src="${remote}">`,
          isEncodingProblem: false,
          isTruncated: false,
        },
      },
    };

    const view = render(
      <I18nProvider locale="es">
        <MessageBody email={email} signImageUrls={signImageUrls} autoLoadImages />
      </I18nProvider>,
    );

    await waitFor(() => {
      expect(signImageUrls).toHaveBeenCalled();
    });

    // A re-render while the request is still open — exactly what the state
    // write inside the effect used to cause.
    view.rerender(
      <I18nProvider locale="es">
        <MessageBody email={email} signImageUrls={signImageUrls} autoLoadImages />
      </I18nProvider>,
    );

    release?.(new Map([[remote, "/jmap/imgproxy?u=hero&e=1&s=sig"]]));

    await waitFor(() => {
      expect(srcDoc()).toContain("/jmap/imgproxy?u=hero");
    });
    // And it was signed exactly once — the guard must not let the re-render
    // start a second request either.
    expect(signImageUrls).toHaveBeenCalledTimes(1);
  });
});

/**
 * The owner's second symptom — "there is nothing that lets me view them".
 *
 * The offer was always rendered; what it did was nothing, because the signed
 * map never survived (see above). Clicking it hid the banner and left the
 * gaps, which reads exactly like an affordance that does not work. This
 * covers the whole gesture: blocked, offered, clicked, loaded.
 */
describe("the show-images offer", () => {
  it("states the count, and the click actually loads them", async () => {
    const user = userEvent.setup();
    const remote = "https://cdn.example.com/banner.png";
    const signImageUrls = vi
      .fn()
      .mockResolvedValue(new Map([[remote, "/jmap/imgproxy?u=ban&e=1&s=sig"]]));

    const email: Email = {
      id: "m4",
      attachments: [],
      htmlBody: [HTML_PART],
      textBody: [],
      bodyValues: {
        "4": {
          value: `<p>Boletin</p><img src="${remote}">`,
          isEncodingProblem: false,
          isTruncated: false,
        },
      },
    };

    render(
      <I18nProvider locale="es">
        {/* The default posture: blocked, with the offer. */}
        <MessageBody email={email} signImageUrls={signImageUrls} />
      </I18nProvider>,
    );

    // Blocked first, and the reader SAYS so rather than showing silent gaps.
    expect(await screen.findByText(/1 imagen/i)).toBeInTheDocument();
    // The BODY, not the CSP header — which names the proxy either way.
    expect(srcDocBody()).not.toContain("/jmap/imgproxy");

    await user.click(screen.getByRole("button", { name: /mostrar imágenes/i }));

    await waitFor(() => {
      expect(srcDocBody()).toContain("/jmap/imgproxy?u=ban");
    });
  });
});
