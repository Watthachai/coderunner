package aiassist

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
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
	if len(req.SystemInstruction.Parts) != 1 || !strings.HasPrefix(req.SystemInstruction.Parts[0].Text, "Write every reply in Thai.\n") {
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

// zipOf is a base64 Office file holding the given parts.
func zipOf(t *testing.T, parts map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for name, body := range parts {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, body)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// A spec as customers send it: a sheet of the new menu's fields.
func specXLSX(t *testing.T) string {
	return zipOf(t, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="ฟิลด์เมนูใหม่" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>ชื่อฟิลด์</t></si><si><t>ชนิด</t></si><si><t>เลขที่สัญญา</t></si><si><r><t>ข้อ</t></r><r><t>ความ</t></r></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>` +
			`<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="C1" t="inlineStr"><is><t>บังคับ</t></is></c></row>` +
			`<row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2" t="s"><v>3</v></c><c r="D2"><v>12</v></c></row>` +
			`</sheetData></worksheet>`,
	})
}

func specDOCX(t *testing.T) string {
	return zipOf(t, map[string]string{
		"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
			`<w:p><w:r><w:t>เมนูสัญญาเช่า</w:t></w:r></w:p>` +
			`<w:p><w:r><w:t xml:space="preserve">ขั้นตอน: </w:t></w:r><w:r><w:t>บันทึก แล้วอนุมัติ</w:t></w:r></w:p>` +
			`<w:tbl><w:tr><w:tc><w:p><w:r><w:t>ฟิลด์</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>ชนิด</w:t></w:r></w:p></w:tc></w:tr>` +
			`<w:tr><w:tc><w:p><w:r><w:t>วันเริ่ม</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>วันที่</w:t></w:r></w:p></w:tc></w:tr></w:tbl>` +
			`</w:body></w:document>`,
	})
}

// Customers send the spec of a new menu as Excel or Word. Gemini reads neither, so CRN
// gives it their text: each sheet as CSV, the document's paragraphs and table rows.
func TestExcelAndWordReachGeminiAsText(t *testing.T) {
	g := newFakeGemini(t)
	body := `{"instructions":"x","messages":[{"role":"user","text":"อยากได้เมนูใหม่ตามไฟล์นี้"}],` +
		`"files":[{"name":"ฟิลด์.xlsx","type":"` + xlsxType + `","data":"` + specXLSX(t) + `"},` +
		`{"name":"สเปก.docx","type":"` + docxType + `","data":"` + specDOCX(t) + `"}],"schema":{"type":"object"}}`
	if w := call(handlerFor(t, g.URL, true), appKey, body); w.Code != 200 {
		t.Fatalf("answered %d %s", w.Code, w.Body)
	}
	raw, _ := json.Marshal((<-g.got)["contents"])
	var contents []struct {
		Parts []map[string]any
	}
	_ = json.Unmarshal(raw, &contents)
	var texts []string
	for _, p := range contents[len(contents)-1].Parts {
		if _, ok := p["inlineData"]; ok {
			t.Errorf("an Office file went to Gemini as a file: %v", p)
		}
		if s, ok := p["text"].(string); ok {
			texts = append(texts, s)
		}
	}
	joined := strings.Join(texts, "\n")
	for _, want := range []string{
		"ฟิลด์.xlsx", "ฟิลด์เมนูใหม่", "ชื่อฟิลด์,ชนิด,บังคับ", "เลขที่สัญญา,ข้อความ,,12",
		"สเปก.docx", "เมนูสัญญาเช่า", "ขั้นตอน: บันทึก แล้วอนุมัติ", "ฟิลด์ | ชนิด", "วันเริ่ม | วันที่",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("Gemini did not get %q in:\n%s", want, joined)
		}
	}
}

func TestAFileThatIsNotTheOfficeFileItSaysIsRefused(t *testing.T) {
	g := newFakeGemini(t)
	body := `{"instructions":"x","messages":[{"role":"user","text":"x"}],"files":[{"name":"a.xlsx","type":"` + xlsxType + `","data":"AAAA"}],"schema":{}}`
	if w := call(handlerFor(t, g.URL, true), appKey, body); w.Code != http.StatusBadRequest {
		t.Errorf("answered %d %s", w.Code, w.Body)
	}
}

// A long sheet is cut, and Gemini is told so, so one file can't cost a fortune.
func TestALongSpreadsheetIsCut(t *testing.T) {
	var rows strings.Builder
	for i := 1; i <= 5000; i++ {
		fmt.Fprintf(&rows, `<row r="%d"><c r="A%d" t="inlineStr"><is><t>%s</t></is></c></row>`, i, i, strings.Repeat("ก", 40))
	}
	data := zipOf(t, map[string]string{
		"xl/workbook.xml":          `<workbook><sheets><sheet name="S" sheetId="1"/></sheets></workbook>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData>` + rows.String() + `</sheetData></worksheet>`,
	})
	g := newFakeGemini(t)
	body := `{"instructions":"x","messages":[{"role":"user","text":"x"}],"files":[{"name":"a.xlsx","type":"` + xlsxType + `","data":"` + data + `"}],"schema":{}}`
	if w := call(handlerFor(t, g.URL, true), appKey, body); w.Code != 200 {
		t.Fatalf("answered %d %s", w.Code, w.Body)
	}
	raw, _ := json.Marshal((<-g.got)["contents"])
	if n := utf8.RuneCount(raw); n > officeTextMax+2000 {
		t.Errorf("Gemini got %d characters", n)
	}
	if !strings.Contains(string(raw), "ตัดเหลือ") {
		t.Error("Gemini was not told the file was cut")
	}
}

// DV gives each customer's site its own key, so one site's key can be taken back without
// touching the others (Jidapa, 25 Nov 2026): one file per site in the keys folder, named
// after the site, read on every request. The log says which site asked, never its key.
// The key all apps shared at first still works until its file is gone.
func TestEachSiteHasItsOwnKey(t *testing.T) {
	g := newFakeGemini(t)
	dir := t.TempDir()
	keys := filepath.Join(dir, "feedback-ai-keys")
	if err := os.MkdirAll(keys, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string]string{"ntc": "ntc-key", "audit": "audit-key", ".DS_Store": "not-a-key"} {
		if err := os.WriteFile(filepath.Join(keys, name), []byte(key+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "gemini-key"), []byte("gemini-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	h := Handler(Config{
		GeminiKeyFile: filepath.Join(dir, "gemini-key"),
		AppKeyFile:    filepath.Join(dir, "feedback-ai-key"),
		AppKeyDir:     keys,
		Model:         "gemini-test",
		Endpoint:      g.URL,
		Logger:        slog.New(slog.NewTextHandler(&logs, nil)),
	})
	answered := func(key string) int {
		w := call(h, key, appCall)
		if w.Code == 200 {
			<-g.got
			<-g.path
			<-g.apiKey
		}
		return w.Code
	}
	for _, site := range []string{"ntc", "audit"} {
		logs.Reset()
		if code := answered(site + "-key"); code != 200 {
			t.Errorf("%s's key: answered %d", site, code)
		}
		if !strings.Contains(logs.String(), "site="+site) || strings.Contains(logs.String(), site+"-key") {
			t.Errorf("%s's call logged %q", site, logs.String())
		}
	}
	if code := answered("not-a-key"); code != http.StatusUnauthorized {
		t.Errorf("a hidden file's content is a key: answered %d", code)
	}
	if err := os.Remove(filepath.Join(keys, "ntc")); err != nil {
		t.Fatal(err)
	}
	if code := answered("ntc-key"); code != http.StatusUnauthorized {
		t.Errorf("a key taken back: answered %d", code)
	}
	if code := answered("audit-key"); code != 200 {
		t.Errorf("another site after one key was taken back: answered %d", code)
	}
	if err := os.WriteFile(filepath.Join(dir, "feedback-ai-key"), []byte(appKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	if code := answered(appKey); code != 200 || !strings.Contains(logs.String(), "site=shared") {
		t.Errorf("the shared key: answered %d, logged %q", code, logs.String())
	}
	for _, name := range []string{"audit", "feedback-ai-key"} {
		p := filepath.Join(keys, name)
		if name == "feedback-ai-key" {
			p = filepath.Join(dir, name)
		}
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	if code := answered("audit-key"); code != http.StatusServiceUnavailable {
		t.Errorf("no key left: answered %d", code)
	}
}

// The AI knows the app only by what the customer shows and says, so it never tells them
// how the system works behind the screen (NTC, 25 Nov 2026: it said a test code would come
// by email, which Film Job never does). CRN adds that rule after every app's instructions,
// so apps built before it keep it too.
func TestTheAIIsToldNeverToGuessHowTheAppWorks(t *testing.T) {
	g := newFakeGemini(t)
	if w := call(handlerFor(t, g.URL, true), appKey, appCall); w.Code != 200 {
		t.Fatalf("answered %d %s", w.Code, w.Body)
	}
	body := <-g.got
	raw, _ := json.Marshal(body["systemInstruction"])
	var si struct{ Parts []struct{ Text string } }
	_ = json.Unmarshal(raw, &si)
	if len(si.Parts) != 1 {
		t.Fatalf("system instruction %s", raw)
	}
	text := si.Parts[0].Text
	app := strings.Index(text, "Write every reply in Thai.")
	rule := strings.Index(text, "Never guess how the app works")
	if app != 0 || rule < 0 {
		t.Fatalf("want the app's instructions, then CRN's rule: %q", text)
	}
	for _, want := range []string{"ไม่ทราบ", "as a request"} {
		if !strings.Contains(text[rule:], want) {
			t.Errorf("rule lacks %q: %q", want, text[rule:])
		}
	}

	// An app that sends no instructions still gets the rule.
	g2 := newFakeGemini(t)
	bare := strings.Replace(appCall, `"instructions": "Write every reply in Thai.",`, "", 1)
	if w := call(handlerFor(t, g2.URL, true), appKey, bare); w.Code != 200 {
		t.Fatalf("answered %d %s", w.Code, w.Body)
	}
	raw, _ = json.Marshal((<-g2.got)["systemInstruction"])
	if !strings.Contains(string(raw), "Never guess how the app works") {
		t.Errorf("no instructions, no rule: %s", raw)
	}
}
