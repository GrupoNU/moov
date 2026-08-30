/**
 * Attachment extensions Moov refuses to send (L3 E7; canon §2.3, /mail/answer/6590).
 *
 * # Why this is a HARD block and not a warning
 *
 * This is the one place in E7 where we copy Gmail without adapting anything.
 * The canon's filter (§1) says it in a sentence: adopt Gmail's mechanics when
 * Gmail has the feature, and diverge only when its reason is a Google business
 * artifact — never when it is a security stance. An executable-attachment block
 * is the purest security stance Gmail takes, encoded from twenty years of
 * watching what people actually click.
 *
 * The plan's adversarial pass caught an earlier draft of this epic proposing a
 * WARNING instead ("we'd tell the user and let them decide"). That is a
 * divergence from a security posture with no arbitration behind it, which P1
 * forbids. A warning also fails on its own terms: the user who most needs the
 * block is precisely the one who will click through it, because the message
 * they are forwarding came from someone they trust.
 *
 * # The trailing-suffix trick, which is the only interesting part
 *
 * A blocked list matched against "the last dot-segment" is trivially defeated.
 * The classic attack is `invoice.pdf.exe`: Windows hides known extensions by
 * default, so the file renders in Explorer as `invoice.pdf` with whatever icon
 * the attacker embedded. Matching only the final segment catches that one — but
 * then `payload.exe.txt` slips past a naive "does the name contain .exe" check
 * in the other direction, and refusing THAT would be a false positive on a
 * perfectly ordinary text file about an executable.
 *
 * So the rule is exactly Gmail's observable behaviour, and it is stated once:
 *
 *   **the FINAL extension decides.** `name.pdf.exe` blocks (final = `exe`);
 *   `name.exe.txt` does not (final = `txt`).
 *
 * That is not a compromise, it is the correct rule: the final extension is what
 * the operating system dispatches on, and the operating system is the thing
 * that would run the file. An intermediate `.exe` is inert — Windows will open
 * `payload.exe.txt` in Notepad.
 *
 * # What this deliberately does NOT do, stated honestly
 *
 * **Archives are not inspected.** A `.zip` or `.gz` containing `payload.exe`
 * attaches normally. Gmail scans inside containers (including password-
 * protected ones, by trying the password from the message body); we cannot,
 * because we are a browser and unpacking arbitrary archives client-side means
 * shipping an unzip implementation and pointing it at attacker-controlled
 * bytes — a worse security position than the one it would defend. The honest
 * consequence is stated here rather than papered over: **the block stops the
 * naive case, not a determined sender.** Server-side scanning (Rspamd already
 * sees every message Mailcow accepts) is where container inspection belongs,
 * and it is not this module's job to pretend otherwise.
 */

/**
 * Gmail's published list of blocked extensions (/mail/answer/6590, retrieved
 * 2026-08-30 via the canon).
 *
 * Transcribed verbatim and in the source's order, WITHOUT editorialising:
 * `.ex` and `.ex_` look like typos and are not — they are real renaming
 * conventions for neutered executables, and dropping them because they look
 * odd is how a list like this rots. `.js` and `.mjs` are both here even though
 * a JavaScript file is inert in a mail client, because the block is about what
 * happens after the file reaches a desktop.
 *
 * Stored WITHOUT the leading dot: the matcher works on the segment after the
 * last dot, and carrying the dot in both places is one more thing to get out of
 * sync.
 */
export const BLOCKED_EXTENSIONS: readonly string[] = [
  "ade",
  "adp",
  "apk",
  "appx",
  "appxbundle",
  "bat",
  "cab",
  "chm",
  "cmd",
  "com",
  "cpl",
  "diagcab",
  "diagcfg",
  "diagpkg",
  "dll",
  "dmg",
  "ex",
  "ex_",
  "exe",
  "hta",
  "img",
  "ins",
  "iso",
  "isp",
  "jar",
  "jnlp",
  "js",
  "jse",
  "lib",
  "lnk",
  "mde",
  "mjs",
  "msc",
  "msi",
  "msix",
  "msixbundle",
  "msp",
  "mst",
  "nsh",
  "pif",
  "ps1",
  "scr",
  "sct",
  "shb",
  "sys",
  "vb",
  "vbe",
  "vbs",
  "vhd",
  "vxd",
  "wsc",
  "wsf",
  "wsh",
  "xll",
];

/** The list as a Set, built once — the matcher runs per attached file. */
const BLOCKED = new Set(BLOCKED_EXTENSIONS);

/**
 * The final extension of a filename, lowercased, or undefined when there is none.
 *
 * Exported because the tests pin its edge cases directly, and because "what did
 * we think the extension was" is the first question when a block looks wrong.
 *
 * The cases it handles, each of which a naive `split(".").pop()` gets wrong:
 *
 *   - **no dot at all** (`README`) → undefined, not the whole name. Otherwise
 *     a file called `exe` would be blocked.
 *   - **a leading dot** (`.bashrc`) → undefined. A dotfile's name is not an
 *     extension; `split(".").pop()` returns `bashrc` and would happily match a
 *     hypothetical entry.
 *   - **a trailing dot** (`payload.exe.`) → the dot is stripped first. Windows
 *     silently discards trailing dots when opening a file, so `payload.exe.`
 *     executes as `payload.exe`; treating the empty final segment as "no
 *     extension" would be a bypass.
 *   - **trailing whitespace** (`payload.exe `) → trimmed, for the same reason.
 *   - **path separators** (`../../payload.exe`) → only the basename is
 *     considered, so a name carrying a directory component cannot hide its
 *     extension behind a dot earlier in the path.
 */
export function finalExtension(filename: string): string | undefined {
  // Only the basename matters. A filename arriving from a File object should
  // never contain a separator, but `name` is attacker-influenced whenever the
  // "file" was built by us from a message (forward-as-attachment), so this is
  // not a hypothetical.
  const basename = filename.split(/[/\\]/).pop() ?? "";

  /*
   * Trailing dots and spaces are stripped REPEATEDLY, not once. Windows
   * discards them all: `payload.exe. . .` opens as `payload.exe`, so a single
   * strip would leave `payload.exe. .` looking like it ends in a space and
   * carrying no extension at all.
   */
  const trimmed = basename.replace(/[\s.]+$/u, "");
  if (trimmed === "") return undefined;

  const dot = trimmed.lastIndexOf(".");
  // `dot <= 0` covers both "no dot" and "leading dot" (a dotfile) in one
  // comparison: at index 0 there is no name before the dot, so what follows is
  // the name, not an extension.
  if (dot <= 0) return undefined;

  const extension = trimmed.slice(dot + 1).toLowerCase();
  return extension === "" ? undefined : extension;
}

/**
 * True when Moov refuses to attach this file.
 *
 * Case-insensitive, because `PAYLOAD.EXE` is the same file to every operating
 * system that would run it.
 */
export function isBlockedAttachment(filename: string): boolean {
  const extension = finalExtension(filename);
  if (extension === undefined) return false;
  return BLOCKED.has(extension);
}
