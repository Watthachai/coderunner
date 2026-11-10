package buildstep

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

// credentialOpened runs between opening a push credential and reading it. A test swaps the
// path here: what was opened is what must be read.
var credentialOpened = func() {}

type dockerConfigKey struct{}

// WithDockerConfig has every docker command under ctx use dir as its config, not ~/.docker,
// which an agent can write: its config can name a credential helper and its cli-plugins
// directory can replace buildx, and docker would run either with the token in reach.
func WithDockerConfig(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, dockerConfigKey{}, dir)
}

// dockerSteering is what in an environment can point docker at another daemon, context,
// builder or plugin, whatever config it is given.
var dockerSteering = []string{"DOCKER_", "BUILDX_", "BUILDKIT_", "COMPOSE_"}

// unsteeredEnv is this process's environment without any of that.
func unsteeredEnv() []string {
	var env []string
	for _, entry := range os.Environ() {
		steers := false
		for _, prefix := range dockerSteering {
			steers = steers || strings.HasPrefix(entry, prefix)
		}
		if !steers {
			env = append(env, entry)
		}
	}
	return env
}

// dockerEnv is this process's environment without any of that, and with the one config.
func dockerEnv(dir string) []string {
	return append(unsteeredEnv(), "DOCKER_CONFIG="+dir)
}

// dockerCommand is how this package runs docker.
func dockerCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", args...)
	if dir, ok := ctx.Value(dockerConfigKey{}).(string); ok {
		cmd.Env = dockerEnv(dir)
	}
	return cmd
}

// readCredentialFile checks and reads one open file: a regular file with one name, mode 600,
// owned by CRN's user, written since notBefore. Opening it refuses a link, and does not wait
// on a pipe. Everything after that goes through the descriptor, so replacing the path changes
// nothing about what is checked or what is read.
func readCredentialFile(path string, notBefore time.Time) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, errors.New("the push credential is not a regular file")
	}
	if err != nil {
		return nil, fmt.Errorf("the push credential: %w", err)
	}
	defer file.Close()
	credentialOpened()
	info, err := file.Stat()
	if err != nil {
		return nil, errors.New("the push credential cannot be read")
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
	if stat.Nlink != 1 {
		return nil, errors.New("the push credential does not have exactly one name")
	}
	if info.Size() > maxPushCredential {
		return nil, errors.New("the push credential is larger than one login")
	}
	if info.ModTime().Before(notBefore) {
		return nil, errors.New("the push credential is older than this build's request for it")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxPushCredential+1))
	if err != nil || int64(len(raw)) != info.Size() {
		return nil, errors.New("the push credential cannot be read")
	}
	return raw, nil
}

// ReadPushCredential reads what an operator left for one push, as readCredentialFile allows,
// holding one login for registryHost and nothing else. Its errors never quote the file.
func ReadPushCredential(path string, notBefore time.Time, registryHost string) ([]byte, error) {
	raw, err := readCredentialFile(path, notBefore)
	if err != nil {
		return nil, err
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

// NewBuildDockerConfig makes the docker config a listed build uses for everything but the
// push: no login, and Docker Desktop's own buildx. The caller removes it.
//
// It names registryHost, the delivery registry, with an empty login. A config that names no
// login at all is not one without a login: docker then picks the platform's credential store
// by itself (on macOS docker-credential-osxkeychain, which holds the machine's standing login)
// and sends what it finds. With one login named, empty, docker asks no store, for any host.
func NewBuildDockerConfig(registryHost string) (string, error) {
	if registryHost == "" || strings.ContainsAny(registryHost, "/ \t\n\"\\") {
		return "", fmt.Errorf("a registry host, not %q", registryHost)
	}
	config, err := json.Marshal(map[string]any{"auths": map[string]any{registryHost: map[string]any{}}})
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "crn-docker-")
	if err != nil {
		return "", err
	}
	plugins := filepath.Join(dir, "cli-plugins")
	if err := os.Mkdir(plugins, 0o700); err == nil {
		err = os.Symlink(buildxPlugin, filepath.Join(plugins, "docker-buildx"))
	}
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "config.json"), append(config, '\n'), 0o600)
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// NewPushDockerConfig makes the docker config of one push: a new directory, mode 700, holding
// one new file, mode 600, and nothing else. It is named on no command line and its content
// is in no environment: docker gets only the directory's path.
func NewPushDockerConfig(credential []byte) (string, error) {
	dir, err := os.MkdirTemp("", "crn-docker-push-")
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(filepath.Join(dir, "config.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_, err = file.Write(credential)
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// RemovePushDockerConfig removes it and checks that it is gone.
func RemovePushDockerConfig(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return errors.New("the push credential could not be removed")
	}
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		return errors.New("the push credential is still there after removing it")
	}
	return nil
}

// LoginOf takes the user:token out of a credential ReadPushCredential accepted.
func LoginOf(credential []byte) (string, error) {
	var config struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(credential, &config); err != nil {
		return "", errors.New("the push credential is not one docker login")
	}
	for _, login := range config.Auths {
		plain, err := base64.StdEncoding.DecodeString(login.Auth)
		if err != nil {
			return "", errors.New("the push credential does not hold a user and a token")
		}
		return string(plain), nil
	}
	return "", errors.New("the push credential holds no login")
}

// RemoveLocalImage takes a tag that never reached the registry off this machine.
func RemoveLocalImage(ctx context.Context, tag string, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := runDocker(ctx, logger, "rmi", "-f", tag); err != nil {
		logger.Warn("remove the local image", "tag", tag, "err", err)
	}
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
