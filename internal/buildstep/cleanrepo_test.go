package buildstep

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// evilScript is a command that leaves a mark when anything runs it.
func evilScript(t *testing.T) (script, marks string) {
	t.Helper()
	dir := t.TempDir()
	script, marks = filepath.Join(dir, "evil"), filepath.Join(dir, "marks")
	body := fmt.Sprintf("#!/bin/sh\necho \"$0 $*\" >> %q\ncat 2>/dev/null\nexit 0\n", marks)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, marks
}

func noMarks(t *testing.T, marks string) {
	t.Helper()
	if raw, err := os.ReadFile(marks); err == nil {
		t.Errorf("a command the repository named was run:\n%s", raw)
	}
}

// remoteWithBranch is a bare remote whose main holds one commit.
func remoteWithBranch(t *testing.T) (remote, head string) {
	t.Helper()
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	gitIn(t, root, "init", "-q", "--bare", "-b", "main", remote)
	seed := filepath.Join(root, "seed")
	gitIn(t, root, "init", "-q", "-b", "main", seed)
	gitIn(t, seed, "config", "user.email", "t@example.test")
	gitIn(t, seed, "config", "user.name", "t")
	writeTree(t, seed, "app.js", "one\n")
	writeTree(t, seed, "gone.txt", "removed by the build\n")
	gitIn(t, seed, "add", "-A")
	gitIn(t, seed, "commit", "-qm", "seed")
	gitIn(t, seed, "push", "-q", remote, "main")
	return remote, gitIn(t, seed, "rev-parse", "HEAD")
}

// Whatever a repository's config says, CRN's settings win, and they name no command. The
// list is written out here so that dropping one from gitConfig fails.
func TestCRNsGitSettingsOverrideTheRepository(t *testing.T) {
	want := map[string]string{
		"core.hooksPath":          "/dev/null",
		"core.fsmonitor":          "false",
		"core.sshCommand":         "ssh",
		"core.askPass":            "",
		"core.attributesFile":     "/dev/null",
		"core.excludesFile":       "/dev/null",
		"credential.helper":       "osxkeychain",
		"protocol.allow":          "never",
		"protocol.https.allow":    "always",
		"protocol.http.allow":     "always",
		"protocol.ssh.allow":      "always",
		"protocol.file.allow":     "always",
		"commit.gpgsign":          "false",
		"push.gpgSign":            "false",
		"gc.auto":                 "0",
		"maintenance.auto":        "false",
		"submodule.recurse":       "false",
		"fetch.recurseSubmodules": "false",
		"push.recurseSubmodules":  "no",
	}
	script, _ := evilScript(t)
	dir := newRepo(t)
	for key := range want {
		gitIn(t, dir, "config", "--local", key, script)
	}
	for key, value := range want {
		out, err := gitCommand(context.Background(), dir, "config", "--get", key).Output()
		if err != nil {
			t.Errorf("%s: %v", key, err)
		}
		if got := strings.TrimSpace(string(out)); got != value {
			t.Errorf("%s = %q, want %q", key, got, value)
		}
	}
}

// git still finds the protocols CRN uses, and refuses the one that runs a command.
func TestOnlyTheTransportsCRNUsesAreAllowed(t *testing.T) {
	script, marks := evilScript(t)
	dir := newRepo(t)
	if err := gitCommand(context.Background(), dir, "ls-remote", "ext::"+script+" %S x").Run(); err == nil {
		t.Error("ext:: was allowed")
	}
	noMarks(t, marks)
	remote, head := remoteWithBranch(t)
	out, err := gitCommand(context.Background(), dir, "ls-remote", remote, "refs/heads/main").Output()
	if err != nil || !strings.Contains(string(out), head) {
		t.Errorf("a file remote: %q, %v", out, err)
	}
}

// Past those settings, git reads the repository's config and nothing above it: the home
// directory and the system are files an agent can write.
func TestGitReadsNoConfigFileAboveTheRepository(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	writeTree(t, home, ".gitconfig", "[user]\n\tname = planted\n")
	writeTree(t, home, ".config/git/config", "[user]\n\temail = planted\n")
	dir := newRepo(t)
	out, err := gitCommand(context.Background(), dir, "config", "--list", "--show-scope").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		scope, _, _ := strings.Cut(line, "\t")
		if scope != "command" && scope != "local" {
			t.Errorf("git read a config file of scope %q: %s", scope, line)
		}
	}
	if strings.Contains(string(out), "planted") {
		t.Errorf("git read the home directory:\n%s", out)
	}
}

func TestNoGitVariableIsPassedOn(t *testing.T) {
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
		"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_SYSTEM",
		"GIT_SSH", "GIT_SSH_COMMAND", "GIT_ASKPASS", "GIT_PROXY_COMMAND", "GIT_EXEC_PATH", "GIT_TEMPLATE_DIR", "GIT_TRACE", "SSH_ASKPASS"} {
		t.Setenv(name, "planted")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "planted")
	allowed := map[string]string{"GIT_TERMINAL_PROMPT": "0", "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_ATTR_NOSYSTEM": "1"}
	seen := map[string]bool{}
	for _, kv := range gitEnv() {
		name, value, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "SSH_ASKPASS") {
			t.Errorf("%s is passed on", name)
		}
		if !strings.HasPrefix(name, "GIT_") {
			continue
		}
		if allowed[name] != value {
			t.Errorf("%s=%s is passed on", name, value)
		}
		seen[name] = true
	}
	for name := range allowed {
		if !seen[name] {
			t.Errorf("%s is not set", name)
		}
	}
}

// A helper the repository names, plainly or for one URL, is not run; CRN's own is.
func TestACredentialHelperInTheRepositoryIsNotRun(t *testing.T) {
	script, marks := evilScript(t)
	dir := newRepo(t)
	gitIn(t, dir, "config", "--local", "credential.helper", "!"+script)
	gitIn(t, dir, "config", "--local", "credential.https://example.test.helper", "!"+script)
	cmd := gitCommand(context.Background(), dir, "credential", "fill")
	cmd.Stdin = strings.NewReader("protocol=https\nhost=example.test\n\n")
	_ = cmd.Run() // nothing answers, and no prompt is allowed: an error is expected
	noMarks(t, marks)
}

// A rewritten URL cannot turn a push into a command.
func TestARewrittenURLCannotRunACommand(t *testing.T) {
	script, marks := evilScript(t)
	remote, _ := remoteWithBranch(t)
	dir := newRepo(t)
	gitIn(t, dir, "config", "--local", "url.ext::"+script+" %S .insteadOf", remote)
	writeTree(t, dir, "a.txt", "a\n")
	_, err := GitCommitAndPush(context.Background(), dir, remote, "main", "m", true, nil)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("err = %v, want the transport refused", err)
	}
	noMarks(t, marks)
}

// The path a project with no list keeps: its .git stays, its hooks do not run.
func TestGitCommitAndPushRunsNoHook(t *testing.T) {
	script, marks := evilScript(t)
	hooks := t.TempDir()
	remote, _ := remoteWithBranch(t)
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	for _, name := range []string{"pre-commit", "prepare-commit-msg", "commit-msg", "post-commit", "pre-push", "reference-transaction", "post-index-change", "pre-auto-gc"} {
		for _, in := range []string{filepath.Join(dir, ".git", "hooks"), hooks} {
			if err := os.MkdirAll(in, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(in, name), []byte("#!/bin/sh\nexec "+script+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	gitIn(t, dir, "config", "--local", "core.hooksPath", hooks)
	gitIn(t, dir, "config", "--local", "core.fsmonitor", script)
	gitIn(t, dir, "config", "--local", "gc.auto", "1")
	writeTree(t, dir, "a.txt", "a\n")
	commit, err := GitCommitAndPush(context.Background(), dir, remote, "built", "m", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	noMarks(t, marks)
	if head := gitIn(t, remote, "rev-parse", "refs/heads/built"); head != commit {
		t.Errorf("the remote is on %s, GitCommitAndPush returned %s", head, commit)
	}
}

// A pushurl in the repo would otherwise win over the origin CRN sets.
func TestGitCommitAndPushIgnoresAPushURL(t *testing.T) {
	for _, force := range []bool{true, false} {
		t.Run(fmt.Sprintf("force=%v", force), func(t *testing.T) {
			remote, _ := remoteWithBranch(t)
			decoy := filepath.Join(t.TempDir(), "decoy.git")
			gitIn(t, filepath.Dir(decoy), "init", "-q", "--bare", decoy)
			dir := t.TempDir()
			gitIn(t, dir, "init", "-q", "-b", "main")
			gitIn(t, dir, "remote", "add", "origin", remote)
			gitIn(t, dir, "config", "--local", "remote.origin.pushurl", decoy)
			writeTree(t, dir, "a.txt", "a\n")
			commit, err := GitCommitAndPush(context.Background(), dir, remote, "built", "m", force, nil)
			if err != nil {
				t.Fatal(err)
			}
			if refs := gitIn(t, decoy, "for-each-ref"); refs != "" {
				t.Errorf("the decoy received:\n%s", refs)
			}
			if head := gitIn(t, remote, "rev-parse", "refs/heads/built"); head != commit {
				t.Errorf("the remote is on %s, want %s", head, commit)
			}
		})
	}
}

func TestGitCloneOrPullRunsNoHook(t *testing.T) {
	script, marks := evilScript(t)
	remote, head := remoteWithBranch(t)
	dir := filepath.Join(t.TempDir(), "workspace")
	gitIn(t, filepath.Dir(dir), "clone", "-q", remote, dir)
	for _, name := range []string{"post-checkout", "post-merge", "reference-transaction", "post-rewrite", "post-index-change"} {
		if err := os.WriteFile(filepath.Join(dir, ".git", "hooks", name), []byte("#!/bin/sh\nexec "+script+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := GitCloneOrPull(context.Background(), dir, remote, "main", nil); err != nil {
		t.Fatal(err)
	}
	noMarks(t, marks)
	if got := gitIn(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD = %s, want %s", got, head)
	}
}

func TestReplaceGitDirKeepsTheOldOneAside(t *testing.T) {
	script, marks := evilScript(t)
	remote, head := remoteWithBranch(t)
	dir := filepath.Join(t.TempDir(), "workspace")
	gitIn(t, filepath.Dir(dir), "clone", "-q", remote, dir)
	gitIn(t, dir, "config", "--local", "core.fsmonitor", script)
	gitIn(t, dir, "config", "--local", "url."+script+".insteadOf", remote)
	writeTree(t, dir, ".git/hooks/reference-transaction", "#!/bin/sh\nexec "+script+"\n")
	if err := os.Chmod(filepath.Join(dir, ".git/hooks/reference-transaction"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTree(t, dir, "ignored-leftover.txt", "kept\n")

	if err := ReplaceGitDir(context.Background(), dir, "main", nil); err != nil {
		t.Fatal(err)
	}
	asides, _ := filepath.Glob(dir + ".stale-git-*")
	if len(asides) != 1 {
		t.Fatalf("asides: %v", asides)
	}
	if _, err := os.Stat(filepath.Join(asides[0], "config")); err != nil {
		t.Errorf("the old .git is not aside: %v", err)
	}
	if err := GitCloneOrPull(context.Background(), dir, remote, "main", nil); err != nil {
		t.Fatal(err)
	}
	noMarks(t, marks)
	if got := gitIn(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD = %s, want %s", got, head)
	}
	if got := gitIn(t, dir, "symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Errorf("the checkout is on %s, want main", got)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "app.js")); err != nil || string(raw) != "one\n" {
		t.Errorf("app.js = %q, %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ignored-leftover.txt")); err != nil {
		t.Errorf("the workspace's other files must stay: %v", err)
	}
}

// A workspace with no .git has nothing to replace: GitCloneOrPull clones it.
func TestReplaceGitDirWithNoGitDoesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	if err := ReplaceGitDir(context.Background(), dir, "main", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("it made the workspace: %v", err)
	}
}

// A .git that is a link goes aside as the link: what it points at is not touched.
func TestReplaceGitDirMovesALinkAsALink(t *testing.T) {
	other := newRepo(t)
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(other, ".git"), filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceGitDir(context.Background(), dir, "main", nil); err != nil {
		t.Fatal(err)
	}
	asides, _ := filepath.Glob(dir + ".stale-git-*")
	if len(asides) != 1 {
		t.Fatalf("asides: %v", asides)
	}
	if info, err := os.Lstat(asides[0]); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("aside: %v, %v", info, err)
	}
	if info, err := os.Lstat(filepath.Join(dir, ".git")); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("the new .git: %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(other, ".git", "config")); err != nil {
		t.Errorf("the linked repository was touched: %v", err)
	}
}

// The same job can run again, as a retry does: nothing aside is lost or in the way.
func TestReplaceGitDirCanRunTwice(t *testing.T) {
	dir := newRepo(t)
	for i := 0; i < 2; i++ {
		if err := ReplaceGitDir(context.Background(), dir, "main", nil); err != nil {
			t.Fatal(err)
		}
	}
	if asides, _ := filepath.Glob(dir + ".stale-git-*"); len(asides) != 2 {
		t.Errorf("asides: %v", asides)
	}
}

// A link that points nowhere still lies at .git, and git would stop at it.
func TestReplaceGitDirMovesABrokenLink(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceGitDir(context.Background(), dir, "main", nil); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(filepath.Join(dir, ".git")); err != nil || !info.IsDir() {
		t.Errorf("the new .git: %v, %v", info, err)
	}
}

// fixtureWorkspace is a workspace on the branch with every kind of change on it.
func fixtureWorkspace(t *testing.T, dir string) {
	t.Helper()
	writeTree(t, dir, "app.js", "two\n")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeTree(t, dir, "src/new.js", "new\n")
	writeTree(t, dir, "run.sh", "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("src/new.js", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	writeTree(t, dir, "crlf.txt", "line\r\n")
	writeTree(t, dir, ".gitignore", "secret.env\n")
	writeTree(t, dir, "secret.env", "left out by the project\n")
	writeTree(t, dir, "node_modules/x/index.js", "left out by CRN\n")
	writeTree(t, dir, ".next/out.js", "left out by CRN\n")
	writeTree(t, dir, ".claude/skills/a.md", "left out by CRN\n")
	writeTree(t, dir, "debug.log", "left out by CRN\n")
	writeTree(t, dir, ".gitattributes", "crlf.txt -text\n")
}

// The same workspace makes the same tree as the path every other project takes, so a listed
// project's commit holds what it always held.
func TestACleanCommitHoldsWhatTheWorkspaceRepoWouldHold(t *testing.T) {
	ctx := context.Background()
	remote, head := remoteWithBranch(t)

	viaWorkspace := filepath.Join(t.TempDir(), "w")
	gitIn(t, filepath.Dir(viaWorkspace), "clone", "-q", remote, viaWorkspace)
	fixtureWorkspace(t, viaWorkspace)
	workspaceCommit, err := GitCommitAndPush(ctx, viaWorkspace, remote, "via-workspace", "m", true, nil)
	if err != nil {
		t.Fatal(err)
	}

	clean := filepath.Join(t.TempDir(), "w")
	gitIn(t, filepath.Dir(clean), "clone", "-q", remote, clean)
	fixtureWorkspace(t, clean)
	repo, err := OpenCleanRepo(ctx, remote, "main", head, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	commit, err := repo.Commit(ctx, clean, "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Push(ctx, commit, false); err != nil {
		t.Fatal(err)
	}
	if want, got := gitIn(t, remote, "rev-parse", workspaceCommit+"^{tree}"), gitIn(t, remote, "rev-parse", commit+"^{tree}"); want != got {
		t.Errorf("the clean tree differs:\n%s", gitIn(t, remote, "diff", "--stat", workspaceCommit, commit))
	}
	if parent := gitIn(t, remote, "rev-parse", commit+"^"); parent != head {
		t.Errorf("parent = %s, want %s", parent, head)
	}
	if got := gitIn(t, remote, "rev-parse", "refs/heads/main"); got != commit {
		t.Errorf("main = %s, want %s", got, commit)
	}
	if got := gitIn(t, remote, "log", "-1", "--format=%an <%ae>", commit); got != "FITT Code Runner <crn@fitt.local>" {
		t.Errorf("author = %q", got)
	}
}

func TestACleanCommitWithNothingChangedIsTheBase(t *testing.T) {
	ctx := context.Background()
	remote, head := remoteWithBranch(t)
	dir := filepath.Join(t.TempDir(), "w")
	gitIn(t, filepath.Dir(dir), "clone", "-q", remote, dir)
	repo, err := OpenCleanRepo(ctx, remote, "main", head, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	commit, err := repo.Commit(ctx, dir, "m")
	if err != nil {
		t.Fatal(err)
	}
	if commit != head {
		t.Errorf("commit = %s, want the base %s", commit, head)
	}
	if err := repo.Push(ctx, commit, false); err != nil {
		t.Errorf("pushing the base: %v", err)
	}
}

// The repository holds no hooks, not even git's samples: nothing is there to be run or
// replaced.
func TestACleanRepoStartsWithNoTemplate(t *testing.T) {
	repo, err := OpenCleanRepo(context.Background(), "unused", "main", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := os.Stat(filepath.Join(repo.Dir, "hooks")); !os.IsNotExist(err) {
		t.Errorf("the repository has a hooks directory: %v", err)
	}
	info, err := os.Stat(repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("mode %o, want 700", perm)
	}
	repo.Close()
	if _, err := os.Stat(repo.Dir); !os.IsNotExist(err) {
		t.Errorf("Close left the repository: %v", err)
	}
}

// What a command prints is read under its name, however the command was called.
func TestGitVerbIsTheSubcommand(t *testing.T) {
	for want, args := range map[string][]string{
		"add":         {"--git-dir=/x", "--work-tree=/y", "add", "-A"},
		"push":        {"push", "--force", "origin"},
		"commit-tree": {"-c", "user.name=FITT Code Runner", "commit-tree", "abc"},
		"":            {"--version"},
	} {
		if got := gitVerb(args); got != want {
			t.Errorf("gitVerb(%q) = %q, want %q", args, got, want)
		}
	}
}

// The gate reads what a clean repository's commands print, and names each by its verb.
func TestAGitCommandIsReadUnderItsName(t *testing.T) {
	seen := map[string]bool{}
	ctx := WithCommandOutput(context.Background(), func(name string, _ []byte) { seen[name] = true })
	remote, head := remoteWithBranch(t)
	repo, err := OpenCleanRepo(ctx, remote, "main", head, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	dir := t.TempDir()
	writeTree(t, dir, "a.txt", "a\n")
	commit, err := repo.Commit(ctx, dir, "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Push(ctx, commit, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"git rev-parse", "git write-tree", "git commit-tree", "git push"} {
		if !seen[want] {
			t.Errorf("%s was not read; read %v", want, seen)
		}
	}
}

// A real remote through a real credential helper, with no file on it changed: the fetch and
// a dry-run push ask the remote for read and write. With a workspace named, the commit of
// what it holds must be the branch's head: the repository sees no change that is not there.
// Opt-in, on the machine and in the session that holds the credentials.
func TestARealRemoteAnswersACleanRepo(t *testing.T) {
	remote, branch := os.Getenv("CRN_TEST_GIT_REMOTE"), os.Getenv("CRN_TEST_GIT_BRANCH")
	if remote == "" || branch == "" {
		t.Skip("set CRN_TEST_GIT_REMOTE and CRN_TEST_GIT_BRANCH")
	}
	ctx := context.Background()
	head, err := RemoteHead(ctx, t.TempDir(), remote, branch, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s is on %s", branch, head)
	repo, err := OpenCleanRepo(ctx, remote, branch, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	t.Log("fetch: passed")
	if out, err := runGit(ctx, repo.Dir, slog.Default(), "--git-dir="+repo.Dir, "push", "--dry-run", remote, head+":refs/heads/crn-clean-repo-dry-run"); err != nil {
		t.Fatalf("dry-run push: %v\n%s", err, out)
	}
	t.Log("dry-run push: passed, nothing sent")
	if workspace := os.Getenv("CRN_TEST_WORKSPACE"); workspace != "" {
		commit, err := repo.Commit(ctx, workspace, "not pushed")
		if err != nil {
			t.Fatal(err)
		}
		if commit != head {
			t.Errorf("the workspace commits to %s, want no change from %s:\n%s", commit, head, gitIn(t, repo.Dir, "diff", "--stat", head, commit))
		} else {
			t.Log("the workspace's commit is the branch's head: no change seen")
		}
	}
}

// A new build has no base: one commit with no parent, replacing the branch.
func TestACleanCommitWithoutABaseStartsANewHistory(t *testing.T) {
	ctx := context.Background()
	remote, _ := remoteWithBranch(t)
	dir := t.TempDir()
	writeTree(t, dir, "fresh.js", "fresh\n")
	repo, err := OpenCleanRepo(ctx, remote, "main", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	commit, err := repo.Commit(ctx, dir, "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Push(ctx, commit, false); err == nil {
		t.Error("a push without force replaced the branch")
	}
	if err := repo.Push(ctx, commit, true); err != nil {
		t.Fatal(err)
	}
	if got := gitIn(t, remote, "rev-list", "--count", "refs/heads/main"); got != "1" {
		t.Errorf("%s commits, want 1", got)
	}
	if got := gitIn(t, remote, "ls-tree", "--name-only", "refs/heads/main"); got != "fresh.js" {
		t.Errorf("the tree holds %q", got)
	}
}

func TestACleanRepoRefusesABranchThatMoved(t *testing.T) {
	ctx := context.Background()
	remote, head := remoteWithBranch(t)
	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, filepath.Dir(other), "clone", "-q", remote, other)
	gitIn(t, other, "config", "user.email", "t@example.test")
	gitIn(t, other, "config", "user.name", "t")
	gitIn(t, other, "commit", "-q", "--allow-empty", "-m", "moved")
	gitIn(t, other, "push", "-q", "origin", "main")
	if _, err := OpenCleanRepo(ctx, remote, "main", head, nil); err == nil || !strings.Contains(err.Error(), "moved") {
		t.Errorf("err = %v, want the move named", err)
	}
}

// A repository nested in the workspace is a submodule to git. The commit may say so, and
// the export refuses it: a commit does not hold its content.
func TestANestedRepositoryIsRefusedOnExport(t *testing.T) {
	ctx := context.Background()
	remote, head := remoteWithBranch(t)
	dir := filepath.Join(t.TempDir(), "w")
	gitIn(t, filepath.Dir(dir), "clone", "-q", remote, dir)
	nested := filepath.Join(dir, "vendor", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, nested, "init", "-q", "-b", "main")
	gitIn(t, nested, "config", "user.email", "t@example.test")
	gitIn(t, nested, "config", "user.name", "t")
	gitIn(t, nested, "commit", "-q", "--allow-empty", "-m", "nested")
	repo, err := OpenCleanRepo(ctx, remote, "main", head, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	commit, err := repo.Commit(ctx, dir, "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := ExportCommit(ctx, repo.Dir, commit, t.TempDir()); err == nil {
		t.Error("the export took a submodule")
	}
}

// The repository exports and scans like any other, through the same commands.
func TestACleanRepoIsReadByTheExportAndTheScan(t *testing.T) {
	ctx := context.Background()
	remote, head := remoteWithBranch(t)
	dir := filepath.Join(t.TempDir(), "w")
	gitIn(t, filepath.Dir(dir), "clone", "-q", remote, dir)
	fixtureWorkspace(t, dir)
	repo, err := OpenCleanRepo(ctx, remote, "main", head, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	commit, err := repo.Commit(ctx, dir, "m")
	if err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := ExportCommit(ctx, repo.Dir, commit, dest); err != nil {
		t.Fatal(err)
	}
	for _, left := range []string{"secret.env", "node_modules/x/index.js", ".next/out.js", ".claude/skills/a.md", "debug.log", "gone.txt", ".git"} {
		if _, err := os.Lstat(filepath.Join(dest, left)); err == nil {
			t.Errorf("the export holds %s", left)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(dest, "crlf.txt")); err != nil || string(raw) != "line\r\n" {
		t.Errorf("crlf.txt = %q, %v", raw, err)
	}
	if info, err := os.Stat(filepath.Join(dest, "run.sh")); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Errorf("run.sh lost its mode: %v, %v", info, err)
	}
	if target, err := os.Readlink(filepath.Join(dest, "link")); err != nil || target != "src/new.js" {
		t.Errorf("link = %q, %v", target, err)
	}
}
