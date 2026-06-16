# Migrate wsstat from gorilla/websocket to coder/websocket

- **Date:** 2026-06-16
- **Commit:** b5bbeba (line numbers are "as of" this commit; the tree has since moved to `3c8b74e` — re-find symbols by name, do not trust line numbers)
- **Branch:** main
- **Module:** `github.com/jkbrsn/wsstat/v2` → bumps to **`github.com/jkbrsn/wsstat/v3`** (see Decisions)

## Decisions

Locked in before implementation:

1. **Library goes to v3 (breaking changes allowed); CLI behavior is unchanged.** The library is a standalone importable package, so simplifying its public API (dropping `ReadPong`, collapsing the ping/pong split) is a semver-major change and the module path bumps to `/v3`. The CLI (`cmd/wsstat`, `internal/app`) is only an internal consumer: its flags, validation, and output stay byte-for-byte identical. The churn in `cmd/` and `internal/app/` is mechanical (import path `/v2 → /v3`, adapt to new library calls), not user-facing.
2. **Keep the int-based message-type API.** Define `wsstat.TextMessage = 1` / `wsstat.BinaryMessage = 2` and convert internally. Do **not** re-export coder's `websocket.MessageType` — that would force every library consumer to import `github.com/coder/websocket` just for constants, coupling them to the transport dependency we want to keep swappable.
3. **Drop `ReadPong` entirely.** Its write-ping-then-read-pong semantics do not exist in coder's model; `PingPong` records both timings around the single blocking `conn.Ping()`.
4. **Accept coder's internal 5s close-handshake timeout.** No 1s `CloseNow()` wrapper. Simpler `Close()`; a dead peer can stall close up to 5s, which is acceptable.
5. **Pong test relies on coder's automatic pong.** Drop the manual server-side ping handler; the test only asserts `MessageCount`, never pong payload.
6. **Ping bursts run sequentially** as N `PingPong()` calls (overlap is not a tested property; mean-RTT math is unaffected).

## Problem

`gorilla/websocket` has been unmaintained for several years. Move both production and test code to the actively maintained `coder/websocket` (v1.8.15, already present in `go.mod` as an indirect dependency).

The migration is not a mechanical import swap. wsstat's reason for existing is the per-phase latency breakdown (DNS / TCP / TLS / WS handshake / message RTT), and the two libraries dial connections through fundamentally different mechanisms. The instrumentation must be re-homed without changing the measured numbers or the public API.

## Goals

- Replace all `gorilla/websocket` usage with `coder/websocket` across production code, tests, and the example.
- Bump the library to `/v3` and keep the int-based message-type API (`WriteMessage(int, …)`, `ReadMessage() (int, …)`, `SubscriptionOptions.MessageType int`, the `MeasureLatency*` wrappers, all `WithX` options) backed by `wsstat.TextMessage`/`BinaryMessage` constants. `ReadPong` is removed (the one intentional public-API break).
- Keep the CLI unchanged: same flags, validation, and output. Only internal import paths and library call sites change.
- Preserve timing semantics: the `Result` phase durations must remain accurate and computed the same way.
- `make lint && make test` pass, including the race-detector + 16x repetition CI run.

## Non-Goals

- No change to the `Result` struct shape or the `calculateResult` math.
- No new transport features (HTTP/2, proxies) beyond what is needed to preserve current behavior.
- No redesign of the subscription layer; only the underlying read/write/message-type plumbing changes.

## RFC 6455 Adherence Wins

Beyond removing an unmaintained dependency, the migration tightens protocol correctness because the library — not hand-rolled wsstat code — owns the framing and control-frame logic. Verified against coder v1.8.15 source:

- **Correct two-way closing handshake** (`close.go:99,157-228`). The primary win: write Close frame, read until the peer's Close echo, then drop the socket. Resolves the Iris-observed ungraceful close (server `1006` / `use of closed network connection`). See section 5.
- **Close-code validation** (`validWireCloseCode`, `close.go:279`). Reserved codes (1004/1005/1006/1015) are rejected, and close status is surfaced through a structured `CloseError` + `CloseStatus(err)` helper — replacing wsstat's manual `FormatCloseMessage` + string-ish `IsCloseError` matching.
- **Library-owned control frames** (`read.go:201,290-323`). Incoming pings are answered with a pong automatically inside the read loop, and control-frame payload limits (≤125 bytes, `frame.go:142`) are enforced. This removes wsstat's manual `PingMessage` / `SetPongHandler` plumbing (section 4).
- **Stricter frame validation** in coder's read path (RSV bits, control-frame fragmentation rules, payload-length encoding) that the current manual usage does not check.

Non-improvement to be explicit about: coder does **not** validate UTF-8 in text frames (neither does wsstat's current gorilla usage), so that gap is unchanged by the migration.

## Constraints & Key API Differences

`coder/websocket` differs from gorilla in ways that drive the design:

- **No dial hooks.** gorilla's `Dialer.NetDialContext` / `NetDialTLSContext` (the instrumentation seam in `newDialer`, `wsstat.go:691`) do not exist. `Dial(ctx, url, *DialOptions)` (`dial.go:120`) runs the handshake through an `*http.Client`. Instrumentation must move into a custom `http.Transport`.
- **Context, not deadlines.** No `SetReadDeadline` / `SetWriteDeadline`. Every `conn.Read(ctx)` / `conn.Write(ctx, …)` takes a per-call context; timeouts come from `context.WithTimeout`.
- **Ping is a synchronous round-trip.** `conn.Ping(ctx)` (`conn.go:223`) sends a ping *and blocks until the matching pong*. There is no `PingMessage` write + `SetPongHandler` split.
- **Only two message types.** `MessageText = 1`, `MessageBinary = 2` (`conn.go:24`). Text/Binary are numerically identical to gorilla, so the public `int` API can be preserved by internal conversion. `CloseMessage` / `PingMessage` / `PongMessage` do not exist as message types — they are handled inside the library and never surface from `Read`.
- **32 KB default read limit.** `conn.SetReadLimit(-1)` disables it (`read.go:97`). gorilla had no limit; preserve old behavior.
- **Different close model.** `conn.Close(StatusNormalClosure, "")` performs the close handshake (`close.go:99`); `CloseNow()` skips it. Close-error inspection uses `websocket.CloseStatus(err) StatusCode` (`close.go:78`), not `IsCloseError` / `IsUnexpectedCloseError`.
- **Server side.** `websocket.Accept(w, r, *AcceptOptions)` (`accept.go:102`) replaces `Upgrader.Upgrade`. Origin checks: `AcceptOptions.InsecureSkipVerify` / `OriginPatterns`.
- **Compression** defaults to disabled on both libraries — no behavior change.

## Detailed Design

### 1. Dial instrumentation via custom `http.Transport`

This is the core of the migration. Replace the `*websocket.Dialer` field and `newDialer` with an `*http.Client` whose transport carries the existing dial logic.

The existing helpers `resolveDialTargets` (`wsstat.go:743`) and `dialWithAddresses` (`wsstat.go:780`) are reusable nearly verbatim — they already do DNS-override lookup, multi-address iteration, TCP timing (`tcpConnected`), and the manual TLS handshake with `tlsHandshakeDone` + `TLSState` capture. They move from closures on the `Dialer` to functions wired into a transport.

```go
// newHTTPClient builds the instrumented client coder's Dial will use.
// Replaces newDialer. Sets timings: dnsLookupDone, tcpConnected, tlsHandshakeDone.
func newHTTPClient(
    result *Result,
    timings *wsTimings,
    tlsConf *tls.Config,
    timeout time.Duration,
    resolves map[string]string,
) *http.Client {
    transport := &http.Transport{
        Proxy:             http.ProxyFromEnvironment, // match http.DefaultTransport, or omit
        DisableKeepAlives: true,                      // one connection per WSStat dial
        ForceAttemptHTTP2: false,                     // WebSocket upgrade requires HTTP/1.1

        DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
            target, err := resolveDialTargets(ctx, addr, timings, resolves)
            if err != nil {
                return nil, err
            }
            return dialWithAddresses(ctx, network, target, timeout, timings, nil)
        },

        DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
            target, err := resolveDialTargets(ctx, addr, timings, resolves)
            if err != nil {
                return nil, err
            }
            wrap := func(netConn net.Conn) (net.Conn, error) {
                // identical body to the current NetDialTLSContext wrap closure
                // (wsstat.go:714-734): tls.Client, Handshake, set tlsHandshakeDone,
                // capture result.TLSState.
            }
            return dialWithAddresses(ctx, network, target, timeout, timings, wrap)
        },
    }
    return &http.Client{
        Transport: transport,
        Timeout:   timeout, // coder uses this as the overall handshake timeout
    }
}
```

Store the client on the struct in place of `dialer`:

```go
type WSStat struct {
    // ...
    conn       atomic.Pointer[websocket.Conn] // now coder's *websocket.Conn
    httpClient *http.Client                   // replaces dialer *websocket.Dialer
    // ...
}
```

`dialStart` is still set in `Dial` immediately before the handshake. `wsHandshakeDone` is still set immediately after `Dial` returns. The `dnsLookupDone` / `tcpConnected` / `tlsHandshakeDone` timings continue to be set inside the reused dial functions. No `calculateResult` change needed.

**Verification risk:** confirm the transport's `DialContext`/`DialTLSContext` actually fire for the upgrade request (they do for HTTP/1.1) and that the manual TLS path is taken for `wss://`. coder selects TLS based on URL scheme; since the transport dials, ensure `DialTLSContext` is used for `https`/`wss`.

### 2. Rewrite `Dial`

`Dial` (`wsstat.go:316`) changes its dial call and header plumbing:

```go
func (ws *WSStat) Dial(targetURL *url.URL, customHeaders http.Header) error {
    ws.result.URL = targetURL
    headers := cloneHeaders(customHeaders)
    ws.timings.dialStart = time.Now()

    conn, resp, err := websocket.Dial(ws.ctx, targetURL.String(), &websocket.DialOptions{
        HTTPClient: ws.httpClient,
        HTTPHeader: headers,
    })
    if err != nil {
        // resp may be non-nil; coder lets you read up to 1024 bytes of body.
        // Preserve the existing "failed dial response '%s'" message shape.
    }
    ws.timings.wsHandshakeDone = time.Now()

    conn.SetReadLimit(-1) // restore gorilla's unlimited-read behavior
    ws.conn.Store(conn)

    // Pong handling: NO SetPongHandler. See section 4.
    // IP lookup block (wsstat.go:343-376) is unchanged.
    // Start pumps (wsstat.go:379-381) unchanged.
    // RequestHeaders/ResponseHeaders capture (wsstat.go:384-385) unchanged;
    // resp.Header still works.
}
```

`documentedDefaultHeaders` (`wsstat.go:34`) and `applyDefaultHeaders` stay — coder sends the same RFC 6455 handshake headers (`Upgrade`, `Connection`, `Sec-WebSocket-Key`, `Sec-WebSocket-Version`), so the documented-defaults reporting remains accurate.

### 3. Message-type conversion layer

Keep the public `int` API. Add unexported converters and route all internal `Read`/`Write` through them:

```go
func toCoderType(mt int) websocket.MessageType  // 1->MessageText, 2->MessageBinary
func fromCoderType(mt websocket.MessageType) int // MessageText->1, MessageBinary->2
```

`websocket.TextMessage` references in production code (`subscription.go:164`, `wrappers.go`, `internal/app/subscription.go:119,127,129`, `_example/main.go:43`) become the literal `wsstat`/local text constant. Recommend defining package-level `const TextMessage = 1` and `BinaryMessage = 2` in the `wsstat` package so callers (including `internal/app`) keep a stable symbol and don't import coder for constants.

`messageTypeLabel` (`internal/app/types.go:216`): the `CloseMessage` / `PingMessage` / `PongMessage` cases (lines 222-226) become unreachable for received frames (coder never returns them from `Read`). Keep `text`/`binary`; drop or comment the dead cases.

### 4. Pump rewrite: contexts, and the ping/pong split

**`readPump`** (`wsstat.go:222`): replace `conn.SetReadDeadline(...)` + `conn.ReadMessage()` with a per-iteration timeout context:

```go
readCtx, cancel := context.WithTimeout(ws.ctx, ws.timeout)
mt, p, err := conn.Read(readCtx)
cancel()
```

Convert `mt` via `fromCoderType`. The error branch's close-error classification (`wsstat.go:529`, `IsUnexpectedCloseError`) becomes:

```go
status := websocket.CloseStatus(err) // -1 if not a close error
if status != websocket.StatusNormalClosure && status != websocket.StatusGoingAway {
    // treat as unexpected close
}
```

**`writePump`** (`wsstat.go:271`): replace `SetWriteDeadline` + `WriteMessage` with `conn.Write(writeCtx, toCoderType(write.messageType), write.data)` using a per-write timeout context. The `PingMessage` sentinel no longer routes here — see below.

**Ping/pong.** This is the second-largest change. Today: `PingPong` → `WriteMessage(PingMessage, nil)` (`wsstat.go:512`) flows through `writePump`; `SetPongHandler` (`wsstat.go:333`) feeds `pongChan`; `ReadPong` (`wsstat.go:578`) consumes it. coder collapses this into one blocking `conn.Ping(ctx)`.

Recommended approach — make `PingPong` call `Ping` directly and record both timings around it, bypassing the write/pong channels:

```go
// PingPong sends a ping and blocks for the pong. Sets messageWrites + messageReads.
func (ws *WSStat) PingPong() error {
    conn := ws.conn.Load()
    if conn == nil {
        return ws.ctx.Err()
    }
    ws.recordWrite()                 // append time.Now() to messageWrites (locked)
    pingCtx, cancel := context.WithTimeout(ws.ctx, ws.timeout)
    defer cancel()
    if err := conn.Ping(pingCtx); err != nil {
        return err
    }
    ws.recordRead()                  // append time.Now() to messageReads (locked)
    return nil
}
```

Consequences to handle:
- `pongChan`, `SetPongHandler`, and `ReadPong` are removed (Decision 3). `ReadPong` is exported, so its removal is the one intentional public-API break that justifies the `/v3` bump. `PingPong` now records both `messageWrites` and `messageReads` around the single `conn.Ping()` call.
- `Ping` requires the read pump to be actively reading so the library can process the inbound pong control frame. The read pump loops on `conn.Read`, which satisfies this. Confirm no deadlock when `Ping` and `readPump` run concurrently (coder supports one concurrent reader + `Ping`).
- `MeasureLatencyPingBurst` (`wrappers.go:155`) and `MeasureLatencyPingBurstWithContext` (`wrappers.go:303`) currently "write N pings, then read N pongs" using `WriteMessage(PingMessage,…)` + `ReadPong`. Rewrite as N sequential `PingPong()` calls (Decision 6 — preserves mean-RTT math). Verify `wrappers_test.go` does not assert in-flight overlap before committing to sequential.

Remove `websocket.PingMessage` references at `wrappers.go:167`, `wrappers.go:334`, `wsstat.go:512`.

### 5. Rewrite `Close` (also fixes the RFC 6455 close-handshake bug)

`Close` (`wsstat.go:611`) both simplifies *and* must fix a pre-existing defect carried over from gorilla. Replace the manual `SetReadDeadline(now)` + `WriteControl(CloseMessage, FormatCloseMessage(...))` + `conn.Close()` block (`wsstat.go:624-643`) with coder's `conn.Close(StatusNormalClosure, "")`, which performs the **full two-way RFC 6455 closing handshake**: it writes the Close frame and then reads until it receives the peer's Close echo before tearing down the TCP socket (`close.go:99` → `closeHandshake` → `waitCloseHandshake`, `close.go:157-228`).

**Ordering is load-bearing.** The current `Close()` calls `ws.cancel()` *first* (`wsstat.go:613`), which stops the read pump. In the coder design the read pump's per-read context derives from `ws.ctx`, so canceling first would kill the read pump before the echo arrives and force an immediate TCP teardown — reproducing the exact ungraceful-close bug observed against Iris (server logs `close handshake ... did not complete cleanly` / `use of closed network connection`; a strict peer would mark it 1006 instead of a clean 1000). The graceful `conn.Close()` must run **before** `ws.cancel()`:

```go
func (ws *WSStat) Close() {
    ws.closeOnce.Do(func() {
        conn := ws.conn.Load()

        // 1. Graceful close FIRST, while the read pump is still alive so the
        //    server's Close echo is read off the socket before TCP teardown.
        //    coder's Close does writeClose + waitCloseHandshake internally.
        if conn != nil {
            if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
                ws.log.Debug().Err(err).Msg("close handshake")
            }
        }

        // 2. Record closeDone AFTER the handshake completes -> accurate timing.
        ws.timings.closeDone = time.Now()

        // 3. Now stop pumps, finalize subscriptions, drain wgPumps, Store(nil).
        ws.cancel()
        for _, state := range ws.activeSubscriptions() {
            ws.finalizeSubscription(state, context.Canceled)
        }
        ws.calculateResult()
        // ... existing pump-drain waitgroup + conn.Store(nil) (wsstat.go:649-670) ...
    })
}
```

Why this is safe with the dedicated read pump:
- `conn.Close()` sends the frame (separate write path), then `waitCloseHandshake` contends with the read pump for coder's `readMu` (bounded 5s). When the server's echo arrives, the read pump's `conn.Read` consumes it off the socket and returns a `CloseError`, releasing `readMu`; `Close` then completes and force-closes TCP. The echo is read **before** TCP teardown, so the server sees a clean handshake.
- No new deadlock: the pump defers (`wsstat.go:223-226`, `:272-275`) already call `wgPumps.Done()` *before* `ws.Close()`, so the drain step never waits on the goroutine that invoked it. `closeOnce` keeps re-entrant calls (pump defer + external caller) idempotent.
- Worst case (peer never echoes): coder's internal 5s timeout in `waitCloseHandshake` bounds the delay, then it force-closes. Decision 4 accepts this 5s bound as-is — no `CloseNow()` wrapper.

Drop `FormatCloseMessage` / `IsCloseError` entirely.

### 6. Test server migration

Every test echo server uses gorilla's `Upgrader`. Migrate each to `websocket.Accept`:

- `Upgrader{CheckOrigin: func(*http.Request) bool { return true }}` → `websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})`.
- `conn.ReadMessage()` → `conn.Read(ctx)`; `conn.WriteMessage(websocket.TextMessage, b)` → `conn.Write(ctx, websocket.MessageText, b)`. Server handlers need a context (use `r.Context()` or `context.Background()`).
- `conn.Close()` (server) → `conn.Close(websocket.StatusNormalClosure, "")` or `CloseNow()`.
- Assertions like `assert.Equal(t, websocket.TextMessage, msgType)` (`internal/app/client_subscription_test.go:248,256,269,277`): since the API still returns `int` and `wsstat.TextMessage == 1`, compare against the `wsstat` constant.

**Special case — the pong test** (`internal/app/client_measurement_test.go`, the `SetPingHandler` server): the server installs `conn.SetPingHandler(...)` and replies with a custom `WriteControl(websocket.PongMessage, …)`. Confirmed: the test only asserts `MessageCount` (and no error) — it never checks pong payload. Per Decision 5, drop the manual handler entirely and rely on coder's automatic pong response; the `Accept`-based echo server is sufficient.

Files with test servers / message-type refs to migrate:
- `wsstat_test.go` (upgrader at `:815`, close check `:834`, many `WriteMessage`/`MessageType` refs)
- `wrappers_test.go`
- `internal/app/testing_helpers.go` (upgrader at `:105`, read/write `:120-133`)
- `internal/app/client_measurement_test.go` (upgraders at `:18,79,139`; pong handler `:150`)
- `internal/app/client_subscription_test.go` (`MessageType` / assertions)

### 7. `go.mod` / `/v3` path sweep / example

- Change the module path in `go.mod` from `github.com/jkbrsn/wsstat/v2` to `github.com/jkbrsn/wsstat/v3`.
- Sweep every internal import of `github.com/jkbrsn/wsstat/v2` → `/v3`: `cmd/wsstat/`, `internal/app/`, `_example/`, and any test files. `grep -rn "jkbrsn/wsstat/v2" --include="*.go" .` must return nothing afterward.
- Promote `github.com/coder/websocket` from indirect to a direct require; remove `github.com/gorilla/websocket`. Run `go mod tidy` (ask before committing manifest changes per repo policy).
- Update non-Go references to the module path: README install/import snippets, any `go install github.com/jkbrsn/wsstat/v2/...` lines, and `_example/` doc text.
- `_example/main.go`: swap the import and use `wsstat.TextMessage`.

## Affected Files

| File | Change |
|---|---|
| `wsstat.go` | Core: client+transport replaces dialer; `Dial`, `readPump`, `writePump`, `Close`, `PingPong`, `ReadMessage` close-error branch, conn field type, message-type converters, `SetReadLimit(-1)`. Remove `pongChan`/`SetPongHandler` path. |
| `wrappers.go` | `TextMessage` constant source; rewrite ping-burst flows (`:155`, `:303`); drop `PingMessage`. |
| `subscription.go` | `TextMessage` default (`:164`) → `wsstat` constant. |
| `internal/app/types.go` | `messageTypeLabel` (`:216`) — keep text/binary, drop close/ping/pong cases. |
| `internal/app/subscription.go` | `TextMessage` refs (`:119,127,129`). |
| `_example/main.go` | Import path `/v3` + `wsstat.TextMessage` constant. |
| `cmd/wsstat/*.go` | Import path `/v2 → /v3` only (no behavior change). |
| `internal/app/*.go` (non-test) | Import path `/v2 → /v3`; `TextMessage` refs; adapt to new library calls. |
| `go.mod` / `go.sum` | Module path `/v2 → /v3`; drop gorilla, promote coder; `go mod tidy`. |
| `README.md` / docs | Update module path in install/import snippets and `go install` lines. |
| `wsstat_test.go` | Server `Accept`, read/write contexts, close checks, type asserts. |
| `wrappers_test.go` | Same test-server migration. |
| `internal/app/testing_helpers.go` | Server `Accept`, read/write contexts. |
| `internal/app/client_measurement_test.go` | Server `Accept`; pong-handler test rework. |
| `internal/app/client_subscription_test.go` | `MessageType` asserts against `wsstat` constants. |

## Edge Cases & Safety

- **Timing parity.** The whole point of the package. After migration, run a comparison (same endpoint, both versions) and confirm DNS/TCP/TLS/WS phase durations are in the same ballpark. The `resolves` DNS-override path and the multi-address fallback in `dialWithAddresses` must still set `dnsLookupDone`/`tcpConnected` exactly once per dial.
- **Read limit.** Forgetting `SetReadLimit(-1)` silently breaks any test or user sending >32 KB frames, and closes the connection with `StatusMessageTooBig`. Set it immediately after `Dial`.
- **Ping ↔ read-pump coupling.** `conn.Ping` blocks until the read side processes the pong. If the read pump has exited (context canceled, connection closing), `Ping` must fail fast via its timeout context derived from `ws.ctx`, not hang.
- **Concurrent write safety.** coder allows one concurrent writer; the single `writePump` preserves this. Ensure the new `PingPong` (which calls `conn.Ping` directly, off the write pump) doesn't violate it — `Ping` uses a separate control-write path and is safe alongside `Write`, but confirm against coder's concurrency docs.
- **`http.Client.Timeout` vs context.** coder wraps the dial context with `HTTPClient.Timeout` if set. Setting both `Timeout` on the client and a deadline-free `ws.ctx` is fine; just avoid double-counting the dial timeout.
- **Close-handshake ordering.** See section 5. Calling `ws.cancel()` before the graceful `conn.Close()` silently reintroduces the ungraceful-close bug (server-side `1006`/`use of closed network connection`). The graceful close must run while the read pump is still alive. This is the single highest-value correctness win of the migration and must have a regression test.
- **Race + 16x CI.** The pump/context rewrite is the highest race risk. Run `go test ./... -race -count=16` (the `RACE=1` make path) before declaring done.

## Implementation Phases

1. **Core dial path.** Introduce `newHTTPClient` + transport, swap the struct field, rewrite `Dial`, add message-type converters and `wsstat.TextMessage`/`BinaryMessage` constants, `SetReadLimit(-1)`. Get a single text round-trip working (skip ping for now).
2. **Pumps + close.** Rewrite `readPump`/`writePump` to contexts; rewrite `Close`; rewrite close-error classification in `ReadMessage`.
3. **Ping/pong.** Rewrite `PingPong`, the two ping-burst wrappers; remove `pongChan`/`SetPongHandler`/`ReadPong`/dead `PingMessage` paths.
4. **Tests + example + manifest + `/v3` sweep.** Migrate all test servers and assertions, the pong-handler test, `_example`, and `go.mod` (module path `/v3`, drop gorilla, promote coder). Sweep all `/v2 → /v3` import paths across `cmd/`, `internal/app/`, tests, README. Run lint + race suite.

## Acceptance Criteria

- No remaining `gorilla/websocket` import anywhere: `grep -rn "gorilla/websocket" --include="*.go" .` returns nothing.
- No remaining `/v2` module path: `grep -rn "jkbrsn/wsstat/v2" --include="*.go" .` returns nothing; `go.mod` declares `github.com/jkbrsn/wsstat/v3`.
- `make lint` passes (gofmt -s, ≤100-char soft / ≤80-line functions excluding tests).
- `make test` and the race + 16x run pass.
- Library API change is limited and intentional: `WriteMessage`/`ReadMessage`/`OneHitMessage*`/`PingPong`/`Subscribe*` signatures and all `WithX` options unchanged (int message-type API preserved via `wsstat.TextMessage`/`BinaryMessage`); `ReadPong` removed.
- CLI unchanged: `cmd/wsstat` flags, validation, and output identical; only import paths and library call sites changed.
- Phase timings remain populated and plausible for both `ws://` and `wss://` targets (manual smoke test against a real endpoint).
- **Clean close handshake.** A test server that records the close status sees `StatusNormalClosure` (1000), not `1006`, for the connect→send→read→close flow. Verifies the Iris-reported defect is resolved.
- `CHANGELOG.md` and `VERSION` updated. This is a **major** version bump (`/v3`): new `3.0.0` line in `CHANGELOG.md`, `VERSION` set to `3.0.0`.

## Resolved Questions

All resolved before implementation — see Decisions for the rationale.

1. **`ReadPong` exported API.** Resolved: **removed** (Decision 3). Its removal is the intentional break behind the `/v3` bump.
2. **Ping-burst concurrency.** Resolved: **sequential** `PingPong()` calls (Decision 6). Verify `wrappers_test.go` does not assert in-flight overlap.
3. **Pong test intent.** Resolved: test asserts only `MessageCount`, so **rely on coder's auto-pong** and drop the manual handler (Decision 5).
4. **Public message-type type.** Resolved: **keep the `int`-based API** via `wsstat.TextMessage`/`BinaryMessage`; do not re-export coder's type (Decision 2).
5. **Close grace bound.** Resolved: **accept coder's 5s default** (Decision 4); no `CloseNow()` wrapper.
