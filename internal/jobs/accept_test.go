package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/buildsettings"
	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

const fakeDigest = "sha256:" + "ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12"

// A shell standing in for docker: it records each call and answers what the
// pipeline reads back. FAKE_REGISTRY_DIGEST is what the registry says v3 is.
const pipelineDocker = `#!/bin/sh
echo "docker $*" >> "$EVENTS"
case "$1 $2" in
  "image inspect") echo "[\"$(echo "$3" | sed 's/:v[0-9]*$//')@${FAKE_LOCAL_DIGEST}\"]" ;;
  "manifest inspect") echo "{\"Descriptor\":{\"digest\":\"${FAKE_REGISTRY_DIGEST}\"}}" ;;
esac
exit 0
`

// A shell standing in for Node: the release checker passes, and the candidate
// gate writes the receipt FAKE_ACCEPT asks for.
const fakeNode = `#!/bin/sh
script=$(basename "$1")
echo "node $script $*" >> "$EVENTS"
if [ "$script" = "verify-delivery.mjs" ]; then
  echo '{"status":"passed"}' > CRN_VERIFICATION.json
  exit 0
fi
image=""; commit=""; receipt=""
while [ $# -gt 0 ]; do
  case "$1" in
    --image) image="$2" ;;
    --commit) commit="$2" ;;
    --receipt) receipt="$2" ;;
  esac
  shift
done
case "$FAKE_ACCEPT" in
  passed) printf '{"status":"passed","image":"%s","commit":"%s","platform":"linux/amd64"}' "$image" "$commit" > "$receipt"; exit 0 ;;
  wrong-image) printf '{"status":"passed","image":"reg/other@sha256:00","commit":"%s","platform":"linux/amd64"}' "$commit" > "$receipt"; exit 0 ;;
  crash) exit 3 ;;
  no-receipt) exit 0 ;;
  *) printf '{"status":"failed","image":"%s","commit":"%s","platform":"linux/amd64","error":"CA file is a symlink (migrate)"}' "$image" "$commit" > "$receipt"; exit 1 ;;
esac
`

// pipelineStore is the little of the store one build touches.
type pipelineStore struct {
	domain.Store
	mu       sync.Mutex
	statuses []domain.JobStatus
	tag      string
}

func (s *pipelineStore) UpdateJobStatus(_ context.Context, _ uuid.UUID, st domain.JobStatus, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses = append(s.statuses, st)
	return nil
}
func (s *pipelineStore) SetJobDockerTag(_ context.Context, _ uuid.UUID, tag string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tag = tag
	return nil
}
func (s *pipelineStore) LastSessionID(context.Context, uuid.UUID) (string, error) { return "", nil }
func (s *pipelineStore) SaveJobTrace(context.Context, *domain.BuildTrace) error   { return nil }
func (s *pipelineStore) SetProjectBuildCache(context.Context, uuid.UUID, string, string) error {
	return nil
}

type nopNotifier struct{}

func (nopNotifier) Notify(context.Context, *domain.BuildEvent) error { return nil }

type pipelineRun struct {
	events []string
	bodies []map[string]any
	store  *pipelineStore
	logs   string
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// runPipeline drives one edit build of an external-tls-only app through runJob,
// with docker and the checker faked and a real git remote and callback endpoint.
func runPipeline(t *testing.T, accept, deliveryJSON string, env map[string]string) pipelineRun {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	events := filepath.Join(root, "events")
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"docker": pipelineDocker, "fake-node": fakeNode} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("EVENTS", events)
	t.Setenv("FAKE_ACCEPT", accept)
	t.Setenv("FAKE_LOCAL_DIGEST", fakeDigest)
	t.Setenv("FAKE_REGISTRY_DIGEST", fakeDigest)
	t.Setenv("CRN_VERIFY_NODE", filepath.Join(bin, "fake-node"))
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.test")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.test")
	for k, v := range env {
		t.Setenv(k, v)
	}

	project := uuid.New()
	branch := "crn/app-" + project.String()[:8]
	remote := filepath.Join(root, "remote.git")
	git(t, root, "init", "--bare", "-q", remote)
	seed := filepath.Join(root, "seed")
	git(t, root, "init", "-q", "-b", branch, seed)
	for name, body := range map[string]string{
		"package.json":      `{"name":"app","dependencies":{"next":"16.2.9"}}`,
		"crn-delivery.json": deliveryJSON,
		"crn.runtime.json":  `{"env":[]}`,
	} {
		if err := os.WriteFile(filepath.Join(seed, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, seed, "add", "-A")
	git(t, seed, "commit", "-q", "-m", "build 2")
	git(t, seed, "push", "-q", remote, branch)

	var mu sync.Mutex
	record := func(line string) {
		mu.Lock()
		defer mu.Unlock()
		f, err := os.OpenFile(events, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Error(err)
			return
		}
		defer f.Close()
		fmt.Fprintln(f, line)
	}
	var run pipelineRun
	terminal := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		run.bodies = append(run.bodies, body)
		mu.Unlock()
		status, _ := body["status"].(string)
		record("callback " + status)
		w.WriteHeader(http.StatusOK)
		if status == "released" || status == "failed" {
			select {
			case terminal <- struct{}{}:
			default:
			}
		}
	}))
	defer srv.Close()

	forbidden := filepath.Join(root, "forbidden")
	if err := os.Mkdir(forbidden, 0o700); err != nil {
		t.Fatal(err)
	}
	if env["NO_FORBIDDEN_LIST"] == "" {
		if err := os.WriteFile(filepath.Join(forbidden, project.String()+".txt"), []byte("hashes\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	org := uuid.New()
	st := &pipelineStore{}
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&syncWriter{w: &logs}, nil))
	if testing.Verbose() {
		logger = slog.New(slog.NewTextHandler(io.MultiWriter(&syncWriter{w: &logs}, os.Stderr), nil))
	}
	m := NewManager(st, nil, nopNotifier{}, logger, filepath.Join(root, "projects"), remote, "", false, false, "", srv.URL, "", map[uuid.UUID]bool{org: true}, nil, true, "172.168.1.234:5050/fitt/ftcdv-uat", nil, "", "", 1, buildsettings.Defaults{}).(*manager)
	m.SetForbiddenValuesDir(forbidden)

	payload, _ := json.Marshal(map[string]any{"mode": "edit", "change": "x", "name": "app", "feedback_widget": false})
	job := &domain.Job{ID: uuid.New(), ProjectID: project, OrgID: org, BuildNo: 3, Payload: payload, Status: domain.JobQueued}
	m.runJob(context.Background(), job)

	select {
	case <-terminal:
	case <-time.After(10 * time.Second):
		t.Log("no terminal callback within 10s")
	}
	// reportToFTC runs on its own goroutine; let anything still in flight land.
	time.Sleep(200 * time.Millisecond)

	raw, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	run.events = strings.Split(strings.TrimSpace(string(raw)), "\n")
	run.store = st
	run.logs = logs.String()
	return run
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// indexOf is the position of the first event holding every part, -1 when none.
func indexOf(events []string, parts ...string) int {
next:
	for i, e := range events {
		for _, p := range parts {
			if !strings.Contains(e, p) {
				continue next
			}
		}
		return i
	}
	return -1
}

const tlsDelivery = `{"schema_version":1,"database":true,"database_mode":"external-tls-only","database_ca_file":"/run/fitt-ca/ca.pem"}`
const wantImage = "172.168.1.234:5050/fitt/ftcdv-uat/crn-demo-app-"

func hasStatus(run pipelineRun, status string) bool {
	return indexOf(run.events, "callback "+status) >= 0
}

// The order Fittcore asked for, from a real run of the build path: the amd64 image
// is built and pushed, the registry says which digest v3 is, the checker runs on
// that digest, and only then does "released" go out.
func TestReleasedGoesOutOnlyAfterTheCheckerPassedOnTheRegistryDigest(t *testing.T) {
	run := runPipeline(t, "passed", tlsDelivery, nil)

	order := []([]string){
		{"callback building"},
		{"node verify-delivery.mjs"},
		{"docker build --platform linux/amd64"},
		{"docker push", ":v3"},
		{"docker manifest inspect", ":v3"},
		{"docker pull --platform linux/amd64", "@" + fakeDigest},
		{"node accept-candidate.mjs", "--image", "@" + fakeDigest},
		{"callback released"},
	}
	last := -1
	for _, parts := range order {
		i := indexOf(run.events, parts...)
		if i < 0 {
			t.Fatalf("no event with %v in:\n%s", parts, strings.Join(run.events, "\n"))
		}
		if i <= last {
			t.Fatalf("%v came at %d, before an earlier step at %d:\n%s", parts, i, last, strings.Join(run.events, "\n"))
		}
		last = i
	}
	if hasStatus(run, "failed") {
		t.Errorf("a failed callback went out:\n%s", strings.Join(run.events, "\n"))
	}
	if !strings.Contains(run.store.tag, ":v3") {
		t.Errorf("job tag = %q, want the v3 image", run.store.tag)
	}
	for _, b := range run.bodies {
		if b["status"] == "released" && b["image_digest"] != fakeDigest {
			t.Errorf("released carries digest %v, want the registry's %s", b["image_digest"], fakeDigest)
		}
	}
	t.Logf("order of events:\n%s", strings.Join(run.events, "\n"))
}

// Whatever goes wrong at the gate, Fittcore hears "failed", never "released", and
// the job is not recorded as done. v3 stays pushed: nothing deletes or retags it.
func TestACandidateThatDoesNotPassIsReportedFailedAndNeverReleased(t *testing.T) {
	for _, accept := range []string{"failed", "crash", "no-receipt", "wrong-image"} {
		t.Run(accept, func(t *testing.T) {
			run := runPipeline(t, accept, tlsDelivery, nil)
			if hasStatus(run, "released") {
				t.Fatalf("released went out:\n%s", strings.Join(run.events, "\n"))
			}
			if !hasStatus(run, "failed") {
				t.Fatalf("no failed callback:\n%s", strings.Join(run.events, "\n"))
			}
			for _, st := range run.store.statuses {
				if st == domain.JobDone {
					t.Errorf("job marked done")
				}
			}
			if strings.Contains(run.store.tag, ":v3") {
				t.Errorf("job tag = %q: a rejected image must not read as the build's image", run.store.tag)
			}
			if i := indexOf(run.events, "docker", "rmi"); i >= 0 {
				t.Errorf("the candidate was removed: %s", run.events[i])
			}
			if i := indexOf(run.events, "docker tag"); i >= 0 {
				t.Errorf("the candidate was retagged: %s", run.events[i])
			}
			for _, b := range run.bodies {
				if b["status"] == "failed" && b["image_ref"] != nil && strings.Contains(fmt.Sprint(b["image_ref"]), ":v3") {
					t.Errorf("failed callback names the rejected image as the build's image: %v", b["image_ref"])
				}
			}
		})
	}
}

// A registry that answers another digest than the one pushed stops the build
// before any check: the check would be on something else.
func TestADigestMismatchWithTheRegistryFailsTheBuild(t *testing.T) {
	run := runPipeline(t, "passed", tlsDelivery, map[string]string{"FAKE_REGISTRY_DIGEST": "sha256:" + strings.Repeat("9", 64)})
	if hasStatus(run, "released") || !hasStatus(run, "failed") {
		t.Fatalf("want failed and no released:\n%s", strings.Join(run.events, "\n"))
	}
	if indexOf(run.events, "node accept-candidate.mjs") >= 0 {
		t.Errorf("the gate ran on a digest the registry does not confirm")
	}
}

// Without the hash list the scan cannot run, so the candidate cannot pass.
func TestWithoutAForbiddenListTheCandidateFails(t *testing.T) {
	run := runPipeline(t, "passed", tlsDelivery, map[string]string{"NO_FORBIDDEN_LIST": "1"})
	if hasStatus(run, "released") || !hasStatus(run, "failed") {
		t.Fatalf("want failed and no released:\n%s", strings.Join(run.events, "\n"))
	}
}

// Every other app keeps the path it had: no second check, released after the push.
func TestAnAppOutsideExternalTLSModeReleasesAsBefore(t *testing.T) {
	run := runPipeline(t, "failed", `{"schema_version":1,"database":true}`, nil)
	if indexOf(run.events, "node accept-candidate.mjs") >= 0 || indexOf(run.events, "docker manifest inspect") >= 0 {
		t.Errorf("a local-database app went through the candidate gate:\n%s", strings.Join(run.events, "\n"))
	}
	if !hasStatus(run, "released") {
		t.Errorf("no released callback:\n%s", strings.Join(run.events, "\n"))
	}
}
