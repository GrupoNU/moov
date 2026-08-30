import { useTranslation } from "../../i18n/I18nProvider";
import { showsConnectionPill, type ConnectionState } from "../../mail/connection";
import styles from "./ConnectionPill.module.css";

/**
 * The connection pill (L3 E9, the gap E2 recorded).
 *
 * # What it fixes
 *
 * Before this, a dead SSE stream looked exactly like an inbox with no new mail:
 * no spinner, no error, no difference at all. A user who knows the connection
 * is down reloads; a user who does not know misses mail and blames themselves.
 *
 * # Why it is a pill and not a banner
 *
 * It must not push the list down. A bar that appears and disappears with the
 * network would reflow the message list — and therefore the virtualizer's
 * viewport — every time a phone changes cell, which is both visually jarring
 * and a source of scroll jumps. So it floats over the top of the list column,
 * small, and out of the way of the rows.
 *
 * # Announced, but not interrupting
 *
 * `role="status"` with `aria-live="polite"` states the change to a screen
 * reader at the next natural pause. `assertive` would cut the user off
 * mid-sentence for a condition that is usually transient, which is exactly the
 * kind of over-announcing that makes people turn live regions off.
 *
 * The region is rendered ALWAYS, with its content emptied when connected,
 * rather than mounted and unmounted with the state: a live region inserted
 * together with its own text is frequently not announced at all, because the
 * assistive tech never observed it changing.
 */
export function ConnectionPill({
  state,
}: {
  readonly state: ConnectionState;
}): React.JSX.Element {
  const { t } = useTranslation();
  const visible = showsConnectionPill(state);

  return (
    <div className={styles.region} role="status" aria-live="polite">
      {visible && (
        <span
          className={[
            styles.pill,
            state === "offline" ? styles.offline : styles.reconnecting,
          ].join(" ")}
        >
          <span className={styles.dot} aria-hidden="true" />
          {state === "offline" ? t("connection.offline") : t("connection.reconnecting")}
        </span>
      )}
    </div>
  );
}
