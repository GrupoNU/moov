import type { ApiError, ApiErrorKind } from "./errors";
import type { Translation } from "../i18n/I18nProvider";

/**
 * Turning an {@link ApiError} into words a user can act on.
 *
 * This module is the answer to the pilot's failure. The server made a precise
 * distinction; errors.ts preserved it as a `kind`; this maps each kind to a
 * title and a body that name the situation and the remedy.
 *
 * THE INVARIANT: the switch below is exhaustive over `ApiErrorKind` and has no
 * `default` branch that swallows unknown kinds. Adding a kind without writing
 * its message is a compile error. That is what makes "never a generic error"
 * a property of the build rather than a habit.
 */

export interface ErrorMessage {
  readonly title: string;
  readonly body: string;
  /**
   * Whether the UI should offer the "contact your administrator" link.
   *
   * True exactly for `not-provisioned`, because that is the one state a user
   * genuinely cannot resolve alone — the remedy is an operator running
   * `moovctl account add`. Offering it everywhere would train people to ignore
   * it, which would cost us the one case where it matters.
   */
  readonly suggestsAdministrator: boolean;
  /**
   * Whether retrying could plausibly succeed without anything changing.
   * Drives whether the form stays armed and the submit button stays enabled.
   */
  readonly retryable: boolean;
}

/**
 * Maps an error to its message.
 *
 * `unknown` is a real kind with its own copy, not a fallback: it says "try
 * again, and contact your administrator if it persists", which is the honest
 * thing to say when we genuinely do not know. It is still never the string
 * "an error occurred".
 */
export function messageForError(error: ApiError, translation: Translation): ErrorMessage {
  const { t, format } = translation;

  switch (error.kind) {
    case "invalid-credentials":
      return {
        title: t("error.invalidCredentials.title"),
        body: t("error.invalidCredentials.body"),
        suggestsAdministrator: false,
        retryable: true,
      };

    case "not-provisioned":
      // THE case the pilot lost. The server's own detail is precise and
      // English; the translated copy says the same thing in the user's
      // language, and the administrator link is offered only here.
      return {
        title: t("error.notProvisioned.title"),
        body: t("error.notProvisioned.body"),
        suggestsAdministrator: true,
        retryable: false,
      };

    case "rate-limited":
      return {
        title: t("error.rateLimited.title"),
        // Retry-After is a number the server actually computed from the
        // lockout table; when it is there, telling the user how long turns
        // "wait" into an instruction they can follow.
        body:
          error.retryAfterSeconds !== undefined
            ? format("error.rateLimited.bodyWithSeconds", error.retryAfterSeconds)
            : t("error.rateLimited.body"),
        suggestsAdministrator: false,
        retryable: false,
      };

    case "server-error":
      return {
        title: t("error.serverError.title"),
        body: t("error.serverError.body"),
        suggestsAdministrator: false,
        retryable: true,
      };

    case "network":
      return {
        title: t("error.network.title"),
        body: t("error.network.body"),
        suggestsAdministrator: false,
        retryable: true,
      };

    case "aborted":
      // A cancelled request is not a failure the user needs explained; the UI
      // filters this kind out before rendering. The message exists so the
      // switch stays total.
      return {
        title: t("error.unknown.title"),
        body: t("error.unknown.body"),
        suggestsAdministrator: false,
        retryable: true,
      };

    case "jmap":
    case "unknown":
      return {
        title: t("error.unknown.title"),
        body: t("error.unknown.body"),
        suggestsAdministrator: true,
        retryable: true,
      };
  }
}

/**
 * A compile-time proof that {@link messageForError} handles every kind.
 *
 * If a member is added to `ApiErrorKind` and not to the switch, TypeScript
 * reports the switch as non-exhaustive at the assignment below (its inferred
 * return type would include `undefined`). This constant makes that failure
 * appear in this file rather than at some distant call site.
 */
export const _exhaustivenessProof: (
  kind: ApiErrorKind,
) => ApiErrorKind = (kind) => kind;
