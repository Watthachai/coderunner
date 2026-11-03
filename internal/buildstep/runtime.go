package buildstep

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// RuntimeFile is what an installer reads to run the image without reading its
// source: the app declares the environment it needs (fitt-build skill,
// references/runs-anywhere.md), CRN writes the rest. The image carries it at
// /app/crn.runtime.json and labels its hash fitt.crn.runtime_sha256. The shape
// was agreed with Fittcore on 3 Nov 2026; change it only with them.
const RuntimeFile = "crn.runtime.json"

// Runtime is crn.runtime.json. Field order is the order in the file.
type Runtime struct {
	Contract int `json:"contract"`
	// MigrationsFirst names the app's first Prisma migration: an installer checks
	// it against the database it is about to upgrade. Empty for an app without one.
	MigrationsFirst string       `json:"migrations_first,omitempty"`
	Port            int          `json:"port"`
	Health          string       `json:"health"`
	Volumes         []string     `json:"volumes"`
	Writable        []string     `json:"writable"`
	Env             []RuntimeEnv `json:"env"`
}

// RuntimeEnv is one variable the app reads at run time. Required is "always",
// "first_start" (only until the first start has succeeded) or "no".
type RuntimeEnv struct {
	Name      string `json:"name"`
	Required  string `json:"required"`
	Secret    bool   `json:"secret,omitempty"`
	MinLength int    `json:"min_length,omitempty"`
	Note      string `json:"note,omitempty"`
}

// feedbackEnv is the variable the injected 💬 widget's route reads.
var feedbackEnv = RuntimeEnv{Name: "FITT_FEEDBACK_URL", Required: "no", Note: "where the feedback widget sends reports"}

// FinalizeRuntime rewrites the app's crn.runtime.json with the fields CRN owns
// (contract, first migration, port, health, volumes, writable) and the widget's
// variable when the widget is in the app, keeping the env the app declared. Run
// it after the widget is injected and before the release check, which then
// checks the file the image will carry. A missing or malformed file fails.
func FinalizeRuntime(dir string) error {
	path := filepath.Join(dir, RuntimeFile)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s is missing: declare the environment the app reads (fitt-build: references/runs-anywhere.md)", RuntimeFile)
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", RuntimeFile, err)
	}
	var rt Runtime
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rt); err != nil {
		return fmt.Errorf("%s: %w", RuntimeFile, err)
	}
	first, err := firstMigration(dir)
	if err != nil {
		return err
	}
	contract, _ := strconv.Atoi(RuntimeContract)
	rt.Contract = contract
	rt.MigrationsFirst = first
	rt.Port = 3000
	rt.Health = "/api/health"
	rt.Volumes = []string{"/data/uploads"}
	rt.Writable = []string{"/tmp", "/data/uploads"}

	env := make([]RuntimeEnv, 0, len(rt.Env)+1)
	for _, e := range rt.Env {
		if e.Name != feedbackEnv.Name {
			env = append(env, e)
		}
	}
	if HasFeedbackWidget(dir) {
		env = append(env, feedbackEnv)
	}
	rt.Env = env

	out, err := json.MarshalIndent(rt, "", "  ")
	if err != nil {
		return fmt.Errorf("%s: %w", RuntimeFile, err)
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

// firstMigration is the earliest Prisma migration directory ("" when none).
func firstMigration(dir string) (string, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "prisma", "migrations"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read prisma/migrations: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", nil
	}
	sort.Strings(names)
	return names[0], nil
}

// RuntimeSHA256 is the hex SHA-256 of the app's crn.runtime.json as it is on
// disk, for the image label.
func RuntimeSHA256(dir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, RuntimeFile))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", RuntimeFile, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
