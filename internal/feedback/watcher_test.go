package feedback

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
	"github.com/Watthachai/fitt-coderunner/internal/github"
)

type fakeStore struct {
	needing  []*domain.FeedbackRequest
	projects map[uuid.UUID]*domain.Project
	setCalls map[uuid.UUID]int
	loseRace bool // when true, SetFeedbackIssue reports the caller lost (won=false)
}

func (f *fakeStore) ListFeedbackNeedingIssue(ctx context.Context, limit int, exclude []uuid.UUID) ([]*domain.FeedbackRequest, error) {
	return f.needing, nil
}
func (f *fakeStore) GetProject(ctx context.Context, id uuid.UUID) (*domain.Project, error) {
	if p, ok := f.projects[id]; ok {
		return p, nil
	}
	return nil, domain.ErrNotFound
}
func (f *fakeStore) SetFeedbackIssue(ctx context.Context, id uuid.UUID, number int, url string) (bool, error) {
	if f.setCalls == nil {
		f.setCalls = map[uuid.UUID]int{}
	}
	f.setCalls[id] = number
	f.needing = nil // once mirrored it no longer needs an issue
	return !f.loseRace, nil
}

type fakeIssuer struct{ created, closed int }

func (i *fakeIssuer) EnsureLabels(ctx context.Context, repoSlug string) error { return nil }
func (i *fakeIssuer) CreateIssue(ctx context.Context, repoSlug, title, body string, labels []string) (github.Issue, error) {
	i.created++
	n := 100 + i.created
	return github.Issue{Number: n, URL: "https://github.com/acme/repo/issues/" + strconv.Itoa(n)}, nil
}
func (i *fakeIssuer) CloseIssue(ctx context.Context, repoSlug string, number int, reason, comment string) error {
	i.closed++
	return nil
}

func TestWatcherMirrorsOnce(t *testing.T) {
	pid, fid := uuid.New(), uuid.New()
	st := &fakeStore{
		needing:  []*domain.FeedbackRequest{{ID: fid, ProjectID: pid, Category: "bug", Priority: "low"}},
		projects: map[uuid.UUID]*domain.Project{pid: {ID: pid, Name: "Shop", RepoURL: "https://github.com/acme/repo.git"}},
	}
	iss := &fakeIssuer{}
	w := NewWatcher(st, iss, "acme", time.Second, nil)

	w.reconcileOnce(context.Background())
	if iss.created != 1 {
		t.Fatalf("created=%d want 1", iss.created)
	}
	if st.setCalls[fid] != 101 {
		t.Fatalf("SetFeedbackIssue number=%d want 101", st.setCalls[fid])
	}
	w.reconcileOnce(context.Background()) // nothing needing now
	if iss.created != 1 {
		t.Fatalf("created=%d want still 1", iss.created)
	}
}

func TestWatcherClosesDuplicateOnLostRace(t *testing.T) {
	pid, fid := uuid.New(), uuid.New()
	st := &fakeStore{
		needing:  []*domain.FeedbackRequest{{ID: fid, ProjectID: pid, Category: "bug", Priority: "low"}},
		projects: map[uuid.UUID]*domain.Project{pid: {ID: pid, Name: "Shop", RepoURL: "https://github.com/acme/repo.git"}},
		loseRace: true,
	}
	iss := &fakeIssuer{}
	w := NewWatcher(st, iss, "acme", time.Second, nil)

	w.reconcileOnce(context.Background())
	if iss.created != 1 || iss.closed != 1 {
		t.Fatalf("lost race must close the duplicate: created=%d closed=%d (want 1,1)", iss.created, iss.closed)
	}
}

func TestWatcherSkipsUnresolvedProject(t *testing.T) {
	fid := uuid.New()
	st := &fakeStore{needing: []*domain.FeedbackRequest{{ID: fid, ProjectID: uuid.New()}}}
	iss := &fakeIssuer{}
	w := NewWatcher(st, iss, "acme", time.Second, nil)

	w.reconcileOnce(context.Background())
	w.reconcileOnce(context.Background())
	if iss.created != 0 {
		t.Fatalf("must not create for an unresolvable project (created=%d)", iss.created)
	}
	if !w.skip[fid] {
		t.Fatalf("unresolvable feedback should be marked skip")
	}
}
