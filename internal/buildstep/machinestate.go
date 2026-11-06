package buildstep

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// MachineState is what a build compares before its agent runs and before anyone hands over a
// token: each program CRN runs after the agent, each config file those programs read, and
// each way to have something run later. An agent runs as CRN's OS user and can change any of
// them. It is kept in CRN's memory, not in a file an agent could rewrite. A value is a
// program's path and hash, a file's hash, or a listing's hash.
type MachineState map[string]string

// cronCommand and launchctlCommand are the system's own, which PATH cannot replace.
var (
	cronCommand      = "/usr/bin/crontab"
	launchctlCommand = "/bin/launchctl"
)

// launchdIgnored are labels the system adds and drops by itself: macOS's own workers, and
// applications someone opened at the screen.
var launchdIgnored = []string{"com.apple.", "application."}

// hashFile is a file's hash, or "absent". Anything else it cannot read is an error: what it
// cannot read it cannot compare.
func hashFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "absent", nil
	}
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// hashProgram finds a program as CRN would and hashes what it finds.
func hashProgram(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "absent", nil
	}
	return hashAtPath(path)
}

func hashAtPath(path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if os.IsNotExist(err) {
		return "absent", nil
	}
	if err != nil {
		return "", err
	}
	sum, err := hashFile(real)
	if err != nil {
		return "", err
	}
	return real + " " + sum, nil
}

// hashDir is the hash of a directory's listing: each name, a link's target, a file's hash.
func hashDir(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return "absent", nil
	}
	if err != nil {
		return "", err
	}
	var listing strings.Builder
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		value := info.Mode().String()
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			if value, err = os.Readlink(path); err != nil {
				return "", err
			}
		case info.Mode().IsRegular():
			if value, err = hashFile(path); err != nil {
				return "", err
			}
		}
		fmt.Fprintf(&listing, "%s %s\n", e.Name(), value)
	}
	sum := sha256.Sum256([]byte(listing.String()))
	return hex.EncodeToString(sum[:]), nil
}

func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// launchdLabels lists what launchd holds for this user, past launchdIgnored.
func launchdLabels(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, launchctlCommand, "list").Output()
	if err != nil {
		return "", fmt.Errorf("launchctl list: %w", err)
	}
	var labels []string
next:
	for _, line := range strings.Split(string(out), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		for _, prefix := range launchdIgnored {
			if strings.HasPrefix(fields[2], prefix) {
				continue next
			}
		}
		labels = append(labels, fields[2])
	}
	sort.Strings(labels)
	return strings.Join(labels, "\n"), nil
}

// CaptureMachineState reads it all. Anything it cannot read is an error, not a blank.
func CaptureMachineState(ctx context.Context) (MachineState, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	state := MachineState{}
	for _, name := range []string{"git", "docker", "node"} {
		if state["program "+name], err = hashProgram(name); err != nil {
			return nil, fmt.Errorf("machine state: %s: %w", name, err)
		}
	}
	gitDir, err := gitCommand(ctx, "", "--exec-path").Output()
	if err != nil {
		return nil, fmt.Errorf("machine state: git --exec-path: %w", err)
	}
	for _, name := range []string{"git-remote-https", "git-credential-osxkeychain"} {
		if state["program "+name], err = hashAtPath(filepath.Join(strings.TrimSpace(string(gitDir)), name)); err != nil {
			return nil, fmt.Errorf("machine state: %s: %w", name, err)
		}
	}
	if state["program docker-buildx"], err = hashAtPath(buildxPlugin); err != nil {
		return nil, fmt.Errorf("machine state: buildx: %w", err)
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if state["program crn-server"], err = hashAtPath(self); err != nil {
		return nil, fmt.Errorf("machine state: this program: %w", err)
	}
	for name, path := range map[string]string{
		"config ~/.docker/config.json":    filepath.Join(home, ".docker", "config.json"),
		"config ~/.gitconfig":             filepath.Join(home, ".gitconfig"),
		"config ~/.config/git/config":     filepath.Join(home, ".config", "git", "config"),
		"config ~/.config/git/ignore":     filepath.Join(home, ".config", "git", "ignore"),
		"config ~/.config/git/attributes": filepath.Join(home, ".config", "git", "attributes"),
		"config /etc/gitconfig":           "/etc/gitconfig",
		"config homebrew gitconfig":       "/opt/homebrew/etc/gitconfig",
	} {
		if state[name], err = hashFile(path); err != nil {
			return nil, fmt.Errorf("machine state: %s: %w", name, err)
		}
	}
	for name, dir := range map[string]string{
		"listing ~/.docker/cli-plugins":  filepath.Join(home, ".docker", "cli-plugins"),
		"listing ~/Library/LaunchAgents": filepath.Join(home, "Library", "LaunchAgents"),
	} {
		if state[name], err = hashDir(dir); err != nil {
			return nil, fmt.Errorf("machine state: %s: %w", name, err)
		}
	}
	cron := exec.CommandContext(ctx, cronCommand, "-l")
	var stdout, stderr bytes.Buffer
	cron.Stdout, cron.Stderr = &stdout, &stderr
	if err := cron.Run(); err != nil && !strings.Contains(stderr.String(), "no crontab") {
		return nil, fmt.Errorf("machine state: crontab: %w", err)
	}
	state["cron"] = hashText(stdout.String())
	labels, err := launchdLabels(ctx)
	if err != nil {
		return nil, fmt.Errorf("machine state: %w", err)
	}
	state["launchd labels"] = hashText(labels)
	return state, nil
}

// Differences names what changed from s to now, values left out, sorted.
func (s MachineState) Differences(now MachineState) []string {
	var differ []string
	for name, value := range s {
		if now[name] != value {
			differ = append(differ, name)
		}
	}
	for name := range now {
		if _, ok := s[name]; !ok {
			differ = append(differ, name)
		}
	}
	sort.Strings(differ)
	return differ
}
