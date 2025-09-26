# wsstat CLI — Interface Improvements

This document tracks where the CLI stands after the September 2025 flag redesign,
what “great” looks like, and the concrete changes that remain. All legacy flags
have already been removed; future phases assume we can continue to ship
intentional breaking changes when they raise the overall UX bar.

## Current State

What ships today (2025-09-26):

- Invocation: `wsstat [options] <url>`; `cmd/wsstat/main.go` wires flags to the
  client in `internal/app` via helper code in `cmd/wsstat/flags.go` and
  `cmd/wsstat/main_helpers.go`.
- Input selection (mutually exclusive):
  - `-text string` — send plain text.
  - `-rpc-method string` — send a JSON-RPC method (id=1, jsonrpc=2.0).
- Subscription controls:
  - `-subscribe`, `-subscribe-once`.
  - `-count` (default 1; defaults to 0 if `-subscribe` was set and user left the
    flag unset).
  - `-buffer` (messages, default 0 = library fallback) and
    `-summary-interval` (Go duration; 0 disables).
- Output / verbosity:
  - `-q` to silence request/timing output.
  - `-v` repeatable (`-v`, `-v -v`, `-v=2`) handled via `verbosityCounter` in
    `cmd/wsstat/flags.go`.
  - `-format auto|raw` (default `auto`).
  - Verbosity level 0 prints a concise URL/IP summary and basic timing bars,
    level 1 adds orange “Target/IP/TLS version” summaries, level ≥2 prints full
    TLS/header detail (`internal/app/client.go:310-420`).
- Connection options:
  - Repeated `-H` / `-header "Key: Value"` parsed into slice-backed headers.
  - `-no-tls` controls the implied scheme when the URL omits one (`ws://` vs
    default `wss://`).
- Response handling:
  - `-format raw` prints payloads verbatim; otherwise JSON payloads are pretty
    printed when possible. JSON-RPC detection is tied to `-rpc-method` usage.
- Tests: `cmd/wsstat/main_test.go` covers flag defaults, while
  `internal/app/client_test.go` exercises validate/printing paths, including the
  new header parsing and verbosity levels.

## Best-in-Class Standards To Aim For

- **Separation of concerns** — verbosity controls “how much”, format controls
  “how it looks”, and the defaults are obvious.
- **Predictable flag ergonomics** — short aliases for common flags, repeatable
  header flags, consistent duration/integer validation, and early flag errors
  with actionable messages.
- **TTY awareness** — color automatically suppressed in non-TTY contexts unless
  explicitly forced, support for the `NO_COLOR` convention, and a controllable
  `-color=auto|always|never` flag.
- **Machine-readable output** — optional structured summaries for automation
  (e.g., JSON), while keeping the human-centric timeline as the default.
- **Documentation parity** — help output, README snippets, and examples stay in
  sync with the actual flag surface.
- **Robust validation** — clear errors when combinations are invalid or when
  values fall outside supported ranges (format, buffer, summary interval, etc.).

## Goal Vision

A CLI that teaches itself through help text, defaults, and examples, and that
scales from “one-off latency probe” to “drop into pipelines” without friction:

- Users see grouped help with defaults, examples, and clarity about mutually
  exclusive options.
- Verbosity is just `-v`, `-vv`, or `-vvv`; quiet mode suppresses request/timing
  output but still emits responses when relevant.
- Machine-readable summaries (e.g., `-format json`) make scripting easy without
  parsing ANSI output.
- Color output respects terminals by default but can be forced or disabled.
- Headers, timeouts, and subscription options are intuitive and discoverable.

## Improvement Steps

### Phase 0 — Completed Flag Redesign (2025-09)

Reference only: `cmd/wsstat/main.go`, `cmd/wsstat/flags.go`, and
`internal/app/client.go` already expose the new flag set. No action required.

### Phase 1 — Harmonise Verbosity & Timing Output

Goal: remove remaining legacy assumptions (e.g., “basic” timeline helpers) and
make verbosity tiers explicit and documented.

Key edits:
- `internal/app/client.go`
  - Replace `printTimingResultsBasic` + tiered split with a single formatter that
    adapts to verbosity (lines ~500-610). Promote the current elaborate tiered
    view to verbosity ≥1 and let “basic” helper remain as the default case.
  - Clarify the three verbosity tiers in comments and ensure `PrintRequestDetails`
    and `PrintTimingResults` stay in sync (lines ~320-420 and ~520-610).
- `internal/app/client_test.go`
  - Update/expand tests around verbosity (search for `VerbosityLevel`) to cover
    all tiers after refactor.
- `cmd/wsstat/main.go`
  - Update help text to describe the three tiers explicitly (lines ~70-100).

### Phase 2 — Color & Non-TTY Controls

Add explicit color control and respect `NO_COLOR`.

Key edits:
- `cmd/wsstat/main.go`
  - Introduce `-color auto|always|never` with default `auto` (flag block around
    lines 20-40) and surface it in help.
- `cmd/wsstat/main_helpers.go`
  - Validate `-color` input and thread the setting into the client struct.
- `internal/app/client.go`
  - Add a `ColorMode` field; wrap existing `colorWSOrange` / `colorTeaGreen`
    calls so they become no-ops when color is disabled.
  - Respect `NO_COLOR` by default (check env once near program start).
- `internal/app/client_test.go`
  - Add coverage ensuring color suppression removes ANSI sequences when
    requested.

### Phase 3 — Machine-Readable Output (`-format json`)

Add a structured output mode for automation while keeping current behavior.

Key edits:
- `cmd/wsstat/main.go`
  - Extend allowed `-format` values to include `json`.
- `internal/app/client.go`
  - Introduce a `FormatJSON` branch in `PrintResponse`, `PrintTimingResults`, and
    subscription summaries to emit compact JSON (likely using small helper
    structs).
  - Ensure JSON output is deterministic and omits ANSI.
- `internal/app/client_test.go`
  - Add tests verifying JSON schema for single-run and subscription flows.
- Documentation — capture the new mode in help and examples.

### Phase 4 — Documentation & Release Polish

Run through user-facing artefacts once code changes land.

Key edits:
- `README.md` / `docs/` (if present) — update flag reference tables and example
  invocations.
- `cmd/wsstat/main_test.go` — snapshot or golden tests for `wsstat -help`.
- `CHANGELOG.md` (if tracked) — summarise breaking changes and new options.

## Rationale Summaries

- **Verbosity cleanup** keeps a tight mapping between `-v` counts and what users
  see, reducing surprise and making docs easier.
- **Color controls** meet terminal best practices and eliminate noisy ANSI in
  logs without custom scripts.
- **JSON output** unlocks scripting/monitoring use cases without forcing users
  to scrape human-oriented output.
- **Documentation polish** ensures the help text, README, and tests stay in
  lockstep with the evolving surface.

## Testing & Release Checklist

- Unit tests: flag parsing (`cmd/wsstat/main_test.go`), client validation, and
  output helpers (`internal/app/client_test.go`) updated for each phase.
- Snapshot/golden tests for `wsstat -help` once help text stabilises.
- Manual checks: TTY vs non-TTY runs (`script -q /dev/null wsstat …`), color
  suppression, and JSON output validation via `jq`.
- Docs and changelog refreshed to advertise new behaviours and any breaking
  shifts.

## File/Code Pointers

- Flag definitions & help: `cmd/wsstat/main.go`, `cmd/wsstat/flags.go`.
- Parsing/validation helpers: `cmd/wsstat/main_helpers.go`.
- Client behaviour (formatting, subscriptions, printing):
  `internal/app/client.go` and corresponding tests in `internal/app/client_test.go`.

---

This cleaned plan keeps the focus on polishing what’s already been built,
without re-litigating the completed flag removal, and lays out concrete edits for
each upcoming phase.
