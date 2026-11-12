#!/bin/zsh
# Installs, as root on the build machine, the one login DV gave for one GitLab project, so the
# push helper can push that project's images. It makes no token.
#
#   ssh -t macagents@172.168.1.171 'sudo zsh ~/fitt-coderunner/deploy/macmini/push-helper-login.sh \
#       fitt/demos /Users/macagents/crn-secrets/incoming/<file> gitlab+deploy-token-1' | tee ~/Downloads/push-helper-login-$(date +%Y%m%d-%H%M%S).txt
#
# The file is what came through the secret channel: user:token on one line, or a docker
# config with one login for the registry. The login must be the user named (Fittcore said
# which token), GitLab must grant it pull on the project and no push outside it, and it is
# stored as the file the helper's config names for that project, owned by _crnpush, mode 600.
# Nothing here prints the token; the evidence is its user, sha256 sums and GitLab's answers.
# Asking GitLab for push on an image that does not exist makes that image, so it only ever asks
# for pull on the project and for push outside it.
set -eu
[ "$(id -u)" = 0 ] || { echo "run it with sudo" >&2; exit 1; }
project="${1:-}" source="${2:-}" expected="${3:-}"
registry="${CRN_PUSH_REGISTRY:-172.168.1.234:5050}"
gitlab="${CRN_GITLAB_URL:-http://172.168.1.234}"
outside="${AUTO_PUSH_OUTSIDE:-fitt/ftcdv-uat/crn-demo-ntc-film-job-efa0ff04}"
probe="${AUTO_PUSH_PROBE:-crn-demo-scope-check}"
crn_user="${CRN_USER:-macagents}"
stop() {
	echo "=== PUSH HELPER LOGIN: STOP, $*"
	exit 1
}
[ -n "$project" ] && [ -n "$source" ] && [ -n "$expected" ] || stop "give the project, the file DV sent and the user it must be"
target=$(/usr/bin/python3 -I -c 'import json,sys; print(json.load(open("/opt/crn-push/crn-push.json"))["logins"].get(sys.argv[1], ""))' "$project")
[ -n "$target" ] || stop "the helper's config has no login file for $project: nothing was stored"
[ -f "$source" ] && [ ! -L "$source" ] || stop "$source is not a regular file"

echo "--- push helper login $(date '+%F %T %z'), project $project"
echo "source: $source, sha256 $(shasum -a 256 "$source" | cut -d' ' -f1), $(stat -f '%z bytes' "$source")"
login=$(/usr/bin/python3 -I - "$source" "$registry" <<'PY'
import base64, json, sys
raw = open(sys.argv[1]).read().strip()
if raw.startswith("{"):
    auths = json.loads(raw).get("auths", {})
    if list(auths) != [sys.argv[2]]:
        sys.exit("the docker config does not hold one login for the registry")
    raw = base64.b64decode(auths[sys.argv[2]]["auth"]).decode()
user, sep, token = raw.partition(":")
if not sep or not user or not token or "\n" in raw or " " in user:
    sys.exit("the file is not user:token")
print(raw)
PY
) || stop "the file is not one login"
user="${login%%:*}"
echo "user: $user"
[ "$user" = "$expected" ] || { login=""; stop "the login is $user, not $expected: nothing was stored"; }

# What GitLab grants, by its own answer. The login goes in a curl config on stdin, never on a
# command line.
grants() {
	printf 'user = "%s"\n' "$login" | curl -sf -K - "$gitlab/jwt/auth?service=container_registry&scope=repository:$1:$2" |
		/usr/bin/python3 -I -c 'import json,sys,base64
try:
    t=json.load(sys.stdin)["token"].split(".")[1]; t+="="*(-len(t)%4)
    print(",".join(sorted({a for e in json.loads(base64.urlsafe_b64decode(t)).get("access",[]) for a in e.get("actions",[])})) or "none")
except Exception:
    print("unreadable")'
}
got=$(grants "$project/$probe" pull || echo "refused")
echo "GitLab grants $user on $project/$probe (pull asked): $got"
[ "$got" = pull ] || { login=""; stop "the login is not good for $project: nothing was stored"; }
got=$(grants "$outside" push,pull || echo "refused")
echo "GitLab grants $user on $outside (push asked): $got"
case "$got" in *push*) login=""; stop "the login can push $outside, outside $project: nothing was stored" ;; esac

json="{\"auths\":{\"$registry\":{\"auth\":\"$(printf '%s' "$login" | base64 | tr -d '\n')\"}}}"
login=""
want=$(printf '%s' "$json" | shasum -a 256 | cut -d' ' -f1)
umask 077
printf '%s' "$json" >"$target.new"
json=""
chown _crnpush:_crnpush "$target.new"
chmod 600 "$target.new"
mv -f "$target.new" "$target"
got=$(shasum -a 256 "$target" | cut -d' ' -f1)
echo "stored: $target ($(stat -f '%Su %Lp %z bytes' "$target")), sha256 $got"
[ "$got" = "$want" ] || stop "the stored login is not the one read"

echo "--- the helper reads it (an empty image, so nothing is pushed)"
answer=$(sudo -u "$crn_user" /usr/bin/sudo -n -u _crnpush /opt/crn-push/crn-push push "$registry/$project/$probe:v1" </dev/null 2>&1 || true)
echo "crn-push: $answer"
case "$answer" in
*"no readable index.json"*) echo "the helper has a login for $project it can read: OK" ;;
*) stop "the helper did not get as far as the image" ;;
esac
echo "the file DV sent is still at $source; remove it when this is reported: rm -P '$source'"
echo "=== PUSH HELPER LOGIN: PASS ($project, $user)"
