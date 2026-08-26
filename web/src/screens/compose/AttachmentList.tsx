import { useTranslation } from "../../i18n/I18nProvider";
import { formatBytes } from "../../mail/format";
import styles from "./AttachmentList.module.css";

/**
 * The composer's attachment strip: what is uploading, what is attached, and
 * what failed — with the reason.
 *
 * # Progress is real, not a spinner
 *
 * A spinner tells the user "something is happening"; a percentage tells them
 * whether to wait. For a 20 MB file on a slow uplink that is the difference
 * between a working app and a frozen one, which is why the upload path uses
 * XHR (see `uploadBlob` in `mail/write.ts`) rather than fetch — fetch reports
 * no upload progress anywhere.
 *
 * # A failed attachment stays visible
 *
 * The obvious implementation drops a file that fails to upload. Then the user
 * clicks Send believing it is attached. Instead a failed entry stays in the
 * list, marked, carrying the SERVER's own sentence — a 413 from the upload
 * endpoint arrives as "the uploaded file exceeds maxSizeUpload", which is
 * precise and actionable, and turning it into "upload failed" would be exactly
 * the defect the pilot taught us about.
 */

/** One entry in the strip, in any of its three states. */
export type ComposerAttachment =
  | {
      readonly kind: "uploading";
      readonly key: string;
      readonly name: string;
      readonly size: number;
      readonly type: string;
      /** 0-100, from the XHR's own progress events. */
      readonly percent: number;
    }
  | {
      readonly kind: "ready";
      readonly key: string;
      readonly name: string;
      readonly size: number;
      readonly type: string;
      readonly blobId: string;
    }
  | {
      readonly kind: "failed";
      readonly key: string;
      readonly name: string;
      readonly size: number;
      readonly type: string;
      /** The server's own words, never a generic sentence. */
      readonly message: string;
    };

export interface AttachmentListProps {
  readonly attachments: readonly ComposerAttachment[];
  readonly onRemove: (key: string) => void;
}

export function AttachmentList({
  attachments,
  onRemove,
}: AttachmentListProps): React.JSX.Element | null {
  const { format, locale } = useTranslation();
  if (attachments.length === 0) return null;

  const total = attachments.reduce((sum, item) => sum + item.size, 0);

  return (
    <section className={styles.strip} aria-label={format("compose.attachments", attachments.length)}>
      <p className={styles.title}>
        {format("compose.attachments", attachments.length)}
        <span className={styles.total}>{formatBytes(total, locale)}</span>
      </p>
      <ul className={styles.list}>
        {attachments.map((attachment) => (
          <li
            key={attachment.key}
            className={[
              styles.item,
              attachment.kind === "failed" ? styles.failed : "",
              attachment.kind === "uploading" ? styles.uploading : "",
            ]
              .filter(Boolean)
              .join(" ")}
          >
            <svg
              className={styles.icon}
              viewBox="0 0 20 20"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.5"
              aria-hidden="true"
              focusable="false"
            >
              <path d="M11.5 2.5H5.8a1 1 0 0 0-1 1v13a1 1 0 0 0 1 1h8.4a1 1 0 0 0 1-1V6.2z" />
              <path d="M11.5 2.5v3.7h3.7" />
            </svg>

            <span className={styles.details}>
              <span className={styles.name} title={attachment.name}>
                {attachment.name}
              </span>
              <span className={styles.meta}>
                {attachment.kind === "uploading"
                  ? format("compose.uploading", attachment.percent)
                  : formatBytes(attachment.size, locale)}
              </span>
              {attachment.kind === "failed" && (
                /* The server's exact sentence, in an alert so it is announced. */
                <span className={styles.reason} role="alert">
                  {attachment.message}
                </span>
              )}
            </span>

            {attachment.kind === "uploading" && (
              <span
                className={styles.progress}
                role="progressbar"
                aria-label={attachment.name}
                aria-valuenow={attachment.percent}
                aria-valuemin={0}
                aria-valuemax={100}
              >
                <span
                  className={styles.progressBar}
                  style={{ width: `${attachment.percent}%` }}
                />
              </span>
            )}

            <button
              type="button"
              className={styles.remove}
              onClick={() => {
                onRemove(attachment.key);
              }}
              aria-label={format("compose.removeAttachment", attachment.name)}
              title={format("compose.removeAttachment", attachment.name)}
            >
              <svg viewBox="0 0 16 16" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round">
                <path d="M4.5 4.5l7 7M11.5 4.5l-7 7" />
              </svg>
            </button>
          </li>
        ))}
      </ul>
    </section>
  );
}
