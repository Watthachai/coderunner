package buildstep

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// stubDocker puts a fake `docker` first on PATH for the test. Every invocation
// appends its arguments to the returned file and sets $n to the number of times
// it has now run, so body can behave differently per attempt.
func stubDocker(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	counter := filepath.Join(dir, "count")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> " + argsFile + "\n" +
		"n=$(cat " + counter + " 2>/dev/null || echo 0); n=$((n+1)); echo $n > " + counter + "\n" +
		body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

func attempts(t *testing.T, argsFile string) int {
	t.Helper()
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("stub docker was never invoked: %v", err)
	}
	return len(strings.Split(strings.TrimSpace(string(raw)), "\n"))
}

// These flags are the fix for the push race, not a preference: BuildKit's
// default wraps the image in an OCI index, and the index is what fails to push.
// Verified against the delivery registry — identical content pushes first try as
// a single manifest and fails repeatedly as an index.
func TestBuildImageDisablesAttestations(t *testing.T) {
	argsFile := stubDocker(t, "exit 0")
	if err := BuildImage(context.Background(), t.TempDir(), "Dockerfile", "demo:v1", discardLogger()); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, argsFile)
	for _, want := range []string{"--provenance=false", "--sbom=false", "--platform " + imagePlatform} {
		if !strings.Contains(got, want) {
			t.Errorf("docker build missing %q; ran: %s", want, got)
		}
	}
}

// A push that fails once and would succeed on a second attempt must not sink the
// build. This is the recoverable half of the index race — the child manifest did
// commit, so pushing again finds it. The unrecoverable half (child never
// committed, every retry fails the same way) is why BuildImage stops producing
// an index at all; no retry count fixes that one.
func TestPushImageRetriesARecoverableFailure(t *testing.T) {
	restore := pushBackoff
	pushBackoff = time.Millisecond
	t.Cleanup(func() { pushBackoff = restore })

	argsFile := stubDocker(t, `if [ "$n" -eq 1 ]; then
  echo "error from registry: blob unknown to registry - sha256:76462b82" >&2
  exit 1
fi
exit 0`)

	if err := PushImage(context.Background(), "reg/demo:v1", discardLogger()); err != nil {
		t.Fatalf("push should have succeeded on the second attempt: %v", err)
	}
	if got := attempts(t, argsFile); got != 2 {
		t.Errorf("docker push ran %d times, want 2", got)
	}
}

// A push that fails for a real reason must still fail the build — retrying is
// how we survive a race, not how we hide a broken registry or bad credentials.
func TestPushImageGivesUpAndReportsTheError(t *testing.T) {
	restore := pushBackoff
	pushBackoff = time.Millisecond
	t.Cleanup(func() { pushBackoff = restore })

	argsFile := stubDocker(t, `echo "denied: requested access to the resource is denied" >&2; exit 1`)

	err := PushImage(context.Background(), "reg/demo:v1", discardLogger())
	if err == nil {
		t.Fatal("a permanently failing push was reported as success")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("error lost the registry's reason: %v", err)
	}
	if got := attempts(t, argsFile); got != pushAttempts {
		t.Errorf("docker push ran %d times, want %d", got, pushAttempts)
	}
}

// A cancelled build must stop pushing immediately rather than sitting through
// the backoff — cancellation is propagated into the image phase on purpose.
func TestPushImageStopsOnCancel(t *testing.T) {
	restore := pushBackoff
	pushBackoff = time.Hour
	t.Cleanup(func() { pushBackoff = restore })

	argsFile := stubDocker(t, "exit 1")
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- PushImage(ctx, "reg/demo:v1", discardLogger()) }()

	// Let the first attempt run and enter the backoff, then cancel.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(argsFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stub docker never ran")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled push reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PushImage ignored cancellation and waited out the backoff")
	}
}
