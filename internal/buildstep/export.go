package buildstep

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var commitID = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// gitObjects runs git over the object store, as the commit names it: refs/replace in a
// workspace cannot answer other bytes for the same id.
func gitObjects(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-replace-objects", "-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd
}

type treeEntry struct {
	mode, kind, sha, path string
}

// ExportCommit writes exactly what the commit's tree holds to dest, which must be empty:
// every file byte for byte with its mode, every symlink with its target. It reads objects
// rather than checking out or archiving, which both obey the tree's own .gitattributes:
// export-ignore could hide a file from the scan and the build, export-subst and eol could
// change one. What cannot be held exactly, a submodule or a path that would collide on
// this filesystem, is an error.
func ExportCommit(ctx context.Context, dir, commit, dest string) error {
	if !commitID.MatchString(commit) {
		return fmt.Errorf("export: %q is not a commit id", commit)
	}
	entries, err := listTree(ctx, dir, commit)
	if err != nil {
		return err
	}
	cat := gitObjects(ctx, dir, "cat-file", "--batch")
	in, err := cat.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cat.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cat.Stderr = &stderr
	if err := cat.Start(); err != nil {
		return fmt.Errorf("export commit %s: %w", commit, err)
	}
	defer func() { _ = in.Close(); _ = cat.Wait() }()
	objects := bufio.NewReader(out)

	// Symlinks last: nothing is ever written through one.
	var links []treeEntry
	for _, e := range entries {
		switch e.mode {
		case "100644", "100755":
			if err := exportFile(in, objects, e, dest); err != nil {
				return fmt.Errorf("export commit %s: %w", commit, err)
			}
		case "120000":
			links = append(links, e)
		default:
			return fmt.Errorf("export commit %s: %s has mode %s, which a commit does not hold the content of", commit, e.path, e.mode)
		}
	}
	for _, e := range links {
		target, err := readBlob(in, objects, e)
		if err != nil {
			return fmt.Errorf("export commit %s: %w", commit, err)
		}
		link := filepath.Join(dest, filepath.FromSlash(e.path))
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			return fmt.Errorf("export commit %s: %w", commit, err)
		}
		if err := os.Symlink(string(target), link); err != nil {
			return fmt.Errorf("export commit %s: %w", commit, err)
		}
	}
	return nil
}

func listTree(ctx context.Context, dir, commit string) ([]treeEntry, error) {
	var stdout, stderr bytes.Buffer
	ls := gitObjects(ctx, dir, "ls-tree", "-r", "-z", "--full-tree", commit)
	ls.Stdout, ls.Stderr = &stdout, &stderr
	if err := ls.Run(); err != nil {
		return nil, fmt.Errorf("export commit %s: %w: %s", commit, err, strings.TrimSpace(stderr.String()))
	}
	var entries []treeEntry
	for _, raw := range bytes.Split(stdout.Bytes(), []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		head, path, ok := strings.Cut(string(raw), "\t")
		fields := strings.Fields(head)
		if !ok || len(fields) != 3 {
			return nil, fmt.Errorf("export commit %s: unreadable tree entry", commit)
		}
		if !safeTreePath(path) {
			return nil, fmt.Errorf("export commit %s: the tree holds a path that may not be written", commit)
		}
		entries = append(entries, treeEntry{mode: fields[0], kind: fields[1], sha: fields[2], path: path})
	}
	return entries, nil
}

func safeTreePath(path string) bool {
	if !filepath.IsLocal(filepath.FromSlash(path)) {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

func exportFile(in io.Writer, objects *bufio.Reader, e treeEntry, dest string) error {
	perm := os.FileMode(0o644)
	if e.mode == "100755" {
		perm = 0o755
	}
	name := filepath.Join(dest, filepath.FromSlash(e.path))
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	// O_EXCL: two names this filesystem folds into one must not silently become one file.
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	defer file.Close()
	size, err := requestBlob(in, objects, e)
	if err != nil {
		return err
	}
	if _, err := io.CopyN(file, objects, size); err != nil {
		return err
	}
	if _, err := objects.Discard(1); err != nil { // the newline after the object
		return err
	}
	if err := file.Chmod(perm); err != nil {
		return err
	}
	return file.Close()
}

func readBlob(in io.Writer, objects *bufio.Reader, e treeEntry) ([]byte, error) {
	size, err := requestBlob(in, objects, e)
	if err != nil {
		return nil, err
	}
	var body bytes.Buffer
	if _, err := io.CopyN(&body, objects, size); err != nil {
		return nil, err
	}
	if _, err := objects.Discard(1); err != nil {
		return nil, err
	}
	return body.Bytes(), nil
}

// requestBlob asks cat-file for one object and returns its size; its bytes follow.
func requestBlob(in io.Writer, objects *bufio.Reader, e treeEntry) (int64, error) {
	if e.kind != "blob" {
		return 0, fmt.Errorf("%s is a %s, not a blob", e.path, e.kind)
	}
	if _, err := fmt.Fprintln(in, e.sha); err != nil {
		return 0, err
	}
	header, err := objects.ReadString('\n')
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(header)
	if len(fields) != 3 || fields[0] != e.sha || fields[1] != "blob" {
		return 0, fmt.Errorf("git answered %q for %s", strings.TrimSpace(header), e.sha)
	}
	return strconv.ParseInt(fields[2], 10, 64)
}

// ScanCommitHistory reads every object this build added to the branch: each commit, its
// message, its trees (file names) and its blobs. The final tree alone would miss a value
// committed and removed again, and git would still push it. base is the head before the
// agent ran; empty reads all the commit reaches.
func ScanCommitHistory(ctx context.Context, dir, base, commit string, scanner *ForbiddenScanner) error {
	if !commitID.MatchString(commit) || (base != "" && !commitID.MatchString(base)) {
		return errors.New("history scan: not a commit id")
	}
	args := []string{"rev-list", "--objects", "--no-object-names", commit}
	if base != "" {
		args = append(args, "^"+base)
	}
	var list, stderr bytes.Buffer
	rev := gitObjects(ctx, dir, args...)
	rev.Stdout, rev.Stderr = &list, &stderr
	if err := rev.Run(); err != nil {
		return fmt.Errorf("history scan: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	stderr.Reset()
	cat := gitObjects(ctx, dir, "cat-file", "--batch")
	cat.Stdin, cat.Stdout, cat.Stderr = &list, scanner.Source("the commits this build added"), &stderr
	if err := cat.Run(); err != nil {
		return fmt.Errorf("history scan: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// HeadCommit is the commit the workspace is on.
func HeadCommit(ctx context.Context, dir string, logger *slog.Logger) (string, error) {
	if logger == nil {
		logger = slog.Default()
	}
	return runGit(ctx, dir, logger, "rev-parse", "HEAD")
}

// RemoteHead asks the remote which commit the branch is on now.
func RemoteHead(ctx context.Context, dir, remote, branch string, logger *slog.Logger) (string, error) {
	if logger == nil {
		logger = slog.Default()
	}
	out, err := runGit(ctx, dir, logger, "ls-remote", remote, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	sha, _, _ := strings.Cut(out, "\t")
	if !commitID.MatchString(sha) {
		return "", fmt.Errorf("the remote holds no branch %s", branch)
	}
	return sha, nil
}
