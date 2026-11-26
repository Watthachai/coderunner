// Package aiassist is the AI behind the 💬 widget's assistant (docs/feedback-assistant.md,
// 24 Nov 2026). An app's own route (/api/fitt-feedback?op=assist) POSTs the conversation
// to FITT_FEEDBACK_AI_URL, which is CRN's /feedback-ai: CRN asks Gemini and answers
// {reply, draft} in the route's shape.
//
// The secrets sit in files on the build machine and are read on every request, so
// adding, changing or taking one back needs no restart: the Gemini API key, and the keys
// apps send as Authorization: Bearer (FITT_FEEDBACK_AI_KEY in each app's env). Each
// customer's site has its own key, a file named after the site in the keys folder, so
// one can be taken back alone (DV, 25 Nov 2026); the key all apps shared at first works
// until its file is gone. Until there is a Gemini key and an app key it answers 503 and
// asks no one. The log says which site asked; nothing the customer wrote or attached.
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
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Config says where the secrets are and which AI to ask: Gemini, or the AI Router when
// RouterURL is set (the office's OpenAI-style /chat/completions, 26 Nov 2026).
type Config struct {
	GeminiKeyFile string // the Gemini API key
	AppKeyFile    string // the key all apps shared at first, logged as site "shared"
	AppKeyDir     string // a file per site: its name is the site, its content the key
	Model         string // e.g. gemini-3.1-flash-lite
	Endpoint      string // https://generativelanguage.googleapis.com/v1beta
	RouterURL     string // e.g. http://172.168.1.208:4000/v1; "" asks Gemini
	RouterModel   string // e.g. fittcore-auto-crn
	RouterKeyFile string // the router's key, sent as Bearer
	Client        *http.Client
	Logger        *slog.Logger
}

func (c Config) viaRouter() bool { return c.RouterURL != "" }

func (c Config) model() string {
	if c.viaRouter() {
		return c.RouterModel
	}
	return c.Model
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
		status, body, site, note := c.serve(r)
		w.Header().Set("content-type", "application/json")
		w.Header().Set("cache-control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
		c.Logger.Info("feedback-ai", "status", status, "site", site, "model", c.model(), "ms", time.Since(start).Milliseconds(), "note", note)
	})
}

func fail(code string) map[string]string { return map[string]string{"error": code} }

func (c Config) serve(r *http.Request) (int, any, string, string) {
	if r.Method != http.MethodPost {
		return http.StatusMethodNotAllowed, fail("method_not_allowed"), "", ""
	}
	keyFile := c.GeminiKeyFile
	if c.viaRouter() {
		keyFile = c.RouterKeyFile
	}
	aiKey, err := readSecret(keyFile)
	keys := c.appKeys()
	if err != nil || len(keys) == 0 {
		return http.StatusServiceUnavailable, fail("not_configured"), "", "a key file is missing"
	}
	bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	site := siteOf(bearer, keys)
	if site == "" {
		return http.StatusUnauthorized, fail("unauthorized"), "", ""
	}
	raw, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxBody))
	if err != nil {
		return http.StatusRequestEntityTooLarge, fail("payload_too_large"), site, ""
	}
	var req appRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return http.StatusBadRequest, fail("invalid_request"), site, "not json"
	}
	conv, err := prepare(req)
	if err != nil {
		return http.StatusBadRequest, fail("invalid_request"), site, err.Error()
	}
	note := fmt.Sprintf("messages=%d files=%d", len(req.Messages), len(req.Files))
	var text string
	if c.viaRouter() {
		text, err = c.askRouter(r.Context(), aiKey, conv)
	} else {
		text, err = c.ask(r.Context(), aiKey, geminiBody(conv))
	}
	if err != nil {
		return http.StatusBadGateway, fail("ai_unreachable"), site, note + " " + err.Error()
	}
	a, err := checkAnswer(text)
	if err != nil {
		return http.StatusBadGateway, fail("bad_answer"), site, note + " " + err.Error()
	}
	return http.StatusOK, a, site, note
}

// appKeys is every key an app may send, by the site it belongs to.
func (c Config) appKeys() map[string]string {
	keys := map[string]string{}
	if c.AppKeyDir != "" {
		entries, _ := os.ReadDir(c.AppKeyDir)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") || !e.Type().IsRegular() {
				continue
			}
			if k, err := readSecret(filepath.Join(c.AppKeyDir, e.Name())); err == nil {
				keys[e.Name()] = k
			}
		}
	}
	if k, err := readSecret(c.AppKeyFile); err == nil {
		keys["shared"] = k
	}
	return keys
}

// siteOf is the site whose key bearer is, or "" when it is no site's.
func siteOf(bearer string, keys map[string]string) string {
	if bearer == "" {
		return ""
	}
	got := sha256.Sum256([]byte(bearer))
	site := ""
	for name, key := range keys {
		want := sha256.Sum256([]byte(key))
		if subtle.ConstantTimeCompare(got[:], want[:]) == 1 {
			site = name
		}
	}
	return site
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

// A conversation is the app's call checked and laid out for any AI: the system text, the
// turns (the customer speaks first and last), the page, the pins and the files in the
// customer's latest turn, and the answer's JSON Schema.
type conversation struct {
	System string
	Turns  []turn
	Schema any
}

type turn struct {
	Role  string // user or assistant
	Parts []piece
}

// A piece is text, or a file the AI reads itself (Type and base64 Data).
type piece struct {
	Text, Name, Type, Data string
}

func prepare(req appRequest) (conversation, error) {
	var conv conversation
	if len(req.Messages) == 0 || len(req.Messages) > maxMessages {
		return conv, errors.New("messages")
	}
	if len(req.Files) > maxFiles {
		return conv, errors.New("files")
	}
	for _, m := range req.Messages {
		if (m.Role != "user" && m.Role != "assistant") || m.Text == "" || utf16Len(m.Text) > maxText {
			return conv, errors.New("message")
		}
		if len(conv.Turns) == 0 && m.Role == "assistant" {
			continue // cut to the last messages: the customer speaks first
		}
		if n := len(conv.Turns); n > 0 && conv.Turns[n-1].Role == m.Role {
			conv.Turns[n-1].Parts = append(conv.Turns[n-1].Parts, piece{Text: m.Text})
			continue
		}
		conv.Turns = append(conv.Turns, turn{Role: m.Role, Parts: []piece{{Text: m.Text}}})
	}
	if len(conv.Turns) == 0 || conv.Turns[len(conv.Turns)-1].Role != "user" {
		return conv, errors.New("the customer has the last word")
	}
	last := &conv.Turns[len(conv.Turns)-1]
	var pins []string
	for _, p := range req.Context.Pins {
		pins = append(pins, truncate(p, 300))
	}
	if len(pins) > 10 {
		pins = pins[:10]
	}
	if page := truncate(req.Context.Page, 500); page != "" || len(pins) > 0 {
		ctx, _ := json.Marshal(map[string]any{"page": page, "pins": pins})
		last.Parts = append(last.Parts, piece{Text: "context: " + string(ctx)})
	}
	for _, f := range req.Files {
		if f.Name == "" || utf8.RuneCountInString(f.Name) > 200 || !slices.Contains(fileTypes, f.Type) {
			return conv, errors.New("file")
		}
		data, err := base64.StdEncoding.DecodeString(f.Data)
		if err != nil || f.Data == "" {
			return conv, errors.New("file data")
		}
		if f.Type == xlsxType || f.Type == docxType {
			text, err := officeText(f.Type, data)
			if err != nil {
				return conv, fmt.Errorf("file %s", err)
			}
			last.Parts = append(last.Parts, piece{Text: "file: " + f.Name + " (its text)\n" + text})
			continue
		}
		last.Parts = append(last.Parts, piece{Text: "file: " + f.Name}, piece{Name: f.Name, Type: f.Type, Data: f.Data})
	}
	var schema any = map[string]any{"type": "object"}
	if len(req.Schema) > 0 {
		if err := json.Unmarshal(req.Schema, &schema); err != nil {
			return conv, errors.New("schema")
		}
	}
	conv.Schema = jsonSchema(schema)
	conv.System = systemText(req.Instructions)
	return conv, nil
}

// geminiBody is the conversation as a generateContent body: the system text as the
// system instruction, assistant turns as model turns, files as inlineData.
func geminiBody(conv conversation) map[string]any {
	var contents []content
	for _, t := range conv.Turns {
		role := "user"
		if t.Role == "assistant" {
			role = "model"
		}
		var parts []part
		for _, p := range t.Parts {
			if p.Type != "" {
				parts = append(parts, part{"inlineData": map[string]string{"mimeType": p.Type, "data": p.Data}})
			} else {
				parts = append(parts, part{"text": p.Text})
			}
		}
		contents = append(contents, content{Role: role, Parts: parts})
	}
	return map[string]any{
		"contents":          contents,
		"systemInstruction": map[string]any{"parts": []part{{"text": conv.System}}},
		"generationConfig": map[string]any{
			"responseMimeType":   "application/json",
			"responseJsonSchema": conv.Schema,
		},
	}
}

// routerBody is the conversation as an OpenAI-style /chat/completions body: pictures as
// image_url, a PDF as a file, a text file as its text. No max_tokens: the router picks.
// The router's model does not hold to response_format (26 Nov 2026: it answered JSON with
// keys of its own), so the system text spells out the schema as well.
func routerBody(model string, conv conversation) map[string]any {
	schema, _ := json.Marshal(conv.Schema)
	system := conv.System + "\nAnswer with one JSON object and nothing else, no code fence, that fits this JSON Schema: " + string(schema)
	messages := []map[string]any{{"role": "system", "content": system}}
	for _, t := range conv.Turns {
		if t.Role == "assistant" {
			var texts []string
			for _, p := range t.Parts {
				texts = append(texts, p.Text)
			}
			messages = append(messages, map[string]any{"role": "assistant", "content": strings.Join(texts, "\n")})
			continue
		}
		var parts []map[string]any
		for _, p := range t.Parts {
			dataURL := "data:" + p.Type + ";base64," + p.Data
			switch {
			case p.Type == "":
				parts = append(parts, map[string]any{"type": "text", "text": p.Text})
			case strings.HasPrefix(p.Type, "image/"):
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": dataURL}})
			case p.Type == "application/pdf":
				parts = append(parts, map[string]any{"type": "file", "file": map[string]string{"filename": p.Name, "file_data": dataURL}})
			default: // text/plain, text/csv: checked base64 already
				b, _ := base64.StdEncoding.DecodeString(p.Data)
				parts = append(parts, map[string]any{"type": "text", "text": strings.ToValidUTF8(string(b), "")})
			}
		}
		messages = append(messages, map[string]any{"role": "user", "content": parts})
	}
	return map[string]any{
		"model":    model,
		"messages": messages,
		"response_format": map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"name": "feedback_answer", "schema": conv.Schema},
		},
	}
}

// crnRules is what CRN tells the AI after the app's own instructions, so it holds for apps
// built before a rule came: the AI knows the app only by what the customer shows and says
// (NTC, 25 Nov 2026: it told a customer a test code would come by email, which Film Job
// never does).
const crnRules = "Never guess how the app works. You know it only by what context, pins and files show and what the customer tells you, " +
	"not what it does behind its screens: what it sends, to whom or when, what it keeps, or how a step works. " +
	"When the customer asks how the app works or how to do something and those do not show it, say in Thai that you do not know (ไม่ทราบ) how the system does that, " +
	"and offer to write what they need as a request to the team; set draft to null until they want that."

func systemText(instructions string) string {
	if instructions == "" {
		return crnRules
	}
	return instructions + "\n" + crnRules
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
		return "", fmt.Errorf("gemini unreachable: %s", reason(err))
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

// askRouter sends the conversation to the AI Router and returns the model's text. What
// the router says about a refused key is never passed on, since it can quote the key.
func (c Config) askRouter(ctx context.Context, key string, conv conversation) (string, error) {
	raw, _ := json.Marshal(routerBody(c.RouterModel, conv))
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.RouterURL, "/")+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+key)
	res, err := c.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ai router unreachable: %s", reason(err))
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	var o struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(out, &o)
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return "", fmt.Errorf("ai router %d: key refused", res.StatusCode)
	case res.StatusCode != http.StatusOK:
		return "", fmt.Errorf("ai router %d: %s", res.StatusCode, truncate(o.Error.Message, 200))
	case len(o.Choices) == 0 || strings.TrimSpace(o.Choices[0].Message.Content) == "":
		finish := ""
		if len(o.Choices) > 0 {
			finish = o.Choices[0].FinishReason
		}
		return "", fmt.Errorf("ai router gave no text (finish: %s)", finish)
	}
	return o.Choices[0].Message.Content, nil
}

// reason is why a call failed without its address (which holds no key, but is long), such
// as "dial tcp 172.168.1.208:4000: connect: no route to host".
func reason(err error) string {
	var u *url.Error
	if errors.As(err, &u) {
		err = u.Err
	}
	return truncate(err.Error(), 200)
}

// checkAnswer holds the model's JSON to what the route accepts.
func checkAnswer(text string) (answer, error) {
	var a answer
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") && strings.HasSuffix(text, "```") && len(text) > 6 {
		text = strings.TrimSuffix(text, "```") // a fence around the JSON: ```json ... ```
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
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
