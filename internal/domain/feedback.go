package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// feedback.go — the in-demo feedback / edit-request read model. A CRN-built
// demo's feedback widget INSERTs one row via PostgREST (public, write-only
// web_anon role); CRN reads them for the Edit Request Panel and can approve one
// into an edit build. See migration 0007 and the feedback-loop design spec.

// FeedbackPin is one element the reporter pinned on the live page. Box and the
// region screenshot are stored/relayed opaquely (CRN doesn't interpret them);
// Selector/Label/Note drive the approve -> edit prompt.
type FeedbackPin struct {
	Selector   string          `json:"selector"`
	Label      string          `json:"label"`
	Note       string          `json:"note"`
	Box        json.RawMessage `json:"box,omitempty"`
	RegionShot string          `json:"region_shot"`
}

// FeedbackPayload is the widget-captured detail (JSONB column). Viewport is
// relayed opaquely; screenshots are base64 data URIs stored inline.
type FeedbackPayload struct {
	Pins      []FeedbackPin   `json:"pins"`
	FullShot  string          `json:"full_shot"`
	Viewport  json.RawMessage `json:"viewport,omitempty"`
	UserAgent string          `json:"user_agent"`
}

// FeedbackRequest mirrors one feedback_requests row (migration 0007).
type FeedbackRequest struct {
	ID        uuid.UUID       `json:"id"`
	ProjectID uuid.UUID       `json:"project_id"`
	Status    string          `json:"status"`   // new|reviewing|approved|building|done|rejected
	Category  string          `json:"category"` // bug|feature|style
	Priority  string          `json:"priority"` // low|med|high
	Note      string          `json:"note"`
	PageURL   string          `json:"page_url"`
	Reporter  string          `json:"reporter"`
	Payload   FeedbackPayload `json:"payload"`
	JobID     *uuid.UUID      `json:"job_id"`
	CreatedAt time.Time       `json:"created_at"`
}
