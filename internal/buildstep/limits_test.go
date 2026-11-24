package buildstep

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

// DV's relay and Gateway take a report only within their limits (DV, 24 Nov 2026):
// the note at most 4,000 characters, at most 20 pins, each pin's note at most 1,000.
// A customer who went over saw only "HTTP 422". The widget checks before sending and
// says in Thai which field is too long and by how much.

// widgetLimits runs the widget's limits block in node and returns what calls, a
// JavaScript expression, comes to, as JSON.
func widgetLimits(t *testing.T, calls string) string {
	t.Helper()
	start := strings.Index(feedbackWidgetJS, "// --- DV's limits")
	end := strings.Index(feedbackWidgetJS, "// --- end of DV's limits")
	if start < 0 || end < start {
		t.Fatal("the widget has no limits block")
	}
	script := feedbackWidgetJS[start:end] + "\nconsole.log(JSON.stringify(" + calls + "));"
	out, err := exec.Command("node", "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func overLimitsOf(t *testing.T, note, pins string) []string {
	t.Helper()
	var over []string
	raw := widgetLimits(t, "overLimits("+note+", "+pins+", \"สิ่งที่อยากให้แก้\")")
	if err := json.Unmarshal([]byte(raw), &over); err != nil {
		t.Fatalf("overLimits gave %s: %v", raw, err)
	}
	return over
}

func TestTwentyPinsOfAThousandCharactersAreWithinTheLimits(t *testing.T) {
	over := overLimitsOf(t, `"ย้ายปุ่มบันทึกไปไว้ใต้ตาราง\nเปลี่ยนสีหัวตาราง"`,
		`Array.from({ length: 20 }, () => ({ note: "ก".repeat(1000) }))`)
	if len(over) != 0 {
		t.Errorf("20 pins of 1,000 characters were held back: %q", over)
	}
	if over := overLimitsOf(t, `"ก".repeat(4000)`, `[]`); len(over) != 0 {
		t.Errorf("a note of 4,000 characters was held back: %q", over)
	}
}

func TestTheWidgetSaysWhichFieldIsTooLongAndByHowMuch(t *testing.T) {
	cases := []struct {
		name, note, pins string
		want             []string
	}{
		{"note", `"ก".repeat(4250)`, `[]`, []string{`"สิ่งที่อยากให้แก้"`, "4,250", "เกินมา 250 ", "4,000"}},
		{"pin note", `"ย้ายปุ่ม"`, `[{ note: "" }, { note: "" }, { note: "ก".repeat(1001) }]`, []string{"จุดที่ 3", "1,001", "เกินมา 1 ", "1,000"}},
		{"pins", `"ย้ายปุ่ม"`, `Array.from({ length: 23 }, () => ({ note: "" }))`, []string{"23 จุด", "เกินมา 3 จุด", "20"}},
	}
	for _, c := range cases {
		over := overLimitsOf(t, c.note, c.pins)
		if len(over) != 1 {
			t.Errorf("%s: want one thing said, got %q", c.name, over)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(over[0], w) {
				t.Errorf("%s: %q does not say %q", c.name, over[0], w)
			}
		}
	}
}

func TestTheWidgetPutsARefusedReportInThai(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{`{"error":"invalid_payload","issues":[{"path":"note","code":"too_big"}]}`, "สิ่งที่อยากให้แก้"},
		{`{"error":"invalid_payload","issues":[{"path":["payload","pins",2,"note"],"code":"too_big"}]}`, "จุดที่ 3"},
		{`{"error":"invalid_payload","issues":[{"path":"payload.pins.2.note","code":"too_big"}]}`, "จุดที่ 3"},
	} {
		raw := widgetLimits(t, "refusal(422, "+jsString(c.body)+")")
		var said string
		if err := json.Unmarshal([]byte(raw), &said); err != nil {
			t.Fatalf("refusal gave %s: %v", raw, err)
		}
		if strings.Contains(said, "HTTP") || !strings.Contains(said, c.want) || !strings.Contains(said, "ยาวเกิน") {
			t.Errorf("422 %s: the customer reads %q, want Thai naming %q", c.body, said, c.want)
		}
	}
	if raw := widgetLimits(t, `refusal(500, "boom")`); raw != `"HTTP 500"` {
		t.Errorf("other refusals: %s, want HTTP 500", raw)
	}
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// The note is what the customer wrote; each pin's note goes in that pin. The widget
// checks the report it is about to send, pictures attached in the chat included.
func TestTheWidgetChecksTheReportItSends(t *testing.T) {
	for _, want := range []string{
		`var all = pins.concat(fromChat);`,
		`var over = overLimits(note, all, noteLbl.textContent);`,
		`note: note,`,
		`pins: all,`,
		`throw new Error(refusal(res.status, text));`,
	} {
		if !strings.Contains(feedbackWidgetJS, want) {
			t.Errorf("the widget lacks %s", want)
		}
	}
}

// The route logs each report's size and what the receiver answered, so a refusal like
// DV's 422 can be traced to what was sent (DV asked, 24 Nov 2026), and never what the
// customer wrote.
func TestTheRouteLogsTheReportsSizeNotItsWords(t *testing.T) {
	route := feedbackRouteFor(t, "layout.tsx")
	status := http.StatusCreated
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(status)
	}))
	defer receiver.Close()
	report := `JSON.stringify({ note: "ย้ายปุ่มบันทึก", payload: { pins: [{ note: "สีเขียว" }, { note: "ก".repeat(1000) }, {}] } })`

	for _, c := range []struct {
		status  int
		want    string
		receive string
	}{
		{http.StatusCreated, "204", `"receiver":201`},
		{http.StatusUnprocessableEntity, "422", `"receiver":422`},
	} {
		status = c.status
		answer, logged := postToFeedbackRouteLogged(t, route, receiver.URL, report)
		if !strings.HasPrefix(answer, c.want) {
			t.Errorf("receiver %d: route answered %q", c.status, answer)
		}
		if len(logged) != 1 {
			t.Fatalf("receiver %d: want one line logged, got %q", c.status, logged)
		}
		for _, w := range []string{`"note":14`, `"pins":3`, `"pin_note_max":1000`, c.receive} {
			if !strings.Contains(logged[0], w) {
				t.Errorf("receiver %d: %q does not say %s", c.status, logged[0], w)
			}
		}
		if strings.Contains(logged[0], "ย้าย") || strings.Contains(logged[0], "ก") || strings.Contains(logged[0], "สี") {
			t.Errorf("the log holds what the customer wrote: %q", logged[0])
		}
	}

	if _, logged := postToFeedbackRouteLogged(t, route, "", report); len(logged) != 1 || !strings.Contains(logged[0], `"receiver":"unreachable"`) {
		t.Errorf("unreachable receiver: logged %q", logged)
	}
}

// The route passes a report of 20 pins of 1,000 characters on as it came: the note
// stays the customer's text and each pin keeps its own note.
func TestTheRoutePassesTwentyPinsOfAThousandCharactersOn(t *testing.T) {
	route := feedbackRouteFor(t, "layout.tsx")
	got := make(chan []byte, 1)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got <- raw
		w.WriteHeader(http.StatusCreated)
	}))
	defer receiver.Close()
	report := `JSON.stringify({ note: "ย้ายปุ่มบันทึกไปไว้ใต้ตาราง", payload: { pins: Array.from({ length: 20 }, (_, i) =>
  ({ selector: "region", label: "จุด " + (i + 1), note: "ก".repeat(1000), box: { x: 0, y: 0, w: 10, h: 10 }, region_shot: "" })) } })`
	if out := postToFeedbackRoute(t, route, receiver.URL, report); out != "204" {
		t.Fatalf("route answered %q, want 204", out)
	}
	var sent struct {
		Note    string `json:"note"`
		Payload struct {
			Pins []struct {
				Note string `json:"note"`
			} `json:"pins"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(<-got, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Note != "ย้ายปุ่มบันทึกไปไว้ใต้ตาราง" || len(sent.Payload.Pins) != 20 {
		t.Fatalf("receiver got note %q and %d pins", sent.Note, len(sent.Payload.Pins))
	}
	for i, p := range sent.Payload.Pins {
		if p.Note != strings.Repeat("ก", 1000) {
			t.Errorf("pin %d reached the receiver with a note of %d characters", i+1, len([]rune(p.Note)))
		}
	}
}
