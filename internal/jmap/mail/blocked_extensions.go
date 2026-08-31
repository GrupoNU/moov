package mail

import "strings"

// Server-side mirror of the executable-attachment block (L3 epic E10,
// completing E7; canon §2.3, support.google.com/mail/answer/6590).
//
// E7 blocks these in the composer, which protects exactly one client: ours.
// A message is CREATED here, in Email/set — so a non-Moov JMAP client
// attaching payload.exe would sail past the client-side block and this
// server would assemble and send what Gmail-class policy refuses. The block
// therefore lives at the assembly chokepoint too, with the same list, the
// same final-extension rule, and the same honesty (a `forbidden` SetError
// naming the extension, not a silent drop).
//
// # Declared == enforced, pinned
//
// The list is TRANSCRIBED from web/src/mail/blockedExtensions.ts — Gmail's
// published list, verbatim and in the source's order — and the two cannot
// drift silently: TestBlockedExtensionsMatchTheClientList reads the
// TypeScript file out of the repo and fails if the sets differ, the same
// discipline that pins maxAttachmentsBytes to the advertised
// maxSizeAttachmentsPerEmail.
//
// # The matching rule (one sentence, same as the client's)
//
// The FINAL extension decides: `name.pdf.exe` blocks (final = exe),
// `name.exe.txt` does not (final = txt) — the final extension is what the
// recipient's operating system dispatches on. Trailing dots and spaces are
// stripped repeatedly first, because Windows discards them all when opening
// a file (`payload.exe. . .` runs as `payload.exe`), and only the basename
// is considered so a path component cannot hide the extension. Archives are
// NOT inspected, here or client-side — that is Rspamd's job on the delivery
// path, and pretending otherwise would be a false promise.
var blockedExtensionList = []string{
	"ade", "adp", "apk", "appx", "appxbundle", "bat", "cab", "chm", "cmd",
	"com", "cpl", "diagcab", "diagcfg", "diagpkg", "dll", "dmg", "ex", "ex_",
	"exe", "hta", "img", "ins", "iso", "isp", "jar", "jnlp", "js", "jse",
	"lib", "lnk", "mde", "mjs", "msc", "msi", "msix", "msixbundle", "msp",
	"mst", "nsh", "pif", "ps1", "scr", "sct", "shb", "sys", "vb", "vbe",
	"vbs", "vhd", "vxd", "wsc", "wsf", "wsh", "xll",
}

// blockedExtensions is the list as a set — the per-part lookup.
var blockedExtensions = func() map[string]bool {
	m := make(map[string]bool, len(blockedExtensionList))
	for _, e := range blockedExtensionList {
		m[e] = true
	}
	return m
}()

// finalAttachmentExtension returns a filename's final extension, lowercased,
// or "" when it has none. The edge cases mirror the client's finalExtension
// (blockedExtensions.ts) case for case; the parity test exercises both
// through the same table.
func finalAttachmentExtension(filename string) string {
	// Only the basename matters: a name carrying a directory component must
	// not hide its extension behind an earlier dot in the path.
	basename := filename
	if i := strings.LastIndexAny(basename, "/\\"); i >= 0 {
		basename = basename[i+1:]
	}

	// Windows discards ALL trailing dots and spaces, so they are stripped
	// repeatedly: `payload.exe. . .` opens as `payload.exe`.
	trimmed := strings.TrimRight(basename, ". \t\n\r")
	if trimmed == "" {
		return ""
	}

	dot := strings.LastIndexByte(trimmed, '.')
	// dot <= 0 covers "no dot" and "leading dot" (a dotfile's name is not an
	// extension) in one comparison.
	if dot <= 0 {
		return ""
	}
	return strings.ToLower(trimmed[dot+1:])
}

// blockedAttachmentExtension returns the blocked extension a filename ends
// in, or "" when the name is acceptable. Returning the extension rather than
// a bool is what lets the SetError name it.
func blockedAttachmentExtension(filename string) string {
	ext := finalAttachmentExtension(filename)
	if ext != "" && blockedExtensions[ext] {
		return ext
	}
	return ""
}
