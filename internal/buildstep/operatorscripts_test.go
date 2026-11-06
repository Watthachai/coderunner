package buildstep

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	testProject = "efa0ff04-7885-4d86-a4ff-4829cccd2d9f"
	testJob     = "7185f870-0000-4000-8000-000000000001"
	otherJob    = "7185f870-0000-4000-8000-000000000002"
)

func scriptPath(t *testing.T, relative string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("../..", relative))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func writeShim(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/bash\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// machineFake is the MacMini push-window.sh looks at: every program it asks prints a file.
type machineFake struct {
	t     *testing.T
	home  string
	state string
	bin   string
}

func newMachineFake(t *testing.T) *machineFake {
	t.Helper()
	m := &machineFake{t: t, home: t.TempDir(), state: t.TempDir(), bin: t.TempDir()}
	writeShim(t, m.bin, "launchctl", `printf 'PID Status Label\n'; cat "`+m.state+`/launchd"`)
	writeShim(t, m.bin, "crontab", `cat "`+m.state+`/cron"`)
	writeShim(t, m.bin, "docker", `[ ! -f "`+m.state+`/docker-down" ] || exit 1; cat "`+m.state+`/containers"`)
	writeShim(t, m.bin, "ps", `cat "`+m.state+`/processes"`)
	// Like pgrep -fl: the pattern is the script's own, against each process's command line.
	writeShim(t, m.bin, "pgrep", `found=1; while IFS= read -r line; do if printf '%s\n' "$line" | grep -qE "$2"; then echo "999 $line"; found=0; fi; done < "`+m.state+`/sessions"; exit $found`)
	m.set("launchd", "- 0 com.fitt.crn-server\n- 0 com.apple.something\n")
	m.set("cron", "")
	m.set("containers", "abc postgres crn-postgres\n")
	m.set("processes", fmt.Sprintf("1 %d /Users/macagents/bin/crn-server\n1 %d /System/Library/x\n77 %d /bin/zsh\n", os.Getuid(), os.Getuid(), os.Getuid()))
	m.set("sessions", "")
	if err := os.MkdirAll(m.pushDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(m.home, "Library", "LaunchAgents"), 0o700); err != nil {
		t.Fatal(err)
	}
	return m
}

func (m *machineFake) pushDir() string {
	return filepath.Join(m.home, "crn-secrets", "push", testProject)
}

func (m *machineFake) set(name, value string) {
	m.t.Helper()
	if err := os.WriteFile(filepath.Join(m.state, name), []byte(value), 0o600); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machineFake) run(stdin string, env []string, args ...string) (string, error) {
	cmd := exec.Command("/bin/bash", append([]string{scriptPath(m.t, "deploy/macmini/push-window.sh")}, args...)...)
	cmd.Env = append([]string{"HOME=" + m.home, "CRN_PUSH_WINDOW_PATH=" + m.bin + ":/opt/homebrew/bin:/usr/bin:/bin"}, env...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// baseline records the machine and returns the hash the laptop would keep.
func (m *machineFake) baseline() string {
	m.t.Helper()
	out, err := m.run("", nil, "baseline")
	if err != nil {
		m.t.Fatalf("baseline: %v: %s", err, out)
	}
	found := regexp.MustCompile(`sha256 ([0-9a-f]{64})\n$`).FindStringSubmatch(out)
	if found == nil {
		m.t.Fatalf("baseline does not end with its sha256: %s", out)
	}
	return found[1]
}

// A check that only reports is not a check: Fittcore's finding on r6.
func TestTheOperatorsCheckFailsWhenAnythingDiffers(t *testing.T) {
	cases := map[string]struct {
		change func(m *machineFake)
		want   string
	}{
		"a new launch agent": {func(m *machineFake) {
			if err := os.WriteFile(filepath.Join(m.home, "Library", "LaunchAgents", "x.plist"), nil, 0o600); err != nil {
				m.t.Fatal(err)
			}
		}, "x.plist"},
		"a new launchd label": {func(m *machineFake) { m.set("launchd", "- 0 com.fitt.crn-server\n- 0 com.evil.agent\n") }, "com.evil.agent"},
		"a label gone":        {func(m *machineFake) { m.set("launchd", "- 0 com.apple.something\n") }, "com.fitt.crn-server"},
		"a cron line":         {func(m *machineFake) { m.set("cron", "* * * * * /tmp/x\n") }, "/tmp/x"},
		"a new container":     {func(m *machineFake) { m.set("containers", "abc postgres crn-postgres\ndef evil evil\n") }, "evil"},
		"a new orphan": {func(m *machineFake) {
			m.set("processes", fmt.Sprintf("1 %d /Users/macagents/bin/crn-server\n1 %d /tmp/evil\n", os.Getuid(), os.Getuid()))
		}, "/tmp/evil"},
		"docker cannot be read": {func(m *machineFake) { m.set("docker-down", "") }, "UNREADABLE docker"},
		"a Claude session":      {func(m *machineFake) { m.set("sessions", "claude --resume\n") }, "an agent session is open"},
		"Claude run by node":    {func(m *machineFake) { m.set("sessions", "node /x/@anthropic-ai/claude-code/cli.js\n") }, "an agent session is open"},
		"another agent session": {func(m *machineFake) { m.set("sessions", "/opt/bin/codex exec\n") }, "an agent session is open"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			m := newMachineFake(t)
			hash := m.baseline()
			if err := os.WriteFile(filepath.Join(m.pushDir(), "READY"), []byte(testJob+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			env := []string{"CRN_BASELINE_SHA256=" + hash}
			if out, err := m.run("", env, "check", testProject, testJob); err != nil || !strings.Contains(out, "CHECK PASSED") {
				t.Fatalf("before the change: %v: %s", err, out)
			}
			c.change(m)
			out, err := m.run("", env, "check", testProject, testJob)
			if err == nil {
				t.Errorf("exit 0: %s", out)
			}
			if !strings.Contains(out, c.want) || !strings.Contains(out, "CHECK FAILED") || strings.Contains(out, "CHECK PASSED") {
				t.Errorf("want %q: %s", c.want, out)
			}
		})
	}
}

// What macOS and Docker Desktop do on their own must not fail it, or it gets ignored.
func TestTheOperatorsCheckIgnoresTheSystemsOwnNoise(t *testing.T) {
	m := newMachineFake(t)
	hash := m.baseline()
	if err := os.WriteFile(filepath.Join(m.pushDir(), "READY"), []byte(testJob+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.set("launchd", "- 0 com.fitt.crn-server\n- 0 com.apple.mdworker.shared.0500\n- 0 application.com.apple.Terminal.1\n")
	m.set("processes", fmt.Sprintf("1 %d /Users/macagents/bin/crn-server\n1 %d /usr/libexec/new\n1 %d /Applications/Docker.app/x\n", os.Getuid(), os.Getuid(), os.Getuid()))
	m.set("sessions", "vim notes-about-claude.txt\n")
	if out, err := m.run("", []string{"CRN_BASELINE_SHA256=" + hash}, "check", testProject, testJob); err != nil {
		t.Errorf("%v: %s", err, out)
	}
}

func TestTheOperatorsCheckFailsWhenTheBaselineOrREADYDoNotMatch(t *testing.T) {
	type setup struct {
		m    *machineFake
		hash string
	}
	ready := func(s setup, job string) {
		if err := os.WriteFile(filepath.Join(s.m.pushDir(), "READY"), []byte(job+"\n"), 0o600); err != nil {
			s.m.t.Fatal(err)
		}
	}
	baselineFile := func(s setup) string { return filepath.Join(s.m.home, "crn-secrets", "push", "baseline.txt") }
	cases := map[string]struct {
		arrange func(s setup) []string
		want    string
	}{
		"nobody is waiting": {func(s setup) []string { return []string{"CRN_BASELINE_SHA256=" + s.hash} }, "not waiting"},
		"another job waits": {func(s setup) []string {
			ready(s, otherJob)
			return []string{"CRN_BASELINE_SHA256=" + s.hash}
		}, "another job"},
		"READY is a link": {func(s setup) []string {
			target := filepath.Join(s.m.state, "ready")
			if err := os.WriteFile(target, []byte(testJob+"\n"), 0o600); err != nil {
				s.m.t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(s.m.pushDir(), "READY")); err != nil {
				s.m.t.Fatal(err)
			}
			return []string{"CRN_BASELINE_SHA256=" + s.hash}
		}, "not waiting"},
		"no baseline": {func(s setup) []string {
			ready(s, testJob)
			if err := os.Remove(baselineFile(s)); err != nil {
				s.m.t.Fatal(err)
			}
			return []string{"CRN_BASELINE_SHA256=" + s.hash}
		}, "no baseline"},
		"the baseline was rewritten": {func(s setup) []string {
			ready(s, testJob)
			if err := os.WriteFile(baselineFile(s), []byte("rewritten\n"), 0o600); err != nil {
				s.m.t.Fatal(err)
			}
			return []string{"CRN_BASELINE_SHA256=" + s.hash}
		}, "not the one recorded"},
		"no hash from the laptop": {func(s setup) []string {
			ready(s, testJob)
			return nil
		}, "not the one recorded"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			m := newMachineFake(t)
			s := setup{m: m, hash: m.baseline()}
			env := c.arrange(s)
			out, err := m.run("", env, "check", testProject, testJob)
			if err == nil {
				t.Errorf("exit 0: %s", out)
			}
			if !strings.Contains(out, c.want) || !strings.Contains(out, "CHECK FAILED") {
				t.Errorf("want %q: %s", c.want, out)
			}
		})
	}
	t.Run("no job named", func(t *testing.T) {
		m := newMachineFake(t)
		if out, err := m.run("", nil, "check", testProject); err == nil {
			t.Errorf("exit 0: %s", out)
		}
	})
}

func TestABaselineOfAMachineItCannotReadIsNotSaved(t *testing.T) {
	m := newMachineFake(t)
	m.set("docker-down", "")
	if out, err := m.run("", nil, "baseline"); err == nil {
		t.Errorf("exit 0: %s", out)
	}
	if _, err := os.Stat(filepath.Join(m.home, "crn-secrets", "push", "baseline.txt")); err == nil {
		t.Error("a baseline was saved")
	}
}

// registryFake is GitLab and the registry as push-window.sh's scope and dead see them.
type registryFake struct {
	images    []string          // what the project's image list says
	grants    map[string]string // image to "pull,push"; absent grants nothing
	refused   bool              // /jwt/auth refuses the login
	jwtStatus int               // /jwt/auth answers this instead
	opened    int               // what the registry answers to opening a push
	noHeader  bool              // the image list does not say whether it ended
	openings  []string
	logins    []string
}

func (f *registryFake) serve(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/registry/repositories"):
			if !f.noHeader {
				w.Header()["X-Next-Page"] = []string{""}
			}
			var list []map[string]string
			for _, image := range f.images {
				list = append(list, map[string]string{"path": image})
			}
			_ = json.NewEncoder(w).Encode(list)
		case r.URL.Path == "/jwt/auth":
			user, password, _ := r.BasicAuth()
			f.logins = append(f.logins, user+":"+password)
			if f.jwtStatus != 0 {
				w.WriteHeader(f.jwtStatus)
				return
			}
			if f.refused {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			repo := strings.Split(r.URL.Query().Get("scope"), ":")[1]
			var access []map[string]any
			if grant, ok := f.grants[repo]; ok {
				access = append(access, map[string]any{"type": "repository", "name": repo, "actions": strings.Split(grant, ",")})
			}
			claims, _ := json.Marshal(map[string]any{"access": access})
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"})
		case strings.HasPrefix(r.URL.Path, "/v2/") && r.Method == http.MethodPost:
			f.openings = append(f.openings, r.URL.Path)
			w.WriteHeader(f.opened)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

const (
	inNTC    = "fitt/ftcdv-uat/ntc"
	inHello  = "fitt/ftcdv-uat/hello"
	outDemos = "fitt/demos/scope-check"
	aLogin   = "gitlab+deploy-token-9:gldt-the-secret-value"
)

func (f *registryFake) ask(t *testing.T, sub string) (string, error) {
	t.Helper()
	srv := f.serve(t)
	defer srv.Close()
	m := newMachineFake(t)
	return m.run(aLogin+"\n", []string{
		"CRN_GITLAB_URL=" + srv.URL, "CRN_REGISTRY_URL=" + srv.URL, "CRN_SCOPE_PROJECT=fitt/ftcdv-uat",
		"CRN_SCOPE_ALLOWED=" + inNTC + " " + inHello, "CRN_SCOPE_OUTSIDE=" + outDemos,
	}, sub)
}

func goodScope() *registryFake {
	return &registryFake{
		images: []string{inHello, inNTC},
		grants: map[string]string{inNTC: "pull,push", inHello: "pull,push"},
		opened: http.StatusUnauthorized,
	}
}

func TestScopePassesOnlyWhenTheTokenWritesExactlyThisProject(t *testing.T) {
	f := goodScope()
	out, err := f.ask(t, "scope")
	if err != nil || !strings.HasSuffix(strings.TrimSpace(out), "SCOPE PASSED") {
		t.Fatalf("%v: %s", err, out)
	}
	if len(f.openings) != 1 || f.openings[0] != "/v2/"+outDemos+"/blobs/uploads/" {
		t.Errorf("the registry was not really asked about the image outside: %v", f.openings)
	}
	if len(f.logins) == 0 || f.logins[0] != aLogin {
		t.Errorf("the login did not reach GitLab: %v", f.logins)
	}
	if strings.Contains(out, "the-secret-value") {
		t.Errorf("the token was printed: %s", out)
	}

	cases := map[string]func(f *registryFake){
		"pull only on an image":      func(f *registryFake) { f.grants[inHello] = "pull" },
		"no access on an image":      func(f *registryFake) { delete(f.grants, inNTC) },
		"more than pull and push":    func(f *registryFake) { f.grants[inNTC] = "delete,pull,push" },
		"push outside the project":   func(f *registryFake) { f.grants[outDemos] = "pull,push" },
		"the registry opens a push":  func(f *registryFake) { f.opened = http.StatusAccepted },
		"the registry gives nothing": func(f *registryFake) { f.opened = http.StatusInternalServerError },
		"the project has another image": func(f *registryFake) {
			f.images = append(f.images, "fitt/ftcdv-uat/new")
		},
		"the project lost an image": func(f *registryFake) { f.images = []string{inNTC} },
		"the list does not end":     func(f *registryFake) { f.noHeader = true },
		"the login is refused":      func(f *registryFake) { f.refused = true },
		"GitLab gives no answer":    func(f *registryFake) { f.jwtStatus = http.StatusBadGateway },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := goodScope()
			change(f)
			out, err := f.ask(t, "scope")
			if err == nil {
				t.Errorf("exit 0: %s", out)
			}
			if !strings.Contains(out, "SCOPE FAILED") || strings.Contains(out, "SCOPE PASSED") {
				t.Errorf("no failure said: %s", out)
			}
			if strings.Contains(out, "the-secret-value") {
				t.Errorf("the token was printed: %s", out)
			}
		})
	}
	t.Run("push granted outside is not then tried", func(t *testing.T) {
		f := goodScope()
		f.grants[outDemos] = "pull,push"
		if _, err := f.ask(t, "scope"); err == nil {
			t.Error("exit 0")
		}
		if len(f.openings) != 0 {
			t.Errorf("it opened a push with a token that holds push: %v", f.openings)
		}
	})
}

func TestDeadPassesOnlyWhenTheTokenCannotPush(t *testing.T) {
	for name, f := range map[string]*registryFake{
		"the login is refused": {refused: true},
		"no push is granted":   {grants: map[string]string{inNTC: "pull"}},
	} {
		t.Run(name, func(t *testing.T) {
			if out, err := f.ask(t, "dead"); err != nil || !strings.Contains(out, "DEAD PASSED") {
				t.Errorf("%v: %s", err, out)
			}
		})
	}
	for name, f := range map[string]*registryFake{
		"it can still push":      {grants: map[string]string{inHello: "pull,push"}},
		"GitLab gives no answer": {jwtStatus: http.StatusBadGateway},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := f.ask(t, "dead")
			if err == nil || !strings.Contains(out, "DEAD FAILED") {
				t.Errorf("%v: %s", err, out)
			}
			if strings.Contains(out, "the-secret-value") {
				t.Errorf("the token was printed: %s", out)
			}
		})
	}
	t.Run("nothing on stdin", func(t *testing.T) {
		m := newMachineFake(t)
		if out, err := m.run("", nil, "dead"); err == nil {
			t.Errorf("exit 0: %s", out)
		}
	})
}

// sshFake stands in for both hosts for push-window-run.sh. A file named fail-<step> makes that
// step fail, fail-dead-<n> the nth dead check. calls records each step in order.
const sshFake = `
while [ $# -gt 0 ]; do case "$1" in -o|-O) shift 2 ;; *) break ;; esac; done
host=$1; shift; cmd="$*"
[ -n "$cmd" ] || exit 0 # closing the shared connection
note() { echo "$1" >> "$FAKE/calls"; }
fails() { [ -f "$FAKE/fail-$1" ]; }
case "$host" in
root@*)
	case "$cmd" in
	true) fails connect && exit 1; exit 0 ;;
	"date +%s") echo 1; exit 0 ;;
	esac
	script=$(cat)
	case "$script" in
	*CRNREVOKEALL*) step=revoke-unknown ;;
	*CRNREVOKE*) step=revoke ;;
	*CRNSHOW*) step=show ;;
	*CRNTOKEN*) step=create ;;
	*) step=unknown ;;
	esac
	note "$step $cmd"
	fails "$step" && { echo "$step failed" >&2; exit 1; }
	case "$step" in
	create)
		n=$(( $(cat "$FAKE/made" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$FAKE/made"
		fails create-unnamed && { echo "CRNTOKEN gitlab+deploy-token-$n:gldt-secret-$n"; exit 0; }
		fails create-tokenless && { echo "CRNMETA id=$n name=crn-push-window-10$n username=u expires_epoch=2 gitlab_clock=t scopes=s"; exit 0; }
		echo "CRNMETA id=$n name=crn-push-window-10$n username=gitlab+deploy-token-$n created_at=c expires_at=e expires_epoch=2 gitlab_clock=t scopes=read_registry,write_registry"
		echo "CRNTOKEN gitlab+deploy-token-$n:gldt-secret-$n"
		fails create-two && echo "CRNTOKEN gitlab+deploy-token-$n:gldt-secret-other"
		;;
	revoke) echo "CRNREVOKE $cmd" ;;
	revoke-unknown) echo "CRNREVOKEALL EMERGENCY" ;;
	show) echo "CRNSHOW {}" ;;
	esac
	;;
macagents@*)
	case "$cmd" in
	*" baseline") note baseline; fails baseline && { echo "baseline saved: 1 lines, sha256 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"; exit 1; }; echo "baseline saved: 1 lines, sha256 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ;;
	*" status "*) cat "$FAKE/status" ;;
	*" check "*) note "check $cmd"; fails check && { echo CHECK FAILED; exit 1; }; echo CHECK PASSED ;;
	*" receive "*) note "receive $cmd"; cat > "$FAKE/received"; fails receive && exit 1; [ -f "$FAKE/never-pushed" ] || echo PUSHED > "$FAKE/status"; echo left ;;
	*" scope")
		cat > /dev/null
		n=$(( $(cat "$FAKE/scopes" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$FAKE/scopes"
		note scope
		{ fails scope || fails "scope-$n"; } && { echo SCOPE FAILED; exit 1; }
		echo SCOPE PASSED ;;
	*" dead")
		login=$(cat); echo "$login" >> "$FAKE/dead-logins"
		n=$(( $(cat "$FAKE/deads" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$FAKE/deads"
		note "dead"
		{ fails dead || fails "dead-$n"; } && { echo DEAD FAILED; exit 1; }
		echo DEAD PASSED ;;
	esac
	;;
esac
exit 0
`

type windowRun struct {
	out, evidence string
	calls         []string
	err           error
	fake, home    string
}

func runWindow(t *testing.T, failing []string, stdin string, args ...string) windowRun {
	t.Helper()
	bin, fake, home := t.TempDir(), t.TempDir(), t.TempDir()
	writeShim(t, bin, "ssh", sshFake)
	writeShim(t, bin, "sleep", `exit 0`)
	for _, name := range failing {
		file := "fail-" + name
		if name == "never-pushed" {
			file = name
		}
		if err := os.WriteFile(filepath.Join(fake, file), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(fake, "status"), []byte("READY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !contains(failing, "no-record") {
		if err := os.MkdirAll(filepath.Join(home, ".crn-push-window"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, ".crn-push-window", "baseline.sha256"), []byte("abc\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("/bin/bash", append([]string{scriptPath(t, "deploy/macmini/push-window-run.sh")}, args...)...)
	cmd.Env = []string{"HOME=" + home, "FAKE=" + fake, "PATH=" + bin + ":/usr/bin:/bin", "TMPDIR=" + t.TempDir(), "CRN_PUSH_WAIT_ROUNDS=3", "CRN_PUSH_WAIT_SECONDS=1"}
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	run := windowRun{out: string(out), err: err, fake: fake, home: home}
	files, _ := filepath.Glob(filepath.Join(home, "Downloads", "push-window-evidence-*"))
	if len(files) != 1 {
		t.Fatalf("evidence files: %v\n%s", files, out)
	}
	raw, readErr := os.ReadFile(files[0])
	if readErr != nil {
		t.Fatal(readErr)
	}
	run.evidence = string(raw)
	if rawCalls, err := os.ReadFile(filepath.Join(fake, "calls")); err == nil {
		run.calls = strings.Split(strings.TrimSpace(string(rawCalls)), "\n")
	}
	return run
}

func contains(list []string, item string) bool {
	for _, x := range list {
		if x == item {
			return true
		}
	}
	return false
}

// steps is the first word of each call.
func (r windowRun) steps() string {
	var names []string
	for _, c := range r.calls {
		names = append(names, strings.Fields(c)[0])
	}
	return strings.Join(names, " ")
}

func (r windowRun) mustStop(t *testing.T) {
	t.Helper()
	if r.err == nil {
		t.Errorf("exit 0\n%s", r.out)
	}
	if strings.Contains(r.evidence, "PASS "+"2") || regexp.MustCompile(`=== .*: PASS`).MatchString(r.evidence) {
		t.Errorf("it says PASS\n%s", r.evidence)
	}
	if !strings.Contains(r.evidence, "=== STOP") {
		t.Errorf("the last line is not STOP\n%s", r.evidence)
	}
	if strings.Contains(r.evidence+r.out, "gldt-secret") {
		t.Errorf("the token was printed\n%s", r.out)
	}
}

func (r windowRun) mustPass(t *testing.T, marker string) {
	t.Helper()
	if r.err != nil {
		t.Errorf("%v\n%s", r.err, r.out)
	}
	lines := strings.Split(strings.TrimSpace(r.evidence), "\n")
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "=== "+marker) || !strings.Contains(last, ": PASS") {
		t.Errorf("the last line: %q", last)
	}
	if strings.Contains(r.evidence+r.out, "gldt-secret") {
		t.Errorf("the token was printed\n%s", r.out)
	}
}

func TestTheRehearsalPassesOnlyWhenEveryStepHeld(t *testing.T) {
	run := runWindow(t, nil, "", "--rehearse")
	run.mustPass(t, "REHEARSAL")
	// Part 1: revoked while live. Part 2: dead before anyone revokes it.
	const want = "create scope show revoke show dead create scope show show dead revoke show dead"
	if got := run.steps(); got != want {
		t.Errorf("steps:\n got %s\nwant %s", got, want)
	}
	for _, id := range []string{"1", "2"} {
		wanted := "revoke PUSH_TOKEN_ID=" + id + " PUSH_TOKEN_NAME=crn-push-window-10" + id + " bash -s"
		if !contains(run.calls, wanted) {
			t.Errorf("token %s was not revoked by its id and name: %v", id, run.calls)
		}
	}
	if !strings.Contains(run.evidence, "id=1 name=crn-push-window-101") || !strings.Contains(run.evidence, "id=2 name=crn-push-window-102") {
		t.Errorf("the evidence does not name the tokens:\n%s", run.evidence)
	}
	raw, _ := os.ReadFile(filepath.Join(run.fake, "dead-logins"))
	if got := strings.Fields(string(raw)); len(got) != 3 || got[0] != "gitlab+deploy-token-1:gldt-secret-1" || got[1] != "gitlab+deploy-token-2:gldt-secret-2" || got[2] != got[1] {
		t.Errorf("the dead checks were not on the token just made: %v", got)
	}

	revokedAfterMade := map[string]bool{"scope": true, "scope-1": true, "scope-2": true, "show": true, "revoke": true, "dead": true, "dead-1": true, "dead-2": true, "dead-3": true}
	for _, failing := range []string{"connect", "create", "create-unnamed", "create-tokenless", "create-two", "scope", "scope-1", "scope-2", "show", "revoke", "dead", "dead-1", "dead-2", "dead-3"} {
		t.Run(failing, func(t *testing.T) {
			run := runWindow(t, []string{failing}, "", "--rehearse")
			run.mustStop(t)
			if revokedAfterMade[failing] && !strings.Contains(run.steps(), "revoke") {
				t.Errorf("a token it made was not revoked: %s", run.steps())
			}
			if strings.Contains(run.steps(), "revoke-unknown") && failing != "create" && failing != "create-unnamed" {
				t.Errorf("revoked by name when it knew the id: %s", run.steps())
			}
		})
	}
	t.Run("a token it cannot name is not used", func(t *testing.T) {
		run := runWindow(t, []string{"create-unnamed"}, "", "--rehearse")
		if strings.Contains(run.steps(), "scope") {
			t.Errorf("it went on with a token it cannot revoke by id: %s", run.steps())
		}
	})
	t.Run("the emergency revoke is never a pass", func(t *testing.T) {
		for _, failing := range []string{"create", "create-unnamed"} {
			run := runWindow(t, []string{failing}, "", "--rehearse")
			run.mustStop(t)
			if !strings.Contains(run.steps(), "revoke-unknown") {
				t.Errorf("%s: a token that may exist was left: %s", failing, run.steps())
			}
		}
		run := runWindow(t, []string{"create", "revoke-unknown"}, "", "--rehearse")
		run.mustStop(t)
		if !strings.Contains(run.evidence, "EMERGENCY REVOKE FAILED") {
			t.Errorf("the operator is not told:\n%s", run.evidence)
		}
	})
}

func TestThePushWindowPassesOnlyWhenEveryStepHeld(t *testing.T) {
	run := runWindow(t, nil, "yes\n", testProject, testJob)
	run.mustPass(t, "PUSH WINDOW")
	const want = "check create scope receive show revoke show dead"
	if got := run.steps(); got != want {
		t.Errorf("steps:\n got %s\nwant %s", got, want)
	}
	if !contains(run.calls, "revoke PUSH_TOKEN_ID=1 PUSH_TOKEN_NAME=crn-push-window-101 bash -s") {
		t.Errorf("not revoked by its id and name: %v", run.calls)
	}
	if !strings.Contains(strings.Join(run.calls, "\n"), "CRN_BASELINE_SHA256=abc fitt-coderunner/deploy/macmini/push-window.sh check "+testProject+" "+testJob) {
		t.Errorf("the check is not given the recorded hash and the job: %v", run.calls)
	}
	if !strings.Contains(strings.Join(run.calls, "\n"), "receive "+testProject+" "+testJob) {
		t.Errorf("the token is not handed to that job: %v", run.calls)
	}
	if raw, _ := os.ReadFile(filepath.Join(run.fake, "received")); strings.TrimSpace(string(raw)) != "gitlab+deploy-token-1:gldt-secret-1" {
		t.Errorf("the build got %q", raw)
	}

	for name, c := range map[string]struct {
		failing []string
		stdin   string
		// made is whether a token existed, so one must have been revoked.
		made bool
	}{
		"the check fails":      {[]string{"check"}, "yes\n", false},
		"no baseline recorded": {[]string{"no-record"}, "yes\n", false},
		"the operator says no": {nil, "no\n", false},
		"nobody answers":       {nil, "", false},
		"no connection":        {[]string{"connect"}, "yes\n", false},
		"scope fails":          {[]string{"scope"}, "yes\n", true},
		"receive fails":        {[]string{"receive"}, "yes\n", true},
		"show fails":           {[]string{"show"}, "yes\n", true},
		"no PUSHED in time":    {[]string{"never-pushed"}, "yes\n", true},
		"revoke fails":         {[]string{"revoke"}, "yes\n", true},
		"it is not dead":       {[]string{"dead"}, "yes\n", true},
	} {
		t.Run(name, func(t *testing.T) {
			run := runWindow(t, c.failing, c.stdin, testProject, testJob)
			run.mustStop(t)
			if !c.made && strings.Contains(run.steps(), "create") {
				t.Errorf("a token was made: %s", run.steps())
			}
			if c.made && !strings.Contains(run.steps(), "revoke") {
				t.Errorf("the token was not revoked: %s", run.steps())
			}
		})
	}
	t.Run("a scope failure hands nothing over", func(t *testing.T) {
		run := runWindow(t, []string{"scope"}, "yes\n", testProject, testJob)
		if strings.Contains(run.steps(), "receive") {
			t.Errorf("the token was handed over: %s", run.steps())
		}
	})
	t.Run("no job named", func(t *testing.T) {
		run := runWindow(t, nil, "yes\n", testProject)
		run.mustStop(t)
		if strings.Contains(run.steps(), "create") {
			t.Errorf("a token was made: %s", run.steps())
		}
	})
}

func TestTheBaselineHashIsKeptOnTheLaptop(t *testing.T) {
	run := runWindow(t, nil, "", "--baseline")
	run.mustPass(t, "BASELINE RECORDED")
	raw, err := os.ReadFile(filepath.Join(run.home, ".crn-push-window", "baseline.sha256"))
	if err != nil || strings.TrimSpace(string(raw)) != strings.Repeat("a", 64) {
		t.Errorf("the laptop keeps %q, %v", raw, err)
	}
	run = runWindow(t, []string{"baseline"}, "", "--baseline")
	run.mustStop(t)
}

// The scripts on the GitLab host, around a gitlab-rails that does what a test says.
func runRegistryScript(t *testing.T, script, rails string, env ...string) (string, string, error) {
	t.Helper()
	bin, marks := t.TempDir(), t.TempDir()
	writeShim(t, bin, "gitlab-rails", `touch "`+marks+`/ran"; `+rails)
	cmd := exec.Command("/bin/bash", scriptPath(t, "deploy/registry/"+script))
	cmd.Env = append([]string{"PATH=" + bin + ":/usr/bin:/bin"}, env...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), marks, err
}

func TestARegistryScriptFailsWhenGitLabDoes(t *testing.T) {
	scripts := map[string][]string{
		"push-token-create.sh":         nil,
		"push-token-show.sh":           {"PUSH_TOKEN_ID=5"},
		"push-token-revoke.sh":         {"PUSH_TOKEN_ID=5", "PUSH_TOKEN_NAME=crn-push-window-1"},
		"push-token-revoke-unknown.sh": nil,
		"group-token-revoke.sh":        nil,
	}
	line := map[string]string{
		"push-token-create.sh":         "CRNTOKEN u:t",
		"push-token-show.sh":           "CRNSHOW {}",
		"push-token-revoke.sh":         "CRNREVOKE id=5",
		"push-token-revoke-unknown.sh": "CRNREVOKEALL EMERGENCY",
		"group-token-revoke.sh":        "CRNGROUP revoked",
	}
	for script, env := range scripts {
		t.Run(script, func(t *testing.T) {
			if out, _, err := runRegistryScript(t, script, `echo "rails noise"; echo "`+line[script]+`"; exit 1`, env...); err == nil {
				t.Errorf("rails failed, exit 0: %s", out)
			}
			if out, _, err := runRegistryScript(t, script, `echo "rails noise"; exit 0`, env...); err == nil {
				t.Errorf("rails said nothing, exit 0: %s", out)
			}
			out, _, err := runRegistryScript(t, script, `echo "rails noise"; echo "`+line[script]+`"; exit 0`, env...)
			if err != nil {
				t.Errorf("a good answer: %v", err)
			}
			if strings.Contains(out, "rails noise") || !strings.Contains(out, line[script]) {
				t.Errorf("what it let through: %q", out)
			}
		})
	}
}

// Fittcore's finding on r6: revoking whatever has the right name proves nothing about one token.
func TestRevokeAndShowNeedTheTokensID(t *testing.T) {
	for _, c := range []struct {
		script string
		env    []string
	}{
		{"push-token-revoke.sh", nil},
		{"push-token-revoke.sh", []string{"PUSH_TOKEN_ID=5"}},
		{"push-token-revoke.sh", []string{"PUSH_TOKEN_ID=5", "PUSH_TOKEN_NAME=crn-push"}},
		{"push-token-revoke.sh", []string{"PUSH_TOKEN_ID=5;x", "PUSH_TOKEN_NAME=crn-push-window-1"}},
		{"push-token-show.sh", nil},
		{"push-token-show.sh", []string{"PUSH_TOKEN_ID=abc"}},
	} {
		out, marks, err := runRegistryScript(t, c.script, `echo CRNREVOKE; echo CRNSHOW`, c.env...)
		if err == nil {
			t.Errorf("%s %v: exit 0: %s", c.script, c.env, out)
		}
		if _, statErr := os.Stat(filepath.Join(marks, "ran")); statErr == nil {
			t.Errorf("%s %v: GitLab was asked", c.script, c.env)
		}
	}
}

// What only a real GitLab can show is in TestTheTokenScriptsOnARealGitLab's harness; these are
// the promises the text of the scripts must keep, and its Ruby must at least parse.
func TestTheTokenScriptsSayWhatTheyMustToGitLab(t *testing.T) {
	read := func(name string) string {
		raw, err := os.ReadFile(scriptPath(t, "deploy/registry/"+name))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	create := read("push-token-create.sh")
	for _, want := range []string{
		`minutes.minutes.from_now.utc.iso8601`,  // GitLab refuses a Time here
		`Projects::DeployTokens::CreateService`, // so GitLab records who made it
		`refused: push-window tokens are still live`,
		`token.scopes == [:read_registry, :write_registry]`,
	} {
		if !strings.Contains(create, want) {
			t.Errorf("push-token-create.sh lacks %q", want)
		}
	}
	if !strings.Contains(read("push-token-revoke.sh"), `find_by(id: Integer(ENV.fetch("PUSH_TOKEN_ID")))`) {
		t.Error("push-token-revoke.sh does not find the token by its id")
	}
	if strings.Contains(read("push-token-revoke.sh"), "found.each") || strings.Contains(read("push-token-revoke.sh"), "live.each") {
		t.Error("push-token-revoke.sh revokes more than the one token")
	}
	names := []string{"push-token-create.sh", "push-token-show.sh", "push-token-revoke.sh", "push-token-revoke-unknown.sh", "group-token-revoke.sh"}
	ruby, rubyErr := exec.LookPath("ruby")
	for _, name := range names {
		text := read(name)
		if strings.Contains(text, "2>/dev/null") {
			t.Errorf("%s hides GitLab's errors", name)
		}
		if !strings.Contains(text, "set -euo pipefail") {
			t.Errorf("%s does not stop on a failed pipe", name)
		}
		if rubyErr != nil {
			continue
		}
		start := strings.Index(text, "runner '") + len("runner '")
		end := strings.LastIndex(text, "' | grep")
		cmd := exec.Command(ruby, "-c", "-e", text[start:end])
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: its Ruby does not parse: %s", name, out)
		}
	}
}

// Nothing the operator's scripts print may identify the token by its value, even by a hash.
func TestNoScriptPrintsAFingerprintOfTheToken(t *testing.T) {
	for _, name := range []string{"deploy/macmini/push-window.sh", "deploy/macmini/push-window-run.sh"} {
		raw, err := os.ReadFile(scriptPath(t, name))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, "login") && strings.Contains(line, "shasum") {
				t.Errorf("%s:%s hashes the login", name, strconv.Itoa(i+1))
			}
			if strings.Contains(line, "cut -c1-12") {
				t.Errorf("%s:%s prints a short hash", name, strconv.Itoa(i+1))
			}
		}
	}
}
