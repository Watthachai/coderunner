package aiassist

import (
	"bytes"
	"encoding/base64"
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

// The AI Router is the office's OpenAI-style /chat/completions (26 Nov 2026). When CRN is
// set to it, /feedback-ai asks it instead of Gemini, with the same conversation.

type fakeRouter struct {
	*httptest.Server
	got    chan map[string]any
	path   chan string
	auth   chan string
	status int
	errMsg string
	answer string // the model's text
}

func newFakeRouter(t *testing.T) *fakeRouter {
	f := &fakeRouter{got: make(chan map[string]any, 1), path: make(chan string, 1), auth: make(chan string, 1), status: 200,
		answer: `{"reply":"สรุปให้แล้วครับ","draft":{"text":"ย้ายปุ่ม \"บันทึก\" ไปไว้ใต้ตารางรายการ","category":"feature","priority":"med"}}`}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.got <- body
		f.path <- r.URL.Path
		f.auth <- r.Header.Get("Authorization")
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(f.status)
		if f.status != 200 {
			out, _ := json.Marshal(map[string]any{"error": map[string]any{"message": f.errMsg, "type": "auth_error", "code": "401"}})
			_, _ = w.Write(out)
			return
		}
		out, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
			"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": f.answer}}}})
		_, _ = w.Write(out)
	}))
	t.Cleanup(f.Close)
	return f
}

func routerHandler(t *testing.T, router string, withKey bool, logs io.Writer) http.Handler {
	t.Helper()
	dir := t.TempDir()
	c := Config{
		GeminiKeyFile: filepath.Join(dir, "gemini-key"), // never written: the router needs no Gemini key
		AppKeyFile:    filepath.Join(dir, "feedback-ai-key"),
		Model:         "gemini-test",
		Endpoint:      "http://gemini.invalid",
		RouterURL:     router + "/v1",
		RouterModel:   "fittcore-auto-crn",
		RouterKeyFile: filepath.Join(dir, "ai-router-key"),
		Logger:        slog.New(slog.NewTextHandler(logs, nil)),
	}
	if err := os.WriteFile(c.AppKeyFile, []byte(appKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if withKey {
		if err := os.WriteFile(c.RouterKeyFile, []byte("router-secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Handler(c)
}

// appCall with a PDF and a text file beside the picture.
func routerCall(t *testing.T) string {
	var call map[string]any
	if err := json.Unmarshal([]byte(appCall), &call); err != nil {
		t.Fatal(err)
	}
	call["files"] = append(call["files"].([]any),
		map[string]any{"name": "สเปก.pdf", "type": "application/pdf", "data": base64.StdEncoding.EncodeToString([]byte("%PDF-1.4"))},
		map[string]any{"name": "หมายเหตุ.txt", "type": "text/plain", "data": base64.StdEncoding.EncodeToString([]byte("ช่องสูงต้องเป็นยาว"))})
	out, _ := json.Marshal(call)
	return string(out)
}

func TestCRNAsksTheAIRouterWhenItIsSetToIt(t *testing.T) {
	r := newFakeRouter(t)
	var logs bytes.Buffer
	w := call(routerHandler(t, r.URL, true, &logs), appKey, routerCall(t))
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
	if got.Reply != "สรุปให้แล้วครับ" || got.Draft == nil || got.Draft.Category != "feature" {
		t.Errorf("answer %s", w.Body)
	}
	if p := <-r.path; p != "/v1/chat/completions" {
		t.Errorf("asked the router at %s", p)
	}
	if a := <-r.auth; a != "Bearer router-secret" {
		t.Errorf("the router got Authorization %q", a)
	}
	if !strings.Contains(logs.String(), "model=fittcore-auto-crn") || strings.Contains(logs.String(), "router-secret") {
		t.Errorf("logged %q", logs.String())
	}

	body := <-r.got
	raw, _ := json.Marshal(body)
	var req struct {
		Model     string `json:"model"`
		MaxTokens *int   `json:"max_tokens"`
		Messages  []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		ResponseFormat struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Name   string         `json:"name"`
				Schema map[string]any `json:"schema"`
			} `json:"json_schema"`
		} `json:"response_format"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	if req.Model != "fittcore-auto-crn" {
		t.Errorf("model %q", req.Model)
	}
	// The router picks how long an answer may be (its people asked to try it so).
	if req.MaxTokens != nil {
		t.Errorf("sent max_tokens %d", *req.MaxTokens)
	}
	var roles []string
	for _, m := range req.Messages {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "system,user,assistant,user" {
		t.Fatalf("roles %v: %s", roles, raw)
	}
	var system, assistant string
	_ = json.Unmarshal(req.Messages[0].Content, &system)
	_ = json.Unmarshal(req.Messages[2].Content, &assistant)
	if !strings.HasPrefix(system, "Write every reply in Thai.\n") || !strings.Contains(system, "Never guess how the app works") {
		t.Errorf("system %q", system)
	}
	if assistant != "ใต้ตารางรายการ หรือมุมขวาล่างครับ" {
		t.Errorf("assistant %q", assistant)
	}
	var last []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
		File *struct {
			Filename string `json:"filename"`
			FileData string `json:"file_data"`
		} `json:"file"`
	}
	if err := json.Unmarshal(req.Messages[3].Content, &last); err != nil {
		t.Fatalf("latest message %s: %v", req.Messages[3].Content, err)
	}
	var texts []string
	var images, files int
	for _, p := range last {
		switch p.Type {
		case "text":
			texts = append(texts, p.Text)
		case "image_url":
			images++
			if p.ImageURL == nil || p.ImageURL.URL != "data:image/png;base64,iVBORw0KGgo=" {
				t.Errorf("picture %+v", p.ImageURL)
			}
		case "file":
			files++
			if p.File == nil || p.File.Filename != "สเปก.pdf" || p.File.FileData != "data:application/pdf;base64,"+base64.StdEncoding.EncodeToString([]byte("%PDF-1.4")) {
				t.Errorf("pdf %+v", p.File)
			}
		default:
			t.Errorf("part of type %q", p.Type)
		}
	}
	joined := strings.Join(texts, "\n")
	for _, want := range []string{"ใต้ตารางรายการ", "/deals", "ปุ่มบันทึก — อันนี้", "จุดที่ 1.png", "สเปก.pdf", "หมายเหตุ.txt", "ช่องสูงต้องเป็นยาว"} {
		if !strings.Contains(joined, want) {
			t.Errorf("latest message lacks %q: %s", want, joined)
		}
	}
	if images != 1 || files != 1 {
		t.Errorf("want 1 picture and 1 pdf, got %d and %d", images, files)
	}
	if req.ResponseFormat.Type != "json_schema" || req.ResponseFormat.JSONSchema.Name == "" {
		t.Errorf("response_format %+v", req.ResponseFormat)
	}
	s, _ := json.Marshal(req.ResponseFormat.JSONSchema.Schema)
	if !strings.Contains(string(s), `"type":["object","null"]`) || strings.Contains(string(s), "nullable") {
		t.Errorf("schema %s", s)
	}
}

// Set to the router, CRN asks no one until the router's key is in place.
func TestTheRouterNeedsItsKey(t *testing.T) {
	r := newFakeRouter(t)
	if w := call(routerHandler(t, r.URL, false, io.Discard), appKey, appCall); w.Code != http.StatusServiceUnavailable {
		t.Errorf("answered %d %s", w.Code, w.Body)
	}
}

// A refused call is a 502; what the router said about a key is never logged, since such a
// message can quote part of the key.
func TestARouterThatRefusesIsA502AndItsWordsOnKeysAreNotLogged(t *testing.T) {
	r := newFakeRouter(t)
	r.status = http.StatusUnauthorized
	r.errMsg = "Authentication Error, Invalid proxy server token passed. Received API Key = sk-...cret"
	var logs bytes.Buffer
	w := call(routerHandler(t, r.URL, true, &logs), appKey, appCall)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "ai_unreachable") {
		t.Errorf("answered %d %s", w.Code, w.Body)
	}
	if !strings.Contains(logs.String(), "401") || strings.Contains(logs.String(), "sk-") || strings.Contains(logs.String(), "Received API Key") {
		t.Errorf("logged %q", logs.String())
	}
}

// Some models wrap their JSON in a code fence; the answer inside is taken.
func TestAnAnswerInACodeFenceIsTaken(t *testing.T) {
	r := newFakeRouter(t)
	r.answer = "```json\n{\"reply\":\"ไม่ทราบครับ เขียนเป็นคำขอให้ทีมไหมครับ\",\"draft\":null}\n```"
	w := call(routerHandler(t, r.URL, true, io.Discard), appKey, appCall)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "ไม่ทราบครับ") {
		t.Errorf("answered %d %s", w.Code, w.Body)
	}
}
