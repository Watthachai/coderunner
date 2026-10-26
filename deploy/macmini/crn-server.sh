#!/bin/zsh
# launchd (com.fitt.crn-server) runs this in the build machine's login session,
# where the keychain holding the gh, claude and docker logins is open; over ssh
# it is not, which is why CRN never starts from ssh. launchd gives a bare PATH,
# so the tools' directories are named here.
set -eu
cd "${0:A:h:h:h}"
export PATH="/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
log="$HOME/crn.log"
# Keep the previous run's log, as the manual restart did.
if [ -s "$log" ]; then cp "$log" "$HOME/crn-$(date +%Y%m%d-%H%M%S).log"; fi
set -a; . ./.env; set +a
exec ./bin/crn-server > "$log" 2>&1
