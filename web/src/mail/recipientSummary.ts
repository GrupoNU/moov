import type { Email, EmailAddress } from "./types";

/**
 * The "para mí" line (C-14, Gmail's shape).
 *
 * Gmail does not print the recipient list under the sender; it prints WHO,
 * in one short phrase — "para mí", "para Juan", "para mí, Ana" — with a ▾
 * that opens the full headers. The phrase is what a reader wants nine times
 * out of ten ("was this to me or to the list?"); the headers are there for
 * the tenth.
 *
 * `ownAddresses` are the reader's own (login name, primary identity); a
 * recipient matching one of them, case-insensitively, reads as `meLabel`
 * ("mí"/"me") and is put FIRST, which is Gmail's order too. Duplicates
 * collapse (a To and a Cc naming the same person once), and names fall back
 * to the address when the sender gave none.
 */
export function recipientSummary(
  email: Pick<Email, "to" | "cc">,
  ownAddresses: readonly string[],
  meLabel: string,
): string | undefined {
  const own = new Set(ownAddresses.map((address) => address.trim().toLowerCase()));
  const recipients: readonly EmailAddress[] = [...(email.to ?? []), ...(email.cc ?? [])];
  if (recipients.length === 0) return undefined;

  const seen = new Set<string>();
  const names: string[] = [];
  let includesMe = false;
  for (const address of recipients) {
    const key = address.email.trim().toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    if (own.has(key)) {
      includesMe = true;
      continue;
    }
    const name = address.name?.trim();
    names.push(name !== undefined && name !== "" ? name : address.email);
  }
  const parts = includesMe ? [meLabel, ...names] : names;
  return parts.length === 0 ? undefined : parts.join(", ");
}

/** One address as "Name <address>", or the bare address without a name. */
export function formatAddress(address: EmailAddress): string {
  const name = address.name?.trim();
  return name !== undefined && name !== "" ? `${name} <${address.email}>` : address.email;
}
