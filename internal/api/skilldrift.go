package api

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
	companion "github.com/Watthachai/fitt-coderunner/skills"
)

// skilldrift.go answers a question the console could not previously answer:
// does this box actually run the companion skills that are in the repository?
//
// fitt-build re-seeds itself from the binary on every restart, so `git pull` +
// restart is enough to deploy it. The skills under skills/ are uploaded by hand.
// Editing one and deploying the server changes nothing — the database keeps
// serving the old body to every build, and nothing anywhere says so. A build can
// therefore pass its whole delivery gate while running a harness nobody reviewed.
//
// Reporting is all this does. Overwriting an operator's dashboard edit on boot
// would be the worse failure, so the decision stays with a human.

// Drift statuses. A skill that matches its source is simply absent from the
// report — the console renders a badge only for something needing attention.
const (
	driftMissing = "missing" // in skills/, no row in the database
	driftStale   = "stale"   // row exists, content no longer matches
)

// skillDrift is one companion skill whose database row disagrees with the copy
// reviewed in skills/.
type skillDrift struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// Detail names what differs, so an operator can tell a reference-file edit
	// from a rewritten SKILL.md without opening the diff.
	Detail string `json:"detail"`
}

// companionDrift compares every skill in skills/ against the rows the database
// returned. Rows with no counterpart in skills/ are not reported: a skill
// authored directly in the dashboard is legitimate, not drift.
func companionDrift(rows []*domain.Skill) []skillDrift {
	byName := make(map[string]*domain.Skill, len(rows))
	for _, row := range rows {
		byName[row.Name] = row
	}

	out := []skillDrift{}
	for dir, src := range companion.Sources() {
		name := companionSkillName(dir, src)
		row, ok := byName[name]
		if !ok {
			out = append(out, skillDrift{
				Name:   name,
				Status: driftMissing,
				Detail: "in skills/ but never uploaded",
			})
			continue
		}
		if detail := companionDiff(src, row); detail != "" {
			out = append(out, skillDrift{Name: name, Status: driftStale, Detail: detail})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// companionSkillName resolves the name a row would be keyed by, mirroring
// handleUploadSkill: frontmatter name first, then the directory (which is the
// zip's top dir when uploaded as documented), then branchSlug to make it
// installable. Resolving it any other way would report a skill as "missing"
// whenever its frontmatter name and directory disagree.
func companionSkillName(dir string, src companion.Source) string {
	name, _ := parseSkillFrontmatter(src.Body)
	if name == "" {
		name = dir
	}
	return branchSlug(name)
}

// companionDiff renders what differs between the repository copy and the stored
// row, or "" when they match.
//
// The body is normalized exactly as an upload would normalize it before the
// comparison. handleUploadSkill rewrites the frontmatter name, so comparing the
// raw file against the stored body would report permanent drift on any skill
// whose name needed slugging.
func companionDiff(src companion.Source, row *domain.Skill) string {
	_, description := parseSkillFrontmatter(src.Body)
	if normalizeSkillBody(src.Body, row.Name, description) != row.Body {
		return "SKILL.md differs"
	}

	var changed []string
	for rel, content := range src.Files {
		if stored, ok := row.Files[rel]; !ok {
			changed = append(changed, rel+" (never uploaded)")
		} else if stored != content {
			changed = append(changed, rel)
		}
	}
	for rel := range row.Files {
		if _, ok := src.Files[rel]; !ok {
			changed = append(changed, rel+" (only in the database)")
		}
	}
	if len(changed) == 0 {
		return ""
	}
	sort.Strings(changed)
	if len(changed) == 1 {
		return changed[0] + " differs"
	}
	return fmt.Sprintf("%d files differ: %s", len(changed), strings.Join(changed, ", "))
}
