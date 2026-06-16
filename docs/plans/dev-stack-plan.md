# wsstat dev stack: mock WS server + smoke-test harness

- **Date:** 2026-06-16
- **Commit:** b5bbeba (line numbers are "as of" this commit; re-find symbols if the tree moved)
- **Branch:** main
- **Module:** `github.com/jkbrsn/wsstat/v2`

## Problem

wsstat has unit/integration tests but no way to exercise the **built CLI binary** end-to-end against a live server across its full feature surface. This matters now for two reasons: (1) the upcoming `gorilla → coder/websocket` migration (`docs/plans/coder-websocket-migration-plan.md`) needs a regression net that proves no user-facing behavior changed, and (2) several migration risks (the 32 KiB read limit, the RFC 6455 close handshake, timeout/context handling) are only observable against a real peer.

Build a `dev/` stack — matching the established pattern in `/home/jakob/versioned/iris/dev` and `/home/jakob/versioned/bcm-probe/dev` — consisting of a Dockerized mock WebSocket server and a `smoke-test.sh` that fires the real `./bin/wsstat` binary through every CLI feature against that server.

## Goals

- A single mock WS server (no TLS initially) exposing one URL path per behavior, so each wsstat feature maps to a deterministic endpoint.
- A `smoke-test.sh` that builds nothing itself, runs `./bin/wsstat` (host-built), and asserts pass/fail per feature with a final tally and non-zero exit on failure.
- A `run.sh` orchestrator that brings the stack up with healthchecks and tears it down cleanly.
- Coverage of every CLI flag/mode listed in the feature matrix below, including the two coder-migration risk areas (large frames, abrupt close).

## Non-Goals

- **No TLS** in the first iteration (designed so a `wss://` port + self-signed cert can be added later — see Open Questions).
- No datastore, migrations, or result-inspection scripts (wsstat persists nothing — drop iris/bcm-probe's ClickHouse/RabbitMQ/Postgres/`query.sh`/seed scripts entirely).
- No containerization of wsstat itself; the binary under test is the host build (decided).
- No blockchain/JSON-RPC-billing/gRPC specifics from the source stacks.

## Decisions (settled)

- **Mock is a separate Go module** (`dev/mock-server/go.mod`), like both source repos — keeps `coder/websocket` out of wsstat's `go.mod` and builds in Docker isolation.
- **Binary under test is host-built** (`make build` → `./bin/wsstat`); only the mock runs in Docker. Fast iteration, tests the real local binary.
- **Mock uses `coder/websocket`** regardless of wsstat's own migration timing — the server side is independent, and iris's `dev/mock-backend/ws.go` is a proven coder-based template to adapt.
- **`/stream` waits for an initial subscribe frame** before pumping notifications (realistic WS subscription model); smoke cases always send one via `-t`. See section 1.
- **Smoke assertions: exit codes + `jq`** on timing-bearing (`-f json`) cases, `jq` skip-guarded when absent.
- **Make target: `make smoke`** → `./dev/run.sh`.

## Proposed layout

```
dev/
├── README.md                 # quick start, port, endpoint table
├── run.sh                    # stack up/down with cleanup trap
├── docker-compose.yaml       # one service: mock-ws (no TLS)
├── smoke-test.sh             # wsstat feature matrix vs. mock paths
└── mock-server/
    ├── Dockerfile            # two-stage Go build (adapted from iris)
    ├── go.mod / go.sum       # separate module; requires coder/websocket
    └── main.go               # multi-path WS behaviors
```

Port convention: iris uses `19xxx`, bcm-probe `18xxx`. wsstat takes **`17xxx`** to avoid collisions when stacks run side by side. Mock WS on host port `17080` (container `:8080`), reserving `17443` for a future `wss://` listener.

## Detailed Design

### 1. Mock server (`dev/mock-server/main.go`)

Adapt the structure of `iris/dev/mock-backend/ws.go` — `websocket.Accept` + a per-connection read loop + a `wsSession` mutex serializing writes (coder permits one concurrent writer) + a subscription pump with cancelable contexts. The key change: **route behavior by URL path**, not by JSON-RPC method, since wsstat is a generic client.

```go
func main() {
    mux := http.NewServeMux()
    mux.HandleFunc("/echo",         handle(echoBehavior))
    mux.HandleFunc("/jsonrpc",      handle(jsonrpcBehavior))
    mux.HandleFunc("/stream",       handle(streamBehavior))   // pushes N msgs/sec
    mux.HandleFunc("/large",        handle(largeBehavior))    // reply > 32 KiB
    mux.HandleFunc("/slow",         handle(slowBehavior))     // delay past timeout
    mux.HandleFunc("/headers",      handle(headersBehavior))  // reflect/require header
    mux.HandleFunc("/close-abrupt", handle(closeAbruptBehavior))
    mux.HandleFunc("/healthz",      func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
    http.ListenAndServe(":8080", mux)
}

// handle wraps a behavior with Accept + SetReadLimit(-1) + the read/write loop.
func handle(b behavior) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
        if err != nil {
            return
        }
        conn.SetReadLimit(-1)
        b(r, conn) // owns the conn lifecycle; defers conn.Close(StatusNormalClosure, "")
    }
}
```

Behaviors (each a `func(*http.Request, *websocket.Conn)`):

- **echo** — read frame, echo identical bytes back. Serves `-text` and `-count` burst.
- **jsonrpc** — parse a JSON-RPC request, reply `{"jsonrpc":"2.0","id":<id>,"result":"ok"}`; `-32700` on parse error. Serves `-rpc-method`.
- **stream** — **wait for an initial subscribe frame**, then start a 1/sec pump of JSON notifications until the client disconnects; honor a `?rate=` / `?count=` query if useful. This mirrors how real WS subscriptions work (client sends a subscribe request first). Serves `-subscribe`, `-subscribe-once`, `-summary-interval` — the smoke cases always pass an initial message (`-t subscribe`) so a frame arrives. Consequence: a bare `wsstat -subscribe` with no payload sends nothing (`subscription.go:210` only sends when `len(Payload) > 0`), so that edge mode is not exercised by `/stream` — acceptable. Reuse iris's `subRegistry`/`pump` cancel pattern (`ws.go:140-200`).
- **large** — reply with a single frame `> 32 KiB` (e.g. 1 MiB, mirroring iris's `wsLargeResponseBytes = 1 << 20`). Exercises the coder `SetReadLimit(-1)` path on the client after migration.
- **slow** — sleep longer than a short client timeout before replying (e.g. 3s). Serves `-timeout` failure-path testing.
- **headers** — echo a chosen request header back in the first frame (e.g. reflect `X-Smoke`), so the test can assert `-H/-header` is transmitted.
- **close-abrupt** — after the first frame, call `conn.CloseNow()` (no close frame), mirroring iris's `ws_closeAbrupt`. Tests client error handling and, post-migration, the close-handshake behavior.

`wsSession.write` mutex and the cancelable pump come straight from iris (`ws.go:137-205`).

### 2. Dockerfile (`dev/mock-server/Dockerfile`)

Adapt `iris/dev/mock-backend/Dockerfile` verbatim except the exposed port:

```dockerfile
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -o /mock-server .

FROM alpine:3.21
COPY --from=build /mock-server /mock-server
EXPOSE 8080
CMD ["/mock-server"]
```

### 3. docker-compose.yaml

One service, with a healthcheck so `--wait` blocks until ready:

```yaml
services:
  mock-ws:
    build: ./mock-server
    ports:
      - "17080:8080"
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:8080/healthz"]
      interval: 2s
      timeout: 2s
      retries: 10
```

(`alpine` ships `wget`; if the scratch-style image lacks it, use a tiny Go `-healthcheck` flag on the binary instead.)

### 4. run.sh

Take iris's orchestrator shape (`iris/dev/run.sh`): `set -euo pipefail`, `SCRIPT_DIR`/`REPO_DIR`, `trap cleanup EXIT` → `docker compose down`, double-run guard, `docker compose up -d --build --wait`. Because wsstat is a short-lived CLI (not a daemon), default behavior differs from iris: bring the stack up, then **run `smoke-test.sh`** and exit with its status. Support `./dev/run.sh up` to leave the stack running for manual `wsstat` invocations.

```bash
docker compose -f "$SCRIPT_DIR/docker-compose.yaml" up -d --build --wait
if [[ "${1:-}" == "up" ]]; then
    echo "Mock WS server ready at ws://localhost:17080/<path>. Ctrl+C to tear down."
    sleep infinity & wait
fi
cd "$REPO_DIR" && make build
WS_URL="ws://localhost:17080" "$SCRIPT_DIR/smoke-test.sh"
```

### 5. smoke-test.sh

Reuse iris's framework skeleton (`iris/dev/smoke-test.sh:45-67` for `check`/`skip`/counters; `:613-616` for the tally + exit). Replace the curl body with `./bin/wsstat` invocations. Config is env-overridable:

```bash
#!/usr/bin/env bash
set -euo pipefail
WS_URL="${WS_URL:-ws://localhost:17080}"
WSSTAT="${WSSTAT:-./bin/wsstat}"
PASS=0 FAIL=0 SKIP=0

check() { local name="$1"; shift; if "$@" >/dev/null 2>&1; then printf "  PASS  %s\n" "$name"; ((PASS++))||true; else printf "  FAIL  %s\n" "$name"; ((FAIL++))||true; fi; }
```

Each case is a small function asserting on **exit code and/or stdout**. Examples:

```bash
# text echo round-trip
check "text echo"        "$WSSTAT" -t hello "$WS_URL/echo"
# JSON-RPC
check "rpc-method"       "$WSSTAT" -rpc-method eth_blockNumber "$WS_URL/jsonrpc"
# burst
check "burst count=5"    "$WSSTAT" -t hi -c 5 "$WS_URL/echo"
# json output parses
check "json format"      bash -c "$WSSTAT -f json -t hi $WS_URL/echo | jq -e .totalTime"
# subscription, first event only (sends an initial subscribe frame)
check "subscribe-once"   "$WSSTAT" -subscribe-once -t subscribe "$WS_URL/stream"
# timeout failure path (expect non-zero exit)
check "timeout trips"    bash -c "! $WSSTAT -timeout 1s -t hi $WS_URL/slow"
# header transmitted (server reflects X-Smoke)
check "custom header"    bash -c "$WSSTAT -t hi -H 'X-Smoke: 1' $WS_URL/headers | grep -q 1"
# DNS resolve override -> 127.0.0.1
check "resolve override" "$WSSTAT" -t hi -resolve "mock:17080:127.0.0.1" "ws://mock:17080/echo"
# large frame (>32 KiB) handled
check "large frame"      "$WSSTAT" -rpc-method ws_large "$WS_URL/large"
# abrupt close handled without hang/panic
check "abrupt close"     bash -c "! $WSSTAT -t hi $WS_URL/close-abrupt"
```

End with iris's tally:

```bash
echo "Results: $PASS passed, $FAIL failed, $SKIP skipped"
exit $((FAIL > 0 ? 1 : 0))
```

Guard `jq`-dependent cases with `skip` when `jq` is absent, mirroring iris's "build the probe on demand, skip if Go unavailable" pattern.

## Feature → endpoint → test matrix

The heart of the regression net. Every CLI flag/mode maps to at least one case.

| wsstat flag / mode | Mock path | Assertion |
|---|---|---|
| `-t / -text` | `/echo` | echo returns sent text; exit 0 |
| `-c / -count` (burst) | `/echo` | N round-trips; exit 0 |
| `-rpc-method` | `/jsonrpc` | JSON-RPC result returned; exit 0 |
| default ping/pong | `/echo` | library auto-pong; exit 0 |
| `-s / -subscribe` (+ `-t`) | `/stream` | sends subscribe frame, receives streamed events |
| `-subscribe-once` (+ `-t`) | `/stream` | sends subscribe frame, exits after first event |
| `-summary-interval` (+ `-t`) | `/stream` | periodic summaries printed |
| `-timeout` | `/slow` | non-zero exit, clean error (no hang) |
| `-f json` | `/echo` | stdout is valid JSON with timing fields |
| `-f raw` / `-f auto` | `/echo` | expected format shape |
| `-H / -header` | `/headers` | header reflected in response |
| `-resolve HOST:PORT:ADDR` | `/echo` | dial via override to 127.0.0.1 |
| `-b / -buffer` (+ `-t`) | `/stream` | runs with custom buffer; exit 0 |
| large frame (>32 KiB) | `/large` | full payload received (coder read-limit) |
| abrupt server close | `/close-abrupt` | client errors cleanly, no hang/panic |
| `-q` / `-v` / `-vv` | `/echo` | verbosity levels don't break output |

`-insecure / -k`, `-no-tls`, and `WithTLSConfig` are deferred with the TLS work (see Open Questions).

## New files

| File | Purpose |
|---|---|
| `dev/README.md` | Quick start, port `17080`, endpoint table |
| `dev/run.sh` | Stack up/down + run smoke test; `up` mode for manual use |
| `dev/docker-compose.yaml` | Single `mock-ws` service + healthcheck |
| `dev/smoke-test.sh` | Feature matrix against `./bin/wsstat` |
| `dev/mock-server/Dockerfile` | Two-stage Go build, expose `:8080` |
| `dev/mock-server/go.mod` / `go.sum` | Separate module; `coder/websocket` |
| `dev/mock-server/main.go` | Multi-path WS behaviors |

No changes to wsstat's own `go.mod`. Add a `make smoke` target invoking `./dev/run.sh`.

## Implementation Phases

1. **Mock server.** Scaffold `dev/mock-server/` (separate module), implement the 7 behaviors + `/healthz`, adapting iris's `Accept`/`wsSession`/`subRegistry` patterns. Verify locally with `go run .` + a manual `wsstat` call.
2. **Compose + run.sh.** Add the single-service compose with healthcheck and the orchestrator with cleanup trap and double-run guard.
3. **smoke-test.sh.** Port iris's `check`/`skip`/tally framework; implement the matrix cases against `./bin/wsstat`.
4. **README + make target.** Document quick start and the endpoint table; wire `make smoke`.

## Acceptance Criteria

- `./dev/run.sh` builds the mock image, waits for health, runs the suite, and tears the stack down on exit (including Ctrl+C).
- Every row in the feature matrix has a smoke-test case; suite prints `Results: X passed, …` and exits non-zero on any failure.
- Suite passes green against the **current gorilla-based** wsstat (establishes the pre-migration baseline).
- Re-running the suite unchanged after the coder migration stays green (the regression-net payoff), with the `/large` and `/close-abrupt` cases specifically guarding the read-limit and close-handshake behaviors.
- `dev/mock-server` builds in Docker with no dependency on wsstat's module.

## Deferred: TLS iteration

Not in the first build. When added: generate a self-signed cert at Docker **build time** (no committed cert fixtures) and add a `wss://` listener on `17443`. New smoke cases: `-insecure/-k` (skip-verify succeeds) and the default verify-fails path. This also unlocks regression coverage for `-no-tls` and `WithTLSConfig`. The compose/Dockerfile from phase 1-2 should leave room for a second port without restructuring.
