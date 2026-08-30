import { useEffect, useMemo, useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import {
  sanitizeEmailHtml,
  type SanitizedEmailHtml,
} from "../../mail/html/sanitize";
import { buildSrcDoc, MESSAGE_SANDBOX } from "../../mail/html/srcdoc";
import styles from "./SecureHtmlBody.module.css";

/**
 * The secure HTML body — the component that replaced `HtmlBodyPlaceholder`
 * (the W-A4 epic; see the policy in mail/html/policy.ts and the isolation in
 * mail/html/srcdoc.ts).
 *
 * Layer boundaries, stated once here because this component is where they
 * meet: the RAW html prop is untrusted and never touches the app's DOM; the
 * SANITIZED string exists only to be embedded in the srcdoc of an iframe
 * whose sandbox has neither allow-scripts nor allow-same-origin; and the
 * only network fetch the frame can express is our HMAC-signed image proxy,
 * after the user's explicit opt-in.
 *
 * # The image opt-in flow
 *
 * Sanitization always runs blocked-first: the first pass strips every remote
 * src and collects the canonical URLs. When the parent flips
 * `blockRemoteImages` to false, an effect asks the signer for proxy paths
 * and the SAME original html is re-sanitized with the mapping — output is
 * never re-fed to the sanitizer, and an image whose URL the server refused
 * to sign simply stays blocked. If signing fails, the failure is said out
 * loud and the images stay hidden; a silent broken-image grid would read as
 * our bug and teach users the banner does nothing.
 *
 * # Height, and the refusal behind it
 *
 * The frame fills the reading pane and the MESSAGE scrolls inside it.
 * Sizing the frame to its content is deliberately not implemented: every
 * mechanism that could measure a cross-origin sandboxed document requires
 * granting the content a capability the sandbox exists to refuse —
 * allow-scripts (a measuring script posting its height) or
 * allow-same-origin (the parent reading scrollHeight). W-A4 forbids both,
 * so the guarantee wins over the feature. The trade also means hostile
 * content cannot grow the frame to cover app UI, and scroll-jacking stays
 * inside a box the user can scroll past.
 */

/** Signs remote image URLs, returning canonical URL → signed proxy path.
 * Injected by the parent so this component needs no client/account. */
export type SignImageUrls = (
  urls: readonly string[],
) => Promise<ReadonlyMap<string, string>>;

export interface SecureHtmlBodyProps {
  /** The raw, UNTRUSTED bodyValue for the text/html part. */
  readonly html: string;
  /** Start with remote images blocked. */
  readonly blockRemoteImages: boolean;
  /** Called when the user explicitly opts in to loading them. */
  readonly onShowRemoteImages: () => void;
  /**
   * Whether the opt-in is OFFERED at all (E2, canon §4.1.9).
   *
   * `false` in the Junk mailbox: the banner still says the images are hidden —
   * silence there would look like a rendering bug — but the button that loads
   * them is not rendered. Disabling it instead would invite the click the
   * policy exists to prevent. Defaults to `true`.
   */
  readonly allowUnblock?: boolean;
  /** The image-proxy signer (mail/api.ts). */
  readonly signImageUrls: SignImageUrls;
  /** Rendered when sanitization fails or yields nothing displayable —
   * typically the message's plain-text alternative. */
  readonly fallback?: React.ReactNode;
}

type SignState = "idle" | "working" | "failed";

/** Runs the sanitizer under a guard: a throw means "no safe rendering
 * exists", which the UI treats as a first-class state, not a crash. */
function trySanitize(
  html: string,
  allowRemoteImages: boolean,
  proxied?: ReadonlyMap<string, string>,
): SanitizedEmailHtml | undefined {
  try {
    return sanitizeEmailHtml(
      html,
      proxied === undefined
        ? { allowRemoteImages }
        : { allowRemoteImages, proxiedUrlFor: (url) => proxied.get(url) },
    );
  } catch {
    return undefined;
  }
}

export function SecureHtmlBody({
  html,
  blockRemoteImages,
  onShowRemoteImages,
  signImageUrls,
  fallback,
  allowUnblock = true,
}: SecureHtmlBodyProps): React.JSX.Element {
  const { t, format } = useTranslation();

  const [proxied, setProxied] = useState<ReadonlyMap<string, string> | undefined>(
    undefined,
  );
  const [signState, setSignState] = useState<SignState>("idle");

  // The blocked-first pass. Its remoteImageUrls list is also the signing
  // request, so the set of URLs we might ever fetch is fixed by this pass —
  // nothing discovered later can widen it.
  const blockedPass = useMemo(() => trySanitize(html, false), [html]);

  const wantsImages = !blockRemoteImages;
  const remoteUrls = useMemo(
    () => blockedPass?.remoteImageUrls ?? [],
    [blockedPass],
  );

  useEffect(() => {
    if (!wantsImages || remoteUrls.length === 0) return;
    if (proxied !== undefined || signState !== "idle") return;
    let cancelled = false;
    setSignState("working");
    signImageUrls(remoteUrls).then(
      (map) => {
        if (cancelled) return;
        setProxied(map);
        setSignState("idle");
      },
      () => {
        if (!cancelled) setSignState("failed");
      },
    );
    return () => {
      cancelled = true;
    };
  }, [wantsImages, remoteUrls, proxied, signState, signImageUrls]);

  // The pass that actually renders: re-run from the ORIGINAL html with the
  // proxy mapping once it exists; identical to blockedPass until then.
  const rendered = useMemo(() => {
    if (wantsImages && proxied !== undefined) {
      return trySanitize(html, true, proxied);
    }
    return blockedPass;
  }, [html, wantsImages, proxied, blockedPass]);

  const srcDoc = useMemo(() => {
    if (rendered === undefined || rendered.html.trim() === "") return undefined;
    return buildSrcDoc(rendered.html, window.location.origin);
  }, [rendered]);

  // Sanitization failed, or stripped the message down to nothing: say so and
  // show the plain-text alternative if the caller has one. Never render an
  // empty frame — it looks like data loss and hides that a formatted version
  // exists.
  if (srcDoc === undefined) {
    return (
      <div className={styles.container}>
        <div className={styles.notice} role="note">
          <p className={styles.noticeTitle}>{t("reader.htmlSanitizeFailed")}</p>
          <p className={styles.noticeBody}>{t("reader.htmlSanitizeFailedBody")}</p>
        </div>
        {fallback}
      </div>
    );
  }

  const showBlockedBanner = blockRemoteImages && remoteUrls.length > 0;
  const inlineDropped = rendered?.droppedInlineImageCount ?? 0;

  return (
    <div className={styles.container}>
      {showBlockedBanner && (
        <div className={styles.imageBanner}>
          <span className={styles.imageBannerText}>
            {format("reader.imagesBlocked", remoteUrls.length)}
          </span>
          {/* Canon §4.1.9: in Spam the count is still stated — silence would
              look like a rendering bug — but there is NO control to load
              them. */}
          {allowUnblock && (
            <button
              type="button"
              className={styles.showImagesButton}
              onClick={onShowRemoteImages}
            >
              {t("reader.showImages")}
            </button>
          )}
        </div>
      )}

      {/* The signing status lives in a permanent live region so its
          transitions are announced; see the login screen's rationale. */}
      <span role="status" aria-live="polite" className={styles.signStatus}>
        {signState === "working" ? t("reader.imagesLoading") : ""}
        {signState === "failed" ? t("reader.imagesFailed") : ""}
      </span>

      {inlineDropped > 0 && (
        <p className={styles.inlineNote} role="note">
          {format("reader.inlineImagesUnavailable", inlineDropped)}
        </p>
      )}

      <iframe
        className={styles.frame}
        /*
         * THE sandbox. Absent grants are the security property, and the
         * attribute is pinned by a test: no scripts, no same-origin (the
         * pair that would void the sandbox), no top navigation, no forms,
         * no modals. Popups are allowed so a link can open its tab, and
         * they escape the sandbox because the opened page is a real site,
         * not mail content — rel="noopener noreferrer" (forced by the
         * sanitizer) keeps the new tab handle-free.
         */
        sandbox={MESSAGE_SANDBOX}
        srcDoc={srcDoc}
        title={t("reader.htmlFrameTitle")}
        /* Belt and braces with the sanitizer's rel=noreferrer: nothing about
         * the app's URL crosses into the frame's requests. */
        referrerPolicy="no-referrer"
      />
    </div>
  );
}
