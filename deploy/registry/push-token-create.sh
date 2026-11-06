#!/bin/bash
# Run on the GitLab host as root, through ssh, at the pause of a listed project's build:
# makes a deploy token that can write the registry of one GitLab project and dies by itself
# in two hours. Prints user:token and nothing else, for push-window.sh to read: pipe it, do
# not run it where the screen shows it. Name the project with PUSH_PROJECT.
set -eu
PUSH_PROJECT="${PUSH_PROJECT:-fitt/ftcdv-uat}" gitlab-rails runner '
project = Project.find_by_full_path(ENV.fetch("PUSH_PROJECT")) or abort("no such project")
token = project.deploy_tokens.create!(
  name: "crn-push-window-#{Time.now.to_i}",
  read_registry: true, write_registry: true,
  expires_at: 2.hours.from_now,
  deploy_token_type: DeployToken.deploy_token_types[:project_type])
puts "CRNTOKEN #{token.username}:#{token.token}"
' 2>/dev/null | sed -n 's/^CRNTOKEN //p'
