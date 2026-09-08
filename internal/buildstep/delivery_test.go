package buildstep

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDeliveryGateRejectsAuthoredSuccess(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required by the release gate")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CRN_VERIFICATION.json"), []byte(`{"status":"passed"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyDelivery(context.Background(), dir); err == nil {
		t.Fatal("incomplete application was released")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "CRN_VERIFICATION.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Status string }
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" {
		t.Fatalf("stale pass receipt survived: %s", raw)
	}
}
func TestDeliveryAssetsMatchRuntime(t *testing.T) {
	for file, want := range map[string]string{"scripts/verify-delivery.mjs": deliveryVerifier, "assets/Dockerfile": productionDockerfile} {
		raw, err := os.ReadFile(filepath.Join("../../cmd/server/skillassets", file))
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != want {
			t.Errorf("injected %s differs from trusted runtime", file)
		}
	}
}
func TestCustomerCredentialsHaveNoFallback(t *testing.T) {
	env := NewDemoEnvExample(4100)
	if env.DevEmail != "" || env.DevPassword != "" || env.BootstrapEmail != "" || env.BootstrapPassword != "" || env.AuthSecret != "" {
		t.Fatal("shared customer credentials in callback")
	}
}

// Opt-in end-to-end test with an isolated application fixture, never customer data.
func TestDeliveryCustomerRuntime(t *testing.T) {
	dir := os.Getenv("CRN_DELIVERY_TEST_DIR")
	if dir == "" {
		t.Skip("set CRN_DELIVERY_TEST_DIR to an isolated application fixture")
	}
	if err := VerifyDelivery(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "CRN_VERIFICATION.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Status  string `json:"status"`
		Unit    int    `json:"unit_tests"`
		Browser int    `json:"browser_tests"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "passed" || result.Unit == 0 || result.Browser == 0 {
		t.Fatalf("runtime checks did not execute: %+v", result)
	}
}
