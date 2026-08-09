#!/bin/sh
# Fetch third-party MIME test messages that are NOT committed to this repository.
#
# Everything committed under testdata/mime-corpus/ is authored by this project
# and carries the repository's license. Classic public test material — Mark
# Crispin's "MIME torture test", the various IMAP/MIME conformance messages
# that circulate with mail servers — is genuinely valuable precisely because it
# was written by someone else, with a different model of what breaks.
#
# It is not committed here because its licensing could not be established with
# confidence, and an AGPL-3.0 repository is the wrong place to guess. Material
# whose license is unclear is fetched at test time instead, kept out of version
# control, and marked `external: true` in manifest.yaml.
#
# Usage:  sh fetch-external.sh
# Output: ./external/  (git-ignored)
#
# ---------------------------------------------------------------------------
# STATUS: no sources are enabled yet.
# ---------------------------------------------------------------------------
# Adding one is deliberately a two-step decision, not a drive-by commit:
#
#   1. Establish the license and record it in manifest.yaml alongside the
#      source URL and the date it was checked. "It's on the public internet"
#      is not a license.
#   2. Confirm the file contains no real personal data. Several classic test
#      messages carry real 1990s addresses of real people in their headers.
#      Sanitize before use: replace addresses with example.com / example.org
#      and names with invented ones, exactly as the generated corpus does.
#
# Until both hold for a given source, leave it out. A smaller corpus with clean
# provenance is worth more to this project than a larger one with a licensing
# question attached — this is the public, open-source face of the company.

set -eu

DIR="$(cd "$(dirname "$0")" && pwd)/external"
mkdir -p "$DIR"

# fetch <url> <destination-filename> <license-note>
# Enable a source by adding a call below AND a matching manifest.yaml entry
# with `provenance: external`, `external: true`, `source:` and `license:`.
fetch() {
	url="$1"
	dest="$2"
	note="$3"
	printf '%s\n' "fetching $dest ($note)"
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --max-time 30 -o "$DIR/$dest" "$url"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -T 30 -O "$DIR/$dest" "$url"
	else
		echo "need curl or wget" >&2
		exit 1
	fi
}

# --- enabled sources (none yet) --------------------------------------------
# Example of the shape an enabled source takes:
#
# fetch "https://example.org/path/torture-test.eml" \
#       "001-crispin-torture-test.eml" \
#       "license confirmed <DATE>, see manifest entry ext-001"

count=$(find "$DIR" -name '*.eml' 2>/dev/null | wc -l | tr -d ' ')
if [ "$count" -eq 0 ]; then
	cat <<'EOF'

No external sources are enabled — this is the expected state.

The committed corpus (110 cases under this directory) runs standalone and needs
nothing from the network. See the comment block at the top of this script for
what enabling a source requires.
EOF
fi
