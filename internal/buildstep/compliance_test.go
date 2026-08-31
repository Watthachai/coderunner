package buildstep

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// compliantSeed is what the skill asks for: the login account, once, keyed by
// email so re-running it on every container start is a no-op.
const compliantSeed = `import { PrismaClient } from "@prisma/client";
const prisma = new PrismaClient();
const email = process.env.DEV_EMAIL ?? "dev@fitt.local";
async function main() {
  await prisma.user.upsert({ where: { email }, update: {}, create: { email, name: "Dev Admin" } });
}
main().finally(() => prisma.$disconnect());
`

// write drops a file into dir, creating parents. Fails the test on error — a
// broken fixture must not read as a passing check.
func write(t *testing.T, dir, name, body string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

// ruleOf returns the single finding's rule, or "" when nothing tripped.
func ruleOf(t *testing.T, findings []Finding) string {
	t.Helper()
	if len(findings) == 0 {
		return ""
	}
	if len(findings) > 1 {
		t.Fatalf("expected at most one finding, got %d: %+v", len(findings), findings)
	}
	return findings[0].Rule
}

func TestCheckSeed(t *testing.T) {
	cases := []struct {
		name string
		seed string
		want string
	}{
		{"login only", compliantSeed, ""},
		{
			// The exact shape the old skill produced: mock rows moved into the seed.
			name: "demo rows in a loop",
			seed: `const tasks = [{ id: "t1" }, { id: "t2" }];
async function main() {
  await prisma.user.upsert({ where: { email }, update: {}, create: { email } });
  for (const t of tasks) await prisma.task.upsert({ where: { id: t.id }, update: t, create: t });
}`,
			want: "seed_rows",
		},
		{
			name: "bulk insert",
			seed: `async function main() { await prisma.product.createMany({ data: products }); }`,
			want: "seed_rows",
		},
		{
			// A different client identifier must not hide the writes.
			name: "aliased client",
			seed: `async function main() {
  await db.user.upsert({ where: { email }, update: {}, create: { email } });
  await db.category.create({ data: { name: "A" } });
}`,
			want: "seed_rows",
		},
		{
			// An app with no login seeds nothing; the file stays so the image's
			// start-up `prisma db seed` is a no-op instead of an error.
			name: "no-op seed",
			seed: `async function main() { /* nothing to seed */ }`,
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, filepath.Join("prisma", "seed.ts"), tc.seed)
			write(t, dir, "TEST_CASES.md", "# เอกสารทดสอบ")

			if got := ruleOf(t, CheckHarnessRules(dir)); got != tc.want {
				t.Fatalf("rule = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCheckSeedNamesTheModels(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, filepath.Join("prisma", "seed.ts"), `async function main() {
  await prisma.user.upsert({ where: { email }, update: {}, create: { email } });
  await prisma.invoice.create({ data: {} });
}`)
	write(t, dir, "TEST_CASES.md", "# เอกสารทดสอบ")

	findings := CheckHarnessRules(dir)
	if len(findings) != 1 {
		t.Fatalf("expected one finding, got %+v", findings)
	}
	// The operator judges whether a second model is legitimate reference data,
	// so the finding has to say which models were written.
	for _, want := range []string{"invoice", "user"} {
		if !strings.Contains(findings[0].Text, want) {
			t.Errorf("finding does not name model %q: %s", want, findings[0].Text)
		}
	}
}

func TestNoSeedNoFinding(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "TEST_CASES.md", "# เอกสารทดสอบ")

	if got := ruleOf(t, CheckHarnessRules(dir)); got != "" {
		t.Fatalf("a static app has no seed to judge, got %q", got)
	}
}

func TestMissingTestCases(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, filepath.Join("prisma", "seed.ts"), compliantSeed)

	if got := ruleOf(t, CheckHarnessRules(dir)); got != "test_cases_missing" {
		t.Fatalf("rule = %q, want test_cases_missing", got)
	}
}

func TestPortChecklist(t *testing.T) {
	cases := []struct {
		name      string
		checklist string
		want      string
	}{
		{"fully ticked", "- [x] Dashboard\n- [x] Catalog\n", ""},
		{"one open item", "- [x] Dashboard\n- [ ] Catalog\n", "checklist_open"},
		{
			// Orphans stay unticked on purpose: declared, with evidence, so they
			// remain visible. That is not unfinished work.
			name:      "orphan excluded",
			checklist: "- [x] Dashboard\n- [ ] [orphan] CatalogPage.tsx — not imported by anything\n",
			want:      "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, "TEST_CASES.md", "# เอกสารทดสอบ")
			write(t, dir, "PORT_CHECKLIST.md", tc.checklist)

			if got := ruleOf(t, CheckHarnessRules(dir)); got != tc.want {
				t.Fatalf("rule = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCleanBuildHasNoFindings(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, filepath.Join("prisma", "seed.ts"), compliantSeed)
	write(t, dir, "TEST_CASES.md", "# เอกสารทดสอบ")
	write(t, dir, "PORT_CHECKLIST.md", "- [x] Dashboard\n")

	if findings := CheckHarnessRules(dir); len(findings) != 0 {
		t.Fatalf("expected a clean build to be quiet, got %+v", findings)
	}
}
