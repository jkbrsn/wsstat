# Changelog

All notable changes to this project will be documented in this file. To keep it lightweight, releases 2+ minor versions back will be churned regularly.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `compact` output format (`-format compact`): renders structured (JSON) subscription events on a single line per message, instead of the multi-line pretty-print of `auto`. At `-v` the per-message block also collapses to one line (`[idx @ ts] N bytes {…}`).
- `WithCloseGrace(d)` option (and the `-close-timeout` CLI flag) bounding how long `Close()` waits for the peer's closing-handshake echo before forcing teardown. Defaults to 3s; `0` forces immediate teardown.
- The CLI now force-quits on a second interrupt: the first `Ctrl-C` (SIGINT/SIGTERM) begins a graceful shutdown bounded by close-grace, and a second immediately exits with code 130. Lets a teardown stuck on a non-echoing peer always be escaped.
- (dev) The mock server now serves `wss://` (port 17443) with a startup-generated self-signed cert, and `dev/smoke-test.sh` exercises the TLS dial path: `-insecure`/`-k`, verify-rejects-self-signed, a verifying handshake trusted via `/ca.pem` + `SSL_CERT_FILE`, and `-no-tls` scheme defaulting.

### Changed

- **BREAKING:** Migrated the underlying WebSocket library from the unmaintained `gorilla/websocket` to `coder/websocket`. The module path is now `github.com/jkbrsn/wsstat/v3`; importers must update their import paths. The `wsstat` CLI is unaffected (same flags, same output).
- `Close()` now performs the full RFC 6455 two-way closing handshake before tearing down the socket, resolving an ungraceful client close where strict peers logged `1006` / `use of closed network connection`. The handshake wait is bounded by `WithCloseGrace` (default 3s) so a write-only / non-echoing peer cannot stall teardown for coder's hard-coded 5s.
- The public message-type API stays `int`-based via the new `wsstat.TextMessage` / `wsstat.BinaryMessage` constants (numerically identical to the previous values), so callers do not need to import the transport package.

### Removed

- **BREAKING:** `ReadPong()`. coder's `Ping` is a synchronous round-trip, so `PingPong()` now records the ping/pong timings directly and the separate `ReadPong` step no longer exists.

## [2.2.2] - 2026-06-16

### Fixed

- (ci) The snap no longer ships the Go toolchain. `prime: []` on the `go-deps` part did not exclude the staged toolchain (craft-parts treats an empty include list as `*`), so the published snap was ~66 MB; an explicit prime exclusion drops it to ~6 MB.

### Changed

- (ci) Snap revisions are now built and published to the `edge` channel by the release workflow instead of the Snapcraft linked-repo auto-build, so builds only happen on an intentional release rather than every `main` push. Promotion to `stable` stays manual via the Snapcraft web UI. See `docs/operations/snap-release-flow.md`.
- (ci) Added snap store metadata (`title`, `contact`, `issues`, `source-code`, `website`), clearing the Snapcraft metadata lint warnings.

## [2.2.1] - 2026-06-16

### Added

- (dev) `dev/` stack for end-to-end CLI testing: a Dockerized mock WebSocket server (`dev/mock-server/`, a separate Go module on `coder/websocket`) exposing one path per behavior, and `dev/smoke-test.sh` firing the host-built `./bin/wsstat` through the full CLI feature matrix.
- (dev) `make smoke` target and `dev/run.sh` orchestrator (`up` mode leaves the mock running for manual use).

### Changed

- Upgraded to Go 1.26.3.
- General update of dependencies.

## [2.2.0] - 2026-02-03

### Added

- (CLI) New option `--timeout` (default 5s).
  - Applies both to connection dial and read timeouts.
- `AGENTS.md`, symlinked to `CLAUDE.md` and `GEMINI.md`.

## [2.1.3] - 2026-01-19

### Changed

- Upgraded to Go 1.25.6.

## [2.1.1] - 2025-12-11

### Fixed

- (CLI) Terminal output now shows the correct IP when using the `--resolve` option.

## [2.1.0] - 2025-12-09

### Added

- (CLI) New option `--resolve`, allowing for direct IP targeting rather than DNS resolution.

[Unreleased]: https://github.com/jkbrsn/wsstat/compare/v2.2.2...HEAD
[2.2.2]: https://github.com/jkbrsn/wsstat/compare/v2.2.1...v2.2.2
[2.2.1]: https://github.com/jkbrsn/wsstat/compare/v2.2.0...v2.2.1
[2.2.0]: https://github.com/jkbrsn/wsstat/compare/v2.1.3...v2.2.0
[2.1.3]: https://github.com/jkbrsn/wsstat/compare/v2.1.1...v2.1.3
[2.1.1]: https://github.com/jkbrsn/wsstat/compare/v2.1.0...v2.1.1
[2.1.0]: https://github.com/jkbrsn/wsstat/compare/v2.0.6...v2.1.0
