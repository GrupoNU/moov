/**
 * Building a composer's initial state from an intent (P3 deliverable 2).
 *
 * Separated from the component so "what does a reply-all to THIS message look
 * like" is a pure function with tests, rather than a branch inside a 400-line
 * component that can only be verified by opening a browser and squinting.
 */

import type { AddressChip } from "../../mail/addresses";
import { makeChip, wireToChips } from "../../mail/addresses";
import {
  attributionFor,
  bodyAsHtml,
  bodyAsText,
  escapeHtml,
  forwardSubject,
  quoteHtml,
  quoteText,
  replyHeaders,
  replyRecipients,
  replySubject,
  textToHtml,
  type ComposeIntent,
} from "../../mail/quoting";
import type { Email } from "../../mail/types";

/** Everything the composer opens with. */
export interface ComposerDraft {
  readonly intent: ComposeIntent;
  /**
   * Identifies this composition. The body editor re-seeds when it changes and
   * only then — see BodyEditor's `seedKey`.
   */
  readonly seedKey: string;
  readonly to: readonly AddressChip[];
  readonly cc: readonly AddressChip[];
  readonly bcc: readonly AddressChip[];
  readonly subject: string;
  readonly text: string;
  /** Present when the composition starts in rich mode. */
  readonly html: string | undefined;
  /** The draft's existing server id, when resuming one. */
  readonly existingDraftId: string | undefined;
  readonly inReplyTo: readonly string[] | undefined;
  readonly references: readonly string[] | undefined;
  /** Which field takes focus when the dialog opens. */
  readonly focusField: "to" | "subject" | "body";
  /** The original's date, for the visually-hidden context line. */
  readonly reference: string | undefined;
}

/** How the attribution and forwarded-header lines are worded, from i18n. */
export interface QuotingStrings {
  readonly attributionLine: (date: string, sender: string) => string;
  readonly forwardedHeader: string;
  readonly from: string;
  readonly date: string;
  readonly subject: string;
  readonly to: string;
  /** Formats an ISO date the way the user reads dates. */
  readonly formatDate: (isoDate: string | undefined) => string;
}

let seedCounter = 0;
function nextSeed(prefix: string): string {
  seedCounter += 1;
  return `${prefix}-${String(seedCounter)}`;
}

/** A blank message. */
export function newDraft(preferHtml: boolean): ComposerDraft {
  return {
    intent: "new",
    seedKey: nextSeed("new"),
    to: [],
    cc: [],
    bcc: [],
    subject: "",
    text: "",
    html: preferHtml ? "" : undefined,
    existingDraftId: undefined,
    inReplyTo: undefined,
    references: undefined,
    focusField: "to",
    reference: undefined,
  };
}

/**
 * A reply or reply-all.
 *
 * The body opens with a blank line ABOVE the quote, so the caret lands where
 * the user writes (top-posting, which is what every mail client on earth does
 * and what recipients expect) rather than under a wall of quoted text.
 */
export function replyDraft(
  original: Email,
  accountEmail: string,
  all: boolean,
  strings: QuotingStrings,
): ComposerDraft {
  const { to, cc } = replyRecipients(original, accountEmail, all);
  const { inReplyTo, references } = replyHeaders(original);
  const attribution = attributionFor(original);
  const line = strings.attributionLine(strings.formatDate(attribution.sentAt), attribution.sender);

  const originalText = bodyAsText(original);
  const originalHtml = bodyAsHtml(original);

  const text = `\n\n${line}\n${quoteText(originalText)}`;
  const html =
    originalHtml === undefined
      ? `<p><br></p>${quoteHtml(line, textToHtml(originalText))}`
      : `<p><br></p>${quoteHtml(line, originalHtml)}`;

  return {
    intent: all ? "replyAll" : "reply",
    seedKey: nextSeed(`reply-${original.id}`),
    to,
    cc,
    bcc: [],
    subject: replySubject(original.subject),
    text,
    html,
    existingDraftId: undefined,
    inReplyTo,
    references,
    // Straight to the body: the recipients and the subject are already right.
    focusField: "body",
    reference: attribution.sentAt,
  };
}

/**
 * A forward.
 *
 * The quoted block is the RFC-conventional "Forwarded message" header — From,
 * Date, Subject, To — because a forward without those is a message whose
 * origin the recipient cannot establish.
 *
 * Attachments of the original are NOT carried over. On this server every body
 * part's `blobId` is null (phase 1 stores one blob per message), so there is
 * nothing to re-attach; a forward that silently dropped attachments the user
 * could see listed would be worse than one that never claimed to carry them.
 * The recipient list starts empty, so the user chooses where it goes.
 */
export function forwardDraft(original: Email, strings: QuotingStrings): ComposerDraft {
  const attribution = attributionFor(original);
  const headerLines = [
    strings.forwardedHeader,
    `${strings.from}: ${attribution.sender}`,
    `${strings.date}: ${strings.formatDate(attribution.sentAt)}`,
    `${strings.subject}: ${original.subject ?? ""}`,
    `${strings.to}: ${(original.to ?? []).map((address) => address.email).join(", ")}`,
  ];

  const originalText = bodyAsText(original);
  const originalHtml = bodyAsHtml(original);

  const text = `\n\n${headerLines.join("\n")}\n\n${originalText}`;
  const htmlHeader = headerLines.map((entry) => `<div>${escapeHtml(entry)}</div>`).join("");
  const html = `<p><br></p>${htmlHeader}<div><br></div>${originalHtml ?? textToHtml(originalText)}`;

  return {
    intent: "forward",
    seedKey: nextSeed(`fwd-${original.id}`),
    to: [],
    cc: [],
    bcc: [],
    subject: forwardSubject(original.subject),
    text,
    html,
    existingDraftId: undefined,
    inReplyTo: undefined,
    references: undefined,
    // A forward has no recipient yet, so that is where the caret belongs.
    focusField: "to",
    reference: attribution.sentAt,
  };
}

/**
 * Resuming an existing draft.
 *
 * The draft's server id is carried so the next save DESTROYS this revision
 * rather than accumulating one message per keystroke burst in Drafts — the
 * consequence of §4.6's immutability, handled in `saveDraft`.
 */
export function resumeDraft(existing: Email): ComposerDraft {
  const html = bodyAsHtml(existing);
  return {
    intent: "draft",
    seedKey: nextSeed(`draft-${existing.id}`),
    to: wireToChips(existing.to),
    cc: wireToChips(existing.cc),
    bcc: wireToChips(existing.bcc),
    subject: existing.subject ?? "",
    text: bodyAsText(existing),
    html,
    existingDraftId: existing.id,
    inReplyTo: existing.inReplyTo ?? undefined,
    references: existing.references ?? undefined,
    focusField: "body",
    reference: existing.sentAt ?? existing.receivedAt,
  };
}

/** A message addressed to one recipient — the "write to this sender" path. */
export function draftTo(address: string, preferHtml: boolean): ComposerDraft {
  return { ...newDraft(preferHtml), to: [makeChip(address)], focusField: "subject" };
}
