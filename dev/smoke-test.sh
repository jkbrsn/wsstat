#!/usr/bin/env bash
# Smoke test for the wsstat CLI against the dev-stack mock WS server.
# Fires the host-built ./bin/wsstat through every CLI feature, one mock path per
# behavior, asserting on exit code and/or stdout. Prints a tally and exits
# non-zero on any failure.
# Dependencies: wsstat (built); jq optional (jq-dependent cases skip without it).
# Usage: ./dev/smoke-test.sh
#        WS_URL=ws://host:port WSSTAT=/path/to/wsstat ./dev/smoke-test.sh
set -euo pipefail

WS_URL="${WS_URL:-ws://localhost:17080}"
WSSTAT="${WSSTAT:-./bin/wsstat}"

PASS=0 FAIL=0 SKIP=0

check() {
	local name="$1"; shift
	if "$@" >/dev/null 2>&1; then
		printf "  PASS  %s\n" "$name"; ((PASS++)) || true
	else
		printf "  FAIL  %s\n" "$name"; ((FAIL++)) || true
	fi
}

skip() {
	printf "  SKIP  %s (%s)\n" "$1" "$2"; ((SKIP++)) || true
}

# check_summary_interval asserts at least one periodic summary fires before the
# count limit ends the run: one periodic + one final == >=2 "Subscription summary".
check_summary_interval() {
	local n
	n=$("$WSSTAT" -s -t sub -c 15 -summary-interval 1s "$WS_URL/stream?rate=5" 2>/dev/null \
		| grep -c "Subscription summary")
	[[ "$n" -ge 2 ]]
}

HAVE_JQ=0
command -v jq >/dev/null 2>&1 && HAVE_JQ=1

echo "wsstat smoke test against $WS_URL"
echo ""

# --- Core messaging (echo) --------------------------------------------------
check "text echo"          "$WSSTAT" -t hello "$WS_URL/echo"
check "burst count=5"      "$WSSTAT" -t hi -c 5 "$WS_URL/echo"
check "rpc-method"         "$WSSTAT" -rpc-method eth_blockNumber "$WS_URL/jsonrpc"

# --- Output formats ---------------------------------------------------------
check "format raw"         "$WSSTAT" -f raw -t hi "$WS_URL/echo"
check "format auto"        "$WSSTAT" -f auto -t hi "$WS_URL/echo"
if [[ $HAVE_JQ -eq 1 ]]; then
	check "format json"    bash -c "$WSSTAT -f json -t hi $WS_URL/echo | jq -es 'any(.[]; .durations_ms.total != null)'"
else
	skip "format json" "jq not installed"
fi

# --- Verbosity --------------------------------------------------------------
check "quiet"              "$WSSTAT" -q -t hi "$WS_URL/echo"
check "verbose"           "$WSSTAT" -v -t hi "$WS_URL/echo"
check "very verbose"      "$WSSTAT" -vv -t hi "$WS_URL/echo"

# --- Request shaping --------------------------------------------------------
check "custom header"      bash -c "$WSSTAT -t hi -H 'X-Smoke: 1' $WS_URL/headers | grep -q 1"
check "resolve override"   "$WSSTAT" -t hi -resolve "mock:17080:127.0.0.1" "ws://mock:17080/echo"

# --- Subscriptions (stream) -------------------------------------------------
check "subscribe-once"     "$WSSTAT" -subscribe-once -t sub "$WS_URL/stream?rate=10"
check "subscribe bounded"  "$WSSTAT" -s -t sub -c 3 "$WS_URL/stream?rate=10"
check "buffer size"        "$WSSTAT" -b 8 -s -t sub -c 3 "$WS_URL/stream?rate=10"
check "summary-interval"   check_summary_interval

# --- Failure & edge paths ---------------------------------------------------
check "timeout trips"      bash -c "! $WSSTAT -timeout 1s -t hi $WS_URL/slow"
check "large frame"        "$WSSTAT" -rpc-method ws_large "$WS_URL/large"
check "abrupt close"       bash -c "! $WSSTAT -t hi $WS_URL/close-abrupt"

echo ""
echo "Results: $PASS passed, $FAIL failed, $SKIP skipped"
exit $((FAIL > 0 ? 1 : 0))
