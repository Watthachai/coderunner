package buildstep

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDemoImageTag(t *testing.T) {
	cases := []struct {
		registry, name string
		buildNo        int
		want           string
	}{
		{"", "Cafe Pre-order & Pick-up", 8, "crn-demo-cafe-pre-order-pick-up:v8"},
		{"registry.gitlab.local/fitt", "Cafe Pre-order & Pick-up", 8, "registry.gitlab.local/fitt/crn-demo-cafe-pre-order-pick-up:v8"},
		{"registry.gitlab.local/fitt/", "X", 1, "registry.gitlab.local/fitt/crn-demo-x:v1"}, // trailing slash trimmed
		{"", "", 3, "crn-demo-demo:v3"},                                                    // empty name -> "demo"
		{"", "!!!___###", 2, "crn-demo-demo:v2"},                                           // nothing usable -> "demo"
		{"", "My_App.v2", 5, "crn-demo-my-app-v2:v5"},                                      // non-charset collapsed to "-"
	}
	for _, c := range cases {
		if got := DemoImageTag(c.registry, c.name, c.buildNo); got != c.want {
			t.Errorf("DemoImageTag(%q,%q,%d) = %q, want %q", c.registry, c.name, c.buildNo, got, c.want)
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

func TestWriteDockerfile(t *testing.T) {
	// Non-next app: skipped, nothing written.
	static := t.TempDir()
	writePkg(t, static, `{"dependencies":{"vite":"^5"}}`)
	if wrote, err := WriteDockerfile(static, nil); err != nil || wrote {
		t.Fatalf("non-next: wrote=%v err=%v, want false/nil", wrote, err)
	}
	if _, err := os.Stat(filepath.Join(static, "Dockerfile")); err == nil {
		t.Error("Dockerfile written for a non-next app")
	}

	// Next app: Dockerfile + .dockerignore written; standalone runner, no source copy.
	next := t.TempDir()
	writePkg(t, next, `{"dependencies":{"next":"16"}}`)
	wrote, err := WriteDockerfile(next, nil)
	if err != nil || !wrote {
		t.Fatalf("next: wrote=%v err=%v, want true/nil", wrote, err)
	}
	df, _ := os.ReadFile(filepath.Join(next, "Dockerfile"))
	if !strings.Contains(string(df), ".next/standalone") {
		t.Error("Dockerfile should copy the standalone output")
	}
	if _, err := os.Stat(filepath.Join(next, ".dockerignore")); err != nil {
		t.Errorf(".dockerignore not written: %v", err)
	}
}

func writePkg(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
