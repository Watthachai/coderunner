package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/buildsettings"
	"github.com/Watthachai/fitt-coderunner/internal/buildstep"
	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

const fakeDigest = "sha256:" + "ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12"

// A shell standing in for docker: it records each call and answers what the
// pipeline reads back. FAKE_REGISTRY_DIGEST is what the registry says v3 is.
const pipelineDocker = `#!/bin/sh
echo "docker $*" >> "$EVENTS"
echo "docker-config $1 ${DOCKER_CONFIG:-none}" >> "$CRN_TEST_DOCKER_LOG"
[ -n "$DOCKER_CONFIG" ] && [ -f "$DOCKER_CONFIG/config.json" ] && echo "docker-auth $1 $(cat "$DOCKER_CONFIG/config.json")" >> "$CRN_TEST_DOCKER_LOG"
[ "$1" = "ps" ] && cat "$FAKE_CONTAINERS" 2>/dev/null
case "$1 $2" in
  "image inspect") echo "[\"$(echo "$3" | sed 's/:v[0-9]*$//')@${FAKE_LOCAL_DIGEST}\"]" ;;
  "manifest inspect") echo "{\"Descriptor\":{\"digest\":\"${FAKE_REGISTRY_DIGEST}\"}}" ;;
esac
case "$1" in
  build)
    for context; do :; done
    cp -RP "$context" "$CONTEXT_COPY"
    [ -n "$FAKE_BUILD_OUTPUT" ] && echo "$FAKE_BUILD_OUTPUT"
    [ -n "$FAKE_BUILD_RUNS" ] && eval "$FAKE_BUILD_RUNS"
    [ -n "$FAKE_BUILD_FAILS" ] && exit 1
    ;;
  save)
    printf 'oci layout of %s' "$2"
    ;;
  push)
    [ -n "$FAKE_PUSH_OUTPUT" ] && echo "$FAKE_PUSH_OUTPUT"
    [ -n "$FAKE_PUSH_RUNS" ] && eval "$FAKE_PUSH_RUNS"
    [ -n "$FAKE_PUSH_FAILS" ] && exit 1
    ;;
esac
exit 0
`

// A shell standing in for crn-push (cmd/crn-push): it records the call and what it was
// given, and says the registry's digest. FAKE_HELPER_FAILS makes it refuse.
const fakeHelper = `#!/bin/sh
echo "crn-push $*" >> "$EVENTS"
cat > "$HELPER_STDIN"
env > "$HELPER_ENV"
if [ -n "$FAKE_HELPER_FAILS" ]; then echo "crn-push: refused: $FAKE_HELPER_FAILS" >&2; exit 1; fi
echo "pushed $2 ${FAKE_HELPER_DIGEST:-$FAKE_REGISTRY_DIGEST}"
`

// A shell standing in for Node: the release checker passes, and the candidate gate takes
// the list on stdin and writes the receipt FAKE_ACCEPT asks for.
const fakeNode = `#!/bin/sh
script=$(basename "$1")
echo "node $script $*" >> "$EVENTS"
if [ "$script" = "verify-delivery.mjs" ]; then
  [ -n "$FAKE_VERIFY_OUTPUT" ] && echo "$FAKE_VERIFY_OUTPUT"
  echo '{"status":"passed"}' > CRN_VERIFICATION.json
  exit 0
fi
cat > "$LIST_SEEN"
[ -n "$FAKE_ACCEPT_OUTPUT" ] && echo "$FAKE_ACCEPT_OUTPUT"
[ -n "$FAKE_ACCEPT_RUNS" ] && eval "$FAKE_ACCEPT_RUNS"
image=""; commit=""; receipt=""; project=""
while [ $# -gt 0 ]; do
  case "$1" in
    --image) image="$2" ;;
    --commit) commit="$2" ;;
    --project) project="$2" ;;
    --receipt) receipt="$2" ;;
  esac
  shift
done
case "$FAKE_ACCEPT" in
  passed) sed -e "s|@IMAGE@|$image|g" -e "s|@COMMIT@|$commit|g" -e "s|@PROJECT@|$project|g" "$FAKE_RECEIPT" > "$receipt"; exit 0 ;;
  crash) exit 3 ;;
  no-receipt) exit 0 ;;
  *) printf '{"status":"failed","image":"%s","commit":"%s","platform":"linux/amd64","error":"CA file is a symlink (migrate)"}' "$image" "$commit" > "$receipt"; exit 1 ;;
esac
`

// The list written by accept-candidate.mjs's forbiddenEntry. None of the values is real.
const (
	testValueHost = "203.0.113.250"
	testValueDB   = "fixture_db_x"
	testValueRole = "fixture_role_x"
	testLineHost  = "e3299a765ddf6ad382feefff38bd5ab2f62663bec6e7be74ccee7670e77d9138 13 1374833046"
	testLineDB    = "9d286135efc6346d32671c8654f7fd1afe4dcd5f50a6cdd84b9e136c30ab65a8 12 1501365033"
	testLineRole  = "0bb451f56996512d9bca2ce6040f6b40d94f6a2117f9f10540312a6285ee0543 14 614203677"
	testList      = "# Hashes. Holds no value.\n" + testLineHost + "\n" + testLineDB + "\n" + testLineRole + "\n"
)

// goodReceipt is what a passing gate writes, with the candidate's names left for the fake
// to fill in. Each test takes away or changes one part of it.
func goodReceipt() map[string]any {
	sum := sha256.Sum256([]byte(testList))
	var matrix []any
	for i := 0; i < buildstep.ExternalTLSCases; i++ {
		connections := any(0)
		if i%5 == 0 {
			connections = nil // a server-side case runs without the tripwire
		}
		matrix = append(matrix, map[string]any{"case": fmt.Sprintf("case %d", i), "path": "runtime", "expected": "fail", "result": "failed", "ok": true, "connections": connections, "leaks": []any{}})
	}
	return map[string]any{
		"schema_version": 1, "status": "passed", "image": "@IMAGE@", "commit": "@COMMIT@", "project": "@PROJECT@",
		"platform": "linux/amd64", "revision": "@COMMIT@",
		"artifact_scan": map[string]any{
			"list_sha256": hex.EncodeToString(sum[:]),
			"values":      map[string]any{"real": 3, "placeholders": 3},
			"hits":        []any{},
			"source":      map[string]any{"directories": 1, "files": 5, "symlinks": 0, "bytes": 10},
			"image": map[string]any{
				"layers": 2,
				"blobs":  []any{map[string]any{"where": "image blobs/sha256/aa", "encoding": "gzip", "bytes": 1}},
				"config": map[string]any{"digest": "sha256:bb", "env": 1, "labels": 1, "history": 1},
			},
		},
		"checker": map[string]any{
			"schema_version": 1, "status": "passed", "image": "@IMAGE@", "database_mode": "external-tls-only",
			"checks":       []any{map[string]any{"name": "delivery contract", "passed": true}},
			"external_tls": map[string]any{"must_fail": matrix},
		},
		"widget": map[string]any{"expected": false, "ran": true, "login_status": 200, "outside_requests": []any{}},
	}
}

// pipelineStore is the little of the store one build touches.
type pipelineStore struct {
	domain.Store
	mu       sync.Mutex
	statuses []domain.JobStatus
	messages []string
	tag      string
	trace    *domain.BuildTrace
}

func (s *pipelineStore) UpdateJobStatus(_ context.Context, _ uuid.UUID, st domain.JobStatus, msg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses = append(s.statuses, st)
	s.messages = append(s.messages, msg)
	return nil
}
func (s *pipelineStore) ListSkills(context.Context) ([]*domain.Skill, error) {
	return []*domain.Skill{{Name: "fitt-build", Enabled: true, Body: "x"}}, nil
}
func (s *pipelineStore) GetBuildSettings(context.Context) (*domain.BuildSettings, error) {
	return nil, domain.ErrNotFound
}
func (s *pipelineStore) SetJobClaudeConfig(context.Context, uuid.UUID, string, string) error {
	return nil
}
func (s *pipelineStore) SetJobSession(context.Context, uuid.UUID, string) error { return nil }
func (s *pipelineStore) ListBuildTraces(context.Context, uuid.UUID) ([]*domain.BuildTrace, error) {
	return nil, nil
}
func (s *pipelineStore) SetJobDockerTag(_ context.Context, _ uuid.UUID, tag string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tag = tag
	return nil
}
func (s *pipelineStore) LastSessionID(context.Context, uuid.UUID) (string, error) { return "", nil }
func (s *pipelineStore) SaveJobTrace(_ context.Context, tr *domain.BuildTrace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trace = tr
	return nil
}
func (s *pipelineStore) SetProjectBuildCache(context.Context, uuid.UUID, string, string) error {
	return nil
}

type nopNotifier struct{}

func (nopNotifier) Notify(context.Context, *domain.BuildEvent) error { return nil }

// recordingNotifier puts each build event in the order of everything else.
type recordingNotifier struct{ record func(string) }

func (n recordingNotifier) Notify(_ context.Context, e *domain.BuildEvent) error {
	n.record("event " + string(e.EventType))
	return nil
}

// fakeAgent stands in for Claude: it does what a test says, as the agent would, in the
// workspace and with the same OS user.
type fakeAgent struct {
	ran *bool
	do  func(agent agentRun)
	lg  func() *slog.Logger
	lst string
}

// agentRun is what a test's agent can reach.
type agentRun struct {
	t    *testing.T
	spec domain.RunSpec
	list string // the path of the project's forbidden list
	log  *slog.Logger
	emit func(domain.ClaudeEvent) error
}

func (f fakeAgent) Run(_ context.Context, spec domain.RunSpec, emit func(domain.ClaudeEvent) error) (domain.RunResult, error) {
	*f.ran = true
	if f.do != nil {
		f.do(agentRun{spec: spec, list: f.lst, log: f.lg(), emit: emit})
	}
	return domain.RunResult{Success: true, SessionID: "s1"}, nil
}
func (f fakeAgent) Improve(context.Context, string, string, string) (string, error) { return "", nil }

type pipelineRun struct {
	events        []string
	bodies        []map[string]any
	store         *pipelineStore
	logs          string
	remote        string
	branch        string
	seed          string // the commit the remote held before the build
	workDir       string
	context       string   // a copy of the docker build context
	dockerConfigs []string // what each docker command was given as its config
	credentialDir string   // the project's directory for the push credential
	listSeen      string   // what the gate got on stdin
	agentRan      bool
}

// head is the commit the remote's branch is on now.
func (r pipelineRun) head(t *testing.T) string {
	t.Helper()
	return git(t, r.remote, "rev-parse", "refs/heads/"+r.branch)
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

// pipelineOpts says what is different about one build. The zero value is a good edit
// build of an external-tls-only app with a good list, no agent, and a passing gate.
type pipelineOpts struct {
	accept   string
	delivery string
	env      map[string]string
	change   string
	agent    func(agentRun)         // when set, the build runs this as its agent
	receipt  func(r map[string]any) // changes the passing receipt
	list     func(t *testing.T, path string)
	noList   bool
	// before runs before the build, on the workspace an earlier build would have left.
	before func(t *testing.T, workDir, remote string)
	// credential replaces the operator: it runs once CRN says it is ready for a token,
	// and is given where the file goes. Nil leaves a good one at once.
	credential func(t *testing.T, path string)
	// noCredential leaves nothing, so the window runs out after a moment.
	noCredential bool
	// credentialFirst puts a file in place before the build starts.
	credentialFirst bool
	// maxConcurrent is the server's build slots; zero means one.
	maxConcurrent int
	// otherBuild, with maxConcurrent above one, holds a slot until the channel closes.
	otherBuild chan struct{}
	// strays stands in for the sweep of what the agent left.
	strays func() ([]string, error)
	// manager sees the manager before the build runs.
	manager func(m *manager)
	// machine answers each look at the machine; nil always says the same.
	machine func(look int) buildstep.MachineState
	// tags changes what GitLab says the project holds: the list so far, and whether the
	// image has been pushed.
	tags func(tags buildstep.RegistryTags, ref buildstep.ImageRef, pushed bool)
	// login answers whether the token can still push, for the n-th question; nil says no.
	login func(n int) (bool, error)
	// tagsErr is GitLab failing to answer about the project's tags.
	tagsErr func(pushed bool) error
	// queuedAgo says how long the job had been queued when it started.
	queuedAgo time.Duration
	// remoteHook is the remote's post-receive hook: the remote's own, not the workspace's.
	remoteHook func(workDir string) string
	// helper sets CRN's push helper to the fake crn-push, so no operator is involved.
	helper bool
}

func runPipeline(t *testing.T, accept, deliveryJSON string, env map[string]string) pipelineRun {
	t.Helper()
	return runPipelineWith(t, pipelineOpts{accept: accept, delivery: deliveryJSON, env: env})
}

// runPipelineWith drives one edit build through runJob, with docker, the checker and
// the agent faked, and a real git remote, list file and callback endpoint.
func runPipelineWith(t *testing.T, o pipelineOpts) pipelineRun {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if o.accept == "" {
		o.accept = "passed"
	}
	if o.delivery == "" {
		o.delivery = tlsDelivery
	}
	if o.change == "" {
		o.change = "x"
	}
	root := t.TempDir()
	events := filepath.Join(root, "events")
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"docker": pipelineDocker, "fake-node": fakeNode, "fake-crn-push": fakeHelper} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	receipt := goodReceipt()
	if o.receipt != nil {
		o.receipt(receipt)
	}
	rawReceipt, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receiptFile := filepath.Join(root, "receipt.template.json")
	if err := os.WriteFile(receiptFile, rawReceipt, 0o644); err != nil {
		t.Fatal(err)
	}
	contextCopy := filepath.Join(root, "context-copy")
	listSeen := filepath.Join(root, "list-seen")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("EVENTS", events)
	t.Setenv("FAKE_ACCEPT", o.accept)
	t.Setenv("FAKE_RECEIPT", receiptFile)
	t.Setenv("FAKE_LOCAL_DIGEST", fakeDigest)
	t.Setenv("FAKE_REGISTRY_DIGEST", fakeDigest)
	t.Setenv("CONTEXT_COPY", contextCopy)
	t.Setenv("LIST_SEEN", listSeen)
	t.Setenv("HELPER_STDIN", filepath.Join(root, "helper-stdin"))
	t.Setenv("HELPER_ENV", filepath.Join(root, "helper-env"))
	dockerConfigs := filepath.Join(root, "docker-configs")
	t.Setenv("CRN_TEST_DOCKER_LOG", dockerConfigs)
	containers := filepath.Join(root, "containers")
	if err := os.WriteFile(containers, []byte("old-container\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_CONTAINERS", containers)
	t.Setenv("CRN_VERIFY_NODE", filepath.Join(bin, "fake-node"))
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.test")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.test")
	for _, name := range []string{"FAKE_BUILD_OUTPUT", "FAKE_BUILD_RUNS", "FAKE_BUILD_FAILS", "FAKE_PUSH_OUTPUT", "FAKE_PUSH_FAILS", "FAKE_PUSH_RUNS", "FAKE_VERIFY_OUTPUT", "FAKE_ACCEPT_OUTPUT", "FAKE_ACCEPT_RUNS", "FAKE_HELPER_FAILS", "FAKE_HELPER_DIGEST"} {
		t.Setenv(name, "")
	}
	for k, v := range o.env {
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
		"crn-delivery.json": o.delivery,
		"crn.runtime.json":  `{"env":[]}`,
	} {
		if body == noDeliveryFile {
			continue
		}
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
	run := pipelineRun{remote: remote, branch: branch, seed: git(t, seed, "rev-parse", "HEAD"), workDir: filepath.Join(root, "projects", project.String()), context: contextCopy}
	if o.before != nil {
		o.before(t, run.workDir, remote)
	}
	if o.remoteHook != nil {
		hook := filepath.Join(remote, "hooks", "post-receive")
		if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(hook, []byte(o.remoteHook(run.workDir)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
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
	listPath := filepath.Join(forbidden, project.String()+".txt")
	t.Setenv("LIST_PATH", listPath)
	if !o.noList {
		if err := os.WriteFile(listPath, []byte(testList), 0o600); err != nil {
			t.Fatal(err)
		}
		if o.list != nil {
			o.list(t, listPath)
		}
	}

	org := uuid.New()
	st := &pipelineStore{}
	var logs strings.Builder
	sink := &syncWriter{w: &logs}
	logger := slog.New(slog.NewTextHandler(sink, nil))
	if testing.Verbose() {
		logger = slog.New(slog.NewTextHandler(io.MultiWriter(sink, os.Stderr), nil))
	}
	var mgr *manager
	var runner domain.ClaudeRunner
	if o.agent != nil {
		runner = fakeAgent{ran: &run.agentRan, do: func(a agentRun) { a.t = t; o.agent(a) }, lg: func() *slog.Logger { return mgr.logger }, lst: listPath}
	}
	slots := o.maxConcurrent
	if slots == 0 {
		slots = 1
	}
	mgr = NewManager(st, runner, recordingNotifier{record}, logger, filepath.Join(root, "projects"), remote, "", false, o.agent != nil, "", srv.URL, "", map[uuid.UUID]bool{org: true}, nil, true, "172.168.1.234:5050/fitt/ftcdv-uat", nil, "", "", slots, buildsettings.Defaults{}).(*manager)
	mgr.SetForbiddenValuesDir(forbidden)
	credentials := filepath.Join(root, "push-credentials")
	mgr.SetPushCredentialDir(credentials)
	mgr.pushCredentialPoll = 5 * time.Millisecond
	mgr.pushCredentialWait = 10 * time.Second
	if o.noCredential {
		mgr.pushCredentialWait = 300 * time.Millisecond
	}
	mgr.revokePoll = 5 * time.Millisecond
	mgr.pushWindowLimit = 3 * time.Second
	looks := 0
	mgr.captureMachine = func(context.Context) (buildstep.MachineState, error) {
		looks++
		if o.machine != nil {
			return o.machine(looks), nil
		}
		return buildstep.MachineState{"program docker": "same"}, nil
	}
	mgr.readRegistryTags = func(_ context.Context, ref buildstep.ImageRef) (buildstep.RegistryTags, error) {
		tags := buildstep.RegistryTags{
			ref.Repository:                  {"v1": "sha256:one", "v2": "sha256:two"},
			"fitt/ftcdv-uat/crn-demo-other": {"v1": "sha256:other"},
		}
		pushed := false
		if raw, err := os.ReadFile(events); err == nil {
			pushed = (strings.Contains(string(raw), "docker push") && os.Getenv("FAKE_PUSH_FAILS") == "") ||
				(strings.Contains(string(raw), "crn-push push") && os.Getenv("FAKE_HELPER_FAILS") == "")
		}
		if pushed {
			tags[ref.Repository][ref.Tag] = fakeDigest
		}
		if o.tagsErr != nil {
			if err := o.tagsErr(pushed); err != nil {
				return nil, err
			}
		}
		if o.tags != nil {
			o.tags(tags, ref, pushed)
		}
		return tags, nil
	}
	questions := 0
	mgr.loginCanPush = func(_ context.Context, _ buildstep.ImageRef, login string) (bool, error) {
		questions++
		if login != "test-user:test-token" {
			t.Errorf("asked about the login %q", login)
		}
		alive, err := false, error(nil)
		if o.login != nil {
			alive, err = o.login(questions)
		}
		if err == nil && !alive {
			record("gitlab: the token cannot push")
		}
		return alive, err
	}
	mgr.stopStrays = func(context.Context, time.Time, *slog.Logger) ([]string, error) {
		record("stop strays")
		if o.strays != nil {
			return o.strays()
		}
		return nil, nil
	}
	if o.helper {
		mgr.SetPushHelper([]string{filepath.Join(bin, "fake-crn-push")})
	}
	if o.manager != nil {
		o.manager(mgr)
	}
	if o.otherBuild != nil {
		mgr.slots <- struct{}{}
		go func() {
			<-o.otherBuild
			mgr.freeSlot()
		}()
	}
	run.credentialDir = filepath.Join(credentials, project.String())
	credentialPath := filepath.Join(run.credentialDir, "config.json")
	if o.credentialFirst {
		if err := os.MkdirAll(run.credentialDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(credentialPath, []byte(goodCredential), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CREDENTIAL_PATH", credentialPath)
	operator := make(chan struct{})
	operatorDone := make(chan struct{})
	go func() {
		defer close(operatorDone)
		if o.noCredential || o.credentialFirst || o.helper {
			return
		}
		ready := filepath.Join(run.credentialDir, "READY")
		for {
			select {
			case <-operator:
				return
			case <-time.After(2 * time.Millisecond):
			}
			if _, err := os.Stat(ready); err != nil {
				continue
			}
			record("operator: ready for a token")
			if o.credential != nil {
				o.credential(t, credentialPath)
			} else {
				putCredential(t, credentialPath, goodCredential)
			}
			return
		}
	}()

	payload, _ := json.Marshal(map[string]any{"mode": "edit", "change": o.change, "name": "app", "feedback_widget": false})
	job := &domain.Job{ID: uuid.New(), ProjectID: project, OrgID: org, BuildNo: 3, Payload: payload, Status: domain.JobQueued}
	if o.queuedAgo > 0 {
		job.QueuedAt = time.Now().Add(-o.queuedAgo)
	}
	mgr.slots <- struct{}{} // the slot the queue gives a build before it runs
	mgr.runJob(context.Background(), job)
	mgr.freeSlot()
	close(operator)
	<-operatorDone

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
	run.logs = sink.read(&logs)
	if seen, err := os.ReadFile(listSeen); err == nil {
		run.listSeen = string(seen)
	}
	if seen, err := os.ReadFile(dockerConfigs); err == nil {
		run.dockerConfigs = strings.Split(strings.TrimSpace(string(seen)), "\n")
	}
	return run
}

// failedAndNeverReleased is what every refusal looks like from outside. A real value
// never leaves in what Fittcore receives or in the job's recorded error.
func failedAndNeverReleased(t *testing.T, run pipelineRun) {
	t.Helper()
	if hasStatus(run, "released") {
		t.Errorf("released went out:\n%s", strings.Join(run.events, "\n"))
	}
	if !hasStatus(run, "failed") {
		t.Errorf("no failed callback:\n%s", strings.Join(run.events, "\n"))
	}
	for _, st := range run.store.statuses {
		if st == domain.JobDone {
			t.Errorf("job marked done")
		}
	}
	bodies, _ := json.Marshal(run.bodies)
	seen := string(bodies) + strings.Join(run.store.messages, "\n")
	for _, value := range []string{testValueHost, testValueDB, testValueRole} {
		if strings.Contains(seen, value) {
			t.Errorf("a real value left in a callback or the job's error: %s", seen)
		}
	}
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

// read takes what was written so far. A callback's goroutine can still be logging.
func (s *syncWriter) read(logs *strings.Builder) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return logs.String()
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

// goodCredential is what an operator's token looks like in the file: a docker config with
// one login, for the registry the build pushes to.
const (
	testToken      = "dGVzdC11c2VyOnRlc3QtdG9rZW4="
	goodCredential = `{"auths":{"172.168.1.234:5050":{"auth":"` + testToken + `"}}}`
)

// putCredential leaves the file the way the operator's script does: whole, in one rename.
func putCredential(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path+".tmp", []byte(body), 0o600); err != nil {
		t.Error(err)
		return
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Error(err)
	}
}

// noDeliveryFile as pipelineOpts.delivery seeds a branch with no crn-delivery.json.
const noDeliveryFile = "none"

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
	for _, accept := range []string{"failed", "crash", "no-receipt"} {
		t.Run(accept, func(t *testing.T) {
			run := runPipeline(t, accept, tlsDelivery, nil)
			failedAndNeverReleased(t, run)
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
	run := runPipelineWith(t, pipelineOpts{noList: true})
	failedAndNeverReleased(t, run)
	if run.head(t) != run.seed || indexOf(run.events, "docker build") >= 0 {
		t.Errorf("it went on to commit and build:\n%s", strings.Join(run.events, "\n"))
	}
}

// Every other app keeps the path it had: no second check, released after the push.
func TestAnAppOutsideExternalTLSModeReleasesAsBefore(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{accept: "failed", delivery: `{"schema_version":1,"database":true}`, noList: true})
	if indexOf(run.events, "node accept-candidate.mjs") >= 0 || indexOf(run.events, "docker manifest inspect") >= 0 {
		t.Errorf("a local-database app went through the candidate gate:\n%s", strings.Join(run.events, "\n"))
	}
	if !hasStatus(run, "released") {
		t.Errorf("no released callback:\n%s", strings.Join(run.events, "\n"))
	}
	i := indexOf(run.events, "docker build")
	if i < 0 || !strings.HasSuffix(run.events[i], " "+run.workDir) {
		t.Errorf("its build context moved from the workspace: %v", run.events[i])
	}
}

// --- 1. the list of forbidden values is pinned before the agent ----------------------------

// Whatever the agent does to the file, the build stops before it commits anything.
func TestAnAgentThatTouchesTheListFailsTheBuild(t *testing.T) {
	cases := map[string]func(a agentRun){
		"edits a hash": func(a agentRun) {
			text := strings.Replace(testList, "1374833046", "1374833047", 1)
			if err := os.WriteFile(a.list, []byte(text), 0o600); err != nil {
				a.t.Fatal(err)
			}
		},
		"takes a value out": func(a agentRun) {
			if err := os.WriteFile(a.list, []byte(testLineHost+"\n"+testLineDB+"\n"), 0o600); err != nil {
				a.t.Fatal(err)
			}
		},
		"swaps a value for another": func(a agentRun) {
			text := strings.Replace(testList, testLineRole, strings.Repeat("a", 64)+" 3 1", 1)
			if err := os.WriteFile(a.list, []byte(text), 0o600); err != nil {
				a.t.Fatal(err)
			}
		},
		"deletes it": func(a agentRun) {
			if err := os.Remove(a.list); err != nil {
				a.t.Fatal(err)
			}
		},
		"replaces it with a symlink": func(a agentRun) {
			other := filepath.Join(a.t.TempDir(), "other.txt")
			if err := os.WriteFile(other, []byte(testList), 0o600); err != nil {
				a.t.Fatal(err)
			}
			if err := os.Remove(a.list); err != nil {
				a.t.Fatal(err)
			}
			if err := os.Symlink(other, a.list); err != nil {
				a.t.Fatal(err)
			}
		},
		"writes the same bytes again as a new file": func(a agentRun) {
			if err := os.Remove(a.list); err != nil {
				a.t.Fatal(err)
			}
			if err := os.WriteFile(a.list, []byte(testList), 0o600); err != nil {
				a.t.Fatal(err)
			}
		},
		"opens it to others": func(a agentRun) {
			if err := os.Chmod(a.list, 0o644); err != nil {
				a.t.Fatal(err)
			}
		},
	}
	for name, touch := range cases {
		t.Run(name, func(t *testing.T) {
			run := runPipelineWith(t, pipelineOpts{agent: touch})
			failedAndNeverReleased(t, run)
			if !run.agentRan {
				t.Fatal("the agent never ran")
			}
			if run.head(t) != run.seed || indexOf(run.events, "docker build") >= 0 {
				t.Errorf("the build went on to commit and build:\n%s", strings.Join(run.events, "\n"))
			}
		})
	}
}

// Something left running after the agent is no different: each later step looks again.
func TestAListTouchedLaterAlsoFailsTheBuild(t *testing.T) {
	t.Run("during the image build", func(t *testing.T) {
		run := runPipelineWith(t, pipelineOpts{env: map[string]string{"FAKE_BUILD_RUNS": `chmod 644 "$LIST_PATH"`}})
		failedAndNeverReleased(t, run)
		if indexOf(run.events, "node accept-candidate.mjs") >= 0 {
			t.Errorf("the gate ran with a list that had moved")
		}
	})
	t.Run("during the gate", func(t *testing.T) {
		run := runPipelineWith(t, pipelineOpts{env: map[string]string{"FAKE_ACCEPT_RUNS": `rm "$LIST_PATH"`}})
		failedAndNeverReleased(t, run)
	})
}

// A list that is not as agreed stops the build before the agent costs anything.
func TestABadListStopsTheBuildBeforeTheAgent(t *testing.T) {
	cases := map[string]func(t *testing.T, path string){
		"a symlink": func(t *testing.T, path string) {
			other := filepath.Join(t.TempDir(), "other.txt")
			if err := os.WriteFile(other, []byte(testList), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(other, path); err != nil {
				t.Fatal(err)
			}
		},
		"mode 644": func(t *testing.T, path string) {
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"two values": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(testLineHost+"\n"+testLineDB+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"four values": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(testList+strings.Repeat("a", 64)+" 3 1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"a malformed line": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(testLineHost+"\n"+testLineDB+"\nhashes\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			run := runPipelineWith(t, pipelineOpts{list: spoil, agent: func(agentRun) {}})
			failedAndNeverReleased(t, run)
			if run.agentRan {
				t.Error("the agent ran on a bad list")
			}
		})
	}
}

// The gate works from the bytes pinned at the start, handed over on stdin.
func TestTheGateGetsThePinnedBytesAndNeverThePath(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", strings.Join(run.events, "\n"), run.logs)
	}
	if run.listSeen != testList {
		t.Errorf("the gate got %q on stdin, want the pinned list", run.listSeen)
	}
	i := indexOf(run.events, "node accept-candidate.mjs")
	if strings.Contains(run.events[i], "forbidden") || strings.Contains(run.events[i], ".txt") {
		t.Errorf("the gate was told where the file is: %s", run.events[i])
	}
}

// Changing what kind of app it is does not take a listed project past the gate.
func TestAnAgentCannotTakeAListedProjectOutOfTheGate(t *testing.T) {
	cases := map[string]func(a agentRun){
		"another database mode": func(a agentRun) {
			if err := os.WriteFile(filepath.Join(a.spec.WorkDir, "crn-delivery.json"), []byte(`{"schema_version":1,"database":true}`), 0o644); err != nil {
				a.t.Fatal(err)
			}
		},
		"no delivery file": func(a agentRun) {
			if err := os.Remove(filepath.Join(a.spec.WorkDir, "crn-delivery.json")); err != nil {
				a.t.Fatal(err)
			}
		},
		"not a Next app": func(a agentRun) {
			if err := os.WriteFile(filepath.Join(a.spec.WorkDir, "package.json"), []byte(`{"name":"app"}`), 0o644); err != nil {
				a.t.Fatal(err)
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			run := runPipelineWith(t, pipelineOpts{agent: change})
			failedAndNeverReleased(t, run)
			if run.head(t) != run.seed || indexOf(run.events, "docker build") >= 0 {
				t.Errorf("it went on to commit and build:\n%s", strings.Join(run.events, "\n"))
			}
		})
	}
}

// The mode that counts is the commit's. A delivery file kept only in the workspace passes
// the look before the commit, so the gate reads it again from what the image was built from.
func TestTheDatabaseModeIsReadFromTheCommit(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{delivery: noDeliveryFile, agent: func(a agentRun) {
		for name, body := range map[string]string{"crn-delivery.json": tlsDelivery, ".gitignore": "crn-delivery.json\n"} {
			if err := os.WriteFile(filepath.Join(a.spec.WorkDir, name), []byte(body), 0o644); err != nil {
				a.t.Fatal(err)
			}
		}
	}})
	failedAndNeverReleased(t, run)
	if indexOf(run.events, "node accept-candidate.mjs") >= 0 {
		t.Errorf("the gate ran on a commit that is not external-tls-only")
	}
}

// The commit starts from the branch, not from the index the agent left: a file the branch
// tracks stays in it, whatever the agent told its own index.
func TestAnAgentsIndexDoesNotDecideWhatIsCommitted(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{agent: func(a agentRun) {
		git(a.t, a.spec.WorkDir, "rm", "-q", "--cached", "crn-delivery.json")
		if err := os.WriteFile(filepath.Join(a.spec.WorkDir, ".gitignore"), []byte("crn-delivery.json\n"), 0o644); err != nil {
			a.t.Fatal(err)
		}
	}})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", strings.Join(run.events, "\n"), run.logs)
	}
	if got := git(t, run.remote, "show", run.head(t)+":crn-delivery.json"); got != tlsDelivery {
		t.Errorf("crn-delivery.json in the commit = %q", got)
	}
}

// --- 2. the image is built from the commit that was pushed ----------------------------------

// Whatever else lies in the workspace, Dockerfile's COPY . . sees only the commit.
func TestTheImageIsBuiltFromThePushedCommitOnly(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{agent: func(a agentRun) {
		for name, body := range map[string]string{
			".gitignore":     "ignored.env\n",
			"ignored.env":    "kept out of the commit\n",
			"build/left.txt": "excluded from every commit\n",
			"app.js":         "tracked\n",
		} {
			path := filepath.Join(a.spec.WorkDir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				a.t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				a.t.Fatal(err)
			}
		}
	}, remoteHook: func(workDir string) string {
		// The workspace moves on after the commit took its snapshot, as a process the agent
		// left behind could make it: a file that is untracked at build time, and an edit.
		return fmt.Sprintf("#!/bin/sh\necho untracked > %q\necho edited > %q\n", filepath.Join(workDir, "untracked.txt"), filepath.Join(workDir, "app.js"))
	}})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", strings.Join(run.events, "\n"), run.logs)
	}
	if raw, err := os.ReadFile(filepath.Join(run.workDir, "untracked.txt")); err != nil || len(raw) == 0 {
		t.Fatalf("the test did not leave an untracked file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(run.workDir, "ignored.env")); err != nil {
		t.Fatalf("the test did not leave an ignored file: %v", err)
	}
	for _, name := range []string{"untracked.txt", "ignored.env", "build/left.txt", ".git"} {
		if _, err := os.Lstat(filepath.Join(run.context, name)); err == nil {
			t.Errorf("the build context holds %s", name)
		}
	}
	if raw, _ := os.ReadFile(filepath.Join(run.context, "app.js")); string(raw) != "tracked\n" {
		t.Errorf("app.js in the context = %q, want the committed bytes", raw)
	}

	// The context is the commit the remote holds, file for file, and the label says so.
	head := run.head(t)
	if head == run.seed {
		t.Fatal("nothing was pushed")
	}
	tree := strings.Split(git(t, run.remote, "ls-tree", "-r", "--name-only", head), "\n")
	var context []string
	err := filepath.Walk(run.context, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(run.context, path)
			context = append(context, rel)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(tree)
	sort.Strings(context)
	if strings.Join(tree, ",") != strings.Join(context, ",") {
		t.Errorf("context %q, commit %q", context, tree)
	}
	i := indexOf(run.events, "docker build")
	if !strings.Contains(run.events[i], "org.opencontainers.image.revision="+head) {
		t.Errorf("the revision label is not the pushed commit %s: %s", head, run.events[i])
	}
	if j := indexOf(run.events, "node accept-candidate.mjs"); !strings.Contains(run.events[j], "--commit "+head) {
		t.Errorf("the gate ran on another commit: %s", run.events[j])
	}
	if strings.Contains(run.events[i], " "+run.workDir) {
		t.Errorf("the workspace was the build context: %s", run.events[i])
	}
}

// The commit the image is built from must be the one the remote holds. Here the remote
// itself puts the branch back after the push.
func TestACommitTheRemoteDoesNotHoldFailsTheBuild(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{agent: func(a agentRun) {
		if err := os.WriteFile(filepath.Join(a.spec.WorkDir, "app.js"), []byte("changed\n"), 0o644); err != nil {
			a.t.Fatal(err)
		}
	}, remoteHook: func(string) string {
		return "#!/bin/sh\nwhile read old new ref; do git update-ref \"$ref\" \"$old\"; done\n"
	}})
	failedAndNeverReleased(t, run)
	if !strings.Contains(failedMessage(run), "the remote's") {
		t.Errorf("the failure does not say why: %s", failedMessage(run))
	}
	if indexOf(run.events, "docker build") >= 0 {
		t.Errorf("it built from a commit the remote does not hold:\n%s", strings.Join(run.events, "\n"))
	}
}

// A file ignored by the commit cannot leave its mark on the labels either.
func TestLabelsAreReadFromTheCommitNotTheWorkspace(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{agent: func(a agentRun) {
		if err := os.WriteFile(filepath.Join(a.spec.WorkDir, "crn.schedule.json"), []byte(`[{"path":"/x","schedule":"1 * * * *"}]`), 0o644); err != nil {
			a.t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(a.spec.WorkDir, ".gitignore"), []byte("crn.schedule.json\n"), 0o644); err != nil {
			a.t.Fatal(err)
		}
	}})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", strings.Join(run.events, "\n"), run.logs)
	}
	i := indexOf(run.events, "docker build")
	if strings.Contains(run.events[i], "1 * * * *") {
		t.Errorf("a schedule the commit does not hold reached the labels: %s", run.events[i])
	}
}

// --- 3. what the commit's history holds -----------------------------------------------------

// A real value in what the build would commit stops it before the push: the remote never
// holds it.
func TestAValueInTheCommitIsReadBeforeThePush(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{agent: func(a agentRun) {
		if err := os.WriteFile(filepath.Join(a.spec.WorkDir, "notes.txt"), []byte("dial "+testValueHost+"\n"), 0o644); err != nil {
			a.t.Fatal(err)
		}
	}})
	failedAndNeverReleased(t, run)
	if !strings.Contains(failedMessage(run), "the commits this build added") {
		t.Errorf("the failure does not say where: %s", failedMessage(run))
	}
	if head := run.head(t); head != run.seed {
		t.Errorf("the remote moved to %s, a commit with a real value in it was pushed", head)
	}
	if indexOf(run.events, "docker build") >= 0 {
		t.Errorf("it went on to build:\n%s", strings.Join(run.events, "\n"))
	}
}

// What the push prints is read too, and stops the build before the image.
func TestARealValueThePushPrintsStopsTheBuild(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{agent: func(a agentRun) {
		if err := os.WriteFile(filepath.Join(a.spec.WorkDir, "app.js"), []byte("changed\n"), 0o644); err != nil {
			a.t.Fatal(err)
		}
	}, remoteHook: func(string) string {
		return "#!/bin/sh\necho 'dial " + testValueHost + "'\n"
	}})
	failedAndNeverReleased(t, run)
	if indexOf(run.events, "docker build") >= 0 {
		t.Errorf("it went on to build:\n%s", strings.Join(run.events, "\n"))
	}
}

// A value the agent committed and removed again stays in the workspace's .git. CRN commits
// the files it finds, on top of the branch, so that history never reaches the remote.
func TestAnAgentsOwnCommitsAreNotPushed(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{agent: func(a agentRun) {
		dir := a.spec.WorkDir
		if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("dial "+testValueHost+"\n"), 0o644); err != nil {
			a.t.Fatal(err)
		}
		git(a.t, dir, "add", "-A")
		git(a.t, dir, "commit", "-q", "-m", "note")
		if err := os.Remove(filepath.Join(dir, "notes.txt")); err != nil {
			a.t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte("kept\n"), 0o644); err != nil {
			a.t.Fatal(err)
		}
	}})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", strings.Join(run.events, "\n"), run.logs)
	}
	head := run.head(t)
	if parent := git(t, run.remote, "rev-parse", head+"^"); parent != run.seed {
		t.Errorf("the pushed commit's parent is %s, want the branch's head %s", parent, run.seed)
	}
	if got := git(t, run.remote, "show", head+":app.js"); got != "kept" {
		t.Errorf("app.js = %q", got)
	}
	if objects := git(t, run.remote, "rev-list", "--objects", "--all"); strings.Contains(objects, "notes.txt") {
		t.Errorf("the agent's commit reached the remote:\n%s", objects)
	}
}

func failedMessage(run pipelineRun) string {
	for _, b := range run.bodies {
		if b["status"] == "failed" {
			return fmt.Sprint(b["message"])
		}
	}
	return ""
}

// --- 4. everything printed is read before "released" ----------------------------------------

func TestARealValueInAnyLogFailsTheBuildBeforeReleased(t *testing.T) {
	printed := func(name string) pipelineOpts {
		return pipelineOpts{env: map[string]string{name: "connected to " + testValueHost + ":5432"}}
	}
	cases := map[string]struct {
		opts  pipelineOpts
		where string
	}{
		"the image build's output":     {printed("FAKE_BUILD_OUTPUT"), "docker build"},
		"the image push's output":      {printed("FAKE_PUSH_OUTPUT"), "docker push"},
		"the release checker's output": {printed("FAKE_VERIFY_OUTPUT"), "the release checker"},
		"the candidate gate's output":  {printed("FAKE_ACCEPT_OUTPUT"), "the candidate gate"},
		"the acceptance receipt":       {pipelineOpts{receipt: func(r map[string]any) { r["note"] = "role " + testValueRole }}, "the acceptance receipt"},
		"the agent's stdout": {pipelineOpts{agent: func(a agentRun) {
			if _, err := a.spec.StdoutTrace.Write([]byte(`{"type":"user","tool_result":"` + testValueDB + `"}`)); err != nil {
				a.t.Fatal(err)
			}
		}}, "the agent's output"},
		"the agent's stderr": {pipelineOpts{agent: func(a agentRun) {
			if _, err := a.spec.StderrTrace.Write([]byte(testValueDB)); err != nil {
				a.t.Fatal(err)
			}
		}}, "the agent's errors"},
		"the live events": {pipelineOpts{agent: func(a agentRun) {
			if err := a.emit(domain.ClaudeEvent{Type: domain.ClaudeAssistant, Text: "the host is " + testValueHost}); err != nil {
				a.t.Fatal(err)
			}
		}}, "the live events"},
		"CRN's own log": {pipelineOpts{agent: func(a agentRun) {
			a.log.Info("something", "value", testValueRole)
		}}, "CRN's log"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			run := runPipelineWith(t, c.opts)
			failedAndNeverReleased(t, run)
			if msg := failedMessage(run); !strings.Contains(msg, c.where) || !strings.Contains(msg, "real value #") {
				t.Errorf("the failure says %q, want where: %s", msg, c.where)
			}
		})
	}
}

// A real value in the request stops the build before it reaches the model.
func TestARealValueInTheRequestNeverReachesTheAgent(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{change: "use host " + testValueHost, agent: func(agentRun) {}})
	failedAndNeverReleased(t, run)
	if run.agentRan {
		t.Error("the agent got a real value")
	}
}

// The request names the placeholders by design, so logs may repeat them. Only the
// artifacts may not hold them, and the gate checks those.
func TestAPlaceholderInALogIsNotAFailure(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{
		change: "use WITHHELD_DB_HOST",
		agent:  func(a agentRun) { a.log.Info("change", "text", "WITHHELD_DB_NAME") },
		env:    map[string]string{"FAKE_BUILD_OUTPUT": "WITHHELD_DB_ROLE"},
	})
	if !hasStatus(run, "released") {
		t.Fatalf("no released: %s\n%s", failedMessage(run), strings.Join(run.events, "\n"))
	}
}

// A command that fails with a value in its output: the error would carry its tail to
// Fittcore, so the message is withheld.
func TestAFailureThatPrintsARealValueDoesNotSendIt(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{env: map[string]string{"FAKE_BUILD_OUTPUT": "cannot reach " + testValueHost, "FAKE_BUILD_FAILS": "1"}})
	failedAndNeverReleased(t, run)
	if msg := failedMessage(run); msg != withheldFailure {
		t.Errorf("the failure said %q", msg)
	}
}

// The payload about to go out is read too, as the callback would send it.
func TestTheCallbackAboutToGoOutIsRead(t *testing.T) {
	list := pinTestList(t)
	gate := &jobGate{list: list, scan: list.NewScanner(), accepted: true}
	if err := gate.beforeRelease([]byte(`{"status":"released","image_ref":"clean"}`)); err != nil {
		t.Fatalf("a clean payload: %v", err)
	}
	err := gate.beforeRelease([]byte(`{"status":"released","image_ref":"reg/` + testValueDB + `:v3"}`))
	if err == nil {
		t.Fatal("a payload holding a value went out")
	}
	if strings.Contains(err.Error(), testValueDB) || !strings.Contains(err.Error(), "callback") {
		t.Errorf("error = %v", err)
	}
}

func pinTestList(t *testing.T) *buildstep.ForbiddenList {
	t.Helper()
	path := filepath.Join(t.TempDir(), "list.txt")
	if err := os.WriteFile(path, []byte(testList), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := buildstep.PinForbiddenList(path)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// A gated job reaches "released" only through the gate, whatever path it took.
func TestAGateThatDidNotAcceptNeverReleases(t *testing.T) {
	list := pinTestList(t)
	gate := &jobGate{list: list, scan: list.NewScanner()}
	if err := gate.beforeRelease([]byte(`{}`)); err == nil {
		t.Fatal("released without an accepted candidate")
	}
}

// What a failure says to Fittcore never holds a real value, whatever failed.
func TestAFailureMessageIsWithheldWhenItHoldsARealValue(t *testing.T) {
	list := pinTestList(t)
	m := &manager{gates: map[uuid.UUID]*jobGate{}}
	id := uuid.New()
	m.gates[id] = &jobGate{list: list, scan: list.NewScanner()}
	if got := m.safeMessage(id, "docker build failed"); got != "docker build failed" {
		t.Errorf("a clean message changed: %q", got)
	}
	got := m.safeMessage(id, "cannot reach "+testValueHost)
	if strings.Contains(got, testValueHost) || got == "" {
		t.Errorf("safeMessage = %q", got)
	}
	if got := m.safeMessage(uuid.New(), "cannot reach "+testValueHost); !strings.Contains(got, testValueHost) {
		t.Errorf("a project with no list changed: %q", got)
	}
}

// --- 5. a receipt must hold every result ----------------------------------------------------

// dig walks a receipt along keys and calls f on the map holding the last one.
func dig(r map[string]any, path string, f func(parent map[string]any, key string)) {
	keys := strings.Split(path, ".")
	for _, k := range keys[:len(keys)-1] {
		r = r[k].(map[string]any)
	}
	f(r, keys[len(keys)-1])
}

func without(path string) func(map[string]any) {
	return func(r map[string]any) { dig(r, path, func(p map[string]any, k string) { delete(p, k) }) }
}

func set(path string, value any) func(map[string]any) {
	return func(r map[string]any) { dig(r, path, func(p map[string]any, k string) { p[k] = value }) }
}

func matrixOf(r map[string]any) []any {
	return r["checker"].(map[string]any)["external_tls"].(map[string]any)["must_fail"].([]any)
}

func TestAReceiptThatSaysPassedMustHoldEveryPart(t *testing.T) {
	first := func(change func(entry map[string]any)) func(map[string]any) {
		return func(r map[string]any) { change(matrixOf(r)[1].(map[string]any)) }
	}
	cases := map[string]func(map[string]any){
		"only status, image, commit and platform": func(r map[string]any) {
			for k := range r {
				if k != "status" && k != "image" && k != "commit" && k != "platform" {
					delete(r, k)
				}
			}
		},
		"no schema_version":         without("schema_version"),
		"schema_version 2":          set("schema_version", 2),
		"no project":                without("project"),
		"another project":           set("project", uuid.NewString()),
		"another image":             set("image", "reg/other@sha256:00"),
		"another commit":            set("commit", "0000"),
		"another platform":          set("platform", "linux/arm64"),
		"no revision":               without("revision"),
		"another revision":          set("revision", "0000"),
		"holds an error":            set("error", "something"),
		"no artifact scan":          without("artifact_scan"),
		"no list hash":              without("artifact_scan.list_sha256"),
		"another list":              set("artifact_scan.list_sha256", strings.Repeat("0", 64)),
		"no values":                 without("artifact_scan.values"),
		"no real count":             without("artifact_scan.values.real"),
		"one real value":            set("artifact_scan.values.real", 1),
		"no real value":             set("artifact_scan.values.real", 0),
		"no placeholder count":      without("artifact_scan.values.placeholders"),
		"two placeholders":          set("artifact_scan.values.placeholders", 2),
		"no hits list":              without("artifact_scan.hits"),
		"a hit":                     set("artifact_scan.hits", []any{map[string]any{"label": "real value #1", "where": "x"}}),
		"no source scan":            without("artifact_scan.source"),
		"no source file read":       set("artifact_scan.source.files", 0),
		"no image scan":             without("artifact_scan.image"),
		"no layer read":             set("artifact_scan.image.layers", 0),
		"no blob read":              set("artifact_scan.image.blobs", []any{}),
		"no config read":            without("artifact_scan.image.config"),
		"no checker":                without("checker"),
		"checker schema_version 2":  set("checker.schema_version", 2),
		"checker no schema_version": without("checker.schema_version"),
		"checker failed":            set("checker.status", "failed"),
		"checker no status":         without("checker.status"),
		"checker on another image":  set("checker.image", "reg/other@sha256:00"),
		"checker in another mode":   set("checker.database_mode", "local"),
		"no check listed":           set("checker.checks", []any{}),
		"a check did not pass":      set("checker.checks", []any{map[string]any{"name": "x", "passed": false}}),
		"no TLS result":             without("checker.external_tls"),
		"no TLS matrix":             without("checker.external_tls.must_fail"),
		"an empty TLS matrix":       set("checker.external_tls.must_fail", []any{}),
		"a TLS case missing": func(r map[string]any) {
			e := r["checker"].(map[string]any)["external_tls"].(map[string]any)
			e["must_fail"] = matrixOf(r)[1:]
		},
		"a TLS case too many": func(r map[string]any) {
			e := r["checker"].(map[string]any)["external_tls"].(map[string]any)
			e["must_fail"] = append(matrixOf(r), map[string]any{"case": "extra", "path": "runtime", "expected": "fail", "result": "failed", "ok": true, "leaks": []any{}})
		},
		"a TLS case counted twice": func(r map[string]any) {
			m := matrixOf(r)
			m[1] = m[2]
		},
		"a TLS case not ok":                first(func(e map[string]any) { e["ok"] = false }),
		"a TLS case without ok":            first(func(e map[string]any) { delete(e, "ok") }),
		"a TLS case that succeeded":        first(func(e map[string]any) { e["result"] = "passed" }),
		"a TLS case with a connection":     first(func(e map[string]any) { e["connections"] = 1 }),
		"a TLS case that printed a secret": first(func(e map[string]any) { e["leaks"] = []any{"password"} }),
		"no widget":                        without("widget"),
		"widget not expected to match":     set("widget.expected", true),
		"widget without expected":          without("widget.expected"),
		"widget did not run":               set("widget.ran", false),
		"widget without ran":               without("widget.ran"),
		"no outside list":                  without("widget.outside_requests"),
		"an outside request":               set("widget.outside_requests", []any{"http://192.0.2.1"}),
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			run := runPipelineWith(t, pipelineOpts{receipt: spoil})
			failedAndNeverReleased(t, run)
			if strings.Contains(run.store.tag, ":v3") {
				t.Errorf("job tag = %q: a rejected image must not read as the build's image", run.store.tag)
			}
		})
	}
}

// The whole receipt passes: the table above fails for its parts, not for everything.
func TestAWholeReceiptPasses(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{})
	if !hasStatus(run, "released") {
		t.Fatalf("no released: %s\n%s", failedMessage(run), run.logs)
	}
}

// v3 goes up once. A push that fails stops the build: nobody tries that tag again, and
// the next round is a new job and v4.
func TestAGatedBuildPushesItsTagOnce(t *testing.T) {
	// A window long enough for a retry to happen, if there were one.
	run := runPipelineWith(t, pipelineOpts{env: map[string]string{"FAKE_PUSH_FAILS": "1"}, manager: func(m *manager) { m.pushWindowLimit = 30 * time.Second }})
	failedAndNeverReleased(t, run)
	pushes := 0
	for _, e := range run.events {
		if strings.HasPrefix(e, "docker push") {
			pushes++
		}
	}
	if pushes != 1 {
		t.Errorf("%d pushes, want 1:\n%s", pushes, strings.Join(run.events, "\n"))
	}
}

// --- what the build summary needs -----------------------------------------------------------

// docker push mounts known layers by itself; the trace records which, from where.
func TestAMountedLayerGoesIntoTheTrace(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{env: map[string]string{"FAKE_PUSH_OUTPUT": "3c5439d0ae62: Mounted from fitt/ftcdv-uat/crn-demo-other-160001c3\nabc: Pushed"}})
	if !hasStatus(run, "released") {
		t.Fatalf("no released: %s", failedMessage(run))
	}
	found := false
	for _, e := range run.store.trace.Events {
		if strings.Contains(e.Text, "3c5439d0ae62: Mounted from fitt/ftcdv-uat/crn-demo-other-160001c3") {
			found = true
		}
		if strings.Contains(e.Text, "abc: Pushed") {
			t.Errorf("a layer that was not mounted went into the trace: %s", e.Text)
		}
	}
	if !found {
		t.Error("the mounted layer is not in the trace")
	}
}
