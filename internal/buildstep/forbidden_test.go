package buildstep

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Written by accept-candidate.mjs's forbiddenEntry, so these also prove that Go reads
// and finds what the Node side hashes. None of the values is real.
const (
	lineHost = "e3299a765ddf6ad382feefff38bd5ab2f62663bec6e7be74ccee7670e77d9138 13 1374833046" // 203.0.113.250
	lineDB   = "9d286135efc6346d32671c8654f7fd1afe4dcd5f50a6cdd84b9e136c30ab65a8 12 1501365033" // fixture_db_x
	lineRole = "0bb451f56996512d9bca2ce6040f6b40d94f6a2117f9f10540312a6285ee0543 14 614203677"  // fixture_role_x
)

const goodList = "# Hashes. Holds no value.\n" + lineHost + "\n" + lineDB + "\n" + lineRole + "\n"

func writeList(t *testing.T, text string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "list.txt")
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPinKeepsTheBytesAndTheirHash(t *testing.T) {
	list, err := PinForbiddenList(writeList(t, goodList, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	if string(list.Bytes()) != goodList {
		t.Errorf("pinned bytes = %q", list.Bytes())
	}
	sum := sha256.Sum256([]byte(goodList))
	if list.SHA256() != hex.EncodeToString(sum[:]) {
		t.Errorf("sha256 = %s", list.SHA256())
	}
}

func TestPinSaysWhenThereIsNoList(t *testing.T) {
	_, err := PinForbiddenList(filepath.Join(t.TempDir(), "none.txt"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want not-exist", err)
	}
}

func TestPinRefusesAnythingButTheAgreedFile(t *testing.T) {
	dir := t.TempDir()
	real := writeList(t, goodList, 0o600)
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"a symlink":        link,
		"a directory":      dir,
		"group readable":   writeList(t, goodList, 0o640),
		"world readable":   writeList(t, goodList, 0o644),
		"owner read only":  writeList(t, goodList, 0o400),
		"two values":       writeList(t, lineHost+"\n"+lineDB+"\n", 0o600),
		"four values":      writeList(t, goodList+"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 3 1\n", 0o600),
		"the same twice":   writeList(t, lineHost+"\n"+lineDB+"\n"+lineHost+"\n", 0o600),
		"a malformed line": writeList(t, lineHost+"\n"+lineDB+"\nnot a hash\n", 0o600),
		"a plain value":    writeList(t, lineHost+"\n"+lineDB+"\n203.0.113.250\n", 0o600),
		"nothing":          writeList(t, "", 0o600),
	}
	for name, path := range cases {
		if _, err := PinForbiddenList(path); err == nil {
			t.Errorf("%s: pinned", name)
		}
	}
}

func TestPinRefusesAListSomeoneElseOwns(t *testing.T) {
	was := listOwner
	listOwner = func() int { return os.Getuid() + 1 }
	defer func() { listOwner = was }()
	if _, err := PinForbiddenList(writeList(t, goodList, 0o600)); err == nil {
		t.Error("pinned a list another user owns")
	}
}

func TestCheckUnchangedSeesEveryWayTheFileCanChange(t *testing.T) {
	cases := map[string]func(t *testing.T, path string){
		"edited": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(strings.Replace(goodList, "1374833046", "1374833047", 1)), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"a value removed": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(lineHost+"\n"+lineDB+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"deleted": func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		},
		"turned into a symlink to the same bytes": func(t *testing.T, path string) {
			copy := writeList(t, goodList, 0o600)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(copy, path); err != nil {
				t.Fatal(err)
			}
		},
		"replaced by a new file with the same bytes": func(t *testing.T, path string) {
			fresh := path + ".new"
			if err := os.WriteFile(fresh, []byte(goodList), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(fresh, path); err != nil {
				t.Fatal(err)
			}
		},
		"opened to others": func(t *testing.T, path string) {
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeList(t, goodList, 0o600)
			list, err := PinForbiddenList(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := list.CheckUnchanged(); err != nil {
				t.Fatalf("before the change: %v", err)
			}
			change(t, path)
			if err := list.CheckUnchanged(); err == nil {
				t.Error("the change went unseen")
			}
			if string(list.Bytes()) != goodList {
				t.Error("the pinned bytes moved with the file")
			}
		})
	}
}

func pinnedScanner(t *testing.T) *ForbiddenScanner {
	t.Helper()
	list, err := PinForbiddenList(writeList(t, goodList, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	return list.NewScanner()
}

func TestTheScannerFindsWhatNodeHashed(t *testing.T) {
	s := pinnedScanner(t)
	s.Scan("docker build", []byte("clean output\n"))
	if s.Problem() != nil {
		t.Fatalf("clean output: %v", s.Problem())
	}
	s.Scan("docker build", []byte("connect to 203.0.113.250:5432"))
	s.Scan("git push", []byte("x-fixture_db_x-y"))
	s.Scan("agent output", []byte("role=fixture_role_x"))
	got := s.Hits()
	want := []ForbiddenHit{
		{Label: "real value #1", Where: "docker build", Offset: 11},
		{Label: "real value #2", Where: "git push", Offset: 2},
		{Label: "real value #3", Where: "agent output", Offset: 5},
	}
	if len(got) != len(want) {
		t.Fatalf("hits = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("hit %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	err := s.Problem()
	if err == nil {
		t.Fatal("no problem reported")
	}
	for _, value := range []string{"203.0.113.250", "fixture_db_x", "fixture_role_x"} {
		if strings.Contains(err.Error(), value) {
			t.Errorf("the problem repeats %s: %v", value, err)
		}
	}
	if !strings.Contains(err.Error(), "docker build") || !strings.Contains(err.Error(), "real value #1") {
		t.Errorf("the problem does not say where: %v", err)
	}
}

func TestAValueSplitAcrossWritesIsFoundOnce(t *testing.T) {
	s := pinnedScanner(t)
	w := s.Source("agent output")
	for _, part := range []string{"12345fixture_", "db_x678", "tail", "fixture_db", "_x"} {
		if _, err := w.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	got := s.Hits()
	if len(got) != 2 || got[0].Offset != 5 || got[1].Offset != 24 {
		t.Errorf("hits = %+v, want two, at 5 and 24", got)
	}
}

func TestAnotherSourceDoesNotContinueTheLastOne(t *testing.T) {
	s := pinnedScanner(t)
	s.Scan("one", []byte("fixture_"))
	s.Scan("two", []byte("db_x"))
	if s.Problem() != nil {
		t.Errorf("two sources joined: %v", s.Problem())
	}
}

func TestHoldsAsksWithoutCounting(t *testing.T) {
	list, err := PinForbiddenList(writeList(t, goodList, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	if !list.Holds([]byte("dial 203.0.113.250")) || list.Holds([]byte("dial 203.0.113.251")) {
		t.Error("Holds is wrong")
	}
	s := list.NewScanner()
	if list.Holds([]byte("fixture_db_x")) && s.Problem() != nil {
		t.Error("Holds counted in a scanner")
	}
}

func TestAPlaceholderIsNotARealValue(t *testing.T) {
	s := pinnedScanner(t)
	s.Scan("change text", []byte("host is WITHHELD_DB_HOST, name WITHHELD_DB_NAME, role WITHHELD_DB_ROLE"))
	if s.Problem() != nil {
		t.Errorf("a placeholder counted as a real value: %v", s.Problem())
	}
}
