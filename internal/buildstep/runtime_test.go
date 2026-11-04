package buildstep

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRuntime(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "crn.runtime.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkMigration(t *testing.T, dir, name string) {
	t.Helper()
	p := filepath.Join(dir, "prisma", "migrations", name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "migration.sql"), []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The app declares its env; CRN writes everything it owns itself, so the
// installer reads one file whose fixed fields cannot be wrong.
func TestFinalizeRuntimeFillsWhatCRNOwns(t *testing.T) {
	dir := t.TempDir()
	writeRuntime(t, dir, `{"port": 8080, "env": [
	  {"name": "DATABASE_URL", "required": "always", "secret": true},
	  {"name": "AUTH_SECRET", "required": "always", "secret": true, "min_length": 32},
	  {"name": "ALLOW_INSECURE_COOKIES", "required": "no", "note": "true when served over http"}
	]}`)
	mkMigration(t, dir, "20261201000000_add_claims")
	mkMigration(t, dir, "20261102000000_init")
	os.WriteFile(filepath.Join(dir, "prisma", "migrations", "migration_lock.toml"), []byte("provider = \"postgresql\""), 0o644)

	if err := FinalizeRuntime(dir); err != nil {
		t.Fatal(err)
	}
	want := `{
  "contract": 1,
  "migrations_first": "20261102000000_init",
  "port": 3000,
  "health": "/api/health",
  "volumes": [
    "/data/uploads"
  ],
  "writable": [
    "/tmp",
    "/data/uploads"
  ],
  "env": [
    {
      "name": "DATABASE_URL",
      "required": "always",
      "secret": true
    },
    {
      "name": "AUTH_SECRET",
      "required": "always",
      "secret": true,
      "min_length": 32
    },
    {
      "name": "ALLOW_INSECURE_COOKIES",
      "required": "no",
      "note": "true when served over http"
    }
  ]
}
`
	if got := readFile(t, filepath.Join(dir, "crn.runtime.json")); got != want {
		t.Fatalf("got\n%s", got)
	}
}

func TestFinalizeRuntimeWithoutMigrationsNamesNone(t *testing.T) {
	dir := t.TempDir()
	writeRuntime(t, dir, `{"env": []}`)
	if err := FinalizeRuntime(dir); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "crn.runtime.json")); strings.Contains(got, "migrations_first") {
		t.Fatalf("a static app has no first migration:\n%s", got)
	}
}

// The widget is injected by CRN after the app is written, so CRN declares the
// variable it reads, and drops it when the widget is gone.
func TestFinalizeRuntimeDeclaresTheWidgetVariableOnlyWithTheWidget(t *testing.T) {
	dir := t.TempDir()
	writeRuntime(t, dir, `{"env": [{"name": "FITT_FEEDBACK_URL", "required": "always"}]}`)
	if err := FinalizeRuntime(dir); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "crn.runtime.json")); strings.Contains(got, "FITT_FEEDBACK_URL") {
		t.Fatalf("no widget, yet its variable is declared:\n%s", got)
	}

	os.MkdirAll(filepath.Join(dir, "public"), 0o755)
	os.WriteFile(filepath.Join(dir, "public", "fitt-feedback.js"), []byte("//"), 0o644)
	for i := 0; i < 2; i++ {
		if err := FinalizeRuntime(dir); err != nil {
			t.Fatal(err)
		}
	}
	got := readFile(t, filepath.Join(dir, "crn.runtime.json"))
	if strings.Count(got, `"FITT_FEEDBACK_URL"`) != 1 || !strings.Contains(got, `"name": "FITT_FEEDBACK_URL",
      "required": "no"`) {
		t.Fatalf("widget variable should be declared once, optional:\n%s", got)
	}
}

func TestFinalizeRuntimeRefusesAMissingOrUnknownShape(t *testing.T) {
	dir := t.TempDir()
	if err := FinalizeRuntime(dir); err == nil || !strings.Contains(err.Error(), "crn.runtime.json") {
		t.Fatalf("missing file: %v", err)
	}
	for _, bad := range []string{`not json`, `{"env": [], "extra": 1}`, `{"env": [{"name": "A", "required": "no", "secrett": true}]}`} {
		writeRuntime(t, dir, bad)
		if err := FinalizeRuntime(dir); err == nil || !strings.Contains(err.Error(), "crn.runtime.json") {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

// The label lets an installer check the file in the image is the one CRN wrote.
func TestRuntimeSHA256IsTheFileHashAndLabelsIt(t *testing.T) {
	dir := t.TempDir()
	writeRuntime(t, dir, `{"env": []}`)
	if err := FinalizeRuntime(dir); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(readFile(t, filepath.Join(dir, "crn.runtime.json"))))
	got, err := RuntimeSHA256(dir)
	if err != nil || got != hex.EncodeToString(sum[:]) {
		t.Fatalf("got %q %v", got, err)
	}
	if l := (ImageLabels{RuntimeSHA256: got}).Map(); l["fitt.crn.runtime_sha256"] != got {
		t.Fatalf("label: %v", l)
	}
	if _, ok := (ImageLabels{}).Map()["fitt.crn.runtime_sha256"]; ok {
		t.Error("no file, no label")
	}
}

// The image carries the file where the installer looks for it.
func TestCustomerImageShipsTheRuntimeFile(t *testing.T) {
	runner := productionDockerfile[strings.Index(productionDockerfile, "AS runner"):]
	if !strings.Contains(runner, "COPY --from=builder --chown=node:node /app/crn.runtime.json ./crn.runtime.json") {
		t.Fatal("runner stage does not copy crn.runtime.json to /app")
	}
}

// fitt.db.mode tells an installer how the image reaches its database. Only an app
// that declares external-tls-only in crn-delivery.json gets the label; others keep
// the labels they had.
func TestReadDBModeAndItsLabel(t *testing.T) {
	for _, tc := range []struct{ delivery, want string }{
		{`{"schema_version":1,"database":true,"database_mode":"external-tls-only","database_ca_file":"/run/fitt-ca/ntc-db-ca.pem"}`, "external-tls-only"},
		{`{"schema_version":1,"database":true}`, ""},
		{`{"schema_version":1,"database":true,"database_mode":"local"}`, ""},
		{"", ""},
	} {
		dir := t.TempDir()
		if tc.delivery != "" {
			if err := os.WriteFile(filepath.Join(dir, "crn-delivery.json"), []byte(tc.delivery), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := ReadDBMode(dir)
		if err != nil || got != tc.want {
			t.Errorf("ReadDBMode(%s) = %q, %v; want %q", tc.delivery, got, err, tc.want)
		}
		labels := ImageLabels{ProjectID: "p", JobID: "j", BuildNo: 3, Schedule: "[]", DBMode: got}.Map()
		if v, ok := labels["fitt.db.mode"]; ok != (tc.want != "") || v != tc.want {
			t.Errorf("label fitt.db.mode = %q (present %v), want %q", v, ok, tc.want)
		}
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "crn-delivery.json"), []byte(`{"database_mode":"plaintext"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDBMode(dir); err == nil {
		t.Error("an unknown database_mode was accepted")
	}
}
