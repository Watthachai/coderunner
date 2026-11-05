package buildstep

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed verify-delivery.mjs
var deliveryVerifier string

// VerifyDelivery executes a trusted verifier, not a generated success report.
// The script provisions disposable data, executes tests and writes its receipt.
func VerifyDelivery(ctx context.Context, dir string) error {
	// A stale/authored receipt must never satisfy a command that did not run.
	if err := os.Remove(filepath.Join(dir, "CRN_VERIFICATION.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	temp, err := os.MkdirTemp("", "crn-delivery-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	script := filepath.Join(temp, "verify-delivery.mjs")
	dockerfile := filepath.Join(temp, "Dockerfile")
	if err = os.WriteFile(script, []byte(deliveryVerifier), 0o600); err != nil {
		return err
	}
	if err = os.WriteFile(dockerfile, []byte(productionDockerfile), 0o600); err != nil {
		return err
	}
	cmd, err := verifierCommand(ctx, dir, script, dockerfile)
	if err != nil {
		return err
	}
	out, err := cmd.CombinedOutput()
	recordOutput(ctx, "the release checker", out)
	if err != nil {
		return fmt.Errorf("customer delivery verification failed: %w\n%s", err, tailLines(string(out), 30))
	}
	var receipt struct {
		Status string `json:"status"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, "CRN_VERIFICATION.json"))
	if err != nil {
		return fmt.Errorf("verification receipt missing: %w", err)
	}
	recordOutput(ctx, "the release checker's receipt", raw)
	if err := json.Unmarshal(raw, &receipt); err != nil || receipt.Status != "passed" {
		return fmt.Errorf("verification did not pass")
	}
	return nil
}
