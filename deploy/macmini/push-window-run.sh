#!/bin/bash
# The operator's one command at a listed build's pause. Run it from the laptop, in Terminal.
# It asks for the root password of the GitLab host once.
#
#   deploy/macmini/push-window-run.sh <project id>            at the pause
#   deploy/macmini/push-window-run.sh --rehearse              before GO: nothing is pushed
#
# It shows what changed on the MacMini and asks before going on, makes the token and pipes it
# straight to the waiting build, waits until CRN says the push is over, revokes the token,
# and keeps the evidence, with no token value in it. A token it made is always revoked when
# it ends, however it ends. --rehearse makes a token, asks GitLab which images it may write
# without writing any, and revokes it.
set -eu
mini="${CRN_MINI:-macagents@172.168.1.171}"
gitlab="${CRN_GITLAB_HOST:-root@172.168.1.234}"
remote_script=fitt-coderunner/deploy/macmini/push-window.sh
here="$(cd "$(dirname "$0")/../.." && pwd)"
evidence="$HOME/Downloads/push-window-evidence-$(date +%Y%m%d-%H%M%S).txt"
control="$(mktemp -d)/gitlab"
made_token=no

on_gitlab() { ssh -o ControlMaster=auto -o ControlPath="$control" -o ControlPersist=30m "$gitlab" "$@"; }
say() { echo "$*" | tee -a "$evidence"; }
finish() {
	if [ "$made_token" = yes ]; then
		say "--- revoke $(date '+%F %T %z')"
		on_gitlab 'bash -s' <"$here/deploy/registry/push-token-revoke.sh" | tee -a "$evidence" || say "REVOKE FAILED: run it again by hand"
		on_gitlab 'bash -s' <"$here/deploy/registry/push-token-show.sh" | tee -a "$evidence" || true
	fi
	ssh -O exit -o ControlPath="$control" "$gitlab" 2>/dev/null || true
	echo "evidence: $evidence"
}
trap finish EXIT

case "${1:-}" in
--rehearse)
	say "--- rehearsal $(date '+%F %T %z'): nothing is pushed"
	on_gitlab true
	made_token=yes
	on_gitlab 'bash -s' <"$here/deploy/registry/push-token-create.sh" | ssh "$mini" "$remote_script scope" | tee -a "$evidence"
	on_gitlab 'bash -s' <"$here/deploy/registry/push-token-show.sh" | tee -a "$evidence"
	;;
????????-????-????-????-????????????)
	project="$1"
	say "--- the machine, compared with the baseline $(date '+%F %T %z')"
	ssh "$mini" "$remote_script check $project" | tee -a "$evidence"
	if [ "$(ssh "$mini" "$remote_script status $project")" != READY ]; then
		say "CRN is not waiting for a token. Nothing was made."
		exit 1
	fi
	printf 'Make the token and hand it over? Type yes: '
	read -r answer
	if [ "$answer" != yes ]; then
		say "stopped by the operator; nothing was made"
		exit 1
	fi
	on_gitlab true
	made_token=yes
	say "--- token $(date '+%F %T %z')"
	on_gitlab 'bash -s' <"$here/deploy/registry/push-token-create.sh" | ssh "$mini" "$remote_script receive $project" | tee -a "$evidence"
	on_gitlab 'bash -s' <"$here/deploy/registry/push-token-show.sh" | tee -a "$evidence"
	say "--- waiting for the push"
	for _ in $(seq 1 180); do
		[ "$(ssh "$mini" "$remote_script status $project")" = PUSHED ] && break
		sleep 5
	done
	say "--- $(ssh "$mini" "$remote_script status $project") $(date '+%F %T %z')"
	;;
*)
	sed -n 2,12p "$0" >&2
	exit 2
	;;
esac
