package jobs

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
)

// DV's auto-deploy (from about 12 Dec 2026 for OKR) takes a build only when the released
// callback and build_done name the same build, field by field (asked 27 Nov 2026).
var identityFields = []string{"job_id", "build_no", "image_digest", "image_ref_canonical", "revision", "image_platform"}

func TestTheReleasedCallbackAndBuildDoneNameTheSameBuild(t *testing.T) {
	run := runPipeline(t, "passed", tlsDelivery, nil)
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
	released := bodyOf(t, run, "released")
	if run.done == nil {
		t.Fatal("no build_done payload")
	}
	commit := run.head(t)
	want := map[string]any{
		"image_digest":        fakeDigest,
		"image_ref_canonical": wantImage + jobSuffix(t, released) + "@" + fakeDigest,
		"revision":            commit,
		"image_platform":      "linux/amd64",
		"git_commit":          commit,
	}
	for k, v := range want {
		if released[k] != v {
			t.Errorf("released %s = %v, want %v", k, released[k], v)
		}
	}
	for _, k := range identityFields {
		if released[k] == nil {
			t.Errorf("released has no %s", k)
			continue
		}
		if !jsonEqual(released[k], run.done[k]) {
			t.Errorf("%s: released says %v, build_done says %v", k, released[k], run.done[k])
		}
	}
	if !jsonEqual(released["git_commit"], run.done["git_commit"]) {
		t.Errorf("git_commit: released says %v, build_done says %v", released["git_commit"], run.done["git_commit"])
	}
}

// "building" goes out before there is an image: it names the job and the build, and no
// image a deploy could take.
func TestBuildingNamesNoImage(t *testing.T) {
	body, err := callbackBody(uuid.New(), 4, "reg.test/crn/app:v4", "", "", "", "", "building", "", nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"image_digest", "image_ref_canonical", "image_platform", "revision", "inputs_purged_at"} {
		if _, ok := got[k]; ok {
			t.Errorf("building carries %s: %v", k, got[k])
		}
	}
	if got["job_id"] == nil || got["build_no"] != float64(4) {
		t.Errorf("building must name the job and the build: %v", got)
	}
}

func TestTheCanonicalRefIsTheRepositoryAtTheDigest(t *testing.T) {
	const d = "sha256:714a220cd8777936c0e67865c20a9340f2494de2d4fc732332e366e3e0448682"
	for ref, want := range map[string]string{
		"172.168.1.234:5050/fitt/ftcdv-uat/crn-demo-x-efa0ff04:v11": "172.168.1.234:5050/fitt/ftcdv-uat/crn-demo-x-efa0ff04@" + d,
		"reg.test/crn/app:v7":   "reg.test/crn/app@" + d,
		"reg.test/crn/app@" + d: "reg.test/crn/app@" + d,
		"localhost:5000/app":    "localhost:5000/app@" + d,
		"branch:crn/app-1":      "",
		"":                      "",
	} {
		if got := identify(uuid.Nil, 1, ref, d, "").ImageRefCanonical; got != want {
			t.Errorf("%q: canonical %q, want %q", ref, got, want)
		}
	}
	if got := identify(uuid.Nil, 1, "reg.test/crn/app:v7", "sha256:short", "").ImageRefCanonical; got != "" {
		t.Errorf("a digest that is not sha256:<64 hex> gives no canonical ref, got %q", got)
	}
	if got := identify(uuid.Nil, 1, "reg.test/crn/app:v7", d, "8bec6f2").Revision; got != "" {
		t.Errorf("revision is the full 40-hex commit or nothing, got %q", got)
	}
}

// DV asked on 25 Nov 2026 to know when CRN deleted what a request brought: the final
// callback says when, and the pictures are gone before it is sent.
func TestTheFinalCallbackSaysWhenThePicturesWereDeleted(t *testing.T) {
	start := time.Now().UTC().Truncate(time.Second)
	var dir string
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, change: onePictureChange, pictures: pictures(t),
		picturesSaved: func(t *testing.T, d string) { dir = d },
		agent:         func(a agentRun) { change(t, a.spec.WorkDir) }})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
	at := purgedAt(t, bodyOf(t, run, "released"))
	if at.Before(start) || at.After(time.Now().Add(time.Second)) {
		t.Errorf("inputs_purged_at %v is not during the build (from %v)", at, start)
	}
	if _, ok := bodyOf(t, run, "building")["inputs_purged_at"]; ok {
		t.Error("building says the pictures were deleted")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the pictures are still there: %v", err)
	}
}

func TestAFailedBuildsCallbackSaysWhenThePicturesWereDeleted(t *testing.T) {
	pics := pictures(t)
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, change: onePictureChange, pictures: pics,
		picturesSaved: func(t *testing.T, dir string) {
			if err := os.Remove(dir + "/01.png"); err != nil {
				t.Fatal(err)
			}
		},
		agent: func(a agentRun) { change(t, a.spec.WorkDir) }})
	failedAndNeverReleased(t, run)
	purgedAt(t, bodyOf(t, run, "failed"))
}

func TestABuildWithoutPicturesSaysNothingOfThem(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, change: onePictureChange,
		agent: func(a agentRun) { change(t, a.spec.WorkDir) }})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
	if v, ok := bodyOf(t, run, "released")["inputs_purged_at"]; ok {
		t.Errorf("a build that brought nothing says it deleted it at %v", v)
	}
}

var rfc3339UTC = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z$`)

func purgedAt(t *testing.T, body map[string]any) time.Time {
	t.Helper()
	s, _ := body["inputs_purged_at"].(string)
	if !rfc3339UTC.MatchString(s) {
		t.Fatalf("inputs_purged_at = %q, want an ISO 8601 UTC time", body["inputs_purged_at"])
	}
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func bodyOf(t *testing.T, run pipelineRun, status string) map[string]any {
	t.Helper()
	for _, b := range run.bodies {
		if b["status"] == status {
			return b
		}
	}
	t.Fatalf("no %s callback among %d", status, len(run.bodies))
	return nil
}

// jobSuffix is the repository's tail after wantImage ("<slug>-<id8>"), read from image_ref.
func jobSuffix(t *testing.T, body map[string]any) string {
	t.Helper()
	ref, _ := body["image_ref"].(string)
	m := regexp.MustCompile(regexp.QuoteMeta(wantImage) + `([^:@]+)`).FindStringSubmatch(ref)
	if m == nil {
		t.Fatalf("image_ref %q is not under %s", ref, wantImage)
	}
	return m[1]
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
