package jobs

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

const mirrorDigest = "sha256:714a220cd8777936c0e67865c20a9340f2494de2d4fc732332e366e3e0448682"

// fakeDocker puts a `docker` running body first on PATH and returns the file its
// invocations are appended to.
func fakeDocker(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	script := "#!/bin/sh\necho \"$@\" >> " + args + "\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return args
}

func mirrorManager(imageRegistry, mirrorRegistry string) *manager {
	return &manager{
		imageRegistry:  imageRegistry,
		mirrorRegistry: mirrorRegistry,
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		hist:           map[uuid.UUID][]domain.BuildEventMsg{},
		subs:           map[uuid.UUID][]*subscriber{},
	}
}

func streamText(m *manager, id uuid.UUID) string {
	var b strings.Builder
	for _, e := range m.hist[id] {
		b.WriteString(e.Text + "\n")
	}
	return b.String()
}

func TestNoMirrorWithoutAMirrorRegistry(t *testing.T) {
	args := fakeDocker(t, "exit 1")
	m := mirrorManager("reg.test/fitt", "")
	job := &domain.Job{ID: uuid.New()}
	m.mirrorImage(context.Background(), "reg.test/fitt/app:v3", job, m.logger)
	if _, err := os.Stat(args); err == nil {
		t.Fatal("docker ran with no mirror registry configured")
	}
	if streamText(m, job.ID) != "" {
		t.Fatalf("published: %q", streamText(m, job.ID))
	}
}

func TestMirrorReportsTheUnchangedDigest(t *testing.T) {
	fakeDocker(t, `case "$*" in
  "image inspect"*) echo '["reg.test/fitt/app@`+mirrorDigest+`"]';;
  *"imagetools inspect"*) echo '{"digest":"`+mirrorDigest+`"}';;
esac`)
	m := mirrorManager("reg.test/fitt", "asia-southeast1-docker.pkg.dev/proj/crn")
	job := &domain.Job{ID: uuid.New()}
	m.mirrorImage(context.Background(), "reg.test/fitt/app:v3", job, m.logger)
	got := streamText(m, job.ID)
	if !strings.Contains(got, "asia-southeast1-docker.pkg.dev/proj/crn/app:v3") || !strings.Contains(got, mirrorDigest) || !strings.Contains(got, "digest unchanged") || strings.Contains(got, "failed") {
		t.Fatalf("stream: %q", got)
	}
}

// The delivery registry already holds the image the customer pulls; a mirror that
// fails is reported in the build's stream, and the build carries on.
func TestAFailedMirrorIsReportedNotFatal(t *testing.T) {
	fakeDocker(t, `case "$*" in
  "image inspect"*) echo '["reg.test/fitt/app@`+mirrorDigest+`"]';;
  push*) echo "denied: artifactregistry.repositories.uploadArtifacts" >&2; exit 1;;
esac`)
	m := mirrorManager("reg.test/fitt", "asia-southeast1-docker.pkg.dev/proj/crn")
	job := &domain.Job{ID: uuid.New()}
	m.mirrorImage(context.Background(), "reg.test/fitt/app:v3", job, m.logger)
	if got := streamText(m, job.ID); !strings.Contains(got, "mirror") || !strings.Contains(got, "denied") {
		t.Fatalf("stream: %q", got)
	}
}

// Fittcore's UAT pulls from its own registry project: a build of an org that has
// one is tagged and pushed there and stays there (no copy to the mirror), and
// every other org's build goes to the shared registry and is mirrored.
func TestAnOrgWithItsOwnRegistryIsPushedThere(t *testing.T) {
	args := fakeDocker(t, `case "$*" in
  "image inspect"*) echo '["reg.test/fitt/demos/crn-demo-hello-fitt-c0241b87@`+mirrorDigest+`"]';;
  *"imagetools inspect"*) echo '{"digest":"`+mirrorDigest+`"}';;
esac`)
	ftc, other := uuid.New(), uuid.New()
	m := mirrorManager("reg.test/fitt/demos", "asia-southeast1-docker.pkg.dev/proj/crn")
	m.orgRegistries = map[uuid.UUID]string{ftc: "reg.test/fitt/ftcdv-uat"}
	project := uuid.MustParse("c0241b87-01b9-4cf6-abda-028016d2908f")
	spec := payloadSpec{Name: "Hello Fitt"}

	ftcJob := &domain.Job{ID: uuid.New(), OrgID: ftc, ProjectID: project, BuildNo: 4}
	if got := m.imageTag(spec, ftcJob); got != "reg.test/fitt/ftcdv-uat/crn-demo-hello-fitt-c0241b87:v4" {
		t.Errorf("Fittcore tag = %q", got)
	}
	otherJob := &domain.Job{ID: uuid.New(), OrgID: other, ProjectID: project, BuildNo: 4}
	if got := m.imageTag(spec, otherJob); got != "reg.test/fitt/demos/crn-demo-hello-fitt-c0241b87:v4" {
		t.Errorf("other org tag = %q", got)
	}

	m.mirrorImage(context.Background(), "reg.test/fitt/ftcdv-uat/crn-demo-hello-fitt-c0241b87:v4", ftcJob, m.logger)
	if _, err := os.Stat(args); err == nil {
		t.Error("docker ran to mirror the build of an org with its own registry")
	}
	if got := streamText(m, ftcJob.ID); got != "" {
		t.Errorf("Fittcore build's stream: %q", got)
	}

	m.mirrorImage(context.Background(), "reg.test/fitt/demos/crn-demo-hello-fitt-c0241b87:v4", otherJob, m.logger)
	if got := streamText(m, otherJob.ID); !strings.Contains(got, "asia-southeast1-docker.pkg.dev/proj/crn/crn-demo-hello-fitt-c0241b87:v4@") {
		t.Errorf("other org's mirror stream: %q", got)
	}
}
