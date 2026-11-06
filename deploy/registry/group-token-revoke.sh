#!/bin/bash
# Run on the GitLab host as root, through ssh, before GO: revokes the group deploy token
# crn-push and prints what is left, as GitLab's API entity shows it. Until a new one is made,
# no project can push an image to this registry from CRN.
set -eu
gitlab-rails runner '
group = Group.find_by_full_path("fitt") or abort("no such group")
tokens = group.deploy_tokens.where(revoked: false).select { |t| t.name == "crn-push" }
tokens.each { |t| t.update!(revoked: true) }
puts "#{Time.now.utc.iso8601} revoked #{tokens.size}"
group.deploy_tokens.select { |t| t.name == "crn-push" || (!t.revoked && t.write_registry) }.each { |t| puts API::Entities::DeployToken.new(t).as_json.to_json }
' 2>/dev/null
