package jobs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// An agent runs as CRN's OS user, so it can write the workspace's .git and the home
// directory CRN shares with it. Whatever it leaves there must do nothing when CRN commits,
// pushes and reads the result of a listed project's build, and the build must still go
// through and be checked against the remote.

// everyHook is each hook git could run on the way through a commit, a push or a fetch.
var everyHook = []string{
	"pre-commit", "pre-merge-commit", "prepare-commit-msg", "commit-msg", "post-commit",
	"pre-push", "post-checkout", "post-merge", "post-rewrite", "pre-rebase", "pre-auto-gc",
	"reference-transaction", "post-index-change", "fsmonitor-watchman", "push-to-checkout",
}

// hostile is what the planted pieces share: a script that leaves a mark when anything runs
// it, and a decoy remote that must never receive anything.
type hostile struct {
	dir, marks, script, decoy string
}

func newHostile(t *testing.T) hostile {
	t.Helper()
	dir := t.TempDir()
	h := hostile{dir: dir, marks: filepath.Join(dir, "marks"), script: filepath.Join(dir, "evil"), decoy: filepath.Join(dir, "decoy.git")}
	body := fmt.Sprintf("#!/bin/sh\necho \"$0 $*\" >> %q\ncat 2>/dev/null\nexit 0\n", h.marks)
	if err := os.WriteFile(h.script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	plainGit(t, dir, "init", "--bare", "-q", h.decoy)
	return h
}

// plainGit runs the test's own git, with none of what a test plants in its environment.
func plainGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") && !strings.HasPrefix(kv, "HOME=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "HOME="+t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (h hostile) writeHooks(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range everyHook {
		if err := os.Link(h.script, filepath.Join(dir, name)); err != nil && !os.IsExist(err) {
			t.Fatal(err)
		}
	}
}

// config is every setting in a git config file that can run a command or send data
// somewhere else, the hook directory among them.
func (h hostile) config(remote string) [][2]string {
	return [][2]string{
		{"core.hooksPath", filepath.Join(h.dir, "hooks")},
		{"core.fsmonitor", h.script},
		{"core.sshCommand", h.script},
		{"core.askPass", h.script},
		{"credential.helper", "!" + h.script},
		{"filter.evil.clean", h.script},
		{"filter.evil.smudge", h.script},
		{"filter.evil.required", "true"},
		{"commit.gpgsign", "true"},
		{"gpg.program", h.script},
		{"gc.auto", "1"},
		{"remote.origin.pushurl", h.decoy},
		{"url." + h.decoy + ".insteadOf", remote},
		{"url." + h.decoy + ".pushInsteadOf", remote},
	}
}

// plantGit fills a repository's .git with all of it.
func (h hostile) plantGit(t *testing.T, repo, remote string) {
	t.Helper()
	h.writeHooks(t, filepath.Join(h.dir, "hooks"))
	h.writeHooks(t, filepath.Join(repo, ".git", "hooks"))
	for _, kv := range h.config(remote) {
		plainGit(t, repo, "config", "--local", kv[0], kv[1])
	}
	included := filepath.Join(h.dir, "included.config")
	if err := os.WriteFile(included, []byte(fmt.Sprintf("[core]\n\thooksPath = %s\n", filepath.Join(h.dir, "hooks"))), 0o644); err != nil {
		t.Fatal(err)
	}
	plainGit(t, repo, "config", "--local", "include.path", included)
	if err := os.MkdirAll(filepath.Join(repo, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "attributes"), []byte("* filter=evil\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (h hostile) nothingRan(t *testing.T) {
	t.Helper()
	if raw, err := os.ReadFile(h.marks); err == nil {
		t.Errorf("something the agent left was run:\n%s", raw)
	}
	if refs := plainGit(t, h.decoy, "for-each-ref"); refs != "" {
		t.Errorf("the decoy remote received:\n%s", refs)
	}
}

func remoteOf(workDir string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(workDir)), "remote.git")
}

// released is the build that went all the way: the agent's change is on the branch, one
// commit on top of it, and the image and the gate are of that commit.
func released(t *testing.T, run pipelineRun, file, want string) {
	t.Helper()
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", strings.Join(run.events, "\n"), run.logs)
	}
	head := run.head(t)
	if parent := plainGit(t, run.remote, "rev-parse", head+"^"); parent != run.seed {
		t.Errorf("the pushed commit's parent is %s, want %s", parent, run.seed)
	}
	if got := plainGit(t, run.remote, "show", head+":"+file); got != want {
		t.Errorf("%s in the commit = %q, want %q", file, got, want)
	}
	if i := indexOf(run.events, "docker build"); i < 0 || !strings.Contains(run.events[i], "org.opencontainers.image.revision="+head) {
		t.Errorf("the image is not of the pushed commit %s:\n%s", head, strings.Join(run.events, "\n"))
	}
	if i := indexOf(run.events, "node accept-candidate.mjs"); i < 0 || !strings.Contains(run.events[i], "--commit "+head) {
		t.Errorf("the gate did not run on the pushed commit %s:\n%s", head, strings.Join(run.events, "\n"))
	}
}

func change(t *testing.T, workDir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(workDir, "app.js"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAHookTheAgentLeftIsNotRun(t *testing.T) {
	for _, hook := range []string{"pre-commit", "pre-push"} {
		t.Run(hook, func(t *testing.T) {
			h := newHostile(t)
			run := runPipelineWith(t, pipelineOpts{agent: func(a agentRun) {
				change(t, a.spec.WorkDir)
				if err := os.Link(h.script, filepath.Join(a.spec.WorkDir, ".git", "hooks", hook)); err != nil {
					if err := os.WriteFile(filepath.Join(a.spec.WorkDir, ".git", "hooks", hook), []byte("#!/bin/sh\nexec "+h.script+"\n"), 0o755); err != nil {
						t.Fatal(err)
					}
				}
			}})
			released(t, run, "app.js", "changed")
			h.nothingRan(t)
		})
	}
}

func TestAHooksPathTheAgentSetIsNotUsed(t *testing.T) {
	h := newHostile(t)
	run := runPipelineWith(t, pipelineOpts{agent: func(a agentRun) {
		change(t, a.spec.WorkDir)
		h.writeHooks(t, filepath.Join(h.dir, "hooks"))
		plainGit(t, a.spec.WorkDir, "config", "--local", "core.hooksPath", filepath.Join(h.dir, "hooks"))
	}})
	released(t, run, "app.js", "changed")
	h.nothingRan(t)
}

// Everything at once: the hooks, and each setting that runs a command or moves the push.
func TestNothingInTheWorkspacesGitIsUsed(t *testing.T) {
	h := newHostile(t)
	run := runPipelineWith(t, pipelineOpts{agent: func(a agentRun) {
		change(t, a.spec.WorkDir)
		// The project's own attributes name the filter too. The tree may say so; only
		// config can make it run.
		if err := os.WriteFile(filepath.Join(a.spec.WorkDir, ".gitattributes"), []byte("app.js filter=evil\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		h.plantGit(t, a.spec.WorkDir, remoteOf(a.spec.WorkDir))
	}})
	released(t, run, "app.js", "changed")
	h.nothingRan(t)
}

// The .git an agent leaves can be anything, or nothing. The build does not depend on it.
func TestABuildDoesNotNeedTheWorkspacesGit(t *testing.T) {
	cases := map[string]func(t *testing.T, h hostile, workDir string){
		"removed": func(t *testing.T, _ hostile, workDir string) {
			if err := os.RemoveAll(filepath.Join(workDir, ".git")); err != nil {
				t.Fatal(err)
			}
		},
		"a link to another repository": func(t *testing.T, h hostile, workDir string) {
			other := filepath.Join(h.dir, "other")
			plainGit(t, h.dir, "init", "-q", other)
			h.plantGit(t, other, remoteOf(workDir))
			if err := os.RemoveAll(filepath.Join(workDir, ".git")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(other, ".git"), filepath.Join(workDir, ".git")); err != nil {
				t.Fatal(err)
			}
		},
		"a file naming another repository": func(t *testing.T, h hostile, workDir string) {
			other := filepath.Join(h.dir, "other")
			plainGit(t, h.dir, "init", "-q", other)
			h.plantGit(t, other, remoteOf(workDir))
			if err := os.RemoveAll(filepath.Join(workDir, ".git")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workDir, ".git"), []byte("gitdir: "+filepath.Join(other, ".git")+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, plant := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHostile(t)
			run := runPipelineWith(t, pipelineOpts{agent: func(a agentRun) {
				change(t, a.spec.WorkDir)
				plant(t, h, a.spec.WorkDir)
			}})
			released(t, run, "app.js", "changed")
			h.nothingRan(t)
		})
	}
}

// git reads the home directory by default, and the agent shares it with CRN.
func TestNothingInTheHomeDirectoryIsUsed(t *testing.T) {
	h := newHostile(t)
	home := t.TempDir()
	run := runPipelineWith(t, pipelineOpts{env: map[string]string{"HOME": home, "XDG_CONFIG_HOME": ""}, agent: func(a agentRun) {
		change(t, a.spec.WorkDir)
		if err := os.WriteFile(filepath.Join(a.spec.WorkDir, "crlf.txt"), []byte("line\r\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		h.writeHooks(t, filepath.Join(h.dir, "hooks"))
		config := "[core]\n\thooksPath = " + filepath.Join(h.dir, "hooks") + "\n\tfsmonitor = " + h.script + "\n" +
			"[credential]\n\thelper = !" + h.script + "\n" +
			"[url \"" + h.decoy + "\"]\n\tinsteadOf = " + remoteOf(a.spec.WorkDir) + "\n"
		xdg := filepath.Join(home, ".config", "git")
		if err := os.MkdirAll(xdg, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{
			filepath.Join(home, ".gitconfig"): config,
			filepath.Join(xdg, "config"):      config,
			filepath.Join(xdg, "ignore"):      "app.js\n",
			filepath.Join(xdg, "attributes"):  "*.txt text eol=lf\n",
		} {
			if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}})
	released(t, run, "app.js", "changed")
	h.nothingRan(t)
	head := run.head(t)
	if got := plainGit(t, run.remote, "cat-file", "-s", head+":crlf.txt"); got != "6" {
		t.Errorf("crlf.txt in the commit is %s bytes, want 6: an attributes file in the home changed it", got)
	}
}

// The GIT_ variables CRN inherited. An agent cannot reach CRN's own environment directly,
// but the service's start script or .env can carry them.
func TestNoGitVariableReachesGit(t *testing.T) {
	h := newHostile(t)
	h.writeHooks(t, filepath.Join(h.dir, "hooks"))
	other := filepath.Join(h.dir, "other")
	plainGit(t, h.dir, "init", "-q", other)
	h.writeHooks(t, filepath.Join(other, ".git", "hooks"))
	config := filepath.Join(h.dir, "evil.config")
	if err := os.WriteFile(config, []byte("[core]\n\thooksPath = "+filepath.Join(h.dir, "hooks")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(h.dir, "trace")
	env := map[string]string{
		"GIT_DIR":                          filepath.Join(other, ".git"),
		"GIT_WORK_TREE":                    other,
		"GIT_INDEX_FILE":                   filepath.Join(h.dir, "index"),
		"GIT_OBJECT_DIRECTORY":             filepath.Join(other, ".git", "objects"),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": filepath.Join(other, ".git", "objects"),
		"GIT_CONFIG_COUNT":                 "1",
		"GIT_CONFIG_KEY_0":                 "core.hooksPath",
		"GIT_CONFIG_VALUE_0":               filepath.Join(h.dir, "hooks"),
		"GIT_CONFIG_PARAMETERS":            "'core.hooksPath=" + filepath.Join(h.dir, "hooks") + "'",
		"GIT_CONFIG_GLOBAL":                config,
		"GIT_CONFIG_SYSTEM":                config,
		"GIT_SSH_COMMAND":                  h.script,
		"GIT_SSH":                          h.script,
		"GIT_ASKPASS":                      h.script,
		"GIT_PROXY_COMMAND":                h.script,
		"GIT_EXTERNAL_DIFF":                h.script,
		"GIT_EDITOR":                       h.script,
		"GIT_TEMPLATE_DIR":                 h.dir,
		"GIT_TRACE":                        trace,
		"GIT_TRACE_SETUP":                  trace,
	}
	run := runPipelineWith(t, pipelineOpts{agent: func(a agentRun) {
		change(t, a.spec.WorkDir)
		for k, v := range env {
			t.Setenv(k, v)
		}
	}})
	for k := range env {
		os.Unsetenv(k)
	}
	released(t, run, "app.js", "changed")
	h.nothingRan(t)
	if raw, err := os.ReadFile(trace); err == nil {
		t.Errorf("git traced where a variable told it to:\n%s", raw)
	}
}

// A hook or a URL rewrite an EARLIER agent left in the .git runs before this build's
// agent starts, when the branch is pulled. The checkout starts from a new .git.
func TestAGitDirAnEarlierBuildLeftIsNotUsed(t *testing.T) {
	h := newHostile(t)
	run := runPipelineWith(t, pipelineOpts{
		before: func(t *testing.T, workDir, remote string) {
			if err := os.MkdirAll(filepath.Dir(workDir), 0o755); err != nil {
				t.Fatal(err)
			}
			// The rewrite would hand this build another project's files as the branch.
			branch := plainGit(t, remote, "for-each-ref", "--format=%(refname:short)", "refs/heads")
			plainGit(t, filepath.Dir(workDir), "clone", "-q", "-b", branch, remote, workDir)
			writing := filepath.Join(h.dir, "writing")
			plainGit(t, h.dir, "init", "-q", "-b", branch, writing)
			if err := os.WriteFile(filepath.Join(writing, "planted.txt"), []byte("planted\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			plainGit(t, writing, "add", "-A")
			plainGit(t, writing, "commit", "-q", "-m", "planted")
			plainGit(t, writing, "push", "-q", h.decoy, branch)
			h.plantGit(t, workDir, remote)
		},
		agent: func(a agentRun) {
			change(t, a.spec.WorkDir)
			if _, err := os.Stat(filepath.Join(a.spec.WorkDir, "planted.txt")); err == nil {
				t.Errorf("the checkout came from the decoy")
			}
		},
	})
	released(t, run, "app.js", "changed")
	if raw, err := os.ReadFile(h.marks); err == nil {
		t.Errorf("something the earlier agent left was run:\n%s", raw)
	}
	if got := plainGit(t, run.remote, "ls-tree", "--name-only", run.head(t)); strings.Contains(got, "planted.txt") {
		t.Errorf("the decoy's file reached the commit:\n%s", got)
	}
	asides, _ := filepath.Glob(run.workDir + ".stale-git-*")
	if len(asides) != 1 {
		t.Errorf("the old .git was not kept aside: %v", asides)
	}
	entries, err := os.ReadDir(filepath.Join(run.workDir, ".git", "hooks"))
	if err == nil && len(entries) > 0 {
		t.Errorf("the new .git holds hooks: %v", entries)
	}
}

// The branch must be where it was when the agent started. An agent can reach the remote
// with CRN's credentials; a push it made is not built on and not overwritten.
func TestABranchThatMovedDuringTheBuildIsRefused(t *testing.T) {
	var agentCommit string
	run := runPipelineWith(t, pipelineOpts{agent: func(a agentRun) {
		dir := a.spec.WorkDir
		change(t, dir)
		branch := plainGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD")
		plainGit(t, dir, "add", "-A")
		plainGit(t, dir, "commit", "-q", "-m", "the agent's own push")
		agentCommit = plainGit(t, dir, "rev-parse", "HEAD")
		plainGit(t, dir, "push", "-q", remoteOf(dir), "HEAD:refs/heads/"+branch)
	}})
	failedAndNeverReleased(t, run)
	if !strings.Contains(failedMessage(run), "moved") {
		t.Errorf("the failure does not say why: %s", failedMessage(run))
	}
	if head := run.head(t); head != agentCommit {
		t.Errorf("the remote is on %s, want it left on %s", head, agentCommit)
	}
	if indexOf(run.events, "docker build") >= 0 {
		t.Errorf("it went on to build:\n%s", strings.Join(run.events, "\n"))
	}
}

// The repository CRN makes for a build does not outlive it.
func TestTheBuildsRepositoryIsRemoved(t *testing.T) {
	temp := t.TempDir()
	run := runPipelineWith(t, pipelineOpts{env: map[string]string{"TMPDIR": temp}, agent: func(a agentRun) { change(t, a.spec.WorkDir) }})
	released(t, run, "app.js", "changed")
	left, _ := filepath.Glob(filepath.Join(temp, "crn-*"))
	if len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}
