module github.com/GrupoNU/moov

// go 1.24.3 rather than 1.24: enmime/v2 v2.3.0 and its html2text dependency both
// declare `go 1.24.3` as their minimum, so the module directive has to meet it.
// Still the 1.24 series, so the go-internal pin below and the Go 1.24 toolchain
// this project targets are unaffected. The `toolchain` line `go mod tidy` also
// wants to add is deliberately omitted — it pins a specific patch release of the
// compiler, which is CI's business rather than the module's. Verified building
// and testing without it under -mod=readonly.
//
// Raising this further is a project-wide decision, not a per-epic one: enmime
// v2.4.x requires go >= 1.25, which is why E4 pins v2.3.0.
go 1.24.3

require (
	github.com/emersion/go-message v0.18.2
	github.com/jackc/pgx/v5 v5.7.2
	github.com/jhillyerd/enmime/v2 v2.3.0
	github.com/pressly/goose/v3 v3.24.1
	github.com/saintfish/chardet v0.0.0-20230101081208-5e3ef4b5456d
	golang.org/x/text v0.34.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/cention-sany/utf7 v0.0.0-20170124080048-26cad61bd60a // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/clipperhouse/displaywidth v0.10.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.6.0 // indirect
	github.com/fatih/color v1.18.0 // indirect
	github.com/gogs/chardet v0.0.0-20211120154057-b7413eaefb8f // indirect
	github.com/inbucket/html2text v1.0.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-runewidth v0.0.19 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/olekukonko/cat v0.0.0-20250911104152-50322a0618f6 // indirect
	github.com/olekukonko/errors v1.2.0 // indirect
	github.com/olekukonko/ll v0.1.6 // indirect
	github.com/olekukonko/tablewriter v1.1.3 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/sethvargo/go-retry v0.3.0 // indirect
	github.com/ssor/bom v0.0.0-20170718123548-6386211fdfcf // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/net v0.50.0 // indirect
	golang.org/x/sync v0.19.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
)

// go-internal is pinned deliberately. It reaches this module only as a
// test-only dependency of a dependency of goose, but `go mod tidy` still
// records it, and its v1.16+ releases declare `go >= 1.25`. Left unpinned,
// tidy resolves to v1.16 and the whole module stops building on the Go 1.24
// toolchain this project targets. v1.14.1 is the last release that builds on
// 1.24. Remove this pin when the project moves to Go 1.25.
