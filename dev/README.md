# wsstat dev stack

A Dockerized mock WebSocket server plus two harnesses that exercise the
host-built `./bin/wsstat` binary end to end across its full CLI surface:

- **smoke-test.sh** -- one assertion per feature; a fast "does each thing work".
- **soak-test.sh** -- a structured flag-combination matrix whose job is to catch
  *interactions*: every flag in each mode (both aliases), every documented
  validation rule asserted to actually reject (a rule that returns exit 0 is a
  silent accept), and effect checks for flags whose only failure mode is being
  ignored (`-o raw` bytes, `-q` suppression, `--color` ANSI, `--body` shape, and
  `--clip` width under a real PTY).

## Quick start

```bash
make smoke          # build mock image, build wsstat, run the smoke suite, tear down
make soak           # same stack, run the combination soak instead
./dev/run.sh        # same as `make smoke`
./dev/run.sh soak   # same as `make soak`
./dev/run.sh up     # leave the mock running for manual wsstat invocations
```

`run.sh up` keeps the stack alive until Ctrl+C:

```bash
./dev/run.sh up
./bin/wsstat -t hello ws://localhost:17080/echo
```

The mock listens on host port **17080** (container `:8080`, `ws://`) and
**17443** (container `:8443`, `wss://` with a startup-generated self-signed
cert). wsstat takes the `17xxx` range to avoid colliding with sibling stacks
(iris `19xxx`, bcm-probe `18xxx`).

## Endpoints

Each path maps to one deterministic behavior so a single wsstat feature can be
tested in isolation.

| Path | Behavior |
|---|---|
| `/echo` | Echoes each frame back unchanged. Serves `-t`, `-c`, `-o`/`--body`/`--clip`, `--resolve`, verbosity. |
| `/jsonrpc` | Replies with a JSON-RPC result (or `-32700` on parse error). Serves `--rpc-method`. |
| `/stream` | Waits for an initial frame, then pumps JSON notifications. `?rate=N` msgs/sec, `?count=N` cap. Serves the `stream` subcommand, `--once`, `--summary-interval`, `-b`. |
| `/large` | Replies with a valid JSON-RPC frame whose result exceeds 32 KiB. |
| `/slow` | Stalls 3s before replying. Serves the `-timeout` failure path. |
| `/headers` | Replies with the value of the `X-Smoke` request header. Serves `-H`/`-header`. |
| `/close-abrupt` | Reads one frame, then drops the connection with no close frame. |
| `/push` | Write-only / non-echoing peer: pumps JSON notifications, never reads. `?rate=N` msgs/sec. Exercises the close-handshake teardown bound (never echoes the client's Close). |
| `/healthz` | HTTP 200 for the compose healthcheck. |
| `/ca.pem` | The server's self-signed cert (PEM). Trust it via `SSL_CERT_FILE` to verify `wss://` without `-insecure`. |

Every path is served on both `ws://` (17080) and `wss://` (17443).

## Layout

```
dev/
├── README.md            # this file
├── run.sh               # stack up/down + run smoke|soak; `up` mode for manual use
├── docker-compose.yaml  # single mock-ws service + healthcheck
├── smoke-test.sh        # wsstat feature matrix vs. mock paths (one case per feature)
├── soak-test.sh         # wsstat flag-combination matrix (positive/reject/effect)
├── pty-run.py           # PTY wrapper with a fixed window size; drives --clip/--color
└── mock-server/         # separate Go module (coder/websocket); not in wsstat's go.mod
```

## Updating the harness when flags change

`smoke-test.sh` and `soak-test.sh` drive the binary by literal flag strings; they
are not derived from `flags.go`, so they do not auto-discover changes. Treat them
as part of a flag's definition of done, alongside `CHANGELOG.md` / `VERSION`.
When you touch the CLI surface, update `soak-test.sh` (and `smoke-test.sh` where
the feature warrants a fast gate):

- **New flag** -> add a `POSITIVE` case for both aliases (short and long). If the
  flag is one whose only failure mode is being silently ignored, add an `EFFECT`
  case asserting its observable behavior.
- **New validation rule** (in `resolveCommon` / `buildMeasure` / `buildStream`)
  -> add a `reject "<name>" "<message regex>" -- ...` case. Reject cases match on
  the error substring, so **rewording an error message fails the test** until the
  regex is updated; that is the tripwire that catches a rule going silent.
- **New mode / subcommand** -> mirror the per-mode `POSITIVE` block and the
  `cross-mode` `REJECT` block (a flag from one mode must error under the other,
  not be dropped).
- **New output contract or changed rendering** (`internal/app/output.go`) ->
  update the `EFFECT` byte/shape assertions (`-o raw` bytes, `--body` shape,
  `--color` ANSI, `-o json` schema).
- **New server behavior** -> only then touch `mock-server/main.go`. Endpoints map
  to behaviors, not flags, so most new flags reuse `/echo`, `/jsonrpc`, or
  `/stream`; add a path only when a flag needs a genuinely new peer response, and
  document it in the Endpoints table above.

A `SILENT ACCEPT` failure means a combination that should error returned exit 0;
that is the regression class the soak exists to catch, not a harness bug.

## Notes

- The mock is a **separate Go module** so `coder/websocket` never enters
  wsstat's own `go.mod`. The binary under test is the host build (`make build`).
- `smoke-test.sh` and `soak-test.sh` assert on exit codes and stdout/stderr.
  `jq`/`curl`/`python3`-dependent cases skip cleanly when those are absent.
  Override targets with `WS_URL` / `WSS_URL` / `WSSTAT` (so both run against a
  manually-started stack, e.g. `./dev/run.sh up` in another terminal).
- `soak-test.sh` uses `pty-run.py` to give `--clip`/`--color auto` a real
  terminal of a pinned width; without a PTY those flags no-op and their effect
  can't be observed. The PTY cases skip when `python3` is unavailable.
- The mock listen ports can be overridden with `PORT` (plain, default `8080`)
  and `TLS_PORT` (default `8443`); Docker keeps the defaults and maps them to
  `17080`/`17443`.
- **TLS** uses a self-signed cert generated in-memory at startup (no committed
  key material), with SANs for `localhost`/`127.0.0.1`/`::1`/`mock`. The smoke
  suite covers `-insecure`/`-k`, the verify-rejects-self-signed path, a verifying
  handshake trusted via `/ca.pem` + `SSL_CERT_FILE`, and `ws://` scheme defaulting.
