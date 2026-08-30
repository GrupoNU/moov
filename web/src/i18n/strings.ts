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

  "error.notProvisioned.title": "This mailbox is not set up in Moov yet",
  "error.notProvisioned.body":
    "Your password was correct, but this mailbox has not been added to Moov. An administrator has to enable it before you can sign in.",

  "error.rateLimited.title": "Too many attempts",
  "error.rateLimited.body": "Wait a moment before trying again.",
  "error.rateLimited.bodyWithSeconds": (seconds: number): string =>
    `Wait about ${seconds} second${seconds === 1 ? "" : "s"} before trying again.`,

  "error.serverError.title": "Moov is not answering right now",
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

  // --- settings ---
  "settings.title": "Settings",
  "settings.open": "Settings",
  "settings.close": "Close",
  "settings.section.appearance": "Appearance",
  "settings.theme.description": "Choose how Moov looks, or follow your system.",

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
  "settings.saveFailed": "The preference could not be saved",

  "settings.section.general": "General",
  "settings.section.inbox": "Inbox",
  "settings.section.account": "Account",
  "settings.section.filters": "Filters",
  "settings.section.forwarding": "Forwarding",
  "settings.section.vacation": "Vacation responder",
  "settings.section.offline": "Offline",

  "settings.language.label": "Language",
  "settings.language.description": "The language Moov's interface is written in.",
  "settings.language.auto": "Match my browser",
  "settings.language.es": "Español",
  "settings.language.en": "English",

  "settings.undoSend.label": "Undo send",
  "settings.undoSend.description":
    "How long a sent message waits before it actually leaves.",
  "settings.undoSend.seconds": (seconds: number): string => `${seconds} seconds`,

  "settings.images.label": "Remote images",
  "settings.images.description":
    "Images always load through Moov's proxy, so the sender never learns you opened the message. Spam is always excluded.",
  "settings.images.always": "Always show",
  "settings.images.ask": "Ask before showing",

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
  "settings.notifications.description":
    "Moov notifies while this tab is open, like Gmail on the web does.",
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
  /*
   * The named gap, stated on screen rather than left to be discovered — E8's
   * precedent for a limitation with a known durable home.
   */
  "offline.depthPending":
    "Moov saves the 200 most recent messages per folder and the messages you open. Choosing how much to save is coming with the next preferences release.",

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

  "settings.filters.soon": "Filters arrive with the Sieve work",
  "settings.filters.soonBody":
    "Rules that label, archive, star or delete mail as it arrives — built on Dovecot's own Sieve, so they keep running when Moov is closed.",
  "settings.forwarding.soon": "Forwarding arrives with the Sieve work",
  "settings.forwarding.soonBody":
    "Forwarding to a verified address, and blocking a sender, are Sieve recipes. They land in the same epic as filters.",
  "settings.vacation.soon": "The vacation responder arrives with the Sieve work",
  "settings.vacation.soonBody":
    "An out-of-office reply with a date range, which never answers mailing lists or spam.",
  "settings.offline.soon": "Offline mode is on its way",
  "settings.offline.soonBody":
    "Read, search and reply without a connection, with outgoing mail queued in an Outbox until you are back.",

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
  "search.searching": "Searching…",
  "search.resultCount": (count: number): string =>
    `${count} result${count === 1 ? "" : "s"}`,
  "search.inMailbox": "In this folder",
  "search.everywhere": "All mail",
  // The graceful degradation the brief requires: never a silent empty list.
  "search.unsupported": "This server cannot answer that search",
  "search.unsupportedBody":
    "Moov's search covers text, sender, recipient and subject, and can be narrowed to one folder and a date range. Other conditions are not available yet.",

  // --- P2: the reading pane ---
  "reader.from": "From",
  "reader.to": "To",
  "reader.cc": "Cc",
  "reader.bcc": "Bcc",
  "reader.replyTo": "Reply to",
  "reader.date": "Date",
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
  "reader.imagesLoading": "Loading images through the privacy proxy…",
  "reader.imagesFailed":
    "The images could not be loaded through the privacy proxy, so they stay hidden. Try again later.",
  "reader.inlineImagesUnavailable": (count: number): string =>
    count === 1
      ? "1 embedded image cannot be displayed yet."
      : `${count} embedded images cannot be displayed yet.`,
  // The honest fallback: sanitization refused the whole document. Never
  // rendered silently — the user is told a formatted version exists.
  "reader.htmlSanitizeFailed": "The formatted version cannot be shown safely",
  "reader.htmlSanitizeFailedBody":
    "This message's formatting could not be made safe to display, so Moov is not showing it. The plain-text version, when the sender included one, is shown below; the original message can be downloaded in full.",
  "reader.parseFailed": "Moov could not read this message's contents",
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
  "shortcuts.archive": "Archive",
  "shortcuts.delete": "Delete",
  "shortcuts.flag": "Star",
  "shortcuts.selectRow": "Select this conversation",
  "shortcuts.compose": "Write a new message",
  "shortcuts.reply": "Reply",
  "shortcuts.replyAll": "Reply to everyone",
  "shortcuts.forward": "Forward",
  "shortcuts.toggleRead": "Mark read or unread",
  "shortcuts.goInbox": "Go to Inbox",
  "shortcuts.goSent": "Go to Sent",
  "shortcuts.goDrafts": "Go to Drafts",
  "shortcuts.goArchive": "Go to Archive",
  "shortcuts.goTrash": "Go to Trash",
  "shortcuts.help": "Show this help",
  "shortcuts.sectionNavigate": "Moving around",
  "shortcuts.sectionActions": "Acting on mail",
  "shortcuts.sectionJump": "Jumping to a folder",
  "shortcuts.comingSoon": "Arriving in the next release",
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
  "reader.spamBannerBody":
    "Moov shows it because you asked for it, and keeps its images and links inert. If it does not belong here, mark it as not spam.",
  "reader.spamImagesBlocked":
    "Images are never loaded for a message in Spam.",
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
  "compose.new": "Write",
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
  "compose.bold": "Bold",
  "compose.italic": "Italic",
  "compose.underline": "Underline",
  "compose.bulletList": "Bulleted list",
  "compose.orderedList": "Numbered list",
  "compose.link": "Insert a link",
  "compose.linkPrompt": "Address of the link",
  "compose.linkInvalid": "A link must be a web address (http, https) or an email address.",
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
  "label.none": "No labels yet",
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

  // The metadata gap, stated where the user meets it rather than in a doc.
  "label.localOnly":
    "Colours and sidebar visibility are stored in this browser, so they do not follow you to another device yet. The labels themselves, and the messages they are on, are shared everywhere.",

  "settings.section.labels": "Labels",
  "settings.labels.description":
    "Labels are IMAP keywords, so they cross folders — and a folder holds only 26 of them.",
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

  "error.notProvisioned.title": "Este buzón todavía no está habilitado en Moov",
  "error.notProvisioned.body":
    "Tu contraseña era correcta, pero este buzón no fue dado de alta en Moov. Un administrador tiene que habilitarlo antes de que puedas entrar.",

  "error.rateLimited.title": "Demasiados intentos",
  "error.rateLimited.body": "Esperá un momento antes de volver a intentar.",
  "error.rateLimited.bodyWithSeconds": (seconds: number): string =>
    `Esperá unos ${seconds} segundo${seconds === 1 ? "" : "s"} antes de volver a intentar.`,

  "error.serverError.title": "Moov no está respondiendo en este momento",
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

  "settings.title": "Configuración",
  "settings.open": "Configuración",
  "settings.close": "Cerrar",
  "settings.section.appearance": "Apariencia",
  "settings.theme.description": "Elegí cómo se ve Moov, o seguí el sistema.",

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
  "settings.saveFailed": "La preferencia no se pudo guardar",

  "settings.section.general": "General",
  "settings.section.inbox": "Recibidos",
  "settings.section.account": "Cuenta",
  "settings.section.filters": "Filtros",
  "settings.section.forwarding": "Reenvío",
  "settings.section.vacation": "Respuesta automática",
  "settings.section.offline": "Sin conexión",

  "settings.language.label": "Idioma",
  "settings.language.description": "El idioma en el que está escrita la interfaz de Moov.",
  "settings.language.auto": "El de mi navegador",
  "settings.language.es": "Español",
  "settings.language.en": "English",

  "settings.undoSend.label": "Deshacer envío",
  "settings.undoSend.description":
    "Cuánto espera un mensaje enviado antes de salir de verdad.",
  "settings.undoSend.seconds": (seconds: number): string => `${seconds} segundos`,

  "settings.images.label": "Imágenes remotas",
  "settings.images.description":
    "Las imágenes siempre pasan por el proxy de Moov, así el remitente nunca se entera de que abriste el mensaje. En Spam nunca se cargan.",
  "settings.images.always": "Mostrar siempre",
  "settings.images.ask": "Preguntar antes de mostrar",

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

  "settings.keyboard.label": "Atajos de teclado",
  "settings.keyboard.description":
    "El vocabulario de teclas de Gmail. La búsqueda (/) y Escape siguen andando igual.",
  "settings.snippets.label": "Fragmentos",
  "settings.snippets.description":
    "Mostrar la primera línea de cada mensaje junto al asunto.",

  "settings.density.label": "Densidad",
  "settings.density.description": "Cuánto espacio ocupa cada fila.",
  "settings.density.default": "Normal",
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
  "settings.notifications.description":
    "Moov notifica mientras esta pestaña está abierta, igual que Gmail en la web.",
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
  "offline.depthPending":
    "Moov guarda los 200 mensajes más recientes de cada carpeta y los que abrís. Elegir cuánto guardar llega con la próxima entrega de preferencias.",

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

  "settings.filters.soon": "Los filtros llegan con la épica de Sieve",
  "settings.filters.soonBody":
    "Reglas que etiquetan, archivan, destacan o borran el correo cuando llega — sobre el Sieve del propio Dovecot, así siguen corriendo con Moov cerrado.",
  "settings.forwarding.soon": "El reenvío llega con la épica de Sieve",
  "settings.forwarding.soonBody":
    "Reenviar a una dirección verificada, y bloquear a un remitente, son recetas de Sieve. Aterrizan en la misma épica que los filtros.",
  "settings.vacation.soon": "La respuesta automática llega con la épica de Sieve",
  "settings.vacation.soonBody":
    "Una respuesta de ausencia con rango de fechas, que nunca le contesta a listas de correo ni al spam.",
  "settings.offline.soon": "El modo sin conexión está en camino",
  "settings.offline.soonBody":
    "Leer, buscar y responder sin conexión, con el correo saliente en cola en una bandeja de salida hasta que vuelvas.",

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
  "search.searching": "Buscando…",
  "search.resultCount": (count: number): string =>
    `${count} resultado${count === 1 ? "" : "s"}`,
  "search.inMailbox": "En esta carpeta",
  "search.everywhere": "Todo el correo",
  "search.unsupported": "Este servidor no puede responder esa búsqueda",
  "search.unsupportedBody":
    "La búsqueda de Moov cubre texto, remitente, destinatario y asunto, y se puede acotar a una carpeta y a un rango de fechas. Otras condiciones todavía no están disponibles.",

  "reader.from": "De",
  "reader.to": "Para",
  "reader.cc": "Cc",
  "reader.bcc": "Cco",
  "reader.replyTo": "Responder a",
  "reader.date": "Fecha",
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
  "reader.imagesLoading": "Cargando imágenes a través del proxy de privacidad…",
  "reader.imagesFailed":
    "Las imágenes no se pudieron cargar a través del proxy de privacidad, así que siguen ocultas. Probá más tarde.",
  "reader.inlineImagesUnavailable": (count: number): string =>
    count === 1
      ? "1 imagen incrustada todavía no se puede mostrar."
      : `${count} imágenes incrustadas todavía no se pueden mostrar.`,
  "reader.htmlSanitizeFailed": "La versión con formato no se puede mostrar de forma segura",
  "reader.htmlSanitizeFailedBody":
    "El formato de este mensaje no se pudo hacer seguro para mostrar, así que Moov no lo muestra. La versión de texto plano, cuando el remitente incluyó una, se muestra abajo; el mensaje original se puede descargar completo.",
  "reader.parseFailed": "Moov no pudo leer el contenido de este mensaje",
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
  "shortcuts.archive": "Archivar",
  "shortcuts.delete": "Eliminar",
  "shortcuts.flag": "Destacar",
  "shortcuts.selectRow": "Seleccionar esta conversación",
  "shortcuts.compose": "Escribir un mensaje nuevo",
  "shortcuts.reply": "Responder",
  "shortcuts.replyAll": "Responder a todos",
  "shortcuts.forward": "Reenviar",
  "shortcuts.toggleRead": "Marcar como leído o sin leer",
  "shortcuts.goInbox": "Ir a la Bandeja de entrada",
  "shortcuts.goSent": "Ir a Enviados",
  "shortcuts.goDrafts": "Ir a Borradores",
  "shortcuts.goArchive": "Ir a Archivo",
  "shortcuts.goTrash": "Ir a la Papelera",
  "shortcuts.help": "Mostrar esta ayuda",
  "shortcuts.sectionNavigate": "Moverse",
  "shortcuts.sectionActions": "Actuar sobre el correo",
  "shortcuts.sectionJump": "Saltar a una carpeta",
  "shortcuts.comingSoon": "Llega en la próxima versión",
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
  "reader.spamBannerBody":
    "Moov lo muestra porque lo pediste, y mantiene sus imágenes y enlaces inertes. Si no corresponde que esté acá, marcalo como que no es spam.",
  "reader.spamImagesBlocked":
    "Las imágenes nunca se cargan en un mensaje que está en Spam.",
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
  "compose.new": "Escribir",
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
  "compose.bold": "Negrita",
  "compose.italic": "Cursiva",
  "compose.underline": "Subrayado",
  "compose.bulletList": "Lista con viñetas",
  "compose.orderedList": "Lista numerada",
  "compose.link": "Insertar un enlace",
  "compose.linkPrompt": "Dirección del enlace",
  "compose.linkInvalid":
    "Un enlace tiene que ser una dirección web (http, https) o una dirección de correo.",
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

  "label.localOnly":
    "Los colores y la visibilidad en la barra lateral se guardan en este navegador, así que todavía no te siguen a otro dispositivo. Las etiquetas en sí, y los mensajes que las tienen, sí se ven en todos lados.",

  "settings.section.labels": "Etiquetas",
  "settings.labels.description":
    "Las etiquetas son keywords IMAP, así que cruzan carpetas — y una carpeta guarda solo 26.",
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
