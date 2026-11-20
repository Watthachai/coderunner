package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

const withPictures = `{"mode":"edit","change":"x","attachments":[{"name":"a.png","mime":"image/png","sha256":"ab","size":9,"file":"01.png"}]}`

// job queues a job with payload; started runs it (building, then failed), session
// records a session.
func job(t *testing.T, s domain.Store, p *domain.Project, org uuid.UUID, payload string, started bool, session string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	j := queueJob(t, s, p, org, uuid.New(), false)
	if _, err := s.(*pgStore).pool.Exec(ctx, `UPDATE project_jobs SET payload = $2 WHERE id = $1`, j.ID, json.RawMessage(payload)); err != nil {
		t.Fatal(err)
	}
	if started {
		for _, st := range []domain.JobStatus{domain.JobBuilding, domain.JobFailed} {
			if err := s.UpdateJobStatus(ctx, j.ID, st, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	if session != "" {
		if err := s.SetJobSession(ctx, j.ID, session); err != nil {
			t.Fatal(err)
		}
	}
	return j.ID
}

func lastSession(t *testing.T, s domain.Store, p *domain.Project, current uuid.UUID) (string, bool) {
	t.Helper()
	sid, pictures, err := s.LastSession(context.Background(), p.ID, current)
	if err != nil {
		t.Fatal(err)
	}
	return sid, pictures
}

// A build with pictures resumes the session before it; the build after it learns that
// pictures have run since, whether or not that build recorded a session, and a build
// that starts a new session ends it. Builds that never started, the asking build itself
// and other projects' builds do not count.
func TestLastSessionSaysWhenPicturesRanSince(t *testing.T) {
	s := openScratchStore(t)
	p, org := newProjectWithOrg(t, s, "Customer")
	other, otherOrg := newProjectWithOrg(t, s, "Other")
	const plain = `{"mode":"edit","change":"x"}`

	if sid, pictures := lastSession(t, s, p, uuid.New()); sid != "" || pictures {
		t.Fatalf("no builds yet: %q %v", sid, pictures)
	}
	job(t, s, p, org, plain, true, "s1")
	job(t, s, other, otherOrg, withPictures, true, "")

	current := job(t, s, p, org, withPictures, true, "")
	if sid, pictures := lastSession(t, s, p, current); sid != "s1" || pictures {
		t.Errorf("a build with pictures looking back: %q %v, want s1 and no pictures since", sid, pictures)
	}
	job(t, s, p, org, `{"mode":"edit","change":"x","attachments":[]}`, true, "")
	job(t, s, p, org, withPictures, false, "")
	if sid, pictures := lastSession(t, s, p, current); sid != "s1" || pictures {
		t.Errorf("no pictures, or never started: %q %v", sid, pictures)
	}

	if err := s.SetJobSession(context.Background(), current, "s1"); err != nil {
		t.Fatal(err)
	}
	if sid, pictures := lastSession(t, s, p, uuid.New()); sid != "s1" || !pictures {
		t.Errorf("after the build with pictures: %q %v, want s1 with pictures since", sid, pictures)
	}

	job(t, s, p, org, withPictures, true, "")
	job(t, s, p, org, plain, true, "s2")
	if sid, pictures := lastSession(t, s, p, uuid.New()); sid != "s2" || pictures {
		t.Errorf("after a new session: %q %v, want s2 and no pictures since", sid, pictures)
	}
	job(t, s, p, org, withPictures, true, "")
	if sid, pictures := lastSession(t, s, p, uuid.New()); sid != "s2" || !pictures {
		t.Errorf("a build with pictures that recorded no session: %q %v, want s2 with pictures since", sid, pictures)
	}
}
