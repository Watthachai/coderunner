// Package config loads CRN configuration from the environment. There is
// exactly one entry point — Load — which fails fast on missing required values
// (no silent defaults for anything security- or connectivity-critical).
package config

import (
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/buildsettings"
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

	// ClaudeModel is the model the spawned `claude` runs, passed as --model.
	// It always has a value (defaultClaudeModel when CRN_CLAUDE_MODEL is
	// unset), so a build never silently inherits whatever model the host
	// account happens to default to.
	ClaudeModel string

	// ClaudeEffort is the default passed as --effort (CRN_CLAUDE_EFFORT,
	// default buildsettings.DefaultEffort). Checked at load: a bad value would
	// fail every build, so it stops the server instead. The dashboard can
	// override ClaudeModel and ClaudeEffort; these are the fallback.
	ClaudeEffort string

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

	// FeedbackIssuePollInterval is how often the feedback→issue watcher scans for
	// un-mirrored feedback. Only used when GithubOwner is set.
	FeedbackIssuePollInterval time.Duration

	// FTCDVCallbackURL / FTCDVCallbackToken: when set, CRN POSTs a build-status
	// callback to FTC DV on each lifecycle transition (in addition to the
	// build_events fan-out). Empty URL disables the callback.
	FTCDVCallbackURL   string
	FTCDVCallbackToken string
	// FTCDVCallbackOrgs are the orgs whose builds FTC DV orders: only their
	// builds are reported to FTCDVCallbackURL, so another org's job ids, images
	// and env never reach it. Set as CRN_FTC_DV_CALLBACK_ORGS="<org uuid>,...";
	// empty sends no callback, and a malformed entry stops startup.
	FTCDVCallbackOrgs map[uuid.UUID]bool
	// FTCDVCallbackCA, from the PEM file CRN_FTC_DV_CALLBACK_CA names, is the
	// only CA the callback client trusts (FTC DV's internal CA); the host's own
	// CAs are left as they are. nil = the system's CAs. An unreadable file or
	// one without a certificate stops startup.
	FTCDVCallbackCA *x509.CertPool

	// InternalTokenHashes are the bearer tokens /internal/* accepts, each by its
	// SHA-256 hex (name -> hash), so no token value sits in .env. Set as
	// CRN_INTERNAL_TOKEN_SHA256="fittcore=<sha256>,operator=<sha256>"; empty
	// leaves /internal open, and a malformed entry stops startup.
	InternalTokenHashes map[string]string

	// BuildImage toggles the docker-image pipeline: after a successful build CRN
	// runs `docker build` on the produced Next standalone app and (if
	// ImageRegistry is set) `docker push`. Default false. Requires a Docker
	// daemon on the CRN host and a prior `docker login` to the registry.
	BuildImage bool
	// ImageRegistry is the registry prefix pushed to, e.g.
	// "registry.gitlab.local/fitt". Empty -> build the image locally only (no
	// push). The full tag is "<registry>/crn-demo-<slug>:v<build_no>".
	ImageRegistry string
	// OrgImageRegistries sends an org's images to that org's own registry
	// project instead of ImageRegistry, e.g. Fittcore's UAT pulls from
	// fitt/ftcdv-uat, which it may read anonymously while the shared project stays
	// closed. Set as CRN_ORG_IMAGE_REGISTRIES="<org uuid>=<registry>,...".
	OrgImageRegistries map[uuid.UUID]string
	// MirrorRegistry, when set (with ImageRegistry), receives a digest-identical
	// copy of every pushed image except those of an org in OrgImageRegistries, e.g. "asia-southeast1-docker.pkg.dev/<project>/<repo>"
	// for the GCP deploy path. The host must be logged in to it (gcloud
	// auth configure-docker). Empty -> no mirror.
	MirrorRegistry string
	// ArtifactDir, when set, is where each build's air-gap bundle is written:
	// <ArtifactDir>/<repo>-v<n>/ with images.tar (docker save of app+migrate),
	// docker-compose.customer.yml, and INSTALL.md — for customers who cannot pull
	// from the registry. Empty -> no tarball (registry delivery only).
	ArtifactDir string

	// MaxConcurrentBuilds caps how many builds run at once, server-wide. Builds
	// of different projects run in parallel (they share no working dir), so this
	// is the resource brake, not a correctness rule: each build ends in a docker
	// build cross-compiled to linux/amd64, which is CPU-heavy and emulated on an
	// ARM host. Raise it to drain a queue faster; lower it if the box thrashes
	// during the docker phase. Default 5, minimum 1.
	MaxConcurrentBuilds int

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
	// TerminalEnabled turns on the per-project terminal WebSocket, a shell on this
	// host with CRN's environment and no auth. Off unless CRN_TERMINAL_ENABLED is
	// true: anyone who can reach the port could otherwise open it.
	TerminalEnabled bool
}

// defaultClaudeModel is the model builds use when CRN_CLAUDE_MODEL is unset:
// the latest Opus. Pinned by full id rather than the CLI's "opus" alias so the
// boot log and the build trace name the exact model that produced a demo.
// Override with a full id ("claude-sonnet-5" — cheaper/faster) or an alias that
// follows the newest of a family ("opus", "sonnet").
const defaultClaudeModel = "claude-opus-5"

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
//	CRN_TERMINAL_ENABLED   (false — "true"/"1" serves the no-auth terminal WebSocket)
//	CRN_CLAUDE_MODEL       (defaultClaudeModel — the latest Opus)
//	CRN_CLAUDE_EFFORT      (buildsettings.DefaultEffort — "xhigh"; must be low|medium|high|xhigh|max)
func Load() (*Config, error) {
	cfg := &Config{
		ListenAddr:         getEnv("CRN_LISTEN_ADDR", ":8080"),
		DatabaseURL:        os.Getenv("CRN_DATABASE_URL"),
		CentralDatabaseURL: os.Getenv("CRN_CENTRAL_DATABASE_URL"),
		MongoURL:           getEnv("CRN_MONGO_URL", "mongodb://localhost:27017"),
		ClaudeBinPath:      os.Getenv("CRN_CLAUDE_BIN"),
		ClaudeModel:        getEnv("CRN_CLAUDE_MODEL", defaultClaudeModel),
		ClaudeEffort:       getEnv("CRN_CLAUDE_EFFORT", buildsettings.DefaultEffort),
		DockerRegistryUser: os.Getenv("CRN_DOCKER_USER"),
		ProjectsDir:        getEnv("CRN_PROJECTS_DIR", "/projects"),
		GitRemote:          os.Getenv("CRN_GIT_REMOTE"),
		GithubOwner:        os.Getenv("CRN_GITHUB_OWNER"),
		RepoPrivate:        getEnvBool("CRN_REPO_PRIVATE", true),
		RunClaude:          getEnvBool("CRN_RUN_CLAUDE", true),
		FeedbackIngestURL:  getEnv("CRN_FEEDBACK_INGEST_URL", "http://localhost:3010/feedback_requests"),
		FTCDVCallbackURL:   os.Getenv("CRN_FTC_DV_CALLBACK_URL"),
		FTCDVCallbackToken: os.Getenv("CRN_FTC_DV_CALLBACK_TOKEN"),
		BuildImage:         getEnvBool("CRN_BUILD_IMAGE", false),
		ImageRegistry:      strings.TrimRight(os.Getenv("CRN_IMAGE_REGISTRY"), "/"),
		MirrorRegistry:     strings.TrimRight(os.Getenv("CRN_MIRROR_REGISTRY"), "/"),
		ArtifactDir:        os.Getenv("CRN_ARTIFACT_DIR"),
		LogLevel:           getEnv("CRN_LOG_LEVEL", "info"),
		Environment:        getEnv("CRN_ENV", "development"),
		TerminalShell:      os.Getenv("CRN_TERMINAL_SHELL"),
		TerminalEnabled:    getEnvBool("CRN_TERMINAL_ENABLED", false),
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

	pollInterval, err := getEnvDuration("CRN_FEEDBACK_ISSUE_POLL_INTERVAL", 20*time.Second)
	if err != nil {
		return nil, err
	}
	cfg.FeedbackIssuePollInterval = pollInterval

	maxBuilds, err := getEnvInt("CRN_MAX_CONCURRENT_BUILDS", 5)
	if err != nil {
		return nil, err
	}
	if maxBuilds < 1 {
		return nil, fmt.Errorf("config: CRN_MAX_CONCURRENT_BUILDS must be >= 1, got %d", maxBuilds)
	}
	cfg.MaxConcurrentBuilds = maxBuilds

	orgRegistries, err := parseOrgRegistries(os.Getenv("CRN_ORG_IMAGE_REGISTRIES"))
	if err != nil {
		return nil, err
	}
	cfg.OrgImageRegistries = orgRegistries

	callbackOrgs, err := parseOrgList("CRN_FTC_DV_CALLBACK_ORGS", os.Getenv("CRN_FTC_DV_CALLBACK_ORGS"))
	if err != nil {
		return nil, err
	}
	cfg.FTCDVCallbackOrgs = callbackOrgs

	tokenHashes, err := parseTokenHashes(os.Getenv("CRN_INTERNAL_TOKEN_SHA256"))
	if err != nil {
		return nil, err
	}
	cfg.InternalTokenHashes = tokenHashes

	if path := os.Getenv("CRN_FTC_DV_CALLBACK_CA"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("config: CRN_FTC_DV_CALLBACK_CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(raw) {
			return nil, fmt.Errorf("config: CRN_FTC_DV_CALLBACK_CA %s holds no PEM certificate", path)
		}
		cfg.FTCDVCallbackCA = pool
	}

	if !buildsettings.ValidEffort(cfg.ClaudeEffort) {
		return nil, fmt.Errorf("config: CRN_CLAUDE_EFFORT must be one of %s, got %q",
			strings.Join(buildsettings.Efforts, ", "), cfg.ClaudeEffort)
	}

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

// parseOrgRegistries reads "<org uuid>=<registry>" pairs separated by commas. A
// malformed pair is an error: it would send that org's images to the shared
// registry without a word.
func parseOrgRegistries(raw string) (map[uuid.UUID]string, error) {
	out := map[uuid.UUID]string{}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		org, registry, ok := strings.Cut(pair, "=")
		id, err := uuid.Parse(strings.TrimSpace(org))
		registry = strings.TrimRight(strings.TrimSpace(registry), "/")
		if !ok || err != nil || registry == "" {
			return nil, fmt.Errorf("config: CRN_ORG_IMAGE_REGISTRIES entry %q is not <org uuid>=<registry>", pair)
		}
		out[id] = registry
	}
	return out, nil
}

// parseTokenHashes reads CRN_INTERNAL_TOKEN_SHA256: comma-separated
// <name>=<sha256 hex> entries.
func parseTokenHashes(raw string) (map[string]string, error) {
	out := map[string]string{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, hash, ok := strings.Cut(entry, "=")
		name, hash = strings.TrimSpace(name), strings.ToLower(strings.TrimSpace(hash))
		if _, err := hex.DecodeString(hash); !ok || name == "" || len(hash) != 64 || err != nil {
			return nil, fmt.Errorf("config: CRN_INTERNAL_TOKEN_SHA256 entry %q is not <name>=<sha256 hex>", entry)
		}
		out[name] = hash
	}
	return out, nil
}

// parseOrgList reads a comma-separated list of org uuids from the env var key.
func parseOrgList(key, raw string) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		id, err := uuid.Parse(entry)
		if err != nil {
			return nil, fmt.Errorf("config: %s entry %q is not an org uuid", key, entry)
		}
		out[id] = true
	}
	return out, nil
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

// getEnvInt parses a numeric tunable, falling back to def when unset.
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
