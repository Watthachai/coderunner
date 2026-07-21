// Package buildinfo exposes the binary's VCS build stamp (git revision + build
// time), read from the build info that `go build` embeds automatically from a git
// checkout. It lets an operator confirm exactly which commit a running CRN server
// is on — e.g. after `git pull` + restart on a remote box, rather than guessing
// whether the deployed binary already contains a given fix.
package buildinfo

import "runtime/debug"

// Info is the resolved build stamp. Zero-value revision is "unknown" (built
// outside a git checkout or with -buildvcs=false).
type Info struct {
	Revision string `json:"revision"` // short git commit, or "unknown"
	Time     string `json:"time"`     // commit time (RFC3339), or ""
	Modified bool   `json:"modified"` // built from a dirty working tree
}

// Read pulls the VCS stamp from the embedded build info. Go records vcs.revision,
// vcs.time and vcs.modified when building from a git checkout (default on).
func Read() Info {
	info := Info{Revision: "unknown"}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			info.Revision = shortRev(s.Value)
		case "vcs.time":
			info.Time = s.Value
		case "vcs.modified":
			info.Modified = s.Value == "true"
		}
	}
	return info
}

// String renders the stamp compactly, e.g. "4f1477e (2026-07-21T…Z) dirty".
func (i Info) String() string {
	s := i.Revision
	if i.Time != "" {
		s += " (" + i.Time + ")"
	}
	if i.Modified {
		s += " dirty"
	}
	return s
}

// shortRev trims a 40-char git SHA to git's 7-char short form (matches
// `git log --oneline`), leaving shorter/empty values untouched.
func shortRev(rev string) string {
	if len(rev) >= 7 {
		return rev[:7]
	}
	return rev
}
