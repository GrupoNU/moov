import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { JmapClient, type BasicCredentials } from "../../api/jmap";
import { useAuth } from "../../auth/AuthProvider";
import { loadSession } from "../../auth/session";
import { useBranding } from "../../branding/BrandingProvider";
import { BrandMark } from "../../components/BrandMark";
import { ThemeToggle } from "../../components/ThemeToggle";
import { useTranslation } from "../../i18n/I18nProvider";
import {
  INITIAL_KEYBOARD_STATE,
  CHORD_TIMEOUT_MS,
  resolveShortcut,
  type KeyboardState,
  type ShortcutAction,
} from "../../keyboard/shortcuts";
import {
  fetchMailboxes,
  fetchMessageDetail,
  queryEmails,
  MailApiError,
  type MailFilter,
} from "../../mail/api";
import { mailboxSegment, resolveMailbox } from "../../mail/mailboxes";
import { isSearchable, normalizeQuery, refusalFor } from "../../mail/search";
import { groupByThread, type ThreadGroup } from "../../mail/threading";
import type { Email, Mailbox, Thread } from "../../mail/types";
import { useRouter } from "../../router/RouterProvider";
import { withMessage, type Route } from "../../router/routes";
import { MailboxList } from "./MailboxList";
import { MessageList } from "./MessageList";
import { ReadingPane } from "./ReadingPane";
import { SearchBar } from "./SearchBar";
import { ShortcutsDialog } from "./ShortcutsDialog";
import styles from "./MailScreen.module.css";

/**
 * The mail screen: the three-column shell and everything that coordinates it.
 *
 * This is the only stateful component in P2. The pure logic it orchestrates —
 * routing, mailbox ordering, thread grouping, the keyboard map, the windowing
 * maths, the debounce — all lives in tested modules, which is what keeps this
 * file about WIRING rather than about behaviour.
 */

export function MailScreen(): React.JSX.Element {
  const { state, signOut } = useAuth();
  const branding = useBranding();
  const { t, format } = useTranslation();
  const { route, navigate, replace } = useRouter();

  const session = state.status === "authenticated" ? state.session : undefined;
  const accountId = session?.primaryAccounts["urn:ietf:params:jmap:mail"] ?? "";

  /*
   * The client is rebuilt only when the credential changes, which is what makes
   * "which credential is this request using" answerable by construction (P1's
   * rule). The credential comes from the same storage AuthProvider validated at
   * sign-in — it is not re-prompted.
   */
  const client = useMemo<JmapClient | undefined>(() => {
    if (state.status !== "authenticated") return undefined;
    const stored: BasicCredentials | undefined = loadSession();
    if (stored === undefined) return undefined;
    const built = new JmapClient(stored);
    // Seed the session so apiUrl/downloadUrl come from the server's own
    // templates rather than from a guess.
    void built.fetchSession().catch(() => undefined);
    return built;
  }, [state.status]);

  const [mailboxes, setMailboxes] = useState<readonly Mailbox[]>([]);
  const [mailboxError, setMailboxError] = useState<string | undefined>(undefined);
  const [isLoadingMailboxes, setLoadingMailboxes] = useState(true);

  const [emails, setEmails] = useState<readonly Email[]>([]);
  const [isLoadingList, setLoadingList] = useState(true);
  const [listError, setListError] = useState<string | undefined>(undefined);
  const [truncated, setTruncated] = useState(false);
  const [refusal, setRefusal] = useState<string | undefined>(undefined);
  const [resultTotal, setResultTotal] = useState<number | undefined>(undefined);

  const [selectedId, setSelectedId] = useState<string | undefined>(undefined);
  const [detail, setDetail] = useState<{ email?: Email; thread?: Thread }>({});
  const [isLoadingDetail, setLoadingDetail] = useState(false);
  const [detailError, setDetailError] = useState<string | undefined>(undefined);

  const [searchText, setSearchText] = useState(
    route.kind === "search" ? route.query : "",
  );
  const [helpOpen, setHelpOpen] = useState(false);
  const [toast, setToast] = useState<string | undefined>(undefined);

  const searchInputRef = useRef<HTMLInputElement | null>(null);

  // --- mailboxes -----------------------------------------------------------

  useEffect(() => {
    if (client === undefined || accountId === "") return undefined;
    const controller = new AbortController();
    setLoadingMailboxes(true);
    void (async () => {
      try {
        const list = await fetchMailboxes(client, accountId, controller.signal);
        if (!controller.signal.aborted) {
          setMailboxes(list);
          setMailboxError(undefined);
        }
      } catch (error) {
        if (!controller.signal.aborted) {
          setMailboxError(error instanceof Error ? error.message : String(error));
        }
      } finally {
        if (!controller.signal.aborted) setLoadingMailboxes(false);
      }
    })();
    return () => {
      controller.abort();
    };
  }, [client, accountId]);

  /** The mailbox the route names, once the list has loaded. */
  const activeMailbox = useMemo<Mailbox | undefined>(() => {
    if (route.kind !== "mailbox") return undefined;
    return resolveMailbox(mailboxes, route.mailboxId);
  }, [route, mailboxes]);

  /*
   * Canonicalise the URL once mailboxes are known: `/mail/mc` becomes
   * `/mail/inbox` when mc has the inbox role. `replace`, not `navigate` — the
   * user did not choose this, so it must not cost them a Back press.
   */
  useEffect(() => {
    if (route.kind !== "mailbox" || activeMailbox === undefined) return;
    const canonical = mailboxSegment(activeMailbox);
    if (canonical !== route.mailboxId) {
      replace({ ...route, mailboxId: canonical });
    }
  }, [route, activeMailbox, replace]);

  // --- the message list ----------------------------------------------------

  /** What the current route asks the server for. */
  const filter = useMemo<MailFilter | undefined>(() => {
    if (route.kind === "search") {
      const query = normalizeQuery(route.query);
      if (!isSearchable(query)) return undefined;
      return { kind: "search", text: query };
    }
    if (activeMailbox === undefined) return undefined;
    return { kind: "mailbox", mailboxId: activeMailbox.id };
  }, [route, activeMailbox]);

  /** Identifies the list, so the virtualizer resets scroll only on a real change. */
  const listKey =
    route.kind === "search" ? `search:${normalizeQuery(route.query)}` : `mailbox:${activeMailbox?.id ?? ""}`;

  useEffect(() => {
    if (client === undefined || accountId === "" || filter === undefined) {
      if (filter === undefined && route.kind === "search") {
        // A query too short to send is not an error: show nothing, quietly.
        setEmails([]);
        setLoadingList(false);
      }
      return undefined;
    }
    const controller = new AbortController();
    setLoadingList(true);
    setRefusal(undefined);
    setListError(undefined);

    void (async () => {
      try {
        const page = await queryEmails(client, accountId, filter, {
          signal: controller.signal,
        });
        if (controller.signal.aborted) return;
        setEmails(page.emails);
        setTruncated(page.truncated);
        setResultTotal(page.total);
      } catch (error) {
        if (controller.signal.aborted) return;
        // An unsupportedFilter is a REFUSAL, not a failure: the server is
        // telling us its repertoire cannot answer this shape. Rendering it as
        // an empty list would be a lie, so it gets its own explanation.
        if (error instanceof MailApiError && error.methodError !== undefined) {
          const declined = refusalFor(
            error.methodError.type,
            error.methodError.description,
          );
          if (declined !== undefined) {
            setEmails([]);
            setRefusal(error.methodError.description ?? t("search.unsupportedBody"));
            return;
          }
        }
        setEmails([]);
        setListError(error instanceof Error ? error.message : String(error));
      } finally {
        if (!controller.signal.aborted) setLoadingList(false);
      }
    })();

    return () => {
      controller.abort();
    };
  }, [client, accountId, filter, route.kind, t]);

  const groups = useMemo(() => groupByThread(emails), [emails]);

  // Keep the selection valid as the list changes underneath it.
  useEffect(() => {
    if (groups.length === 0) {
      setSelectedId(undefined);
      return;
    }
    if (selectedId === undefined || !groups.some((group) => group.id === selectedId)) {
      setSelectedId(groups[0]?.id);
    }
  }, [groups, selectedId]);

  // --- the open message ----------------------------------------------------

  const openMessageId = route.messageId;

  useEffect(() => {
    if (client === undefined || accountId === "" || openMessageId === undefined) {
      setDetail({});
      setDetailError(undefined);
      return undefined;
    }
    const controller = new AbortController();
    setLoadingDetail(true);
    setDetailError(undefined);
    void (async () => {
      try {
        const result = await fetchMessageDetail(client, accountId, openMessageId, {
          signal: controller.signal,
        });
        if (!controller.signal.aborted) {
          setDetail({
            ...(result.email !== undefined ? { email: result.email } : {}),
            ...(result.thread !== undefined ? { thread: result.thread } : {}),
          });
        }
      } catch (error) {
        if (!controller.signal.aborted) {
          setDetailError(error instanceof Error ? error.message : String(error));
        }
      } finally {
        if (!controller.signal.aborted) setLoadingDetail(false);
      }
    })();
    return () => {
      controller.abort();
    };
  }, [client, accountId, openMessageId]);

  // --- navigation ----------------------------------------------------------

  const openGroup = useCallback(
    (group: ThreadGroup): void => {
      navigate(withMessage(route, group.latest.id));
    },
    [navigate, route],
  );

  const closeMessage = useCallback((): void => {
    navigate(withMessage(route, undefined));
  }, [navigate, route]);

  const goToMailbox = useCallback(
    (mailbox: Mailbox): void => {
      navigate({ kind: "mailbox", mailboxId: mailboxSegment(mailbox) });
      setSearchText("");
    },
    [navigate],
  );

  const runSearch = useCallback(
    (text: string): void => {
      const query = normalizeQuery(text);
      const next: Route =
        query === ""
          ? { kind: "mailbox", mailboxId: "inbox" }
          : { kind: "search", query };
      // Typing replaces rather than pushes: one Back press should leave the
      // search, not walk back through every keystroke that built it.
      replace(next);
    },
    [replace],
  );

  // --- the keyboard --------------------------------------------------------

  const keyboardRef = useRef<KeyboardState>(INITIAL_KEYBOARD_STATE);
  const chordTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  const runAction = useCallback(
    (action: ShortcutAction): void => {
      const index = groups.findIndex((group) => group.id === selectedId);
      switch (action.kind) {
        case "next": {
          const next = groups[Math.min(index + 1, groups.length - 1)];
          if (next !== undefined) setSelectedId(next.id);
          break;
        }
        case "previous": {
          const previous = groups[Math.max(index - 1, 0)];
          if (previous !== undefined) setSelectedId(previous.id);
          break;
        }
        case "open": {
          const current = groups[index];
          if (current !== undefined) openGroup(current);
          break;
        }
        case "back":
          closeMessage();
          break;
        case "focusSearch":
          searchInputRef.current?.focus();
          searchInputRef.current?.select();
          break;
        case "goToMailbox": {
          const target = mailboxes.find((mailbox) => mailbox.role === action.role);
          if (target !== undefined) goToMailbox(target);
          break;
        }
        case "help":
          setHelpOpen(true);
          break;
        case "closeOverlay":
          if (helpOpen) setHelpOpen(false);
          else if (openMessageId !== undefined) closeMessage();
          break;
        // P3 wires these. Announcing the fact is more honest than a key that
        // silently does nothing and reads as a bug.
        case "archive":
        case "delete":
        case "toggleRead":
        case "toggleFlag":
          setToast(t("action.notYet"));
          break;
      }
    },
    [groups, selectedId, openGroup, closeMessage, mailboxes, goToMailbox, helpOpen, openMessageId, t],
  );

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent): void => {
      const { action, nextState } = resolveShortcut(
        {
          key: event.key,
          ctrlKey: event.ctrlKey,
          metaKey: event.metaKey,
          altKey: event.altKey,
          shiftKey: event.shiftKey,
          target: event.target,
        },
        keyboardRef.current,
      );

      keyboardRef.current = nextState;

      // A `g` prefix expires, so a stray press cannot swallow the next real
      // keystroke indefinitely.
      if (chordTimer.current !== undefined) clearTimeout(chordTimer.current);
      if (nextState.pendingG) {
        chordTimer.current = setTimeout(() => {
          keyboardRef.current = INITIAL_KEYBOARD_STATE;
        }, CHORD_TIMEOUT_MS);
      }

      if (action === undefined) return;
      // Only now is the event ours — preventDefault after deciding, never
      // before, so unbound keys reach the browser untouched.
      event.preventDefault();
      runAction(action);
    };

    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
      if (chordTimer.current !== undefined) clearTimeout(chordTimer.current);
    };
  }, [runAction]);

  // Toasts clear themselves.
  useEffect(() => {
    if (toast === undefined) return undefined;
    const timer = setTimeout(() => {
      setToast(undefined);
    }, 2600);
    return () => {
      clearTimeout(timer);
    };
  }, [toast]);

  // --- render --------------------------------------------------------------

  const username = state.status === "authenticated" ? state.username : "";
  const isReading = openMessageId !== undefined;

  return (
    <div className={styles.shell}>
      <header className={styles.header}>
        <div className={styles.headerBrand}>
          <BrandMark branding={branding} size="sm" />
        </div>

        <SearchBar
          ref={searchInputRef}
          value={searchText}
          onChange={setSearchText}
          onSearch={runSearch}
          isSearching={isLoadingList && route.kind === "search"}
        />

        <div className={styles.headerActions}>
          <button
            type="button"
            className={styles.iconButton}
            onClick={() => {
              setHelpOpen(true);
            }}
            aria-label={t("shortcuts.title")}
            title={`${t("shortcuts.title")} (?)`}
          >
            <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" aria-hidden="true" focusable="false">
              <circle cx="10" cy="10" r="7.5" />
              <path d="M7.8 7.7a2.2 2.2 0 1 1 2.9 2.1c-.5.2-.8.6-.8 1.1v.4M10 14.2v.1" />
            </svg>
          </button>
          <ThemeToggle />
          <span className={styles.account} title={username}>
            {format("shell.signedInAs", username)}
          </span>
          <button className={styles.signOut} type="button" onClick={signOut}>
            {t("shell.signOut")}
          </button>
        </div>
      </header>

      <div className={[styles.body, isReading ? styles.reading : ""].filter(Boolean).join(" ")}>
        <nav className={styles.sidebar} aria-label={t("shell.mailboxes")}>
          {mailboxError !== undefined ? (
            <div className={styles.sidebarError}>
              <p>{t("mailbox.loadFailed")}</p>
            </div>
          ) : (
            <MailboxList
              mailboxes={mailboxes}
              selectedId={activeMailbox?.id}
              onSelect={goToMailbox}
              isLoading={isLoadingMailboxes}
            />
          )}
        </nav>

        <main className={styles.listColumn} id="main">
          <MessageList
            listKey={listKey}
            groups={groups}
            selectedId={selectedId}
            onSelect={(group) => {
              setSelectedId(group.id);
            }}
            onOpen={openGroup}
            isLoading={isLoadingList}
            notice={
              <ListNotice
                refusal={refusal}
                truncated={truncated}
                shown={groups.length}
                error={listError}
                total={resultTotal}
                isSearch={route.kind === "search"}
              />
            }
            empty={
              <EmptyState
                isSearch={route.kind === "search"}
                query={route.kind === "search" ? route.query : ""}
                hasRefusal={refusal !== undefined}
              />
            }
          />
        </main>

        {isReading && client !== undefined && (
          <aside className={styles.readerColumn} aria-label={t("list.selectMessage")}>
            <ReadingPane
              email={detail.email}
              thread={detail.thread}
              isLoading={isLoadingDetail}
              error={detailError}
              onClose={closeMessage}
              client={client}
              accountId={accountId}
            />
          </aside>
        )}
      </div>

      <ShortcutsDialog
        isOpen={helpOpen}
        onClose={() => {
          setHelpOpen(false);
        }}
      />

      {/* A single always-present live region: messages announced when they
          appear, rather than a region inserted together with its own text. */}
      <div className={styles.toast} role="status" aria-live="polite">
        {toast !== undefined && <span className={styles.toastBubble}>{toast}</span>}
      </div>
    </div>
  );
}

/** The banner above the list: a refusal, a truncation warning, or a count. */
function ListNotice({
  refusal,
  truncated,
  shown,
  error,
  total,
  isSearch,
}: {
  readonly refusal: string | undefined;
  readonly truncated: boolean;
  readonly shown: number;
  readonly error: string | undefined;
  readonly total: number | undefined;
  readonly isSearch: boolean;
}): React.JSX.Element | null {
  const { t, format } = useTranslation();

  if (error !== undefined) {
    return (
      <div className={styles.noticeError} role="alert">
        <strong>{t("list.loadFailed")}</strong>
        <span>{error}</span>
      </div>
    );
  }

  if (refusal !== undefined) {
    // The honest degradation the brief demands: never a silent empty list.
    return (
      <div className={styles.noticeWarn} role="status">
        <strong>{t("search.unsupported")}</strong>
        <span>{t("search.unsupportedBody")}</span>
      </div>
    );
  }

  if (truncated) {
    return (
      <div className={styles.noticeInfo} role="status">
        {isSearch ? format("list.truncatedSearch", shown) : format("list.truncated", shown)}
      </div>
    );
  }

  if (isSearch && total !== undefined && total > 0) {
    return <div className={styles.noticeCount}>{format("search.resultCount", total)}</div>;
  }

  return null;
}

function EmptyState({
  isSearch,
  query,
  hasRefusal,
}: {
  readonly isSearch: boolean;
  readonly query: string;
  readonly hasRefusal: boolean;
}): React.JSX.Element | null {
  const { t, format } = useTranslation();
  const branding = useBranding();

  // A refusal has its own banner; a second "nothing found" beneath it would
  // contradict it.
  if (hasRefusal) return null;

  return (
    <div className={styles.empty}>
      <BrandMark branding={branding} size="lg" iconOnly />
      <p className={styles.emptyTitle}>{isSearch ? t("list.emptySearch") : t("list.empty")}</p>
      <p className={styles.emptyBody}>
        {isSearch ? format("list.emptySearchBody", query) : t("list.emptyBody")}
      </p>
    </div>
  );
}
