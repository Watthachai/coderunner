#!/bin/zsh
# Sets up the push helper on the build machine, as root, once:
#
#   ssh -t macagents@172.168.1.171 'sudo zsh ~/fitt-coderunner/deploy/macmini/push-helper-setup.sh'
#
# It makes a hidden user, _crnpush, that alone can read the registry login (/var/crnpush,
# mode 700), installs crn-push and its config where only root can change them (/opt/crn-push),
# and lets CRN's user run that one command as _crnpush, and nothing else
# (/etc/sudoers.d/crn-push). It puts no login anywhere: push-helper-login.sh does, from the
# laptop. Running it again changes nothing that is already right.
set -eu
[ "$(id -u)" = 0 ] || { echo "run it with sudo" >&2; exit 1; }
crn_user="${CRN_USER:-macagents}"
repo="/Users/$crn_user/fitt-coderunner"
helper_user=_crnpush
home=/var/crnpush
opt=/opt/crn-push
registry="${CRN_PUSH_REGISTRY:-172.168.1.234:5050}"
projects="${CRN_PUSH_PROJECTS:-fitt/ftcdv-uat}"

[ -x "$repo/bin/crn-push" ] || { echo "no $repo/bin/crn-push: run deploy.sh first" >&2; exit 1; }

echo "--- the user $helper_user"
if ! dscl . -read "/Users/$helper_user" >/dev/null 2>&1; then
	id=""
	for candidate in $(seq 480 499); do
		if ! dscl . -search /Users UniqueID "$candidate" | grep -q . && ! dscl . -search /Groups PrimaryGroupID "$candidate" | grep -q .; then
			id=$candidate
			break
		fi
	done
	[ -n "$id" ] || { echo "no free id between 480 and 499" >&2; exit 1; }
	dscl . -create "/Groups/$helper_user"
	dscl . -create "/Groups/$helper_user" PrimaryGroupID "$id"
	dscl . -create "/Groups/$helper_user" Password '*'
	dscl . -create "/Users/$helper_user"
	dscl . -create "/Users/$helper_user" UniqueID "$id"
	dscl . -create "/Users/$helper_user" PrimaryGroupID "$id"
	dscl . -create "/Users/$helper_user" NFSHomeDirectory "$home"
	dscl . -create "/Users/$helper_user" UserShell /usr/bin/false
	dscl . -create "/Users/$helper_user" RealName "CRN registry push"
	dscl . -create "/Users/$helper_user" Password '*'
	dscl . -create "/Users/$helper_user" IsHidden 1
	echo "made $helper_user, uid $id"
else
	echo "$helper_user is there, uid $(id -u $helper_user)"
fi

echo "--- $home, for the login and the log"
install -d -o "$helper_user" -g "$helper_user" -m 700 "$home" "$home/tmp"

echo "--- $opt, which only root can change"
install -d -o root -g wheel -m 755 "$opt"
install -o root -g wheel -m 755 "$repo/bin/crn-push" "$opt/crn-push.new"
mv -f "$opt/crn-push.new" "$opt/crn-push"
project_list=""
for p in ${=projects}; do project_list="${project_list:+$project_list,}\"$p\""; done
cat >"$opt/crn-push.json.new" <<EOF
{"registry":"$registry","scheme":"http","projects":[$project_list],"credential":"$home/auth.json","log":"$home/push.log","work_dir":"$home/tmp"}
EOF
chown root:wheel "$opt/crn-push.json.new"
chmod 644 "$opt/crn-push.json.new"
mv -f "$opt/crn-push.json.new" "$opt/crn-push.json"
cat "$opt/crn-push.json"
echo "crn-push sha256 $(shasum -a 256 "$opt/crn-push" | cut -d' ' -f1)"

echo "--- sudo: $crn_user may run crn-push push as $helper_user, nothing else"
rule="$crn_user ALL=($helper_user) NOPASSWD: $opt/crn-push push *"
tmp=$(mktemp)
printf '%s\n' "$rule" >"$tmp"
visudo -cf "$tmp" >/dev/null || { rm -f "$tmp"; echo "visudo refused the rule" >&2; exit 1; }
install -o root -g wheel -m 440 "$tmp" /etc/sudoers.d/crn-push
rm -f "$tmp"
visudo -c >/dev/null || { echo "sudoers no longer parses: /etc/sudoers.d/crn-push is the new file" >&2; exit 1; }
cat /etc/sudoers.d/crn-push

echo "--- checks"
problems=0
if sudo -u "$crn_user" test -r "$home/tmp" 2>/dev/null; then
	echo "PROBLEM: $crn_user can read $home"
	problems=$((problems + 1))
else
	echo "$crn_user cannot read $home: OK"
fi
if sudo -u "$crn_user" test -w "$opt/crn-push" 2>/dev/null || sudo -u "$crn_user" test -w "$opt" 2>/dev/null; then
	echo "PROBLEM: $crn_user can change $opt"
	problems=$((problems + 1))
else
	echo "$crn_user cannot change $opt: OK"
fi
# The helper refuses while an agent runs, so it must see other users' command lines.
if sudo -u "$helper_user" /usr/bin/pgrep -f 'bin/crn-server' >/dev/null; then
	echo "$helper_user sees CRN's processes by their command line: OK"
else
	echo "PROBLEM: $helper_user cannot see CRN's command lines, so it could not see an agent"
	problems=$((problems + 1))
fi
answer=$(sudo -u "$crn_user" /usr/bin/sudo -n -u "$helper_user" "$opt/crn-push" push "$registry/fitt/not-served/crn-demo-x:v1" </dev/null 2>&1 || true)
case "$answer" in
*"not one this helper serves"*) echo "$crn_user runs crn-push as $helper_user, and it refuses another project: OK" ;;
*) echo "PROBLEM: crn-push through sudo answered: $answer"; problems=$((problems + 1)) ;;
esac
answer=$(sudo -u "$crn_user" /usr/bin/sudo -n -u "$helper_user" /bin/cat "$home/push.log" 2>&1 || true)
case "$answer" in
*"a password is required"* | *"not allowed"* | *"may not run"*) echo "$crn_user cannot run anything else as $helper_user: OK" ;;
*) echo "PROBLEM: $crn_user ran cat as $helper_user: $answer"; problems=$((problems + 1)) ;;
esac
if [ -e "$home/auth.json" ]; then
	echo "login: $home/auth.json is there ($(stat -f '%Su %Lp %z bytes' "$home/auth.json"))"
else
	echo "login: none yet; run deploy/macmini/push-helper-login.sh from the laptop"
fi
if [ "$problems" = 0 ]; then echo "=== PUSH HELPER SETUP: PASS"; else echo "=== PUSH HELPER SETUP: $problems PROBLEM(S)"; exit 1; fi
