package buildstep

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strings"
)

// Mirroring a delivered image to a second registry (Google Artifact Registry,
// KR "code runner on GCP") without changing its digest. A customer or FTC DV pins
// the digest CRN reports; the copy in Artifact Registry must answer to that same
// digest, so it is copied registry to registry as a carbon copy — never rebuilt
// or re-pushed, which could recompress layers and produce a new manifest.
//
// Auth is ambient, like PushImage: the CRN host must be logged in to both
// registries (for Artifact Registry, `gcloud auth configure-docker <region>-docker.pkg.dev`
// with a service account that may write to the repository).

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// RemoteDigest asks the registry for the manifest digest of ref — the digest a
// pull by digest uses, as the registry stores it.
func RemoteDigest(ctx context.Context, ref string, logger *slog.Logger) (string, error) {
	out, err := dockerOutput(ctx, logger, "buildx", "imagetools", "inspect", ref, "--format", "{{json .Manifest}}")
	if err != nil {
		return "", err
	}
	var m struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal([]byte(out), &m); err != nil || !digestPattern.MatchString(m.Digest) {
		return "", fmt.Errorf("buildstep: registry gave no manifest digest for %s: %q", ref, tailLines(out, 3))
	}
	return m.Digest, nil
}

// MirrorImage copies ref to dest byte for byte and returns the digest both now
// answer to. It fails if the registry ends up holding anything else.
func MirrorImage(ctx context.Context, ref, dest string, logger *slog.Logger) (string, error) {
	digest, err := RemoteDigest(ctx, ref, logger)
	if err != nil {
		return "", err
	}
	// --prefer-index=false: with a single source, copy it as it is instead of
	// wrapping it in a new index (which would have a digest of its own).
	if _, err := dockerOutput(ctx, logger, "buildx", "imagetools", "create", "--prefer-index=false", "--tag", dest, repository(ref)+"@"+digest); err != nil {
		return "", err
	}
	got, err := RemoteDigest(ctx, dest, logger)
	if err != nil {
		return "", err
	}
	if got != digest {
		return "", fmt.Errorf("buildstep: mirror changed the digest: %s holds %s, %s holds %s", ref, digest, dest, got)
	}
	return digest, nil
}

// MirrorTag is where tag lands under mirrorRegistry: the same path and tag below
// the registry prefix, so the two copies are easy to tell apart and to match.
func MirrorTag(imageRegistry, mirrorRegistry, tag string) string {
	path := strings.TrimPrefix(tag, strings.TrimRight(imageRegistry, "/")+"/")
	return strings.TrimRight(mirrorRegistry, "/") + "/" + path
}

// repository is ref without its tag or digest. A ':' before the last '/' is a
// registry port, not a tag.
func repository(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	return ref
}

// dockerOutput runs the docker CLI and returns its stdout. stderr — where buildx
// writes progress — is kept for the error, never mixed into the answer.
func dockerOutput(ctx context.Context, logger *slog.Logger, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String() + "\n" + stdout.String())
		logger.Error("docker command failed", "args", strings.Join(args, " "), "output", detail)
		return "", fmt.Errorf("docker %s: %w\n%s", strings.Join(args, " "), err, tailLines(detail, 25))
	}
	logger.Info("docker command ok", "args", strings.Join(args, " "))
	return strings.TrimSpace(stdout.String()), nil
}
