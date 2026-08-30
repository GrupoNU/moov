import { describe, expect, it } from "vitest";

import {
  BLOCKED_EXTENSIONS,
  finalExtension,
  isBlockedAttachment,
} from "./blockedExtensions";

/**
 * The blocked-extension gate (E7, canon §2.3).
 *
 * The list itself is data and a test that re-asserts it would just be the list
 * written twice. What is worth pinning is the MATCHER, because every bypass of
 * a block like this one is a filename trick rather than a missing entry.
 */

describe("finalExtension", () => {
  it("takes the last dot-segment, lowercased", () => {
    expect(finalExtension("report.PDF")).toBe("pdf");
    expect(finalExtension("archive.tar.gz")).toBe("gz");
  });

  it("returns undefined for a name with no extension", () => {
    // The important half: a file literally named "exe" must not be blocked.
    expect(finalExtension("README")).toBeUndefined();
    expect(finalExtension("exe")).toBeUndefined();
  });

  it("treats a dotfile's name as a name, not an extension", () => {
    expect(finalExtension(".bashrc")).toBeUndefined();
    expect(finalExtension(".exe")).toBeUndefined();
  });

  it("strips trailing dots and spaces, which Windows discards when opening", () => {
    expect(finalExtension("payload.exe.")).toBe("exe");
    expect(finalExtension("payload.exe ")).toBe("exe");
    expect(finalExtension("payload.exe. . .")).toBe("exe");
  });

  it("considers only the basename, never a directory component", () => {
    expect(finalExtension("../../payload.exe")).toBe("exe");
    expect(finalExtension("c:\\temp\\payload.exe")).toBe("exe");
    // A dot in a directory name must not become the file's extension.
    expect(finalExtension("v1.2/README")).toBeUndefined();
  });

  it("returns undefined for an empty or dots-only name", () => {
    expect(finalExtension("")).toBeUndefined();
    expect(finalExtension("...")).toBeUndefined();
    expect(finalExtension("   ")).toBeUndefined();
  });
});

describe("isBlockedAttachment", () => {
  it("blocks a plain executable, in any case", () => {
    expect(isBlockedAttachment("setup.exe")).toBe(true);
    expect(isBlockedAttachment("SETUP.EXE")).toBe(true);
    expect(isBlockedAttachment("Setup.Exe")).toBe(true);
  });

  it("blocks the double-extension trick — the case the block exists for", () => {
    // Windows hides known extensions, so this renders as "invoice.pdf".
    expect(isBlockedAttachment("invoice.pdf.exe")).toBe(true);
    expect(isBlockedAttachment("photo.jpg.scr")).toBe(true);
    expect(isBlockedAttachment("contrato.docx.js")).toBe(true);
  });

  it("does NOT block a blocked extension that is not the final one", () => {
    // The inverse of the case above, and just as important: the OS dispatches
    // on the final extension, so this opens in a text editor.
    expect(isBlockedAttachment("payload.exe.txt")).toBe(false);
    expect(isBlockedAttachment("notes.about.dll.md")).toBe(false);
  });

  it("allows ordinary documents", () => {
    for (const name of [
      "informe.pdf",
      "hoja.xlsx",
      "foto.jpeg",
      "presentacion.pptx",
      "datos.csv",
      "mensaje.eml",
    ]) {
      expect(isBlockedAttachment(name)).toBe(false);
    }
  });

  it("allows archives — we cannot inspect them, and say so", () => {
    // Documented limitation, not an oversight: Gmail scans inside containers,
    // we cannot client-side. Pinned so removing the behaviour is a deliberate
    // act rather than a silent one.
    expect(isBlockedAttachment("fotos.zip")).toBe(false);
    expect(isBlockedAttachment("backup.tar.gz")).toBe(false);
    expect(isBlockedAttachment("archivo.7z")).toBe(false);
  });

  it("blocks every extension on the published list", () => {
    for (const extension of BLOCKED_EXTENSIONS) {
      expect(isBlockedAttachment(`archivo.${extension}`)).toBe(true);
    }
  });

  it("keeps the odd-looking entries Gmail publishes", () => {
    // `.ex` and `.ex_` are real neutered-executable conventions, not typos.
    // A future cleanup that "fixes" the list would break exactly here.
    expect(isBlockedAttachment("thing.ex")).toBe(true);
    expect(isBlockedAttachment("thing.ex_")).toBe(true);
    expect(isBlockedAttachment("module.mjs")).toBe(true);
  });

  it("does not block a name that merely contains a blocked word", () => {
    expect(isBlockedAttachment("executable-notes.txt")).toBe(false);
    expect(isBlockedAttachment("jarra.png")).toBe(false);
  });
});
