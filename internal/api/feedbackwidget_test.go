package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// Build requests may opt into the 💬 feedback widget. The strict decoder must
// accept the field, and an edit request must hand it on to the build.
func TestRequestsAcceptFeedbackWidget(t *testing.T) {
	var ingest ingestBody
	r := httptest.NewRequest("POST", "/internal/projects", strings.NewReader(`{"project_id":"p","name":"Hello Fitt","feedback_widget":true}`))
	if err := decodeJSON(r, &ingest); err != nil {
		t.Fatalf("ingest with feedback_widget rejected: %v", err)
	}
	if ingest.FeedbackWidget == nil || !*ingest.FeedbackWidget {
		t.Errorf("ingest FeedbackWidget = %v, want true", ingest.FeedbackWidget)
	}

	var edit editProjectBody
	r = httptest.NewRequest("POST", "/internal/projects/p/edit", strings.NewReader(`{"change":"x","feedback_widget":false}`))
	if err := decodeJSON(r, &edit); err != nil {
		t.Fatalf("edit with feedback_widget rejected: %v", err)
	}
	if edit.FeedbackWidget == nil || *edit.FeedbackWidget {
		t.Errorf("edit FeedbackWidget = %v, want false", edit.FeedbackWidget)
	}
}

func TestEditPayloadCarriesFeedbackWidget(t *testing.T) {
	off := false
	for _, tc := range []struct {
		widget *bool
		want   string // the feedback_widget value in the payload, "" = absent
	}{{nil, ""}, {&off, "false"}} {
		raw, err := editPayload("x", "Hello Fitt", tc.widget)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if string(got["feedback_widget"]) != tc.want {
			t.Errorf("widget %v: payload feedback_widget = %q, want %q", tc.widget, got["feedback_widget"], tc.want)
		}
		if string(got["mode"]) != `"edit"` || string(got["name"]) != `"Hello Fitt"` || string(got["change"]) != `"x"` {
			t.Errorf("edit payload lost its fields: %s", raw)
		}
	}
}
