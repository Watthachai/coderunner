package api

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

type orgStore struct {
	domain.Store
	projects map[uuid.UUID]*domain.Project
}

func (s *orgStore) EnsureOrg(context.Context, *domain.Org) error { return nil }

func (s *orgStore) GetProject(_ context.Context, id uuid.UUID) (*domain.Project, error) {
	if p, ok := s.projects[id]; ok {
		return p, nil
	}
	return nil, domain.ErrNotFound
}

func (s *orgStore) CreateProject(_ context.Context, p *domain.Project) error {
	s.projects[p.ID] = p
	return nil
}

type orgJobs struct {
	domain.JobManager
	orgs []uuid.UUID
}

func (j *orgJobs) Enqueue(_ context.Context, _, orgID uuid.UUID, _ []byte) (*domain.Job, error) {
	j.orgs = append(j.orgs, orgID)
	return &domain.Job{ID: uuid.New(), BuildNo: 1}, nil
}

// A build is the org's that owns the project. The org decides where the image
// is pushed (CRN_ORG_IMAGE_REGISTRIES), so a re-export of an existing project
// that names another org must not move its image into that org's registry.
func TestIngestBuildsAnExistingProjectUnderItsOwnOrg(t *testing.T) {
	owner, other := uuid.New(), uuid.New()
	existing := uuid.New()
	store := &orgStore{projects: map[uuid.UUID]*domain.Project{existing: {ID: existing, OrgID: owner, Name: "Shop"}}}
	jobs := &orgJobs{}
	s := &server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), store: store, jm: jobs}

	post := func(projectID, orgID uuid.UUID) {
		t.Helper()
		body := `{"project_id":"` + projectID.String() + `","org_id":"` + orgID.String() + `","name":"Shop"}`
		w := httptest.NewRecorder()
		s.handleIngest(w, httptest.NewRequest("POST", "/internal/projects", strings.NewReader(body)))
		if w.Code != 202 {
			t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
		}
	}
	post(existing, other)
	fresh := uuid.New()
	post(fresh, other)

	if len(jobs.orgs) != 2 || jobs.orgs[0] != owner || jobs.orgs[1] != other {
		t.Errorf("job orgs = %v, want [%s (the owner) %s (a new project's own)]", jobs.orgs, owner, other)
	}
}
