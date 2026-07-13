package feedback

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

func TestIssueContent(t *testing.T) {
	f := &domain.FeedbackRequest{
		ID:        uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		Category:  "bug",
		Priority:  "high",
		Note:      "Button color is wrong\nsecond line",
		PageURL:   "http://localhost:3002/",
		CreatedAt: time.Unix(0, 0).UTC(),
		Payload: domain.FeedbackPayload{
			Pins: []domain.FeedbackPin{{Label: "btn", Selector: "button.buy", Note: "make it green"}},
		},
	}
	title, body, labels := IssueContent(f)

	if title != "[feedback] bug: Button color is wrong" {
		t.Errorf("title=%q", title)
	}
	for _, want := range []string{"fitt:feedback", "type:bug", "prio:high"} {
		found := false
		for _, l := range labels {
			if l == want {
				found = true
			}
		}
		if !found {
			t.Errorf("labels missing %q: %v", want, labels)
		}
	}
	for _, frag := range []string{"**Priority:** high", "button.buy", "make it green", "Mirrored from CRN feedback 11111111"} {
		if !strings.Contains(body, frag) {
			t.Errorf("body missing %q\n---\n%s", frag, body)
		}
	}
}
