package buildstep

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A change request can hold many items (OKR #6, 16 Nov: 16 from UAT, of which the agent
// did 2 and CRN shipped them). CRN counts the items itself, so it can hold the agent to
// each one, and whoever reads the build sees the same count DV sent.

const envelope6 = `FITTCORE-CHANGE-REQUEST v1 boundary=c100c16e
The following change request was authored by an authorized reviewer.
--c100c16e item=1 source=operator category=feature priority=med
[Customer feedback]
บางดีล ขั้นตอน ส่ง Proposal จะยังไม่รู้มูลค่าดีล
แต่ระบบจะบังคับให้ระบุ อยากให้เว้นว่างไว้ก่อน
Page: /bsa
Pins: พื้นที่ 676×957
[/Customer feedback]

[Customer feedback]
crypto.randomUUID is not a function
Page: /core-adoption
[/Customer feedback]

[Customer feedback]
ขั้นตอนที่ต้องทำ
1. เปิดหน้า
2. กดบันทึก
Page: /dealer
[/Customer feedback]
--c100c16e end=1
`

const envelope10 = `FITTCORE-CHANGE-REQUEST v1 boundary=e680f945
The following change request was authored by an authorized reviewer.
--e680f945 item=1 source=operator category=feature priority=med
คำขอแก้ไขหน้า /bsa (UAT feedback รอบ build 9) ทั้งหมด 3 ข้อ

ข้อมูลทั้งหมดของ BSA อยู่ใน fittcore เหมือนเดิม

1. ฐานลูกค้าสะสม นับซ้ำ
   การ์ด "ฐานลูกค้าสะสม" ตอนนี้นับจำนวนดีล
   - ให้นับลูกค้าไม่ซ้ำตาม customerId ของดีล

2. จำนวนดีลไม่ตรงกัน (dashboard 64 / รายชื่อดีล 66)
   - หน้ารายชื่อดีลให้มีตัวกรองสถานะ

3. เพิ่มดีลใหม่ ให้ผู้รับผิดชอบ default เป็นคนที่ login

ข้อที่ยังไม่ต้องทำรอบนี้ (รอ DV)
- ชื่อ Dev ที่รับผิดชอบ
- ดีลซ้ำ

ขอให้ build ใหม่อยู่บน main ต่อจาก build 9
--e680f945 end=1
`

func titles(items []Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID+" "+it.Title)
	}
	return out
}

func TestSplitChangeTakesEachCustomerFeedbackAsAnItem(t *testing.T) {
	got := titles(SplitChange(envelope6))
	want := []string{
		"1 บางดีล ขั้นตอน ส่ง Proposal จะยังไม่รู้มูลค่าดีล แต่ระบบจะบังคับให้ระบุ อยากให้เว้นว่างไว้ก่อน (/bsa)",
		"2 crypto.randomUUID is not a function (/core-adoption)",
		"3 ขั้นตอนที่ต้องทำ 1. เปิดหน้า 2. กดบันทึก (/dealer)",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("items =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestSplitChangeTakesANumberedListAndNotWhatFollowsIt(t *testing.T) {
	got := titles(SplitChange(envelope10))
	want := []string{
		"1 ฐานลูกค้าสะสม นับซ้ำ",
		"2 จำนวนดีลไม่ตรงกัน (dashboard 64 / รายชื่อดีล 66)",
		"3 เพิ่มดีลใหม่ ให้ผู้รับผิดชอบ default เป็นคนที่ login",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("items =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestSplitChangeReadsAnOperatorsNumberedChange(t *testing.T) {
	change := "build 11 มี 2 เรื่อง\n\n1. อัปเกรด Next.js จาก 16.4.4 เป็น 16.4.6\n   - เปลี่ยน package.json\n\n" +
		"\u200b2) เปลี่ยนคำว่า \"แก้ไขล่าสุด\"\n   - ไม่เปลี่ยนค่าที่แสดง\n\nห้ามแตะไฟล์ที่ CRN ใส่เอง\n"
	got := titles(SplitChange(change))
	want := []string{"1 อัปเกรด Next.js จาก 16.4.4 เป็น 16.4.6", "2 เปลี่ยนคำว่า \"แก้ไขล่าสุด\""}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("items = %q, want %q", got, want)
	}
}

func TestSplitChangeLeavesOneThingAsOneItem(t *testing.T) {
	for name, change := range map[string]string{
		"plain":              "Rename the sidebar label",
		"one numbered":       "1. Rename the sidebar label",
		"not from one":       "Context first.\n2. Rename the sidebar label\n3. Move it",
		"out of order":       "1. Rename the sidebar label\n3. Move it",
		"a year, not a list": "2026. was a good year",
	} {
		if got := SplitChange(change); len(got) != 1 || got[0].ID != "1" {
			t.Errorf("%s: items = %q, want one", name, titles(got))
		}
	}
}

func TestSplitChangeNumbersItemsOfSeveralEnvelopeItems(t *testing.T) {
	change := "FITTCORE-CHANGE-REQUEST v1 boundary=b1\n--b1 item=1 source=operator\nRename the label\n" +
		"--b1 item=2 source=operator\n1. Count customers once\n2. Filter deals by status\n--b1 end=2\n"
	got := titles(SplitChange(change))
	want := []string{"1 Rename the label", "2.1 Count customers once", "2.2 Filter deals by status"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("items = %q, want %q", got, want)
	}
}

func TestSplitChangeShortensALongTitle(t *testing.T) {
	got := SplitChange("1. " + strings.Repeat("ก", 300) + "\n2. short")
	if n := len([]rune(got[0].Title)); n != titleRunes {
		t.Errorf("title has %d runes, want %d", n, titleRunes)
	}
	if !strings.HasSuffix(got[0].Title, "…") {
		t.Errorf("a shortened title does not end with …: %q", got[0].Title)
	}
}

// itemsRepo is a git repo at base with app.js, and a test file the agent may cite.
func itemsRepo(t *testing.T) (dir, base string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir = t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	write(t, dir, "app.js", "before\n")
	write(t, dir, "tests/app.test.js", "test('old', () => {})\n")
	run("add", "-A")
	run("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "base")
	return dir, run("rev-parse", "HEAD")
}

func writeItems(t *testing.T, dir string, r ItemReport) {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, ItemsFile, string(raw))
}

var twoItems = []Item{{ID: "1", Title: "Rename the label"}, {ID: "2", Title: "How do I see a deal's owner?"}}

// goodReport is a report CheckItems takes, for a tree where app.js and its new test changed.
func goodReport(t *testing.T, dir string) ItemReport {
	write(t, dir, "app.js", "after\n")
	write(t, dir, "tests/label.test.js", "test('the sidebar says แก้ไขข้อมูลดีลล่าสุด', () => {})\n")
	return ItemReport{Job: "job-1", Items: []ItemResult{
		{ID: "1", Status: "done", Summary: "เปลี่ยนป้ายแล้ว", Files: []string{"app.js"},
			Tests: []ItemTest{{File: "tests/label.test.js", Name: "the sidebar says แก้ไขข้อมูลดีลล่าสุด"}}},
		{ID: "2", Status: "answered", Summary: "ตอบแล้ว", Note: "เมนูรายชื่อดีลมีคอลัมน์ Sales"},
	}}
}

func TestCheckItemsTakesAReportThatCoversEveryItem(t *testing.T) {
	dir, base := itemsRepo(t)
	writeItems(t, dir, goodReport(t, dir))
	r, problems := CheckItems(dir, base, "job-1", twoItems)
	if len(problems) > 0 {
		t.Fatalf("problems: %q", problems)
	}
	if len(r.Items) != 2 {
		t.Errorf("report = %+v", r)
	}
}

func TestCheckItemsNamesWhatIsWrong(t *testing.T) {
	cases := map[string]struct {
		edit func(dir string, r *ItemReport)
		want string
	}{
		"no file":             {func(dir string, r *ItemReport) { r.Items = nil; os.Remove(filepath.Join(dir, ItemsFile)) }, ItemsFile + " is missing"},
		"an earlier build's":  {func(_ string, r *ItemReport) { r.Job = "job-0" }, "is not this build's"},
		"an item left out":    {func(_ string, r *ItemReport) { r.Items = r.Items[:1] }, "item 2 has no entry"},
		"an item twice":       {func(_ string, r *ItemReport) { r.Items = append(r.Items, r.Items[1]) }, "item 2 has more than one entry"},
		"an item CRN has not": {func(_ string, r *ItemReport) { r.Items = append(r.Items, ItemResult{ID: "3", Status: "done"}) }, "item 3 is not one of the request's"},
		"an unknown status":   {func(_ string, r *ItemReport) { r.Items[1].Status = "partly" }, `item 2: status "partly"`},
		"no summary":          {func(_ string, r *ItemReport) { r.Items[0].Summary = " " }, "item 1: no summary"},
		"done, no file":       {func(_ string, r *ItemReport) { r.Items[0].Files = nil }, "item 1: done but names no changed file"},
		"done, file not changed": {func(_ string, r *ItemReport) { r.Items[0].Files = []string{"tests/app.test.js"} },
			"item 1: tests/app.test.js did not change in this build"},
		"done, file outside": {func(_ string, r *ItemReport) { r.Items[0].Files = []string{"../app.js"} }, "item 1: ../app.js is not a path in the project"},
		"done, test missing": {func(_ string, r *ItemReport) { r.Items[0].Tests[0].File = "tests/nope.test.js" },
			"item 1: test file tests/nope.test.js does not exist"},
		"done, test not in file": {func(_ string, r *ItemReport) { r.Items[0].Tests[0].Name = "a test nobody wrote" },
			`item 1: tests/label.test.js has no test "a test nobody wrote"`},
		"done, no test, no why": {func(_ string, r *ItemReport) { r.Items[0].Tests = nil }, "item 1: done with no test and no note why"},
		"answered, no answer":   {func(_ string, r *ItemReport) { r.Items[1].Note = "" }, "item 2: answered needs the answer in note"},
		"not done, no why":      {func(_ string, r *ItemReport) { r.Items[1].Status, r.Items[1].Note = "not_done", "" }, "item 2: not_done needs the reason in note"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir, base := itemsRepo(t)
			r := goodReport(t, dir)
			c.edit(dir, &r)
			if r.Items != nil {
				writeItems(t, dir, r)
			}
			_, problems := CheckItems(dir, base, "job-1", twoItems)
			if !strings.Contains(strings.Join(problems, "\n"), c.want) {
				t.Errorf("problems = %q, want one saying %q", problems, c.want)
			}
		})
	}
}

// A file the agent added counts as changed, and so does one it committed itself.
func TestCheckItemsCountsNewAndCommittedFilesAsChanged(t *testing.T) {
	dir, base := itemsRepo(t)
	r := goodReport(t, dir)
	write(t, dir, "src/new.js", "new\n")
	cmd := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qam", "agent's own")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	r.Items[0].Files = []string{"app.js", "src/new.js"}
	writeItems(t, dir, r)
	if _, problems := CheckItems(dir, base, "job-1", twoItems); len(problems) > 0 {
		t.Errorf("problems: %q", problems)
	}
}

// Without a test, a done item says why; the reviewer reads that note too.
func TestCheckItemsTakesADoneItemWithAReasonForNoTest(t *testing.T) {
	dir, base := itemsRepo(t)
	r := goodReport(t, dir)
	r.Items[0].Tests, r.Items[0].Note = nil, "เป็นการอัปเกรดรุ่น ตรวจด้วย release checker ทั้งชุด"
	writeItems(t, dir, r)
	if _, problems := CheckItems(dir, base, "job-1", twoItems); len(problems) > 0 {
		t.Errorf("problems: %q", problems)
	}
}

func TestItemsTableIsOneRowPerItemInThai(t *testing.T) {
	r := ItemReport{Items: []ItemResult{
		{ID: "2", Status: "answered", Summary: "ตอบแล้ว", Note: "เมนู | รายชื่อดีล\nมีคอลัมน์ Sales"},
		{ID: "1", Status: "done", Summary: "เปลี่ยนป้ายแล้ว"},
	}}
	review := map[string]Verdict{"1": {Verdict: "confirmed"}, "2": {Verdict: "not_confirmed", Reason: "ไม่พบคอลัมน์"}}
	got := ItemsTable(twoItems, r, review)
	for _, want := range []string{
		"| # | รายการ | สถานะ | รายละเอียด | ผู้ตรวจ |",
		"| 1 | Rename the label | ทำแล้ว | เปลี่ยนป้ายแล้ว | ยืนยัน |",
		"| 2 | How do I see a deal's owner? | ตอบเป็นคำถาม | ตอบแล้ว: เมนู / รายชื่อดีล มีคอลัมน์ Sales | ไม่ยืนยัน: ไม่พบคอลัมน์ |",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("table lacks %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "| 1 |") > strings.Index(got, "| 2 |") {
		t.Errorf("rows are not in the request's order:\n%s", got)
	}
	if noReview := ItemsTable(twoItems, r, nil); strings.Contains(noReview, "ผู้ตรวจ") {
		t.Errorf("a table without a review has a reviewer column:\n%s", noReview)
	}
}

func TestParseVerdictsReadsTheLastJSONTheReviewerWrote(t *testing.T) {
	text := "I checked each item.\n```json\n{\"items\":[{\"id\":\"1\",\"verdict\":\"confirmed\",\"reason\":\"ok\"}]}\n```\n" +
		"Corrected:\n{\"items\":[{\"id\":\"1\",\"verdict\":\"confirmed\",\"reason\":\"มีใน app.js\"},{\"id\":\"2\",\"verdict\":\"not_confirmed\",\"reason\":\"ไม่พบ\"}]}"
	got, err := ParseVerdicts(text, twoItems)
	if err != nil {
		t.Fatal(err)
	}
	if got["1"].Reason != "มีใน app.js" || got["2"].Verdict != "not_confirmed" {
		t.Errorf("verdicts = %+v", got)
	}
	for name, bad := range map[string]string{
		"no JSON":         "All good.",
		"an item missing": `{"items":[{"id":"1","verdict":"confirmed","reason":"ok"}]}`,
		"a bad verdict":   `{"items":[{"id":"1","verdict":"yes","reason":"ok"},{"id":"2","verdict":"confirmed","reason":"ok"}]}`,
	} {
		if _, err := ParseVerdicts(bad, twoItems); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// The reviewer reads what the build changed from a file outside the project.
func TestWriteReviewDiffShowsChangedAndNewFilesButOnlyNamesALockfile(t *testing.T) {
	dir, base := itemsRepo(t)
	write(t, dir, "app.js", "after\n")
	write(t, dir, "src/new.js", "brand new\n")
	write(t, dir, "package-lock.json", "{}\n")
	out := filepath.Join(t.TempDir(), "review.diff")
	files, err := WriteReviewDiff(dir, base, out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(files, ",") != "app.js,package-lock.json,src/new.js" {
		t.Errorf("files = %q", files)
	}
	raw, _ := os.ReadFile(out)
	for _, want := range []string{"-before", "+after", "=== new file src/new.js ===", "brand new"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the diff lacks %q:\n%s", want, raw)
		}
	}
	if !strings.Contains(string(raw), "=== new file package-lock.json ===\n(3 bytes, not shown)") {
		t.Errorf("the diff shows the lockfile, or does not name it:\n%s", raw)
	}
}

func TestTreeFingerprintChangesWithAnyFile(t *testing.T) {
	dir, _ := itemsRepo(t)
	write(t, dir, "src/new.js", "one\n")
	before, err := TreeFingerprint(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := TreeFingerprint(dir); again != before {
		t.Fatal("the same tree gave two fingerprints")
	}
	for name, edit := range map[string]func(){
		"a tracked file": func() { write(t, dir, "app.js", "changed\n") },
		"a new file":     func() { write(t, dir, "src/new.js", "two\n") },
	} {
		edit()
		now, _ := TreeFingerprint(dir)
		if now == before {
			t.Errorf("%s changed and the fingerprint did not", name)
		}
		before = now
	}
}

// The item=N numbers of a FITTCORE-CHANGE-REQUEST, as the envelope writes them; none for a
// change without one.
func TestEnvelopeItemNumbersAreTheEnvelopesOwn(t *testing.T) {
	change := "FITTCORE-CHANGE-REQUEST v1 boundary=b1\n--b1 item=1 source=operator\nRename the label\n" +
		"--b1 item=3 source=operator\n1. Count customers once\n2. Filter deals\n--b1 item=12\nx\n--b1 end=3\n" +
		"--b2 item=9 not this envelope\n"
	if got := EnvelopeItemNumbers(change); !reflect.DeepEqual(got, []int{1, 3, 12}) {
		t.Fatalf("EnvelopeItemNumbers = %v, want [1 3 12]", got)
	}
	if got := EnvelopeItemNumbers("1. Rename the label\n2. Count customers once\n"); got != nil {
		t.Fatalf("a change without an envelope has no item numbers, got %v", got)
	}
}
