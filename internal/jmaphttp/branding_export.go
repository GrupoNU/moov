package jmaphttp

// Exported handles onto the branding rules, for the ONE consumer that must
// agree with them byte for byte: `moovctl branding`, which writes the
// directories this package reads.
//
// # Why these are exported at all
//
// The CLI lives in a different module path (cmd/moovctl) and therefore cannot
// see this package's unexported identifiers, not even from a test. Without a
// seam, the two implementations of "what is a valid host" and "what is the
// config file called" drift silently — and the symptom of that drift is the
// worst kind: an operator runs `branding set`, it reports success, and the
// login page never changes, because the directory was written under a name the
// server refuses to resolve. A test in cmd/moovctl pins them against these.
//
// They are deliberately thin and read-only: they expose the RULES, never the
// store, so nothing here widens the package's real API. The names carry the
// ForTest suffix so their purpose survives a grep — but they are in a normal
// file rather than an _test.go one because an external package's test cannot
// import another package's test binary.

// ResolveBrandingHostForTest exposes host normalization so the CLI can pin its
// own copy against it. See resolveBrandingHost.
func ResolveBrandingHostForTest(raw string) string { return resolveBrandingHost(raw) }

// BrandingConfigFileForTest is the per-host document's filename, exposed so the
// writer and the reader cannot disagree about it.
const BrandingConfigFileForTest = brandingConfigFile
