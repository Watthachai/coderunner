package buildstep

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CleanRepo is a repository CRN makes for one build, outside the workspace, once the agent
// is done. A listed project's build commits and pushes from here, and reads the result back
// from here. The workspace's .git is not read at all: its hooks, its config, its objects,
// and whatever it points at are out of the picture, and only the workspace's files enter.
//
// The commit holds the files as they are, one commit on top of the branch. Commits the
// agent made itself stay behind in the workspace.
type CleanRepo struct {
	// Dir is the repository, for reading its objects.
	Dir string

	remote, branch, base string
	log                  *slog.Logger
}

// OpenCleanRepo makes the repository. With a base, the branch must still be on that commit:
// a remote that moved since the agent started is not built on. An empty base starts a new
// history, as a new build does.
func OpenCleanRepo(ctx context.Context, remote, branch, base string, logger *slog.Logger) (*CleanRepo, error) {
	if logger == nil {
		logger = slog.Default()
	}
	dir, err := os.MkdirTemp("", "crn-git-")
	if err != nil {
		return nil, err
	}
	r := &CleanRepo{Dir: dir, remote: remote, branch: branch, base: base, log: logger.With("component", "buildstep.git", "branch", branch)}
	if _, err := runGit(ctx, dir, r.log, "init", "-q", "--bare", "--template=", dir); err != nil {
		r.Close()
		return nil, err
	}
	if base == "" {
		return r, nil
	}
	if _, err := r.git(ctx, "fetch", "-q", "--no-tags", remote, "refs/heads/"+branch); err != nil {
		r.Close()
		return nil, err
	}
	head, err := r.git(ctx, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil {
		r.Close()
		return nil, err
	}
	if head != base {
		r.Close()
		return nil, fmt.Errorf("the remote's %s moved during this build: it is on %s, the build started from %s", branch, head, base)
	}
	return r, nil
}

// Commit makes the commit of what workDir holds, leaving out what the project's .gitignore
// and excludeBody leave out, and returns it. With nothing changed it returns the base.
func (r *CleanRepo) Commit(ctx context.Context, workDir, message string) (string, error) {
	writeGitExclude(r.Dir)
	if r.base != "" {
		if _, err := r.git(ctx, "read-tree", r.base); err != nil {
			return "", err
		}
	}
	if _, err := runGit(ctx, workDir, r.log, "--git-dir="+r.Dir, "--work-tree="+workDir, "add", "-A"); err != nil {
		return "", err
	}
	tree, err := r.git(ctx, "write-tree")
	if err != nil {
		return "", err
	}
	args := []string{"-c", "user.name=FITT Code Runner", "-c", "user.email=crn@fitt.local", "commit-tree", tree, "-m", message}
	if r.base != "" {
		baseTree, err := r.git(ctx, "rev-parse", "--verify", r.base+"^{tree}")
		if err != nil {
			return "", err
		}
		if tree == baseTree {
			return r.base, nil
		}
		args = append(args, "-p", r.base)
	}
	commit, err := r.git(ctx, args...)
	if err != nil {
		return "", err
	}
	if _, err := r.git(ctx, "update-ref", "refs/heads/build", commit); err != nil {
		return "", err
	}
	return commit, nil
}

// Push sends the commit to the branch, with --force only when force is set.
func (r *CleanRepo) Push(ctx context.Context, commit string, force bool) error {
	args := []string{"push", r.remote, commit + ":refs/heads/" + r.branch}
	if force {
		args = []string{"push", "--force", r.remote, commit + ":refs/heads/" + r.branch}
	}
	_, err := r.git(ctx, args...)
	return err
}

// Close removes the repository. The commit lives on at the remote.
func (r *CleanRepo) Close() {
	if err := os.RemoveAll(r.Dir); err != nil {
		r.log.Warn("remove the build's repository", "dir", r.Dir, "err", err)
	}
}

func (r *CleanRepo) git(ctx context.Context, args ...string) (string, error) {
	out, err := runGit(ctx, r.Dir, r.log, append([]string{"--git-dir=" + r.Dir}, args...)...)
	return strings.TrimSpace(out), err
}

// ReplaceGitDir gives a workspace a new, empty .git, keeping the old one aside. An edit
// pulls the branch into the workspace before its agent starts, and the .git there is what
// an earlier agent left. resetWorkspace sweeps the aside with the others.
func ReplaceGitDir(ctx context.Context, dir, branch string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	gitDir := filepath.Join(dir, ".git")
	if _, err := os.Lstat(gitDir); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("buildstep: read .git: %w", err)
	}
	aside := dir + ".stale-git-" + time.Now().Format("20060102-150405.000000000")
	if err := os.Rename(gitDir, aside); err != nil {
		return fmt.Errorf("buildstep: move .git aside: %w", err)
	}
	log := logger.With("component", "buildstep.git", "dir", dir, "branch", branch)
	log.Info("the workspace's .git was moved aside", "moved_to", aside)
	_, err := runGit(ctx, dir, log, "init", "-q", "--template=", "-b", branch)
	return err
}
