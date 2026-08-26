/**
 * Reply, reply-all and forward: recipients, subject, and the quoted body (P3).
 *
 * # Why quoting is a pure module with its own tests
 *
 * Quoting is the part of a composer that everyone gets subtly wrong and nobody
 * notices until a thread is unreadable. The failure modes are all silent:
 * quoting the *sanitized* HTML of a message and then having it sanitized again
 * on the next reply (progressive mangling); building an attribution line by
 * string concatenation and shipping the English one to a Spanish user;
 * forgetting that `Reply-To` overrides `From`; replying-all to yourself. Each
 * of those is a pure function of the message plus the account address, so each
 * is enumerable in a test rather than discovered in a thread.
 *
 * # The three rules that matter
 *
 * 1. **Reply-To wins over From.** RFC 5322 §3.6.2 exists precisely so a sender
 *    can direct replies elsewhere (a ticketing address, a list). Ignoring it
 *    sends the reply to a mailbox nobody reads.
 *
 * 2. **The quoted HTML is UNTRUSTED and stays that way.** This module produces
 *    a `<blockquote>` wrapper around the original HTML *as a string*; it is
 *    never inserted into the DOM here. The composer sanitizes the whole
 *    document through the P2b pipeline before it is editable, and the
 *    assembled message is sanitized again on the way out — compose output is
 *    untrusted input to the next reader, which is this epic's standing rule.
 *
 * 3. **References/In-Reply-To are threading, not decoration.** The server
 *    threads on them (L2-sync-engine §2.3), so a reply that omits them starts
 *    a new conversation in every client on earth. `replyHeaders` builds the
 *    pair per RFC 5322 §3.6.4: In-Reply-To is the parent's Message-ID, and
 *    References is the parent's References plus the parent's Message-ID,
 *    capped so a 200-message thread does not grow an unbounded header.
 */

import { formatAddress, type AddressChip, dedupeAddresses, wireToChips } from "./addresses";
import type { Email, EmailAddress } from "./types";

/** What kind of composition the user started. */
export type ComposeIntent = "new" | "reply" | "replyAll" | "forward" | "draft";

/**
 * The subject prefix caps.
 *
 * RFC 5322 places no limit; mail clients that keep stacking "Re: Re: Re:"
 * produce subjects that no longer fit a row. One prefix is the convention
 * every modern client follows.
 */
const REPLY_PREFIX = /^\s*re\s*:\s*/i;
const FORWARD_PREFIX = /^\s*(?:fwd?|rv|tr|wg)\s*:\s*/i;

/** `Re: <subject>`, without stacking a second `Re:`. */
export function replySubject(subject: string | null | undefined): string {
  const base = (subject ?? "").trim();
  if (base === "") return "Re:";
  return REPLY_PREFIX.test(base) ? base : `Re: ${base}`;
}

/** `Fwd: <subject>`, without stacking. */
export function forwardSubject(subject: string | null | undefined): string {
  const base = (subject ?? "").trim();
  if (base === "") return "Fwd:";
  return FORWARD_PREFIX.test(base) ? base : `Fwd: ${base}`;
}

/**
 * Who a reply goes to.
 *
 * `replyTo` overrides `from` (RFC 5322 §3.6.2). Reply-all adds the original
 * To and Cc as Cc, minus the account's own address and minus anyone already
 * in To — otherwise the sender receives their own reply and someone gets two
 * copies.
 */
export function replyRecipients(
  email: Email,
  accountEmail: string,
  all: boolean,
): { to: readonly AddressChip[]; cc: readonly AddressChip[] } {
  const primary = email.replyTo ?? email.from ?? [];
  const to = dedupeAddresses(wireToChips(primary), []);

  if (!all) return { to, cc: [] };

  const others: EmailAddress[] = [...(email.to ?? []), ...(email.cc ?? [])];
  const exclude = [accountEmail, ...to.map((chip) => chip.email)];
  const cc = dedupeAddresses(wireToChips(others), exclude);
  return { to, cc };
}

/** Everyone a forward starts with: nobody. */
export function forwardRecipients(): { to: readonly AddressChip[]; cc: readonly AddressChip[] } {
  return { to: [], cc: [] };
}

/**
 * The In-Reply-To / References pair for a reply (RFC 5322 §3.6.4).
 *
 * `maxReferences` caps the chain. RFC 5322 says a client SHOULD include the
 * whole parent chain, but a 300-message thread produces a header of several
 * kilobytes that some MTAs truncate mid-token — corrupting threading for
 * everyone downstream. The convention every large client follows is to keep
 * the FIRST reference (the thread root, which is what threading algorithms
 * anchor on) and the last N, which is what this does.
 */
export function replyHeaders(
  parent: Email,
  maxReferences = 20,
): { inReplyTo: readonly string[]; references: readonly string[] } {
  const parentId = parent.messageId?.[0];
  if (parentId === undefined) {
    // A message with no Message-ID cannot be threaded onto; the reply starts
    // its own conversation, which is the only honest outcome.
    return { inReplyTo: [], references: [] };
  }

  const existing = [...(parent.references ?? [])];
  const chain = [...existing, parentId];

  if (chain.length <= maxReferences) {
    return { inReplyTo: [parentId], references: chain };
  }
  const root = chain[0];
  const tail = chain.slice(chain.length - (maxReferences - 1));
  return {
    inReplyTo: [parentId],
    references: root === undefined ? tail : [root, ...tail],
  };
}

/** The pieces an attribution line needs, so the i18n table owns the wording. */
export interface Attribution {
  /** ISO date of the original, for the caller to format in the user's locale. */
  readonly sentAt: string | undefined;
  /** The sender, rendered as `Ana <ana@x.com>` or the bare address. */
  readonly sender: string;
}

/** Extracts the attribution facts from a message. */
export function attributionFor(email: Email): Attribution {
  const first = email.from?.[0];
  const sender =
    first === undefined
      ? ""
      : formatAddress({ name: first.name ?? undefined, email: first.email });
  return {
    sentAt: email.sentAt ?? email.receivedAt,
    sender,
  };
}

/**
 * Prefixes every line with "> " — the plain-text quoting convention since
 * RFC 3676, and what every client renders as a quote level.
 *
 * Already-quoted lines get a second `>` with no space between (`>> text`),
 * which is what makes quote DEPTH visible; adding a space each level would
 * make deep quotes drift right until they wrap.
 */
export function quoteText(body: string): string {
  return body
    .split(/\r\n|\r|\n/)
    .map((line) => (line.startsWith(">") ? `>${line}` : `> ${line}`))
    .join("\n");
}

/**
 * Wraps original HTML in the quote structure Gmail/Thunderbird both emit and
 * both recognise.
 *
 * The `type="cite"` attribute and the left border are what other clients key
 * off to collapse a quote. The original HTML is embedded VERBATIM as a string
 * — see rule 2 in the file header: this function never parses or trusts it,
 * and the composer sanitizes the assembled document before it is editable.
 */
export function quoteHtml(attributionLine: string, originalHtml: string): string {
  return (
    `<p>${escapeHtml(attributionLine)}</p>` +
    `<blockquote type="cite" style="margin:0 0 0 0.8ex;border-left:2px solid #ccc;padding-left:1ex">` +
    originalHtml +
    `</blockquote>`
  );
}

/**
 * Escapes text for insertion into HTML.
 *
 * Used for the attribution line and for the text→HTML conversion below, both
 * of which build markup from strings that came from a message. Without it, a
 * sender whose display name is `<img onerror=...>` would inject markup into
 * the reply — which the sanitizer would catch, but a defense that relies on
 * the next layer is not a defense.
 */
export function escapeHtml(value: string): string {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

/**
 * Renders a plain-text body as HTML, preserving line structure.
 *
 * Used when replying to a text-only message from the HTML composer. Blank
 * lines become paragraph breaks and single newlines become `<br>`, which is
 * how a reader expects a plain-text quote to look.
 */
export function textToHtml(text: string): string {
  return text
    .split(/\n{2,}/)
    .map((block) => `<p>${escapeHtml(block).replace(/\n/g, "<br>")}</p>`)
    .join("");
}

/**
 * Renders HTML as plain text, well enough for the text/plain alternative.
 *
 * # Why a DOM walk and not a regex
 *
 * Stripping tags with a regex is the canonical wrong answer: it leaves entity
 * references undecoded (`&amp;` in the text part), it concatenates block
 * elements with no line breaks (a table becomes one endless line), and it can
 * be defeated by malformed markup. Parsing with `DOMParser` into an INERT
 * document — which does not execute scripts, load resources, or run any
 * handler — and reading `textContent` per block gives the browser's own
 * correct answer.
 *
 * This is safe precisely because nothing from the parsed document reaches the
 * live DOM: only strings come back out.
 */
export function htmlToText(html: string): string {
  if (typeof DOMParser === "undefined") {
    // Non-browser fallback (tests in a bare environment): strip tags crudely
    // rather than throw. The browser path is the one that ships.
    return html.replace(/<[^>]*>/g, " ").replace(/\s+/g, " ").trim();
  }
  const doc = new DOMParser().parseFromString(html, "text/html");
  const blocks = ["P", "DIV", "BR", "LI", "TR", "H1", "H2", "H3", "H4", "H5", "H6", "BLOCKQUOTE"];

  const parts: string[] = [];
  const walk = (node: Node): void => {
    if (node.nodeType === 3) {
      parts.push(node.textContent ?? "");
      return;
    }
    if (node.nodeType !== 1) return;
    const element = node as Element;
    const tag = element.tagName.toUpperCase();
    if (tag === "SCRIPT" || tag === "STYLE" || tag === "HEAD") return;
    if (tag === "BR") {
      parts.push("\n");
      return;
    }
    const isBlock = blocks.includes(tag);
    if (isBlock) parts.push("\n");
    for (const child of Array.from(element.childNodes)) walk(child);
    if (isBlock) parts.push("\n");
  };
  walk(doc.body);

  return parts
    .join("")
    .replace(/[ \t]+/g, " ")
    .replace(/\n{3,}/g, "\n\n")
    .trim();
}

/** The body of a message as plain text, whichever part it has. */
export function bodyAsText(email: Email): string {
  const textPart = email.textBody?.[0];
  if (textPart?.partId !== undefined && textPart.partId !== null) {
    const value = email.bodyValues?.[textPart.partId]?.value;
    if (value !== undefined) return value;
  }
  const htmlPart = email.htmlBody?.[0];
  if (htmlPart?.partId !== undefined && htmlPart.partId !== null) {
    const value = email.bodyValues?.[htmlPart.partId]?.value;
    if (value !== undefined) return htmlToText(value);
  }
  return "";
}

/** The body of a message as HTML, or undefined when it only has text. */
export function bodyAsHtml(email: Email): string | undefined {
  const htmlPart = email.htmlBody?.[0];
  if (htmlPart?.partId !== undefined && htmlPart.partId !== null) {
    return email.bodyValues?.[htmlPart.partId]?.value;
  }
  return undefined;
}
