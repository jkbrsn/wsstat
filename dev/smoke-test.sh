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
WSS_URL="${WSS_URL:-wss://localhost:17443}"
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
	n=$("$WSSTAT" stream -t sub -c 15 --summary-interval 1s "$WS_URL/stream?rate=5" 2>/dev/null \
		| grep -c "Subscription summary")
	[[ "$n" -ge 2 ]]
}

# check_teardown_bound measures the wall-clock of a full run against the
# write-only /push peer, which never echoes the closing handshake. coder's
# Conn.Close blocks 5s waiting for that echo; wsstat bounds the wait to its
# close-grace (default 3s) and forces teardown, so the run lands near 3s, not 5s.
# Here we pin close-grace to 1s and assert < 2500ms: tight enough to catch a
# regression to the unbounded 5s stall, loose enough to absorb the 3 pushed
# frames (~150ms at rate=20) plus scheduling jitter.
check_teardown_bound() {
	local start end ms
	start=$(date +%s%3N)
	"$WSSTAT" stream --close-timeout 1s -t sub -c 3 "$WS_URL/push?rate=20" >/dev/null 2>&1 || true
	end=$(date +%s%3N)
	ms=$((end - start))
	printf "    (teardown wall-clock: %dms)\n" "$ms" >&2
	[[ "$ms" -lt 2500 ]]
}

# check_stream_raw_clean asserts `stream -o raw` emits only verbatim payload
# bytes: the concatenated JSON frames, with no "Streaming subscription events"
# header and no "Subscription summary" block. Frames are undelimited, so a clean
# run is exactly N adjacent JSON objects.
check_stream_raw_clean() {
	local out
	out=$("$WSSTAT" stream -o raw -t sub -c 3 "$WS_URL/stream?rate=10" 2>/dev/null)
	if grep -q "Streaming subscription events\|Subscription summary" <<<"$out"; then
		return 1
	fi
	if [[ $HAVE_JQ -eq 1 ]]; then
		jq -es 'length == 3 and all(.[]; .method == "subscription")' <<<"$out" >/dev/null
	else
		[[ -n "$out" ]]
	fi
}

# check_file_record runs a measure with --file to a fresh path and asserts the recorded
# NDJSON file is created and captures the echoed payload. --file opens O_EXCL, so the path
# must not exist beforehand; a temp dir gives a clean, writable target.
check_file_record() {
	local dir out
	dir=$(mktemp -d)
	# shellcheck disable=SC2064
	trap "rm -rf '$dir'" RETURN
	out="$dir/record.jsonl"
	"$WSSTAT" -t smoke-payload --file "$out" "$WS_URL/echo" >/dev/null 2>&1 || return 1
	[[ -s "$out" ]] || return 1
	grep -q "smoke-payload" "$out"
}

# check_wss_verify_ca fetches the mock's self-signed cert over the plain port and
# trusts it via SSL_CERT_FILE, then dials wss:// WITHOUT -insecure. This is the
# only case that exercises a successful *verifying* TLS handshake end-to-end (the
# realistic production path that -insecure bypasses).
check_wss_verify_ca() {
	local ca http_url
	ca=$(mktemp)
	# shellcheck disable=SC2064
	trap "rm -f '$ca'" RETURN
	# /ca.pem is served over plain HTTP; curl rejects the ws:// scheme.
	http_url="${WS_URL/#ws:\/\//http://}"
	curl -fsS "$http_url/ca.pem" -o "$ca" || return 1
	SSL_CERT_FILE="$ca" "$WSSTAT" -t hi "$WSS_URL/echo" >/dev/null 2>&1
}

HAVE_JQ=0
command -v jq >/dev/null 2>&1 && HAVE_JQ=1
HAVE_CURL=0
command -v curl >/dev/null 2>&1 && HAVE_CURL=1

echo "wsstat smoke test against $WS_URL"
echo ""

# --- Core messaging (echo) --------------------------------------------------
check "text echo"          "$WSSTAT" -t hello "$WS_URL/echo"
check "burst count=5"      "$WSSTAT" -t hi -c 5 "$WS_URL/echo"
check "rpc-method"         "$WSSTAT" -rpc-method eth_blockNumber "$WS_URL/jsonrpc"

# --- Output axes ------------------------------------------------------------
check "output raw"         "$WSSTAT" -o raw -t hi "$WS_URL/echo"
check "body auto"          "$WSSTAT" --body auto -t hi "$WS_URL/echo"
check "body compact"       "$WSSTAT" --body compact -t hi "$WS_URL/echo"
if [[ $HAVE_JQ -eq 1 ]]; then
	check "output json"    bash -c "$WSSTAT -o json -t hi $WS_URL/echo | jq -es 'any(.[]; .durations_ms.total != null)'"
else
	skip "output json" "jq not installed"
fi
# Axis purity: text-only flags must be rejected under -o json|raw.
check "json rejects -v"    bash -c "! $WSSTAT -o json -v -t hi $WS_URL/echo"
check "raw measure needs msg" bash -c "! $WSSTAT -o raw $WS_URL/echo"
check "file record"        check_file_record

# --- Verbosity --------------------------------------------------------------
check "quiet"              "$WSSTAT" -q -t hi "$WS_URL/echo"
check "verbose"           "$WSSTAT" -v -t hi "$WS_URL/echo"
check "very verbose"      "$WSSTAT" -vv -t hi "$WS_URL/echo"

# --- Request shaping --------------------------------------------------------
check "custom header"      bash -c "$WSSTAT -t hi -H 'X-Smoke: 1' $WS_URL/headers | grep -q 1"
check "resolve override"   "$WSSTAT" -t hi -resolve "mock:17080:127.0.0.1" "ws://mock:17080/echo"

# --- Stream subcommand ------------------------------------------------------
check "stream once"        "$WSSTAT" stream --once -t sub "$WS_URL/stream?rate=10"
check "stream bounded"     "$WSSTAT" stream -t sub -c 3 "$WS_URL/stream?rate=10"
check "buffer size"        "$WSSTAT" stream -b 8 -t sub -c 3 "$WS_URL/stream?rate=10"
check "stream raw clean"   check_stream_raw_clean
check "summary-interval"   check_summary_interval

# --- Failure & edge paths ---------------------------------------------------
check "timeout trips"      bash -c "! $WSSTAT -timeout 1s -t hi $WS_URL/slow"
check "large frame"        bash -c "$WSSTAT -o raw --rpc-method ws_large $WS_URL/large | wc -c | awk '{exit (\$1 > 32768) ? 0 : 1}'"
check "abrupt close"       bash -c "! $WSSTAT -t hi $WS_URL/close-abrupt"
check "teardown bound"     check_teardown_bound

# --- TLS / wss:// -----------------------------------------------------------
check "wss insecure"       "$WSSTAT" -insecure -t hi "$WSS_URL/echo"
check "wss -k short form"   "$WSSTAT" -k -t hi "$WSS_URL/echo"
check "wss verify rejects"  bash -c "! $WSSTAT -t hi $WSS_URL/echo"
if [[ $HAVE_CURL -eq 1 ]]; then
	check "wss verify trusts ca" check_wss_verify_ca
else
	skip "wss verify trusts ca" "curl not installed"
fi
check "ws:// scheme"       "$WSSTAT" -t hi "ws://localhost:17080/echo"

# --- v2 migration errors ----------------------------------------------------
# wsstat exits non-zero; the pipeline status is grep's, so a found message passes.
check "removed -subscribe" bash -c "$WSSTAT -subscribe -t hi $WS_URL/stream 2>&1 | grep -q 'removed in v3'"
check "removed -format"    bash -c "$WSSTAT -format json -t hi $WS_URL/echo 2>&1 | grep -q 'removed in v3'"

echo ""
echo "Results: $PASS passed, $FAIL failed, $SKIP skipped"
exit $((FAIL > 0 ? 1 : 0))
