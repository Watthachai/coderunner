package jobs

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// Builds of different projects run side by side, up to the slot cap, whatever lane their
// images go to. Since 23 Nov no build waits for another's: not to start, not to push.

// queueStore is the queue as dispatch reads it: queued jobs in order, a lock per project.
type queueStore struct {
	domain.Store
	mu     sync.Mutex
	queued []*domain.Job
	locked map[uuid.UUID]bool
}

func (s *queueStore) NextRunnableJob(context.Context) (*domain.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.queued {
		if !s.locked[j.ProjectID] {
			return j, nil
		}
	}
	return nil, nil
}

func (s *queueStore) AcquireProjectLock(_ context.Context, project uuid.UUID) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locked[project] {
		return nil, domain.ErrProjectLocked
	}
	s.locked[project] = true
	for i, j := range s.queued {
		if j.ProjectID == project {
			s.queued = append(s.queued[:i], s.queued[i+1:]...)
			break
		}
	}
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.locked, project)
	}, nil
}

func TestTwoGatedBuildsRunSideBySide(t *testing.T) {
	store := &queueStore{locked: map[uuid.UUID]bool{}}
	org := uuid.New()
	for i := 0; i < 2; i++ {
		store.queued = append(store.queued, &domain.Job{ID: uuid.New(), ProjectID: uuid.New(), OrgID: org, BuildNo: 1, Status: domain.JobQueued})
	}
	m := &manager{
		store:              store,
		logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		slots:              make(chan struct{}, 2),
		imageRegistry:      "172.168.1.234:5050/fitt/ftcdv-uat",
		pushWindowProjects: map[string]bool{"fitt/ftcdv-uat": true},
	}
	started := make(chan uuid.UUID, 2)
	release := make(chan struct{})
	defer close(release)
	m.runJobFn = func(ctx context.Context, job *domain.Job) {
		started <- job.ID
		<-release
	}
	m.dispatch(context.Background())
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d of 2 gated builds started", i)
		}
	}
}

// A build that is not gated runs beside a gated one.
func TestAnUngatedBuildRunsBesideAGatedOne(t *testing.T) {
	store := &queueStore{locked: map[uuid.UUID]bool{}}
	gated := &domain.Job{ID: uuid.New(), ProjectID: uuid.New(), OrgID: uuid.New(), BuildNo: 1, Status: domain.JobQueued}
	plain := &domain.Job{ID: uuid.New(), ProjectID: uuid.New(), OrgID: uuid.New(), BuildNo: 1, Status: domain.JobQueued}
	store.queued = []*domain.Job{gated, plain}
	m := &manager{
		store:              store,
		logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		slots:              make(chan struct{}, 2),
		imageRegistry:      "172.168.1.234:5050/fitt/demos",
		orgRegistries:      map[uuid.UUID]string{gated.OrgID: "172.168.1.234:5050/fitt/ftcdv-uat"},
		pushWindowProjects: map[string]bool{"fitt/ftcdv-uat": true},
	}
	started := make(chan uuid.UUID, 2)
	release := make(chan struct{})
	m.runJobFn = func(ctx context.Context, job *domain.Job) {
		started <- job.ID
		<-release
	}
	m.dispatch(context.Background())
	seen := map[uuid.UUID]bool{}
	for i := 0; i < 2; i++ {
		select {
		case id := <-started:
			seen[id] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("the plain build did not start beside the gated one: %v", seen)
		}
	}
	close(release)
}
