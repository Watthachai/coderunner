#!/bin/bash
# Run on the GitLab host as root, through ssh, before GO: revokes the group deploy token
# crn-push. Until a new one is made, no project can push an image to this registry.
set -eu
gitlab-rails runner '
group = Group.find_by_full_path("fitt") or abort("no such group")
tokens = group.deploy_tokens.where(revoked: false).select { |t| t.name == "crn-push" }
tokens.each { |t| t.update!(revoked: true) }
left = group.deploy_tokens.where(revoked: false).select { |t| t.write_registry }.map(&:name)
puts "revoked #{tokens.size}, group tokens that can still write: #{left.empty? ? "none" : left.join(", ")}"
' 2>/dev/null
