#!/usr/bin/env bash
# Generative soak harness for the wsstat CLI against the dev-stack mock server.
#
# Where smoke-test.sh asserts that each feature works once, this harness drives a
# structured combination MATRIX whose purpose is to catch interaction bugs:
#   - POSITIVE: every flag, in each mode, with its long/short alias, succeeds.
#   - REJECT:   every documented validation rule actually rejects (exit != 0 with
#               the expected message). A rule that returns exit 0 is a SILENT
#               ACCEPT -- a flag combination quietly dropped instead of refused.
#   - EFFECT:   flags whose only failure mode is being ignored are checked for
#               their observable effect (-o raw bytes, -q suppression, --color
#               ANSI, --body shape, --clip width under a real PTY).
#
# It is the combination/soak sibling of smoke-test.sh and shares its conventions:
# asserts on exit code and stdout/stderr, prints a tally, exits non-zero on any
# failure, and skips jq/curl/python3-dependent cases cleanly when those are absent.
#
# Usage: ./dev/soak.sh
#        WS_URL=ws://host:port WSS_URL=wss://host:port WSSTAT=/path/to/wsstat ./dev/soak.sh
set -uo pipefail  # NOT -e: reject cases run the binary expecting failure.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
WS_URL="${WS_URL:-ws://localhost:17080}"
WSS_URL="${WSS_URL:-wss://localhost:17443}"
B="${WSSTAT:-./bin/wsstat}"

PASS=0 FAIL=0 SKIP=0
declare -a FAILURES=()

OUTF="$(mktemp)" ERRF="$(mktemp)"
cleanup() { rm -f "$OUTF" "$ERRF"; }
trap cleanup EXIT

HAVE_JQ=0;  command -v jq      >/dev/null 2>&1 && HAVE_JQ=1
HAVE_CURL=0; command -v curl   >/dev/null 2>&1 && HAVE_CURL=1
HAVE_PTY=0; command -v python3 >/dev/null 2>&1 && HAVE_PTY=1

# --- result recording -------------------------------------------------------
P() { printf "  PASS  %s\n" "$1"; ((PASS++)) || true; }
F() { printf "  FAIL  %s -- %s\n" "$1" "$2"; FAILURES+=("$1 -- $2"); ((FAIL++)) || true; }
S() { printf "  SKIP  %s (%s)\n" "$1" "$2"; ((SKIP++)) || true; }
section() { printf "\n--- %s\n" "$1"; }

# _run CMD...  -> sets RC, leaves stdout in $OUTF and stderr in $ERRF.
_run() { "$@" >"$OUTF" 2>"$ERRF"; RC=$?; }
errtail() { tail -n1 "$ERRF" 2>/dev/null; }

# --- assertions -------------------------------------------------------------
# ok NAME -- CMD...                 expect exit 0
ok() { local n="$1"; shift 2; _run "$@"
	if [[ $RC -eq 0 ]]; then P "$n"; else F "$n" "expected exit 0, got $RC: $(errtail)"; fi; }

# reject NAME REGEX -- CMD...       expect exit != 0 AND stderr matches REGEX
reject() { local n="$1" re="$2"; shift 3; _run "$@"
	if [[ $RC -eq 0 ]]; then F "$n" "SILENT ACCEPT: expected rejection, got exit 0"
	elif ! grep -Eq -- "$re" "$ERRF"; then F "$n" "exit $RC but stderr !~ /$re/: $(errtail)"
	else P "$n"; fi; }

# outhas NAME REGEX -- CMD...       expect exit 0 AND stdout matches REGEX
outhas() { local n="$1" re="$2"; shift 3; _run "$@"
	if [[ $RC -ne 0 ]]; then F "$n" "expected exit 0, got $RC: $(errtail)"
	elif ! grep -Eq -- "$re" "$OUTF"; then F "$n" "stdout !~ /$re/"
	else P "$n"; fi; }

# outlacks NAME REGEX -- CMD...     expect exit 0 AND stdout does NOT match REGEX
outlacks() { local n="$1" re="$2"; shift 3; _run "$@"
	if [[ $RC -ne 0 ]]; then F "$n" "expected exit 0, got $RC: $(errtail)"
	elif grep -Eq -- "$re" "$OUTF"; then F "$n" "stdout unexpectedly ~ /$re/"
	else P "$n"; fi; }

# outeq NAME WANT -- CMD...         expect exit 0 AND stdout (sans trailing \n) == WANT
outeq() { local n="$1" want="$2"; shift 3; _run "$@"; local got; got="$(cat "$OUTF")"
	if [[ $RC -ne 0 ]]; then F "$n" "expected exit 0, got $RC: $(errtail)"
	elif [[ "$got" != "$want" ]]; then F "$n" "stdout '$got' != '$want'"
	else P "$n"; fi; }

# pred NAME CMD...                  expect the predicate command to succeed
pred() { local n="$1"; shift; if "$@"; then P "$n"; else F "$n" "predicate failed"; fi; }

# --- predicates (defined before use; bash binds functions as it reads them) --
stream_raw_clean() {
	"$B" stream -o raw -t s -c 3 "$WS_URL/stream?rate=10" >"$OUTF" 2>/dev/null
	if grep -Eq "Streaming subscription events|Subscription summary" "$OUTF"; then return 1; fi
	if [[ $HAVE_JQ -eq 1 ]]; then
		jq -es 'length == 3 and all(.[]; .method == "subscription")' "$OUTF" >/dev/null
	else
		[[ -s "$OUTF" ]]
	fi
}
summary_interval_fires() {
	local n
	n=$("$B" stream -t s -c 15 --summary-interval 1s "$WS_URL/stream?rate=5" 2>/dev/null \
		| grep -c "Subscription summary")
	[[ "$n" -ge 2 ]]
}
json_measure_total() { "$B" -o json -t hi "$WS_URL/echo" 2>/dev/null | jq -es 'any(.[]; .durations_ms.total != null)' >/dev/null; }

# Repeated -t conversation payloads against the stateful /subscriptions endpoint.
SUB_BTC='{"method":"subscribe","subscription":{"type":"trades","coin":"BTC"}}'
UNSUB_BTC='{"method":"unsubscribe","subscription":{"type":"trades","coin":"BTC"}}'
# multi_send_ordered asserts the full conversation semantics: three frames on one
# connection produce, in order, a subscribe ack, the duplicate rejection (only
# reachable on a second frame of the same session), and an unsubscribe ack.
# -o json gives one NDJSON line per reply, so order is asserted by line number.
multi_send_ordered() {
	"$B" stream -c 3 -o json --send-delay 100ms \
		-t "$SUB_BTC" -t "$SUB_BTC" -t "$UNSUB_BTC" "$WS_URL/subscriptions" >"$OUTF" 2>/dev/null || return 1
	sed -n 1p "$OUTF" | grep -q '"method":"subscribe"' || return 1
	sed -n 2p "$OUTF" | grep -q "Already subscribed" || return 1
	sed -n 3p "$OUTF" | grep -q '"method":"unsubscribe"'
}
json_stream_method()  { "$B" stream -o json -t s -c 3 "$WS_URL/stream?rate=10" 2>/dev/null | jq -es 'all(.[]; .schema_version != null and .type != null) and any(.[]; .type == "subscription_message" and .payload.method == "subscription")' >/dev/null; }
recv_limit_skips_sends() {
	# Receive limit reached before all sends: the remaining -t frames are skipped
	# and the run still exits 0 with exactly one reply received (the stream ends
	# with a subscription_summary record either way, so count message records).
	"$B" stream -c 1 -o json --send-delay 200ms \
		-t "$SUB_BTC" -t "$UNSUB_BTC" "$WS_URL/subscriptions" >"$OUTF" 2>/dev/null || return 1
	[[ $(grep -c '"type":"subscription_message"' "$OUTF") -eq 1 ]] &&
		! grep -q '"method":"unsubscribe"' "$OUTF"
}
send_delay_staggers() {
	# --send-delay's only failure mode is being ignored (an undelayed conversation
	# still arrives in order): two sends 600ms apart put a floor on the wall clock.
	local t0 t1; t0=$(date +%s%N)
	"$B" stream -c 2 --send-delay 600ms \
		-t "$SUB_BTC" -t "$UNSUB_BTC" "$WS_URL/subscriptions" >/dev/null 2>&1 || return 1
	t1=$(date +%s%N)
	[[ $(( (t1 - t0) / 1000000 )) -ge 550 ]]
}

# _pty_maxwidth COLS -- CMD...  -> widest output line in columns (ANSI/CR stripped)
_pty_maxwidth() {
	local cols="$1"; shift 2
	python3 "$SCRIPT_DIR/pty-run.py" "$cols" 24 -- "$@" \
		| sed -r 's/\x1b\[[0-9;]*m//g' | tr -d '\r' \
		| awk '{ if (length > m) m = length } END { print m + 0 }'
}
pty_color_auto_on() {
	python3 "$SCRIPT_DIR/pty-run.py" 80 24 -- "$B" --color auto -t hi "$WS_URL/echo" | grep -qF $'\x1b['
}
pty_clip_bounds() {
	local big w; big=$(printf 'x%.0s' $(seq 1 200))
	w=$(_pty_maxwidth 40 -- "$B" --clip -t "$big" "$WS_URL/echo")
	[[ "$w" -le 40 ]]
}
pty_noclip_overflows() {
	local big w; big=$(printf 'x%.0s' $(seq 1 200))
	w=$(_pty_maxwidth 40 -- "$B" -t "$big" "$WS_URL/echo")
	[[ "$w" -gt 40 ]]
}
wss_verify_ca() {
	local ca http; ca=$(mktemp); trap "rm -f '$ca'" RETURN
	http="${WS_URL/#ws:\/\//http://}"
	curl -fsS "$http/ca.pem" -o "$ca" || return 1
	SSL_CERT_FILE="$ca" "$B" -t hi "$WSS_URL/echo" >/dev/null 2>&1
}

# --- --file response-sink predicates ----------------------------------------
# --file opens O_EXCL, so each case needs a path that does not exist yet; every
# predicate mints its own temp dir and cleans up on RETURN.
file_record_measure() {
	local dir out; dir=$(mktemp -d); trap "rm -rf '$dir'" RETURN
	out="$dir/rec.jsonl"
	"$B" -t file-payload --file "$out" "$WS_URL/echo" >/dev/null 2>&1 || return 1
	[[ -s "$out" ]] && grep -q "file-payload" "$out"
}
file_record_rpc_compacts() {
	# A JSON-RPC response is recorded as a single compact NDJSON line.
	local dir out; dir=$(mktemp -d); trap "rm -rf '$dir'" RETURN
	out="$dir/rec.jsonl"
	"$B" --rpc-method m --file "$out" "$WS_URL/jsonrpc" >/dev/null 2>&1 || return 1
	[[ $(grep -c . "$out") -eq 1 ]] && grep -q '"result":"ok"' "$out"
}
file_record_stream() {
	# Stream mode records one NDJSON line per received frame.
	local dir out; dir=$(mktemp -d); trap "rm -rf '$dir'" RETURN
	out="$dir/rec.jsonl"
	"$B" stream -t s -c 3 --file "$out" "$WS_URL/stream?rate=10" >/dev/null 2>&1 || return 1
	[[ $(grep -c . "$out") -ge 3 ]]
}
file_additive_to_json() {
	# --file is orthogonal to -o: stdout keeps the JSON contract while the sink
	# records the verbatim response body, not the JSON envelope.
	local dir out; dir=$(mktemp -d); trap "rm -rf '$dir'" RETURN
	out="$dir/rec.jsonl"
	"$B" -o json -t file-payload --file "$out" "$WS_URL/echo" >"$OUTF" 2>/dev/null || return 1
	grep -q "file-payload" "$out" || return 1
	grep -q "durations_ms" "$out" && return 1      # sink is the body, not the envelope
	if [[ $HAVE_JQ -eq 1 ]]; then
		jq -es 'any(.[]; .durations_ms.total != null)' "$OUTF" >/dev/null || return 1
	fi
	return 0
}
file_payloadless_leaves_nothing() {
	# A ping-mode measure records no payload; the empty capture is removed so it
	# does not block the next run.
	local dir out; dir=$(mktemp -d); trap "rm -rf '$dir'" RETURN
	out="$dir/rec.jsonl"
	"$B" --file "$out" "$WS_URL/echo" >/dev/null 2>&1 || return 1
	[[ ! -e "$out" ]]
}
file_rejects_existing() {
	# O_EXCL: --file refuses to clobber an existing path, and says so.
	local dir out; dir=$(mktemp -d); trap "rm -rf '$dir'" RETURN
	out="$dir/rec.jsonl"; : >"$out"
	"$B" -t hi --file "$out" "$WS_URL/echo" >/dev/null 2>"$ERRF" && return 1
	grep -Eq "response file|file exists" "$ERRF"
}

# --- ping-mode predicates ----------------------------------------------------
ping_json_shape() {
	# Exactly one ping_reply per ping, and ping_summary is the final record.
	"$B" ping -c 2 -i 50ms -o json "$WS_URL/echo" 2>/dev/null | jq -es '
		([.[] | select(.type == "ping_reply")]   | length == 2) and
		(last | .type == "ping_summary" and .loss_pct == 0)' >/dev/null
}
ping_deadline_ends() {
	# No -c: only --deadline bounds the run, so this is the row that catches a
	# broken -w. timeout(1) is the hang backstop that keeps the harness moving.
	timeout 5 "$B" ping -w 500ms -i 100ms "$WS_URL/echo" >"$OUTF" 2>/dev/null || return 1
	grep -q "STATS" "$OUTF"
}
ping_timeout_survivable() {
	# A missed pong is survivable: the run outlives the first timeout and keeps
	# pinging until --count, so both pings time out instead of the first one
	# killing the connection.
	timeout 15 "$B" ping -c 2 -i 100ms --timeout 400ms --close-timeout 300ms \
		"$WS_URL/push?rate=20" >"$OUTF" 2>/dev/null && return 1
	[[ $(grep -c "timeout: seq=" "$OUTF") -eq 2 ]] && grep -q "2 sent, 0 received" "$OUTF"
}
ping_total_loss() {
	# /push never reads, so it never pongs: the single ping times out on its own
	# deadline, the run exits 1 (the liveness-gate contract), and the summary
	# reports total loss. --close-timeout bounds the non-echoing teardown.
	timeout 10 "$B" ping -c 1 -i 100ms --timeout 500ms --close-timeout 300ms \
		"$WS_URL/push?rate=20" >"$OUTF" 2>/dev/null && return 1
	grep -q "1 sent, 0 received, 100.0% loss" "$OUTF"
}

echo "wsstat soak (structured matrix) against $WS_URL"
echo "binary: $B"

# ===========================================================================
section "POSITIVE: measure-mode flags (each flag, both aliases)"
ok "bare ping (no message)"        -- "$B" "$WS_URL/echo"
ok "measure explicit subcommand"   -- "$B" measure -t hi "$WS_URL/echo"
ok "-t text"                       -- "$B" -t hi "$WS_URL/echo"
ok "--text text (alias)"           -- "$B" --text hi "$WS_URL/echo"
ok "--rpc-method"                  -- "$B" --rpc-method eth_blockNumber "$WS_URL/jsonrpc"
ok "-c burst"                      -- "$B" -t hi -c 5 "$WS_URL/echo"
ok "--count burst (alias)"         -- "$B" -t hi --count 5 "$WS_URL/echo"
ok "-o text"                       -- "$B" -o text -t hi "$WS_URL/echo"
ok "-o json"                       -- "$B" -o json -t hi "$WS_URL/echo"
ok "-o raw (with message)"         -- "$B" -o raw -t hi "$WS_URL/echo"
ok "--output json (alias)"         -- "$B" --output json -t hi "$WS_URL/echo"
ok "--body auto"                   -- "$B" --body auto -t hi "$WS_URL/echo"
ok "--body compact"                -- "$B" --body compact -t hi "$WS_URL/echo"
ok "--color auto"                  -- "$B" --color auto -t hi "$WS_URL/echo"
ok "--color always"                -- "$B" --color always -t hi "$WS_URL/echo"
ok "--color never"                 -- "$B" --color never -t hi "$WS_URL/echo"
ok "-q quiet"                      -- "$B" -q -t hi "$WS_URL/echo"
ok "--quiet (alias)"               -- "$B" --quiet -t hi "$WS_URL/echo"
ok "-v verbose"                    -- "$B" -v -t hi "$WS_URL/echo"
ok "--verbose (alias)"             -- "$B" --verbose -t hi "$WS_URL/echo"
ok "-vv very verbose"              -- "$B" -vv -t hi "$WS_URL/echo"
ok "-H header"                     -- "$B" -t hi -H "X-Smoke: 1" "$WS_URL/headers"
ok "--header (alias)"              -- "$B" -t hi --header "X-Smoke: 1" "$WS_URL/headers"
ok "-H repeated"                   -- "$B" -t hi -H "X-Smoke: 1" -H "X-Other: 2" "$WS_URL/headers"
ok "--resolve override"            -- "$B" -t hi --resolve "mock:17080:127.0.0.1" "ws://mock:17080/echo"
ok "--timeout positive"           -- "$B" --timeout 5s -t hi "$WS_URL/echo"
ok "--close-timeout positive"     -- "$B" --close-timeout 2s -t hi "$WS_URL/echo"
ok "--close-timeout >5s (capped)" -- "$B" --close-timeout 9s -t hi "$WS_URL/echo"

section "POSITIVE: representative valid combinations (measure)"
ok "rpc + -o json"                 -- "$B" --rpc-method m -o json "$WS_URL/jsonrpc"
ok "rpc + -o raw"                  -- "$B" --rpc-method m -o raw "$WS_URL/jsonrpc"
ok "rpc + --body compact"          -- "$B" --rpc-method m --body compact "$WS_URL/jsonrpc"
ok "-t + -c + -v"                  -- "$B" -t hi -c 3 -v "$WS_URL/echo"
ok "-t + -vv + --color always"     -- "$B" -t hi -vv --color always "$WS_URL/echo"
ok "-q + -o text"                  -- "$B" -q -o text -t hi "$WS_URL/echo"
ok "-c + -H + --timeout"           -- "$B" -t hi -c 2 -H "X-Smoke: 1" --timeout 5s "$WS_URL/headers"

section "POSITIVE: stream-mode flags (each flag, both aliases)"
ok "stream --once"                 -- "$B" stream --once -t s "$WS_URL/stream?rate=10"
ok "stream -c"                     -- "$B" stream -c 3 -t s "$WS_URL/stream?rate=10"
ok "stream --count (alias)"        -- "$B" stream --count 3 -t s "$WS_URL/stream?rate=10"
ok "stream -b buffer"              -- "$B" stream -b 8 -c 3 -t s "$WS_URL/stream?rate=10"
ok "stream --buffer (alias)"       -- "$B" stream --buffer 8 -c 3 -t s "$WS_URL/stream?rate=10"
ok "stream --summary-interval"     -- "$B" stream --summary-interval 1s -c 5 -t s "$WS_URL/stream?rate=5"
ok "stream -o raw"                 -- "$B" stream -o raw -c 3 -t s "$WS_URL/stream?rate=10"
ok "stream -o json"                -- "$B" stream -o json -c 3 -t s "$WS_URL/stream?rate=10"
ok "stream --color always"         -- "$B" stream --color always -c 3 -t s "$WS_URL/stream?rate=10"
ok "stream -H + -c"                -- "$B" stream -H "X-Smoke: 1" -c 3 -t s "$WS_URL/stream?rate=10"
ok "stream --timeout + -c"         -- "$B" stream --timeout 5s -c 3 -t s "$WS_URL/stream?rate=10"
ok "stream once + -o json"         -- "$B" stream --once -o json -t s "$WS_URL/stream?rate=10"
ok "stream repeated -t"            -- "$B" stream -c 2 --send-delay 100ms -t "$SUB_BTC" -t "$UNSUB_BTC" "$WS_URL/subscriptions"
ok "stream repeated --text (alias)" -- "$B" stream -c 2 --send-delay 100ms --text "$SUB_BTC" --text "$UNSUB_BTC" "$WS_URL/subscriptions"
ok "stream repeated -t default delay" -- "$B" stream -c 2 -t "$SUB_BTC" -t "$UNSUB_BTC" "$WS_URL/subscriptions"

# ===========================================================================
section "POSITIVE: ping-mode flags (each flag, both aliases)"
ok "ping -c bounded"           -- "$B" ping -c 3 -i 50ms "$WS_URL/echo"
ok "ping --count (alias)"      -- "$B" ping --count 3 -i 50ms "$WS_URL/echo"
ok "ping --interval (alias)"   -- "$B" ping -c 2 --interval 50ms "$WS_URL/echo"
ok "ping -w deadline"          -- timeout 5 "$B" ping -w 400ms -i 100ms "$WS_URL/echo"
ok "ping --deadline (alias)"   -- timeout 5 "$B" ping --deadline 400ms -i 100ms "$WS_URL/echo"
ok "ping -o json"              -- "$B" ping -c 2 -i 50ms -o json "$WS_URL/echo"
ok "ping -q"                   -- "$B" ping -c 2 -i 50ms -q "$WS_URL/echo"
ok "ping -v"                   -- "$B" ping -c 2 -i 50ms -v "$WS_URL/echo"
ok "ping wss + -k"             -- "$B" ping -c 2 -i 50ms -k "$WSS_URL/echo"

# ===========================================================================
section "REJECT: mutually exclusive / invalid-value rules"
reject "-q + -v"             "cannot be combined"            -- "$B" -q -v -t hi "$WS_URL/echo"
reject "-q + -vv"            "cannot be combined"            -- "$B" -q -vv -t hi "$WS_URL/echo"
reject "-t + --rpc-method"   "mutually exclusive"            -- "$B" -t a --rpc-method b "$WS_URL/echo"
reject "--timeout negative"  "zero or greater"               -- "$B" --timeout -1s -t hi "$WS_URL/echo"
reject "--close-timeout neg" "zero or greater"               -- "$B" --close-timeout -1s -t hi "$WS_URL/echo"
reject "--color invalid"     "auto, always, or never"        -- "$B" --color bogus -t hi "$WS_URL/echo"
reject "measure -c 0"        "greater than 0"                -- "$B" -c 0 -t hi "$WS_URL/echo"
reject "measure -c negative" "greater than 0"                -- "$B" -c -1 -t hi "$WS_URL/echo"
reject "no URL argument"     "exactly one URL"               -- "$B" -t hi
reject "two URL arguments"   "exactly one URL"               -- "$B" -t hi "$WS_URL/echo" "$WS_URL/echo"
reject "-o raw measure no msg" "requires --text or --rpc-method" -- "$B" -o raw "$WS_URL/echo"

section "REJECT: axis purity (text-only flags under -o json|raw)"
for o in json raw; do
	reject "-o $o + --body"    "only applies to text output" -- "$B" -o "$o" --body compact -t hi "$WS_URL/echo"
	reject "-o $o + --clip"    "only applies to text output" -- "$B" -o "$o" --clip -t hi "$WS_URL/echo"
	reject "-o $o + -q"        "only applies to text output" -- "$B" -o "$o" -q -t hi "$WS_URL/echo"
	reject "-o $o + -v"        "only applies to text output" -- "$B" -o "$o" -v -t hi "$WS_URL/echo"
	reject "-o $o + --verbose" "only applies to text output" -- "$B" -o "$o" --verbose -t hi "$WS_URL/echo"
	reject "-o $o + -vv"       "only applies to text output" -- "$B" -o "$o" -vv -t hi "$WS_URL/echo"
done

section "REJECT: stream-mode rules"
reject "stream -c negative"        "zero or greater"           -- "$B" stream -c -1 -t s "$WS_URL/stream"
reject "stream --once + -c"        "cannot be combined"        -- "$B" stream --once -c 3 -t s "$WS_URL/stream"
reject "stream --once + --count"   "cannot be combined"        -- "$B" stream --once --count 3 -t s "$WS_URL/stream"
reject "stream summary + -o raw"   "no effect with -o raw"     -- "$B" stream --summary-interval 1s -o raw -t s "$WS_URL/stream"
reject "stream --send-delay single -t" "no effect without repeated -t" -- "$B" stream --send-delay 1s -t s "$WS_URL/stream"
reject "stream --send-delay no -t"     "no effect without repeated -t" -- "$B" stream --send-delay 1s "$WS_URL/stream"
reject "stream --send-delay negative"  "zero or greater"               -- "$B" stream --send-delay -1s -t a -t b "$WS_URL/stream"
reject "measure repeated -t"           "stream subcommand"             -- "$B" -t a -t b "$WS_URL/echo"

section "REJECT: ping-mode rules"
# Flags without meaning in ping mode are stub-registered and rejected outright;
# a row per flag guards against a regression back to silent accept.
reject "ping + -t"            "not supported in ping mode" -- "$B" ping -t hi "$WS_URL/echo"
reject "ping + --text"        "not supported in ping mode" -- "$B" ping --text hi "$WS_URL/echo"
reject "ping + --rpc-method"  "not supported in ping mode" -- "$B" ping --rpc-method m "$WS_URL/echo"
reject "ping + --rpc-version" "not supported in ping mode" -- "$B" ping --rpc-version 1.0 "$WS_URL/echo"
reject "ping + --file"        "not supported in ping mode" -- "$B" ping --file rec.ndjson "$WS_URL/echo"
reject "ping + --body"        "not supported in ping mode" -- "$B" ping --body compact "$WS_URL/echo"
reject "ping + --clip"        "not supported in ping mode" -- "$B" ping --clip "$WS_URL/echo"
reject "ping + -o raw"        "no meaning in ping mode"    -- "$B" ping -o raw "$WS_URL/echo"
reject "ping -c negative"     "zero or greater"            -- "$B" ping -c -1 "$WS_URL/echo"
reject "ping -w zero"         "greater than zero"          -- "$B" ping -w 0s "$WS_URL/echo"
reject "ping -i below floor"  "at least 10ms"              -- "$B" ping -i 1ms "$WS_URL/echo"
# Stream-only flags under ping: the flag package must reject them outright.
reject "ping + --once"             "not defined" -- "$B" ping --once "$WS_URL/echo"
reject "ping + --buffer"           "not defined" -- "$B" ping --buffer 8 "$WS_URL/echo"
reject "ping + --summary-interval" "not defined" -- "$B" ping --summary-interval 1s "$WS_URL/echo"
reject "ping + --send-delay"       "not defined" -- "$B" ping --send-delay 1s "$WS_URL/echo"

section "REJECT: v2 flags removed in v3"
reject "-subscribe"      "removed in v3" -- "$B" -subscribe -t hi "$WS_URL/stream"
reject "-s"              "removed in v3" -- "$B" -s -t hi "$WS_URL/stream"
reject "-subscribe-once" "removed in v3" -- "$B" -subscribe-once -t hi "$WS_URL/stream"
reject "-format"         "removed in v3" -- "$B" -format json -t hi "$WS_URL/echo"
reject "-f"              "removed in v3" -- "$B" -f json -t hi "$WS_URL/echo"
reject "-no-tls"         "removed in v3" -- "$B" -no-tls -t hi "$WS_URL/echo"

section "REJECT: cross-mode flags must error, not be silently dropped"
# Stream-only flags under measure: the flag package must reject them outright.
reject "measure + --once"             "not defined" -- "$B" --once -t hi "$WS_URL/echo"
reject "measure + -b"                 "not defined" -- "$B" -b 8 -t hi "$WS_URL/echo"
reject "measure + --buffer"           "not defined" -- "$B" --buffer 8 -t hi "$WS_URL/echo"
reject "measure + --summary-interval" "not defined" -- "$B" --summary-interval 1s -t hi "$WS_URL/echo"
reject "measure + --send-delay"       "not defined" -- "$B" --send-delay 1s -t hi "$WS_URL/echo"

# ===========================================================================
section "EFFECT: output contracts produce the right bytes"
outeq    "-o raw echo is verbatim"       "hi"               -- "$B" -o raw -t hi "$WS_URL/echo"
outhas   "-o raw rpc is decoded JSON"    '"result":"ok"'    -- "$B" -o raw --rpc-method m "$WS_URL/jsonrpc"
outhas   "-q emits the response"         "hi"               -- "$B" -q -t hi "$WS_URL/echo"
outlacks "-q suppresses timing block"    "Total time"       -- "$B" -q -t hi "$WS_URL/echo"
outhas   "default shows timing block"    "Total time"       -- "$B" -t hi "$WS_URL/echo"
outhas   "-v adds timing diagram"        "Total"            -- "$B" -v -t hi "$WS_URL/echo"
# --body shape: a nested JSON echo renders multi-line under auto, one line under compact.
outhas   "--body auto pretty-prints"     '^  "a": \{'                      -- "$B" --body auto    -t '{"a":{"b":1,"c":2}}' "$WS_URL/echo"
outhas   "--body compact one-lines"      'Response: \{"a":\{"b":1,"c":2\}\}' -- "$B" --body compact -t '{"a":{"b":1,"c":2}}' "$WS_URL/echo"

section "EFFECT: --color honors the mode without a TTY"
outhas   "--color always emits ANSI"     $'\x1b\\['         -- "$B" --color always -t hi "$WS_URL/echo"
outlacks "--color never has no ANSI"     $'\x1b\\['         -- "$B" --color never -v -t hi "$WS_URL/echo"
outlacks "--color auto off when piped"   $'\x1b\\['         -- "$B" --color auto -t hi "$WS_URL/echo"

section "EFFECT: stream output contracts"
# -o raw stream emits only verbatim JSON frames: no header, no summary block.
pred "stream -o raw is header/summary-free" stream_raw_clean
# --summary-interval fires at least one periodic summary before the count ends.
pred "stream --summary-interval fires periodically" summary_interval_fires
# Repeated -t: three staggered frames on one connection, replies in send order.
pred "stream repeated -t conversation is ordered" multi_send_ordered
# Repeated -t edge: the receive limit ends the run before the second send fires.
pred "stream -c 1 skips the remaining sends" recv_limit_skips_sends
# --send-delay observably staggers the sends rather than being dropped.
pred "stream --send-delay staggers sends" send_delay_staggers

section "EFFECT: -o json is valid, schema-stable JSON"
if [[ $HAVE_JQ -eq 1 ]]; then
	pred "measure -o json has durations_ms.total" json_measure_total
	pred "stream  -o json frames carry method"    json_stream_method
else
	S "-o json structural checks" "jq not installed"
fi

# ===========================================================================
section "EFFECT: ping output"
outhas   "ping prints pong lines"      'pong: seq=1 rtt='                             -- "$B" ping -c 2 -i 50ms "$WS_URL/echo"
outhas   "ping STATS summary"          'STATS .*2 sent, 2 received, 0.0% loss'        -- "$B" ping -c 2 -i 50ms "$WS_URL/echo"
outhas   "ping rtt stats line"         'rtt: min=.*ms avg=.*ms max=.*ms stddev=.*ms'  -- "$B" ping -c 2 -i 50ms "$WS_URL/echo"
outlacks "ping -q drops pong lines"    'pong: seq='                                   -- "$B" ping -c 2 -i 50ms -q "$WS_URL/echo"
outhas   "ping -q keeps the summary"   'STATS '                                       -- "$B" ping -c 2 -i 50ms -q "$WS_URL/echo"
pred "ping -w ends an unbounded run"                ping_deadline_ends
pred "ping missed pong is survivable (2 timeouts)"   ping_timeout_survivable
pred "ping total loss: exit 1 and 100% loss summary" ping_total_loss
if [[ $HAVE_JQ -eq 1 ]]; then
	pred "ping -o json shape (replies + final summary)" ping_json_shape
else
	S "ping -o json shape" "jq not installed"
fi

# ===========================================================================
section "EFFECT: --file records response payloads as NDJSON"
# --file is additive and orthogonal to -o; it has no validation rules beyond
# O_EXCL, so coverage is behavioral: what lands in the sink, and what does not.
pred "measure records the echoed payload"       file_record_measure
pred "rpc response recorded as one compact line" file_record_rpc_compacts
pred "stream records one line per frame"         file_record_stream
pred "additive to -o json (body, not envelope)"  file_additive_to_json
pred "payload-less run leaves no empty file"     file_payloadless_leaves_nothing
pred "refuses to overwrite an existing path"     file_rejects_existing

# ===========================================================================
section "EFFECT (PTY): TTY-only flags exercised under a real terminal"
if [[ $HAVE_PTY -eq 1 ]]; then
	pred "PTY: --color auto emits ANSI on a TTY"   pty_color_auto_on
	pred "PTY: --clip bounds line width"           pty_clip_bounds
	pred "PTY: without --clip a line overflows"    pty_noclip_overflows
else
	S "PTY clip/color effect checks" "python3 not installed"
fi

# ===========================================================================
section "EFFECT: -H header reaches the server, --resolve dials the override"
outhas "-H value is transmitted"  "smoke-value" -- "$B" -q -t hi -H "X-Smoke: smoke-value" "$WS_URL/headers"
outhas "--resolve reaches mock"   "Response"    -- "$B" -t hi --resolve "mock:17080:127.0.0.1" "ws://mock:17080/echo"

# ===========================================================================
section "EFFECT: TLS verification posture"
ok       "wss + -insecure connects"  -- "$B" -insecure -t hi "$WSS_URL/echo"
ok       "wss + -k connects"         -- "$B" -k -t hi "$WSS_URL/echo"
reject   "wss verify rejects self-signed" "." -- "$B" -t hi "$WSS_URL/echo"
if [[ $HAVE_CURL -eq 1 ]]; then
	pred "wss verifies via trusted CA" wss_verify_ca
else
	S "wss verifies via trusted CA" "curl not installed"
fi

# ===========================================================================
section "EFFECT: failure paths surface as non-zero exit"
reject "short --timeout trips on /slow" "." -- "$B" --timeout 1s -t hi "$WS_URL/slow"
reject "abrupt close is an error"       "." -- "$B" -t hi "$WS_URL/close-abrupt"

# ===========================================================================
echo ""
echo "Results: $PASS passed, $FAIL failed, $SKIP skipped"
if [[ $FAIL -gt 0 ]]; then
	echo ""
	echo "Failures:"
	for f in "${FAILURES[@]}"; do printf "  - %s\n" "$f"; done
fi
exit $((FAIL > 0 ? 1 : 0))
