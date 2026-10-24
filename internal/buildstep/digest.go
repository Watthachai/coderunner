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

// FTC DV pulls a delivered image by digest only, so CRN reports the digest the
// delivery registry gave the push. That registry speaks plain HTTP, which buildx
// imagetools refuses, so the digest is read from the local image's RepoDigests
// rather than asked of the registry.
//
// Same code as feature/gcp-mirror's mirror.go; when that branch lands, keep one copy.

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// PushedDigest is the manifest digest the registry gave ref when it was pushed,
// from the local image's RepoDigests.
func PushedDigest(ctx context.Context, ref string, logger *slog.Logger) (string, error) {
	out, err := dockerOutput(ctx, logger, "image", "inspect", ref, "--format", "{{json .RepoDigests}}")
	if err != nil {
		return "", err
	}
	var digests []string
	_ = json.Unmarshal([]byte(out), &digests)
	repo := repository(ref)
	for _, d := range digests {
		if r, digest, ok := strings.Cut(d, "@"); ok && r == repo && digestPattern.MatchString(digest) {
			return digest, nil
		}
	}
	return "", fmt.Errorf("buildstep: no digest from %s for the local image (was it pushed?): %q", repo, tailLines(out, 3))
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

// dockerOutput runs the docker CLI and returns its stdout. stderr is kept for
// the error, never mixed into the answer.
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
