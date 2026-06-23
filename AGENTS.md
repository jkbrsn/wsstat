# Agent Guide

Guidance for AI agents working in this repository.

## Commands

Run `make` (or `make explain`) for build/test/lint targets and their flags (`N=`, `RACE=1`, `V=1`).

```bash
make test
make build

# Targeted tests:
go test ./... -run '^TestName$' -v -race   # single test across packages
go test ./ -run 'TestWSStat/Basic' -v      # focused subtest
```

## Architecture

Module `github.com/jkbrsn/wsstat/v3`. Three layers:

1. **CLI** (`cmd/wsstat/`) - parses flags, validates URLs (auto-adds `wss://`), builds config, delegates to the app layer.
2. **App** (`internal/app/`) - `Client` orchestrates measurement/subscription flows and output formatting.
3. **Core** (root pkg: `wsstat.go`, `result.go`, `subscription.go`, `wrappers.go`) - public API; wraps coder/websocket with timing instrumentation. `WSStat` produces a `Result` with DNS/TCP/TLS/WS/RTT timings.

All layers use the functional options pattern: `New(opts ...Option)` with `WithTimeout()`, `WithTLSConfig()`, `WithBufferSize()`, etc.

## Conventions

- **Errors**: wrap with `fmt.Errorf("context: %w", err)`, no trailing periods; sentinels as `var ErrX = errors.New(...)`.
- **Logging**: default `zerolog.Nop()`, injected via options; keep tests quiet.
- **Security**: TLS verifies by default; CLI `-insecure`/`-k` keeps TLS but sets `InsecureSkipVerify` (use `ws://` for plaintext).
- **Style**: `gofmt -s`; soft 100-char lines; max 80 lines per function (tests excluded).

## Testing

- Tests start a shared `httptest` echo server on a random port (TestMain); `echoServerAddrWs` holds its URL.
- `testify` (assert/require); CI runs with the race detector and 16x repetition.

## PR/Commit Standards

- Conventional commits: `feat:`, `fix:`, `docs:`, `chore(scope):`.
- `make lint && make test` must pass.
- User-facing changes: update `CHANGELOG.md` and `VERSION`.
