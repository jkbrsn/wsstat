# Plan: `connect` subcommand — interactive session with timing instrumentation

- date: 2026-07-16
- commit: 55c85fd
- branch: main
- status: draft (Phase 0 pending)

## TL;DR

Add `wsstat connect <url>`: open a persistent connection, read messages from stdin
line-by-line, print incoming frames, and annotate the session with timing data
(per-message send-to-response RTT, receive timestamps, exit summary). A follow-up
phase adds an optional TUI with an input box and a live measurement panel. The
plain REPL niche is already served by websocat; the timing instrumentation is
wsstat's reason to build this, so it is a requirement, not a nice-to-have.

**Phase 0 of this plan is design resolution.** Several decisions below are
sketched with a recommended default but must be confirmed before implementation
starts (see Open Questions).

## Problem

wsstat covers one-shot measurement (`measure`), send-once/read-many (`stream`),
and latency-over-time (`ping`), but has no bidirectional human-driven mode.
Debugging a JSON-RPC or streaming endpoint today means switching to websocat
mid-session and losing all timing context. `connect` closes that gap:
websocat-style interaction, wsstat-style instrumentation.

## Goals

- Interactive send/receive loop: type a line, it's sent as a text frame;
  incoming frames print as they arrive.
- Timing on everything: RTT from each send to its matched response, wall-clock
  timestamps on received frames, connection phase timings (DNS/TCP/TLS/WS) on
  connect, and a summary on exit (counts, RTT min/avg/max).
- Works non-interactively: piped stdin sends one message per line and the mode
  behaves identically minus prompts. This gives scriptability and testability
  for free.
- `-o json` emits NDJSON records per event, consistent with ping/stream.
- (Later phase) `--ui` TUI: input box + transcript + live measurement panel
  showing latency data on the latest response as messages roll in.

## Non-Goals

- Readline features (history, editing beyond the terminal's own line
  discipline) in the plain mode. Raw-mode line editing is a rabbit hole; the
  TUI phase covers users who want polish.
- Binary frame sending. There is no `-b/--binary` send flag today (`-b` is the
  stream buffer flag); an escape syntax for binary payloads is out of scope
  for v1. Received binary frames are handled (printed as size + preview).
- Auto-reconnect. `WSStat.DialContext` is single-use (`wsstat.go:459`); one
  session per invocation, like every other mode.
- Core-library API changes beyond what Phase 0 concludes is strictly needed
  (see Open Question 1).

## Phase 0 — Design resolution

Before writing code, settle these. Each has a recommended default; the point of
this phase is to confirm or overturn them with the maintainer.

1. **RTT pairing strategy.** The core has no per-frame RTT for arbitrary
   send/receive pairs (`Result.MessageRTT` at `result.go:55` is a single
   aggregate). Options:
   - *Next-frame heuristic*: RTT = time from `WriteMessage` to the next
     received frame while a send is outstanding. Simple, wrong on servers that
     push unsolicited frames.
   - *JSON-RPC id matching*: when the sent line parses as a JSON-RPC request,
     match the response by `id` (helpers already exist:
     `decodeJSONRPCResponse` in `internal/app/jsonrpc.go:57`). Unsolicited
     frames print without an RTT annotation.
   - **Recommended: both.** JSON-RPC id matching when the payload qualifies,
     next-frame heuristic otherwise, and label heuristic RTTs as approximate
     (`~12.3ms`) in text output / a `matched: "heuristic"|"jsonrpc"` field in
     JSON.

   Layering note: the core already records paired write/read timestamp
   ledgers (`wsstat.go:230-231`) but exposes only their mean
   (`Result.MessageRTT`). The v4 API plan (docs/plans/v4-api-plan.md,
   component 7) exposes the per-pair RTTs from those ledgers. Build connect
   on v3 with app-layer timing regardless — JSON-RPC matching needs its own
   send-time capture anyway — and when connect moves to v4, the heuristic
   path for unmatched traffic can read `Result.MessageRTTs()` instead.
   Semantic (JSON-RPC id) matching stays in the app layer permanently; the
   core is protocol-agnostic.
2. **Core API surface.** Drive the session with the raw
   `WriteMessage`/`ReadMessage` pair (`wsstat.go:587`, `wsstat.go:687`) in a
   read goroutine feeding a channel, rather than `Subscribe` (its
   decoder/matcher hooks are unexported, and the subscription abstraction adds
   nothing here). Caveat to resolve: `WriteMessage` is fire-and-forget with no
   error return — decide whether connect needs a write-error signal (likely
   surfaced soon after via the read loop erroring; confirm that's acceptable).
   The v4 API plan (component 1) gives `WriteMessage` an error return, which
   resolves this properly; on v3 the read-loop-surfaces-it behavior is the
   ceiling.
3. **Interleaving policy (plain mode).** Incoming frames printing while the
   user types will clobber the input line. Recommended: accept it (wscat
   does), with `> ` prefixing sent lines and `< ` prefixing received lines so
   the transcript stays readable. No prompt redraw tricks. The TUI phase is
   the real fix.
4. **TUI dependency.** No TUI framework in `go.mod`; `golang.org/x/term` and
   `go-isatty` are already present. Recommended: `bubbletea` (+ `bubbles` for
   the input box) for the TUI phase — hand-rolling panels on x/term is more
   code than the feature warrants, and this is the one place a new dependency
   is justified. Confirm before adding (dependency manifests need sign-off).
5. **Flag surface.** Which common groups apply: messaging (`-t` as initial
   message(s) sent on connect), response (`--file` sink records received
   frames), output (`-o`, `--body`, `--clip`, color), connection
   (`-k`, `--timeout`, headers, subprotocols), diagnostics. Connect-specific:
   `--ui` (TUI), possibly `--rtt-match=jsonrpc|next|off`. Reject the rest via
   the `registerUnsupported` pattern ping uses (`cmd/wsstat/main.go:357-408`).
6. **Prompt/UX details.** Whether to print a connect banner with phase
   timings (recommended: yes, reuse the measure-style timing line), whether
   empty input lines send or are skipped (recommended: skipped), and the exit
   gesture (EOF/ctrl-D closes cleanly; ctrl-C via the existing
   `interruptContext`, `cmd/wsstat/main.go:230`).

Deliverable: this plan updated with decisions inlined and Open Questions
emptied, then implementation proceeds.

## Detailed Design (per current recommendations)

### 1. CLI layer (`cmd/wsstat/`)

- Add `case "connect"` to the dispatch switch in `main()`
  (`cmd/wsstat/main.go:122-162`) → `runConnect(args[1:])`.
- Add `buildConnect(args)` mirroring `buildPing` (`cmd/wsstat/main.go:419`):
  `flag.FlagSet` + `newCommonFlags()` + selective group registrars
  (`registerMessagingFlags`, `registerResponseFlags`, `registerOutputFlags`,
  `registerConnectionFlags`, `registerDiagnosticFlags` in
  `cmd/wsstat/config.go:79-134`) + connect-specific flags + `registerRemoved`
  + `registerUnsupported` for inapplicable common flags.
- `resolveCommon(fs, &cf, app.ModeConnect)` (`cmd/wsstat/config.go:151`)
  builds options; `runConnect` opens the response sink via `openResponseSink`
  (`cmd/wsstat/main.go:503`) and calls the app entrypoint.
- Exit codes: 0 on clean close (EOF or server close), `exitRuntime` on dial
  or transport error, `exitUsage` on flag errors. Update `usage.go` and the
  doc comment block at the top of `main.go`.

### 2. App layer (`internal/app/connect.go`, new)

- Add `ModeConnect` to the `Mode` enum (`internal/app/format.go:33-43`) and a
  `validateConnect()` arm in `Client.Validate()` (`internal/app/client.go:435`).
- Entry point:

  ```go
  // RunConnect drives an interactive session: stdin lines out, frames in,
  // RTT annotations, summary on exit. in is os.Stdin in production.
  func (c *Client) RunConnect(ctx context.Context, target *url.URL, in io.Reader) (*ConnectReport, error)
  ```

  Injecting `in` (rather than reading `os.Stdin` directly) is what makes the
  piped-input path unit-testable without swapping `os.Stdin`.
- Structure, modeled on `runSubscriptionLoop` (`internal/app/subscription.go:63`):
  - Dial via `wsstatOptions()` (`internal/app/client.go:338`) +
    `WithUnboundedReads()` (`wsstat.go:1116`) so idle sessions don't time out.
    Print the connect banner with phase timings from `ExtractResult()`.
  - Goroutine A: `bufio.Scanner` over `in`, each line → `sendCh`; on EOF,
    close `sendCh`.
  - Goroutine B: `ReadMessage()` loop → `recvCh`; exits on `ErrClosed`/ctx.
  - Main select loop over `ctx.Done()`, `sendCh`, `recvCh`: on send, stamp
    `time.Now()`, register the pending RTT match (jsonrpc id or next-frame),
    `WriteMessage`, echo `> line` (text mode, TTY only); on receive, resolve
    RTT match, emit the frame.
  - Initial `-t` messages (from `TextMessages()`) are sent on connect before
    the loop starts, reusing the send path so they get RTT annotations too.
- `ConnectReport`: sent/received counts, byte counts, RTT min/avg/max over
  matched pairs, session duration. Printed as summary on exit (text) or a
  final NDJSON record (json).

### 3. Output records (`internal/app/types.go`, `output.go`)

- Text mode, one line per event:

  ```
  > {"jsonrpc":"2.0","id":1,"method":"eth_blockNumber"}
  < 14:32:07.412  12.3ms  {"jsonrpc":"2.0","id":1,"result":"0x1b4"}
  < 14:32:09.001      —   {"method":"subscription","params":...}
  ```

  Received lines respect `--body`/`--clip`/verbosity via the existing
  formatting path (`printSubscriptionMessage` at `internal/app/output.go:120`
  is the model). Colors via `colorEnabled()`/palette
  (`internal/app/output.go:64-105`).
- JSON mode (NDJSON via `printJSONLine`, `internal/app/output.go:108`), new
  record types alongside `pingReplyJSON` (`internal/app/types.go:102`):
  - `connect_open` — target, phase timings.
  - `connect_send` — seq, payload (clipped per `--clip`), timestamp.
  - `connect_message` — seq, payload, size, timestamp, `rtt_ms *float64`,
    `matched` (`"jsonrpc"|"heuristic"|null`).
  - `connect_summary` — the `ConnectReport` fields.
  All carry `schema_version` (`JSONSchemaVersion`).
- Raw mode: received payload bytes verbatim via `writeRaw`
  (`internal/app/output.go:181`); no annotations.
- `--file` sink: received frames through `writeResponseLine`
  (`internal/app/output.go:708`), same as stream.

### 4. TUI mode (`--ui`, later phase)

Separate binary surface over the same `RunConnect` core: refactor the select
loop so events (sent/received/annotated) flow through a narrow interface the
plain printer and the TUI both consume, rather than printing inline.

Layout (bubbletea):

```
┌─ wsstat connect wss://... ────────────────────────────┐
│ transcript (scrollback): > sent / < received lines    │
│                                                       │
├─ latest response ──────────┬─ session ────────────────┤
│ RTT        12.3 ms         │ sent 14   recv 15        │
│ size       412 B           │ RTT min/avg/max          │
│ received   14:32:07.412    │ 9.8 / 12.1 / 18.4 ms     │
│ matched    jsonrpc id=1    │ uptime 00:03:12          │
├────────────────────────────┴─────────────────────────-┤
│ > input box                                           │
└───────────────────────────────────────────────────────┘
```

The "latest response" panel updates live as frames roll in — this is the
niche: watching latency per response while interacting. Session panel keeps a
rolling RTT distribution. Requires a TTY; `--ui` with piped stdin is a usage
error. Keep the TUI layer thin: all measurement logic stays in the shared
core so the TUI is view-only.

## Affected Files

| File | Change |
|---|---|
| `cmd/wsstat/main.go` | dispatch case, `buildConnect`/`runConnect`, doc comment |
| `cmd/wsstat/config.go` | `ModeConnect` handling in `resolveCommon` if mode-specific validation applies |
| `cmd/wsstat/usage.go` | connect help text |
| `internal/app/format.go` | `ModeConnect` enum value |
| `internal/app/client.go` | `validateConnect()`, connect options if any |
| `internal/app/connect.go` (new) | session loop, RTT matching, `ConnectReport` |
| `internal/app/types.go` | `connect_*` JSON record types |
| `internal/app/output.go` | connect text printers |
| `internal/app/connect_test.go` (new) | see Testing |
| `cmd/wsstat/main_test.go` | flag/build coverage |
| `internal/app/tui/` (new, TUI phase) | bubbletea model/view |
| `go.mod`/`go.sum` (TUI phase) | bubbletea + bubbles (needs sign-off) |
| `CHANGELOG.md` | `### Added` bullet under `[Unreleased]` |
| `README.md` | connect section, mirroring the ping section |

## Edge Cases & Safety

- **Server closes mid-session**: read loop returns; print/emit summary, exit 0
  if close was clean (normal closure), `exitRuntime` otherwise.
- **Unsolicited frames while a send is pending** (next-frame heuristic):
  mis-attribution is inherent; the `matched` field / `~` marker keeps it
  honest. JSON-RPC matching avoids it entirely for RPC traffic.
- **Multiple outstanding sends**: allowed (user types faster than the server
  replies). JSON-RPC matching handles it; next-frame heuristic pairs FIFO.
- **Slow terminal vs fast server**: stdout writes are synchronous in the
  select loop, same backpressure story as stream mode. No unbounded buffering.
- **Large frames**: `--max-message-size` / `WithReadLimit` applies as in other
  modes; `--clip` bounds terminal output.
- **stdin closed but connection alive** (piped input): after EOF, keep
  reading until all pending RTT matches resolve or `--timeout` lapses, then
  close. Exact linger rule is a Phase 0 detail.
- **Ctrl-C**: first SIGINT cancels ctx (clean close + summary), second
  hard-exits 130 — existing `interruptContext` behavior.

## Implementation Phases

1. **Design resolution** (Phase 0 above) — confirm the six decisions, update
   this doc.
2. **Plain connect mode** — CLI wiring, `RunConnect` with piped and TTY
   input, text output with RTT annotations, summary. Tests. CHANGELOG.
3. **JSON/raw output + sink** — `connect_*` records, `--file`, schema doc
   test coverage (mirror `schema_doc_test.go`).
4. **TUI** — dependency sign-off, event-stream refactor of the loop, bubbletea
   model, `--ui` flag. Tests limited to the shared core; the view stays thin.

Phases 2 and 3 could be one PR; 4 is definitely separate.

## Testing

- App layer: `RunConnect` with an `io.Reader` script of lines against
  `newConversationTestServer` (`internal/app/testing_helpers_test.go:172`,
  records inbound + acks) — assert transcript order, RTT presence, report
  counts. Stdout captured via `captureStdoutFrom`
  (`internal/app/testing_helpers_test.go:22`).
- RTT matching: table-driven unit tests for jsonrpc-id vs heuristic pairing,
  including unsolicited-frame and multiple-outstanding cases (delay/chatty
  server modes, following `newPingServer`'s pattern in
  `internal/app/ping_test.go:41`).
- CLI layer: `buildConnect` flag validation in `cmd/wsstat/main_test.go`
  (unsupported-flag rejection, url parsing).
- TUI: core event stream unit-tested; the bubbletea view gets at most a
  smoke test (`teatest`) — acceptable to leave mostly untested if thin.
- `make lint && make test` green; CI runs race + 16x repetition, so the
  select loop must be race-clean by construction.

## Acceptance Criteria

- `wsstat connect wss://echo.example.com` connects, shows phase timings,
  echoes typed lines with per-message RTT, and prints a summary on ctrl-D.
- `printf 'a\nb\n' | wsstat connect -o json ws://...` emits valid NDJSON:
  one `connect_open`, two `connect_send`, two `connect_message` with
  `rtt_ms` set, one `connect_summary`.
- `-t` initial messages are sent and RTT-annotated.
- Unsolicited server pushes print without breaking RTT attribution for
  JSON-RPC traffic.
- Exit codes: 0 clean, 1 runtime, 2 usage, 130 double-interrupt.
- (TUI phase) `--ui` renders the layout above and the latest-response panel
  updates live against the dev-stack mock server (`verify-wsstat`).

## Open Questions

Tracked as Phase 0 decisions 1-6 above: RTT pairing strategy, core API
surface (write-error signaling), interleaving policy, TUI dependency choice,
exact flag surface, and prompt/UX details (banner, empty lines, linger rule
after stdin EOF).
