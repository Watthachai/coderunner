package buildstep

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// NTC build 9 (23 Nov 2026): every report said "Page: /", because the app changes
// screens without changing its address, and pins said "div.relative" or "main.flex-1",
// because Tailwind gives every element a class. The builder guessed which field and
// which menu the owner meant. A pin now names what a person sees: the button, field,
// menu or text by its words, the window or box it is in, and the screen.

// fakeDOM is enough of a DOM for the block: elements with attributes, text, parents,
// labels and querySelector for tag names, [attr=value] and "ancestor descendant".
const fakeDOM = `
function T(text) { return { nodeName: "#text", text: text, children: [] }; }
function E(tag, attrs, kids) {
  var el = { nodeName: tag.toUpperCase(), attrs: attrs || {}, children: [], parentElement: null };
  (kids || []).forEach(function (k) {
    if (typeof k === "string") k = T(k);
    k.parentElement = el;
    el.children.push(k);
  });
  el.getAttribute = function (n) { return Object.prototype.hasOwnProperty.call(el.attrs, n) ? el.attrs[n] : null; };
  Object.defineProperty(el, "textContent", { get: function () {
    return el.children.map(function (c) { return c.nodeName === "#text" ? c.text : c.textContent; }).join("");
  } });
  el.type = el.attrs.type || "";
  el.querySelector = function (sel) { return find(el, sel); };
  return el;
}
function simple(el, s) {
  var m = /^\[([\w-]+)=["']?([^"'\]]*)["']?\]$/.exec(s);
  if (m) return el.getAttribute(m[1]) === m[2];
  return el.nodeName.toLowerCase() === s;
}
function matches(el, sel, root) {
  var parts = sel.trim().split(/\s+/);
  if (!simple(el, parts.pop())) return false;
  for (var p = el.parentElement; parts.length && p && p !== root.parentElement; p = p.parentElement) {
    if (simple(p, parts[parts.length - 1])) parts.pop();
  }
  return parts.length === 0;
}
function find(root, sel) {
  var alts = sel.split(",");
  var stack = root.children.slice();
  while (stack.length) {
    var el = stack.shift();
    if (el.nodeName === "#text") continue;
    if (alts.some(function (a) { return matches(el, a, root); })) return el;
    stack = el.children.concat(stack);
  }
  return null;
}
function doc(title, body) {
  var html = E("html", {}, [body]);
  return { title: title, querySelector: function (s) { return find(html, s); } };
}
`

// pointed runs the widget's "what the customer pointed at" block in node, after setup
// (JavaScript using fakeDOM), and returns what expr comes to, as JSON.
func pointed(t *testing.T, setup, expr string) string {
	t.Helper()
	start := strings.Index(feedbackWidgetJS, "// --- what the customer pointed at")
	end := strings.Index(feedbackWidgetJS, "// --- end of what the customer pointed at")
	if start < 0 || end < start {
		t.Fatal("the widget has no block for what the customer pointed at")
	}
	limits := strings.Index(feedbackWidgetJS, "var NOTE_MAX")
	script := feedbackWidgetJS[limits:strings.Index(feedbackWidgetJS[limits:], "\n")+limits] + "\n" +
		fakeDOM + feedbackWidgetJS[start:end] + "\n" + setup + "\nconsole.log(JSON.stringify(" + expr + "));"
	out, err := exec.Command("node", "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func pointedString(t *testing.T, setup, expr string) string {
	t.Helper()
	raw := pointed(t, setup, expr)
	var s string
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("%s gave %s: %v", expr, raw, err)
	}
	return s
}

func TestAPinSaysWhatItIsByTheWordsOnIt(t *testing.T) {
	cases := []struct{ name, setup, want string }{
		{"the text inside a button", `var b = E("button", {class: "px-3"}, [E("span", {class: "ml-1"}, ["บันทึก"])]); var el = b.children[0];`, `ปุ่ม "บันทึก"`},
		{"a field by its label", `var lab = E("label", {}, ["สูง (ซม.)"]); var el = E("input", {class: "border", type: "number"}); el.labels = [lab];`, `ช่อง "สูง (ซม.)"`},
		{"a field by its placeholder", `var el = E("input", {class: "w-full", placeholder: "ค้นหาเลขใบงาน"});`, `ช่อง "ค้นหาเลขใบงาน"`},
		{"a list to choose from", `var el = E("select", {class: "w-full", "aria-label": "โชว์รูม"});`, `ช่องเลือก "โชว์รูม"`},
		{"a tick box", `var lab = E("label", {}, ["ติดบานหน้า"]); var el = E("input", {type: "checkbox", class: "h-4"}); el.labels = [lab];`, `ตัวเลือก "ติดบานหน้า"`},
		{"a menu in the sidebar", `var nav = E("aside", {class: "w-64"}, [E("button", {class: "w-full"}, [E("span", {}, ["วางบิลสิ้นเดือน"])])]); var el = nav.children[0].children[0];`, `เมนู "วางบิลสิ้นเดือน"`},
		{"a link", `var el = E("a", {class: "underline", href: "/x"}, ["ดูใบงานทั้งหมด"]);`, `ลิงก์ "ดูใบงานทั้งหมด"`},
		{"a column of a table", `var el = E("th", {class: "px-2"}, ["ยอดรวม"]);`, `หัวตาราง "ยอดรวม"`},
		{"a heading", `var el = E("h2", {class: "t-section"}, ["รายการใบงานเดือนนี้"]);`, `หัวข้อ "รายการใบงานเดือนนี้"`},
		{"a box with a heading", `var el = E("div", {class: "p-4"}, [E("h3", {}, ["ผู้ช่วยช่างติดตั้ง"]), E("p", {}, ["เลือกได้หลายคน"])]);`, `ส่วน "ผู้ช่วยช่างติดตั้ง"`},
		{"plain text", `var el = E("div", {class: "relative"}, ["ราคาประเมิน ฿15,000"]);`, `ข้อความ "ราคาประเมิน ฿15,000"`},
		{"nothing to read", `var el = E("div", {class: "relative"});`, `div.relative`},
	}
	for _, c := range cases {
		if got := pointedString(t, c.setup, "whatIs(el)"); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	long := pointedString(t, `var el = E("p", {class: "text-sm"}, ["ก".repeat(200)]);`, "whatIs(el)")
	if len([]rune(long)) > 80 || !strings.HasSuffix(long, `…"`) {
		t.Errorf("a long text is not cut short: %q", long)
	}
}

func TestAPinSaysWhichWindowOrBoxItIsIn(t *testing.T) {
	cases := []struct{ name, setup, want string }{
		{"a window", `var el = E("button", {}, ["พิมพ์"]); E("div", {role: "dialog"}, [E("h2", {}, ["พิมพ์ใบวางบิล"]), E("div", {class: "flex"}, [el])]);`, `ในหน้าต่าง "พิมพ์ใบวางบิล"`},
		{"a window named for screen readers", `var el = E("button", {}, ["ตกลง"]); E("dialog", {"aria-label": "ยืนยันทำแทนช่าง"}, [el]);`, `ในหน้าต่าง "ยืนยันทำแทนช่าง"`},
		{"a box with its own heading", `var el = E("input", {placeholder: "กว้าง"}); E("div", {class: "rounded"}, [E("h3", {}, ["ขนาดกระจก"]), E("div", {class: "grid"}, [el])]);`, `ใน "ขนาดกระจก"`},
		{"only the screen's own heading", `var el = E("button", {}, ["บันทึก"]); E("main", {}, [E("h1", {}, ["รุ่นรถ"]), E("div", {class: "flex"}, [el])]);`, ``},
		{"no box around it", `var el = E("button", {}, ["บันทึก"]); E("div", {class: "flex"}, [el]);`, ``},
		{"a menu beside the app's header", `var el = E("button", {}, ["เปิดใบงาน"]); E("div", {class: "flex"}, [E("aside", {}, [el]), E("div", {}, [E("header", {}, [E("h2", {}, ["NTC Film Job"])]), E("main", {}, [E("h1", {}, ["รุ่นรถ"])])])]);`, ``},
		{"the screen's part under the app's header", `var el = E("main", {}, [E("h1", {}, ["รุ่นรถ"])]); E("div", {class: "flex-1"}, [E("header", {}, [E("h2", {}, ["NTC Film Job"])]), el]);`, ``},
	}
	for _, c := range cases {
		if got := pointedString(t, c.setup, `whereIs(el, "รุ่นรถ")`); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestTheScreenIsNamedByWhatTheAppShows(t *testing.T) {
	cases := []struct{ name, setup, want string }{
		{"the menu marked current", `var d = doc("NTC", E("body", {}, [E("nav", {}, [E("a", {"aria-current": "page"}, ["คิวตัดฟิล์ม"])]), E("main", {}, [E("h1", {}, ["งานที่ต้องตัด"])])]));`, "คิวตัดฟิล์ม"},
		{"the screen's heading", `var d = doc("NTC", E("body", {}, [E("header", {}, [E("h2", {}, ["NTC Film Job"])]), E("main", {class: "flex-1"}, [E("div", {}, [E("h1", {class: "t-page"}, ["วางบิลสิ้นเดือน & ใบวางบิล (BL)"])])])]));`, "วางบิลสิ้นเดือน & ใบวางบิล (BL)"},
		{"the tab's title", `var d = doc("ตั้งค่าบริษัท", E("body", {}, [E("div", {}, ["…"])]));`, "ตั้งค่าบริษัท"},
	}
	for _, c := range cases {
		if got := pointedString(t, c.setup, "screenName(d)"); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAPinsLabelPutsItTogether(t *testing.T) {
	got := pointedString(t, `var lab = E("label", {}, ["สูง"]); var el = E("input", {class: "border"}); el.labels = [lab];
		E("div", {class: "rounded"}, [E("h3", {}, ["ขนาดกระจก"]), E("div", {}, [lab, el])]);`, `pinLabel(el, "รุ่นรถ")`)
	if want := `ช่อง "สูง" ใน "ขนาดกระจก" · หน้าจอ "รุ่นรถ"`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := pointedString(t, `var el = E("button", {}, ["ส่ง"]);`, `pinLabel(el, "")`); got != `ปุ่ม "ส่ง"` {
		t.Errorf("without a screen: got %q", got)
	}
}

// DV's relay and Gateway at a customer's site take a label of at most 200 characters,
// and turn the whole report away when one is longer (Jidapa, 25 Nov 2026). The widget
// counts as JavaScript does, which is never less than the characters a person sees, and
// never cuts an emoji in half.
func TestEveryLabelFitsDVsRelay(t *testing.T) {
	cases := []struct{ name, setup, expr string }{
		{"a pin on a long screen", `var el = E("button", {}, ["ส่ง"]);`, `pinLabel(el, "ก".repeat(400))`},
		{"a pin in a long box", `var el = E("button", {}, ["ส่ง".repeat(30)]); E("div", {}, [E("h3", {}, ["ข".repeat(300)]), el]);`, `pinLabel(el, "ค".repeat(60))`},
		{"a crop over a long list", `var items = Array.from({ length: 80 }, (_, i) => ({ text: "รายการที่ " + i, x: 10, y: 10 + i, w: 50, h: 1 }));`,
			`regionLabel(800, 600, "ใบงาน".repeat(12), regionWords(items, { x: 0, y: 0, w: 800, h: 600 }))`},
		{"a picture with a long name", "", `attachedLabel("ใบเสนอราคา".repeat(30) + ".png")`},
		{"a cut through an emoji", "", `words("ก".repeat(198) + "😀😀😀", LABEL_MAX)`},
	}
	for _, c := range cases {
		raw := pointed(t, c.setup, `(function (s) { return { n: s.length, whole: s.isWellFormed() }; })(`+c.expr+`)`)
		var got struct {
			N     int  `json:"n"`
			Whole bool `json:"whole"`
		}
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("%s: %s: %v", c.name, raw, err)
		}
		if got.N > 200 || !got.Whole {
			t.Errorf("%s: a label of %d characters (whole: %v); DV's relay takes 200", c.name, got.N, got.Whole)
		}
	}
	if got := pointedString(t, "", `attachedLabel("ใบเสนอราคา.png")`); got != "รูปที่แนบ: ใบเสนอราคา.png" {
		t.Errorf("a picture's label: %q", got)
	}
}

func TestACropSaysTheWordsInsideIt(t *testing.T) {
	setup := `var items = [
		{ text: "ขนาดกระจก", x: 110, y: 110, w: 80, h: 20 },
		{ text: " กว้าง ", x: 110, y: 140, w: 40, h: 20 },
		{ text: "สูง", x: 160, y: 140, w: 30, h: 20 },
		{ text: "สูง", x: 160, y: 170, w: 30, h: 20 },
		{ text: "เมนูด้านซ้าย", x: 5, y: 140, w: 60, h: 20 },
		{ text: "ลงชื่อออก", x: 900, y: 10, w: 60, h: 20 },
	];`
	if got := pointedString(t, setup, `regionWords(items, { x: 100, y: 100, w: 300, h: 200 })`); got != "ขนาดกระจก กว้าง สูง" {
		t.Errorf("got %q", got)
	}
	label := pointedString(t, setup, `regionLabel(670, 643, "รุ่นรถ", regionWords(items, { x: 100, y: 100, w: 300, h: 200 }))`)
	if want := `พื้นที่ 670×643 · หน้าจอ "รุ่นรถ" · ข้อความในกรอบ "ขนาดกระจก กว้าง สูง"`; label != want {
		t.Errorf("got %q, want %q", label, want)
	}
	if got := pointedString(t, "", `regionLabel(670, 643, "", "")`); got != "พื้นที่ 670×643" {
		t.Errorf("a crop with nothing to read: %q", got)
	}
}

// A report with nothing pinned on the page says which screen it was sent from in its
// note, where DV's change request carries it to the builder; pins already say it.
func TestAReportWithoutPinsSaysItsScreen(t *testing.T) {
	cases := []struct {
		name, expr, want string
	}{
		{"no pins", `noteWithScreen("อยากได้ใบกำกับสรุปยอดแต่ละเดือน", "เอกสารการเงิน", 0)`, "อยากได้ใบกำกับสรุปยอดแต่ละเดือน\n(หน้าจอ: เอกสารการเงิน)"},
		{"pins say it", `noteWithScreen("อยากได้ใบกำกับสรุปยอดแต่ละเดือน", "เอกสารการเงิน", 1)`, "อยากได้ใบกำกับสรุปยอดแต่ละเดือน"},
		{"the note names it", `noteWithScreen("หน้าจอเอกสารการเงิน: เพิ่มปุ่มสรุป", "เอกสารการเงิน", 0)`, "หน้าจอเอกสารการเงิน: เพิ่มปุ่มสรุป"},
		{"no screen found", `noteWithScreen("แก้สีปุ่ม", "", 0)`, "แก้สีปุ่ม"},
		{"no note", `noteWithScreen("", "เอกสารการเงิน", 0)`, ""},
		{"no room left", `noteWithScreen("ก".repeat(3995), "เอกสารการเงิน", 0)`, strings.Repeat("ก", 3995)},
	}
	for _, c := range cases {
		if got := pointedString(t, "", c.expr); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// The widget uses the block for pins, crops, the AI's context and the report, and a
// short report with nothing pinned asks once where it is before it goes.
func TestTheWidgetNamesWhatTheCustomerPointedAt(t *testing.T) {
	for _, want := range []string{
		`label: pinLabel(el, screenName(document)),`,
		`label: regionLabel(w, h, screenName(document), regionWords(textsOnScreen(), { x: x, y: y, w: w, h: h })),`,
		`page: location.pathname + (screenName(document) ? " · หน้าจอ \"" + screenName(document) + "\"" : ""),`,
		`var note = noteWithScreen((noteEl.value || "").trim(), screenName(document), pins.length);`,
		`ช่วยบอกด้วยว่าตรงไหน กด 📍 ชี้จุดที่อยากให้แก้ ทีมจะได้แก้ถูกที่ ถ้าจะส่งเลย กดส่งอีกครั้งครับ`,
	} {
		if !strings.Contains(feedbackWidgetJS, want) {
			t.Errorf("the widget lacks %s", want)
		}
	}
	if strings.Contains(feedbackWidgetJS, "label: shortLabel(el)") {
		t.Error("a pin is still labelled by its CSS class")
	}
}

// The 💬 assistant asks where before it drafts, never guesses a name, and writes each
// change with its screen and the words on the element, the way the builder can find it.
func TestTheAssistantAsksWhereAndNamesItInTheDraft(t *testing.T) {
	for _, want := range []string{
		"Never guess a screen, menu, button, field or document the customer did not name",
		"หน้าจอรุ่นรถ: เปลี่ยนชื่อช่อง",
		"context.page is the page's address and, after หน้าจอ, the title of the screen",
		"For a new document, report or menu, ask what it must show",
	} {
		if !strings.Contains(feedbackRouteAssist, want) {
			t.Errorf("the assistant's instructions lack %q", want)
		}
	}
	for _, want := range []string{"บอกด้วยว่าอยู่ตรงไหน", "เช่น ย้ายปุ่มบันทึกไปไว้ใต้ตาราง"} {
		if !strings.Contains(feedbackWidgetJS, want) {
			t.Errorf("the widget lacks %q", want)
		}
	}
}
