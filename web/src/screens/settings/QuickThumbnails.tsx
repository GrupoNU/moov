/**
 * The line-art previews inside the quick-settings panel (E12/B2, canon 07 §4).
 *
 * # Why these are drawn rather than described
 *
 * Gmail's quick panel puts a small picture of the RESULT beside every radio:
 * three list densities as three stacks of lines at three spacings, the reading
 * pane's three positions as three arrangements of two rectangles. That is not
 * decoration. "Compacta" and "Cómoda" are words whose meaning is exactly the
 * thing they are describing, and a user choosing between them from the labels
 * alone is guessing; the thumbnail answers the question before the click does.
 * The same is true of "A la derecha de la bandeja" versus "Debajo" — a picture
 * of the split is understood in a glance that a sentence is not.
 *
 * # Why inline SVG and not images
 *
 * They must recolour with the theme, and a PNG cannot: a light-mode thumbnail
 * on a dark surface is a white card in the middle of the panel. Everything here
 * strokes and fills with `currentColor` at reduced opacity, so the whole set
 * follows the palette in both themes and in forced-colors mode with no second
 * asset and no extra request.
 *
 * # Why every one of them is `aria-hidden`
 *
 * Each thumbnail sits beside a radio that already carries the option's name.
 * Announcing the picture too would read the label twice, and a "graphic"
 * announced between a radio and its text is exactly the noise that makes people
 * turn a screen reader's verbosity down. The picture is for the eye; the label
 * is the accessible name, and it is complete on its own.
 */

import styles from "./QuickSettingsPanel.module.css";

/** The frame every thumbnail shares: same box, same rounded card, same rule. */
function Frame({ children }: { readonly children: React.ReactNode }): React.JSX.Element {
  return (
    <svg
      className={styles.thumb}
      viewBox="0 0 48 32"
      aria-hidden="true"
      focusable="false"
    >
      <rect
        x="0.6"
        y="0.6"
        width="46.8"
        height="30.8"
        rx="3"
        fill="none"
        stroke="currentColor"
        strokeOpacity="0.35"
      />
      {children}
    </svg>
  );
}

/** One list row: a dot for the avatar and a bar for the text. */
function Row({ y, width = 26 }: { readonly y: number; readonly width?: number }): React.JSX.Element {
  return (
    <>
      <circle cx="9" cy={y} r="2.2" fill="currentColor" fillOpacity="0.35" />
      <rect
        x="14"
        y={y - 1.4}
        width={width}
        height="2.8"
        rx="1.4"
        fill="currentColor"
        fillOpacity="0.25"
      />
    </>
  );
}

/**
 * The three densities, as three row spacings.
 *
 * The row COUNT is the signal, not the gap: "compact" fits five rows in the
 * frame where "comfortable" fits three, which is precisely what the setting
 * buys and what a user is choosing between.
 */
export function DensityThumb({
  density,
}: {
  readonly density: "default" | "comfortable" | "compact";
}): React.JSX.Element {
  const spec = {
    comfortable: [8, 16, 24],
    default: [7, 14, 21, 28],
    compact: [5.5, 11, 16.5, 22, 27.5],
  }[density];
  return (
    <Frame>
      {spec.map((y) => (
        <Row key={y} y={y} />
      ))}
    </Frame>
  );
}

/**
 * The three themes.
 *
 * These are the ONE set here that does not use `currentColor` for its fill, and
 * deliberately: a theme swatch has to show the theme's own colours, or "oscuro"
 * would render as a light card in light mode and mean nothing. "Sistema" is
 * drawn as the two halves, which is the honest picture of "whichever your OS
 * says" — and the only one of the three that is not simply a colour.
 */
export function ThemeThumb({
  theme,
}: {
  readonly theme: "light" | "dark" | "system";
}): React.JSX.Element {
  return (
    <svg
      className={styles.thumb}
      viewBox="0 0 48 32"
      aria-hidden="true"
      focusable="false"
    >
      {theme === "system" ? (
        <>
          <path d="M3.6 0.6h20.4v30.8H3.6a3 3 0 0 1-3-3V3.6a3 3 0 0 1 3-3z" fill="#ffffff" />
          <path d="M24 0.6h20.4a3 3 0 0 1 3 3v24.8a3 3 0 0 1-3 3H24z" fill="#1f2124" />
          <rect x="7" y="7" width="12" height="2.6" rx="1.3" fill="#5f6368" />
          <rect x="7" y="13" width="9" height="2.6" rx="1.3" fill="#9aa0a6" />
          <rect x="29" y="7" width="12" height="2.6" rx="1.3" fill="#c9ccd1" />
          <rect x="29" y="13" width="9" height="2.6" rx="1.3" fill="#7d8288" />
        </>
      ) : (
        <>
          <rect
            x="0.6"
            y="0.6"
            width="46.8"
            height="30.8"
            rx="3"
            fill={theme === "dark" ? "#1f2124" : "#ffffff"}
          />
          <rect
            x="7"
            y="7"
            width="20"
            height="2.6"
            rx="1.3"
            fill={theme === "dark" ? "#c9ccd1" : "#5f6368"}
          />
          <rect
            x="7"
            y="13"
            width="14"
            height="2.6"
            rx="1.3"
            fill={theme === "dark" ? "#7d8288" : "#9aa0a6"}
          />
          <rect
            x="7"
            y="19"
            width="24"
            height="2.6"
            rx="1.3"
            fill={theme === "dark" ? "#7d8288" : "#9aa0a6"}
          />
        </>
      )}
      <rect
        x="0.6"
        y="0.6"
        width="46.8"
        height="30.8"
        rx="3"
        fill="none"
        stroke="currentColor"
        strokeOpacity="0.35"
      />
    </svg>
  );
}

/**
 * The three inbox types, as what each one puts at the TOP of the list.
 *
 * "No leídos primero" draws its first rows heavier; "Destacados primero" puts a
 * star on them. That is the whole difference the setting makes, so it is the
 * whole difference the picture shows.
 */
export function InboxTypeThumb({
  inboxType,
}: {
  readonly inboxType: "default" | "unread_first" | "starred_first";
}): React.JSX.Element {
  const rows = [7, 14, 21, 28];
  return (
    <Frame>
      {rows.map((y, index) => {
        const emphasised = index < 2 && inboxType !== "default";
        return (
          <g key={y}>
            {inboxType === "starred_first" && emphasised ? (
              // A star where the avatar dot would be — the flag the sort keys on.
              <path
                d="M9 3.2l.8 1.7 1.9.3-1.4 1.3.3 1.9L9 7.5l-1.6.9.3-1.9L6.3 5.2l1.9-.3z"
                transform={`translate(0 ${String(y - 5.5)})`}
                fill="currentColor"
                fillOpacity="0.55"
              />
            ) : (
              <circle
                cx="9"
                cy={y}
                r="2.2"
                fill="currentColor"
                fillOpacity={emphasised ? 0.55 : 0.35}
              />
            )}
            <rect
              x="14"
              y={y - 1.6}
              width={emphasised ? 30 : 26}
              height={emphasised ? 3.2 : 2.8}
              rx="1.6"
              fill="currentColor"
              fillOpacity={emphasised ? 0.5 : 0.25}
            />
          </g>
        );
      })}
    </Frame>
  );
}

/**
 * The three reading-pane positions, as the arrangement of list and reader.
 *
 * The list is drawn as rows and the reader as a filled block, so the picture
 * says which pane is which — two empty rectangles side by side would show the
 * SPLIT without showing what goes in it.
 */
export function ReadingPaneThumb({
  pane,
}: {
  readonly pane: "none" | "right" | "bottom";
}): React.JSX.Element {
  if (pane === "none") {
    return (
      <Frame>
        {[7, 14, 21, 28].map((y) => (
          <Row key={y} y={y} />
        ))}
      </Frame>
    );
  }
  if (pane === "right") {
    return (
      <Frame>
        {[7, 14, 21, 28].map((y) => (
          <Row key={y} y={y} width={6} />
        ))}
        <line x1="24" y1="1" x2="24" y2="31" stroke="currentColor" strokeOpacity="0.35" />
        <rect x="27" y="6" width="16" height="2.6" rx="1.3" fill="currentColor" fillOpacity="0.5" />
        <rect x="27" y="12" width="14" height="2.2" rx="1.1" fill="currentColor" fillOpacity="0.25" />
        <rect x="27" y="17" width="16" height="2.2" rx="1.1" fill="currentColor" fillOpacity="0.25" />
        <rect x="27" y="22" width="11" height="2.2" rx="1.1" fill="currentColor" fillOpacity="0.25" />
      </Frame>
    );
  }
  return (
    <Frame>
      {[6, 12].map((y) => (
        <Row key={y} y={y} />
      ))}
      <line x1="1" y1="17" x2="47" y2="17" stroke="currentColor" strokeOpacity="0.35" />
      <rect x="7" y="21" width="22" height="2.6" rx="1.3" fill="currentColor" fillOpacity="0.5" />
      <rect x="7" y="26" width="30" height="2.2" rx="1.1" fill="currentColor" fillOpacity="0.25" />
    </Frame>
  );
}
