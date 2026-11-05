package buildstep

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func writeTree(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "config", "user.email", "t@example.test")
	gitIn(t, dir, "config", "user.name", "t")
	return dir
}

func treeNames(t *testing.T, root string) []string {
	t.Helper()
	var names []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			rel, _ := filepath.Rel(root, path)
			names = append(names, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	return names
}

func TestExportHoldsExactlyWhatTheCommitHolds(t *testing.T) {
	dir := newRepo(t)
	writeTree(t, dir, "src/app.js", "tracked\n")
	writeTree(t, dir, "run.sh", "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("src/app.js", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	writeTree(t, dir, ".gitignore", "ignored.env\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-qm", "one")
	commit := gitIn(t, dir, "rev-parse", "HEAD")

	// What a build context must not pick up from the workspace.
	writeTree(t, dir, "ignored.env", "ignored\n")
	writeTree(t, dir, "untracked.txt", "untracked\n")
	writeTree(t, dir, "src/app.js", "edited after the commit\n")

	dest := t.TempDir()
	if err := ExportCommit(context.Background(), dir, commit, dest); err != nil {
		t.Fatal(err)
	}
	want := []string{".gitignore", "link", "run.sh", "src/app.js"}
	if got := treeNames(t, dest); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("export holds %q, want %q", got, want)
	}
	if raw, _ := os.ReadFile(filepath.Join(dest, "src/app.js")); string(raw) != "tracked\n" {
		t.Errorf("src/app.js = %q, want the committed bytes", raw)
	}
	if target, err := os.Readlink(filepath.Join(dest, "link")); err != nil || target != "src/app.js" {
		t.Errorf("link -> %q (%v), want the committed target", target, err)
	}
	if info, _ := os.Stat(filepath.Join(dest, "run.sh")); info.Mode().Perm() != 0o755 {
		t.Errorf("run.sh mode = %o, want 755", info.Mode().Perm())
	}
	if info, _ := os.Stat(filepath.Join(dest, "src/app.js")); info.Mode().Perm() != 0o644 {
		t.Errorf("src/app.js mode = %o, want 644", info.Mode().Perm())
	}
}

// git archive and a checkout both obey the tree's own .gitattributes: a file could hide
// from the scan and the build context, or differ from what the commit holds.
func TestAttributesInTheTreeCannotChangeTheExport(t *testing.T) {
	dir := newRepo(t)
	writeTree(t, dir, ".gitattributes", "hidden.txt export-ignore\nsubst.txt export-subst\ncrlf.txt eol=crlf\nfiltered.txt filter=nope\n")
	writeTree(t, dir, "hidden.txt", "must be exported\n")
	writeTree(t, dir, "subst.txt", "$Format:%H$\n")
	writeTree(t, dir, "crlf.txt", "one\ntwo\n")
	writeTree(t, dir, "filtered.txt", "as committed\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-qm", "one")
	dest := t.TempDir()
	if err := ExportCommit(context.Background(), dir, gitIn(t, dir, "rev-parse", "HEAD"), dest); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"hidden.txt":   "must be exported\n",
		"subst.txt":    "$Format:%H$\n",
		"crlf.txt":     "one\ntwo\n",
		"filtered.txt": "as committed\n",
	} {
		raw, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
		} else if string(raw) != want {
			t.Errorf("%s = %q, want %q", name, raw, want)
		}
		// The bytes hash to the blob the commit names.
		got := gitIn(t, dir, "hash-object", "--no-filters", filepath.Join(dest, name))
		blob := gitIn(t, dir, "rev-parse", "HEAD:"+name)
		if got != blob {
			t.Errorf("%s hashes to %s, the commit holds %s", name, got, blob)
		}
	}
}

// refs/replace would let a workspace answer other bytes for the same commit.
func TestAReplaceRefCannotChangeTheExport(t *testing.T) {
	dir := newRepo(t)
	writeTree(t, dir, "a.txt", "committed\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-qm", "one")
	commit := gitIn(t, dir, "rev-parse", "HEAD")
	blob := gitIn(t, dir, "rev-parse", "HEAD:a.txt")
	writeTree(t, dir, "other.txt", "swapped in\n")
	other := gitIn(t, dir, "hash-object", "-w", "other.txt")
	gitIn(t, dir, "replace", blob, other)
	dest := t.TempDir()
	if err := ExportCommit(context.Background(), dir, commit, dest); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(dest, "a.txt")); string(raw) != "committed\n" {
		t.Errorf("a.txt = %q, want the committed bytes", raw)
	}
}

func TestExportRefusesWhatItCannotHoldExactly(t *testing.T) {
	dir := newRepo(t)
	writeTree(t, dir, "a.txt", "x\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-qm", "one")
	commit := gitIn(t, dir, "rev-parse", "HEAD")
	if err := ExportCommit(context.Background(), dir, "--output=/tmp/x", t.TempDir()); err == nil {
		t.Error("exported a name that is not a commit id")
	}
	gitIn(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+commit+",vendor/sub")
	gitIn(t, dir, "commit", "-qm", "two")
	if err := ExportCommit(context.Background(), dir, gitIn(t, dir, "rev-parse", "HEAD"), t.TempDir()); err == nil {
		t.Error("exported a submodule, whose content the commit does not hold")
	}
}

func TestHistoryScanReadsEveryNewCommitNotJustTheLast(t *testing.T) {
	dir := newRepo(t)
	writeTree(t, dir, "a.txt", "base\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-qm", "base")
	base := gitIn(t, dir, "rev-parse", "HEAD")
	ctx := context.Background()

	clean := pinnedScanner(t)
	if err := ScanCommitHistory(ctx, dir, base, base, clean); err != nil {
		t.Fatal(err)
	}
	if clean.Problem() != nil {
		t.Fatalf("no new commit: %v", clean.Problem())
	}

	writeTree(t, dir, "notes.txt", "dial 203.0.113.250\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-qm", "with a value")
	if err := os.Remove(filepath.Join(dir, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-qm", "the value gone again")
	head := gitIn(t, dir, "rev-parse", "HEAD")

	s := pinnedScanner(t)
	if err := ScanCommitHistory(ctx, dir, base, head, s); err != nil {
		t.Fatal(err)
	}
	if s.Problem() == nil {
		t.Error("a value in an earlier commit of this build went unseen")
	}

	// A commit message and a file name are objects too.
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "message with fixture_role_x")
	inMessage := pinnedScanner(t)
	if err := ScanCommitHistory(ctx, dir, head, gitIn(t, dir, "rev-parse", "HEAD"), inMessage); err != nil {
		t.Fatal(err)
	}
	if inMessage.Problem() == nil {
		t.Error("a value in a commit message went unseen")
	}
	head = gitIn(t, dir, "rev-parse", "HEAD")
	writeTree(t, dir, "fixture_db_x.txt", "clean\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-qm", "named")
	inName := pinnedScanner(t)
	if err := ScanCommitHistory(ctx, dir, head, gitIn(t, dir, "rev-parse", "HEAD"), inName); err != nil {
		t.Fatal(err)
	}
	if inName.Problem() == nil {
		t.Error("a value in a file name went unseen")
	}

	// An empty base reads everything the commit reaches, as for a new project.
	all := pinnedScanner(t)
	if err := ScanCommitHistory(ctx, dir, "", gitIn(t, dir, "rev-parse", "HEAD"), all); err != nil {
		t.Fatal(err)
	}
	if all.Problem() == nil {
		t.Error("the whole history went unread")
	}
}

func TestRemoteHeadIsWhatTheRemoteHolds(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitIn(t, root, "init", "--bare", "-q", remote)
	dir := newRepo(t)
	writeTree(t, dir, "a.txt", "x\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-qm", "one")
	gitIn(t, dir, "push", "-q", remote, "main")
	commit := gitIn(t, dir, "rev-parse", "HEAD")
	got, err := RemoteHead(context.Background(), dir, remote, "main", nil)
	if err != nil || got != commit {
		t.Errorf("RemoteHead = %q (%v), want %s", got, err, commit)
	}
	if got, err := RemoteHead(context.Background(), dir, remote, "nope", nil); err == nil {
		t.Errorf("a branch the remote lacks gave %q", got)
	}
}

func TestCommandsHandWhatTheyPrintToTheSink(t *testing.T) {
	dir := newRepo(t)
	var seen []string
	ctx := WithCommandOutput(context.Background(), func(name string, output []byte) {
		seen = append(seen, name+": "+string(output))
	})
	if _, err := runGit(ctx, dir, discardLogger(), "rev-parse", "--is-inside-work-tree"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || !strings.Contains(seen[0], "git rev-parse: true") {
		t.Errorf("sink saw %q", seen)
	}
	// Without a sink nothing changes.
	if _, err := runGit(context.Background(), dir, discardLogger(), "rev-parse", "--is-inside-work-tree"); err != nil {
		t.Fatal(err)
	}
}
