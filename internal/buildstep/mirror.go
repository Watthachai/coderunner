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
// digest.
//
// The copy is pushed from the local image store, right after the delivery push:
// the delivery registry speaks plain HTTP, which buildx imagetools refuses, so it
// cannot be read back registry to registry. With the containerd image store (the
// build machine's) a push sends the stored manifest unchanged; the mirror's digest
// is then checked against the one the delivery registry gave, so a store that
// re-encodes layers fails loudly instead of handing out a second digest.
//
// Auth is ambient, like PushImage: the CRN host must be logged in to both
// registries (for Artifact Registry, docker login at the machine itself with the
// crn-pusher key — not over ssh, where the keychain is out of reach).

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

// MirrorImage pushes the local image ref (already pushed to its delivery
// registry) to dest and returns the digest both answer to. It fails if dest ends
// up holding anything else.
func MirrorImage(ctx context.Context, ref, dest string, logger *slog.Logger) (string, error) {
	digest, err := PushedDigest(ctx, ref, logger)
	if err != nil {
		return "", err
	}
	if _, err := dockerOutput(ctx, logger, "tag", ref, dest); err != nil {
		return "", err
	}
	if err := PushImage(ctx, dest, logger); err != nil {
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
