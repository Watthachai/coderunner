package domain

// JobStatus is the lifecycle state of a single build job.
//
// Allowed transitions (see CRN-architecture.md §3 State Machine):
//
//	queued    -> building | cancelled
//	building  -> done | failed | cancelled
//	done      -> (terminal)
//	failed    -> (terminal)
//	cancelled -> (terminal)
type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobBuilding  JobStatus = "building"
	JobDone      JobStatus = "done"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

// IsTerminal reports whether no further transition is possible from s.
func (s JobStatus) IsTerminal() bool {
	switch s {
	case JobDone, JobFailed, JobCancelled:
		return true
	default:
		return false
	}
}

// Valid reports whether s is a known JobStatus value.
func (s JobStatus) Valid() bool {
	switch s {
	case JobQueued, JobBuilding, JobDone, JobFailed, JobCancelled:
		return true
	default:
		return false
	}
}

// ProjectStatus is the registry-level state of a project.
type ProjectStatus string

const (
	ProjectPending  ProjectStatus = "pending"
	ProjectActive   ProjectStatus = "active"
	ProjectArchived ProjectStatus = "archived"
)

// EditRequestStatus is the lifecycle state of an external edit request.
type EditRequestStatus string

const (
	EditPending     EditRequestStatus = "pending"
	EditMergedToJob EditRequestStatus = "merged_to_job"
	EditRejected    EditRequestStatus = "rejected"
)

// EditPriority is the requested urgency of an edit request.
type EditPriority string

const (
	PriorityNormal EditPriority = "normal"
	PriorityUrgent EditPriority = "urgent"
)

// BuildEventType is the kind of notification emitted to the central DB
// (build_events table) for fan-out to FBD and FTC DV.
type BuildEventType string

const (
	EventBuildStarted   BuildEventType = "build_started"
	EventBuildDone      BuildEventType = "build_done"
	EventBuildFailed    BuildEventType = "build_failed"
	EventBuildCancelled BuildEventType = "build_cancelled"
)
