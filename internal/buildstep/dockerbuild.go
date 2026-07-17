package buildstep

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// dockerbuild.go turns a built Next.js (App Router, standalone) demo into an
// opaque production Docker image and pushes it to a registry. Only the compiled
// standalone output ships in the image — no .tsx/source — so a customer can run
// the demo on their own LAN without seeing our code. CRN writes its own known-
// good Dockerfile (deterministic, mirrors the fitt-build harness asset) rather
// than trusting whatever the model produced, then execs the host `docker`.

// productionDockerfile is the multi-stage build for a Next.js app using
// output:"standalone". The runner stage copies ONLY .next/standalone + static +
// public, so source files never land in the image. Kept in sync with the
// fitt-build skill's assets/Dockerfile.
const productionDockerfile = `# Auto-written by FITT Code Runner — production image for this demo.
# Multi-stage build for a Next.js (App Router) app using output: "standalone".
# The runner copies ONLY the compiled standalone output — no source ships.
FROM node:20-alpine AS deps
RUN apk add --no-cache libc6-compat
WORKDIR /app
COPY package.json package-lock.json* ./
RUN npm ci --ignore-scripts

FROM node:20-alpine AS builder
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
# prisma generate needs no database; safe no-op if the project has no Prisma.
RUN npx prisma generate 2>/dev/null || true
ENV NEXT_TELEMETRY_DISABLED=1
RUN npm run build

FROM node:20-alpine AS runner
WORKDIR /app
ENV NODE_ENV=production
ENV NEXT_TELEMETRY_DISABLED=1
RUN addgroup --system --gid 1001 nodejs \
  && adduser --system --uid 1001 nextjs
COPY --from=builder /app/public ./public
COPY --from=builder --chown=nextjs:nodejs /app/.next/standalone ./
COPY --from=builder --chown=nextjs:nodejs /app/.next/static ./.next/static
USER nextjs
EXPOSE 3000
ENV PORT=3000
ENV HOSTNAME=0.0.0.0
CMD ["node", "server.js"]
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

// DemoImageTag builds the image tag for a build: "<registry>/crn-demo-<slug>:v<n>"
// (registry omitted when empty -> a local-only tag). name is sanitized to the
// docker repo charset.
func DemoImageTag(registry, name string, buildNo int) string {
	repo := "crn-demo-" + dockerSlug(name)
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

// WriteDockerfile writes CRN's deterministic production Dockerfile + .dockerignore
// into dir, overwriting any model-produced versions (which have been unreliable).
// Returns false (no error) when dir is not a Next app — nothing to build.
func WriteDockerfile(dir string, logger *slog.Logger) (bool, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if !IsNextApp(dir) {
		return false, nil
	}
	for _, f := range []struct{ name, body string }{
		{"Dockerfile", productionDockerfile},
		{".dockerignore", dockerIgnore},
	} {
		if err := os.WriteFile(filepath.Join(dir, f.name), []byte(f.body), 0o644); err != nil {
			return false, fmt.Errorf("buildstep: write %s: %w", f.name, err)
		}
	}
	return true, nil
}

// BuildImage runs `docker build -t tag dir`, streaming combined output to the
// logger. Requires a Docker daemon on the host.
func BuildImage(ctx context.Context, dir, tag string, logger *slog.Logger) error {
	return runDocker(ctx, logger, "build", "-t", tag, dir)
}

// PushImage runs `docker push tag`. The host must already be `docker login`'d to
// the registry (CRN uses ambient docker credentials — like git uses ambient git
// credentials — rather than handling auth itself).
func PushImage(ctx context.Context, tag string, logger *slog.Logger) error {
	return runDocker(ctx, logger, "push", tag)
}

// runDocker execs the host `docker` CLI, capturing combined output so a failure
// surfaces the real error (not just a non-zero exit).
func runDocker(ctx context.Context, logger *slog.Logger, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		logger.Error("docker command failed", "args", strings.Join(args, " "), "output", strings.TrimSpace(string(out)))
		return fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
	}
	logger.Info("docker command ok", "args", strings.Join(args, " "))
	return nil
}
