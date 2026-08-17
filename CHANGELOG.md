# Changelog

All notable changes to this project will be documented in this file. To keep it lightweight, releases 2+ minor versions back will be churned regularly.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [3.4.0] - 2026-08-17

### Added

- **Proxy reporting.** A run routed through `HTTP_PROXY`/`HTTPS_PROXY` now says so instead of presenting the proxy hop as the target. Text output gains a `Proxy:` line and a warning; `-o json` gains `target.proxy` plus `warnings` on `timing_summary` and `subscription_summary`, and `proxy` on `ping_summary` and `check_report`. `Result.Proxy` and the exported `ProxyTimingCaveat` carry the same to library consumers. Additive optional JSON fields, so `schema_version` is unchanged per ADR 0003. See the README's "Proxies" section for what the timings mean through an `http://` vs `https://` proxy.
- (dev) `dev/soak-test.sh` gains check-mode coverage (positive flags, one REJECT row per `validateCheck` rule, and the exit 3 / exit 0-with-warnings, `-q` and `-o json` output contracts), three proxy-environment rows exercising `HTTP_PROXY`/`NO_PROXY` via the `--resolve` trick for a non-loopback host, and positive rows for `--validate-utf8`, `--show-secrets`, `--debug` and `--rpc-version 1.0`.

### Changed

- Upgraded to Go 1.26.6.
- (lib) `Subscribe` now returns the new `ErrSubscriptionConflict` sentinel when a subscription is already active on the connection, instead of silently breaking delivery. A subscription with no way to attribute frames claims all of them, so a second one left both silent and backed unclaimed frames up until the read pump blocked for good. Cancel the first subscription (and wait for its `Done`) before registering another, or dial a second connection.

### Fixed

- (lib) A subscription registered right after `DialContext` is no longer torn down by the read pump's per-read timeout, which killed `wsstat stream` against any feed that stayed quiet past `--timeout`.
- (lib) Frames already received when the peer closes are no longer dropped before the stream loop can print or capture them.
- (lib) The TLS handshake is now bounded by the dial timeout; a peer that connected but never spoke TLS leaked a goroutine and a socket per stalled dial.
- (lib) `SubscriptionStats.MessageCount`/`ByteCount` and `Result.MessageCount` no longer count the error envelope delivered when a subscription ends, which over-reported by one.
- (lib) A ping whose pong never arrives no longer unbalances the write/read ledgers; one failed `PingPong` used to zero `MessageRTT` and `MessageCount` for the rest of the connection's life.
- `--file` capture failures now reach the exit code. A flush or close error (ENOSPC, quota) was discarded, so a silently truncated capture still exited 0.
- `-k/--insecure` and `WithTLSConfig` now apply when a proxy is in use; net/http performs the target handshake itself on that path, so a custom TLS config was ignored entirely and the dial failed against any certificate not chaining to the system roots.
- `check` now sends a user-supplied `Host` header on the version-reject probe, which otherwise addressed the default virtual host and mis-scored `negotiation.version-reject`.

## [3.3.0] - 2026-07-23

### Added

- **`check` subcommand.** `wsstat check <url>` runs a small set of observational RFC 6455 conformance checks (handshake correctness, subprotocol/extension/version negotiation, ping/pong, fragmentation tolerance, and close semantics) over at most 5 connections plus one plain HTTP request, reporting pass/warn/fail/skip per check in the text or JSON output contract. Exit 3 signals a failed check (warnings exit 0), so it works as a CI conformance gate; an unreachable endpoint or an interrupted run is a runtime error (exit 1), never a failed check or a pass; `-o json` emits one `check_report` record. See the README's "Check Mode" section and `wsstat check -h`.
- **(lib) `CloseStatus` helper.** `CloseStatus(err error) int` returns the RFC 6455 close status carried by an error from `ReadMessage`/`ReadMessageJSON`, or `-1` when the error is not a close error, so callers need not import `coder/websocket`.
- **(lib) `WriteMessageFragmented` method.** Sends one text or binary message as `len(fragments)+1` frames (one non-final frame per fragment plus a trailing empty fin continuation from the streaming writer), synchronously, bypassing the write pump; run it on a connection dialed without compression, as permessage-deflate does not preserve fragment boundaries.

### Fixed

- A user-supplied `Host` header (CLI `-H 'Host: ...'` or lib dial headers) is now sent on the wire. Previously net/http silently dropped the `Host` key from the header map and the handshake carried the URL host; the value is now routed through the dialer's dedicated Host override, and `Result.RequestHeaders` reflects what was actually sent.
- (lib) A read timeout firing during the closing handshake no longer tears the connection down mid-handshake, which the transport masked as a clean close and could fabricate a close echo that never arrived; once a close begins, teardown is bounded by the close grace instead of the read timeout.

## [3.2.0] - 2026-07-14

### Added

- **`ping` subcommand.** `wsstat ping <url>` dials once and sends a WebSocket ping frame every `-i/--interval`, printing a per-ping RTT line and a `ping(8)`-style `STATS` summary; a missed pong is a survivable `timeout` and the run continues, exit 1 covers total loss and dial failure. See the README's "Ping Mode" section and `wsstat ping -h` for the full flag and output contract.
- **(lib) `WithUnboundedReads` option.** Drops the read pump's per-read timeout so long-lived sessions carried only by control frames (e.g. a ping/pong monitor) are not torn down as idle.
- **(lib) `WithDiscardReads` option.** Makes the read pump drop data frames not claimed by a subscription instead of queueing them, so a session that never reads keeps processing pongs against a chatty peer.
- **(lib) `PingPongContext` method.** `PingPong` additionally bounded by a caller context, so cancellation interrupts a ping blocked on an unresponsive peer instead of waiting out the full read timeout.
- `-f` is now the short form of `--file`. The v2 `-format` alias it previously belonged to keeps its "removed in v3" hint under the long name only.
- **Repeatable `-t` in `stream` mode.** `wsstat stream` now accepts `-t/--text` multiple times, sending each message in argv order on the same connection, spaced by the new `--send-delay` flag (default `1s`); a single `-t` behaves as before and `measure` rejects repeats. See the README's "Stream Mode" section for details.

## [3.1.1] - 2026-07-13

### Fixed

- `wsstat measure --version` and `wsstat stream --version` now print the version instead of failing with an unknown-flag error.
- (lib) Mixed subscription + measurement sessions no longer report a zeroed `MessageRTT`: `Subscribe`'s initial payload write is no longer recorded in the RTT write ledger (subscription responses were never recorded as reads, so the unbalanced ledgers discarded all RTT data). A write dropped during connection teardown likewise no longer leaves an orphan write timing.
- (lib) `DialContext` now rejects reuse: dialing a closed instance returns `ErrClosed`, and a second dial on a live instance returns an error instead of silently leaking the previous connection and its pumps. A failed dial leaves the instance reusable.
- (lib) Request/response read timings are now stamped when the frame arrives in the read pump rather than when the consumer drains the channel, so time spent buffered no longer inflates `MessageRTT` (matching how subscription deliveries were already stamped).
- Subcommand help (`wsstat measure -h`, `wsstat stream -h`) now prints to stdout, matching top-level `-h` and GNU convention, so it can be piped to a pager. Parse errors keep printing usage to stderr.
- Misordered invocations now get targeted hints: flags after the URL (`wsstat <url> -v`) explain that flags must come before the URL, and a subcommand after global flags (`wsstat -v stream <url>`) explains that the subcommand must come first. Previously both failed with a bare "expected exactly one URL argument".
- `-b/--buffer` help no longer claims `[default: 0]`; the effective default queue length is 32.

### Changed

- Upgraded to Go 1.26.5.
- General update of dependencies.

## [3.1.0] - 2026-06-24

### Added

- **`--file` response recording.** A new `--file <path>` flag records response payloads to a file as NDJSON (one per line), in both `measure` and `stream` modes. It is additive and orthogonal to `-o`: only response bodies go to the file, while latency summaries and start/end chrome keep going to stdout/stderr. JSON payloads are compacted to a single line so a JSON-RPC stream produces valid `.jsonl`; non-JSON payloads are written verbatim. The file is opened exclusively and the run fails rather than overwriting an existing file.

## [3.0.1] - 2026-06-24

### Changed

- (ci) The manual release workflow now creates or reuses the remote release tag before changelog generation and verifies any existing tag points at the current commit, so failed release runs can be rerun without manual tag cleanup.

### Fixed

- `wsstat -help` (single-dash long form) now prints the top-level overview listing both subcommands, matching `wsstat --help` and `wsstat -h`. Previously it fell through to the `measure` usage, hiding the `stream` subcommand.

## [3.0.0] - 2026-06-23

### Added

- **CLI subcommands.** Mode is now an explicit subcommand: `wsstat measure <url>` (also the bare `wsstat <url>` form) and `wsstat stream <url>` for long-lived feeds. `stream --once` exits after the first event. Each subcommand's `-h` lists only its own flags.
- **Three orthogonal output axes.** `-o, --output text|json|raw` selects the whole-stdout contract; `--body auto|compact` selects human body rendering; `--clip` clips each rendered line to the terminal width on a TTY (no-op when piped/redirected). `-o json` is schema-stable: `-v`/`-vv` never change which fields appear. `-o raw` writes payload bytes verbatim (no label, color, timing, or added newline) in both measure and stream modes; stream frames are concatenated undelimited (binary-safe), so use `-o json` when you need delimited machine-readable streaming. `-o raw` in measure mode requires `--text` or `--rpc-method`; with `--rpc-method` the frame is decoded before output, so `raw` emits compact JSON rather than byte-for-byte wire content.
- `--body` now governs the measured response too: `--body auto` pretty-prints any JSON response (a JSON-RPC reply or a plain-JSON text echo), `--body compact` one-lines it (previously the measured response was always compact JSON regardless of format, and `--body` only shaped decoded JSON-RPC, not arbitrary JSON text responses).
- `WithValidateUTF8(bool)` library option and `--validate-utf8` CLI flag for opt-in UTF-8 validation of inbound text frames (coder/websocket performs none, per RFC 6455 §5.6). Invalid frames are logged at warn level and counted in `Result.InvalidUTF8Frames` rather than failing the connection; the CLI surfaces the count as a `warning:` line in text output and a `warnings` array in the `-o json` timing envelope (additive, no schema bump).
- `CloseWith(code, reason)` library method to close with a chosen RFC 6455 close status and reason instead of `Close`'s default `StatusNormalClosure` (1000). Validates the code (sendable codes only: 1000-1003, 1007-1011, 3000-4999) and reason length (<=123 bytes); otherwise it tears down exactly like `Close` and is idempotent.
- `WithCloseGrace(d)` library option (and the `--close-timeout` CLI flag) bounding how long `Close()` waits for the peer's closing-handshake echo before forcing teardown. The library option defaults to 3s and treats `0` as immediate teardown; the CLI flag forwards only positive values, so `--close-timeout 0` keeps the 3s default (the handshake is capped at 5s either way).
- The CLI now force-quits on a second interrupt: the first `Ctrl-C` (SIGINT/SIGTERM) begins a graceful shutdown bounded by close-grace, and a second immediately exits with code 130. Lets a teardown stuck on a non-echoing peer always be escaped.
- **JSON error envelope.** Under `-o json`, a runtime failure now prints a schema-stable `{"schema_version","type":"error","error"}` record to stdout (newline-terminated, matching the NDJSON data stream) instead of falling back to plain `Error:` text, so a `wsstat ... -o json | jq` pipeline stays parseable on the failure path. Usage errors still print plain text to stderr.
- `--show-secrets` flag: by default `-vv` now masks sensitive header values as `[redacted]`; pass `--show-secrets` to print them. Text-only, like the other `-vv` flags. Masking covers the standard credential headers (`Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`) plus any non-standard header whose name looks credential-bearing (contains `auth`, `cookie`, `token`, `secret`, `api-key`/`apikey`, or `password`, case-insensitive), so custom auth headers like `X-Api-Key` / `X-Auth-Token` are masked too.
- `--rpc-version 1.0|2.0` flag (default `2.0`) for `--rpc-method`. `1.0` emits a legacy JSON-RPC 1.0 request (`{"id":1,"method":...,"params":[]}` — no `jsonrpc` field, integer id, positional params array) and relaxes response decoding to accept version-less / `1.0` replies (treating `"error":null` as absent, and `"result":null` beside a real error as absent). The encode path otherwise stays strict 2.0. Requires `--rpc-method` or `--text`.
- (dev) `dev/soak-test.sh` (and `make soak`): a structured flag-combination soak complementing the per-feature `smoke-test.sh`. Drives every flag in each mode (both aliases), asserts every validation rule actually rejects (a combination that should error but exits 0 is flagged as a silent accept), and checks the observable effect of flags that could be silently ignored, including `--clip`/`--color auto` under a real PTY via `dev/pty-run.py`.
- **Payload from a file or stdin.** `-t @path` reads the text payload from a file and `-t @-` reads it from stdin; bytes are sent verbatim (no trailing-newline stripping). A literal leading `@` is escaped as `@@`.
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
- Upgraded to Go 1.26.4.

### Removed

- **BREAKING (CLI):** `-subscribe`, `-subscribe-once` (and `-s`), `-format`/`-f`, and `-no-tls`. See the migration table above.
- **BREAKING:** `ReadPong()`. coder's `Ping` is a synchronous round-trip, so `PingPong()` now records the ping/pong timings directly and the separate `ReadPong` step no longer exists.

### Fixed

- **Data race on the measurement `Result`.** `calculateResult` wrote every `Result` field unsynchronized, so calling `ExtractResult()` concurrently with `Close()` (or with the streaming subscription tick) raced under `-race`. The result computation and its snapshot copy are now guarded by an internal mutex; `ExtractResult()` returns a consistent snapshot even while `Close()` finalizes. The concurrency-safety contract is now documented on the `WSStat` godoc, and `nextSubscriptionID` uses `atomic.Uint64` for correct alignment on 32-bit platforms.
- `--quiet` (alias of `-q`) is now accepted; previously only `-q` parsed despite the help advertising `--quiet`.
- The failed-handshake response body reflected into the returned dial error is now bounded to 4 KiB (`io.LimitReader`), so a hostile server cannot reflect an unbounded body into the error string.
- `ReadMessageJSON()` now applies the same close-status contract as `ReadMessage()`: an abnormal close (any status other than normal/going-away) is wrapped as an `unexpected close error` instead of returning the raw transport error, so close handling is identical regardless of decode path.

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

[Unreleased]: https://github.com/jkbrsn/wsstat/compare/v3.4.0...HEAD
[3.4.0]: https://github.com/jkbrsn/wsstat/compare/v3.3.0...v3.4.0
[3.3.0]: https://github.com/jkbrsn/wsstat/compare/v3.2.0...v3.3.0
[3.2.0]: https://github.com/jkbrsn/wsstat/compare/v3.1.1...v3.2.0
[3.1.1]: https://github.com/jkbrsn/wsstat/compare/v3.1.0...v3.1.1
[3.1.0]: https://github.com/jkbrsn/wsstat/compare/v3.0.1...v3.1.0
[3.0.1]: https://github.com/jkbrsn/wsstat/compare/v3.0.0...v3.0.1
[3.0.0]: https://github.com/jkbrsn/wsstat/compare/v2.2.2...v3.0.0
[2.2.2]: https://github.com/jkbrsn/wsstat/compare/v2.2.1...v2.2.2
[2.2.1]: https://github.com/jkbrsn/wsstat/compare/v2.2.0...v2.2.1
[2.2.0]: https://github.com/jkbrsn/wsstat/compare/v2.1.3...v2.2.0
[2.1.3]: https://github.com/jkbrsn/wsstat/compare/v2.1.1...v2.1.3
[2.1.1]: https://github.com/jkbrsn/wsstat/compare/v2.1.0...v2.1.1
[2.1.0]: https://github.com/jkbrsn/wsstat/compare/v2.0.6...v2.1.0
