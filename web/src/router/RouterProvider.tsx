import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";

import { formatRoute, parseRoute, routesEqual, type Route } from "./routes";

/**
 * The router: a subscription to the History API, and nothing more.
 *
 * `useSyncExternalStore` is deliberately NOT used here even though the browser
 * history is an external store. Its `getSnapshot` must return a referentially
 * stable value, and a parsed Route is a fresh object on every call — the
 * standard fix (memoising by URL string) reimplements the state this component
 * already holds. A `popstate` listener writing state is the same thing with
 * less ceremony.
 */

export interface RouterValue {
  readonly route: Route;
  /** Pushes a new entry: the user chose to go somewhere, and Back must return. */
  readonly navigate: (route: Route) => void;
  /**
   * Replaces the current entry: the app is correcting the URL to match state
   * the user did not choose (a role alias resolving to an id, a search whose
   * text changed by one keystroke). Pushing these would make Back require one
   * press per character typed.
   */
  readonly replace: (route: Route) => void;
  readonly back: () => void;
}

const RouterContext = createContext<RouterValue | undefined>(undefined);

export interface RouterProviderProps {
  readonly children: ReactNode;
  /** Tests supply an initial URL instead of touching window.history. */
  readonly initialUrl?: string;
}

export function RouterProvider({
  children,
  initialUrl,
}: RouterProviderProps): React.JSX.Element {
  const [route, setRoute] = useState<Route>(() =>
    parseRoute(
      initialUrl ??
        (typeof window === "undefined"
          ? "/"
          : `${window.location.pathname}${window.location.search}`),
    ),
  );

  // Back and forward. Without this listener the URL changes and the app does
  // not — the single most common way a hand-rolled router is broken.
  useEffect(() => {
    if (typeof window === "undefined") return undefined;
    const onPopState = (): void => {
      setRoute(parseRoute(`${window.location.pathname}${window.location.search}`));
    };
    window.addEventListener("popstate", onPopState);
    return () => {
      window.removeEventListener("popstate", onPopState);
    };
  }, []);

  const navigate = useCallback((next: Route): void => {
    setRoute((current) => {
      // Navigating to where you already are must not add a history entry;
      // otherwise clicking the open message five times means five Backs.
      if (routesEqual(current, next)) return current;
      if (typeof window !== "undefined") {
        window.history.pushState(null, "", formatRoute(next));
      }
      return next;
    });
  }, []);

  const replace = useCallback((next: Route): void => {
    setRoute((current) => {
      if (routesEqual(current, next)) return current;
      if (typeof window !== "undefined") {
        window.history.replaceState(null, "", formatRoute(next));
      }
      return next;
    });
  }, []);

  const back = useCallback((): void => {
    if (typeof window !== "undefined") window.history.back();
  }, []);

  const value = useMemo<RouterValue>(
    () => ({ route, navigate, replace, back }),
    [route, navigate, replace, back],
  );

  return <RouterContext.Provider value={value}>{children}</RouterContext.Provider>;
}

export function useRouter(): RouterValue {
  const context = useContext(RouterContext);
  if (context === undefined) {
    throw new Error("useRouter must be used inside a <RouterProvider>");
  }
  return context;
}
