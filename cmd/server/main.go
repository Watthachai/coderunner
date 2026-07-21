// Command server is the CRN entrypoint. It loads configuration, constructs the
// structured logger, opens the store, wires the job manager + Claude runner +
// notifier, builds the chi HTTP/WebSocket router via the api package, and runs
// until SIGINT/SIGTERM, then shuts down gracefully.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/Watthachai/fitt-coderunner/internal/api"
	"github.com/Watthachai/fitt-coderunner/internal/buildinfo"
	"github.com/Watthachai/fitt-coderunner/internal/claude"
	"github.com/Watthachai/fitt-coderunner/internal/config"
	"github.com/Watthachai/fitt-coderunner/internal/domain"
	"github.com/Watthachai/fitt-coderunner/internal/feedback"
	"github.com/Watthachai/fitt-coderunner/internal/jobs"
	"github.com/Watthachai/fitt-coderunner/internal/store"
)

func main() {
	if err := run(); err != nil {
		// Logger may not exist yet; use a last-resort handler.
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg)
	slog.SetDefault(logger)
	modelLabel := cfg.ClaudeModel
	if modelLabel == "" {
		modelLabel = "(cli default)"
	}
	build := buildinfo.Read()
	logger.Info("starting CRN", "env", cfg.Environment, "addr", cfg.ListenAddr,
		"run_claude", cfg.RunClaude, "claude_model", modelLabel,
		"git_remote", cfg.GitRemote, "github_owner", cfg.GithubOwner,
		"revision", build.Revision, "built", build.Time, "modified", build.Modified)

	// Ensure the per-project working-dir root exists so file materialization and
	// git pushes can write into it.
	if err := os.MkdirAll(cfg.ProjectsDir, 0o755); err != nil {
		return err
	}

	// rootCtx is cancelled on the first SIGINT/SIGTERM and used for shutdown.
	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// --- Store (Postgres via pgx) ---
	// TODO(store): store.New must open the pgx pool, ping, and return domain.Store.
	st, err := store.New(rootCtx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	// (Re)seed the built-in `fitt-build` harness skill. The code is the source of
	// truth: EnsureBuiltinSkill re-applies this body/description/files on every
	// restart (ON CONFLICT DO UPDATE) so the canonical harness is always current,
	// while PRESERVING the operator's enabled flag (enable/disable stays an
	// operator decision). fitt-build ships SKILL.md plus reference guides and
	// Docker templates under references/ and assets/ (builtinSkillFiles).
	if err := st.EnsureBuiltinSkill(rootCtx, &domain.Skill{
		Name:        builtinSkillName,
		Description: builtinSkillDescription,
		Body:        builtinSkillBody,
		Files:       builtinSkillFiles,
		Enabled:     true,
		IsBuiltin:   true,
	}); err != nil {
		return err
	}

	// --- Notifier (writes build_events to the central DB) ---
	// TODO(store): store.NewNotifier targets the central DSN for fan-out.
	notifier, err := store.NewNotifier(rootCtx, cfg.CentralDatabaseURL)
	if err != nil {
		return err
	}

	// --- Claude runner (spawns `claude --output-format stream-json`) ---
	// TODO(claude): claude.NewRunner wires the binary path + projects dir.
	runner := claude.NewRunner(cfg.ClaudeBinPath, cfg.ProjectsDir, cfg.ClaudeModel, logger)

	// --- Job manager (queue + lifecycle + per-org advisory lock) ---
	// jobs.NewManager composes store + runner + notifier and the build-step
	// config (projects dir, git remote, run-Claude toggle).
	jobManager := jobs.NewManager(st, runner, notifier, logger, cfg.ProjectsDir, cfg.GitRemote, cfg.GithubOwner, cfg.RepoPrivate, cfg.RunClaude, cfg.FeedbackIngestURL, cfg.FTCDVCallbackURL, cfg.FTCDVCallbackToken, cfg.BuildImage, cfg.ImageRegistry, cfg.ArtifactDir)

	// Reconcile ghost builds: any job still 'building' at boot was orphaned by a
	// prior process (restart/crash mid-build). Fail them now so the dashboard
	// doesn't show a build stuck "building" forever, and subscribers get a
	// terminal event. Runs before ListenAndServe so the state is clean on first
	// request.
	jobManager.ReconcileOrphans(rootCtx)

	// Resume any queue stranded by the restart: a job queued while a prior build
	// held the org has no Enqueue/trigger to chain to it once that process is
	// gone, so it would sit 'queued' forever. Kick the worker per org now (after
	// the orphan reconcile freed the per-org "1 building" slot).
	jobManager.ResumeQueued(rootCtx)

	// --- Feedback→GitHub watcher (owner model only) ---
	// Mirrors every in-demo feedback row into a GitHub issue so the panel's
	// history is visible in the repo. No-op when CRN_GITHUB_OWNER is unset.
	if cfg.GithubOwner != "" {
		watcher := feedback.NewWatcher(st, feedback.NewGHIssuer(logger),
			cfg.GithubOwner, cfg.FeedbackIssuePollInterval, logger)
		go watcher.Run(rootCtx)
		logger.Info("feedback→issue watcher started", "interval", cfg.FeedbackIssuePollInterval)
	}

	// --- HTTP / WebSocket server (chi router) ---
	// api.NewServer registers all routes and returns an http.Handler. The git
	// remote is echoed back to FBD in the ingest response.
	handler := api.NewServer(logger, st, jobManager, cfg.GitRemote, cfg.GithubOwner, cfg.ProjectsDir, cfg.TerminalShell, cfg.ClaudeBinPath, cfg.ClaudeModel)

	srv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: handler,
	}

	// Run the server; report ListenAndServe errors through a channel so we can
	// select against shutdown.
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("http listening", "addr", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return err
	case <-rootCtx.Done():
		logger.Info("shutdown signal received, draining")
	}

	// Graceful shutdown bounded by the configured timeout.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
		return err
	}
	logger.Info("shutdown complete")
	return nil
}

// newLogger builds the slog logger: JSON in production, human-readable text in
// development, at the configured level.
func newLogger(cfg *config.Config) *slog.Logger {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if cfg.Environment == "production" {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(h)
}
