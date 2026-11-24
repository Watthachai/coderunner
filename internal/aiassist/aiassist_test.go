package aiassist

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The app's route sends what docs/feedback-assistant.md describes; this is one such call.
const appCall = `{
  "instructions": "Write every reply in Thai.",
  "messages": [
    {"role": "user", "text": "ย้ายปุ่มบันทึกไปไว้ใต้ตารางได้ไหม"},
    {"role": "assistant", "text": "ใต้ตารางรายการ หรือมุมขวาล่างครับ"},
    {"role": "user", "text": "ใต้ตารางรายการ"}
  ],
  "context": {"page": "/deals", "pins": ["ปุ่มบันทึก — อันนี้"]},
  "files": [{"name": "จุดที่ 1.png", "type": "image/png", "data": "iVBORw0KGgo="}],
  "schema": {"type": "object", "properties": {"reply": {"type": "string"},
    "draft": {"type": "object", "nullable": true, "properties": {"text": {"type": "string"}}, "required": ["text"]}},
    "required": ["reply"]}
}`

const appKey = "app-key-for-tests"

type fakeGemini struct {
	*httptest.Server
	got    chan map[string]any
	path   chan string
	apiKey chan string
	status int
	answer string // the model's text
}

func newFakeGemini(t *testing.T) *fakeGemini {
	f := &fakeGemini{got: make(chan map[string]any, 1), path: make(chan string, 1), apiKey: make(chan string, 1), status: 200,
		answer: `{"reply":"สรุปให้แล้วครับ","draft":{"text":"ย้ายปุ่ม \"บันทึก\" ไปไว้ใต้ตารางรายการ","category":"feature","priority":"med"}}`}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.got <- body
		f.path <- r.URL.Path
		f.apiKey <- r.Header.Get("x-goog-api-key")
		w.WriteHeader(f.status)
		if f.status != 200 {
			_, _ = io.WriteString(w, `{"error":{"code":400,"message":"API key not valid"}}`)
			return
		}
		out, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{
			"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": f.answer}}}, "finishReason": "STOP"}}})
		_, _ = w.Write(out)
	}))
	t.Cleanup(f.Close)
	return f
}

func handlerFor(t *testing.T, gemini string, withKeys bool) http.Handler {
	t.Helper()
	dir := t.TempDir()
	c := Config{
		GeminiKeyFile: filepath.Join(dir, "gemini-key"),
		AppKeyFile:    filepath.Join(dir, "feedback-ai-key"),
		Model:         "gemini-test",
		Endpoint:      gemini,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if withKeys {
		if err := os.WriteFile(c.GeminiKeyFile, []byte("gemini-secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(c.AppKeyFile, []byte(appKey+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Handler(c)
}

func call(h http.Handler, bearer, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/feedback-ai", strings.NewReader(body))
	r.Header.Set("content-type", "application/json")
	if bearer != "" {
		r.Header.Set("authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// An app asks with its key; CRN asks Gemini and answers what the route expects.
func TestTheAssistantAnswersWithGeminisDraft(t *testing.T) {
	g := newFakeGemini(t)
	w := call(handlerFor(t, g.URL, true), appKey, appCall)
	if w.Code != 200 {
		t.Fatalf("answered %d %s", w.Code, w.Body)
	}
	var got struct {
		Reply string `json:"reply"`
		Draft *struct{ Text, Category, Priority string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Reply != "สรุปให้แล้วครับ" || got.Draft == nil || got.Draft.Text != `ย้ายปุ่ม "บันทึก" ไปไว้ใต้ตารางรายการ` || got.Draft.Category != "feature" || got.Draft.Priority != "med" {
		t.Errorf("answer %s", w.Body)
	}
	if p := <-g.path; p != "/models/gemini-test:generateContent" {
		t.Errorf("asked Gemini at %s", p)
	}
	if k := <-g.apiKey; k != "gemini-secret" {
		t.Errorf("Gemini got key %q", k)
	}
}

// What Gemini gets: CRN's instructions as the system instruction, the conversation with
// assistant turns as model turns, and the page, the pins and the files in the customer's
// latest message; the answer is JSON in the route's shape.
func TestGeminiGetsTheConversationTheContextAndTheFiles(t *testing.T) {
	g := newFakeGemini(t)
	if w := call(handlerFor(t, g.URL, true), appKey, appCall); w.Code != 200 {
		t.Fatalf("answered %d %s", w.Code, w.Body)
	}
	body := <-g.got
	raw, _ := json.Marshal(body)
	var req struct {
		SystemInstruction struct{ Parts []struct{ Text string } } `json:"systemInstruction"`
		Contents          []struct {
			Role  string
			Parts []struct {
				Text       string
				InlineData *struct{ MimeType, Data string } `json:"inlineData"`
			}
		}
		GenerationConfig struct {
			ResponseMimeType   string         `json:"responseMimeType"`
			ResponseJSONSchema map[string]any `json:"responseJsonSchema"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	if len(req.SystemInstruction.Parts) != 1 || req.SystemInstruction.Parts[0].Text != "Write every reply in Thai." {
		t.Errorf("system instruction %+v", req.SystemInstruction)
	}
	if len(req.Contents) != 3 || req.Contents[0].Role != "user" || req.Contents[1].Role != "model" || req.Contents[2].Role != "user" {
		t.Fatalf("contents %s", raw)
	}
	if req.Contents[1].Parts[0].Text != "ใต้ตารางรายการ หรือมุมขวาล่างครับ" {
		t.Errorf("model turn %+v", req.Contents[1])
	}
	last := req.Contents[2].Parts
	var texts []string
	var inline int
	for _, p := range last {
		texts = append(texts, p.Text)
		if p.InlineData != nil {
			inline++
			if p.InlineData.MimeType != "image/png" || p.InlineData.Data != "iVBORw0KGgo=" {
				t.Errorf("file part %+v", p.InlineData)
			}
		}
	}
	joined := strings.Join(texts, "\n")
	for _, want := range []string{"ใต้ตารางรายการ", "/deals", "ปุ่มบันทึก — อันนี้", "จุดที่ 1.png"} {
		if !strings.Contains(joined, want) {
			t.Errorf("latest message lacks %q: %s", want, joined)
		}
	}
	if inline != 1 {
		t.Errorf("want 1 file, got %d", inline)
	}
	if req.GenerationConfig.ResponseMimeType != "application/json" {
		t.Errorf("mime %q", req.GenerationConfig.ResponseMimeType)
	}
	// The route's schema says nullable the OpenAPI way; JSON Schema says it with a type list.
	s, _ := json.Marshal(req.GenerationConfig.ResponseJSONSchema)
	if !strings.Contains(string(s), `"draft":{"properties":{"text":{"type":"string"}},"required":["text"],"type":["object","null"]}`) || strings.Contains(string(s), "nullable") {
		t.Errorf("schema %s", s)
	}
}

// Only apps holding the key may spend the Gemini key, and nothing is asked before both
// keys are in place.
func TestOnlyAppsWithTheKeyAreAnswered(t *testing.T) {
	g := newFakeGemini(t)
	h := handlerFor(t, g.URL, true)
	for _, bearer := range []string{"", "wrong-key"} {
		if w := call(h, bearer, appCall); w.Code != http.StatusUnauthorized {
			t.Errorf("bearer %q: answered %d", bearer, w.Code)
		}
	}
	if w := call(handlerFor(t, g.URL, false), appKey, appCall); w.Code != http.StatusServiceUnavailable {
		t.Errorf("without keys: answered %d", w.Code)
	}
	select {
	case <-g.got:
		t.Error("Gemini was asked")
	default:
	}
}

// Whatever goes wrong at Gemini, the app gets a 502 and shows its own "try again".
func TestAFailedOrMalformedAnswerIsA502(t *testing.T) {
	for name, set := range map[string]func(*fakeGemini){
		"gemini refuses": func(g *fakeGemini) { g.status = 400 },
		"not json":       func(g *fakeGemini) { g.answer = "สวัสดีครับ" },
		"empty reply":    func(g *fakeGemini) { g.answer = `{"reply":"  ","draft":null}` },
		"bad category": func(g *fakeGemini) {
			g.answer = `{"reply":"ได้ครับ","draft":{"text":"ย้ายปุ่ม","category":"urgent","priority":"med"}}`
		},
	} {
		g := newFakeGemini(t)
		set(g)
		if w := call(handlerFor(t, g.URL, true), appKey, appCall); w.Code != http.StatusBadGateway {
			t.Errorf("%s: answered %d %s", name, w.Code, w.Body)
		}
	}
	g := newFakeGemini(t)
	g.answer = `{"reply":"ใช้เมนูด้านซ้ายครับ","draft":null}`
	if w := call(handlerFor(t, g.URL, true), appKey, appCall); w.Code != 200 || !strings.Contains(w.Body.String(), `"draft":null`) {
		t.Errorf("no draft yet: %d %s", w.Code, w.Body)
	}
}

func TestARequestOutsideTheContractIsRefused(t *testing.T) {
	g := newFakeGemini(t)
	h := handlerFor(t, g.URL, true)
	for name, body := range map[string]string{
		"not json":     `hello`,
		"no messages":  `{"instructions":"x","messages":[],"schema":{}}`,
		"bad role":     `{"instructions":"x","messages":[{"role":"system","text":"x"}],"schema":{}}`,
		"word file":    `{"instructions":"x","messages":[{"role":"user","text":"x"}],"files":[{"name":"a.docx","type":"application/msword","data":"AAAA"}],"schema":{}}`,
		"long message": `{"instructions":"x","messages":[{"role":"user","text":"` + strings.Repeat("ก", 2001) + `"}],"schema":{}}`,
	} {
		if w := call(h, appKey, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: answered %d", name, w.Code)
		}
	}
	if w := call(h, appKey, `{"instructions":"`+strings.Repeat("a", 16<<20)+`"}`); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("too large: answered %d", w.Code)
	}
}

// The window keeps the last 20 messages, so a conversation may start with the assistant;
// Gemini wants the customer first.
func TestAConversationCutToItsLastMessagesStartsWithTheCustomer(t *testing.T) {
	g := newFakeGemini(t)
	body := `{"instructions":"x","messages":[{"role":"assistant","text":"ครับ"},{"role":"user","text":"เปลี่ยนสีหัวตาราง"}],"schema":{"type":"object"}}`
	if w := call(handlerFor(t, g.URL, true), appKey, body); w.Code != 200 {
		t.Fatalf("answered %d %s", w.Code, w.Body)
	}
	raw, _ := json.Marshal((<-g.got)["contents"])
	if !strings.HasPrefix(string(raw), `[{"parts":[{"text":"เปลี่ยนสีหัวตาราง"}`) {
		t.Errorf("contents %s", raw)
	}
}
