import { useEffect, useMemo, useRef, useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import {
  DEFAULT_FRAME_WIDTH_PX,
  estimateFrameHeight,
  frameSizing,
} from "../../mail/html/frameHeight";
import {
  sanitizeEmailHtml,
  type SanitizedEmailHtml,
} from "../../mail/html/sanitize";
import { splitQuotedTail } from "../../mail/html/quotedTail";
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
 * # Height, and the price of the sandbox (C-03)
 *
 * The frame is sized to its content by an ESTIMATE computed in the parent
 * from the sanitized string (`mail/html/frameHeight.ts`, which also records
 * why every measuring alternative was rejected). No capability is granted for
 * it: every mechanism that could MEASURE a cross-origin sandboxed document —
 * allow-scripts for a script posting its height, allow-same-origin for the
 * parent reading scrollHeight — is one the sandbox exists to refuse, and
 * W-A4 forbids both. The estimate needs no channel because the parent already
 * holds the very string it hands to the srcdoc.
 *
 * The price is that it IS an estimate: an undershoot leaves the frame with its
 * own scrollbar (the state every message used to be in), an overshoot leaves
 * white space. It is clamped, so hostile content cannot grow the frame over
 * the app's chrome, and a very long message is clipped behind an explicit
 * "show the whole message" — Gmail's own shape. A test pins that the sandbox
 * attribute stayed byte-identical through all of this.
 *
 * # Quoted-text trimming (L3 epic E1, canon §2.1)
 *
 * The same refusal shapes the "Show trimmed content" toggle. The quote cannot
 * be collapsed by a script inside the frame (no allow-scripts) nor by the
 * parent reaching into it (no allow-same-origin), and it cannot be MARKED
 * before sanitization because `class` is dropped by the policy (policy.ts:
 * DOM-clobbering surface). So the tail is located in the SANITIZED STRING by
 * `mail/html/quotedTail.ts` — a pure substring split whose halves reassemble
 * byte-for-byte — and both halves go to `buildSrcDoc`, which emits the tail or
 * omits it. The toggle itself is a real button in the APP's chrome above the
 * frame; pressing it re-renders the srcdoc.
 *
 * No capability is added to the frame, the sanitizer's output is never
 * re-parsed, and a trimmed quote is genuinely ABSENT from the document rather
 * than hidden by a style rule that a select-all would happily copy anyway.
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
  /*
   * E1: whether the trimmed quote is expanded. Starts collapsed — that IS the
   * feature. The component is keyed by message id upstream, so this can never
   * carry over from one message to the next.
   */
  const [showQuoted, setShowQuoted] = useState(false);

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

  /*
   * E1: the quoted tail, split off the SANITIZED markup.
   *
   * Recomputed whenever the sanitized output changes (an image unblock
   * re-sanitizes from the original), because the offsets belong to that exact
   * string. `splitQuotedTail` is a substring operation, so this costs a scan
   * and allocates two slices — no parse, no tree, nothing to leak.
   */
  const split = useMemo(
    () => (rendered === undefined ? undefined : splitQuotedTail(rendered.html)),
    [rendered],
  );
  const hasQuotedTail = split !== undefined && split.quoted !== "";

  const srcDoc = useMemo(() => {
    if (rendered === undefined || split === undefined) return undefined;
    // Emptiness is judged on the VISIBLE half: a message whose entire body is
    // a quote would otherwise render an empty frame with a toggle under it.
    if (rendered.html.trim() === "") return undefined;
    return buildSrcDoc(split.visible, window.location.origin, {
      quotedHtml: split.quoted,
      showQuoted: showQuoted,
    });
  }, [rendered, split, showQuoted]);

  /*
   * C-03: the frame's height, estimated in the PARENT (see the header comment
   * and mail/html/frameHeight.ts).
   *
   * The width the estimate is computed against is the container's own, read
   * with a ResizeObserver on OUR element — nothing about this touches the
   * frame's document. Where the observer does not exist (jsdom, old
   * webviews) the default column width is assumed; the estimate degrades to
   * "a bit off", never to a broken pane.
   */
  const containerRef = useRef<HTMLDivElement | null>(null);
  const [frameWidth, setFrameWidth] = useState(DEFAULT_FRAME_WIDTH_PX);
  const [showWhole, setShowWhole] = useState(false);

  useEffect(() => {
    const element = containerRef.current;
    if (element === null || typeof ResizeObserver === "undefined") return undefined;
    const observer = new ResizeObserver((entries) => {
      const width = entries[0]?.contentRect.width;
      if (width !== undefined && width > 0) setFrameWidth(Math.round(width));
    });
    observer.observe(element);
    return () => {
      observer.disconnect();
    };
  }, []);

  const sizing = useMemo(() => {
    if (split === undefined) return undefined;
    // The SAME string the srcdoc carries: the visible half, plus the tail
    // only when it is shown — a hidden quote must not reserve its height.
    const inDocument = showQuoted ? split.visible + split.quoted : split.visible;
    return frameSizing(estimateFrameHeight(inDocument, frameWidth), showWhole);
  }, [split, showQuoted, frameWidth, showWhole]);

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
    <div className={styles.container} ref={containerRef}>
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
        /* C-03: the estimated height, as an inline style so the number is
         * observable (tests) and so a stylesheet cannot silently override
         * the clamp. Nothing else about the element changed. */
        style={sizing === undefined ? undefined : { height: `${sizing.heightPx}px` }}
      />

      {/*
        C-03: a very long message is clipped at the collapsed cap and offered
        whole. The control is the app's, outside the frame — the same
        mechanism as the trimmed-quote toggle: no capability crosses in, the
        parent just decides a different height. `aria-expanded` says which.
      */}
      {sizing?.isClipped === true && (
        <div className={styles.trimRow}>
          <button
            type="button"
            className={styles.trimToggle}
            onClick={() => {
              setShowWhole((current) => !current);
            }}
            aria-expanded={showWhole}
          >
            <span className={styles.trimLabel}>
              {showWhole ? t("reader.showLess") : t("reader.showWholeMessage")}
            </span>
          </button>
        </div>
      )}

      {/*
        E1 / canon §2.1: "Show trimmed content".

        The control is Gmail's "⋯" — a small, quiet affordance rather than a
        banner, because a quoted tail is the NORMAL state of a reply and a
        loud notice about it would shout on every message in a thread.

        It lives OUT here in the app's chrome rather than in the frame, which
        is the whole mechanism: the frame has no script to run a toggle with,
        so the button re-renders the srcdoc with the tail emitted or omitted.
        `aria-expanded` states which it is, so a screen-reader user knows
        there is more before deciding to press it.
      */}
      {hasQuotedTail && (
        <div className={styles.trimRow}>
          <button
            type="button"
            className={styles.trimToggle}
            onClick={() => {
              setShowQuoted((current) => !current);
            }}
            aria-expanded={showQuoted}
            title={showQuoted ? t("reader.hideTrimmed") : t("reader.showTrimmed")}
          >
            <span className={styles.trimDots} aria-hidden="true">
              •••
            </span>
            <span className={styles.trimLabel}>
              {showQuoted ? t("reader.hideTrimmed") : t("reader.showTrimmed")}
            </span>
          </button>
        </div>
      )}
    </div>
  );
}
