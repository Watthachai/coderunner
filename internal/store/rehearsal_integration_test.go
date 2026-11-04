package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

func newProjectWithOrg(t *testing.T, s domain.Store, name string) (*domain.Project, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	org := &domain.Org{ID: uuid.New(), Name: name + " org"}
	if err := s.EnsureOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	p := &domain.Project{ID: uuid.New(), OrgID: org.ID, Name: name}
	if err := s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	return p, org.ID
}

func queueJob(t *testing.T, s domain.Store, p *domain.Project, org uuid.UUID, id uuid.UUID, rehearsal bool) *domain.Job {
	t.Helper()
	buildNo, err := s.BumpBuildNo(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	j := &domain.Job{ID: id, ProjectID: p.ID, OrgID: org, Status: domain.JobQueued, BuildNo: buildNo, Payload: json.RawMessage(`{}`), Rehearsal: rehearsal}
	if err := s.CreateJob(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	return j
}

// The rehearsal switch is stored on the job and read back by every job read;
// a job inserted without it (FTC DV's own inserts included) is normal.
func TestRehearsalIsStoredPerJobAndDefaultsOff(t *testing.T) {
	s := openScratchStore(t)
	ctx := context.Background()
	p, org := newProjectWithOrg(t, s, "Rehearsal")

	rehearsal := queueJob(t, s, p, org, uuid.New(), true)
	normal := queueJob(t, s, p, org, uuid.Nil, false)

	got, err := s.GetJob(ctx, rehearsal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Rehearsal || got.ID != rehearsal.ID {
		t.Errorf("rehearsal job read back as id %s rehearsal %v", got.ID, got.Rehearsal)
	}
	got, err = s.GetJob(ctx, normal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rehearsal {
		t.Error("a job created without the switch reads back as a rehearsal")
	}
}

// A frozen project runs only the job the freeze allows; its other queued jobs
// wait (no failure, so nothing reports), and other projects are untouched.
func TestFrozenProjectRunsOnlyTheAllowedJob(t *testing.T) {
	s := openScratchStore(t)
	ctx := context.Background()
	frozen, org := newProjectWithOrg(t, s, "Frozen")
	other, otherOrg := newProjectWithOrg(t, s, "Other")

	waiting := queueJob(t, s, frozen, org, uuid.New(), false)
	allowedID := uuid.New()
	if err := s.FreezeProject(ctx, frozen.ID, "NTC build 3 rehearsal", allowedID); err != nil {
		t.Fatal(err)
	}

	next, err := s.NextRunnableJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if next != nil {
		t.Fatalf("frozen project's other job %s was offered to run", next.ID)
	}

	otherJob := queueJob(t, s, other, otherOrg, uuid.New(), false)
	next, err = s.NextRunnableJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if next == nil || next.ID != otherJob.ID {
		t.Fatalf("an unfrozen project's job was not offered: got %v", next)
	}
	if err := s.UpdateJobStatus(ctx, otherJob.ID, domain.JobDone, ""); err != nil {
		t.Fatal(err)
	}

	allowed := queueJob(t, s, frozen, org, allowedID, true)
	next, err = s.NextRunnableJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if next == nil || next.ID != allowed.ID {
		t.Fatalf("the allowed job was not offered: got %v", next)
	}
	if err := s.UpdateJobStatus(ctx, allowed.ID, domain.JobDone, ""); err != nil {
		t.Fatal(err)
	}

	if err := s.UnfreezeProject(ctx, frozen.ID); err != nil {
		t.Fatal(err)
	}
	next, err = s.NextRunnableJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if next == nil || next.ID != waiting.ID {
		t.Fatalf("after unfreeze the waiting job was not offered: got %v", next)
	}
}
