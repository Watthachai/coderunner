#!/bin/bash
# Run on the laptop, in Terminal, once push-helper-setup.sh has passed on the build machine:
# gives the push helper its login.
#
#   deploy/macmini/push-helper-login.sh
#
# It asks for the build machine's login password (for sudo there) and then GitLab's root
# password. GitLab makes one deploy token, crn-auto-push, for AUTO_PUSH_PROJECT
# (deploy/registry/auto-push-token-create.sh); the login goes from that answer straight to
# /var/crnpush/auth.json on the build machine, owned by _crnpush, mode 600. It is never
# shown, and it is checked by its sha256 on both sides. The evidence, in ~/Downloads, holds
# GitLab's id and dates and no token value.
set -euo pipefail
mini="${CRN_MINI:-macagents@172.168.1.171}"
gitlab="${CRN_GITLAB_HOST:-root@172.168.1.234}"
registry="${CRN_PUSH_REGISTRY:-172.168.1.234:5050}"
project="${AUTO_PUSH_PROJECT:-fitt/ftcdv-uat}"
inside="${AUTO_PUSH_INSIDE:-fitt/ftcdv-uat/crn-demo-ntc-film-job-efa0ff04}"
outside="${AUTO_PUSH_OUTSIDE:-fitt/demos/crn-demo-scope-check}"
here="$(cd "$(dirname "$0")/../.." && pwd)"
evidence="$HOME/Downloads/push-helper-login-$(date +%Y%m%d-%H%M%S).txt"
say() { echo "$*" | tee -a "$evidence"; }
stop() {
	say "=== PUSH HELPER LOGIN: STOP, $*"
	exit 1
}
pw="" login="" json=""
trap 'pw="" login="" json=""' EXIT

say "--- push helper login $(date '+%F %T %z'), project $project"
ssh "$mini" 'test -x /opt/crn-push/crn-push && test -f /opt/crn-push/crn-push.json && test -e /etc/sudoers.d/crn-push' ||
	stop "the build machine has no push helper: run push-helper-setup.sh there first; nothing was made"

read -rsp "Password of $mini (for sudo there): " pw
echo
printf '%s\n' "$pw" | ssh "$mini" 'sudo -S -p "" -v' 2>/dev/null || stop "sudo on the build machine refused that password; nothing was made"
say "sudo on the build machine: OK"

say "--- GitLab makes the token (it asks for its root password)"
answer=$(AUTO_PUSH_PROJECT="$project" ssh "$gitlab" "AUTO_PUSH_PROJECT=$project bash -s" <"$here/deploy/registry/auto-push-token-create.sh") ||
	stop "GitLab did not make a token"
meta=$(printf '%s\n' "$answer" | sed -n 's/^CRNMETA //p')
login=$(printf '%s\n' "$answer" | sed -n 's/^CRNTOKEN //p')
answer=""
[ -n "$meta" ] && [ -n "$login" ] || stop "GitLab's answer does not hold one token"
say "token: $meta"

json="{\"auths\":{\"$registry\":{\"auth\":\"$(printf '%s' "$login" | base64 | tr -d '\n')\"}}}"
want=$(printf '%s' "$json" | shasum -a 256 | cut -d' ' -f1)
got=$({ printf '%s\n' "$pw"; printf '%s' "$json"; } | ssh "$mini" 'sudo -S -p "" /bin/sh -c "umask 077; t=/var/crnpush/auth.json.new; cat > \$t && chown _crnpush:_crnpush \$t && chmod 600 \$t && mv -f \$t /var/crnpush/auth.json && shasum -a 256 /var/crnpush/auth.json"' | cut -d' ' -f1) ||
	stop "the login could not be stored: revoke token $(printf '%s' "$meta" | sed -n 's/^id=\([0-9]*\).*/\1/p') on GitLab"
json="" pw=""
say "login sha256 sent $want, stored $got"
[ "$got" = "$want" ] || stop "the stored login is not the one sent: revoke the token"

say "--- what the token may do, by GitLab's own answer (no image is sent)"
can_push() {
	local body
	body=$(printf 'user = "%s"\n' "$login" | curl -sf -K - "http://${registry%%:*}/jwt/auth?service=container_registry&scope=repository:$1:push,pull") || return 2
	printf '%s' "$body" | python3 -I -c 'import json,sys,base64
try:
    t=json.load(sys.stdin)["token"].split(".")[1]; t+="="*(-len(t)%4)
    acts=[a for e in json.loads(base64.urlsafe_b64decode(t)).get("access",[]) for a in e.get("actions",[])]
except Exception:
    sys.exit(2)
sys.exit(0 if "push" in acts else 1)'
}
rc=0
can_push "$inside" || rc=$?
[ "$rc" = 0 ] && say "$inside: push OK" || stop "the token cannot push $inside (answer $rc): revoke it"
rc=0
can_push "$outside" || rc=$?
case "$rc" in
1) say "$outside: no push OK" ;;
0) stop "the token can push $outside, outside $project: revoke it" ;;
*) stop "GitLab gave no usable answer about $outside: revoke the token" ;;
esac
login=""

say "--- the helper reads its login (an empty image, so nothing is pushed)"
answer=$(ssh "$mini" "/usr/bin/sudo -n -u _crnpush /opt/crn-push/crn-push push $registry/$inside:v1 </dev/null" 2>&1 || true)
say "crn-push: $answer"
case "$answer" in
*"no readable index.json"*) say "the helper has a login it can read: OK" ;;
*) stop "the helper did not get as far as the image" ;;
esac
say "=== PUSH HELPER LOGIN: PASS"
echo "evidence: $evidence"
