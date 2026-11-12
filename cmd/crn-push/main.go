// crn-push is the push helper (internal/registrypush). CRN runs it through sudo, as the user
// that alone can read the registry login:
//
//	docker save <ref> | sudo -n -u _crnpush /opt/crn-push/crn-push push <ref>
//
// It prints "pushed <ref> <digest> <login>" and exits 0, or says why not and exits non-zero. Its
// config is /opt/crn-push/crn-push.json, which only root can change.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Watthachai/fitt-coderunner/internal/registrypush"
)

const configFile = "/opt/crn-push/crn-push.json"

func main() {
	if len(os.Args) != 3 || os.Args[1] != "push" {
		fmt.Fprintln(os.Stderr, "usage: docker save <ref> | crn-push push <ref>")
		os.Exit(2)
	}
	if os.Geteuid() == 0 {
		fmt.Fprintln(os.Stderr, "crn-push: run it as its own user, not root")
		os.Exit(2)
	}
	cfg, err := registrypush.LoadConfig(configFile, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "crn-push:", err)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	ref := os.Args[2]
	res, err := registrypush.Push(ctx, cfg, ref, os.Stdin, registrypush.Deps{AgentSessions: registrypush.AgentSessions, Caller: os.Getenv("SUDO_USER")})
	if err != nil {
		fmt.Fprintln(os.Stderr, "crn-push:", err)
		os.Exit(1)
	}
	fmt.Printf("pushed %s %s %s\n", ref, res.Digest, res.Login)
}
