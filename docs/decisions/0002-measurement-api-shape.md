# 0002. Measurement API shape: connection methods plus free one-shot functions

Date: 2026-06-18
Commit: d62f4df
Status: proposed

## Context

v3 freezes the public API. The v2 convenience layer had grown to nine `MeasureLatency*` free
functions across a `{text, JSON, ping} x {single, burst} x {plain, WithContext}` matrix. It was
asymmetric — only the three `*BurstWithContext` variants took `ctx`/`opts`, so single-shot calls
could be neither canceled nor configured — and inconsistent (return types `[]byte` vs `[]string`,
error wrapping `%v` vs `%w` vs none). Usage showed the matrix was largely speculative: only the
three `*BurstWithContext` variants (CLI) and plain `MeasureLatency` (example) had callers; five of
nine had none. The obvious fix — ctx+opts on every function — would have frozen an even larger
surface.

The underlying tension is that one-shot measurement and a live connection are different lifecycles.
A live connection (subscriptions, multi-step) is a stateful handle; a one-shot dials, sends, reads,
closes, and is discarded. A terminal `Measure*` *method* models the one-shot badly — it makes
`WSStat` a single-use receiver — whereas a free function models it honestly and is the single place
to get New -> Dial -> ... -> Close cleanup right (where the connection-leak fix lives). The repo
uses both lifecycles.

## Decision

Two API surfaces, split along lifecycle:

- **Connection API** — methods on `*WSStat` for live/stateful flows: `New`, `DialContext` (the only
  dial entry; `Dial` removed), `WriteMessage`/`ReadMessage`, `WriteMessageJSON`/`ReadMessageJSON`,
  `PingPong`, `Subscribe`/`SubscribeOnce`, `Close`, `ExtractResult`.
- **One-shot API** — three free functions, each owning the full lifecycle including cleanup on every
  error path: `MeasureText(ctx, target, msgs []string, ...Option)`,
  `MeasureJSON(ctx, target, msgs []any, ...Option)`, `MeasurePing(ctx, target, count int, ...Option)`.
  Single message = a one-element slice; headers via `WithHeaders`.

The nine v2 wrappers and `Dial` are removed; `OneHitMessage`/`OneHitMessageJSON` become unexported
helpers.

## Consequences

- Every public measurement path is cancelable and configurable; return types normalize to
  `[]string`/`[]any`.
- `DialContext` + context-aware read/write become load-bearing: the one-shots are plain synchronous
  functions that honor `ctx` through them, letting the ~120-line goroutine/channel cancellation
  shuttle be deleted. Read/write must honor a per-call ctx/deadline, not only `ws.ctx`.
- The `%w` / exported-sentinels error contract has one home: the three free functions.
- Breaking vs v2 (names, signatures, `Dial` removal); covered by the v3 CHANGELOG migration table.
  In-tree migration is four call sites (`internal/app/measurement.go:23,54,77`, `_example/main.go:28`)
  plus `internal/app/subscription.go:34` (`Dial` -> `DialContext`).
- `proposed` until the reshape ships; flip to `accepted` after review against the code.

## Sources

- `docs/design/measure-wrapper-api.md` — full options analysis (ephemeral; this branch only)
- `wrappers.go`, `wsstat.go` — the surface being replaced
