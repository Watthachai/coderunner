// Package config loads CRN configuration from the environment. There is
// exactly one entry point — Load — which fails fast on missing required values
// (no silent defaults for anything security- or connectivity-critical).
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the fully-resolved runtime configuration.
type Config struct {
	// ListenAddr is the host:port the CRN HTTP/WS server binds to.
	ListenAddr string

	// DatabaseURL is the pgx DSN for the CRN-local Postgres (jobs, projects).
	DatabaseURL string

	// CentralDatabaseURL is the pgx DSN for the shared "DB กลาง" where
	// build_events are written for fan-out. During standalone dev this may
	// equal DatabaseURL (CRN-architecture.md §8 Phase 9).
	CentralDatabaseURL string

	// MongoURL is the document store for BRD/PRD content (CRN-architecture.md §2.2).
	MongoURL string

	// ClaudeBinPath is the absolute path to the `claude` CLI used to spawn runs.
	ClaudeBinPath string

	// ClaudeModel optionally pins the model the spawned `claude` uses via
	// --model (e.g. "sonnet" for the latest Sonnet, or a full model id). Empty
	// uses the CLI's default model.
	ClaudeModel string

	// DockerRegistryUser is the Docker Hub user image tags are pushed under:
	// {DockerRegistryUser}/{project_id}:v{build_no}.
	DockerRegistryUser string

	// ProjectsDir is the root holding per-project working dirs (.git + src).
	ProjectsDir string

	// GitRemote is the HTTPS remote the per-build branch is force-pushed to
	// (https://github.com/<owner>/<repo>.git). Optional: when empty the
	// git-push step is skipped (logged notice) and builds still complete.
	GitRemote string

	// GithubOwner opts the whole service into the "one private GitHub repo per
	// project" model. When set, each project gets its OWN repo named
	// "crn-<slug>-<id8>" under this owner and builds/edits push to that repo's
	// "main" (via the `gh` CLI). When EMPTY the legacy shared-remote behavior is
	// used unchanged: a "crn/<slug>-<id8>" branch is pushed to CRN_GIT_REMOTE.
	GithubOwner string

	// RepoPrivate controls whether the per-project GitHub repos created when
	// GithubOwner is set are private (default) or public. Ignored when
	// GithubOwner is empty.
	RepoPrivate bool

	// RunClaude toggles whether the build invokes Claude Code after the files
	// are materialized. Default true: a real Claude session runs the fitt-build
	// harness skill. Set CRN_RUN_CLAUDE=false to materialize + push only.
	RunClaude bool

	// FeedbackIngestURL is the PostgREST endpoint baked into the in-demo feedback
	// widget injected into every build (where the widget POSTs feedback). Defaults
	// to the local PostgREST; set a public URL for deployed demos. Empty disables
	// the widget injection.
	FeedbackIngestURL string

	// LogLevel controls slog verbosity: "debug" | "info" | "warn" | "error".
	LogLevel string

	// ShutdownTimeout bounds graceful shutdown before forced close.
	ShutdownTimeout time.Duration

	// Environment is "development" | "production"; controls log handler choice.
	Environment string

	// TerminalShell optionally overrides the OS shell spawned for the per-project
	// interactive terminal WebSocket. Empty means fall back to $SHELL, then
	// /bin/zsh, then /bin/bash, then /bin/sh (the first that exists at runtime).
	// The fallback logic lives in the terminal handler; this field only carries
	// the operator's explicit override.
	TerminalShell string
}

// Load reads configuration from the environment.
//
// Required (Load errors if unset/empty):
//
//	CRN_DATABASE_URL, CRN_CLAUDE_BIN, CRN_DOCKER_USER
//
// Optional (defaulted):
//
//	CRN_LISTEN_ADDR        (":8080")
//	CRN_CENTRAL_DATABASE_URL (falls back to CRN_DATABASE_URL)
//	CRN_MONGO_URL          ("mongodb://localhost:27017")
//	CRN_PROJECTS_DIR       ("/projects")
//	CRN_GIT_REMOTE         ("" — when empty the git-push step is skipped)
//	CRN_GITHUB_OWNER       ("" — when empty the shared-remote/branch model is used)
//	CRN_REPO_PRIVATE       (true — per-project repos are private; "false"/"0" public)
//	CRN_RUN_CLAUDE         (true — set "false"/"0" to materialize + push only)
//	CRN_LOG_LEVEL          ("info")
//	CRN_SHUTDOWN_TIMEOUT   ("15s")
//	CRN_ENV                ("development")
//	CRN_TERMINAL_SHELL     ("" — falls back to $SHELL, then /bin/zsh/bash/sh)
func Load() (*Config, error) {
	cfg := &Config{
		ListenAddr:         getEnv("CRN_LISTEN_ADDR", ":8080"),
		DatabaseURL:        os.Getenv("CRN_DATABASE_URL"),
		CentralDatabaseURL: os.Getenv("CRN_CENTRAL_DATABASE_URL"),
		MongoURL:           getEnv("CRN_MONGO_URL", "mongodb://localhost:27017"),
		ClaudeBinPath:      os.Getenv("CRN_CLAUDE_BIN"),
		ClaudeModel:        os.Getenv("CRN_CLAUDE_MODEL"),
		DockerRegistryUser: os.Getenv("CRN_DOCKER_USER"),
		ProjectsDir:        getEnv("CRN_PROJECTS_DIR", "/projects"),
		GitRemote:          os.Getenv("CRN_GIT_REMOTE"),
		GithubOwner:        os.Getenv("CRN_GITHUB_OWNER"),
		RepoPrivate:        getEnvBool("CRN_REPO_PRIVATE", true),
		RunClaude:          getEnvBool("CRN_RUN_CLAUDE", true),
		FeedbackIngestURL:  getEnv("CRN_FEEDBACK_INGEST_URL", "http://localhost:3010/feedback_requests"),
		LogLevel:           getEnv("CRN_LOG_LEVEL", "info"),
		Environment:        getEnv("CRN_ENV", "development"),
		TerminalShell:      os.Getenv("CRN_TERMINAL_SHELL"),
	}

	if cfg.CentralDatabaseURL == "" {
		// Standalone dev: central DB == local DB until the shared VM exists.
		cfg.CentralDatabaseURL = cfg.DatabaseURL
	}

	timeout, err := getEnvDuration("CRN_SHUTDOWN_TIMEOUT", 15*time.Second)
	if err != nil {
		return nil, err
	}
	cfg.ShutdownTimeout = timeout

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	missing := func(name, val string) error {
		if val == "" {
			return fmt.Errorf("config: required env %s is not set", name)
		}
		return nil
	}
	for _, e := range []error{
		missing("CRN_DATABASE_URL", c.DatabaseURL),
		missing("CRN_CLAUDE_BIN", c.ClaudeBinPath),
		missing("CRN_DOCKER_USER", c.DockerRegistryUser),
	} {
		if e != nil {
			return e
		}
	}
	return nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvDuration(key string, def time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be a Go duration (e.g. 15s): %w", key, err)
	}
	return d, nil
}

// getEnvInt is provided for implementers who add numeric tunables (kept here so
// the helper convention is consistent). Currently unused by Load.
func getEnvInt(key string, def int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be an integer: %w", key, err)
	}
	return n, nil
}

var _ = getEnvInt // silence unused until an implementer needs it

// getEnvBool parses a boolean tunable. "true" / "1" (case-insensitive) are true;
// everything else — including an unset or unparseable value — yields def.
func getEnvBool(key string, def bool) bool {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1":
		return true
	case "false", "0":
		return false
	default:
		return def
	}
}
