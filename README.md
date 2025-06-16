# wsstat [![Go Documentation](http://img.shields.io/badge/go-documentation-blue.svg?style=flat-square)][godocs]  [![MIT License](http://img.shields.io/badge/license-MIT-blue.svg?style=flat-square)][license]

[godocs]: http://godoc.org/github.com/jkbrsn/wsstat
[license]: /LICENSE

The aim of this project is to provide a simple and easy to use tool to check the status of a WebSocket endpoint:

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

What I've done is to basically try to replicate what [reorx/httpstat](https://github.com/reorx/httpstat) and [davecheney/httpstat](https://github.com/davecheney/httpstat) does for HTTP requests, but instead do it for WebSocket connections, and it should be quite clear that this project draws a lot of inspiration from those two.

> Imitation is the sincerest form of flattery.

## Installation

There are a number of ways to install this tool, depending on your preference. The easiest one at the moment is to install via Snap, and with time I'll aim to add Homebrew support for macOS users.

### Snap installation

If you are using a Linux distribution that supports Snap, you can install the tool from the Snap Store:

```sh
sudo snap install wsstat
```

### Go installation

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

### Binary download

#### Linux & macOS

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

#### Windows

1. Download the `wsstat-windows-<ARCH>.exe` binary from the latest release on [the release page](https://github.com/jkbrsn/wsstat/releases).
2. Place the binary in a directory of your choice and add the directory to your `PATH` environment variable.
3. Rename the binary to `wsstat.exe` for convenience.
4. You can now run `wsstat` from the command prompt or PowerShell.

## Usage

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
```

## Building

To build the project from source, you can use the `go build` command ro just run the Makefile:

```sh
go build -o wsstat cmd/wsstat/main.go
make build
```

If you need to build it for a different platform than your host machine, there's a `build-all` command available through the Makefile:

```sh
make build-all
```
