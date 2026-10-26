package buildstep

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The image's org.opencontainers.image.revision label and the build trace take
// the commit GitCommitAndPush returns, and FTCDV checks it against the repo by
// the full 40-character SHA, so a short SHA is not enough.
func TestGitCommitAndPushReturnsTheFullCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	remote := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.Command("git", "init", "--bare", "-q", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := GitCommitAndPush(context.Background(), dir, remote, "main", "build 1", nil)
	if err != nil {
		t.Fatalf("GitCommitAndPush: %v", err)
	}
	out, err := exec.Command("git", "-C", remote, "rev-parse", "refs/heads/main").Output()
	if err != nil {
		t.Fatalf("rev-parse remote main: %v", err)
	}
	if want := strings.TrimSpace(string(out)); got != want || len(got) != 40 {
		t.Errorf("GitCommitAndPush = %q, want the pushed commit %q (40 characters)", got, want)
	}
}
