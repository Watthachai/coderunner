#!/bin/bash
# Run on the laptop, in Terminal, around a push the push helper makes: every tag of a GitLab
# project before and after, and the registry's write log of the time between. It asks for
# GitLab's root password once each time and changes nothing.
#
#   deploy/macmini/registry-evidence.sh before fitt/demos
#   deploy/macmini/registry-evidence.sh after <the folder before made> <group/project/image:tag>
#
# after says PASS only if the tags differ by that one new tag (an image with no tags before
# may lose its "(no tags)" line) and the write log shows writes to that image only, that tag
# written. The folder in ~/Downloads holds every file and their sha256.
set -euo pipefail
gitlab="${CRN_GITLAB_HOST:-root@172.168.1.234}"
here="$(cd "$(dirname "$0")/../.." && pwd)"
scripts="$here/deploy/registry"
control_dir="$(mktemp -d)"
control="$control_dir/gitlab"
on_gitlab() { ssh -o ControlMaster=auto -o ControlPath="$control" -o ControlPersist=5m "$gitlab" "$@"; }
trap 'ssh -o ControlPath="$control" -O exit "$gitlab" >/dev/null 2>&1 || true; rm -rf "$control_dir"' EXIT
stop() {
	echo "=== REGISTRY EVIDENCE: STOP, $*"
	exit 1
}
read_tags() {
	on_gitlab "PROJECT=$1 bash -s" <"$scripts/project-tags.sh" >"$2" || stop "the tags of $1 could not be read"
	tail -1 "$2" | grep -q '^PROJECTTAGS END [0-9]*$' || stop "the list of $1's tags did not end"
}

case "${1:-}" in
before)
	project="${2:-}"
	case "$project" in */*) ;; *) stop "give the project, group/project" ;; esac
	dir="$HOME/Downloads/registry-evidence-${project//\//-}-$(date +%Y%m%d-%H%M%S)"
	mkdir -p "$dir"
	printf '%s\n' "$project" >"$dir/project.txt"
	on_gitlab date +%s >"$dir/start.txt" || stop "GitLab's clock could not be read"
	read_tags "$project" "$dir/tags-before.txt"
	echo "--- $project before, from $(cat "$dir/start.txt") by GitLab's clock"
	cat "$dir/tags-before.txt"
	echo "=== REGISTRY EVIDENCE: BEFORE RECORDED"
	echo "folder: $dir"
	;;
after)
	dir="${2:-}" allow="${3:-}"
	[ -s "$dir/project.txt" ] && [ -s "$dir/start.txt" ] && [ -s "$dir/tags-before.txt" ] || stop "$dir is not a folder before made"
	project="$(cat "$dir/project.txt")" start="$(cat "$dir/start.txt")"
	repo="${allow%:*}" tag="${allow##*:}"
	case "$repo" in "$project"/*) ;; *) stop "$allow is not an image of $project" ;; esac
	read_tags "$project" "$dir/tags-after.txt"
	end="$(on_gitlab date +%s)" || stop "GitLab's clock could not be read"
	printf '%s\n' "$end" >"$dir/end.txt"
	diff <(grep -v '^PROJECTTAGS END' "$dir/tags-before.txt") <(grep -v '^PROJECTTAGS END' "$dir/tags-after.txt") | grep -E '^[<>]' >"$dir/tags-diff.txt" || true
	echo "--- $project, before (<) and after (>)"
	cat "$dir/tags-diff.txt"
	problems=0
	added="$(grep -c '^> ' "$dir/tags-diff.txt" || true)"
	if [ "$added" != 1 ] || ! grep -Eq "^> $repo $tag sha256:[0-9a-f]{64}$" "$dir/tags-diff.txt"; then
		echo "PROBLEM: the new tags are not $allow alone"
		problems=$((problems + 1))
	fi
	while IFS= read -r line; do
		[ "$line" = "< $repo (no tags)" ] && continue
		echo "PROBLEM: gone or changed: ${line#< }"
		problems=$((problems + 1))
	done < <(grep '^< ' "$dir/tags-diff.txt" || true)
	echo "--- write log, $start to $end by GitLab's clock, may write $allow"
	if on_gitlab "WINDOW_START=$start WINDOW_END=$end PUSH_ALLOW_IMAGE=$allow bash -s" <"$scripts/registry-write-log.sh" >"$dir/writelog.txt" 2>&1; then
		echo "write log PASSED"
	else
		echo "PROBLEM: the write log did not pass"
		problems=$((problems + 1))
	fi
	sed -n '/== summary/,/== registry token requests/p' "$dir/writelog.txt" | head -12
	(cd "$dir" && shasum -a 256 project.txt start.txt end.txt tags-before.txt tags-after.txt tags-diff.txt writelog.txt >SHA256SUMS)
	cat "$dir/SHA256SUMS"
	if [ "$problems" = 0 ]; then echo "=== REGISTRY EVIDENCE: PASS, $allow"; else stop "$problems problem(s); both sides stop until they have talked"; fi
	;;
*)
	stop "before <group/project>, or after <folder> <group/project/image:tag>"
	;;
esac
