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
	m.t.Logf("push-window.sh %s: exit %v\n%s", strings.Join(args, " "), err, out)
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

// iCloud Photos' daemon and iTerm's process-info helper come and go every few minutes: on 9
// and 10 Nov they stopped the window four times, gone or back, with nothing else changed.
// CRN's own sweep ends anything the agent started, whatever its name, before READY.
func TestTheOperatorsCheckIgnoresMacOSAndITermHelpersComingAndGoing(t *testing.T) {
	uid := os.Getuid()
	with := fmt.Sprintf("1 %d /Users/macagents/bin/crn-server\n1 %d cloudphotod\n1 %d /Applications/iTerm.app/Contents/XPCServices/pidinfo.xpc/Contents/MacOS/pidinfo\n", uid, uid, uid)
	without := fmt.Sprintf("1 %d /Users/macagents/bin/crn-server\n", uid)
	for name, states := range map[string][2]string{"gone": {with, without}, "back": {without, with}} {
		t.Run(name, func(t *testing.T) {
			m := newMachineFake(t)
			m.set("processes", states[0])
			hash := m.baseline()
			if err := os.WriteFile(filepath.Join(m.pushDir(), "READY"), []byte(testJob+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			m.set("processes", states[1])
			if out, err := m.run("", []string{"CRN_BASELINE_SHA256=" + hash}, "check", testProject, testJob); err != nil || !strings.Contains(out, "CHECK PASSED") {
				t.Errorf("%v: %s", err, out)
			}
		})
	}
	t.Run("a pidinfo elsewhere still counts", func(t *testing.T) {
		m := newMachineFake(t)
		hash := m.baseline()
		if err := os.WriteFile(filepath.Join(m.pushDir(), "READY"), []byte(testJob+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		m.set("processes", fmt.Sprintf("1 %d /Users/macagents/bin/crn-server\n1 %d /tmp/pidinfo.xpc/Contents/MacOS/pidinfo\n", uid, uid))
		if out, err := m.run("", []string{"CRN_BASELINE_SHA256=" + hash}, "check", testProject, testJob); err == nil || !strings.Contains(out, "CHECK FAILED") {
			t.Errorf("a look-alike passed: %v: %s", err, out)
		}
	})
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
	images    []string            // what the project's image list says
	tags      map[string][]string // an image\'s tags; its list says them, each digest is its name
	grants    map[string]string   // image to "pull,push"; absent grants nothing
	refused   bool                // /jwt/auth refuses the login
	jwtStatus int                 // /jwt/auth answers this instead
	opened    int                 // what the registry answers to opening a push
	noHeader  bool                // the image list does not say whether it ended
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
			if i := strings.Index(r.URL.Path, "/repositories/"); i >= 0 {
				parts := strings.Split(r.URL.Path[i+len("/repositories/"):], "/")
				number, _ := strconv.Atoi(parts[0])
				image := f.images[number-1]
				if len(parts) == 3 {
					_ = json.NewEncoder(w).Encode(map[string]string{"digest": "sha256:" + parts[2]})
					return
				}
				list := []map[string]string{}
				for _, name := range f.tags[image] {
					list = append(list, map[string]string{"name": name})
				}
				_ = json.NewEncoder(w).Encode(list)
				return
			}
			list := []map[string]any{}
			for i, image := range f.images {
				list = append(list, map[string]any{"id": i + 1, "path": image})
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
	return f.askAllowed(t, sub, inNTC+" "+inHello)
}

// askAllowed is ask with CRN_SCOPE_ALLOWED as given; empty means the project's images as
// they are when the window opens.
func (f *registryFake) askAllowed(t *testing.T, sub, allowed string) (string, error) {
	t.Helper()
	srv := f.serve(t)
	defer srv.Close()
	m := newMachineFake(t)
	return m.run(aLogin+"\n", []string{
		"CRN_GITLAB_URL=" + srv.URL, "CRN_REGISTRY_URL=" + srv.URL, "CRN_SCOPE_PROJECT=fitt/ftcdv-uat",
		"CRN_SCOPE_ALLOWED=" + allowed, "CRN_SCOPE_OUTSIDE=" + outDemos,
	}, sub)
}

func (f *registryFake) askTags(t *testing.T) (string, error) {
	t.Helper()
	srv := f.serve(t)
	defer srv.Close()
	m := newMachineFake(t)
	return m.run("", []string{"CRN_GITLAB_URL=" + srv.URL, "CRN_SCOPE_PROJECT=fitt/ftcdv-uat"}, "tags")
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

// A customer's first image adds an image to the project, so a list written in the script
// would stop the next window. Without one, the scope is the project's images as GitLab
// lists them when the window opens: each must be pull,push, nothing outside, as before.
func TestScopeWithoutAListIsTheProjectAsItIs(t *testing.T) {
	f := goodScope()
	f.images = append(f.images, "fitt/ftcdv-uat/crn-demo-new-12345678")
	f.grants["fitt/ftcdv-uat/crn-demo-new-12345678"] = "pull,push"
	out, err := f.askAllowed(t, "scope", "")
	if err != nil || !strings.HasSuffix(strings.TrimSpace(out), "SCOPE PASSED") {
		t.Fatalf("%v: %s", err, out)
	}
	for _, image := range f.images {
		if !strings.Contains(out, image+" pull,push OK") {
			t.Errorf("%s was not checked: %s", image, out)
		}
	}
	if !strings.Contains(out, "the project holds:") {
		t.Errorf("the evidence does not list the project's images: %s", out)
	}
	for name, change := range map[string]func(f *registryFake){
		"pull only on the new image": func(f *registryFake) { f.grants["fitt/ftcdv-uat/crn-demo-new-12345678"] = "pull" },
		"push outside the project":   func(f *registryFake) { f.grants[outDemos] = "pull,push" },
		"the list does not end":      func(f *registryFake) { f.noHeader = true },
	} {
		t.Run(name, func(t *testing.T) {
			f := goodScope()
			f.images = append(f.images, "fitt/ftcdv-uat/crn-demo-new-12345678")
			f.grants["fitt/ftcdv-uat/crn-demo-new-12345678"] = "pull,push"
			change(f)
			if out, err := f.askAllowed(t, "scope", ""); err == nil || !strings.Contains(out, "SCOPE FAILED") {
				t.Errorf("%v: %s", err, out)
			}
		})
	}
}

func TestDeadWithoutAListChecksEveryImageOfTheProject(t *testing.T) {
	f := goodScope()
	f.images = append(f.images, "fitt/ftcdv-uat/crn-demo-new-12345678")
	f.grants = map[string]string{"fitt/ftcdv-uat/crn-demo-new-12345678": "pull,push"}
	out, err := f.askAllowed(t, "dead", "")
	if err == nil || !strings.Contains(out, "crn-demo-new-12345678 WRONG: it can still push") {
		t.Errorf("a live push on the new image was not seen: %v: %s", err, out)
	}
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

func TestSessionsFailsWhenAnAgentIsOpen(t *testing.T) {
	m := newMachineFake(t)
	if out, err := m.run("", nil, "sessions"); err != nil || !strings.Contains(out, "SESSIONS PASSED") {
		t.Errorf("none open: %v: %s", err, out)
	}
	m.set("sessions", "claude --resume\n")
	if out, err := m.run("", nil, "sessions"); err == nil || !strings.Contains(out, "SESSIONS FAILED") {
		t.Errorf("one open: %v: %s", err, out)
	}
}

func TestTagsListsEveryTagOrFails(t *testing.T) {
	f := goodScope()
	f.tags = map[string][]string{inNTC: {"v2", "v1"}}
	out, err := f.askTags(t)
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	want := inHello + " (no tags)\n" + inNTC + " v1 sha256:v1\n" + inNTC + " v2 sha256:v2\n"
	if out != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
	f = goodScope()
	f.noHeader = true
	if out, err := f.askTags(t); err == nil || !strings.Contains(out, "TAGS FAILED") {
		t.Errorf("a list that does not end: %v: %s", err, out)
	}
	f = goodScope()
	f.tags = map[string][]string{inNTC: {"v1", "v1"}}
	if out, err := f.askTags(t); err == nil || !strings.Contains(out, "TAGS FAILED") {
		t.Errorf("a tag listed twice: %v: %s", err, out)
	}
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
	*WINDOW_START*) step=writelog ;;
	*CRNREVOKEALL*) step=revoke-unknown ;;
	*CRNREVOKE*) step=revoke ;;
	*CRNSHOW*) step=show ;;
	*CRNTOKEN*) step=create ;;
	*) step=unknown ;;
	esac
	note "$step $cmd"
	if [ "$step" = show ]; then
		n=$(( $(cat "$FAKE/shows" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$FAKE/shows"
		fails "show-$n" && { echo "show $n failed" >&2; exit 1; }
	fi
	if fails "$step"; then
		[ "$step" = writelog ] && printf '== summary\nWRONG: a write outside the allowed image\nWRITE LOG FAILED: 1 problem(s)\n'
		echo "$step failed" >&2
		exit 1
	fi
	case "$step" in
	create)
		n=$(( $(cat "$FAKE/made" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$FAKE/made"
		fails create-unnamed && { echo "CRNTOKEN gitlab+deploy-token-$n:gldt-secret-$n"; exit 0; }
		fails create-noid && { echo "CRNMETA name=crn-push-window-10$n live_before=0 live_after=1 username=u expires_epoch=2 gitlab_clock=t scopes=s"; echo "CRNTOKEN gitlab+deploy-token-$n:gldt-secret-$n"; exit 0; }
		fails create-tokenless && { echo "CRNMETA id=$n name=crn-push-window-10$n live_before=0 live_after=1 username=u expires_epoch=2 gitlab_clock=t scopes=s"; exit 0; }
		before=0; fails create-count && before=1
		echo "CRNMETA id=$n name=crn-push-window-10$n live_before=$before live_after=1 username=gitlab+deploy-token-$n created_at=c expires_at=e expires_epoch=2 gitlab_clock=t scopes=read_registry,write_registry"
		echo "CRNTOKEN gitlab+deploy-token-$n:gldt-secret-$n"
		fails create-two && echo "CRNTOKEN gitlab+deploy-token-$n:gldt-secret-other"
		;;
	revoke) echo "CRNREVOKE $cmd" ;;
	revoke-unknown) echo "CRNREVOKEALL EMERGENCY" ;;
	show) if fails show-no-jwt; then echo "CRNSHOW {}"; else echo 'CRNSHOW {"id":1,"registry_jwt_minutes":5}'; fi ;;
	writelog) printf '== summary\nwrites listed here\nWRITE LOG PASSED\n== raw, nginx\nraw line kept in its file only\n' ;;
	esac
	;;
macagents@*)
	case "$cmd" in
	*" sessions") note sessions; fails sessions && { echo SESSIONS FAILED; exit 1; }; echo SESSIONS PASSED ;;
	*" tags")
		n=$(( $(cat "$FAKE/tagreads" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$FAKE/tagreads"
		note tags
		{ fails tags || fails "tags-$n"; } && { echo TAGS FAILED >&2; exit 1; }
		if [ "$n" -gt 1 ] && [ -f "$FAKE/tags-after" ]; then cat "$FAKE/tags-after"
		elif [ -f "$FAKE/tags-before" ]; then cat "$FAKE/tags-before"
		else echo "fitt/x/ntc v2 sha256:2"; fi ;;
	*" baseline") note baseline; fails baseline && { echo "baseline saved: 1 lines, sha256 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"; exit 1; }; echo "baseline saved: 1 lines, sha256 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ;;
	*" status "*) cat "$FAKE/status" ;;
	*" check "*) note "check $cmd"; fails check && { echo CHECK FAILED; exit 1; }; echo CHECK PASSED ;;
	*" receive "*) note "receive $cmd"; cat > "$FAKE/received"; fails receive && exit 1; [ -f "$FAKE/never-pushed" ] || echo PUSHED > "$FAKE/status"; [ -f "$FAKE/tags-after" ] || printf 'fitt/x/ntc v2 sha256:2\nfitt/x/ntc v3 sha256:3\n' > "$FAKE/tags-after"; echo left ;;
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
	writeShim(t, bin, "sleep", `echo "$1" >> "$FAKE/sleeps"`)
	for _, name := range failing {
		if file, content, ok := strings.Cut(name, "="); ok {
			if err := os.WriteFile(filepath.Join(fake, file), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			continue
		}
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

// logTranscript leaves what the operator saw in the test's output, for the transcript Fittcore
// reads: go test -v.
func (r windowRun) logTranscript(t *testing.T) {
	t.Helper()
	t.Logf("exit: %v\nsteps: %s\nevidence:\n%s", r.err, r.steps(), r.evidence)
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

// Fittcore's order: 0 live before, one made, the right scope, no write and no tag changed, that
// one revoked, none live, and it cannot push after.
func TestTheRehearsalPassesOnlyWhenEveryStepHeld(t *testing.T) {
	run := runWindow(t, nil, "", "--rehearse")
	run.mustPass(t, "REHEARSAL")
	const want = "sessions tags create scope show revoke show dead tags writelog"
	if got := run.steps(); got != want {
		t.Errorf("steps:\n got %s\nwant %s", got, want)
	}
	if !contains(run.calls, "revoke PUSH_TOKEN_ID=1 PUSH_TOKEN_NAME=crn-push-window-101 bash -s") {
		t.Errorf("not revoked by its id and name: %v", run.calls)
	}
	if !contains(run.calls, "writelog WINDOW_START=1 WINDOW_END=1 PUSH_ALLOW_IMAGE= bash -s") {
		t.Errorf("the write log may not allow any write: %v", run.calls)
	}
	for _, want := range []string{"id=1 name=crn-push-window-101", "live_before=0 live_after=1", "WRITE LOG PASSED", "write log sha256: ", "waiting 330s"} {
		if !strings.Contains(run.evidence, want) {
			t.Errorf("the evidence lacks %q:\n%s", want, run.evidence)
		}
	}
	// A registry token issued before the revoke works for its minutes more: the log reaches past it.
	if raw, _ := os.ReadFile(filepath.Join(run.fake, "sleeps")); strings.TrimSpace(string(raw)) != "330" {
		t.Errorf("it did not wait out registry tokens: %q", raw)
	}
	if strings.Contains(run.evidence, "raw line kept in its file only") {
		t.Error("the raw log was copied into the evidence")
	}
	raw, _ := os.ReadFile(filepath.Join(run.fake, "dead-logins"))
	if got := strings.Fields(string(raw)); len(got) != 1 || got[0] != "gitlab+deploy-token-1:gldt-secret-1" {
		t.Errorf("the dead check was not on the token just made: %v", got)
	}

	nothingMade := map[string]bool{"connect": true, "sessions": true, "tags": true, "tags-1": true}
	for _, failing := range []string{"connect", "sessions", "tags-1", "create", "create-unnamed", "create-tokenless", "create-two", "create-count", "create-noid", "scope", "show", "show-1", "show-2", "show-no-jwt", "revoke", "dead", "tags-2", "writelog", "tags-after=fitt/x/ntc v2 sha256:other\n"} {
		t.Run(failing, func(t *testing.T) {
			run := runWindow(t, []string{failing}, "", "--rehearse")
			run.mustStop(t)
			run.logTranscript(t)
			if nothingMade[failing] && strings.Contains(run.steps(), "create") {
				t.Errorf("a token was made: %s", run.steps())
			}
			if strings.HasPrefix(failing, "create") && failing != "create" && failing != "create-unnamed" && failing != "create-noid" && !strings.Contains(run.steps(), "revoke ") {
				t.Errorf("a token it knew was not revoked by id: %s", run.steps())
			}
			if strings.Contains(run.steps(), "revoke-unknown") && failing != "create" && failing != "create-unnamed" && failing != "create-noid" {
				t.Errorf("revoked by name when it knew the id: %s", run.steps())
			}
			if !nothingMade[failing] && !strings.Contains(run.steps(), "writelog") {
				t.Errorf("a token's window has no write log: %s", run.steps())
			}
		})
	}
	t.Run("a token it cannot name is not used", func(t *testing.T) {
		for _, failing := range []string{"create-unnamed", "create-noid"} {
			run := runWindow(t, []string{failing}, "", "--rehearse")
			if strings.Contains(run.steps(), "scope") {
				t.Errorf("%s: it went on with a token it cannot revoke by id: %s", failing, run.steps())
			}
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

// A second run, not a gate: a token left alone is dead after its expires_at, before any revoke.
func TestProvingExpiryLeavesTheTokenAloneUntilItIsDead(t *testing.T) {
	run := runWindow(t, nil, "", "--prove-expiry")
	run.mustPass(t, "EXPIRY")
	const want = "sessions create scope show show dead revoke show dead writelog"
	if got := run.steps(); got != want {
		t.Errorf("steps:\n got %s\nwant %s", got, want)
	}
	if !contains(run.calls, "create PUSH_TOKEN_MINUTES=2 bash -s") {
		t.Errorf("not a short-lived token: %v", run.calls)
	}
	for _, failing := range []string{"sessions", "dead-1", "dead-2", "revoke"} {
		t.Run(failing, func(t *testing.T) {
			run := runWindow(t, []string{failing}, "", "--prove-expiry")
			run.mustStop(t)
			run.logTranscript(t)
		})
	}
	t.Run("it says what did not hold, and revokes", func(t *testing.T) {
		run := runWindow(t, []string{"dead-1"}, "", "--prove-expiry")
		if !strings.Contains(run.evidence, "EXPIRY NOT SHOWN") || !strings.Contains(run.steps(), "revoke") {
			t.Errorf("%s\n%s", run.steps(), run.evidence)
		}
	})
}

const testImage = "fitt/x/ntc:v3"

func TestThePushWindowPassesOnlyWhenEveryStepHeld(t *testing.T) {
	run := runWindow(t, nil, "yes\n", testProject, testJob, testImage)
	run.mustPass(t, "PUSH WINDOW")
	const want = "check tags create scope receive show revoke show dead tags writelog"
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
	if !contains(run.calls, "writelog WINDOW_START=1 WINDOW_END=1 PUSH_ALLOW_IMAGE="+testImage+" bash -s") {
		t.Errorf("the write log is not told the one image: %v", run.calls)
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
		"the check fails":                  {[]string{"check"}, "yes\n", false},
		"no baseline recorded":             {[]string{"no-record"}, "yes\n", false},
		"the operator says no":             {nil, "no\n", false},
		"nobody answers":                   {nil, "", false},
		"no connection":                    {[]string{"connect"}, "yes\n", false},
		"the tags cannot be read":          {[]string{"tags-1"}, "yes\n", false},
		"scope fails":                      {[]string{"scope"}, "yes\n", true},
		"receive fails":                    {[]string{"receive"}, "yes\n", true},
		"show fails":                       {[]string{"show"}, "yes\n", true},
		"no PUSHED in time":                {[]string{"never-pushed"}, "yes\n", true},
		"revoke fails":                     {[]string{"revoke"}, "yes\n", true},
		"it is not dead":                   {[]string{"dead"}, "yes\n", true},
		"the write log fails":              {[]string{"writelog"}, "yes\n", true},
		"the tags after are unread":        {[]string{"tags-2"}, "yes\n", true},
		"no new tag":                       {[]string{"tags-after=fitt/x/ntc v2 sha256:2\n"}, "yes\n", true},
		"another tag moved":                {[]string{"tags-after=fitt/x/ntc v2 sha256:other\nfitt/x/ntc v3 sha256:3\n"}, "yes\n", true},
		"a second new tag":                 {[]string{"tags-after=fitt/x/ntc v2 sha256:2\nfitt/x/ntc v3 sha256:3\nfitt/x/ntc v4 sha256:4\n"}, "yes\n", true},
		"another image got the tag":        {[]string{"tags-after=fitt/x/hello v3 sha256:3\nfitt/x/ntc v2 sha256:2\n"}, "yes\n", true},
		"another image lost its emptiness": {[]string{"tags-before=fitt/x/hello (no tags)\nfitt/x/ntc (no tags)\n", "tags-after=fitt/x/hello v1 sha256:1\nfitt/x/ntc v3 sha256:3\n"}, "yes\n", true},
		"an image with no tags is gone":    {[]string{"tags-before=fitt/x/hello (no tags)\nfitt/x/ntc v2 sha256:2\n", "tags-after=fitt/x/ntc v2 sha256:2\nfitt/x/ntc v3 sha256:3\n"}, "yes\n", true},
	} {
		t.Run(name, func(t *testing.T) {
			run := runWindow(t, c.failing, c.stdin, testProject, testJob, testImage)
			run.mustStop(t)
			run.logTranscript(t)
			if !c.made && strings.Contains(run.steps(), "create") {
				t.Errorf("a token was made: %s", run.steps())
			}
			if c.made && !strings.Contains(run.steps(), "revoke") {
				t.Errorf("the token was not revoked: %s", run.steps())
			}
			if c.made && !strings.Contains(run.steps(), "writelog") {
				t.Errorf("a token's window has no write log: %s", run.steps())
			}
		})
	}
	t.Run("an image's first tag is that tag", func(t *testing.T) {
		run := runWindow(t, []string{"tags-before=fitt/x/hello (no tags)\nfitt/x/ntc (no tags)\n", "tags-after=fitt/x/hello (no tags)\nfitt/x/ntc v3 sha256:3\n"}, "yes\n", testProject, testJob, testImage)
		run.mustPass(t, "PUSH WINDOW")
	})
	t.Run("a scope failure hands nothing over", func(t *testing.T) {
		run := runWindow(t, []string{"scope"}, "yes\n", testProject, testJob, testImage)
		if strings.Contains(run.steps(), "receive") {
			t.Errorf("the token was handed over: %s", run.steps())
		}
	})
	for name, args := range map[string][]string{
		"no job named":          {testProject},
		"a bad job":             {testProject, "not-a-job", testImage},
		"no image named":        {testProject, testJob},
		"a bad image":           {testProject, testJob, "ntc:v3"},
		"an image with a space": {testProject, testJob, "fitt/x/ntc:v3 bash"},
	} {
		t.Run(name, func(t *testing.T) {
			run := runWindow(t, nil, "yes\n", args...)
			run.mustStop(t)
			run.logTranscript(t)
			if strings.Contains(run.steps(), "create") {
				t.Errorf("a token was made: %s", run.steps())
			}
		})
	}
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
	t.Logf("%s %v: exit %v, stdout %q", script, env, err, stdout.String())
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

// writeLogFake is what the write log reads: the logs of a window that starts at 14:00:00 UTC.
type writeLogFake struct {
	t                      *testing.T
	nginx, registry, rails string
}

const (
	logNTC   = "fitt/ftcdv-uat/ntc"
	logHello = "fitt/ftcdv-uat/hello"
)

func newWriteLogFake(t *testing.T) *writeLogFake {
	t.Helper()
	f := &writeLogFake{t: t, nginx: t.TempDir(), registry: t.TempDir(), rails: t.TempDir()}
	f.append(f.registry, "current", "")
	f.append(f.nginx, "gitlab_registry_access.log", "")
	return f
}

func (f *writeLogFake) append(dir, name, text string) {
	f.t.Helper()
	file, err := os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		f.t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(text); err != nil {
		f.t.Fatal(err)
	}
}

// request writes one request into both logs at 14:mm:ss, as nginx and the registry write it.
func (f *writeLogFake) request(at, method, target string, status int) {
	f.t.Helper()
	f.nginxOnly(at, method, target, status)
	f.registryOnly(at, method, target, status)
}

func (f *writeLogFake) nginxOnly(at, method, target string, status int) {
	f.append(f.nginx, "gitlab_registry_access.log", fmt.Sprintf("172.168.1.171 - - [06/Nov/2026:14:%s +0000] \"%s %s HTTP/1.1\" %d 0 \"\" \"docker\" -\n", at, method, target, status))
}

func (f *writeLogFake) registryOnly(at, method, target string, status int) {
	id := fmt.Sprintf("C%s%d", strings.ReplaceAll(at, ":", ""), status)
	f.append(f.registry, "current", fmt.Sprintf("2026-11-06_14:%s.00001 time=\"2026-11-06T14:%s.000Z\" level=info msg=\"authorized request\" auth_project_paths=\"[fitt/ftcdv-uat]\" auth_user_name= auth_user_type=deploy_token correlation_id=%s\n", at, at, id))
	line, _ := json.Marshal(map[string]any{"msg": "access", "method": method, "uri": target, "status": status, "remote_ip": "172.168.1.171", "time": "2026-11-06T14:" + at + ".000Z", "correlation_id": id})
	f.append(f.registry, "current", fmt.Sprintf("2026-11-06_14:%s.00002 %s\n", at, line))
}

// aPush is what docker push leaves: a mount from another image, a blob, a manifest, the tag.
func (f *writeLogFake) aPush() {
	f.request("00:10", "POST", "/v2/"+logNTC+"/blobs/uploads/?mount=sha256:aaa&from="+logHello, 201)
	f.request("00:11", "POST", "/v2/"+logNTC+"/blobs/uploads/", 202)
	f.request("00:12", "PATCH", "/v2/"+logNTC+"/blobs/uploads/u1?_state=SECRETSTATE", 202)
	f.request("00:13", "PUT", "/v2/"+logNTC+"/blobs/uploads/u1?_state=SECRETSTATE&digest=sha256:bbb", 201)
	f.request("00:14", "PUT", "/v2/"+logNTC+"/manifests/sha256:ccc", 201)
	f.request("00:15", "PUT", "/v2/"+logNTC+"/manifests/v3", 201)
}

func (f *writeLogFake) run(allow string) (string, error) {
	f.t.Helper()
	ruby, err := exec.LookPath("ruby")
	if err != nil {
		f.t.Skip("no ruby here")
	}
	cmd := exec.Command("/bin/bash", scriptPath(f.t, "deploy/registry/registry-write-log.sh"))
	cmd.Env = []string{
		"PATH=/usr/bin:/bin", "WRITE_LOG_RUBY=" + ruby, "WRITE_LOG_NGINX=" + f.nginx, "WRITE_LOG_REGISTRY=" + f.registry, "WRITE_LOG_RAILS=" + f.rails,
		"WINDOW_START=1793973600", "WINDOW_END=1793974200", "PUSH_ALLOW_IMAGE=" + allow, // 14:00:00 to 14:10:00 UTC
	}
	out, err := cmd.CombinedOutput()
	f.t.Logf("exit: %v\n%s", err, out)
	return string(out), err
}

func TestAWindowThatPushedOnlyTheOneTagPasses(t *testing.T) {
	f := newWriteLogFake(t)
	f.aPush()
	f.append(f.rails, "production_json.log", `{"time":"2026-11-06T14:00:09.000Z","path":"/jwt/auth","status":200,"remote_ip":"172.168.1.171","params":[{"key":"account","value":"gitlab+deploy-token-7"},{"key":"scope","value":"repository:`+logNTC+`:push,pull"}]}`+"\n")
	out, err := f.run(logNTC + ":v3")
	if err != nil || !strings.Contains(out, "WRITE LOG PASSED") {
		t.Fatalf("%v", err)
	}
	for _, want := range []string{
		"writes to " + logNTC + ": 6", "kind=mount", "from=" + logHello, "kind=blob", "ref=v3", "ref=sha256:ccc",
		"who=deploy_token", "who=not recorded by nginx", "ip=172.168.1.171",
		"account=gitlab+deploy-token-7", "== raw, nginx", "== raw, registry",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q", want)
		}
	}
	if strings.Contains(out, "SECRETSTATE") {
		t.Error("the upload state was printed")
	}
}

// Fittcore's rule: a write that was put back still fails, however the tags end up.
func TestAWriteLogFailsOnAnyOtherWrite(t *testing.T) {
	cases := map[string]func(f *writeLogFake){
		"a blob to another image": func(f *writeLogFake) { f.request("01:00", "POST", "/v2/"+logHello+"/blobs/uploads/", 202) },
		"a mount into another image": func(f *writeLogFake) {
			f.request("01:00", "POST", "/v2/"+logHello+"/blobs/uploads/?mount=sha256:a&from="+logNTC, 201)
		},
		"a manifest to another image":      func(f *writeLogFake) { f.request("01:00", "PUT", "/v2/"+logHello+"/manifests/v2", 201) },
		"a delete on another image":        func(f *writeLogFake) { f.request("01:00", "DELETE", "/v2/"+logHello+"/manifests/sha256:a", 202) },
		"a delete on the image":            func(f *writeLogFake) { f.request("01:00", "DELETE", "/v2/"+logNTC+"/manifests/sha256:a", 202) },
		"a delete that failed oddly":       func(f *writeLogFake) { f.request("01:00", "DELETE", "/v2/"+logHello+"/manifests/v2", 500) },
		"another tag of the image":         func(f *writeLogFake) { f.request("01:00", "PUT", "/v2/"+logNTC+"/manifests/v2", 201) },
		"a write nginx did not see":        func(f *writeLogFake) { f.registryOnly("01:00", "PUT", "/v2/"+logHello+"/manifests/v2", 201) },
		"a write the registry did not log": func(f *writeLogFake) { f.nginxOnly("01:00", "PUT", "/v2/"+logHello+"/manifests/v2", 201) },
		"something else under /v2/":        func(f *writeLogFake) { f.request("01:00", "POST", "/v2/_catalog", 200) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newWriteLogFake(t)
			f.aPush()
			change(f)
			out, err := f.run(logNTC + ":v3")
			if err == nil || !strings.Contains(out, "WRITE LOG FAILED") || strings.Contains(out, "WRITE LOG PASSED") {
				t.Errorf("exit %v", err)
			}
		})
	}
}

func TestAWriteLogNeedsTheTagAndBothLogs(t *testing.T) {
	t.Run("the tag was never written", func(t *testing.T) {
		f := newWriteLogFake(t)
		f.request("00:11", "POST", "/v2/"+logNTC+"/blobs/uploads/", 202)
		if out, err := f.run(logNTC + ":v3"); err == nil || !strings.Contains(out, "never written") {
			t.Errorf("exit %v", err)
		}
	})
	t.Run("the tag was refused", func(t *testing.T) {
		f := newWriteLogFake(t)
		f.request("00:15", "PUT", "/v2/"+logNTC+"/manifests/v3", 401)
		if out, err := f.run(logNTC + ":v3"); err == nil || !strings.Contains(out, "never written") {
			t.Errorf("exit %v", err)
		}
	})
	t.Run("a log with nothing in the window", func(t *testing.T) {
		f := newWriteLogFake(t)
		f.nginxOnly("00:15", "PUT", "/v2/"+logNTC+"/manifests/v3", 201)
		if out, err := f.run(logNTC + ":v3"); err == nil || !strings.Contains(out, "shows nothing") {
			t.Errorf("exit %v", err)
		}
	})
	t.Run("no log at all", func(t *testing.T) {
		f := newWriteLogFake(t)
		if err := os.Remove(filepath.Join(f.nginx, "gitlab_registry_access.log")); err != nil {
			t.Fatal(err)
		}
		if _, err := f.run(logNTC + ":v3"); err == nil {
			t.Error("exit 0")
		}
	})
	t.Run("a line it cannot read", func(t *testing.T) {
		f := newWriteLogFake(t)
		f.aPush()
		f.append(f.nginx, "gitlab_registry_access.log", "this is not a log line\n")
		if out, err := f.run(logNTC + ":v3"); err == nil || !strings.Contains(out, "cannot be read") {
			t.Errorf("exit %v", err)
		}
	})
	t.Run("no window", func(t *testing.T) {
		ruby, err := exec.LookPath("ruby")
		if err != nil {
			t.Skip("no ruby here")
		}
		cmd := exec.Command("/bin/bash", scriptPath(t, "deploy/registry/registry-write-log.sh"))
		cmd.Env = []string{"PATH=/usr/bin:/bin", "WRITE_LOG_RUBY=" + ruby}
		if out, err := cmd.CombinedOutput(); err == nil {
			t.Errorf("exit 0: %s", out)
		}
	})
}

// A rehearsal writes nothing. Its one refused probe outside the project is not a write.
func TestARehearsalsWriteLogAllowsNoWrite(t *testing.T) {
	const outside = "/v2/fitt/demos/crn-demo-scope-check/blobs/uploads/"
	f := newWriteLogFake(t)
	f.request("00:10", "POST", outside, 401)
	out, err := f.run("")
	if err != nil || !strings.Contains(out, "WRITE LOG PASSED") {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(out, "may write: nothing") || !strings.Contains(out, "refused: ") {
		t.Error("the output does not say what it allowed and what was refused")
	}
	for name, change := range map[string]func(f *writeLogFake){
		"a blob":     func(f *writeLogFake) { f.request("01:00", "POST", "/v2/"+logNTC+"/blobs/uploads/", 202) },
		"a manifest": func(f *writeLogFake) { f.request("01:00", "PUT", "/v2/"+logNTC+"/manifests/v3", 201) },
		"a delete":   func(f *writeLogFake) { f.request("01:00", "DELETE", "/v2/"+logHello+"/manifests/v2", 202) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newWriteLogFake(t)
			f.request("00:10", "POST", outside, 401)
			change(f)
			if out, err := f.run(""); err == nil || !strings.Contains(out, "WRITE LOG FAILED") {
				t.Errorf("exit %v", err)
			}
		})
	}
	t.Run("logs that show nothing prove nothing", func(t *testing.T) {
		f := newWriteLogFake(t)
		if out, err := f.run(""); err == nil || !strings.Contains(out, "shows nothing") {
			t.Errorf("exit %v", err)
		}
	})
	t.Run("nginx showing nothing proves nothing", func(t *testing.T) {
		f := newWriteLogFake(t)
		f.registryOnly("00:10", "POST", outside, 401)
		if out, err := f.run(""); err == nil || !strings.Contains(out, "nginx: no line") {
			t.Errorf("exit %v", err)
		}
	})
	t.Run("what is outside the window does not count", func(t *testing.T) {
		f := newWriteLogFake(t)
		f.request("00:10", "POST", outside, 401)
		f.request("30:00", "PUT", "/v2/"+logHello+"/manifests/v2", 201)
		if out, err := f.run(""); err != nil {
			t.Errorf("exit %v: %s", err, out)
		}
	})
}

func TestAWriteLogKeepsNoSecret(t *testing.T) {
	f := newWriteLogFake(t)
	f.request("00:10", "POST", "/v2/fitt/demos/x/blobs/uploads/?_state=SECRETSTATE", 401)
	f.append(f.registry, "current", `2026-11-06_14:00:11.00001 time="2026-11-06T14:00:11.000Z" msg="x" authorization="Bearer SECRETBEARER" token=eyJhbGci.eyJzdWIi.SECRETSIG other=gldt-SECRETDEPLOYTOKEN`+"\n")
	out, err := f.run("")
	if err != nil {
		t.Fatalf("%v", err)
	}
	for _, secret := range []string{"SECRETSTATE", "SECRETBEARER", "SECRETSIG", "SECRETDEPLOYTOKEN"} {
		if strings.Contains(out, secret) {
			t.Errorf("%s was printed", secret)
		}
	}
	if !strings.Contains(out, "[REDACTED") {
		t.Error("nothing says it was redacted")
	}
}
