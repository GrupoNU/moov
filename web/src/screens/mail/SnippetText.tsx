import { Fragment } from "react";

import { parseSnippet, type SnippetSegment } from "../../mail/snippet";
import styles from "./SnippetText.module.css";

/**
 * Renders a search snippet's highlighted text (L3 epic E3).
 *
 * # Why there is no `dangerouslySetInnerHTML` here, and never will be
 *
 * The server pins a contract — a snippet's only markup is `<mark>` (see
 * `internal/jmap/mail/snippet.go`) — and this component does not rely on it.
 * `parseSnippet` splits the string on the two literal token sequences and
 * hands back plain-text segments; each one is rendered as a React CHILD, which
 * React emits as a text node. Text nodes do not parse markup, so a snippet
 * containing `<script>` or `<img onerror=…>` renders as those literal
 * characters and nothing runs.
 *
 * That is a stronger property than sanitizing the string would give: there is
 * no parse step to attack, because the only elements in the output are the
 * `<mark>` this component itself creates.
 *
 * The `<mark>` element is also the RIGHT element semantically — HTML defines
 * it as "text marked or highlighted for reference purposes" — so a screen
 * reader can announce the match rather than the highlight being colour alone.
 */

export interface SnippetTextProps {
  /** The raw snippet string from `SearchSnippet/get`. */
  readonly raw: string | undefined;
  /** Rendered when there is no snippet — the row's ordinary text. */
  readonly fallback: React.ReactNode;
}

export function SnippetText({ raw, fallback }: SnippetTextProps): React.ReactElement {
  const segments = parseSnippet(raw);
  if (segments.length === 0) return <>{fallback}</>;

  /*
   * The index is the key, and here that is correct rather than a shortcut.
   *
   * A segment has no identity of its own — it IS a position in one string, and
   * the whole list is rebuilt whenever that string changes. There is no reorder
   * or insertion a stable key could help React through, because a new snippet
   * replaces every segment at once.
   */
  return (
    <>
      {segments.map((segment: SnippetSegment, index: number) =>
        segment.isMatch ? (
          <mark key={index} className={styles.mark}>
            {segment.text}
          </mark>
        ) : (
          <Fragment key={index}>{segment.text}</Fragment>
        ),
      )}
    </>
  );
}
