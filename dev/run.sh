#!/usr/bin/env bash
# Bring up the wsstat dev stack (Dockerized mock WS server) and run the smoke
# suite against a host-built ./bin/wsstat.
# Usage: ./dev/run.sh        build mock, build wsstat, run smoke-test.sh, tear down
#        ./dev/run.sh up     leave the mock running for manual wsstat invocations
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(dirname "$SCRIPT_DIR")"
COMPOSE="docker compose -f $SCRIPT_DIR/docker-compose.yaml"

# Guard against a second instance: the EXIT trap runs `compose down`, which would
# tear the stack out from under a concurrent run.
if $COMPOSE ps --status running -q 2>/dev/null | grep -q .; then
	echo "ERROR: dev stack is already running." >&2
	echo "Stop the other instance first (Ctrl+C in its terminal), then retry." >&2
	exit 1
fi

cleanup() {
	echo ""
	echo "==> Stopping mock WS server..."
	$COMPOSE down
}
trap cleanup EXIT

echo "==> Starting mock WS server..."
$COMPOSE up -d --build --wait

if [[ "${1:-}" == "up" ]]; then
	echo ""
	echo "Mock WS server ready (Ctrl+C to tear down):"
	echo "  ws://localhost:17080/<path>   wss://localhost:17443/<path> (self-signed)"
	echo "Paths: /echo /jsonrpc /stream /large /slow /headers /close-abrupt /push"
	echo "Example: ./bin/wsstat -t hello ws://localhost:17080/echo"
	echo "Example: ./bin/wsstat -insecure -t hello wss://localhost:17443/echo"
	echo ""
	# Block until Ctrl+C so the EXIT trap tears the stack down.
	sleep infinity &
	wait
fi

echo "==> Building wsstat..."
cd "$REPO_DIR"
make build

echo "==> Running smoke test..."
echo ""
WS_URL="ws://localhost:17080" "$SCRIPT_DIR/smoke-test.sh"
