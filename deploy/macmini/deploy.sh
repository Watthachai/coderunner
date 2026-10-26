#!/bin/zsh
# Brings the build machine to the latest dev and restarts CRN under launchd.
# Works over ssh once install-agent.sh has run at the screen.
set -eu
repo="${0:A:h:h:h}"
. "$repo/deploy/macmini/lib.sh"
cd "$repo"

if ! launchctl print "$domain/$label" >/dev/null 2>&1; then
	echo "CRN is not run by launchd yet: run 'make agent-install' at the machine's screen first" >&2
	exit 1
fi
refuse_if_busy
git pull --ff-only
go build -o bin/crn-server ./cmd/server
launchctl kickstart -k "$domain/$label"
wait_healthy
