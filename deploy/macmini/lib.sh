# Shared by install-agent.sh and deploy.sh.
label=com.fitt.crn-server
domain="gui/$(id -u)"
port="${PORT:-8080}"
export PATH="/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

# A restart fails whatever is building, so refuse while anything runs or waits.
# CRN not answering means nothing can be running. Once /internal wants a key,
# the check sends the operator's (a header file, so the key is not on the
# command line); any answer but 200 refuses rather than guessing.
refuse_if_busy() {
	local out code vitals busy
	local header="$HOME/crn-secrets/operator-auth-header"
	local -a auth
	[ -s "$header" ] && auth=(-H "@$header")
	out=$(curl -sS -m 5 -w '\n%{http_code}' "${auth[@]}" "localhost:$port/internal/dashboard" 2>/dev/null) || return 0
	code=${out##*$'\n'}
	vitals=${out%$'\n'*}
	if [ "$code" != 200 ]; then
		echo "refusing: CRN answered $code to the busy check (is $header right?)" >&2
		exit 1
	fi
	busy=$(print -r -- "$vitals" | python3 -c 'import sys,json; v=json.load(sys.stdin)["vitals"]; print(v["building"]+v["queued"])')
	if [ "$busy" != 0 ]; then
		echo "refusing: $busy build(s) running or queued; try again when they finish" >&2
		exit 1
	fi
}

wait_healthy() {
	for _ in {1..30}; do
		sleep 1
		if curl -fsS -m 2 "localhost:$port/healthz"; then echo; return 0; fi
	done
	echo "CRN did not answer /healthz; see ~/crn.log and ~/crn-launchd.log" >&2
	return 1
}
