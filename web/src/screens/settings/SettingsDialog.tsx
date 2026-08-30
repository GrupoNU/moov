import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";

import { ThemeToggle } from "../../components/ThemeToggle";
import { useTranslation } from "../../i18n/I18nProvider";
import { usePrefs } from "../../mail/PrefsProvider";
import {
  AUTO_ADVANCE,
  DENSITIES,
  IMAGES_POLICIES,
  INBOX_TYPES,
  LANGUAGES,
  NOTIFICATION_MODES,
  READING_PANES,
  UNDO_SEND_SECONDS,
  type Prefs,
} from "../../mail/prefs";
import { searchSettings, type SearchableRow } from "../../mail/settingsSearch";
import type { Identity } from "../../mail/write";
import { LabelsSection, type LabelsSectionProps } from "./LabelsSection";
import {
  SECTION_IDS,
  SECTION_TITLES,
  SETTINGS_ROWS,
  type PlainStringKey,
  type SectionId,
} from "./registry";
import styles from "./SettingsDialog.module.css";

/**
 * The settings sheet — the full surface (L3 epic E5).
 *
 * # Dialog, not a route, and why
 *
 * P1 chose a `<dialog>` for three properties: `showModal()` makes the rest of
 * the page inert (not merely covered), traps focus while it is open, and closes
 * on Escape. Promoting settings to a route would give up all three and require
 * re-implementing them, and it would put the mail route model — which owns
 * mailboxes, messages and searches — in the business of describing a
 * preferences panel. Gmail's own settings are a full page, but Gmail's settings
 * are also fifteen tabs deep with server round trips per tab; ours are thirteen
 * preferences and four honest skeletons. The dialog is right for the size, and
 * the shape below (rail + panel) is the part of Gmail's IA that actually
 * carries: sections you can jump between without scroll-hunting.
 *
 * # The IA, adapted from Gmail (canon §3)
 *
 * General · Appearance · Inbox · Account · Filters · Forwarding · Vacation ·
 * Offline. The last four are SKELETONS — a named explanation of what lands and
 * when, and no control at all. That is principle P4 taken literally: never a
 * dead control, but a named absence is honest. A greyed-out "Create filter"
 * button would be the dead control; a paragraph saying filters arrive with the
 * Sieve epic is information.
 *
 * Deliberately absent: the entire IMAP/POP block. GC-9 — Dovecot IS the IMAP
 * server, so porting Gmail's IMAP settings would import Google's
 * web-store-vs-IMAP impedance debt to solve a problem we do not have.
 *
 * # Every row is registered twice, on purpose
 *
 * Each row appears in `registry.ts` (for search) and as JSX (for render).
 * The alternative — generating the JSX from the registry — was rejected: every
 * control here has different wiring (a select, a switch, a radio group, a
 * textarea with its own save), and a generator able to express all of them
 * would be a worse abstraction than the duplication. A test asserts the two
 * lists have not drifted, which is the drift-catching mechanism that
 * generation would have bought.
 */

export interface SettingsDialogProps {
  readonly isOpen: boolean;
  readonly onClose: () => void;
  /** The account's sending identity, for the read-only display and signature. */
  readonly identity?: Identity | undefined;
  /**
   * Saves the signature via `Identity/set`.
   *
   * Passed in rather than called here because the JMAP client lives in
   * MailScreen and this sheet is deliberately client-free — everything else it
   * writes goes through the prefs context.
   */
  readonly onSaveSignature?: ((textSignature: string) => Promise<boolean>) | undefined;
  /**
   * E8: everything the label manager needs, passed whole.
   *
   * Passed in for the same reason the signature saver is: the label operations
   * are JMAP calls and bounded migrations, and the JMAP client lives in
   * MailScreen. This sheet stays client-free, which is what keeps it testable
   * without standing up auth and a server.
   *
   * Optional, so a caller that has no label plumbing (a test, an embedding)
   * still renders every other section — the labels section then shows its own
   * empty state rather than crashing the sheet.
   */
  readonly labels?: LabelsSectionProps | undefined;
}

export function SettingsDialog({
  isOpen,
  onClose,
  identity,
  onSaveSignature,
  labels,
}: SettingsDialogProps): React.JSX.Element {
  const { t } = useTranslation();
  const dialogRef = useRef<HTMLDialogElement | null>(null);
  const returnFocusRef = useRef<HTMLElement | null>(null);
  const [query, setQuery] = useState("");
  const [activeSection, setActiveSection] = useState<SectionId>("general");
  const prefs = usePrefs();

  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) return;

    if (isOpen && !dialog.open) {
      // Remember where focus was so it can be restored on close.
      returnFocusRef.current =
        document.activeElement instanceof HTMLElement ? document.activeElement : null;
      dialog.showModal();
    } else if (!isOpen && dialog.open) {
      dialog.close();
      returnFocusRef.current?.focus();
    }
  }, [isOpen]);

  // A closed sheet forgets its search: reopening settings to find the same
  // thing again is unusual, and leaving the filter applied makes the panel look
  // half-empty for a reason the user has to remember.
  useEffect(() => {
    if (!isOpen) setQuery("");
  }, [isOpen]);

  // The dialog can close by means we did not initiate (Escape, the backdrop),
  // so the parent's state is synchronised from the element's own event rather
  // than assumed.
  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) return undefined;
    const handleClose = (): void => {
      returnFocusRef.current?.focus();
      onClose();
    };
    dialog.addEventListener("close", handleClose);
    return () => {
      dialog.removeEventListener("close", handleClose);
    };
  }, [onClose]);

  /*
   * Backdrop dismissal, attached natively rather than as a React onClick: a
   * <dialog> is not an interactive element, so an onClick on it is both a
   * jsx-a11y error and a genuine keyboard trap. It is a pure ENHANCEMENT for
   * pointer users — Escape and the close button both dismiss, and both work
   * from the keyboard.
   */
  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) return undefined;
    const onBackdropClick = (event: MouseEvent): void => {
      if (event.target === dialog) onClose();
    };
    dialog.addEventListener("click", onBackdropClick);
    return () => {
      dialog.removeEventListener("click", onBackdropClick);
    };
  }, [onClose]);

  /*
   * The search haystack is built from the RENDERED strings, so it is
   * automatically in the user's language — searching "idioma" in Spanish and
   * "language" in English both work without a second table, and a translation
   * fix improves the search for free.
   */
  const searchableRows = useMemo<readonly SearchableRow[]>(
    () =>
      SETTINGS_ROWS.map((row) => ({
        id: row.id,
        sectionId: row.sectionId,
        label: t(row.labelKey),
        ...(row.descriptionKey !== undefined ? { description: t(row.descriptionKey) } : {}),
        keywords: row.keywords,
      })),
    [t],
  );

  const search = useMemo(
    () => searchSettings(searchableRows, query),
    [searchableRows, query],
  );

  /*
   * While filtering, EVERY matching section renders — the rail's selection is
   * suspended, because a search whose results are hidden behind a section the
   * user is not standing in is a search that appears to have found nothing.
   */
  const visibleSections = search.isFiltering
    ? SECTION_IDS.filter((id) => search.sectionIds.has(id))
    : [activeSection];

  const showRow = useCallback(
    (id: string): boolean => !search.isFiltering || search.rowIds.has(id),
    [search],
  );

  return (
    <dialog ref={dialogRef} className={styles.dialog} aria-labelledby="settings-title">
      <div className={styles.content}>
        <div className={styles.header}>
          <div className={styles.headerText}>
            <h2 className={styles.title} id="settings-title">
              {t("settings.title")}
            </h2>
            <SettingsSearchBox value={query} onChange={setQuery} />
          </div>
          <button
            type="button"
            className={styles.close}
            onClick={onClose}
            aria-label={t("settings.close")}
          >
            <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" aria-hidden="true" focusable="false">
              <path d="M5.5 5.5l9 9M14.5 5.5l-9 9" />
            </svg>
          </button>
        </div>

        {/*
          The persistence status, stated rather than assumed.

          A server without the preferences capability still lets the user move
          every control — the choices apply for the session — but saying so is
          the difference between a degraded mode and a silent lie about what
          was saved.
        */}
        {(prefs.status === "unavailable" || prefs.error !== undefined) && (
          <p
            className={[styles.status, prefs.error !== undefined ? styles.statusError : ""]
              .filter(Boolean)
              .join(" ")}
            role="status"
          >
            {prefs.error !== undefined
              ? `${t("settings.saveFailed")}: ${prefs.error}`
              : t("settings.unavailable")}
          </p>
        )}

        <div className={styles.panes}>
          {/*
            The rail. Buttons rather than links, because there is no URL behind
            them — the sheet is not routed — and a link with href="#" is a
            keyboard trap dressed as navigation.
          */}
          <nav className={styles.nav} aria-label={t("settings.nav.label")}>
            {SECTION_IDS.map((id) => (
              <button
                key={id}
                type="button"
                className={[
                  styles.navItem,
                  !search.isFiltering && id === activeSection ? styles.navItemActive : "",
                ]
                  .filter(Boolean)
                  .join(" ")}
                aria-current={!search.isFiltering && id === activeSection ? "true" : undefined}
                onClick={() => {
                  // Picking a section clears the search: the two are competing
                  // ways to choose what is on screen, and leaving both active
                  // would show a section filtered by a query the user has
                  // stopped thinking about.
                  setQuery("");
                  setActiveSection(id);
                }}
              >
                {t(SECTION_TITLES[id])}
              </button>
            ))}
          </nav>

          <div className={styles.panel}>
            {search.isEmpty ? (
              <div className={styles.empty}>
                <p className={styles.emptyTitle}>{t("settings.search.empty")}</p>
                <p className={styles.emptyBody}>{t("settings.search.emptyBody")}</p>
              </div>
            ) : (
              visibleSections.map((sectionId) => (
                <SettingsSection key={sectionId} titleKey={SECTION_TITLES[sectionId]}>
                  {sectionId === "general" && (
                    <GeneralSection prefs={prefs} showRow={showRow} />
                  )}
                  {sectionId === "appearance" && (
                    <AppearanceSection prefs={prefs} showRow={showRow} />
                  )}
                  {sectionId === "inbox" && <InboxSection prefs={prefs} showRow={showRow} />}
                  {sectionId === "account" && (
                    <AccountSection
                      identity={identity}
                      onSaveSignature={onSaveSignature}
                      showRow={showRow}
                    />
                  )}
                  {sectionId === "labels" && showRow("labels") && labels !== undefined && (
                    <LabelsSection {...labels} />
                  )}
                  {sectionId === "filters" && showRow("filters") && (
                    <Skeleton titleKey="settings.filters.soon" bodyKey="settings.filters.soonBody" />
                  )}
                  {sectionId === "forwarding" && showRow("forwarding") && (
                    <Skeleton
                      titleKey="settings.forwarding.soon"
                      bodyKey="settings.forwarding.soonBody"
                    />
                  )}
                  {sectionId === "vacation" && showRow("vacation") && (
                    <Skeleton
                      titleKey="settings.vacation.soon"
                      bodyKey="settings.vacation.soonBody"
                    />
                  )}
                  {sectionId === "offline" && showRow("offline") && (
                    <Skeleton titleKey="settings.offline.soon" bodyKey="settings.offline.soonBody" />
                  )}
                </SettingsSection>
              ))
            )}
          </div>
        </div>
      </div>
    </dialog>
  );
}

// ---------------------------------------------------------------------------
// the sections
// ---------------------------------------------------------------------------

type PrefsApi = ReturnType<typeof usePrefs>;

interface SectionProps {
  readonly prefs: PrefsApi;
  readonly showRow: (id: string) => boolean;
}

function GeneralSection({ prefs, showRow }: SectionProps): React.JSX.Element {
  const { t, format } = useTranslation();
  const set = prefs.setPref;

  return (
    <>
      {showRow("language") && (
        <SettingRow labelKey="settings.language.label" descriptionKey="settings.language.description">
          {/*
            The locale switcher the canon flagged as missing (§6.10: "no locale
            switcher (browser-detect only)"). `null` is "follow the browser",
            which is what the wire carries and what I18nProvider's optional
            `locale` prop was reserved for — its own comment says "used by tests
            and by a future user preference".
          */}
          <Select
            label={t("settings.language.label")}
            value={prefs.prefs.language ?? "auto"}
            onChange={(value) => {
              void set("language", value === "auto" ? null : (value as "es" | "en"));
            }}
            options={[
              { value: "auto", label: t("settings.language.auto") },
              ...LANGUAGES.map((code) => ({
                value: code,
                label: code === "es" ? t("settings.language.es") : t("settings.language.en"),
              })),
            ]}
          />
        </SettingRow>
      )}

      {showRow("undoSend") && (
        <SettingRow labelKey="settings.undoSend.label" descriptionKey="settings.undoSend.description">
          {/*
            Gmail's exact four values (canon §2.3). The SERVER consumes this —
            it is what sets `sendAt` on a submission — so the composer's
            countdown keeps reflecting the server's own deadline rather than
            re-deriving it from this number, which is why there is nothing to
            wire on the client beyond the save.
          */}
          <Select
            label={t("settings.undoSend.label")}
            value={String(prefs.prefs.undoSendSeconds)}
            onChange={(value) => {
              void set("undoSendSeconds", Number(value) as Prefs["undoSendSeconds"]);
            }}
            options={UNDO_SEND_SECONDS.map((seconds) => ({
              value: String(seconds),
              label: format("settings.undoSend.seconds", seconds),
            }))}
          />
        </SettingRow>
      )}

      {showRow("images") && (
        <SettingRow labelKey="settings.images.label" descriptionKey="settings.images.description">
          <Select
            label={t("settings.images.label")}
            value={prefs.prefs.imagesPolicy}
            onChange={(value) => {
              void set("imagesPolicy", value as Prefs["imagesPolicy"]);
            }}
            options={IMAGES_POLICIES.map((policy) => ({
              value: policy,
              label: policy === "always" ? t("settings.images.always") : t("settings.images.ask"),
            }))}
          />
        </SettingRow>
      )}

      {showRow("conversationView") && (
        <SettingRow
          labelKey="settings.conversation.label"
          descriptionKey="settings.conversation.description"
        >
          <Switch
            label={t("settings.conversation.label")}
            checked={prefs.prefs.conversationView}
            onChange={(checked) => {
              void set("conversationView", checked);
            }}
          />
        </SettingRow>
      )}

      {showRow("hoverActions") && (
        <SettingRow labelKey="settings.hover.label" descriptionKey="settings.hover.description">
          <Switch
            label={t("settings.hover.label")}
            checked={prefs.prefs.hoverActions}
            onChange={(checked) => {
              void set("hoverActions", checked);
            }}
          />
        </SettingRow>
      )}

      {showRow("autoAdvance") && (
        <SettingRow
          labelKey="settings.autoAdvance.label"
          descriptionKey="settings.autoAdvance.description"
        >
          <Select
            label={t("settings.autoAdvance.label")}
            value={prefs.prefs.autoAdvance}
            onChange={(value) => {
              void set("autoAdvance", value as Prefs["autoAdvance"]);
            }}
            options={AUTO_ADVANCE.map((mode) => ({
              value: mode,
              label:
                mode === "list"
                  ? t("settings.autoAdvance.list")
                  : mode === "newer"
                    ? t("settings.autoAdvance.newer")
                    : t("settings.autoAdvance.older"),
            }))}
          />
        </SettingRow>
      )}

      {showRow("keyboardShortcuts") && (
        <SettingRow labelKey="settings.keyboard.label" descriptionKey="settings.keyboard.description">
          <Switch
            label={t("settings.keyboard.label")}
            checked={prefs.prefs.keyboardShortcuts}
            onChange={(checked) => {
              void set("keyboardShortcuts", checked);
            }}
          />
        </SettingRow>
      )}

      {showRow("showSnippets") && (
        <SettingRow labelKey="settings.snippets.label" descriptionKey="settings.snippets.description">
          <Switch
            label={t("settings.snippets.label")}
            checked={prefs.prefs.showSnippets}
            onChange={(checked) => {
              void set("showSnippets", checked);
            }}
          />
        </SettingRow>
      )}
    </>
  );
}

function AppearanceSection({ prefs, showRow }: SectionProps): React.JSX.Element {
  const { t } = useTranslation();
  const set = prefs.setPref;

  return (
    <>
      {showRow("theme") && (
        <SettingRow labelKey="theme.label" descriptionKey="settings.theme.description">
          {/*
            Controlled by the account preference. ThemeToggle still writes the
            attribute and the localStorage cache itself, which is what the
            pre-paint script in index.html reads next load — prefs is the source
            of truth, localStorage is its mirror.
          */}
          <ThemeToggle
            value={prefs.prefs.theme}
            onChange={(theme) => {
              void set("theme", theme);
            }}
          />
        </SettingRow>
      )}

      {showRow("density") && (
        <SettingRow labelKey="settings.density.label" descriptionKey="settings.density.description">
          <Select
            label={t("settings.density.label")}
            value={prefs.prefs.density}
            onChange={(value) => {
              void set("density", value as Prefs["density"]);
            }}
            options={DENSITIES.map((density) => ({
              value: density,
              label:
                density === "default"
                  ? t("settings.density.default")
                  : density === "comfortable"
                    ? t("settings.density.comfortable")
                    : t("settings.density.compact"),
            }))}
          />
        </SettingRow>
      )}

      {showRow("readingPane") && (
        <SettingRow
          labelKey="settings.readingPane.label"
          descriptionKey="settings.readingPane.description"
        >
          <Select
            label={t("settings.readingPane.label")}
            value={prefs.prefs.readingPane}
            onChange={(value) => {
              void set("readingPane", value as Prefs["readingPane"]);
            }}
            options={READING_PANES.map((pane) => ({
              value: pane,
              label:
                pane === "none"
                  ? t("settings.readingPane.none")
                  : pane === "right"
                    ? t("settings.readingPane.right")
                    : t("settings.readingPane.bottom"),
            }))}
          />
        </SettingRow>
      )}
    </>
  );
}

function InboxSection({ prefs, showRow }: SectionProps): React.JSX.Element {
  const { t } = useTranslation();
  const set = prefs.setPref;

  return (
    <>
      {showRow("inboxType") && (
        <SettingRow
          labelKey="settings.inboxType.label"
          descriptionKey="settings.inboxType.description"
        >
          <Select
            label={t("settings.inboxType.label")}
            value={prefs.prefs.inboxType}
            onChange={(value) => {
              void set("inboxType", value as Prefs["inboxType"]);
            }}
            options={INBOX_TYPES.map((type) => ({
              value: type,
              label:
                type === "default"
                  ? t("settings.inboxType.default")
                  : type === "unread_first"
                    ? t("settings.inboxType.unread_first")
                    : t("settings.inboxType.starred_first"),
            }))}
          />
        </SettingRow>
      )}

      {showRow("notifications") && <NotificationsRow prefs={prefs} />}
    </>
  );
}

/**
 * The notifications row (GC-2).
 *
 * # What the toggle now drives
 *
 * E9b landed the firing side: `mail/notify.ts` decides which messages are
 * genuinely new and whether a toast may be shown, and `MailScreen` constructs
 * the `Notification` on the SSE refresh path. So this row is a live control end
 * to end — the placeholder that said otherwise is deleted rather than reworded.
 *
 * Turning it on requests the browser permission and the row reports the honest
 * outcome (granted / denied / will-ask / unsupported). A denied browser is
 * stated as such rather than left looking like our bug, which is the failure
 * mode every notification opt-in has.
 *
 * Gmail's third mode ("important mail only") is absent on purpose — it needs
 * the importance classifier, which is AI-phase, and a mode whose classifier
 * does not exist is the dead control in another costume (GC-2).
 */
function NotificationsRow({ prefs }: { readonly prefs: PrefsApi }): React.JSX.Element {
  const { t } = useTranslation();
  const supported = typeof Notification !== "undefined";
  const [permission, setPermission] = useState<NotificationPermission | undefined>(
    supported ? Notification.permission : undefined,
  );

  const note = !supported
    ? t("settings.notifications.unsupported")
    : permission === "granted"
      ? t("settings.notifications.granted")
      : permission === "denied"
        ? t("settings.notifications.denied")
        : t("settings.notifications.pending");

  return (
    <SettingRow
      labelKey="settings.notifications.label"
      descriptionKey="settings.notifications.description"
    >
      <div className={styles.signatureField}>
        <Select
          label={t("settings.notifications.label")}
          value={prefs.prefs.notifications}
          onChange={(value) => {
            const mode = value as Prefs["notifications"];
            void prefs.setPref("notifications", mode);
            /*
             * The permission is requested on the way IN only. Asking again on
             * "off" would be a prompt for a capability the user just declined
             * to use, and browsers rate-limit the request — spending it here
             * would leave the real opt-in unable to ask.
             */
            if (mode === "new" && supported && Notification.permission === "default") {
              void Notification.requestPermission().then((result) => {
                setPermission(result);
              });
            }
          }}
          options={NOTIFICATION_MODES.map((mode) => ({
            value: mode,
            label:
              mode === "new" ? t("settings.notifications.new") : t("settings.notifications.off"),
          }))}
        />
        <span className={styles.signatureNote}>{note}</span>
      </div>
    </SettingRow>
  );
}

/** Account: the identity, read-only, plus an editable text signature. */
function AccountSection({
  identity,
  onSaveSignature,
  showRow,
}: {
  readonly identity: Identity | undefined;
  readonly onSaveSignature: ((textSignature: string) => Promise<boolean>) | undefined;
  readonly showRow: (id: string) => boolean;
}): React.JSX.Element {
  const { t } = useTranslation();
  const [draft, setDraft] = useState(identity?.textSignature ?? "");
  const [state, setState] = useState<"idle" | "saving" | "saved" | "failed">("idle");
  const fieldId = useId();

  // The identity arrives asynchronously; the textarea must adopt it when it
  // does, but must NOT stomp on what the user has typed since.
  const loadedFor = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (identity === undefined || loadedFor.current === identity.id) return;
    loadedFor.current = identity.id;
    setDraft(identity.textSignature);
  }, [identity]);

  return (
    <>
      {showRow("identity") && (
        <SettingRow labelKey="settings.identity.label" descriptionKey="settings.identity.description">
          {identity === undefined ? (
            <span className={styles.signatureNote}>{t("settings.identity.missing")}</span>
          ) : (
            <span className={styles.identity}>
              {identity.name === "" ? identity.email : identity.name}
              {identity.name !== "" && (
                <span className={styles.identityEmail}>{identity.email}</span>
              )}
            </span>
          )}
        </SettingRow>
      )}

      {showRow("signature") && (
        <SettingRow labelKey="settings.signature.label" descriptionKey="settings.signature.description">
          <div className={styles.signatureField}>
            {/*
              A real <label> here, unlike the row's heading: this control IS a
              single input, so the label can point at it and give it its
              accessible name. The row heading stays the visible title.
            */}
            <label className="visually-hidden" htmlFor={fieldId}>
              {t("settings.signature.label")}
            </label>
            <textarea
              id={fieldId}
              className={styles.signatureInput}
              value={draft}
              disabled={identity === undefined || onSaveSignature === undefined}
              onChange={(event) => {
                setDraft(event.target.value);
                setState("idle");
              }}
            />
            <div className={styles.signatureActions}>
              <button
                type="button"
                className={styles.signatureSave}
                disabled={
                  identity === undefined ||
                  onSaveSignature === undefined ||
                  state === "saving" ||
                  draft === (identity?.textSignature ?? "")
                }
                onClick={() => {
                  if (onSaveSignature === undefined) return;
                  setState("saving");
                  void onSaveSignature(draft).then((ok) => {
                    setState(ok ? "saved" : "failed");
                  });
                }}
              >
                {t("settings.signature.save")}
              </button>
              {/*
                An explicit save, unlike every other row here.

                The rest are single-value controls where the gesture IS the
                decision, so an optimistic save is right. A signature is prose:
                autosaving on every keystroke would mean a half-typed sentence
                is briefly the user's real signature, and debouncing it just
                moves the window. `Identity/set` is also not a preference — it
                is the account's outbound identity, and it deserves a
                deliberate commit.
              */}
              <span className={styles.signatureNote} role="status">
                {state === "saving"
                  ? t("settings.saving")
                  : state === "saved"
                    ? t("settings.signature.saved")
                    : state === "failed"
                      ? t("settings.signature.failed")
                      : ""}
              </span>
            </div>
          </div>
        </SettingRow>
      )}
    </>
  );
}

// ---------------------------------------------------------------------------
// the primitives
// ---------------------------------------------------------------------------

/** The settings search box (D-5). */
function SettingsSearchBox({
  value,
  onChange,
}: {
  readonly value: string;
  readonly onChange: (value: string) => void;
}): React.JSX.Element {
  const { t } = useTranslation();
  const id = useId();

  return (
    <div className={styles.search}>
      <svg
        className={styles.searchIcon}
        viewBox="0 0 20 20"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.7"
        strokeLinecap="round"
        aria-hidden="true"
        focusable="false"
      >
        <circle cx="9" cy="9" r="5.6" />
        <path d="M13.2 13.2l3.4 3.4" />
      </svg>
      <label className="visually-hidden" htmlFor={id}>
        {t("settings.search.label")}
      </label>
      <input
        id={id}
        type="search"
        className={styles.searchInput}
        placeholder={t("settings.search.placeholder")}
        value={value}
        onChange={(event) => {
          onChange(event.target.value);
        }}
        onKeyDown={(event) => {
          /*
           * Escape CLEARS the query rather than closing the sheet, and only
           * while there is a query to clear. It is stopped from propagating so
           * MailScreen's global handler does not read it as "close the
           * settings" — with an empty box it is allowed through, which is the
           * behaviour a user expects from a search field inside a modal.
           */
          if (event.key === "Escape" && value !== "") {
            event.stopPropagation();
            event.preventDefault();
            onChange("");
          }
        }}
      />
      {value !== "" && (
        <button
          type="button"
          className={styles.searchClear}
          aria-label={t("settings.search.clear")}
          onClick={() => {
            onChange("");
          }}
        >
          <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true" focusable="false">
            <path d="M6 6l8 8M14 6l-8 8" />
          </svg>
        </button>
      )}
    </div>
  );
}

/** A labelled select. */
function Select({
  label,
  value,
  onChange,
  options,
}: {
  readonly label: string;
  readonly value: string;
  readonly onChange: (value: string) => void;
  readonly options: readonly { readonly value: string; readonly label: string }[];
}): React.JSX.Element {
  return (
    <select
      className={styles.select}
      aria-label={label}
      value={value}
      onChange={(event) => {
        onChange(event.target.value);
      }}
    >
      {options.map((option) => (
        <option key={option.value} value={option.value}>
          {option.label}
        </option>
      ))}
    </select>
  );
}

/**
 * A two-state switch.
 *
 * A real `<input type="checkbox">` with a drawn track, not a div with a role:
 * Space, the focus ring, the checked state and the accessible role all come
 * from the browser. `role="switch"` is added because that is what it IS to a
 * screen reader — "on/off", not "checked/unchecked".
 */
function Switch({
  label,
  checked,
  onChange,
}: {
  readonly label: string;
  readonly checked: boolean;
  readonly onChange: (checked: boolean) => void;
}): React.JSX.Element {
  return (
    <span className={styles.switch}>
      <input
        type="checkbox"
        role="switch"
        className={styles.switchInput}
        aria-label={label}
        checked={checked}
        onChange={(event) => {
          onChange(event.target.checked);
        }}
      />
      <span className={styles.switchTrack} aria-hidden="true" />
    </span>
  );
}

/**
 * A named absence (principle P4).
 *
 * Not a disabled control and not a placeholder that looks interactive: a
 * statement of what is coming and which work brings it. The dashed border says
 * "nothing here yet" without inviting a click.
 */
function Skeleton({
  titleKey,
  bodyKey,
}: {
  readonly titleKey: PlainStringKey;
  readonly bodyKey: PlainStringKey;
}): React.JSX.Element {
  const { t } = useTranslation();
  return (
    <div className={styles.skeleton}>
      <strong className={styles.skeletonTitle}>{t(titleKey)}</strong>
      <p className={styles.skeletonBody}>{t(bodyKey)}</p>
    </div>
  );
}

/** A titled group of settings rows. */
function SettingsSection({
  titleKey,
  children,
}: {
  readonly titleKey: PlainStringKey;
  readonly children: React.ReactNode;
}): React.JSX.Element {
  const { t } = useTranslation();
  return (
    /*
     * A <section> named by its own heading, so a screen reader user can jump
     * between groups instead of walking every row of a long sheet.
     */
    <section className={styles.section} aria-labelledby={`settings-${titleKey}`}>
      <h3 className={styles.sectionTitle} id={`settings-${titleKey}`}>
        {t(titleKey)}
      </h3>
      <div className={styles.rows}>{children}</div>
    </section>
  );
}

/**
 * One setting: what it is on the left, the control on the right.
 *
 * The label is NOT a <label> element and does not point at the control. Some
 * controls here are single inputs (a switch) and some are groups (the theme
 * radios, which carry their own fieldset/legend); a <label> can only name the
 * first kind, and pointing one at a fieldset produces a name that screen
 * readers announce inconsistently. So each control stays responsible for its
 * own accessible name — ThemeToggle's legend says "Theme" — and this text is
 * the VISIBLE heading of the row.
 */
function SettingRow({
  labelKey,
  descriptionKey,
  children,
}: {
  readonly labelKey: PlainStringKey;
  readonly descriptionKey?: PlainStringKey;
  readonly children: React.ReactNode;
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
      <div className={styles.rowControl}>{children}</div>
    </div>
  );
}
