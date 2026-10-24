package buildstep

import (
	"context"
	"strings"
	"testing"
)

const digestA = "sha256:714a220cd8777936c0e67865c20a9340f2494de2d4fc732332e366e3e0448682"

// FTC DV pulls by digest only, so the released callback carries the digest the
// delivery registry gave the push. That registry speaks plain HTTP, which buildx
// imagetools refuses, so the digest comes from the local image's RepoDigests.
func TestPushedDigestReadsTheDeliveryRegistrysDigest(t *testing.T) {
	args := stubDocker(t, `echo '["other.test/x@sha256:0000000000000000000000000000000000000000000000000000000000000002","172.168.1.234:5050/fitt/demos/crn-demo-app-c0241b87@`+digestA+`"]'`)
	got, err := PushedDigest(context.Background(), "172.168.1.234:5050/fitt/demos/crn-demo-app-c0241b87:v2", discardLogger())
	if err != nil || got != digestA {
		t.Fatalf("got %q, %v; want %s", got, err, digestA)
	}
	want := "image inspect 172.168.1.234:5050/fitt/demos/crn-demo-app-c0241b87:v2 --format {{json .RepoDigests}}"
	if ran := strings.TrimSpace(readFile(t, args)); ran != want {
		t.Errorf("ran %q, want %q", ran, want)
	}
}

func TestPushedDigestFailsWhenTheImageWasNeverPushed(t *testing.T) {
	for _, out := range []string{`[]`, `["other.test/x@` + digestA + `"]`, `["172.168.1.234:5050/fitt/demos/app@latest"]`, `not json`} {
		stubDocker(t, "echo '"+out+"'")
		if got, err := PushedDigest(context.Background(), "172.168.1.234:5050/fitt/demos/app:v2", discardLogger()); err == nil {
			t.Errorf("accepted %s as %q", out, got)
		}
	}
}
