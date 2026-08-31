import { useEffect, useId, useRef, useState } from "react";

import { useTranslation } from "../../i18n/I18nProvider";
import type { VacationResponse } from "../../mail/filters";
import {
  localDayEnd,
  localDayStart,
  utcDateToLocalDate,
  validateVacation,
  type VacationDraft,
  type VacationProblem,
} from "../../mail/vacationWindow";
import type { PlainStringKey } from "./registry";
import styles from "./FiltersSection.module.css";

/**
 * The vacation responder (L3 epic E6, canon §2.8).
 *
 * # An explicit save, like the signature and unlike every preference row
 *
 * The rest of the settings sheet saves optimistically on each gesture, because
 * each gesture IS a complete decision. This form is not: it is a date range plus
 * prose, and autosaving it would mean a half-typed sentence is briefly the reply
 * every correspondent receives. The signature row set that precedent for exactly
 * the same reason, and this one has a stronger version of it — a vacation reply
 * is transmitted on the user's behalf without them present.
 *
 * # The dates, and whose semantics they are
 *
 * `<input type="date">`, not `datetime-local`. Gmail's responder is configured
 * in DAYS ("starts 12:00 AM, ends 11:59 PM"), and the hour-level control would
 * be offering a precision the product does not have — plus the two boundaries
 * are then computed rather than typed, which is what makes them correct.
 * `mail/vacationWindow.ts` carries the mapping and the reasoning; this component
 * only calls it.
 *
 * # HTML is read, never written
 *
 * `htmlBody` exists on the wire and the server sanitizes it. This form edits the
 * TEXT body only and never sends `htmlBody` in a patch, so a responder whose
 * HTML was configured elsewhere keeps it. The note says so on screen, because a
 * user editing the text and seeing an HTML reply go out would otherwise think
 * the save failed.
 */

export interface VacationSectionProps {
  readonly vacation: VacationResponse;
  /** Saves the patch; resolves true when the server accepted it. */
  readonly onSave: (patch: Partial<Omit<VacationResponse, "htmlBody">>) => Promise<boolean>;
  readonly error?: string | undefined;
}

export function VacationSection({
  vacation,
  onSave,
  error,
}: VacationSectionProps): React.JSX.Element {
  const { t } = useTranslation();
  const idPrefix = useId();
  const [draft, setDraft] = useState<VacationDraft>(() => draftFrom(vacation));
  const [problems, setProblems] = useState<readonly VacationProblem[]>([]);
  const [state, setState] = useState<"idle" | "saving" | "saved" | "failed">("idle");

  /*
   * The server's object arrives asynchronously and must be adopted when it
   * does — but must not stomp on what the user has typed since. The same
   * guard the signature row uses, keyed on the identity of the loaded object
   * rather than on a field, because every field here is editable.
   */
  const loaded = useRef<VacationResponse | undefined>(undefined);
  useEffect(() => {
    if (loaded.current === vacation) return;
    loaded.current = vacation;
    setDraft(draftFrom(vacation));
  }, [vacation]);

  const patch = (next: Partial<VacationDraft>): void => {
    setDraft((current) => ({ ...current, ...next }));
    setProblems([]);
    setState("idle");
  };

  const submit = (): void => {
    const found = validateVacation(draft);
    if (found.length > 0) {
      setProblems(found);
      return;
    }
    setState("saving");
    void onSave({
      isEnabled: draft.isEnabled,
      fromDate: draft.fromDate === "" ? null : (localDayStart(draft.fromDate) ?? null),
      toDate: draft.toDate === "" ? null : (localDayEnd(draft.toDate) ?? null),
      // The empty string is the wire's "unset": the server maps it to null
      // itself (`patchNullableString`), so sending "" is not a special case
      // here — it is the documented way to clear a field.
      subject: draft.subject.trim() === "" ? null : draft.subject,
      textBody: draft.textBody.trim() === "" ? null : draft.textBody,
    }).then((ok) => {
      setState(ok ? "saved" : "failed");
    });
  };

  return (
    <form
      className={styles.wrap}
      aria-label={t("settings.section.vacation")}
      onSubmit={(event) => {
        event.preventDefault();
        submit();
      }}
    >
      <p className={styles.explain}>{t("vacation.description")}</p>

      {error !== undefined && (
        <p className={styles.error} role="alert">
          {error}
        </p>
      )}

      <label className={styles.check}>
        <input
          type="checkbox"
          role="switch"
          checked={draft.isEnabled}
          onChange={(event) => {
            patch({ isEnabled: event.target.checked });
          }}
        />
        <span>{t("vacation.enable")}</span>
      </label>

      <div className={styles.sizeRow}>
        <div className={styles.field}>
          <label className={styles.fieldLabel} htmlFor={`${idPrefix}-from`}>
            {t("vacation.from")}
          </label>
          <input
            id={`${idPrefix}-from`}
            type="date"
            className={styles.input}
            value={draft.fromDate}
            onChange={(event) => {
              patch({ fromDate: event.target.value });
            }}
          />
        </div>
        <div className={styles.field}>
          <label className={styles.fieldLabel} htmlFor={`${idPrefix}-to`}>
            {t("vacation.to")}
          </label>
          <input
            id={`${idPrefix}-to`}
            type="date"
            className={styles.input}
            value={draft.toDate}
            onChange={(event) => {
              patch({ toDate: event.target.value });
            }}
          />
        </div>
      </div>

      {/*
        The timezone note, on screen. The server keeps no per-account timezone
        and honors the instants we send; the day boundaries are therefore ours,
        and a user in UTC−3 deserves to know that "hasta el 14" means their own
        23:59, not somebody else's.
      */}
      <p className={styles.note}>{t("vacation.dateHint")}</p>

      <div className={styles.field}>
        <label className={styles.fieldLabel} htmlFor={`${idPrefix}-subject`}>
          {t("vacation.subject")}
        </label>
        <input
          id={`${idPrefix}-subject`}
          type="text"
          className={styles.input}
          value={draft.subject}
          placeholder={t("vacation.subjectPlaceholder")}
          onChange={(event) => {
            patch({ subject: event.target.value });
          }}
        />
      </div>

      <div className={styles.field}>
        <label className={styles.fieldLabel} htmlFor={`${idPrefix}-body`}>
          {t("vacation.body")}
        </label>
        <textarea
          id={`${idPrefix}-body`}
          className={styles.textarea}
          rows={5}
          value={draft.textBody}
          onChange={(event) => {
            patch({ textBody: event.target.value });
          }}
        />
      </div>

      {vacation.htmlBody !== null && (
        <p className={styles.note}>{t("vacation.htmlNote")}</p>
      )}

      {problems.length > 0 && (
        <ul className={styles.problems} role="alert">
          {problems.map((problem) => (
            <li key={problem}>{t(VACATION_PROBLEM_KEYS[problem])}</li>
          ))}
        </ul>
      )}

      <div className={styles.builderActions}>
        <button type="submit" className={styles.primary} disabled={state === "saving"}>
          {state === "saving" ? t("vacation.saving") : t("vacation.save")}
        </button>
        <span className={styles.hint} role="status">
          {state === "saved"
            ? t("vacation.saved")
            : state === "failed"
              ? t("vacation.saveFailed")
              : ""}
        </span>
      </div>
    </form>
  );
}

/** The server's object as the form's fields hold it. */
function draftFrom(vacation: VacationResponse): VacationDraft {
  return {
    isEnabled: vacation.isEnabled,
    fromDate: utcDateToLocalDate(vacation.fromDate),
    toDate: utcDateToLocalDate(vacation.toDate),
    subject: vacation.subject ?? "",
    textBody: vacation.textBody ?? "",
  };
}

const VACATION_PROBLEM_KEYS: Readonly<Record<VacationProblem, PlainStringKey>> = {
  endBeforeStart: "vacation.problem.endBeforeStart",
  emptyMessage: "vacation.problem.emptyMessage",
  invalidDate: "vacation.problem.invalidDate",
  multilineSubject: "vacation.problem.multilineSubject",
};
