package buildstep

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// RepoImageFile is how a repository that is not a Next.js app has CRN build its image with
// the repository's own Dockerfile (FITT Billing, 13 Nov: Express + Vite, no root
// package.json, so the release check in verify-delivery.mjs can never pass on it). The
// repository's owner commits it. CRN then builds the commit it pushed with that Dockerfile,
// tags the image with the commit, checks what the image says about itself, and pushes it;
// it runs no container, no database and no migration, and adds no feedback widget.
//
//	{"schema_version": 1, "dockerfile": "deploy/billing.Dockerfile",
//	 "revision_build_arg": "BILLING_REVISION", "user": "node"}
//
// revision_build_arg is the build argument that gets the commit SHA; the Dockerfile must then
// label org.opencontainers.image.revision with it. Without it CRN labels the image itself.
// user, when set, is the user the image must run as.
const RepoImageFile = "crn-image.json"

// RepoImage is a repository's crn-image.json.
type RepoImage struct {
	SchemaVersion    int    `json:"schema_version"`
	Dockerfile       string `json:"dockerfile"`
	RevisionBuildArg string `json:"revision_build_arg,omitempty"`
	User             string `json:"user,omitempty"`
}

var (
	repoPath     = regexp.MustCompile(`^[A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+)*$`)
	buildArgName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	imageUser    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*(?::[A-Za-z0-9_][A-Za-z0-9_.-]*)?$`)
)

// ReadRepoImage reads dir's crn-image.json: nil when there is none. A file CRN cannot follow
// exactly is an error, never a fallback to the Next.js pipeline.
func ReadRepoImage(dir string) (*RepoImage, error) {
	raw, err := readRegular(filepath.Join(dir, RepoImageFile))
	if err != nil || raw == nil {
		return nil, err
	}
	var img RepoImage
	dec := json.NewDecoder(bytes.NewReader(*raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&img); err != nil {
		return nil, fmt.Errorf("%s: %w", RepoImageFile, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: one JSON object only", RepoImageFile)
	}
	switch {
	case img.SchemaVersion != 1:
		return nil, fmt.Errorf("%s: schema_version must be 1", RepoImageFile)
	case !repoPath.MatchString(img.Dockerfile) || filepath.Clean(img.Dockerfile) != img.Dockerfile || hasDotDot(img.Dockerfile):
		return nil, fmt.Errorf("%s: dockerfile %q is not a path inside the repository", RepoImageFile, img.Dockerfile)
	case img.RevisionBuildArg != "" && !buildArgName.MatchString(img.RevisionBuildArg):
		return nil, fmt.Errorf("%s: revision_build_arg %q is not a build argument name", RepoImageFile, img.RevisionBuildArg)
	case img.User != "" && !imageUser.MatchString(img.User):
		return nil, fmt.Errorf("%s: user %q is not a user name", RepoImageFile, img.User)
	}
	dockerfile, err := readRegular(filepath.Join(dir, img.Dockerfile))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", RepoImageFile, err)
	}
	if dockerfile == nil {
		return nil, fmt.Errorf("%s: %s is not in the repository", RepoImageFile, img.Dockerfile)
	}
	return &img, nil
}

func hasDotDot(path string) bool {
	for _, part := range strings.Split(path, "/") {
		if part == ".." || part == "." {
			return true
		}
	}
	return false
}

// readRegular reads a regular file: nil when there is nothing at path, an error when what is
// there is a link, a directory or anything else.
func readRegular(path string) (*[]byte, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return &raw, nil
}

// BuildArgs is the one build argument the image gets: the commit it is built from.
func (img *RepoImage) BuildArgs(commit string) map[string]string {
	if img.RevisionBuildArg == "" {
		return nil
	}
	return map[string]string{img.RevisionBuildArg: commit}
}

// RepoImageBase is what decides how a repository's image is built, as the repository held it
// before the agent ran.
type RepoImageBase struct {
	// Image is the owner's crn-image.json; nil when the repository has none.
	Image *RepoImage
	files map[string]*[]byte // nil: not there
}

// SnapshotRepoImage records crn-image.json and, when there is one, the Dockerfile it names
// and the .dockerignore files docker would read with it.
func SnapshotRepoImage(dir string) (*RepoImageBase, error) {
	img, err := ReadRepoImage(dir)
	if err != nil {
		return nil, err
	}
	base := &RepoImageBase{Image: img, files: map[string]*[]byte{}}
	for _, name := range base.paths() {
		if base.files[name], err = readRegular(filepath.Join(dir, name)); err != nil {
			return nil, err
		}
	}
	return base, nil
}

func (b *RepoImageBase) paths() []string {
	if b.Image == nil {
		return []string{RepoImageFile}
	}
	return []string{RepoImageFile, b.Image.Dockerfile, b.Image.Dockerfile + ".dockerignore", ".dockerignore"}
}

// Unchanged says whether dir still holds those files as they were: the repository's owner
// decides how its image is built, and a build that adds, changes or removes any of them stops.
// Without a crn-image.json before, a build may not add one, or an agent could take a Next.js
// app out of the release check.
func (b *RepoImageBase) Unchanged(dir string) error {
	for _, name := range b.paths() {
		now, err := readRegular(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		was := b.files[name]
		if (was == nil) != (now == nil) || (was != nil && !bytes.Equal(*was, *now)) {
			return fmt.Errorf("this build added, changed or removed %s: only the repository's owner decides how its image is built (%s)", name, RepoImageFile)
		}
	}
	return nil
}

// CommitImageTag is the tag of an image built from the repository's own Dockerfile: CRN's
// image name for the project (as DemoImageTag), tagged with the full commit SHA.
func CommitImageTag(registry, name, projectID, commit string) string {
	return demoImageRepo(registry, name, projectID) + ":" + commit
}

// BuildImageFrom is BuildImage with build arguments: docker build --platform linux/amd64 -f
// <dir>/<dockerfile> --build-arg k=v --label k=v -t <tag> <dir>.
func BuildImageFrom(ctx context.Context, dir, dockerfile, tag string, buildArgs, labels map[string]string, logger *slog.Logger) error {
	return runDocker(ctx, logger, buildArgsFor(dir, dockerfile, tag, buildArgs, labels)...)
}

// CheckRepoImage checks what a built image says about itself: linux/amd64, the label
// org.opencontainers.image.revision = commit, and user when one is asked for.
func CheckRepoImage(ctx context.Context, tag, commit, user string) error {
	out, err := dockerCommand(ctx, "image", "inspect", tag).Output()
	if err != nil {
		return fmt.Errorf("docker image inspect %s: %w", tag, err)
	}
	var images []struct {
		Os, Architecture string
		Config           struct {
			User   string
			Labels map[string]string
		}
	}
	if err := json.Unmarshal(out, &images); err != nil || len(images) != 1 {
		return fmt.Errorf("docker image inspect %s: not one image", tag)
	}
	img := images[0]
	var wrong []string
	if img.Os != "linux" || img.Architecture != "amd64" {
		wrong = append(wrong, fmt.Sprintf("platform is %s/%s, not linux/amd64", img.Os, img.Architecture))
	}
	if got := img.Config.Labels["org.opencontainers.image.revision"]; got != commit {
		wrong = append(wrong, fmt.Sprintf("label org.opencontainers.image.revision is %q, not the commit %s", got, commit))
	}
	if user != "" && img.Config.User != user {
		wrong = append(wrong, fmt.Sprintf("user is %q, not %q", img.Config.User, user))
	}
	if len(wrong) > 0 {
		return fmt.Errorf("the image %s: %s", tag, strings.Join(wrong, "; "))
	}
	return nil
}
