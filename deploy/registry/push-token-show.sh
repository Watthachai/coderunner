#!/bin/bash
# Run on the GitLab host as root, through ssh. Prints one push-window token, named by
# PUSH_TOKEN_ID, as GitLab's own API entity shows it, plus what the evidence needs. It holds no
# token value and changes nothing. Any failure is a non-zero exit.
#
# login_allowed_now is the answer. The entity's "expired" compares with today's date, so it
# stays false until the next day; a login is refused from expires_at itself, by the scope
# DeployToken.active (expires_at >= NOW()) in Gitlab::Auth#deploy_token_check.
# registry_jwt_minutes is how long a registry token GitLab already issued stays good: revoking
# the deploy token does not recall one.
set -euo pipefail
case "${PUSH_TOKEN_ID:-}" in
'' | *[!0-9]*)
	echo "give PUSH_TOKEN_ID" >&2
	exit 2
	;;
esac
PUSH_PROJECT="${PUSH_PROJECT:-fitt/ftcdv-uat}" PUSH_TOKEN_ID="$PUSH_TOKEN_ID" gitlab-rails runner '
project = Project.find_by_full_path(ENV.fetch("PUSH_PROJECT")) or abort("no such project")
token = project.deploy_tokens.find_by(id: Integer(ENV.fetch("PUSH_TOKEN_ID"))) or abort("this project has no token with that id")
live = project.deploy_tokens.active.select { |t| t.name.start_with?("crn-push-window-") }
audit = begin
  AuditEvents::ProjectAuditEvent.where(project_id: project.id, target_id: token.id).order(:id).map do |e|
    { id: e.id, created_at: e.created_at.utc.iso8601, name: e.details[:event_name], author: e.details[:author_name] }
  end
rescue StandardError => e
  "unavailable: #{e.class}"
end
puts "CRNSHOW " + API::Entities::DeployToken.new(token).as_json.merge(
  created_at: token.created_at.utc.iso8601,
  gitlab_clock: Time.current.utc.iso8601,
  login_allowed_now: project.deploy_tokens.active.where(id: token.id).exists?,
  live_push_window_tokens: live.map(&:id),
  registry_jwt_minutes: Gitlab::CurrentSettings.container_registry_token_expire_delay,
  audit: audit
).to_json
' | grep -E '^CRNSHOW '
