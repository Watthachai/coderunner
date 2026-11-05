package buildstep

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
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
	// Forbidden is the list of value hashes, pinned before the agent ran.
	Forbidden *ForbiddenList
	// ReceiptPath, when set, keeps the receipt after the check, passed or not.
	ReceiptPath string
}

// ExternalTLSCases is how many configurations the release checker must see refused, as
// its tlsFailurePlan lists them: a smaller matrix means something went unchecked.
const ExternalTLSCases = 53

// placeholderValues is how many placeholders the gate scans for (accept-candidate.mjs).
const placeholderValues = 3

// AcceptCandidate is the gate before "released": it runs accept-candidate.mjs on the
// registry's image, from a tree exported from the commit, with the pinned list on stdin,
// and passes only on a receipt that holds every result for this very image and commit
// (validateReceipt). Anything else (a crash, a timeout, a missing, partial or foreign
// receipt, a list that moved since the pin) is an error.
func AcceptCandidate(ctx context.Context, dir string, a Acceptance, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	if a.Forbidden == nil {
		return errors.New("candidate acceptance: no list of forbidden values was pinned before the agent ran")
	}
	if err := a.Forbidden.CheckUnchanged(); err != nil {
		return fmt.Errorf("candidate acceptance: %w", err)
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
	if err := ExportCommit(ctx, dir, a.Commit, source); err != nil {
		return fmt.Errorf("candidate acceptance: %w", err)
	}

	receiptFile := filepath.Join(temp, "receipt.json")
	widget := "off"
	if a.Widget {
		widget = "on"
	}
	cmd, err := verifierCommand(ctx, source, filepath.Join(tools, "accept-candidate.mjs"),
		"--image", a.Image, "--commit", a.Commit, "--project", a.ProjectID, "--widget", widget, "--receipt", receiptFile)
	if err != nil {
		return err
	}
	cmd.Stdin = bytes.NewReader(a.Forbidden.Bytes())
	out, runErr := cmd.CombinedOutput()
	recordOutput(ctx, "the candidate gate", out)
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
	recordOutput(ctx, "the acceptance receipt", raw)
	var receipt struct {
		Status string `json:"status"`
		Error  string `json:"error"`
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
	if err := validateReceipt(raw, a); err != nil {
		return fmt.Errorf("candidate acceptance: a receipt that says passed does not hold what passing takes: %w", err)
	}
	logger.Info("candidate accepted", "image", a.Image, "commit", a.Commit)
	return nil
}

// acceptanceReceipt is what a receipt must hold. Pointers tell a result that is missing
// from one that is empty: a receipt is not trusted for what it leaves out.
type acceptanceReceipt struct {
	SchemaVersion *int   `json:"schema_version"`
	Status        string `json:"status"`
	Image         string `json:"image"`
	Commit        string `json:"commit"`
	Project       string `json:"project"`
	Platform      string `json:"platform"`
	Revision      string `json:"revision"`
	Error         string `json:"error"`
	ArtifactScan  *struct {
		ListSHA256 string `json:"list_sha256"`
		Values     *struct {
			Real         *int `json:"real"`
			Placeholders *int `json:"placeholders"`
		} `json:"values"`
		Hits   *[]json.RawMessage `json:"hits"`
		Source *struct {
			Files int `json:"files"`
		} `json:"source"`
		Image *struct {
			Layers int               `json:"layers"`
			Blobs  []json.RawMessage `json:"blobs"`
			Config *struct {
				Digest string `json:"digest"`
			} `json:"config"`
		} `json:"image"`
	} `json:"artifact_scan"`
	Checker *struct {
		SchemaVersion int    `json:"schema_version"`
		Status        string `json:"status"`
		Image         string `json:"image"`
		DatabaseMode  string `json:"database_mode"`
		Checks        []struct {
			Name   string `json:"name"`
			Passed bool   `json:"passed"`
		} `json:"checks"`
		ExternalTLS *struct {
			MustFail []struct {
				Case        string   `json:"case"`
				Path        string   `json:"path"`
				Expected    string   `json:"expected"`
				Result      string   `json:"result"`
				OK          bool     `json:"ok"`
				Connections *int     `json:"connections"`
				Leaks       []string `json:"leaks"`
			} `json:"must_fail"`
		} `json:"external_tls"`
	} `json:"checker"`
	Widget *struct {
		Expected        *bool     `json:"expected"`
		Ran             bool      `json:"ran"`
		OutsideRequests *[]string `json:"outside_requests"`
	} `json:"widget"`
}

// validateReceipt passes only when a receipt shows every step ran on this candidate and
// found nothing. It reads the results, not the status: a receipt that merely says
// "passed" proves nothing.
func validateReceipt(raw []byte, a Acceptance) error {
	var r acceptanceReceipt
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("unreadable: %w", err)
	}
	var problems []string
	bad := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if r.SchemaVersion == nil || *r.SchemaVersion != 1 {
		bad("schema_version is not 1")
	}
	if r.Status != "passed" {
		bad("status is %q", r.Status)
	}
	if r.Error != "" {
		bad("it holds an error")
	}
	if r.Image != a.Image {
		bad("it is for image %s, want %s", r.Image, a.Image)
	}
	if r.Commit != a.Commit {
		bad("it is for commit %s, want %s", r.Commit, a.Commit)
	}
	if r.Project != a.ProjectID {
		bad("it is for project %s, want %s", r.Project, a.ProjectID)
	}
	if r.Platform != imagePlatform {
		bad("it is for platform %s, want %s", r.Platform, imagePlatform)
	}
	if r.Revision != a.Commit {
		bad("the image's revision label is %q, want %s", r.Revision, a.Commit)
	}

	if scan := r.ArtifactScan; scan == nil {
		bad("no artifact scan")
	} else {
		if scan.ListSHA256 != a.Forbidden.SHA256() {
			bad("the scan used another list of forbidden values")
		}
		if scan.Values == nil || scan.Values.Real == nil || *scan.Values.Real != ForbiddenValues {
			bad("the scan did not look for %d real values", ForbiddenValues)
		}
		if scan.Values == nil || scan.Values.Placeholders == nil || *scan.Values.Placeholders != placeholderValues {
			bad("the scan did not look for %d placeholders", placeholderValues)
		}
		if scan.Hits == nil {
			bad("the scan reports no hits list")
		} else if len(*scan.Hits) != 0 {
			bad("the scan found %d hits", len(*scan.Hits))
		}
		if scan.Source == nil || scan.Source.Files < 1 {
			bad("the scan read no source files")
		}
		if scan.Image == nil || scan.Image.Layers < 1 || len(scan.Image.Blobs) < 1 {
			bad("the scan read no image layers")
		} else if scan.Image.Config == nil || scan.Image.Config.Digest == "" {
			bad("the scan read no image config")
		}
	}

	if c := r.Checker; c == nil {
		bad("no release checker result")
	} else {
		if c.SchemaVersion != 1 {
			bad("the checker's schema_version is not 1")
		}
		if c.Status != "passed" {
			bad("the checker's status is %q", c.Status)
		}
		if c.Image != a.Image {
			bad("the checker ran on image %s, want %s", c.Image, a.Image)
		}
		if c.DatabaseMode != DBModeExternalTLS {
			bad("the checker ran in database mode %q, want %s", c.DatabaseMode, DBModeExternalTLS)
		}
		if len(c.Checks) == 0 {
			bad("the checker lists no checks")
		}
		for _, check := range c.Checks {
			if !check.Passed {
				bad("the check %q did not pass", check.Name)
			}
		}
		if c.ExternalTLS == nil {
			bad("no external TLS result")
		} else {
			seen := map[string]bool{}
			for _, m := range c.ExternalTLS.MustFail {
				name := m.Case + " (" + m.Path + ")"
				seen[name] = true
				if !m.OK || m.Expected != "fail" || m.Result != "failed" {
					bad("the TLS case %s did not fail as required", name)
				}
				if m.Connections != nil && *m.Connections != 0 {
					bad("the TLS case %s opened %d connections", name, *m.Connections)
				}
				if len(m.Leaks) != 0 {
					bad("the TLS case %s printed a secret", name)
				}
			}
			if len(c.ExternalTLS.MustFail) != ExternalTLSCases || len(seen) != ExternalTLSCases {
				bad("the TLS matrix holds %d different cases of %d lines, want %d", len(seen), len(c.ExternalTLS.MustFail), ExternalTLSCases)
			}
		}
	}

	if w := r.Widget; w == nil {
		bad("no widget result")
	} else {
		if w.Expected == nil || *w.Expected != a.Widget {
			bad("the widget check did not expect widget=%v", a.Widget)
		}
		if !w.Ran {
			bad("the widget check did not run")
		}
		if w.OutsideRequests == nil {
			bad("the widget check reports no outside requests list")
		} else if len(*w.OutsideRequests) != 0 {
			bad("the page asked another origin")
		}
	}

	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
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
