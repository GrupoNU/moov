// Package version carries the build identity of the moovd binary.
//
// The values are placeholders in a plain `go build`; release builds stamp them
// with -ldflags, which is the only supported way to set them:
//
//	go build -ldflags "-X github.com/GrupoNU/moov/internal/version.Version=1.2.3 \
//	                   -X github.com/GrupoNU/moov/internal/version.Commit=$(git rev-parse --short HEAD) \
//	                   -X github.com/GrupoNU/moov/internal/version.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
//	         ./cmd/moovd
//
// The Makefile's `build` target does exactly that. Keeping the variables in
// their own package (rather than in main) lets any package report the running
// version — metrics labels, JMAP session capabilities, log fields — without
// importing main.
package version

import "runtime/debug"

// Values injected at link time. Defaults describe an unstamped developer build.
var (
	// Version is the release version, or "dev" for an unstamped build.
	Version = "dev"
	// Commit is the git commit the binary was built from, short form.
	Commit = "unknown"
	// Date is the build timestamp in RFC 3339, UTC.
	Date = "unknown"
)

// Info is a snapshot of the build identity.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	Go      string `json:"go"`
}

// Get returns the build identity, falling back to the Go build info embedded by
// the toolchain when the link-time values were not stamped. That fallback makes
// `go install github.com/GrupoNU/moov/cmd/moovd@v1.2.3` report something useful.
func Get() Info {
	info := Info{Version: Version, Commit: Commit, Date: Date, Go: "unknown"}

	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	info.Go = bi.GoVersion

	if info.Version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		info.Version = bi.Main.Version
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if info.Commit == "unknown" && s.Value != "" {
				const short = 12
				if len(s.Value) > short {
					info.Commit = s.Value[:short]
				} else {
					info.Commit = s.Value
				}
			}
		case "vcs.time":
			if info.Date == "unknown" && s.Value != "" {
				info.Date = s.Value
			}
		}
	}
	return info
}

// String renders the build identity on one line, for --version and log banners.
func (i Info) String() string {
	return "moovd " + i.Version + " (commit " + i.Commit + ", built " + i.Date + ", " + i.Go + ")"
}
