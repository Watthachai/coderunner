package domain

import "errors"

// Sentinel errors shared across packages. Implementations return these (wrapped
// with %w if adding context) and callers match with errors.Is.
var (
	// ErrNotFound is returned by Store reads when no row matches.
	ErrNotFound = errors.New("domain: not found")

	// ErrProjectLocked is returned by Store.AcquireProjectLock when another
	// session already holds the project's advisory lock (that project has a
	// build in progress). Concurrency is bounded per PROJECT, not per org:
	// different projects build in parallel.
	ErrProjectLocked = errors.New("domain: project build lock held")

	// ErrInvalidTransition is returned when a JobStatus change is not allowed by
	// the state machine.
	ErrInvalidTransition = errors.New("domain: invalid job status transition")

	// ErrUnauthorized is returned by auth middleware / Store on a bad or revoked
	// API key.
	ErrUnauthorized = errors.New("domain: unauthorized")

	// ErrSkillBuiltin is returned by Store.DeleteSkill when the target skill is
	// built-in (is_builtin = true) and therefore not deletable. The api layer
	// maps it to HTTP 409.
	ErrSkillBuiltin = errors.New("domain: skill is built-in and cannot be deleted")
)
