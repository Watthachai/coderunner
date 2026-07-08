package buildstep

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScaffoldRunPrisma(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "prisma"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prisma", "schema.prisma"), []byte("datasource db {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A pre-existing (model-generated) compose that must be OVERWRITTEN.
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wrote, err := ScaffoldRun(dir, nil)
	if err != nil || !wrote {
		t.Fatalf("wrote=%v err=%v, want true/nil", wrote, err)
	}

	dc, _ := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if !strings.Contains(string(dc), "prisma db push") {
		t.Errorf("docker-compose.yml not overwritten with the working runner")
	}

	qs, err := os.ReadFile(filepath.Join(dir, "QUICKSTART.md"))
	if err != nil {
		t.Fatalf("QUICKSTART.md not written: %v", err)
	}
	// Paste-safety: no trailing "# ..." comments inside command lines.
	if strings.Contains(string(qs), "# then set") || strings.Contains(string(qs), "# create the schema") {
		t.Errorf("QUICKSTART still has a paste-breaking inline comment")
	}
}

func TestScaffoldRunNoPrismaSkips(t *testing.T) {
	dir := t.TempDir()
	wrote, err := ScaffoldRun(dir, nil)
	if err != nil || wrote {
		t.Fatalf("no prisma: wrote=%v err=%v, want false/nil", wrote, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err == nil {
		t.Errorf("docker-compose.yml written for a non-prisma app")
	}
}
