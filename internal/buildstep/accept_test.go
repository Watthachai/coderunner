package buildstep

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A real receipt, written by a real gate on a real amd64 image (CRN_TLS_E2E_RECEIPTS),
// must satisfy the Go side: the two sides name their fields apart, and a mismatch would
// only show at the end of a paid build.
func TestARealPassedReceiptSatisfiesTheValidator(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "receipt-passed.json"))
	if err != nil {
		t.Fatal(err)
	}
	listRaw, err := os.ReadFile(filepath.Join("testdata", "receipt-passed.list"))
	if err != nil {
		t.Fatal(err)
	}
	list, err := PinForbiddenList(writeList(t, string(listRaw), 0o600))
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Image, Commit, Project string
		Widget                 struct{ Expected bool }
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	a := Acceptance{Image: r.Image, Commit: r.Commit, ProjectID: r.Project, Widget: r.Widget.Expected, Forbidden: list}
	if err := validateReceipt(raw, a); err != nil {
		t.Fatalf("a real receipt was refused: %v", err)
	}
	wrong := a
	wrong.Commit = "another"
	if validateReceipt(raw, wrong) == nil {
		t.Error("the same receipt passed for another commit")
	}
	// AcceptCandidate reads the status first; the validator does not lean on that.
	failed := strings.Replace(string(raw), `"status": "passed"`, `"status": "failed"`, 1)
	if validateReceipt([]byte(failed), a) == nil {
		t.Error("a receipt that says failed passed the validator")
	}
}

// The list on the machine that runs the builds, when pointed at it, pins as it stands.
func TestTheListOnThisMachinePins(t *testing.T) {
	path := os.Getenv("CRN_REAL_FORBIDDEN_LIST")
	if path == "" {
		t.Skip("set CRN_REAL_FORBIDDEN_LIST")
	}
	list, err := PinForbiddenList(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("pinned %d bytes, sha256 %s", len(list.Bytes()), list.SHA256())
}

// The whole chain with nothing faked: an image built from an export of a commit, the
// Go gate, the real script, real Docker. Opt-in: Docker, OpenSSL 3.4+, Node 22/24, minutes.
//
//	CRN_TLS_E2E=1 go test ./internal/buildstep -run TestTheRealGate -timeout 40m
func TestTheRealGateAcceptsAnImageBuiltFromACommit(t *testing.T) {
	if os.Getenv("CRN_TLS_E2E") != "1" {
		t.Skip("set CRN_TLS_E2E=1")
	}
	ctx := context.Background()
	const project = "11111111-1111-4111-8111-111111111111"
	const value = "203.0.113.250"

	dir := t.TempDir()
	if out, err := exec.Command("cp", "-R", "../../tests/fixtures/external-tls-app/.", dir).CombinedOutput(); err != nil {
		t.Fatalf("copy fixture: %v\n%s", err, out)
	}
	writeTree(t, dir, "public/fitt-feedback.js", feedbackWidgetForTest(t))
	writeTree(t, dir, ".gitignore", "public/ignored.txt\n")
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "config", "user.email", "t@example.test")
	gitIn(t, dir, "config", "user.name", "t")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-qm", "the commit")
	commit := gitIn(t, dir, "rev-parse", "HEAD")
	// In the workspace, where the Dockerfile's COPY public ./public would pick them up,
	// holding a value the gate looks for: only a build from the commit can pass.
	writeTree(t, dir, "public/untracked.txt", "dial "+value+"\n")
	writeTree(t, dir, "public/ignored.txt", "dial "+value+"\n")

	listRaw, err := os.ReadFile(filepath.Join("testdata", "receipt-passed.list"))
	if err != nil {
		t.Fatal(err)
	}
	list, err := PinForbiddenList(writeList(t, string(listRaw), 0o600))
	if err != nil {
		t.Fatal(err)
	}

	buildContext := t.TempDir()
	if err := ExportCommit(ctx, dir, commit, buildContext); err != nil {
		t.Fatal(err)
	}
	const tag = "crn-tls-fixture:go-gate"
	labels := map[string]string{"org.opencontainers.image.revision": commit}
	if err := BuildImage(ctx, buildContext, "Dockerfile.fixture", tag, labels, discardLogger()); err != nil {
		t.Fatal(err)
	}
	id, err := exec.Command("docker", "image", "inspect", "--format", "{{.Id}}", tag).Output()
	if err != nil {
		t.Fatal(err)
	}
	image := "crn-tls-fixture@" + strings.TrimSpace(string(id))

	receipt := filepath.Join(t.TempDir(), "receipt.json")
	if dest := os.Getenv("CRN_TLS_E2E_RECEIPTS"); dest != "" {
		receipt = filepath.Join(dest, "passed-go-gate.json")
	}
	a := Acceptance{Image: image, Commit: commit, ProjectID: project, Widget: true, Forbidden: list, ReceiptPath: receipt}
	if err := AcceptCandidate(ctx, dir, a, discardLogger()); err != nil {
		t.Fatalf("the real gate refused a good image: %v", err)
	}
	raw, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), value) {
		t.Error("the receipt repeats a value")
	}
	// And the image really lacks them.
	for _, name := range []string{"untracked.txt", "ignored.txt"} {
		if err := exec.Command("docker", "run", "--rm", "--entrypoint", "test", image, "!", "-e", "/app/public/"+name).Run(); err != nil {
			t.Errorf("the image holds public/%s: %v", name, err)
		}
	}
}

// The same chain, with a workspace build instead of a commit's: the planted file reaches
// the image, and the gate must see it. This proves the test above can fail.
func TestTheRealGateRefusesAnImageBuiltFromTheWorkspace(t *testing.T) {
	if os.Getenv("CRN_TLS_E2E") != "1" {
		t.Skip("set CRN_TLS_E2E=1")
	}
	ctx := context.Background()
	dir := t.TempDir()
	if out, err := exec.Command("cp", "-R", "../../tests/fixtures/external-tls-app/.", dir).CombinedOutput(); err != nil {
		t.Fatalf("copy fixture: %v\n%s", err, out)
	}
	writeTree(t, dir, "public/fitt-feedback.js", feedbackWidgetForTest(t))
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "config", "user.email", "t@example.test")
	gitIn(t, dir, "config", "user.name", "t")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-qm", "the commit")
	commit := gitIn(t, dir, "rev-parse", "HEAD")
	writeTree(t, dir, "public/untracked.txt", "dial 203.0.113.250\n")

	listRaw, err := os.ReadFile(filepath.Join("testdata", "receipt-passed.list"))
	if err != nil {
		t.Fatal(err)
	}
	list, err := PinForbiddenList(writeList(t, string(listRaw), 0o600))
	if err != nil {
		t.Fatal(err)
	}
	const tag = "crn-tls-fixture:go-gate-workspace"
	labels := map[string]string{"org.opencontainers.image.revision": commit}
	if err := BuildImage(ctx, dir, "Dockerfile.fixture", tag, labels, discardLogger()); err != nil {
		t.Fatal(err)
	}
	id, err := exec.Command("docker", "image", "inspect", "--format", "{{.Id}}", tag).Output()
	if err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(t.TempDir(), "receipt.json")
	if dest := os.Getenv("CRN_TLS_E2E_RECEIPTS"); dest != "" {
		receipt = filepath.Join(dest, "failed-go-gate-workspace-build.json")
	}
	a := Acceptance{Image: "crn-tls-fixture@" + strings.TrimSpace(string(id)), Commit: commit, ProjectID: "11111111-1111-4111-8111-111111111111", Widget: true, Forbidden: list, ReceiptPath: receipt}
	err = AcceptCandidate(ctx, dir, a, discardLogger())
	if err == nil {
		t.Fatal("the gate passed an image holding a file the commit does not")
	}
	if !strings.Contains(err.Error(), "real value #1") {
		t.Errorf("err = %v, want the value found in the image", err)
	}
	if strings.Contains(err.Error(), "203.0.113.250") {
		t.Errorf("the error repeats the value: %v", err)
	}
}

func feedbackWidgetForTest(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("fitt-feedback.js")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
