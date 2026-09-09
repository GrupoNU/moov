/**
 * Desktop notifications for new mail (L3 E9, GC-2).
 *
 * # What "parity" means here, and why it is smaller than it looks
 *
 * Gmail's own documentation settles the scope: "You get email notifications …
 * **after you sign in to Gmail and open it in your browser**"
 * (/mail/answer/1075549, canon §2.9). Gmail web does not run a push service
 * worker that wakes a closed browser; the notification is a foreground feature
 * of an open tab. So the parity bar is a `new Notification` fired from the tab
 * we already have, driven by the SSE stream E2 already opened — no VAPID, no
 * subscription endpoint, no server change at all. Web Push is beyond-Gmail
 * territory (plan §6), not a gap.
 *
 * # This module is pure on purpose
 *
 * Two decisions live here, both of which are easy to get subtly wrong and
 * impossible to test through a component that needs auth, a router, a JMAP
 * client and an EventSource:
 *
 *   1. **which messages are genuinely new** ({@link newArrivals}) — the refresh
 *      path refetches a whole window, so "the list changed" is not "mail
 *      arrived": a read receipt, an archive from another client, or simply a
 *      re-render all produce a new array with the same messages in it;
 *   2. **whether a notification may fire at all** ({@link shouldNotify}) — the
 *      product of a preference, a browser permission and the document's focus.
 *
 * The firing itself (constructing `Notification`, wiring its `onclick`) is
 * three lines in the screen, because that is all that is left once the two
 * decisions above are made somewhere they can be tested.
 */

import type { Email } from "./types";

/**
 * What the detector remembers between refreshes.
 *
 * `ids` is the set of message ids the previous window contained, and
 * `newestReceivedAt` is the high-water mark of their `receivedAt` — as an
 * epoch-millisecond number, so comparisons never depend on string formatting.
 *
 * `seeded` is the flag that makes the FIRST observation silent. Without it,
 * signing in would fire a notification for every message in the inbox, which
 * is the single most obnoxious bug this feature can ship.
 */
export interface ArrivalState {
  readonly ids: ReadonlySet<string>;
  readonly newestReceivedAt: number;
  readonly seeded: boolean;
}

/** The state before anything has been observed. */
export const EMPTY_ARRIVAL_STATE: ArrivalState = {
  ids: new Set<string>(),
  newestReceivedAt: 0,
  seeded: false,
};

/** What one observation produced: the messages to announce and the next state. */
export interface ArrivalResult {
  /** Newest first, and only ever messages that genuinely just arrived. */
  readonly arrivals: readonly Email[];
  readonly state: ArrivalState;
}

/** `receivedAt` as epoch milliseconds; 0 when absent or unparseable. */
export function receivedAtMillis(email: Email): number {
  if (email.receivedAt === undefined) return 0;
  const parsed = Date.parse(email.receivedAt);
  return Number.isNaN(parsed) ? 0 : parsed;
}

/**
 * The messages in `window` that arrived since the last observation.
 *
 * # The two conditions, and why neither alone is enough
 *
 * A message is an arrival when it is **both**:
 *
 *   - **absent from the previously seen id set** — this is what excludes the
 *     overwhelmingly common case, a refresh that returns the same window
 *     because a flag changed somewhere; and
 *   - **newer than the newest message previously seen** — this is what excludes
 *     the case an id set alone gets wrong: paginating, or a window that grows,
 *     surfaces OLD messages this client had never fetched. They are new to the
 *     id set and not new to the mailbox, and notifying for them is how a user
 *     scrolling their inbox gets a burst of toasts about mail from March.
 *
 * The strict `>` matters: a refresh whose newest message is the same message
 * announces nothing, and two messages that share a timestamp with the previous
 * high-water mark are treated as already-seen. Erring toward silence is the
 * correct direction for a feature whose failure mode is noise.
 *
 * # Seeding
 *
 * The first call — `state.seeded === false` — returns no arrivals and records
 * everything. That is the sign-in case and the "the list finished loading"
 * case, which are the same thing from here.
 */
export function newArrivals(
  window: readonly Email[],
  state: ArrivalState,
): ArrivalResult {
  const ids = new Set<string>();
  let newest = state.newestReceivedAt;
  for (const email of window) {
    ids.add(email.id);
    const at = receivedAtMillis(email);
    if (at > newest) newest = at;
  }

  const next: ArrivalState = { ids, newestReceivedAt: newest, seeded: true };

  if (!state.seeded) return { arrivals: [], state: next };

  const arrivals = window
    .filter(
      (email) =>
        !state.ids.has(email.id) && receivedAtMillis(email) > state.newestReceivedAt,
    )
    .sort((a, b) => receivedAtMillis(b) - receivedAtMillis(a));

  return { arrivals, state: next };
}

// ---------------------------------------------------------------------------
// the firing predicate
// ---------------------------------------------------------------------------

/** The document's attention, as the predicate cares about it. */
export interface DocumentAttention {
  /** `document.hasFocus()` — the window has keyboard focus. */
  readonly hasFocus: boolean;
  /** `document.visibilityState === "visible"`. */
  readonly isVisible: boolean;
}

/** Everything {@link shouldNotify} needs, and nothing it does not. */
export interface NotifyContext {
  /** The E5 preference: "new" or "off". */
  readonly mode: "new" | "off";
  /** `Notification.permission`, or undefined where the API does not exist. */
  readonly permission: NotificationPermission | undefined;
  readonly attention: DocumentAttention;
  /** False when the mailbox in view is one that must never notify. */
  readonly mailboxNotifiable: boolean;
}

/**
 * Whether a notification may be shown right now.
 *
 * Four conditions, all necessary:
 *
 *   1. **the preference is on.** GC-2 ships two modes; "off" means off.
 *   2. **the browser granted permission.** `default` is not a maybe — calling
 *      `new Notification` without a grant does nothing in every modern browser
 *      and throws in some. The permission PROMPT belongs to the settings
 *      toggle (E5 already requests it there); mail arriving is not a moment to
 *      interrupt someone with a browser dialog.
 *   3. **the document is not focused.** This is the one that is a product
 *      decision rather than a technical constraint, so it is worth stating:
 *      when the user is looking at the app, the mail simply appears in the
 *      list — that IS the notification. A desktop toast on top of it is a
 *      duplicate of something already on screen, and Gmail behaves the same
 *      way. "Not focused" covers both hidden (another tab, minimised) and
 *      visible-but-unfocused (a second monitor, a window behind the editor),
 *      because in both of those the list is not where the user is looking.
 *   4. **the mailbox may notify.** Junk, Trash and Sent never do — see
 *      {@link mailboxNotifiable}.
 */
export function shouldNotify(context: NotifyContext): boolean {
  if (context.mode !== "new") return false;
  if (context.permission !== "granted") return false;
  if (!context.mailboxNotifiable) return false;
  // Focused means "the user is here". Anything else — hidden, or visible on a
  // monitor they are not typing into — is a moment a toast is useful.
  if (context.attention.hasFocus && context.attention.isVisible) return false;
  return true;
}

/**
 * Whether mail landing in a mailbox with this role deserves a desktop toast.
 *
 * Spam is the obvious one: notifying about mail the filter already judged
 * unwanted defeats the filter. Trash is mail on its way out. **Sent is the
 * subtle one and the reason this is a function rather than a `role === "junk"`
 * check at the call site:** our own SSE fires when a message we sent lands in
 * Sent, so without this exclusion sending a mail would notify the sender about
 * their own mail, from their own machine, seconds after they clicked Send.
 *
 * `null` — a user-created folder — DOES notify: a Sieve rule filing mail into
 * "Clientes" is still mail arriving, and Gmail notifies for filtered mail that
 * stays in the mailbox. Drafts is excluded for the same reason as Sent: autosave
 * writes there constantly.
 */
export function mailboxNotifiable(role: string | null | undefined): boolean {
  return role !== "junk" && role !== "trash" && role !== "sent" && role !== "drafts";
}

// ---------------------------------------------------------------------------
// the content of one notification
// ---------------------------------------------------------------------------

/** The fields a `Notification` is constructed from. */
export interface NotificationContent {
  readonly title: string;
  readonly body: string;
  readonly tag: string;
  readonly icon: string;
}

/**
 * The icon a desktop toast carries.
 *
 * The BRAND-RESOLVED path, not the static `/icons/icon-192.png`: the server
 * answers this per Host, so a customer's notification carries their mark and
 * an unbranded installation gets Moov's own bytes indistinguishably. A toast
 * is the app's most out-of-context surface — it appears over somebody else's
 * window, with no other chrome to identify it — so it is the last place the
 * wrong logo should show up.
 */
export const NOTIFICATION_ICON = "/branding/icons/icon-192.png";

/** How much of the preview rides along under the subject. */
const PREVIEW_LIMIT = 120;

/**
 * Builds one notification's content.
 *
 * **Title is the sender, body is the subject** — that order is Gmail's and it
 * is the right one: a toast is glanced at, and "who" is what decides whether
 * the glance becomes a click. The display name is preferred over the address
 * because it is what the list shows; the address is the fallback, never a
 * silent blank.
 *
 * The `tag` is the message id, which is what makes re-notification impossible:
 * a browser replaces a notification carrying a tag it is already showing. That
 * matters because a flaky connection can deliver the same StateChange twice,
 * and the arrival detector is per-tab — two open tabs would otherwise show two
 * toasts for one message. With the tag they collapse into one.
 *
 * Nothing here is HTML: `Notification` renders plain text, and the strings come
 * from headers we do not control. The `preview` is truncated rather than
 * wrapped because browsers clip silently at a length they do not publish, and a
 * sentence cut mid-word by us is more honest than one cut mid-word by Chrome.
 */
export function notificationContent(
  email: Email,
  fallbackSender: string,
  noSubject: string,
): NotificationContent {
  const from = email.from?.[0];
  const title =
    from === undefined
      ? fallbackSender
      : from.name !== null && from.name.trim() !== ""
        ? from.name
        : from.email;

  const subject =
    email.subject !== undefined && email.subject !== null && email.subject.trim() !== ""
      ? email.subject
      : noSubject;

  const preview = (email.preview ?? "").trim();
  const body =
    preview === ""
      ? subject
      : `${subject}\n${preview.length > PREVIEW_LIMIT ? `${preview.slice(0, PREVIEW_LIMIT)}…` : preview}`;

  return { title, body, tag: `moov-mail-${email.id}`, icon: NOTIFICATION_ICON };
}
