#!/bin/bash
# Run on the GitLab host as root, through ssh, as soon as the push is over: revokes the one
# token named by PUSH_TOKEN_ID and PUSH_TOKEN_NAME, and fails unless GitLab then holds it
# revoked and no push-window token is live. Any failure is a non-zero exit.
#
# GitLab has no revoke service for a project's token (the API's DELETE destroys the record,
# and the evidence with it), so this calls the model's revoke!, as GitLab's own settings page
# does. No audit event comes of that: gitlab_clock is GitLab's own clock.
set -euo pipefail
case "${PUSH_TOKEN_ID:-}" in
'' | *[!0-9]*)
	echo "give PUSH_TOKEN_ID" >&2
	exit 2
	;;
esac
case "${PUSH_TOKEN_NAME:-}" in
crn-push-window-*) ;;
*)
	echo "give PUSH_TOKEN_NAME" >&2
	exit 2
	;;
esac
PUSH_PROJECT="${PUSH_PROJECT:-fitt/ftcdv-uat}" PUSH_TOKEN_ID="$PUSH_TOKEN_ID" PUSH_TOKEN_NAME="$PUSH_TOKEN_NAME" gitlab-rails runner '
project = Project.find_by_full_path(ENV.fetch("PUSH_PROJECT")) or abort("no such project")
token = project.deploy_tokens.find_by(id: Integer(ENV.fetch("PUSH_TOKEN_ID"))) or abort("this project has no token with that id")
abort("token #{token.id} is named #{token.name}, not the one made") unless token.name == ENV.fetch("PUSH_TOKEN_NAME")
was_revoked = token.revoked
token.revoke! unless was_revoked
token.reload
abort("token #{token.id} is still not revoked") unless token.revoked
live = project.deploy_tokens.active.select { |t| t.name.start_with?("crn-push-window-") }
puts "CRNREVOKE id=#{token.id} name=#{token.name} revoked=#{token.revoked} already=#{was_revoked} login_allowed_now=#{project.deploy_tokens.active.where(id: token.id).exists?} live_push_window_tokens=#{live.size} gitlab_clock=#{Time.current.utc.iso8601}"
abort("push-window tokens are still live: #{live.map(&:id).join(",")}") unless live.empty?
' | grep -E '^CRNREVOKE '
