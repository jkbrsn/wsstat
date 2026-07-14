# Ping Mode: Continuous WebSocket Ping/Pong Monitoring

- **Date**: 2026-07-14
- **Commit**: 698411d
- **Branch**: feat/ping-command
- **Status**: Proposed

## Problem

wsstat's `measure` mode answers "how fast is this endpoint right now": it dials, performs
`-c N` back-to-back interactions, and prints one aggregated result. Nothing answers "how
does latency behave over time": per-ping lines as they happen, jitter, drops, an idle-timeout
proxy killing the connection at 60s. ICMP `ping` cannot see any of this because it never
traverses the WebSocket path (LB, proxy, upgrade route). `httping` fills this gap for HTTP;
nothing fills it for WebSocket.

Add a third mode, `wsstat ping <url>`: dial once, send a WebSocket ping frame every
`--interval` on that connection, print a per-ping RTT line live, run until `--count` is
reached or Ctrl-C, then print a `ping(8)`-style summary (sent/received/loss,
min/avg/max/stddev).

**Scope boundary**: ping frames only on a single connection. No plain HTTP probe, no
redial-on-drop (a dropped connection ends the run and is reported — redial loops hide
exactly the failure being observed), no interim summaries in v1.

## Constraints

- **Zero core-package changes.** Everything needed is already public: `wsstat.New`
  (`wsstat.go:148`), `DialContext` (`wsstat.go:441`), `PingPong` (`wsstat.go:603`),
  `ExtractResult` (`wsstat.go:719`), `Close`. The loop lives in `internal/app`.
- `PingPong()` is synchronous (round-trip bounded by `ws.timeout`) and returns no
  duration; `Result.MessageRTT` collapses to a mean in `calculateResultLocked`
  (`wsstat.go:260-266`) and per-ping timestamps are not exposed. Per-ping RTT is therefore
  wall-clock measured by the caller around the `PingPong()` call. No min/max/stddev
  aggregation exists anywhere in the codebase; the accumulator is net-new.
- `PingPong()` takes no context: an in-flight ping cannot be canceled, so Ctrl-C during a
  ping resolves within `ws.timeout`. Acceptable for v1 (the second Ctrl-C force-exits 130
  via `interruptContext`, `cmd/wsstat/main.go:214-230`); do not add a `PingPongCtx` core
  method for this.
- Politeness: default interval 1s; reject intervals below 10ms so a typo cannot flood a
  production endpoint.
- The JSON output contract is schema-pinned: `TestSchemaDocDrift`
  (`internal/app/schema_doc_test.go:42-44`) asserts the emitted record-type set against
  `docs/schema/wsstat-output-v1.schema.json`. New record types land in the same change.
- `internal/app` must not import `coder/websocket`; error classification uses `errors.Is`
  against `context.DeadlineExceeded` and wsstat sentinels only.

## Design

### 1. Semantics

- **Sequence**: 1-based `seq`, matching `ping(8)` familiarity. First ping fires immediately
  after dial, subsequent pings on a `time.Ticker`. Pings are sequential (synchronous
  `PingPong`); when RTT exceeds the interval the missed tick is dropped and the next ping
  fires immediately, so the effective rate is `max(interval, RTT)`.
- **Loss**: a ping counts as *sent* when `PingPong()` is invoked, *received* on nil error.
  `errors.Is(err, context.DeadlineExceeded)` → lost, keep going (the next pong proves the
  connection survived). Any other error (`wsstat.ErrClosed`, close-status errors, net
  errors) → the connection is dead: count that ping lost, end the run, print the summary
  plus the error.
- **Stats**: min/avg/max over received pongs; population stddev via sum/sum-of-squares
  (what `ping(8)` labels mdev). Undefined (omitted) when zero pongs received.
- **End of run**: count reached, ctx canceled (first Ctrl-C or `--deadline` expiry), or
  connection death — all paths print the summary.

### 2. App layer (`internal/app`)

New file `internal/app/ping.go`:

```go
// pingStats accumulates per-ping RTTs: sent, received, min/max/sum,
// sum-of-squares for stddev. ~30 lines, plain code.
type pingStats struct { ... }

// PingReport is returned for exit-code decisions; per-ping lines are
// printed live inside the loop (same shape as runSubscriptionLoop).
type PingReport struct {
    Target   *url.URL
    Sent     int
    Received int
    Min, Avg, Max, Stddev time.Duration
}

// RunPing dials once via wsstat.New(c.wsstatOptions()...) + DialContext,
// prints the header line from ExtractResult's dial timings, then loops:
// wall-clock PingPong, classify, print, accumulate, wait on ticker/ctx.
// Always prints the summary before returning. The error return is for
// runtime failures only (bad header, dial failure); ctx cancellation
// (Ctrl-C, --deadline) is swallowed — report returned, nil error — so the
// caller decides the exit code from the report, not the error.
func (c *Client) RunPing(ctx context.Context, target *url.URL) (*PingReport, error)
```

Loop skeleton (mirrors the ticker-in-select pattern of `runSubscriptionLoop`,
`internal/app/subscription.go:63-118`):

```go
defer ticker.Stop()
loop:
for seq := 1; c.count == 0 || seq <= c.count; seq++ {
    start := time.Now()
    err := ws.PingPong()
    rtt := time.Since(start)
    // classify → print ping_reply line → accumulate
    if dead(err) || (c.count != 0 && seq == c.count) {
        break
    }
    select {
    case <-ctx.Done():
        break loop // must be labeled: a bare break exits only the select
    case <-ticker.C:
    }
}
// print summary from pingStats — the authoritative RTT source. Do not use
// ExtractResult().MessageRTT here: PingPong records timestamps internally,
// so the core mean double-counts these pings and includes timed-out ones.
```

Client config: add `interval time.Duration` field + `WithInterval(d)` option
(`client.go`, next to `WithCount`, `client.go:130`).

`Validate()` (`client.go:425-484`): add a `ModePing` branch — count ≥ 0 (0 = unlimited,
the default, matching stream's count semantics), interval ≥ 10ms (default 1s applied when
unset), reject measure/stream-only options (`-t`, `--rpc-method`, `--once`, `-b`,
`--summary-interval`, `--send-delay`), reject `-o raw` and `--file` (no response payloads
in this mode).

Add `ModePing` to the `Mode` enum (`internal/app/format.go:36-39`).

### 3. Output

**Text** — two printers in `internal/app/output.go` following
`printSubscriptionMessage` (`output.go:111`) / `printSubscriptionSummary` (`output.go:248`):

```
PING wss://echo.example.com (dns 5ms, tcp 10ms, tls 12ms, ws 7ms)
pong: seq=1 rtt=12.3ms
pong: seq=2 rtt=11.8ms
timeout: seq=3 (5s)
pong: seq=4 rtt=12.1ms
^C
--- wss://echo.example.com ping statistics ---
4 sent, 3 received, 25.0% loss
rtt min/avg/max/stddev = 11.8/12.1/12.3/0.2 ms
```

Reuse `colorizeGreen` (`output.go:91`) for pong lines and `colorizeOrange` (`output.go:83`)
for timeouts; connection-death line in red matching whatever the check-mode plan settles
for fail (plain text when color is off). `-q` prints the summary block only (like
`ping -q`), suppressing the `PING …` header line too — the quiet early-return pattern of
`PrintTimingResults` (`output.go:651`); `-v` adds the target/TLS summaries measure mode
prints.

**JSON** — add to `internal/app/types.go` next to `subscriptionSummaryJSON` (:78), one
NDJSON line per record via `printJSONLine` (`output.go:99`):

```go
type pingReplyJSON struct {
    Schema string  `json:"schema_version"`
    Type   string  `json:"type"` // "ping_reply"
    Seq    int     `json:"seq"`
    RTTMs  float64 `json:"rtt_ms,omitempty"` // omitted when lost
    Lost   bool    `json:"lost,omitempty"`
    Error  string  `json:"error,omitempty"` // timeout / close detail
}

type pingSummaryJSON struct {
    Schema   string  `json:"schema_version"`
    Type     string  `json:"type"` // "ping_summary"
    URL      string  `json:"url"`
    Sent     int     `json:"sent"`
    Received int     `json:"received"`
    LossPct  float64 `json:"loss_pct"`
    MinMs    float64 `json:"min_ms,omitempty"` // omitted when received == 0
    AvgMs    float64 `json:"avg_ms,omitempty"`
    MaxMs    float64 `json:"max_ms,omitempty"`
    StddevMs float64 `json:"stddev_ms,omitempty"`
}
```

Dial-timing breakdown stays out of the JSON contract in v1 (it is measure mode's job;
the text header line is informational). Update `docs/schema/wsstat-output-v1.schema.json`
and the `want` list in `TestSchemaDocDrift` in the same change.

### 4. CLI (`cmd/wsstat`)

- `main.go`: `case args[0] == "ping": err = runPing(args[1:])` in the dispatch switch
  (:122-126); `buildPing`/`runPing` mirroring `buildMeasure` (:233) / `runMeasure` (:389).
  `buildPing` registers `registerCommon` + `registerRemoved` plus three mode flags:
  `-c/--count` (default 0 = run until Ctrl-C), `-i/--interval` (`fs.DurationVar`,
  default 1s), and `-w/--deadline` (`fs.DurationVar`, default 0 = none; max wall-clock
  for the whole run, like `ping -w`), then `resolveCommon(fs, &cf, app.ModePing)`.
- `--deadline` is **ping-only and CLI-layer-only**: no other subcommand registers it and
  it never reaches `app.Client`. `runPing` layers `context.WithTimeout` over
  `interruptContext`'s ctx when set; expiry cancels the ctx and behaves exactly like a
  first Ctrl-C (summary, exit code from the report). `buildPing` rejects `-w <= 0` when
  explicitly set (usage error), so no `Validate()` change is needed for it.
- **Exit codes**: 0 when at least one pong was received (partial loss included — the run
  observed a live endpoint); `exitRuntime = 1` on dial failure, runtime error, or zero
  pongs received (total loss), making `wsstat ping -c 3 <url>` a usable liveness gate;
  2 usage; 130 only on the forced second Ctrl-C (existing path). No new exit-code
  constant needed.
- **Mechanism** (diverges from `runMeasure`/`runStream`, which funnel every returned
  error through `runtimeErr` → exit 1): `RunPing` swallows ctx cancellation (Ctrl-C,
  deadline) and returns the report with a nil error, so cancellation never hits the
  `runtimeErr` path. `runPing` then inspects the report itself: `report.Received == 0`
  → return `runtimeErr` (total loss, exit 1); otherwise return nil (exit 0). Only dial
  and output-write failures flow through the error return.
- `usage.go`: `printPingUsage`, a `case "ping"` in `printHelpFor` (:12), USAGE/COMMANDS/
  example lines in `printTopUsage` (:32-38).

## Affected Files

| File | Change |
|------|--------|
| `internal/app/ping.go` | new: `pingStats`, `PingReport`, `RunPing` |
| `internal/app/format.go` | `ModePing` |
| `internal/app/client.go` | `interval` field, `WithInterval`, `Validate()` branch |
| `internal/app/types.go` | `pingReplyJSON`, `pingSummaryJSON` |
| `internal/app/output.go` | header/reply/summary printers |
| `cmd/wsstat/main.go` | dispatch case, `buildPing`, `runPing` |
| `cmd/wsstat/usage.go` | ping usage, top usage |
| `docs/schema/wsstat-output-v1.schema.json` | `ping_reply`, `ping_summary` records |
| `internal/app/ping_test.go`, `internal/app/schema_doc_test.go`, `cmd/wsstat/main_test.go` | tests |
| `dev/smoke-test.sh` | ping subcommand section (phase 3) |
| `README.md`, `CHANGELOG.md`, `CLAUDE.md`/`AGENTS.md` | docs (phase 4) |

## Implementation Phases

### Phase 1 — Runner and stats

1. `pingStats` + table-driven unit tests (empty, single, known stddev fixture).
2. `RunPing` against the shared echo server (`TestMain` / `echoServerAddrWs`): short
   interval, `-c 3`, assert sent=received=3 and monotonic seq; ctx-cancel mid-run prints
   summary and returns nil error (the exit-code contract depends on this); server that
   closes after N pongs → run ends with the loss counted and a report still returned;
   interval far below RTT (1ms interval, handler delaying pongs ~10ms) → pings stay
   sequential, missed ticks dropped, seq still monotonic (the documented
   `max(interval, RTT)` degradation).
3. **Settle by test**: whether a timed-out `conn.Ping` leaves the coder connection usable
   (a handler that swallows one ping then resumes). If a timeout poisons the connection,
   fold timeout into the connection-death path and simplify the loss model — decide here,
   before the output contract freezes. Either outcome fits the JSON contract unchanged
   (`pingReplyJSON`'s `lost` + `error` fields cover both); only the loop control flow and
   docs differ.
4. `ModePing` + `WithInterval` + `Validate()` rejections (alongside
   `client_validation_test.go`).

### Phase 2 — Output and schema

5. Printers (text `-q`/`-v` variants, JSON records); schema doc + `TestSchemaDocDrift`
   `want` list updated; output tests mirror `client_output_test.go`.

### Phase 3 — CLI wiring

6. Dispatch, `buildPing`/`runPing`, usage text. E2E in `cmd/wsstat/main_test.go`: exit 0
   on `-c 2` against the echo server; exit 1 against a handler that never pongs
   (zero received); exit 2 on `-t` with ping mode and on `-w 0s` explicitly set;
   `-w 300ms -i 100ms` with no `-c` terminates on its own with exit 0.
7. `dev/smoke-test.sh`: add a `# --- Ping subcommand ---` section following the existing
   check patterns:
   - `check "ping bounded" "$WSSTAT" ping -c 3 -i 100ms "$WS_URL/echo"` — happy path,
     exit 0.
   - jq-gated JSON case: `ping -c 2 -o json` piped to `jq -es` asserting two `ping_reply`
     records and one `ping_summary`.
   - Usage rejection: `bash -c "! $WSSTAT ping -t hi $WS_URL/echo"` — exit 2.
   - Total loss (exit 1) is covered at unit level only: coder/websocket answers pings
     below the handler, so the mock server has no path that suppresses pongs. Do not add
     a no-pong endpoint for this; revisit only if a live-endpoint loss case proves
     necessary.
8. `make lint && make test`; drive the built binary against the dev-stack mock server
   (verify-wsstat) for the live per-line behavior a captured-buffer test cannot show.

### Phase 4 — Documentation

9. `README.md`: Modes section (~:165) gains ping; exit-code note in Exit Codes (~:251).
10. `CHANGELOG.md`: `## [Unreleased]` → Added.
11. `CLAUDE.md`/`AGENTS.md`: app-layer description gains ping.

## Edge Cases

- **Interval < RTT**: sequential pings, dropped ticks; effective rate degrades gracefully.
  Document in `printPingUsage`.
- **Ctrl-C or deadline expiry during an in-flight ping**: resolves within `ws.timeout`
  (no ctx on `PingPong`), so a run can overshoot `--deadline` by up to `ws.timeout`;
  second Ctrl-C forces exit 130. The in-flight ping still counts.
- **Unlimited default**: `wsstat ping <url>` with no `-c` runs until Ctrl-C, like
  `ping(8)`. The 1s default interval keeps that polite.
- **Zero pongs**: summary prints counts, omits rtt stats, exit 1.
- **`-c N` reached exactly**: no trailing interval wait after the last ping.
- **ws:// targets**: unchanged; the text header line simply omits the tls segment.

## Acceptance Criteria

- `wsstat ping -c 5 wss://<echo>` prints the header, five pong lines, and the statistics
  block; exit 0.
- `wsstat ping -c 3 -o json <url>` emits exactly three `ping_reply` lines and one
  `ping_summary` line conforming to the updated schema; `TestSchemaDocDrift` passes.
- Ctrl-C mid-run prints the summary for completed pings and exits 0 (≥1 pong received).
- `wsstat ping -w 1s -i 200ms wss://<echo>` terminates on its own with the summary and
  exit 0; `-w` is rejected as unknown by `measure` and `stream`.
- Zero pongs received → exit 1; bad flags (`-t`, `-o raw`, interval < 10ms) → exit 2.
- No new exports in the root package; no `coder/websocket` import in `internal/app`.
- `make lint && make test` (race, 16×) green; README, CHANGELOG, agent docs updated.

## Future Work

- `--summary-interval` interim statistics blocks, reusing stream mode's ticker pattern.
- `--reconnect` to redial on connection death and mark the gap, for long-lived
  availability monitoring (changes what "loss" means; deliberately out of v1).
- Adaptive/flood modes are out of scope permanently: firing pings faster than the server
  answers is hostile to shared endpoints.

## Relationship to Check Mode

Independent of `docs/plans/check-mode-plan.md`; either lands first. Ping is a fraction of
the size and exercises the same add-a-mode seams (dispatch, `Mode` enum, `Validate()`
branch, schema pinning), so landing it first derisks check mode's wiring.
