package buildstep

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// DBModeExternalTLS is the crn-delivery.json database_mode of an app that only
// talks to an external PostgreSQL over TLS.
const DBModeExternalTLS = "external-tls-only"

//go:embed accept-candidate.mjs
var acceptCandidateScript string

// Acceptance names the one candidate AcceptCandidate checks.
type Acceptance struct {
	Image     string // <repository>@sha256:<digest>, as the registry holds it
	Commit    string // the commit the image was built from
	ProjectID string
	Widget    bool // this build must carry the feedback widget (false: must not)
	// ForbiddenFile lists hashes of values that may appear nowhere; never the values.
	ForbiddenFile string
	// ReceiptPath, when set, keeps the receipt after the check, passed or not.
	ReceiptPath string
}

// AcceptCandidate is the gate before "released": it runs accept-candidate.mjs on the
// registry's image, from a clean tree of the commit, and passes only on a receipt that
// says passed for this very image and commit. Anything else (a crash, a timeout, a
// missing or foreign receipt) is an error.
func AcceptCandidate(ctx context.Context, dir string, a Acceptance, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	if _, err := os.Stat(a.ForbiddenFile); err != nil {
		return fmt.Errorf("candidate acceptance: no list of forbidden values for this project: %w", err)
	}
	temp, err := os.MkdirTemp("", "crn-accept-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	tools, source := filepath.Join(temp, "tools"), filepath.Join(temp, "source")
	for _, d := range []string{tools, source} {
		if err := os.Mkdir(d, 0o700); err != nil {
			return err
		}
	}
	for name, body := range map[string]string{"verify-delivery.mjs": deliveryVerifier, "accept-candidate.mjs": acceptCandidateScript} {
		if err := os.WriteFile(filepath.Join(tools, name), []byte(body), 0o600); err != nil {
			return err
		}
	}
	if err := exportCommit(ctx, dir, a.Commit, source); err != nil {
		return err
	}

	receiptFile := filepath.Join(temp, "receipt.json")
	widget := "off"
	if a.Widget {
		widget = "on"
	}
	cmd, err := verifierCommand(ctx, source, filepath.Join(tools, "accept-candidate.mjs"),
		"--image", a.Image, "--commit", a.Commit, "--project", a.ProjectID, "--widget", widget, "--forbidden", a.ForbiddenFile, "--receipt", receiptFile)
	if err != nil {
		return err
	}
	out, runErr := cmd.CombinedOutput()
	raw, readErr := os.ReadFile(receiptFile)
	if readErr != nil {
		if runErr != nil {
			return fmt.Errorf("candidate acceptance: %w\n%s", runErr, tailLines(string(out), 30))
		}
		return fmt.Errorf("candidate acceptance: no receipt: %w", readErr)
	}
	if a.ReceiptPath != "" {
		if err := os.MkdirAll(filepath.Dir(a.ReceiptPath), 0o755); err != nil {
			logger.Warn("keep acceptance receipt failed", "err", err)
		} else if err := os.WriteFile(a.ReceiptPath, raw, 0o644); err != nil {
			logger.Warn("keep acceptance receipt failed", "err", err)
		}
	}
	var receipt struct {
		Status   string `json:"status"`
		Image    string `json:"image"`
		Commit   string `json:"commit"`
		Platform string `json:"platform"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return fmt.Errorf("candidate acceptance: unreadable receipt: %w", err)
	}
	if receipt.Status != "passed" {
		return fmt.Errorf("candidate acceptance failed for %s: %s", a.Image, receipt.Error)
	}
	if runErr != nil {
		return fmt.Errorf("candidate acceptance: %w\n%s", runErr, tailLines(string(out), 30))
	}
	if receipt.Image != a.Image || receipt.Commit != a.Commit || receipt.Platform != "linux/"+strings.TrimPrefix(imagePlatform, "linux/") {
		return fmt.Errorf("candidate acceptance: the receipt is for %s at %s (%s), want %s at %s (%s)", receipt.Image, receipt.Commit, receipt.Platform, a.Image, a.Commit, imagePlatform)
	}
	logger.Info("candidate accepted", "image", a.Image, "commit", a.Commit)
	return nil
}

// exportCommit writes the commit's tree, and nothing else, to dest: the checks must not
// leave the project's workspace dirty, and must see what the commit holds.
func exportCommit(ctx context.Context, dir, commit, dest string) error {
	var archive, stderr bytes.Buffer
	git := exec.CommandContext(ctx, "git", "-C", dir, "archive", "--format=tar", commit)
	git.Stdout, git.Stderr = &archive, &stderr
	if err := git.Run(); err != nil {
		return fmt.Errorf("candidate acceptance: export commit %s: %w: %s", commit, err, strings.TrimSpace(stderr.String()))
	}
	tar := exec.CommandContext(ctx, "tar", "-xf", "-", "-C", dest)
	tar.Stdin = &archive
	if out, err := tar.CombinedOutput(); err != nil {
		return fmt.Errorf("candidate acceptance: unpack commit %s: %w: %s", commit, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// verifierCommand runs a script with the Node the delivery checks use, in dir. A cancel
// asks it to stop (it removes what it created) and kills it if it does not.
func verifierCommand(ctx context.Context, dir, script string, args ...string) (*exec.Cmd, error) {
	node := os.Getenv("CRN_VERIFY_NODE")
	if node == "" {
		node = "node"
	}
	node, err := exec.LookPath(node)
	if err != nil {
		return nil, fmt.Errorf("verification Node runtime unavailable: %w", err)
	}
	cmd := exec.CommandContext(ctx, node, append([]string{script}, args...)...)
	// npm and Playwright must use the same selected runtime as the verifier.
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "PATH=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "PATH="+filepath.Dir(node)+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Dir = dir
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 30 * time.Second
	return cmd, nil
}
