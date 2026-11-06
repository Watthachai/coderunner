#!/bin/bash
# Run on the GitLab host as root, through ssh, at the pause of a listed project's build: makes
# one new deploy token for this one job, through GitLab's own service, so GitLab records it.
# It holds the two scopes GitLab needs for a registry push (read_registry and write_registry:
# "You need both read and write access to push images") and no other. It refuses if a
# push-window token is still live. PUSH_TOKEN_MINUTES is at most 30.
#
# Prints two lines and nothing else, for push-window-run.sh to read. CRNMETA goes in the
# evidence; CRNTOKEN goes to the waiting build and nowhere else: do not run this where the
# screen shows it. Any failure is a non-zero exit.
#
# expires_at is an ISO 8601 string because GitLab validates the value as given, and refuses a
# Time, whose to_s is not ISO 8601 (app/validators/iso8601_date_validator.rb).
set -euo pipefail
PUSH_PROJECT="${PUSH_PROJECT:-fitt/ftcdv-uat}" PUSH_AUTHOR="${PUSH_AUTHOR:-root}" PUSH_TOKEN_MINUTES="${PUSH_TOKEN_MINUTES:-30}" gitlab-rails runner '
project = Project.find_by_full_path(ENV.fetch("PUSH_PROJECT")) or abort("no such project")
author = User.find_by_username(ENV.fetch("PUSH_AUTHOR")) or abort("no such user")
minutes = Integer(ENV.fetch("PUSH_TOKEN_MINUTES"))
abort("a push-window token lives 1 to 30 minutes") unless minutes.between?(1, 30)
live = -> { project.deploy_tokens.active.select { |t| t.name.start_with?("crn-push-window-") } }
before = live.call
abort("refused: push-window tokens are still live: #{before.map(&:id).join(",")}") unless before.empty?
result = Projects::DeployTokens::CreateService.new(project, author, {
  name: "crn-push-window-#{Time.now.to_i}",
  read_registry: true,
  write_registry: true,
  expires_at: minutes.minutes.from_now.utc.iso8601
}).execute
abort("GitLab did not make the token: #{result[:message]}") unless result[:status] == :success
token = result[:deploy_token]
unless live.call.map(&:id) == [token.id] && token.scopes == [:read_registry, :write_registry]
  token.revoke!
  abort("not exactly one live token with the two registry scopes: #{token.id} was revoked")
end
puts "CRNMETA id=#{token.id} name=#{token.name} username=#{token.username} created_at=#{token.created_at.utc.iso8601} expires_at=#{token.expires_at.utc.iso8601} expires_epoch=#{token.expires_at.to_i} gitlab_clock=#{Time.current.utc.iso8601} scopes=#{token.scopes.join(",")}"
puts "CRNTOKEN #{token.username}:#{token.token}"
' | grep -E '^CRN(META|TOKEN) '
