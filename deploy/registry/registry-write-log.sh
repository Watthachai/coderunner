#!/bin/bash
# Run on the GitLab host as root, through ssh, after a push window's token is dead. It only reads
# logs. A comparison of tags sees only how things ended; this shows every write that happened,
# so a tag written and put back still shows.
#
#   WINDOW_START, WINDOW_END   the window, in seconds, by this host's clock
#   PUSH_ALLOW_IMAGE           group/project/image:tag this window may push; empty: no write at all
#
# It reads nginx on the registry's port and the registry itself, which also sees what reached it
# without nginx. Prints a summary, then every line of the window with only secrets removed: the
# upload _state, Authorization values, JWTs and GitLab tokens. Method, path, status, time and
# address stay.
#
# A write is POST, PUT, PATCH or DELETE under /v2/ that was not refused with 401 or 403. It
# passes only if each log shows the window and every write is a blob, a mount or a manifest
# whose destination is the allowed image, the only tag it writes is the allowed tag, and that
# tag was written. Exit 0 only then.
set -euo pipefail
case "${WINDOW_START:-}" in '' | *[!0-9]*)
	echo "give WINDOW_START" >&2
	exit 2
	;;
esac
case "${WINDOW_END:-}" in '' | *[!0-9]*)
	echo "give WINDOW_END" >&2
	exit 2
	;;
esac
export WINDOW_START WINDOW_END PUSH_ALLOW_IMAGE="${PUSH_ALLOW_IMAGE:-}"
exec "${WRITE_LOG_RUBY:-/opt/gitlab/embedded/bin/ruby}" -rjson -rtime -rzlib -e '
from = Integer(ENV.fetch("WINDOW_START"))
to = Integer(ENV.fetch("WINDOW_END"))
allow = ENV.fetch("PUSH_ALLOW_IMAGE")
allow_repo, _, allow_tag = allow.rpartition(":")
allow_repo = nil if allow.empty?
nginx_dir = ENV.fetch("WRITE_LOG_NGINX", "/var/log/gitlab/nginx")
registry_dir = ENV.fetch("WRITE_LOG_REGISTRY", "/var/log/gitlab/registry")
rails_dir = ENV.fetch("WRITE_LOG_RAILS", "/var/log/gitlab/gitlab-rails")
abort("the window ends before it starts") if to < from

read_lines = lambda do |path|
  raw = File.binread(path)
  raw = Zlib.gunzip(raw) if raw.start_with?("\x1f\x8b".b)
  raw.force_encoding("UTF-8").scrub.split("\n")
end
secrets = [
  [/_state=[^&" ]+/, "_state=[REDACTED]"],
  [/(authorization"?[:=] *"?)(bearer |basic |token )?[^"&, ]+/i, "\\1[REDACTED]"],
  [/eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)?/, "[REDACTED-JWT]"],
  [/\b(glpat|gldt|glrt|glcbt|glptt|glft|glimt|glagent|gloas|glsoat|gltst|glwt)-[A-Za-z0-9_.-]{8,}/, "[REDACTED-GLTOKEN]"]
]
redact = ->(line) { secrets.reduce(line) { |text, (pattern, value)| text.gsub(pattern, value) } }
iso = ->(t) { Time.at(t).utc.iso8601 }
inside = ->(t) { t.to_i >= from && t.to_i <= to }
problems = []

# What a request under /v2/ does, or nil when it does not write.
classify = lambda do |method, target|
  next nil unless %w[POST PUT PATCH DELETE].include?(method)
  path, query = target.split("?", 2)
  next nil unless path.start_with?("/v2/")
  m = path.match(%r{\A/v2/(.+?)/(blobs/uploads|blobs|manifests)(?:/(.*))?\z})
  next { repo: "(no repository)", kind: "other" } unless m
  params = (query || "").split("&").map { |p| k, v = p.split("=", 2); [k, v] }.to_h
  kind = if method == "DELETE" then "delete"
         elsif m[2] == "manifests" then "manifest"
         elsif params["mount"] then "mount"
         elsif m[2] == "blobs/uploads" then "blob"
         else "other"
         end
  { repo: m[1], kind: kind, ref: m[3], from: params["from"] }
end

# nginx on the registry port
nginx_lines = []
nginx_files = Dir[File.join(nginx_dir, "gitlab_registry_access.log*")].select { |f| File.file?(f) }.sort
abort("no nginx log for the registry in #{nginx_dir}") if nginx_files.empty?
nginx_requests = []
nginx_files.each do |file|
  read_lines.call(file).each do |line|
    m = line.match(/\A(\S+) \S+ \S+ \[([^\]]+)\] "(?:(\S+) (\S+) [^"]*|[^"]*)" (\d+)/)
    unless m
      problems << "nginx: a line that cannot be read in #{File.basename(file)}" unless line.strip.empty?
      next
    end
    time = Time.strptime(m[2], "%d/%b/%Y:%H:%M:%S %z")
    next unless inside.call(time)
    nginx_lines << [time.to_i, line]
    nginx_requests << { time: time.to_i, ip: m[1], method: m[3], target: m[4], status: m[5].to_i, identity: "not recorded by nginx" } if m[3]
  end
end

# the registry: its access lines, and what it says about who asked
registry_files = Dir[File.join(registry_dir, "@*")].select { |f| File.file?(f) }.sort
registry_files << File.join(registry_dir, "current")
abort("no registry log in #{registry_dir}") unless File.file?(registry_files.last)
registry_lines = []
registry_requests = []
who = {}
registry_files.each do |file|
  read_lines.call(file).each do |line|
    next if line.strip.empty?
    stamp = line[/\A(\d{4}-\d\d-\d\d)_(\d\d:\d\d:\d\d)/] ? Time.parse($1 + "T" + $2 + "Z") : nil
    body = stamp ? line.sub(/\A\S+ /, "") : line
    record = begin
      body.start_with?("{") ? JSON.parse(body) : nil
    rescue JSON::ParserError
      nil
    end
    time = stamp
    time ||= Time.parse(record["time"]) if record && record["time"]
    if time.nil? && body =~ /\btime="([^"]+)"/
      time = Time.parse($1)
    end
    unless time
      problems << "registry: a line with no time in #{File.basename(file)}"
      next
    end
    next unless inside.call(time)
    registry_lines << [time.to_i, line]
    if record && record["msg"] == "access"
      registry_requests << { time: time.to_i, ip: record["remote_ip"], method: record["method"], target: record["uri"].to_s, status: record["status"].to_i, id: record["correlation_id"] }
    elsif body =~ /auth_user_type=(\S*)/
      type = $1
      name = body[/auth_user_name=(\S*)/, 1]
      request = body[/correlation_id=(\S+)/, 1]
      who[request] = [type, name.to_s.empty? ? nil : name].compact.join(" ") if request
    end
  end
end
registry_requests.each { |r| r[:identity] = who[r[:id]] || "not recorded" }

# who asked GitLab for a registry token, where the Rails log says
token_requests = []
rails_note = "read from production_json.log"
begin
  Dir[File.join(rails_dir, "production_json.log*")].select { |f| File.file?(f) }.sort.each do |file|
    read_lines.call(file).each do |line|
      next unless line.include?("/jwt/auth")
      record = JSON.parse(line) rescue next
      next unless record["path"] == "/jwt/auth" && inside.call(Time.parse(record["time"]))
      params = (record["params"] || []).map { |p| [p["key"], p["value"]] }.to_h
      token_requests << "#{record["time"]} status=#{record["status"]} ip=#{record["remote_ip"]} account=#{params["account"] || "not recorded"} scope=#{params["scope"]}"
    end
  end
rescue SystemCallError => e
  rails_note = "not available: #{e.class}"
end

summary = []
judge = lambda do |source, requests|
  writes = []
  refused = []
  requests.each do |r|
    what = r[:method] && classify.call(r[:method], r[:target])
    next unless what
    entry = r.merge(what)
    [401, 403].include?(r[:status]) ? refused << entry : writes << entry
  end
  writes.each do |w|
    place = "#{w[:method]} #{w[:repo]} #{w[:kind]}"
    if allow_repo.nil?
      problems << "#{source}: a write in a window that may write nothing: #{place}"
    elsif w[:repo] != allow_repo
      problems << "#{source}: a write outside the allowed image: #{place}"
    elsif !%w[blob mount manifest].include?(w[:kind])
      problems << "#{source}: a write that is not a blob or a manifest: #{place}"
    elsif w[:kind] == "manifest" && w[:ref] != allow_tag && !w[:ref].to_s.start_with?("sha256:")
      problems << "#{source}: a manifest for another tag: #{w[:repo]}:#{w[:ref]}"
    end
  end
  if allow_repo && writes.none? { |w| w[:repo] == allow_repo && w[:kind] == "manifest" && w[:ref] == allow_tag && w[:status] == 201 }
    problems << "#{source}: the allowed tag #{allow} was never written"
  end
  summary << "== #{source}: #{requests.size} requests in the window, #{writes.size} writes, #{refused.size} refused"
  writes.group_by { |w| w[:repo] }.sort.each do |repo, list|
    summary << "writes to #{repo}: #{list.size}"
    list.sort_by { |w| w[:time] }.each do |w|
      detail = [w[:ref] && w[:kind] == "manifest" ? "ref=#{w[:ref]}" : nil, w[:from] ? "from=#{w[:from]}" : nil].compact.join(" ")
      summary << "  #{iso.call(w[:time])} #{w[:method]} #{redact.call(w[:target])} #{w[:status]} ip=#{w[:ip]} kind=#{w[:kind]} who=#{w[:identity]} #{detail}".rstrip
    end
  end
  refused.sort_by { |w| w[:time] }.each do |w|
    summary << "refused: #{iso.call(w[:time])} #{w[:method]} #{redact.call(w[:target])} #{w[:status]} ip=#{w[:ip]} who=#{w[:identity]}"
  end
end
judge.call("nginx", nginx_requests)
judge.call("registry", registry_requests)
problems << "nginx: no line in the window, so it shows nothing" if nginx_lines.empty?
problems << "registry: no line in the window, so it shows nothing" if registry_lines.empty?

puts "== summary"
puts "window #{iso.call(from)} to #{iso.call(to)} by this host, now #{Time.now.utc.iso8601}"
puts "may write: #{allow_repo ? allow : "nothing"}"
puts "redacted: upload _state, Authorization values, JWTs, GitLab tokens. Nothing else."
puts "who: nginx records no identity. The registry records the kind of login and, when it has it, the name. Rails records the account a client names when it asks for a registry token."
puts summary
puts "== registry token requests, #{rails_note}"
puts token_requests.sort
if problems.empty?
  puts "WRITE LOG PASSED"
else
  problems.uniq.each { |p| puts "WRONG: #{p}" }
  puts "WRITE LOG FAILED: #{problems.uniq.size} problem(s)"
end
puts "== raw, nginx"
nginx_lines.sort_by(&:first).each { |_, line| puts redact.call(line) }
puts "== raw, registry"
registry_lines.sort_by(&:first).each { |_, line| puts redact.call(line) }
exit(problems.empty? ? 0 : 1)
'
