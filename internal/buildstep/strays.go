package buildstep

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// strayExempt are where launchd starts things for this user on its own schedule, and Docker
// Desktop's helpers. Whatever runs from here is left alone, and is what an operator still
// looks at before handing over a token. An application someone opens at the screen during a
// build is not on this list.
var strayExempt = []string{"/System/", "/usr/libexec/", "/usr/sbin/", "/sbin/", "/Library/Apple/", "/Applications/Docker.app/"}

const strayRounds = 30

type process struct {
	pid, ppid, uid int
	started        time.Time
	path           string
}

const processTime = "Mon Jan _2 15:04:05 2006"

// parseProcesses reads `ps -axo pid=,ppid=,uid=,lstart=,comm=`. A line it cannot read is an
// error: a process the sweep cannot see is one it would leave running.
func parseProcesses(out string, loc *time.Location) ([]process, error) {
	var list []process
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 9 {
			return nil, fmt.Errorf("process list: unreadable line %q", line)
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		uid, err3 := strconv.Atoi(fields[2])
		started, err4 := time.ParseInLocation(processTime, strings.Join(fields[3:8], " "), loc)
		if err := errors.Join(err1, err2, err3, err4); err != nil {
			return nil, fmt.Errorf("process list: unreadable line %q", line)
		}
		list = append(list, process{pid: pid, ppid: ppid, uid: uid, started: started, path: strings.Join(fields[8:], " ")})
	}
	return list, nil
}

// strayProcesses are the ones this user started since `since` that nothing owns any more:
// launchd (1) adopted them because their parent is gone. A live process's child waits for a
// later round, when its parent has been stopped.
func strayProcesses(list []process, since time.Time, uid, self int) []process {
	var stray []process
	for _, p := range list {
		if p.uid != uid || p.pid == self || p.ppid != 1 || p.started.Before(since.Truncate(time.Second)) {
			continue
		}
		exempt := false
		for _, prefix := range strayExempt {
			exempt = exempt || strings.HasPrefix(p.path, prefix)
		}
		if !exempt {
			stray = append(stray, p)
		}
	}
	return stray
}

// psCommand is the system's own, which PATH cannot replace.
var psCommand = "/bin/ps"

// listProcesses is a variable so a test can show a process that will not stay stopped.
var listProcesses = func(ctx context.Context) ([]process, error) {
	cmd := exec.CommandContext(ctx, psCommand, "-axo", "pid=,ppid=,uid=,lstart=,comm=")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("process list: %w", err)
	}
	return parseProcesses(stdout.String(), time.Local)
}

// StopStrayProcesses kills what an agent left running once it has exited, including one
// that left its process group, started a new session or cleared its environment: this
// looks at who started it and when, not at how it describes itself. It says what it killed,
// and is an error when it cannot see the processes or they keep coming back.
//
// It stops anything of this user's that fits, so no other build may be running. What it
// cannot catch: a process run from an exempt path, one started earlier, and a job the
// agent scheduled to start later.
func StopStrayProcesses(ctx context.Context, since time.Time, logger *slog.Logger) ([]string, error) {
	if logger == nil {
		logger = slog.Default()
	}
	var stopped []string
	for round := 0; round < strayRounds; round++ {
		list, err := listProcesses(ctx)
		if err != nil {
			return stopped, err
		}
		stray := strayProcesses(list, since, os.Getuid(), os.Getpid())
		if len(stray) == 0 {
			return stopped, nil
		}
		for _, p := range stray {
			if err := syscall.Kill(p.pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return stopped, fmt.Errorf("stop process %d: %w", p.pid, err)
			}
			logger.Warn("stopped a process the agent left", "pid", p.pid, "path", p.path)
			stopped = append(stopped, fmt.Sprintf("%d %s", p.pid, p.path))
		}
		select {
		case <-ctx.Done():
			return stopped, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return stopped, errors.New("processes the agent left keep coming back: " + strings.Join(stopped[max(0, len(stopped)-5):], ", "))
}
