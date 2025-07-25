# wsstat package

Use the `wsstat` Golang package to trace WebSocket connection and latency in your Go applications. It wraps the [gorilla/websocket](https://pkg.go.dev/github.com/gorilla/websocket) package for the WebSocket protocol implementation, and measures the duration of the different phases of the connection cycle. The program takes inspiration from the [go-httpstat](https://github.com/tcnksm/go-httpstat) package, which is useful for tracing HTTP requests.

## Install

Install to use in your project with `go get`:

```bash
go get github.com/jkbrsn/wsstat/pkg/wsstat
```

## Usage

The [_example/main.go](./_example/main.go) program demonstrates two ways to use the `wsstat` package to trace a WebSocket connection. The example only executes one-hit message reads and writes, but WSStat also support operating on a continuous connection.

Run the example like this, from project root:

```bash
go run pkg/wsstat/_example/main.go <a WebSocket URL>
```

### Testing the package

Run the tests:

```bash
make test
```
