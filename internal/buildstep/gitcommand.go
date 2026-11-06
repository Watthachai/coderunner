package buildstep

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// gitConfig is what CRN sets on every git command. An agent runs as CRN's OS user, so every
// config file git would read is one it can write: the home directory's, the system's
// (under Homebrew, owned by that same user) and the repository's. gitEnv has git skip the
// first two; these settings override the third, and each is a way a config file could run
// a command or change what is committed.
//
// They cannot cover settings named by the file itself, such as filter.<name>.clean,
// url.<base>.insteadOf and include.path. Where those matter, CleanRepo does not read the
// workspace's config at all.
var gitConfig = []string{
	"core.hooksPath=/dev/null",
	"core.fsmonitor=false",
	"core.sshCommand=ssh",
	"core.askPass=",
	// git reads these two from the home directory even with its config file skipped.
	"core.attributesFile=/dev/null",
	"core.excludesFile=/dev/null",
	// The empty value drops every helper named before it, URL-scoped ones too. CRN's own
	// credentials are in the Keychain.
	"credential.helper=",
	"credential.helper=osxkeychain",
	// ext:: runs a command; a rewritten URL could name it.
	"protocol.allow=never",
	"protocol.https.allow=always",
	"protocol.http.allow=always",
	"protocol.ssh.allow=always",
	"protocol.file.allow=always",
	"commit.gpgsign=false",
	"push.gpgSign=false",
	"gc.auto=0",
	"maintenance.auto=false",
	"submodule.recurse=false",
	"fetch.recurseSubmodules=false",
	"push.recurseSubmodules=no",
}

// gitEnv is CRN's environment without anything addressed to git, plus what skips the config
// files in the home directory and the system.
func gitEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") || strings.HasPrefix(kv, "SSH_ASKPASS") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_ATTR_NOSYSTEM=1",
	)
}

// gitCommand is the one way CRN runs git.
func gitCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	full := make([]string, 0, 2*len(gitConfig)+len(args))
	for _, setting := range gitConfig {
		full = append(full, "-c", setting)
	}
	cmd := exec.CommandContext(ctx, "git", append(full, args...)...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	return cmd
}
