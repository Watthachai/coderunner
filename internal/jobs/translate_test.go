package jobs

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// The live console showed every Bash call as a bare "Bash $": the dashboard reads a Bash
// command from text, and CRN put it in file, where FILES also counted it as a file
// (OKR build 4, 13 Nov).

func toolUse(name, input string) domain.ClaudeEvent {
	return domain.ClaudeEvent{Type: domain.ClaudeAssistant, ToolName: name, ToolInput: json.RawMessage(input)}
}

func TestABashCallCarriesItsCommandAsText(t *testing.T) {
	msg := translate(uuid.New(), toolUse("Bash", `{"command":"git fetch origin\ngit status --short"}`))
	if msg.Kind != domain.WSToolCall || msg.Tool != "Bash" {
		t.Fatalf("not a Bash tool call: %+v", msg)
	}
	if msg.Text != "git fetch origin git status --short" {
		t.Errorf("text = %q, want the command on one line", msg.Text)
	}
	if msg.File != "" {
		t.Errorf("file = %q, want none: a command is not a file", msg.File)
	}
}

func TestAFileToolStillNamesItsFile(t *testing.T) {
	msg := translate(uuid.New(), toolUse("Read", `{"file_path":"/w/Dockerfile"}`))
	if msg.File != "/w/Dockerfile" {
		t.Errorf("file = %q, want /w/Dockerfile", msg.File)
	}
}

func TestASearchCarriesItsPatternAsText(t *testing.T) {
	msg := translate(uuid.New(), toolUse("Grep", `{"pattern":"STORAGE|uploads"}`))
	if msg.Text != "STORAGE|uploads" || msg.File != "" {
		t.Errorf("text = %q, file = %q; want the pattern as text and no file", msg.Text, msg.File)
	}
}
