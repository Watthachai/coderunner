// Package aiassist is the AI behind the 💬 widget's assistant (docs/feedback-assistant.md,
// 24 Nov 2026). An app's own route (/api/fitt-feedback?op=assist) POSTs the conversation
// to FITT_FEEDBACK_AI_URL, which is CRN's /feedback-ai: CRN asks Gemini and answers
// {reply, draft} in the route's shape.
//
// Two secrets sit in files on the build machine and are read on every request, so
// adding or changing one needs no restart: the Gemini API key, and the key apps send as
// Authorization: Bearer (FITT_FEEDBACK_AI_KEY in each app's env). Until both are there
// it answers 503 and asks no one. Nothing the customer wrote or attached is logged.
package aiassist

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Config says where the secrets are and which Gemini to ask.
type Config struct {
	GeminiKeyFile string // the Gemini API key
	AppKeyFile    string // the key apps send as Bearer
	Model         string // e.g. gemini-3.1-flash-lite
	Endpoint      string // https://generativelanguage.googleapis.com/v1beta
	Client        *http.Client
	Logger        *slog.Logger
}

// The route's own limits (internal/buildstep/feedback.go), so CRN takes what it sends.
const (
	maxBody     = 16 << 20
	maxMessages = 20
	maxText     = 2000
	maxFiles    = 10
	maxReply    = 4000
	maxDraft    = 4000
	timeout     = 28 * time.Second // the route waits 30 s
)

var (
	fileTypes  = []string{"image/png", "image/jpeg", "image/webp", "application/pdf", "text/plain", "text/csv", xlsxType, docxType}
	categories = []string{"bug", "feature", "style"}
	priorities = []string{"low", "med", "high"}
)

type appRequest struct {
	Instructions string `json:"instructions"`
	Messages     []struct {
		Role string `json:"role"`
		Text string `json:"text"`
	} `json:"messages"`
	Context struct {
		Page string   `json:"page"`
		Pins []string `json:"pins"`
	} `json:"context"`
	Files []struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Data string `json:"data"`
	} `json:"files"`
	Schema json.RawMessage `json:"schema"`
}

type draft struct {
	Text     string `json:"text"`
	Category string `json:"category"`
	Priority string `json:"priority"`
}

type answer struct {
	Reply string `json:"reply"`
	Draft *draft `json:"draft"`
}

// Handler serves POST /feedback-ai.
func Handler(c Config) http.Handler {
	if c.Client == nil {
		c.Client = &http.Client{Timeout: timeout}
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		status, body, note := c.serve(r)
		w.Header().Set("content-type", "application/json")
		w.Header().Set("cache-control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
		c.Logger.Info("feedback-ai", "status", status, "model", c.Model, "ms", time.Since(start).Milliseconds(), "note", note)
	})
}

func fail(code string) map[string]string { return map[string]string{"error": code} }

func (c Config) serve(r *http.Request) (int, any, string) {
	if r.Method != http.MethodPost {
		return http.StatusMethodNotAllowed, fail("method_not_allowed"), ""
	}
	geminiKey, err1 := readSecret(c.GeminiKeyFile)
	appKey, err2 := readSecret(c.AppKeyFile)
	if err1 != nil || err2 != nil {
		return http.StatusServiceUnavailable, fail("not_configured"), "a key file is missing"
	}
	bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	got, want := sha256.Sum256([]byte(bearer)), sha256.Sum256([]byte(appKey))
	if bearer == "" || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		return http.StatusUnauthorized, fail("unauthorized"), ""
	}
	raw, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxBody))
	if err != nil {
		return http.StatusRequestEntityTooLarge, fail("payload_too_large"), ""
	}
	var req appRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return http.StatusBadRequest, fail("invalid_request"), "not json"
	}
	gReq, err := geminiRequest(req)
	if err != nil {
		return http.StatusBadRequest, fail("invalid_request"), err.Error()
	}
	note := fmt.Sprintf("messages=%d files=%d", len(req.Messages), len(req.Files))
	text, err := c.ask(r.Context(), geminiKey, gReq)
	if err != nil {
		return http.StatusBadGateway, fail("ai_unreachable"), note + " " + err.Error()
	}
	a, err := checkAnswer(text)
	if err != nil {
		return http.StatusBadGateway, fail("bad_answer"), note + " " + err.Error()
	}
	return http.StatusOK, a, note
}

func readSecret(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", errors.New("empty")
	}
	return s, nil
}

type part map[string]any

type content struct {
	Role  string `json:"role"`
	Parts []part `json:"parts"`
}

// geminiRequest turns the app's call into a generateContent body: CRN's instructions as
// the system instruction, assistant turns as model turns, and the page, the pins and the
// files in the customer's latest message.
func geminiRequest(req appRequest) (map[string]any, error) {
	if len(req.Messages) == 0 || len(req.Messages) > maxMessages {
		return nil, errors.New("messages")
	}
	if len(req.Files) > maxFiles {
		return nil, errors.New("files")
	}
	var contents []content
	for _, m := range req.Messages {
		if (m.Role != "user" && m.Role != "assistant") || m.Text == "" || utf16Len(m.Text) > maxText {
			return nil, errors.New("message")
		}
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		}
		if len(contents) == 0 && role == "model" {
			continue // cut to the last messages: the customer speaks first
		}
		if n := len(contents); n > 0 && contents[n-1].Role == role {
			contents[n-1].Parts = append(contents[n-1].Parts, part{"text": m.Text})
			continue
		}
		contents = append(contents, content{Role: role, Parts: []part{{"text": m.Text}}})
	}
	if len(contents) == 0 || contents[len(contents)-1].Role != "user" {
		return nil, errors.New("the customer has the last word")
	}
	last := &contents[len(contents)-1]
	var pins []string
	for _, p := range req.Context.Pins {
		pins = append(pins, truncate(p, 300))
	}
	if len(pins) > 10 {
		pins = pins[:10]
	}
	if page := truncate(req.Context.Page, 500); page != "" || len(pins) > 0 {
		ctx, _ := json.Marshal(map[string]any{"page": page, "pins": pins})
		last.Parts = append(last.Parts, part{"text": "context: " + string(ctx)})
	}
	for _, f := range req.Files {
		if f.Name == "" || utf8.RuneCountInString(f.Name) > 200 || !slices.Contains(fileTypes, f.Type) {
			return nil, errors.New("file")
		}
		data, err := base64.StdEncoding.DecodeString(f.Data)
		if err != nil || f.Data == "" {
			return nil, errors.New("file data")
		}
		if f.Type == xlsxType || f.Type == docxType {
			text, err := officeText(f.Type, data)
			if err != nil {
				return nil, fmt.Errorf("file %s", err)
			}
			last.Parts = append(last.Parts, part{"text": "file: " + f.Name + " (its text)\n" + text})
			continue
		}
		last.Parts = append(last.Parts, part{"text": "file: " + f.Name}, part{"inlineData": map[string]string{"mimeType": f.Type, "data": f.Data}})
	}
	var schema any = map[string]any{"type": "object"}
	if len(req.Schema) > 0 {
		if err := json.Unmarshal(req.Schema, &schema); err != nil {
			return nil, errors.New("schema")
		}
	}
	body := map[string]any{
		"contents": contents,
		"generationConfig": map[string]any{
			"responseMimeType":   "application/json",
			"responseJsonSchema": jsonSchema(schema),
		},
	}
	if req.Instructions != "" {
		body["systemInstruction"] = map[string]any{"parts": []part{{"text": req.Instructions}}}
	}
	return body, nil
}

// jsonSchema rewrites the route's OpenAPI-style schema ("nullable": true) as JSON Schema
// (a type list with "null"), which responseJsonSchema takes.
func jsonSchema(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, x := range t {
			if k != "nullable" {
				out[k] = jsonSchema(x)
			}
		}
		if t["nullable"] == true {
			if typ, ok := t["type"].(string); ok {
				out["type"] = []string{typ, "null"}
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = jsonSchema(x)
		}
		return out
	}
	return v
}

// ask sends the body to Gemini and returns the model's text.
func (c Config) ask(ctx context.Context, key string, body map[string]any) (string, error) {
	raw, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	url := strings.TrimRight(c.Endpoint, "/") + "/models/" + c.Model + ":generateContent"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-goog-api-key", key)
	res, err := c.Client.Do(req)
	if err != nil {
		return "", errors.New("gemini unreachable")
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	var g struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(out, &g)
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gemini %d %s: %s", res.StatusCode, g.Error.Status, truncate(g.Error.Message, 200))
	}
	if len(g.Candidates) == 0 {
		return "", fmt.Errorf("gemini gave no answer (blocked: %q)", g.PromptFeedback.BlockReason)
	}
	var text strings.Builder
	for _, p := range g.Candidates[0].Content.Parts {
		if !p.Thought {
			text.WriteString(p.Text)
		}
	}
	if text.Len() == 0 {
		return "", fmt.Errorf("gemini gave no text (finish: %s)", g.Candidates[0].FinishReason)
	}
	return text.String(), nil
}

// checkAnswer holds the model's JSON to what the route accepts.
func checkAnswer(text string) (answer, error) {
	var a answer
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &a); err != nil {
		return a, errors.New("model answer is not the json asked for")
	}
	a.Reply = strings.TrimSpace(a.Reply)
	if a.Reply == "" || utf16Len(a.Reply) > maxReply {
		return a, errors.New("model reply empty or too long")
	}
	if a.Draft != nil {
		a.Draft.Text = strings.TrimSpace(a.Draft.Text)
		if a.Draft.Text == "" || utf16Len(a.Draft.Text) > maxDraft || !slices.Contains(categories, a.Draft.Category) || !slices.Contains(priorities, a.Draft.Priority) {
			return a, errors.New("model draft outside the contract")
		}
	}
	return a, nil
}

// utf16Len counts as the route does (JavaScript's length).
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}
