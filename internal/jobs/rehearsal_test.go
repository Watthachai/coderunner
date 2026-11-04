package jobs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// countingFTC is an FTC DV callback endpoint that only counts what reaches it.
func countingFTC(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// A rehearsal job never reaches FTC DV, whatever its status: Fittcore must not
// see a job, a status change or an activity for it.
func TestReportToFTCWithholdsEveryStatusOfARehearsalJob(t *testing.T) {
	srv, hits := countingFTC(t)
	org := uuid.New()
	m := callbackManager(srv.URL, org)
	job := &domain.Job{ID: uuid.New(), OrgID: org, BuildNo: 3, Rehearsal: true}

	for _, status := range []string{"building", "released", "failed"} {
		m.reportToFTC(job, "reg/app:v3", "sha256:abc", "remote", "main", "2e000b5b7c3e9c256e461e984f6616716e1383e8", status, "", nil)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("rehearsal job sent %d callbacks, want 0", n)
	}
}

// The switch is off unless a job carries it: every other job of the same org
// reports as before.
func TestReportToFTCSendsForANormalJob(t *testing.T) {
	srv, hits := countingFTC(t)
	org := uuid.New()
	m := callbackManager(srv.URL, org)

	m.reportToFTC(&domain.Job{ID: uuid.New(), OrgID: org, BuildNo: 4}, "", "", "", "", "", "building", "", nil)
	if n := hits.Load(); n != 1 {
		t.Fatalf("normal job sent %d callbacks, want 1", n)
	}
}

// Every production callback goes through reportToFTC, so no path (build, cancel,
// failure, orphan recovery) can skip the rehearsal check.
func TestOnlyReportToFTCCallsFTCCallback(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	call := regexp.MustCompile(`\bm\.ftcCallback\(`)
	calls := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		calls += len(call.FindAll(src, -1))
	}
	if calls != 1 {
		t.Fatalf("m.ftcCallback( appears %d times in production code, want exactly 1 (inside reportToFTC)", calls)
	}
}

// recordingStore keeps the job Enqueue creates.
type recordingStore struct {
	domain.Store
	created *domain.Job
}

func (s *recordingStore) BumpBuildNo(context.Context, uuid.UUID) (int, error) { return 3, nil }
func (s *recordingStore) CreateJob(_ context.Context, j *domain.Job) error {
	s.created = j
	return nil
}

// An operator can allocate the job id before the build starts and mark the job
// as a rehearsal; a plain Enqueue stays a normal job with a fresh id.
func TestEnqueueWithCarriesTheAllocatedIDAndTheRehearsalSwitch(t *testing.T) {
	st := &recordingStore{}
	m := &manager{store: st, logger: callbackManager("", uuid.New()).logger, slots: make(chan struct{})}
	id := uuid.New()

	job, err := m.EnqueueWith(context.Background(), uuid.New(), uuid.New(), []byte(`{}`), domain.EnqueueOptions{JobID: id, Rehearsal: true})
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != id || st.created.ID != id {
		t.Errorf("job id = %s (stored %s), want %s", job.ID, st.created.ID, id)
	}
	if !st.created.Rehearsal {
		t.Error("stored job is not a rehearsal")
	}

	job, err = m.Enqueue(context.Background(), uuid.New(), uuid.New(), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if job.ID == uuid.Nil || job.ID == id || st.created.Rehearsal {
		t.Errorf("plain Enqueue gave id %s rehearsal %v, want a fresh id and a normal job", job.ID, st.created.Rehearsal)
	}
}
