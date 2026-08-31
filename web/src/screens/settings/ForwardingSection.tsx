import { useState } from "react";

import { useConfirm } from "../../components/useConfirm";
import { useTranslation } from "../../i18n/I18nProvider";
import { isBlockableAddress } from "../../mail/blockedSenders";
import {
  FORWARD_DISPOSITIONS,
  type ForwardAll,
  type ForwardDisposition,
  type ForwardingAddress,
} from "../../mail/filters";
import { formatFullDate } from "../../mail/format";
import styles from "./FiltersSection.module.css";

/**
 * Forwarding (L3 epic E6, canon §2.11 — verification flow GC-4).
 *
 * # The state machine this section IS
 *
 *   nothing → **pending** (the server mailed a code to the DESTINATION)
 *           → **accepted** (the account owner pasted the code back)
 *           → in use (a rule or the forward-all setting redirects there)
 *           → removal, which the server REFUSES while it is in use.
 *
 * Each transition is a different affordance, and the design rule is that the
 * state is never inferred from what is on screen: a pending address renders the
 * code box, an accepted one renders its verification date, and neither is
 * reachable from the other by guessing.
 *
 * # Why the code is pasted rather than clicked
 *
 * The token travels only through the destination mailbox — that is the entire
 * consent property (`internal/jmaphttp/forwarding.go`: "the requester cannot
 * produce it without the destination owner's cooperation"). The route that
 * consumes it is AUTHENTICATED as the account owner, so the destination's owner
 * clicking a link in their own browser would not be signed in as us. Relaying
 * the code into this box is therefore not a lesser version of a magic link — it
 * is the flow that matches where the two parties actually are.
 *
 * # Removing an address that rules use
 *
 * The server refuses with `forbidden` and a sentence that names the fix. That
 * sentence is surfaced verbatim rather than replaced, because only the server
 * knows whether the blocker is a filter or the forward-all setting, and the
 * confirm dialog warns BEFORE the attempt that rules pointing there will break.
 */

export interface ForwardingSectionProps {
  readonly addresses: readonly ForwardingAddress[];
  readonly forwardAll: ForwardAll;
  /** Registers a destination; the server mails the code. */
  readonly onAdd: (email: string) => Promise<boolean>;
  /** Consumes a code through the aux verify route. */
  readonly onVerify: (token: string) => Promise<boolean>;
  readonly onRemove: (address: ForwardingAddress) => void;
  readonly onSaveForwardAll: (patch: Partial<ForwardAll>) => void;
  readonly isBusy?: boolean;
  readonly error?: string | undefined;
}

export function ForwardingSection({
  addresses,
  forwardAll,
  onAdd,
  onVerify,
  onRemove,
  onSaveForwardAll,
  isBusy = false,
  error,
}: ForwardingSectionProps): React.JSX.Element {
  const { t, format, locale } = useTranslation();
  const { confirm, dialog: confirmDialog } = useConfirm();
  const [value, setValue] = useState("");
  const [addState, setAddState] = useState<"idle" | "adding" | "failed">("idle");
  const [invalid, setInvalid] = useState(false);

  const verified = addresses.filter((address) => address.state === "accepted");
  const pending = addresses.filter((address) => address.state === "pending");

  const submit = (): void => {
    const email = value.trim();
    if (!isBlockableAddress(email)) {
      setInvalid(true);
      return;
    }
    setInvalid(false);
    setAddState("adding");
    void onAdd(email).then((ok) => {
      setAddState(ok ? "idle" : "failed");
      if (ok) setValue("");
    });
  };

  return (
    <div className={styles.wrap}>
      {confirmDialog}

      <p className={styles.explain}>{t("forwarding.description")}</p>

      {error !== undefined && (
        <p className={styles.error} role="alert">
          {error}
        </p>
      )}

      {/*
        The list's accessible name is the SECTION's title, not the heading just
        above it: "Reenvío: direcciones de destino" is what a screen-reader user
        needs when they land on the list out of context, and it is also what
        lets the registry's drift scan see this row as rendered (the rail's
        heading reaches its key only through a lookup table a source scan
        cannot follow — the same mechanism LabelsSection documents).
      */}
      <h4 className={styles.builderTitle}>{t("forwarding.addressesTitle")}</h4>

      <ul
        className={styles.list}
        aria-label={`${t("settings.section.forwarding")}: ${t("forwarding.addressesTitle")}`}
      >
        {addresses.map((address) => (
          <li key={address.id} className={styles.row}>
            <div className={styles.rowText}>
              <span className={styles.rowName}>
                {address.email}
                <span className={address.state === "accepted" ? styles.paused : styles.paused}>
                  {address.state === "accepted"
                    ? t("forwarding.accepted")
                    : t("forwarding.pending")}
                </span>
              </span>
              {address.state === "accepted" && address.verifiedAt !== null && (
                <span className={styles.rowOrder}>
                  {format(
                    "forwarding.verifiedOn",
                    formatFullDate(address.verifiedAt, locale),
                  )}
                </span>
              )}
              {address.state === "pending" && (
                <VerifyBox
                  address={address}
                  onVerify={onVerify}
                  disabled={isBusy}
                />
              )}
            </div>
            <div className={styles.rowActions}>
              <button
                type="button"
                className={styles.danger}
                disabled={isBusy}
                onClick={() => {
                  void (async () => {
                    if (
                      !(await confirm({
                        message: format("forwarding.removeConfirm", address.email),
                        destructive: true,
                        confirmLabel: t("forwarding.remove"),
                      }))
                    ) {
                      return;
                    }
                    onRemove(address);
                  })();
                }}
              >
                {t("forwarding.remove")}
              </button>
            </div>
          </li>
        ))}

        {addresses.length === 0 && <li className={styles.empty}>{t("forwarding.none")}</li>}
      </ul>

      <form
        className={styles.sizeRow}
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <label className={styles.field}>
          <span className={styles.fieldLabel}>{t("forwarding.add")}</span>
          {/*
            `type="text"` with an email keyboard, NOT `type="email"`.

            A native email input blocks form submission on its own terms, and
            those terms are not the server's: it silently swallows the submit
            with a browser tooltip, so our own message — the one worded to match
            what `looksLikeAddress` actually accepts — never runs. One validator
            that agrees with the server beats two that disagree with each other.
          */}
          <input
            type="text"
            inputMode="email"
            autoComplete="email"
            className={styles.input}
            value={value}
            placeholder={t("forwarding.addPlaceholder")}
            disabled={isBusy || addState === "adding"}
            onChange={(event) => {
              setValue(event.target.value);
              setInvalid(false);
              setAddState("idle");
            }}
          />
        </label>
        <button
          type="submit"
          className={styles.primary}
          disabled={isBusy || addState === "adding"}
        >
          {addState === "adding" ? t("forwarding.adding") : t("forwarding.add")}
        </button>
      </form>

      {invalid && (
        <p className={styles.error} role="alert">
          {t("blocked.invalid")}
        </p>
      )}
      {addState === "failed" && (
        <p className={styles.error} role="alert">
          {t("forwarding.addFailed")}
        </p>
      )}
      {pending.length > 0 && (
        <p className={styles.note}>
          {format("forwarding.codeSent", pending.map((a) => a.email).join(", "))}
        </p>
      )}

      {/* --- forward all --- */}

      <h4 className={styles.builderTitle}>{t("forwarding.forwardAllTitle")}</h4>

      {verified.length === 0 ? (
        /*
          No verified destination means forward-all cannot be turned on at all —
          the server refuses an enabled Forwarding with an unverified address.
          So the switch is REPLACED by the precondition rather than rendered
          disabled next to an empty picker.
        */
        <p className={styles.note}>{t("forwarding.forwardAllNeedsVerified")}</p>
      ) : (
        <>
          <label className={styles.check}>
            <input
              type="checkbox"
              role="switch"
              checked={forwardAll.enabled}
              disabled={isBusy}
              onChange={(event) => {
                const enabled = event.target.checked;
                onSaveForwardAll(
                  // Turning it on with no address chosen would be refused, so
                  // the first verified destination is supplied — the same
                  // default a picker with one option would produce anyway.
                  enabled && forwardAll.address === null
                    ? { enabled, address: verified[0]?.email ?? null }
                    : { enabled },
                );
              }}
            />
            <span>{t("forwarding.forwardAllEnable")}</span>
          </label>

          <label className={styles.field}>
            <span className={styles.fieldLabel}>{t("forwarding.forwardAllTo")}</span>
            <select
              className={styles.select}
              value={forwardAll.address ?? ""}
              disabled={isBusy}
              onChange={(event) => {
                onSaveForwardAll({ address: event.target.value });
              }}
            >
              {verified.map((address) => (
                <option key={address.id} value={address.email}>
                  {address.email}
                </option>
              ))}
            </select>
          </label>

          <label className={styles.field}>
            <span className={styles.fieldLabel}>{t("forwarding.disposition")}</span>
            <select
              className={styles.select}
              value={forwardAll.disposition}
              disabled={isBusy}
              onChange={(event) => {
                onSaveForwardAll({
                  disposition: event.target.value as ForwardDisposition,
                });
              }}
            >
              {FORWARD_DISPOSITIONS.map((disposition) => (
                <option key={disposition} value={disposition}>
                  {disposition === "keep"
                    ? t("forwarding.dispositionKeep")
                    : t("forwarding.dispositionArchive")}
                </option>
              ))}
            </select>
          </label>
        </>
      )}
    </div>
  );
}

/**
 * The code box for one pending address.
 *
 * Its own component with its own state, so two pending addresses do not share
 * one input — which is the bug a single lifted `code` field would produce, and
 * the one where a user pastes a code under the wrong address and gets the
 * server's deliberately-uninformative refusal.
 */
function VerifyBox({
  address,
  onVerify,
  disabled,
}: {
  readonly address: ForwardingAddress;
  readonly onVerify: (token: string) => Promise<boolean>;
  readonly disabled: boolean;
}): React.JSX.Element {
  const { t } = useTranslation();
  const [code, setCode] = useState("");
  const [state, setState] = useState<"idle" | "verifying" | "failed">("idle");

  return (
    <form
      className={styles.sizeRow}
      aria-label={`${t("forwarding.verify")}: ${address.email}`}
      onSubmit={(event) => {
        event.preventDefault();
        if (code.trim() === "") return;
        setState("verifying");
        void onVerify(code.trim()).then((ok) => {
          setState(ok ? "idle" : "failed");
          if (ok) setCode("");
        });
      }}
    >
      <label className={styles.field}>
        <span className={styles.fieldLabel}>{t("forwarding.codeLabel")}</span>
        <input
          type="text"
          className={styles.inputShort}
          value={code}
          disabled={disabled || state === "verifying"}
          onChange={(event) => {
            setCode(event.target.value);
            setState("idle");
          }}
        />
      </label>
      <button
        type="submit"
        className={styles.secondary}
        disabled={disabled || state === "verifying" || code.trim() === ""}
      >
        {state === "verifying" ? t("forwarding.verifying") : t("forwarding.verify")}
      </button>
      {state === "failed" && (
        <span className={styles.error} role="alert">
          {t("forwarding.verifyFailed")}
        </span>
      )}
    </form>
  );
}
