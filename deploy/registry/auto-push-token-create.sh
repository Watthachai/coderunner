#!/bin/bash
# Run on the GitLab host as root, through ssh, once: makes the push helper's deploy token for
# one GitLab project, through GitLab's own service, so GitLab records it. It holds the two
# scopes a registry push needs (read_registry, write_registry) and no other, and lives
# AUTO_PUSH_DAYS days (at most 366). It refuses if a crn-auto-push token of the project is
# still live: revoke that one first (push-token-revoke.sh with its id).
#
# Prints two lines and nothing else, for push-helper-login.sh to read. CRNMETA goes in the
# evidence; CRNTOKEN goes to the build machine's _crnpush and nowhere else: do not run this
# where the screen shows it.
set -euo pipefail
AUTO_PUSH_PROJECT="${AUTO_PUSH_PROJECT:-fitt/ftcdv-uat}" AUTO_PUSH_AUTHOR="${AUTO_PUSH_AUTHOR:-root}" AUTO_PUSH_DAYS="${AUTO_PUSH_DAYS:-365}" gitlab-rails runner '
project = Project.find_by_full_path(ENV.fetch("AUTO_PUSH_PROJECT")) or abort("no such project")
author = User.find_by_username(ENV.fetch("AUTO_PUSH_AUTHOR")) or abort("no such user")
days = Integer(ENV.fetch("AUTO_PUSH_DAYS"))
abort("a crn-auto-push token lives 1 to 366 days") unless days.between?(1, 366)
live = -> { project.deploy_tokens.active.select { |t| t.name == "crn-auto-push" } }
before = live.call
abort("refused: a crn-auto-push token is still live: #{before.map(&:id).join(",")}") unless before.empty?
result = Projects::DeployTokens::CreateService.new(project, author, {
  name: "crn-auto-push",
  read_registry: true,
  write_registry: true,
  expires_at: days.days.from_now.utc.iso8601
}).execute
abort("GitLab did not make the token: #{result[:message]}") unless result[:status] == :success
token = result[:deploy_token]
unless live.call.map(&:id) == [token.id] && token.scopes == [:read_registry, :write_registry]
  token.revoke!
  abort("not exactly one live crn-auto-push token with the two registry scopes: #{token.id} was revoked")
end
puts "CRNMETA id=#{token.id} name=#{token.name} project=#{project.full_path} username=#{token.username} created_at=#{token.created_at.utc.iso8601} expires_at=#{token.expires_at.utc.iso8601} scopes=#{token.scopes.join(",")}"
puts "CRNTOKEN #{token.username}:#{token.token}"
' | grep -E '^CRN(META|TOKEN) '
