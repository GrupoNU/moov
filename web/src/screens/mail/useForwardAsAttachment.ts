import { useCallback, useState } from "react";

import type { JmapClient } from "../../api/jmap";
import {
  emlFilename,
  partitionBySize,
  RFC822_TYPE,
} from "../../mail/forwardAsAttachment";
import type { Email } from "../../mail/types";
import { maxAttachmentsSize, maxUploadSize, uploadBlob, uploadUrlFor } from "../../mail/write";
import type { ComposerAttachment } from "../compose/AttachmentList";

/**
 * Forwarding messages as `.eml` attachments (L3 E7; canon §2.3).
 *
 * # The round trip, and why it is one
 *
 * The bytes have to leave the server and come back: our own download route
 * hands us the raw RFC 822 message, and the upload route turns it into a
 * `blobId` a draft can reference. There is no server-side "attach this message
 * to that draft" method in RFC 8621 — `Email/set` takes blob ids, and a blob id
 * is what upload produces. So the client fetches and re-posts.
 *
 * The download cannot be a plain `<a download>` for the same reason
 * `DownloadOriginalButton` documents: the route requires HTTP Basic, and a
 * browser navigation carries no `Authorization` header, so the pilot answers
 * 401 and the browser pops its own credential dialog at the user.
 *
 * # Multi-select, and the partial success that has to be reported
 *
 * Selecting four messages and forwarding them produces four attachments — or
 * three plus an honest sentence naming the fourth. Failing the whole operation
 * because one message was over the cap would be the wrong trade: the user asked
 * for four things, three of them are possible, and the client's job is to do
 * those and say what it could not do.
 */

export interface ForwardAsAttachmentApi {
  /** True while the blobs are being fetched and re-uploaded. */
  readonly isPreparing: boolean;
  /**
   * Prepares `.eml` attachments for these messages.
   *
   * Resolves with the attachments that were built and the names of the messages
   * that did not fit, so the caller can open a composer AND tell the truth
   * about what is in it.
   */
  prepare: (emails: readonly Email[]) => Promise<{
    readonly attachments: readonly ComposerAttachment[];
    readonly refused: readonly string[];
  }>;
}

export function useForwardAsAttachment({
  client,
  accountId,
  authorization,
  uploadUrlTemplate,
  sessionCapabilities,
}: {
  readonly client: JmapClient | undefined;
  readonly accountId: string;
  readonly authorization: string;
  readonly uploadUrlTemplate: string | undefined;
  readonly sessionCapabilities: Readonly<Record<string, unknown>> | undefined;
}): ForwardAsAttachmentApi {
  const [isPreparing, setPreparing] = useState(false);

  const prepare = useCallback(
    async (
      emails: readonly Email[],
    ): Promise<{
      readonly attachments: readonly ComposerAttachment[];
      readonly refused: readonly string[];
    }> => {
      if (client === undefined || emails.length === 0) {
        return { attachments: [], refused: [] };
      }

      /*
       * Only messages the server gave us both a blob and a size for.
       *
       * `blobId` is absent on a row that came from a projection rather than a
       * fetch, and there is nothing to download for it. `size` is optional on
       * the wire, and a message whose size we do not know cannot be checked
       * against the cap — attaching it anyway would push the whole draft over
       * the limit and fail the send at the far end, which is a worse outcome
       * than refusing it here.
       *
       * Either way the message is NAMED in the refused list rather than
       * silently dropped: the user asked for it, and a forward that quietly
       * carries three of four messages is a forward that misleads.
       */
      const withBlobs = emails.filter(
        (email): email is Email & { blobId: string; size: number } =>
          email.blobId !== undefined && email.size !== undefined,
      );
      const missing = emails
        .filter((email) => email.blobId === undefined || email.size === undefined)
        .map((email) => emlFilename(email.subject));

      /*
       * The size gate BEFORE any download, using `email.size` — the size the
       * server already told us. Downloading 40 MB to discover it cannot be
       * uploaded is the exact waste the client-side gate exists to prevent, and
       * here it would be paid once per message.
       */
      const perFile = maxUploadSize(sessionCapabilities);
      const total = maxAttachmentsSize(sessionCapabilities);
      const { accepted, refused } = partitionBySize(withBlobs, {
        ...(perFile !== undefined ? { perFile } : {}),
        ...(total !== undefined ? { total } : {}),
      });

      if (accepted.length === 0) {
        return {
          attachments: [],
          refused: [...missing, ...refused.map((email) => emlFilename(email.subject))],
        };
      }

      setPreparing(true);
      const attachments: ComposerAttachment[] = [];
      const failed: string[] = [];

      try {
        for (const email of accepted) {
          const filename = emlFilename(email.subject);
          try {
            const blob = await client.downloadBlob(
              accountId,
              email.blobId,
              filename,
              RFC822_TYPE,
            );
            /*
             * Re-typed as `message/rfc822` on the way up. The download route
             * serves the bytes under whatever type it was asked for, and what
             * matters is the type the RECIPIENT's client sees: `message/rfc822`
             * is what makes an attachment openable as a message (RFC 2046
             * §5.2.1) rather than a file to save.
             */
            const file = new File([blob], filename, { type: RFC822_TYPE });
            const uploaded = await uploadBlob(
              uploadUrlFor(uploadUrlTemplate, accountId),
              authorization,
              file,
            );
            attachments.push({
              kind: "ready",
              key: `eml-${email.id}`,
              name: filename,
              size: uploaded.size,
              // The server echoes the type it stored; ours is what we sent.
              type: RFC822_TYPE,
              blobId: uploaded.blobId,
            });
          } catch {
            // One message failing must not lose the others: it joins the list
            // the caller reports, and the loop continues.
            failed.push(filename);
          }
        }
      } finally {
        setPreparing(false);
      }

      return {
        attachments,
        refused: [
          ...missing,
          ...refused.map((email) => emlFilename(email.subject)),
          ...failed,
        ],
      };
    },
    [client, accountId, authorization, uploadUrlTemplate, sessionCapabilities],
  );

  return { isPreparing, prepare };
}
