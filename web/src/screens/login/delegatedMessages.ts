import type { DelegatedFailure } from "../../auth/AuthProvider";
import type { Translation } from "../../i18n/I18nProvider";

/**
 * The copy for each delegated-link failure (epic M2, contract §3.7).
 *
 * Its own module rather than a second export from the screen, so the screen
 * file exports one component and Fast Refresh keeps working — and so a test
 * can check the mapping without mounting anything.
 */

export interface LinkMessage {
  readonly title: string;
  readonly body: string;
  readonly suggestsAdministrator: boolean;
}

/**
 * Maps a failure onto its copy.
 *
 * Exhaustive over `DelegatedFailure` with no `default` branch, for the reason
 * `errorMessages.ts` states: adding a failure kind without writing its words
 * must be a compile error, not a blank screen.
 */
export function messageFor(reason: DelegatedFailure, { t, format }: Translation): LinkMessage {
  switch (reason.kind) {
    case "invalid":
      return {
        title: t("delegated.expired.title"),
        body: t("delegated.expired.body"),
        suggestsAdministrator: false,
      };
    case "not-provisioned":
      // The existing copy, verbatim: same situation, different route in.
      return {
        title: t("error.notProvisioned.title"),
        body: t("error.notProvisioned.body"),
        suggestsAdministrator: true,
      };
    case "unusable":
      return {
        title: t("delegated.unusable.title"),
        body: t("delegated.unusable.body"),
        suggestsAdministrator: true,
      };
    case "not-configured":
      /*
       * The host has no issuer configured, so this link belongs to a
       * different installation. The user cannot tell that apart from an
       * expired link and should not have to, so the copy is the same — the
       * remedy is identical.
       */
      return {
        title: t("delegated.expired.title"),
        body: t("delegated.expired.body"),
        suggestsAdministrator: false,
      };
    case "unavailable":
      return {
        title: t("delegated.unavailable.title"),
        body:
          reason.retryAfterSeconds === undefined
            ? t("delegated.unavailable.body")
            : format("delegated.unavailable.bodyWithSeconds", reason.retryAfterSeconds),
        suggestsAdministrator: false,
      };
  }
}
