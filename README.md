# wsstat

[![Go Documentation](http://img.shields.io/badge/go-documentation-blue.svg?style=flat-square)][godocs]
[![Go Report Card](https://goreportcard.com/badge/github.com/jkbrsn/wsstat/v3)](https://goreportcard.com/report/github.com/jkbrsn/wsstat/v3)
[![MIT License](http://img.shields.io/badge/license-MIT-blue.svg?style=flat-square)][license]

[godocs]: http://godoc.org/github.com/jkbrsn/wsstat/v3
[license]: /LICENSE

This project provides a way to stat a WebSocket connection; measure the latency and learn the transport details. It implements the Go package [wsstat][godocs] and a CLI tool, also named `wsstat`.

## wsstat CLI Client

The CLI client provides a simple and easy to use tool to check the status of a WebSocket endpoint:

```sh
~ wsstat -v echo.websocket.org

Target: echo.websocket.org
IP: 66.241.124.119
Messages sent: 1
WS version: 13
TLS version: TLS 1.3

  DNS Lookup    TCP Connection    TLS Handshake    WS Handshake    Message RTT
|  1.541ms  |      287.437ms  |     321.186ms  |    730.487ms  |   518.387ms  |
|           |                 |                |               |              |
|  DNS lookup:1.541ms         |                |               |              |
|                 TCP connected:288.979ms      |               |              |
|                                       TLS done:610.165ms     |              |
|                                                        WS done:1340.652ms   |
-                                                                         Total:2366.209ms
```

Without `-v`, output is a compact summary:

```sh
~ wsstat echo.websocket.org

URL: echo.websocket.org
IP:  66.241.124.119

Round-trip time: 492.884ms (1 message)
Total time: 2296.079ms
```

The client replicates what [reorx/httpstat](https://github.com/reorx/httpstat) and [davecheney/httpstat](https://github.com/davecheney/httpstat) does for HTTP, but for WebSocket. It is said that imitation is the sincerest form of flattery, and inspiration has for certain been sourced from these projects.

### Install

#### Snap

If you are using a Linux distribution that supports Snap, you can install the tool from the Snap Store:

```sh
sudo snap install wsstat
```

#### Go

Requires that you have Go installed on your system and that you have `$GOPATH/bin` in your `PATH`. Recommended Go version is 1.26 or later.

Install via Go:

```sh
# To install the latest version, specify other releases with @<tag>
go install github.com/jkbrsn/wsstat/v3/cmd/wsstat@latest

# To include the version in the binary, build from a clone of the repo
git clone https://github.com/jkbrsn/wsstat.git
cd wsstat
go install -ldflags "-X main.version=$(cat VERSION)" ./cmd/wsstat
```

Note: a remote `go install ...@<tag>` cannot inject the version via ldflags, so a binary installed that way reports its version as `unknown`. Build from a clone (as above) to get the version stamped.

The snap is listed here: [snapcraft.io/wsstat](https://snapcraft.io/wsstat)

> Maintainers: see [docs/operations/snap-release-flow.md](./docs/operations/snap-release-flow.md) for how snap revisions are built, published to `edge`, and promoted.

#### Binary

Prebuilt release binaries are currently published for **Linux/amd64 only**. On other platforms, build from source (see [macOS and Windows](#macos-and-windows) below).

##### Linux

Download the binary from the latest release (`amd64`) on [the release page](https://github.com/jkbrsn/wsstat/releases):

```sh
wget https://github.com/jkbrsn/wsstat/releases/download/<tag>/wsstat-<tag>
```

Make the binary executable:

```sh
chmod +x wsstat
```

Move the binary to a directory in your `PATH`:

```sh
sudo mv wsstat /usr/local/bin/wsstat  # system-wide
mv wsstat ~/bin/wsstat  # user-specific, ensure ~/bin is in your PATH
```

##### macOS and Windows

Currently not actively supported, but you may build and try it yourself:

```sh
git clone https://github.com/jkbrsn/wsstat.git
cd wsstat
make build-all

# Binary ends up in ./bin/wsstat-<OS>-<ARCH>
```

Then for Windows:

1. Place the binary in a directory of your choice and add the directory to your `PATH` environment variable.
2. Rename the binary to `wsstat.exe` for convenience.
3. You should now be able to run `wsstat` from the command prompt or PowerShell.

For macOS:

1. Make the binary executable: `chmod +x wsstat-darwin-<ARCH>`
2. Move the binary to a directory in your `PATH`: `sudo mv wsstat-darwin-<ARCH> /usr/local/bin/wsstat`

### Usage

Some examples:

```bash
# Basic request
wsstat wss://echo.example.com

# Send a text message
wsstat -t "ping" wss://echo.example.com

# Read the payload from a file or stdin (@file / @-)
wsstat -t @payload.json wss://echo.example.com
echo '{"hello":"world"}' | wsstat -t @- wss://echo.example.com

# Send an RPC method (JSON-RPC 2.0 by default; --rpc-version 1.0 for legacy servers)
wsstat --rpc-method eth_blockNumber wss://rpc.example.com/ws

# Start a stream
wsstat stream --summary-interval 5s wss://stream.example.com/feed

# Record response payloads to a file as NDJSON (in addition to normal output)
wsstat stream --file capture.ndjson wss://stream.example.com/feed

# Attach headers to dial request
wsstat -H "Authorization: Bearer TOKEN" -H "Origin: https://foo" wss://api.example.com/ws

# Resolve to a target IP and set a longer timeout
wsstat --resolve example.com:443:127.0.0.1 --timeout 30s wss://example.com/ws

# Allow insecure connection, make output extra verbose
wsstat --insecure -vv wss://self-signed.example.com
```

For a full list of the available options, run `wsstat -h`, `wsstat measure -h`, `wsstat stream -h`, `wsstat ping -h`, or `wsstat check -h`.

### Modes

`wsstat <url>` (or `wsstat measure <url>`) measures connection latency. `wsstat
stream <url>` keeps the socket open for long-lived feeds. `wsstat ping <url>`
watches ping/pong latency over time. `wsstat check <url>` runs observational RFC
6455 conformance checks. A host literally named
`measure`/`stream`/`ping`/`check` must be spelled with a scheme
(`wsstat wss://ping`).

In measure mode, `-c N` aggregates timing across all interactions; the response
printed is the first one (measure does not concatenate responses).

### Stream Mode

Long-lived streaming endpoints are exercised with the `stream` subcommand:

```sh
wsstat stream -t '{"method":"subscribe"}' wss://example.org/stream
```

`stream` keeps the socket open, forwards each incoming frame to stdout, and
periodically snapshots timing metrics. Use `-b/--buffer` to adjust the
per-subscription queue length and `--summary-interval` (for example, `30s`) to
print recurring summaries that include per-subscription message counts, byte
totals, and mean inter-arrival latency.

`measure` defaults to `-c 1`. In `stream`, `-c 0` (the default) keeps the
connection open until you cancel it, while any positive value limits delivery to
that many events before wsstat disconnects:

```sh
wsstat stream -c 5 -t '{"method":"subscribe"}' wss://example.org/stream
```

For a single-response probe, run `stream --once`, which streams and exits after
the first event:

```sh
wsstat stream --once -t '{"method":"subscribe_ticker"}' wss://example.org/ws
```

In `stream`, `-t` may be repeated to hold a multi-frame conversation on a single
connection: each message is sent in argv order, spaced by `--send-delay`
(default `1s`) so the server can answer each frame before the next arrives. The
first message is the subscribe payload; receiving is unchanged, so `-c`,
`--once`, and `--timeout` still bound the read side. If the receive limit is
reached before all messages are sent, the remaining sends are skipped:

```sh
wsstat stream -c 4 -o json \
  -t '{"method":"subscribe","subscription":{"type":"trades","coin":"BTC"}}' \
  -t '{"method":"unsubscribe","subscription":{"type":"trades","coin":"BTC"}}' \
  wss://example.org/ws
```

### Ping Mode

`wsstat ping <url>` dials once and sends a WebSocket ping frame every
`-i/--interval` (default `1s`) on that connection, printing a per-ping RTT line
as each pong arrives and a summary at the end:

```sh
wsstat ping -c 5 wss://echo.example.com
```

```text
PING wss://echo.example.com (dns 5ms, tcp 10ms, tls 12ms, ws 7ms)
pong: seq=1 rtt=12.3ms
pong: seq=2 rtt=11.8ms
timeout: seq=3 (5s)
pong: seq=4 rtt=12.1ms
...
STATS wss://echo.example.com (5 sent, 4 received, 20.0% loss)
rtt: min=11.8ms avg=12.1ms max=12.3ms stddev=0.2ms
```

With no `-c` the run continues until you interrupt it (`Ctrl-C`) or the optional
`-w/--deadline` elapses, like `ping(8)`. `-o json` emits one `ping_reply` record
per ping and a final `ping_summary` (which stays the last record even on total
loss); `-q` prints the summary block only.

A missed pong (no reply within `--timeout`, default `5s`) is reported as a
`timeout` line and the run **continues**, exactly like `ping(8)` — so a transient
drop shows up as loss in the summary without ending the run. The run ends only at
`--count`, on `Ctrl-C`/`--deadline`, or when the connection actually closes. The
exit code is 0 when at least one pong was received and 1 on total loss or a dial
failure, so `wsstat ping -c 3 <url>` doubles as a liveness gate.

### Check Mode

`wsstat check <url>` runs a small set of observational RFC 6455 conformance
checks (handshake correctness, subprotocol/extension/version negotiation,
ping/pong, fragmentation tolerance, and close semantics) in a few seconds over at
most 5 connections plus one plain HTTP request, and reports pass/warn/fail/skip
per check in the text or JSON output contract. Every exchange is well-formed: no
malformed frames, no connection storm:

```sh
wsstat check wss://echo.example.com
```

```text
Handshake
  ok   101 upgrade
  ok   Sec-WebSocket-Accept valid
  ok   Upgrade/Connection headers
Negotiation
  ok   subprotocol (none offered)
  ok   subprotocol echo
  ok   permessage-deflate
  ok   unsupported version rejected
Behavior
  ok   ping/pong
  ok   fragmented text
  ok   close handshake

10 passed, 0 warnings, 0 failed
```

`-v` appends the per-check detail and timing; `-q` prints only the summary line.
`-o json` emits exactly one `check_report` record:

```sh
wsstat check -o json wss://echo.example.com
# {"schema_version":"1.0","type":"check_report","url":"wss://echo.example.com","checks":[{"id":"handshake.upgrade","group":"handshake","status":"pass","detail":"101 Switching Protocols","took_ms":0.495},{"id":"handshake.accept","group":"handshake","status":"pass","detail":"validated during handshake","took_ms":0},{"id":"handshake.headers","group":"handshake","status":"pass","detail":"Upgrade/Connection tokens present","took_ms":0},{"id":"negotiation.subprotocol-none","group":"negotiation","status":"pass","detail":"none offered, none selected","took_ms":0},{"id":"negotiation.subprotocol-echo","group":"negotiation","status":"pass","detail":"selected wsstat-check","took_ms":0.477},{"id":"negotiation.deflate","group":"negotiation","status":"pass","detail":"permessage-deflate: permessage-deflate","took_ms":0.239},{"id":"negotiation.version-reject","group":"negotiation","status":"pass","detail":"rejected (400, advertises 13)","took_ms":0.235},{"id":"behavior.ping-pong","group":"behavior","status":"pass","detail":"ping -> pong","took_ms":0.084},{"id":"behavior.fragmentation","group":"behavior","status":"pass","detail":"fragmented text accepted","took_ms":0.348},{"id":"behavior.close-echo","group":"behavior","status":"pass","detail":"close 1000 echoed, clean shutdown","took_ms":0.349}],"passed":10,"warned":0,"failed":0,"skipped":0}
```

The exit code is 0 when no check fails (warnings included) and 3 when any check
fails, so `wsstat check <url>` works as a CI conformance gate. A dial that fails
the handshake fails the dependent checks and skips the rest; a dial or output
failure is a runtime error (exit 1).

Skipped by design (blocked by the client always emitting valid, masked frames):
unsolicited-pong tolerance, client masking enforcement, and the Tier 2
adversarial probes (invalid UTF-8, RSV bits, reserved/invalid opcodes and close
codes, oversized/fragmented control frames), plus limits and performance checks.

### Output

Output is split across three orthogonal axes:

- `-o, --output text|json|raw` — the whole-stdout contract. `json` emits
  newline-delimited envelopes with a stable, published schema (see
  [docs/schema/](./docs/schema/); `-v`/`-vv` never change which fields appear);
  `raw` writes payload bytes verbatim with nothing added — no
  label, color, or trailing newline, so frames stay binary-safe and stream frames
  concatenate undelimited (use `-o json` when you need a delimiter). In measure
  mode `raw` requires `--text` or `--rpc-method`; with `--rpc-method` the response
  frame is decoded before output, so `raw` emits compact JSON rather than
  byte-for-byte wire content.
- `--body auto|compact` — human body rendering (text output only). `auto`
  pretty-prints; `compact` puts each message on one line.
- `--clip` — clips each rendered line to the terminal width with a trailing `...`
  (text output, TTY only; a no-op when piped or redirected).

Orthogonal to all three axes, `-f/--file <path>` additionally records each response
payload to `<path>` as NDJSON, one per line. It captures response bodies only
(summaries and other chrome still go to stdout), refuses to overwrite an
existing file, and removes the file again if nothing was recorded.

```sh
# Machine-readable streaming summaries and events
wsstat stream -o json --summary-interval 5s wss://example.org/stream

# Scannable one-line-per-event output, clipped to the terminal
wsstat stream --body compact --clip wss://example.org/stream
```

`--body`, `--clip`, `-q`, `-v`, and `-vv` apply only to `-o text` and are
rejected under `-o json|raw`.

### Exit Codes

| Code | Meaning |
|------|---------|
| 0    | Success (also `--help` and `--version`) |
| 1    | Runtime failure (dial, measurement, stream, or output write) |
| 2    | Usage error (bad flag or argument) |
| 3    | One or more checks failed (`check` mode) |
| 130  | Interrupted; a second `Ctrl-C` forces teardown |

In `ping` mode exit 1 also covers total loss (zero pongs received), while partial
loss with at least one pong still exits 0, so `wsstat ping -c N <url>` works as a
liveness gate.

In `check` mode exit 3 signals that at least one check produced a `fail` verdict;
warnings still exit 0, so `wsstat check <url>` works as a CI conformance gate.

Usage errors (exit 2) always print plain text to stderr. Under `-o json`, a
runtime failure (exit 1) prints a `{"type":"error"}` envelope to stdout so a
`wsstat ... -o json | jq` pipeline stays parseable on the failure path:

```sh
wsstat -o json ws://127.0.0.1:1
# {"schema_version":"1.0","type":"error","error":"measuring latency: ..."}
```

## wsstat Library Package

Use the `wsstat` Golang package to trace WebSocket connection and latency in your Go applications. It wraps [coder/websocket](https://pkg.go.dev/github.com/coder/websocket) for the WebSocket protocol implementation, and measures the duration of the different phases of the connection cycle.

### Install

Install to use in your Go project:

```bash
go get github.com/jkbrsn/wsstat/v3
```

### Usage

The [examples/main.go](./examples/main.go) program demonstrates two ways to use the `wsstat` package to trace a WebSocket connection. The example only executes one-hit message reads and writes, but WSStat also support operating on a continuous connection.

Run the example like this, from project root:

```bash
go run examples/main.go <a WebSocket URL>
```

## Build & Test

The project has a `Makefile` that provides a number of commands to build and test the project:

```sh
# build
make build
make build-all  # build for all supported platforms
make clean      # remove built binaries and clear the golangci-lint cache

# test
make test
make test V=1 RACE=1  # test with optional flags

# lint
make lint
```

## Contributing

For contributions, please open a GitHub issue with questions or suggestions. Before submitting an issue, have a look at the existing [TODO list](./docs/TODO.md) to see if what you've got in mind is already in the works.
