package buildstep

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// buildxPlugin is Docker Desktop's own copy. The link in ~/.docker/cli-plugins is one an
// agent can point anywhere.
const buildxPlugin = "/Applications/Docker.app/Contents/Resources/cli-plugins/docker-buildx"

// maxPushCredential is far more than one login needs.
const maxPushCredential = 8 * 1024

type dockerConfigKey struct{}

// WithDockerConfig has every docker command under ctx use dir as its config, not ~/.docker,
// which an agent can write: its config can name a credential helper and its cli-plugins
// directory can replace buildx, and docker would run either with the token in reach.
func WithDockerConfig(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, dockerConfigKey{}, dir)
}

// dockerCommand is how this package runs docker.
func dockerCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", args...)
	if dir, ok := ctx.Value(dockerConfigKey{}).(string); ok {
		cmd.Env = append(os.Environ(), "DOCKER_CONFIG="+dir)
	}
	return cmd
}

// ReadPushCredential reads what an operator left for one push: a regular file, mode 600,
// owned by CRN's user, written since the build asked at notBefore, holding one login for
// registryHost and nothing else. Its errors never quote the file.
func ReadPushCredential(path string, notBefore time.Time, registryHost string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("the push credential: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("the push credential is not a regular file")
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		return nil, fmt.Errorf("the push credential has mode %o, want 600", perm)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != listOwner() {
		return nil, errors.New("the push credential is not owned by this user")
	}
	if info.Size() > maxPushCredential {
		return nil, errors.New("the push credential is larger than one login")
	}
	if info.ModTime().Before(notBefore) {
		return nil, errors.New("the push credential is older than this build's request for it")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("the push credential cannot be read")
	}
	var config struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, errors.New("the push credential is not one docker login")
	}
	login, ok := config.Auths[registryHost]
	if len(config.Auths) != 1 || !ok {
		return nil, errors.New("the push credential is not for this build's registry alone")
	}
	plain, err := base64.StdEncoding.DecodeString(login.Auth)
	if err != nil || !bytes.Contains(plain, []byte(":")) {
		return nil, errors.New("the push credential does not hold a user and a token")
	}
	return raw, nil
}

// NewDockerConfig makes a docker config directory holding credential and the plugin docker
// build needs. The caller removes it.
func NewDockerConfig(credential []byte) (string, error) {
	dir, err := os.MkdirTemp("", "crn-docker-")
	if err != nil {
		return "", err
	}
	plugins := filepath.Join(dir, "cli-plugins")
	if err := os.Mkdir(plugins, 0o700); err == nil {
		err = os.Symlink(buildxPlugin, filepath.Join(plugins, "docker-buildx"))
	}
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "config.json"), credential, 0o600)
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// ClearDockerCredential leaves the config in dir with no login: what follows the push reads
// the registry without one.
func ClearDockerCredential(dir string) error {
	return os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}\n"), 0o600)
}

// ContainerIDs lists every container, running or not.
func ContainerIDs(ctx context.Context, logger *slog.Logger) (map[string]bool, error) {
	if logger == nil {
		logger = slog.Default()
	}
	out, err := dockerOutput(ctx, logger, "ps", "-aq", "--no-trunc")
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, id := range strings.Fields(out) {
		ids[id] = true
	}
	return ids, nil
}

// RemoveContainersExcept removes every container not in keep, and says which. A container
// is outside the agent's process tree, so it outlives the agent, and one given the docker
// socket can change the image a build is about to push.
func RemoveContainersExcept(ctx context.Context, keep map[string]bool, logger *slog.Logger) ([]string, error) {
	if logger == nil {
		logger = slog.Default()
	}
	ids, err := ContainerIDs(ctx, logger)
	if err != nil {
		return nil, err
	}
	var removed []string
	for id := range ids {
		if keep[id] {
			continue
		}
		if err := runDocker(ctx, logger, "rm", "-f", id); err != nil {
			return removed, err
		}
		removed = append(removed, id)
	}
	return removed, nil
}
