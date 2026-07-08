package jobs

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCodeFromToolInput(t *testing.T) {
	cases := []struct {
		name, tool, input     string
		wantBefore, wantAfter string
	}{
		{"write", "Write", `{"file_path":"a.ts","content":"line1\nline2"}`, "", "line1\nline2"},
		{"edit", "Edit", `{"file_path":"a.ts","old_string":"foo","new_string":"bar"}`, "foo", "bar"},
		{"multiedit", "MultiEdit", `{"edits":[{"old_string":"a","new_string":"b"},{"old_string":"c","new_string":"d"}]}`, "a\nc\n", "b\nd\n"},
		{"bash", "Bash", `{"command":"ls"}`, "", ""},
		{"empty", "Write", ``, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before, after := codeFromToolInput(c.tool, json.RawMessage(c.input))
			if before != c.wantBefore || after != c.wantAfter {
				t.Errorf("got (%q,%q), want (%q,%q)", before, after, c.wantBefore, c.wantAfter)
			}
		})
	}
}

func TestCapDiff(t *testing.T) {
	if got := capDiff("short"); got != "short" {
		t.Errorf("short capped: %q", got)
	}
	long := strings.Repeat("x", maxDiffRunes+500)
	if got := capDiff(long); !strings.HasSuffix(got, "… (truncated)") {
		t.Errorf("long: missing truncation marker")
	}
	// Rune-safe: a multibyte string over the cap must not produce a broken rune.
	thai := strings.Repeat("ก", maxDiffRunes+10)
	got := capDiff(thai)
	if strings.ContainsRune(got, '�') {
		t.Errorf("cap split a multibyte rune")
	}
}
