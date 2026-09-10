import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import {
  BrandAdminClient,
  BrandAdminError,
  type AssetKind,
  type BrandAdminDoc,
  type BrandPatch,
} from "./adminApi";
import { useBrandingRefresh } from "./refreshContext";

/**
 * The brand-admin controller: the probe, the document, and the four writes.
 *
 * # Why the probe is cached for the session
 *
 * "May this user administer this host" is answered by Mailcow's domain-admin
 * list, which changes on the timescale of an operator running a command — not
 * on the timescale of a settings tab being opened. Probing on every mount would
 * put a request on the path of every visit to Settings, for an answer that is
 * "no" for almost everyone and stable for the rest. So it is asked ONCE, after
 * the credential is known, and the answer lives as long as the page does.
 *
 * It is never asked on the LOGIN screen, and that is a rule rather than an
 * optimisation: there is no credential yet, so the request could only be
 * unauthenticated, and an unauthenticated probe of an admin route is an
 * enumeration oracle for "which hosts have brand administrators".
 *
 * # Why every write re-reads the brand
 *
 * The panel is the one place in the app where a user changes what the app
 * LOOKS like. Leaving the old accent on screen after a successful save reads as
 * the save having failed, so each write ends by asking the provider to re-read
 * `GET /branding` — which repaints the whole app through the same code path the
 * boot fetch uses. `applyBranding` stays the single writer of the seeds.
 *
 * # Errors are held, not thrown
 *
 * Every write resolves `true` or `false` and records the failure. A rejected
 * promise would have to be caught at each of a dozen call sites in the section,
 * and the thing the section actually needs — a sentence to show, and which
 * field the server named — is exactly what {@link BrandAdminError} carries.
 */

export interface BrandAdminState {
  /** `undefined` until the probe answers, or when this user is not an admin. */
  readonly doc: BrandAdminDoc | undefined;
  /** The last failure, as a {@link BrandAdminError} kind, or undefined. */
  readonly error: BrandAdminError | undefined;
  readonly save: (patch: BrandPatch) => Promise<boolean>;
  readonly uploadAsset: (kind: AssetKind, file: File) => Promise<boolean>;
  readonly removeAsset: (kind: AssetKind) => Promise<boolean>;
  readonly reset: () => Promise<boolean>;
}

export interface UseBrandAdminOptions {
  /**
   * The `Authorization` header value, or "" when there is no session.
   *
   * Empty short-circuits everything: no probe, no client, no tab. That is what
   * keeps this off the login screen without the hook having to know which
   * screen it is on.
   */
  readonly authorization: string;
  readonly fetchImpl?: typeof fetch;
}

export function useBrandAdmin({
  authorization,
  fetchImpl,
}: UseBrandAdminOptions): BrandAdminState {
  const [doc, setDoc] = useState<BrandAdminDoc | undefined>(undefined);
  const [error, setError] = useState<BrandAdminError | undefined>(undefined);
  const refreshBranding = useBrandingRefresh();

  const client = useMemo(
    () =>
      authorization === ""
        ? undefined
        : new BrandAdminClient({
            authorization,
            ...(fetchImpl !== undefined ? { fetchImpl } : {}),
          }),
    [authorization, fetchImpl],
  );

  /*
   * The session cache. A ref rather than state because reading it must not
   * cause a render, and it is keyed by the AUTHORIZATION so signing in as
   * somebody else asks again — a cache that survived a user change would show
   * one person's tab to the next.
   */
  const probed = useRef<string | undefined>(undefined);

  const load = useCallback(
    async (signal?: AbortSignal): Promise<void> => {
      if (client === undefined) return;
      try {
        const probe = await client.probe(signal);
        if (probe === undefined) {
          // Not an administrator. Not an error: it is the normal answer, and
          // the tab simply does not exist.
          setDoc(undefined);
          setError(undefined);
          return;
        }
        setDoc(await client.get(signal));
        setError(undefined);
      } catch (thrown) {
        if (thrown instanceof DOMException && thrown.name === "AbortError") return;
        setDoc(undefined);
        setError(
          thrown instanceof BrandAdminError
            ? thrown
            : new BrandAdminError("network", "the brand could not be loaded"),
        );
      }
    },
    [client],
  );

  useEffect(() => {
    if (client === undefined || probed.current === authorization) return undefined;
    probed.current = authorization;
    const controller = new AbortController();
    void load(controller.signal);
    return () => {
      controller.abort();
    };
  }, [authorization, client, load]);

  /**
   * The one path every write takes: run it, keep the returned document, repaint
   * the app, and turn a failure into held state rather than a rejection.
   *
   * The server answers each write with the WHOLE document, so the local copy is
   * replaced rather than patched — which is what makes `warnings`, `iconUrls`
   * and `iconSource` correct after an upload without a second request.
   */
  const run = useCallback(
    async (operation: () => Promise<BrandAdminDoc>): Promise<boolean> => {
      try {
        const next = await operation();
        setDoc(next);
        setError(undefined);
        await refreshBranding();
        return true;
      } catch (thrown) {
        setError(
          thrown instanceof BrandAdminError
            ? thrown
            : new BrandAdminError("network", "the change could not be saved"),
        );
        return false;
      }
    },
    [refreshBranding],
  );

  const save = useCallback(
    (patch: BrandPatch) =>
      client === undefined ? Promise.resolve(false) : run(() => client.update(patch)),
    [client, run],
  );

  const uploadAsset = useCallback(
    (kind: AssetKind, file: File) =>
      client === undefined ? Promise.resolve(false) : run(() => client.putAsset(kind, file)),
    [client, run],
  );

  const removeAsset = useCallback(
    (kind: AssetKind) =>
      client === undefined ? Promise.resolve(false) : run(() => client.deleteAsset(kind)),
    [client, run],
  );

  const reset = useCallback(
    () => (client === undefined ? Promise.resolve(false) : run(() => client.reset())),
    [client, run],
  );

  return { doc, error, save, uploadAsset, removeAsset, reset };
}
