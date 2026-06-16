# Changelog

All notable changes to this project will be documented in this file. To keep it lightweight, releases 2+ minor versions back will be churned regularly.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- (dev) `dev/` stack for end-to-end CLI testing: a Dockerized mock WebSocket server (`dev/mock-server/`, a separate Go module on `coder/websocket`) exposing one path per behavior, and `dev/smoke-test.sh` firing the host-built `./bin/wsstat` through the full CLI feature matrix.
- (dev) `make smoke` target and `dev/run.sh` orchestrator (`up` mode leaves the mock running for manual use).

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

[Unreleased]: https://github.com/jkbrsn/wsstat/compare/v2.2.0...HEAD
[2.2.0]: https://github.com/jkbrsn/wsstat/compare/v2.1.3...v2.2.0
[2.1.3]: https://github.com/jkbrsn/wsstat/compare/v2.1.1...v2.1.3
[2.1.1]: https://github.com/jkbrsn/wsstat/compare/v2.1.0...v2.1.1
[2.1.0]: https://github.com/jkbrsn/wsstat/compare/v2.0.6...v2.1.0
