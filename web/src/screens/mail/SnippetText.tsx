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

  return (
    <>
      {segments.map((segment: SnippetSegment, index: number) =>
        segment.isMatch ? (
          // eslint-disable-next-line react/no-array-index-key -- segments have
          // no identity of their own; they are positions in one string, and the
          // list is re-created wholesale whenever that string changes.
          <mark key={index} className={styles.mark}>
            {segment.text}
          </mark>
        ) : (
          // eslint-disable-next-line react/no-array-index-key -- as above.
          <Fragment key={index}>{segment.text}</Fragment>
        ),
      )}
    </>
  );
}
