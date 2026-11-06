#!/bin/bash
# The operator's side of a listed project's push window (internal/jobs/pushwindow.go). The
# token never goes through a screen, a clipboard or a command line. Each command exits 0 only
# when what it checks held.
#
#   push-window.sh baseline                before GO: record this machine, print its sha256
#   push-window.sh check <project> <job>   at the pause: that job waits, and nothing changed
#   push-window.sh status <project>        READY, PUSHED or NONE
#   push-window.sh receive <project> <job> token on stdin: leave it for that job
#   push-window.sh scope                   token on stdin: it writes this project's images only
#   push-window.sh dead                    token on stdin: it can no longer push
#   push-window.sh sessions                exit 0 only when no agent's session is open
#   push-window.sh tags                    every tag of the project, from GitLab, sorted
#
# deploy/macmini/push-window-run.sh runs these from the laptop.
set -eu
export PATH="${CRN_PUSH_WINDOW_PATH:-/opt/homebrew/bin:/Applications/Docker.app/Contents/Resources/bin:/usr/bin:/bin:/usr/sbin:/sbin}"
umask 077

export CRN_REGISTRY_HOST="${CRN_REGISTRY_HOST:-172.168.1.234:5050}"
export CRN_REGISTRY_URL="${CRN_REGISTRY_URL:-http://$CRN_REGISTRY_HOST}"
export CRN_GITLAB_URL="${CRN_GITLAB_URL:-http://172.168.1.234}"
export CRN_SCOPE_PROJECT="${CRN_SCOPE_PROJECT:-fitt/ftcdv-uat}"
export CRN_SCOPE_ALLOWED="${CRN_SCOPE_ALLOWED:-fitt/ftcdv-uat/crn-demo-ntc-film-job-efa0ff04 fitt/ftcdv-uat/crn-demo-hello-fitt-c0241b87 fitt/ftcdv-uat/crn-demo-atelier-minimal-earth-cafca94c fitt/ftcdv-uat/crn-demo-audit-planning-task-management-160001c3}"
export CRN_SCOPE_OUTSIDE="${CRN_SCOPE_OUTSIDE:-fitt/demos/crn-demo-scope-check}"
push_dir="${CRN_PUSH_CREDENTIAL_DIR:-$HOME/crn-secrets/push}"
baseline="$push_dir/baseline.txt"

# What the machine looks like. It leaves out what macOS and Docker Desktop change on their own,
# the same things the build leaves out (launchdIgnored and strayExempt in internal/buildstep),
# since a check that always fails gets ignored. What it cannot read it says so about.
state() {
	echo "## launch agents"
	ls -1 "$HOME/Library/LaunchAgents" 2>/dev/null || true
	echo "## launchd labels"
	labels=$(launchctl list 2>/dev/null) || echo "UNREADABLE launchd"
	printf '%s\n' "${labels:-}" | awk 'NR>1 {print $3}' | sed -E 's/\.[0-9]+$//' | { grep -vE '^(com\.apple\.|application\.)' || true; } | sort -u
	echo "## cron"
	crontab -l 2>/dev/null || true
	echo "## containers"
	containers=$(docker ps -a --format '{{.ID}} {{.Image}} {{.Names}}' </dev/null 2>/dev/null) || echo "UNREADABLE docker"
	printf '%s\n' "${containers:-}" | sort
	echo "## orphans of this user"
	orphans=$(ps -axo ppid=,uid=,comm=) || echo "UNREADABLE processes"
	printf '%s\n' "${orphans:-}" | awk -v uid="$(id -u)" '$1==1 && $2==uid {sub(/^ *[0-9]+ +[0-9]+ +/,""); print}' |
		{ grep -vE '^(/System/|/usr/libexec/|/usr/sbin/|/sbin/|/Library/Apple/|/Applications/Docker\.app/)' || true; } | sort -u
}

# Any agent's session left on the machine could read what the build is about to hold.
agent_sessions() {
	pgrep -fl '(^|/)(claude|codex|gemini|aider|opencode|cursor-agent|goose|amp)( |$)|claude-code' | cut -c1-80 || true
}

sha_of() { shasum -a 256 <"$1" | cut -d' ' -f1; }

need_id() {
	case "${1:-}" in
	????????-????-????-????-????????????) ;;
	*)
		echo "give the $2's id" >&2
		exit 2
		;;
	esac
}

# A login on stdin, checked against GitLab and the registry. It writes nothing: it only asks
# GitLab what it would allow, and the registry to open an upload it must refuse. Prints no
# token. scope passes when the login gets exactly pull,push on every image of the project, the
# project holds no other image, and the registry refuses it everywhere else. dead passes when
# GitLab gives the login no push anywhere it had one.
registry_check() {
	python3 -I -c "$(
		cat <<'PY'
import base64, json, os, sys, urllib.error, urllib.parse, urllib.request

mode = sys.argv[1]
login = ""
if mode != "tags":
    login = sys.stdin.readline().rstrip("\n")
    user, _, token = login.partition(":")
    if not user or not token:
        print("no user:token on stdin", file=sys.stderr)
        sys.exit(1)
gitlab = os.environ["CRN_GITLAB_URL"]
registry = os.environ["CRN_REGISTRY_URL"]
project = os.environ["CRN_SCOPE_PROJECT"]
allowed = os.environ["CRN_SCOPE_ALLOWED"].split()
outside = os.environ["CRN_SCOPE_OUTSIDE"].split()
basic = "Basic " + base64.b64encode(login.encode()).decode()


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


opener = urllib.request.build_opener(NoRedirect)


def ask(address, headers=None, method="GET"):
    req = urllib.request.Request(address, headers=headers or {}, method=method)
    try:
        with opener.open(req, timeout=20) as res:
            return res.status, res.headers, res.read()
    except urllib.error.HTTPError as e:
        return e.code, e.headers, e.read()


images_of_project = gitlab + "/api/v4/projects/" + urllib.parse.quote(project, safe="") + "/registry/repositories"


def listed(address):
    """A whole GitLab list, page by page. A list that does not say it ended is an error."""
    seen, page = [], 1
    while True:
        status, headers, body = ask(address + "?per_page=100&page=" + str(page))
        following = headers.get("X-Next-Page")
        if status != 200 or following is None:
            raise ValueError("a list from GitLab")
        seen += json.loads(body)
        if following == "":
            return seen
        if following != str(page + 1):
            raise ValueError("a list from GitLab")
        page += 1


def repositories():
    return [r["path"] for r in listed(images_of_project)]


def tag_lines():
    lines = []
    for image in listed(images_of_project):
        names = [t["name"] for t in listed(images_of_project + "/" + str(image["id"]) + "/tags")]
        if not names:
            lines.append(image["path"] + " (no tags)")
        for name in names:
            status, _, body = ask(images_of_project + "/" + str(image["id"]) + "/tags/" + urllib.parse.quote(name, safe=""))
            if status != 200 or not json.loads(body).get("digest"):
                raise ValueError("a tag from GitLab")
            lines.append(image["path"] + " " + name + " " + json.loads(body)["digest"])
    if len(lines) != len(set(lines)):
        raise ValueError("GitLab lists something twice")
    return sorted(lines)


def actions_on(repo):
    """refused and no token, or what GitLab grants on this image and the registry token."""
    scope = urllib.parse.quote("repository:" + repo + ":push,pull", safe="")
    status, _, body = ask(gitlab + "/jwt/auth?service=container_registry&scope=" + scope, {"Authorization": basic})
    if status in (401, 403):
        return None, None
    if status != 200:
        raise ValueError("GitLab answered " + str(status))
    jwt = json.loads(body)["token"]
    part = jwt.split(".")[1]
    claims = json.loads(base64.urlsafe_b64decode(part + "=" * (-len(part) % 4)))
    granted = set()
    for entry in claims.get("access") or []:
        if entry.get("name") != repo and entry.get("actions"):
            raise ValueError("GitLab granted another image")
        granted |= set(entry.get("actions") or [])
    return sorted(granted), jwt


def main():
    problems = []
    if mode == "tags":
        print("\n".join(tag_lines()))
        return 0
    if mode == "scope":
        held = repositories()
        if sorted(held) != sorted(allowed):
            problems.append("the project's images are not the allowed ones")
            print("WRONG the project holds:", " ".join(sorted(held)))
        for repo in allowed:
            granted, _ = actions_on(repo)
            if granted == ["pull", "push"]:
                print(repo, "pull,push OK")
            else:
                problems.append(repo)
                print(repo, "WRONG:", "refused" if granted is None else ",".join(granted) or "no access")
        for repo in outside:
            granted, jwt = actions_on(repo)
            if granted is not None and "push" in granted:
                problems.append(repo)
                print(repo, "WRONG: GitLab grants push outside the project")
                continue
            headers = {"Authorization": "Bearer " + jwt} if jwt else {}
            status, answer, _ = ask(registry + "/v2/" + repo + "/blobs/uploads/", headers, "POST")
            if status in (401, 403):
                print(repo, "the registry refuses a push:", status, "OK")
            else:
                problems.append(repo)
                print(repo, "WRONG: the registry answered", status, "to opening a push")
                if status == 202 and answer.get("Location"):
                    ask(urllib.parse.urljoin(registry, answer["Location"]), headers, "DELETE")
    else:
        for repo in allowed:
            granted, _ = actions_on(repo)
            if granted is None or "push" not in granted:
                print(repo, "login refused OK" if granted is None else "no push OK")
            else:
                problems.append(repo)
                print(repo, "WRONG: it can still push")
    label = "SCOPE" if mode == "scope" else "DEAD"
    if problems:
        print(label, "FAILED:", len(problems), "wrong")
        return 1
    print(label, "PASSED")
    return 0


try:
    sys.exit(main())
except (OSError, ValueError, KeyError, IndexError, TypeError) as e:
    print({"scope": "SCOPE", "dead": "DEAD", "tags": "TAGS"}[mode], "FAILED: no usable answer:", type(e).__name__)
    sys.exit(1)
PY
	)" "$1"
}

case "${1:-}" in
baseline)
	mkdir -p "$push_dir"
	state >"$baseline.tmp"
	if grep -q '^UNREADABLE' "$baseline.tmp"; then
		rm -f "$baseline.tmp"
		echo "the machine could not be read: no baseline was saved" >&2
		exit 1
	fi
	mv "$baseline.tmp" "$baseline"
	echo "baseline saved: $(wc -l <"$baseline" | tr -d ' ') lines, $(date '+%F %T %z'), sha256 $(sha_of "$baseline")"
	;;
check)
	need_id "${2:-}" project
	need_id "${3:-}" job
	problems=0
	problem() {
		echo "PROBLEM: $*"
		problems=$((problems + 1))
	}
	ready="$push_dir/$2/READY"
	if [ -L "$ready" ] || [ ! -f "$ready" ]; then
		problem "CRN is not waiting for a token"
	elif [ "$(cat "$ready")" != "$3" ]; then
		problem "CRN is waiting for another job: $(cut -c1-36 "$ready")"
	else
		echo "CRN is waiting for a token: job $3"
	fi
	if [ -L "$baseline" ] || [ ! -f "$baseline" ]; then
		problem "no baseline to compare with"
	elif [ "${CRN_BASELINE_SHA256:-}" != "$(sha_of "$baseline")" ]; then
		problem "the baseline is not the one recorded on the laptop"
	else
		echo "--- new since the baseline (+) and gone (-)"
		changes=$(state | diff "$baseline" - | grep -E '^[<>]' | sed -e 's/^> /+ /' -e 's/^< /- /' || true)
		if [ -n "$changes" ]; then
			printf '%s\n' "$changes"
			problem "the machine is not as it was"
		else
			echo "nothing changed"
		fi
	fi
	echo "--- agent sessions on this machine"
	sessions=$(agent_sessions)
	if [ -n "$sessions" ]; then
		printf '%s\n' "$sessions"
		problem "an agent session is open"
	else
		echo "none"
	fi
	if [ "$problems" -gt 0 ]; then
		echo "CHECK FAILED: $problems problem(s)"
		exit 1
	fi
	echo "CHECK PASSED"
	;;
status)
	need_id "${2:-}" project
	if [ -f "$push_dir/$2/PUSHED" ]; then
		echo PUSHED
	elif [ -f "$push_dir/$2/READY" ]; then
		echo READY
	else
		echo NONE
	fi
	;;
receive)
	need_id "${2:-}" project
	need_id "${3:-}" job
	IFS= read -r login || true
	case "$login" in
	*:?*) ;;
	*)
		echo "no user:token on stdin; nothing was left" >&2
		exit 1
		;;
	esac
	dir="$push_dir/$2"
	if [ -L "$dir/READY" ] || [ ! -f "$dir/READY" ] || [ "$(cat "$dir/READY")" != "$3" ]; then
		echo "CRN is not waiting for a token for job $3: nothing was left. REVOKE that token." >&2
		exit 1
	fi
	auth=$(printf '%s' "$login" | base64 | tr -d '\n')
	tmp="$dir/config.json.tmp.$$"
	printf '{"auths":{"%s":{"auth":"%s"}}}\n' "$CRN_REGISTRY_HOST" "$auth" >"$tmp"
	chmod 600 "$tmp"
	mv "$tmp" "$dir/config.json"
	echo "left for job $3, login ${login%%:*}"
	;;
scope | dead | tags)
	registry_check "$1"
	;;
sessions)
	sessions=$(agent_sessions)
	if [ -n "$sessions" ]; then
		printf '%s\n' "$sessions"
		echo "SESSIONS FAILED: an agent session is open"
		exit 1
	fi
	echo "SESSIONS PASSED: none"
	;;
*)
	sed -n 2,15p "$0" >&2
	exit 2
	;;
esac
