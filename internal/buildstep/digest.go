package buildstep

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// FTC DV pulls a delivered image by digest only, so CRN reports the digest the
// delivery registry gave the push. That registry speaks plain HTTP, which buildx
// imagetools refuses, so the digest is read from the local image's RepoDigests
// rather than asked of the registry.

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
