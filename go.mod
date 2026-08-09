module github.com/GrupoNU/moov

go 1.24

require (
	github.com/jackc/pgx/v5 v5.7.2
	github.com/pressly/goose/v3 v3.24.1
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/sethvargo/go-retry v0.3.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/crypto v0.31.0 // indirect
	golang.org/x/sync v0.10.0 // indirect
	golang.org/x/text v0.21.0 // indirect
)

// go-internal is pinned deliberately. It reaches this module only as a
// test-only dependency of a dependency of goose, but `go mod tidy` still
// records it, and its v1.16+ releases declare `go >= 1.25`. Left unpinned,
// tidy resolves to v1.16 and the whole module stops building on the Go 1.24
// toolchain this project targets. v1.14.1 is the last release that builds on
// 1.24. Remove this pin when the project moves to Go 1.25.
