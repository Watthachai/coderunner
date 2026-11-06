#!/bin/bash
# The operator's one command for a listed build's push window. Run it from the laptop, in
# Terminal. It asks for the root password of the GitLab host once.
#
#   deploy/macmini/push-window-run.sh --baseline               before GO: record the machine
#   deploy/macmini/push-window-run.sh <project id> <job id>    at the pause
#   deploy/macmini/push-window-run.sh --rehearse               before GO: nothing is pushed
#
# At the pause it checks that this job waits and that the machine is as it was, asks, makes one
# token, sees it can write this project's images and nothing else, pipes it straight to the
# waiting build, waits until CRN says the push is over, revokes that token by its id, and
# checks it is dead. A token it made is always revoked when it ends, however it ends.
#
# --rehearse does that twice with no build and no write. The first token shows that revoking a
# live token kills it. The second, left alone, shows that one dies at its expires_at.
#
# The last line says PASS only if every step held; anything else is STOP and a non-zero exit.
# The evidence, in ~/Downloads, holds GitLab's ids and times and no token value.
set -euo pipefail
mini="${CRN_MINI:-macagents@172.168.1.171}"
gitlab="${CRN_GITLAB_HOST:-root@172.168.1.234}"
remote_script="${CRN_REMOTE_SCRIPT:-fitt-coderunner/deploy/macmini/push-window.sh}"
here="$(cd "$(dirname "$0")/../.." && pwd)"
scripts="$here/deploy/registry"
record="${CRN_WINDOW_RECORD:-$HOME/.crn-push-window}/baseline.sha256"
wait_rounds="${CRN_PUSH_WAIT_ROUNDS:-180}" # 15 minutes, Fittcore's limit on a live token
wait_seconds="${CRN_PUSH_WAIT_SECONDS:-5}"
expiry_minutes="${CRN_REHEARSE_EXPIRY_MINUTES:-2}"
mkdir -p "$HOME/Downloads"
evidence="$HOME/Downloads/push-window-evidence-$(date +%Y%m%d-%H%M%S).txt"
control_dir="$(mktemp -d)"
control="$control_dir/gitlab"

marker=""
completed=no
problems=0
token_open=no
token_id=""
token_name=""
expires_epoch=""
login=""

on_gitlab() { ssh -o ControlMaster=auto -o ControlPath="$control" -o ControlPersist=30m "$gitlab" "$@"; }
say() { echo "$*" | tee -a "$evidence"; }
problem() {
	say "PROBLEM: $*"
	problems=$((problems + 1))
}
stop() {
	problem "$*"
	exit 1
}

make_token() {
	local answer meta
	token_id="" token_name="" login="" expires_epoch=""
	token_open=yes
	say "--- create one token, $1 minutes"
	answer=$(on_gitlab "PUSH_TOKEN_MINUTES=$1 bash -s" <"$scripts/push-token-create.sh" 2>>"$evidence") || stop "GitLab did not make a token"
	meta=$(printf '%s\n' "$answer" | sed -n 's/^CRNMETA //p')
	login=$(printf '%s\n' "$answer" | sed -n 's/^CRNTOKEN //p')
	answer=""
	token_id=$(printf '%s\n' "$meta" | sed -n 's/^id=\([0-9][0-9]*\) name=crn-push-window-[0-9][0-9]* .*/\1/p')
	token_name=$(printf '%s\n' "$meta" | sed -n 's/^id=[0-9][0-9]* name=\(crn-push-window-[0-9][0-9]*\) .*/\1/p')
	expires_epoch=$(printf '%s\n' "$meta" | sed -n 's/.* expires_epoch=\([0-9][0-9]*\) .*/\1/p')
	if [ -z "$token_id" ] || [ -z "$token_name" ] || [ -z "$expires_epoch" ]; then
		stop "GitLab's answer does not say which token it made"
	fi
	say "token: $meta"
	case "$login" in
	*:?*) ;;
	*) stop "GitLab's answer holds no login" ;;
	esac
	if [ "$(printf '%s\n' "$login" | wc -l | tr -d ' ')" != 1 ]; then
		stop "GitLab's answer holds more than one login"
	fi
}

show_token() {
	local answer
	if answer=$(on_gitlab "PUSH_TOKEN_ID=$token_id bash -s" <"$scripts/push-token-show.sh" 2>>"$evidence"); then
		say "$answer"
	else
		problem "GitLab would not show token $token_id"
		return 1
	fi
}

token_scope() {
	say "--- what token $token_id may write"
	printf '%s\n' "$login" | ssh "$mini" "$remote_script scope" 2>&1 | tee -a "$evidence"
}

token_dead() {
	say "--- is token $token_id dead"
	printf '%s\n' "$login" | ssh "$mini" "$remote_script dead" 2>&1 | tee -a "$evidence"
}

# The one way a token ends: by its id, then GitLab's record, then a login that no longer works.
close_token() {
	local answer
	[ "$token_open" = yes ] || return 0
	token_open=no
	if [ -z "$token_id" ]; then
		problem "a token may exist and its id never came back: emergency revoke, which is not a normal window"
		if answer=$(on_gitlab 'bash -s' <"$scripts/push-token-revoke-unknown.sh" 2>>"$evidence"); then
			say "$answer"
		else
			problem "EMERGENCY REVOKE FAILED: revoke by hand now"
		fi
		return 0
	fi
	say "--- revoke token $token_id"
	if answer=$(on_gitlab "PUSH_TOKEN_ID=$token_id PUSH_TOKEN_NAME=$token_name bash -s" <"$scripts/push-token-revoke.sh" 2>>"$evidence"); then
		say "$answer"
	else
		problem "REVOKE FAILED for token $token_id: revoke it by hand now"
	fi
	show_token || true
	token_dead || problem "token $token_id may still be able to push"
	login=""
}

finish() {
	local code=$? rc=1
	trap - EXIT INT TERM
	set +e
	close_token
	ssh -O exit -o ControlPath="$control" "$gitlab" 2>/dev/null
	rm -rf "$control_dir"
	if [ "$code" = 0 ] && [ "$problems" = 0 ] && [ "$completed" = yes ]; then
		say "=== $marker: PASS $(date '+%F %T %z')"
		rc=0
	else
		say "=== STOP $(date '+%F %T %z'): this did not pass. Both sides stop until they have talked."
	fi
	echo "evidence: $evidence"
	exit "$rc"
}
trap finish EXIT
trap 'exit 130' INT TERM

case "${1:-}" in
--baseline)
	marker="BASELINE RECORDED"
	answer=$(ssh "$mini" "$remote_script baseline" 2>&1) || stop "no baseline: $answer"
	say "$answer"
	hash=$(printf '%s\n' "$answer" | sed -n 's/.*sha256 \([0-9a-f]\{64\}\)$/\1/p')
	[ -n "$hash" ] || stop "the MacMini did not say the baseline's sha256"
	mkdir -p "$(dirname "$record")"
	printf '%s\n' "$hash" >"$record"
	completed=yes
	;;
--rehearse)
	marker="REHEARSAL"
	say "--- rehearsal $(date '+%F %T %z'): no build, nothing is pushed"
	on_gitlab true || stop "no connection to GitLab"

	say "--- part 1: a live token is revoked by its id, and dies"
	make_token 30
	token_scope || stop "token $token_id can write the wrong things"
	show_token || stop "no record of token $token_id"
	close_token
	[ "$problems" = 0 ] || exit 1

	say "--- part 2: a token left alone dies at its expires_at"
	make_token "$expiry_minutes"
	token_scope || stop "token $token_id can write the wrong things"
	show_token || stop "no record of token $token_id"
	now=$(on_gitlab date +%s) || stop "GitLab's clock could not be read"
	left=$((expires_epoch + 15 - now))
	if [ "$left" -gt 0 ]; then
		say "--- not revoking it: waiting ${left}s, past its expires_at by GitLab's clock"
		sleep "$left"
	fi
	show_token || stop "no record of token $token_id"
	token_dead || stop "token $token_id outlived its expires_at"
	say "--- token $token_id died by itself at its expires_at"
	close_token
	[ "$problems" = 0 ] || exit 1
	completed=yes
	;;
????????-????-????-????-????????????)
	project="$1"
	job="${2:-}"
	case "$job" in
	????????-????-????-????-????????????) ;;
	*) stop "give the job's id as well" ;;
	esac
	marker="PUSH WINDOW for job $job"
	[ -s "$record" ] || stop "no baseline was recorded: nothing was made"
	hash=$(cat "$record")
	say "--- the machine, compared with the baseline $(date '+%F %T %z')"
	if ! ssh "$mini" "CRN_BASELINE_SHA256=$hash $remote_script check $project $job" 2>&1 | tee -a "$evidence"; then
		stop "the check failed: nothing was made"
	fi
	printf 'Make the token and hand it over? Type yes: '
	read -r answer || answer=""
	[ "$answer" = yes ] || stop "stopped by the operator: nothing was made"
	on_gitlab true || stop "no connection to GitLab: nothing was made"
	make_token 30
	token_scope || stop "token $token_id can write the wrong things: nothing was handed over"
	say "--- hand token $token_id to job $job"
	if ! printf '%s\n' "$login" | ssh "$mini" "$remote_script receive $project $job" 2>&1 | tee -a "$evidence"; then
		stop "the build did not take the token"
	fi
	show_token || stop "no record of token $token_id"
	say "--- waiting for the push, at most $((wait_rounds * wait_seconds))s"
	status=""
	for _ in $(seq 1 "$wait_rounds"); do
		status=$(ssh "$mini" "$remote_script status $project" 2>/dev/null) || status=""
		[ "$status" = PUSHED ] && break
		sleep "$wait_seconds"
	done
	[ "$status" = PUSHED ] || stop "CRN did not say the push is over, within the limit on a live token"
	say "--- CRN says the push is over $(date '+%F %T %z')"
	completed=yes
	;;
*)
	sed -n 2,19p "$0" >&2
	exit 2
	;;
esac
