#!/bin/zsh
# Run once at the build machine's own screen (Screen Sharing is fine), not over
# ssh: it hands CRN to launchd, which then starts it at every login and again
# after a crash, in the session where the keychain is open.
set -eu
repo="${0:A:h:h:h}"
. "$repo/deploy/macmini/lib.sh"
plist="$HOME/Library/LaunchAgents/$label.plist"

refuse_if_busy
mkdir -p "$HOME/Library/LaunchAgents"
sed -e "s#__REPO__#$repo#g" -e "s#__HOME__#$HOME#g" "$repo/deploy/macmini/$label.plist" > "$plist"
plutil -lint "$plist"
launchctl bootout "$domain/$label" 2>/dev/null || true
# A CRN started by hand still holds the port.
pid=$(lsof -nP -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null || true)
if [ -n "$pid" ]; then kill $pid; sleep 2; fi
launchctl bootstrap "$domain" "$plist"
wait_healthy
echo "CRN now runs under launchd ($label)."
