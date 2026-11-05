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

	got, err := GitCommitAndPush(context.Background(), dir, remote, "main", "build 1", true, nil)
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

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// An edit build pushes without --force: a fast-forward lands, and a remote that
// moved underneath the build refuses the push instead of losing its commits.
func TestGitCommitAndPushWithoutForceNeverRewritesTheRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitIn(t, filepath.Dir(remote), "init", "--bare", "-q", remote)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	base, err := GitCommitAndPush(context.Background(), dir, remote, "main", "build 1", true, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Fast-forward: lands.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ff, err := GitCommitAndPush(context.Background(), dir, remote, "main", "build 2", false, nil)
	if err != nil {
		t.Fatalf("fast-forward push refused: %v", err)
	}
	if got := gitIn(t, remote, "rev-parse", "refs/heads/main"); got != ff || ff == base {
		t.Fatalf("remote main = %s, want the fast-forward %s", got, ff)
	}

	// The remote moves on elsewhere; a non-forced push of a diverged HEAD fails
	// and the remote keeps its commit.
	other := t.TempDir()
	gitIn(t, other, "clone", "-q", "-b", "main", remote, ".")
	gitIn(t, other, "-c", "user.email=x@x", "-c", "user.name=x", "commit", "-q", "--allow-empty", "-m", "elsewhere")
	gitIn(t, other, "push", "-q", "origin", "HEAD:refs/heads/main")
	moved := gitIn(t, remote, "rev-parse", "refs/heads/main")

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("v3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := GitCommitAndPush(context.Background(), dir, remote, "main", "build 3", false, nil); err == nil {
		t.Fatal("a non-forced push over a moved remote succeeded")
	}
	if got := gitIn(t, remote, "rev-parse", "refs/heads/main"); got != moved {
		t.Fatalf("remote main = %s after a refused push, want it untouched at %s", got, moved)
	}
}

// A build that stops before its first commit (cancelled, failed) leaves a working
// directory without .git. The next edit must still clone the remote: the
// leftovers are moved aside, not deleted, and not mixed into the checkout.
func TestGitCloneOrPullMovesAsideAnUnfinishedWorkspace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitIn(t, filepath.Dir(remote), "init", "--bare", "-q", remote)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "app.txt"), []byte("pushed from a laptop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	head, err := GitCommitAndPush(context.Background(), src, remote, "main", "code", true, nil)
	if err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "export.zip"), []byte("left by a cancelled build"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := GitCloneOrPull(context.Background(), dir, remote, "main", nil); err != nil {
		t.Fatalf("clone over an unfinished workspace: %v", err)
	}
	if got := gitIn(t, dir, "rev-parse", "HEAD"); got != head {
		t.Fatalf("workspace HEAD = %s, want the remote's %s", got, head)
	}
	if _, err := os.Stat(filepath.Join(dir, "export.zip")); !os.IsNotExist(err) {
		t.Error("the leftover file was mixed into the fresh checkout")
	}
	stale, _ := filepath.Glob(dir + ".stale-*")
	if len(stale) != 1 {
		t.Fatalf("moved-aside copies = %v, want exactly one", stale)
	}
	if b, err := os.ReadFile(filepath.Join(stale[0], "export.zip")); err != nil || string(b) != "left by a cancelled build" {
		t.Errorf("the leftovers were not kept: %v", err)
	}
}
