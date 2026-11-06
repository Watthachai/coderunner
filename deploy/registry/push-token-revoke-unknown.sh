#!/bin/bash
# EMERGENCY ONLY, for when a token may have been made and its id never came back. Revokes every
# live push-window token of the project and says which. This is never evidence of a normal
# window: push-window-run.sh ends in STOP whenever it has to run this.
set -euo pipefail
PUSH_PROJECT="${PUSH_PROJECT:-fitt/ftcdv-uat}" gitlab-rails runner '
project = Project.find_by_full_path(ENV.fetch("PUSH_PROJECT")) or abort("no such project")
found = project.deploy_tokens.active.select { |t| t.name.start_with?("crn-push-window-") }
found.each(&:revoke!)
left = project.deploy_tokens.active.select { |t| t.name.start_with?("crn-push-window-") }
puts "CRNREVOKEALL EMERGENCY ids=#{found.map(&:id).join(",")} live_push_window_tokens=#{left.size} gitlab_clock=#{Time.current.utc.iso8601}"
abort("push-window tokens are still live: #{left.map(&:id).join(",")}") unless left.empty?
' | grep -E '^CRNREVOKEALL '
