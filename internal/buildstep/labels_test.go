package buildstep

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestImageLabelsNameTheProjectAndTheContract(t *testing.T) {
	m := ImageLabels{ProjectID: "p-1", JobID: "j-1", Commit: "abc123", BuildNo: 7, Schedule: "[]"}.Map()
	want := map[string]string{
		"fitt.crn.project_id": "p-1", "fitt.crn.job_id": "j-1", "fitt.crn.build_no": "7",
		"fitt.crn.contract": RuntimeContract, "fitt.crn.schedule": "[]", "org.opencontainers.image.revision": "abc123",
	}
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("got %v", m)
	}
	if _, ok := (ImageLabels{ProjectID: "p"}).Map()["org.opencontainers.image.revision"]; ok {
		t.Error("an empty commit must not label a revision")
	}
}

func TestReadScheduleIsAnArrayOrNothing(t *testing.T) {
	dir := t.TempDir()
	if got, err := ReadSchedule(dir); err != nil || got != "[]" {
		t.Fatalf("missing file: %q %v", got, err)
	}
	file := filepath.Join(dir, "crn.schedule.json")
	os.WriteFile(file, []byte("[\n {\"name\": \"a\", \"path\": \"/api/cron/a\", \"every_minutes\": 15}\n]"), 0o644)
	if got, _ := ReadSchedule(dir); got != `[{"name":"a","path":"/api/cron/a","every_minutes":15}]` {
		t.Fatalf("not compacted: %s", got)
	}
	for _, bad := range []string{`{}`, `not json`} {
		os.WriteFile(file, []byte(bad), 0o644)
		if _, err := ReadSchedule(dir); err == nil || !strings.Contains(err.Error(), "crn.schedule.json") {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

func TestBuildImagePassesLabelsSorted(t *testing.T) {
	args := stubDocker(t, `exit 0`)
	if err := BuildImage(context.Background(), t.TempDir(), "Dockerfile", "reg/app:v1", map[string]string{"b": "2", "a": "1"}, discardLogger()); err != nil {
		t.Fatal(err)
	}
	if ran := readFile(t, args); !strings.Contains(ran, "--label a=1 --label b=2") {
		t.Errorf("ran: %s", ran)
	}
}
