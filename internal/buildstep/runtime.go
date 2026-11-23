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

// widgetEnv is what the injected widget's route reads. CRN writes these entries
// itself, in place of any the app wrote. Until 23 Nov 2026 only FITT_FEEDBACK_URL
// was here, and the release check refused OKR build 16 for FITT_TICKET_WINDOW.
var widgetEnv = []RuntimeEnv{
	{Name: "FITT_FEEDBACK_URL", Required: "no", Note: "where the feedback widget sends reports"},
	{Name: "FITT_TICKET_WINDOW", Required: "no", Note: "1 shows the feedback widget's ticket window; it also needs FITT_FEEDBACK_URL and PLATFORM_FITTCORE_ORIGIN on the same origin"},
}

// fittcoreOriginEnv is read by the route too, but an app that signs in at fittcore
// reads it itself and declares it as it needs it: CRN keeps that entry, adds this
// one only when the app has none, and takes out only this one.
var fittcoreOriginEnv = RuntimeEnv{Name: "PLATFORM_FITTCORE_ORIGIN", Required: "no", Note: "the company fittcore; the feedback widget's ticket window needs it"}

// FinalizeRuntime rewrites the app's crn.runtime.json with the fields CRN owns
// (contract, first migration, port, health, volumes, writable) and the widget's
// variables when the widget is in the app, keeping the env the app declared. Run
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

	rt.Env = withWidgetEnv(rt.Env, HasFeedbackWidget(dir))

	out, err := json.MarshalIndent(rt, "", "  ")
	if err != nil {
		return fmt.Errorf("%s: %w", RuntimeFile, err)
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

// withWidgetEnv is the app's env with the widget's variables when the widget is in
// the app (widget true) and without them when it is not.
func withWidgetEnv(appEnv []RuntimeEnv, widget bool) []RuntimeEnv {
	owned := map[string]bool{}
	for _, e := range widgetEnv {
		owned[e.Name] = true
	}
	env := make([]RuntimeEnv, 0, len(appEnv)+len(widgetEnv)+1)
	hasOrigin := false
	for _, e := range appEnv {
		switch {
		case owned[e.Name]:
		case e == fittcoreOriginEnv && !widget:
		default:
			hasOrigin = hasOrigin || e.Name == fittcoreOriginEnv.Name
			env = append(env, e)
		}
	}
	if !widget {
		return env
	}
	if !hasOrigin {
		env = append(env, fittcoreOriginEnv)
	}
	return append(env, widgetEnv...)
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
