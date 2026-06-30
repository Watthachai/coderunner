// Package buildstep holds the side-effecting, std-lib-only steps a build runs
// after the job manager has decided what to do: materialize the incoming files
// onto disk and git-commit/force-push the result to a remote.
//
// It is deliberately dependency-free (standard library only) so it can be unit
// tested without the domain/store wiring and reused by any caller. Every
// blocking helper takes a context.Context and a *slog.Logger; git stdout/stderr
// is streamed to the logger so a build's git output is visible in the logs.
package buildstep

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// FileEntry is one file to materialize: a path relative to the project working
// directory plus its full text content.
type FileEntry struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// WriteFiles materializes files under dir, creating parent directories as
// needed. Each file's Path is treated as relative to dir; any path that escapes
// dir (absolute, or containing a ".." segment that climbs above dir) is rejected
// so a malicious payload cannot write outside the project workspace.
func WriteFiles(dir string, files []FileEntry) error {
	absRoot, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("buildstep: resolve dir: %w", err)
	}
	for _, f := range files {
		if f.Path == "" {
			return fmt.Errorf("buildstep: empty file path")
		}
		// Clean the relative path and reject any escape via "..".
		clean := filepath.Clean(f.Path)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("buildstep: file path %q escapes project dir", f.Path)
		}

		target := filepath.Join(absRoot, clean)
		// Defense in depth: ensure the joined target is still within absRoot.
		rel, err := filepath.Rel(absRoot, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("buildstep: file path %q escapes project dir", f.Path)
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("buildstep: mkdir for %q: %w", f.Path, err)
		}
		if err := os.WriteFile(target, []byte(f.Content), 0o644); err != nil {
			return fmt.Errorf("buildstep: write %q: %w", f.Path, err)
		}
	}
	return nil
}

// GitCommitAndPush stages everything in dir, commits it, and force-pushes HEAD
// to <remote> as refs/heads/<branch>. It initializes the repo (git init +
// local user identity) and (re)points origin at remote on first use. The host's
// ambient git credentials are used as-is; no tokens are injected. It returns the
// short SHA of the pushed HEAD.
//
// A no-op commit (nothing staged) is tolerated: the push still publishes the
// current HEAD so the remote branch is created/updated either way.
func GitCommitAndPush(ctx context.Context, dir, remote, branch, message string, logger *slog.Logger) (string, error) {
	if logger == nil {
		logger = slog.Default()
	}
	log := logger.With("component", "buildstep.git", "dir", dir, "branch", branch)

	gitDir := filepath.Join(dir, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		if _, err := runGit(ctx, dir, log, "init"); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", fmt.Errorf("buildstep: stat .git: %w", err)
	}

	// Local identity so commits never depend on a global git config being set.
	if _, err := runGit(ctx, dir, log, "config", "user.email", "crn@fitt.local"); err != nil {
		return "", err
	}
	if _, err := runGit(ctx, dir, log, "config", "user.name", "FITT Code Runner"); err != nil {
		return "", err
	}

	// (Re)point origin at the desired remote. remote-set-url fails if origin does
	// not exist yet, so fall back to remote add.
	if _, err := runGit(ctx, dir, log, "remote", "set-url", "origin", remote); err != nil {
		if _, addErr := runGit(ctx, dir, log, "remote", "add", "origin", remote); addErr != nil {
			return "", addErr
		}
	}

	if _, err := runGit(ctx, dir, log, "add", "-A"); err != nil {
		return "", err
	}

	// Commit. A "nothing to commit" exit is non-fatal — we still push HEAD.
	if _, err := runGit(ctx, dir, log, "-c", "commit.gpgsign=false", "commit", "-m", message); err != nil {
		log.Info("git commit produced no new commit (continuing to push HEAD)", "detail", err.Error())
	}

	refspec := "HEAD:refs/heads/" + branch
	if _, err := runGit(ctx, dir, log, "push", "--force", "origin", refspec); err != nil {
		return "", err
	}

	sha, err := runGit(ctx, dir, log, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sha), nil
}

// runGit runs one git subcommand in dir, streaming combined stdout/stderr to the
// logger, and returns stdout. A non-zero exit yields an error that includes the
// captured output so callers can log/inspect it.
func runGit(ctx context.Context, dir string, log *slog.Logger, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	out := strings.TrimRight(stdout.String(), "\n")
	errOut := strings.TrimRight(stderr.String(), "\n")
	log.Info("git", "args", strings.Join(args, " "), "stdout", out, "stderr", errOut)
	if err != nil {
		return out, fmt.Errorf("buildstep: git %s: %w: %s", strings.Join(args, " "), err, errOut)
	}
	return out, nil
}
