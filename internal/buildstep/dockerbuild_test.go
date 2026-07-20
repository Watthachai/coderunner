package buildstep

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDemoImageTag(t *testing.T) {
	const pid = "cc0b6195-46b4-4c29-a167-c0dd321d10d9" // -> id8 "cc0b6195"
	cases := []struct {
		registry, name, projectID string
		buildNo                   int
		want                      string
	}{
		{"", "Cafe Pre-order & Pick-up", pid, 8, "crn-demo-cafe-pre-order-pick-up-cc0b6195:v8"},
		{"registry.gitlab.local/fitt", "Cafe Pre-order & Pick-up", pid, 8, "registry.gitlab.local/fitt/crn-demo-cafe-pre-order-pick-up-cc0b6195:v8"},
		{"registry.gitlab.local/fitt/", "X", pid, 1, "registry.gitlab.local/fitt/crn-demo-x-cc0b6195:v1"}, // trailing slash trimmed
		{"", "", pid, 3, "crn-demo-demo-cc0b6195:v3"},                                                     // empty name -> "demo"
		{"", "My_App.v2", pid, 5, "crn-demo-my-app-v2-cc0b6195:v5"},                                       // non-charset collapsed to "-"
		{"", "X", "", 1, "crn-demo-x:v1"},                                                                 // no project id -> no suffix
		// Two different projects sharing a name do NOT collide (id8 differs):
		{"reg", "Shop", "aaaaaaaa-1111", 1, "reg/crn-demo-shop-aaaaaaaa:v1"},
		{"reg", "Shop", "bbbbbbbb-2222", 1, "reg/crn-demo-shop-bbbbbbbb:v1"},
	}
	for _, c := range cases {
		if got := DemoImageTag(c.registry, c.name, c.projectID, c.buildNo); got != c.want {
			t.Errorf("DemoImageTag(%q,%q,%q,%d) = %q, want %q", c.registry, c.name, c.projectID, c.buildNo, got, c.want)
		}
	}
}

func TestIsNextApp(t *testing.T) {
	dir := t.TempDir()
	// no package.json
	if IsNextApp(dir) {
		t.Error("no package.json should not be a Next app")
	}
	// non-next
	writePkg(t, dir, `{"dependencies":{"vite":"^5"}}`)
	if IsNextApp(dir) {
		t.Error("vite app should not be a Next app")
	}
	// next in dependencies
	writePkg(t, dir, `{"dependencies":{"next":"16.2.9","react":"19"}}`)
	if !IsNextApp(dir) {
		t.Error("next in dependencies should be a Next app")
	}
	// next in devDependencies
	writePkg(t, dir, `{"devDependencies":{"next":"16"}}`)
	if !IsNextApp(dir) {
		t.Error("next in devDependencies should be a Next app")
	}
}

func TestMigrateImageTag(t *testing.T) {
	const pid = "cc0b6195-46b4-4c29-a167-c0dd321d10d9"
	got := MigrateImageTag("reg/fitt/demos", "Shop", pid, 3)
	want := "reg/fitt/demos/crn-demo-shop-cc0b6195-migrate:v3"
	if got != want {
		t.Errorf("MigrateImageTag = %q, want %q", got, want)
	}
}

func TestWriteImageBundle(t *testing.T) {
	const appImg = "reg/fitt/demos/crn-demo-shop-cc0b6195:v3"
	const migImg = "reg/fitt/demos/crn-demo-shop-cc0b6195-migrate:v3"

	// Non-next app: skipped, nothing written.
	static := t.TempDir()
	writePkg(t, static, `{"dependencies":{"vite":"^5"}}`)
	if wrote, err := WriteImageBundle(static, appImg, migImg, "reg", 4123, nil); err != nil || wrote {
		t.Fatalf("non-next: wrote=%v err=%v, want false/nil", wrote, err)
	}
	if _, err := os.Stat(filepath.Join(static, "Dockerfile")); err == nil {
		t.Error("bundle written for a non-next app")
	}

	// Next app: full bundle written.
	next := t.TempDir()
	writePkg(t, next, `{"dependencies":{"next":"16"}}`)
	wrote, err := WriteImageBundle(next, appImg, migImg, "reg", 4123, nil)
	if err != nil || !wrote {
		t.Fatalf("next: wrote=%v err=%v, want true/nil", wrote, err)
	}
	for _, f := range []string{"Dockerfile", ".dockerignore", "Dockerfile.migrate", "docker-compose.customer.yml", "INSTALL.md"} {
		if _, err := os.Stat(filepath.Join(next, f)); err != nil {
			t.Errorf("%s not written: %v", f, err)
		}
	}
	// App Dockerfile ships only the standalone output (no source).
	df, _ := os.ReadFile(filepath.Join(next, "Dockerfile"))
	if !strings.Contains(string(df), ".next/standalone") {
		t.Error("app Dockerfile should copy the standalone output")
	}
	// Migrate Dockerfile runs migrate deploy + seed, copies only prisma (no app source).
	mf, _ := os.ReadFile(filepath.Join(next, "Dockerfile.migrate"))
	if !strings.Contains(string(mf), "prisma migrate deploy") || !strings.Contains(string(mf), "prisma db seed") {
		t.Error("migrate Dockerfile should migrate deploy + seed")
	}
	if strings.Contains(string(mf), "COPY . .") {
		t.Error("migrate Dockerfile must NOT copy the whole app source")
	}
	// Customer compose references both images + the rendered port, no leftover placeholders.
	cc, _ := os.ReadFile(filepath.Join(next, "docker-compose.customer.yml"))
	s := string(cc)
	for _, want := range []string{appImg, migImg, ":-4123}:3000", "service_completed_successfully"} {
		if !strings.Contains(s, want) {
			t.Errorf("customer compose missing %q", want)
		}
	}
	if strings.Contains(s, "{{") {
		t.Error("customer compose has an unrendered placeholder")
	}
}

func writePkg(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
