# Shared by install-agent.sh and deploy.sh.
label=com.fitt.crn-server
domain="gui/$(id -u)"
port="${PORT:-8080}"
export PATH="/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

# A restart fails whatever is building, so refuse while anything runs or waits.
# CRN not answering means nothing can be running.
refuse_if_busy() {
	local vitals busy
	vitals=$(curl -fsS -m 5 "localhost:$port/internal/dashboard" 2>/dev/null) || return 0
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
