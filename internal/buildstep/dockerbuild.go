package buildstep

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// dockerbuild.go builds the compiled Next.js application plus the production
// tooling needed for migrations and first-install bootstrap. The template is
// also packaged in the fitt-build skill and exercised by the delivery verifier.
// Runtime database scripts are shipped source; this is not a source-secrecy boundary.

const productionDockerfile = `# Auto-written by FITT Code Runner — locked customer runtime.
FROM node:22-bookworm-slim AS deps
RUN apt-get update && apt-get install -y --no-install-recommends openssl ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts

FROM deps AS builder
COPY . .
RUN mkdir -p public prisma /runtime-config && for f in prisma.config.*; do if [ -f "$f" ]; then cp "$f" /runtime-config/; fi; done
ENV NEXT_TELEMETRY_DISABLED=1
# Build never connects to customer infrastructure; Prisma config may require a URL.
ENV DATABASE_URL=postgresql://build:build@127.0.0.1:1/build
RUN npm run db:generate --if-present
RUN npm run build
# Retain only lockfile-resolved production tools for migrations/bootstrap.
RUN npm prune --omit=dev --ignore-scripts

FROM node:22-bookworm-slim AS runner
RUN apt-get update && apt-get install -y --no-install-recommends openssl ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
ENV NODE_ENV=production
ENV NEXT_TELEMETRY_DISABLED=1
COPY --from=builder --chown=node:node /app/node_modules ./node_modules
COPY --from=builder --chown=node:node /app/.next/standalone ./
COPY --from=builder --chown=node:node /app/.next/static ./.next/static
COPY --from=builder --chown=node:node /app/public ./public
COPY --from=builder --chown=node:node /app/prisma ./prisma
COPY --from=builder --chown=node:node /app/package.json ./package.json
COPY --from=builder --chown=node:node /runtime-config/ ./
RUN printf '#!/bin/sh\nset -eu\nif [ -f prisma/schema.prisma ]; then\n  npm run db:deploy\n  npm run db:seed\nfi\nexec node server.js\n' > /app/docker-entrypoint.sh && chmod +x /app/docker-entrypoint.sh
USER node
EXPOSE 3000
ENV PORT=3000
ENV HOSTNAME=0.0.0.0
CMD ["/app/docker-entrypoint.sh"]
`

const dockerIgnore = `node_modules
.next
.git
.env
.env.*
Dockerfile
.dockerignore
npm-debug.log
`

// customerCompose runs the delivered demo against an EXTERNAL Postgres the operator
// supplies via DATABASE_URL — no bundled database, no separate migrate service. The
// app image applies committed migrations on start. Placeholders rendered per build.
const customerCompose = `# Customer deployment. Supply unique secrets through your deployment environment.
services:
  app:
    image: {{APP_IMAGE}}
    environment:
      DATABASE_URL: "${DATABASE_URL:-}"
      AUTH_SECRET: "${AUTH_SECRET:-}"
      BOOTSTRAP_ADMIN_EMAIL: "${BOOTSTRAP_ADMIN_EMAIL:-}"
      BOOTSTRAP_ADMIN_PASSWORD: "${BOOTSTRAP_ADMIN_PASSWORD:-}"
      FITT_FEEDBACK_URL: "${FITT_FEEDBACK_URL:-}"
    ports:
      - "${APP_PORT:-{{PORT}}}:3000"
    restart: unless-stopped
`

// DemoEnvExample is the runtime env contract for the delivered demo image — an
// example a consumer (FTC DV) can adapt. The operator supplies the real values at
// run time (compose/orchestrator); NONE of these are baked into the image. Mirrors
// what customerCompose injects, so keep the two in step.
type DemoEnvExample struct {
	// DatabaseURL is REQUIRED — the app self-migrates + reads it.
	DatabaseURL string `json:"DATABASE_URL"`
	// Port is the port the app listens on INSIDE the container (fixed).
	Port string `json:"PORT"`
	// AppPort is the suggested HOST port to publish → container 3000 (per-project,
	// avoids collisions); override freely.
	AppPort string `json:"APP_PORT"`
	// Deprecated callback keys retained as empty strings for older consumers.
	// Customer applications use persisted accounts and once-only bootstrap inputs.
	DevEmail    string `json:"DEV_EMAIL"`
	DevPassword string `json:"DEV_PASSWORD"`
	// FeedbackURL is where the in-demo feedback widget POSTs. Read from RUNTIME env
	// (the widget's data-ingest is server-rendered from process.env.FITT_FEEDBACK_URL),
	// so the operator points it at their receiver per deployment without a rebuild.
	// Only relevant when the widget was injected (CRN_FEEDBACK_INGEST_URL set at build).
	FeedbackURL       string `json:"FITT_FEEDBACK_URL"`
	BootstrapEmail    string `json:"BOOTSTRAP_ADMIN_EMAIL"`
	BootstrapPassword string `json:"BOOTSTRAP_ADMIN_PASSWORD"`
	AuthSecret        string `json:"AUTH_SECRET"`
}

// NewDemoEnvExample builds the env contract for a demo whose per-project host port
// is `port` (from ScaffoldPort).
func NewDemoEnvExample(port int) DemoEnvExample {
	return DemoEnvExample{
		DatabaseURL: "postgresql://USER:PASSWORD@HOST:5432/DB?schema=public",
		Port:        "3000",
		AppPort:     strconv.Itoa(port),
		DevEmail:    "",
		DevPassword: "",
		FeedbackURL: "http://FEEDBACK_HOST:PORT/api/ingest/feedback",
	}
}

const installMD = `# INSTALL — Customer application

The image contains compiled application output plus database tooling. Supply your own PostgreSQL connection through DATABASE_URL and run docker-compose.customer.yml. The app applies committed migrations with db:deploy and executes db:seed before serving; a failure stops startup.

## First installation
For a local-account application, supply a unique BOOTSTRAP_ADMIN_EMAIL and BOOTSTRAP_ADMIN_PASSWORD (at least 16 characters), plus a strong AUTH_SECRET through deployment secrets. No shared password ships. The bootstrap transaction creates the first administrator and a durable completion marker. Existing users are adopted without creation. Change the initial password at first login, then remove the bootstrap variables from deployment.

## Restarts and issue builds
Use the SAME customer database and preserve the authentication secret. Migrations retain their history; completed bootstrap ignores absent or changed bootstrap credentials. Accounts, password hashes and business records are not reseeded. A deleted initial account is not recreated. Account recovery uses the application's documented recovery process.

## Upgrade and recovery
Back up the database before migrations. Review BUILD_NOTES.md and CRN_VERIFICATION.json. Deploy image {{APP_IMAGE}} with docker compose -f docker-compose.customer.yml up -d, then verify health and core flows. A schema change may require a forward fix; an older image is not automatically compatible with a migrated database. Keep the previous image and tested recovery instructions. Never reset or db-push a customer database.

Published port defaults to {{PORT}}; override APP_PORT. For private registries authenticate using your deployment secret manager. Do not place passwords on command lines or commit .env files. Required provider configuration and live integrations are listed in BUILD_NOTES.md.
`

// DemoImageTag builds the image tag for a build:
// "<registry>/crn-demo-<slug>-<id8>:v<n>" (registry omitted when empty -> a
// local-only tag). The 8-char project-id suffix keeps two different projects
// that happen to share a demo name from colliding on the same image repo (both
// use per-project build numbers, so v1 would otherwise overwrite v1). name is
// sanitized to the docker repo charset.
func DemoImageTag(registry, name, projectID string, buildNo int) string {
	id8 := projectID
	if len(id8) > 8 {
		id8 = id8[:8]
	}
	repo := "crn-demo-" + dockerSlug(name)
	if id8 != "" {
		repo += "-" + id8
	}
	if registry != "" {
		repo = strings.TrimRight(registry, "/") + "/" + repo
	}
	return fmt.Sprintf("%s:v%d", repo, buildNo)
}

// dockerSlug lowercases name and keeps only the docker repo charset
// ([a-z0-9._-]), collapsing runs of other characters to a single "-". Falls back
// to "demo" if nothing usable remains.
func dockerSlug(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "demo"
	}
	return s
}

// IsNextApp reports whether dir looks like a Next.js app (package.json depends on
// "next"). The docker-image pipeline only handles Next standalone builds; other
// stacks are skipped (logged by the caller).
func IsNextApp(dir string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return false
	}
	_, inDeps := pkg.Dependencies["next"]
	_, inDev := pkg.DevDependencies["next"]
	return inDeps || inDev
}

// WriteImageBundle writes CRN's deterministic on-prem delivery bundle into dir,
// overwriting any model-produced versions:
//   - Dockerfile             (compiled app plus runtime database tooling)
//   - .dockerignore
//   - docker-compose.customer.yml (app only; points at the operator's external Postgres)
//   - INSTALL.md
//
// appImage is the (deterministic) tag the pipeline will build+push; registry + port
// are rendered into the compose/INSTALL. The app image self-migrates on start, so
// there is no separate migrate image. Returns false (no error) when dir is not a
// Next app — nothing to bundle.
func WriteImageBundle(dir, appImage, registry string, port int, logger *slog.Logger) (bool, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if !IsNextApp(dir) {
		return false, nil
	}
	rep := strings.NewReplacer(
		"{{APP_IMAGE}}", appImage,
		"{{REGISTRY}}", registry,
		"{{PORT}}", strconv.Itoa(port),
	)
	for _, f := range []struct{ name, body string }{
		{"Dockerfile", productionDockerfile},
		{".dockerignore", dockerIgnore},
		{"docker-compose.customer.yml", rep.Replace(customerCompose)},
		{"INSTALL.md", rep.Replace(installMD)},
	} {
		if err := os.WriteFile(filepath.Join(dir, f.name), []byte(f.body), 0o644); err != nil {
			return false, fmt.Errorf("buildstep: write %s: %w", f.name, err)
		}
	}
	return true, nil
}

// imagePlatform is the target platform for demo images. Customers run on amd64
// (x86_64) machines while CRN often builds on arm64 Macs, so we pin it — Docker
// Desktop's buildx emulates the cross-build via QEMU. Not runtime-configurable
// yet; change here if an arm64 customer target ever appears.
const imagePlatform = "linux/amd64"

// BuildImage runs `docker build --platform linux/amd64 -f <dockerfile> -t <tag>
// <dir>`, streaming combined output to the logger. Requires a Docker daemon +
// buildx on the host.
func BuildImage(ctx context.Context, dir, dockerfile, tag string, logger *slog.Logger) error {
	// -f is resolved relative to the docker process's CWD, NOT the build context. CRN
	// runs from its own repo root (which has a Go server Dockerfile), so a bare
	// "Dockerfile" would match THAT instead of the workspace's — build the wrong
	// image. Always pass the context-absolute path.
	//
	// --provenance/--sbom off is load-bearing, not tidiness. BuildKit otherwise
	// attaches attestations, which wraps the result in an OCI index instead of a
	// plain manifest — and the index is what races its own child on push, which
	// is how two 30-minute builds died before this landed (see PushImage).
	//
	// Measured against the delivery registry with identical image content: as an
	// index it failed every push; as a single manifest it pushed first try. A
	// customer needs an image it can pull, not a supply-chain record.
	return runDocker(ctx, logger, "build", "--platform", imagePlatform,
		"--provenance=false", "--sbom=false",
		"-f", filepath.Join(dir, dockerfile), "-t", tag, dir)
}

// pushAttempts/pushBackoff bound the retry in PushImage. The race it exists for
// resolves in milliseconds, so a second attempt is nearly always enough; the
// third covers an ordinary network blip on a 200MB+ upload.
const pushAttempts = 3

var pushBackoff = 3 * time.Second

// PushImage runs `docker push tag`, retrying a failure a couple of times. The
// host must already be `docker login`'d to the registry (CRN uses ambient docker
// credentials — like git uses ambient git credentials — rather than handling auth
// itself).
//
// The retry is a net, not the fix — BuildImage is the fix.
//
// Docker 29 pushes an image index and the manifests it references CONCURRENTLY,
// so the registry can validate the index before its child has committed and
// reject the whole push with MANIFEST_BLOB_UNKNOWN. Whether pushing again helps
// depends on how that race landed, and it lands both ways: one build's child
// committed anyway and the retry succeeded, the next build's did not and every
// retry failed identically — the client re-sends the index, never the child it
// is missing. So this cannot be relied on to rescue the race; not producing an
// index in the first place is what does.
//
// It stays because it still earns its place: a genuine multi-platform build has
// an index legitimately, and a 200MB+ upload can fail partway for reasons that
// are simply transient.
func PushImage(ctx context.Context, tag string, logger *slog.Logger) error {
	var err error
	for attempt := 1; attempt <= pushAttempts; attempt++ {
		if err = runDocker(ctx, logger, "push", tag); err == nil {
			return nil
		}
		if attempt == pushAttempts || ctx.Err() != nil {
			break
		}
		logger.Warn("docker push failed; retrying", "tag", tag, "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(pushBackoff):
		}
	}
	return err
}

// SaveImages writes a `docker save` tarball of images to outPath — an air-gap
// bundle a customer can `docker load < images.tar`. Requires a Docker daemon.
func SaveImages(ctx context.Context, outPath string, images []string, logger *slog.Logger) error {
	return runDocker(ctx, logger, append([]string{"save", "-o", outPath}, images...)...)
}

// runDocker execs the host `docker` CLI, capturing combined output so a failure
// surfaces the real error (not just a non-zero exit). The returned error wraps a
// tail of docker's own output, so it propagates to build_events/the FTC DV callback
// — the operator sees the real reason (e.g. `npm ci` lockfile mismatch) instead of
// a bare "exit status 1". The full output is also logged at Error level.
func runDocker(ctx context.Context, logger *slog.Logger, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(out))
		logger.Error("docker command failed", "args", strings.Join(args, " "), "output", trimmed)
		return fmt.Errorf("docker %s: %w\n%s", strings.Join(args, " "), err, tailLines(trimmed, 25))
	}
	logger.Info("docker command ok", "args", strings.Join(args, " "))
	return nil
}

// tailLines returns the last n lines of s (prefixed with an ellipsis when truncated)
// so a wrapped error carries the meaningful end of a build log without dumping the
// whole thing into build_events.
func tailLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return "…\n" + strings.Join(lines[len(lines)-n:], "\n")
}
