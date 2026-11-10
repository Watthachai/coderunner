package buildstep

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
		{"docker desktop's own", process{10, 1, me, after, "/Applications/Docker.app/Contents/MacOS/com.docker.backend"}, false},
		{"another app", process{10, 1, me, after, "/Applications/Other.app/Contents/MacOS/Other"}, true},
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

// Fittcore's finding on r6: looking at a path and then reading it leaves a gap. What is checked
// and what is read are one open file, so swapping the path in that gap changes nothing.
func TestAPushCredentialIsCheckedAndReadThroughOneOpenFile(t *testing.T) {
	const good = `{"auths":{"r":{"auth":"dTp0"}}}`
	dir := t.TempDir()
	path, other := filepath.Join(dir, "config.json"), filepath.Join(dir, "other")
	if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte(`{"auths":{"r":{"auth":"b3RoZXI6dG9rZW4="}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := credentialOpened
	credentialOpened = func() {
		if err := os.Rename(path, path+".moved"); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(other, path); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { credentialOpened = old })
	raw, err := ReadPushCredential(path, time.Time{}, "r")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != good {
		t.Errorf("read the file that replaced it: %s", raw)
	}
}

func TestAPushCredentialThatIsNotOneRegularFileIsRefused(t *testing.T) {
	const good = `{"auths":{"r":{"auth":"dTp0"}}}`
	t.Run("a pipe, which must not hold the build", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := ReadPushCredential(path, time.Time{}, "r")
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "not a regular file") {
				t.Errorf("err = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("reading a pipe held the build")
		}
	})
	t.Run("a directory", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadPushCredential(path, time.Time{}, "r"); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a file removed once it is open", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
			t.Fatal(err)
		}
		old := credentialOpened
		credentialOpened = func() { _ = os.Remove(path) }
		t.Cleanup(func() { credentialOpened = old })
		if _, err := ReadPushCredential(path, time.Time{}, "r"); err == nil || !strings.Contains(err.Error(), "one name") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a second name for one file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.json")
		if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(path, filepath.Join(dir, "another-name")); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadPushCredential(path, time.Time{}, "r"); err == nil || !strings.Contains(err.Error(), "one name") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("the mode, the size and the age", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.json")
		if err := os.WriteFile(path, []byte(good), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadPushCredential(path, time.Time{}, "r"); err == nil || !strings.Contains(err.Error(), "mode") {
			t.Errorf("mode: err = %v", err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadPushCredential(path, time.Now().Add(time.Hour), "r"); err == nil || !strings.Contains(err.Error(), "older") {
			t.Errorf("age: err = %v", err)
		}
		if err := os.WriteFile(path, make([]byte, maxPushCredential+1), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadPushCredential(path, time.Time{}, "r"); err == nil || !strings.Contains(err.Error(), "larger") {
			t.Errorf("size: err = %v", err)
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

func TestTheBuildsDockerConfigHoldsNoLogin(t *testing.T) {
	dir, err := NewBuildDockerConfig("172.168.1.234:5050")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the directory: %v, %v", info, err)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "config.json")); strings.TrimSpace(string(raw)) != `{"auths":{"172.168.1.234:5050":{}}}` {
		t.Errorf("the file holds %q", raw)
	}
	for _, bad := range []string{"", "172.168.1.234:5050/fitt", "a b", `a"b`} {
		if dir, err := NewBuildDockerConfig(bad); err == nil {
			_ = os.RemoveAll(dir)
			t.Errorf("a config for host %q", bad)
		}
	}
	if target, err := os.Readlink(filepath.Join(dir, "cli-plugins", "docker-buildx")); err != nil || target != buildxPlugin {
		t.Errorf("the plugin links to %q, %v", target, err)
	}
}

// NTC build 3, 9 Nov: the acceptance step read the registry with the keychain's revoked
// crn-push. A config that names no login is not a config without one: docker then looks
// for the platform's credential store (on macOS docker-credential-osxkeychain) by itself.
func TestTheBuildsDockerConfigAsksNoCredentialStore(t *testing.T) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("no docker CLI")
	}
	bin, marks := t.TempDir(), t.TempDir()
	helper := "#!/bin/sh\necho \"$0 $1\" >> '" + filepath.Join(marks, "asked") + "'\n" +
		`printf '{"ServerURL":"127.0.0.1:1","Username":"leak","Secret":"leak"}'` + "\n"
	for _, name := range []string{"docker-credential-osxkeychain", "docker-credential-desktop", "docker-credential-secretservice", "docker-credential-pass", "docker-credential-wincred"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(helper), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+filepath.Dir(docker)+string(os.PathListSeparator)+"/usr/bin:/bin")
	dir, err := NewBuildDockerConfig("127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ctx, cancel := context.WithTimeout(WithDockerConfig(context.Background(), dir), 30*time.Second)
	defer cancel()
	// Nothing answers there: only whether docker asked a store for a login matters.
	out, _ := dockerCommand(ctx, "manifest", "inspect", "--insecure", "127.0.0.1:1/crn/accept:v1").CombinedOutput()
	if raw, err := os.ReadFile(filepath.Join(marks, "asked")); err == nil {
		t.Errorf("docker asked a credential store for a login: %s(docker said %q)", raw, strings.TrimSpace(string(out)))
	}
}

// The push's config: a new directory and a new file, not a link, and nothing else in it.
func TestThePushsDockerConfigIsNewAndClosed(t *testing.T) {
	dir, err := NewPushDockerConfig([]byte(`{"auths":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	info, err := os.Lstat(dir)
	if err != nil || info.Mode().Perm() != 0o700 || !info.IsDir() {
		t.Errorf("the directory: %v, %v", info, err)
	}
	file, err := os.Lstat(filepath.Join(dir, "config.json"))
	if err != nil || file.Mode().Perm() != 0o600 || !file.Mode().IsRegular() {
		t.Errorf("the file: %v, %v", file, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Errorf("the directory holds %v, %v", entries, err)
	}
	if err := RemovePushDockerConfig(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dir); err == nil {
		t.Error("it is still there")
	}
}

func TestALoginIsReadFromTheCredential(t *testing.T) {
	got, err := LoginOf([]byte(`{"auths":{"r":{"auth":"dXNlcjp0b2tlbg=="}}}`))
	if err != nil || got != "user:token" {
		t.Errorf("%q, %v", got, err)
	}
	for _, bad := range []string{`nope`, `{"auths":{}}`, `{"auths":{"r":{"auth":"%%%"}}}`} {
		if _, err := LoginOf([]byte(bad)); err == nil {
			t.Errorf("read %q", bad)
		}
	}
}

// --- what is compared with how it was ---------------------------------------------------

func TestAChangeToTheMachineIsSeen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	bin := t.TempDir()
	writeTree(t, bin, "node", "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(bin, "node"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx := context.Background()
	before, err := CaptureMachineState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	same, err := CaptureMachineState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if differ := before.Differences(same); len(differ) != 0 {
		t.Fatalf("two looks differ: %v", differ)
	}
	cases := map[string]func(){
		"config ~/.docker/config.json":    func() { writeTree(t, home, ".docker/config.json", `{"credsStore":"evil"}`) },
		"config ~/.gitconfig":             func() { writeTree(t, home, ".gitconfig", "[core]\n\thooksPath=/x\n") },
		"config ~/.config/git/config":     func() { writeTree(t, home, ".config/git/config", "x") },
		"config ~/.config/git/ignore":     func() { writeTree(t, home, ".config/git/ignore", "x") },
		"config ~/.config/git/attributes": func() { writeTree(t, home, ".config/git/attributes", "x") },
		"listing ~/.docker/cli-plugins":   func() { writeTree(t, home, ".docker/cli-plugins/docker-buildx", "#!/bin/sh\n") },
		"listing ~/Library/LaunchAgents":  func() { writeTree(t, home, "Library/LaunchAgents/evil.plist", "x") },
		"program node":                    func() { writeTree(t, bin, "node", "#!/bin/sh\necho changed\n") },
	}
	for want, change := range cases {
		t.Run(want, func(t *testing.T) {
			change()
			now, err := CaptureMachineState(ctx)
			if err != nil {
				t.Fatal(err)
			}
			differ := before.Differences(now)
			if !slices.Contains(differ, want) {
				t.Errorf("not seen: %v", differ)
			}
			before = now
		})
	}
	t.Run("the same program from another place", func(t *testing.T) {
		other := t.TempDir()
		raw, err := os.ReadFile(filepath.Join(bin, "node"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(other, "node"), raw, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", other+string(os.PathListSeparator)+os.Getenv("PATH"))
		now, err := CaptureMachineState(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if differ := before.Differences(now); !slices.Contains(differ, "program node") {
			t.Errorf("not seen: %v", differ)
		}
	})
	t.Run("a program that shadows another", func(t *testing.T) {
		writeTree(t, bin, "docker", "#!/bin/sh\n")
		if err := os.Chmod(filepath.Join(bin, "docker"), 0o755); err != nil {
			t.Fatal(err)
		}
		now, err := CaptureMachineState(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if differ := before.Differences(now); !slices.Contains(differ, "program docker") {
			t.Errorf("not seen: %v", differ)
		}
	})
}

// fakeTool is a stand-in for a system program, printing what a file holds.
func fakeTool(t *testing.T, stdout string, code int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tool")
	body := fmt.Sprintf("#!/bin/sh\ncat <<'OUT'\n%s\nOUT\nexit %d\n", stdout, code)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAJobScheduledByTheAgentIsSeen(t *testing.T) {
	oldCron, oldLaunchctl := cronCommand, launchctlCommand
	t.Cleanup(func() { cronCommand, launchctlCommand = oldCron, oldLaunchctl })
	ctx := context.Background()
	const list = "PID\tStatus\tLabel\n1\t0\tcom.docker.helper\n-\t0\tcom.fitt.crn-server"
	cronCommand, launchctlCommand = fakeTool(t, "0 1 * * * nothing", 0), fakeTool(t, list, 0)
	before, err := CaptureMachineState(ctx)
	if err != nil {
		t.Fatal(err)
	}

	cronCommand = fakeTool(t, "* * * * * evil", 0)
	now, err := CaptureMachineState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(before.Differences(now), ","); got != "cron" {
		t.Errorf("a cron line: %s", got)
	}

	cronCommand = fakeTool(t, "0 1 * * * nothing", 0)
	launchctlCommand = fakeTool(t, list+"\n-\t0\tevil.job", 0)
	if now, err = CaptureMachineState(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(before.Differences(now), ","); got != "launchd labels" {
		t.Errorf("a launchd job: %s", got)
	}

	// macOS's own come and go by themselves, as do applications.
	launchctlCommand = fakeTool(t, list+"\n7\t0\tcom.apple.mdworker.shared.0F\n8\t0\tapplication.com.google.Chrome.9", 0)
	if now, err = CaptureMachineState(ctx); err != nil {
		t.Fatal(err)
	}
	if got := before.Differences(now); len(got) != 0 {
		t.Errorf("noise was seen as a change: %v", got)
	}
}

// What it cannot read it cannot compare, so a look that fails is an error, not a blank.
func TestAMachineItCannotReadIsAnError(t *testing.T) {
	oldCron, oldLaunchctl := cronCommand, launchctlCommand
	t.Cleanup(func() { cronCommand, launchctlCommand = oldCron, oldLaunchctl })
	ctx := context.Background()
	launchctlCommand = fakeTool(t, "PID\tStatus\tLabel", 0)
	cronCommand = fakeTool(t, "denied", 1)
	if _, err := CaptureMachineState(ctx); err == nil {
		t.Error("a crontab that failed passed")
	}
	cronCommand = filepath.Join(t.TempDir(), "no-such-tool")
	if _, err := CaptureMachineState(ctx); err == nil {
		t.Error("no crontab program passed")
	}
	// "no crontab" is an answer: this user simply has none.
	cronCommand = fakeTool(t, "", 1)
	body := "#!/bin/sh\necho 'crontab: no crontab for me' >&2\nexit 1\n"
	if err := os.WriteFile(cronCommand, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureMachineState(ctx); err != nil {
		t.Errorf("no crontab: %v", err)
	}
	cronCommand = oldCron
	launchctlCommand = filepath.Join(t.TempDir(), "no-such-tool")
	if _, err := CaptureMachineState(ctx); err == nil {
		t.Error("no launchctl passed")
	}
}

// Each program CRN runs after the agent is on the list.
func TestTheMachineStateNamesEveryProgram(t *testing.T) {
	state, err := CaptureMachineState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"git", "docker", "node", "git-remote-https", "git-credential-osxkeychain", "docker-buildx", "crn-server"} {
		if _, ok := state["program "+name]; !ok {
			t.Errorf("program %s is not on the list", name)
		}
	}
	if state["program git"] == "absent" || state["program crn-server"] == "absent" {
		t.Errorf("a program that is there reads as absent: %v", state)
	}
	for _, name := range []string{"config /etc/gitconfig", "config homebrew gitconfig"} {
		if _, ok := state[name]; !ok {
			t.Errorf("%s is not on the list", name)
		}
	}
}

func TestDifferencesAreBothWays(t *testing.T) {
	a := MachineState{"same": "1", "changed": "1", "gone": "1"}
	b := MachineState{"same": "1", "changed": "2", "new": "1"}
	if got := strings.Join(a.Differences(b), ","); got != "changed,gone,new" {
		t.Errorf("got %s", got)
	}
}

// --- GitLab ------------------------------------------------------------------------------

func TestAnImageTagIsTakenApart(t *testing.T) {
	got, err := ParseImageRef("172.168.1.234:5050/fitt/ftcdv-uat/crn-demo-x:v3")
	if err != nil {
		t.Fatal(err)
	}
	want := ImageRef{API: "http://172.168.1.234", Project: "fitt/ftcdv-uat", Repository: "fitt/ftcdv-uat/crn-demo-x", Tag: "v3"}
	if got != want {
		t.Errorf("got %+v", got)
	}
	for _, bad := range []string{"no-slash", "host/image", "host/a/b"} {
		if _, err := ParseImageRef(bad); err == nil {
			t.Errorf("read %q", bad)
		}
	}
}

// fakeGitLab answers a list the way GitLab does: a page at a time, X-Next-Page always there and
// empty on the last page. Keys are "<path>?page=<n>"; a tag's detail gives a digest of its name.
func fakeGitLab(t *testing.T, pages map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("tags") {
			t.Errorf("tags carried on the repository list, which can be cut short: %s", r.URL)
		}
		if !strings.HasPrefix(r.URL.EscapedPath(), "/api/v4/projects/fitt%2Fx/") {
			t.Errorf("the project is not one path segment: %s", r.URL.EscapedPath())
		}
		page := r.URL.Query().Get("page")
		if body, ok := pages[r.URL.Path+"?page="+page]; ok {
			n, _ := strconv.Atoi(page)
			next := ""
			if _, more := pages[r.URL.Path+"?page="+strconv.Itoa(n+1)]; more {
				next = strconv.Itoa(n + 1)
			}
			w.Header()["X-Next-Page"] = []string{next}
			fmt.Fprint(w, body)
			return
		}
		if i := strings.LastIndex(r.URL.Path, "/tags/"); i >= 0 {
			fmt.Fprintf(w, `{"digest":"sha256:%s"}`, r.URL.Path[i+len("/tags/"):])
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

const testRepositories = "/api/v4/projects/fitt/x/registry/repositories"

func TestEveryTagOfEveryImageIsRead(t *testing.T) {
	srv := fakeGitLab(t, map[string]string{
		testRepositories + "?page=1":        `[{"id":1,"path":"fitt/x/a"}]`,
		testRepositories + "?page=2":        `[{"id":2,"path":"fitt/x/b"}]`,
		testRepositories + "/1/tags?page=1": `[{"name":"v1"}]`,
		testRepositories + "/1/tags?page=2": `[{"name":"v2"}]`,
		testRepositories + "/2/tags?page=1": `[]`,
	})
	defer srv.Close()
	got, err := ReadRegistryTags(context.Background(), ImageRef{API: srv.URL, Project: "fitt/x"})
	if err != nil {
		t.Fatal(err)
	}
	if got["fitt/x/a"]["v1"] != "sha256:v1" || got["fitt/x/a"]["v2"] != "sha256:v2" {
		t.Errorf("got %v", got)
	}
	if tags, ok := got["fitt/x/b"]; !ok || len(tags) != 0 {
		t.Errorf("an image with no tags must still be listed: %v", got)
	}
}

// Fittcore's finding on r6: a list carried inside another list can be cut short.
func TestAnImageWithMoreTagsThanOnePageIsReadToTheEnd(t *testing.T) {
	pages := map[string]string{testRepositories + "?page=1": `[{"id":7,"path":"fitt/x/many"}]`}
	const total = 250
	for page := 0; page*100 < total; page++ {
		var names []string
		for i := page * 100; i < min((page+1)*100, total); i++ {
			names = append(names, fmt.Sprintf(`{"name":"t%d"}`, i))
		}
		pages[fmt.Sprintf("%s/7/tags?page=%d", testRepositories, page+1)] = "[" + strings.Join(names, ",") + "]"
	}
	srv := fakeGitLab(t, pages)
	defer srv.Close()
	got, err := ReadRegistryTags(context.Background(), ImageRef{API: srv.URL, Project: "fitt/x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got["fitt/x/many"]) != total {
		t.Errorf("read %d tags of %d", len(got["fitt/x/many"]), total)
	}
	if got["fitt/x/many"]["t249"] != "sha256:t249" {
		t.Errorf("the last tag: %q", got["fitt/x/many"]["t249"])
	}
}

// A list that repeats itself cannot be trusted to be the whole list.
func TestARepeatInTheListIsAnError(t *testing.T) {
	cases := map[string]map[string]string{
		"the same image id twice": {
			testRepositories + "?page=1":        `[{"id":1,"path":"fitt/x/a"},{"id":1,"path":"fitt/x/b"}]`,
			testRepositories + "/1/tags?page=1": `[]`,
		},
		"the same image path twice": {
			testRepositories + "?page=1":        `[{"id":1,"path":"fitt/x/a"},{"id":2,"path":"fitt/x/a"}]`,
			testRepositories + "/1/tags?page=1": `[]`,
			testRepositories + "/2/tags?page=1": `[]`,
		},
		"the same image on the next page": {
			testRepositories + "?page=1":        `[{"id":1,"path":"fitt/x/a"}]`,
			testRepositories + "?page=2":        `[{"id":1,"path":"fitt/x/a"}]`,
			testRepositories + "/1/tags?page=1": `[]`,
		},
		"the same tag twice": {
			testRepositories + "?page=1":        `[{"id":1,"path":"fitt/x/a"}]`,
			testRepositories + "/1/tags?page=1": `[{"name":"v1"},{"name":"v1"}]`,
		},
		"the same tag on the next page": {
			testRepositories + "?page=1":        `[{"id":1,"path":"fitt/x/a"}]`,
			testRepositories + "/1/tags?page=1": `[{"name":"v1"}]`,
			testRepositories + "/1/tags?page=2": `[{"name":"v1"}]`,
		},
	}
	for name, pages := range cases {
		t.Run(name, func(t *testing.T) {
			srv := fakeGitLab(t, pages)
			defer srv.Close()
			_, err := ReadRegistryTags(context.Background(), ImageRef{API: srv.URL, Project: "fitt/x"})
			if err == nil || !strings.Contains(err.Error(), "twice") {
				t.Errorf("err = %v", err)
			}
		})
	}
}

// An answer it cannot read must not read as fewer tags, and neither must an answer that does
// not say it is the last.
func TestATagListItCannotReadIsAnError(t *testing.T) {
	list := func(next string, set bool, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			if set {
				w.Header()["X-Next-Page"] = []string{next}
			}
			fmt.Fprint(w, body)
		}
	}
	oneImage := func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/repositories") {
			w.Header()["X-Next-Page"] = []string{""}
			fmt.Fprint(w, `[{"id":1,"path":"a"}]`)
			return true
		}
		return false
	}
	// anImage lists these images, and each has no tags: only the image can be what is wrong.
	anImage := func(images string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header()["X-Next-Page"] = []string{""}
			if strings.HasSuffix(r.URL.Path, "/repositories") {
				fmt.Fprint(w, images)
				return
			}
			fmt.Fprint(w, "[]")
		}
	}
	cases := map[string]http.HandlerFunc{
		"refused": func(w http.ResponseWriter, _ *http.Request) {
			w.Header()["X-Next-Page"] = []string{""}
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "[]") // reads as no images at all, if the status is not looked at
		},
		"not json":                    list("", true, "<html>"),
		"it does not say if it ended": list("", false, "[]"),
		"the next page is not a page": list("soon", true, `[{"id":1,"path":"a"}]`),
		"the next page goes back":     list("1", true, `[{"id":1,"path":"a"}]`),
		"a page with nothing on it, and more to come": func(w http.ResponseWriter, r *http.Request) {
			next := ""
			if r.URL.Query().Get("page") == "1" {
				next = "2"
			}
			w.Header()["X-Next-Page"] = []string{next}
			fmt.Fprint(w, "[]")
		},
		"an image with no id":   anImage(`[{"path":"a"}]`),
		"an image with no path": anImage(`[{"id":1}]`),
		"the total says there are more": func(w http.ResponseWriter, _ *http.Request) {
			w.Header()["X-Next-Page"] = []string{""}
			w.Header().Set("X-Total", "9")
			fmt.Fprint(w, "[]")
		},
		"a tag list it cannot read": func(w http.ResponseWriter, r *http.Request) {
			if !oneImage(w, r) {
				w.WriteHeader(http.StatusInternalServerError)
			}
		},
		"a tag with no name": func(w http.ResponseWriter, r *http.Request) {
			if oneImage(w, r) {
				return
			}
			if strings.HasSuffix(r.URL.Path, "/tags") {
				w.Header()["X-Next-Page"] = []string{""}
				fmt.Fprint(w, `[{}]`)
				return
			}
			fmt.Fprint(w, `{"digest":"sha256:x"}`) // only the name can be what is wrong
		},
		"a tag it cannot read": func(w http.ResponseWriter, r *http.Request) {
			if oneImage(w, r) {
				return
			}
			if strings.HasSuffix(r.URL.Path, "/tags") {
				w.Header()["X-Next-Page"] = []string{""}
				fmt.Fprint(w, `[{"name":"v1"}]`)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
		},
		"a tag with no digest": func(w http.ResponseWriter, r *http.Request) {
			if oneImage(w, r) {
				return
			}
			if strings.HasSuffix(r.URL.Path, "/tags") {
				w.Header()["X-Next-Page"] = []string{""}
				fmt.Fprint(w, `[{"name":"v1"}]`)
				return
			}
			fmt.Fprint(w, `{}`)
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			if _, err := ReadRegistryTags(context.Background(), ImageRef{API: srv.URL, Project: "p/q"}); err == nil {
				t.Error("no error")
			}
		})
	}
}

func TestOnlyThisBuildsTagMayBeNew(t *testing.T) {
	tags := func() RegistryTags {
		return RegistryTags{"p/a": {"v1": "1", "v2": "2"}, "p/b": {"v1": "b1"}}
	}
	cases := []struct {
		name   string
		change func(RegistryTags)
		digest string
		want   string // "" passes
	}{
		{"only the new tag", func(r RegistryTags) { r["p/a"]["v3"] = "d" }, "d", ""},
		{"nothing pushed and nothing changed", func(RegistryTags) {}, "", ""},
		{"the first tag of a new image", func(r RegistryTags) { delete(r["p/a"], "x"); r["p/n"] = map[string]string{"v3": "d"} }, "", "new image p/n"},
		{"the tag is missing", func(RegistryTags) {}, "d", "is not there"},
		{"the tag is another digest", func(r RegistryTags) { r["p/a"]["v3"] = "other" }, "d", "not the pushed digest"},
		{"a tag appeared with nothing pushed", func(r RegistryTags) { r["p/a"]["v3"] = "d" }, "", "new tag p/a:v3"},
		{"a tag moved", func(r RegistryTags) { r["p/a"]["v3"] = "d"; r["p/a"]["v1"] = "x" }, "d", "tag changed p/a:v1"},
		{"a tag went", func(r RegistryTags) { r["p/a"]["v3"] = "d"; delete(r["p/a"], "v2") }, "d", "tag gone p/a:v2"},
		{"another image moved", func(r RegistryTags) { r["p/a"]["v3"] = "d"; r["p/b"]["v1"] = "x" }, "d", "tag changed p/b:v1"},
		{"another image went", func(r RegistryTags) { r["p/a"]["v3"] = "d"; delete(r, "p/b") }, "d", "image gone p/b"},
		{"a tag on another image", func(r RegistryTags) { r["p/a"]["v3"] = "d"; r["p/b"]["v3"] = "d" }, "d", "new tag p/b:v3"},
		{"another tag on this image", func(r RegistryTags) { r["p/a"]["v3"] = "d"; r["p/a"]["latest"] = "d" }, "d", "new tag p/a:latest"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			after := tags()
			c.change(after)
			err := CompareRegistryTags(tags(), after, "p/a", "v3", c.digest)
			if c.want == "" && err != nil {
				t.Errorf("err = %v", err)
			}
			if c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
	t.Run("a new image holding this build's tag", func(t *testing.T) {
		before := RegistryTags{"p/b": {"v1": "b1"}}
		after := RegistryTags{"p/b": {"v1": "b1"}, "p/a": {"v3": "d"}}
		if err := CompareRegistryTags(before, after, "p/a", "v3", "d"); err != nil {
			t.Errorf("err = %v", err)
		}
	})
}

func jwtWith(actions ...string) string {
	quoted := `"` + strings.Join(actions, `","`) + `"`
	body := base64.RawURLEncoding.EncodeToString([]byte(`{"access":[{"type":"repository","actions":[` + quoted + `]}]}`))
	return `{"token":"h.` + body + `.s"}`
}

func TestGitLabIsAskedWhetherALoginCanStillPush(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		alive  bool
		isErr  bool
	}{
		"it may push":           {200, jwtWith("pull", "push"), true, false},
		"it may only pull":      {200, jwtWith("pull"), false, false},
		"revoked":               {401, "", false, false},
		"forbidden":             {403, "", false, false},
		"gitlab is down":        {502, "", false, true},
		"not json":              {200, "<html>", false, true},
		"not a token":           {200, `{"token":"x"}`, false, true},
		"a token it can't read": {200, `{"token":"a.%%%.c"}`, false, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, password, ok := r.BasicAuth()
				if !ok || user != "u" || password != "secret-token" {
					t.Errorf("the login was not sent: %v %q", ok, user)
				}
				if got := r.URL.Query().Get("scope"); got != "repository:p/a:push,pull" {
					t.Errorf("scope %q", got)
				}
				if strings.Contains(r.URL.RawQuery, "secret-token") {
					t.Error("the token is in the address")
				}
				w.WriteHeader(c.status)
				fmt.Fprint(w, c.body)
			}))
			defer srv.Close()
			alive, err := RegistryLoginCanPush(context.Background(), ImageRef{API: srv.URL, Repository: "p/a"}, "u:secret-token")
			if alive != c.alive || (err != nil) != c.isErr {
				t.Errorf("alive = %v, err = %v", alive, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret-token") {
				t.Errorf("the token is in the error: %v", err)
			}
		})
	}
	t.Run("no answer", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		address := srv.URL
		srv.Close()
		if _, err := RegistryLoginCanPush(context.Background(), ImageRef{API: address, Repository: "p/a"}, "u:t"); err == nil {
			t.Error("no error")
		}
	})
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

// Fittcore's finding on r6: a variable left in the environment can point docker at another
// daemon, context or builder, whatever config it is given.
func TestADockerCommandKeepsNothingThatPointsDockerElsewhere(t *testing.T) {
	for _, name := range []string{"DOCKER_CONFIG", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH", "BUILDX_BUILDER", "BUILDKIT_HOST", "BUILDX_CONFIG", "COMPOSE_FILE"} {
		t.Setenv(name, "tcp://somewhere-else:2375")
	}
	t.Setenv("KEEP_ME", "yes")
	env := dockerCommand(WithDockerConfig(context.Background(), "/the/dir"), "version").Env
	configs := 0
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		switch {
		case value == "tcp://somewhere-else:2375":
			t.Errorf("%s reached docker", name)
		case name == "DOCKER_CONFIG":
			configs++
		}
	}
	if configs != 1 || !slices.Contains(env, "DOCKER_CONFIG=/the/dir") {
		t.Errorf("want one DOCKER_CONFIG, the given one: %d in %v", configs, env)
	}
	if !slices.Contains(env, "KEEP_ME=yes") {
		t.Error("the rest of the environment did not reach docker")
	}
}

// The operator's script and the build agree on the file, and the token stays off the screen.
func TestTheOperatorsScriptLeavesWhatTheBuildAccepts(t *testing.T) {
	const project = "efa0ff04-7885-4d86-a4ff-4829cccd2d9f"
	const job = "7185f870-0000-4000-8000-000000000001"
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

	status := func(want string) {
		t.Helper()
		if out, err := run("", "status", project); err != nil || strings.TrimSpace(out) != want {
			t.Errorf("status = %q, %v, want %s", out, err, want)
		}
	}
	status("NONE")

	// Nobody is waiting: nothing is left, and the operator is told to revoke.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := run(login+"\n", "receive", project, job)
	if err == nil || !strings.Contains(out, "REVOKE") {
		t.Errorf("with no READY: %v, %s", err, out)
	}
	if _, err := os.Lstat(path); err == nil {
		t.Error("a token was left with nobody waiting")
	}
	if strings.Contains(out, "the-secret-value") {
		t.Errorf("the token was printed: %s", out)
	}

	if err := os.WriteFile(filepath.Join(dir, "READY"), []byte(job+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status("READY")
	// Another job's pause: nothing is left.
	out, err = run(login+"\n", "receive", project, "7185f870-0000-4000-8000-000000000002")
	if err == nil || !strings.Contains(out, "REVOKE") {
		t.Errorf("for another job: %v, %s", err, out)
	}
	if _, err := os.Lstat(path); err == nil {
		t.Error("a token was left for another job")
	}
	out, err = run(login+"\n", "receive", project, job)
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if strings.Contains(out, "the-secret-value") {
		t.Errorf("the token was printed: %s", out)
	}
	// Fittcore's finding on r6: not even a fingerprint of it. The token's id says which one.
	if strings.Contains(strings.ToLower(out), "sha256") {
		t.Errorf("a fingerprint of the token was printed: %s", out)
	}
	if !strings.Contains(out, job) {
		t.Errorf("it does not say which job: %s", out)
	}
	raw, err := ReadPushCredential(path, time.Time{}, "172.168.1.234:5050")
	if err != nil {
		t.Fatalf("the build refuses the file: %v", err)
	}
	if !strings.Contains(string(raw), "Z2l0bGFi") {
		t.Errorf("the file: %s", raw)
	}

	if err := os.WriteFile(filepath.Join(dir, "PUSHED"), []byte(job+" sha256:x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status("PUSHED")

	for _, bad := range []string{"", "no-separator\n", ":\n"} {
		if out, err := run(bad, "receive", project, job); err == nil {
			t.Errorf("took %q: %s", bad, out)
		}
	}
	if out, err := run("", "receive", "not-a-project", job); err == nil {
		t.Errorf("took a bad id: %s", out)
	}
	if out, err := run(login+"\n", "receive", project); err == nil {
		t.Errorf("took no job: %s", out)
	}
}

// GitLab itself, on a machine the firewall lets through. It only reads.
func TestTheRealGitLabAnswersAsTheCodeExpects(t *testing.T) {
	if os.Getenv("CRN_TEST_REAL_GITLAB") == "" {
		t.Skip("set CRN_TEST_REAL_GITLAB on a machine that can reach GitLab")
	}
	ctx := context.Background()
	ref, err := ParseImageRef("172.168.1.234:5050/fitt/ftcdv-uat/crn-demo-ntc-film-job-efa0ff04:v3")
	if err != nil {
		t.Fatal(err)
	}
	tags, err := ReadRegistryTags(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	for repo, names := range tags {
		t.Logf("%s: %v", repo, names)
	}
	if got := tags[ref.Repository]["v2"]; got != "sha256:3fe6d67110c91d4ca3ec6e2616026e6372ed8ddba2cc70095846f931ef993b2c" {
		t.Errorf("v2 reads as %q", got)
	}
	if err := CompareRegistryTags(tags, tags, ref.Repository, ref.Tag, ""); err != nil {
		t.Errorf("the same list differs: %v", err)
	}
	if alive, err := RegistryLoginCanPush(ctx, ref, "nobody:nothing"); err != nil || alive {
		t.Errorf("a bad login: alive %v, err %v", alive, err)
	}
}

// The real machine, looked at twice: a build must not fail on the machine's own noise.
func TestTheRealMachineHoldsStill(t *testing.T) {
	if os.Getenv("CRN_TEST_REAL_MACHINE") == "" {
		t.Skip("set CRN_TEST_REAL_MACHINE on the build machine")
	}
	ctx := context.Background()
	before, err := CaptureMachineState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(45 * time.Second)
	now, err := CaptureMachineState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if differ := before.Differences(now); len(differ) != 0 {
		t.Errorf("two looks 45 seconds apart differ: %v", differ)
	}
	t.Logf("%d things compared", len(before))
}

// docker itself, on a machine that has it: a registry on this machine that wants a login,
// the plugin from the config directory, and no use of the home directory. Nothing leaves
// the machine.
func TestDockerUsesOnlyTheConfigItIsGivenForReal(t *testing.T) {
	if os.Getenv("CRN_TEST_REAL_DOCKER") == "" {
		t.Skip("set CRN_TEST_REAL_DOCKER: this runs a registry container")
	}
	ctx := context.Background()
	logger := slog.Default()
	auth := t.TempDir()
	if err := os.WriteFile(filepath.Join(auth, "htpasswd"), []byte(testRegistryHtpasswd+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// On the docker daemon's own network, where its 127.0.0.1 is the registry and plain http is
	// allowed. Nothing outside Docker's machine can reach it.
	name := fmt.Sprintf("crn-test-registry-%d", os.Getpid())
	port := fmt.Sprint(45000 + os.Getpid()%5000)
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name, "--network", "host",
		"-v", auth+":/auth:ro", "-e", "REGISTRY_HTTP_ADDR=127.0.0.1:"+port, "-e", "REGISTRY_AUTH=htpasswd", "-e", "REGISTRY_AUTH_HTPASSWD_REALM=test",
		"-e", "REGISTRY_AUTH_HTPASSWD_PATH=/auth/htpasswd", "registry:2").CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", "-v", name).Run() })
	for i := 0; ; i++ {
		logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
		if strings.Contains(string(logs), "listening on") {
			break
		}
		if i == 100 {
			t.Fatalf("the registry did not start:\n%s", logs)
		}
		time.Sleep(200 * time.Millisecond)
	}
	host := "127.0.0.1:" + port
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
	// And what could be left in CRN's environment: a daemon and a context that do not exist.
	// docker fails at once if it obeys either.
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	t.Setenv("DOCKER_CONTEXT", "crn-test-no-such-context")

	build := t.TempDir()
	writeTree(t, build, "Dockerfile", "FROM scratch\nCOPY a.txt /a.txt\n")
	writeTree(t, build, "a.txt", fmt.Sprintf("push window %d\n", time.Now().UnixNano()))

	noLogin, err := NewBuildDockerConfig(host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(noLogin) })
	bare := WithDockerConfig(ctx, noLogin)
	if err := BuildImage(bare, build, "Dockerfile", tag, map[string]string{"t": "1"}, logger); err != nil {
		t.Fatalf("docker build with the given config: %v", err)
	}
	if raw, err := os.ReadFile(marks); err == nil {
		t.Errorf("the buildx in the home directory ran: %s", raw)
	}
	refused := func(ctx context.Context, when string) {
		t.Helper()
		err := PushImageOnce(ctx, tag, logger)
		if err == nil {
			t.Errorf("%s: the push worked", when)
			return
		}
		// Only a refusal shows it had no login. A network failure would show nothing.
		if text := err.Error(); !strings.Contains(text, "no basic auth") && !strings.Contains(text, "unauthorized") && !strings.Contains(text, "authentication required") {
			t.Errorf("%s: it failed for another reason: %v", when, err)
		}
	}
	refused(bare, "with no login, and a working one in the home directory")

	withLogin, err := NewPushDockerConfig([]byte(`{"auths":{"` + host + `":{"auth":"dGVzdC11c2VyOnRlc3QtcGFzcw=="}}}`))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(withLogin) })
	given := WithDockerConfig(ctx, withLogin)
	if err := PushImageOnce(given, tag, logger); err != nil {
		t.Fatalf("the push with the given login: %v", err)
	}
	digest, err := PushedDigest(given, tag, logger)
	if err != nil || !strings.HasPrefix(digest, "sha256:") {
		t.Errorf("digest %q, %v", digest, err)
	}
	t.Logf("pushed %s", digest)
	if raw, err := os.ReadFile(marks); err == nil {
		t.Errorf("the buildx in the home directory ran during the push: %s", raw)
	}
	if err := RemovePushDockerConfig(withLogin); err != nil {
		t.Fatal(err)
	}
	refused(bare, "after the login was removed")
}
