# Design proposal: shape of the `MeasureLatency*` wrapper family

Status: **decided** — C core + three free one-shots; all sub-decisions resolved (headers ->
`WithHeaders` option, names -> `MeasureText`/`MeasureJSON`/`MeasurePing`, `Dial` -> dropped,
`OneHitMessage(JSON)` -> unexported). Ready to graduate to an ADR.
Scope: the convenience API in `wrappers.go` plus the `DialContext`/ctx-aware read-write additions
to the core that the chosen shape requires. Does not change `Result`'s timing fields.

## Why this needs deciding before v3.0.0

These are exported functions. v3.0.0 freezes their names and signatures. Two of the review's
findings converge here:

1. **Context/options are only on the `*Burst*WithContext` variants** — the primary single-shot
   functions can't be canceled or configured at all.
2. **The family is a bloated, asymmetric matrix** — adding the obvious fix (ctx + opts on every
   function) makes the surface *bigger*, which is the opposite of what the second finding wants.

So this isn't a checkbox; it's a fork in the road. Pick the shape now, because every option
below is a breaking change relative to the others, and v3 is the only free window to take one.

## Decision

**Option C, refined: the live-connection method API is the core; three free functions own the
one-shot lifecycle.** No high-level terminal `Measure*` methods on `WSStat` — a method that dials
and closes internally would make the type a single-use receiver (a handle you never hold). The
free one-shots are kept deliberately, *not* for v2 familiarity (a clean `/v3` break makes that
worth zero) but because the one-shot and the live connection are genuinely different lifecycles,
the repo's own CLI is a one-shot consumer (`internal/app/measurement.go:23,54,77`), and a single
free function is the one place to get New->Dial->...->Close cleanup right (where the connection-leak
blocker lives).

Two surfaces:

**Connection API (methods on `*WSStat`)** — live/stateful flows (subscriptions, multi-step):

- `New(opts ...Option) *WSStat`
- `DialContext(ctx, target *url.URL, headers http.Header) error` (the only dial entry; `Dial` removed)
- existing live methods: `WriteMessage`/`ReadMessage`, `WriteMessageJSON`/`ReadMessageJSON`,
  `PingPong`, `Subscribe`/`SubscribeOnce`, `Close`, `ExtractResult`

**One-shot API (free functions)** — fire-and-forget; each owns the full lifecycle incl. cleanup on
every error path:

```go
func MeasureText(ctx context.Context, target *url.URL, msgs []string, opts ...Option) (*Result, []string, error)
func MeasureJSON(ctx context.Context, target *url.URL, msgs []any,    opts ...Option) (*Result, []any,    error)
func MeasurePing(ctx context.Context, target *url.URL, count int,     opts ...Option) (*Result, error)
```

Single message = a 1-element slice. Request headers are passed via `WithHeaders(http.Header)`, so
all three signatures are uniformly `(ctx, target, payload, ...opts)`. The current functions 1-6 are
removed; the three `*BurstWithContext` variants (7-9) collapse into these.

### Implied decisions (forced by this shape)

1. **`DialContext` becomes a prerequisite** (resolves the open DialContext question -> yes). The
   one-shots are plain synchronous functions that honor `ctx` via `DialContext` + per-call ctx on
   read/write, which lets the ~120-line goroutine/channel shuttle (`wrappers.go:193-350`) be
   deleted. Requires read/write to accept/honor a ctx or deadline rather than only `ws.ctx`.
2. **No terminal `Measure*` methods on `WSStat`** — building blocks (write/read/ping) stay;
   high-level measurement lives only in the free functions. (Supersedes the original Option C
   sketch below, which had `(ws *WSStat) Measure(...)` methods.)
3. **Return types normalize** to `[]string` / `[]any`; the scalar `[]byte` / `any` single-shot
   returns disappear with the single->burst collapse.
4. **The error-contract change** (`%w` + exported sentinels) has one home: the three free
   functions. Aligns with the error-contract release blocker.
5. **`Dial` removed**: only `DialContext` is exposed (ctx-first, always cancelable; keeping a
   `context.Background()` sugar would be an uncancelable-dial footgun). `internal/app/subscription.go:34`
   + tests update to `DialContext(ctx, ...)`.
6. **`OneHitMessage(JSON)` unexported**: both become internal helpers used by the free one-shots; the
   public live-connection surface stays `WriteMessage`/`ReadMessage` etc. A public round-trip
   convenience, if ever wanted, gets added deliberately (e.g. `Roundtrip(...)`), not kept by default.
7. **Headers via `WithHeaders(http.Header)`** for the one-shots, so their signatures are uniform
   `(ctx, target, payload, ...opts)`. The connection-API `DialContext` keeps headers positional —
   it is the explicit live API. (Yes, two header conventions, deliberately split along the lifecycle.)

### Migration impact (in-tree)

- `internal/app/measurement.go:23,54,77` — the three `*BurstWithContext` calls become
  `MeasureText` / `MeasureJSON` / `MeasurePing`.
- `_example/main.go:28` — `MeasureLatency(u, msg, h)` becomes `MeasureText(ctx, u, []string{msg})`
  (plus `WithHeaders` if headers move to an option).
- External v2 users: covered by the CHANGELOG migration table (clean `/v3` break).

The sections below are retained as the rationale record for how this decision was reached.

## Current surface (9 functions)

| # | Function | ctx | opts | payload | returns |
| --- | --- | --- | --- | --- | --- |
| 1 | `MeasureLatency` | no | no | `msg string` | `(*Result, []byte, error)` |
| 2 | `MeasureLatencyBurst` | no | no | `msgs []string` | `(*Result, []string, error)` |
| 3 | `MeasureLatencyJSON` | no | no | `v any` | `(*Result, any, error)` |
| 4 | `MeasureLatencyJSONBurst` | no | no | `v []any` | `(*Result, []any, error)` |
| 5 | `MeasureLatencyPing` | no | no | (none) | `(*Result, error)` |
| 6 | `MeasureLatencyPingBurst` | no | no | `pingCount int` | `(*Result, error)` |
| 7 | `MeasureLatencyBurstWithContext` | yes | yes | `msgs []string` | `(*Result, []string, error)` |
| 8 | `MeasureLatencyJSONBurstWithContext` | yes | yes | `v []any` | `(*Result, []any, error)` |
| 9 | `MeasureLatencyPingBurstWithContext` | yes | yes | `pingCount int` | `(*Result, error)` |

Asymmetries baked in:

- **ctx/opts coverage is partial**: only the 3 Burst variants (7-9) take them; the other 6 can't
  be canceled or configured.
- **Single vs burst is redundant**: a single message is a 1-element burst. Functions 1/3 differ
  from 2/4 only in scalar-vs-slice return.
- **Return types are inconsistent**: text single returns `[]byte`, text burst returns `[]string`;
  JSON single returns `any`, JSON burst returns `[]any`. (Why bytes one place and string another?)
- **Error wrapping is inconsistent** (same root as the `%w` finding): `MeasureLatency` wraps with
  `%v` (`wrappers.go:24,29`), `MeasureLatencyBurst` returns the dial error raw (`:50`), the
  `*WithContext` variants use `%w` (`:213,275`). No two agree.
- **The non-context variants are synchronous; the context variants spawn a goroutine + channel**
  (`wrappers.go:197-233`). So "just add ctx everywhere" also means restructuring 6 functions to
  the goroutine pattern, or finding a cancellation approach that doesn't need it.
- **A `customHeaders http.Header` positional** sits in every signature — a candidate to become an
  option (`WithHeaders`) so the signatures shrink uniformly.

## Forces / constraints

- v3 is breaking already (module `/v3`, gorilla→coder), so renames are *allowed* here — the cost
  is migration-guide text, not a second major bump.
- Ping genuinely has no JSON dimension (no payload), so the grid is 2 payload modalities (text,
  JSON) + ping, not a clean 3×N.
- The convenience functions exist for the 80% case; full control already has a home (construct
  `WSStat` via `New(opts...)` and drive it). The wrappers should not try to be the power-user API.
- Cancellation in the synchronous path: `Dial`/`ReadMessage`/`WriteMessage` already thread
  `ws.ctx`. The cleaner long-term fix (tracked separately) is a public `DialContext(ctx, ...)` +
  context-aware read/write, which would let the wrappers be canceled *without* the goroutine
  shuttle. This proposal assumes that lands, so the canonical wrapper can be synchronous and still
  cancelable.

## Options

### Option A — Collapse to one function per modality (recommended)

Single-shot *is* a 1-element burst. Three functions, each always `(ctx, ..., ...opts)`:

```go
func MeasureText(ctx context.Context, target *url.URL, msgs []string, opts ...Option) (*Result, []string, error)
func MeasureJSON(ctx context.Context, target *url.URL, msgs []any,    opts ...Option) (*Result, []any,    error)
func MeasurePing(ctx context.Context, target *url.URL, count int,     opts ...Option) (*Result, error)
```

Headers move into an option: `WithHeaders(http.Header)`. Single message:
`MeasureText(ctx, u, []string{msg})` then `resps[0]`.

- **Surface**: 9 → 3 functions. Symmetric, no `Burst`/`WithContext` suffix soup.
- **Every wrapper is cancelable and configurable** — both findings resolved at once.
- **Return types normalized**: text → `[]string`, JSON → `[]any`, consistent.
- Cost: renames everything (loud but mechanical migration); single-message callers index `[0]`.
- Pairs naturally with the `%w`/sentinels error-contract change (one error style across three funcs).

### Option B — Keep single×burst×modality, but make it symmetric

Six functions, all take `(ctx, ..., ...opts)`; drop the separate `*WithContext` names by folding
ctx into the base names.

```go
func MeasureLatency(ctx, target, msg string, opts ...Option) (*Result, []byte, error)
func MeasureLatencyBurst(ctx, target, msgs []string, opts ...Option) (*Result, []string, error)
func MeasureLatencyJSON(ctx, target, v any, opts ...Option) (*Result, any, error)
func MeasureLatencyJSONBurst(ctx, target, v []any, opts ...Option) (*Result, []any, error)
func MeasureLatencyPing(ctx, target, opts ...Option) (*Result, error)
func MeasureLatencyPingBurst(ctx, target, count int, opts ...Option) (*Result, error)
```

- **Surface**: 9 → 6. Keeps the scalar single-shot ergonomics (no `[0]` indexing).
- Symmetric and predictable; smaller rename than A (names mostly survive, signatures gain ctx+opts).
- Cost: still six functions for what is really three operations; the single/burst split persists.
  Return-type inconsistency (`[]byte` vs `[]string`) survives unless also normalized.

### Option C — Method-first, minimal free functions

Make `WSStat` methods the real API; keep one or two free functions for the trivial case.

```go
func (ws *WSStat) Measure(ctx context.Context, target *url.URL, msgs []string) (*Result, []string, error)
func (ws *WSStat) MeasureJSON(ctx context.Context, target *url.URL, msgs []any) (*Result, []any, error)
func (ws *WSStat) MeasurePing(ctx context.Context, target *url.URL, count int) (*Result, error)
// plus a single free convenience:
func MeasureText(ctx, target, msgs, opts ...Option) (*Result, []string, error) // = New(opts).Measure(...)
```

- Cleanest separation: config via `New(opts...)`, behavior via methods; free funcs are sugar.
- Most idiomatic for power users; discoverable via the type.
- Cost: biggest conceptual shift for existing callers; the "just call a function" path narrows.

### Option D — Single request-struct entry (future-proof escape hatch)

```go
type MeasureRequest struct {
    Target   *url.URL
    Headers  http.Header
    Mode     MeasureMode // Text | JSON | Ping
    Messages []any
    Count    int         // ping count
}
func Measure(ctx context.Context, req MeasureRequest, opts ...Option) (*Result, []any, error)
```

- **Never breaks again**: new capability = new struct field, additive forever.
- One function to learn.
- Cost: least idiomatic/discoverable for the simple case; weak typing on `Messages []any` /
  return; "mode" enum is a runtime switch where the type system used to help. Feels un-Go-like for
  a small tool.

### Option E — Status quo plus fill the gaps (not recommended)

Add the 3 missing single `*WithContext` variants and `...opts` to the 6 plain ones. ~12 functions.

- Maximally backward-compatible *shape*.
- Directly worsens the "bloat" finding; freezes the largest possible surface. Listed only for
  completeness.

## How the decision was reached

The doc initially recommended **Option A** (three free functions as the primary surface). Discussion
surfaced two facts that moved it to refined **C**:

- **Usage**: of the 9 functions, only the three `*BurstWithContext` variants (CLI) and plain
  `MeasureLatency` (example) have callers; functions 2-6 have none. The matrix is mostly speculative
  surface aimed at hypothetical external users — argues for trimming, not preserving (rules out B),
  and not generalizing (rules out D).
- **Lifecycle**: the live connection (subscriptions, multi-step) and the one-shot measurement are
  different lifecycles. Methods model the live handle well; a terminal `Measure*` *method* models the
  one-shot badly (single-use receiver). Free functions model the one-shot honestly. So the two
  surfaces are not redundant layers of the same thing — they serve different lifecycles, and the
  repo uses both. Keeping a *minimal* free surface (the three) on top of the method core is the
  coherent result, with A's `[0]`-on-single-message cost accepted as honest (single = burst of one).

See **Decision** above for the chosen shape.

## Open questions for discussion

All resolved.

- Single-shot ergonomics: accept `[]string{msg}` + `resps[0]` (single = burst of one).
- Method-vs-free: C core + three free one-shots.
- `DialContext` as prerequisite: yes.
- Headers: `WithHeaders(http.Header)` option for the one-shots; positional on `DialContext`.
- Naming: `MeasureText` / `MeasureJSON` / `MeasurePing` ("Latency" is redundant with the package).
- `Dial`: dropped; only `DialContext`.
- `OneHitMessage(JSON)`: unexported helpers.
