import { describe, expect, it } from "vitest";

import { headerSection, unfoldHeaders } from "./rawMessage";

describe("slicing the header section", () => {
  it("cuts at the first CRLF CRLF, as the wire format says", () => {
    const raw = "From: a@b.c\r\nSubject: hi\r\n\r\nthe body\r\nmore body";
    expect(headerSection(raw)).toBe("From: a@b.c\nSubject: hi");
  });

  it("accepts bare LFs, which is what a message that has been through tools has", () => {
    expect(headerSection("From: a@b.c\nSubject: hi\n\nbody")).toBe("From: a@b.c\nSubject: hi");
  });

  /*
   * A headers-only message (a bounce, a delivery receipt) has no blank line at
   * all. Returning nothing for it would show an empty "Show original", which
   * looks exactly like a bug.
   */
  it("returns the whole thing when there is no body", () => {
    expect(headerSection("From: a@b.c\nSubject: bounced")).toBe(
      "From: a@b.c\nSubject: bounced",
    );
  });

  it("does not mistake a blank line INSIDE the body for the boundary", () => {
    const raw = "From: a@b.c\n\nfirst para\n\nsecond para";
    expect(headerSection(raw)).toBe("From: a@b.c");
  });

  it("handles an empty message without throwing", () => {
    expect(headerSection("")).toBe("");
  });
});

describe("unfolding", () => {
  it("joins a continuation line back onto its header", () => {
    expect(unfoldHeaders("References: <a@x>\n <b@x>\n <c@x>\nFrom: a@b.c")).toBe(
      "References: <a@x> <b@x> <c@x>\nFrom: a@b.c",
    );
  });

  it("leaves an unfolded section untouched", () => {
    expect(unfoldHeaders("From: a@b.c\nSubject: hi")).toBe("From: a@b.c\nSubject: hi");
  });

  it("treats a tab continuation the same as a space one", () => {
    expect(unfoldHeaders("Subject: a\n\tcontinued")).toBe("Subject: a continued");
  });
});
