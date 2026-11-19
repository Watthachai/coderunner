// Package attach checks the pictures an edit request carries and keeps them, outside every
// repository, for as long as their build lives (docs/edit-attachments.md).
//
// The contract was agreed with DV on 19 Nov 2026: at most 10 PNG, JPEG or WebP pictures a
// request, 5 MiB each and 20 MiB together; the sha256 and the kind are checked against the
// bytes; one picture that is not right refuses the whole request. The pictures never go
// into git, the image or the database, and they are deleted when the build ends.
package attach

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

const (
	// MaxCount is the most pictures one request may carry.
	MaxCount = 10
	// MaxSize is the most bytes one picture may have, decoded.
	MaxSize = 5 << 20
	// MaxTotal is the most bytes a request's pictures may have together, decoded.
	MaxTotal = 20 << 20
	// MaxBody caps the edit request's body: the pictures in base64 (a third more than
	// MaxTotal) and the change text.
	MaxBody = 32 << 20
	maxName = 255
)

// Input is one picture as the request sends it.
type Input struct {
	Item   *int   `json:"item,omitempty"`
	Name   string `json:"name"`
	Mime   string `json:"mime"`
	SHA256 string `json:"sha256"`
	Bytes  string `json:"bytes"`
}

type kind struct {
	mime, ext string
	is        func([]byte) bool
}

var kinds = []kind{
	{"image/png", ".png", func(b []byte) bool { return bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) }},
	{"image/jpeg", ".jpg", func(b []byte) bool { return bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}) }},
	{"image/webp", ".webp", func(b []byte) bool {
		return len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP"
	}},
}

func kindOf(mime string) (kind, bool) {
	for _, k := range kinds {
		if k.mime == mime {
			return k, true
		}
	}
	return kind{}, false
}

// sniff names the kind the bytes are, whatever the request says; "" when none of the three.
func sniff(b []byte) string {
	for _, k := range kinds {
		if k.is(b) {
			return k.mime
		}
	}
	return ""
}

// Decode checks every picture of a request. items are the item=N numbers of the change's
// FITTCORE-CHANGE-REQUEST (none when it has no envelope). The first problem refuses them
// all: the error says which picture and why, and nothing is kept.
func Decode(in []Input, items []int) ([]domain.Attachment, error) {
	if len(in) > MaxCount {
		return nil, fmt.Errorf("attachments: at most %d pictures per request, got %d", MaxCount, len(in))
	}
	out := make([]domain.Attachment, 0, len(in))
	seen := map[string]int{}
	total := 0
	for i, raw := range in {
		a, k, err := decodeOne(raw, items)
		if err != nil {
			return nil, fmt.Errorf("attachments[%d]: %w", i, err)
		}
		if j, dup := seen[a.SHA256]; dup {
			return nil, fmt.Errorf("attachments[%d]: the same picture as attachments[%d]", i, j)
		}
		seen[a.SHA256] = i
		total += a.Size
		a.File = fmt.Sprintf("%02d%s", i+1, k.ext)
		out = append(out, a)
	}
	if total > MaxTotal {
		return nil, fmt.Errorf("attachments: the pictures add up to %d bytes; at most %d (20 MiB)", total, MaxTotal)
	}
	return out, nil
}

func decodeOne(in Input, items []int) (domain.Attachment, kind, error) {
	var a domain.Attachment
	name := strings.TrimSpace(in.Name)
	switch {
	case name == "":
		return a, kind{}, errors.New("name is required")
	case len(name) > maxName:
		return a, kind{}, fmt.Errorf("name is longer than %d bytes", maxName)
	case !utf8.ValidString(name):
		return a, kind{}, errors.New("name is not UTF-8")
	case strings.IndexFunc(name, unicode.IsControl) >= 0:
		return a, kind{}, errors.New("name has a control character")
	}
	a.Name = name

	if in.Item != nil {
		n := *in.Item
		switch {
		case n < 1:
			return a, kind{}, errors.New("item must be 1 or more")
		case len(items) == 0:
			return a, kind{}, errors.New("item is given but the change has no item=N blocks")
		case !slices.Contains(items, n):
			return a, kind{}, fmt.Errorf("item %d is not an item of the change (it has items %s)", n, joinInts(items))
		}
		a.Item = n
	}

	k, ok := kindOf(in.Mime)
	if !ok {
		return a, kind{}, fmt.Errorf("mime must be image/png, image/jpeg or image/webp, not %q", in.Mime)
	}
	a.Mime = k.mime

	if in.Bytes == "" {
		return a, kind{}, errors.New("bytes is empty")
	}
	data, err := base64.StdEncoding.DecodeString(in.Bytes)
	if err != nil {
		return a, kind{}, errors.New("bytes is not base64 (standard alphabet, padded)")
	}
	if len(data) == 0 {
		return a, kind{}, errors.New("bytes is empty")
	}
	if len(data) > MaxSize {
		return a, kind{}, fmt.Errorf("the picture is %d bytes; at most %d (5 MiB)", len(data), MaxSize)
	}
	switch is := sniff(data); {
	case is == "":
		return a, kind{}, errors.New("the bytes are not a PNG, JPEG or WebP picture")
	case is != k.mime:
		return a, kind{}, fmt.Errorf("the bytes are %s, not %s", is, k.mime)
	}

	want := strings.ToLower(strings.TrimSpace(in.SHA256))
	if len(want) != 64 {
		return a, kind{}, errors.New("sha256 must be 64 hex characters")
	}
	if _, err := hex.DecodeString(want); err != nil {
		return a, kind{}, errors.New("sha256 must be 64 hex characters")
	}
	if hexSum(data) != want {
		return a, kind{}, errors.New("sha256 does not match the bytes")
	}
	a.SHA256, a.Size, a.Data = want, len(data), data
	return a, k, nil
}

func hexSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func joinInts(ns []int) string {
	s := make([]string, len(ns))
	for i, n := range ns {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, ", ")
}

// Store keeps each job's pictures in a folder of its own, readable by CRN's account only.
type Store struct{ dir string }

// NewStore keeps pictures under dir, made absolute so the agent can be given full paths.
func NewStore(dir string) *Store {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return &Store{dir: dir}
}

// Dir is the folder of the job's pictures.
func (s *Store) Dir(job uuid.UUID) string { return filepath.Join(s.dir, job.String()) }

// Path is where a picture of the job is. Only the base of a.File counts, so a payload can
// never point outside the job's folder.
func (s *Store) Path(job uuid.UUID, a domain.Attachment) string {
	return filepath.Join(s.Dir(job), filepath.Base(filepath.Clean("/"+a.File)))
}

// Save writes the job's pictures, all or none: they are written into a folder of their
// own and that folder only then takes the job's name. A job that already has pictures
// keeps them and Save fails.
func (s *Store) Save(job uuid.UUID, atts []domain.Attachment) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("attach: make the store: %w", err)
	}
	tmp, err := os.MkdirTemp(s.dir, ".incoming-")
	if err != nil {
		return fmt.Errorf("attach: %w", err)
	}
	defer os.RemoveAll(tmp)
	for _, a := range atts {
		if err := os.WriteFile(filepath.Join(tmp, filepath.Base(s.Path(job, a))), a.Data, 0o600); err != nil {
			return fmt.Errorf("attach: write %s: %w", a.File, err)
		}
	}
	if _, err := os.Lstat(s.Dir(job)); err == nil {
		return fmt.Errorf("attach: job %s already has pictures", job)
	}
	if err := os.Rename(tmp, s.Dir(job)); err != nil {
		return fmt.Errorf("attach: %w", err)
	}
	return nil
}

// Check confirms that the job's pictures are on disk as CRN received them.
func (s *Store) Check(job uuid.UUID, atts []domain.Attachment) error {
	for _, a := range atts {
		b, err := os.ReadFile(s.Path(job, a))
		if err != nil {
			return fmt.Errorf("picture %s is missing (%v)", a.File, err)
		}
		if hexSum(b) != a.SHA256 {
			return fmt.Errorf("picture %s has changed since CRN received it", a.File)
		}
	}
	return nil
}

// Drop deletes the job's pictures and says how many files went. A job without pictures
// is nothing to do.
func (s *Store) Drop(job uuid.UUID) (int, error) {
	entries, err := os.ReadDir(s.Dir(job))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("attach: %w", err)
	}
	if err := os.RemoveAll(s.Dir(job)); err != nil {
		return 0, fmt.Errorf("attach: %w", err)
	}
	return len(entries), nil
}

// Sweep deletes every folder whose job keep does not claim, and anything else in the store
// (a folder Save never finished, a stray file). It returns what it deleted.
func (s *Store) Sweep(keep func(uuid.UUID) bool) ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("attach: %w", err)
	}
	var gone []string
	var errs []error
	for _, e := range entries {
		if id, err := uuid.Parse(e.Name()); err == nil && e.IsDir() && id.String() == e.Name() && keep(id) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.dir, e.Name())); err != nil {
			errs = append(errs, err)
			continue
		}
		gone = append(gone, e.Name())
	}
	return gone, errors.Join(errs...)
}

// FindCopies lists, relative to dir, the files that are byte for byte one of the pictures,
// whatever their name. .git is not looked at: what is committed comes from the tree.
func FindCopies(dir string, atts []domain.Attachment) ([]string, error) {
	if len(atts) == 0 {
		return nil, nil
	}
	sums := map[int64]map[string]bool{}
	for _, a := range atts {
		if sums[int64(a.Size)] == nil {
			sums[int64(a.Size)] = map[string]bool{}
		}
		sums[int64(a.Size)][a.SHA256] = true
	}
	var found []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" && path != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		want := sums[info.Size()]
		if want == nil {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if want[hexSum(b)] {
			rel, _ := filepath.Rel(dir, path)
			found = append(found, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("attach: look for copies: %w", err)
	}
	sort.Strings(found)
	return found, nil
}
