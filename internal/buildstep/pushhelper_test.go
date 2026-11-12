package buildstep

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const helperDigest = "sha256:" + "ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12"

// fakeHelperSetup puts a docker that saves "layout of <ref>" on PATH, and returns a helper
// that answers with answer and records what it got.
func fakeHelperSetup(t *testing.T, answer string, exit int) (helper []string, got string) {
	t.Helper()
	dir := t.TempDir()
	docker := "#!/bin/sh\n[ \"$1\" = save ] && printf 'layout of %s' \"$2\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(docker), 0o755); err != nil {
		t.Fatal(err)
	}
	got = filepath.Join(dir, "got")
	script := "#!/bin/sh\n{ echo \"args $*\"; cat; } > " + got + "\necho '" + answer + "'\necho 'helper said no' >&2\nexit " + string(rune('0'+exit)) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "helper"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return []string{filepath.Join(dir, "helper"), "--as", "pusher"}, got
}

func TestPushThroughHelperGivesTheHelperTheSavedImageAndTakesItsDigest(t *testing.T) {
	ref := "172.168.1.234:5050/fitt/ftcdv-uat/crn-demo-x-12345678:v5"
	helper, got := fakeHelperSetup(t, "pushed "+ref+" "+helperDigest+" gitlab+deploy-token-1", 0)
	digest, login, err := PushThroughHelper(context.Background(), helper, ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	if digest != helperDigest || login != "gitlab+deploy-token-1" {
		t.Errorf("digest %s login %s", digest, login)
	}
	seen, _ := os.ReadFile(got)
	if want := "args --as pusher push " + ref + "\nlayout of " + ref; string(seen) != want {
		t.Errorf("the helper got %q, want %q", seen, want)
	}
}

func TestPushThroughHelperBelievesOnlyAnAnswerAboutThisTag(t *testing.T) {
	ref := "172.168.1.234:5050/fitt/ftcdv-uat/crn-demo-x-12345678:v5"
	for name, answer := range map[string]string{
		"another tag":    "pushed 172.168.1.234:5050/fitt/ftcdv-uat/crn-demo-x-12345678:v6 " + helperDigest,
		"no digest":      "pushed " + ref,
		"not a digest":   "pushed " + ref + " sha256:short",
		"something else": "done",
	} {
		t.Run(name, func(t *testing.T) {
			helper, _ := fakeHelperSetup(t, answer, 0)
			if _, _, err := PushThroughHelper(context.Background(), helper, ref, nil); err == nil {
				t.Errorf("it took %q", answer)
			}
		})
	}
}

func TestPushThroughHelperSaysWhyTheHelperFailed(t *testing.T) {
	ref := "172.168.1.234:5050/fitt/ftcdv-uat/crn-demo-x-12345678:v5"
	helper, _ := fakeHelperSetup(t, "", 1)
	_, _, err := PushThroughHelper(context.Background(), helper, ref, nil)
	if err == nil || !strings.Contains(err.Error(), "helper said no") {
		t.Errorf("err = %v", err)
	}
}
