// Package domain holds the shared types and the cross-package interfaces
// (ports) for CRN. Every other internal package codes against the interfaces
// declared here — never against concrete implementations.
//
// Layering rule: this package MUST NOT import any other internal package.
// It depends only on the standard library so it can be imported everywhere
// without creating cycles.
package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Org identifies a tenant. Each org gets exactly one API key and is limited
// to one concurrently-building job (CRN-architecture.md §2.4).
type Org struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Project is a registry entry for one buildable codebase owned by an org.
type Project struct {
	ID           uuid.UUID     `json:"id"`
	OrgID        uuid.UUID     `json:"org_id"`
	Name         string        `json:"name"`
	Status       ProjectStatus `json:"status"`
	CurrentBuild int           `json:"current_build"`
	Stack        string        `json:"stack"`
	// RepoURL is the project's dedicated GitHub repo https clone URL under the
	// "one repo per project" model (empty when that model is disabled or no repo
	// has been created for the project yet).
	RepoURL   string    `json:"repo_url"`
	CreatedAt time.Time `json:"created_at"`
}

// Job is a single unit of work in the build queue — the heart of the state
// machine. payload carries the requirement + assets handed to Claude Code.
type Job struct {
	ID         uuid.UUID       `json:"id"`
	ProjectID  uuid.UUID       `json:"project_id"`
	OrgID      uuid.UUID       `json:"org_id"`
	Status     JobStatus       `json:"status"`
	BuildNo    int             `json:"build_no"`
	Payload    json.RawMessage `json:"payload"`
	SessionID  string          `json:"session_id,omitempty"` // Claude Code session for --resume
	DockerTag  string          `json:"docker_tag,omitempty"` // {registry}/{project_id}:v{build_no}
	ErrorMsg   string          `json:"error_msg,omitempty"`
	QueuedAt   time.Time       `json:"queued_at"`
	StartedAt  *time.Time      `json:"started_at,omitempty"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
}

// EditRequest is an external "please change X" request (CRN-architecture.md §6).
// It either spawns a job immediately (org idle) or is queued (org building).
type EditRequest struct {
	ID          uuid.UUID         `json:"id"`
	ProjectID   uuid.UUID         `json:"project_id"`
	JobID       *uuid.UUID        `json:"job_id,omitempty"`
	Requester   string            `json:"requester"`
	DiffRequest json.RawMessage   `json:"diff_request"` // { "change": "...", "files": [...] }
	Priority    EditPriority      `json:"priority"`
	Status      EditRequestStatus `json:"status"`
	CreatedAt   time.Time         `json:"created_at"`
}

// BuildEvent is a notification row written to the central DB for fan-out to
// FBD and FTC DV (CRN-architecture.md §2.2 build_events).
type BuildEvent struct {
	ID            uuid.UUID       `json:"id"`
	JobID         uuid.UUID       `json:"job_id"`
	EventType     BuildEventType  `json:"event_type"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	NotifiedFBD   bool            `json:"notified_fbd"`
	NotifiedFTCDV bool            `json:"notified_ftcdv"`
}

// APIKey is the per-org credential for the external edit API. Only the hash is
// ever persisted; the plaintext (sk-org-{org_id}-{random_32}) is shown once at
// creation time (CRN-architecture.md §6 Auth).
type APIKey struct {
	ID        uuid.UUID  `json:"id"`
	OrgID     uuid.UUID  `json:"org_id"`
	Hash      string     `json:"-"` // never serialized
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// TriggerRequest is the minimal signal FTC DV sends to wake CRN for a queued
// job (CRN-architecture.md §2.3). CRN re-reads authoritative state from the
// store; the trigger is just a notification, not a source of truth.
type TriggerRequest struct {
	JobID     uuid.UUID `json:"job_id"`
	ProjectID uuid.UUID `json:"project_id"`
	OrgID     uuid.UUID `json:"org_id"`
}

// ProjectStatusView is the read model returned by GET /status.
type ProjectStatusView struct {
	Status       JobStatus `json:"status"`
	CurrentBuild int       `json:"current_build"`
	QueueDepth   int       `json:"queue_depth"`
	SessionID    string    `json:"session_id,omitempty"`
	DockerTag    string    `json:"docker_tag,omitempty"`
}
