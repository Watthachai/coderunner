package domain

import (
	"time"

	"github.com/google/uuid"
)

// dashboard.go holds the read-model returned by GET /internal/dashboard — a
// single point-in-time snapshot an operator console renders: vitals (counts),
// what is building now, the incoming FIFO queue, every project with its latest
// status, and a recent activity feed. The json tags here are a frontend
// contract; do not change them without coordinating with the console.

// DashboardSnapshot is the full operator-console read model.
type DashboardSnapshot struct {
	Vitals      DashboardVitals `json:"vitals"`
	Building    []BuildingJob   `json:"building"`
	Queue       []QueuedJob     `json:"queue"`
	Projects    []ProjectRow    `json:"projects"`
	Activity    []ActivityRow   `json:"activity"`
	GeneratedAt time.Time       `json:"generated_at"`
}

// DashboardVitals are the headline counts shown at the top of the console.
type DashboardVitals struct {
	Projects    int `json:"projects"`
	Queued      int `json:"queued"`
	Building    int `json:"building"`
	DoneToday   int `json:"done_today"`
	FailedToday int `json:"failed_today"`
}

// BuildingJob is one in-flight build (job status='building'), enriched with
// project + org names for display. StartedAt is null until the job entered
// 'building'.
type BuildingJob struct {
	JobID       uuid.UUID  `json:"job_id"`
	ProjectID   uuid.UUID  `json:"project_id"`
	ProjectName string     `json:"project_name"`
	OrgName     string     `json:"org_name"`
	BuildNo     int        `json:"build_no"`
	Branch      string     `json:"branch"`
	StartedAt   *time.Time `json:"started_at"`
}

// QueuedJob is one waiting build (job status='queued'), enriched with project
// + org names. The queue is rendered FIFO (queued_at ASC).
type QueuedJob struct {
	JobID       uuid.UUID `json:"job_id"`
	ProjectID   uuid.UUID `json:"project_id"`
	ProjectName string    `json:"project_name"`
	OrgName     string    `json:"org_name"`
	BuildNo     int       `json:"build_no"`
	QueuedAt    time.Time `json:"queued_at"`
}

// ProjectRow is one project with its registry state and a summary of its latest
// job. The last_* fields are empty/null when the project has no jobs yet.
type ProjectRow struct {
	ID             uuid.UUID  `json:"id"`
	Name           string     `json:"name"`
	OrgName        string     `json:"org_name"`
	Status         string     `json:"status"`
	CurrentBuild   int        `json:"current_build"`
	LastStatus     string     `json:"last_status"`
	LastBranch     string     `json:"last_branch"`
	LastActivityAt *time.Time `json:"last_activity_at"`
}

// ActivityRow is one recent build_events entry, enriched with the project name
// and the originating job's build number.
type ActivityRow struct {
	Type        string    `json:"type"`
	ProjectID   uuid.UUID `json:"project_id"`
	ProjectName string    `json:"project_name"`
	BuildNo     int       `json:"build_no"`
	At          time.Time `json:"at"`
}
