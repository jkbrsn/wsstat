# Architecture overview

How wsstat is put together, at the level that outlives any single function signature. For exact
types and signatures, read the godoc; for *why* a seam is shaped the way it is, read the ADRs
(`docs/decisions/`). This document names files and symbols, not line numbers, on purpose.

wsstat measures the timing of a WebSocket connection — DNS, TCP, TLS, the WS upgrade handshake,
message round-trips, and the closing handshake — and exposes those as a `Result`. It ships as both
a Go library (the root package) and a CLI (`cmd/wsstat`).

## Layers

Three layers, importing strictly downward (`internal/` enforces that outside code only sees the
core):

1. **Core** — the root package (`wsstat.go`, `measure.go`, `result.go`, `subscription.go`). Wraps
   `coder/websocket` with timing instrumentation. Owns the `WSStat` connection handle, the `Result`
   value, the subscription system, and the free one-shot measurement functions. No knowledge of the
   CLI or of output formatting.
2. **App** (`internal/app/`) — the `Client` that orchestrates a measurement or subscription run and
   formats output (text / JSON / raw, plus the body/clip/verbosity axes). Drives the core; knows
   nothing about flag parsing.
3. **CLI** (`cmd/wsstat/`) — parses flags, validates the URL (auto-adds `wss://`), builds config,
   and dispatches the `measure` / `stream` subcommands to the app layer.

All three layers use the **functional-options pattern**: a constructor takes `...Option`, seeds
defaults, then applies the options. `New(opts...)` in the core, the `Client` constructor in the
app, config assembly in the CLI. New configuration is added as an option, never a new constructor.

## Public API surface

The library exposes two surfaces, split along **connection lifecycle** (see
[ADR 0002](../decisions/0002-measurement-api-shape.md)):

- **Connection API** — methods on `*WSStat` for live, stateful flows (subscriptions, multi-step
  exchanges): construct with `New`, dial with `DialContext`, then read/write/ping/subscribe, and
  `Close`. The handle is held across the interaction.
- **One-shot API** — free functions (`MeasureText` / `MeasureJSON` / `MeasurePing`) for
  fire-and-forget measurement. Each owns the entire dial → send → read → close lifecycle, including
  cleanup on every error path, and returns a `Result`. No handle outlives the call.

The split is deliberate: a one-shot modelled as a method would make `WSStat` a single-use receiver,
and centralising the lifecycle in one free function is the single place to get teardown right. The
exact signatures live in godoc.

## Timing instrumentation

The core does not use a stock HTTP client. `newHTTPClient` builds an instrumented `*http.Transport`
whose `DialContext` / `DialTLSContext` callbacks are where per-phase timestamps are captured —
`dnsLookupDone`, `tcpConnected`, `tlsHandshakeDone` — while `Dial`/`DialContext` records the dial
start and the completed WS upgrade, and `Close` records the close. (This hand-built transport
replaces the dialer hooks the pre-v3 gorilla transport provided; the coder migration moved the
instrumentation here.) Message round-trip times are recorded by the read/write pumps under a small
mutex.

`calculateResult` turns those raw timestamps into the `Result`'s per-phase and cumulative durations.
`Result` carries a custom `fmt.Formatter` for compact vs verbose rendering.

## Connection lifecycle and close-grace

A dialed `WSStat` runs two goroutines: a **read pump** and a **write pump**, coordinated by a
context and a `sync.WaitGroup`. Incoming frames are dispatched to subscriptions first and otherwise
delivered on the read channel; outgoing frames flow through the write channel. The live `*websocket.Conn`
is held in an `atomic.Pointer` so the pumps can load-and-nil-check it without locking.

`Close` is idempotent (`sync.Once`) and its ordering is **load-bearing**:

1. **Graceful close first**, while the read pump is still alive, so the peer's Close echo is read
   off the socket before teardown. `gracefulClose` performs coder's two-way RFC 6455 closing
   handshake (write Close frame, await the echo), bounded by `closeGrace` (default a few seconds).
   coder's own `Close` blocks on a hard-coded ~5s wait for the echo; a write-only or non-echoing
   peer never echoes, so on `closeGrace` expiry wsstat forces the **raw** socket shut to unblock
   coder's read instead of stalling the full 5s.
2. **Then cancel the context**, stopping the pumps and finalizing any subscriptions.
3. **Then wait** for the pumps to drain, bounded by a timeout.

Cancelling the context *before* the graceful close would kill the read pump before the echo
arrives and force an ungraceful 1006 teardown — hence the order. The raw `net.Conn` is captured
during dialing (the underlying socket, not the TLS wrapper) precisely so step 1 can force it shut;
`closeGrace` is exposed via `WithCloseGrace`.

## Subscriptions

`Subscribe` / `SubscribeOnce` register a subscription against the live connection. The read pump
calls `dispatchIncoming`, which routes each frame to matching subscriptions before falling back to
the plain read channel. Each subscription has its own buffered channel guarded by a per-subscription
mutex and a `closed` flag, so delivery can never send on a closed channel and `finalizeSubscription`
can never double-close. The registry of active subscriptions is guarded by an `RWMutex`; per-sub
counters (message/byte counts) are atomic. `finalizeSubscription` is the single idempotent exit
point — closing the buffer and the done channel exactly once — funnelled to from every path
(unsubscribe, cancel, dispatch error, connection close).

## DNS resolve-override path

`WithResolves` supplies `host:port -> IP` overrides (the CLI's `--resolve`, curl-style). The
instrumented transport's dial callbacks run every address through `resolveDialTargets`, which
consults the override map first and otherwise falls back to `net.DefaultResolver.LookupHost`;
`dialWithAddresses` then tries the resolved addresses in order.

The security-critical property: an override changes only the **dial address**. The TLS
`ServerName` is set to the **original hostname**, never the override IP, so certificate hostname
verification still applies under `--resolve`. Overriding where you connect does not bypass who you
verify against — there is no MITM shortcut here. (`-insecure` is the separate, explicit opt-out
that sets `InsecureSkipVerify` while keeping TLS.)

## Where to look next

- Exact API: godoc on the root package and `internal/app`.
- Decisions and their rationale: `docs/decisions/` (ADRs).
- Conventions (errors, logging, style): the repo `CLAUDE.md` / `AGENTS.md`.
