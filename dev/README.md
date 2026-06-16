# wsstat dev stack

A Dockerized mock WebSocket server plus a smoke-test harness that exercises the
host-built `./bin/wsstat` binary end to end across its full CLI surface.

## Quick start

```bash
make smoke          # build mock image, build wsstat, run the suite, tear down
./dev/run.sh        # same as `make smoke`
./dev/run.sh up     # leave the mock running for manual wsstat invocations
```

`run.sh up` keeps the stack alive until Ctrl+C:

```bash
./dev/run.sh up
./bin/wsstat -t hello ws://localhost:17080/echo
```

The mock listens on host port **17080** (container `:8080`). Port `17443` is
reserved for a future `wss://` listener (TLS is deferred). wsstat takes the
`17xxx` range to avoid colliding with sibling stacks (iris `19xxx`,
bcm-probe `18xxx`).

## Endpoints

Each path maps to one deterministic behavior so a single wsstat feature can be
tested in isolation.

| Path | Behavior |
|---|---|
| `/echo` | Echoes each frame back unchanged. Serves `-t`, `-c`, `-f`, `-resolve`, verbosity. |
| `/jsonrpc` | Replies with a JSON-RPC result (or `-32700` on parse error). Serves `-rpc-method`. |
| `/stream` | Waits for an initial frame, then pumps JSON notifications. `?rate=N` msgs/sec, `?count=N` cap. Serves `-s`, `-subscribe-once`, `-summary-interval`, `-b`. |
| `/large` | Replies with a valid JSON-RPC frame whose result exceeds 32 KiB. |
| `/slow` | Stalls 3s before replying. Serves the `-timeout` failure path. |
| `/headers` | Replies with the value of the `X-Smoke` request header. Serves `-H`/`-header`. |
| `/close-abrupt` | Reads one frame, then drops the connection with no close frame. |
| `/push` | Write-only / non-echoing peer: pumps JSON notifications, never reads. `?rate=N` msgs/sec. Exercises the close-handshake teardown bound (never echoes the client's Close). |
| `/healthz` | HTTP 200 for the compose healthcheck. |

## Layout

```
dev/
├── README.md            # this file
├── run.sh               # stack up/down + run smoke test; `up` mode for manual use
├── docker-compose.yaml  # single mock-ws service + healthcheck
├── smoke-test.sh        # wsstat feature matrix vs. mock paths
└── mock-server/         # separate Go module (coder/websocket); not in wsstat's go.mod
```

## Notes

- The mock is a **separate Go module** so `coder/websocket` never enters
  wsstat's own `go.mod`. The binary under test is the host build (`make build`).
- `smoke-test.sh` asserts on exit codes and stdout. `jq`-dependent cases skip
  cleanly when `jq` is absent. Override targets with `WS_URL` / `WSSTAT`.
- The mock listen port can be overridden with `PORT` (default `8080`); Docker
  keeps the default and maps it to `17080`.
- **TLS is deferred.** A `wss://` listener on `17443` with a build-time
  self-signed cert will unlock `-insecure`/`-k`, `-no-tls`, and `WithTLSConfig`
  coverage.
