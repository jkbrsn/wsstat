# wsstat [![Go Documentation](http://img.shields.io/badge/go-documentation-blue.svg?style=flat-square)][godocs]  [![MIT License](http://img.shields.io/badge/license-MIT-blue.svg?style=flat-square)][license]

[godocs]: http://godoc.org/github.com/jkbrsn/wsstat
[license]: /LICENSE

This is a project that provides a way to measure the latency of a WebSocket connection. It implements the Go package [wsstat](https://github.com/jkbrsn/wsstat) and a CLI tool, also named `wsstat`.

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

##### Linux & macOS

Download the binary appropriate for your system from the latest release on [the release page](https://github.com/jkbrsn/wsstat/releases):

```sh
wget https://github.com/jkbrsn/wsstat/releases/download/<tag>/wsstat-<OS>-<ARCH>
```

Make the binary executable:

```sh
chmod +x wsstat-<OS>-<ARCH>
```

Move the binary to a directory in your `PATH`:

```sh
sudo mv wsstat-<OS>-<ARCH> /usr/local/bin/wsstat  # system-wide
mv wsstat-<OS>-<ARCH> ~/bin/wsstat  # user-specific, ensure ~/bin is in your PATH
```

##### Windows

1. Download the `wsstat-windows-<ARCH>.exe` binary from the latest release on [the release page](https://github.com/jkbrsn/wsstat/releases).
2. Place the binary in a directory of your choice and add the directory to your `PATH` environment variable.
3. Rename the binary to `wsstat.exe` for convenience.
4. You can now run `wsstat` from the command prompt or PowerShell.

### Usage

Basic usage:

```sh
wsstat example.org
```

With verbose output:

```sh
wsstat -v ws://example.local
```

For more options:

```sh
wsstat -h

Usage:  wsstat [options] <url>

Measure WebSocket latency or stream subscription events.
If the URL omits a scheme, wsstat assumes wss:// unless -no-tls is provided.

Input (choose one):
  -rpc-method string   JSON-RPC method name to send (id=1, jsonrpc=2.0)
  -text string         text message to send

Subscription:
  -subscribe           stream events until interrupted
  -subscribe-once      subscribe and exit after the first event
  -count int           number of interactions to perform; 0 means unlimited when subscribing (default 1; defaults to 0 when subscribing)
  -buffer int          subscription delivery buffer size (messages) (default 0)
  -summary-interval    print subscription summaries every interval (e.g., 1s, 5m, 1h); 0 disables

Connection:
  -H / -header string  HTTP header to include with the request (repeatable; format: Key: Value)
  -no-tls              assume ws:// when input URL lacks scheme (default wss://)
  -color string        color output: auto, always, or never (auto|always|never; default "auto")

Output:
  -q                   quiet all output but the response
  -v                   increase verbosity; repeatable (e.g., -v -v) or use -v=N
  -format string       output format: auto, json, or raw (default "auto")

Verbosity:
  default  minimal request info with summary timings
  -v       adds target/TLS summaries and timing diagram
  -vv      includes full TLS certificates and headers

General:
  -version             print the program version

Examples:
  wsstat wss://echo.example.com
  wsstat -text "ping" wss://echo.example.com
  wsstat -rpc-method eth_blockNumber wss://rpc.example.com/ws
  wsstat -subscribe -count 1 wss://stream.example.com/feed
  wsstat -subscribe -summary-interval 5s wss://stream.example.com/feed
  wsstat -H "Authorization: Bearer TOKEN" -H "Origin: https://foo" wss://api.example.com/ws
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
