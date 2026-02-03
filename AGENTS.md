# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Test Commands

```bash
# Build
make build              # Build binary → bin/wsstat
make build-all          # Build for all 9 platforms (linux/darwin/windows × 386/amd64/arm64)

# Test
make test               # Run all tests with -shuffle=on
make test N=5           # Run tests 5 times (burst/flaky detection)
make test RACE=1        # Enable race detector
make test V=1           # Verbose output
make test N=3 RACE=1 V=1  # Combined flags

# Single package
go test ./internal/app -v -race

# Single test
go test ./... -run '^TestName$' -v -race
go test ./ -run 'TestWSStat/Basic' -v  # Focused subtest

# Lint & Format
make lint               # golangci-lint (revive, misspell, standard linters)
make fmt                # gofmt -s -w .
```

## Project Structure

- **Module**: `github.com/jkbrsn/wsstat/v2`
- **CLI**: `cmd/wsstat/` - flag parsing, config assembly, delegates to internal/app
- **App layer**: `internal/app/` - high-level Client API, output formatting, orchestration
- **Core library**: root package (`wsstat.go`, `result.go`, `subscription.go`, `wrappers.go`) - public API; wraps gorilla/websocket with timing instrumentation

## Architecture

Three-layer design:
1. **CLI** parses flags, validates URLs (auto-adds `wss://` if missing), builds config
2. **internal/app.Client** orchestrates measurement/subscription flows, handles output formatting
3. **Root wsstat package** provides `WSStat` type for timed WebSocket operations with `Result` containing DNS/TCP/TLS/WS/RTT timings

All layers use functional options pattern: `New(opts ...Option)` with `WithTimeout()`, `WithTLSConfig()`, `WithBufferSize()`, etc.

## Code Conventions

- **Imports**: std lib, blank line, third-party, blank line, local packages; alphabetical within groups
- **Formatting**: `gofmt -s`; soft 100-char line limit; max 80 lines per function (excluding tests)
- **Errors**: wrap with `fmt.Errorf("context: %w", err)`; no trailing periods; sentinels as `var ErrX = errors.New(...)`
- **Logging**: default `zerolog.Nop()`; inject via options; avoid noisy logs in tests
- **Types**: prefer concrete types; avoid `interface{}`; nil-safe slices/maps; pass `context.Context`
- **Security**: TLS verifies by default; CLI `-insecure` switches to `ws://`

## Testing Notes

- Tests start an echo WebSocket server on `localhost:8080` (TestMain); avoid port conflicts
- Uses `testify` (assert, require) for assertions
- CI runs with race detector and 16x repetition for flaky detection

## PR/Commit Standards

- Conventional commits: `feat:`, `fix:`, `docs:`, `chore(scope):` (e.g., `chore(version): bump to 2.2.0`)
- Ensure `make lint && make test` pass before submitting
- If user-facing behavior changes, update `CHANGELOG.md` and `VERSION`
