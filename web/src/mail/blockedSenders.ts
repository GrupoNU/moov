/**
 * Blocked senders — the `type: "blocked"` slice of the rule surface
 * (E6, canon §2.2's "Block sender" row).
 *
 * # One script, two settings sections
 *
 * There is no separate blocked-senders object on the wire. `internal/sieve/
 * model.go` says why, and this module is the client side of that decision:
 *
 * > The type tag is what lets the UI render "Bloqueados" and "Reenvío" as their
 * > own settings sections while the storage is one script.
 *
 * So "Bloqueados" IS `FilterRule/get` filtered to `type === "blocked"`, and
 * blocking a sender is a `FilterRule/set create` with that tag. The server
 * compiles it as "an exact address match filed to Junk", which is Gmail's exact
 * documented behaviour: "All future emails from them go to Spam".
 *
 * # Block is not unsubscribe, and the UI must not conflate them
 *
 * Canon §2.2 records both facts on the same row: blocking sends future mail to
 * Spam, and it "does NOT unsubscribe". They are different remedies for
 * different problems — a mailing list you no longer want is unsubscribed, a
 * person or a spammer is blocked — and offering only one when the message
 * carries a `List-Unsubscribe` header hides the better answer.
 * {@link blockAdvice} is what the block dialog uses to say both.
 *
 * # The validation this mirrors
 *
 * The model refuses a blocked rule with no address and one whose address is not
 * an address:
 *
 * ```go
 * case RuleBlocked:
 *     if len(r.Criteria.From) == 0 { addf("%s: a blocked-sender rule needs the sender address", where) }
 *     for _, a := range r.Criteria.From { if !looksLikeAddress(a) { … } }
 * ```
 *
 * {@link isBlockableAddress} is `looksLikeAddress` restated, so the UI refuses
 * before the round trip instead of surfacing a validation list afterwards.
 */

import { EMPTY_RULE, type FilterRule, type FilterRuleDraft } from "./filters";
import type { Email } from "./types";
import { unsubscribeInfo } from "./unsubscribe";

/** The blocked senders among a rule list, in the order the script runs them. */
export function blockedRules(rules: readonly FilterRule[]): readonly FilterRule[] {
  return rules.filter((rule) => rule.type === "blocked");
}

/** Every address currently blocked, lowercased for comparison. */
export function blockedAddresses(rules: readonly FilterRule[]): ReadonlySet<string> {
  const out = new Set<string>();
  for (const rule of blockedRules(rules)) {
    for (const address of rule.from) out.add(address.trim().toLowerCase());
  }
  return out;
}

/** True when this address already has a blocked rule. */
export function isBlocked(rules: readonly FilterRule[], address: string): boolean {
  return blockedAddresses(rules).has(address.trim().toLowerCase());
}

/**
 * `internal/sieve/model.go`'s `looksLikeAddress`, restated.
 *
 * Shallow on purpose, and the server says why: "Deliverability is the mail
 * system's problem; this only keeps garbage out of generated code." Matching
 * the server's exact strictness matters more than being clever — a UI stricter
 * than the server refuses valid addresses, and a looser one produces a refusal
 * the user cannot act on.
 */
export function isBlockableAddress(value: string): boolean {
  const address = value.trim();
  if (address === "") return false;
  // The control-character check (`safeScriptString`) and the whitespace check.
  for (const char of address) {
    const code = char.codePointAt(0) ?? 0;
    if (code < 0x20 || code === 0x7f) return false;
  }
  if (/[ \t]/.test(address)) return false;
  const at = address.indexOf("@");
  return at > 0 && at < address.length - 1 && !address.slice(at + 1).includes("@");
}

/**
 * The draft that blocks one sender.
 *
 * `name` carries the address so the settings list has something to render and
 * the generated script's comment names the rule. `stop` is deliberately FALSE:
 * the blocked rule is emitted first regardless of position ("a blocked sender
 * must not receive a vacation reply"), and setting `stop` would additionally
 * skip every later rule AND the external section for that message — a
 * side effect the user did not ask for when they clicked "block".
 */
export function blockDraft(address: string): FilterRuleDraft {
  const normalized = address.trim().toLowerCase();
  return { ...EMPTY_RULE, name: normalized, type: "blocked", from: [normalized] };
}

/**
 * What the block dialog should say about THIS message.
 *
 * The unsubscribe half is present exactly when the message carries a usable
 * `List-Unsubscribe` (E2's parser decides that, and reusing it is what keeps
 * the two features agreeing about what "usable" means).
 */
export interface BlockAdvice {
  /** The address that would be blocked, normalized. */
  readonly address: string;
  /** True when the sender offers an unsubscribe route worth mentioning. */
  readonly hasUnsubscribe: boolean;
}

export function blockAdvice(email: Email, address: string): BlockAdvice {
  return {
    address: address.trim().toLowerCase(),
    hasUnsubscribe: unsubscribeInfo(email) !== undefined,
  };
}
