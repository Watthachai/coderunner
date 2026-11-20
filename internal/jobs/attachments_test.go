package jobs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/attach"
	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// DV asked on 19 Nov 2026 to send the testers' screenshots with an edit request
// (docs/edit-attachments.md). The agent is shown each one; none goes into git or the
// image; they are deleted when the build ends, however it ends.

const onePictureChange = "FITTCORE-CHANGE-REQUEST v1 boundary=b1\n--b1 item=1 source=operator\nThe red box on the deal page\n--b1 end=1\n"

func pictures(t *testing.T) []domain.Attachment {
	t.Helper()
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{5}, 50)...)
	jpg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{6}, 50)...)
	input := func(name, mime string, b []byte) attach.Input {
		s := sha256.Sum256(b)
		return attach.Input{Name: name, Mime: mime, SHA256: hex.EncodeToString(s[:]), Bytes: base64.StdEncoding.EncodeToString(b)}
	}
	one := 1
	pin := input("pin.png", "image/png", png)
	pin.Item = &one
	got, err := attach.Decode([]attach.Input{pin, input("page.jpg", "image/jpeg", jpg)}, []int{1})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

var picturePath = regexp.MustCompile(`/\S+/0\d\.(?:png|jpg|webp)`)

func TestTheAgentIsShownEachPictureAndTheyGoWhenTheBuildEnds(t *testing.T) {
	var prompt string
	var seen []string
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, change: onePictureChange, pictures: pictures(t),
		agent: func(a agentRun) {
			prompt = a.spec.Prompt
			for _, p := range picturePath.FindAllString(prompt, -1) {
				if _, err := os.Stat(p); err == nil {
					seen = append(seen, p)
				}
			}
			change(t, a.spec.WorkDir)
		}})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
	pin, page := filepath.Join(run.pictureDir, "01.png"), filepath.Join(run.pictureDir, "02.jpg")
	for _, want := range []string{"item 1: " + pin, "the whole request: " + page, "Read tool", "Do not copy"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the agent's prompt lacks %q:\n%s", want, prompt)
		}
	}
	if len(seen) != 2 {
		t.Errorf("the agent could open %v, want both pictures", seen)
	}
	if _, err := os.Stat(run.pictureDir); !os.IsNotExist(err) {
		t.Errorf("the pictures are still there after the build: %v", err)
	}
	if !strings.Contains(traceText(run), "CRN ได้รับรูป 2 รูปกับคำขอนี้") {
		t.Errorf("the trace does not say the build had pictures:\n%s", traceText(run))
	}
}

func TestAPictureCopiedIntoTheProjectFailsTheBuild(t *testing.T) {
	pics := pictures(t)
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, change: onePictureChange, pictures: pics,
		agent: func(a agentRun) {
			change(t, a.spec.WorkDir)
			p := filepath.Join(a.spec.WorkDir, "public", "shot.png")
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, pics[0].Data, 0o644); err != nil {
				t.Fatal(err)
			}
		}})
	failedAndNeverReleased(t, run)
	if run.head(t) != run.seed {
		t.Error("the branch moved: a build holding a picture was pushed")
	}
	if msg := strings.Join(run.store.messages, "\n"); !strings.Contains(msg, "public/shot.png") || !strings.Contains(msg, "nothing was committed") {
		t.Errorf("the failure does not name the copy: %q", msg)
	}
	if _, err := os.Stat(run.pictureDir); !os.IsNotExist(err) {
		t.Errorf("a failed build left its pictures: %v", err)
	}
}

func TestAPictureGoneBeforeTheBuildFailsItBeforeTheAgentRuns(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, change: onePictureChange, pictures: pictures(t),
		picturesSaved: func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "01.png")); err != nil {
				t.Fatal(err)
			}
		},
		agent: func(a agentRun) { change(t, a.spec.WorkDir) }})
	failedAndNeverReleased(t, run)
	if run.agentRan {
		t.Error("the agent ran without the request's pictures")
	}
	if msg := strings.Join(run.store.messages, "\n"); !strings.Contains(msg, "01.png is missing") {
		t.Errorf("the failure does not say which picture is gone: %q", msg)
	}
	if _, err := os.Stat(run.pictureDir); !os.IsNotExist(err) {
		t.Errorf("the rest of the pictures are still there: %v", err)
	}
}

func TestTheReviewerIsShownThePicturesToo(t *testing.T) {
	var seen turns
	change := "FITTCORE-CHANGE-REQUEST v1 boundary=b1\n--b1 item=1 source=operator\n" + twoItemChange + "\n--b1 end=1\n"
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, change: change, reviewMin: 2, pictures: pictures(t),
		agent: reviewed(t, &seen, confirmBoth, func(a agentRun, _ int) { report(t, a, doneLabel(t, a.spec.WorkDir), answeredOwner) })})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
	specs := seen.all()
	if len(specs) != 2 || len(specs[1].Tools) == 0 {
		t.Fatalf("%d runs, want the builder and the reviewer", len(specs))
	}
	if !strings.Contains(specs[1].Prompt, "item 1: "+filepath.Join(run.pictureDir, "01.png")) {
		t.Errorf("the reviewer is not shown the pictures:\n%s", specs[1].Prompt)
	}
}

// pictureStore holds jobs for Enqueue, Cancel and the boot sweep.
type pictureStore struct {
	domain.Store
	mu        sync.Mutex
	jobs      map[uuid.UUID]*domain.Job
	createErr error
	onCreate  func(j *domain.Job)
}

func (s *pictureStore) BumpBuildNo(context.Context, uuid.UUID) (int, error) { return 4, nil }
func (s *pictureStore) CreateJob(_ context.Context, j *domain.Job) error {
	if s.onCreate != nil {
		s.onCreate(j)
	}
	if s.createErr != nil {
		return s.createErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[j.ID] = j
	return nil
}
func (s *pictureStore) GetJob(_ context.Context, id uuid.UUID) (*domain.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		return j, nil
	}
	return nil, domain.ErrNotFound
}
func (s *pictureStore) UpdateJobStatus(_ context.Context, id uuid.UUID, st domain.JobStatus, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[id].Status = st
	return nil
}
func (s *pictureStore) FailOrphanedBuilds(context.Context, string) ([]*domain.Job, error) {
	return nil, nil
}

func pictureManager(t *testing.T, st *pictureStore) *manager {
	t.Helper()
	m := &manager{store: st, notifier: nopNotifier{}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		slots: make(chan struct{}), subs: map[uuid.UUID][]*subscriber{}, cancels: map[uuid.UUID]context.CancelFunc{}}
	m.SetAttachmentsDir(t.TempDir())
	return m
}

// The pictures are in place before the job exists, so no worker can start it without
// them; a job that cannot be recorded leaves none behind.
func TestEnqueueKeepsThePicturesBeforeTheJobIsQueued(t *testing.T) {
	pics := pictures(t)
	st := &pictureStore{jobs: map[uuid.UUID]*domain.Job{}}
	m := pictureManager(t, st)
	var atCreate error
	st.onCreate = func(j *domain.Job) { atCreate = m.attach.Check(j.ID, pics) }

	job, err := m.EnqueueWith(context.Background(), uuid.New(), uuid.New(), []byte(`{}`), domain.EnqueueOptions{Attachments: pics})
	if err != nil {
		t.Fatal(err)
	}
	if atCreate != nil {
		t.Fatalf("the pictures were not in place when the job was recorded: %v", atCreate)
	}
	if err := m.attach.Check(job.ID, pics); err != nil {
		t.Fatalf("after Enqueue: %v", err)
	}

	st.createErr = errors.New("database down")
	st.onCreate = nil
	id := uuid.New()
	if _, err := m.EnqueueWith(context.Background(), uuid.New(), uuid.New(), []byte(`{}`), domain.EnqueueOptions{JobID: id, Attachments: pics}); err == nil {
		t.Fatal("Enqueue reported success with the database down")
	}
	if _, err := os.Stat(m.attach.Dir(id)); !os.IsNotExist(err) {
		t.Fatalf("a job never recorded left its pictures: %v", err)
	}

	bare := &manager{store: &pictureStore{jobs: map[uuid.UUID]*domain.Job{}}, logger: m.logger}
	if _, err := bare.EnqueueWith(context.Background(), uuid.New(), uuid.New(), []byte(`{}`), domain.EnqueueOptions{Attachments: pics}); err == nil {
		t.Fatal("a CRN with nowhere to keep pictures took them")
	}
}

func TestCancellingAQueuedJobDeletesItsPictures(t *testing.T) {
	st := &pictureStore{jobs: map[uuid.UUID]*domain.Job{}}
	m := pictureManager(t, st)
	job, err := m.EnqueueWith(context.Background(), uuid.New(), uuid.New(), []byte(`{}`), domain.EnqueueOptions{Attachments: pictures(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Cancel(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.attach.Dir(job.ID)); !os.IsNotExist(err) {
		t.Fatalf("a cancelled job kept its pictures: %v", err)
	}
}

// At boot, only a job still waiting to run keeps its pictures.
func TestBootDeletesThePicturesOfEveryJobNotWaiting(t *testing.T) {
	st := &pictureStore{jobs: map[uuid.UUID]*domain.Job{}}
	m := pictureManager(t, st)
	waiting, ended, unknown := uuid.New(), uuid.New(), uuid.New()
	st.jobs[waiting] = &domain.Job{ID: waiting, Status: domain.JobQueued}
	st.jobs[ended] = &domain.Job{ID: ended, Status: domain.JobFailed}
	for _, id := range []uuid.UUID{waiting, ended, unknown} {
		if err := m.attach.Save(id, pictures(t)); err != nil {
			t.Fatal(err)
		}
	}
	m.ReconcileOrphans(context.Background())
	if _, err := os.Stat(m.attach.Dir(waiting)); err != nil {
		t.Errorf("a waiting job lost its pictures: %v", err)
	}
	for _, id := range []uuid.UUID{ended, unknown} {
		if _, err := os.Stat(m.attach.Dir(id)); !os.IsNotExist(err) {
			t.Errorf("job %s is not waiting and kept its pictures: %v", id, err)
		}
	}
}

// DV asked on 20 Nov 2026 that a customer's pictures not ride along into later builds:
// the pictures are in the history of the session the build resumed, so once a build with
// pictures has run, the project's next build starts a new session.
func TestTheBuildAfterOneWithPicturesStartsANewSession(t *testing.T) {
	resumed := "unset"
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, change: onePictureChange,
		lastSession: "s-before", picturesSince: true,
		agent: func(a agentRun) { resumed = a.spec.ResumeSessionID; change(t, a.spec.WorkDir) }})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
	if resumed != "" {
		t.Errorf("the build resumed %q, a session that holds pictures", resumed)
	}
	if !strings.Contains(traceText(run), "เริ่ม session ใหม่") {
		t.Errorf("the trace does not say the build started a new session:\n%s", traceText(run))
	}
	if run.store.lastSessionJob == uuid.Nil {
		t.Error("the build did not leave itself out when it looked for the last session")
	}
}

// A build with pictures keeps what the project's builds so far have done: it resumes the
// last session when no pictures have run since.
func TestABuildWithPicturesResumesTheSessionBeforeIt(t *testing.T) {
	var resumed string
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, change: onePictureChange, pictures: pictures(t),
		lastSession: "s-before",
		agent:       func(a agentRun) { resumed = a.spec.ResumeSessionID; change(t, a.spec.WorkDir) }})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
	if resumed != "s-before" {
		t.Errorf("the build resumed %q, want the session before it", resumed)
	}
	if strings.Contains(traceText(run), "เริ่ม session ใหม่") {
		t.Errorf("the trace says a new session for a build that resumed one:\n%s", traceText(run))
	}
}

// DV's own projects (OKR) keep their session after pictures: CRN_KEEP_SESSION_AFTER_PICTURES.
func TestAProjectKeptOnItsSessionResumesAfterPictures(t *testing.T) {
	var resumed string
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, change: onePictureChange,
		lastSession: "s-before", picturesSince: true, keepSession: true,
		agent: func(a agentRun) { resumed = a.spec.ResumeSessionID; change(t, a.spec.WorkDir) }})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
	if resumed != "s-before" {
		t.Errorf("a project kept on its session resumed %q", resumed)
	}
}
