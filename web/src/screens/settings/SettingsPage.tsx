import { useConfirm } from "../../components/useConfirm";
import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import { usePrefs } from "../../mail/PrefsProvider";
import {
  AUTO_ADVANCE,
  IMAGES_POLICIES,
  INBOX_TYPES,
  LANGUAGES,
  MAX_SIGNATURE_ITEMS,
  NOTIFICATION_MODES,
  OFFLINE_DEPTH_BOUNDS,
  READING_PANES,
  REPLY_BEHAVIORS,
  UNDO_SEND_SECONDS,
  type Prefs,
  type SignatureItem,
} from "../../mail/prefs";
import { searchSettings, type SearchableRow } from "../../mail/settingsSearch";
import type { Identity } from "../../mail/write";
import { BlockedSection, type BlockedSectionProps } from "./BlockedSection";
import { FiltersSection, type FiltersSectionProps } from "./FiltersSection";
import { ForwardingSection, type ForwardingSectionProps } from "./ForwardingSection";
import { LabelsSection, type LabelsSectionProps } from "./LabelsSection";
import { OptionGroup } from "./OptionGroup";
/*
 * F-34/F-35: the page renders the PANEL's thumbnails and the same label tables
 * the panel reads. One implementation behind both surfaces is what keeps them
 * from drifting into different words for the same option.
 */
import {
  AUTO_ADVANCE_LABELS,
  AUTO_ADVANCE_NOTES,
  IMAGES_LABELS,
  IMAGES_NOTES,
  INBOX_TYPE_LABELS,
  READING_PANE_LABELS,
  REPLY_BEHAVIOR_LABELS,
  REPLY_BEHAVIOR_NOTES,
} from "./optionLabels";
import { InboxTypeThumb, ReadingPaneThumb } from "./QuickThumbnails";
import { QuotaRow, type QuotaRowProps } from "./QuotaRow";
import { VacationSection, type VacationSectionProps } from "./VacationSection";
import { SETTINGS_TABS, type SettingsTab } from "../../router/routes";
import {
  SECTION_IDS,
  SECTION_TITLES,
  SETTINGS_ROWS,
  TAB_TITLES,
  sectionsOfTab,
  type PlainStringKey,
} from "./registry";
import { useSaveFeedback } from "./useSaveFeedback";
import styles from "./SettingsPage.module.css";

/**
 * The settings PAGE — the full surface (L3 epic E5; re-homed by E12).
 *
 * # It was a dialog until E12, and the reversal is deliberate
 *
 * P1 chose a `<dialog>` for three properties `showModal()` supplies free:
 * inertness, a focus trap, and Escape. The header that stood here argued the
 * dialog was "right for the size" — thirteen preferences against Gmail's
 * fifteen tabs. Canon 07 §5 overrules it, and the argument that wins is not
 * about size at all: Gmail's settings are a DESTINATION. They replace the list
 * area while the top bar and rail stay put, which means they have a URL. You
 * can send someone "the filters tab", bookmark it, reach it with Back, and land
 * on it from the quick panel. A `<dialog>` can be none of those, and this app's
 * previous one could not: there was no address for "settings, filters".
 *
 * The three free properties are given up, and two of them SHOULD be: inertness
 * and a focus trap are wrong on a page — a page is something you can tab out
 * of. What replaces Escape is what already returns from any other view: `u`, or
 * Back, or the explicit "Back to mail" affordance.
 *
 * # The IA, from canon 07 §5
 *
 * A horizontal tab row — General · Labels · Inbox · Account · Filters and
 * blocked addresses · Forwarding · Offline — over two-column rows: the setting's
 * name in a fixed left column, its control on the right. The tab set is Gmail's
 * with its stated exclusions applied: no Complementos/Chat/Temas (Google
 * ecosystem chrome) and no POP/IMAP (GC-9 — Dovecot IS the IMAP server, so
 * porting Gmail's IMAP settings would import Google's web-store-vs-IMAP
 * impedance debt to solve a problem we do not have).
 *
 * TABS are coarser than SECTIONS, which is new here and is Gmail's own shape:
 * "Filters and blocked addresses" is one tab holding two sections. The mapping
 * lives in `registry.ts` as a total record, so a section with no tab is a
 * compile error rather than a section that silently renders nowhere.
 *
 * Two settings are deliberately NOT on this page — theme and density. Their
 * controls live in the quick panel (B2), which is the only surface where the
 * change is visible as you make it, and duplicating them here would be two live
 * controls over one preference. Their registry rows stay, so the search still
 * finds them, and the page renders a pointer at the panel instead.
 *
 * Sections whose capability the server does not advertise render an honest
 * SKELETON — a named absence rather than a greyed-out control. That is
 * principle P4 taken literally.
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

export interface SettingsPageProps {
  /** The tab the route names. */
  readonly tab: SettingsTab;
  /** Navigates to another tab — the caller owns the URL. */
  readonly onSelectTab: (tab: SettingsTab) => void;
  /** Leaves settings for the mail the user came from. */
  readonly onClose: () => void;
  /**
   * Opens the quick-settings panel, for the two rows whose control lives there.
   *
   * Absent renders the pointer as plain text rather than as a button — a
   * caller with no panel wired must not offer to open one.
   */
  readonly onOpenQuickSettings?: (() => void) | undefined;
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
  /**
   * E7: the address index, for the autocomplete row.
   *
   * Absent hides the row entirely — a browser with no usable storage has no
   * index to govern, and a switch over nothing is the dead control P4 forbids.
   */
  readonly addresses?: AddressSettings | undefined;
  /**
   * E6: everything the four Sieve-backed sections need, passed whole.
   *
   * Each is INDEPENDENTLY optional, and absent means the section falls back to
   * its honest skeleton. That is not defensive coding — it is the shape of the
   * server: `internal/jmaphttp/session.go` gates the filter, vacation and quota
   * capabilities on three separate config fields, so a deployment can genuinely
   * have one and not the others, and the sheet has to render that truthfully
   * rather than assuming they arrive together.
   */
  readonly filters?: FiltersSectionProps | undefined;
  readonly blocked?: BlockedSectionProps | undefined;
  readonly forwarding?: ForwardingSectionProps | undefined;
  readonly vacation?: VacationSectionProps | undefined;
  readonly quota?: QuotaRowProps | undefined;
}

/**
 * What the autocomplete row needs (E7).
 *
 * A narrow structural type rather than importing `AddressIndexApi`: this
 * dialog has no business with the index's feeds, and naming only what it reads
 * keeps a future change to `record`/`recordSent` from rippling into a settings
 * screen that never called them.
 */
export interface AddressSettings {
  readonly enabled: boolean;
  readonly setEnabled: (enabled: boolean) => void;
  readonly count: number;
  readonly clear: () => Promise<void>;
}


export function SettingsPage({
  tab,
  onSelectTab,
  onClose,
  onOpenQuickSettings,
  identity,
  onSaveSignature,
  labels,
  addresses,
  filters,
  blocked,
  forwarding,
  vacation,
  quota,
}: SettingsPageProps): React.JSX.Element {
  const { t } = useTranslation();
  const [query, setQuery] = useState("");
  const prefs = usePrefs();

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
   * D-5: while filtering, the TABS are suspended and every matching section
   * renders, wherever it lives.
   *
   * This is the one place the page deliberately stops being a tabbed page, and
   * it is the same rule the rail followed before: a search whose results are
   * hidden behind a tab the user is not standing in is a search that appears to
   * have found nothing. The tab row stays visible and stays operable — picking
   * one clears the query, because the two are competing ways to choose what is
   * on screen.
   */
  const visibleSections = search.isFiltering
    ? SECTION_IDS.filter((id) => search.sectionIds.has(id))
    : sectionsOfTab(tab);

  const showRow = useCallback(
    (id: string): boolean => !search.isFiltering || search.rowIds.has(id),
    [search],
  );

  /*
   * P0-7: the rows that exist ONLY as search results.
   *
   * Theme and density have no control on this page — theirs lives in the quick
   * panel, where the change is visible as you make it. They are still
   * registered so the settings search (D-5) can find them, and this is what
   * separates "found by searching" from "browsing the tab": a search hit
   * renders a row that OPENS the panel, and browsing renders nothing at all
   * rather than a row that settles nothing.
   */
  const isSearchHit = useCallback(
    (id: string): boolean => search.isFiltering && search.rowIds.has(id),
    [search],
  );

  /*
   * E6: the quota is re-read when the Account section comes into view.
   *
   * Usage moves with every delivery and `Quota/changes` answers
   * `cannotCalculateChanges` on purpose ("quota usage has no changelog; refetch
   * with Quota/get"), so there is no push to subscribe to and no cursor to
   * poll. Refetching exactly when the number is about to be READ is the whole
   * refresh strategy, and it is the one the server's own comment prescribes.
   *
   * `visibleSections` rather than the tab, so a SEARCH that surfaces the
   * storage row also refreshes it — otherwise finding it by typing
   * "almacenamiento" would show a figure from whenever the page last loaded.
   */
  const showsAccount = visibleSections.includes("account");
  const refreshQuota = quota?.onRefresh;
  useEffect(() => {
    if (!showsAccount) return;
    refreshQuota?.();
  }, [showsAccount, refreshQuota]);

  return (
    <div className={styles.page}>
      <div className={styles.header}>
        <div className={styles.headerTop}>
          {/*
            "Back to mail" is an explicit control, not only a keyboard path.
            The page took over the list area, so the way out has to be visible:
            `u` and the browser's Back both work, but neither is discoverable by
            someone who arrived here by clicking a gear.
          */}
          <button
            type="button"
            className={styles.back}
            onClick={onClose}
            aria-label={t("settings.backToMail")}
            title={t("settings.backToMail")}
          >
            <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
              <path d="M16 10H4.5m0 0l4.8-4.8M4.5 10l4.8 4.8" />
            </svg>
          </button>
          <h1 className={styles.title}>{t("settings.title")}</h1>
          {/* D-5: the search moves into the page header, where canon 07 §5 puts
              it — "an addition, placed unobtrusively". */}
          <SettingsSearchBox value={query} onChange={setQuery} />
        </div>

        {/*
          The horizontal tab row (canon 07 §5).

          Real APG `tab`s inside a `tablist`, so the arrow keys move between
          them and only the selected one is in the tab order — which is what
          makes a seven-tab row one stop rather than seven. They are BUTTONS and
          not links even though each has a URL, because the panel below is not
          re-fetched: `onSelectTab` navigates, and rendering them as anchors
          would invite a middle-click that opens a second copy of the whole app
          to show a different heading.
        */}
        <div className={styles.tabs} role="tablist" aria-label={t("settings.tabs.label")}>
          {SETTINGS_TABS.map((id) => {
            const isActive = !search.isFiltering && id === tab;
            return (
              <button
                key={id}
                type="button"
                role="tab"
                id={`settings-tab-${id}`}
                aria-selected={isActive}
                aria-controls="settings-panel"
                /* Roving tabindex: only the selected tab is reachable by Tab,
                   and the arrows move within the row (the APG contract). */
                tabIndex={isActive ? 0 : -1}
                className={[styles.tab, isActive ? styles.tabActive : ""]
                  .filter(Boolean)
                  .join(" ")}
                onClick={() => {
                  // Picking a tab clears the search: the two are competing ways
                  // to choose what is on screen, and leaving both active would
                  // show a tab filtered by a query the user has stopped
                  // thinking about.
                  setQuery("");
                  onSelectTab(id);
                }}
                onKeyDown={(event) => {
                  const step =
                    event.key === "ArrowRight" ? 1 : event.key === "ArrowLeft" ? -1 : 0;
                  if (step === 0) return;
                  event.preventDefault();
                  const index = SETTINGS_TABS.indexOf(id);
                  // Wraps at both ends, which APG lists as the expected
                  // behaviour and which saves the row from a dead end.
                  const next =
                    SETTINGS_TABS[
                      (index + step + SETTINGS_TABS.length) % SETTINGS_TABS.length
                    ];
                  if (next === undefined) return;
                  setQuery("");
                  onSelectTab(next);
                  document.getElementById(`settings-tab-${next}`)?.focus();
                }}
              >
                {t(TAB_TITLES[id])}
              </button>
            );
          })}
        </div>
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

      <div
        className={styles.panel}
        id="settings-panel"
        role="tabpanel"
        /*
         * The panel is named by its tab — but only while a tab is genuinely
         * selected. Under a search the tabs are suspended, and pointing at a
         * tab that is not selected would name the panel after a heading that
         * is not what it contains.
         */
        {...(search.isFiltering ? {} : { "aria-labelledby": `settings-tab-${tab}` })}
        /* Focusable so a keyboard user can reach the panel's content directly
           from its tab, which is the APG tabpanel contract for a panel whose
           first child is not itself focusable. */
        tabIndex={0}
      >
        {search.isEmpty ? (
          <div className={styles.empty}>
            <p className={styles.emptyTitle}>{t("settings.search.empty")}</p>
            <p className={styles.emptyBody}>{t("settings.search.emptyBody")}</p>
          </div>
        ) : (
          visibleSections.map((sectionId) => (
            <SettingsSection
              key={sectionId}
              titleKey={SECTION_TITLES[sectionId]}
              /*
                F-47: the small-caps heading only where it separates two things.
                Under a SEARCH it always shows, because the tabs are suspended
                and the heading is then the only thing saying where a hit lives.
              */
              showTitle={search.isFiltering || visibleSections.length > 1}
            >
              {sectionId === "general" && (
                <GeneralSection prefs={prefs} showRow={showRow} />
              )}
              {sectionId === "inbox" && (
                <InboxSection
                  prefs={prefs}
                  showRow={showRow}
                  isSearchHit={isSearchHit}
                  onOpenQuickSettings={onOpenQuickSettings}
                />
              )}
              {sectionId === "account" && (
                <AccountSection
                  identity={identity}
                  onSaveSignature={onSaveSignature}
                  showRow={showRow}
                  addresses={addresses}
                  quota={quota}
                  prefs={prefs}
                />
              )}
              {sectionId === "labels" && showRow("labels") && labels !== undefined && (
                <LabelsSection {...labels} />
              )}
              {/*
                E6. Each section renders itself when the server offers the
                capability, and its skeleton when it does not — the skeleton
                now says "this server does not offer X", which is the true
                sentence once the feature exists in the app.
              */}
              {sectionId === "filters" &&
                showRow("filters") &&
                (filters !== undefined ? (
                  <FiltersSection {...filters} />
                ) : (
                  <Skeleton
                    titleKey="settings.filters.soon"
                    bodyKey="settings.filters.soonBody"
                  />
                ))}
              {sectionId === "blocked" &&
                showRow("blocked") &&
                (blocked !== undefined ? (
                  <BlockedSection {...blocked} />
                ) : (
                  <Skeleton
                    titleKey="settings.filters.soon"
                    bodyKey="settings.filters.soonBody"
                  />
                ))}
              {sectionId === "forwarding" &&
                showRow("forwarding") &&
                (forwarding !== undefined ? (
                  <ForwardingSection {...forwarding} />
                ) : (
                  <Skeleton
                    titleKey="settings.forwarding.soon"
                    bodyKey="settings.forwarding.soonBody"
                  />
                ))}
              {sectionId === "vacation" &&
                showRow("vacation") &&
                (vacation !== undefined ? (
                  <VacationSection {...vacation} />
                ) : (
                  <Skeleton
                    titleKey="settings.vacation.soon"
                    bodyKey="settings.vacation.soonBody"
                  />
                ))}
              {sectionId === "offline" && (
                <OfflineSection prefs={prefs} showRow={showRow} />
              )}
            </SettingsSection>
          ))
        )}
      </div>
    </div>
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
          {(save) => (
            <>
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
                  save(set("language", value === "auto" ? null : (value as "es" | "en")));
                }}
                options={[
                  { value: "auto", label: t("settings.language.auto") },
                  ...LANGUAGES.map((code) => ({
                    value: code,
                    label: code === "es" ? t("settings.language.es") : t("settings.language.en"),
                  })),
                ]}
              />
            </>
          )}
        </SettingRow>
      )}

      {showRow("undoSend") && (
        <SettingRow labelKey="settings.undoSend.label" descriptionKey="settings.undoSend.description">
          {(save) => (
            <>
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
                  save(set("undoSendSeconds", Number(value) as Prefs["undoSendSeconds"]));
                }}
                options={UNDO_SEND_SECONDS.map((seconds) => ({
                  value: String(seconds),
                  label: format("settings.undoSend.seconds", seconds),
                }))}
              />
            </>
          )}
        </SettingRow>
      )}

      {showRow("images") && (
        <SettingRow labelKey="settings.images.label" descriptionKey="settings.images.description">
          {(save) => (
            <>
              {/*
                F-26: two options, so radios with an inline explanation — Gmail's
                own shape. A collapsed select shows ONE of them, which is the wrong
                picture for a choice whose difficulty is telling two similar options
                apart.
              */}
              <OptionGroup<Prefs["imagesPolicy"]>
                legendKey="settings.images.label"
                showLegend={false}
                variant="inline"
                value={prefs.prefs.imagesPolicy}
                options={IMAGES_POLICIES}
                labelKey={(policy) => IMAGES_LABELS[policy]}
                describeKey={(policy) => IMAGES_NOTES[policy]}
                onChange={(policy) => {
                  save(set("imagesPolicy", policy));
                }}
              />
            </>
          )}
        </SettingRow>
      )}

      {showRow("conversationView") && (
        <SettingRow
          labelKey="settings.conversation.label"
          descriptionKey="settings.conversation.description"
        >
          {(save) => (
            <>
              <Switch
                label={t("settings.conversation.label")}
                checked={prefs.prefs.conversationView}
                onChange={(checked) => {
                  save(set("conversationView", checked));
                }}
              />
            </>
          )}
        </SettingRow>
      )}

      {showRow("hoverActions") && (
        <SettingRow labelKey="settings.hover.label" descriptionKey="settings.hover.description">
          {(save) => (
            <>
              <Switch
                label={t("settings.hover.label")}
                checked={prefs.prefs.hoverActions}
                onChange={(checked) => {
                  save(set("hoverActions", checked));
                }}
              />
            </>
          )}
        </SettingRow>
      )}

      {showRow("autoAdvance") && (
        <SettingRow
          labelKey="settings.autoAdvance.label"
          descriptionKey="settings.autoAdvance.description"
        >
          {(save) => (
            <>
              <OptionGroup<Prefs["autoAdvance"]>
                legendKey="settings.autoAdvance.label"
                showLegend={false}
                variant="inline"
                value={prefs.prefs.autoAdvance}
                options={AUTO_ADVANCE}
                labelKey={(mode) => AUTO_ADVANCE_LABELS[mode]}
                describeKey={(mode) => AUTO_ADVANCE_NOTES[mode]}
                onChange={(mode) => {
                  save(set("autoAdvance", mode));
                }}
              />
            </>
          )}
        </SettingRow>
      )}

      {showRow("keyboardShortcuts") && (
        <SettingRow labelKey="settings.keyboard.label" descriptionKey="settings.keyboard.description">
          {(save) => (
            <>
              <Switch
                label={t("settings.keyboard.label")}
                checked={prefs.prefs.keyboardShortcuts}
                onChange={(checked) => {
                  save(set("keyboardShortcuts", checked));
                }}
              />
            </>
          )}
        </SettingRow>
      )}

      {showRow("showSnippets") && (
        <SettingRow labelKey="settings.snippets.label" descriptionKey="settings.snippets.description">
          {(save) => (
            <>
              <Switch
                label={t("settings.snippets.label")}
                checked={prefs.prefs.showSnippets}
                onChange={(checked) => {
                  save(set("showSnippets", checked));
                }}
              />
            </>
          )}
        </SettingRow>
      )}

      {/*
        E7 / prefs v2: Gmail's "Show 'Send & Archive' button in reply".

        Ours defaults ON where Gmail's defaults off — a registered divergence
        taken after the fact, because the button already shipped visible and a
        default of false would REMOVE a control users already have.
      */}
      {showRow("sendAndArchive") && (
        <SettingRow
          labelKey="settings.sendAndArchive.label"
          descriptionKey="settings.sendAndArchive.description"
        >
          {(save) => (
            <>
              <Switch
                label={t("settings.sendAndArchive.label")}
                checked={prefs.prefs.sendAndArchive}
                onChange={(checked) => {
                  save(set("sendAndArchive", checked));
                }}
              />
            </>
          )}
        </SettingRow>
      )}

      {/*
        E5 v2: which reply is the default (canon §2.3). It moves the reader's
        primary button AND the `r` key together — the button the eye lands on
        and the key the hand reaches for must never disagree.
      */}
      {showRow("replyBehavior") && (
        <SettingRow
          labelKey="settings.replyBehavior.label"
          descriptionKey="settings.replyBehavior.description"
        >
          {(save) => (
            <>
              <OptionGroup<Prefs["defaultReplyBehavior"]>
                legendKey="settings.replyBehavior.label"
                showLegend={false}
                variant="inline"
                value={prefs.prefs.defaultReplyBehavior}
                options={REPLY_BEHAVIORS}
                labelKey={(behavior) => REPLY_BEHAVIOR_LABELS[behavior]}
                describeKey={(behavior) => REPLY_BEHAVIOR_NOTES[behavior]}
                onChange={(behavior) => {
                  save(set("defaultReplyBehavior", behavior));
                }}
              />
            </>
          )}
        </SettingRow>
      )}
    </>
  );
}

/**
 * "Recibidos" (canon 07 §5): what the inbox looks like and how it behaves.
 *
 * E12 moved the reading pane here from the deleted "appearance" section — where
 * an open message appears is a fact about the inbox, not about colour — and
 * moved theme and density OUT of the page entirely, to the quick panel. What
 * stands in their place is a POINTER, not a duplicate control: see
 * `QuickPanelPointer` on why a second live control over one preference is worse
 * than a sentence saying where the first one is.
 */
function InboxSection({
  prefs,
  showRow,
  isSearchHit,
  onOpenQuickSettings,
}: SectionProps & {
  /**
   * P0-7: true only when a SEARCH surfaced this row.
   *
   * Distinct from `showRow`, which is true both while browsing the tab and
   * while filtering. The quick-panel rows must appear in one case and not the
   * other, and folding that into `showRow` would have made every other row's
   * predicate carry a condition that is about two of them.
   */
  readonly isSearchHit: (id: string) => boolean;
  readonly onOpenQuickSettings?: (() => void) | undefined;
}): React.JSX.Element {
  const set = prefs.setPref;

  return (
    <>
      {/*
        F-34/F-35: the two rows Gmail illustrates, illustrated.

        Both were one-line `<select>`s on a tab that had NO previews at all,
        while the quick panel showed the same two settings as radios with
        pictures. The picture is not decoration here: "A la derecha de la
        lista" versus "Debajo" is understood in a glance that a sentence is
        not, and a collapsed select shows one of the three.

        Same component, same thumbnails, same label tables as the panel — see
        `OptionGroup` on why the reuse is the point rather than a saving.
      */}
      {showRow("inboxType") && (
        <SettingRow
          labelKey="settings.inboxType.label"
          descriptionKey="settings.inboxType.description"
        >
          {(save) => (
            <>
              <OptionGroup<Prefs["inboxType"]>
                legendKey="settings.inboxType.label"
                showLegend={false}
                value={prefs.prefs.inboxType}
                options={INBOX_TYPES}
                labelKey={(type) => INBOX_TYPE_LABELS[type]}
                onChange={(type) => {
                  save(set("inboxType", type));
                }}
                renderThumb={(type) => <InboxTypeThumb inboxType={type} />}
              />
            </>
          )}
        </SettingRow>
      )}

      {showRow("readingPane") && (
        <SettingRow
          labelKey="settings.readingPane.label"
          descriptionKey="settings.readingPane.description"
        >
          {(save) => (
            <>
              <OptionGroup<Prefs["readingPane"]>
                legendKey="settings.readingPane.label"
                showLegend={false}
                value={prefs.prefs.readingPane}
                options={READING_PANES}
                labelKey={(pane) => READING_PANE_LABELS[pane]}
                onChange={(pane) => {
                  save(set("readingPane", pane));
                }}
                renderThumb={(pane) => <ReadingPaneThumb pane={pane} />}
              />
            </>
          )}
        </SettingRow>
      )}

      {showRow("notifications") && <NotificationsRow prefs={prefs} />}

      {/*
        P0-7: theme and density are NOT rows on this tab.

        They were, as pointers reading "Abrir los ajustes rápidos" — two rows
        that looked like settings and did nothing but tell you where the real
        control was. The reasoning was sound (one live control per preference,
        and the quick panel is where the change is visible AS YOU MAKE IT) and
        the conclusion was not: Gmail never puts a row on a settings page that
        does not settle anything, and a user reading Recibidos top to bottom
        meets two dead ends before finding the four live controls.

        The registry entries STAY, and that is the point of the distinction:
        someone typing "densidad" into the settings search must find something.
        What they find now is a row that OPENS THE PANEL rather than an empty
        anchor — `isSearchHit` is true only while a search is filtering, so the
        pointer exists exactly where it is useful and nowhere else.
      */}
      {isSearchHit("theme") && (
        <SettingRow labelKey="theme.label" descriptionKey="settings.theme.description">
          <QuickPanelPointer onOpen={onOpenQuickSettings} />
        </SettingRow>
      )}
      {isSearchHit("density") && (
        <SettingRow
          labelKey="settings.density.label"
          descriptionKey="settings.density.description"
        >
          <QuickPanelPointer onOpen={onOpenQuickSettings} />
        </SettingRow>
      )}
    </>
  );
}

/**
 * What the page renders where a quick-panel control would be.
 *
 * # Why a pointer rather than the control itself
 *
 * Theme and density belong in the quick panel because that is the only surface
 * where the change is visible AS YOU MAKE IT — the whole reason Gmail keeps
 * them there. Rendering them here as well would put two live controls over one
 * preference. They would not disagree (both write `PrefsProvider`), but the
 * user would have no way to know that, and the pair invites exactly the "I
 * changed it and it changed back" report that comes from changing one, going to
 * the other, and seeing a stale render.
 *
 * # Why the row exists at all — and only under a SEARCH (P0-7)
 *
 * It used to render whenever the Recibidos tab did, so a user reading the tab
 * top to bottom met two rows that looked like settings and settled nothing.
 * Gmail never does that, and the review called them dead rows.
 *
 * Deleting them outright would have been the other mistake: the registry still
 * lists both so the settings search (D-5) can find "densidad" and "tema", and
 * a search result leading to an empty anchor looks like a bug in the search.
 *
 * So they render exactly when a search surfaced them (`isSearchHit`), and what
 * they render is a way IN to the panel — not a note about where to look. The
 * button degrades to plain text when no opener was wired, rather than
 * rendering a control that leads nowhere.
 */
function QuickPanelPointer({
  onOpen,
}: {
  readonly onOpen?: (() => void) | undefined;
}): React.JSX.Element {
  const { t } = useTranslation();
  return (
    <div className={styles.pointer}>
      <span className={styles.pointerNote}>{t("settings.inQuickPanel")}</span>
      {onOpen !== undefined && (
        <button type="button" className={styles.pointerButton} onClick={onOpen}>
          {t("settings.openQuickPanel")}
        </button>
      )}
    </div>
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
  addresses,
  quota,
  prefs,
}: {
  readonly identity: Identity | undefined;
  readonly onSaveSignature: ((textSignature: string) => Promise<boolean>) | undefined;
  readonly showRow: (id: string) => boolean;
  readonly addresses: AddressSettings | undefined;
  readonly quota: QuotaRowProps | undefined;
  /** prefs v2: the named signatures row lives in this section. */
  readonly prefs: PrefsApi;
}): React.JSX.Element {
  const { t, format } = useTranslation();
  const [draft, setDraft] = useState(identity?.textSignature ?? "");
  const [state, setState] = useState<"idle" | "saving" | "saved" | "failed">("idle");
  const fieldId = useId();
  /** E11: replaces the `window.confirm` that guarded clearing the index. */
  const { confirm, dialog: confirmDialog } = useConfirm();

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
      {confirmDialog}
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

      {showRow("signatures") && <SignaturesRow prefs={prefs} confirm={confirm} />}

      {/*
        E7: the address-autocomplete opt-out (canon §2.3).

        Three controls in one row because they are one decision: whether
        addresses are collected, and what to do with the ones already here. An
        opt-out that only stops FUTURE collection while silently keeping the
        existing index is a setting about display dressed up as a privacy
        control, so "delete saved addresses" sits beside the switch rather than
        somewhere else.
      */}
      {showRow("addressAutocomplete") && addresses !== undefined && (
        <SettingRow
          labelKey="settings.addressAutocomplete.label"
          descriptionKey="settings.addressAutocomplete.description"
        >
          <div className={styles.signatureField}>
            <Switch
              label={
                addresses.enabled
                  ? t("settings.addressAutocomplete.on")
                  : t("settings.addressAutocomplete.off")
              }
              checked={addresses.enabled}
              onChange={addresses.setEnabled}
            />
            <div className={styles.signatureActions}>
              <span className={styles.signatureNote}>
                {format("settings.addressAutocomplete.count", addresses.count)}
              </span>
              <button
                type="button"
                className={styles.signatureSave}
                disabled={addresses.count === 0}
                onClick={() => {
                  // E11: our own confirm, like every other one in the app.
                  void (async () => {
                    if (
                      !(await confirm({
                        message: t("settings.addressAutocomplete.clearConfirm"),
                        destructive: true,
                      }))
                    ) {
                      return;
                    }
                    void addresses.clear();
                  })();
                }}
              >
                {t("settings.addressAutocomplete.clear")}
              </button>
            </div>
            {/*
              The "this browser only" caveat is GONE, and its string with it:
              prefs v2 carries `addressAutocomplete`, so the choice roams. What
              remains true and stays said — in this row's own description — is
              that the INDEX is browser-local and never uploaded. That sentence
              is about the data, not about the setting, and removing it would
              have been the dishonest half of this change.
            */}
          </div>
        </SettingRow>
      )}

      {/*
        E6: the storage bar (RFC 9425). Absent when the server has no quota
        capability — the row disappears rather than showing an empty bar, which
        is the same rule the autocomplete row above follows.
      */}
      {showRow("quota") && quota !== undefined && (
        <SettingRow labelKey="quota.label" descriptionKey="quota.description">
          <QuotaRow {...quota} />
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
/**
 * The named signatures (E7, prefs v2 `signatures`).
 *
 * # What this UI does and does not do, stated so the gap is not silent
 *
 * It creates, renames, edits and deletes named signatures as PLAIN TEXT, and
 * picks which one new mail and replies start with. It does NOT edit `htmlBody`
 * — there is no rich editor here — and that is a real limitation with a real
 * consequence a user can hit: a signature whose HTML was set by another client
 * shows its plain-text form in this box.
 *
 * The important half is what happens on save: `htmlBody` is CARRIED THROUGH
 * untouched, never blanked. Writing an empty string would silently destroy
 * formatting the user never asked to remove, which is the same class of failure
 * as a dropped preference — a control that appears to edit one thing and
 * quietly discards another. `settings.signatures.textOnly` says this on screen.
 *
 * # Why every mutation sends the whole `signatures` object
 *
 * RFC 8620 §5.3's patch pointers are one level deep, so `signatures/items` is
 * addressable but `signatures/items/work` is not (the server refuses it as
 * `invalidPatch`, and says so). More importantly, `forNew` and the item it
 * names must move together: creating a signature and selecting it as two saves
 * would put a dangling reference on the wire in between, which the server
 * correctly refuses. One object, one save, no intermediate state.
 */
function SignaturesRow({
  prefs,
  confirm,
}: {
  readonly prefs: PrefsApi;
  /*
   * The confirmer is PASSED IN rather than created here. `useConfirm` returns a
   * dialog element that its caller must render, and `AccountSection` already
   * mounts one — a second would put two <dialog>s in the same section competing
   * for the top layer, which is how a confirmation ends up behind the sheet it
   * was opened from.
   */
  readonly confirm: ReturnType<typeof useConfirm>["confirm"];
}): React.JSX.Element {
  const { t, format } = useTranslation();
  const signatures = prefs.prefs.signatures;
  const entries = Object.entries(signatures.items);
  const isFull = entries.length >= MAX_SIGNATURE_ITEMS;

  const write = (next: Prefs["signatures"]): void => {
    void prefs.setPref("signatures", next);
  };

  const updateItem = (id: string, patch: Partial<SignatureItem>): void => {
    const current = signatures.items[id];
    if (current === undefined) return;
    write({
      ...signatures,
      items: { ...signatures.items, [id]: { ...current, ...patch } },
    });
  };

  return (
    <SettingRow
      labelKey="settings.signatures.label"
      descriptionKey="settings.signatures.description"
    >
      <div className={styles.signatureField}>
        {entries.length === 0 ? (
          <span className={styles.signatureNote}>{t("settings.signatures.empty")}</span>
        ) : (
          entries.map(([id, item]) => (
            <div key={id} className={styles.signatureItem}>
              <input
                type="text"
                className={styles.signatureName}
                aria-label={t("settings.signatures.namePlaceholder")}
                placeholder={t("settings.signatures.namePlaceholder")}
                value={item.name}
                onChange={(event) => {
                  updateItem(id, { name: event.target.value });
                }}
              />
              <textarea
                className={styles.signatureInput}
                aria-label={`${t("settings.signatures.bodyPlaceholder")} — ${item.name}`}
                placeholder={t("settings.signatures.bodyPlaceholder")}
                value={item.textBody}
                onChange={(event) => {
                  // `htmlBody` is untouched by construction: `updateItem`
                  // spreads the current item, so formatting set elsewhere
                  // survives an edit here rather than being blanked.
                  updateItem(id, { textBody: event.target.value });
                }}
              />
              <button
                type="button"
                className={styles.signatureSave}
                onClick={() => {
                  void (async () => {
                    if (
                      !(await confirm({
                        message: format("settings.signatures.deleteConfirm", item.name),
                        destructive: true,
                      }))
                    ) {
                      return;
                    }
                    const { [id]: _removed, ...rest } = signatures.items;
                    /*
                     * The two references are cleared IN THE SAME object when
                     * they pointed at the deleted item. A dangling `forNew` is
                     * refused by the server — deliberately, because the
                     * fallback it would silently produce is a DIFFERENT
                     * signature going out under the user's name — so clearing
                     * here is not defensive, it is what makes the save legal.
                     */
                    write({
                      items: rest,
                      forNew: signatures.forNew === id ? null : signatures.forNew,
                      forReply: signatures.forReply === id ? null : signatures.forReply,
                    });
                  })();
                }}
              >
                {t("settings.signatures.delete")}
              </button>
            </div>
          ))
        )}

        <div className={styles.signatureActions}>
          <button
            type="button"
            className={styles.signatureSave}
            disabled={isFull}
            onClick={() => {
              /*
               * The id is opaque to the server (it validates only that it is
               * non-empty and under 64 bytes), so it is generated from the
               * clock — unique enough for a per-account map of at most ten, and
               * with no dependency on a UUID the bundle would have to carry.
               */
              const id = `sig-${String(Date.now())}`;
              write({
                ...signatures,
                items: {
                  ...signatures.items,
                  [id]: { name: t("settings.signatures.namePlaceholder"), textBody: "", htmlBody: "" },
                },
              });
            }}
          >
            {t("settings.signatures.add")}
          </button>
          {isFull && (
            <span className={styles.signatureNote}>
              {format("settings.signatures.full", MAX_SIGNATURE_ITEMS)}
            </span>
          )}
        </div>

        <div className={styles.signatureActions}>
          <Select
            label={t("settings.signatures.forNew")}
            value={signatures.forNew ?? ""}
            onChange={(value) => {
              write({ ...signatures, forNew: value === "" ? null : value });
            }}
            options={[
              { value: "", label: t("settings.signatures.none") },
              ...entries.map(([id, item]) => ({ value: id, label: item.name })),
            ]}
          />
          <Select
            label={t("settings.signatures.forReply")}
            value={signatures.forReply ?? ""}
            onChange={(value) => {
              write({ ...signatures, forReply: value === "" ? null : value });
            }}
            options={[
              { value: "", label: t("settings.signatures.none") },
              ...entries.map(([id, item]) => ({ value: id, label: item.name })),
            ]}
          />
        </div>

        <span className={styles.signatureNote}>{t("settings.signatures.textOnly")}</span>
      </div>
    </SettingRow>
  );
}

/**
 * The offline section (E9b, prefs v2 `offlineDepth`).
 *
 * It replaces the "coming with the next preferences release" skeleton, which was
 * a string with no render site at all — the gate found it defined in both
 * locales and shown nowhere, which is the honest-note pattern failing in the
 * quietest possible way. Two real controls are the fix; the string is deleted.
 */
function OfflineSection({ prefs, showRow }: SectionProps): React.JSX.Element {
  const { t } = useTranslation();
  const set = prefs.setPref;
  const depth = prefs.prefs.offlineDepth;

  return (
    <>
      {showRow("offlineHeaders") && (
        <SettingRow
          labelKey="settings.offlineHeaders.label"
          descriptionKey="settings.offlineHeaders.description"
        >
          {(save) => (
            <>
              <NumberField
                label={t("settings.offlineHeaders.label")}
                value={depth.headersPerMailbox}
                bounds={OFFLINE_DEPTH_BOUNDS.headersPerMailbox}
                onCommit={(next) => {
                  save(set("offlineDepth", { ...depth, headersPerMailbox: next }));
                }}
              />
            </>
          )}
        </SettingRow>
      )}

      {showRow("offlineBodies") && (
        <SettingRow
          labelKey="settings.offlineBodies.label"
          descriptionKey="settings.offlineBodies.description"
        >
          {(save) => (
            <>
              <div className={styles.signatureField}>
                <NumberField
                  label={t("settings.offlineBodies.label")}
                  value={depth.bodies}
                  bounds={OFFLINE_DEPTH_BOUNDS.bodies}
                  onCommit={(next) => {
                    save(set("offlineDepth", { ...depth, bodies: next }));
                  }}
                />
                {/*
                  The limitation Gmail declares too, said NEXT TO THE NUMBER rather
                  than in a doc: without it a high depth reads as "everything is
                  available offline", and the first missing attachment on a train
                  reads as a bug.
                */}
                <span className={styles.signatureNote}>
                  {t("settings.offlineDepth.attachments")}
                </span>
              </div>
            </>
          )}
        </SettingRow>
      )}
    </>
  );
}

/**
 * A whole number constrained to an inclusive range.
 *
 * # Why it commits on blur rather than on every keystroke
 *
 * Every other control here saves on the gesture, because the gesture IS the
 * decision — a switch has two states and picking one is picking it. A number is
 * typed, and typing "500" passes through "5" and "50", both of which are valid
 * values the optimistic save would happily persist and push to every other
 * device. Committing on blur (and on Enter) makes the decision the moment the
 * user is done, which is what a select's `change` already means for the others.
 *
 * # Why an out-of-range value is refused HERE and not by the server
 *
 * It is refused by both. The server's `prefsPatchBoundedInt` is the real
 * enforcement and cannot be bypassed; this check exists so the user is stopped
 * AT the boundary with the range in front of them, rather than after a round
 * trip that returns an error in a settings screen they have already moved on
 * from. The bounds are mirrored from the server's own constants
 * ({@link OFFLINE_DEPTH_BOUNDS}), which the account capability also advertises.
 *
 * An invalid entry reverts to the stored value on commit rather than being
 * held: a text box that refuses to lose focus is a trap, and a red box left
 * behind after the sheet closes is a change the user thinks they made.
 */
function NumberField({
  label,
  value,
  bounds,
  onCommit,
}: {
  readonly label: string;
  readonly value: number;
  readonly bounds: { readonly min: number; readonly max: number };
  readonly onCommit: (value: number) => void;
}): React.JSX.Element {
  const { format } = useTranslation();
  const [draft, setDraft] = useState(String(value));
  const [invalid, setInvalid] = useState(false);

  /*
   * The stored value wins whenever it changes underneath us — another tab's
   * save, or the server clamping ours. Without this the box would keep showing
   * what was typed after the provider adopted a different answer.
   */
  useEffect(() => {
    setDraft(String(value));
    setInvalid(false);
  }, [value]);

  const commit = (): void => {
    const parsed = Number(draft.trim());
    if (
      draft.trim() === "" ||
      !Number.isInteger(parsed) ||
      parsed < bounds.min ||
      parsed > bounds.max
    ) {
      setInvalid(true);
      setDraft(String(value));
      return;
    }
    setInvalid(false);
    if (parsed !== value) onCommit(parsed);
  };

  return (
    <span className={styles.numberField}>
      <input
        type="number"
        inputMode="numeric"
        className={styles.numberInput}
        aria-label={label}
        // The native constraints too, so the browser's own stepper and its
        // validity state agree with the check above instead of offering values
        // `commit` would then reject.
        min={bounds.min}
        max={bounds.max}
        step={1}
        aria-invalid={invalid || undefined}
        value={draft}
        onChange={(event) => {
          setDraft(event.target.value);
          setInvalid(false);
        }}
        onBlur={commit}
        onKeyDown={(event) => {
          if (event.key === "Enter") {
            event.preventDefault();
            commit();
          }
        }}
      />
      <span className={styles.signatureNote} role={invalid ? "alert" : undefined}>
        {invalid
          ? format("settings.offlineDepth.invalid", bounds.min, bounds.max)
          : format("settings.offlineDepth.range", bounds.min, bounds.max)}
      </span>
    </span>
  );
}

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

/**
 * A titled group of settings rows.
 *
 * # F-47: the heading is HIDDEN when the tab holds only one section
 *
 * Standing on "General" showed the word twice — once as the selected tab, and
 * again 40px below in small caps as the section's own heading. Gmail does not,
 * and it does not because the two are the same fact: with one section per tab,
 * the tab IS the heading. The duplicate cost a line of vertical space at the
 * top of six of the seven tabs and made the page look like it had a header
 * nobody needed.
 *
 * It is hidden rather than DELETED, and that distinction is the whole care in
 * this change: the `<section>` is named by this heading, so removing the
 * element would leave an unnamed landmark and a screen-reader user with no way
 * to tell one group from the next. `visually-hidden` keeps the name in the
 * accessibility tree and takes it off the screen.
 *
 * On the one tab that genuinely holds two sections — Filtros, with FILTROS and
 * BLOQUEADOS — the headings render, because there the small caps are doing real
 * work: they are the only thing separating two lists of different objects.
 */
function SettingsSection({
  titleKey,
  showTitle,
  children,
}: {
  readonly titleKey: PlainStringKey;
  /** False collapses the heading to its accessible name only (F-47). */
  readonly showTitle: boolean;
  readonly children: React.ReactNode;
}): React.JSX.Element {
  const { t } = useTranslation();
  return (
    /*
     * A <section> named by its own heading, so a screen reader user can jump
     * between groups instead of walking every row of a long sheet.
     */
    <section className={styles.section} aria-labelledby={`settings-${titleKey}`}>
      <h3
        className={showTitle ? styles.sectionTitle : "visually-hidden"}
        id={`settings-${titleKey}`}
      >
        {t(titleKey)}
      </h3>
      <div className={styles.rows}>{children}</div>
    </section>
  );
}

/**
 * One setting: its NAME in a fixed ~340px left column, its control on the right
 * (canon 07 §5).
 *
 * E12 turned this from a flex row into a two-column grid, and that is the whole
 * visual difference between this page and the sheet it replaces. With
 * `justify-content: space-between`, every control sat at a different x
 * depending on how long its label happened to be; a fixed name column lines
 * them all up so the eye can scan down them.
 *
 * The label is NOT a <label> element and does not point at the control. Some
 * controls here are single inputs (a switch) and some are groups (the offline
 * depths, the signature editor) carrying their own fieldset/legend; a <label>
 * can only name the first kind, and pointing one at a fieldset produces a name
 * that screen readers announce inconsistently. So each control stays
 * responsible for its own accessible name, and this text is the VISIBLE heading
 * of the row.
 */
function SettingRow({
  labelKey,
  descriptionKey,
  children,
}: {
  readonly labelKey: PlainStringKey;
  readonly descriptionKey?: PlainStringKey;
  /**
   * The control, or a function receiving this row's {@link SaveReporter}.
   *
   * A render prop rather than a context, and rather than a prop on every
   * control: the tick belongs to THE ROW, and only the row knows where to draw
   * it. A context would have made every control reach for an ambient value
   * that is meaningless outside a row; a prop threaded through `Select`,
   * `Switch`, `NumberField` and `OptionGroup` would have put settings-page
   * concerns inside four generic controls. Rows that write nothing (the
   * identity display) pass a plain node and cost nothing.
   */
  readonly children: React.ReactNode | ((save: SaveReporter) => React.ReactNode);
}): React.JSX.Element {
  const { t } = useTranslation();
  const { isSaved, report } = useSaveFeedback();
  return (
    <div className={styles.row}>
      <div className={styles.rowText}>
        <span className={styles.rowLabel}>{t(labelKey)}</span>
        {descriptionKey !== undefined && (
          /*
            F-19: the description is clipped to one line by CSS, so the full
            sentence rides along as a `title`. The text NODE is complete either
            way — a screen reader reads the node, not the box — so this is for
            the sighted reader whose row happens to have long prose.
          */
          <span className={styles.rowDescription} title={t(descriptionKey)}>
            {t(descriptionKey)}
          </span>
        )}
      </div>
      <div className={styles.rowControl}>
        {typeof children === "function" ? children(report) : children}
        {/*
          F-38: the receipt autosave was missing.

          `role="status"` rather than `alert`: this is a confirmation of
          something the user just did and is looking straight at, so it should
          be announced politely at the end of what the screen reader is saying
          — not interrupt it. It is rendered only while true, so the live region
          announces on APPEARANCE rather than sitting empty and announcing a
          removal two seconds later.
        */}
        {isSaved && (
          <span className={styles.saved} role="status">
            {t("settings.saved")}
          </span>
        )}
      </div>
    </div>
  );
}

/** What a row hands its control so a successful write can be confirmed (F-38). */
export type SaveReporter = (save: Promise<boolean>) => void;
