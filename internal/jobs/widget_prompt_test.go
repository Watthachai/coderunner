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
	for _, want := range []string{"FITT_TICKET_WINDOW=1", "PLATFORM_FITTCORE_ORIGIN", "/api/auth/get-session", "?op=", "POST /api/fitt-feedback without op", "never its own"} {
		if !strings.Contains(p, want) {
			t.Errorf("the widget prompt lacks %q", want)
		}
	}
}
