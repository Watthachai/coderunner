package claude

import (
	"os"
	"slices"
	"strings"
)

// childEnvNames are the variables a build's Claude process may inherit: what
// the CLI, npm, Docker and the browser tests need to run. It extends the
// delivery gate's own list (verify-delivery.mjs isolatedEnvironment).
var childEnvNames = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL",
	"TMPDIR", "TMP", "TEMP", "TERM", "TZ", "LANG",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
	"http_proxy", "https_proxy", "no_proxy", "all_proxy",
	"NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "SSL_CERT_DIR",
	"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH",
	"PLAYWRIGHT_BROWSERS_PATH",
	"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME",
}

// childEnvPrefixes cover locale settings and the CLI's own configuration and
// credentials (CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY, ...).
var childEnvPrefixes = []string{"LC_", "CLAUDE_", "ANTHROPIC_"}

// childEnv filters environ down to what Claude needs. Claude runs every tool
// without asking and its prompt can carry end-user text, so the Runner's own
// variables — database URLs, the callback token, ssh-agent — must not reach it.
// The result is never nil: exec.Cmd reads a nil Env as "inherit everything".
func childEnv(environ []string) []string {
	out := []string{}
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if slices.Contains(childEnvNames, name) || hasAnyPrefix(name, childEnvPrefixes) {
			out = append(out, kv)
		}
	}
	return out
}

// claudeEnv is the environment every Claude process the Runner starts gets.
func claudeEnv() []string { return agentEnv(os.Environ()) }

// noBackgroundTasks takes run_in_background off the CLI's Agent and Bash tools (checked on
// 2.1.325). A headless session ends soon after the agent's turn does, and kills what still
// runs: OKR #6 (16 Nov) handed 14 of 16 items to two background agents, ended its turn to
// wait for them, and CRN shipped the other two. Without background tasks every sub-agent
// and command returns its result before the agent can go on.
const noBackgroundTasks = "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS"

// agentEnv is childEnv with background tasks off, whatever the Runner's environment says.
func agentEnv(environ []string) []string {
	out := []string{}
	for _, kv := range childEnv(environ) {
		if name, _, _ := strings.Cut(kv, "="); name != noBackgroundTasks {
			out = append(out, kv)
		}
	}
	return append(out, noBackgroundTasks+"=1")
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
