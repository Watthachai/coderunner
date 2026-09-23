package config

import (
	"strings"
	"testing"
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
