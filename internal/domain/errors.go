package domain

import "errors"

// Sentinel errors shared across packages. Implementations return these (wrapped
// with %w if adding context) and callers match with errors.Is.
var (
	// ErrNotFound is returned by Store reads when no row matches.
	ErrNotFound = errors.New("domain: not found")

	// ErrOrgLocked is returned by Store.AcquireOrgLock when another session
	// already holds the org's advisory lock (a build is in progress).
	ErrOrgLocked = errors.New("domain: org build lock held")

	// ErrInvalidTransition is returned when a JobStatus change is not allowed by
	// the state machine.
	ErrInvalidTransition = errors.New("domain: invalid job status transition")

	// ErrUnauthorized is returned by auth middleware / Store on a bad or revoked
	// API key.
	ErrUnauthorized = errors.New("domain: unauthorized")
)
