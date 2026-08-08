// Separate module: it builds against a PATCHED go-imap checkout
// (branch-v2 tip + PR #757), not the pinned upstream pseudo-version used by
// the parent spike. Keeping it out of the parent module avoids a `replace`
// directive leaking into the main spike's dependency graph.
module github.com/GrupoNU/moov/spikes/s2-goimap/pr757

go 1.24

require github.com/emersion/go-imap/v2 v2.0.0-beta.8

// Point this at a checkout of github.com/emersion/go-imap branch v2 with
// https://github.com/emersion/go-imap/pull/757 applied. See README.md.
replace github.com/emersion/go-imap/v2 => ./goimap
