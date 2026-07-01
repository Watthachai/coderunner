// Package github is a thin wrapper over the authenticated `gh` CLI on the host.
// It supports the "one private GitHub repo per project" model: creating a
// per-project repo, listing its open issues, and commenting on an issue when a
// build driven by that issue finishes.
//
// It shells out to `gh` (std-lib os/exec only, no SDK) and relies on the host's
// ambient `gh` authentication (a token with `repo` scope). The caller decides
// whether to use this package at all — it is only exercised when
// CRN_GITHUB_OWNER is set (see internal/jobs + internal/api).
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
)

// Issue is one open GitHub issue of a project's repo. The json tags match the
// `gh issue list --json number,title,body,url` output so it can be unmarshaled
// directly, and they are also the on-the-wire shape the CRN issues API returns.
type Issue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	URL    string `json:"url"`
}

// EnsureRepo makes sure a private (or public) repo <owner>/<name> exists,
// creating it via `gh repo create` when absent. An "already exists" result is
// treated as success (idempotent). It returns the https clone URL
// "https://github.com/<owner>/<name>.git".
func EnsureRepo(ctx context.Context, owner, name string, private bool, logger *slog.Logger) (string, error) {
	if logger == nil {
		logger = slog.Default()
	}
	log := logger.With("component", "github", "owner", owner, "repo", name)

	repoURL := "https://github.com/" + owner + "/" + name + ".git"

	visibility := "--public"
	if private {
		visibility = "--private"
	}
	out, err := runGH(ctx, log, "repo", "create", owner+"/"+name, visibility)
	if err != nil {
		// A repo that already exists is not an error for our idempotent create.
		if strings.Contains(strings.ToLower(out+err.Error()), "already exists") {
			log.Info("github repo already exists", "url", repoURL)
			return repoURL, nil
		}
		return "", fmt.Errorf("github: create repo %s/%s: %w", owner, name, err)
	}
	log.Info("github repo created", "url", repoURL, "private", private)
	return repoURL, nil
}

// ListIssues returns the open issues of repoSlug ("owner/name") via
// `gh issue list --json number,title,body,url`. An empty result yields a nil
// slice and no error.
func ListIssues(ctx context.Context, repoSlug string, logger *slog.Logger) ([]Issue, error) {
	if logger == nil {
		logger = slog.Default()
	}
	log := logger.With("component", "github", "repo", repoSlug)

	out, err := runGH(ctx, log, "issue", "list",
		"--repo", repoSlug, "--state", "open",
		"--json", "number,title,body,url")
	if err != nil {
		return nil, fmt.Errorf("github: list issues %s: %w", repoSlug, err)
	}
	var issues []Issue
	if err := json.Unmarshal([]byte(out), &issues); err != nil {
		return nil, fmt.Errorf("github: parse issues %s: %w", repoSlug, err)
	}
	return issues, nil
}

// CommentIssue posts body as a comment on issue #number of repoSlug
// ("owner/name") via `gh issue comment`. It is best-effort: any failure is
// logged and returned, but callers treat it as non-fatal to the build.
func CommentIssue(ctx context.Context, repoSlug string, number int, body string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	log := logger.With("component", "github", "repo", repoSlug, "issue", number)

	if _, err := runGH(ctx, log, "issue", "comment", strconv.Itoa(number),
		"--repo", repoSlug, "--body", body); err != nil {
		log.Warn("github comment issue failed", "err", err)
		return fmt.Errorf("github: comment issue %s#%d: %w", repoSlug, number, err)
	}
	log.Info("github issue commented")
	return nil
}

// runGH runs one `gh` subcommand, returning its trimmed stdout. A non-zero exit
// yields an error that includes the captured stderr so callers can inspect it
// (e.g. the "already exists" check in EnsureRepo).
func runGH(ctx context.Context, log *slog.Logger, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	out := strings.TrimSpace(stdout.String())
	errOut := strings.TrimSpace(stderr.String())
	log.Info("gh", "args", strings.Join(args, " "), "stderr", errOut)
	if err != nil {
		return out, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, errOut)
	}
	return out, nil
}
