package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

type rehearsalJobs struct {
	domain.JobManager
	plain int
	with  []domain.EnqueueOptions
}

func (j *rehearsalJobs) Enqueue(context.Context, uuid.UUID, uuid.UUID, []byte) (*domain.Job, error) {
	j.plain++
	return &domain.Job{ID: uuid.New(), BuildNo: 3}, nil
}

func (j *rehearsalJobs) EnqueueWith(_ context.Context, _, _ uuid.UUID, _ []byte, o domain.EnqueueOptions) (*domain.Job, error) {
	j.with = append(j.with, o)
	return &domain.Job{ID: o.JobID, BuildNo: 3, Rehearsal: o.Rehearsal}, nil
}

type freezeStore struct {
	orgStore
	frozen   map[uuid.UUID]string
	allowJob map[uuid.UUID]uuid.UUID
}

func (s *freezeStore) FreezeProject(_ context.Context, id uuid.UUID, reason string, allow uuid.UUID) error {
	s.frozen[id], s.allowJob[id] = reason, allow
	return nil
}

func (s *freezeStore) UnfreezeProject(_ context.Context, id uuid.UUID) error {
	delete(s.frozen, id)
	delete(s.allowJob, id)
	return nil
}

func withID(req *http.Request, id uuid.UUID) *http.Request {
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", id.String())
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
}

func rehearsalServer() (*server, *freezeStore, *rehearsalJobs, uuid.UUID) {
	pid := uuid.New()
	st := &freezeStore{
		orgStore: orgStore{projects: map[uuid.UUID]*domain.Project{pid: {ID: pid, OrgID: uuid.New(), Name: "NTC Film Job"}}},
		frozen:   map[uuid.UUID]string{}, allowJob: map[uuid.UUID]uuid.UUID{},
	}
	jm := &rehearsalJobs{}
	return &server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), store: st, jm: jm}, st, jm, pid
}

// An edit may name its job id in advance and mark itself a rehearsal; both go
// to the job manager together. An edit that says neither is the usual one.
func TestEditCarriesAnAllocatedJobIDAndTheRehearsalSwitch(t *testing.T) {
	s, _, jm, pid := rehearsalServer()
	jobID := uuid.New()

	w := httptest.NewRecorder()
	body := `{"change":"x","job_id":"` + jobID.String() + `","rehearsal":true}`
	s.handleEditProject(w, withID(httptest.NewRequest("POST", "/internal/projects/x/edit", strings.NewReader(body)), pid))
	if w.Code != 202 {
		t.Fatalf("rehearsal edit: %d %s", w.Code, w.Body.String())
	}
	if len(jm.with) != 1 || jm.with[0].JobID != jobID || !jm.with[0].Rehearsal || jm.plain != 0 {
		t.Fatalf("enqueue options = %+v (plain %d), want job %s as a rehearsal", jm.with, jm.plain, jobID)
	}
	if !strings.Contains(w.Body.String(), jobID.String()) || !strings.Contains(w.Body.String(), `"rehearsal":true`) {
		t.Errorf("response does not name the job and the switch: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	s.handleEditProject(w, withID(httptest.NewRequest("POST", "/internal/projects/x/edit", strings.NewReader(`{"change":"y"}`)), pid))
	if w.Code != 202 || jm.plain != 1 || len(jm.with) != 1 {
		t.Fatalf("plain edit: %d, plain %d, with %d — want the usual Enqueue", w.Code, jm.plain, len(jm.with))
	}

	w = httptest.NewRecorder()
	s.handleEditProject(w, withID(httptest.NewRequest("POST", "/internal/projects/x/edit", strings.NewReader(`{"change":"z","job_id":"not-a-uuid"}`)), pid))
	if w.Code != 400 {
		t.Errorf("bad job_id: %d, want 400", w.Code)
	}
}

// An operator freezes a project so that only one named job may run, and lifts
// the freeze later; a freeze must name its reason and the job it allows.
func TestFreezeAndUnfreezeAProject(t *testing.T) {
	s, st, _, pid := rehearsalServer()
	allow := uuid.New()

	w := httptest.NewRecorder()
	body := `{"reason":"NTC build 3 rehearsal","allow_job_id":"` + allow.String() + `"}`
	s.handleFreezeProject(w, withID(httptest.NewRequest("PUT", "/internal/projects/x/freeze", strings.NewReader(body)), pid))
	if w.Code != 200 || st.frozen[pid] != "NTC build 3 rehearsal" || st.allowJob[pid] != allow {
		t.Fatalf("freeze: %d %s; stored %q allow %s", w.Code, w.Body.String(), st.frozen[pid], st.allowJob[pid])
	}

	for _, bad := range []string{`{"allow_job_id":"` + allow.String() + `"}`, `{"reason":"r"}`, `{"reason":"r","allow_job_id":"x"}`} {
		w = httptest.NewRecorder()
		s.handleFreezeProject(w, withID(httptest.NewRequest("PUT", "/internal/projects/x/freeze", strings.NewReader(bad)), pid))
		if w.Code != 400 {
			t.Errorf("freeze %s: %d, want 400", bad, w.Code)
		}
	}

	w = httptest.NewRecorder()
	s.handleUnfreezeProject(w, withID(httptest.NewRequest("DELETE", "/internal/projects/x/freeze", nil), pid))
	if w.Code != 200 {
		t.Fatalf("unfreeze: %d %s", w.Code, w.Body.String())
	}
	if _, still := st.frozen[pid]; still {
		t.Error("project still frozen after unfreeze")
	}
}
