#!/bin/bash
# Run on the GitLab host as root, through ssh. Prints every push-window token of the project
# as GitLab's own API entity shows it: id, name, username, expires_at, scopes, revoked,
# expired. That entity holds no token value. Changes nothing.
set -eu
PUSH_PROJECT="${PUSH_PROJECT:-fitt/ftcdv-uat}" gitlab-rails runner '
project = Project.find_by_full_path(ENV.fetch("PUSH_PROJECT")) or abort("no such project")
tokens = project.deploy_tokens.select { |t| t.name.start_with?("crn-push-window-") }
puts "#{Time.now.utc.iso8601} #{project.full_path} push-window tokens: #{tokens.size}"
tokens.each { |t| puts API::Entities::DeployToken.new(t).as_json.to_json }
' 2>/dev/null
