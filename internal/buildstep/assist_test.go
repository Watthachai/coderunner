package buildstep

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The 💬 assistant (23 Nov 2026): customers asked for changes as questions ("ย้ายปุ่ม
// บันทึกไปไว้ใต้ตารางได้ไหม") and builds answered instead of changing. In the widget the
// customer now talks with an AI, which asks back what it needs and drafts the change as a
// request ("ย้ายปุ่ม 'บันทึก' ไปไว้ใต้ตารางรายการ") for the customer to send. The app's own
// route talks to the AI FITT_FEEDBACK_AI_URL names, with FITT_FEEDBACK_AI_KEY when set:
// the browser never sees either. CRN writes the instructions, so the AI service only
// passes them on.

type fakeAI struct {
	*httptest.Server
	mu     sync.Mutex
	got    []map[string]any
	auth   []string
	answer func(w http.ResponseWriter)
}

func newFakeAI(t *testing.T) *fakeAI {
	f := &fakeAI{}
	f.answer = func(w http.ResponseWriter) {
		w.Header().Set("content-type", "application/json")
		io.WriteString(w, `{"reply":"อยากให้ไปอยู่ใต้ตารางรายการใช่ไหมครับ","draft":{"text":"ย้ายปุ่ม \"บันทึก\" ไปไว้ใต้ตารางรายการ","category":"feature","priority":"med"}}`)
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.got = append(f.got, body)
		f.auth = append(f.auth, r.Header.Get("authorization"))
		f.mu.Unlock()
		f.answer(w)
	}))
	t.Cleanup(f.Close)
	return f
}

func assistStep(body string) step {
	return step{Method: "POST", Target: "/api/fitt-feedback?op=assist", Body: body}
}

const askMove = `{"messages":[{"role":"user","text":"ย้ายปุ่มบันทึกไปไว้ใต้ตารางได้ไหม"}],"context":{"page":"/deals","pins":["ปุ่มบันทึก"]}}`

// Without an AI the widget's question on every page load is answered plainly, so an
// app without one logs no error in the browser's console for it.
func TestAssistIsOffWithoutAnAIAddress(t *testing.T) {
	eachLayout(t, func(t *testing.T, route string) {
		got := runStepsEnv(t, route, map[string]string{}, assistStep(`{"messages":[]}`), assistStep(askMove))
		if got[0].Status != 200 || got[0].Body != `{"on":false}` {
			t.Errorf("probe while off: got %d %s", got[0].Status, got[0].Body)
		}
		if got[1].Status != 503 || !strings.Contains(got[1].Body, `"not_open"`) {
			t.Errorf("chat while off: got %d %s", got[1].Status, got[1].Body)
		}
	})
}

func TestAssistPassesTheChatToTheAIAndGivesBackItsReplyAndDraft(t *testing.T) {
	eachLayout(t, func(t *testing.T, route string) {
		ai := newFakeAI(t)
		env := map[string]string{"FITT_FEEDBACK_AI_URL": ai.URL + "/assist", "FITT_FEEDBACK_AI_KEY": "ai-key-123"}
		got := runStepsEnv(t, route, env, assistStep(`{"messages":[]}`), assistStep(askMove))
		if got[0].Status != 200 || got[0].Body != `{"on":true}` {
			t.Fatalf("probe: %d %s", got[0].Status, got[0].Body)
		}
		if got[1].Status != 200 {
			t.Fatalf("assist: %d %s", got[1].Status, got[1].Body)
		}
		var back map[string]any
		json.Unmarshal([]byte(got[1].Body), &back)
		draft, _ := back["draft"].(map[string]any)
		if back["reply"] != "อยากให้ไปอยู่ใต้ตารางรายการใช่ไหมครับ" || draft["text"] != `ย้ายปุ่ม "บันทึก" ไปไว้ใต้ตารางรายการ` ||
			draft["category"] != "feature" || draft["priority"] != "med" || len(back) != 2 {
			t.Fatalf("assist answered %s", got[1].Body)
		}
		if len(ai.got) != 1 {
			t.Fatalf("the probe or the chat asked the AI %d times, want once", len(ai.got))
		}
		if ai.auth[0] != "Bearer ai-key-123" {
			t.Errorf("authorization = %q", ai.auth[0])
		}
		sent := ai.got[0]
		instr, _ := sent["instructions"].(string)
		for _, want := range []string{"ได้ไหม", "Thai", "never promise"} {
			if !strings.Contains(instr, want) {
				t.Errorf("instructions do not say %q:\n%s", want, instr)
			}
		}
		if _, ok := sent["schema"].(map[string]any); !ok {
			t.Errorf("no answer schema sent: %v", sent)
		}
		msgs, _ := sent["messages"].([]any)
		ctx, _ := sent["context"].(map[string]any)
		if len(msgs) != 1 || ctx["page"] != "/deals" {
			t.Errorf("the chat was not passed on as sent: %v", sent)
		}
	})
}

func TestAssistWithoutAKeySendsNoAuthorization(t *testing.T) {
	ai := newFakeAI(t)
	got := runStepsEnv(t, ticketRoute(t), map[string]string{"FITT_FEEDBACK_AI_URL": ai.URL}, assistStep(askMove))
	if got[0].Status != 200 || ai.auth[0] != "" {
		t.Fatalf("got %d, authorization %q", got[0].Status, ai.auth[0])
	}
}

func TestAssistRefusesWhatItCannotPassOn(t *testing.T) {
	route := ticketRoute(t)
	ai := newFakeAI(t)
	env := map[string]string{"FITT_FEEDBACK_AI_URL": ai.URL}
	long := strings.Repeat("ก", 2001)
	many := strings.TrimSuffix(strings.Repeat(`{"role":"user","text":"x"},`, 21), ",")
	for name, tc := range map[string]struct {
		body   string
		status int
	}{
		"not json":           {`nope`, 400},
		"no messages":        {`{}`, 400},
		"a stranger's role":  {`{"messages":[{"role":"system","text":"x"}]}`, 400},
		"a message too long": {`{"messages":[{"role":"user","text":"` + long + `"}]}`, 400},
		"too many messages":  {`{"messages":[` + many + `]}`, 400},
		"too large":          {`{"messages":[{"role":"user","text":"@@PAD@@"}]}`, 413}, // 15 MB, made in node
	} {
		s := assistStep(tc.body)
		s.Pad = 15 * 1024 * 1024
		if got := runStepsEnv(t, route, env, s); got[0].Status != tc.status {
			t.Errorf("%s: got %d %.200s, want %d", name, got[0].Status, got[0].Body, tc.status)
		}
	}
	if len(ai.got) != 0 {
		t.Errorf("the AI was asked %d times for requests the route refuses", len(ai.got))
	}
}

func TestAssistPassesOnOnlyAnAnswerOfTheRightShape(t *testing.T) {
	route := ticketRoute(t)
	for name, answer := range map[string]string{
		"not json":          `oops`,
		"no reply":          `{"draft":null}`,
		"a stranger's kind": `{"reply":"ok","draft":{"text":"x","category":"urgent","priority":"med"}}`,
		"an empty draft":    `{"reply":"ok","draft":{"text":"  ","category":"bug","priority":"low"}}`,
		"a reply too long":  `{"reply":"` + strings.Repeat("ก", 4001) + `","draft":null}`,
	} {
		ai := newFakeAI(t)
		ai.answer = func(w http.ResponseWriter) { io.WriteString(w, answer) }
		got := runStepsEnv(t, route, map[string]string{"FITT_FEEDBACK_AI_URL": ai.URL}, assistStep(askMove))
		if got[0].Status != 502 || !strings.Contains(got[0].Body, `"bad_answer"`) {
			t.Errorf("%s: got %d %s", name, got[0].Status, got[0].Body)
		}
	}
	ai := newFakeAI(t)
	ai.answer = func(w http.ResponseWriter) {
		io.WriteString(w, `{"reply":"บอกเพิ่มได้ไหมครับว่าอยู่หน้าไหน","draft":null,"extra":"x"}`)
	}
	got := runStepsEnv(t, route, map[string]string{"FITT_FEEDBACK_AI_URL": ai.URL}, assistStep(askMove))
	if got[0].Status != 200 || got[0].Body != `{"reply":"บอกเพิ่มได้ไหมครับว่าอยู่หน้าไหน","draft":null}` {
		t.Errorf("a reply with no draft yet: got %d %s", got[0].Status, got[0].Body)
	}
}

func TestAssistSaysWhenTheAIIsNotThere(t *testing.T) {
	route := ticketRoute(t)
	ai := newFakeAI(t)
	ai.answer = func(w http.ResponseWriter) { w.Header().Set("location", "http://elsewhere.test/"); w.WriteHeader(302) }
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	for name, u := range map[string]string{"redirect": ai.URL, "unreachable": down.URL} {
		got := runStepsEnv(t, route, map[string]string{"FITT_FEEDBACK_AI_URL": u}, assistStep(askMove))
		if got[0].Status != 502 || !strings.Contains(got[0].Body, `"ai_unreachable"`) {
			t.Errorf("%s: got %d %s", name, got[0].Status, got[0].Body)
		}
	}
}

// The details field asked "อยากให้เปลี่ยนเป็นแบบไหน?", a question, and customers answered
// with questions. It now names what to write, with a request for its example.
func TestTheDetailsFieldAsksForAChangeNotAQuestion(t *testing.T) {
	if strings.Contains(feedbackWidgetJS, "อยากให้เปลี่ยนเป็นแบบไหน?") {
		t.Error("the details field still asks a question")
	}
	for _, want := range []string{"สิ่งที่อยากให้แก้", "เช่น ย้ายปุ่มบันทึกไปไว้ใต้ตาราง"} {
		if !strings.Contains(feedbackWidgetJS, want) {
			t.Errorf("the widget lacks %q", want)
		}
	}
}

// The assistant shows only where the app's route says it has one, and what the AI says
// is shown as text, never as markup.
func TestTheWidgetOffersTheAssistantOnlyWhereTheAppHasOne(t *testing.T) {
	for _, want := range []string{`"?op=assist"`, `r.data.on === true`, `bubble.textContent = text`} {
		if !strings.Contains(feedbackWidgetJS, want) {
			t.Errorf("the widget lacks %s", want)
		}
	}
	for _, line := range strings.Split(feedbackWidgetJS, "\n") {
		if strings.Contains(line, "innerHTML") && (strings.Contains(line, "reply") || strings.Contains(line, "draft")) {
			t.Errorf("the AI's words go in as markup: %s", line)
		}
	}
}

// The 🎤 button turns speech into text, in Thai, where the browser can.
func TestTheMicListensInThaiWhereTheBrowserCan(t *testing.T) {
	for _, want := range []string{`window.SpeechRecognition || window.webkitSpeechRecognition`, `rec.lang = "th-TH"`} {
		if !strings.Contains(feedbackWidgetJS, want) {
			t.Errorf("the widget lacks %s", want)
		}
	}
}

// Customers explain with pictures — crops of the page, screenshots they paste — and
// with documents ("อยากได้เมนูใหม่จากเอกสารไฟล์นี้"). The chat passes them to the AI, which
// reads them, so the draft can say what the team needs from them.
func TestAssistPassesTheCustomersFilesToTheAI(t *testing.T) {
	ai := newFakeAI(t)
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGNgYGD4DwABBAEAwS2OUAAAAABJRU5ErkJggg=="
	pdf := "JVBERi0xLjQKJcOkw7zDtsOfCg=="
	body := `{"messages":[{"role":"user","text":"อยากได้เมนูใหม่จากเอกสารไฟล์นี้"}],"files":[` +
		`{"name":"ตัวอย่าง.pdf","type":"application/pdf","data":"` + pdf + `"},` +
		`{"name":"จุดที่ 1.png","type":"image/png","data":"` + png + `"}]}`
	got := runStepsEnv(t, ticketRoute(t), map[string]string{"FITT_FEEDBACK_AI_URL": ai.URL}, assistStep(body))
	if got[0].Status != 200 {
		t.Fatalf("got %d %s", got[0].Status, got[0].Body)
	}
	files, _ := ai.got[0]["files"].([]any)
	if len(files) != 2 {
		t.Fatalf("the AI got %d files: %v", len(files), ai.got[0]["files"])
	}
	first, _ := files[0].(map[string]any)
	if first["name"] != "ตัวอย่าง.pdf" || first["type"] != "application/pdf" || first["data"] != pdf {
		t.Errorf("the document was not passed on as sent: %v", first)
	}
	if instr, _ := ai.got[0]["instructions"].(string); !strings.Contains(instr, "files") {
		t.Errorf("the instructions do not tell the AI about the files:\n%s", instr)
	}
}

func TestAssistTakesOnlyFilesTheAICanRead(t *testing.T) {
	route := ticketRoute(t)
	ai := newFakeAI(t)
	env := map[string]string{"FITT_FEEDBACK_AI_URL": ai.URL}
	msg := `"messages":[{"role":"user","text":"x"}]`
	file := func(name, typ, data string) string {
		return `{"name":"` + name + `","type":"` + typ + `","data":"` + data + `"}`
	}
	seven := strings.TrimSuffix(strings.Repeat(file("a.png", "image/png", "AAAA")+",", 11), ",")
	big := "@@PAD@@" // 9 MB of base64, made in node
	for name, tc := range map[string]struct {
		body   string
		status int
	}{
		"an old Word file": {`{` + msg + `,"files":[` + file("a.doc", "application/msword", "AAAA") + `]}`, 400},
		"PowerPoint":       {`{` + msg + `,"files":[` + file("a.pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation", "AAAA") + `]}`, 400},
		"not base64":       {`{` + msg + `,"files":[` + file("a.png", "image/png", "<svg>") + `]}`, 400},
		"no name":          {`{` + msg + `,"files":[` + file("", "image/png", "AAAA") + `]}`, 400},
		"too many files":   {`{` + msg + `,"files":[` + seven + `]}`, 400},
		"files too large":  {`{` + msg + `,"files":[` + file("a.pdf", "application/pdf", big) + `,` + file("b.pdf", "application/pdf", big) + `]}`, 413},
	} {
		s := assistStep(tc.body)
		s.Pad = 9 * 1024 * 1024
		if got := runStepsEnv(t, route, env, s); got[0].Status != tc.status {
			t.Errorf("%s: got %d %.200s, want %d", name, got[0].Status, got[0].Body, tc.status)
		}
	}
	if len(ai.got) != 0 {
		t.Errorf("the AI was asked %d times for requests the route refuses", len(ai.got))
	}
}

// In the chat the customer attaches pictures and documents with 📎 or pastes a
// screenshot; the AI gets them with the page's crops. Pictures also go to the team in
// the report, as pins in the payload's own shape (PNG, longest side at most 1600px).
// A spec comes as PDF, Excel (.xlsx) or Word (.docx), which the AI reads; old .doc and
// .xls files and PowerPoint are asked for in one of those, and a file with macros
// (.xlsm, .docm) as one without (DV takes none, 25 Nov 2026).
func TestTheChatTakesPicturesAndDocuments(t *testing.T) {
	for _, want := range []string{
		`type='file'`, `application/pdf`, `clipboardData`,
		`files: chatFiles()`, `label: attachedLabel(f.name)`, `selector: "region"`, `toDataURL("image/png")`, `1600 /`,
		`.xlsx,.docx`, `spreadsheetml.sheet`, `wordprocessingml.document`, `บันทึกเป็น .docx, .xlsx หรือ PDF`,
		`(xlsm|xltm|docm|dotm)`, `ไฟล์ที่มี macro`,
	} {
		if !strings.Contains(feedbackWidgetJS, want) {
			t.Errorf("the widget lacks %s", want)
		}
	}
}

// The spec of a new menu often comes as Excel or Word: the route passes them on as
// sent, and the AI (CRN's turns them into text for Gemini) reads them.
func TestAssistPassesExcelAndWordToTheAI(t *testing.T) {
	ai := newFakeAI(t)
	body := `{"messages":[{"role":"user","text":"อยากได้เมนูใหม่ตามไฟล์นี้"}],"files":[` +
		`{"name":"ฟิลด์.xlsx","type":"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet","data":"UEsDBA=="},` +
		`{"name":"สเปก.docx","type":"application/vnd.openxmlformats-officedocument.wordprocessingml.document","data":"UEsDBA=="}]}`
	got := runStepsEnv(t, ticketRoute(t), map[string]string{"FITT_FEEDBACK_AI_URL": ai.URL}, assistStep(body))
	if got[0].Status != 200 {
		t.Fatalf("got %d %s", got[0].Status, got[0].Body)
	}
	files, _ := ai.got[0]["files"].([]any)
	if len(files) != 2 {
		t.Fatalf("the AI got %d files: %v", len(files), ai.got[0]["files"])
	}
	for i, name := range []string{"ฟิลด์.xlsx", "สเปก.docx"} {
		if f, _ := files[i].(map[string]any); f["name"] != name || f["data"] != "UEsDBA==" {
			t.Errorf("file %d was not passed on as sent: %v", i, f)
		}
	}
}

// The route told the AI to answer how-to questions itself, and at NTC it made up how Film
// Job works (25 Nov 2026). It now answers only from what the customer shows, and otherwise
// says it does not know and offers to write a request; CRN's /feedback-ai adds the same rule.
func TestTheAssistantSaysItDoesNotKnowRatherThanGuess(t *testing.T) {
	_, route := feedbackRoute("app", ".tsx", "http://x.test/feedback")
	if strings.Contains(route, "only asks how to use the app, answer briefly") {
		t.Error("the route still tells the AI to answer how-to questions itself")
	}
	for _, want := range []string{"Never guess how the app works", "ไม่ทราบ", "as a request"} {
		if !strings.Contains(route, want) {
			t.Errorf("the route's instructions lack %q", want)
		}
	}
}

// The widget marks each call to the app's own route with X-Fitt-Feedback, which the route
// requires of a post; a call to an ingest address elsewhere (apps that are not Next) goes
// unmarked, since that server never agreed to the header.
func TestTheWidgetMarksOnlyCallsToItsOwnRoute(t *testing.T) {
	if strings.Count(feedbackWidgetJS, "headers: jsonHeaders(INGEST)") != 2 {
		t.Error("not every report to INGEST goes through jsonHeaders")
	}
	for _, want := range []string{`"X-Fitt-Feedback"`, "function own(", "headers: jsonHeaders(INGEST)", "headers: jsonHeaders(TICKETS)", "own(TICKETS) ? { \"X-Fitt-Feedback\": \"1\" }"} {
		if !strings.Contains(feedbackWidgetJS, want) {
			t.Errorf("the widget lacks %q", want)
		}
	}
}
