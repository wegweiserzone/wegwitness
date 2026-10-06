// Package buildinfo exposes the version of the running binary.
package buildinfo

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// Stamped at link time via -ldflags. Do not read these directly; use [Get].
var (
	version = ""
	commit  = ""
	date    = ""
)

// Info describes the build a binary was produced from.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	Date      string `json:"date,omitempty"`
	GoVersion string `json:"goVersion"`
	Platform  string `json:"platform"`
	Modified  bool   `json:"modified,omitempty"`
}

// Get returns the build information for this binary.
func Get() Info {
	info := Info{
		Version:   version,
		Commit:    commit,
		Date:      date,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}

	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = s.Value
				}
			case "vcs.time":
				if info.Date == "" {
					info.Date = s.Value
				}
			case "vcs.modified":
				info.Modified = s.Value == "true"
			}
		}
	}

	if info.Version == "" {
		info.Version = "dev"
	}
	return info
}

// String returns a single-line human-readable form, such as
// "wegweiser 0.1.0 (a1b2c3d, go1.26.0, linux/amd64)".
func (i Info) String() string {
	// On an untagged build "git describe --always" yields the abbreviated
	// commit itself, optionally suffixed with -dirty, so repeating it in the
	// detail would say the same thing twice.
	base := strings.TrimSuffix(i.Version, "-dirty")
	redundant := i.Commit == "" || strings.HasPrefix(i.Commit, base)

	detail := ""
	switch {
	case !redundant:
		detail = shortCommit(i.Commit)
		if i.Modified {
			detail += "-dirty"
		}
		detail += ", "
	case i.Modified && !strings.HasSuffix(i.Version, "-dirty"):
		detail = "dirty, "
	}
	return "wegweiser " + i.Version + " (" + detail + i.GoVersion + ", " + i.Platform + ")"
}

// shortCommit abbreviates a full commit hash to the conventional seven
// characters, leaving shorter identifiers untouched.
func shortCommit(c string) string {
	const short = 7
	if len(c) <= short {
		return c
	}
	return c[:short]
}
