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

	// DockerRegistryUser is the Docker Hub user image tags are pushed under:
	// {DockerRegistryUser}/{project_id}:v{build_no}.
	DockerRegistryUser string

	// ProjectsDir is the root holding per-project working dirs (.git + src).
	ProjectsDir string

	// GitRemote is the HTTPS remote the per-build branch is force-pushed to
	// (https://github.com/<owner>/<repo>.git). Optional: when empty the
	// git-push step is skipped (logged notice) and builds still complete.
	GitRemote string

	// RunClaude toggles whether the build invokes Claude Code after the files
	// are materialized. Default false: materialize + push only.
	RunClaude bool

	// LogLevel controls slog verbosity: "debug" | "info" | "warn" | "error".
	LogLevel string

	// ShutdownTimeout bounds graceful shutdown before forced close.
	ShutdownTimeout time.Duration

	// Environment is "development" | "production"; controls log handler choice.
	Environment string
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
//	CRN_RUN_CLAUDE         (false — "true"/"1" to run Claude Code post-materialize)
//	CRN_LOG_LEVEL          ("info")
//	CRN_SHUTDOWN_TIMEOUT   ("15s")
//	CRN_ENV                ("development")
func Load() (*Config, error) {
	cfg := &Config{
		ListenAddr:         getEnv("CRN_LISTEN_ADDR", ":8080"),
		DatabaseURL:        os.Getenv("CRN_DATABASE_URL"),
		CentralDatabaseURL: os.Getenv("CRN_CENTRAL_DATABASE_URL"),
		MongoURL:           getEnv("CRN_MONGO_URL", "mongodb://localhost:27017"),
		ClaudeBinPath:      os.Getenv("CRN_CLAUDE_BIN"),
		DockerRegistryUser: os.Getenv("CRN_DOCKER_USER"),
		ProjectsDir:        getEnv("CRN_PROJECTS_DIR", "/projects"),
		GitRemote:          os.Getenv("CRN_GIT_REMOTE"),
		RunClaude:          getEnvBool("CRN_RUN_CLAUDE", false),
		LogLevel:           getEnv("CRN_LOG_LEVEL", "info"),
		Environment:        getEnv("CRN_ENV", "development"),
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
