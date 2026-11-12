#!/bin/bash
# Run on the GitLab host as root, through ssh: every image and tag of one GitLab project, with
# its digest, as GitLab's own registry client reads them. A private project (fitt/demos) is not
# readable without a login, and a deploy token cannot list a project's images, so this is how
# its tags are read before and after a push. It changes nothing.
#
#   PROJECT   group/project
#
# Prints "<image> <tag> <digest>" lines sorted, "<image> (no tags)" for an image with none,
# then "PROJECTTAGS END <lines>". Anything it cannot read is a non-zero exit, never a shorter
# list.
set -euo pipefail
PROJECT="${PROJECT:?give PROJECT}" gitlab-rails runner '
project = Project.find_by_full_path(ENV.fetch("PROJECT")) or abort("no such project")
lines = []
project.container_repositories.each do |repo|
  tags = repo.tags
  lines << "#{repo.path} (no tags)" if tags.empty?
  tags.each do |t|
    digest = t.digest or abort("no digest for #{repo.path}:#{t.name}")
    lines << "#{repo.path} #{t.name} #{digest}"
  end
end
abort("GitLab lists something twice") unless lines.uniq.size == lines.size
puts lines.sort
puts "PROJECTTAGS END #{lines.size}"
' | grep -E '^([a-z0-9._/-]+ (\(no tags\)|[A-Za-z0-9_.-]+ sha256:[0-9a-f]{64})|PROJECTTAGS END [0-9]+)$'
