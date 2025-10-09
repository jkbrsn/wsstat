# wsstat

[![Go Documentation](http://img.shields.io/badge/go-documentation-blue.svg?style=flat-square)][godocs]
[![Go Report Card](https://goreportcard.com/badge/github.com/jkbrsn/wsstat)](https://goreportcard.com/report/github.com/jkbrsn/wsstat)
[![MIT License](http://img.shields.io/badge/license-MIT-blue.svg?style=flat-square)][license]

[godocs]: http://godoc.org/github.com/jkbrsn/wsstat
[license]: /LICENSE

This project provides a way to stat a WebSocket connection; measure the latency and learn the transport details. It implements the Go package [wsstat][godocs] and a CLI tool, also named `wsstat`.

## wsstat CLI Client

The CLI client provides a simple and easy to use tool to check the status of a WebSocket endpoint:

```sh
~ wsstat example.org

Target: example.org
IP: 1.2.3.4
WS version: 13
TLS version: TLS 1.3

  DNS Lookup    TCP Connection    TLS Handshake    WS Handshake    Message RTT
|     61ms  |           22ms  |          44ms  |         29ms  |        27ms  |
|           |                 |                |               |              |
|  DNS lookup:61ms            |                |               |              |
|                 TCP connected:84ms           |               |              |
|                                       TLS done:128ms         |              |
|                                                        WS done:158ms        |
-                                                                         Total:186ms
```

The client replicates what [reorx/httpstat](https://github.com/reorx/httpstat) and [davecheney/httpstat](https://github.com/davecheney/httpstat) does for HTTP, but for WebSocket. It is said that imitation is the sincerest form of flattery, and inspiration has for certain been sourced from these projects.

### Install

#### Snap

If you are using a Linux distribution that supports Snap, you can install the tool from the Snap Store:

```sh
sudo snap install wsstat
```

#### Go

Requires that you have Go installed on your system and that you have `$GOPATH/bin` in your `PATH`. Recommended Go version is 1.21 or later.

Install via Go:

```sh
# To install the latest version, specify other releases with @<tag>
go install github.com/jkbrsn/wsstat@latest

# To include the version in the binary, run the install from the root of the repo
git clone github.com/jkbrsn/wsstat
cd wsstat
git fetch --all
git checkout origin/main
go install -ldflags "-X main.version=$(cat VERSION)" github.com/jkbrsn/wsstat@latest
```

Note: installing the package with `@latest`  will always install the latest version no matter the other parameters of the command.

The snap is listed here: [snapcraft.io/wsstat](https://snapcraft.io/wsstat)

#### Binary

##### Linux

Download the binary from the latest release (`amd64`) on [the release page](https://github.com/jkbrsn/wsstat/releases):

```sh
wget https://github.com/jkbrsn/wsstat/releases/download/<tag>/wsstat
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

All the options are available from the help output, have a look at `wsstat -h`:

```sh
wsstat 2.0.0
Measure latency on WebSocket connections

USAGE:
  wsstat [options] <url>
  wsstat -subscribe [options] <url>

General:
  -c, --count <int>              number of interactions [default: 1; unlimited when subscribing]
      --version                  print program version and exit

Input (choose one):
      --rpc-method <string>      JSON-RPC method name to send (with id=1, jsonrpc=2.0)
  -t, --text <string>            text message to send

Subscription:
  -s, --subscribe                stream events until interrupted
      --subscribe-once           subscribe and exit after the first event
  -b, --buffer <int>             subscription delivery buffer size in messages [default: 0]
      --summary-interval <duration>
                                 print stat summaries every interval (e.g., 5s, 1m) [default: disabled]

Connection:
  -H, --header <string>          HTTP header to include with request (repeatable; format: "Key: Value")
  -k, --insecure                 skip TLS certificate verification (use with caution)
      --no-tls                   assume ws:// when URL lacks scheme [default: wss://]
      --color <string>           color output mode: auto, always, never [default: auto]

Output:
  -q, --quiet                    suppress all output except response
  -v, --verbose                  increase verbosity (level 1)
  -vv                            increase verbosity (level 2)
  -f, --format <string>          output format: auto, json, raw [default: auto]

Verbosity Levels:
  (default)                      minimal request info with summary timings
  -v                             adds target/TLS summaries and timing diagram
  -vv                            includes full TLS certificates and headers

Examples:
  wsstat wss://echo.example.com
  wsstat -t "ping" wss://echo.example.com
  wsstat --rpc-method eth_blockNumber wss://rpc.example.com/ws
  wsstat --subscribe --summary-interval 5s wss://stream.example.com/feed
  wsstat -H "Authorization: Bearer TOKEN" -H "Origin: https://foo" wss://api.example.com/ws
  wsstat --insecure -vv wss://self-signed.example.com
```

### Subscription Mode

Long-lived streaming endpoints can be exercised with the subscription mode:

```sh
wsstat -subscribe -text '{"method":"subscribe"}' wss://example.org/stream
```

When `-subscribe` is supplied the client keeps the socket open, forwards each
incoming frame to stdout, and periodically snapshots timing metrics. Use
`-buffer` to adjust the per-subscription queue length and `-summary-interval`
(for example, `30s`) to print recurring summaries that include per-subscription
message counts, byte totals, and mean inter-arrival latency.

Control how many interactions occur by setting `-count`. Non-subscription
commands default to `-count 1`. When streaming (`-subscribe`), `-count 0`
keeps the connection open until you cancel it, while any positive value limits
delivery to that many events before wsstat disconnects:

```sh
wsstat -subscribe -count 5 -text '{"method":"subscribe"}' wss://example.org/stream
```

For a single-response probe, you can either run `-subscribe -count 1` or use the
dedicated helper `-subscribe-once`, both of which subscribe and exit after the
first event:

```sh
wsstat -subscribe -count 1 -text '{"method":"subscribe_ticker"}' wss://example.org/ws
```

```sh
wsstat -subscribe-once -text '{"method":"subscribe_ticker"}' wss://example.org/ws
```

For machine-readable output of summaries, add `-format json`.

## wsstat Package

Use the `wsstat` Golang package to trace WebSocket connection and latency in your Go applications. It wraps [gorilla/websocket](https://pkg.go.dev/github.com/gorilla/websocket) for the WebSocket protocol implementation, and measures the duration of the different phases of the connection cycle.

### Install

Install to use in your Go project:

```bash
go get github.com/jkbrsn/wsstat
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

# test
make test
make test V=1 RACE=1  # test with optional flags

# lint
make lint
```

## Contributing

For contributions, please open a GitHub issue with questions or suggestions. Before submitting an issue, have a look at the existing [TODO list](./TODO.md) to see if what you've got in mind is already in the works.
