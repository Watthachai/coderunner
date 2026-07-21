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

func TestWriteImageBundle(t *testing.T) {
	const appImg = "reg/fitt/demos/crn-demo-shop-cc0b6195:v3"

	// Non-next app: skipped, nothing written.
	static := t.TempDir()
	writePkg(t, static, `{"dependencies":{"vite":"^5"}}`)
	if wrote, err := WriteImageBundle(static, appImg, "reg", 4123, nil); err != nil || wrote {
		t.Fatalf("non-next: wrote=%v err=%v, want false/nil", wrote, err)
	}
	if _, err := os.Stat(filepath.Join(static, "Dockerfile")); err == nil {
		t.Error("bundle written for a non-next app")
	}

	// Next app: full bundle written (no separate migrate image — app self-migrates).
	next := t.TempDir()
	writePkg(t, next, `{"dependencies":{"next":"16"}}`)
	wrote, err := WriteImageBundle(next, appImg, "reg", 4123, nil)
	if err != nil || !wrote {
		t.Fatalf("next: wrote=%v err=%v, want true/nil", wrote, err)
	}
	for _, f := range []string{"Dockerfile", ".dockerignore", "docker-compose.customer.yml", "INSTALL.md"} {
		if _, err := os.Stat(filepath.Join(next, f)); err != nil {
			t.Errorf("%s not written: %v", f, err)
		}
	}
	// No separate migrate image is written anymore.
	if _, err := os.Stat(filepath.Join(next, "Dockerfile.migrate")); err == nil {
		t.Error("Dockerfile.migrate should NOT be written (app self-migrates)")
	}
	// App Dockerfile ships only the standalone output (no source) and self-migrates
	// on start via a data-safe db push (no --accept-data-loss).
	df := readFile(t, filepath.Join(next, "Dockerfile"))
	for _, want := range []string{".next/standalone", "prisma db push", "node server.js"} {
		if !strings.Contains(df, want) {
			t.Errorf("app Dockerfile missing %q", want)
		}
	}
	if strings.Contains(df, "--accept-data-loss") {
		t.Error("app Dockerfile must NOT use --accept-data-loss (data-safe migrate)")
	}
	// Customer compose is app-only against an external DATABASE_URL — no bundled DB,
	// no migrate service, rendered port, no leftover placeholders.
	s := readFile(t, filepath.Join(next, "docker-compose.customer.yml"))
	for _, want := range []string{appImg, "DATABASE_URL", ":-4123}:3000"} {
		if !strings.Contains(s, want) {
			t.Errorf("customer compose missing %q", want)
		}
	}
	for _, unwanted := range []string{"postgres:16", "service_completed_successfully", "{{"} {
		if strings.Contains(s, unwanted) {
			t.Errorf("customer compose should not contain %q", unwanted)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func writePkg(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
