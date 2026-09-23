package domain

import "time"

// BuildSettings is the operator's saved choice of Claude model and effort for
// builds — the single row of build_settings. It is absent until someone saves
// it from the dashboard; until then the environment's defaults apply.
type BuildSettings struct {
	Model  string
	Effort string
	// UpdatedAt is set by the store on save.
	UpdatedAt time.Time
	// UpdatedFrom is the client address that saved it. /internal has no auth,
	// so this is the only record of who changed a setting that drives spend.
	UpdatedFrom string
}
