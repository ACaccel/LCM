// Package buildinfo exposes the version of the running binary.
package buildinfo

import "runtime/debug"

// Set at link time:
//
//	go build -ldflags "-X github.com/ACaccel/lcm/internal/buildinfo.Version=v1.2.3"
var (
	Version = ""
	Commit  = ""
	Date    = ""
)

// Info describes the running build.
type Info struct {
	Version string
	Commit  string
	Date    string
}

// Get returns the build info. Values not injected at link time fall back to
// the module and VCS data embedded by the Go toolchain.
func Get() Info {
	info := Info{Version: Version, Commit: Commit, Date: Date}
	bi, ok := debug.ReadBuildInfo()
	if ok {
		if info.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			info.Version = bi.Main.Version
		}
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
			}
		}
	}
	if info.Version == "" {
		info.Version = "dev"
	}
	return info
}

// String formats the build info on one line.
func (i Info) String() string {
	s := i.Version
	if i.Commit != "" {
		c := i.Commit
		if len(c) > 12 {
			c = c[:12]
		}
		s += " (" + c + ")"
	}
	if i.Date != "" {
		s += " " + i.Date
	}
	return s
}
