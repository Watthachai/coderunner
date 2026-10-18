package buildstep

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

// RuntimeContract is the version of the run-anywhere rules (fitt-build skill,
// references/runs-anywhere.md) an image was built and checked under. The deploy
// side in GCP refuses to put an image without it on the cloud: such an app may
// write files where Cloud Run cannot keep them.
const RuntimeContract = "1"

// ImageLabels names what a delivered image is and where it came from, so whoever
// holds only the image — the deploy side in GCP, FTCDV — can tell its project,
// build and commit without asking CRN.
type ImageLabels struct {
	ProjectID, JobID, Commit string
	BuildNo                  int
	// Schedule is the app's crn.schedule.json as compact JSON (ReadSchedule):
	// on Cloud Run, Cloud Scheduler calls these jobs instead of the app itself.
	Schedule string
}

// Map is the labels as docker build takes them. A build pushed without a commit
// (no git remote configured) names no revision rather than an empty one.
func (l ImageLabels) Map() map[string]string {
	m := map[string]string{
		"fitt.crn.project_id": l.ProjectID,
		"fitt.crn.job_id":     l.JobID,
		"fitt.crn.build_no":   strconv.Itoa(l.BuildNo),
		"fitt.crn.contract":   RuntimeContract,
		"fitt.crn.schedule":   l.Schedule,
	}
	if l.Commit != "" {
		m["org.opencontainers.image.revision"] = l.Commit
	}
	return m
}

// ReadSchedule returns the app's crn.schedule.json compacted, or "[]" when the
// app declares no scheduled work. The release checker has already validated
// each entry; this only refuses a file that is not a JSON array at all.
func ReadSchedule(workDir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(workDir, "crn.schedule.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return "[]", nil
	}
	if err != nil {
		return "", fmt.Errorf("read crn.schedule.json: %w", err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return "", fmt.Errorf("crn.schedule.json must be a JSON array: %w", err)
	}
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		return "", fmt.Errorf("crn.schedule.json: %w", err)
	}
	return out.String(), nil
}
