#!/bin/bash
# Run on the GitLab host as root, through ssh, before GO, and only once both sides have agreed
# the order: revokes the group deploy token crn-push by name and prints it and every other live
# token of the group that can write the registry, as GitLab's API entity shows them. Until a
# new one is made, no project can push an image to this registry from CRN. Any failure is a
# non-zero exit.
set -euo pipefail
gitlab-rails runner '
group = Group.find_by_full_path("fitt") or abort("no such group")
found = group.deploy_tokens.select { |t| t.name == "crn-push" }
abort("expected one token named crn-push, found #{found.size}") unless found.size == 1
token = found.first
was_revoked = token.revoked
token.revoke! unless was_revoked
token.reload
puts "CRNGROUP revoked id=#{token.id} name=#{token.name} revoked=#{token.revoked} already=#{was_revoked} login_allowed_now=#{group.deploy_tokens.active.where(id: token.id).exists?} gitlab_clock=#{Time.current.utc.iso8601}"
writers = group.deploy_tokens.active.select(&:write_registry)
writers.each { |t| puts "CRNGROUP writer " + API::Entities::DeployToken.new(t).as_json.to_json }
abort("token #{token.id} can still log in") unless token.revoked && !group.deploy_tokens.active.where(id: token.id).exists?
' | grep -E '^CRNGROUP '
