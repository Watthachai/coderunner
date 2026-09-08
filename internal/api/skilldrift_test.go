package api

import (
	"strings"
	"testing"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
	companion "github.com/Watthachai/fitt-coderunner/skills"
)

// storedAs builds the row an upload of this source would produce, so a test can
// assert that an untouched skill reports no drift.
func storedAs(t *testing.T, dir string, src companion.Source) *domain.Skill {
	t.Helper()
	name := companionSkillName(dir, src)
	_, description := parseSkillFrontmatter(src.Body)
	files := map[string]string{}
	for rel, content := range src.Files {
		files[rel] = content
	}
	return &domain.Skill{
		Name:        name,
		Description: description,
		Body:        normalizeSkillBody(src.Body, name, description),
		Files:       files,
		Enabled:     true,
	}
}

// oneSource returns a named companion skill to build cases from — data-tables
// has both a SKILL.md and a references/ file, so it exercises both comparisons.
func oneSource(t *testing.T) (string, companion.Source) {
	t.Helper()
	const dir = "data-tables"
	src, ok := companion.Sources()[dir]
	if !ok {
		t.Fatalf("skills/%s is missing — update this test if it was renamed", dir)
	}
	if src.Body == "" || len(src.Files) == 0 {
		t.Fatalf("skills/%s embedded without a body or reference files", dir)
	}
	return dir, src
}

// The embed must actually carry every skill in the directory. A pattern that
// silently matched nothing would make companionDrift report a permanent
// all-clear — the exact failure this whole file exists to prevent.
func TestCompanionSourcesAreEmbedded(t *testing.T) {
	sources := companion.Sources()
	if len(sources) < 9 {
		t.Fatalf("expected the companion skills to be embedded, got %d", len(sources))
	}
	for dir, src := range sources {
		if !strings.HasPrefix(src.Body, "---") {
			t.Errorf("skills/%s/SKILL.md has no frontmatter block", dir)
		}
		name, description := parseSkillFrontmatter(src.Body)
		if name != dir {
			t.Errorf("skills/%s declares name %q — the directory and the frontmatter name must match", dir, name)
		}
		if description == "" {
			t.Errorf("skills/%s has no frontmatter description", dir)
		}
		for rel := range src.Files {
			if !strings.HasPrefix(rel, "references/") {
				t.Errorf("skills/%s embedded unexpected file %q", dir, rel)
			}
		}
	}
}

func TestCompanionDriftSilentWhenRowsMatchSource(t *testing.T) {
	var rows []*domain.Skill
	for dir, src := range companion.Sources() {
		rows = append(rows, storedAs(t, dir, src))
	}
	if got := companionDrift(rows); len(got) != 0 {
		t.Fatalf("uploaded-and-unchanged skills reported as drifted: %+v", got)
	}
}

func TestCompanionDriftReportsMissingRow(t *testing.T) {
	dir, src := oneSource(t)
	name := companionSkillName(dir, src)

	// Every companion skill uploaded except this one.
	var rows []*domain.Skill
	for d, s := range companion.Sources() {
		if d == dir {
			continue
		}
		rows = append(rows, storedAs(t, d, s))
	}

	got := companionDrift(rows)
	if len(got) != 1 || got[0].Name != name || got[0].Status != driftMissing {
		t.Fatalf("a skill that was never uploaded must be reported: %+v", got)
	}
}

func TestCompanionDriftReportsEditedBodyAndFiles(t *testing.T) {
	dir, src := oneSource(t)

	body := storedAs(t, dir, src)
	body.Body += "\n\nan operator edited this in the dashboard\n"

	files := storedAs(t, dir, src)
	var ref string
	for rel := range files.Files {
		ref = rel
		break
	}
	files.Files[ref] = "stale"

	for _, tc := range []struct {
		name string
		row  *domain.Skill
		want string
	}{
		{"edited body", body, "SKILL.md differs"},
		{"edited reference", files, ref + " differs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Only the mutated skill is uploaded, so any report is about it.
			got := companionDrift([]*domain.Skill{tc.row})
			var found *skillDrift
			for i := range got {
				if got[i].Name == tc.row.Name {
					found = &got[i]
				}
			}
			if found == nil {
				t.Fatalf("edited skill not reported: %+v", got)
			}
			if found.Status != driftStale {
				t.Errorf("status = %q, want %q", found.Status, driftStale)
			}
			if found.Detail != tc.want {
				t.Errorf("detail = %q, want %q", found.Detail, tc.want)
			}
		})
	}
}

// A skill written straight into the dashboard has no copy in skills/. That is a
// supported way to author one (mail-service began that way), not drift.
func TestCompanionDriftIgnoresRowsWithNoSource(t *testing.T) {
	var rows []*domain.Skill
	for dir, src := range companion.Sources() {
		rows = append(rows, storedAs(t, dir, src))
	}
	rows = append(rows, &domain.Skill{Name: "authored-in-the-dashboard", Body: "---\nname: x\n---\n"})

	if got := companionDrift(rows); len(got) != 0 {
		t.Fatalf("a dashboard-authored skill was reported as drift: %+v", got)
	}
}
