package buildstep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	digestA = "sha256:714a220cd8777936c0e67865c20a9340f2494de2d4fc732332e366e3e0448682"
	digestB = "sha256:0000000000000000000000000000000000000000000000000000000000000001"
)

func TestRemoteDigestReadsTheRegistrysManifestDigest(t *testing.T) {
	args := stubDocker(t, `echo '{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"`+digestA+`","size":481}'`)
	got, err := RemoteDigest(context.Background(), "reg.test/crn/app:v3", discardLogger())
	if err != nil || got != digestA {
		t.Fatalf("got %q, %v; want %s", got, err, digestA)
	}
	if ran := readFile(t, args); !strings.Contains(ran, "buildx imagetools inspect reg.test/crn/app:v3") {
		t.Errorf("ran: %s", ran)
	}
}

func TestRemoteDigestRefusesAnAnswerThatIsNotADigest(t *testing.T) {
	for _, out := range []string{`{"digest":"latest"}`, `not json`, `{}`} {
		stubDocker(t, "echo '"+out+"'")
		if _, err := RemoteDigest(context.Background(), "reg.test/crn/app:v3", discardLogger()); err == nil {
			t.Errorf("accepted %s", out)
		}
	}
}

// The copy goes registry to registry by digest, as a carbon copy: an index would
// get a new digest, and a rebuild or re-push could too.
func TestMirrorImageCopiesByDigestAndProvesTheCopyMatches(t *testing.T) {
	args := stubDocker(t, `case "$*" in *inspect*) echo '{"digest":"`+digestA+`"}';; esac`)
	got, err := MirrorImage(context.Background(), "reg.test/crn/app:v3", "asia-southeast1-docker.pkg.dev/p/crn/app:v3", discardLogger())
	if err != nil || got != digestA {
		t.Fatalf("got %q, %v", got, err)
	}
	ran := strings.Split(strings.TrimSpace(readFile(t, args)), "\n")
	want := []string{
		"buildx imagetools inspect reg.test/crn/app:v3 --format {{json .Manifest}}",
		"buildx imagetools create --prefer-index=false --tag asia-southeast1-docker.pkg.dev/p/crn/app:v3 reg.test/crn/app@" + digestA,
		"buildx imagetools inspect asia-southeast1-docker.pkg.dev/p/crn/app:v3 --format {{json .Manifest}}",
	}
	if strings.Join(ran, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran:\n%s\nwant:\n%s", strings.Join(ran, "\n"), strings.Join(want, "\n"))
	}
}

func TestMirrorImageFailsWhenTheCopyHasAnotherDigest(t *testing.T) {
	stubDocker(t, `case "$*" in *"inspect reg.test"*) echo '{"digest":"`+digestA+`"}';; *inspect*) echo '{"digest":"`+digestB+`"}';; esac`)
	_, err := MirrorImage(context.Background(), "reg.test/crn/app:v3", "mirror.test/crn/app:v3", discardLogger())
	if err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("err = %v, want a digest mismatch", err)
	}
}

func TestMirrorImageFailsWhenTheCopyFails(t *testing.T) {
	stubDocker(t, `case "$*" in *create*) echo "denied: permission" >&2; exit 1;; *) echo '{"digest":"`+digestA+`"}';; esac`)
	if _, err := MirrorImage(context.Background(), "reg.test/crn/app:v3", "mirror.test/crn/app:v3", discardLogger()); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("err = %v, want the registry's refusal", err)
	}
}

func TestMirrorTagKeepsTheImagesPathUnderTheMirror(t *testing.T) {
	for _, c := range []struct{ registry, mirror, tag, want string }{
		{"172.168.1.171:5000/fitt", "asia-southeast1-docker.pkg.dev/proj/crn", "172.168.1.171:5000/fitt/crn-demo-shop-cc0b6195:v3", "asia-southeast1-docker.pkg.dev/proj/crn/crn-demo-shop-cc0b6195:v3"},
		{"reg.test/fitt/", "mirror.test/x/", "reg.test/fitt/app:v1", "mirror.test/x/app:v1"},
	} {
		if got := MirrorTag(c.registry, c.mirror, c.tag); got != c.want {
			t.Errorf("MirrorTag(%q, %q, %q) = %q, want %q", c.registry, c.mirror, c.tag, got, c.want)
		}
	}
}

func TestRepositoryDropsTheTagButNotTheRegistryPort(t *testing.T) {
	for in, want := range map[string]string{
		"localhost:15001/crn/demo:v1":               "localhost:15001/crn/demo",
		"reg.test/crn/app":                          "reg.test/crn/app",
		"reg.test/crn/app@" + digestA:               "reg.test/crn/app",
		"asia-southeast1-docker.pkg.dev/p/r/app:v9": "asia-southeast1-docker.pkg.dev/p/r/app",
	} {
		if got := repository(in); got != want {
			t.Errorf("repository(%q) = %q, want %q", in, got, want)
		}
	}
}

// Opt-in: two throwaway local registries stand in for the delivery registry and
// Artifact Registry. Proves the mirrored manifest is byte-identical.
func TestMirrorImageAgainstRealRegistries(t *testing.T) {
	if os.Getenv("CRN_MIRROR_TEST") == "" {
		t.Skip("set CRN_MIRROR_TEST=1 to run against two throwaway local registries (needs Docker)")
	}
	ctx := context.Background()
	run := func(args ...string) string {
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	for i, port := range []string{"15011", "15012"} {
		name := fmt.Sprintf("crn-mirror-test-%d", i)
		exec.Command("docker", "rm", "-f", name).Run()
		run("run", "-d", "--rm", "--name", name, "-p", "127.0.0.1:"+port+":5000", "registry:2")
		t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.txt"), []byte(t.Name()), 0o644)
	os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\nCOPY x.txt /x.txt\n"), 0o644)
	src, dest := "localhost:15011/fitt/crn-demo-test:v1", "localhost:15012/gcp/crn-demo-test:v1"
	if err := BuildImage(ctx, dir, "Dockerfile", src, discardLogger()); err != nil {
		t.Fatal(err)
	}
	if err := PushImage(ctx, src, discardLogger()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exec.Command("docker", "image", "rm", "-f", src).Run() })
	digest, err := MirrorImage(ctx, src, dest, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", "http://127.0.0.1:15012/v2/gcp/crn-demo-test/manifests/v1", nil)
	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	sum := sha256.Sum256(raw)
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != digest {
		t.Fatalf("mirrored manifest hashes to %s, want %s", got, digest)
	}
}
