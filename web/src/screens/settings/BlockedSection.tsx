import { useState } from "react";

import { useConfirm } from "../../components/useConfirm";
import { useTranslation } from "../../i18n/I18nProvider";
import {
  blockDraft,
  blockedRules,
  isBlockableAddress,
  isBlocked,
} from "../../mail/blockedSenders";
import type { FilterRule, FilterRuleDraft } from "../../mail/filters";
import styles from "./FiltersSection.module.css";

/**
 * Blocked senders (L3 epic E6, canon §2.2).
 *
 * # Why this is its own section over the same data
 *
 * Gmail gives blocked addresses their own place in Settings, separate from
 * Filters, even though a block is mechanically a filter — and the reason it is
 * right for us too is stronger than convention: on this server a blocked rule
 * has NO visible action. The Junk filing is compiled from the type tag, not
 * stored in an action field, so a blocked rule listed among filters would show
 * a condition and an empty "then" — a row that looks broken.
 *
 * So the rule list is sliced by type (`mail/blockedSenders.ts`) and each half
 * is rendered by the section that can describe it.
 *
 * # The one sentence that has to be here
 *
 * "Blocking does not unsubscribe you from anything" (canon §2.2 records both
 * facts on the same row). Someone blocking a newsletter is choosing the worse
 * remedy — their Spam folder fills instead of the mail stopping — and the
 * sentence is what lets them choose the other one.
 */

export interface BlockedSectionProps {
  /** The FULL rule list; the blocked slice is taken here. */
  readonly rules: readonly FilterRule[];
  readonly onBlock: (draft: FilterRuleDraft) => void;
  readonly onUnblock: (rule: FilterRule) => void;
  readonly isBusy?: boolean;
  readonly error?: string | undefined;
}

export function BlockedSection({
  rules,
  onBlock,
  onUnblock,
  isBusy = false,
  error,
}: BlockedSectionProps): React.JSX.Element {
  const { t, format } = useTranslation();
  const { confirm, dialog: confirmDialog } = useConfirm();
  const [value, setValue] = useState("");
  const [problem, setProblem] = useState<"invalid" | "duplicate" | undefined>(undefined);

  const blocked = blockedRules(rules);

  const submit = (): void => {
    const address = value.trim();
    if (!isBlockableAddress(address)) {
      setProblem("invalid");
      return;
    }
    if (isBlocked(rules, address)) {
      setProblem("duplicate");
      return;
    }
    onBlock(blockDraft(address));
    setValue("");
    setProblem(undefined);
  };

  return (
    <div className={styles.wrap}>
      {confirmDialog}

      <p className={styles.explain}>{t("blocked.description")}</p>

      {error !== undefined && (
        <p className={styles.error} role="alert">
          {error}
        </p>
      )}

      <form
        className={styles.sizeRow}
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <label className={styles.field}>
          <span className={styles.fieldLabel}>{t("blocked.add")}</span>
          {/*
            `type="text"`, not `type="email"` — the same reason ForwardingSection
            states: a native email input blocks the submit on ITS rules, so our
            message (worded to match the server's `looksLikeAddress`) never gets
            to run.
          */}
          <input
            type="text"
            inputMode="email"
            autoComplete="email"
            className={styles.input}
            value={value}
            placeholder={t("blocked.addPlaceholder")}
            disabled={isBusy}
            onChange={(event) => {
              setValue(event.target.value);
              setProblem(undefined);
            }}
          />
        </label>
        <button type="submit" className={styles.primary} disabled={isBusy}>
          {t("blocked.add")}
        </button>
      </form>

      {problem !== undefined && (
        <p className={styles.error} role="alert">
          {problem === "invalid" ? t("blocked.invalid") : t("blocked.duplicate")}
        </p>
      )}

      <ul className={styles.list} aria-label={t("settings.section.blocked")}>
        {blocked.map((rule) => {
          const address = rule.from[0] ?? rule.name;
          return (
            <li key={rule.id} className={styles.row}>
              <div className={styles.rowText}>
                <span className={styles.rowName}>{address}</span>
              </div>
              <div className={styles.rowActions}>
                <button
                  type="button"
                  className={styles.secondary}
                  disabled={isBusy}
                  onClick={() => {
                    void (async () => {
                      if (
                        !(await confirm({
                          message: format("blocked.removeConfirm", address),
                          confirmLabel: t("blocked.remove"),
                        }))
                      ) {
                        return;
                      }
                      onUnblock(rule);
                    })();
                  }}
                >
                  {t("blocked.remove")}
                </button>
              </div>
            </li>
          );
        })}

        {blocked.length === 0 && <li className={styles.empty}>{t("blocked.none")}</li>}
      </ul>
    </div>
  );
}
