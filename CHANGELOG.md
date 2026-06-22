# Changelog

All notable changes to this project will be documented in this file. To keep it lightweight, releases 2+ minor versions back will be churned regularly.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **CLI subcommands.** Mode is now an explicit subcommand: `wsstat measure <url>` (also the bare `wsstat <url>` form) and `wsstat stream <url>` for long-lived feeds. `stream --once` exits after the first event. Each subcommand's `-h` lists only its own flags.
- **Three orthogonal output axes.** `-o, --output text|json|raw` selects the whole-stdout contract; `--body auto|compact` selects human body rendering; `--clip` clips each rendered line to the terminal width on a TTY (no-op when piped/redirected). `-o json` is schema-stable: `-v`/`-vv` never change which fields appear. `-o raw` writes payload bytes verbatim (no label, color, timing, or added newline) in both measure and stream modes; stream frames are concatenated undelimited (binary-safe), so use `-o json` when you need delimited machine-readable streaming. `-o raw` in measure mode requires `--text` or `--rpc-method`; with `--rpc-method` the frame is decoded before output, so `raw` emits compact JSON rather than byte-for-byte wire content.
- `--body` now governs the measured response too: `--body auto` pretty-prints any JSON response (a JSON-RPC reply or a plain-JSON text echo), `--body compact` one-lines it (previously the measured response was always compact JSON regardless of format, and `--body` only shaped decoded JSON-RPC, not arbitrary JSON text responses).
- `WithValidateUTF8(bool)` library option and `--validate-utf8` CLI flag for opt-in UTF-8 validation of inbound text frames (coder/websocket performs none, per RFC 6455 §5.6). Invalid frames are logged at warn level and counted in `Result.InvalidUTF8Frames` rather than failing the connection; the CLI surfaces the count as a `warning:` line in text output and a `warnings` array in the `-o json` timing envelope (additive, no schema bump).
- `CloseWith(code, reason)` library method to close with a chosen RFC 6455 close status and reason instead of `Close`'s default `StatusNormalClosure` (1000). Validates the code (sendable codes only: 1000-1003, 1007-1011, 3000-4999) and reason length (<=123 bytes); otherwise it tears down exactly like `Close` and is idempotent.
- `WithCloseGrace(d)` library option (and the `--close-timeout` CLI flag) bounding how long `Close()` waits for the peer's closing-handshake echo before forcing teardown. The library option defaults to 3s and treats `0` as immediate teardown; the CLI flag forwards only positive values, so `--close-timeout 0` keeps the 3s default (the handshake is capped at 5s either way).
- The CLI now force-quits on a second interrupt: the first `Ctrl-C` (SIGINT/SIGTERM) begins a graceful shutdown bounded by close-grace, and a second immediately exits with code 130. Lets a teardown stuck on a non-echoing peer always be escaped.
- **JSON error envelope.** Under `-o json`, a runtime failure now prints a schema-stable `{"schema_version","type":"error","error"}` record to stdout (newline-terminated, matching the NDJSON data stream) instead of falling back to plain `Error:` text, so a `wsstat ... -o json | jq` pipeline stays parseable on the failure path. Usage errors still print plain text to stderr.
- (dev) The mock server now serves `wss://` (port 17443) with a startup-generated self-signed cert, and `dev/smoke-test.sh` exercises the TLS dial path: `-insecure`/`-k`, verify-rejects-self-signed, and a verifying handshake trusted via `/ca.pem` + `SSL_CERT_FILE`.
- `--show-secrets` flag: by default `-vv` now masks sensitive header values (`Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`) as `[redacted]`; pass `--show-secrets` to print them. Text-only, like the other `-vv` flags.
- `--rpc-version 1.0|2.0` flag (default `2.0`) for `--rpc-method`. `1.0` emits a legacy JSON-RPC 1.0 request (`{"id":1,"method":...,"params":[]}` — no `jsonrpc` field, integer id, positional params array) and relaxes response decoding to accept version-less / `1.0` replies (treating `"error":null` as absent, and `"result":null` beside a real error as absent). The encode path otherwise stays strict 2.0. Requires `--rpc-method` or `--text`.
- (dev) `dev/soak-test.sh` (and `make soak`): a structured flag-combination soak complementing the per-feature `smoke-test.sh`. Drives every flag in each mode (both aliases), asserts every validation rule actually rejects (a combination that should error but exits 0 is flagged as a silent accept), and checks the observable effect of flags that could be silently ignored, including `--clip`/`--color auto` under a real PTY via `dev/pty-run.py`.
- **Payload from a file or stdin.** `-t @path` reads the text payload from a file and `-t @-` reads it from stdin; bytes are sent verbatim (no trailing-newline stripping). A literal leading `@` is escaped as `@@`.
- `wsstat help measure` / `wsstat help stream` now print that subcommand's usage (previously `help <anything>` always printed the top-level usage).
- `NO_COLOR` is now documented in the help text (it already forced color off under `--color auto`).
- `--debug` flag wiring the core's zerolog debug logs to stderr, independent of the `-v`/`-vv` output verbosity (which only shape stdout). Off by default; safe to combine with any `-o` mode or `-q` since it never touches the stdout output contract.
- **Published JSON output schema.** `docs/schema/wsstat-output-v1.schema.json` (draft 2020-12) validates a single `-o json` NDJSON record across all five types (`timing`, `response`, `subscription_summary`, `subscription_message`, `error`); `docs/schema/README.md` documents the version semantics. `schema_version` is a single monotonic version for the whole output family: a breaking change to any record bumps it (`1.0` -> `2.0`); additive optional fields do not. The schema is intentionally open so additive fields still validate. A drift test pins the schema's version and record-type set to the code. See [ADR 0003](./docs/decisions/0003-json-output-schema-and-timing-precision.md).

### Changed

- **BREAKING (CLI):** The flag surface was reworked for 3.0.0. Mode moved from the `-subscribe`/`-subscribe-once` booleans to the `stream` subcommand; the overloaded `-format` split into `-o`/`--body`/`--clip`; and text-only flags (`--body`, `--clip`, `-q`, `-v`, `-vv`) are now rejected (not silently ignored) under `-o json|raw`. Removed v2 flags emit a targeted "removed in v3; use X" error, detected after flag parsing so a value that merely looks like a removed flag (e.g. `-t -s` sending the text `-s`) is not misread. Migration:

  | v2 | v3 |
  |---|---|
  | `wsstat -subscribe <url>` | `wsstat stream <url>` |
  | `wsstat -subscribe-once <url>` | `wsstat stream --once <url>` |
  | `wsstat -format json` | `wsstat -o json` |
  | `wsstat -format compact` | `wsstat --body compact` |
  | `wsstat -format truncate` | `wsstat --body compact --clip` |
  | `wsstat -format raw` | `wsstat -o raw` |
  | `wsstat -f <x>` | removed — use `-o`/`--body`/`--clip` |
  | `wsstat -no-tls <host>` | `wsstat ws://<host>` |
  | `wsstat -count N -subscribe` | `wsstat stream -c N <url>` |

- **BREAKING:** Migrated the underlying WebSocket library from the unmaintained `gorilla/websocket` to `coder/websocket`. The module path is now `github.com/jkbrsn/wsstat/v3`; importers must update their import paths.
- `Close()` now performs the full RFC 6455 two-way closing handshake before tearing down the socket, resolving an ungraceful client close where strict peers logged `1006` / `use of closed network connection`. The handshake wait is bounded by `WithCloseGrace` (default 3s) so a write-only / non-echoing peer cannot stall teardown for coder's hard-coded 5s.
- The public message-type API stays `int`-based via the new `wsstat.TextMessage` / `wsstat.BinaryMessage` constants (numerically identical to the previous values), so callers do not need to import the transport package.
- **BREAKING (CLI):** the URL scheme is now allowlisted to `ws`/`wss` at parse time. `http://`/`https://` (and any other scheme) are rejected with `unsupported scheme "...": use ws:// or wss://` instead of being silently dialed as plaintext by the lenient underlying dialer. Scheme-less input still defaults to `wss://`.
- **Exit codes normalized.** Post-parse argument/validation errors now exit `2` (matching flag-parse errors) instead of `1`, reserving `1` for genuine runtime/network failures. The full table (`0` success, `1` runtime, `2` usage, `130` interrupt) is documented in `wsstat -h` and the README.
- Dropped the `github.com/jkbrsn/jsonrpc` dependency (and its transitive `github.com/bytedance/sonic` JIT/asm surface). The CLI only built a fixed JSON-RPC request and decoded the reply, so both are now handled inline with the standard library `encoding/json`. No CLI behavior change; the binary no longer links a runtime code-generation library.
- **Sub-millisecond timing precision.** Phase durations now render as float milliseconds at microsecond resolution (rounded to 3 decimals) instead of truncating to whole ms, in both text and `-o json` output. A sub-millisecond phase (e.g. a `ws://localhost` dial) now shows non-zero. The JSON `durations_ms`/`timeline_ms` (and subscription `*_ms`) values are now `number` rather than integer; consumers must not assume integer values. Key names and the nil-for-zero semantics are unchanged, so this is part of `schema_version` `1.0` (no bump). See [ADR 0003](./docs/decisions/0003-json-output-schema-and-timing-precision.md).

### Removed

- **BREAKING (CLI):** `-subscribe`, `-subscribe-once` (and `-s`), `-format`/`-f`, and `-no-tls`. See the migration table above.
- **BREAKING:** `ReadPong()`. coder's `Ping` is a synchronous round-trip, so `PingPong()` now records the ping/pong timings directly and the separate `ReadPong` step no longer exists.

### Fixed

- **Data race on the measurement `Result`.** `calculateResult` wrote every `Result` field unsynchronized, so calling `ExtractResult()` concurrently with `Close()` (or with the streaming subscription tick) raced under `-race`. The result computation and its snapshot copy are now guarded by an internal mutex; `ExtractResult()` returns a consistent snapshot even while `Close()` finalizes. The concurrency-safety contract is now documented on the `WSStat` godoc, and `nextSubscriptionID` uses `atomic.Uint64` for correct alignment on 32-bit platforms.
- `stream -o raw` now emits payload bytes only. The `Streaming subscription events` header, per-tick blank lines, and `Subscription summary` blocks no longer leak into raw stream output (only `-o json` had been special-cased, so raw fell through to the human text path). Raw is now verbatim in both measure and stream modes, matching the documented contract.
- `stream --once` now rejects an explicitly-set `-c`/`--count` instead of silently overriding it. `--once` always yields exactly one event, so combining it with a count is a configuration error rather than a no-op.
- `--quiet` (alias of `-q`) and `--verbose` (alias of `-v`) are now accepted. Previously only `-q` parsed even though the help advertised `--quiet`, and `--verbose` (valid in v2) had been dropped; both long forms are rejected under `-o json|raw`, the same as their short forms.
- `--clip` now applies to every text response body shape. Non-JSON-RPC map and array responses were printed unclipped; clipping now composes uniformly across all rendered bodies.
- The failed-handshake response body reflected into the returned dial error is now bounded to 4 KiB (`io.LimitReader`), so a hostile server cannot reflect an unbounded body into the error string.
- `ReadMessageJSON()` now applies the same close-status contract as `ReadMessage()`: an abnormal close (any status other than normal/going-away) is wrapped as an `unexpected close error` instead of returning the raw transport error, so close handling is identical regardless of decode path.
- The `-v` output no longer prints a double colon in `Messages sent:: N` (the label carried its own colon on top of the format string's).
- `--version` now prints `wsstat <version>`, matching the help-header format (was `Version: <version>`).
- `--close-timeout` above 5s now prints a one-line stderr notice that the transport caps the close handshake at 5s, instead of silently accepting a value with no effect.

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
