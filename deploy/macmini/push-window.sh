#!/bin/bash
# The operator's side of a listed project's push window (internal/jobs/pushwindow.go). The
# token never goes through a screen, a clipboard or a command line.
#
#   push-window.sh baseline             before GO: what this machine looks like
#   push-window.sh check <project id>   at the pause: what is new since the baseline
#   push-window.sh receive <project id> token on stdin: leave it for the waiting build
#   push-window.sh scope                token on stdin: which repositories it may write
#
# From the laptop, at the pause, with the project's id:
#   ssh root@172.168.1.234 'bash -s' < deploy/registry/push-token-create.sh | \
#     ssh macagents@172.168.1.171 fitt-coderunner/deploy/macmini/push-window.sh receive <id>
set -eu
export PATH="/opt/homebrew/bin:/Applications/Docker.app/Contents/Resources/bin:/usr/bin:/bin:/usr/sbin:/sbin"
umask 077

registry="${CRN_REGISTRY_HOST:-172.168.1.234:5050}"
gitlab="${CRN_GITLAB_URL:-http://172.168.1.234}"
push_dir="${CRN_PUSH_CREDENTIAL_DIR:-$HOME/crn-secrets/push}"
baseline="$push_dir/baseline.txt"

state() {
	echo "## launch agents"
	ls -1 "$HOME/Library/LaunchAgents" 2>/dev/null || true
	echo "## launchd labels"
	launchctl list 2>/dev/null | awk 'NR>1 {print $3}' | sed -E 's/\.[0-9]+$//' | sort -u
	echo "## cron"
	crontab -l 2>/dev/null || true
	echo "## containers"
	docker ps -a --format '{{.ID}} {{.Image}} {{.Names}}' </dev/null 2>/dev/null | sort || true
	echo "## orphans of this user"
	/bin/ps -axo ppid=,uid=,comm= | awk -v uid="$(id -u)" '$1==1 && $2==uid {sub(/^ *[0-9]+ +[0-9]+ +/,""); print}' | sort -u
}

need_project() {
	case "${1:-}" in
	????????-????-????-????-????????????) ;;
	*)
		echo "give the project's id" >&2
		exit 2
		;;
	esac
}

case "${1:-}" in
baseline)
	mkdir -p "$push_dir"
	state >"$baseline"
	echo "baseline saved: $(wc -l <"$baseline" | tr -d ' ') lines, $(date '+%F %T %z')"
	;;
check)
	need_project "${2:-}"
	if [ -f "$push_dir/$2/READY" ]; then
		echo "CRN is waiting for a token: job $(cat "$push_dir/$2/READY")"
	else
		echo "CRN is NOT waiting for a token"
	fi
	[ -f "$baseline" ] || {
		echo "no baseline to compare with" >&2
		exit 1
	}
	echo "--- new since the baseline (+) and gone (-)"
	state | diff "$baseline" - | grep -E '^[<>]' | sed -e 's/^> /+ /' -e 's/^< /- /' || echo "nothing changed"
	echo "--- other Claude sessions on this machine"
	pgrep -fl '(^|/)claude( |$)' | cut -c1-80 || echo "none"
	;;
receive)
	need_project "${2:-}"
	IFS= read -r login || true
	case "$login" in
	*:?*) ;;
	*)
		echo "no user:token on stdin; nothing was left" >&2
		exit 1
		;;
	esac
	dir="$push_dir/$2"
	if [ ! -f "$dir/READY" ]; then
		echo "CRN is not waiting for a token: nothing was left. REVOKE that token." >&2
		exit 1
	fi
	auth=$(printf '%s' "$login" | base64 | tr -d '\n')
	tmp="$dir/config.json.tmp.$$"
	printf '{"auths":{"%s":{"auth":"%s"}}}\n' "$registry" "$auth" >"$tmp"
	chmod 600 "$tmp"
	mv "$tmp" "$dir/config.json"
	echo "left for job $(cat "$dir/READY"), token sha256 $(printf '%s' "${login#*:}" | shasum -a 256 | cut -c1-12)"
	;;
scope)
	IFS= read -r login || true
	case "$login" in
	*:?*) ;;
	*)
		echo "no user:token on stdin" >&2
		exit 1
		;;
	esac
	# Only asks GitLab what it would allow. It writes nothing to the registry.
	for repo in ${CRN_SCOPE_REPOS:-fitt/ftcdv-uat/crn-demo-ntc-film-job-efa0ff04 fitt/ftcdv-uat/crn-demo-hello-fitt-c0241b87 fitt/ftcdv-uat/crn-demo-atelier-minimal-earth-cafca94c fitt/ftcdv-uat/crn-demo-audit-planning-task-management-160001c3 fitt/demos/crn-demo-scope-check}; do
		answer=$(printf 'user = "%s"\n' "$login" | curl -sS -m 15 -K - "$gitlab/jwt/auth?service=container_registry&scope=repository:$repo:push,pull" || true)
		printf '%s' "$answer" | python3 -c '
import base64, json, sys
repo = sys.argv[1]
try:
    token = json.load(sys.stdin)["token"]
    body = token.split(".")[1]
    claims = json.loads(base64.urlsafe_b64decode(body + "=" * (-len(body) % 4)))
    actions = [a for entry in claims.get("access", []) for a in entry.get("actions", [])]
    print(repo, ",".join(sorted(actions)) or "no access")
except Exception as e:
    print(repo, "refused")
' "$repo"
	done
	;;
*)
	sed -n 2,12p "$0" >&2
	exit 2
	;;
esac
