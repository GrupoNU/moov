/**
 * The string table.
 *
 * # Why a table and not literals in components
 *
 * The pilot's language is Spanish and the product's language is English, and
 * both are true at once: the code, comments and identifiers are English (the
 * project convention), while Diego's pilot users read Spanish. Hardcoding
 * either one into components would force a rewrite to serve the other.
 *
 * So every user-visible string in the app comes from here, keyed by a
 * TypeScript-checked identifier. The shape is a flat record rather than a
 * nested one because a flat key ("login.submit") greps cleanly and cannot be
 * partially overridden by accident.
 *
 * # The completeness guarantee
 *
 * `Strings` is derived from the English table, so every other locale must
 * supply EXACTLY the same keys — a missing translation is a compile error, not
 * a key rendered raw on screen. This is the mechanism that makes "i18n-ready"
 * a property the type checker enforces rather than a promise.
 *
 * # Interpolation
 *
 * Values are either plain strings or functions of their parameters. A function
 * keeps word ORDER a property of the translation: "wait 30 seconds" and
 * "esperá 30 segundos" put the number in different places relative to the
 * verb, and a template with positional holes cannot express that.
 *
 * # The brand name is a parameter, never a literal
 *
 * The product is installed under the host's own name: on mail.areacorp.com.ar
 * it is "Área Mail", not Moov. So every string that names the product takes
 * the brand as its FIRST and ONLY parameter, `(brand: string) => string`, and
 * `useTranslation` binds it — call sites still write `t("filters.activate")`
 * and get "Activar las reglas de Área Mail". Threading the brand through every
 * call site instead would put the same `useBranding()` line in forty
 * components and make forgetting it the default failure.
 *
 * That includes strings that describe the SOFTWARE rather than the brand
 * ("Moov never deletes a Sieve script it did not write"). The user is talking
 * to the product in front of them; which engine is behind it is not their
 * concern, and naming an unfamiliar one mid-sentence reads as a bug.
 *
 * The exceptions are deliberate and pinned by the ALLOWED allowlist in
 * `strings.test.ts`: `legal.poweredBy` is an attribution, and an attribution
 * that renamed itself per host would be worthless.
 *
 * Spanish must stay grammatical for ANY name. "las reglas de ${brand}" works
 * for every brand; "el ${brand}" or "la ${brand} nueva" would not, because the
 * name's gender and number are unknowable. Prefer constructions where the name
 * is a bare apposition or follows "de".
 */

/** The English strings — the source of truth for the key set. */
export const en = {
  // --- application chrome ---
  "app.skipToContent": "Skip to main content",
  "app.loading": "Loading…",

  // --- login screen ---
  "login.heading": "Sign in",
  "login.subheading": "Use your full email address and its password.",
  "login.emailLabel": "Email address",
  "login.emailPlaceholder": "you@example.com",
  "login.passwordLabel": "Password",
  "login.submit": "Sign in",
  "login.submitting": "Signing in…",
  "login.showPassword": "Show password",
  "login.hidePassword": "Hide password",
  "login.passwordShown": "Password is showing",
  "login.passwordHidden": "Password is hidden",
  "login.needHelp": "Need help?",
  "login.contactAdministrator": "Contact your administrator",

  // Client-side validation. These fire before any request, so they must be
  // about the FORM, never about the credentials.
  "login.error.emailRequired": "Enter your email address.",
  "login.error.emailInvalid": "Enter a complete email address, including the domain.",
  "login.error.passwordRequired": "Enter your password.",

  // --- the error taxonomy (the pilot's lesson) ---
  //
  // Each of these corresponds to one ApiErrorKind. The rule every one of them
  // follows: say what happened, then say what to do. "An error occurred" is
  // exactly what this table exists to make impossible.
  "error.invalidCredentials.title": "That email and password did not match",
  "error.invalidCredentials.body":
    "Check the address and password and try again. Use the password for this mailbox, not the one for another service.",

  "error.notProvisioned.title": (brand: BrandName): string =>
    `This mailbox is not set up in ${brand} yet`,
  "error.notProvisioned.body": (brand: BrandName): string =>
    `Your password was correct, but this mailbox has not been added to ${brand}. An administrator has to enable it before you can sign in.`,

  "error.rateLimited.title": "Too many attempts",
  "error.rateLimited.body": "Wait a moment before trying again.",
  "error.rateLimited.bodyWithSeconds": (seconds: number): string =>
    `Wait about ${seconds} second${seconds === 1 ? "" : "s"} before trying again.`,

  "error.serverError.title": (brand: BrandName): string =>
    `${brand} is not answering right now`,
  "error.serverError.body":
    "The server is reachable but could not complete the request. This is not a problem with your account — try again shortly.",

  "error.network.title": "Could not reach the server",
  "error.network.body":
    "Check your connection. If you are on a VPN or a company network, confirm it is connected.",

  "error.unknown.title": "Something went wrong",
  "error.unknown.body": "Try again. If it keeps happening, contact your administrator.",

  // --- the authenticated shell (P1 lands here; P2 fills it) ---
  "shell.mailboxes": "Mailboxes",
  "shell.signOut": "Sign out",
  "shell.signedInAs": (email: string): string => `Signed in as ${email}`,

  // --- E12: the top bar (canon 07 §1) ---
  //
  // The hamburger says what it will DO, not what the rail currently is —
  // "Collapse the folder list" on an expanded rail — because a toggle labelled
  // with its state reads as a claim rather than as an offer.
  "shell.collapseSidebar": "Collapse the folder list",
  "shell.expandSidebar": "Expand the folder list",
  "shell.accountMenu": "Account",
  "shell.help": "Support and keyboard shortcuts",

  // --- E12: quick settings, the gear's docked panel (canon 07 §4) ---
  "quickSettings.title": "Quick settings",
  "quickSettings.seeAll": "See all settings",
  "quickSettings.close": "Close quick settings",

  // --- E12/B4: the list toolbar (canon 07 §3) ---
  //
  // The six scopes of Gmail's select-all dropdown. They are the same six the
  // `* a`/`* n`/`* r`/`* u`/`* s`/`* t` chords already resolve to (canon §2.4),
  // wired to the same reducer — the menu is a second surface over one
  // mechanism, not a second implementation of it.
  "action.selectMenu": "Selection options",
  "action.select.all": "All",
  "action.select.none": "None",
  "action.select.read": "Read",
  "action.select.unread": "Unread",
  "action.select.starred": "Starred",
  "action.select.unstarred": "Unstarred",
  "list.refresh": "Refresh",
  /*
   * The pager. Two shapes because the server's `total` is exact or ABSENT,
   * never an estimate — see `mail/paging.ts`.
   *
   * The locale tag is EXPLICIT ("en" here, "es" in the Spanish table) and not
   * a bare `toLocaleString()`. A bare call follows the ambient environment,
   * which means the English string renders "15.224" on a machine set to
   * Spanish — the wrong thousands separator for the language actually on
   * screen. A test caught exactly that. The string table is the locale, so the
   * separator belongs to the string rather than to the host.
   */
  "list.page.range": (first: number, last: number, total: number): string =>
    `${first.toLocaleString("en")}–${last.toLocaleString("en")} of ${total.toLocaleString("en")}`,
  "list.page.rangeUnknown": (first: number, last: number): string =>
    `${first.toLocaleString("en")}–${last.toLocaleString("en")}`,
  /*
   * B-01: the honest bound, for when the server declined to count.
   *
   * "of more than 50" rather than "of 50": the number is a FLOOR, and the
   * wording has to say so or it reads as a total the pager does not have. Gmail
   * writes the same sentence in the same situation.
   */
  "list.page.rangeAtLeast": (first: number, last: number, atLeast: number): string =>
    `${first.toLocaleString("en")}–${last.toLocaleString("en")} of more than ${atLeast.toLocaleString("en")}`,
  "list.page.newer": "Newer",
  "list.page.older": "Older",
  /*
   * B-05: the view-level `⋮`, which acts on the LIST rather than on a
   * selection — Gmail's own menu at the same spot.
   *
   * "Mark all as read" is bounded to the loaded page and the label says so.
   * Gmail's is bounded too (it acts on the conversations it has); ours cannot
   * honestly claim more, because the server counts a folder's contents only
   * when the count is cheap and this menu is not a place to start a job whose
   * size nobody knows.
   */
  "list.overflow.markPageRead": "Mark everything on this page as read",
  "list.overflow.selectMatching": "Select everything that matches",
  "list.overflow.refresh": "Refresh",
  // The row's star, which toggles the same `$flagged` keyword `s` does.
  "list.star": "Star",
  "list.unstar": "Remove star",

  // --- E12/B5: the pane divider (canon 07 §6) ---
  //
  // The value is spoken with its UNIT: `aria-valuenow` alone is announced as a
  // bare number, which for a splitter says nothing.
  "shell.resizePane": "Resize the reading pane",
  "shell.resizePaneValue": (pixels: number): string => `${String(pixels)} pixels`,

  // --- settings ---
  "settings.title": "Settings",
  "settings.open": "Settings",
  "settings.close": "Close",
  "settings.section.appearance": "Appearance",
  "settings.theme.description": (brand: BrandName): string =>
    `Choose how ${brand} looks, or follow your system.`,

  // --- settings: E5, the full surface ---
  "settings.search.label": "Search settings",
  "settings.search.placeholder": "Search settings",
  "settings.search.clear": "Clear search",
  "settings.search.empty": "No setting matches that",
  "settings.search.emptyBody": "Try another word, or clear the search to see every setting.",
  "settings.nav.label": "Settings sections",
  "settings.saving": "Saving…",
  "settings.unavailable":
    "This server does not store preferences. Your choices apply to this session only.",
  /* F-38: the receipt the autosave was missing — beside the row that saved,
     for two seconds. Not a toast: a toast for a setting the user is looking
     straight at appears somewhere else on the screen to say so. */
  "settings.saved": "Saved ✓",
  "settings.saveFailed": "The preference could not be saved",

  "settings.section.general": "General",
  "settings.section.inbox": "Inbox",
  "settings.section.account": "Account",
  "settings.section.filters": "Filters",
  /* E6: its own section, Gmail's own shape (Filters and blocked addresses). */
  "settings.section.blocked": "Blocked",
  "settings.section.forwarding": "Forwarding",
  "settings.section.vacation": "Vacation responder",
  "settings.section.offline": "Offline",

  // --- E12: the settings PAGE (canon 07 §5) ---
  //
  // `settings.tab.filters` is the FOLDED name — the tab holds both the filter
  // list and the blocked senders, and a tab called just "Filters" is a tab
  // nobody looks in for a blocked address.
  "settings.tab.filters": "Filters and blocked addresses",
  "settings.backToMail": "Back to mail",
  "settings.tabs.label": "Settings sections",
  // The pointer the page renders where a quick-panel control would be, so
  // someone who searched for "density" here is told where it lives rather than
  // finding nothing.
  "settings.inQuickPanel": "Choose this in quick settings, where you can see the change as you make it.",
  "settings.openQuickPanel": "Open quick settings",

  "settings.language.label": "Language",
  "settings.language.description": (brand: BrandName): string =>
    `The language the ${brand} interface is written in.`,
  "settings.language.auto": "Match my browser",
  "settings.language.es": "Español",
  "settings.language.en": "English",

  "settings.undoSend.label": "Undo send",
  "settings.undoSend.description":
    "How long a sent message waits before it actually leaves.",
  "settings.undoSend.seconds": (seconds: number): string => `${seconds} seconds`,

  "settings.images.label": "Remote images",
  "settings.images.description": (brand: BrandName): string =>
    `Images always load through the ${brand} proxy, so the sender never learns you opened the message. Spam is always excluded.`,
  "settings.images.always": "Always show",
  "settings.images.ask": "Ask before showing",
  /*
   * F-26: Gmail puts a one-line explanation beside each radio, and it is what
   * makes the radios worth more than the select they replace — the sentence
   * answers "what does this one do" AT the moment of comparison.
   */
  "settings.images.alwaysNote": "Every message renders its images through the proxy.",
  "settings.images.askNote": "A message with images shows a bar you can accept.",

  "settings.conversation.label": "Conversation view",
  // E1 landed the behavior this once promised for "this release": the list
  // groups by conversation and the reader shows the whole thread. A setting
  // that still disclaimed a shipped feature would teach users not to trust it.
  "settings.conversation.description":
    "Group replies into a single conversation. The list shows one row per conversation, and opening one shows every message in it.",
  "settings.hover.label": "Hover actions",
  "settings.hover.description": "Show archive, delete and read buttons on a row when you point at it.",
  "settings.autoAdvance.label": "Auto-advance",
  "settings.autoAdvance.description": "Where to land after you archive or delete a message.",
  "settings.autoAdvance.list": "Back to the list",
  "settings.autoAdvance.newer": "Newer message",
  "settings.autoAdvance.older": "Older message",
  "settings.autoAdvance.listNote": "The reader closes and the list keeps the cursor.",
  "settings.autoAdvance.newerNote": "Opens the message above the one you just cleared.",
  "settings.autoAdvance.olderNote": "Opens the message below — the way an inbox drains.",

  "settings.keyboard.label": "Keyboard shortcuts",
  "settings.keyboard.description":
    "Gmail's key vocabulary. Search (/) and Escape keep working either way.",
  "settings.snippets.label": "Snippets",
  "settings.snippets.description": "Show the first line of each message next to its subject.",

  "settings.density.label": "Density",
  "settings.density.description": "How much room each row takes.",
  "settings.density.default": "Default",
  "settings.density.comfortable": "Comfortable",
  "settings.density.compact": "Compact",

  "settings.readingPane.label": "Reading pane",
  "settings.readingPane.description": "Where an open message appears.",
  "settings.readingPane.none": "No split",
  "settings.readingPane.right": "Right of the list",
  "settings.readingPane.bottom": "Below the list",

  "settings.inboxType.label": "Inbox type",
  "settings.inboxType.description": "What sorts to the top of your inbox.",
  "settings.inboxType.default": "Default",
  "settings.inboxType.unread_first": "Unread first",
  "settings.inboxType.starred_first": "Starred first",

  "settings.notifications.label": "Desktop notifications",
  "settings.notifications.description": (brand: BrandName): string =>
    `${brand} notifies while this tab is open, like Gmail on the web does.`,
  "settings.notifications.new": "New mail",
  "settings.notifications.off": "Off",
  "settings.notifications.granted": "Your browser allows notifications.",
  "settings.notifications.denied":
    "Your browser is blocking notifications for this site. Allow them in the address bar to turn this on.",
  "settings.notifications.pending": "Your browser will ask for permission.",
  "settings.notifications.unsupported": "This browser cannot show notifications.",
  /*
   * E9b: the sender name a notification falls back to when the message carries
   * no From at all. Rare, and it must never render as an empty title bar.
   */
  "notification.unknownSender": "Unknown sender",
  "notification.noSubject": "(no subject)",

  // --- E9b: connection honesty (the pill above the list) ---
  //
  // Two states, worded to be actionable rather than alarming. "Offline" says
  // what the user is seeing (saved mail); "reconnecting" promises what is
  // happening, which is true — the stream heals itself.
  "connection.offline": "No connection — showing saved mail",
  "connection.reconnecting": "Reconnecting…",

  // --- E9b: offline ---
  "offline.banner.stale":
    "You are offline. This is the mail saved on this device, and it may not be up to date.",
  "offline.search.label": (count: number): string =>
    `${String(count)} result${count === 1 ? "" : "s"} in saved mail. Offline search covers only what is stored on this device.`,
  "offline.body.unavailable": "This message was not saved for offline reading",
  "offline.body.unavailableBody":
    "Only the messages you opened while online are stored on this device. It will be here as soon as you reconnect.",
  "offline.attachments.unavailable":
    "Attachments cannot be opened offline — they are not stored on this device.",
  "offline.empty.title": "No saved mail on this device",
  "offline.empty.body":
    "You are offline and nothing has been stored yet. Connect once and the mail you read will be available here.",

  // --- E9b: the Outbox ---
  "outbox.name": "Outbox",
  "outbox.queued": "Waiting to send",
  "outbox.sending": "Sending…",
  "outbox.failed": "Could not be sent",
  "outbox.retry": "Try again",
  "outbox.discard": "Discard",
  "outbox.queuedToast":
    "No connection — the message is in your Outbox and will go out when you reconnect.",
  "outbox.queueFailed":
    "The message could not be stored on this device. Copy your text before closing this window.",
  "outbox.sentToast": (count: number): string =>
    `${String(count)} message${count === 1 ? "" : "s"} from the Outbox sent`,
  "outbox.empty": "Nothing is waiting to be sent.",
  "outbox.explain":
    "These messages were written offline. They go out on their own as soon as there is a connection.",

  "settings.identity.label": "Sending address",
  "settings.identity.description": "The name and address your mail is sent from.",
  "settings.identity.missing": "No sending identity is configured for this account.",
  "settings.signature.label": "Signature",
  "settings.signature.description": "Appended to the messages you write.",
  "settings.signature.save": "Save signature",
  "settings.signature.saved": "Signature saved",
  "settings.signature.failed": "The signature could not be saved",

  // --- E7: the address autocomplete row (canon §2.3) ---
  "settings.addressAutocomplete.label": "Address autocomplete",
  /*
   * The description carries the privacy statement, not a tooltip: where the
   * index lives is the first thing someone deciding about this row wants to
   * know, and burying it costs the row its trustworthiness.
   */
  /*
   * Reworded for prefs v2: the CHOICE now roams, the INDEX still does not.
   *
   * The old sentence ran the two together ("it does not roam to your other
   * devices"), which was accurate when both were browser-local and became
   * misleading the moment the switch started roaming. Saying which of the two
   * travels is the whole point of the row's honesty — a user deciding about
   * this control wants to know where the addresses live, and separately whether
   * turning it off here turns it off everywhere. It does.
   */
  "settings.addressAutocomplete.description":
    "Addresses are saved as you send and read mail, and suggested when you write. The saved addresses live only in this browser and are never uploaded; this on/off choice is part of your account and applies on every device.",
  "settings.addressAutocomplete.on": "Save addresses automatically",
  /* Gmail's own wording for the opt-out (/contacts/answer/1069522). */
  "settings.addressAutocomplete.off": "I'll add contacts myself",
  "settings.addressAutocomplete.count": (count: number): string =>
    count === 1 ? "1 saved address" : `${count} saved addresses`,
  "settings.addressAutocomplete.clear": "Delete saved addresses",
  "settings.addressAutocomplete.clearConfirm":
    "Delete every address saved in this browser? Autocomplete starts over from nothing.",
  "settings.addressAutocomplete.cleared": "Saved addresses deleted",

  /*
   * The E6 skeletons survive for ONE case each: a server that does not
   * advertise the capability. They no longer say "arrives with the Sieve
   * epic" — that epic landed — they say what the SERVER is missing, which is
   * the honest sentence when the feature exists in the app and not on the
   * deployment.
   */
  "settings.filters.soon": "This server does not offer filters",
  "settings.filters.soonBody": (brand: BrandName): string =>
    `Filters are Sieve rules stored on the mail server. This deployment does not advertise the capability, so ${brand} has nothing to configure.`,
  "settings.forwarding.soon": "This server does not offer forwarding",
  "settings.forwarding.soonBody":
    "Forwarding and blocked senders are Sieve recipes. This deployment does not advertise the capability.",
  "settings.vacation.soon": "This server does not offer a vacation responder",
  "settings.vacation.soonBody":
    "The out-of-office reply is Dovecot's Sieve `vacation`. This deployment does not advertise the capability.",

  // --- E6: filters (GC-4) ---
  "filters.description": (brand: BrandName): string =>
    `Rules run on the mail server as it arrives, so they keep working when ${brand} is closed. They run IN ORDER, top to bottom.`,
  "filters.create": "Create a filter",
  /*
   * F-42. The note says whose format this is, in the one place a user is
   * deciding what to do with the file — naming it after Gmail's XML would send
   * them to Gmail with something Gmail cannot read.
   */
  "filters.export": "Export filters",
  "filters.import": "Import filters",
  "filters.transferNote": (brand: BrandName): string =>
    `The file is the ${brand} JSON format, not Gmail's XML: it round-trips with another ${brand} account and Gmail will not read it.`,
  "filters.imported": (count: number): string =>
    count === 1 ? "1 filter imported." : `${String(count)} filters imported.`,
  "filters.import.notJson": "That file is not JSON. Pick the file an export produced.",
  "filters.import.notOurFormat": (brand: BrandName): string =>
    `That file is not a ${brand} filter export. Gmail's XML export is not supported.`,
  "filters.import.futureVersion": (brand: BrandName): string =>
    `That file was written by a newer version of ${brand}. Update, then import it.`,
  "filters.import.noRules": "That export has no filters in it.",
  "filters.import.badRule":
    "One of the filters in that file is malformed, so none were imported.",
  "filters.edit": "Edit",
  "filters.delete": "Delete",
  "filters.none": "No filters yet.",
  "filters.deleteConfirm": (name: string): string =>
    `Delete the filter “${name}”? Mail already filed stays where it is; new mail stops being filtered.`,
  "filters.enabled": "Active",
  "filters.disabled": "Paused",
  "filters.moveUp": "Move up",
  "filters.moveDown": "Move down",
  "filters.unnamed": "(unnamed filter)",
  "filters.order": (position: number, total: number): string =>
    `Rule ${position} of ${total}`,
  "filters.criteria": "When",
  "filters.actions": "Then",
  "filters.empty": "—",
  "filters.saving": "Saving…",
  "filters.saveFailed": "The filter could not be saved",
  "filters.loadFailed": "The filters could not be loaded",

  // The scriptActive banner — the honesty bit, in words.
  "filters.foreignScript": (brand: BrandName): string =>
    `The ${brand} rules are not running`,
  "filters.foreignScriptBody": (brand: BrandName): string =>
    `Another Sieve script is active on the server, so these rules exist but do not filter your mail. ${brand} never deletes a script it did not write — activating the ${brand} rules leaves the other one stored, just no longer the active one.`,
  "filters.activate": (brand: BrandName): string => `Activate the ${brand} rules`,
  "filters.activating": "Activating…",
  "filters.activateFailed": "The rules could not be activated",
  "filters.activated": (brand: BrandName): string => `The ${brand} rules are active`,

  // The builder.
  "filters.builder.newTitle": "New filter",
  "filters.builder.editTitle": "Edit filter",
  "filters.builder.name": "Name",
  "filters.builder.namePlaceholder": "Invoices",
  "filters.builder.criteriaLegend": "When a message arrives that matches",
  "filters.builder.actionsLegend": "Do this",
  "filters.builder.from": "From",
  "filters.builder.to": "To or Cc",
  "filters.builder.subject": "Subject",
  "filters.builder.sizeOver": "Larger than",
  "filters.builder.sizeUnder": "Smaller than",
  "filters.builder.attachment": "Attachment",
  "filters.builder.attachmentAny": "Doesn't matter",
  "filters.builder.attachmentYes": "Has an attachment",
  "filters.builder.attachmentNo": "Has no attachment",
  "filters.builder.moveTo": "Move to folder",
  "filters.builder.moveToNone": "Leave in place",
  "filters.builder.labels": "Apply labels",
  "filters.builder.markRead": "Mark as read",
  "filters.builder.star": "Star it",
  "filters.builder.forward": "Forward to",
  "filters.builder.forwardNone": "Do not forward",
  "filters.builder.forwardHint":
    "Only verified addresses can receive forwarded mail. Add one under Forwarding first.",
  "filters.builder.delete": "Move to Trash",
  "filters.builder.neverSpam": "Never send to Spam",
  "filters.builder.stop": "Stop processing further rules",
  "filters.builder.save": "Save",
  "filters.builder.cancel": "Cancel",
  "filters.builder.noDateNote": (brand: BrandName): string =>
    `There is no date condition, and no free-text search condition: Sieve has no equivalent, so ${brand} restricts rather than pretends. Use search for those.`,
  "filters.builder.multiHint": "One per line.",

  // The builder's problems, mirrored from the server's own model.
  "filters.problem.noCriteria": "Add at least one condition.",
  "filters.problem.noActions": "Add at least one action.",
  "filters.problem.moveAndDelete": "A filter can move a message OR trash it, not both.",
  "filters.problem.forwardUnverified":
    "That forwarding address is not verified. Verify it under Forwarding first.",
  "filters.problem.forwardNotAddress": "That is not an email address.",
  "filters.problem.blockedNeedsAddress": "A blocked sender needs an address.",
  "filters.problem.blockedNotAddress": "That is not an email address.",
  "filters.problem.controlCharacters": "Remove the line breaks and control characters.",
  "filters.problem.negativeSize": "A size cannot be negative.",

  // --- E6: blocked senders (canon §2.2) ---
  "blocked.description":
    "Mail from these addresses goes straight to Spam. Blocking does not unsubscribe you from anything.",
  "blocked.none": "You have not blocked anyone.",
  "blocked.add": "Block an address",
  /* F-46: the FIELD is named for what goes in it. Repeating the button's own
     words above the box printed "Block an address" twice, one of which was not
     a button — which reads as a stray heading rather than as a label. */
  "blocked.addressLabel": "Address",
  "blocked.addPlaceholder": "sender@example.com",
  "blocked.remove": "Unblock",
  "blocked.removeConfirm": (address: string): string =>
    `Unblock ${address}? Their mail goes back to your inbox.`,
  "blocked.invalid": "Enter a complete email address.",
  "blocked.duplicate": "That address is already blocked.",
  "blocked.action": "Block sender",
  "blocked.dialogTitle": (address: string): string => `Block ${address}?`,
  "blocked.dialogBody":
    "All future mail from this address goes to Spam. Mail already in your inbox stays where it is.",
  "blocked.dialogUnsubscribe":
    "This message offers an unsubscribe link. If it is a newsletter you signed up for, unsubscribing is the cleaner fix — blocking does not unsubscribe.",
  "blocked.confirm": "Block",
  "blocked.blocked": (address: string): string => `${address} is blocked`,
  "blocked.failed": "The sender could not be blocked",

  // --- E6: vacation responder (canon §2.8) ---
  "vacation.description":
    "An automatic reply while you are away. It answers each sender at most once every four days, and never answers mailing lists or spam.",
  "vacation.enable": "Send an automatic reply",
  "vacation.from": "First day",
  "vacation.to": "Last day",
  "vacation.dateHint":
    "The reply starts at 00:00 and ends at 23:59 on those days, in your own timezone. Leave a field empty for no bound.",
  "vacation.subject": "Subject",
  "vacation.subjectPlaceholder": "Out of the office",
  "vacation.body": "Message",
  "vacation.save": "Save",
  "vacation.saving": "Saving…",
  "vacation.saved": "Vacation reply saved",
  "vacation.saveFailed": "The vacation reply could not be saved",
  "vacation.loadFailed": "The vacation reply could not be loaded",
  "vacation.htmlNote": (brand: BrandName): string =>
    `This responder has an HTML body set elsewhere. ${brand} edits the plain-text version and leaves the HTML untouched.`,
  "vacation.problem.endBeforeStart": "The last day is before the first day.",
  "vacation.problem.emptyMessage": "Write a subject or a message.",
  "vacation.problem.invalidDate": "That is not a date.",
  "vacation.problem.multilineSubject": "The subject has to be a single line.",

  // The inbox banner, Gmail's own shape (a bar across the top + "End now").
  "vacation.banner": "Your vacation reply is on",
  "vacation.bannerUntil": (date: string): string => `Your vacation reply is on until ${date}`,
  "vacation.endNow": "End now",
  "vacation.ending": "Ending…",
  "vacation.endFailed": "The vacation reply could not be turned off",

  // --- E6: forwarding (canon §2.11) ---
  "forwarding.description":
    "Send a copy of incoming mail to another address. The address has to be verified first — we mail it a code.",
  "forwarding.addressesTitle": "Destination addresses",
  "forwarding.none": "No forwarding addresses yet.",
  "forwarding.add": "Add a forwarding address",
  "forwarding.addPlaceholder": "you@elsewhere.com",
  "forwarding.adding": "Sending the code…",
  "forwarding.addFailed": "The address could not be added",
  "forwarding.pending": "Waiting for the code",
  "forwarding.accepted": "Verified",
  "forwarding.verifiedOn": (date: string): string => `Verified on ${date}`,
  "forwarding.codeSent": (address: string): string =>
    `We sent a code to ${address}. Open that mailbox, copy the code and paste it here.`,
  "forwarding.codeLabel": "Verification code",
  "forwarding.verify": "Verify",
  "forwarding.verifying": "Verifying…",
  "forwarding.verified": (address: string): string => `${address} is verified`,
  "forwarding.verifyFailed":
    "That code did not work. It may be wrong, expired, or for another address.",
  "forwarding.remove": "Remove",
  "forwarding.removeConfirm": (address: string): string =>
    `Remove ${address}? Any filter that forwards there stops working.`,
  "forwarding.removeFailed": "The address could not be removed",
  "forwarding.forwardAllTitle": "Forward all mail",
  "forwarding.forwardAllEnable": "Forward a copy of every message",
  "forwarding.forwardAllTo": "Forward to",
  "forwarding.forwardAllNeedsVerified":
    "Add and verify a destination address first.",
  "forwarding.disposition": (brand: BrandName): string => `Keep the ${brand} copy`,
  "forwarding.dispositionKeep": "in the inbox",
  "forwarding.dispositionArchive": "in Archive",
  "forwarding.saveFailed": "Forwarding could not be saved",
  "forwarding.loadFailed": "Forwarding could not be loaded",

  // --- E6: quota (RFC 9425, canon §2.11) ---
  "quota.label": "Storage",
  "quota.description": "How much of your mailbox is in use, read from the mail server.",
  "quota.used": (used: string, limit: string): string => `${used} of ${limit} used`,
  "quota.percent": (percent: number): string => `${percent}% full`,
  "quota.noLimit": "This mailbox has no storage limit.",
  "quota.loadFailed": "The storage figure could not be read",
  "quota.refresh": "Refresh",

  "settings.offline.soon": "Offline mode is on its way",
  "settings.offline.soonBody":
    "Read, search and reply without a connection, with outgoing mail queued in an Outbox until you are back.",

  /*
   * --- E9b: the offline depth (prefs v2 `offlineDepth`) ---
   *
   * The row that replaces the "coming with the next release" note. Both
   * descriptions state the COST of turning the number up, because that is the
   * only thing the user cannot see for themselves — a depth control with no
   * mention of storage is a slider people move to the maximum and then wonder
   * why the phone complains.
   */
  "settings.offlineHeaders.label": "Messages saved per folder",
  "settings.offlineHeaders.description":
    "How many recent messages are kept on this device for reading and searching offline. More messages means more storage used.",
  "settings.offlineBodies.label": "Message bodies saved",
  "settings.offlineBodies.description":
    "How many opened messages keep their full text offline. Bodies are much larger than the list entries, so this number is lower.",
  /* The bounds, shown beside the control so a refused save is impossible. */
  "settings.offlineDepth.range": (min: number, max: number): string =>
    `Between ${String(min)} and ${String(max)}`,
  "settings.offlineDepth.invalid": (min: number, max: number): string =>
    `Enter a whole number between ${String(min)} and ${String(max)}`,
  /*
   * Attachments stay excluded at any depth — the same limitation Gmail
   * declares. Saying it HERE, next to the number, is what keeps a user from
   * reading a high depth as "everything is available offline".
   */
  "settings.offlineDepth.attachments":
    "Attachments are never saved offline, at any setting.",

  // ------------------------------------------------------------------
  // L2-brand-admin: the "Brand" tab
  //
  // The reader here is a DOMAIN ADMINISTRATOR looking at their own
  // installation, so every string names the thing they are changing — "the
  // sign-in screen", "the installed icon" — rather than the file it lands in.
  // Where a rule of the server would otherwise look arbitrary (SVG refused,
  // 2 MiB, a short name of twelve), the reason is on screen BEFORE the attempt,
  // which is the same principle the label budget follows.
  // ------------------------------------------------------------------
  "settings.section.brand": "Brand",
  "settings.brand.description": (brand: BrandName): string =>
    `The name, colour and images this installation wears. Everyone who signs in here sees ${brand} exactly as you leave it.`,

  "brand.identity.heading": "Name and text",
  "brand.name.label": "Name",
  "brand.name.description":
    "The full name of this installation. It titles the browser tab and names the sign-in screen.",
  "brand.shortName.label": "Short name",
  "brand.shortName.description": "Appears under the installed icon. Twelve characters at most.",
  "brand.shortName.counter": (used: number, max: number): string =>
    `${String(used)} of ${String(max)} characters`,
  "brand.tagline.label": "Tagline",
  "brand.tagline.description":
    "One line under the name on the sign-in screen. Leave it empty for none.",

  "brand.links.heading": "Links",
  "brand.supportUrl.label": "Support",
  "brand.supportUrl.description":
    "Where “Need help?” goes. A web address or a mailto: link. Empty shows no link at all.",
  "brand.privacyUrl.label": "Privacy policy",
  "brand.privacyUrl.description":
    "Your own privacy policy, in the footer. Separate from the licence and source links, which are ours and stay.",
  "brand.termsUrl.label": "Terms of service",
  "brand.termsUrl.description": "Your own terms, in the footer. Empty shows no link.",
  "brand.url.invalid": "Enter a web address starting with https:// or http://, or a mailto: link.",

  "brand.colors.heading": "Colour",
  "brand.primary.label": "Primary colour",
  "brand.primary.description":
    "Buttons, links and the selected row. Everything else is derived from it.",
  "brand.onPrimary.label": "Text on the primary colour",
  "brand.onPrimary.description":
    "Left automatic, a legible one is chosen for you. Override it only if you have to.",
  "brand.onPrimary.auto": "Automatic",
  "brand.onPrimary.custom": "Choose it myself",
  "brand.splashFrom.label": "Sign-in gradient, from",
  "brand.splashTo.label": "Sign-in gradient, to",
  "brand.color.hexLabel": (field: string): string => `${field}, as a hex value`,
  "brand.color.invalid": "Enter a colour like #5b5bd6.",
  "brand.splash.automatic":
    "Left empty, it follows your primary colour. Type a colour to choose it yourself.",
  /*
   * The effective accents, beside the colour the administrator chose.
   *
   * A pastel primary is legal and common, and the palette will DARKEN it to
   * keep text at 4.5:1 — so the buttons come out a different colour from the
   * one in the picker. The sentence below explains it; these two swatches are
   * what make it checkable, because "darkened" means nothing until you can see
   * to what.
   */
  "brand.color.effective.heading": "What the buttons and links will use",
  "brand.color.effective.chosen": "Chosen",
  "brand.color.effective.light": "Light theme",
  "brand.color.effective.dark": "Dark theme",
  "brand.color.effective.containerLight": "Buttons and rows, light",
  "brand.color.effective.containerDark": "Buttons and rows, dark",
  /*
   * The "adjust it and DECLARE it" rule, made visible.
   *
   * The provider already prints this to the console; an administrator who
   * typed a pale mint and got a deeper one deserves to read WHY on the screen
   * where they typed it, not in devtools.
   */
  "brand.color.adjustedLight": (hex: string): string =>
    `Your colour was darkened to ${hex} in the light theme so text stays legible (4.5:1).`,
  "brand.color.adjustedDark": (hex: string): string =>
    `Your colour was lightened to ${hex} in the dark theme so text stays legible (4.5:1).`,

  "brand.preview.heading": "Preview",
  "brand.preview.description":
    "How the colour looks before you save it. Nothing on this page changes until you do.",
  "brand.preview.light": "Light theme",
  "brand.preview.dark": "Dark theme",
  "brand.preview.compose": "Compose",
  "brand.preview.pill": "Inbox",
  "brand.preview.rowSubject": "A selected conversation",
  "brand.preview.rowSnippet": "The row that is open, in the accent colour.",
  "brand.preview.button": "Primary button",
  "brand.preview.link": "A link",

  "brand.images.heading": "Images",
  "brand.images.description":
    "PNG, JPEG, WebP or GIF, up to 2 MB each. SVG is refused: it is a document that can carry script, and these images are shown before anyone has signed in.",
  "brand.logo.label": "Logo",
  "brand.logo.description":
    "Your wordmark, in the top bar and on the sign-in screen. Any shape; it is sized by height.",
  "brand.logoDark.label": "Logo for dark backgrounds",
  "brand.logoDark.description":
    "A variant for the dark theme and the sign-in panel. Without one, a dark logo is drawn on a light plate instead.",
  "brand.icon.label": "Square icon",
  "brand.icon.description":
    "The installed app icon and the favicon. Square, at least 512×512 — a non-square image is accepted and cropped, and we say so.",
  "brand.splash.label": "Sign-in image",
  "brand.splashText.label": "Show name and tagline over the image",
  "brand.splashText.description":
    "Turn this off when the image already has your logo or slogan in it — otherwise the sign-in screen shows them twice. With no image the name always appears.",
  "brand.splash.description": "The picture beside the sign-in form. Wide images work best.",
  "brand.image.drop": "Drag an image here, or",
  "brand.image.choose": "choose a file",
  "brand.image.chooseFor": (field: string): string => `Choose an image for ${field}`,
  "brand.image.remove": "Remove",
  "brand.image.removeFor": (field: string): string => `Remove the image for ${field}`,
  "brand.image.uploading": "Uploading…",
  "brand.image.none": "None yet",
  "brand.image.dimensions": (width: number, height: number, bytes: string): string =>
    `${String(width)}×${String(height)}, ${bytes}`,
  "brand.image.onLight": "On light",
  "brand.image.onDark": "On dark",
  "brand.image.tooLarge": "That image is over 2 MB. Save it smaller and try again.",
  "brand.image.unsupported": "That file is not a PNG, JPEG, WebP or GIF.",
  "brand.icons.heading": "Generated icons",
  "brand.icons.fromLogo":
    "These were made from your logo because no square icon is set.",
  "brand.icons.fromDefault": "These are the stock icons: upload a square icon to replace them.",
  "brand.warnings.heading": "Worth knowing",

  "brand.danger.heading": "Reset",
  "brand.reset.label": "Back to the stock brand",
  "brand.reset.description":
    "Clears the name, colours and every image. You keep your access and can set it up again.",
  "brand.reset.button": "Reset the brand",
  "brand.reset.confirm":
    "Reset this installation to the stock brand? The name, colours and images are cleared for everyone who signs in here.",
  "brand.error.network": "That could not be saved. Check the connection and try again.",
  "brand.error.notAdmin": "You no longer administer this installation.",

  // --- E7: Send & Archive, the reply default, and named signatures (v2) ---
  "settings.sendAndArchive.label": "Show “Send & Archive” in replies",
  "settings.sendAndArchive.description":
    "Adds a second send button to replies that files the conversation in Archive as it sends.",
  "settings.replyBehavior.label": "Default reply behaviour",
  "settings.replyBehavior.description":
    "Which reply the button and the “r” key open. “Reply all” stays available either way, and Shift+A is always reply all.",
  "settings.replyBehavior.reply": "Reply",
  "settings.replyBehavior.replyAll": "Reply all",
  "settings.replyBehavior.replyNote": "Answers the sender only.",
  "settings.replyBehavior.replyAllNote": "Answers everyone the message was addressed to.",

  "settings.signatures.label": "Signatures",
  "settings.signatures.description":
    "Several signatures with names, and which one new messages and replies start with. Saved to your account, so they follow you to every device.",
  "settings.signatures.add": "New signature",
  "settings.signatures.namePlaceholder": "Name",
  "settings.signatures.bodyPlaceholder": "Signature text",
  "settings.signatures.delete": "Delete",
  "settings.signatures.deleteConfirm": (name: string): string =>
    `Delete the signature “${name}”? Messages already sent are unaffected.`,
  "settings.signatures.forNew": "For new messages",
  "settings.signatures.forReply": "For replies and forwards",
  /*
   * The fallback, named rather than implied: with "None" chosen the composer
   * uses the identity signature above, which is the RFC 8621 §6 behaviour every
   * other mail client sees.
   */
  "settings.signatures.none": "None (use the signature above)",
  "settings.signatures.empty": "No named signatures yet.",
  "settings.signatures.full": (max: number): string =>
    `${String(max)} signatures is the maximum.`,
  /*
   * The honest limitation. Rich signatures created elsewhere are PRESERVED —
   * nothing here overwrites an htmlBody — they simply cannot be edited in this
   * panel yet, and a user who has one needs to know why the box shows plain
   * text.
   */
  "settings.signatures.textOnly":
    "Signatures are edited as plain text here. Formatting set elsewhere is kept, not shown.",

  // --- theme control ---
  "theme.label": "Theme",
  "theme.light": "Light",
  "theme.dark": "Dark",
  "theme.system": "System",

  // --- P2: mailboxes ---
  "mailbox.inbox": "Inbox",
  "mailbox.drafts": "Drafts",
  "mailbox.sent": "Sent",
  "mailbox.archive": "Archive",
  "mailbox.junk": "Junk",
  "mailbox.trash": "Trash",
  "mailbox.all": "All mail",
  "mailbox.flagged": "Starred",
  "mailbox.unreadCount": (count: number): string =>
    `${count} unread message${count === 1 ? "" : "s"}`,
  "mailbox.itemCount": (count: number): string =>
    `${count} item${count === 1 ? "" : "s"}`,
  "mailbox.collapse": "Collapse folder",
  "mailbox.expand": "Expand folder",
  "mailbox.loadFailed": "Could not load your folders",
  "mailbox.retry": "Try again",
  // P0-5: the rail's collapse, and the qualifier a custom folder gets when its
  // name collides with a role row's label.
  "mailbox.more": "More",
  "mailbox.less": "Less",
  "mailbox.customSuffix": "folder",

  // --- P2: the message list ---
  "list.loading": "Loading messages…",
  "list.empty": "Nothing here",
  "list.emptyBody": "This folder has no messages.",
  "list.emptySearch": "No matches",
  "list.emptySearchBody": (query: string): string =>
    `Nothing matched “${query}”. Try fewer or different words.`,
  "list.label": "Message list",
  "list.selectMessage": "Select a message to read it",
  "list.selectMessageBody":
    "Choose a conversation from the list, or press j and k to move through it.",
  "list.attachment": "Has an attachment",
  "list.flagged": "Starred",
  "list.unread": "Unread",
  "list.threadSize": (count: number): string => `${count} messages in this conversation`,
  // E1: the same count when it came from client-side grouping, which can only
  // see the fetched window. Said plainly rather than presented as the total.
  "list.threadSizeInWindow": (count: number): string =>
    `${count} messages from this conversation in these results`,
  "list.noSubject": "(no subject)",
  "list.unknownSender": "(unknown sender)",
  // The honest ceiling message. The server answers at most 200 rows and has no
  // working offset, so a longer folder genuinely cannot be paged through yet.
  //
  // Two variants, because the advice has to differ: in a folder, searching IS
  // the way to reach older mail; inside a search, telling the user to search
  // is advice they have already taken, so the honest thing is to say the
  // result set is capped and suggest narrowing it.
  "list.truncated": (shown: number): string =>
    `Showing the ${shown} most recent conversations. This server cannot page further yet — use search to find older mail.`,
  "list.truncatedSearch": (shown: number): string =>
    `Showing the ${shown} most recent matches. There may be more — add words to narrow the search.`,
  "list.loadFailed": "Could not load these messages",

  // --- P2: search ---
  "search.label": "Search mail",
  "search.placeholder": "Search mail",
  "search.clear": "Clear search",
  /* A-11: leaves a VIRTUAL VIEW (Destacados, Pospuestos, a label) rather than
     clearing a query — the ✕ beside `is:starred` in the box. It navigates, so
     the wording is about the view, not about the text. */
  "search.clearView": "Exit this view",
  "search.searching": "Searching…",
  "search.resultCount": (count: number): string =>
    `${count} result${count === 1 ? "" : "s"}`,
  "search.inMailbox": "In this folder",
  "search.everywhere": "All mail",
  // The graceful degradation the brief requires: never a silent empty list.
  "search.unsupported": "This server cannot answer that search",
  "search.unsupportedBody": (brand: BrandName): string =>
    `Search in ${brand} covers text, sender, recipient and subject, and can be narrowed to one folder and a date range. Other conditions are not available yet.`,

  // --- P2: the reading pane ---
  "reader.from": "From",
  "reader.to": "To",
  "reader.cc": "Cc",
  "reader.bcc": "Bcc",
  "reader.replyTo": "Reply to",
  "reader.date": "Date",
  // C-14: "to me ▾" — the recipient phrase and the caret behind it.
  "reader.me": "me",
  // The phrase's verb, lower-case as Gmail writes it ("to me"); `reader.to`
  // stays the capitalised header label.
  "reader.toPhrase": "to",
  "reader.subject": "Subject",
  "reader.showDetails": "Show details",
  "reader.hideDetails": "Hide details",
  "reader.close": "Back to the list",
  "reader.loading": "Loading the message…",
  "reader.loadFailed": "Could not load this message",
  "reader.attachments": (count: number): string =>
    `${count} attachment${count === 1 ? "" : "s"}`,
  "reader.download": "Download",
  "reader.downloadMessage": "Download the original message",
  "reader.downloading": "Preparing the download…",
  "reader.downloadFailed": "The download did not start. Try again.",
  "reader.threadContext": (count: number): string =>
    `Conversation with ${count} messages`,
  // E1 / canon §2.1: the `;` and `:` pair, and the control that drives them.
  // "Collapse" keeps the newest message open — see collapseAll's rationale.
  "reader.expandAll": "Expand all",
  "reader.collapseAll": "Collapse all",
  // C-06: the open conversation's place in the list, beside ‹ ›.
  "reader.positionOf": (index: number, total: number): string =>
    `${index.toLocaleString("en")} of ${total.toLocaleString("en")}`,
  // C-07: the × on the folder chip beside the subject — Gmail's "Recibidos ×",
  // which removes the conversation from Inbox (archives it).
  "reader.removeFromFolder": (name: string): string => `Remove from ${name}`,
  "reader.threadLoadFailed":
    "The rest of this conversation could not be loaded. The message you opened is shown below.",
  "reader.emptyBody": "This message has no text content.",
  "reader.bodyTruncated":
    "This message is long and has been shortened. Download the original to read all of it.",
  // --- the secure HTML renderer (W-A4) ---
  // The iframe's accessible name: what the region IS, for a screen-reader
  // user landing on it.
  "reader.htmlFrameTitle": "Message content",
  // Remote images: blocked by default (they leak the reader's IP and the
  // moment of opening to the sender), loadable through the privacy proxy on
  // an explicit action. The banner explains the WHY in one clause, because a
  // bare "images blocked" reads as a malfunction.
  "reader.imagesBlocked": (count: number): string =>
    count === 1
      ? "1 remote image is hidden to protect your privacy."
      : `${count} remote images are hidden to protect your privacy.`,
  "reader.showImages": "Show images",
  // E1 / canon §2.1: Gmail's "Show trimmed content". The wording says CONTENT
  // rather than "quote", because what is hidden is often a forwarded header
  // block or an Outlook divider, not a quotation.
  "reader.showTrimmed": "Show trimmed content",
  "reader.hideTrimmed": "Hide trimmed content",
  // C-03: a very long message is clipped at a cap and offered whole — the
  // control that raises the sandboxed frame's estimated height.
  "reader.showWholeMessage": "Show the whole message",
  "reader.showLess": "Show less",
  "reader.imagesLoading": "Loading images through the privacy proxy…",
  "reader.imagesFailed":
    "The images could not be loaded through the privacy proxy, so they stay hidden. Try again later.",
  // C-11: inline images ARE displayed now; this counts the ones that could
  // not be — no matching part, too large to inline, or the fetch failed.
  "reader.inlineImagesUnavailable": (count: number): string =>
    count === 1
      ? "1 embedded image cannot be displayed."
      : `${count} embedded images cannot be displayed.`,
  // The honest fallback: sanitization refused the whole document. Never
  // rendered silently — the user is told a formatted version exists.
  "reader.htmlSanitizeFailed": "The formatted version cannot be shown safely",
  "reader.htmlSanitizeFailedBody": (brand: BrandName): string =>
    `This message's formatting could not be made safe to display, so ${brand} is not showing it. The plain-text version, when the sender included one, is shown below; the original message can be downloaded in full.`,
  "reader.parseFailed": (brand: BrandName): string =>
    `${brand} could not read this message's contents`,
  "reader.parseFailedBody":
    "The message is stored safely and can be downloaded in full, but its structure could not be parsed.",

  // --- P2: keyboard ---
  "shortcuts.title": "Keyboard shortcuts",
  "shortcuts.close": "Close",
  "shortcuts.disabled":
    "Keyboard shortcuts are off in Settings. Escape and / still work, so you can close this and reach search.",
  "shortcuts.open": "Open the message",
  // E1: j/k and n/p are two different axes and the help has to say so, or the
  // second pair looks like a duplicate of the first.
  "shortcuts.next": "Next conversation",
  "shortcuts.previous": "Previous conversation",
  "shortcuts.conversationNext": "Next message in this conversation",
  "shortcuts.conversationPrevious": "Previous message in this conversation",
  "shortcuts.expandAll": "Expand every message in the conversation",
  "shortcuts.collapseAll": "Collapse the conversation",
  "shortcuts.back": "Back to the list",
  "shortcuts.search": "Search",
  /* E-32: the keys INSIDE the box. They are the box's own — the typing guard
     refuses every global key while an input has focus — but a user does not
     care which module implements a key. */
  "shortcuts.searchLeave": "Leave the search box (the text stays)",
  "shortcuts.searchRun": "Run the search",
  "shortcuts.searchSuggestions": "Move through the suggestions",
  "shortcuts.archive": "Archive",
  "shortcuts.delete": "Delete",
  "shortcuts.flag": "Star",
  "shortcuts.selectRow": "Select this conversation",
  "shortcuts.compose": "Write a new message",
  "shortcuts.reply": "Reply",
  "shortcuts.replyAll": "Reply to everyone",
  "shortcuts.forward": "Forward",
  // E11: Gmail's directional pair (canon §2.7), replacing the single toggle
  // that used to live here — with a mixed selection a toggle has no meaning.
  "shortcuts.markRead": "Mark as read",
  "shortcuts.markUnread": "Mark as unread",
  // E11: the application keys that reach the toolbar and its overflow.
  "shortcuts.focusToolbar": "Move focus to the toolbar",
  "shortcuts.moreActions": "Open the more-actions menu",
  // E11: the composer's own keys. Handled by the composer, not the global
  // resolver — inside a text field the typing guard refuses everything — but
  // documented here because the user does not care which module owns a key.
  "shortcuts.send": "Send the message",
  "shortcuts.focusCc": "Add or focus Cc",
  "shortcuts.focusBcc": "Add or focus Bcc",
  "shortcuts.goInbox": "Go to Inbox",
  "shortcuts.goSent": "Go to Sent",
  "shortcuts.goDrafts": "Go to Drafts",
  "shortcuts.goArchive": "Go to Archive",
  "shortcuts.goTrash": "Go to Trash",
  "shortcuts.help": "Show this help",
  "shortcuts.sectionNavigate": "Moving around",
  "shortcuts.sectionActions": "Acting on mail",
  "shortcuts.sectionSelection": "Selecting",
  "shortcuts.sectionCompose": "Writing",
  "shortcuts.sectionJump": "Jumping to a folder",
  "action.notYet": "This action arrives in the next release",

  // --- E2: the rest of Gmail's triage vocabulary ---
  "shortcuts.spam": "Report spam, or mark as not spam",
  "shortcuts.undo": "Undo the last action",
  "shortcuts.archiveNext": "Archive and go to the next message",
  "shortcuts.archivePrevious": "Archive and go to the previous message",
  "shortcuts.markUnreadFromHere": "Mark unread from here down",
  "shortcuts.selectAll": "Select every conversation",
  "shortcuts.selectNone": "Clear the selection",
  "shortcuts.selectRead": "Select the read ones",
  "shortcuts.selectUnread": "Select the unread ones",
  "shortcuts.selectStarred": "Select the starred ones",
  "shortcuts.selectUnstarred": "Select the ones without a star",
  // E8: Gmail's "label as" key.
  "shortcuts.labelAs": "Label as",

  // --- P3: actions on messages ---
  //
  // The wording of delete is load-bearing. Server arbitration W-A2 makes
  // `destroy` a MOVE to Trash unless the message is already there, in which
  // case it really is erased. One word for both promises would be a lie in one
  // of the two cases, so the UI asks which one it is (see deleteIsPermanent).
  "action.markRead": "Mark as read",
  "action.markUnread": "Mark as unread",
  "action.flag": "Star",
  "action.unflag": "Remove star",
  "action.archive": "Archive",
  "action.delete": "Move to Trash",
  "action.deleteForever": "Delete permanently",
  "action.move": "Move to",
  "action.moveTo": "Move to folder",
  "action.reply": "Reply",
  "action.replyAll": "Reply all",
  "action.forward": "Forward",
  "action.more": "More actions",
  "action.selectAll": "Select all",
  "action.clearSelection": "Clear the selection",
  "action.selected": (count: number): string => `${count} selected`,
  "action.selectRow": "Select this conversation",
  "action.undo": "Undo",

  // E11: the app's own confirm/prompt, replacing window.confirm/prompt.
  "dialog.confirmTitle": "Are you sure?",
  "dialog.promptTitle": "Enter a value",
  "dialog.confirm": "Confirm",
  "dialog.cancel": "Cancel",
  "dialog.ok": "OK",
  "action.confirmDeleteForever": (count: number): string =>
    count === 1
      ? "Delete this message permanently? This cannot be undone."
      : `Delete these ${count} messages permanently? This cannot be undone.`,
  "action.confirm": "Delete permanently",
  "action.cancel": "Cancel",
  // Every failure names what failed AND restores the prior state â never a
  // silent revert.
  "action.failedTitle": "That action did not go through",
  "action.failedRestored": "Nothing changed on the server; the list has been put back.",
  "action.partialFailure": (done: number, failed: number): string =>
    `${done} succeeded, ${failed} failed. The failed ones have been put back.`,
  "action.doneArchived": (count: number): string =>
    count === 1 ? "Archived" : `${count} archived`,
  "action.doneDeleted": (count: number): string =>
    count === 1 ? "Moved to Trash" : `${count} moved to Trash`,
  "action.doneDeletedForever": (count: number): string =>
    count === 1 ? "Deleted permanently" : `${count} deleted permanently`,
  "action.doneMoved": (folder: string): string => `Moved to ${folder}`,

  // --- E2: spam, undo, the completed reader, and emptying the trash ---
  //
  // "Report spam" rather than "Move to Junk": the user's intent is a verdict
  // about the message, and the folder it lands in is an implementation detail
  // of that verdict. Inside Junk the same control means the opposite, so it
  // gets its own label rather than a toggled state on one word.
  "action.spam": "Report spam",
  "action.notSpam": "Not spam",
  "action.doneSpam": (count: number): string =>
    count === 1 ? "Reported as spam" : `${count} reported as spam`,
  "action.doneNotSpam": (count: number): string =>
    count === 1 ? "Moved back to the inbox" : `${count} moved back to the inbox`,
  "action.undoDone": "The action was undone",
  "action.undoFailed": "That could not be undone",
  "action.undoExpired": "There is nothing to undo",
  "action.emptyTrash": "Empty trash now",
  "action.emptyTrashConfirm": (count: number): string =>
    count === 1
      ? "Delete the 1 message in Trash permanently? This cannot be undone."
      : `Delete all ${count} messages in Trash permanently? This cannot be undone.`,
  "action.emptyTrashEmpty": "The Trash is already empty",
  "action.emptyTrashDone": (count: number): string =>
    count === 1 ? "1 message deleted permanently" : `${count} messages deleted permanently`,
  "action.emptyTrashWorking": "Emptying the Trash…",
  "action.print": "Print",
  "action.viewOriginal": "Show original",
  "action.next": "Next message",
  "action.previous": "Previous message",
  "action.unsubscribe": "Unsubscribe",

  // --- E2: the reader's new surfaces ---
  "reader.spamBanner": "This message is in Spam",
  "reader.spamBannerBody": (brand: BrandName): string =>
    `${brand} shows it because you asked for it, and keeps its images and links inert. If it does not belong here, mark it as not spam.`,
  "reader.spamImagesBlocked":
    "Images are never loaded for a message in Spam.",
  // --- E10: suspicious mail outside Spam (canon §4.1.15) ---
  "reader.suspiciousBanner": "This message looks like spam",
  "reader.suspiciousBannerBody":
    "The mail scanner flagged it, but a rule or setting kept it out of Spam. Its remote images stay hidden; everything else works normally.",
  "reader.originalTitle": "Original message",
  "reader.originalHeaders": "Headers, exactly as they arrived",
  "reader.originalLoading": "Loading the original…",
  "reader.originalFailed": "Could not load the original message",
  "reader.copy": "Copy to clipboard",
  "reader.copied": "Copied",
  "reader.copyFailed": "Could not copy. Select the text and copy it manually.",
  "reader.unsubscribeFrom": (list: string): string => `Unsubscribe from ${list}`,
  "reader.unsubscribeOpensTab": "Opens the sender's page in a new tab",
  "reader.unsubscribeLatency":
    "It can take a few days for the sender to stop sending.",

  // --- P3: the composer ---
  // Gmail's own word for the rail's pill (canon 07 §2), not a synonym of it:
  // the label is muscle memory, and "Write" is a word Gmail never shows.
  "compose.new": "Compose",
  "compose.title": "New message",
  "compose.titleReply": "Reply",
  "compose.titleForward": "Forward",
  "compose.titleDraft": "Draft",
  "compose.from": "From",
  "compose.to": "To",
  "compose.cc": "Cc",
  "compose.bcc": "Bcc",
  "compose.showCc": "Add Cc",
  "compose.showBcc": "Add Bcc",
  "compose.subject": "Subject",
  "compose.subjectPlaceholder": "Subject",
  "compose.body": "Message",
  "compose.send": "Send",
  "compose.sending": "Sending…",
  "compose.discard": "Discard",
  "compose.close": "Close the composer",
  // E12 (canon 07 §7): the floating card's three sizes.
  "compose.minimize": "Minimise",
  "compose.expand": "Expand",
  "compose.maximize": "Full screen",
  "compose.restore": "Exit full screen",
  "compose.attach": "Attach a file",
  "compose.attachments": (count: number): string =>
    count === 1 ? "1 attachment" : `${count} attachments`,
  "compose.removeAttachment": (name: string): string => `Remove ${name}`,
  "compose.removeRecipient": (address: string): string => `Remove ${address}`,
  "compose.recipientCount": (count: number): string =>
    count === 1 ? "1 recipient" : `${count} recipients`,
  "compose.uploading": (percent: number): string => `Uploading… ${percent}%`,
  "compose.uploadFailed": "This file could not be attached",
  "compose.plainText": "Plain text",
  "compose.richText": "Formatting",
  "compose.formatOptions": "Formatting options",
  "compose.bold": "Bold",
  "compose.italic": "Italic",
  "compose.underline": "Underline",
  "compose.bulletList": "Bulleted list",
  "compose.orderedList": "Numbered list",
  "compose.link": "Insert a link",
  "compose.emoji": "Insert an emoji",
  "compose.signature": "Insert a signature",
  "compose.insertImage": "Insert an image",
  /* D-03: the tooltip states the limit where the control is. Our send path
     writes every part as `disposition: "attachment"`, with no `cid`, so an
     inline <img> would arrive broken — the honest word is "attach". */
  "compose.insertImageHint": "Insert an image (attached, not embedded in the body)",
  "compose.linkPrompt": "Address of the link",
  "compose.linkInvalid": "A link must be a web address (http, https) or an email address.",
  "compose.linkTitle": "Insert link",
  "compose.linkPlaceholder": "example.com",
  "compose.addressInvalid": (address: string): string =>
    `${address} is not a complete email address.`,
  "compose.noRecipients": "Add at least one recipient before sending.",
  "compose.attributionLine": (date: string, sender: string): string =>
    `On ${date}, ${sender} wrote:`,
  "compose.forwardedHeader": "---------- Forwarded message ----------",
  "compose.forwardedFrom": "From",
  "compose.forwardedDate": "Date",
  "compose.forwardedSubject": "Subject",
  "compose.forwardedTo": "To",

  // --- E7: identity and daily sending (canon §2.3) ---
  "compose.suggestions.label": "Suggested addresses",
  "compose.more": "More options",
  "compose.plainTextMode": "Plain text mode",
  "compose.plainTextWarning":
    "Switching to plain text removes the formatting from this message. The text stays.",
  "send.andArchive": "Send & archive",
  "send.andArchiveHint": "Sends this reply and archives the conversation",
  "send.sentAndArchived": "Message sent — conversation archived",
  "send.archiveFailed": "The message was sent, but the conversation could not be archived",
  "send.canceledUnarchived": "Send canceled — the conversation is back in your inbox",
  /*
   * Gmail's own wording, adapted (/mail/answer/6584). It says WHY rather than
   * "not allowed", because a user who does not know the reason assumes a bug
   * and tries again with the same file.
   */
  "compose.blockedExtension": (name: string): string =>
    `${name} was not attached: this kind of file is blocked because it presents a security risk.`,
  "compose.blockedExtensionHint":
    "To send it, put it in a .zip first, or share it with a link.",

  // Forward as attachment (canon §2.3, /mail/answer/9337672).
  "action.forwardAsAttachment": "Forward as attachment",
  "forwardAttachment.preparing": "Preparing the message…",
  "forwardAttachment.failed": "The message could not be attached",
  "forwardAttachment.tooLarge": (limit: string): string =>
    `These messages exceed the ${limit} limit and were not attached.`,
  "forwardAttachment.subject": (count: number): string =>
    count === 1 ? "Forwarded message" : `${count} forwarded messages`,

  // --- P3: drafts ---
  "draft.saving": "Saving…",
  "draft.saved": "Draft saved",
  "draft.unsaved": "Unsaved changes",
  "draft.saveFailed": "The draft could not be saved",
  "draft.discardConfirm": "Discard this draft? What you wrote will be lost.",
  "draft.discarded": "Draft discarded",
  "draft.discardFailed": "The draft could not be discarded",

  // --- P3: sending, with undo ---
  "send.undoWindow": (seconds: number): string => `Sending in ${seconds}s`,
  "send.undo": "Undo",
  "send.sent": "Message sent",
  "send.canceled": "Send canceled — the message was not transmitted",
  "send.failedTitle": "The message was not sent",
  "send.cannotUnsend": "Too late to undo — the message has already gone out.",
  "send.sizeExceeded": (limit: string): string =>
    `This file is larger than the ${limit} this server accepts.`,
  "send.attachmentsExceeded": (limit: string): string =>
    `The attachments add up to more than the ${limit} one message may carry.`,

  // --- P3: folders ---
  "folder.create": "New folder",
  "folder.name": "Folder name",
  "folder.createFailed": "The folder could not be created",

  // --- E8: labels under the 26-keyword ceiling (GC-5) ---
  //
  // The honesty strings here are the epic's product requirement, not polish.
  // Maildir gives 26 durable keywords per folder and the semi-system ones
  // (`$Forwarded`, `$MDNSent`, `NonJunk`) spend from the same 26, so the UI
  // states the budget rather than discovering it at the 27th create — which
  // would succeed, apply, and lose the label weeks later when Dovecot rebuilds
  // its index. Folders are the unlimited alternative and the copy says so.
  "list.emptyLabel": "Nothing labelled that",
  "list.emptyLabelBody": (name: string): string =>
    `No message carries "${name}" yet. Select some mail and use "Label as" to apply it.`,
  "label.plural": "Labels",
  "label.labelAs": "Label as",
  "label.manage": "Manage labels…",
  /*
   * F-30: an actionable empty state, not a full stop.
   *
   * "No labels yet" is true and useless — it tells a user who has arrived
   * looking for labels that they are in the right place and offers them
   * nothing. Gmail's own empty states name the first thing to do; the three
   * examples exist because "what would I even call one" is the actual block,
   * and clicking one fills the name box rather than creating anything.
   */
  "label.none": "No labels yet",
  "label.emptyTitle": "Create your first label",
  "label.emptyBody":
    "A label crosses folders: one message can carry several, and clicking one shows everything that carries it. Start from an example or type your own name.",
  "label.emptyExamples": "For example:",
  "label.example.invoices": "Invoices",
  "label.example.travel": "Travel",
  "label.example.followUp": "Follow up",
  "label.useExample": (name: string): string => `Use “${name}” as the name`,

  /*
   * F-31: the swatches carry names.
   *
   * A grid of coloured squares has no accessible name beyond its id, and the
   * ids were being read out raw ("slate", "amber-bold"). These are the names a
   * person would use, and they are the swatch's title AND its accessible name —
   * so the tooltip and the screen reader say the same thing.
   */
  "label.colorName": (hue: string, step: string): string => `${hue} · ${step}`,
  "label.step.pale": "Pale",
  "label.step.bold": "Bold",
  "label.hue.slate": "Grey",
  "label.hue.red": "Red",
  "label.hue.orange": "Orange",
  "label.hue.amber": "Amber",
  "label.hue.lime": "Lime",
  "label.hue.green": "Green",
  "label.hue.teal": "Teal",
  "label.hue.cyan": "Cyan",
  "label.hue.blue": "Blue",
  "label.hue.indigo": "Indigo",
  "label.hue.purple": "Purple",
  "label.hue.pink": "Pink",
  // P0-5c: the folder-visibility table, the other half of the rail's curation.
  "folders.heading": "Folders",
  "folders.help":
    "Folders your mail client created to sync calendars, contacts or sync errors are hidden from the rail by default. Nothing is deleted — show any of them here.",
  "folders.unavailable":
    "This server cannot store the choice yet, so it would not survive a reload. The rail still hides the folders it recognises.",
  "folders.visibility": "In the folder rail",
  "folders.none": "No folders",
  "label.more": (count: number): string => `+${count}`,
  "label.openLabel": (name: string): string => `Show everything labelled "${name}"`,
  "label.create": "New label",
  "label.name": "Label name",
  "label.color": "Colour",
  "label.rename": "Rename",
  "label.delete": "Delete",
  "label.visibility": "In the sidebar",
  "label.visibility.show": "Show",
  "label.visibility.showIfUnread": "Show if unread",
  "label.visibility.hide": "Hide",
  "label.applied": "Label applied",
  "label.removed": "Label removed",

  // The budget, said out loud.
  "label.budget": (available: number, ceiling: number): string =>
    `${available} of ${ceiling} available`,
  "label.budgetFull": "No label slots left",
  "label.budgetExplained":
    "A folder holds 26 durable IMAP keywords, and labels share them with the flags other mail apps set. Folders have no such limit — use one for anything you file rather than tag.",
  "label.createFolderInstead": "Create a folder instead",

  // Validation, one sentence per refusal.
  "label.error.empty": "Give the label a name.",
  "label.error.tooLong": "That name is too long.",
  "label.error.reserved": "That name is reserved by the mail system.",
  "label.error.duplicate": "A label with that name already exists.",
  "label.error.control": "That name contains characters a mail server cannot store.",
  "label.error.full": "There is no keyword slot left for a new label.",

  // Rename and delete are data migrations over every message that carries the
  // keyword, so they report progress, they can be stopped, and they never
  // claim to have finished when they stopped early.
  "label.renameTitle": (name: string): string => `Rename "${name}"`,
  "label.deleteTitle": (name: string): string => `Delete "${name}"`,
  "label.deleteConfirm": (name: string): string =>
    `Remove "${name}" from every message that carries it? The messages themselves are not deleted.`,
  "label.migrating": (done: number): string => `${done} messages updated…`,
  "label.migrateDone": (done: number): string => `${done} messages updated`,
  "label.migrateIncomplete": (done: number): string =>
    `${done} messages updated — some still carry the old label. Run it again to finish.`,
  "label.migrateAborted": (done: number): string =>
    `Stopped after ${done} messages. The rest keep the old label.`,
  "label.migrateFailed": "The label could not be changed",
  "label.abort": "Stop",

  /*
   * The gap this string used to declare is CLOSED (prefs v2 `labels`), so the
   * disclaimer is gone rather than softened. What replaces it is not a smaller
   * caveat but the positive fact — a user who read the old warning needs to be
   * told it no longer applies, and an absence would leave them believing it.
   */
  "label.roams":
    "Colours and sidebar visibility are saved to your account, so they follow you to every device.",

  "settings.section.labels": "Labels",
  "settings.labels.description":
    "Labels are IMAP keywords, so they cross folders — and a folder holds only 26 of them.",

  // --- E3: the search language (canon §2.5) ---
  //
  // The honesty rule of this block: when a term cannot be used, the UI names
  // WHICH term and why. "This server cannot answer that search" alone leaves
  // the user editing a query at random, which is how a search box teaches
  // people that search does not work.
  "search.options": "Search options",
  "search.options.open": "Show search options",
  "search.options.close": "Hide search options",
  "search.options.from": "From",
  "search.options.to": "To",
  "search.options.subject": "Subject",
  "search.options.words": "Has the words",
  "search.options.size": "Size",
  "search.options.sizeLarger": "greater than",
  "search.options.sizeSmaller": "less than",
  "search.options.sizeUnit": "Unit",
  "search.options.dateWithin": "Date within",
  "search.options.dateOf": "of",
  "search.options.dateAnchor": "Date to search around",
  /* E-20: why a field a Gmail user expects is missing. A note, never a
     disabled input: this one cannot be built, and a greyed field would still
     advertise it. */
  "search.options.noExcludeNote":
    "There is no field for excluding words: this server cannot answer a search for everything that does NOT match, so the field would only ever fail.",
  "search.options.hasAttachment": "Has attachment",
  "search.options.scope": "Search",
  /* E-15: the DEFAULT scope, named for what it actually is. An empty `in:`
     sends no scope condition and the server excludes Spam and Trash itself —
     which is Gmail's "All mail". The option used to be labelled "In this
     folder", which described a scoping that never happened. */
  "search.options.scopeDefault": "All mail (except Spam and Bin)",
  "search.options.scopeAll": "All mail, Spam and Bin included",
  "search.options.submit": "Search",
  "search.options.reset": "Clear",
  // E12/B7 (canon 07 §8): the second button of Gmail's advanced panel.
  "search.options.createFilter": "Create filter",
  "search.options.createFilterUnusable":
    "Add a sender, recipient, subject, size or attachment condition first — a filter with none would match every message.",
  "search.options.filterDrops": (fields: string): string =>
    `A filter cannot carry these over: ${fields}.`,
  "search.within.1d": "1 day",
  "search.within.3d": "3 days",
  "search.within.1w": "1 week",
  "search.within.2w": "2 weeks",
  "search.within.1m": "1 month",
  "search.within.2m": "2 months",
  "search.within.6m": "6 months",
  "search.within.1y": "1 year",

  // The chips row (Gmail's five).
  "search.chip.from": "From",
  "search.chip.to": "To",
  "search.chip.anyTime": "Any time",
  "search.chip.hasAttachment": "Has attachment",
  "search.chip.isUnread": "Is unread",
  "search.chip.last7": "Last 7 days",
  "search.chip.last30": "Last 30 days",
  "search.chip.last90": "Last 90 days",
  "search.chip.remove": (name: string): string => `Remove the ${name} filter`,
  "search.chips.label": "Search filters",

  // Suggestions.
  "search.suggestions.label": "Search suggestions",
  "search.suggestions.recent": "Recent searches",
  "search.suggestions.labels": "Labels",
  "search.suggestions.operators": "Search operators",
  "search.suggestions.values": "Values",
  "search.suggestions.messages": "Messages",
  /* E-08: the row that keeps the five previews from reading as "all there is". */
  "search.allResults": (query: string): string => `All results for “${query}”`,
  /* E-14: the strip over a results list — what, where, how much. */
  "search.context": (query: string, scope: string): string =>
    `Results for “${query}” in ${scope}`,
  "search.contextCount": (total: number): string => `· ${total.toLocaleString("en")}`,
  "search.contextCountAtLeast": (atLeast: number): string =>
    `· more than ${atLeast.toLocaleString("en")}`,
  "search.allResultsKey": "Enter",
  "search.quick.label": "Quick searches",
  "search.quick.sentByMe": "Sent by me",
  "search.suggestions.clearRecent": "Clear recent searches",

  // Refusals, each naming the term at fault.
  "search.refused.title": "Part of that search could not be used",
  "search.refused.negation": (operator: string): string =>
    `“${operator}” — this server cannot search for the ABSENCE of an address or a word, only for its presence.`,
  "search.refused.deferred": (operator: string): string =>
    `“${operator}” is not supported yet.`,
  "search.refused.badValue": (operator: string): string =>
    `“${operator}” did not have a value this server could read.`,
  "search.problem.needsTextOrFolder":
    "Add a word to search for, or choose a folder: filters like “is:unread” cannot be answered on their own.",
  "search.problem.labelNeedsText":
    "A label search cannot be narrowed to one folder. Remove the folder, or add a word to search for.",
  "search.problem.unknownMailbox": (name: string): string =>
    `There is no folder called “${name}”.`,
  "search.problem.tooManyBranches": (count: number): string =>
    `${count} alternatives joined by OR is more than this server answers at once. Use at most 4.`,
  "search.problem.branchNotAnswerable": (index: number): string =>
    `Alternative ${index} needs a word or a folder of its own — an OR only widens a search, it never narrows one.`,
  "search.approximate.folded": (fields: string): string =>
    `${fields} were searched across the whole message, not only in those headers.`,
  "search.scopeEverything": "Including Spam and Trash",
  "search.zeroResults": "No messages matched",
  "search.zeroResultsBody":
    "Every term was searched. Try removing one, or search all mail including Spam and Trash.",

  // --- E4: snooze, mute and schedule send (canon §2.2 and §2.3) ---
  //
  // The preset LABELS are Gmail's, from its fetchable help page. The TIMES
  // behind them are ours — canon §5 puts "snooze preset times" in the
  // UNSOURCED register — and they are documented in `mail/snoozePresets.ts`.
  "snooze.action": "Snooze",
  "snooze.menuLabel": "Snooze until",
  "snooze.laterToday": "Later today",
  "snooze.tomorrow": "Tomorrow",
  "snooze.thisWeekend": "This weekend",
  "snooze.nextWeek": "Next week",
  "snooze.pickDate": "Pick date & time",
  "snooze.pickDateLabel": "Wake this conversation at",
  "snooze.pickDateConfirm": "Snooze",
  "snooze.pickDateInvalid": "Choose a date and time in the future.",
  "snooze.mailboxName": "Snoozed",
  /* The empty state for Pospuestos before anything has ever been snoozed. The
     folder is created on the first real snooze (GC-10), so until then there is
     a rail entry and nothing behind it — this says so without implying an
     error. */
  "snooze.emptyPlaceholder": "Snoozed messages will show up here.",
  "snooze.done": (count: number): string =>
    count === 1 ? "Conversation snoozed" : `${count} conversations snoozed`,
  "snooze.undone": "Back in your inbox",
  "snooze.unsnooze": "Unsnooze",
  "snooze.wakesAt": (when: string): string => `Wakes ${when}`,
  "snooze.returnsTo": (folder: string): string => `Returns to ${folder}`,
  "snooze.unavailable":
    "Snoozing is not available on this server.",
  "snooze.empty": "Nothing is snoozed.",
  "snooze.explain":
    "Snoozed mail leaves your inbox and comes back at the time you chose. It is a real folder, so your other mail apps see it too.",

  "mute.action": "Mute",
  "mute.unmute": "Unmute",
  "mute.done": "Muted — replies will skip your inbox",
  "mute.undone": "Unmuted",
  "mute.badge": "Muted",
  "mute.badgeExplain":
    "Replies to this conversation skip the inbox and go straight to Archive.",
  "mute.viewName": "Muted",
  "mute.empty": "No conversations are muted.",
  "mute.clientSideNotice":
    "This list is built from the muted conversations loaded in this view.",

  "schedule.action": "Schedule send",
  "schedule.menuLabel": "Send later",
  "schedule.thisAfternoon": "This afternoon",
  "schedule.tomorrowMorning": "Tomorrow morning",
  "schedule.mondayMorning": "Monday morning",
  "schedule.pickDate": "Pick date & time",
  "schedule.pickDateLabel": "Send at",
  "schedule.pickDateConfirm": "Schedule",
  "schedule.pickDateInvalid": "Choose a date and time in the future.",
  "schedule.tooFarAhead": (days: number): string =>
    `This server schedules sends up to ${days} days ahead.`,
  "schedule.scheduled": (when: string): string => `Scheduled for ${when}`,
  "schedule.viewName": "Scheduled",
  "schedule.empty": "Nothing is scheduled to be sent.",
  /* "Destacados" — the starred view (canon 07 §2). Gmail's own word in each
     locale, because the rail entry is muscle memory. */
  "starred.viewName": "Starred",
  /* The list is Inbox-scoped, and the empty state says so rather than claiming
     the account holds no starred mail at all — the server cannot answer a
     keyword filter without a folder beside it. */
  "starred.empty": "No starred messages in your inbox.",
  "schedule.explain":
    "These messages are still drafts. They go out at the time you chose, and cancelling one leaves the draft where it is.",
  "schedule.cancel": "Cancel send",
  "schedule.canceled": "Send cancelled — the message is still in Drafts",
  "schedule.sendNow": "Send now",
  "schedule.sentNow": "Sending now",
  "schedule.noRecipients": "(no recipients)",
  "schedule.overQuota": (limit: number): string =>
    `You already have ${limit} scheduled sends, which is the limit. Cancel one to schedule another.`,

  "shortcuts.snooze": "Snooze",
  "shortcuts.mute": "Mute or unmute the conversation",
  "shortcuts.goSnoozed": "Go to Snoozed",

  // --- the legal footer (AGPL-3.0 §13) ---
  //
  // The source-code link is a LICENSE OBLIGATION, not a credit: AGPL §13
  // requires that a user interacting with the program over a network be
  // offered the corresponding source. It is therefore not removable by a
  // customer's branding, and it points at the exact commit that built the
  // bundle so the offer is honest about what is running.
  "legal.poweredBy": "Powered by Moov · NU Desarrollos Conscientes",
  "legal.sourceCode": "Source code",
  "legal.sourceCommit": (commit: string): string => `Build ${commit}`,
  "legal.license": "AGPL-3.0 license",
  "legal.privacy": "Privacy",
  "legal.terms": "Terms",
  "legal.label": "Legal and license",
} as const;

/**
 * The shape every locale must satisfy: exactly the English keys, with matching
 * value types.
 *
 * Literal string types are WIDENED to `string`, while function types are kept
 * exactly. Without the widening, `as const` on the English table would make
 * each value its own literal type — and a Spanish translation would then be a
 * type error for the crime of not being the English sentence. Function values
 * keep their precise signature, which is what makes `format("...", n)` check
 * its arguments.
 */
export type Strings = {
  [K in keyof typeof en]: (typeof en)[K] extends (...args: infer A) => string
    ? (...args: A) => string
    : string;
};

/** A translation key. */
export type StringKey = keyof Strings;

/**
 * The host's brand name, as a NOMINAL type.
 *
 * A plain `string` parameter would not do. The table already holds a dozen
 * entries shaped `(name: string) => string` — `label.renameTitle`,
 * `forwarding.codeSent` — and a structural test could not tell those from a
 * brand string, so `t` would have silently swallowed keys that need a real
 * argument and rendered the brand name where a label name belonged.
 *
 * The intersection with an unexported unique symbol makes the two shapes
 * genuinely disjoint to the type system while staying a `string` at runtime:
 * `brandName(name)` is the only way in, and it is a cast, not a wrapper.
 */
declare const brandNameTag: unique symbol;
export type BrandName = string & { readonly [brandNameTag]: true };

/** Marks a name as THE brand name. Compiles to nothing. */
export function brandName(name: string): BrandName {
  return name as BrandName;
}

/** A string that names the product, and therefore takes the brand. */
export type BrandString = (brand: BrandName) => string;

/**
 * True when a value is exactly a brand string.
 *
 * The test is on the PARAMETER TUPLE, not on assignability to `BrandString`.
 * `(name: string) => string` IS assignable to `(brand: BrandName) => string` —
 * parameters are contravariant, and `BrandName` is a `string` — so the obvious
 * `extends BrandString` would have swallowed `label.renameTitle` and every
 * other one-string format key. Comparing `Parameters<T>` to `[BrandName]`
 * puts `BrandName` in a covariant position, where a plain `string` does not
 * match it and the nominal tag does its job.
 */
type IsBrandString<T> = T extends (...args: never[]) => string
  ? Parameters<T> extends [BrandName]
    ? true
    : false
  : false;

/**
 * The keys `useTranslation`'s `t` resolves on its own.
 *
 * Plain text, or text that only needs the brand — which `t` supplies. Both are
 * "plain" from a caller's point of view, and that is the distinction that
 * matters at a call site: `t(key)` returns a finished string.
 */
export type PlainKey = {
  [K in keyof Strings]: Strings[K] extends string
    ? K
    : IsBrandString<Strings[K]> extends true
      ? K
      : never;
}[keyof Strings];

/**
 * Keys whose value is a formatting function of REAL parameters.
 *
 * Brand strings are excluded deliberately: a brand string is `t`'s business,
 * and letting a call site pass its own brand would be the one way to get an
 * unbranded word on screen.
 */
export type FormatKey = {
  [K in keyof Strings]: Strings[K] extends string
    ? never
    : IsBrandString<Strings[K]> extends true
      ? never
      : K;
}[keyof Strings];

/**
 * Spanish — the pilot's language.
 *
 * Rioplatense register ("iniciá", "esperá"), matching how the product is
 * spoken about with its actual users. The typing above guarantees this object
 * is complete: removing a key here fails the build.
 */
export const es: Strings = {
  "app.skipToContent": "Saltar al contenido principal",
  "app.loading": "Cargando…",

  "login.heading": "Iniciá sesión",
  "login.subheading": "Usá tu dirección de correo completa y su contraseña.",
  "login.emailLabel": "Dirección de correo",
  "login.emailPlaceholder": "vos@ejemplo.com",
  "login.passwordLabel": "Contraseña",
  "login.submit": "Iniciar sesión",
  "login.submitting": "Iniciando sesión…",
  "login.showPassword": "Mostrar contraseña",
  "login.hidePassword": "Ocultar contraseña",
  "login.passwordShown": "La contraseña está visible",
  "login.passwordHidden": "La contraseña está oculta",
  "login.needHelp": "¿Necesitás ayuda?",
  "login.contactAdministrator": "Contactá a tu administrador",

  "login.error.emailRequired": "Ingresá tu dirección de correo.",
  "login.error.emailInvalid": "Ingresá una dirección de correo completa, con el dominio.",
  "login.error.passwordRequired": "Ingresá tu contraseña.",

  "error.invalidCredentials.title": "Ese correo y esa contraseña no coinciden",
  "error.invalidCredentials.body":
    "Revisá la dirección y la contraseña e intentá de nuevo. Usá la contraseña de este buzón, no la de otro servicio.",

  "error.notProvisioned.title": (brand: BrandName): string =>
    `Este buzón todavía no está habilitado en ${brand}`,
  "error.notProvisioned.body": (brand: BrandName): string =>
    `Tu contraseña era correcta, pero este buzón no fue dado de alta en ${brand}. Un administrador tiene que habilitarlo antes de que puedas entrar.`,

  "error.rateLimited.title": "Demasiados intentos",
  "error.rateLimited.body": "Esperá un momento antes de volver a intentar.",
  "error.rateLimited.bodyWithSeconds": (seconds: number): string =>
    `Esperá unos ${seconds} segundo${seconds === 1 ? "" : "s"} antes de volver a intentar.`,

  "error.serverError.title": (brand: BrandName): string =>
    `${brand} no está respondiendo en este momento`,
  "error.serverError.body":
    "El servidor está accesible pero no pudo completar el pedido. No es un problema de tu cuenta: intentá de nuevo en un momento.",

  "error.network.title": "No se pudo contactar al servidor",
  "error.network.body":
    "Revisá tu conexión. Si estás en una VPN o en la red de la empresa, confirmá que esté conectada.",

  "error.unknown.title": "Algo salió mal",
  "error.unknown.body":
    "Intentá de nuevo. Si sigue pasando, contactá a tu administrador.",

  "shell.mailboxes": "Carpetas",
  "shell.signOut": "Cerrar sesión",
  "shell.signedInAs": (email: string): string => `Sesión iniciada como ${email}`,

  "shell.collapseSidebar": "Contraer la lista de carpetas",
  "shell.expandSidebar": "Expandir la lista de carpetas",
  "shell.accountMenu": "Cuenta",
  "shell.help": "Ayuda y atajos de teclado",

  "quickSettings.title": "Ajustes rápidos",
  "quickSettings.seeAll": "Ver todos los ajustes",
  "quickSettings.close": "Cerrar los ajustes rápidos",

  "action.selectMenu": "Opciones de selección",
  "action.select.all": "Todos",
  "action.select.none": "Ninguno",
  "action.select.read": "Leídos",
  "action.select.unread": "No leídos",
  "action.select.starred": "Destacados",
  "action.select.unstarred": "Sin destacar",
  "list.refresh": "Actualizar",
  "list.page.range": (first: number, last: number, total: number): string =>
    `${first.toLocaleString("es")}–${last.toLocaleString("es")} de ${total.toLocaleString("es")}`,
  "list.page.rangeUnknown": (first: number, last: number): string =>
    `${first.toLocaleString("es")}–${last.toLocaleString("es")}`,
  /* B-01: la cota honesta — "de más de N" es un PISO, y el texto lo dice. */
  "list.page.rangeAtLeast": (first: number, last: number, atLeast: number): string =>
    `${first.toLocaleString("es")}–${last.toLocaleString("es")} de más de ${atLeast.toLocaleString("es")}`,
  "list.page.newer": "Más recientes",
  "list.page.older": "Más antiguos",
  /* B-05: el `⋮` de la vista — actúa sobre la LISTA, no sobre la selección.
     El alcance de "marcar como leído" está acotado a la página cargada y el
     texto lo dice, en vez de prometer una carpeta entera. */
  "list.overflow.markPageRead": "Marcar todo lo de esta página como leído",
  "list.overflow.selectMatching": "Seleccionar todo lo que coincide",
  "list.overflow.refresh": "Actualizar",
  "list.star": "Destacar",
  "list.unstar": "Quitar el destacado",

  "shell.resizePane": "Redimensionar el panel de lectura",
  "shell.resizePaneValue": (pixels: number): string => `${String(pixels)} píxeles`,

  "settings.title": "Configuración",
  "settings.open": "Configuración",
  "settings.close": "Cerrar",
  "settings.section.appearance": "Apariencia",
  "settings.theme.description": (brand: BrandName): string =>
    `Elegí cómo se ve ${brand}, o seguí el sistema.`,

  "settings.search.label": "Buscar en la configuración",
  "settings.search.placeholder": "Buscar en la configuración",
  "settings.search.clear": "Limpiar la búsqueda",
  "settings.search.empty": "Ningún ajuste coincide",
  "settings.search.emptyBody":
    "Probá con otra palabra, o limpiá la búsqueda para ver todos los ajustes.",
  "settings.nav.label": "Secciones de configuración",
  "settings.saving": "Guardando…",
  "settings.unavailable":
    "Este servidor no guarda preferencias. Tus elecciones valen solo para esta sesión.",
  "settings.saved": "Guardado ✓",
  "settings.saveFailed": "La preferencia no se pudo guardar",

  "settings.section.general": "General",
  "settings.section.inbox": "Recibidos",
  "settings.section.account": "Cuenta",
  "settings.section.filters": "Filtros",
  "settings.section.blocked": "Bloqueados",
  "settings.section.forwarding": "Reenvío",
  "settings.section.vacation": "Respuesta automática",
  "settings.section.offline": "Sin conexión",

  "settings.tab.filters": "Filtros y direcciones bloqueadas",
  "settings.backToMail": "Volver al correo",
  "settings.tabs.label": "Secciones de configuración",
  "settings.inQuickPanel":
    "Elegí esto en los ajustes rápidos, donde ves el cambio mientras lo hacés.",
  "settings.openQuickPanel": "Abrir los ajustes rápidos",

  "settings.language.label": "Idioma",
  "settings.language.description": (brand: BrandName): string =>
    `El idioma en el que está escrita la interfaz de ${brand}.`,
  "settings.language.auto": "El de mi navegador",
  "settings.language.es": "Español",
  "settings.language.en": "English",

  "settings.undoSend.label": "Deshacer envío",
  "settings.undoSend.description":
    "Cuánto espera un mensaje enviado antes de salir de verdad.",
  "settings.undoSend.seconds": (seconds: number): string => `${seconds} segundos`,

  "settings.images.label": "Imágenes remotas",
  "settings.images.description": (brand: BrandName): string =>
    `Las imágenes siempre pasan por el proxy de ${brand}, así el remitente nunca se entera de que abriste el mensaje. En Spam nunca se cargan.`,
  "settings.images.always": "Mostrar siempre",
  "settings.images.ask": "Preguntar antes de mostrar",
  "settings.images.alwaysNote": "Cada mensaje muestra sus imágenes a través del proxy.",
  "settings.images.askNote": "Un mensaje con imágenes muestra una barra que podés aceptar.",

  "settings.conversation.label": "Vista de conversación",
  "settings.conversation.description":
    "Agrupa las respuestas en una sola conversación. La lista muestra una fila por conversación, y al abrir una se ven todos sus mensajes.",
  "settings.hover.label": "Acciones al pasar el cursor",
  "settings.hover.description":
    "Mostrar los botones de archivar, borrar y leído en la fila que estás señalando.",
  "settings.autoAdvance.label": "Avance automático",
  "settings.autoAdvance.description": "Dónde quedás después de archivar o borrar un mensaje.",
  "settings.autoAdvance.list": "Volver a la lista",
  "settings.autoAdvance.newer": "Mensaje más nuevo",
  "settings.autoAdvance.older": "Mensaje más viejo",
  "settings.autoAdvance.listNote": "El lector se cierra y la lista conserva el cursor.",
  "settings.autoAdvance.newerNote": "Abre el mensaje de arriba del que acabás de sacar.",
  "settings.autoAdvance.olderNote": "Abre el de abajo — como se vacía una bandeja.",

  "settings.keyboard.label": "Atajos de teclado",
  "settings.keyboard.description":
    "El vocabulario de teclas de Gmail. La búsqueda (/) y Escape siguen andando igual.",
  "settings.snippets.label": "Fragmentos",
  "settings.snippets.description":
    "Mostrar la primera línea de cada mensaje junto al asunto.",

  "settings.density.label": "Densidad",
  "settings.density.description": "Cuánto espacio ocupa cada fila.",
  /* F-08: "Predeterminada", not "Normal". `settings.inboxType.default` right
     below already says "Predeterminada" for the same idea, and two words for
     one concept in one panel makes the user wonder what the difference is. */
  "settings.density.default": "Predeterminada",
  "settings.density.comfortable": "Cómoda",
  "settings.density.compact": "Compacta",

  "settings.readingPane.label": "Panel de lectura",
  "settings.readingPane.description": "Dónde aparece un mensaje abierto.",
  "settings.readingPane.none": "Sin dividir",
  "settings.readingPane.right": "A la derecha de la lista",
  "settings.readingPane.bottom": "Debajo de la lista",

  "settings.inboxType.label": "Tipo de bandeja",
  "settings.inboxType.description": "Qué se ordena arriba de todo en tus recibidos.",
  "settings.inboxType.default": "Predeterminada",
  "settings.inboxType.unread_first": "No leídos primero",
  "settings.inboxType.starred_first": "Destacados primero",

  "settings.notifications.label": "Notificaciones de escritorio",
  "settings.notifications.description": (brand: BrandName): string =>
    `${brand} notifica mientras esta pestaña está abierta, igual que Gmail en la web.`,
  "settings.notifications.new": "Correo nuevo",
  "settings.notifications.off": "Desactivadas",
  "settings.notifications.granted": "Tu navegador permite las notificaciones.",
  "settings.notifications.denied":
    "Tu navegador está bloqueando las notificaciones de este sitio. Habilitalas desde la barra de direcciones para activarlas.",
  "settings.notifications.pending": "Tu navegador te va a pedir permiso.",
  "settings.notifications.unsupported": "Este navegador no puede mostrar notificaciones.",
  "notification.unknownSender": "Remitente desconocido",
  "notification.noSubject": "(sin asunto)",

  "connection.offline": "Sin conexión — mostrando datos guardados",
  "connection.reconnecting": "Reconectando…",

  "offline.banner.stale":
    "Estás sin conexión. Este es el correo guardado en este dispositivo, y puede no estar actualizado.",
  "offline.search.label": (count: number): string =>
    `${String(count)} resultado${count === 1 ? "" : "s"} en el correo guardado. La búsqueda sin conexión solo alcanza lo que está en este dispositivo.`,
  "offline.body.unavailable": "Este mensaje no está guardado para leer sin conexión",
  "offline.body.unavailableBody":
    "Solo se guardan los mensajes que abriste con conexión. Va a estar acá apenas te reconectes.",
  "offline.attachments.unavailable":
    "Los adjuntos no se pueden abrir sin conexión — no se guardan en este dispositivo.",
  "offline.empty.title": "No hay correo guardado en este dispositivo",
  "offline.empty.body":
    "Estás sin conexión y todavía no se guardó nada. Conectate una vez y el correo que leas va a quedar disponible acá.",

  "outbox.name": "Bandeja de salida",
  "outbox.queued": "Esperando para enviarse",
  "outbox.sending": "Enviando…",
  "outbox.failed": "No se pudo enviar",
  "outbox.retry": "Reintentar",
  "outbox.discard": "Descartar",
  "outbox.queuedToast":
    "Sin conexión — el mensaje quedó en la bandeja de salida y va a salir cuando te reconectes.",
  "outbox.queueFailed":
    "No se pudo guardar el mensaje en este dispositivo. Copiá el texto antes de cerrar esta ventana.",
  "outbox.sentToast": (count: number): string =>
    `Se ${count === 1 ? "envió" : "enviaron"} ${String(count)} mensaje${count === 1 ? "" : "s"} de la bandeja de salida`,
  "outbox.empty": "No hay nada esperando para enviarse.",
  "outbox.explain":
    "Estos mensajes se escribieron sin conexión. Salen solos apenas haya conexión.",

  "settings.identity.label": "Dirección de envío",
  "settings.identity.description": "El nombre y la dirección desde los que sale tu correo.",
  "settings.identity.missing": "Esta cuenta no tiene una identidad de envío configurada.",
  "settings.signature.label": "Firma",
  "settings.signature.description": "Se agrega a los mensajes que escribís.",
  "settings.signature.save": "Guardar la firma",
  "settings.signature.saved": "Firma guardada",
  "settings.signature.failed": "La firma no se pudo guardar",

  // --- E7: autocompletado de direcciones (canon §2.3) ---
  "settings.addressAutocomplete.label": "Autocompletado de direcciones",
  "settings.addressAutocomplete.description":
    "Las direcciones se guardan a medida que enviás y leés correo, y se sugieren cuando escribís. Las direcciones guardadas viven solo en este navegador y nunca se suben a ningún lado; esta opción de activado/desactivado es parte de tu cuenta y vale en todos tus dispositivos.",
  "settings.addressAutocomplete.on": "Guardar direcciones automáticamente",
  "settings.addressAutocomplete.off": "Yo agrego mis contactos",
  "settings.addressAutocomplete.count": (count: number): string =>
    count === 1 ? "1 dirección guardada" : `${count} direcciones guardadas`,
  "settings.addressAutocomplete.clear": "Borrar direcciones guardadas",
  "settings.addressAutocomplete.clearConfirm":
    "¿Borrar todas las direcciones guardadas en este navegador? El autocompletado arranca de cero.",
  "settings.addressAutocomplete.cleared": "Direcciones guardadas borradas",

  "settings.filters.soon": "Este servidor no ofrece filtros",
  "settings.filters.soonBody": (brand: BrandName): string =>
    `Los filtros son reglas Sieve guardadas en el servidor de correo. Esta instalación no anuncia la capacidad, así que ${brand} no tiene nada que configurar.`,
  "settings.forwarding.soon": "Este servidor no ofrece reenvío",
  "settings.forwarding.soonBody":
    "El reenvío y los remitentes bloqueados son recetas de Sieve. Esta instalación no anuncia la capacidad.",
  "settings.vacation.soon": "Este servidor no ofrece respuesta automática",
  "settings.vacation.soonBody":
    "La respuesta de ausencia es el `vacation` de Sieve, en Dovecot. Esta instalación no anuncia la capacidad.",

  // --- E6: filtros (GC-4) ---
  "filters.description": (brand: BrandName): string =>
    `Las reglas corren en el servidor de correo cuando el mensaje llega, así siguen funcionando con ${brand} cerrado. Se aplican EN ORDEN, de arriba hacia abajo.`,
  "filters.create": "Crear un filtro",
  "filters.export": "Exportar filtros",
  "filters.import": "Importar filtros",
  "filters.transferNote": (brand: BrandName): string =>
    `El archivo es el JSON propio de ${brand}, no el XML de Gmail: vuelve a entrar en otra cuenta de ${brand} y Gmail no lo lee.`,
  "filters.imported": (count: number): string =>
    count === 1 ? "Se importó 1 filtro." : `Se importaron ${String(count)} filtros.`,
  "filters.import.notJson": "Ese archivo no es JSON. Elegí el que produjo una exportación.",
  "filters.import.notOurFormat": (brand: BrandName): string =>
    `Ese archivo no es una exportación de filtros de ${brand}. El XML de Gmail no está soportado.`,
  "filters.import.futureVersion": (brand: BrandName): string =>
    `Ese archivo lo escribió una versión más nueva de ${brand}. Actualizá y volvé a importarlo.`,
  "filters.import.noRules": "Esa exportación no tiene ningún filtro.",
  "filters.import.badRule":
    "Uno de los filtros de ese archivo está mal formado, así que no se importó ninguno.",
  "filters.edit": "Editar",
  "filters.delete": "Eliminar",
  "filters.none": "Todavía no hay filtros.",
  "filters.deleteConfirm": (name: string): string =>
    `¿Eliminar el filtro «${name}»? El correo ya archivado queda donde está; el correo nuevo deja de filtrarse.`,
  "filters.enabled": "Activo",
  "filters.disabled": "En pausa",
  "filters.moveUp": "Subir",
  "filters.moveDown": "Bajar",
  "filters.unnamed": "(filtro sin nombre)",
  "filters.order": (position: number, total: number): string =>
    `Regla ${position} de ${total}`,
  "filters.criteria": "Cuando",
  "filters.actions": "Entonces",
  "filters.empty": "—",
  "filters.saving": "Guardando…",
  "filters.saveFailed": "El filtro no se pudo guardar",
  "filters.loadFailed": "No se pudieron cargar los filtros",

  "filters.foreignScript": (brand: BrandName): string =>
    `Las reglas de ${brand} no se están ejecutando`,
  "filters.foreignScriptBody": (brand: BrandName): string =>
    `Hay otro script Sieve activo en el servidor, así que estas reglas existen pero no filtran tu correo. ${brand} nunca borra un script que no escribió: al activar las reglas de ${brand}, el otro queda guardado, solo deja de ser el activo.`,
  "filters.activate": (brand: BrandName): string => `Activar las reglas de ${brand}`,
  "filters.activating": "Activando…",
  "filters.activateFailed": "No se pudieron activar las reglas",
  "filters.activated": (brand: BrandName): string => `Las reglas de ${brand} están activas`,

  "filters.builder.newTitle": "Filtro nuevo",
  "filters.builder.editTitle": "Editar el filtro",
  "filters.builder.name": "Nombre",
  "filters.builder.namePlaceholder": "Facturas",
  "filters.builder.criteriaLegend": "Cuando llegue un mensaje que cumpla",
  "filters.builder.actionsLegend": "Hacer esto",
  "filters.builder.from": "De",
  "filters.builder.to": "Para o Cc",
  "filters.builder.subject": "Asunto",
  "filters.builder.sizeOver": "Más grande que",
  "filters.builder.sizeUnder": "Más chico que",
  "filters.builder.attachment": "Adjunto",
  "filters.builder.attachmentAny": "No importa",
  "filters.builder.attachmentYes": "Tiene adjunto",
  "filters.builder.attachmentNo": "No tiene adjunto",
  "filters.builder.moveTo": "Mover a la carpeta",
  "filters.builder.moveToNone": "Dejarlo donde está",
  "filters.builder.labels": "Aplicar etiquetas",
  "filters.builder.markRead": "Marcarlo como leído",
  "filters.builder.star": "Destacarlo",
  "filters.builder.forward": "Reenviar a",
  "filters.builder.forwardNone": "No reenviar",
  "filters.builder.forwardHint":
    "Solo las direcciones verificadas pueden recibir correo reenviado. Agregá una en Reenvío primero.",
  "filters.builder.delete": "Mover a la papelera",
  "filters.builder.neverSpam": "Nunca marcarlo como spam",
  "filters.builder.stop": "Dejar de aplicar las reglas siguientes",
  "filters.builder.save": "Guardar",
  "filters.builder.cancel": "Cancelar",
  "filters.builder.noDateNote": (brand: BrandName): string =>
    `No hay condición por fecha ni condición de búsqueda libre: Sieve no tiene equivalente, así que ${brand} se restringe en lugar de fingir. Para eso está la búsqueda.`,
  "filters.builder.multiHint": "Uno por línea.",

  "filters.problem.noCriteria": "Agregá al menos una condición.",
  "filters.problem.noActions": "Agregá al menos una acción.",
  "filters.problem.moveAndDelete":
    "Un filtro puede mover el mensaje O mandarlo a la papelera, no las dos cosas.",
  "filters.problem.forwardUnverified":
    "Esa dirección de reenvío no está verificada. Verificala en Reenvío primero.",
  "filters.problem.forwardNotAddress": "Eso no es una dirección de correo.",
  "filters.problem.blockedNeedsAddress": "Un remitente bloqueado necesita una dirección.",
  "filters.problem.blockedNotAddress": "Eso no es una dirección de correo.",
  "filters.problem.controlCharacters": "Sacá los saltos de línea y los caracteres de control.",
  "filters.problem.negativeSize": "Un tamaño no puede ser negativo.",

  // --- E6: bloqueados (canon §2.2) ---
  "blocked.description":
    "El correo de estas direcciones va directo a Spam. Bloquear no te da de baja de ninguna lista.",
  "blocked.none": "No bloqueaste a nadie.",
  "blocked.add": "Bloquear una dirección",
  "blocked.addressLabel": "Dirección",
  "blocked.addPlaceholder": "remitente@ejemplo.com",
  "blocked.remove": "Desbloquear",
  "blocked.removeConfirm": (address: string): string =>
    `¿Desbloquear a ${address}? Su correo vuelve a tu bandeja de entrada.`,
  "blocked.invalid": "Escribí una dirección de correo completa.",
  "blocked.duplicate": "Esa dirección ya está bloqueada.",
  "blocked.action": "Bloquear al remitente",
  "blocked.dialogTitle": (address: string): string => `¿Bloquear a ${address}?`,
  "blocked.dialogBody":
    "Todo el correo futuro de esta dirección va a Spam. El correo que ya está en tu bandeja queda donde está.",
  "blocked.dialogUnsubscribe":
    "Este mensaje ofrece un enlace para darte de baja. Si es un boletín al que te suscribiste, darte de baja es la solución más limpia: bloquear no te da de baja.",
  "blocked.confirm": "Bloquear",
  "blocked.blocked": (address: string): string => `${address} está bloqueado`,
  "blocked.failed": "No se pudo bloquear al remitente",

  // --- E6: respuesta automática (canon §2.8) ---
  "vacation.description":
    "Una respuesta automática mientras estás afuera. Le contesta a cada remitente como mucho una vez cada cuatro días, y nunca le contesta a listas de correo ni al spam.",
  "vacation.enable": "Enviar una respuesta automática",
  "vacation.from": "Primer día",
  "vacation.to": "Último día",
  "vacation.dateHint":
    "La respuesta arranca a las 00:00 y termina a las 23:59 de esos días, en tu propia zona horaria. Dejá el campo vacío para no poner límite.",
  "vacation.subject": "Asunto",
  "vacation.subjectPlaceholder": "Fuera de la oficina",
  "vacation.body": "Mensaje",
  "vacation.save": "Guardar",
  "vacation.saving": "Guardando…",
  "vacation.saved": "Respuesta automática guardada",
  "vacation.saveFailed": "La respuesta automática no se pudo guardar",
  "vacation.loadFailed": "No se pudo cargar la respuesta automática",
  "vacation.htmlNote": (brand: BrandName): string =>
    `Esta respuesta tiene un cuerpo HTML puesto desde otro lado. ${brand} edita la versión de texto y deja el HTML intacto.`,
  "vacation.problem.endBeforeStart": "El último día es anterior al primero.",
  "vacation.problem.emptyMessage": "Escribí un asunto o un mensaje.",
  "vacation.problem.invalidDate": "Eso no es una fecha.",
  "vacation.problem.multilineSubject": "El asunto tiene que ser una sola línea.",

  "vacation.banner": "Tu respuesta automática está activa",
  "vacation.bannerUntil": (date: string): string =>
    `Tu respuesta automática está activa hasta el ${date}`,
  "vacation.endNow": "Finalizar ahora",
  "vacation.ending": "Finalizando…",
  "vacation.endFailed": "No se pudo apagar la respuesta automática",

  // --- E6: reenvío (canon §2.11) ---
  "forwarding.description":
    "Mandar una copia del correo que llega a otra dirección. La dirección tiene que verificarse primero: le enviamos un código.",
  "forwarding.addressesTitle": "Direcciones de destino",
  "forwarding.none": "Todavía no hay direcciones de reenvío.",
  "forwarding.add": "Agregar una dirección de reenvío",
  "forwarding.addPlaceholder": "vos@otrolado.com",
  "forwarding.adding": "Enviando el código…",
  "forwarding.addFailed": "No se pudo agregar la dirección",
  "forwarding.pending": "Esperando el código",
  "forwarding.accepted": "Verificada",
  "forwarding.verifiedOn": (date: string): string => `Verificada el ${date}`,
  "forwarding.codeSent": (address: string): string =>
    `Te enviamos un código a ${address}. Abrí ese buzón, copiá el código y pegalo acá.`,
  "forwarding.codeLabel": "Código de verificación",
  "forwarding.verify": "Verificar",
  "forwarding.verifying": "Verificando…",
  "forwarding.verified": (address: string): string => `${address} está verificada`,
  "forwarding.verifyFailed":
    "Ese código no funcionó. Puede estar mal, vencido, o ser de otra dirección.",
  "forwarding.remove": "Quitar",
  "forwarding.removeConfirm": (address: string): string =>
    `¿Quitar ${address}? Cualquier filtro que reenvíe ahí deja de funcionar.`,
  "forwarding.removeFailed": "No se pudo quitar la dirección",
  "forwarding.forwardAllTitle": "Reenviar todo el correo",
  "forwarding.forwardAllEnable": "Reenviar una copia de cada mensaje",
  "forwarding.forwardAllTo": "Reenviar a",
  "forwarding.forwardAllNeedsVerified":
    "Agregá y verificá una dirección de destino primero.",
  "forwarding.disposition": (brand: BrandName): string => `Conservar la copia de ${brand}`,
  "forwarding.dispositionKeep": "en Recibidos",
  "forwarding.dispositionArchive": "en Archivo",
  "forwarding.saveFailed": "El reenvío no se pudo guardar",
  "forwarding.loadFailed": "No se pudo cargar el reenvío",

  // --- E6: cuota (RFC 9425, canon §2.11) ---
  "quota.label": "Almacenamiento",
  "quota.description":
    "Cuánto de tu buzón está en uso, leído del servidor de correo.",
  "quota.used": (used: string, limit: string): string => `${used} de ${limit} usados`,
  "quota.percent": (percent: number): string => `${percent}% ocupado`,
  "quota.noLimit": "Este buzón no tiene límite de almacenamiento.",
  "quota.loadFailed": "No se pudo leer el almacenamiento",
  "quota.refresh": "Actualizar",

  "settings.offline.soon": "El modo sin conexión está en camino",
  "settings.offline.soonBody":
    "Leer, buscar y responder sin conexión, con el correo saliente en cola en una bandeja de salida hasta que vuelvas.",

  "settings.offlineHeaders.label": "Mensajes guardados por carpeta",
  "settings.offlineHeaders.description":
    "Cuántos mensajes recientes se guardan en este dispositivo para leer y buscar sin conexión. Más mensajes ocupan más espacio.",
  "settings.offlineBodies.label": "Cuerpos guardados",
  "settings.offlineBodies.description":
    "Cuántos mensajes abiertos conservan su texto completo sin conexión. Los cuerpos son mucho más grandes que las entradas de la lista, por eso este número es menor.",
  "settings.offlineDepth.range": (min: number, max: number): string =>
    `Entre ${String(min)} y ${String(max)}`,
  "settings.offlineDepth.invalid": (min: number, max: number): string =>
    `Ingresá un número entero entre ${String(min)} y ${String(max)}`,
  "settings.offlineDepth.attachments":
    "Los adjuntos nunca se guardan sin conexión, con ninguna configuración.",

  // --- L2-brand-admin: la pestaña «Marca» ---
  "settings.section.brand": "Marca",
  "settings.brand.description": (brand: BrandName): string =>
    `El nombre, el color y las imágenes que lleva esta instalación. Todos los que inicien sesión acá ven ${brand} tal como lo dejes.`,

  "brand.identity.heading": "Nombre y textos",
  "brand.name.label": "Nombre",
  "brand.name.description":
    "El nombre completo de esta instalación. Titula la pestaña del navegador y nombra la pantalla de inicio de sesión.",
  "brand.shortName.label": "Nombre corto",
  "brand.shortName.description": "Aparece bajo el icono instalado. Doce caracteres como máximo.",
  "brand.shortName.counter": (used: number, max: number): string =>
    `${String(used)} de ${String(max)} caracteres`,
  "brand.tagline.label": "Bajada",
  "brand.tagline.description":
    "Una línea debajo del nombre en la pantalla de inicio de sesión. Vacía no muestra ninguna.",

  "brand.links.heading": "Enlaces",
  "brand.supportUrl.label": "Soporte",
  "brand.supportUrl.description":
    "A dónde lleva «¿Necesitás ayuda?». Una dirección web o un enlace mailto:. Vacío no muestra ningún enlace.",
  "brand.privacyUrl.label": "Política de privacidad",
  "brand.privacyUrl.description":
    "Tu propia política de privacidad, en el pie. Va aparte de los enlaces de licencia y código fuente, que son nuestros y se quedan.",
  "brand.termsUrl.label": "Términos del servicio",
  "brand.termsUrl.description": "Tus propios términos, en el pie. Vacío no muestra enlace.",
  "brand.url.invalid":
    "Ingresá una dirección web que empiece con https:// o http://, o un enlace mailto:.",

  "brand.colors.heading": "Color",
  "brand.primary.label": "Color principal",
  "brand.primary.description":
    "Los botones, los enlaces y la fila seleccionada. Todo lo demás se deriva de él.",
  "brand.onPrimary.label": "Texto sobre el color principal",
  "brand.onPrimary.description":
    "Si lo dejás automático, se elige uno legible por vos. Cambialo solo si hace falta.",
  "brand.onPrimary.auto": "Automático",
  "brand.onPrimary.custom": "Elegirlo yo",
  "brand.splashFrom.label": "Degradado del inicio de sesión, desde",
  "brand.splashTo.label": "Degradado del inicio de sesión, hasta",
  "brand.color.hexLabel": (field: string): string => `${field}, en hexadecimal`,
  "brand.color.invalid": "Ingresá un color como #5b5bd6.",
  "brand.splash.automatic":
    "Si lo dejás vacío, sigue a tu color principal. Escribí un color para elegirlo vos.",
  "brand.color.effective.heading": "Lo que van a usar los botones y los enlaces",
  "brand.color.effective.chosen": "Elegido",
  "brand.color.effective.light": "Tema claro",
  "brand.color.effective.dark": "Tema oscuro",
  "brand.color.effective.containerLight": "Botones y filas, claro",
  "brand.color.effective.containerDark": "Botones y filas, oscuro",
  "brand.color.adjustedLight": (hex: string): string =>
    `Tu color se oscureció a ${hex} en el tema claro para seguir legible (4,5:1).`,
  "brand.color.adjustedDark": (hex: string): string =>
    `Tu color se aclaró a ${hex} en el tema oscuro para seguir legible (4,5:1).`,

  "brand.preview.heading": "Vista previa",
  "brand.preview.description":
    "Cómo queda el color antes de guardarlo. Nada de esta página cambia hasta que lo hagas.",
  "brand.preview.light": "Tema claro",
  "brand.preview.dark": "Tema oscuro",
  "brand.preview.compose": "Redactar",
  "brand.preview.pill": "Recibidos",
  "brand.preview.rowSubject": "Una conversación seleccionada",
  "brand.preview.rowSnippet": "La fila abierta, en el color de acento.",
  "brand.preview.button": "Botón principal",
  "brand.preview.link": "Un enlace",

  "brand.images.heading": "Imágenes",
  "brand.images.description":
    "PNG, JPEG, WebP o GIF, hasta 2 MB cada una. SVG se rechaza: es un documento que puede llevar scripts, y estas imágenes se muestran antes de que nadie haya iniciado sesión.",
  "brand.logo.label": "Logo",
  "brand.logo.description":
    "Tu logotipo, en la barra superior y en la pantalla de inicio de sesión. Cualquier forma; se ajusta por altura.",
  "brand.logoDark.label": "Logo para fondos oscuros",
  "brand.logoDark.description":
    "Una variante para el tema oscuro y el panel de inicio de sesión. Sin ella, un logo oscuro se dibuja sobre una placa clara.",
  "brand.icon.label": "Icono cuadrado",
  "brand.icon.description":
    "El icono de la app instalada y el favicon. Cuadrado, de 512×512 como mínimo — una imagen que no sea cuadrada se acepta y se recorta, y lo avisamos.",
  "brand.splash.label": "Imagen del inicio de sesión",
  "brand.splashText.label": "Mostrar nombre y bajada sobre la imagen",
  "brand.splashText.description":
    "Desactivalo cuando la imagen ya trae tu logo o tu eslogan — si no, la pantalla de inicio de sesión los muestra dos veces. Sin imagen, el nombre se muestra siempre.",
  "brand.splash.description":
    "La imagen junto al formulario de inicio de sesión. Funcionan mejor las apaisadas.",
  "brand.image.drop": "Arrastrá una imagen acá, o",
  "brand.image.choose": "elegí un archivo",
  "brand.image.chooseFor": (field: string): string => `Elegir una imagen para ${field}`,
  "brand.image.remove": "Quitar",
  "brand.image.removeFor": (field: string): string => `Quitar la imagen de ${field}`,
  "brand.image.uploading": "Subiendo…",
  "brand.image.none": "Todavía ninguna",
  "brand.image.dimensions": (width: number, height: number, bytes: string): string =>
    `${String(width)}×${String(height)}, ${bytes}`,
  "brand.image.onLight": "Sobre claro",
  "brand.image.onDark": "Sobre oscuro",
  "brand.image.tooLarge": "Esa imagen pesa más de 2 MB. Guardala más chica y probá de nuevo.",
  "brand.image.unsupported": "Ese archivo no es PNG, JPEG, WebP ni GIF.",
  "brand.icons.heading": "Iconos generados",
  "brand.icons.fromLogo": "Se hicieron con tu logo porque no hay un icono cuadrado cargado.",
  "brand.icons.fromDefault":
    "Estos son los iconos de fábrica: subí un icono cuadrado para reemplazarlos.",
  "brand.warnings.heading": "Para tener en cuenta",

  "brand.danger.heading": "Restablecer",
  "brand.reset.label": "Volver a la marca de fábrica",
  "brand.reset.description":
    "Borra el nombre, los colores y todas las imágenes. Conservás tu acceso y podés configurarla de nuevo.",
  "brand.reset.button": "Restablecer la marca",
  "brand.reset.confirm":
    "¿Restablecer esta instalación a la marca de fábrica? Se borran el nombre, los colores y las imágenes para todos los que inicien sesión acá.",
  "brand.error.network": "Eso no se pudo guardar. Revisá la conexión y probá de nuevo.",
  "brand.error.notAdmin": "Ya no administrás esta instalación.",

  "settings.sendAndArchive.label": "Mostrar «Enviar y archivar» en las respuestas",
  "settings.sendAndArchive.description":
    "Agrega un segundo botón de envío a las respuestas que archiva la conversación al enviarla.",
  "settings.replyBehavior.label": "Comportamiento de respuesta por defecto",
  "settings.replyBehavior.description":
    "Qué respuesta abren el botón y la tecla «r». «Responder a todos» sigue disponible igual, y Shift+A siempre responde a todos.",
  "settings.replyBehavior.reply": "Responder",
  "settings.replyBehavior.replyAll": "Responder a todos",
  "settings.replyBehavior.replyNote": "Le contesta solo al remitente.",
  "settings.replyBehavior.replyAllNote": "Le contesta a todos los destinatarios del mensaje.",

  "settings.signatures.label": "Firmas",
  "settings.signatures.description":
    "Varias firmas con nombre, y con cuál arrancan los mensajes nuevos y las respuestas. Se guardan en tu cuenta, así que te siguen a todos tus dispositivos.",
  "settings.signatures.add": "Nueva firma",
  "settings.signatures.namePlaceholder": "Nombre",
  "settings.signatures.bodyPlaceholder": "Texto de la firma",
  "settings.signatures.delete": "Borrar",
  "settings.signatures.deleteConfirm": (name: string): string =>
    `¿Borrar la firma «${name}»? Los mensajes ya enviados no se tocan.`,
  "settings.signatures.forNew": "Para mensajes nuevos",
  "settings.signatures.forReply": "Para respuestas y reenvíos",
  "settings.signatures.none": "Ninguna (usar la firma de arriba)",
  "settings.signatures.empty": "Todavía no hay firmas con nombre.",
  "settings.signatures.full": (max: number): string =>
    `${String(max)} firmas es el máximo.`,
  "settings.signatures.textOnly":
    "Acá las firmas se editan como texto plano. El formato puesto en otro lado se conserva, no se muestra.",

  "theme.label": "Tema",
  "theme.light": "Claro",
  "theme.dark": "Oscuro",
  "theme.system": "Sistema",

  "mailbox.inbox": "Bandeja de entrada",
  "mailbox.drafts": "Borradores",
  "mailbox.sent": "Enviados",
  "mailbox.archive": "Archivo",
  "mailbox.junk": "Spam",
  "mailbox.trash": "Papelera",
  "mailbox.all": "Todo el correo",
  "mailbox.flagged": "Destacados",
  "mailbox.unreadCount": (count: number): string =>
    `${count} mensaje${count === 1 ? "" : "s"} sin leer`,
  "mailbox.itemCount": (count: number): string =>
    `${count} elemento${count === 1 ? "" : "s"}`,
  "mailbox.collapse": "Contraer carpeta",
  "mailbox.expand": "Expandir carpeta",
  "mailbox.loadFailed": "No se pudieron cargar tus carpetas",
  "mailbox.retry": "Reintentar",
  "mailbox.more": "Más",
  "mailbox.less": "Menos",
  "mailbox.customSuffix": "carpeta",

  "list.loading": "Cargando mensajes…",
  "list.empty": "No hay nada acá",
  "list.emptyBody": "Esta carpeta no tiene mensajes.",
  "list.emptySearch": "Sin coincidencias",
  "list.emptySearchBody": (query: string): string =>
    `Nada coincidió con «${query}». Probá con menos palabras u otras distintas.`,
  "list.label": "Lista de mensajes",
  "list.selectMessage": "Elegí un mensaje para leerlo",
  "list.selectMessageBody":
    "Elegí una conversación de la lista, o usá j y k para recorrerla.",
  "list.attachment": "Tiene un adjunto",
  "list.flagged": "Destacado",
  "list.unread": "Sin leer",
  "list.threadSize": (count: number): string => `${count} mensajes en esta conversación`,
  "list.threadSizeInWindow": (count: number): string =>
    `${count} mensajes de esta conversación en estos resultados`,
  "list.noSubject": "(sin asunto)",
  "list.unknownSender": "(remitente desconocido)",
  "list.truncated": (shown: number): string =>
    `Se muestran las ${shown} conversaciones más recientes. Este servidor todavía no puede paginar más allá: usá la búsqueda para encontrar correo más viejo.`,
  "list.truncatedSearch": (shown: number): string =>
    `Se muestran las ${shown} coincidencias más recientes. Puede haber más: agregá palabras para acotar la búsqueda.`,
  "list.loadFailed": "No se pudieron cargar estos mensajes",

  "search.label": "Buscar correo",
  "search.placeholder": "Buscar correo",
  "search.clear": "Limpiar la búsqueda",
  "search.clearView": "Salir de esta vista",
  "search.searching": "Buscando…",
  "search.resultCount": (count: number): string =>
    `${count} resultado${count === 1 ? "" : "s"}`,
  "search.inMailbox": "En esta carpeta",
  "search.everywhere": "Todo el correo",
  "search.unsupported": "Este servidor no puede responder esa búsqueda",
  "search.unsupportedBody": (brand: BrandName): string =>
    `La búsqueda de ${brand} cubre texto, remitente, destinatario y asunto, y se puede acotar a una carpeta y a un rango de fechas. Otras condiciones todavía no están disponibles.`,

  "reader.from": "De",
  "reader.to": "Para",
  "reader.cc": "Cc",
  "reader.bcc": "Cco",
  "reader.replyTo": "Responder a",
  "reader.date": "Fecha",
  "reader.me": "mí",
  "reader.toPhrase": "para",
  "reader.subject": "Asunto",
  "reader.showDetails": "Mostrar detalles",
  "reader.hideDetails": "Ocultar detalles",
  "reader.close": "Volver a la lista",
  "reader.loading": "Cargando el mensaje…",
  "reader.loadFailed": "No se pudo cargar este mensaje",
  "reader.attachments": (count: number): string =>
    `${count} adjunto${count === 1 ? "" : "s"}`,
  "reader.download": "Descargar",
  "reader.downloadMessage": "Descargar el mensaje original",
  "reader.downloading": "Preparando la descarga…",
  "reader.downloadFailed": "La descarga no se inició. Intentá de nuevo.",
  "reader.threadContext": (count: number): string =>
    `Conversación con ${count} mensajes`,
  "reader.expandAll": "Expandir todo",
  "reader.collapseAll": "Contraer todo",
  "reader.positionOf": (index: number, total: number): string =>
    `${index.toLocaleString("es")} de ${total.toLocaleString("es")}`,
  "reader.removeFromFolder": (name: string): string => `Quitar de ${name}`,
  "reader.threadLoadFailed":
    "No se pudo cargar el resto de esta conversación. Abajo se muestra el mensaje que abriste.",
  "reader.emptyBody": "Este mensaje no tiene contenido de texto.",
  "reader.bodyTruncated":
    "Este mensaje es largo y se acortó. Descargá el original para leerlo completo.",
  "reader.htmlFrameTitle": "Contenido del mensaje",
  "reader.imagesBlocked": (count: number): string =>
    count === 1
      ? "1 imagen remota está oculta para proteger tu privacidad."
      : `${count} imágenes remotas están ocultas para proteger tu privacidad.`,
  "reader.showImages": "Mostrar imágenes",
  "reader.showTrimmed": "Mostrar el contenido recortado",
  "reader.hideTrimmed": "Ocultar el contenido recortado",
  "reader.showWholeMessage": "Mostrar el mensaje completo",
  "reader.showLess": "Mostrar menos",
  "reader.imagesLoading": "Cargando imágenes a través del proxy de privacidad…",
  "reader.imagesFailed":
    "Las imágenes no se pudieron cargar a través del proxy de privacidad, así que siguen ocultas. Probá más tarde.",
  "reader.inlineImagesUnavailable": (count: number): string =>
    count === 1
      ? "1 imagen incrustada no se puede mostrar."
      : `${count} imágenes incrustadas no se pueden mostrar.`,
  "reader.htmlSanitizeFailed": "La versión con formato no se puede mostrar de forma segura",
  "reader.htmlSanitizeFailedBody": (brand: BrandName): string =>
    `El formato de este mensaje no se pudo hacer seguro para mostrar, así que ${brand} no lo muestra. La versión de texto plano, cuando el remitente incluyó una, se muestra abajo; el mensaje original se puede descargar completo.`,
  "reader.parseFailed": (brand: BrandName): string =>
    `${brand} no pudo leer el contenido de este mensaje`,
  "reader.parseFailedBody":
    "El mensaje está guardado a salvo y se puede descargar completo, pero no se pudo interpretar su estructura.",

  "shortcuts.title": "Atajos de teclado",
  "shortcuts.close": "Cerrar",
  "shortcuts.disabled":
    "Los atajos de teclado están desactivados en Configuración. Escape y / siguen andando, así podés cerrar esto y llegar a la búsqueda.",
  "shortcuts.open": "Abrir el mensaje",
  "shortcuts.next": "Conversación siguiente",
  "shortcuts.previous": "Conversación anterior",
  "shortcuts.conversationNext": "Mensaje siguiente de esta conversación",
  "shortcuts.conversationPrevious": "Mensaje anterior de esta conversación",
  "shortcuts.expandAll": "Expandir todos los mensajes de la conversación",
  "shortcuts.collapseAll": "Contraer la conversación",
  "shortcuts.back": "Volver a la lista",
  "shortcuts.search": "Buscar",
  "shortcuts.searchLeave": "Salir de la caja de búsqueda (el texto queda)",
  "shortcuts.searchRun": "Ejecutar la búsqueda",
  "shortcuts.searchSuggestions": "Recorrer las sugerencias",
  "shortcuts.archive": "Archivar",
  "shortcuts.delete": "Eliminar",
  "shortcuts.flag": "Destacar",
  "shortcuts.selectRow": "Seleccionar esta conversación",
  "shortcuts.compose": "Escribir un mensaje nuevo",
  "shortcuts.reply": "Responder",
  "shortcuts.replyAll": "Responder a todos",
  "shortcuts.forward": "Reenviar",
  "shortcuts.markRead": "Marcar como leído",
  "shortcuts.markUnread": "Marcar como no leído",
  "shortcuts.focusToolbar": "Mover el foco a la barra de acciones",
  "shortcuts.moreActions": "Abrir el menú de más acciones",
  "shortcuts.send": "Enviar el mensaje",
  "shortcuts.focusCc": "Agregar o enfocar Cc",
  "shortcuts.focusBcc": "Agregar o enfocar Cco",
  "shortcuts.goInbox": "Ir a la Bandeja de entrada",
  "shortcuts.goSent": "Ir a Enviados",
  "shortcuts.goDrafts": "Ir a Borradores",
  "shortcuts.goArchive": "Ir a Archivo",
  "shortcuts.goTrash": "Ir a la Papelera",
  "shortcuts.help": "Mostrar esta ayuda",
  "shortcuts.sectionNavigate": "Moverse",
  "shortcuts.sectionActions": "Actuar sobre el correo",
  "shortcuts.sectionSelection": "Seleccionar",
  "shortcuts.sectionCompose": "Redactar",
  "shortcuts.sectionJump": "Saltar a una carpeta",
  "action.notYet": "Esta acción llega en la próxima versión",

  // --- E2: el resto del vocabulario de triage de Gmail ---
  "shortcuts.spam": "Marcar como spam, o quitar el spam",
  "shortcuts.undo": "Deshacer la última acción",
  "shortcuts.archiveNext": "Archivar e ir al mensaje siguiente",
  "shortcuts.archivePrevious": "Archivar e ir al mensaje anterior",
  "shortcuts.markUnreadFromHere": "Marcar como no leídos de acá para abajo",
  "shortcuts.selectAll": "Seleccionar todas las conversaciones",
  "shortcuts.selectNone": "Limpiar la selección",
  "shortcuts.selectRead": "Seleccionar los leídos",
  "shortcuts.selectUnread": "Seleccionar los no leídos",
  "shortcuts.selectStarred": "Seleccionar los destacados",
  "shortcuts.selectUnstarred": "Seleccionar los que no están destacados",
  "shortcuts.labelAs": "Etiquetar como",

  // --- P3: acciones sobre mensajes ---
  "action.markRead": "Marcar como leído",
  "action.markUnread": "Marcar como no leído",
  "action.flag": "Destacar",
  "action.unflag": "Quitar el destaque",
  "action.archive": "Archivar",
  "action.delete": "Mover a la Papelera",
  "action.deleteForever": "Eliminar definitivamente",
  "action.move": "Mover a",
  "action.moveTo": "Mover a una carpeta",
  "action.reply": "Responder",
  "action.replyAll": "Responder a todos",
  "action.forward": "Reenviar",
  "action.more": "Más acciones",
  "action.selectAll": "Seleccionar todo",
  "action.clearSelection": "Limpiar la selección",
  "action.selected": (count: number): string =>
    count === 1 ? "1 seleccionado" : `${count} seleccionados`,
  "action.selectRow": "Seleccionar esta conversación",
  "action.undo": "Deshacer",

  "dialog.confirmTitle": "¿Estás seguro?",
  "dialog.promptTitle": "Ingresá un valor",
  "dialog.confirm": "Confirmar",
  "dialog.cancel": "Cancelar",
  "dialog.ok": "Aceptar",
  "action.confirmDeleteForever": (count: number): string =>
    count === 1
      ? "¿Eliminar este mensaje definitivamente? No se puede deshacer."
      : `¿Eliminar estos ${count} mensajes definitivamente? No se puede deshacer.`,
  "action.confirm": "Eliminar definitivamente",
  "action.cancel": "Cancelar",
  "action.failedTitle": "Esa acción no se aplicó",
  "action.failedRestored": "No cambió nada en el servidor; la lista quedó como estaba.",
  "action.partialFailure": (done: number, failed: number): string =>
    `${done} se aplicaron y ${failed} fallaron. Los que fallaron quedaron como estaban.`,
  "action.doneArchived": (count: number): string =>
    count === 1 ? "Archivado" : `${count} archivados`,
  "action.doneDeleted": (count: number): string =>
    count === 1 ? "Movido a la Papelera" : `${count} movidos a la Papelera`,
  "action.doneDeletedForever": (count: number): string =>
    count === 1 ? "Eliminado definitivamente" : `${count} eliminados definitivamente`,
  "action.doneMoved": (folder: string): string => `Movido a ${folder}`,

  // --- E2: spam, deshacer, el lector completo y vaciar la papelera ---
  "action.spam": "Marcar como spam",
  "action.notSpam": "No es spam",
  "action.doneSpam": (count: number): string =>
    count === 1 ? "Marcado como spam" : `${count} marcados como spam`,
  "action.doneNotSpam": (count: number): string =>
    count === 1 ? "Devuelto a la bandeja de entrada" : `${count} devueltos a la bandeja de entrada`,
  "action.undoDone": "Se deshizo la acción",
  "action.undoFailed": "No se pudo deshacer",
  "action.undoExpired": "No hay nada para deshacer",
  "action.emptyTrash": "Vaciar la papelera",
  "action.emptyTrashConfirm": (count: number): string =>
    count === 1
      ? "¿Eliminar definitivamente el mensaje de la Papelera? No se puede deshacer."
      : `¿Eliminar definitivamente los ${count} mensajes de la Papelera? No se puede deshacer.`,
  "action.emptyTrashEmpty": "La Papelera ya está vacía",
  "action.emptyTrashDone": (count: number): string =>
    count === 1 ? "1 mensaje eliminado definitivamente" : `${count} mensajes eliminados definitivamente`,
  "action.emptyTrashWorking": "Vaciando la Papelera…",
  "action.print": "Imprimir",
  "action.viewOriginal": "Ver original",
  "action.next": "Mensaje siguiente",
  "action.previous": "Mensaje anterior",
  "action.unsubscribe": "Cancelar la suscripción",

  // --- E2: las superficies nuevas del lector ---
  "reader.spamBanner": "Este mensaje está en Spam",
  "reader.spamBannerBody": (brand: BrandName): string =>
    `${brand} lo muestra porque lo pediste, y mantiene sus imágenes y enlaces inertes. Si no corresponde que esté acá, marcalo como que no es spam.`,
  "reader.spamImagesBlocked":
    "Las imágenes nunca se cargan en un mensaje que está en Spam.",
  // --- E10: correo sospechoso fuera de Spam (canon §4.1.15) ---
  "reader.suspiciousBanner": "Este mensaje parece spam",
  "reader.suspiciousBannerBody":
    "El filtro de correo lo marcó, pero una regla o configuración lo mantuvo fuera de Spam. Sus imágenes remotas quedan ocultas; todo lo demás funciona con normalidad.",
  "reader.originalTitle": "Mensaje original",
  "reader.originalHeaders": "Encabezados, tal como llegaron",
  "reader.originalLoading": "Cargando el original…",
  "reader.originalFailed": "No se pudo cargar el mensaje original",
  "reader.copy": "Copiar al portapapeles",
  "reader.copied": "Copiado",
  "reader.copyFailed": "No se pudo copiar. Seleccioná el texto y copialo a mano.",
  "reader.unsubscribeFrom": (list: string): string => `Cancelar la suscripción a ${list}`,
  "reader.unsubscribeOpensTab": "Abre la página del remitente en una pestaña nueva",
  "reader.unsubscribeLatency":
    "El remitente puede tardar unos días en dejar de enviar.",

  // --- P3: el compositor ---
  // La palabra exacta de Gmail en es-419 (canon 07 §2). "Escribir" era un
  // sinónimo razonable y por eso mismo estaba mal: la etiqueta es memoria
  // muscular, y Gmail nunca muestra esa palabra.
  "compose.new": "Redactar",
  "compose.title": "Mensaje nuevo",
  "compose.titleReply": "Responder",
  "compose.titleForward": "Reenviar",
  "compose.titleDraft": "Borrador",
  "compose.from": "De",
  "compose.to": "Para",
  "compose.cc": "Cc",
  "compose.bcc": "Cco",
  "compose.showCc": "Agregar Cc",
  "compose.showBcc": "Agregar Cco",
  "compose.subject": "Asunto",
  "compose.subjectPlaceholder": "Asunto",
  "compose.body": "Mensaje",
  "compose.send": "Enviar",
  "compose.sending": "Enviando…",
  "compose.discard": "Descartar",
  "compose.close": "Cerrar el compositor",
  "compose.minimize": "Minimizar",
  "compose.expand": "Expandir",
  "compose.maximize": "Pantalla completa",
  "compose.restore": "Salir de pantalla completa",
  "compose.attach": "Adjuntar un archivo",
  "compose.attachments": (count: number): string =>
    count === 1 ? "1 adjunto" : `${count} adjuntos`,
  "compose.removeAttachment": (name: string): string => `Quitar ${name}`,
  "compose.removeRecipient": (address: string): string => `Quitar ${address}`,
  "compose.recipientCount": (count: number): string =>
    count === 1 ? "1 destinatario" : `${count} destinatarios`,
  "compose.uploading": (percent: number): string => `Subiendo… ${percent}%`,
  "compose.uploadFailed": "Este archivo no se pudo adjuntar",
  "compose.plainText": "Texto plano",
  "compose.richText": "Formato",
  "compose.formatOptions": "Opciones de formato",
  "compose.bold": "Negrita",
  "compose.italic": "Cursiva",
  "compose.underline": "Subrayado",
  "compose.bulletList": "Lista con viñetas",
  "compose.orderedList": "Lista numerada",
  "compose.link": "Insertar un enlace",
  "compose.emoji": "Insertar un emoji",
  "compose.signature": "Insertar una firma",
  "compose.insertImage": "Insertar una imagen",
  "compose.insertImageHint": "Insertar una imagen (se adjunta, no queda incrustada en el cuerpo)",
  "compose.linkPrompt": "Dirección del enlace",
  "compose.linkInvalid":
    "Un enlace tiene que ser una dirección web (http, https) o una dirección de correo.",
  "compose.linkTitle": "Insertar enlace",
  "compose.linkPlaceholder": "ejemplo.com",
  "compose.addressInvalid": (address: string): string =>
    `${address} no es una dirección de correo completa.`,
  "compose.noRecipients": "Agregá al menos un destinatario antes de enviar.",
  "compose.attributionLine": (date: string, sender: string): string =>
    `El ${date}, ${sender} escribió:`,
  "compose.forwardedHeader": "---------- Mensaje reenviado ----------",
  "compose.forwardedFrom": "De",
  "compose.forwardedDate": "Fecha",
  "compose.forwardedSubject": "Asunto",
  "compose.forwardedTo": "Para",

  // --- E7: identidad y envío diario (canon §2.3) ---
  "compose.suggestions.label": "Direcciones sugeridas",
  "compose.more": "Más opciones",
  "compose.plainTextMode": "Modo texto sin formato",
  "compose.plainTextWarning":
    "Pasar a texto sin formato le saca el formato a este mensaje. El texto queda.",
  "send.andArchive": "Enviar y archivar",
  "send.andArchiveHint": "Envía esta respuesta y archiva la conversación",
  "send.sentAndArchived": "Mensaje enviado — conversación archivada",
  "send.archiveFailed": "El mensaje se envió, pero la conversación no se pudo archivar",
  "send.canceledUnarchived": "Envío cancelado — la conversación volvió a tu bandeja",
  "compose.blockedExtension": (name: string): string =>
    `${name} no se adjuntó: este tipo de archivo está bloqueado por presentar un riesgo de seguridad.`,
  "compose.blockedExtensionHint":
    "Para mandarlo, ponelo en un .zip primero, o compartilo con un enlace.",

  "action.forwardAsAttachment": "Reenviar como adjunto",
  "forwardAttachment.preparing": "Preparando el mensaje…",
  "forwardAttachment.failed": "El mensaje no se pudo adjuntar",
  "forwardAttachment.tooLarge": (limit: string): string =>
    `Estos mensajes superan el límite de ${limit} y no se adjuntaron.`,
  "forwardAttachment.subject": (count: number): string =>
    count === 1 ? "Mensaje reenviado" : `${count} mensajes reenviados`,

  // --- P3: borradores ---
  "draft.saving": "Guardando…",
  "draft.saved": "Borrador guardado",
  "draft.unsaved": "Cambios sin guardar",
  "draft.saveFailed": "El borrador no se pudo guardar",
  "draft.discardConfirm": "¿Descartar este borrador? Se pierde lo que escribiste.",
  "draft.discarded": "Borrador descartado",
  "draft.discardFailed": "El borrador no se pudo descartar",

  // --- P3: envío, con deshacer ---
  "send.undoWindow": (seconds: number): string => `Enviando en ${seconds}s`,
  "send.undo": "Deshacer",
  "send.sent": "Mensaje enviado",
  "send.canceled": "Envío cancelado — el mensaje no se transmitió",
  "send.failedTitle": "El mensaje no se envió",
  "send.cannotUnsend": "Ya es tarde para deshacer: el mensaje ya salió.",
  "send.sizeExceeded": (limit: string): string =>
    `Este archivo supera los ${limit} que acepta este servidor.`,
  "send.attachmentsExceeded": (limit: string): string =>
    `Los adjuntos suman más de los ${limit} que puede llevar un mensaje.`,

  // --- P3: carpetas ---
  "folder.create": "Carpeta nueva",
  "folder.name": "Nombre de la carpeta",
  "folder.createFailed": "La carpeta no se pudo crear",

  // --- E8: etiquetas bajo el techo de 26 keywords (GC-5) ---
  "list.emptyLabel": "Nada con esa etiqueta",
  "list.emptyLabelBody": (name: string): string =>
    `Todavía ningún mensaje tiene «${name}». Seleccioná correo y usá «Etiquetar como» para aplicarla.`,
  "label.plural": "Etiquetas",
  "label.labelAs": "Etiquetar como",
  "label.manage": "Administrar etiquetas…",
  "label.none": "Todavía no hay etiquetas",
  "label.emptyTitle": "Creá tu primera etiqueta",
  "label.emptyBody":
    "Una etiqueta cruza carpetas: un mensaje puede llevar varias, y al hacer clic en una ves todo lo que la lleva. Arrancá desde un ejemplo o escribí tu propio nombre.",
  "label.emptyExamples": "Por ejemplo:",
  "label.example.invoices": "Facturas",
  "label.example.travel": "Viajes",
  "label.example.followUp": "Pendiente",
  "label.useExample": (name: string): string => `Usar «${name}» como nombre`,

  "label.colorName": (hue: string, step: string): string => `${hue} · ${step}`,
  "label.step.pale": "Suave",
  "label.step.bold": "Intenso",
  "label.hue.slate": "Gris",
  "label.hue.red": "Rojo",
  "label.hue.orange": "Naranja",
  "label.hue.amber": "Ámbar",
  "label.hue.lime": "Lima",
  "label.hue.green": "Verde",
  "label.hue.teal": "Verde azulado",
  "label.hue.cyan": "Cian",
  "label.hue.blue": "Azul",
  "label.hue.indigo": "Índigo",
  "label.hue.purple": "Violeta",
  "label.hue.pink": "Rosa",
  "folders.heading": "Carpetas",
  "folders.help":
    "Las carpetas que tu cliente de correo crea para sincronizar calendarios, contactos o errores de sincronización quedan ocultas del riel por defecto. No se borra nada: mostrá acá la que quieras.",
  "folders.unavailable":
    "Este servidor todavía no puede guardar la elección, así que no sobreviviría a una recarga. El riel sigue ocultando las carpetas que reconoce.",
  "folders.visibility": "En el riel de carpetas",
  "folders.none": "Sin carpetas",
  "label.more": (count: number): string => `+${count}`,
  "label.openLabel": (name: string): string => `Ver todo lo etiquetado como «${name}»`,
  "label.create": "Etiqueta nueva",
  "label.name": "Nombre de la etiqueta",
  "label.color": "Color",
  "label.rename": "Renombrar",
  "label.delete": "Eliminar",
  "label.visibility": "En la barra lateral",
  "label.visibility.show": "Mostrar",
  "label.visibility.showIfUnread": "Mostrar si hay sin leer",
  "label.visibility.hide": "Ocultar",
  "label.applied": "Etiqueta aplicada",
  "label.removed": "Etiqueta quitada",

  "label.budget": (available: number, ceiling: number): string =>
    `${available} de ${ceiling} disponibles`,
  "label.budgetFull": "No queda lugar para más etiquetas",
  "label.budgetExplained":
    "Una carpeta guarda 26 keywords IMAP duraderas, y las etiquetas las comparten con las marcas que ponen otros clientes de correo. Las carpetas no tienen ese límite: usá una para lo que archivás y una etiqueta para lo que marcás.",
  "label.createFolderInstead": "Crear una carpeta en su lugar",

  "label.error.empty": "Poné un nombre para la etiqueta.",
  "label.error.tooLong": "Ese nombre es demasiado largo.",
  "label.error.reserved": "Ese nombre está reservado por el sistema de correo.",
  "label.error.duplicate": "Ya existe una etiqueta con ese nombre.",
  "label.error.control": "Ese nombre tiene caracteres que un servidor de correo no puede guardar.",
  "label.error.full": "No queda ninguna keyword libre para una etiqueta nueva.",

  "label.renameTitle": (name: string): string => `Renombrar «${name}»`,
  "label.deleteTitle": (name: string): string => `Eliminar «${name}»`,
  "label.deleteConfirm": (name: string): string =>
    `¿Quitar «${name}» de todos los mensajes que la tienen? Los mensajes no se borran.`,
  "label.migrating": (done: number): string => `${done} mensajes actualizados…`,
  "label.migrateDone": (done: number): string => `${done} mensajes actualizados`,
  "label.migrateIncomplete": (done: number): string =>
    `${done} mensajes actualizados — algunos todavía tienen la etiqueta anterior. Ejecutalo de nuevo para terminar.`,
  "label.migrateAborted": (done: number): string =>
    `Se detuvo después de ${done} mensajes. El resto conserva la etiqueta anterior.`,
  "label.migrateFailed": "La etiqueta no se pudo cambiar",
  "label.abort": "Detener",

  "label.roams":
    "Los colores y la visibilidad en la barra lateral se guardan en tu cuenta, así que te siguen a todos tus dispositivos.",

  "settings.section.labels": "Etiquetas",
  "settings.labels.description":
    "Las etiquetas son keywords IMAP, así que cruzan carpetas — y una carpeta guarda solo 26.",

  // --- E3: el lenguaje de búsqueda (canon §2.5) ---
  "search.options": "Opciones de búsqueda",
  "search.options.open": "Mostrar opciones de búsqueda",
  "search.options.close": "Ocultar opciones de búsqueda",
  "search.options.from": "De",
  "search.options.to": "Para",
  "search.options.subject": "Asunto",
  "search.options.words": "Contiene las palabras",
  "search.options.size": "Tamaño",
  "search.options.sizeLarger": "mayor que",
  "search.options.sizeSmaller": "menor que",
  "search.options.sizeUnit": "Unidad",
  "search.options.dateWithin": "Fecha dentro de",
  "search.options.dateOf": "de",
  "search.options.dateAnchor": "Fecha alrededor de la cual buscar",
  "search.options.noExcludeNote":
    "No hay campo para excluir palabras: este servidor no puede responder una búsqueda por todo lo que NO coincide, así que el campo solo podría fallar.",
  "search.options.hasAttachment": "Tiene adjunto",
  "search.options.scope": "Buscar en",
  "search.options.scopeDefault": "Todo el correo (salvo Spam y Papelera)",
  "search.options.scopeAll": "Todo el correo, con Spam y Papelera",
  "search.options.submit": "Buscar",
  "search.options.reset": "Limpiar",
  "search.options.createFilter": "Crear filtro",
  "search.options.createFilterUnusable":
    "Agregá primero una condición de remitente, destinatario, asunto, tamaño o adjunto — un filtro sin ninguna coincidiría con todos los mensajes.",
  "search.options.filterDrops": (fields: string): string =>
    `Un filtro no puede trasladar esto: ${fields}.`,
  "search.within.1d": "1 día",
  "search.within.3d": "3 días",
  "search.within.1w": "1 semana",
  "search.within.2w": "2 semanas",
  "search.within.1m": "1 mes",
  "search.within.2m": "2 meses",
  "search.within.6m": "6 meses",
  "search.within.1y": "1 año",

  "search.chip.from": "De",
  "search.chip.to": "Para",
  "search.chip.anyTime": "Cualquier fecha",
  "search.chip.hasAttachment": "Con adjunto",
  "search.chip.isUnread": "Sin leer",
  "search.chip.last7": "Últimos 7 días",
  "search.chip.last30": "Últimos 30 días",
  "search.chip.last90": "Últimos 90 días",
  "search.chip.remove": (name: string): string => `Quitar el filtro ${name}`,
  "search.chips.label": "Filtros de búsqueda",

  "search.suggestions.label": "Sugerencias de búsqueda",
  "search.suggestions.recent": "Búsquedas recientes",
  "search.suggestions.labels": "Etiquetas",
  "search.suggestions.operators": "Operadores de búsqueda",
  "search.suggestions.values": "Valores",
  "search.suggestions.messages": "Mensajes",
  "search.allResults": (query: string): string => `Todos los resultados para «${query}»`,
  "search.context": (query: string, scope: string): string =>
    `Resultados de «${query}» en ${scope}`,
  "search.contextCount": (total: number): string => `· ${total.toLocaleString("es")}`,
  "search.contextCountAtLeast": (atLeast: number): string =>
    `· más de ${atLeast.toLocaleString("es")}`,
  "search.allResultsKey": "Enter",
  "search.quick.label": "Búsquedas rápidas",
  "search.quick.sentByMe": "Enviados por mí",
  "search.suggestions.clearRecent": "Borrar búsquedas recientes",

  "search.refused.title": "Una parte de esa búsqueda no se pudo usar",
  "search.refused.negation": (operator: string): string =>
    `«${operator}» — este servidor no puede buscar la AUSENCIA de una dirección o una palabra, solo su presencia.`,
  "search.refused.deferred": (operator: string): string =>
    `«${operator}» todavía no está soportado.`,
  "search.refused.badValue": (operator: string): string =>
    `«${operator}» no tenía un valor que el servidor pudiera leer.`,
  "search.problem.needsTextOrFolder":
    "Agregá una palabra para buscar, o elegí una carpeta: filtros como «is:unread» no se pueden responder solos.",
  "search.problem.labelNeedsText":
    "Una búsqueda por etiqueta no se puede acotar a una carpeta. Sacá la carpeta, o agregá una palabra para buscar.",
  "search.problem.unknownMailbox": (name: string): string =>
    `No hay ninguna carpeta que se llame «${name}».`,
  "search.problem.tooManyBranches": (count: number): string =>
    `${count} alternativas unidas con OR son más de las que este servidor responde de una vez. Usá 4 como máximo.`,
  "search.problem.branchNotAnswerable": (index: number): string =>
    `La alternativa ${index} necesita una palabra o una carpeta propia — un OR solo amplía una búsqueda, nunca la acota.`,
  "search.approximate.folded": (fields: string): string =>
    `${fields} se buscaron en todo el mensaje, no solo en esos encabezados.`,
  "search.scopeEverything": "Incluyendo Spam y Papelera",
  "search.zeroResults": "Ningún mensaje coincide",
  "search.zeroResultsBody":
    "Se buscaron todos los términos. Probá sacando alguno, o buscá en todo el correo incluyendo Spam y Papelera.",

  // --- E4: posponer, silenciar y programar el envío (canon §2.2 y §2.3) ---
  "snooze.action": "Posponer",
  "snooze.menuLabel": "Posponer hasta",
  "snooze.laterToday": "Más tarde hoy",
  "snooze.tomorrow": "Mañana",
  "snooze.thisWeekend": "Este fin de semana",
  "snooze.nextWeek": "La próxima semana",
  "snooze.pickDate": "Elegir fecha y hora",
  "snooze.pickDateLabel": "Volver a mostrar esta conversación el",
  "snooze.pickDateConfirm": "Posponer",
  "snooze.pickDateInvalid": "Elegí una fecha y hora futuras.",
  "snooze.mailboxName": "Pospuestos",
  "snooze.emptyPlaceholder": "Los correos pospuestos aparecerán acá.",
  "snooze.done": (count: number): string =>
    count === 1 ? "Conversación pospuesta" : `${count} conversaciones pospuestas`,
  "snooze.undone": "De vuelta en tu bandeja",
  "snooze.unsnooze": "Traer ahora",
  "snooze.wakesAt": (when: string): string => `Vuelve ${when}`,
  "snooze.returnsTo": (folder: string): string => `Vuelve a ${folder}`,
  "snooze.unavailable": "Este servidor no permite posponer mensajes.",
  "snooze.empty": "No hay nada pospuesto.",
  "snooze.explain":
    "El correo pospuesto sale de tu bandeja y vuelve a la hora que elegiste. Es una carpeta real, así que tus otras apps de correo también la ven.",

  "mute.action": "Silenciar",
  "mute.unmute": "Dejar de silenciar",
  "mute.done": "Silenciada — las respuestas van a saltear tu bandeja",
  "mute.undone": "Ya no está silenciada",
  "mute.badge": "Silenciada",
  "mute.badgeExplain":
    "Las respuestas de esta conversación saltean la bandeja y van directo a Archivo.",
  "mute.viewName": "Silenciadas",
  "mute.empty": "No hay conversaciones silenciadas.",
  "mute.clientSideNotice":
    "Esta lista se arma con las conversaciones silenciadas que están cargadas en esta vista.",

  "schedule.action": "Programar envío",
  "schedule.menuLabel": "Enviar más tarde",
  "schedule.thisAfternoon": "Esta tarde",
  "schedule.tomorrowMorning": "Mañana a la mañana",
  "schedule.mondayMorning": "El lunes a la mañana",
  "schedule.pickDate": "Elegir fecha y hora",
  "schedule.pickDateLabel": "Enviar el",
  "schedule.pickDateConfirm": "Programar",
  "schedule.pickDateInvalid": "Elegí una fecha y hora futuras.",
  "schedule.tooFarAhead": (days: number): string =>
    `Este servidor programa envíos hasta ${days} días adelante.`,
  "schedule.scheduled": (when: string): string => `Programado para ${when}`,
  "schedule.viewName": "Programados",
  "schedule.empty": "No hay nada programado para enviarse.",
  "starred.viewName": "Destacados",
  "starred.empty": "No hay mensajes destacados en tu bandeja de entrada.",
  "schedule.explain":
    "Estos mensajes siguen siendo borradores. Salen a la hora que elegiste, y si cancelás uno el borrador se queda donde está.",
  "schedule.cancel": "Cancelar envío",
  "schedule.canceled": "Envío cancelado — el mensaje sigue en Borradores",
  "schedule.sendNow": "Enviar ahora",
  "schedule.sentNow": "Enviando ahora",
  "schedule.noRecipients": "(sin destinatarios)",
  "schedule.overQuota": (limit: number): string =>
    `Ya tenés ${limit} envíos programados, que es el límite. Cancelá uno para programar otro.`,

  "shortcuts.snooze": "Posponer",
  "shortcuts.mute": "Silenciar o dejar de silenciar la conversación",
  "shortcuts.goSnoozed": "Ir a Pospuestos",

  // --- the legal footer (AGPL-3.0 §13) ---
  "legal.poweredBy": "Con tecnología de Moov · NU Desarrollos Conscientes",
  "legal.sourceCode": "Código fuente",
  "legal.sourceCommit": (commit: string): string => `Build ${commit}`,
  "legal.license": "Licencia AGPL-3.0",
  "legal.privacy": "Privacidad",
  "legal.terms": "Términos",
  "legal.label": "Legal y licencia",
};

/** The locales the app ships with. */
export const locales = { en, es } as const;

/** A supported locale tag. */
export type Locale = keyof typeof locales;

/** The pilot ships in Spanish; `en` is the fallback for everything else. */
export const DEFAULT_LOCALE: Locale = "es";

/**
 * Picks the best locale for a browser's language list.
 *
 * Matches on the PRIMARY subtag, so "es-AR", "es-419" and "es" all resolve to
 * Spanish — a user whose browser says es-AR must not fall back to English over
 * a region code.
 */
export function resolveLocale(
  languages: readonly string[],
  available: Readonly<Record<string, Strings>> = locales,
): Locale {
  for (const language of languages) {
    const primary = language.toLowerCase().split("-")[0];
    if (primary !== undefined && primary in available) {
      return primary as Locale;
    }
  }
  return DEFAULT_LOCALE;
}
