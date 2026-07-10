# wsstat v4 API Changes

**Date:** 2026-07-10
**Commit:** 68c92e0 (various-cleanup)

## TL;DR

Several library behaviors are honest only for the wsstat CLI's lockstep dial → write → read → close pattern: writes swallow errors at debug level, the subscription demux hooks are unexported and therefore unusable, "connection went away" surfaces as three different error shapes, and an idle connection self-destructs after the read timeout. v4 fixes the public contract (writes return errors, exported subscription hooks, sentinel-based error surface, idle keepalive) and pays down the internal/app option ceremony. Ships as `github.com/jkbrsn/wsstat/v4` with a migration table in the CHANGELOG in the 3.0.0 style.

## Problem Statement

The v3 library works correctly for the CLI but misleads external importers:

1. **Silent write failures.** `WriteMessage`/`WriteMessageJSON` (wsstat.go:569, 583) return nothing; a JSON marshal failure or a drop-during-close is logged at debug and vanishes. For a measurement library, a failed send is a result the caller needs.
2. **Unreachable subscription demux.** `SubscriptionOptions.decoder`/`matcher` (subscription.go:47, 51) are unexported with no setters; only same-package tests can populate them. External callers can run exactly one subscription — `dispatchIncoming` delivers matcherless frames only when `len(states) == 1` (subscription.go:374, 394) — and registering a second silently routes frames to *neither*.
3. **Inconsistent error surface.** The package launders message types into stable ints so the transport can swap (wsstat.go:57-65), yet: `ReadMessage` passes raw coder close errors through (`classifyReadErr`, wsstat.go:631-641), forcing callers to import coder/websocket to classify them; a read blocked at Close time returns `ws.ctx.Err()` = `context.Canceled` (wsstat.go:678) instead of `ErrClosed`; `Subscribe` returns an ad-hoc `errors.New("websocket connection is not established")` (subscription.go:158-160) instead of the existing `ErrConnectionNotEstablished` sentinel.
4. **Bounded idle lifetime.** With no active subscription, `readFrame` applies `ws.timeout` (default 5s) to every read (wsstat.go:375-390); a deadline hit takes the pump's error path and closes the connection (wsstat.go:334-340). "Dial, wait 6s, ping" fails with default config, undocumented.
5. **internal/app ceremony.** 25 `WithX` options (client.go:129-267) plus 10 getters (client.go:271-301) exist so the CLI can read back values it just set; count/once validation is duplicated between `Validate` (client.go:433-441) and cmd/wsstat (main.go:256, 299-305). `StreamSubscriptionOnce` duplicates `StreamSubscription` via a `c.count` defer-restore hack (subscription.go:229-231) and has already drifted (a blank line escapes the quiet guard, subscription.go:106-110). The output layer hardcodes stdout across ~55 call sites with a split write-error policy (JSON propagates, output.go:99-108; text swallows, e.g. output.go:127).
6. **Result formatting.** `printURLAndIPSection` dereferences `r.URL` without a nil check (result.go:128-142) — garbled `%!v(PANIC=...)` output on a zero-value Result — and all durations truncate to integer milliseconds (result.go:95, 193-204), so a latency tool prints "0 ms" on localhost.

**Constraints:**

- Module path bumps to `/v4`; v3 stays tagged and importable.
- The CLI (cmd/wsstat, internal/app) migrates in the same change — it is the reference consumer.
- Behavior the CLI's smoke/soak matrix pins (output contracts, exit codes) must not change; this is a library-surface release.
- Component 6 is API-compatible and may ship in a v3 patch first; it is listed here so it isn't forgotten.

## Non-Goals

- Reconnect/redial support — `WSStat` stays single-use (guard added in 68c92e0).
- Synchronous per-write acks (blocking until `conn.Write` returns) — would serialize pipelined writers; the enqueue contract below is deliberate.
- Promoting internal/app to a public package.
- New transports; coder/websocket remains the engine.

## Detailed Design

### 1) Writes return errors; wire-time write stamps

**Goal:** A failed send is observable, and write timings mark wire time, not enqueue time.

New signatures (wsstat.go):

```go
// WriteMessage queues a message for delivery. A nil return means the message was
// queued (delivery is asynchronous); ErrClosed means it was dropped because the
// connection is closing and was never sent.
func (ws *WSStat) WriteMessage(messageType int, data []byte) error

// WriteMessageJSON is WriteMessage for JSON payloads; a marshal failure is
// returned and nothing is queued.
func (ws *WSStat) WriteMessageJSON(v any) error
```

Steps:

1. `enqueueWrite` (wsstat.go:547-563) already returns `bool`; map `false` → `ErrClosed` in both public methods.
2. Move the write-timing stamp from the public methods into `writePump`, around the successful `conn.Write` (wsstat.go:419-425 region): append to `timings.messageWrites` only after `conn.Write` returns nil. Enqueue order equals pump processing order (single channel, single consumer), so ledger pairing is preserved and a full `writeChan` no longer backdates RTT for pipelined callers.
3. A `conn.Write` failure already ends the pump and triggers Close; keep that, but raise the log from debug to warn — it is now the only place a wire-level write failure is recorded.
4. Update `examples/main.go`, internal/app call sites, and godoc ("Sets time: MessageWrites" comments).

### 2) Exported subscription demux hooks

**Goal:** Multi-subscription demux is usable, or a second subscription is an explicit error — never silent misrouting.

Export the two hook types and fields (subscription.go:30, 34, 47, 51):

```go
// Decoder optionally decodes a frame before matching/delivery.
type Decoder func(messageType int, data []byte) (any, error)

// Matcher reports whether this subscription claims the frame.
type Matcher func(messageType int, data []byte, decoded any) bool

type SubscriptionOptions struct {
    ID          string
    MessageType int
    Payload     []byte
    Decoder     Decoder   // was: decoder subscriptionDecoder
    Matcher     Matcher   // was: matcher subscriptionMatcher
    Buffer      int
}
```

Steps:

1. Rename the unexported types/fields; `Subscribe` (subscription.go:180-181) and `dispatchIncoming` need only the field renames.
2. Guard the silent-misroute hole: in `Subscribe`, if another matcherless subscription is already active and the new one is also matcherless, return a new sentinel `ErrSubscriptionConflict` ("a second subscription requires a Matcher"). The `deliverAll := len(states) == 1` fallback (subscription.go:374) stays for the single-subscription case.
3. Fix the doc comment advertising "internal matching heuristics (such as explicit IDs)" — describe what the code does.
4. Same-package tests that set `decoder:`/`matcher:` literals update mechanically.

### 3) Coherent error surface

**Goal:** Callers classify every failure with `errors.Is` against package sentinels and never import coder/websocket.

Add to the sentinel block (wsstat.go:69-75):

```go
// ErrNormalClosure is returned by reads when the peer closed the connection
// cleanly (close status 1000/1001).
var ErrNormalClosure = errors.New("wsstat: connection closed by peer")
```

Steps:

1. `classifyReadErr` (wsstat.go:631-641): wrap normal/going-away closes as `fmt.Errorf("%w: %w", ErrNormalClosure, err)` instead of passing raw coder errors through; keep the "unexpected close error" wrap for other statuses (consider a second sentinel `ErrUnexpectedClosure` so that class is also branchable).
2. `ReadMessage`'s ctx-done branch (wsstat.go:678): return `ErrClosed` instead of `ws.ctx.Err()`.
3. `Subscribe` (subscription.go:158-160): return `ErrConnectionNotEstablished`.
4. Audit remaining `errors.New` returns in the root package for the same treatment.
5. Regression boundary: `TestReadMessageDoesNotMaskCloseError` and `TestCloseHandshakeStatus` in wsstat_test.go assert the current shapes — update them deliberately as part of this component, not incidentally.

### 4) Idle connections survive

**Goal:** A quiet connection with no subscription stays open; the read deadline becomes a keepalive tick.

`readFrame` already distinguishes a deadline hit from a real error via `deadlineHit` (wsstat.go:388). In `readPump`'s error path (wsstat.go:334-340), the subscription case continues the loop; extend the same treatment to the no-subscription case **when no consumer is blocked in ReadMessage** — a deadline hit with a pending read must still surface as a timeout error.

Sketch: track pending reads with an `atomic.Int64` incremented on entry to `ReadMessage`/`ReadMessageJSON`'s blocking select and decremented on exit:

```go
if deadlineHit && ws.pendingReads.Load() == 0 {
    continue // idle tick: nothing was waiting for this frame
}
```

Document on `WithTimeout` (and `DialContext`) that the timeout bounds dial and each in-flight read, not connection lifetime. Add a test mirroring `TestSubscriptionSurvivesIdleBeyondTimeout` for the no-subscription case: dial with a shrunk timeout, sleep past it, `PingPong` must succeed.

### 5) internal/app: Config struct, single stream path, injected writer

**Goal:** The app layer stops wearing a public-library costume; one validation site, one output writer, one write-error policy.

1. **Config struct.** Replace `NewClient(opts ...Option)` (client.go:114-126), the 25 options, and the getters with:

   ```go
   type Config struct {
       Count int; Headers []string; Resolves []string
       RPCMethod, RPCVersion, Text string
       Output Output; ResponseFile string; Body Body; Clip bool
       ShowSecrets bool; ColorMode string; Quiet bool; Verbosity int
       Mode Mode; Once bool; Buffer int; SummaryInterval time.Duration
       Insecure bool; Timeout, CloseGrace time.Duration; ReadLimit int64
       Subprotocols []string; ValidateUTF8, Debug bool
       DebugW io.Writer; Out io.Writer // nil → os.Stderr / os.Stdout
   }
   func NewClient(cfg Config) (*Client, error)  // absorbs Validate()
   ```

   cmd/wsstat fills the struct directly from flags; the duplicated count/once checks (main.go:256, 299-305 vs client.go:433-441) collapse into `NewClient`. The few getters the CLI genuinely needs (`Output()`, `ResponseFilePath()`, `Once()` at main.go:360, 388, 426, 433) either stay as the only survivors or the CLI reads its own flag values.

2. **Delete `StreamSubscriptionOnce`** (subscription.go:239-263). `NewClient` sets `Count = 1` when `Once` is set; `runStream` calls `StreamSubscription` unconditionally. Gate the banner on `!c.once` if the once path's missing banner is intentional. This removes the defer-restore mutation (subscription.go:229-231) and fixes the `-q` blank-line drift (subscription.go:106-110) in one move.

3. **Inject `Out io.Writer`.** Route all ~55 direct `fmt.Print*`/`os.Stdout` sites in output.go through `c.out` (mirroring `debugW`, client.go:107). Retire `captureStdoutFrom` (testing_helpers_test.go:22); output tests assert against a `bytes.Buffer` and can run `t.Parallel`.

4. **One write-error policy: propagate.** Text-path prints adopt the JSON path's behavior (output.go:99-108): return write errors so EPIPE exits non-zero under every output mode. Also fix `runSubscriptionLoop`'s closed-channel busy-spin (subscription.go:88-90): set the drained channel variable to nil on `!ok` instead of `continue`.

### 6) Result formatting (v3-patch eligible)

**Goal:** The formatter never garbles and sub-millisecond latencies are visible.

- Nil-check `r.URL` in `printURLAndIPSection` (result.go:128-142); print `<no url>` or skip the section.
- Replace integer-ms truncation (result.go:95, 193-204) with the CLI's precision approach (up to 3 decimals; internal/app/formatting.go already does this) so `%+v` on a Result matches the tool's own standard.

## Affected Files

- `wsstat.go` — write signatures, sentinels, classifyReadErr, ReadMessage ctx branch, readPump idle tick, writePump stamping
- `subscription.go` (root) — exported Decoder/Matcher, Subscribe sentinel + conflict guard
- `result.go` — nil guard, duration precision
- `measure.go`, `examples/main.go` — adapt to write errors
- `wsstat_test.go`, `measure_test.go` — new tests + deliberate updates to close-error assertions
- `internal/app/client.go` — Config struct, NewClient, getter removal
- `internal/app/subscription.go` — StreamSubscriptionOnce deletion, loop fix
- `internal/app/output.go`, `formatting.go` — injected writer, unified error policy
- `internal/app/*_test.go` — buffer-based output assertions, helper retirement
- `cmd/wsstat/main.go`, `config.go` — Config construction, validation consolidation
- `go.mod` — module path `/v4`
- `CHANGELOG.md`, `README.md`, `docs/architecture/overview.md` — v4 entry with migration table, import-path updates

## Implementation Phases

Each phase leaves the tree green; 1–2 are mechanical, 3–4 carry the behavior risk, 5 is the big internal diff.

1. **Component 6** on v3 (patch release) — no API change.
2. **Module bump** to `/v4` + component 3 (error surface) — small diff, touches the most tests.
3. **Component 1** (write errors + pump stamping) — update all call sites in one commit.
4. **Components 2 and 4** (demux export, idle tick) — independent; either order.
5. **Component 5** (internal/app rework) — largest diff, zero external surface.
6. **Docs pass** — CHANGELOG migration table (v3 → v4: signature changes, new sentinels, idle-behavior change), README library section, ADR if the error-surface decision warrants one.

## Edge Cases & Safety

- **Pump-stamped writes vs ExtractResult:** the stamp now lands after `conn.Write`, so a snapshot taken between enqueue and write sees fewer writes — acceptable; the ledger only ever contains messages that reached the wire.
- **Idle tick vs pending read:** without the `pendingReads` guard, a caller blocked in `ReadMessage` on a dead-silent server would wait forever instead of timing out. The guard preserves the timeout contract for active readers.
- **`ErrNormalClosure` wrapping:** wrap (`%w: %w`), don't replace — `websocket.CloseStatus` must still work on the chain for anyone who does drop to coder types.
- **Subscription conflict guard:** check-and-register must be atomic under `subscriptionMu` to avoid two racing matcherless Subscribes both passing the check.
- **Race exposure:** every component touches pump/consumer seams; the 16x shuffled `-race` CI job is the backstop — run `make test RACE=1` locally per phase.

## Acceptance Criteria

- `grep -rn 'coder/websocket' examples/ internal/app/` shows no imports needed for error classification (message-type ints and sentinels suffice).
- A dropped or failed write is observable by the caller (error return) — no debug-only failure paths remain on the write surface.
- Two subscriptions with matchers both receive their frames (new test); two matcherless subscriptions fail fast with `ErrSubscriptionConflict`.
- Dial + idle sleep past timeout + `PingPong` succeeds; a blocked `ReadMessage` against a silent server still times out.
- `internal/app` has no functional options and no stdout-swap test helper; output tests run under `t.Parallel`.
- `fmt.Sprintf("%+v", &wsstat.Result{})` contains no `PANIC` marker; sub-ms RTTs render with decimals.
- CHANGELOG carries a v3→v4 migration table; smoke + soak matrices pass unchanged.

## Open Questions

- Fold clean-close into `ErrClosed` instead of a separate `ErrNormalClosure`? Separate is proposed: "we closed" vs "peer closed" is a distinction measurement callers act on.
- Should `WriteMessage` optionally block until wire-write (a `WriteMessageSync` or option) for callers who want hard delivery confirmation? Deferred unless a concrete consumer asks.
- Keep any getters on `app.Client`, or have cmd/wsstat track the three values it needs from its own flags? Cosmetic; decide at implementation.
