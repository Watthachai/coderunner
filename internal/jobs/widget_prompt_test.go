package jobs

import (
	"strings"
	"testing"
)

// Where the 📋 window is on, CRN's route sends the request's cookie to the company
// fittcore to learn who the user is, for the window's calls and, since 25 Nov 2026, for
// 💬 reports (app_user_ref). AEC build 10 sent the app's own session cookie there,
// because that app keeps its fittcore session on its server: the agent is told the app
// must hand the route the fittcore cookie on every one of those calls, never its own.
func TestTheAgentIsToldWhichCookieTheRouteSendsOut(t *testing.T) {
	p := widgetPrompt()
	for _, want := range []string{"FITT_TICKET_WINDOW=1", "PLATFORM_FITTCORE_ORIGIN", "/api/auth/get-session", "?op=", "POST /api/fitt-feedback without op", "never its own",
		// Since 25 Nov 2026 the route also sends a 💬 report on to FITT_FEEDBACK_URL with the
		// user's fittcore cookies, and only those (DV: the fittcore asks who sent it); every
		// fittcore names them paperclip-<instance id>.*.
		"paperclip-<instance id>.session_token", "under the names the fittcore set"} {
		if !strings.Contains(p, want) {
			t.Errorf("the widget prompt lacks %q", want)
		}
	}
}

// Since 431eafb (27 Nov 2026) the route takes a POST only with X-Fitt-Feedback: 1, as the
// widget sends it; CRN's own checker lacked it and NTC Film Job #11 failed on a 403. An
// app test that posts to the route needs it too.
func TestTheAgentIsToldAPostToTheRouteCarriesTheWidgetsMark(t *testing.T) {
	p := widgetPrompt()
	for _, want := range []string{"X-Fitt-Feedback: 1", "403"} {
		if !strings.Contains(p, want) {
			t.Errorf("the widget prompt lacks %q", want)
		}
	}
}
