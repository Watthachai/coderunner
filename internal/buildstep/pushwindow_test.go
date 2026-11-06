package buildstep

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const testRegistryHtpasswd = `test-user:$2y$05$XGyudoa8cV0a1UHholDhHOS9F1C6T7naLaF5uJ.a2EhafcNl0OgU2`

func TestProcessesAreReadFromPs(t *testing.T) {
	out := `    1     0     0 Tue Nov  3 11:26:15 2026     /sbin/launchd
 4242     1   501 Fri Nov  6 14:18:53 2026     /Applications/Some App.app/Contents/MacOS/Some App
`
	list, err := parseProcesses(out, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("%d processes: %+v", len(list), list)
	}
	got := list[1]
	if got.pid != 4242 || got.ppid != 1 || got.uid != 501 || got.path != "/Applications/Some App.app/Contents/MacOS/Some App" {
		t.Errorf("read %+v", got)
	}
	if want := time.Date(2026, 11, 6, 14, 18, 53, 0, time.UTC); !got.started.Equal(want) {
		t.Errorf("started %v, want %v", got.started, want)
	}
}

// A process the sweep cannot read is one it would leave running.
func TestAProcessListItCannotReadIsAnError(t *testing.T) {
	for _, out := range []string{"1 0 0 /sbin/launchd\n", "x 1 501 Fri Nov  6 14:18:53 2026 /bin/sh\n", "7 1 501 Someday Nov  6 14:18:53 2026 /bin/sh\n"} {
		if _, err := parseProcesses(out, time.UTC); err == nil {
			t.Errorf("read %q without error", out)
		}
	}
}

func TestWhichProcessesAreStrays(t *testing.T) {
	since := time.Date(2026, 11, 6, 14, 0, 0, 500, time.UTC)
	after := since.Add(time.Minute)
	const me, self = 501, 99
	cases := []struct {
		name string
		p    process
		want bool
	}{
		{"an orphan the agent left", process{10, 1, me, after, "/bin/sh"}, true},
		{"an orphan in the second the agent started", process{10, 1, me, since.Truncate(time.Second), "/bin/sh"}, true},
		{"a program in the workspace", process{10, 1, me, after, "/Users/x/.crn-workspaces/p/evil"}, true},
		{"homebrew's node", process{10, 1, me, after, "/opt/homebrew/bin/node"}, true},
		{"a system tool outside the exempt paths", process{10, 1, me, after, "/usr/bin/perl"}, true},
		{"older than the agent", process{10, 1, me, since.Add(-time.Minute), "/bin/sh"}, false},
		{"another user's", process{10, 1, 0, after, "/bin/sh"}, false},
		{"a child of something alive", process{10, 77, me, after, "/bin/sh"}, false},
		{"CRN itself", process{self, 1, me, after, "/Users/x/bin/crn-server"}, false},
		{"a service macOS started", process{10, 1, me, after, "/System/Library/x"}, false},
		{"a daemon macOS started", process{10, 1, me, after, "/usr/libexec/y"}, false},
		{"a daemon in sbin", process{10, 1, me, after, "/usr/sbin/cfprefsd"}, false},
		{"launchd's own sbin", process{10, 1, me, after, "/sbin/launchd"}, false},
		{"apple's library", process{10, 1, me, after, "/Library/Apple/z"}, false},
		{"an app opened at the screen", process{10, 1, me, after, "/Applications/Docker.app/Contents/MacOS/com.docker.backend"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strayProcesses([]process{c.p}, since, me, self)
			if (len(got) == 1) != c.want {
				t.Errorf("stray = %v, want %v", got, c.want)
			}
		})
	}
}

func TestASweepThatCannotSeeProcessesIsAnError(t *testing.T) {
	old := psCommand
	psCommand = filepath.Join(t.TempDir(), "no-such-ps")
	t.Cleanup(func() { psCommand = old })
	if _, err := StopStrayProcesses(context.Background(), time.Now(), nil); err == nil {
		t.Error("no error")
	}
}

// A process that comes back as often as it is stopped is one the build cannot be rid of.
func TestAProcessThatKeepsComingBackIsAnError(t *testing.T) {
	old := listProcesses
	t.Cleanup(func() { listProcesses = old })
	listProcesses = func(context.Context) ([]process, error) {
		// No process can have this pid: macOS's largest is 99998.
		return []process{{pid: 99999, ppid: 1, uid: os.Getuid(), started: time.Now(), path: "/bin/sh"}}, nil
	}
	stopped, err := StopStrayProcesses(context.Background(), time.Now().Add(-time.Minute), nil)
	if err == nil || !strings.Contains(err.Error(), "keep coming back") {
		t.Errorf("err = %v", err)
	}
	if len(stopped) != strayRounds {
		t.Errorf("tried %d times, want %d", len(stopped), strayRounds)
	}
}

// startOrphan leaves a process that left its group and session and cleared its environment,
// and whose parent is gone: launchd has it. It returns the process's pid.
func startOrphan(t *testing.T) int {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "pid")
	script := `use POSIX; fork and exit; setsid; open(F, ">", $ARGV[0]); print F $$; close(F); open(STDIN, "</dev/null"); open(STDOUT, ">/dev/null"); open(STDERR, ">/dev/null"); sleep 300;`
	if out, err := exec.Command("/usr/bin/env", "-i", "/usr/bin/perl", "-e", script, pidFile).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	for i := 0; i < 200; i++ {
		if raw, err := os.ReadFile(pidFile); err == nil && len(raw) > 0 {
			pid, err := strconv.Atoi(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no pid")
	return 0
}

func waitGone(pid int) bool {
	for i := 0; i < 150; i++ {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// The real thing on a real process table. It stops any of this user's new orphans, so it
// is opt-in, for a machine with nothing else starting up.
func TestTheSweepStopsAProcessThatRanAwayForReal(t *testing.T) {
	if os.Getenv("CRN_TEST_REAL_STRAYS") == "" {
		t.Skip("set CRN_TEST_REAL_STRAYS: this stops every new orphan of this user")
	}
	older := startOrphan(t)
	time.Sleep(1200 * time.Millisecond)
	since := time.Now()
	time.Sleep(1100 * time.Millisecond)
	stray := startOrphan(t)
	child := exec.Command("/bin/sleep", "300")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })

	stopped, err := StopStrayProcesses(context.Background(), since, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("stopped: %q", stopped)
	if !waitGone(stray) {
		t.Errorf("the orphan %d is still running", stray)
	}
	if err := syscall.Kill(older, 0); err != nil {
		t.Errorf("the older orphan %d was stopped: %v", older, err)
	}
	if err := syscall.Kill(child.Process.Pid, 0); err != nil {
		t.Errorf("a live process's child was stopped: %v", err)
	}
}

func TestAPushCredentialFromAnotherUserIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"auths":{"r":{"auth":"dTp0"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPushCredential(path, time.Time{}, "r"); err != nil {
		t.Fatalf("a good file was refused: %v", err)
	}
	old := listOwner
	listOwner = func() int { return os.Getuid() + 1 }
	t.Cleanup(func() { listOwner = old })
	if _, err := ReadPushCredential(path, time.Time{}, "r"); err == nil {
		t.Error("a file owned by another user was taken")
	}
}

// Each refusal says what is wrong, which is all an operator gets: never the file's content.
func TestAPushCredentialSaysWhyItIsRefused(t *testing.T) {
	const good = `{"auths":{"r":{"auth":"dTp0"}}}`
	cases := map[string]struct {
		body string
		want string
	}{
		"another registry": {`{"auths":{"other":{"auth":"dTp0"}}}`, "not for this build's registry"},
		"two logins":       {`{"auths":{"r":{"auth":"dTp0"},"s":{"auth":"dTp0"}}}`, "not for this build's registry"},
		"no logins":        {`{"auths":{}}`, "not for this build's registry"},
		"not one login":    {`user:token`, "not one docker login"},
		"no separator":     {`{"auths":{"r":{"auth":"bm9zZXA="}}}`, "does not hold a user and a token"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(c.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadPushCredential(path, time.Time{}, "r"); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
	t.Run("a link", func(t *testing.T) {
		dir := t.TempDir()
		real, link := filepath.Join(dir, "real"), filepath.Join(dir, "config.json")
		if err := os.WriteFile(real, []byte(good), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadPushCredential(link, time.Time{}, "r"); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestAPushCredentialErrorNeverQuotesTheFile(t *testing.T) {
	const secret = "dGhlLXNlY3JldA"
	for _, body := range []string{secret, `{"auths":{"r":{"auth":"` + secret + `%"}}}`, `{"auths":{"other":{"auth":"` + secret + `"}}}`} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := ReadPushCredential(path, time.Time{}, "r")
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Errorf("err = %v", err)
		}
	}
}

func TestADockerConfigHoldsTheLoginAndThePlugin(t *testing.T) {
	dir, err := NewDockerConfig([]byte(`{"auths":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the directory: %v, %v", info, err)
	}
	if info, err := os.Stat(filepath.Join(dir, "config.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the file: %v, %v", info, err)
	}
	if target, err := os.Readlink(filepath.Join(dir, "cli-plugins", "docker-buildx")); err != nil || target != buildxPlugin {
		t.Errorf("the plugin links to %q, %v", target, err)
	}
	if err := ClearDockerCredential(dir); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "config.json")); strings.TrimSpace(string(raw)) != "{}" {
		t.Errorf("after clearing: %q", raw)
	}
}

func TestDockerCommandsUseTheConfigTheyAreGiven(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", "")
	plain := dockerCommand(context.Background(), "version")
	if plain.Env != nil {
		t.Errorf("without one, docker's environment was set: %v", plain.Env)
	}
	given := dockerCommand(WithDockerConfig(context.Background(), "/the/dir"), "version")
	last := given.Env[len(given.Env)-1]
	if last != "DOCKER_CONFIG=/the/dir" {
		t.Errorf("the last variable, which wins, is %q", last)
	}
}

// The operator's script and the build agree on the file, and the token stays off the screen.
func TestTheOperatorsScriptLeavesWhatTheBuildAccepts(t *testing.T) {
	const project = "efa0ff04-7885-4d86-a4ff-4829cccd2d9f"
	script, err := filepath.Abs("../../deploy/macmini/push-window.sh")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	run := func(stdin string, args ...string) (string, error) {
		cmd := exec.Command("/bin/bash", append([]string{script}, args...)...)
		cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	const login = "gitlab+deploy-token-9:gldt-the-secret-value"
	dir := filepath.Join(home, "crn-secrets", "push", project)
	path := filepath.Join(dir, "config.json")

	// Nobody is waiting: nothing is left, and the operator is told to revoke.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := run(login+"\n", "receive", project)
	if err == nil || !strings.Contains(out, "REVOKE") {
		t.Errorf("with no READY: %v, %s", err, out)
	}
	if _, err := os.Lstat(path); err == nil {
		t.Error("a token was left with nobody waiting")
	}
	if strings.Contains(out, "the-secret-value") {
		t.Errorf("the token was printed: %s", out)
	}

	if err := os.WriteFile(filepath.Join(dir, "READY"), []byte("job-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = run(login+"\n", "receive", project)
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if strings.Contains(out, "the-secret-value") {
		t.Errorf("the token was printed: %s", out)
	}
	if !strings.Contains(out, "job-1") {
		t.Errorf("it does not say which job: %s", out)
	}
	raw, err := ReadPushCredential(path, time.Time{}, "172.168.1.234:5050")
	if err != nil {
		t.Fatalf("the build refuses the file: %v", err)
	}
	if !strings.Contains(string(raw), "Z2l0bGFi") {
		t.Errorf("the file: %s", raw)
	}

	for _, bad := range []string{"", "no-separator\n", ":\n"} {
		if out, err := run(bad, "receive", project); err == nil {
			t.Errorf("took %q: %s", bad, out)
		}
	}
	if out, err := run("", "receive", "not-a-project"); err == nil {
		t.Errorf("took a bad id: %s", out)
	}
}

// docker itself, on a machine that has it: a registry on this machine that wants a login,
// the plugin from the config directory, and no use of the home directory. Nothing leaves
// the machine.
func TestDockerUsesOnlyTheConfigItIsGivenForReal(t *testing.T) {
	if os.Getenv("CRN_TEST_REAL_DOCKER") == "" {
		t.Skip("set CRN_TEST_REAL_DOCKER: this runs a registry container")
	}
	ctx := context.Background()
	auth := t.TempDir()
	if err := os.WriteFile(filepath.Join(auth, "htpasswd"), []byte(testRegistryHtpasswd+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("crn-test-registry-%d", os.Getpid())
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name, "-p", "127.0.0.1::5000",
		"-v", auth+":/auth:ro", "-e", "REGISTRY_AUTH=htpasswd", "-e", "REGISTRY_AUTH_HTPASSWD_REALM=test",
		"-e", "REGISTRY_AUTH_HTPASSWD_PATH=/auth/htpasswd", "registry:2").CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	raw, err := exec.Command("docker", "port", name, "5000").Output()
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := strings.Cut(strings.TrimSpace(strings.Split(string(raw), "\n")[0]), ":")
	host := "localhost:" + port
	tag := host + "/push-window/check:v1"
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", tag).Run() })

	// What an agent could have left in the home directory: a working login, and a buildx
	// that gives itself away.
	home := t.TempDir()
	marks := filepath.Join(t.TempDir(), "marks")
	writeTree(t, home, ".docker/config.json", `{"auths":{"`+host+`":{"auth":"dGVzdC11c2VyOnRlc3QtcGFzcw=="}}}`)
	writeTree(t, home, ".docker/cli-plugins/docker-buildx", "#!/bin/sh\necho ran >> "+marks+"\nexit 1\n")
	if err := os.Chmod(filepath.Join(home, ".docker/cli-plugins/docker-buildx"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	build := t.TempDir()
	writeTree(t, build, "Dockerfile", "FROM scratch\nCOPY a.txt /a.txt\n")
	writeTree(t, build, "a.txt", fmt.Sprintf("push window %d\n", time.Now().UnixNano()))

	noLogin, err := NewDockerConfig([]byte("{}\n"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(noLogin) })
	bare := WithDockerConfig(ctx, noLogin)
	if err := BuildImage(bare, build, "Dockerfile", tag, map[string]string{"t": "1"}, nil); err != nil {
		t.Fatalf("docker build with the given config: %v", err)
	}
	if raw, err := os.ReadFile(marks); err == nil {
		t.Errorf("the buildx in the home directory ran: %s", raw)
	}
	if err := PushImageOnce(bare, tag, nil); err == nil {
		t.Error("the push worked with no login: it used the one in the home directory")
	}

	withLogin, err := NewDockerConfig([]byte(`{"auths":{"` + host + `":{"auth":"dGVzdC11c2VyOnRlc3QtcGFzcw=="}}}`))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(withLogin) })
	given := WithDockerConfig(ctx, withLogin)
	if err := PushImageOnce(given, tag, nil); err != nil {
		t.Fatalf("the push with the given login: %v", err)
	}
	digest, err := PushedDigest(given, tag, nil)
	if err != nil || !strings.HasPrefix(digest, "sha256:") {
		t.Errorf("digest %q, %v", digest, err)
	}
	t.Logf("pushed %s", digest)
	if err := ClearDockerCredential(withLogin); err != nil {
		t.Fatal(err)
	}
	var runErr *exec.ExitError
	if err := PushImageOnce(given, tag, nil); err == nil {
		t.Error("the push worked after the login was cleared")
	} else if errors.As(err, &runErr) {
		t.Logf("after clearing: %v", runErr)
	}
}
