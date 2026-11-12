package buildstep

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
)

// PushThroughHelper hands the local image ref to CRN's push helper (cmd/crn-push) and returns
// the digest the registry gave it and the login the helper pushed with (a name, never a
// token). docker save writes the image, as this build's docker
// config allows; the helper reads it on stdin and pushes it with a login only it can read.
// helper is the command that runs it, such as sudo -n -u _crnpush /opt/crn-push/crn-push.
// The helper gets this process's environment without docker's: it is told nothing but the
// tag.
func PushThroughHelper(ctx context.Context, helper []string, ref string, logger *slog.Logger) (digest, login string, err error) {
	if logger == nil {
		logger = slog.Default()
	}
	if len(helper) == 0 {
		return "", "", errors.New("no push helper is set")
	}
	save := dockerCommand(ctx, "save", ref)
	push := exec.CommandContext(ctx, helper[0], append(append([]string{}, helper[1:]...), "push", ref)...)
	push.Env = unsteeredEnv()
	reader, writer := io.Pipe()
	save.Stdout = writer
	var saveErr, pushOut, pushErr bytes.Buffer
	save.Stderr = &saveErr
	push.Stdin = reader
	push.Stdout = &pushOut
	push.Stderr = &pushErr
	if err := push.Start(); err != nil {
		return "", "", fmt.Errorf("start the push helper: %w", err)
	}
	saveDone := make(chan error, 1)
	go func() {
		err := save.Run()
		_ = writer.CloseWithError(err)
		saveDone <- err
	}()
	helperErr := push.Wait()
	_ = reader.Close()
	errSave := <-saveDone
	recordOutput(ctx, "crn-push", append(pushOut.Bytes(), pushErr.Bytes()...))
	if helperErr != nil {
		logger.Error("the push helper failed", "ref", ref, "output", strings.TrimSpace(pushErr.String()))
		return "", "", fmt.Errorf("the push helper: %w\n%s", helperErr, tailLines(strings.TrimSpace(pushErr.String()), 10))
	}
	if errSave != nil {
		return "", "", fmt.Errorf("docker save %s: %w\n%s", ref, errSave, tailLines(strings.TrimSpace(saveErr.String()), 10))
	}
	fields := strings.Fields(strings.TrimSpace(pushOut.String()))
	if len(fields) != 4 || fields[0] != "pushed" || fields[1] != ref || !digestPattern.MatchString(fields[2]) {
		return "", "", fmt.Errorf("the push helper did not say it pushed %s: %q", ref, tailLines(pushOut.String(), 3))
	}
	logger.Info("the push helper pushed", "ref", ref, "digest", fields[2], "login", fields[3])
	return fields[2], fields[3], nil
}
