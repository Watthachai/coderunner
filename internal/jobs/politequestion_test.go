package jobs

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/buildstep"
)

// Customers ask for changes politely, as questions: "ย้ายปุ่มบันทึกไปไว้ใต้ตารางได้ไหม".
// Builds answered "ทำได้ครับ" and changed nothing (23 Nov 2026). Such a question asks for
// the change; only a question that asks for information is answered without changing.
func TestAPoliteQuestionIsAChangeToMake(t *testing.T) {
	items := []buildstep.Item{{ID: "1", Title: "ย้ายปุ่มบันทึกไปไว้ใต้ตารางได้ไหม"}}
	for name, prompt := range map[string]string{
		"edit":     editPrompt("ย้ายปุ่มบันทึกไปไว้ใต้ตารางได้ไหม"),
		"items":    itemsPrompt(items, uuid.New()),
		"reviewer": reviewPrompt(items, "x", "/tmp/d", nil),
	} {
		if !strings.Contains(prompt, "ได้ไหม") || !strings.Contains(prompt, "asks for information") {
			t.Errorf("%s prompt does not say a polite question asks for the change:\n%s", name, prompt)
		}
	}
}
