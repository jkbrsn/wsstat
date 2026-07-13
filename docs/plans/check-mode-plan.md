# Check Mode: Observational RFC 6455 Conformance Checks

- **Date**: 2026-07-13
- **Commit**: 34f69f0
- **Branch**: various-cleanup
- **Status**: Proposed

## Problem

wsstat has two modes: `measure` (latency) and `stream` (subscription events). Neither answers
"does this endpoint behave like a correct WebSocket server?" The ecosystem is bimodal: the
Autobahn Testsuite runs ~500 fuzz cases against implementations you control (Docker,
Python-2-era tooling, minutes per run), while cat-style clients (websocat, wscat) do no
conformance checking at all. No tool offers a fast, polite, point-at-a-URL conformance report.

Add a third mode, `wsstat check <url>`, that runs a small set of **observational** RFC 6455
checks (handshake correctness, negotiation, ping/pong, close semantics, fragmentation
tolerance) in a few seconds over a handful of connections, and reports pass/warn/fail in the
existing text/JSON output contracts.

**Scope boundary**: Tier 1 only. Checks that require sending malformed frames (RSV bits,
reserved opcodes, invalid close codes, invalid UTF-8) are blocked by `coder/websocket`
exposing no raw-frame write path and are explicitly deferred (see Future Work).

## Constraints

- `coder/websocket` frame construction is private: the client always emits valid,
  masked frames. Everything in Tier 1 must be expressible through its public API
  (`Ping`, `Write`, `Writer`, `Close(status, reason)`) plus plain HTTP requests.
- Checks must be polite against production endpoints: every well-formed exchange, no
  connection storms. Budget: ≤ 5 connections + 1 plain HTTP request, sequential.
- coder strictly validates *inbound* frames (RSV, opcodes, fragmented control frames,
  close payloads) and fails the read with a close-status error. Check mode inherits this
  for free: a misbehaving server surfaces as a failed check, not a crash.
- The JSON output contract is schema-pinned: `TestSchemaDocDrift`
  (`internal/app/schema_doc_test.go`) asserts `docs/schema/wsstat-output-v1.schema.json`
  lists every record type the code emits. A new record type must land in the same change.
- The `internal/app` package must not import `coder/websocket` directly; peer close codes
  must be surfaced through the root package.

## Design

### 1. Check catalog

Each check produces `pass`, `warn`, `fail`, or `skip` plus a one-line detail. Grouped:

**Handshake** (connection 1, reused by Behavior checks)

| ID | Check | Method | Verdict logic |
|----|-------|--------|---------------|
| `handshake.upgrade` | 101 upgrade completes | `DialContext` succeeds | fail on dial error; detail carries the HTTP status/body wsstat already captures on failed handshakes (`wsstat.go` DialContext, ~:483) |
| `handshake.accept` | `Sec-WebSocket-Accept` valid | Library-enforced: coder's `verifyServerResponse` rejects a bad accept hash during dial | pass when dial succeeds (annotate "validated during handshake") |
| `handshake.headers` | `Upgrade: websocket` / `Connection: Upgrade` tokens present in response | Inspect `Result.ResponseHeaders` (`result.go:37`) | warn on missing/miscased tokens (coder tolerates some) |

**Negotiation**

| ID | Check | Method | Verdict logic |
|----|-------|--------|---------------|
| `negotiation.subprotocol-none` | Server must not select a subprotocol when none offered (RFC 6455 §4.2.2) | Connection 1 dials with no `WithSubprotocols`; check `Result.Subprotocol == ""` | fail if non-empty |
| `negotiation.subprotocol-echo` | Server selects one of the offered subprotocols or none | Connection 2 offers `["wsstat-check", <user-provided via -sub if set>]`; coder fails the dial if the server invents one | pass on empty or offered value; fail if dial rejects an invented protocol |
| `negotiation.deflate` | `permessage-deflate` response params valid (RFC 7692 §7) | Connection 3 dials with `WithCompression(true)`; parse `Result.Compression` (`result.go:35`) | pass if absent (not negotiated → informational detail) or params well-formed; warn on unknown/duplicate params, `server_max_window_bits` out of 8–15 |
| `negotiation.version-reject` | Unsupported `Sec-WebSocket-Version` rejected with the advertised-versions header (RFC 6455 §4.4) | One plain `net/http` request with `Sec-WebSocket-Version: 99` (honoring `-insecure` TLS config and custom headers) | pass on non-101 + `Sec-WebSocket-Version` response header listing 13; warn on non-101 without the header; fail on 101 |

**Behavior** (connection 1 continued, then connections 4–5)

| ID | Check | Method | Verdict logic |
|----|-------|--------|---------------|
| `behavior.ping-pong` | Ping answered by pong with matching payload | `WSStat.PingPong()` (`wsstat.go:603`); coder verifies the payload match internally | fail on timeout/error; detail includes RTT |
| `behavior.fragmentation` | Fragmented text message tolerated | Connection 4: new core method sends a 3-fragment text message, then `PingPong()` proves the connection survived | fail if the server kills the connection (close 1002 etc.); echo content is server-dependent and not asserted |
| `behavior.close-echo` | Close handshake completed with a valid echoed status (RFC 6455 §5.5.1, §7.4) | Connection 5: `CloseWith(1000, "")` (`wsstat.go` CloseWith) while a `ReadMessage` drains; extract the peer status from the read error | pass on echoed 1000 (or a valid registered code, warn); warn on no-echo/TCP-drop (wsstat's `gracefulClose` timeout path); skip if connection 5 fails to dial |

Skipped-by-design (documented in `check` usage text): unsolicited-pong tolerance (coder has no
public pong-send), masking enforcement, UTF-8/RSV/opcode probes (Tier 2), limits/perf.

### 2. Core package additions (root `wsstat`)

Two small, generally-useful additions; no new transport.

```go
// CloseStatus returns the RFC 6455 close status carried by an error from
// ReadMessage/ReadMessageJSON, or -1 if the error is not a close error.
// Wraps coder's websocket.CloseStatus so callers need not import coder.
func CloseStatus(err error) int

// WriteMessageFragmented sends one text or binary message as len(fragments)
// frames (fin only on the last), synchronously, bypassing the write pump.
// Uses coder's Conn.Writer; one Write+flush per fragment.
func (ws *WSStat) WriteMessageFragmented(messageType int, fragments [][]byte) error
```

Notes:
- `classifyReadErr` (`wsstat.go:631`) wraps non-1000/1001 closes as "unexpected close error"
  but preserves the chain, so `CloseStatus` works on its output via `errors` unwrapping
  (coder's `CloseStatus` already walks the chain).
- Verify during implementation that coder's `msgWriter` emits one frame per `Write` call
  (it does for uncompressed messages; with flate, fragment boundaries are not guaranteed —
  run the fragmentation check on a connection dialed without compression).
- Verify that after `CloseWith`, the read pump surfaces the peer's close echo as an error on
  `readChan` before coder's internal `waitCloseHandshake` swallows it. If it races, add a
  `ReceivedCloseStatus() int` accessor populated in `readPump` (`wsstat.go:312`) instead;
  decide by test, prefer the error-based route.

### 3. App layer (`internal/app`)

New file `internal/app/check.go`:

```go
type CheckStatus int // CheckPass, CheckWarn, CheckFail, CheckSkip

type CheckEntry struct {
    ID     string        // e.g. "behavior.ping-pong"
    Group  string        // "handshake" | "negotiation" | "behavior"
    Status CheckStatus
    Detail string        // one line; "" when self-evident
    Took   time.Duration
}

type CheckReport struct {
    Target  *url.URL
    Entries []CheckEntry
    // Passed/Warned/Failed/Skipped counts derived, not stored
}

// RunCheck executes the Tier 1 catalog sequentially and always returns a
// report; a dial failure fails the dependent checks and skips the rest of
// that connection's checks rather than aborting.
func (c *Client) RunCheck(ctx context.Context, target *url.URL) (*CheckReport, error)
```

- `RunCheck` parallels `MeasureLatency` (`internal/app/client.go:363`): builds `wsstat.New`
  instances from `c.wsstatOptions()` (`client.go:304`), one per connection in the catalog,
  each closed before the next dials. The version-reject probe uses `net/http` with the
  client's TLS config and headers.
- Per-check timeout: the existing `-timeout` (dial) plus a per-check cap derived from it;
  a hung server must not stall the run past `~4×timeout`.
- Add `ModeCheck` to the `Mode` enum (`internal/app/format.go:33`) and a `case ModeCheck`
  in `Client.Validate()` (`client.go:401`) rejecting measure/stream-only options
  (`-c`, `--once`, `-b`, `--summary-interval`, `-t`, `--rpc-method`).
- The error return is for runtime failures only (bad target, ctx canceled). Check verdicts
  live in the report.

### 4. Output

**Text** — `PrintCheckResults(report *CheckReport) error` in `internal/app/output.go`,
following the printer pattern (`PrintTimingResults`, `output.go:646`): branch on `c.output`,
reuse `colorizeGreen`/`colorizeOrange` (`output.go:83-91`) plus red for fail. Layout:

```
Handshake
  ✓ 101 Switching Protocols
  ✓ Sec-WebSocket-Accept valid (validated during handshake)
  ✗ Connection header missing "Upgrade" token
Negotiation
  ✓ subprotocol: none offered, none selected
  ⚠ permessage-deflate: server ignores client_max_window_bits
  ✓ unsupported version rejected (426, advertises 13)
Behavior
  ✓ ping → pong (12ms)
  ✓ fragmented text accepted
  ✓ close 1000 echoed, clean shutdown

8 passed, 1 warning, 1 failed
```

ASCII fallback (`ok`/`warn`/`FAIL`) when color is off, matching the existing color-mode
plumbing (`ColorMode`, `client.go:292`). `-v` adds `Detail` and `Took` per line; `-q` prints
the summary line only.

**JSON** — add to `internal/app/types.go`, mirroring `subscriptionSummaryJSON` (:78):

```go
type checkEntryJSON struct {
    ID     string `json:"id"`
    Group  string `json:"group"`
    Status string `json:"status"` // "pass" | "warn" | "fail" | "skip"
    Detail string `json:"detail,omitempty"`
    TookMs float64 `json:"took_ms"`
}

type checkReportJSON struct {
    Schema  string           `json:"schema_version"`
    Type    string           `json:"type"` // "check_report"
    URL     string           `json:"url"`
    Checks  []checkEntryJSON `json:"checks"`
    Passed  int              `json:"passed"`
    Warned  int              `json:"warned"`
    Failed  int              `json:"failed"`
    Skipped int              `json:"skipped"`
}
```

One `check_report` object via `printJSONLine` (`output.go:99`). Update
`docs/schema/wsstat-output-v1.schema.json` (add the `check_report` record) in the same
change so `TestSchemaDocDrift` passes. `-o raw` is rejected at validation (no raw payload
in this mode).

### 5. CLI (`cmd/wsstat`)

- `main.go`: add `case args[0] == "check": err = runCheck(args[1:])` to the dispatch switch
  (:117-133); add `buildCheck`/`runCheck` mirroring `buildMeasure` (:232) / `runMeasure`
  (:379). `buildCheck` registers only `registerCommon` + `registerRemoved` (no
  check-specific flags in v1) and calls `resolveCommon(fs, &cf, app.ModeCheck)`.
- Exit codes (`main.go:50`): add `exitCheckFailed = 3` — `runCheck` returns it when any
  check is `fail` (warnings exit 0). Runtime errors keep `exitRuntime = 1`. This makes
  `wsstat check` usable as a CI gate.
- `usage.go`: `printCheckUsage`, a `case "check"` in `printHelpFor` (:12), a line in
  `printTopUsage` command/description/examples blocks (:32-58), and the new exit code in
  the exit-codes block (:45).

### 6. Name

`check`, not `validate`: shorter, and `validate` collides with `Client.Validate()`.

## Affected Files

| File | Change |
|------|--------|
| `wsstat.go` | `CloseStatus(err)` helper, `WriteMessageFragmented` |
| `internal/app/check.go` | new: check catalog, `CheckReport`, `RunCheck` |
| `internal/app/format.go` | `ModeCheck` |
| `internal/app/client.go` | `Validate()` branch for `ModeCheck` |
| `internal/app/types.go` | `checkReportJSON`, `checkEntryJSON` |
| `internal/app/output.go` | `PrintCheckResults` |
| `cmd/wsstat/main.go` | dispatch case, `buildCheck`, `runCheck`, `exitCheckFailed` |
| `cmd/wsstat/usage.go` | check usage, top usage, exit codes |
| `docs/schema/wsstat-output-v1.schema.json` | `check_report` record |
| `internal/app/check_test.go`, `wsstat_test.go`, `cmd/wsstat/main_test.go` | tests |
| `README.md`, `CHANGELOG.md`, `CLAUDE.md`/`AGENTS.md` | docs (phase 4) |

## Implementation Phases

### Phase 1 — Core primitives

1. `CloseStatus(err error) int` in `wsstat.go` + test: dial the echo server, `CloseWith(1000)`,
   assert the drained read error yields 1000. This test also settles the close-echo race
   noted in Design §2; fall back to `ReceivedCloseStatus()` if it proves unreliable.
2. `WriteMessageFragmented` + test against the shared echo server (`TestMain` /
   `echoServerAddrWs`): send 3 fragments, read one reassembled echo. Verify per-`Write`
   framing with a raw-frame-recording test handler if the echo server reassembles silently.

### Phase 2 — Check runner

3. `internal/app/check.go`: catalog, runner, per-connection sequencing, version-reject HTTP
   probe. Table-driven tests with purpose-built `httptest` handlers per misbehavior:
   - well-behaved echo server → all pass
   - handler selecting an unoffered subprotocol → `negotiation.subprotocol-echo` fail
   - handler returning malformed `Sec-WebSocket-Extensions` → `negotiation.deflate` warn
   - handler answering 101 to `Sec-WebSocket-Version: 99` → `negotiation.version-reject` fail
   - handler dropping TCP instead of close handshake → `behavior.close-echo` warn
   - unreachable port → `handshake.upgrade` fail, dependent checks skip, runner still
     returns a full report
4. `ModeCheck` + `Validate()` rejections (flag-combination tests alongside
   `client_validation_test.go`).

### Phase 3 — Output and CLI

5. `PrintCheckResults` text/JSON + `checkReportJSON`; update the published schema doc;
   `TestSchemaDocDrift` green. Output tests mirror `client_output_test.go`.
6. CLI wiring: dispatch, `buildCheck`/`runCheck`, exit code 3, usage text. E2E test in
   `cmd/wsstat` against a local echo server asserting exit codes 0 (all pass) and 3 (a fail).
7. `make lint && make test`.

### Phase 4 — Documentation

8. `README.md`: extend Modes (~:165) with `check`; add usage examples (`wsstat check
   wss://echo.example.com`, `-o json` sample `check_report` line); document exit code 3 in
   Exit Codes (~:237); note the mode under the CLI feature list.
9. `CHANGELOG.md`: `## [Unreleased]` → Added: `check` mode entry.
10. `CLAUDE.md` and `AGENTS.md`: update the app-layer description ("orchestrates
    measurement/subscription flows") to include check; `docs/schema/README.md` if it
    enumerates record types.

## Edge Cases

- **Dial failure**: `handshake.*` fail with the captured HTTP status/body; all
  connection-dependent checks report `skip`; exit 3.
- **ws:// targets**: all checks apply unchanged (no TLS-specific checks in the catalog).
- **Servers that close idle connections fast**: each connection performs its checks
  immediately after dial; no sleeps between checks.
- **Compression interplay**: the fragmentation connection always dials with compression off
  (fragment boundaries are undefined under permessage-deflate).
- **Ctrl-C**: ctx cancellation mid-run prints the report for completed checks, remaining as
  `skip`, and exits 130 (existing signal path, `main.go:226`).
- **Non-echo servers**: no check asserts echoed payloads; `behavior.fragmentation` proves
  liveness via a follow-up ping, not echo content.

## Acceptance Criteria

- `wsstat check wss://<well-behaved echo>` prints the grouped report, all pass, exit 0.
- `wsstat check -o json <url>` emits exactly one `check_report` line conforming to the
  updated schema; `TestSchemaDocDrift` passes.
- A server with any `fail` verdict → exit 3; runtime error (DNS failure with unreachable
  host) → exit 1; bad flags → exit 2.
- Full run against a live endpoint completes in ≤ ~4× the configured `-timeout`, ≤ 5
  WebSocket connections + 1 HTTP request.
- `make lint && make test` (race, 16×) green; no `coder/websocket` import in `internal/app`.
- README, CHANGELOG, agent docs updated.

## Future Work — Tier 2 Adversarial Probes (NOT to be implemented now)

Autobahn-grade conformance requires sending malformed frames and asserting the server
closes with 1002/1007: RSV bits without a negotiated extension, reserved opcodes,
fragmented/oversized control frames, invalid and reserved close codes, unmasked client
frames, invalid UTF-8 (fail-fast) in text frames, interleaved fragmentation violations,
unsolicited pongs.

This is blocked by `coder/websocket`: frame construction (`writeFrame`, opcodes, RSV,
masking) is unexported and there is no client-side hijack. Tier 2 therefore needs a raw
transport path: manual HTTP/1.1 upgrade + a minimal hand-rolled RFC 6455 frame codec over
the raw `net.Conn`. wsstat already captures that conn post-TLS (`captureNetConn`,
`wsstat.go:201`), so the dial/TLS/timing instrumentation is reusable, but the codec and a
parallel read path are a substantial new layer (~the size of the entire Tier 1 change).

Design intent when it happens: `wsstat check --probe <url>`, opt-in because firing
malformed frames at production endpoints is borderline hostile; one fresh connection per
probe; probe results appear as a fourth report group (`probe.*`) in the same `check_report`
schema (additive, no schema version bump). Letter grades (SSL-Labs style) stay out of scope
until the check set stabilizes.

Tier 1's report schema and exit-code contract are designed so Tier 2 slots in without
breaking changes; nothing in this plan should be blocked or complicated to accommodate it.
