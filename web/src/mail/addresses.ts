/**
 * Address parsing, validation and rendering for the composer (P3).
 *
 * # Why this is hand-written and not a regex one-liner
 *
 * The composer's address fields are the one place where the user's typing
 * becomes an RFC 5322 header on a message we DKIM-sign. Three things have to
 * be true at once, and no single regex gives all three:
 *
 *   1. **Pasting works.** People paste "Ana <ana@x.com>, Bea <bea@y.com>" out
 *      of another client, and a field that treats that as one malformed
 *      address is a field people abandon. So the parser splits on commas and
 *      semicolons *at the top level* — never inside a quoted display name,
 *      which is exactly where a naive `split(",")` corrupts `"Gómez, Ana"
 *      <ana@x.com>` into two broken entries.
 *   2. **Invalid input is kept, not silently dropped.** A chip that fails
 *      validation stays on screen marked invalid. Dropping it would leave the
 *      user believing a recipient was added.
 *   3. **The wire shape is JMAP's `EmailAddress`**, not a string — the server
 *      types `from`/`to`/`cc`/`bcc` as `EmailAddress[]` (RFC 8621 §4.1.2.3)
 *      and does its own `mail.ParseAddress` on them. What we send must be
 *      splittable into `{name, email}` cleanly.
 *
 * # The validation rule, and its deliberate limits
 *
 * `isValidEmail` accepts the practical intersection of what Dovecot/Postfix
 * route and what a person can type: a non-empty local part with no spaces or
 * angle brackets, one `@`, and a domain with at least one dot and no
 * consecutive dots. It deliberately does NOT implement RFC 5322's full
 * grammar — that grammar admits addresses (`"a b"@x`, comments, nested
 * folding) that no real mailbox uses and that would only widen what we accept
 * without making any real address work. Anything this refuses, the server
 * would refuse too; the difference is that we say so before the send.
 */

import type { EmailAddress } from "./types";

/** One address as the composer holds it, valid or not. */
export interface AddressChip {
  /** Stable identity for React keys and for removal — chips can duplicate. */
  readonly key: string;
  /** The display name, or undefined when the input carried none. */
  readonly name: string | undefined;
  /** The address, trimmed and with angle brackets removed. */
  readonly email: string;
  /** False when {@link isValidEmail} refuses `email`. */
  readonly isValid: boolean;
}

/** Monotonic counter so two chips for the same address are still distinct. */
let chipCounter = 0;

/** Builds a chip from a name/address pair, assigning it a fresh key. */
export function makeChip(email: string, name?: string): AddressChip {
  const trimmed = email.trim();
  chipCounter += 1;
  const cleanName = name?.trim();
  return {
    key: `chip-${chipCounter}`,
    ...(cleanName !== undefined && cleanName !== "" ? { name: cleanName } : { name: undefined }),
    email: trimmed,
    isValid: isValidEmail(trimmed),
  };
}

/**
 * True when the address is one this client will send to.
 *
 * See the file header for why this is narrower than RFC 5322. The specific
 * refusals, each because it produces a header that either bounces or forges:
 * a space (header injection surface), angle brackets or commas (would split
 * the header), no `@` or more than one, an empty local part or domain, a
 * domain with no dot (a bare hostname is not deliverable off-LAN), leading or
 * trailing dots, or consecutive dots.
 */
export function isValidEmail(value: string): boolean {
  const address = value.trim();
  if (address === "" || address.length > 254) return false;
  // Control characters and whitespace would break the header apart; CR/LF in
  // particular is header injection.
  // eslint-disable-next-line no-control-regex
  if (/[\s<>,;:"\\[\]()\u0000-\u001f\u007f]/.test(address)) return false;

  const at = address.lastIndexOf("@");
  if (at <= 0 || at === address.length - 1) return false;
  const local = address.slice(0, at);
  const domain = address.slice(at + 1);
  if (local.includes("@")) return false;
  if (local.length > 64) return false;
  if (local.startsWith(".") || local.endsWith(".") || local.includes("..")) return false;

  if (!domain.includes(".")) return false;
  if (domain.startsWith(".") || domain.endsWith(".") || domain.includes("..")) return false;
  if (domain.startsWith("-") || domain.endsWith("-")) return false;
  // Every label must be a plausible DNS label.
  for (const label of domain.split(".")) {
    if (label === "" || label.length > 63) return false;
    if (!/^[\p{L}\p{N}](?:[\p{L}\p{N}-]*[\p{L}\p{N}])?$/u.test(label)) return false;
  }
  return true;
}

/**
 * Splits a header-ish string into individual address tokens.
 *
 * Splitting happens on `,` and `;` only OUTSIDE double quotes and outside
 * angle brackets. That is the whole point: `"Gómez, Ana" <ana@x.com>` is ONE
 * address, and a `split(",")` turns it into two broken ones — the single most
 * common paste in a corporate mail client, since Outlook writes names that
 * way.
 */
export function splitAddressList(input: string): readonly string[] {
  const out: string[] = [];
  let current = "";
  let inQuotes = false;
  let depth = 0;

  for (const char of input) {
    if (char === '"') {
      inQuotes = !inQuotes;
      current += char;
      continue;
    }
    if (!inQuotes && char === "<") depth += 1;
    if (!inQuotes && char === ">" && depth > 0) depth -= 1;
    if (!inQuotes && depth === 0 && (char === "," || char === ";")) {
      if (current.trim() !== "") out.push(current.trim());
      current = "";
      continue;
    }
    current += char;
  }
  if (current.trim() !== "") out.push(current.trim());
  return out;
}

/**
 * Parses one address token into a name/email pair.
 *
 * Handles the three forms people actually produce: `ana@x.com`,
 * `Ana <ana@x.com>` and `"Gómez, Ana" <ana@x.com>`. An unparseable token still
 * returns a pair — with the whole token as the `email` — so the caller can
 * make an invalid chip out of it instead of dropping the user's typing.
 */
export function parseAddress(token: string): { name: string | undefined; email: string } {
  const trimmed = token.trim();
  const open = trimmed.lastIndexOf("<");
  const close = trimmed.lastIndexOf(">");

  if (open !== -1 && close > open) {
    const email = trimmed.slice(open + 1, close).trim();
    let name = trimmed.slice(0, open).trim();
    // A quoted display name keeps its inner text, not its quotes.
    if (name.length >= 2 && name.startsWith('"') && name.endsWith('"')) {
      name = name.slice(1, -1).replace(/\\(.)/g, "$1").trim();
    }
    return { name: name === "" ? undefined : name, email };
  }
  return { name: undefined, email: trimmed };
}

/** Parses a whole pasted list into chips, preserving invalid entries. */
export function parseAddressList(input: string): readonly AddressChip[] {
  return splitAddressList(input).map((token) => {
    const { name, email } = parseAddress(token);
    return makeChip(email, name);
  });
}

/**
 * True when a keystroke should commit the pending text into a chip.
 *
 * Comma, semicolon, Enter and Tab are the four every mail client uses. Tab is
 * included deliberately even though it also moves focus: a user who types an
 * address and tabs away means "that is a recipient", and losing it because
 * they did not press Enter is the single most annoying composer bug there is.
 */
export function isCommitKey(key: string): boolean {
  return key === "," || key === ";" || key === "Enter" || key === "Tab";
}

/** Renders a chip the way a header would: `Ana <ana@x.com>` or the bare address. */
export function formatAddress(chip: Pick<AddressChip, "name" | "email">): string {
  if (chip.name === undefined || chip.name === "") return chip.email;
  // A name containing a comma, a quote or a bracket must be quoted, or the
  // rendered string would not re-parse to the same pair.
  const needsQuotes = /[",;<>@\\]/.test(chip.name);
  const name = needsQuotes ? `"${chip.name.replace(/(["\\])/g, "\\$1")}"` : chip.name;
  return `${name} <${chip.email}>`;
}

/** Converts chips to the JMAP wire shape, dropping the invalid ones. */
export function chipsToWire(chips: readonly AddressChip[]): readonly EmailAddress[] {
  return chips
    .filter((chip) => chip.isValid)
    .map((chip) => ({ name: chip.name ?? null, email: chip.email }));
}

/** Converts server addresses into chips (for reply prefill). */
export function wireToChips(
  addresses: readonly EmailAddress[] | null | undefined,
): readonly AddressChip[] {
  if (addresses === null || addresses === undefined) return [];
  return addresses.map((address) => makeChip(address.email, address.name ?? undefined));
}

/**
 * Removes addresses already present, and the account's own address.
 *
 * Reply-all's real job: the sender should not be a recipient of their own
 * reply, and an address must not appear in both To and Cc. Comparison is
 * case-insensitive on the whole address, which is wrong for the local part in
 * theory (RFC 5321 §2.3.11 makes it the receiving server's business) and
 * right in practice for every mail system in use.
 */
export function dedupeAddresses(
  chips: readonly AddressChip[],
  exclude: readonly string[],
): readonly AddressChip[] {
  const seen = new Set(exclude.map((address) => address.trim().toLowerCase()));
  const out: AddressChip[] = [];
  for (const chip of chips) {
    const key = chip.email.trim().toLowerCase();
    if (key === "" || seen.has(key)) continue;
    seen.add(key);
    out.push(chip);
  }
  return out;
}

/** True when every chip is valid and there is at least one. */
export function hasValidRecipients(...lists: readonly (readonly AddressChip[])[]): boolean {
  let any = false;
  for (const list of lists) {
    for (const chip of list) {
      if (!chip.isValid) return false;
      any = true;
    }
  }
  return any;
}
