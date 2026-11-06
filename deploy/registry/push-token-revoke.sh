#!/bin/bash
# Run on the GitLab host as root, through ssh, as soon as CRN's trace says the push is over:
# revokes every live push-window token of the project, and says how many.
set -eu
PUSH_PROJECT="${PUSH_PROJECT:-fitt/ftcdv-uat}" gitlab-rails runner '
project = Project.find_by_full_path(ENV.fetch("PUSH_PROJECT")) or abort("no such project")
live = project.deploy_tokens.where(revoked: false).select { |t| t.name.start_with?("crn-push-window-") }
live.each { |t| t.update!(revoked: true) }
left = project.deploy_tokens.where(revoked: false).select { |t| t.name.start_with?("crn-push-window-") }.size
puts "revoked #{live.size}, live now #{left}"
' 2>/dev/null
