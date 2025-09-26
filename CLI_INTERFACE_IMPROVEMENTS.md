# wsstat CLI — Interface Improvements

This document proposes a pragmatic path to polish the wsstat command-line
experience. It captures where things are today, the standards we want to hit,
the end-state we are aiming for, and concrete steps to get there. We will ship
these changes without backwards-compatibility shims; old options will be
removed rather than aliased.

## Current State

What the CLI exposes today (as of 2025-09-26):

- Invocation shape: `wsstat [options] <url>`
- Input selection (mutually exclusive):
  - `-text` string — send a plain text message
  - `-json` string — send a JSON‑RPC method name (id=1, jsonrpc=2.0)
- Subscription controls:
  - `-subscribe` — stream events
  - `-subscribe-once` — stream a single event (acts like subscribe + count=1)
  - `-count` int — number of interactions; special default in subscribe mode
  - `-subscription-interval` duration — periodic summaries (0 disables)
  - `-subscription-buffer` int — internal buffered messages
- Output/verbosity:
  - `-b` — basic output
  - `-v` — verbose output
  - `-q` — quiet (only response)
  - `-raw` — print payloads as raw data (no pretty JSON)
- Connection and meta:
  - `-headers` string — comma‑separated `Key: Value` pairs
  - `-insecure` — if URL lacks scheme, default to `ws://` (otherwise `wss://`)
  - `-version` — print version

Observed behavior and semantics (from cmd/wsstat and internal/app):

- Mutually exclusive checks exist for `-text`/`-json` and for `-b`/`-v`/`-q`.
- Count defaults:
  - Non-subscribe flows: default `count = 1`.
  - Subscribe flows: if user did not set `-count`, effective `count = 0` (unlimited).
- `-subscribe-once` is a convenience for `-subscribe` with `count = 1`.
- `-headers` expects a single comma‑separated string which is parsed into
  `http.Header` (easy to mistype compared to repeated flags).
- `-insecure` controls the scheme assumption when missing (`ws://` vs `wss://`).
  This differs from many tools where “insecure” often means “skip TLS verify”.
- Help groups options but does not show defaults inline and uses mixed naming
  styles (short and long, some long names are quite verbose).
- Output formatting blends “how much” (verbosity) and “how” (rendering) with
  `-b`, `-v`, `-q`, and `-raw`. JSON pretty printing is automatic when possible.
- Colored ASCII timing output is clear for humans; JSON/machine output is not yet
  a first‑class mode.

Pain points that appear for new users:

- `-json` reads as an output format flag in many tools; here it means
  “JSON‑RPC method name”.
- Header syntax as a single `-headers` string is fragile.
- `-subscription-interval` and `-subscription-buffer` are long to type.
- `-insecure` naming is misleading given its specific “assume ws://” behavior.
- Help output lacks explicit defaults/examples, so users need to infer behavior.

## Best‑in‑Class Standards To Aim For

Baseline CLI UX and interoperability conventions to target:

- Clear separation of concerns:
  - Verbosity = amount of information: `-q` to silence; `-v` repeatable for more detail.
  - Format = how content is rendered: `--format auto|raw|json|table` (room to grow).
- Predictable, conventional flags:
  - Long options with meaningful names and short aliases for frequent flags.
  - Repeatable `-H/--header "Key: Value"` instead of a comma‑separated string.
  - Duration flags accept Go durations; examples shown in help.
  - Explicit color control: `--color=auto|always|never`; honor `NO_COLOR`.
- Helpful help:
  - Description line and grouped sections with “(choose one)” where applicable.
  - Defaults shown inline, including special defaults (e.g., subscribe mode).
  - 5–6 real‑world examples covering core flows.
- Robust behavior in pipelines/automation:
  - Human‑friendly output by default, machine‑readable on demand (`--format json`).
  - Non‑TTY auto‑disable colors unless `--color=always`.
  - Exit codes documented and consistent (0 success; 1 runtime/connect; 2 usage).
Note: We will not maintain backwards compatibility. Old flags are removed.

## Goal Vision

An intuitive wsstat where:

- The help teaches by example; defaults are obvious; common tasks take 1–2 flags.
- Users choose one input: `--text` or `--rpc-method`, and optionally `--subscribe`.
- Verbosity is a simple `-q`, `-v`, `-vv`; formatting is chosen via `--format`.
- Headers are set with familiar `-H` flags.
- URL scheme rules are explicit: if no scheme, assume `wss://` unless `--no-tls`.
- Streaming surfaces periodic summaries with `--summary-interval`.
- Machine‑readable summaries exist when `--format json` is set.

Illustrative help (draft, not binding):

Usage: wsstat [options] <url>

Description:
  Measure WebSocket latency or stream subscription events. If <url> has no
  scheme, wss:// is assumed; use --no-tls to assume ws://.

Input (choose one):
  --text string                 Text to send once or Count times
  --rpc-method string           JSON‑RPC method name (id=1, jsonrpc=2.0)

Subscription:
  --subscribe                   Stream events until interrupted
  --count int                   Interactions/messages (default: 1; defaults to 0 with
                                --subscribe if not set)
  --summary-interval duration   Print periodic summaries (e.g., 1s, 5m; default: 0)
  --buffer int                  Subscription buffer (messages; default: 0, auto)

Output:
  -q, --quiet                   Only responses
  -v, --verbose                 Increase detail (repeatable)
  --format string               Output style: auto|raw (default: auto)

Connection:
  -H, --header "K: V"           Request header (repeatable)
  --no-tls                      If URL has no scheme, assume ws:// instead of wss://

General:
  --version                     Print version and exit
  -h, --help                    Show help

Examples:
  wsstat wss://echo.example.com
  wsstat --text "ping" wss://echo.example.com
  wsstat --rpc-method eth_blockNumber wss://rpc.example.com/ws
  wsstat --subscribe --count 1 wss://stream.example.com/feed
  wsstat --subscribe --summary-interval 5s wss://stream.example.com/feed
  wsstat -H "Authorization: Bearer TOKEN" -H "Origin: https://foo" wss://…

## Improvement Steps

Prioritized, incremental, and with breaking flag changes allowed.

### Phase 1 — Flag Set Redesign (single breaking pass)

- Replace, remove, and rename flags to the new surface (no aliasing):
  - Input:
    - Rename `-json` → `-rpc-method`.
    - Keep `-text`.
  - Subscription:
    - Keep `-subscribe` and `-subscribe-once`.
    - Keep `-count` semantics (default 1; default 0 when `-subscribe` and user did not set it).
    - Rename `-subscription-interval` → `-summary-interval`.
    - Rename `-subscription-buffer` → `-buffer` (messages).
  - Output:
    - Remove `-b` (basic) entirely.
    - Remove `-raw`; introduce `-format {auto,raw}` (default `auto`).
    - Change `-v` to be repeatable as a count (see note below).
    - Keep `-q`.
  - Connection:
    - Replace `-headers` (comma-separated) with repeatable `-H` / `-header "K: V"`.
    - Rename `-insecure` → `-no-tls` (assume `ws://` when scheme is missing).
  - General: keep `-version`.

Edits to perform:
- `cmd/wsstat/main.go:16` (flag var block)
  - Remove `basic`, `rawOutput`, `insecure` vars.
  - Add `format` string flag with default `"auto"`.
  - Rename `jsonMethod` flag name to `"rpc-method"` and var to `rpcMethod`.
  - Rename `subInterval` flag name to `"summary-interval"` and var to `summaryInterval`.
  - Rename `subBuffer` flag name to `"buffer"` and var to `bufferSize`.
  - Add repeatable header flag `-H` and `-header` using a custom `flag.Value` (e.g., `type headerList []string` with `Set(String)` appending; see example below).
  - Add `noTLS` bool flag replacing `insecure`.

- `cmd/wsstat/main.go:45` (custom usage)
  - Regroup help into sections: Description, Input (choose one), Subscription, Output, Connection, General, Examples.
  - Reflect the renamed flags and show defaults inline.
  - Mention special default for `-count` in subscribe mode.

- `cmd/wsstat/main.go:83` (construct `app.Client{...}`)
  - Replace `JSONMethod` → `RPCMethod` assignment.
  - Replace `RawOutput` → `Format` (string) assignment.
  - Replace `Insecure` → `NoTLS` assignment.
  - Replace `Basic`/`Verbose` with `Verbosity` (int) and keep `Quiet`.
  - Replace `SubscriptionBuffer`/`SubscriptionInterval` with `Buffer`/`SummaryInterval`.
  - Replace `InputHeaders` (string) with `Headers` (`[]string`).

- `cmd/wsstat/main_helpers.go:50` (parseValidateInput)
  - Update mutual exclusivity checks to remove `basic` and handle `-q` vs verbosity sensibly: disallow `-q` with any `-v`.
  - Update `textMessage` vs `rpcMethod` check.

- `cmd/wsstat/main_helpers.go:81` (parseWSURI)
  - Replace `*insecure` with `*noTLS` in scheme selection.

- Add a new repeatable header type in `cmd/wsstat/main_helpers.go` (near the trackedIntFlag or directly in main.go):
  - `type headerList []string` with methods `Set(string) error` and `String() string`.
  - Register it for `-H` and `-header`.

Note on repeatable `-v` with stdlib flags:
- The standard library does not support `-vv`; use repeated `-v` (e.g., `-v -v`) or `-v=2` by implementing a `flag.Value` that increments each time `Set` is called.
- Add `type incrFlag int` with `Set` incrementing and expose it as the `-v` flag.

### Phase 2 — Internal API and Printing Adjustments

- `internal/app/client.go:52` (`type Client`) — make structural changes:
  - Replace `InputHeaders string` → `Headers []string`.
  - Replace `JSONMethod string` → `RPCMethod string`.
  - Replace `RawOutput bool` → `Format string` (`"auto"|"raw"`).
  - Replace `Insecure bool` → `NoTLS bool` (if needed externally; scheme handling stays in main).
  - Replace `Basic bool`, `Verbose bool` → `Verbosity int` and keep `Quiet bool`.
  - Replace `SubscriptionBuffer int` → `Buffer int`.
  - Replace `SubscriptionInterval time.Duration` → `SummaryInterval time.Duration`.

- Header parsing:
  - Change `parseHeaders(headers string) (http.Header, error)` at `internal/app/client.go:672`
    to `parseHeaders(pairs []string) (http.Header, error)` and loop over each `"K: V"` item.
  - Update call sites at `internal/app/client.go:156` and `internal/app/client.go:222` to pass `c.Headers`.

- JSON‑RPC flow:
  - Update references: `c.JSONMethod` → `c.RPCMethod` in `measureJSON` (`internal/app/client.go:124`) and `subscriptionPayload` (`internal/app/client.go:258`).
  - In `PrintResponse` (`internal/app/client.go:546`), the branch that marshals JSON when JSON‑RPC was used should check `c.RPCMethod != ""`.

- Verbosity and format behavior:
  - Remove all `c.Basic` branches (`PrintRequestDetails` at `internal/app/client.go:461` and `PrintTimingResults` at `internal/app/client.go:533`).
  - Map behavior to `c.Verbosity` (0 = normal; 1+ = verbose) and `c.Format`:
    - `PrintRequestDetails`:
      - If `c.Quiet`: skip.
      - If `c.Verbosity >= 1`: use the current “verbose” branch (headers, TLS details, etc.).
      - Else: use the current “standard output” branch.
    - `printSubscriptionMessage` (search within `internal/app/client.go`):
      - If `c.Quiet || c.Format == "raw"`: print payload as-is.
      - Else if `c.Verbosity >= 1`: keep current verbose path (bytes + pretty JSON).
      - Else: keep current default path (timestamp + line, pretty JSON if possible).
    - `PrintTimingResults`:
      - Always use the tiered view (call `printTimingResultsTiered`). Remove the basic variant.

- Subscription timer:
  - Update `runSubscriptionLoop` to use `c.SummaryInterval` instead of `c.SubscriptionInterval` (`internal/app/client.go:288–291`).
  - Update buffer assignment to use `c.Buffer` (`internal/app/client.go:243–245`).

### Phase 3 — Help Text and Examples (user-facing polish)

- `cmd/wsstat/main.go:45` — rewrite the custom `flag.Usage`:
  - Add a short Description after Usage.
  - Group flags into: Input (choose one), Subscription, Output, Connection, General.
  - Show defaults inline:
    - `-count` default 1; note special default 0 when `-subscribe` and `-count` not set.
    - `-summary-interval` default 0 (disabled).
    - `-buffer` default 0 (auto).
    - `-format` default `auto`.
  - Add 5–6 real examples for ping, text, JSON‑RPC, subscription once, subscription with summaries, and headers.

- Ensure the help reflects the stdlib constraint for `-v` (repeatable `-v -v` or `-v=2`).

### Phase 4 — Color, Non‑TTY, Exit Codes (optional but recommended)

- Add `-color auto|always|never` with default `auto` and honor `NO_COLOR`.
- Auto‑disable color in non‑TTY unless `always`.
- Document exit codes in README/help footer: 0 success; 1 connection/runtime; 2 usage/validation.
- Prefer sending human diagnostics to stderr; keep machine‑readable or primary data on stdout.
- Likely edits: color toggles and writers in `internal/app/client.go` (printing helpers and the ASCII timeline paths).

### Phase 5 — Future Enhancements (optional)

- `-format json` for one‑shot and periodic summaries (machine‑readable).
- `-timeout` for overall run/connect (align with core options if available).
- `-data-json` and `-data-file` for raw payloads beyond basic text/JSON‑RPC.
- `-scheme {ws,wss}` as an explicit alternative to `-no-tls`.

Acceptance:
- `wsstat --help` shows groups, defaults, and examples; no runtime changes.

### Phase 2 — Naming & Aliases (compat first)

- Add new, clearer flags while keeping old names as hidden or documented aliases:
  - `--rpc-method` (alias existing `-json`).
  - `--summary-interval` (alias existing `--subscription-interval`).
  - `--buffer` (alias existing `--subscription-buffer`).
  - `-H/--header` repeatable (alias existing `--headers`).
  - `--no-tls` (alias existing `--insecure`).
- Make `-v` repeatable (`-v`, `-vv`). Keep `-b` as a hidden alias for one release.
- Keep `-raw` for now; announce upcoming replacement by `--format raw` (Phase 3).
- Files to touch: `cmd/wsstat/main.go`, `cmd/wsstat/main_helpers.go`,
  `internal/app/client.go` (header parsing path can prefer repeated headers first).

Acceptance:
- Old flags continue to work; help prefers new names; repeated headers supported.

### Phase 3 — Format/Verbosity Separation

- Introduce `--format {auto,raw}` with default `auto`:
  - `auto` retains current behavior (pretty‑print JSON when detected).
  - `raw` mirrors current `-raw` behavior.
- Deprecate `-raw` in favor of `--format raw` (keep as alias one release).
- Consider removing `-b` from help (keep as hidden alias), rely on `-v` levels and
  a concise default.
- Files: `cmd/wsstat/main.go`, `internal/app/client.go` (printing paths).

Acceptance:
- `--format raw` produces identical payload output to `-raw`.

### Phase 4 — Color, Non‑TTY, Exit Codes

- Add `--color=auto|always|never` with default `auto`; honor `NO_COLOR`.
- Auto‑disable color when stdout is not a TTY unless `always`.
- Document exit codes: 0 success; 1 connection/runtime; 2 usage/validation.
- Send human messages/errors to stderr; keep machine data on stdout where feasible.
- Files: printing helpers in `internal/app/client.go`.

Acceptance:
- Colors off in non‑TTY by default; documented exit codes; stderr/stdout separation.

### Phase 5 — Deprecations & Cleanups

- Print one‑time deprecation notices when an old flag is used (env‑gated for tests).
- After one release:
  - Drop `-b` from binary (if usage telemetry/docs indicate low adoption).
  - Keep `-json` and `--insecure` as synonyms without advertising, or remove per policy.
- Update README, changelog, and examples to new flags.

Acceptance:
- No breaking changes without a prior release announcing deprecations.

### Phase 6 — Optional Enhancements

- `--format json` for one‑shot and periodic subscription summaries (machine‑readable).
- `--timeout` for overall run/connect timeouts (align with core options).
- `--data-json` and `--data-file` for raw payloads beyond simple text/JSON‑RPC.
- `--scheme {ws,wss}` as explicit alternative to `--no-tls`.

Acceptance:
- New modes come with examples and tests; existing flows unchanged by default.

## Rationale Summaries

- Rename `-json` → `--rpc-method`:
  - “json” is widely used to mean output format; here it is a message kind.
  - Avoids ambiguity; leaves room for `--format json` later.
- Replace `-headers` with repeatable `-H`:
  - Familiar (curl/wget); safer than comma‑separated strings.
- `-summary-interval` and `-buffer` names:
  - Shorter to type; still clear; keep old names as aliases.
- `-no-tls` instead of `-insecure`:
  - Matches actual behavior (scheme assumption); avoids implying “skip verify”.
- Separate verbosity and format:
  - Reduces cognitive load; mirrors common `-q`/`-v` plus `--format` patterns.
- Explicit defaults and examples in help:
  - Shortens the time to first success and reduces trial‑and‑error.

## Testing & Release Checklist

- Unit tests for flag parsing and validation (cmd/wsstat/main_test.go):
  - Mutually exclusive flags still enforced (input choice; verbosity vs quiet).
  - Count defaults: non‑subscribe = 1, subscribe (unset count) = 0.
  - Header parsing with repeated `-H`/`-header`.
  - `-v` repeatability using repeated flags or `-v=2`.
  - `-format raw` produces raw payload output.
- Snapshot tests for `-help` output groups and examples.
- Manual checks: TTY vs non‑TTY color behavior, stderr/stdout separation.
- Docs: README updated, examples validated; call out the breaking flag changes clearly.

## File/Code Pointers

- Flag definitions and usage text: `cmd/wsstat/main.go`
- Parse/validate and scheme handling: `cmd/wsstat/main_helpers.go`
- Client printing/formatting/headers: `internal/app/client.go`

---

This plan keeps behavior stable while improving the surface area users interact
with every day. The low‑risk phases (1–3) deliver most of the UX value; later
phases expand capability without breaking workflows.
