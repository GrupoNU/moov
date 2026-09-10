import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";

import { BrandMark } from "../../components/BrandMark";
import { useConfirm } from "../../components/useConfirm";
import { useTranslation } from "../../i18n/I18nProvider";
import { MOOV_DEFAULT_BRANDING, brandSeeds, type Branding } from "../../branding/branding";
import {
  THEME_SURFACES,
  derivePalette,
  deriveSplashColors,
  type ThemeName,
} from "../../branding/palette";
import {
  ACCEPTED_IMAGE_EXTENSIONS,
  ACCEPTED_IMAGE_TYPES,
  ASSET_KINDS,
  checkImageFile,
  type AssetKind,
  type BrandAdminDoc,
  type BrandPatch,
} from "../../branding/adminApi";
import type { PlainStringKey } from "./registry";
import { useSaveFeedback } from "./useSaveFeedback";
import styles from "./BrandSection.module.css";

/**
 * The brand administration panel (L2-brand-admin §5).
 *
 * # What makes this section different from every other one
 *
 * Everything else in Settings changes what ONE person sees. This changes what
 * everyone who signs in to this host sees — including the login screen, before
 * anybody has authenticated. So two properties are load-bearing here and
 * nowhere else on this page:
 *
 *   1. **Nothing changes until it is saved.** The colour preview is drawn in
 *      two scoped containers whose CSS variables are set on THE CONTAINER, not
 *      on `document.documentElement`. Writing the seeds to the root — which is
 *      what `applyBranding` does, correctly, for a brand that has been saved —
 *      would repaint the whole app while an administrator drags a colour picker
 *      through every hue between the old value and the new one. The test pins
 *      that the root is never touched, because this is the kind of thing a
 *      later refactor "simplifies" by reaching for the existing helper.
 *   2. **The adjustment is declared.** `derivePalette` will move a colour that
 *      cannot clear 4.5:1, and it already says so in the console. An
 *      administrator who typed a pale mint and got a deeper green needs that
 *      sentence beside the field they typed in, or the app looks like it
 *      ignored them.
 *
 * # Why text fields save on blur and images save on drop
 *
 * The same rule the rest of the page follows — the gesture IS the decision —
 * applied to two different kinds of gesture. Typing is not finished until the
 * field is left, so a name saves on blur or Enter with the row's "Guardado ✓".
 * Choosing a file IS finished the moment it is chosen, so an upload starts
 * immediately and reports its own progress. A Save button over both would have
 * been ceremony over the first and a lie about the second.
 *
 * # The section owns no transport
 *
 * Every write is a prop, exactly as the label manager and the four Sieve
 * sections do it, so this file renders and validates and never holds a
 * credential. The host wires it to {@link BrandAdminClient}.
 */

/** What the panel needs from its host, which owns the client. */
export interface BrandSectionProps {
  /** The document as last read or written. */
  readonly doc: BrandAdminDoc;
  /** Writes the named fields. Resolves true when the server accepted them. */
  readonly onSave: (patch: BrandPatch) => Promise<boolean>;
  /** Replaces one image. */
  readonly onUploadAsset: (kind: AssetKind, file: File) => Promise<boolean>;
  /** Removes one image. */
  readonly onRemoveAsset: (kind: AssetKind) => Promise<boolean>;
  /** Back to the stock brand. */
  readonly onReset: () => Promise<boolean>;
  /** The last failure, already turned into a sentence by the host. */
  readonly error?: string | undefined;
  /** Which field the server named in a 400, so its control can be marked. */
  readonly errorField?: string | undefined;
  /**
   * D-5's row filter, supplied by the PAGE rather than by the host that wires
   * the client.
   *
   * Optional so the host's prop object does not have to carry a concern that
   * belongs to the settings search; the page passes it after spreading, and a
   * caller rendering this section on its own gets every group.
   */
  readonly showRow?: ((id: string) => boolean) | undefined;
}

/** The longest a short name may be — the server's own ceiling. */
const SHORT_NAME_MAX = 12;
const NAME_MAX = 64;
const TAGLINE_MAX = 160;

export function BrandSection({
  doc,
  onSave,
  onUploadAsset,
  onRemoveAsset,
  onReset,
  error,
  errorField,
  showRow = () => true,
}: BrandSectionProps): React.JSX.Element {
  const { t } = useTranslation();
  const { confirm, dialog } = useConfirm();

  return (
    <div className={styles.wrap}>
      <p className={styles.explain}>{t("settings.brand.description")}</p>

      {error !== undefined && (
        <p className={styles.error} role="alert">
          {error}
        </p>
      )}

      {showRow("brandIdentity") && (
        <IdentityGroup doc={doc} onSave={onSave} errorField={errorField} />
      )}
      {showRow("brandLinks") && <LinksGroup doc={doc} onSave={onSave} errorField={errorField} />}
      {showRow("brandColors") && <ColorGroup doc={doc} onSave={onSave} errorField={errorField} />}
      {showRow("brandImages") && (
        <ImagesGroup
          doc={doc}
          onSave={onSave}
          onUploadAsset={onUploadAsset}
          onRemoveAsset={onRemoveAsset}
        />
      )}

      {showRow("brandReset") && (
        <div className={styles.group}>
          <h4 className={styles.groupTitle}>{t("brand.danger.heading")}</h4>
          <div className={styles.row}>
            <div className={styles.rowText}>
              <span className={styles.rowLabel}>{t("brand.reset.label")}</span>
              <span className={styles.rowDescription}>{t("brand.reset.description")}</span>
            </div>
            <div className={styles.rowControl}>
              <button
                type="button"
                className={styles.dangerButton}
                /*
                 * Destructive and irreversible from the UI's point of view —
                 * the images are gone from the server, not merely unlinked —
                 * so it asks, and the dialog says what is cleared rather than
                 * "are you sure".
                 */
                onClick={() => {
                  void (async () => {
                    const ok = await confirm({
                      message: t("brand.reset.confirm"),
                      title: t("brand.reset.label"),
                      confirmLabel: t("brand.reset.button"),
                      destructive: true,
                    });
                    if (ok) await onReset();
                  })();
                }}
              >
                {t("brand.reset.button")}
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Rendered unconditionally so `confirm()`'s promise can always settle. */}
      {dialog}
    </div>
  );
}

// ---------------------------------------------------------------------------
// identity
// ---------------------------------------------------------------------------

function IdentityGroup({
  doc,
  onSave,
  errorField,
}: {
  readonly doc: BrandAdminDoc;
  readonly onSave: (patch: BrandPatch) => Promise<boolean>;
  readonly errorField: string | undefined;
}): React.JSX.Element {
  const { t, format } = useTranslation();

  return (
    <div className={styles.group}>
      <h4 className={styles.groupTitle}>{t("brand.identity.heading")}</h4>

      <TextRow
        labelKey="brand.name.label"
        descriptionKey="brand.name.description"
        value={doc.name}
        maxLength={NAME_MAX}
        invalid={errorField === "name"}
        onCommit={(value) => onSave({ name: value })}
      />

      <TextRow
        labelKey="brand.shortName.label"
        descriptionKey="brand.shortName.description"
        value={doc.shortName}
        maxLength={SHORT_NAME_MAX}
        invalid={errorField === "shortName"}
        /*
         * A LIVE counter rather than a message after the fact. Twelve
         * characters is a hard server rule with a visible consequence (the
         * text under an installed icon), and it counts CODE POINTS rather than
         * UTF-16 units, so a name with an emoji is not counted as two.
         */
        counter={(draft) =>
          format("brand.shortName.counter", Array.from(draft).length, SHORT_NAME_MAX)
        }
        onCommit={(value) => onSave({ shortName: value })}
      />

      <TextRow
        labelKey="brand.tagline.label"
        descriptionKey="brand.tagline.description"
        value={doc.tagline}
        maxLength={TAGLINE_MAX}
        invalid={errorField === "tagline"}
        onCommit={(value) => onSave({ tagline: value })}
      />
    </div>
  );
}

// ---------------------------------------------------------------------------
// links
// ---------------------------------------------------------------------------

/**
 * The scheme allow-list, mirroring `isSafeLinkUrl` in branding.ts and the
 * server's own `safeSupportURL`.
 *
 * Validated HERE as well as there, because the point of inline validation is
 * that the administrator learns at the field rather than after a round trip —
 * and because a `javascript:` URL typed into "Support" would become an `href`
 * on the login screen if either side stopped checking.
 */
function isAcceptableLink(value: string): boolean {
  if (value === "") return true; // Empty clears the link; that is a valid state.
  const lower = value.toLowerCase();
  return (
    lower.startsWith("https://") || lower.startsWith("http://") || lower.startsWith("mailto:")
  );
}

function LinksGroup({
  doc,
  onSave,
  errorField,
}: {
  readonly doc: BrandAdminDoc;
  readonly onSave: (patch: BrandPatch) => Promise<boolean>;
  readonly errorField: string | undefined;
}): React.JSX.Element {
  const { t } = useTranslation();

  const rows = [
    {
      field: "supportUrl" as const,
      labelKey: "brand.supportUrl.label" as PlainStringKey,
      descriptionKey: "brand.supportUrl.description" as PlainStringKey,
      value: doc.supportUrl,
    },
    {
      field: "privacyUrl" as const,
      labelKey: "brand.privacyUrl.label" as PlainStringKey,
      descriptionKey: "brand.privacyUrl.description" as PlainStringKey,
      value: doc.privacyUrl,
    },
    {
      field: "termsUrl" as const,
      labelKey: "brand.termsUrl.label" as PlainStringKey,
      descriptionKey: "brand.termsUrl.description" as PlainStringKey,
      value: doc.termsUrl,
    },
  ];

  return (
    <div className={styles.group}>
      <h4 className={styles.groupTitle}>{t("brand.links.heading")}</h4>
      {rows.map((row) => (
        <TextRow
          key={row.field}
          labelKey={row.labelKey}
          descriptionKey={row.descriptionKey}
          value={row.value}
          type="url"
          invalid={errorField === row.field}
          validate={(value) => (isAcceptableLink(value) ? undefined : t("brand.url.invalid"))}
          onCommit={(value) => onSave({ [row.field]: value })}
        />
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// colour, and the live preview
// ---------------------------------------------------------------------------

const HEX_PATTERN = /^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/;

/** Expands `#abc` to `#aabbcc` so `<input type="color">` accepts it. */
function toColorInputValue(value: string, fallback: string): string {
  if (!HEX_PATTERN.test(value)) return fallback;
  if (value.length === 4) {
    const [, r, g, b] = value;
    return `#${r ?? ""}${r ?? ""}${g ?? ""}${g ?? ""}${b ?? ""}${b ?? ""}`.toLowerCase();
  }
  return value.toLowerCase();
}

function ColorGroup({
  doc,
  onSave,
  errorField,
}: {
  readonly doc: BrandAdminDoc;
  readonly onSave: (patch: BrandPatch) => Promise<boolean>;
  readonly errorField: string | undefined;
}): React.JSX.Element {
  const { t, format } = useTranslation();

  /*
   * The DRAFT colours: what the preview shows, which is not what the app is
   * wearing until a save lands. Seeded from the document and re-seeded when it
   * changes, so a successful save and a reset both settle the preview onto the
   * truth rather than leaving a stale draft on screen.
   */
  const [primary, setPrimary] = useState(doc.colors.primary);
  /*
   * The two gradient stops hold "" when the operator has not chosen them, NOT
   * the value the server resolved.
   *
   * `doc.colors` is the EFFECTIVE palette, so seeding these from it would put
   * the derived gradient into the fields as text the operator appears to have
   * typed — and they would then have to select and delete it to get back to
   * automatic, without any reason to believe that would work. `colorsConfigured`
   * is what tells the two apart; an unset field shows the derived value as a
   * placeholder instead, which says "this is what you will get, and it follows
   * your primary colour".
   */
  const [splashFrom, setSplashFrom] = useState(
    doc.colorsConfigured.has("splashFrom") ? doc.colors.splashFrom : "",
  );
  const [splashTo, setSplashTo] = useState(
    doc.colorsConfigured.has("splashTo") ? doc.colors.splashTo : "",
  );
  /*
   * `onPrimary` is a HINT, not a setting, so its control is opt-in. Left
   * automatic, `derivePalette` picks whichever of near-black and white clears
   * 4.5:1 on the final accent — which is right far more often than a person
   * guessing, and is the difference between a legible button and an invisible
   * label. The override exists because a brand guideline can be a real
   * constraint; it is not the default because it is the field most likely to
   * be set wrong.
   */
  const [onPrimaryOverride, setOnPrimaryOverride] = useState(doc.colors.onPrimary !== "");
  const [onPrimary, setOnPrimary] = useState(doc.colors.onPrimary);

  useEffect(() => {
    setPrimary(doc.colors.primary);
    setSplashFrom(doc.colorsConfigured.has("splashFrom") ? doc.colors.splashFrom : "");
    setSplashTo(doc.colorsConfigured.has("splashTo") ? doc.colors.splashTo : "");
    setOnPrimary(doc.colors.onPrimary);
    setOnPrimaryOverride(doc.colors.onPrimary !== "");
  }, [doc.colors, doc.colorsConfigured]);

  const effectivePrimary = HEX_PATTERN.test(primary)
    ? primary
    : MOOV_DEFAULT_BRANDING.colors.primary;
  const effectiveOnPrimary =
    onPrimaryOverride && HEX_PATTERN.test(onPrimary) ? onPrimary : undefined;

  const palette = useMemo(
    () => derivePalette(effectivePrimary, effectiveOnPrimary),
    [effectivePrimary, effectiveOnPrimary],
  );

  /*
   * The gradient the server WILL serve for the draft primary — the same
   * arithmetic Go's `branding.DeriveSplashColors` runs, mirrored so the panel
   * does not need a round trip per keystroke of the colour picker. It is both
   * the placeholder in an unset field and what the preview paints, so what an
   * administrator is promised and what they are shown cannot disagree.
   */
  const derivedSplash = useMemo(() => deriveSplashColors(effectivePrimary), [effectivePrimary]);

  const effectiveSplashFrom = HEX_PATTERN.test(splashFrom) ? splashFrom : derivedSplash.from;
  const effectiveSplashTo = HEX_PATTERN.test(splashTo) ? splashTo : derivedSplash.to;

  /*
   * The brand the PREVIEW wears: the draft colours over the saved everything
   * else, so an administrator picking a colour sees it under their own logo
   * and name rather than under a placeholder.
   */
  const previewBranding = useMemo<Branding>(
    () => ({
      ...MOOV_DEFAULT_BRANDING,
      name: doc.name === "" ? MOOV_DEFAULT_BRANDING.name : doc.name,
      logoUrl: doc.assets.logo?.url ?? "",
      logoDarkUrl: doc.assets.logoDark?.url ?? "",
      colors: {
        primary: effectivePrimary,
        onPrimary: palette.light.onAccent,
        splashFrom: effectiveSplashFrom,
        splashTo: effectiveSplashTo,
      },
      isDefault: false,
    }),
    [
      doc.name,
      doc.assets.logo,
      doc.assets.logoDark,
      effectivePrimary,
      palette,
      effectiveSplashFrom,
      effectiveSplashTo,
    ],
  );

  return (
    <div className={styles.group}>
      <h4 className={styles.groupTitle}>{t("brand.colors.heading")}</h4>

      <ColorRow
        labelKey="brand.primary.label"
        descriptionKey="brand.primary.description"
        value={primary}
        saved={doc.colors.primary}
        invalid={errorField === "primary"}
        onChangeValue={setPrimary}
        onCommit={(value) => onSave({ colors: { primary: value } })}
      />

      <div className={styles.row}>
        <div className={styles.rowText}>
          <span className={styles.rowLabel}>{t("brand.onPrimary.label")}</span>
          <span className={styles.rowDescription}>{t("brand.onPrimary.description")}</span>
        </div>
        <div className={styles.rowControl}>
          <div className={styles.onPrimaryChoice}>
            <label className={styles.radioLabel}>
              <input
                type="radio"
                name="brand-on-primary-mode"
                checked={!onPrimaryOverride}
                onChange={() => {
                  setOnPrimaryOverride(false);
                  // Clearing on the server is what "automatic" MEANS: the
                  // stored hint goes away so the derivation owns the value.
                  void onSave({ colors: { onPrimary: "" } });
                }}
              />
              {t("brand.onPrimary.auto")}
            </label>
            <label className={styles.radioLabel}>
              <input
                type="radio"
                name="brand-on-primary-mode"
                checked={onPrimaryOverride}
                onChange={() => {
                  setOnPrimaryOverride(true);
                  /*
                   * Seeded with what the derivation just chose, so switching to
                   * manual starts from the correct answer rather than from
                   * black — the administrator adjusts a good value instead of
                   * fixing a bad one.
                   */
                  if (!HEX_PATTERN.test(onPrimary)) setOnPrimary(palette.light.onAccent);
                }}
              />
              {t("brand.onPrimary.custom")}
            </label>
          </div>
          {onPrimaryOverride && (
            <ColorField
              label={t("brand.onPrimary.label")}
              value={onPrimary}
              saved={doc.colors.onPrimary}
              invalid={errorField === "onPrimary"}
              onChangeValue={setOnPrimary}
              onCommit={(value) => onSave({ colors: { onPrimary: value } })}
            />
          )}
        </div>
      </div>

      {/*
        The two gradient stops are AUTOMATIC until somebody types in them.
        `saved` is "" when the field is unconfigured, which is what makes
        clearing the field a real write ("" clears on the server) rather than a
        no-op against the derived value the document reports.
      */}
      <ColorRow
        labelKey="brand.splashFrom.label"
        descriptionKey="brand.splash.automatic"
        value={splashFrom}
        saved={doc.colorsConfigured.has("splashFrom") ? doc.colors.splashFrom : ""}
        placeholder={derivedSplash.from}
        wellFallback={effectiveSplashFrom}
        invalid={errorField === "splashFrom"}
        onChangeValue={setSplashFrom}
        onCommit={(value) => onSave({ colors: { splashFrom: value } })}
      />
      <ColorRow
        labelKey="brand.splashTo.label"
        value={splashTo}
        saved={doc.colorsConfigured.has("splashTo") ? doc.colors.splashTo : ""}
        placeholder={derivedSplash.to}
        wellFallback={effectiveSplashTo}
        invalid={errorField === "splashTo"}
        onChangeValue={setSplashTo}
        onCommit={(value) => onSave({ colors: { splashTo: value } })}
      />

      {/*
        THE EFFECTIVE ACCENTS, beside the colour that was chosen.

        The owner picked a pastel cyan with white text on it. Both were
        unusable as sent — white on #b8faff is 1.3:1 — so the palette darkened
        the accent and replaced the white, correctly. From the picker, though,
        that reads as the app ignoring them: they set a pale colour and got
        dark buttons.

        The sentence below already SAYS so. These swatches are what make it
        checkable: "darkened to #0e6b74" means nothing until the chosen colour
        and the two results are on screen together, in the order the eye
        compares them.
      */}
      <div className={styles.effective}>
        <span className={styles.effectiveHeading}>{t("brand.color.effective.heading")}</span>
        <ul className={styles.effectiveList}>
          <EffectiveSwatch label={t("brand.color.effective.chosen")} color={effectivePrimary} />
          <EffectiveSwatch
            label={t("brand.color.effective.light")}
            color={palette.light.accent}
            onColor={palette.light.onAccent}
          />
          <EffectiveSwatch
            label={t("brand.color.effective.dark")}
            color={palette.dark.accent}
            onColor={palette.dark.onAccent}
          />
          {/*
            The container is a SEPARATE answer to "what will my colour look
            like", and for a pastel brand it is the more surprising one: the
            two swatches above are deep because they have to carry text, while
            the chrome stays in the hue that was chosen. Showing only the ink
            made the derivation look like it had thrown the colour away.
          */}
          <EffectiveSwatch
            label={t("brand.color.effective.containerLight")}
            color={palette.light.accentContainer}
            onColor={palette.light.onAccentContainer}
          />
          <EffectiveSwatch
            label={t("brand.color.effective.containerDark")}
            color={palette.dark.accentContainer}
            onColor={palette.dark.onAccentContainer}
          />
        </ul>
        {/*
          There is deliberately NO "your colour was used as-is" branch here.

          The two themes pull in opposite directions — the accent must clear
          4.5:1 against white AND against #151824 — and an exhaustive sweep of
          the sRGB cube found ZERO colours that satisfy both untouched (Moov's
          own #5b5bd6 reads at 3.29:1 on the dark surfaces and is raised). A
          branch that can never render is a claim nobody can check, so what an
          administrator gets is the two swatches plus the sentence below, which
          always has something to say.
        */}
      </div>

      {/*
        The AA notice, inline. `palette.adjusted` carries a prose reason per
        theme; what an administrator needs is the RESULTING hex, which is the
        accent they will actually see, so the sentence names that.
      */}
      {palette.adjusted.light !== undefined && (
        <p className={styles.notice} role="status">
          {format("brand.color.adjustedLight", palette.light.accent)}
        </p>
      )}
      {palette.adjusted.dark !== undefined && (
        <p className={styles.notice} role="status">
          {format("brand.color.adjustedDark", palette.dark.accent)}
        </p>
      )}

      <div className={styles.preview}>
        <h5 className={styles.previewTitle}>{t("brand.preview.heading")}</h5>
        <p className={styles.explain}>{t("brand.preview.description")}</p>
        <div className={styles.previewPair}>
          <PreviewPane theme="light" branding={previewBranding} palette={palette} />
          <PreviewPane theme="dark" branding={previewBranding} palette={palette} />
        </div>
      </div>
    </div>
  );
}

/**
 * One accent, as a chip carrying its own hex.
 *
 * The hex is TEXT rather than a tooltip: the value is the thing an
 * administrator takes back to a brand guideline, and a colour they can only
 * point at is not one they can write down. `onColor` paints the chip's label
 * on the accent itself, so the pair is shown doing the job it will actually do
 * — which is how a replaced `onPrimary` becomes visible rather than merely
 * described.
 */
function EffectiveSwatch({
  label,
  color,
  onColor,
}: {
  readonly label: string;
  readonly color: string;
  readonly onColor?: string;
}): React.JSX.Element {
  return (
    <li className={styles.effectiveItem}>
      <span
        className={styles.effectiveChip}
        style={{ background: color, color: onColor ?? "transparent" }}
        /* Decorative: the hex beside it is the accessible content, and a
           colour swatch has no name a screen reader could usefully read. */
        aria-hidden="true"
      >
        {onColor === undefined ? "" : "Aa"}
      </span>
      <span className={styles.effectiveLabel}>{label}</span>
      <code className={styles.effectiveHex}>{color}</code>
    </li>
  );
}

/**
 * One themed preview: a mini top bar, a selected row, a button and a link.
 *
 * # The whole point is the SCOPE of the variables
 *
 * The seeds go on THIS element's `style`, which makes them cascade to its
 * subtree and nowhere else. `applyBranding` writes the same names onto
 * `document.documentElement`, which is right for a brand that has been saved
 * and would be wrong here: the page the administrator is standing on would
 * repaint through every colour they drag past.
 *
 * The per-theme semantic tokens are re-pointed on the same element, because
 * tokens.css maps `--color-accent` to `--brand-accent-light` in its light block
 * and to `--brand-accent-dark` in its dark ones — and this pane has to show the
 * OTHER theme from the one the page is in. Re-pointing them locally is what
 * makes a dark preview possible inside a light page without a second document.
 */
function PreviewPane({
  theme,
  branding,
  palette,
}: {
  readonly theme: ThemeName;
  readonly branding: Branding;
  readonly palette: ReturnType<typeof derivePalette>;
}): React.JSX.Element {
  const { t } = useTranslation();
  const surfaces = THEME_SURFACES[theme];
  const themed = palette[theme];

  const style = {
    ...brandSeeds(branding, palette),
    "--color-accent": themed.accent,
    "--color-accent-hover": themed.accentHover,
    "--color-accent-active": themed.accentActive,
    "--color-on-accent": themed.onAccent,
    "--color-accent-tint": themed.accentTint,
    "--color-accent-tint-strong": themed.accentTintStrong,
    // The tonal trio, for the same reason as the line above: this pane shows
    // the OTHER theme from the page's, and tokens.css routes these per theme.
    "--color-accent-container": themed.accentContainer,
    "--color-on-accent-container": themed.onAccentContainer,
    "--color-selected-row": themed.selectedRow,
    "--color-active-pill": themed.activePill,
    "--surface-default": surfaces.surfaceDefault,
    "--text-strong": surfaces.textStrong,
    "--surface-canvas": surfaces.surfaceCanvas,
    "--text-default": surfaces.textDefault,
    /*
     * `color-scheme` so the browser paints form controls and scrollbars of the
     * previewed theme rather than of the page's — otherwise a dark pane shows
     * a light scrollbar and stops looking like the thing it is previewing.
     */
    colorScheme: theme,
  } as React.CSSProperties;

  return (
    <figure className={styles.pane} style={style} data-theme-preview={theme}>
      <figcaption className={styles.paneCaption}>
        {t(theme === "light" ? "brand.preview.light" : "brand.preview.dark")}
      </figcaption>
      <div className={styles.paneBody}>
        <div className={styles.paneBar}>
          <BrandMark branding={branding} size="sm" />
        </div>
        {/*
          The three TONES, in the places the app wears them, so the trio is
          visible as a trio.

          One accent produces three related tones — a light container for the
          compose button, a slightly fainter pill for the active folder, the
          faintest tint for the selected row — and an administrator who only
          saw the button could not tell whether they were choosing one colour
          or three. Shown together, the family is the thing on screen.
        */}
        <div className={styles.paneTones}>
          <span className={styles.paneCompose}>{t("brand.preview.compose")}</span>
          <span className={styles.panePill}>{t("brand.preview.pill")}</span>
        </div>
        <div className={styles.paneRow}>
          <span className={styles.paneRowSubject}>{t("brand.preview.rowSubject")}</span>
          <span className={styles.paneRowSnippet}>{t("brand.preview.rowSnippet")}</span>
        </div>
        <div className={styles.paneActions}>
          {/*
            Not a real button: this pane is a PICTURE of the app, and a
            focusable control inside it would put four tab stops between the
            colour fields and the next setting, all of them doing nothing.
          */}
          <span className={styles.paneButton}>{t("brand.preview.button")}</span>
          <span className={styles.paneLink}>{t("brand.preview.link")}</span>
        </div>
      </div>
    </figure>
  );
}

// ---------------------------------------------------------------------------
// images
// ---------------------------------------------------------------------------

const ASSET_LABELS: Readonly<Record<AssetKind, PlainStringKey>> = {
  logo: "brand.logo.label",
  logoDark: "brand.logoDark.label",
  icon: "brand.icon.label",
  splash: "brand.splash.label",
};

const ASSET_DESCRIPTIONS: Readonly<Record<AssetKind, PlainStringKey>> = {
  logo: "brand.logo.description",
  logoDark: "brand.logoDark.description",
  icon: "brand.icon.description",
  splash: "brand.splash.description",
};

/** Bytes, in the units an administrator reads a file size in. */
function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${String(bytes)} B`;
  if (bytes < 1024 * 1024) return `${String(Math.round(bytes / 1024))} kB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

/**
 * The "draw the name and tagline over the image" switch.
 *
 * # Why an operator needs it at all
 *
 * A real splash image often already carries the brand. The pilot owner's
 * artwork has his logo AND his slogan painted into it, so the panel drew a
 * second copy on top of the first — and that is not a contrast problem, so no
 * amount of choosing ink for the text fixes it. The text should not be there.
 *
 * # Why it saves immediately
 *
 * The same rule the rest of the page follows: the gesture IS the decision. A
 * checkbox has no "finished typing" moment to wait for, so it writes on change
 * exactly as the radio buttons above it do.
 */
function SplashTextRow({
  doc,
  onSave,
}: {
  readonly doc: BrandAdminDoc;
  readonly onSave: (patch: BrandPatch) => Promise<boolean>;
}): React.JSX.Element {
  const { t } = useTranslation();
  const { isSaved, report } = useSaveFeedback();
  const id = useId();

  return (
    <div className={styles.row}>
      <div className={styles.rowText}>
        <label className={styles.rowLabel} htmlFor={id}>
          {t("brand.splashText.label")}
        </label>
        <span className={styles.rowDescription}>{t("brand.splashText.description")}</span>
      </div>
      <div className={styles.rowControl}>
        <input
          id={id}
          type="checkbox"
          checked={doc.splashText}
          onChange={(event) => {
            report(onSave({ splashText: event.target.checked }));
          }}
        />
        {isSaved && (
          <span className={styles.saved} role="status">
            {t("settings.saved")}
          </span>
        )}
      </div>
    </div>
  );
}

function ImagesGroup({
  doc,
  onSave,
  onUploadAsset,
  onRemoveAsset,
}: {
  readonly doc: BrandAdminDoc;
  readonly onSave: (patch: BrandPatch) => Promise<boolean>;
  readonly onUploadAsset: (kind: AssetKind, file: File) => Promise<boolean>;
  readonly onRemoveAsset: (kind: AssetKind) => Promise<boolean>;
}): React.JSX.Element {
  const { t } = useTranslation();

  return (
    <div className={styles.group}>
      <h4 className={styles.groupTitle}>{t("brand.images.heading")}</h4>
      <p className={styles.explain}>{t("brand.images.description")}</p>

      {ASSET_KINDS.map((kind) => (
        <AssetRow
          key={kind}
          kind={kind}
          asset={doc.assets[kind]}
          onUpload={onUploadAsset}
          onRemove={onRemoveAsset}
        />
      ))}

      {/*
        The switch belongs HERE, under the splash upload, because it is about
        that image: an operator turns it off while looking at artwork that
        already carries their logo and slogan.
      */}
      <SplashTextRow doc={doc} onSave={onSave} />

      {/*
        The generated icons, from the URLs the DOCUMENT gives — never rebuilt
        from a base path here. The server's `?v=` cache-buster is the only
        thing that makes a re-uploaded icon visible without a hard reload, and
        reconstructing the URL would drop it.
      */}
      {Object.keys(doc.iconUrls).length > 0 && (
        <div className={styles.icons}>
          <h5 className={styles.groupSubtitle}>{t("brand.icons.heading")}</h5>
          {doc.iconSource === "logo" && (
            <p className={styles.explain}>{t("brand.icons.fromLogo")}</p>
          )}
          {doc.iconSource === "default" && (
            <p className={styles.explain}>{t("brand.icons.fromDefault")}</p>
          )}
          {doc.iconIssue !== "" && <p className={styles.notice}>{doc.iconIssue}</p>}
          <ul className={styles.iconList}>
            {["icon-192", "icon-maskable-192"].map((name) => {
              const url = doc.iconUrls[name];
              if (url === undefined) return null;
              return (
                <li key={name} className={styles.iconItem}>
                  <img className={styles.iconImage} src={url} alt={name} width={64} height={64} />
                  <span className={styles.iconName}>{name}</span>
                </li>
              );
            })}
          </ul>
        </div>
      )}

      {doc.warnings.length > 0 && (
        <div className={styles.warnings}>
          <h5 className={styles.groupSubtitle}>{t("brand.warnings.heading")}</h5>
          <ul className={styles.warningList}>
            {doc.warnings.map((warning) => (
              <li key={warning}>{warning}</li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

/**
 * One upload slot: the current image, a drop zone, a picker and a remove.
 *
 * The logo variants are shown over BOTH a light and a dark chip, because that
 * is the failure this slot exists to prevent — a black wordmark that looks
 * perfect in the settings page and is invisible on the login panel. Showing it
 * on both grounds turns "you need a dark variant" from a sentence in the help
 * text into something the administrator can see.
 */
function AssetRow({
  kind,
  asset,
  onUpload,
  onRemove,
}: {
  readonly kind: AssetKind;
  readonly asset: BrandAdminDoc["assets"][AssetKind];
  readonly onUpload: (kind: AssetKind, file: File) => Promise<boolean>;
  readonly onRemove: (kind: AssetKind) => Promise<boolean>;
}): React.JSX.Element {
  const { t, format } = useTranslation();
  const inputRef = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | undefined>(undefined);
  const [dragging, setDragging] = useState(false);
  const hintId = useId();
  const label = t(ASSET_LABELS[kind]);

  const accept = async (file: File | undefined): Promise<void> => {
    if (file === undefined) return;
    const refusal = checkImageFile(file);
    if (refusal !== undefined) {
      setProblem(refusal === "tooLarge" ? t("brand.image.tooLarge") : t("brand.image.unsupported"));
      return;
    }
    setProblem(undefined);
    setBusy(true);
    try {
      await onUpload(kind, file);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className={styles.row}>
      <div className={styles.rowText}>
        <span className={styles.rowLabel}>{label}</span>
        <span className={styles.rowDescription} id={hintId}>
          {t(ASSET_DESCRIPTIONS[kind])}
        </span>
      </div>
      <div className={styles.rowControl}>
        <div className={styles.assetControl}>
          <AssetPreview kind={kind} asset={asset} />

          <div
            className={[styles.dropZone, dragging ? styles.dropZoneActive : ""]
              .filter(Boolean)
              .join(" ")}
            onDragOver={(event) => {
              event.preventDefault();
              setDragging(true);
            }}
            onDragLeave={() => {
              setDragging(false);
            }}
            onDrop={(event) => {
              event.preventDefault();
              setDragging(false);
              void accept(event.dataTransfer.files[0]);
            }}
          >
            <span className={styles.dropText}>
              {t("brand.image.drop")}{" "}
              <button
                type="button"
                className={styles.linkButton}
                aria-label={format("brand.image.chooseFor", label)}
                aria-describedby={hintId}
                disabled={busy}
                onClick={() => {
                  inputRef.current?.click();
                }}
              >
                {t("brand.image.choose")}
              </button>
            </span>
            <input
              ref={inputRef}
              type="file"
              data-testid={`brand-file-${kind}`}
              className="visually-hidden"
              /*
               * Both the MIME types and the extensions. Platforms disagree
               * about which one they honour — a file whose type the OS does not
               * know is filtered out by a type-only accept, and a type-only
               * accept is ignored outright by some Android pickers — so listing
               * both is the only way the dialog offers the same set everywhere.
               * The DROP path has no accept at all, which is why `checkImageFile`
               * is the real gate and this is only a convenience.
               */
              accept={[...ACCEPTED_IMAGE_TYPES, ...ACCEPTED_IMAGE_EXTENSIONS].join(",")}
              tabIndex={-1}
              aria-hidden="true"
              onChange={(event) => {
                const file = event.target.files?.[0];
                // Cleared so choosing the SAME file twice fires `change` again
                // — the retry after a server-side refusal.
                event.target.value = "";
                void accept(file);
              }}
            />
          </div>

          <div className={styles.assetMeta}>
            {busy && (
              <span className={styles.busy} role="status">
                {t("brand.image.uploading")}
              </span>
            )}
            {asset !== null ? (
              <>
                <span className={styles.assetSize}>
                  {format(
                    "brand.image.dimensions",
                    asset.width,
                    asset.height,
                    formatBytes(asset.bytes),
                  )}
                </span>
                <button
                  type="button"
                  className={styles.linkButton}
                  aria-label={format("brand.image.removeFor", label)}
                  disabled={busy}
                  onClick={() => {
                    void onRemove(kind);
                  }}
                >
                  {t("brand.image.remove")}
                </button>
              </>
            ) : (
              <span className={styles.assetSize}>{t("brand.image.none")}</span>
            )}
          </div>

          {problem !== undefined && (
            <p className={styles.fieldError} role="alert">
              {problem}
            </p>
          )}
        </div>
      </div>
    </div>
  );
}

function AssetPreview({
  kind,
  asset,
}: {
  readonly kind: AssetKind;
  readonly asset: BrandAdminDoc["assets"][AssetKind];
}): React.JSX.Element | null {
  const { t } = useTranslation();
  if (asset === null) return null;

  // A logo is judged against the grounds it will sit on; an icon and a splash
  // are judged on their own.
  const onBothGrounds = kind === "logo" || kind === "logoDark";
  if (!onBothGrounds) {
    return (
      <div className={styles.chipRow}>
        <div className={styles.chipLight}>
          <img className={styles.assetImage} src={asset.url} alt="" />
        </div>
      </div>
    );
  }

  return (
    <div className={styles.chipRow}>
      <div className={styles.chipLight}>
        <img className={styles.assetImage} src={asset.url} alt="" />
        <span className={styles.chipCaption}>{t("brand.image.onLight")}</span>
      </div>
      <div className={styles.chipDark}>
        <img className={styles.assetImage} src={asset.url} alt="" />
        <span className={styles.chipCaption}>{t("brand.image.onDark")}</span>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// the shared row controls
// ---------------------------------------------------------------------------

/**
 * A text row that saves on blur or Enter, with this page's own "Guardado ✓".
 *
 * # Why the row owns the DRAFT rather than reading the document
 *
 * The value on screen and the value on the server are two different things
 * between a keystroke and a blur, and conflating them breaks in both
 * directions. Binding the input straight to the document fights the typist
 * every time a save resolves mid-keystroke; leaving it uncontrolled makes a
 * live counter impossible, because nothing above the DOM knows what was typed.
 *
 * So the row holds the draft, re-seeds it when the SAVED value changes (a
 * successful save, a reset, a document reloaded from elsewhere), and hands it
 * to an optional `counter` render prop. One state, two readers, no drift.
 */
function TextRow({
  labelKey,
  descriptionKey,
  value,
  type = "text",
  maxLength,
  counter,
  invalid,
  validate,
  onCommit,
}: {
  readonly labelKey: PlainStringKey;
  readonly descriptionKey?: PlainStringKey;
  readonly value: string;
  readonly type?: "text" | "url";
  readonly maxLength?: number;
  /** Renders a live hint from the DRAFT — the short name's "n de 12". */
  readonly counter?: (draft: string) => string;
  readonly invalid?: boolean;
  readonly validate?: (value: string) => string | undefined;
  readonly onCommit: (value: string) => Promise<boolean>;
}): React.JSX.Element {
  const { t } = useTranslation();
  const { isSaved, report } = useSaveFeedback();
  const [draft, setDraft] = useState(value);
  const [problem, setProblem] = useState<string | undefined>(undefined);
  const inputId = useId();
  const hintId = useId();
  const errorId = useId();

  useEffect(() => {
    setDraft(value);
  }, [value]);

  const commit = useCallback(
    (next: string): void => {
      if (next === value) return; // Nothing changed: no request, no receipt.
      const refusal = validate?.(next);
      if (refusal !== undefined) {
        setProblem(refusal);
        return;
      }
      setProblem(undefined);
      report(onCommit(next));
    },
    [onCommit, report, validate, value],
  );

  const describedBy = [descriptionKey !== undefined ? hintId : "", problem !== undefined ? errorId : ""]
    .filter(Boolean)
    .join(" ");

  return (
    <div className={styles.row}>
      <div className={styles.rowText}>
        <label className={styles.rowLabel} htmlFor={inputId}>
          {t(labelKey)}
        </label>
        {descriptionKey !== undefined && (
          <span className={styles.rowDescription} id={hintId}>
            {t(descriptionKey)}
          </span>
        )}
      </div>
      <div className={styles.rowControl}>
        <input
          id={inputId}
          className={[styles.input, invalid === true || problem !== undefined ? styles.inputInvalid : ""]
            .filter(Boolean)
            .join(" ")}
          type={type}
          value={draft}
          {...(maxLength !== undefined ? { maxLength } : {})}
          {...(describedBy !== "" ? { "aria-describedby": describedBy } : {})}
          aria-invalid={invalid === true || problem !== undefined}
          onChange={(event) => {
            setDraft(event.target.value);
            if (problem !== undefined) setProblem(undefined);
          }}
          onBlur={(event) => {
            commit(event.target.value);
          }}
          onKeyDown={(event) => {
            if (event.key !== "Enter") return;
            event.preventDefault();
            commit(event.currentTarget.value);
          }}
        />
        {counter !== undefined && <span className={styles.counter}>{counter(draft)}</span>}
        {problem !== undefined && (
          <p className={styles.fieldError} id={errorId} role="alert">
            {problem}
          </p>
        )}
        {isSaved && (
          <span className={styles.saved} role="status">
            {t("settings.saved")}
          </span>
        )}
      </div>
    </div>
  );
}

/** A colour row: the row chrome around a {@link ColorField}. */
function ColorRow({
  labelKey,
  descriptionKey,
  value,
  saved,
  placeholder,
  wellFallback,
  invalid,
  onChangeValue,
  onCommit,
}: {
  readonly labelKey: PlainStringKey;
  readonly descriptionKey?: PlainStringKey;
  readonly value: string;
  readonly saved: string;
  readonly placeholder?: string;
  readonly wellFallback?: string;
  readonly invalid?: boolean;
  readonly onChangeValue: (value: string) => void;
  readonly onCommit: (value: string) => Promise<boolean>;
}): React.JSX.Element {
  const { t } = useTranslation();
  return (
    <div className={styles.row}>
      <div className={styles.rowText}>
        <span className={styles.rowLabel}>{t(labelKey)}</span>
        {descriptionKey !== undefined && (
          <span className={styles.rowDescription}>{t(descriptionKey)}</span>
        )}
      </div>
      <div className={styles.rowControl}>
        <ColorField
          label={t(labelKey)}
          value={value}
          saved={saved}
          {...(placeholder !== undefined ? { placeholder } : {})}
          {...(wellFallback !== undefined ? { wellFallback } : {})}
          {...(invalid !== undefined ? { invalid } : {})}
          onChangeValue={onChangeValue}
          onCommit={onCommit}
        />
      </div>
    </div>
  );
}

/**
 * A colour: a swatch picker AND a hex field, and the hex field is the one that
 * matters.
 *
 * `<input type="color">` alone would be unusable for this job. A brand colour
 * arrives as a hex string in a brand guideline, and there is no way to TYPE one
 * into a native colour well — the administrator would have to find `#5b5bd6`
 * by eye in a gradient. It is also the control screen readers and keyboard
 * users handle worst. So the text field is the primary control, the well is the
 * convenience beside it, and both write the same value.
 */
function ColorField({
  label,
  value,
  saved,
  placeholder,
  wellFallback,
  invalid,
  onChangeValue,
  onCommit,
}: {
  readonly label: string;
  /** The DRAFT — what is on screen and what the preview is painted from. */
  readonly value: string;
  /**
   * The SAVED value, so "nothing changed, do not write" can be decided.
   *
   * The draft is lifted (the preview above reads it), which means this field
   * cannot tell "unchanged" from "typed back to the same thing" on its own —
   * `value` has already moved. Passing the server's copy separately is the
   * only honest way to answer the question, and getting it wrong means a PUT
   * on every blur of an untouched field.
   */
  readonly saved: string;
  /**
   * What an EMPTY field will get, shown as the input's placeholder.
   *
   * Its presence is also what makes the field CLEARABLE: a field with a
   * placeholder has a meaningful empty state ("automatic"), so "" commits as a
   * clear. A field without one has no such state, and an empty value there is
   * a half-typed hex that must not be written.
   */
  readonly placeholder?: string;
  /** What the colour WELL shows while the text field is empty. */
  readonly wellFallback?: string;
  readonly invalid?: boolean;
  readonly onChangeValue: (value: string) => void;
  readonly onCommit: (value: string) => Promise<boolean>;
}): React.JSX.Element {
  const { t, format } = useTranslation();
  const { isSaved, report } = useSaveFeedback();
  const [problem, setProblem] = useState<string | undefined>(undefined);
  const wellId = useId();
  const hexId = useId();
  const errorId = useId();

  const commit = (next: string): void => {
    const normalized = next.trim().toLowerCase();
    if (normalized === saved) return;
    /*
     * Emptying a field that HAS a placeholder is a real instruction — "go back
     * to automatic" — and "" is exactly what the API's clear looks like. In a
     * field with no automatic state the same keystroke is a half-typed hex, so
     * it is refused as one.
     */
    if (normalized === "" && placeholder !== undefined) {
      setProblem(undefined);
      report(onCommit(""));
      return;
    }
    if (!HEX_PATTERN.test(normalized)) {
      setProblem(t("brand.color.invalid"));
      return;
    }
    setProblem(undefined);
    report(onCommit(normalized));
  };

  return (
    <div className={styles.colorField}>
      <input
        id={wellId}
        className={styles.colorWell}
        type="color"
        /* The well cannot show "no colour", so it falls back to the stock
           primary while the hex field is empty or mid-edit. */
        value={toColorInputValue(
          value,
          wellFallback ?? MOOV_DEFAULT_BRANDING.colors.primary,
        )}
        aria-label={label}
        onChange={(event) => {
          onChangeValue(event.target.value);
        }}
        /* The well fires `change` on every drag step, so the SAVE waits for
           the interaction to end — otherwise dragging through a gradient
           would be one PUT per pixel.
         *
         * It commits only what the DRAFT holds, never the well's own displayed
         * value. The two differ exactly when the field is empty and the well is
         * showing the automatic colour: committing that would turn "follow the
         * primary" into a pinned copy of today's derived value, because the
         * administrator tabbed through a control without touching it. */
        onBlur={() => {
          commit(value);
        }}
      />
      <input
        id={hexId}
        className={[styles.hexInput, invalid === true || problem !== undefined ? styles.inputInvalid : ""]
          .filter(Boolean)
          .join(" ")}
        type="text"
        inputMode="text"
        spellCheck={false}
        value={value}
        placeholder={placeholder ?? "#5b5bd6"}
        aria-label={format("brand.color.hexLabel", label)}
        aria-invalid={invalid === true || problem !== undefined}
        {...(problem !== undefined ? { "aria-describedby": errorId } : {})}
        onChange={(event) => {
          onChangeValue(event.target.value);
          if (problem !== undefined) setProblem(undefined);
        }}
        onBlur={(event) => {
          commit(event.target.value);
        }}
        onKeyDown={(event) => {
          if (event.key !== "Enter") return;
          event.preventDefault();
          commit(event.currentTarget.value);
        }}
      />
      {problem !== undefined && (
        <p className={styles.fieldError} id={errorId} role="alert">
          {problem}
        </p>
      )}
      {isSaved && (
        <span className={styles.saved} role="status">
          {t("settings.saved")}
        </span>
      )}
    </div>
  );
}

