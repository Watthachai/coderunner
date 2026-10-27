package config

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// requiredEnv sets the three values Load refuses to start without.
func requiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CRN_DATABASE_URL", "postgres://unused")
	t.Setenv("CRN_CLAUDE_BIN", "/usr/bin/true")
	t.Setenv("CRN_DOCKER_USER", "crn")
}

func TestClaudeEffortDefaultsToXhigh(t *testing.T) {
	requiredEnv(t)
	t.Setenv("CRN_CLAUDE_EFFORT", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClaudeEffort != "xhigh" {
		t.Errorf("ClaudeEffort = %q, want xhigh", cfg.ClaudeEffort)
	}
}

func TestClaudeEffortComesFromEnv(t *testing.T) {
	requiredEnv(t)
	t.Setenv("CRN_CLAUDE_EFFORT", "high")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClaudeEffort != "high" {
		t.Errorf("ClaudeEffort = %q, want high", cfg.ClaudeEffort)
	}
}

// A mistyped effort would reach every build's command line and fail it there,
// one at a time. Refusing to start says so once, where the mistake is.
func TestClaudeEffortTypoStopsStartup(t *testing.T) {
	requiredEnv(t)
	t.Setenv("CRN_CLAUDE_EFFORT", "extreme")
	_, err := Load()
	if err == nil {
		t.Fatal("Load accepted CRN_CLAUDE_EFFORT=extreme")
	}
	if !strings.Contains(err.Error(), "CRN_CLAUDE_EFFORT") || !strings.Contains(err.Error(), "xhigh") {
		t.Errorf("error should name the variable and the valid values: %v", err)
	}
}

// A customer's images go to that customer's registry project (Fittcore's UAT pulls
// from fitt/ftcdv-uat), every other org's stay in the shared one.
func TestOrgImageRegistriesComeFromEnv(t *testing.T) {
	requiredEnv(t)
	t.Setenv("CRN_ORG_IMAGE_REGISTRIES", " 088c8070-3ec3-46a9-b53f-52dcff35c23e=172.168.1.234:5050/fitt/ftcdv-uat/ , ")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	org := uuid.MustParse("088c8070-3ec3-46a9-b53f-52dcff35c23e")
	if got := cfg.OrgImageRegistries[org]; got != "172.168.1.234:5050/fitt/ftcdv-uat" || len(cfg.OrgImageRegistries) != 1 {
		t.Errorf("OrgImageRegistries = %v", cfg.OrgImageRegistries)
	}
}

// A mistyped entry would send that customer's images to the shared registry
// without a word, so CRN refuses to start instead.
func TestOrgImageRegistriesTypoStopsStartup(t *testing.T) {
	for _, bad := range []string{"088c8070=reg/fitt/x", "088c8070-3ec3-46a9-b53f-52dcff35c23e", "088c8070-3ec3-46a9-b53f-52dcff35c23e="} {
		requiredEnv(t)
		t.Setenv("CRN_ORG_IMAGE_REGISTRIES", bad)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CRN_ORG_IMAGE_REGISTRIES") {
			t.Errorf("%q: err = %v, want one naming CRN_ORG_IMAGE_REGISTRIES", bad, err)
		}
	}
}

func TestTerminalIsOffUnlessEnabled(t *testing.T) {
	requiredEnv(t)
	for env, want := range map[string]bool{"": false, "false": false, "yes please": false, "true": true, "1": true} {
		t.Setenv("CRN_TERMINAL_ENABLED", env)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.TerminalEnabled != want {
			t.Errorf("CRN_TERMINAL_ENABLED=%q: TerminalEnabled = %v, want %v", env, cfg.TerminalEnabled, want)
		}
	}
}

func TestFTCDVCallbackOrgsComeFromEnv(t *testing.T) {
	requiredEnv(t)
	t.Setenv("CRN_FTC_DV_CALLBACK_ORGS", " 088c8070-3ec3-46a9-b53f-52dcff35c23e ,")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	ftc := uuid.MustParse("088c8070-3ec3-46a9-b53f-52dcff35c23e")
	if len(cfg.FTCDVCallbackOrgs) != 1 || !cfg.FTCDVCallbackOrgs[ftc] {
		t.Errorf("FTCDVCallbackOrgs = %v, want only %s", cfg.FTCDVCallbackOrgs, ftc)
	}
}

func TestFTCDVCallbackOrgsTypoStopsStartup(t *testing.T) {
	requiredEnv(t)
	t.Setenv("CRN_FTC_DV_CALLBACK_ORGS", "088c8070-3ec3-46a9-b53f")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CRN_FTC_DV_CALLBACK_ORGS") {
		t.Errorf("Load() err = %v, want one naming CRN_FTC_DV_CALLBACK_ORGS", err)
	}
}

func TestFTCDVCallbackCAIsReadFromItsFile(t *testing.T) {
	requiredEnv(t)
	t.Setenv("CRN_FTC_DV_CALLBACK_CA", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FTCDVCallbackCA != nil {
		t.Error("no CRN_FTC_DV_CALLBACK_CA: want the system's CAs (nil pool)")
	}

	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	ca := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CRN_FTC_DV_CALLBACK_CA", ca)
	if cfg, err = Load(); err != nil {
		t.Fatal(err)
	}
	if cfg.FTCDVCallbackCA == nil || !cfg.FTCDVCallbackCA.Equal(func() *x509.CertPool { p := x509.NewCertPool(); p.AddCert(srv.Certificate()); return p }()) {
		t.Error("FTCDVCallbackCA does not hold the file's certificate")
	}
}

func TestFTCDVCallbackCAThatIsNoCertificateStopsStartup(t *testing.T) {
	requiredEnv(t)
	bad := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(bad, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{bad, filepath.Join(t.TempDir(), "missing.crt")} {
		t.Setenv("CRN_FTC_DV_CALLBACK_CA", path)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CRN_FTC_DV_CALLBACK_CA") {
			t.Errorf("CRN_FTC_DV_CALLBACK_CA=%s: Load() err = %v, want one naming the variable", path, err)
		}
	}
}
