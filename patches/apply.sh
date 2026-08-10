#!/bin/sh
# Re-apply the Moov patch set to the vendored copy of go-imap/v2.
#
# Why this exists: go-imap/v2 is a pre-release library carried with three local
# patches (see README.md). `go mod vendor` regenerates vendor/ from the module
# cache, which is pristine upstream — so every `go mod vendor`, `go mod tidy
# && go mod vendor`, or dependency bump silently reverts the patch set. This
# script puts it back, and `make vendor-patches` is the supported way to run it.
#
# The correctness net is TestVendoredPatchSetIsApplied in internal/imap, which
# fails the build if the vendored tree is unpatched. This script is the fix;
# that test is the alarm.
#
# Usage:
#   sh patches/apply.sh            # apply all patches, idempotently
#   sh patches/apply.sh --check    # report status, change nothing (exit 1 if unpatched)
#
# Portability: POSIX sh. Runs from Git Bash on Windows and from Linux CI.

set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
patch_dir="$repo_root/patches"
vendor_dir="$repo_root/vendor/github.com/emersion/go-imap/v2"

check_only=0
if [ "${1:-}" = "--check" ]; then
	check_only=1
fi

if [ ! -d "$vendor_dir" ]; then
	echo "patches/apply.sh: $vendor_dir does not exist." >&2
	echo "Run 'go mod vendor' first, then re-run this script." >&2
	exit 1
fi

# `git apply` is used rather than `patch` because it is guaranteed present
# (this is a git repository) and it handles the git-style diffs verbatim,
# including new files. --directory keeps the patches expressed relative to the
# go-imap source root, so the patch files stay valid as upstream patches we can
# send to emersion without rewriting any path.
#
# --exclude='*_test.go' is not cosmetic: `go mod vendor` deliberately omits
# test files, so a hunk touching one has nothing to apply against. The patches
# keep their test hunks because an upstream PR needs them; the vendored tree
# simply skips those hunks. The behaviour they assert is re-asserted from our
# own side by TestVendoredPatchSetIsApplied.
apply_one() {
	patch_file=$1
	rel="vendor/github.com/emersion/go-imap/v2"
	set -- --directory="$rel" --exclude='*_test.go'

	if git -C "$repo_root" apply --check --reverse "$@" "$patch_file" 2>/dev/null; then
		echo "  already applied: $(basename "$patch_file")"
		return 0
	fi

	if [ "$check_only" -eq 1 ]; then
		echo "  NOT APPLIED:     $(basename "$patch_file")"
		return 1
	fi

	if ! git -C "$repo_root" apply "$@" "$patch_file"; then
		echo "patches/apply.sh: $(basename "$patch_file") failed to apply." >&2
		echo "The pin was probably bumped. See patches/README.md, section" >&2
		echo "'Bumping the pin', for the re-validation procedure." >&2
		return 2
	fi
	echo "  applied:         $(basename "$patch_file")"
}

echo "go-imap patch set -> $vendor_dir"

rc=0
for p in "$patch_dir"/0*.patch; do
	[ -e "$p" ] || continue
	apply_one "$p" || rc=$?
done

if [ "$rc" -ne 0 ]; then
	if [ "$check_only" -eq 1 ]; then
		echo "patch set INCOMPLETE — run 'make vendor-patches'" >&2
	fi
	exit "$rc"
fi

echo "patch set complete"
