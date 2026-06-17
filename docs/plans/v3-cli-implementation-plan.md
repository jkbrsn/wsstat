# v3 CLI Rehaul — Implementation Plan

**Date:** 2026-06-17
**Commit:** b60ce62 (flask-rework)

Companion to `docs/plans/v3-cli-flag-layout.md` (the design/rationale doc). This plan is
the *how*: concrete code changes, sequencing, and tests. Where a decision is questioned,
the rationale lives in the layout doc — this document does not re-argue it.

## TL;DR

Split the overloaded `-format` enum into three orthogonal axes (output contract / body
rendering / clip), make mode an explicit subcommand (`measure` | `stream`) instead of
two interacting booleans, and reject rather than silently ignore conflicting flags. The
work lands in three buildable phases: (1) refactor the `internal/app` output model,
(2) replace the `cmd/wsstat` flag surface with subcommand dispatch, (3) update docs and
the smoke stack. Outcome: a predictable CLI where each flag owns exactly one concern and
`-o json` is a stable machine contract.

## Problem Statement

`cmd/wsstat` collapses three orthogonal concerns into two flag groups (`-format` mixes
body rendering with the output contract; mode is two booleans plus an overloaded
`-count`), producing silent-precedence bugs and no-op flags. Full problem analysis and
the target surface are in `docs/plans/v3-cli-flag-layout.md`. v3.0.0 is the breaking
boundary to fix it (`VERSION` already reads `3.0.0`).

**Constraints:**

- Stdlib-first (`CLAUDE.md`): hand-roll subcommand dispatch with `flag.FlagSet`, no CLI
  framework dependency.
- The core library (`github.com/jkbrsn/wsstat/v3`, root package) is **out of scope** —
  only `internal/app` and `cmd/wsstat` change. The `wsstat.WithCloseGrace` core option
  and timing instrumentation stay as-is.
- Tests run an echo server on `localhost:8080` via `TestMain`; CI runs `-race` at 16x.

## Non-Goals

- No change to the core `wsstat` package API or timing measurement.
- No new JSON envelope fields; the schema stays exactly as in `internal/app/types.go`
  (`JSONSchemaVersion` unchanged). Opt-in `--include`/`--detail` enrichment is deferred
  past 3.0.0.
- No `--clip-width N`, no `-vvv` level-3 verbosity, no raw stream `--delimiter`/`--print0`
  — all explicitly deferred in the layout doc.
- No backward-compat aliases for removed flags beyond a targeted migration *error*.

## Proposed Solution

Three components, implemented in the phase order below:

1. **Internal output model** (`internal/app`) — replace the single `Format` enum with
   independent `Output` / `Body` / `Clip` fields plus an explicit `Mode`, rewrite every
   `c.format == …` branch onto the relevant axis, and fix the three behavior gaps (raw
   verbatim, body-applies-to-measured-response, clip-per-line).
2. **CLI subcommand dispatch** (`cmd/wsstat`) — `args[0]`-keyed dispatch to `measure` /
   `stream`, each with its own `flag.FlagSet` sharing a common registrar; per-command
   validation; targeted migration errors for removed flags.
3. **Docs & smoke stack** — README, CHANGELOG (incl. the close-grace wording fix),
   `dev/` docs, `dev/smoke-test.sh`, mock-server comments.

---

## Detailed Design

### 1) Internal output model (`internal/app`)

**Goal:** Make the three output axes and mode first-class in the `Client`, eliminating
the `Format`-enum coupling.

#### New types — replace `internal/app/format.go`

Drop `Format` and its five constants (`format.go:9-17`) and `ParseFormat`
(`format.go:21-31`). Introduce:

```go
// Output is the whole-stdout contract.
type Output string
const (
    OutputText Output = "text" // human; governed by Body/Clip/verbosity
    OutputJSON Output = "json" // schema-stable NDJSON envelopes
    OutputRaw  Output = "raw"  // verbatim payload bytes, nothing added
)

// Body is the human rendering of a payload (text output only).
type Body string
const (
    BodyAuto    Body = "auto"    // pretty / multi-line
    BodyCompact Body = "compact" // one line per message
)

// Mode is the operation; previously two booleans.
type Mode int
const (
    ModeMeasure Mode = iota
    ModeStream
)

func ParseOutput(s string) (Output, error) // "" → OutputText
func ParseBody(s string) (Body, error)      // "" → BodyAuto
```

#### `Client` struct + options (`internal/app/client.go`)

- Remove field `format Format` (`client.go:64`); add `output Output`, `body Body`,
  `clip bool`, `mode Mode`, `once bool`. Keep `quiet`, `verbosityLevel`, `colorMode`,
  `buffer`, `summaryInterval`, timeouts, `insecure`.
- Remove `subscribe`/`subscribeOnce` fields.
- Replace `WithFormat` (`client.go:128`) with `WithOutput(Output)`, `WithBodyRender(Body)`,
  `WithClip(bool)`. Replace `WithSubscription`/`WithSubscriptionOnce` (`client.go:148,153`)
  with `WithMode(Mode)` and `WithStreamOnce(bool)`.
- `NewClient` defaults (`client.go:89-99`): `output: OutputText`, `body: BodyAuto`,
  `mode: ModeMeasure`.
- Remove the `Format()` accessor (`client.go:187`); add `Output()`/`Body()` accessors if
  tests need them (audit `client_test.go`).

#### `Validate()` (`internal/app/client.go:273-325`)

- Delete the `subscribeOnce → subscribe=true` + count-fudge block (`client.go:304-312`):
  mode and count are validated per-command in `cmd` *before* the client is built (see
  component 2), so the client receives an already-valid config.
- Replace the `ParseFormat` normalization (`client.go:282-286`) with `ParseOutput`/
  `ParseBody` normalization, or drop it entirely if `cmd` guarantees valid enums.
- Keep `colorMode`, `buffer`, `summaryInterval` validation.

#### Rewrite the format branches

Every site the explorer found, remapped onto the new axes:

| Site | Old | New |
|---|---|---|
| `output.go:101` | `c.format == formatJSON` | `c.output == OutputJSON` |
| `output.go:106` | `c.format == formatRaw` | `c.output == OutputRaw` |
| `output.go:111` | `compact \|\| truncate` | `c.body == BodyCompact` |
| `output.go:150` (`clipLine`) | `c.format != formatTruncate` | `!c.clip` |
| `output.go:197,392,428,480` | `c.format == formatJSON` | `c.output == OutputJSON` |
| `output.go:445` (`PrintResponse`) | `c.format == formatRaw` | `c.output == OutputRaw` (rewrite, below) |
| `measurement.go:103` (`processTextResponse`) | `format == formatRaw` | `output == OutputRaw` |
| `subscription.go:108,216,250` | `c.format != formatJSON` | `c.output != OutputJSON` |

`processTextResponse` (`measurement.go:98-116`) takes a `Format` param; change its
signature to take `Output` (it only distinguishes raw from non-raw).

#### Behavior fix A — `raw` is verbatim (`PrintResponse`, `printSubscriptionMessage`)

`PrintResponse` (`output.go:421-462`) currently prints `"Response: " + value + "\n"` and
`json.Marshal`s JSON-RPC compact. `printSubscriptionMessage` (`output.go:100-140`) uses
`fmt.Println(payload)` for raw (adds `\n`). Under `OutputRaw`, both must write payload
bytes via `os.Stdout.Write` with **no label, color, timing, or trailing newline**, in
both modes. Stream frames are concatenated undelimited (binary-safe; see layout doc
"Why verbatim for streams too").

```go
// raw branch, shared helper
func writeRaw(b []byte) error { _, err := os.Stdout.Write(b); return err }
```

#### Behavior fix B — `Body` governs the measured response too

`PrintResponse` (`output.go:447-453`) always `json.Marshal`s a JSON-RPC map compact,
ignoring rendering. Route it through the same renderer stream messages use
(`renderJSON(data, compact bool)` in `types.go:160-183`): `BodyAuto` → `renderJSON(…,
false)` (pretty), `BodyCompact` → `renderJSON(…, true)` (one line). Non-JSON responses
fall back to their current string form.

#### Behavior fix C — `clip` applies per line, for `auto` too

`clipLine` (`output.go:149-158`) clips a single assembled string only under
`formatTruncate`. Replace with a helper that, when `c.clip` and stdout is a TTY
(`terminalWidth() > 0`, `output.go:162-172`), clips **each line** of a possibly
multi-line rendered body:

```go
func (c *Client) clipBody(s string) string {
    if !c.clip { return s }
    w := terminalWidth()
    if w <= 0 { return s }
    // split on "\n", clipToWidth each line, rejoin
}
```

Apply at every point a rendered body/line is emitted in text mode (the `auto` multi-line
branch in `printSubscriptionMessage:124-128` currently bypasses clipping — route it
through `clipBody`).

#### JSON stability audit

Confirm no JSON builder varies with `verbosityLevel`. `PrintTimingResults` JSON branch
(`output.go:480`) calls `buildTimingSummaryFromResult` unconditionally — good. Check
`PrintRequestDetails` JSON (`output.go:392`) and the subscription JSON paths emit the
same fields regardless of `-v/-vv`. This is an audit step, not expected to need changes.

### 2) CLI subcommand dispatch (`cmd/wsstat`)

**Goal:** Replace the global flag block + `parseConfig` with `args[0]`-keyed dispatch to
two subcommands, each owning its flags and validation.

#### Dispatch (`cmd/wsstat/main.go`)

Replace the `var (...)` flag block (`main.go:46-76`), `init()` (`main.go:78-94`), and the
`flag.Parse()`/`run()` flow (`main.go:96-105,129-191`) with:

```go
func main() {
    args := os.Args[1:]
    if err := checkRemovedFlags(args); err != nil { fail(err) } // migration errors
    var err error
    switch {
    case len(args) > 0 && args[0] == "stream":
        err = runStream(args[1:])
    case len(args) > 0 && args[0] == "measure":
        err = runMeasure(args[1:])
    case len(args) > 0 && args[0] == "--version", len(args) > 0 && args[0] == "-version":
        printVersion(); return
    default:
        err = runMeasure(args) // bare form
    }
    if err != nil { fail(err) }
}
```

Dispatch keys on `args[0]` only (layout doc explains why scanning is unsafe with stdlib
`flag`). `interruptContext` (`main.go:107-127`) is unchanged and used by both run paths.

#### Shared-flag registrar

```go
type commonFlags struct {
    headers   headerList
    resolves  resolveList
    rpcMethod, text string
    output, body, color string
    clip, quiet, v1, v2, insecure bool
    timeout, closeTimeout time.Duration
}
func registerCommon(fs *flag.FlagSet, c *commonFlags) // -H,-t,--rpc-method,-o,--body,--clip,-q,-v,-vv,-k,--timeout,--close-timeout,--color,--resolve
```

`runMeasure` / `runStream` each: build a `flag.FlagSet(name, ExitOnError)`, call
`registerCommon`, register their own `-c/--count` (+ `stream`: `--once`, `-b/--buffer`,
`--summary-interval`), `fs.Parse(args)`, then validate and build the client.

- `measure`: `--count` default 1, must be `>= 1`.
- `stream`: `--count` default 0 (`0` = unlimited), must be `>= 0`.

This removes `trackedIntFlag` (`flags.go:39-73`) and `resolveCountValue`
(`config.go:127-132`) entirely — the `WasSet` dance existed only to resolve the
mode-dependent count flip, which subcommands eliminate. Plain `fs.Int` per command
suffices.

#### Per-command validation (in `cmd`, before client construction)

- `--text` + `--rpc-method` both set → error (currently `config.go:54-56`).
- Output-axis conflicts: `-o json|raw` with any of `--body`, `--clip`, `-q`, `-v`, `-vv`
  → error "`--X` only applies to text output". `--color` is exempt (stays inert).
- `-o raw` under `measure` without `--text`/`--rpc-method` → error (no payload to emit).
- Parse `-o` via `app.ParseOutput`, `--body` via `app.ParseBody`, `--color` against
  `auto|always|never` (currently `config.go:63-68`).
- Exactly one positional URL after the subcommand (currently `config.go:58-61`).

#### URL parsing (`cmd/wsstat/config.go`)

Keep `parseWSURI` (`config.go:106-122`) but remove the `*noTLS` branch (`config.go:110-113`);
default scheme is always `wss://`. Users wanting plaintext type `ws://` explicitly.

#### Migration errors

```go
var removedFlags = map[string]string{
    "subscribe":      "use the `stream` subcommand",
    "subscribe-once": "use `stream --once`",
    "format":         "use `-o` (text|json|raw), `--body`, and/or `--clip`",
    "f":              "use `-o` (text|json|raw), `--body`, and/or `--clip`",
    "no-tls":         "type a `ws://` URL instead",
}
func checkRemovedFlags(args []string) error // scan for -flag/--flag/-flag=val, return targeted error
```

Pre-scan (before dispatch) so a removed flag yields "`-format` was removed in v3; use …"
instead of stdlib's generic "flag provided but not defined". Token match on the flag name
with optional `=value`.

#### Client construction

Replace the 16-option block (`main.go:140-157`) with the new options:
`WithMode`, `WithStreamOnce`, `WithOutput`, `WithBodyRender`, `WithClip`, `WithCount`,
`WithHeaders`, `WithResolves`, `WithRPCMethod`, `WithTextMessage`, `WithColorMode`,
`WithQuiet`, `WithVerbosity`, `WithBuffer`, `WithSummaryInterval`, `WithInsecure`,
`WithTimeout`, `WithCloseGrace`.

#### Per-command usage text

`printUsage` (`main.go:193-251`) splits into `measureUsage` / `streamUsage` wired to each
`FlagSet.Usage`, plus a short top-level usage listing the two subcommands. Each lists
only that command's valid flags.

### 3) Docs & smoke stack

- **README.md:128-177** — replace all `-subscribe`/`-subscribe-once`/`-format`/`-count`
  examples with `stream`/`measure`, `-o`, `--body`, `--clip`.
- **CHANGELOG.md** — add the v3 breaking-changes entry from the layout doc's migration
  table. **Fix CHANGELOG.md:14**: the core forwards `WithCloseGrace` only when the value
  is `> 0` (`internal/app/client.go:219-220`), so a CLI `0` keeps the 3s default — change
  "`0` forces immediate teardown" to "`0` uses the default (3s)". Update CHANGELOG.md:12-13
  (`-format compact`/`truncate`) and :16 (`-no-tls`).
- **dev/README.md:35,69** — update flag references (`-s`, `-subscribe-once`, `-no-tls`).
- **dev/smoke-test.sh** — rewrite invocations: `-close-timeout`→stays, `-f raw/auto/json`
  → `-o`/`--body`, `-subscribe-once` → `stream --once`, `-s` → `stream`, `-no-tls` →
  `ws://` URL (lines 49,85,86,88,103,110,123).
- **dev/mock-server/main.go:155,299-307** — update doc comments referencing old flags.

---

## Affected Files

- `internal/app/format.go` (rewrite) — `Output`/`Body` types, `ParseOutput`/`ParseBody`;
  drop `Format`.
- `internal/app/client.go` (modify) — fields, options, `Validate`, accessors.
- `internal/app/output.go` (modify) — rebranch; raw-verbatim; body-on-response;
  clip-per-line.
- `internal/app/measurement.go` (modify) — `processTextResponse` signature + branch.
- `internal/app/subscription.go` (modify) — three `OutputJSON` rebranches.
- `internal/app/types.go` (unchanged expected) — JSON envelopes stay; audit only.
- `cmd/wsstat/main.go` (rewrite) — dispatch, run paths, registrar, usage, migration.
- `cmd/wsstat/config.go` (modify) — drop `resolveCountValue`, `noTLS` branch; per-command
  validation helpers.
- `cmd/wsstat/flags.go` (modify) — drop `trackedIntFlag`; keep `headerList`, `resolveList`.
- Tests: `cmd/wsstat/{main,config,flags}_test.go`, `internal/app/{format,clip,
  client_output,client_subscription,client_validation}_test.go` (see Testing Strategy).
- Docs: `README.md`, `CHANGELOG.md`, `dev/README.md`, `dev/smoke-test.sh`,
  `dev/mock-server/main.go`. `VERSION` already `3.0.0`.

## Implementation Phases

Each phase leaves the tree buildable with `make test` green.

1. **Internal model (`internal/app`).** Components 1 above. To keep the build green while
   `cmd` still uses the old surface, update the single client-construction site
   (`main.go:140-157`) to the new options in this phase, mapping the *existing* v2 flags
   onto them (e.g. `-format json` → `WithOutput(OutputJSON)`). The v2 flag surface still
   parses; only the internal model and the three behavior fixes change. Update
   `internal/app` tests. End state: builds, app tests green, raw/body/clip semantics
   corrected.
2. **CLI subcommands (`cmd/wsstat`).** Component 2: replace the flag block with dispatch,
   registrar, per-command validation, migration errors; remove `trackedIntFlag`,
   `resolveCountValue`, `noTLS`. Rewrite `cmd` tests. End state: new surface live.
3. **Docs & smoke (component 3).** Update all docs and `dev/smoke-test.sh`; run the smoke
   stack. End state: docs match the shipped surface.

## Edge Cases & Safety

- **Binary frames under `-o raw`:** payload may be binary (`types.go:223` `BinaryMessage`);
  `writeRaw` adds no bytes, so binary is uncorrupted. Stream frames are undelimited by
  design — `-o json` is the delimited path.
- **Host literally named `stream`/`measure`:** `wsstat stream` with no URL errors as a
  missing-URL; to target such a host, use `wsstat wss://stream` or `wsstat measure stream`.
- **Global flag before subcommand** (`wsstat -v stream url`): parsed as `measure` with a
  stray positional → clean error. Documented `go test`-style rule.
- **`os.Stdout.Write` partial/short writes** in raw mode: propagate the error up the run
  path (exit non-zero), matching existing error handling.
- **Second SIGINT:** `interruptContext` hard-exit (130) preserved for both run paths so a
  stuck stream teardown is always escapable.
- **`--once` + `--count`:** `stream --once` implies a single event; reject `--count` other
  than its default (mirrors the old subscribe-once count==1 rule, now explicit in `stream`
  validation).

## Testing Strategy

**New / rewritten tests:**

- `cmd/wsstat/main_test.go` — `TestDispatch` (bare → measure, `measure`/`stream` tokens,
  `--version`, `-v stream` stray-positional error); `TestMigrationErrors` (each removed
  flag → targeted message).
- `cmd/wsstat/config_test.go` — drop `TestResolveCountValue`; trim `TestParseWSURI` noTLS
  cases; replace `TestParseConfig` with `TestMeasureFlags` / `TestStreamFlags` covering
  per-command count bounds, `--text`+`--rpc-method` conflict, text-only-flag-under-`-o
  json|raw` rejection, `-o raw` measure-without-message rejection.
- `cmd/wsstat/flags_test.go` — drop `TestTrackedIntFlag`; keep `TestHeaderList`,
  `TestResolveList`.
- `internal/app/format_test.go` — replace `TestParseFormat` with `TestParseOutput` /
  `TestParseBody`.
- `internal/app/client_output_test.go` — assert raw = verbatim bytes (no label/newline);
  `BodyAuto` pretty-prints a measured JSON-RPC response, `BodyCompact` one-lines it.
- `internal/app/clip_test.go` — clip applies per line for multi-line `auto`.
- `internal/app/client_subscription_test.go` / `client_validation_test.go` — update for
  `Output`/`Mode` instead of `Format`/`subscribe*`.

**Commands:**

- `make test` (full); `go test ./internal/app/ -run 'TestParseOutput|TestRaw|TestBody|TestClip' -race`
- `go test ./cmd/wsstat/ -run 'TestDispatch|TestMigration|TestMeasureFlags|TestStreamFlags' -race`
- `bash dev/smoke-test.sh` after phase 3.

**Regression boundary (must keep passing):** the echo-server integration in
`internal/app` `TestMain` (port 8080); existing timing-measurement tests in
`client_measurement_test.go` (unaffected by the output refactor).

## Acceptance Criteria

Inherited verbatim from `docs/plans/v3-cli-flag-layout.md` "Acceptance criteria"; the
implementation-specific additions:

- `wsstat <url>` and `wsstat measure <url>` produce byte-identical output.
- `-o raw` writes payload bytes via `os.Stdout.Write` with no label/color/timing/newline
  in both modes; `-o raw` measure without a message flag is rejected.
- `--body auto` pretty-prints the measured response; `--body compact` one-lines it;
  `--clip` clips each line of multi-line `auto` output.
- `-o json` emits identical envelope fields regardless of `-v/-vv`.
- `--body`/`--clip`/`-q`/`-v`/`-vv` under `-o json|raw` are rejected with a clear error;
  `--color` is not.
- Removed flags (`-subscribe`, `-subscribe-once`, `-format`/`-f`, `-no-tls`) emit a
  targeted migration error (test-verified).
- Each subcommand's `-h` lists only its valid flags.
- `make lint && make test` pass; `bash dev/smoke-test.sh` passes.
- README/CHANGELOG/dev docs updated; CHANGELOG close-grace `0` wording corrected to
  "uses the default (3s)".

## Open Questions

None blocking — all design decisions are resolved in the layout doc's "Resolved
decisions" section. One implementation-level choice, decided here rather than left open:
text-only-flag rejection lives in the `cmd` per-command validation (CLI-appropriate
messages), with `app.Validate` only guarding invalid enum values as a backstop.
