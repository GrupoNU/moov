import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { BrandingProvider } from "../../branding/BrandingProvider";
import { I18nProvider } from "../../i18n/I18nProvider";
import { MOOV_DEFAULT_BRANDING } from "../../branding/branding";
import { derivePalette } from "../../branding/palette";
import type { BrandAdminDoc } from "../../branding/adminApi";
import { BrandSection, type BrandSectionProps } from "./BrandSection";

/**
 * The brand administration panel.
 *
 * The properties worth a test, in the order they would hurt if they broke:
 *
 *   1. The live preview NEVER touches `document.documentElement`. This is the
 *      one that a later "simplification" reaches for `applyBranding` and
 *      silently breaks — and the symptom is the whole app strobing while an
 *      administrator drags a colour picker, which no unit test would notice
 *      unless it was written to.
 *   2. A partial PUT carries only the changed field, and "" means clear.
 *   3. The AA notice appears for a colour that cannot clear 4.5:1 and stays
 *      away for one that can.
 *   4. A refused upload is refused BEFORE the request, beside the control.
 */

const DOC: BrandAdminDoc = {
  host: "mail.acme.example",
  isDefault: false,
  name: "Acme Mail",
  shortName: "Acme",
  tagline: "Correo de Acme",
  supportUrl: "https://acme.example/help",
  privacyUrl: "",
  termsUrl: "",
  colors: {
    primary: "#5b5bd6",
    onPrimary: "",
    splashFrom: "#1e1b4b",
    splashTo: "#4c1d95",
  },
  assets: {
    logo: { url: "/branding/assets/logo.png?v=3", bytes: 4096, width: 320, height: 80 },
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
  iconUrls: {
    "icon-192": "/branding/icons/icon-192.png?v=3",
    "icon-maskable-192": "/branding/icons/icon-maskable-192.png?v=3",
  },
  version: 3,
};

function renderSection(
  overrides: Partial<BrandSectionProps> = {},
  docOverrides: Partial<BrandAdminDoc> = {},
): {
  readonly onSave: ReturnType<typeof vi.fn>;
  readonly onUploadAsset: ReturnType<typeof vi.fn>;
  readonly onRemoveAsset: ReturnType<typeof vi.fn>;
  readonly onReset: ReturnType<typeof vi.fn>;
} {
  const onSave = vi.fn(() => Promise.resolve(true));
  const onUploadAsset = vi.fn(() => Promise.resolve(true));
  const onRemoveAsset = vi.fn(() => Promise.resolve(true));
  const onReset = vi.fn(() => Promise.resolve(true));
  render(
    /* An explicit `branding` short-circuits the provider's fetch, so these
       tests exercise the SECTION rather than the transport. */
    <BrandingProvider branding={MOOV_DEFAULT_BRANDING}>
      <I18nProvider locale="es">
        <BrandSection
          doc={{ ...DOC, ...docOverrides }}
          onSave={onSave}
          onUploadAsset={onUploadAsset}
          onRemoveAsset={onRemoveAsset}
          onReset={onReset}
          {...overrides}
        />
      </I18nProvider>
    </BrandingProvider>,
  );
  return { onSave, onUploadAsset, onRemoveAsset, onReset };
}

/** A file of a chosen type and size, without allocating the bytes. */
function fileOf(name: string, type: string, size: number): File {
  const file = new File(["x"], name, { type });
  Object.defineProperty(file, "size", { value: size });
  return file;
}

describe("the text fields save on blur, one field at a time", () => {
  it("PUTs ONLY the field that changed", async () => {
    const user = userEvent.setup();
    const { onSave } = renderSection();

    const name = screen.getByLabelText("Nombre");
    await user.clear(name);
    await user.type(name, "Área Mail");
    await user.tab();

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave).toHaveBeenCalledWith({ name: "Área Mail" });
  });

  it("sends the EMPTY STRING to clear a field, which is not the same as omitting it", async () => {
    const user = userEvent.setup();
    const { onSave } = renderSection();

    const tagline = screen.getByLabelText("Bajada");
    await user.clear(tagline);
    await user.tab();

    // Absent would mean "unchanged"; "" is the only way to say "remove it".
    expect(onSave).toHaveBeenCalledWith({ tagline: "" });
  });

  it("saves on Enter as well as on blur", async () => {
    const user = userEvent.setup();
    const { onSave } = renderSection();
    await user.type(screen.getByLabelText("Bajada"), " nuevo{Enter}");
    expect(onSave).toHaveBeenCalledWith({ tagline: "Correo de Acme nuevo" });
  });

  it("does NOT write when the value is unchanged", async () => {
    const user = userEvent.setup();
    const { onSave } = renderSection();
    await user.click(screen.getByLabelText("Nombre"));
    await user.tab();
    expect(onSave).not.toHaveBeenCalled();
  });

  it("shows the page's own receipt after a save", async () => {
    const user = userEvent.setup();
    renderSection();
    await user.type(screen.getByLabelText("Nombre"), "!{Enter}");
    expect(await screen.findByText("Guardado ✓")).toBeInTheDocument();
  });

  it("counts the short name LIVE, because twelve is a hard server rule", async () => {
    const user = userEvent.setup();
    renderSection();
    expect(screen.getByText("4 de 12 caracteres")).toBeInTheDocument();
    await user.type(screen.getByLabelText("Nombre corto"), "!!");
    expect(screen.getByText("6 de 12 caracteres")).toBeInTheDocument();
  });

  it("caps the short name at twelve in the input itself", () => {
    renderSection();
    expect(screen.getByLabelText("Nombre corto")).toHaveAttribute("maxLength", "12");
  });
});

describe("the link fields validate the scheme before the round trip", () => {
  it.each([
    ["https://acme.example/otro"],
    ["http://acme.example/help"],
    ["mailto:soporte@acme.example"],
    [""],
  ])("accepts %s", async (value) => {
    const user = userEvent.setup();
    const { onSave } = renderSection();
    const field = screen.getByLabelText("Soporte");
    await user.clear(field);
    if (value !== "") await user.type(field, value);
    await user.tab();
    expect(onSave).toHaveBeenCalledWith({ supportUrl: value });
  });

  it.each([["javascript:alert(1)"], ["data:text/html,<script>"], ["acme.example"]])(
    "refuses %s at the field, with no request",
    async (value) => {
      const user = userEvent.setup();
      const { onSave } = renderSection();
      const field = screen.getByLabelText("Soporte");
      await user.clear(field);
      await user.type(field, value);
      await user.tab();

      expect(onSave).not.toHaveBeenCalled();
      expect(screen.getByRole("alert")).toHaveTextContent(/https:\/\//);
      expect(field).toHaveAttribute("aria-invalid", "true");
    },
  );

  it("marks the control the SERVER named in a 400", () => {
    renderSection({ errorField: "privacyUrl", error: "No se pudo guardar" });
    expect(screen.getByLabelText("Política de privacidad")).toHaveAttribute(
      "aria-invalid",
      "true",
    );
    expect(screen.getByLabelText("Soporte")).toHaveAttribute("aria-invalid", "false");
  });
});

describe("the colour picker, and the preview that must not escape its box", () => {
  it("NEVER writes a brand seed onto <html> — the page is not the preview", async () => {
    const user = userEvent.setup();
    renderSection();
    const root = document.documentElement;
    const before = root.style.getPropertyValue("--brand-primary");

    const hex = screen.getByLabelText("Color principal, en hexadecimal");
    await user.clear(hex);
    await user.type(hex, "#ff0000");

    /*
     * The whole point of the scoped container. `applyBranding` writes these
     * same names onto the root, correctly, for a brand that has been SAVED —
     * and reaching for it here would repaint the app through every hue an
     * administrator drags past.
     */
    expect(root.style.getPropertyValue("--brand-primary")).toBe(before);
    expect(root.style.getPropertyValue("--color-accent")).toBe("");
  });

  it("derives the preview's variables from the TYPED colour, on the container", async () => {
    const user = userEvent.setup();
    renderSection();
    const hex = screen.getByLabelText("Color principal, en hexadecimal");
    await user.clear(hex);
    await user.type(hex, "#ff0000");

    const light = document.querySelector<HTMLElement>('[data-theme-preview="light"]');
    const dark = document.querySelector<HTMLElement>('[data-theme-preview="dark"]');
    expect(light).not.toBeNull();
    expect(dark).not.toBeNull();

    const expected = derivePalette("#ff0000");
    expect(light?.style.getPropertyValue("--brand-primary")).toBe("#ff0000");
    // The SEMANTIC token is re-pointed locally too, which is what lets a dark
    // pane live inside a light page.
    expect(light?.style.getPropertyValue("--color-accent")).toBe(expected.light.accent);
    expect(dark?.style.getPropertyValue("--color-accent")).toBe(expected.dark.accent);
    // And the two panes really do differ, which is the reason both exist.
    expect(light?.style.getPropertyValue("--color-accent")).not.toBe(
      dark?.style.getPropertyValue("--color-accent"),
    );
  });

  it("shows the AA notice for a colour that cannot clear 4.5:1 — and names the hex used", async () => {
    const user = userEvent.setup();
    renderSection();
    const hex = screen.getByLabelText("Color principal, en hexadecimal");
    await user.clear(hex);
    await user.type(hex, "#c0ffee");

    const adjusted = derivePalette("#c0ffee");
    // A pale mint fails in the LIGHT theme, and the sentence must name the
    // colour the administrator will actually see.
    const notice = await screen.findByText(/se oscureció a/);
    expect(notice).toHaveTextContent(adjusted.light.accent);
    expect(notice).toHaveTextContent("4,5:1");
  });

  it("shows NO light-theme notice for a colour that passes as sent", async () => {
    const user = userEvent.setup();
    renderSection();
    const hex = screen.getByLabelText("Color principal, en hexadecimal");
    await user.clear(hex);
    await user.type(hex, "#5b5bd6");
    // Moov's own primary passes in light and is lifted in dark; the light
    // notice must not appear, or the notice means nothing.
    expect(screen.queryByText(/se oscureció a/)).not.toBeInTheDocument();
  });

  it("refuses a value that is not a hex, at the field, with no request", async () => {
    const user = userEvent.setup();
    const { onSave } = renderSection();
    const hex = screen.getByLabelText("Color principal, en hexadecimal");
    await user.clear(hex);
    await user.type(hex, "rebeccapurple");
    await user.tab();
    expect(onSave).not.toHaveBeenCalled();
    expect(screen.getByRole("alert")).toHaveTextContent("#5b5bd6");
  });

  it("saves a good colour as a nested one-key patch", async () => {
    const user = userEvent.setup();
    const { onSave } = renderSection();
    const hex = screen.getByLabelText("Color principal, en hexadecimal");
    await user.clear(hex);
    await user.type(hex, "#123456");
    await user.tab();
    expect(onSave).toHaveBeenCalledWith({ colors: { primary: "#123456" } });
  });

  it("gives the colour a TEXT alternative to the well, which is the primary control", () => {
    renderSection();
    // A brand colour arrives as a hex string in a guideline; there is no way to
    // type one into a native colour well, and it is the control screen readers
    // handle worst.
    expect(screen.getByLabelText("Color principal, en hexadecimal")).toHaveAttribute(
      "type",
      "text",
    );
    expect(screen.getByLabelText("Color principal")).toHaveAttribute("type", "color");
  });

  it("clears the stored hint when the administrator returns to automatic", async () => {
    const user = userEvent.setup();
    const { onSave } = renderSection({}, { colors: { ...DOC.colors, onPrimary: "#000000" } });
    await user.click(screen.getByLabelText("Automático"));
    // "Automatic" MEANS the stored hint goes away so the derivation owns it.
    expect(onSave).toHaveBeenCalledWith({ colors: { onPrimary: "" } });
  });

  it("reveals the override field only when asked, seeded with the derived value", async () => {
    const user = userEvent.setup();
    renderSection();
    expect(
      screen.queryByLabelText("Texto sobre el color principal, en hexadecimal"),
    ).not.toBeInTheDocument();
    await user.click(screen.getByLabelText("Elegirlo yo"));
    const field = screen.getByLabelText("Texto sobre el color principal, en hexadecimal");
    // Seeded with what the derivation chose: the administrator adjusts a good
    // value rather than fixing black.
    expect(field).toHaveValue(derivePalette(DOC.colors.primary).light.onAccent);
  });
});

describe("uploads", () => {
  it("uploads a good file to the slot that asked for it", async () => {
    const user = userEvent.setup();
    const { onUploadAsset } = renderSection();
    /*
     * The picker BUTTON is what a user operates and what carries the slot's
     * name; the `<input type="file">` beside it is only where the bytes land,
     * which is why it is reached by a test id rather than by a label of its own.
     */
    expect(
      screen.getByRole("button", { name: "Elegir una imagen para Logo para fondos oscuros" }),
    ).toBeInTheDocument();
    const file = fileOf("dark.png", "image/png", 2048);
    await user.upload(screen.getByTestId("brand-file-logoDark"), file);
    await waitFor(() => {
      expect(onUploadAsset).toHaveBeenCalledWith("logoDark", file);
    });
  });

  it("refuses an over-size file BEFORE the request, beside the control", async () => {
    const user = userEvent.setup();
    const { onUploadAsset } = renderSection();
    const input = document.querySelectorAll<HTMLInputElement>('input[type="file"]')[0];
    expect(input).toBeDefined();
    await user.upload(input!, fileOf("big.png", "image/png", 3 * 1024 * 1024));
    expect(onUploadAsset).not.toHaveBeenCalled();
    expect(await screen.findByText(/más de 2 MB/)).toBeInTheDocument();
  });

  it("refuses an SVG DROPPED on the zone, and explains why in the section", async () => {
    /*
     * Dropped rather than picked, and that is the case worth testing: the file
     * picker's `accept` already filters an SVG out, but a DRAG-AND-DROP has no
     * accept filter at all — the browser hands over whatever was dragged. The
     * client pre-check is the only thing between that and a request that was
     * never going to succeed.
     */
    const { onUploadAsset } = renderSection();
    const zone = document.querySelector<HTMLElement>('[class*="dropZone"]');
    expect(zone).not.toBeNull();
    fireEvent.drop(zone!, {
      dataTransfer: { files: [fileOf("logo.svg", "image/svg+xml", 1024)] },
    });
    await waitFor(() => {
      expect(screen.getByText(/no es PNG, JPEG, WebP ni GIF/)).toBeInTheDocument();
    });
    expect(onUploadAsset).not.toHaveBeenCalled();
    // And the standing explanation, so the refusal is not a surprise.
    expect(screen.getByText(/SVG se rechaza/)).toBeInTheDocument();
  });

  it("accepts a good file DROPPED on the zone", async () => {
    const { onUploadAsset } = renderSection();
    const zone = document.querySelector<HTMLElement>('[class*="dropZone"]');
    const file = fileOf("logo.png", "image/png", 2048);
    fireEvent.drop(zone!, { dataTransfer: { files: [file] } });
    await waitFor(() => {
      expect(onUploadAsset).toHaveBeenCalledWith("logo", file);
    });
  });

  it("shows a logo over BOTH grounds — the failure this slot exists to prevent", () => {
    renderSection();
    expect(screen.getByText("Sobre claro")).toBeInTheDocument();
    expect(screen.getByText("Sobre oscuro")).toBeInTheDocument();
  });

  it("removes an asset from the slot that has one, and offers no removal where there is none", async () => {
    const user = userEvent.setup();
    const { onRemoveAsset } = renderSection();
    await user.click(screen.getByLabelText("Quitar la imagen de Logo"));
    expect(onRemoveAsset).toHaveBeenCalledWith("logo");
    expect(screen.queryByLabelText("Quitar la imagen de Icono cuadrado")).not.toBeInTheDocument();
  });

  it("renders the generated icons at EXACTLY the URLs the document gave", () => {
    renderSection();
    // Reconstructing the path would drop the `?v=` cache-buster, which is the
    // only thing that makes a re-uploaded icon visible without a hard reload.
    expect(screen.getByAltText("icon-192")).toHaveAttribute(
      "src",
      "/branding/icons/icon-192.png?v=3",
    );
    expect(screen.getByAltText("icon-maskable-192")).toHaveAttribute(
      "src",
      "/branding/icons/icon-maskable-192.png?v=3",
    );
  });

  it("says the icons came from the logo when no square icon is set", () => {
    renderSection();
    expect(screen.getByText(/porque no hay un icono cuadrado/)).toBeInTheDocument();
  });

  it("renders the server's warnings as a muted list", () => {
    renderSection({}, { warnings: ["El icono no es cuadrado: se recortó al centro."] });
    expect(screen.getByText("Para tener en cuenta")).toBeInTheDocument();
    expect(
      screen.getByText("El icono no es cuadrado: se recortó al centro."),
    ).toBeInTheDocument();
  });

  it("shows no warnings heading when there are none", () => {
    renderSection();
    expect(screen.queryByText("Para tener en cuenta")).not.toBeInTheDocument();
  });
});

describe("the reset", () => {
  it("asks before clearing, and says what is cleared", async () => {
    const user = userEvent.setup();
    const { onReset } = renderSection();
    await user.click(screen.getByRole("button", { name: "Restablecer la marca" }));
    // The dialog says what goes, not "are you sure": the images are removed
    // from the server, not merely unlinked.
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(/Se borran el nombre, los colores/);
    await user.click(within(dialog).getByRole("button", { name: "Restablecer la marca" }));
    await waitFor(() => {
      expect(onReset).toHaveBeenCalled();
    });
  });

  it("does nothing when the confirmation is declined", async () => {
    const user = userEvent.setup();
    const { onReset } = renderSection();
    await user.click(screen.getByRole("button", { name: "Restablecer la marca" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: /Cancelar/i }));
    expect(onReset).not.toHaveBeenCalled();
  });
});

describe("the section's own error strip", () => {
  it("announces a held failure at the top, as an alert", () => {
    renderSection({ error: "Eso no se pudo guardar." });
    expect(screen.getByRole("alert")).toHaveTextContent("Eso no se pudo guardar.");
  });
});

describe("the settings search filters the groups (D-5)", () => {
  it("renders only the group a search surfaced", () => {
    renderSection({ showRow: (id) => id === "brandColors" });
    expect(screen.getByText("Color")).toBeInTheDocument();
    expect(screen.queryByText("Imágenes")).not.toBeInTheDocument();
    expect(screen.queryByText("Nombre y textos")).not.toBeInTheDocument();
  });

  it("renders every group when nothing is filtering", () => {
    renderSection();
    for (const heading of ["Nombre y textos", "Enlaces", "Color", "Imágenes", "Restablecer"]) {
      expect(screen.getByText(heading)).toBeInTheDocument();
    }
  });
});
