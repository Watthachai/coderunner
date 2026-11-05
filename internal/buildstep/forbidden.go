package buildstep

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// ForbiddenValues is how many real values a project's list holds: the database's host,
// name and role, as its change request withholds them.
const ForbiddenValues = 3

// maxForbiddenList bounds what is read of a list: three short lines and a comment.
const maxForbiddenList = 64 << 10

var forbiddenLine = regexp.MustCompile(`^([0-9a-f]{64}) (\d+) (\d+)$`)

// listOwner is who must own a list: the user CRN runs as.
var listOwner = os.Getuid

type forbiddenEntry struct {
	sha256  string
	length  int
	rolling uint64
}

// ForbiddenList is a project's list of forbidden value hashes, pinned before the agent
// starts. The agent runs as the same OS user and could rewrite the file, so everything
// after the pin works from these bytes, and CheckUnchanged tells whether the file moved.
type ForbiddenList struct {
	path    string
	info    os.FileInfo
	bytes   []byte
	sha256  string
	entries []forbiddenEntry
}

// PinForbiddenList reads the list once. The error wraps fs.ErrNotExist when there is no
// list; anything but a regular file, mode 600, owned by this user, with exactly
// ForbiddenValues well-formed and different lines, is refused.
func PinForbiddenList(path string) (*ForbiddenList, error) {
	info, raw, err := readForbiddenFile(path)
	if err != nil {
		return nil, err
	}
	entries, err := parseForbiddenList(raw)
	if err != nil {
		return nil, fmt.Errorf("forbidden list %s: %w", path, err)
	}
	sum := sha256.Sum256(raw)
	return &ForbiddenList{path: path, info: info, bytes: raw, sha256: hex.EncodeToString(sum[:]), entries: entries}, nil
}

// readForbiddenFile looks at the path itself, opens it without following a link, and
// checks the file it opened is the one it looked at.
func readForbiddenFile(path string) (os.FileInfo, []byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("forbidden list: %w", err)
	}
	if err := checkForbiddenFile(path, info); err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("forbidden list: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("forbidden list: %w", err)
	}
	if !os.SameFile(info, opened) {
		return nil, nil, fmt.Errorf("forbidden list %s changed while it was being read", path)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxForbiddenList+1))
	if err != nil {
		return nil, nil, fmt.Errorf("forbidden list: %w", err)
	}
	if len(raw) > maxForbiddenList {
		return nil, nil, fmt.Errorf("forbidden list %s is larger than a list can be", path)
	}
	return info, raw, nil
}

func checkForbiddenFile(path string, info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("forbidden list %s must be a regular file, not %s", path, info.Mode().Type())
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("forbidden list %s must be mode 600, not %o", path, info.Mode().Perm())
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("forbidden list %s: cannot read its owner", path)
	}
	if int(stat.Uid) != listOwner() {
		return fmt.Errorf("forbidden list %s must be owned by uid %d, not %d", path, listOwner(), stat.Uid)
	}
	return nil
}

func parseForbiddenList(raw []byte) ([]forbiddenEntry, error) {
	var entries []forbiddenEntry
	seen := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := forbiddenLine.FindStringSubmatch(line)
		if m == nil {
			return nil, errors.New("a line is malformed (want: <sha256> <length> <rolling>)")
		}
		length, err := strconv.Atoi(m[2])
		if err != nil || length < 1 {
			return nil, errors.New("a line has no usable length")
		}
		rolling, err := strconv.ParseUint(m[3], 10, 64)
		if err != nil || rolling >= scanPrime {
			return nil, errors.New("a line has no usable rolling hash")
		}
		if seen[m[1]] {
			return nil, errors.New("a value is listed twice")
		}
		seen[m[1]] = true
		entries = append(entries, forbiddenEntry{sha256: m[1], length: length, rolling: rolling})
	}
	if len(entries) != ForbiddenValues {
		return nil, fmt.Errorf("it holds %d values, want %d", len(entries), ForbiddenValues)
	}
	return entries, nil
}

// Bytes is a copy of the list as it was pinned.
func (l *ForbiddenList) Bytes() []byte { return bytes.Clone(l.bytes) }

// SHA256 is the hash of the bytes as they were pinned.
func (l *ForbiddenList) SHA256() string { return l.sha256 }

// CheckUnchanged says whether the file on disk is still the one pinned: the same file,
// unmodified, with the same bytes. The pinned bytes are used either way; a change means
// something touched the list during the build, so the build must not pass.
func (l *ForbiddenList) CheckUnchanged() error {
	info, raw, err := readForbiddenFile(l.path)
	if err != nil {
		return fmt.Errorf("the list of forbidden values changed during the build: %w", err)
	}
	if !os.SameFile(l.info, info) {
		return fmt.Errorf("the list of forbidden values changed during the build: %s is another file now", l.path)
	}
	if !info.ModTime().Equal(l.info.ModTime()) {
		return fmt.Errorf("the list of forbidden values changed during the build: %s was modified", l.path)
	}
	if !bytes.Equal(raw, l.bytes) {
		return fmt.Errorf("the list of forbidden values changed during the build: %s holds other bytes", l.path)
	}
	return nil
}

// Holds says whether data holds a real value, without recording anything.
func (l *ForbiddenList) Holds(data []byte) bool {
	s := l.NewScanner()
	s.Scan("", data)
	return len(s.Hits()) > 0
}

// --- the scanner: the same search as accept-candidate.mjs, over logs -----------------------

const (
	scanBase  uint64 = 257
	scanPrime uint64 = 2147483647
	// maxForbiddenHits bounds what a scanner keeps: the build has failed long before.
	maxForbiddenHits = 100
)

// ForbiddenHit says where a value was, never what it is.
type ForbiddenHit struct {
	Label, Where string
	Offset       int64
}

type scanGroup struct {
	length  int
	pow     uint64
	targets map[uint64][]labelledEntry
}

type labelledEntry struct {
	forbiddenEntry
	label string
}

// ForbiddenScanner finds real values in everything a build prints. Placeholders are not
// its business: they belong in the change text, so logs may repeat them.
type ForbiddenScanner struct {
	mu     sync.Mutex
	groups []*scanGroup
	keep   int
	hits   []ForbiddenHit
}

// NewScanner starts a scanner over the pinned values.
func (l *ForbiddenList) NewScanner() *ForbiddenScanner {
	byLength := map[int]*scanGroup{}
	s := &ForbiddenScanner{}
	for i, e := range l.entries {
		g := byLength[e.length]
		if g == nil {
			pow := uint64(1)
			for range e.length {
				pow = pow * scanBase % scanPrime
			}
			g = &scanGroup{length: e.length, pow: pow, targets: map[uint64][]labelledEntry{}}
			byLength[e.length] = g
			s.groups = append(s.groups, g)
		}
		g.targets[e.rolling] = append(g.targets[e.rolling], labelledEntry{e, fmt.Sprintf("real value #%d", i+1)})
		if e.length-1 > s.keep {
			s.keep = e.length - 1
		}
	}
	return s
}

// Scan reads one whole output.
func (s *ForbiddenScanner) Scan(where string, data []byte) {
	_, _ = s.Source(where).Write(data)
}

// Source is a stream of one output: a value split across two writes is still found.
func (s *ForbiddenScanner) Source(where string) io.Writer {
	return &scanSource{scanner: s, where: where}
}

type scanSource struct {
	scanner *ForbiddenScanner
	where   string
	tail    []byte
	before  int64
}

func (w *scanSource) Write(chunk []byte) (int, error) {
	s := w.scanner
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(chunk) == 0 {
		return 0, nil
	}
	buf := append(bytes.Clone(w.tail), chunk...)
	fresh := len(w.tail) // a window must end in the new bytes, or it was seen
	for _, g := range s.groups {
		var h uint64
		for i, b := range buf {
			h = (h*scanBase + uint64(b)) % scanPrime
			if i >= g.length {
				h = (h + scanPrime - uint64(buf[i-g.length])*g.pow%scanPrime) % scanPrime
			}
			if i < g.length-1 || i < fresh {
				continue
			}
			candidates, ok := g.targets[h]
			if !ok {
				continue
			}
			sum := sha256.Sum256(buf[i-g.length+1 : i+1])
			digest := hex.EncodeToString(sum[:])
			for _, e := range candidates {
				if e.sha256 == digest && len(s.hits) < maxForbiddenHits {
					s.hits = append(s.hits, ForbiddenHit{Label: e.label, Where: w.where, Offset: w.before + int64(i-g.length+1)})
				}
			}
		}
	}
	cut := max(0, len(buf)-s.keep)
	w.before += int64(cut)
	w.tail = bytes.Clone(buf[cut:])
	return len(chunk), nil
}

// Hits is what was found so far.
func (s *ForbiddenScanner) Hits() []ForbiddenHit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ForbiddenHit(nil), s.hits...)
}

// Problem is nil while nothing was found. It says where, never what.
func (s *ForbiddenScanner) Problem() error {
	hits := s.Hits()
	if len(hits) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var places []string
	for _, h := range hits {
		place := h.Label + " in " + h.Where
		if !seen[place] && len(places) < 8 {
			seen[place] = true
			places = append(places, place)
		}
	}
	return fmt.Errorf("Forbidden content in this build's logs: %s", strings.Join(places, "; "))
}

// --- what commands print ------------------------------------------------------------------

type commandOutputKey struct{}

// WithCommandOutput hands sink what every command of this package prints under ctx,
// success or not, named by the command: docker build, git push, the release checker.
func WithCommandOutput(ctx context.Context, sink func(name string, output []byte)) context.Context {
	return context.WithValue(ctx, commandOutputKey{}, sink)
}

func recordOutput(ctx context.Context, name string, output []byte) {
	if sink, ok := ctx.Value(commandOutputKey{}).(func(string, []byte)); ok && len(output) > 0 {
		sink(name, output)
	}
}
