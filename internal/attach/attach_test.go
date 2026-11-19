package attach

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

var (
	pngBytes  = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 40)...)
	jpegBytes = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{2}, 40)...)
	webpBytes = append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), bytes.Repeat([]byte{3}, 40)...)
)

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func input(name, mime string, b []byte) Input {
	return Input{Name: name, Mime: mime, SHA256: sum(b), Bytes: base64.StdEncoding.EncodeToString(b)}
}

func item(n int) *int { return &n }

// Each picture comes back with what CRN keeps of it: the item it belongs to, the sender's
// name, its type, its sha256 in lower case, its size, and the file name CRN gives it.
func TestDecodeKeepsEachPictureUnderCRNsOwnName(t *testing.T) {
	in := []Input{
		input("pin-1.png", "image/png", pngBytes),
		input("../../etc/passwd", "image/jpeg", jpegBytes),
		input("กรอบ.webp", "image/webp", webpBytes),
	}
	in[0].Item = item(2)
	in[1].SHA256 = strings.ToUpper(in[1].SHA256)

	got, err := Decode(in, []int{1, 2})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	want := []domain.Attachment{
		{Item: 2, Name: "pin-1.png", Mime: "image/png", SHA256: sum(pngBytes), Size: len(pngBytes), File: "01.png"},
		{Name: "../../etc/passwd", Mime: "image/jpeg", SHA256: sum(jpegBytes), Size: len(jpegBytes), File: "02.jpg"},
		{Name: "กรอบ.webp", Mime: "image/webp", SHA256: sum(webpBytes), Size: len(webpBytes), File: "03.webp"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d pictures, want %d", len(got), len(want))
	}
	for i := range want {
		g := got[i]
		if !bytes.Equal(g.Data, map[int][]byte{0: pngBytes, 1: jpegBytes, 2: webpBytes}[i]) {
			t.Errorf("picture %d: the bytes are not the ones sent", i)
		}
		g.Data = nil
		if !reflect.DeepEqual(g, want[i]) {
			t.Errorf("picture %d = %+v, want %+v", i, g, want[i])
		}
	}
}

func TestDecodeWithNoPicturesIsNothing(t *testing.T) {
	got, err := Decode(nil, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("Decode(nil) = %v, %v", got, err)
	}
}

// One picture that is not right refuses them all, and the reason names it.
func TestDecodeRefusesTheWholeRequestOverOnePicture(t *testing.T) {
	big := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, MaxSize)...)
	cases := []struct {
		name  string
		edit  func(in []Input) []Input
		items []int
		want  string
	}{
		{"no name", func(in []Input) []Input { in[1].Name = " "; return in }, nil, "attachments[1]: name is required"},
		{"long name", func(in []Input) []Input { in[1].Name = strings.Repeat("a", 256); return in }, nil, "attachments[1]: name is longer than 255 bytes"},
		{"control in name", func(in []Input) []Input { in[1].Name = "a\nb.png"; return in }, nil, "attachments[1]: name has a control character"},
		{"other mime", func(in []Input) []Input { in[1].Mime = "image/gif"; return in }, nil, `attachments[1]: mime must be image/png, image/jpeg or image/webp, not "image/gif"`},
		{"not base64", func(in []Input) []Input { in[1].Bytes = "%%%"; return in }, nil, "attachments[1]: bytes is not base64 (standard alphabet, padded)"},
		{"empty", func(in []Input) []Input { in[1].Bytes = ""; return in }, nil, "attachments[1]: bytes is empty"},
		{"too big", func(in []Input) []Input { in[1] = input("big.png", "image/png", big); return in }, nil, "attachments[1]: the picture is 5242888 bytes; at most 5242880 (5 MiB)"},
		{"not a picture", func(in []Input) []Input { in[1] = input("a.png", "image/png", []byte("hello, world")); return in }, nil, "attachments[1]: the bytes are not a PNG, JPEG or WebP picture"},
		{"other kind", func(in []Input) []Input { in[1] = input("a.png", "image/png", jpegBytes); return in }, nil, "attachments[1]: the bytes are image/jpeg, not image/png"},
		{"bad sha256", func(in []Input) []Input { in[1].SHA256 = "abc"; return in }, nil, "attachments[1]: sha256 must be 64 hex characters"},
		{"wrong sha256", func(in []Input) []Input { in[1].SHA256 = sum(pngBytes); return in }, nil, "attachments[1]: sha256 does not match the bytes"},
		{"same picture twice", func(in []Input) []Input { in[1] = input("again.png", "image/png", pngBytes); return in }, nil, "attachments[1]: the same picture as attachments[0]"},
		{"item zero", func(in []Input) []Input { in[1].Item = item(0); return in }, []int{1}, "attachments[1]: item must be 1 or more"},
		{"item without envelope", func(in []Input) []Input { in[1].Item = item(1); return in }, nil, "attachments[1]: item is given but the change has no item=N blocks"},
		{"item not in change", func(in []Input) []Input { in[1].Item = item(4); return in }, []int{1, 2, 3}, "attachments[1]: item 4 is not an item of the change (it has items 1, 2, 3)"},
		{"eleven", func(in []Input) []Input {
			for len(in) < 11 {
				b := append(append([]byte{}, pngBytes...), byte(len(in)))
				in = append(in, input("p.png", "image/png", b))
			}
			return in
		}, nil, "attachments: at most 10 pictures per request, got 11"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := c.edit([]Input{input("ok.png", "image/png", pngBytes), input("b.jpg", "image/jpeg", jpegBytes)})
			got, err := Decode(in, c.items)
			if err == nil || err.Error() != c.want {
				t.Fatalf("Decode error = %v, want %q", err, c.want)
			}
			if got != nil {
				t.Fatalf("Decode kept %d pictures of a refused request", len(got))
			}
		})
	}
}

func TestDecodeRefusesPicturesThatAddUpToMoreThanTwentyMiB(t *testing.T) {
	var in []Input
	for i := 0; i < 5; i++ {
		b := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, MaxSize-8)...)
		b[8] = byte(i)
		in = append(in, input("p.png", "image/png", b))
	}
	_, err := Decode(in, nil)
	if err == nil || err.Error() != "attachments: the pictures add up to 26214400 bytes; at most 20971520 (20 MiB)" {
		t.Fatalf("Decode error = %v", err)
	}
	if _, err := Decode(in[:4], nil); err != nil {
		t.Fatalf("four pictures of 5 MiB are 20 MiB, which is allowed: %v", err)
	}
}

func decoded(t *testing.T) []domain.Attachment {
	t.Helper()
	got, err := Decode([]Input{input("a.png", "image/png", pngBytes), input("b.webp", "image/webp", webpBytes)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// The pictures land in the job's own folder, readable by CRN's account only, and only once
// all of them are written.
func TestSaveWritesTheJobsPicturesForCRNOnly(t *testing.T) {
	s := NewStore(t.TempDir())
	job := uuid.New()
	atts := decoded(t)
	if err := s.Save(job, atts); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for _, a := range atts {
		p := s.Path(job, a)
		if filepath.Dir(p) != s.Dir(job) || !filepath.IsAbs(p) {
			t.Fatalf("Path = %s, want a file in %s", p, s.Dir(job))
		}
		b, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(b, a.Data) {
			t.Fatalf("%s holds %d bytes (%v), want the picture", p, len(b), err)
		}
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v, want 0600", p, fi.Mode().Perm())
		}
	}
	if fi, _ := os.Stat(s.Dir(job)); fi.Mode().Perm() != 0o700 {
		t.Errorf("job folder mode %v, want 0700", fi.Mode().Perm())
	}
	if err := s.Check(job, atts); err != nil {
		t.Fatalf("Check right after Save: %v", err)
	}
	if err := s.Save(job, atts); err == nil {
		t.Fatal("a second Save for the same job replaced its pictures")
	}
	entries, _ := os.ReadDir(filepath.Dir(s.Dir(job)))
	if len(entries) != 1 {
		t.Fatalf("the store holds %d entries, want only the job's folder (no half-written ones)", len(entries))
	}
}

func TestCheckNamesAPictureThatIsGoneOrChanged(t *testing.T) {
	s := NewStore(t.TempDir())
	job := uuid.New()
	atts := decoded(t)
	if err := s.Save(job, atts); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path(job, atts[1]), pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Check(job, atts); err == nil || !strings.Contains(err.Error(), "02.webp has changed since CRN received it") {
		t.Fatalf("Check after a change = %v", err)
	}
	if err := os.Remove(s.Path(job, atts[0])); err != nil {
		t.Fatal(err)
	}
	if err := s.Check(job, atts); err == nil || !strings.Contains(err.Error(), "01.png is missing") {
		t.Fatalf("Check after a removal = %v", err)
	}
}

// A payload naming a file outside the job's folder still only reaches inside it.
func TestPathStaysInTheJobsFolder(t *testing.T) {
	s := NewStore(t.TempDir())
	job := uuid.New()
	if p := s.Path(job, domain.Attachment{File: "../../x.png"}); filepath.Dir(p) != s.Dir(job) {
		t.Fatalf("Path = %s, outside %s", p, s.Dir(job))
	}
}

func TestDropDeletesTheJobsPictures(t *testing.T) {
	s := NewStore(t.TempDir())
	job, other := uuid.New(), uuid.New()
	if err := s.Save(job, decoded(t)); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(other, decoded(t)); err != nil {
		t.Fatal(err)
	}
	n, err := s.Drop(job)
	if err != nil || n != 2 {
		t.Fatalf("Drop = %d, %v; want 2 files", n, err)
	}
	if _, err := os.Stat(s.Dir(job)); !os.IsNotExist(err) {
		t.Fatalf("the job's folder is still there: %v", err)
	}
	if _, err := os.Stat(s.Dir(other)); err != nil {
		t.Fatalf("Drop touched another job's pictures: %v", err)
	}
	if n, err := s.Drop(job); err != nil || n != 0 {
		t.Fatalf("a second Drop = %d, %v; want nothing to do", n, err)
	}
	if n, err := NewStore(filepath.Join(t.TempDir(), "never-made")).Drop(job); err != nil || n != 0 {
		t.Fatalf("Drop with no store folder = %d, %v", n, err)
	}
}

// At boot every folder whose job is not waiting to run goes, and so does anything else in
// the store.
func TestSweepKeepsOnlyTheFoldersOfJobsStillWaiting(t *testing.T) {
	s := NewStore(t.TempDir())
	waiting, ended := uuid.New(), uuid.New()
	for _, j := range []uuid.UUID{waiting, ended} {
		if err := s.Save(j, decoded(t)); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Dir(s.Dir(waiting))
	if err := os.Mkdir(filepath.Join(root, ".incoming-123"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stray.png"), pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	gone, err := s.Sweep(func(id uuid.UUID) bool { return id == waiting })
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(gone) != 3 {
		t.Fatalf("Sweep removed %v, want the ended job's folder, the half-written one and the stray file", gone)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 || entries[0].Name() != waiting.String() {
		t.Fatalf("left %v, want only %s", entries, waiting)
	}
	if _, err := NewStore(filepath.Join(t.TempDir(), "never-made")).Sweep(func(uuid.UUID) bool { return false }); err != nil {
		t.Fatalf("Sweep with no store folder: %v", err)
	}
}

// A copy of a picture anywhere in the project, under any name, is found; .git is not looked at.
func TestFindCopiesFindsAPictureUnderAnyName(t *testing.T) {
	dir := t.TempDir()
	atts := decoded(t)
	for name, body := range map[string][]byte{
		"public/shot.png":       pngBytes,
		"docs/notes.txt":        []byte("the screenshot shows a red box"),
		"src/same-size.bin":     bytes.Repeat([]byte{9}, len(webpBytes)),
		".git/objects/aa/x.png": webpBytes,
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := FindCopies(dir, atts)
	if err != nil {
		t.Fatalf("FindCopies: %v", err)
	}
	if len(got) != 1 || got[0] != "public/shot.png" {
		t.Fatalf("FindCopies = %v, want [public/shot.png]", got)
	}
	if got, err := FindCopies(dir, nil); err != nil || got != nil {
		t.Fatalf("FindCopies with no pictures = %v, %v", got, err)
	}
}
