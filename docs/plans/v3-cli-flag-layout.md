# v3 CLI Flag Layout

Status: proposed. Target: 3.0.0 (breaking changes permitted).

## Problem

The current `cmd/wsstat` surface collapses three orthogonal concerns into two flag
groups, producing non-obvious interactions:

- `-format` mixes **body rendering** (`auto`/`compact`/`truncate`) with the **output
  contract** (`json`/`raw`). `json` and `raw` change the whole program's output, not
  one message body: `json` suppresses human metadata and emits NDJSON envelopes
  (`output.go:101`, `subscription.go:108`); `raw` dumps payload bytes and silences the
  timing report (`output.go:106`). So `-format json -vv` silently ignores `-vv`.
- **Mode** (measure / stream / stream-once) is two booleans plus an overloaded
  `-count` whose meaning flips based on `-subscribe` (`config.go:resolveCountValue`).
  `-subscribe -subscribe-once` together is never rejected; `subscribe-once` wins
  silently (`main.go:166`).
- `truncate` is really `compact` + clip-to-terminal-width, i.e. a modifier wearing a
  format's clothes.

## Design: three orthogonal output axes + subcommands

The deliberate goal is a *boring*, predictable surface: mode is a subcommand, the
stdout contract is one flag, human rendering is two more, and machine output (`json`)
is schema-stable regardless of verbosity.

### Axis 1 — Mode (subcommands)

```
wsstat <url>            # measure: bare form preserved (ping-parity, headline use case)
wsstat measure <url>    # explicit alias of the bare form
wsstat stream <url>     # replaces -subscribe; --once replaces -subscribe-once
```

- Bare `wsstat <url>` dispatches to `measure` (no regression for the common case).
  Dispatch inspects **only `args[0]`**: if it is exactly `measure` or `stream`, that
  subcommand is selected; otherwise everything is handed to `measure` (see the
  dispatch note below for why scanning further is unsafe with stdlib `flag`).
- `stream` owns `--once`, `--buffer`, `--summary-interval`, and `--count` (event limit,
  `0` = unlimited). `measure` owns `--count` (interaction count, `>=1`). The flip in
  `resolveCountValue` disappears: each command defines its own `--count` default and
  bounds.
- Eliminates the two-boolean mode and its silent precedence; `--once` is a single
  flag on one command.

### Axis 2 — Output contract: `-o, --output text|json|raw`

The single stdout-contract axis. All three values change *what stdout is*, not how one
body is rendered:

- `text` (default): human output governed by Axis 3 + verbosity.
- `json`: structured envelopes for the entire program (timing report and stream
  events alike). **JSON is schema-stable**: `-v/-vv` do not change which fields appear,
  so downstream parsers never break on a verbosity flag. Replaces `-format json`.
- `raw`: verbatim payload bytes, no envelope and no timing report — the same whole-
  stdout contract change `json` makes, just unstructured. Replaces `-format raw` and
  the previously-considered `--raw` toggle.

  **Precise definition.** `raw` writes the payload via `os.Stdout.Write` with *nothing*
  added: no `Response:` label (today's path adds one, `output.go:438-446`), no color,
  no timing output, and no automatic trailing newline — **in both measure and stream
  modes**. This is a behavior change from the current raw path, which labels and
  newline-terminates (`output.go:107`, `output.go:446`).

  **Why verbatim for streams too (resolved).** wsstat surfaces binary frames
  (`types.go:223` `BinaryMessage`; `SubscriptionMessage.Data` is `[]byte`), so `raw`
  can carry arbitrary bytes. Any injected delimiter (e.g. a per-frame newline) would
  corrupt binary payloads and is ambiguous for any text payload containing newlines.
  Therefore `raw` adds nothing, ever — streamed frames are concatenated verbatim and
  the consumer is responsible for framing. Callers who need delimited, parseable,
  binary-safe streaming use `-o json` (one NDJSON envelope per frame), which exists for
  exactly that. A future opt-in `--delimiter`/`--print0` is possible but deferred
  (YAGNI).

  **Measure + raw requires a message.** A bare `measure` (ping with no `-t`/
  `--rpc-method`) has no response payload, so `wsstat -o raw <url>` in measure mode
  would emit nothing. Reject it: `-o raw` under `measure` requires `--text` or
  `--rpc-method`. (`json` measure is still valid without a message — it emits the
  timing envelope.)

Opt-in JSON enrichment (e.g. full certs/headers) is intentionally **not** wired to
verbosity. If it is ever needed, add an explicit `--include headers,certs` (or
`--detail full`) flag. This is **deferred past 3.0.0**: ship stable JSON first and add
opt-in fields only when a concrete need appears (YAGNI).

### Axis 3 — Body rendering (text contract only)

- `--body auto|compact` (the human subset of the old `-format`):
  - `auto`: pretty / multi-line (current `auto`).
  - `compact`: one line per message (current `compact`).
- The body axis governs **every** payload body — both streamed messages **and** the
  measured response. Today a measured JSON-RPC response is always `json.Marshal`ed
  compact regardless of format (`output.go:447-453`), so `auto` never pretty-prints it.
  In v3, `--body auto` pretty-prints the measured response and `--body compact`
  one-lines it, identically to how stream messages are rendered.
- `--clip` modifier (boolean): clip each rendered line to terminal width on a TTY.
  Replaces the `truncate` format and composes with both `auto` and `compact`. Because
  `auto` is multi-line, clip must apply **per line** of the rendered body, not to a
  single assembled string (today's `clipLine` clips one line and only under
  `formatTruncate`, `output.go:149-158`). No-op when stdout is not a TTY. A width
  override (`--clip-width N`) is deferred until `--clip` proves insufficient — stdlib
  `flag` has no optional-value support, so a bare boolean plus a later explicit-width
  flag is the only clean shape.

### Verbosity (text contract only) — unchanged semantics, kept axis-pure

`-q` (payload only) → default (summary) → `-v` (target/TLS + diagram) → `-vv` (full
certs/headers). Applies only when `--output text`. Under `json`/`raw` these flags
(and `--body`/`--clip`) are **rejected with a clear error**, not silently ignored —
silent no-op flags are one of the problems this rehaul fixes. `--color` is exempt: its
`auto` default already produces no color when stdout is not a terminal, so it stays
inert under `json`/`raw` rather than being rejected.

## Shared vs per-command flags

Hand-roll subcommand dispatch with `flag.NewFlagSet` per command (stdlib-first; no new
dependency). A single `registerCommon(fs *flag.FlagSet, ...)` helper registers the
shared flags onto each command's FlagSet:

- **Connection (shared):** `-H/--header`, `--resolve`, `-k/--insecure`, `--timeout`,
  `--close-timeout`, `--color`.
- **Input (shared, mutually exclusive):** `--rpc-method`, `-t/--text`.
- **Output (shared):** `-o/--output`, `--body`, `--clip`, `-q`, `-v`, `-vv`.
- **measure-only:** `-c/--count` (`>=1`, default 1).
- **stream-only:** `-c/--count` (`>=0`, default 0 = unlimited), `--once`, `-b/--buffer`,
  `--summary-interval`.
- **Top-level:** `--version`.

### Dispatch (`cmd/wsstat/main.go`)

The subcommand must be the **first token**. Scanning past leading flags to find a
subcommand is unsafe with stdlib `flag`: it cannot know which `-x` tokens consume a
following value, so a flag *value* could be mistaken for a command. Concretely,
`wsstat -t stream <url>` (a text message that happens to be `"stream"`) would be
misread as the `stream` subcommand. Inspecting only `args[0]` avoids this entirely.

```go
func main() {
    args := os.Args[1:]
    switch {
    case len(args) > 0 && args[0] == "stream":
        runStream(args[1:])
    case len(args) > 0 && args[0] == "measure":
        runMeasure(args[1:])
    default: // bare form: hand everything to measure
        runMeasure(args)
    }
}
```

Consequence (documented, not a bug): global flags cannot precede the subcommand —
`wsstat stream -v <url>`, not `wsstat -v stream <url>`. This is the standard
`go test`-style rule. A host literally named `stream`/`measure` must be spelled with a
scheme (`wsstat wss://stream`) or via the explicit `measure` command.

### Internal model: drop the single `Format` enum

Open Question 1 is resolved: **split** `internal/app`'s single `Format` type
(`format.go:10`) into three independent fields/options so the axis-purity reaches the
core, not just the CLI names. Keeping `Format` would preserve the old coupling under
prettier flags.

- `Output` — `text | json | raw` (`WithOutput`)
- `Body` — `auto | compact` (`WithBodyRender`)
- `Clip` — `bool` (`WithClip`)

This removes the `formatJSON`-overrides-everything and `formatRaw`-silences-metadata
branches scattered through `output.go`/`subscription.go`, replacing them with explicit
checks on the relevant axis.

**Make mode explicit internally too.** Rather than the current
`subscribe`/`subscribeOnce` booleans normalized late in `Client.Validate`
(`client.go:304-312`, where `subscribeOnce` silently forces `subscribe=true` and fudges
`count`), give `measure` and `stream` separate parse/validate paths that validate their
own `--count` bounds *before* constructing the client. The client then receives an
already-valid, mode-specific config; the normalization block goes away.

## Removals / renames

- Drop `-no-tls`: a double-negative that only seeds scheme inference for schemeless
  URLs. Users type `ws://` explicitly. `parseWSURI` defaults to `wss://`.
- Keep `--close-timeout` (not renamed to `--close-grace`): "timeout" is the more
  familiar word for CLI users, and the internal `WithCloseGrace` name is never seen by
  them. Because the flag does *not* error on expiry (it proceeds with teardown), the
  help text must say so explicitly, e.g. "max wait for the peer's close echo before
  forcing teardown".
- `-subscribe`/`-subscribe-once` → `stream` / `stream --once`.
- `-format` → `--output` (contract: `text|json|raw`) + `--body` (text rendering) +
  `--clip` (text modifier). **Drop the `-f` short form entirely** rather than reassign
  it; the new short form is `-o`. Reassigning `-f` to a different meaning would silently
  mis-parse v2 muscle memory.
- **Targeted migration errors.** Removed flags (`-subscribe`, `-subscribe-once`,
  `-format`/`-f`, `-no-tls`) should produce a specific "removed in v3; use X instead"
  error rather than stdlib's generic "flag provided but not defined". Implement by
  pre-scanning args (or registering stub `flag.Value`s that error in `Set`) and emitting
  the mapping from the migration table.

### Bundled doc fix: close-timeout wording

CHANGELOG.md:14 says `0` *forces immediate teardown*; the CLI help (`main.go:58`) says
`0` *uses the default (3s)*. These contradict. Before fixing the docs, confirm the
**actual** behavior in the core (`WithCloseGrace`) and correct whichever doc is wrong —
do not just pick a sentence. Bundled here because it touches the same flag.

## v2 → v3 migration table (for CHANGELOG)

| v2 | v3 |
|---|---|
| `wsstat -subscribe <url>` | `wsstat stream <url>` |
| `wsstat -subscribe-once <url>` | `wsstat stream --once <url>` |
| `wsstat -format json` | `wsstat -o json` |
| `wsstat -format compact` | `wsstat --body compact` |
| `wsstat -format truncate` | `wsstat --body compact --clip` |
| `wsstat -format raw` | `wsstat -o raw` |
| `wsstat -f <x>` | removed — use `-o`/`--body`/`--clip` |
| `wsstat -close-timeout 2s` | unchanged (`--close-timeout 2s`) |
| `wsstat -no-tls <host>` | `wsstat ws://<host>` |
| `wsstat -count N` (measure) | unchanged (`-c N`) |
| `wsstat -count N -subscribe` | `wsstat stream -c N <url>` |

## Resolved decisions (no open questions)

- **`raw` stream framing → verbatim everywhere.** Because wsstat surfaces binary frames,
  `raw` injects no bytes in either mode; streams are undelimited and `-o json` is the
  parseable-streaming path (see Axis 2). A future `--delimiter`/`--print0` is deferred.
- **Repeatable `-v` → out of scope.** Keep distinct `-v`/`-vv` (stdlib bools, two
  levels). The verbosity ladder is content-bounded — `-vv` already prints everything
  there is, so there is no level-3 to expose. If a genuine level-3 need appears later, a
  custom counter `flag.Value` (parsing `-vvv`) is an additive, non-breaking change.
- **Internal shape:** split the `Format` enum into `Output`/`Body`/`Clip` fields +
  options; mode is explicit per-command (see "Internal model" above).
- **`measure` subcommand:** exposed as a typed alias of the bare form.
- `raw` is an output contract (`-o raw`), not a toggle; JSON is schema-stable and not
  enriched by `-v/-vv` (opt-in `--include` deferred past 3.0.0); `--close-timeout` is
  kept; `--clip` ships as a boolean (`--clip-width` deferred); dispatch keys on
  `args[0]` only; text-only flags are rejected (not no-op'd) under `json`/`raw`.

## Acceptance criteria

- `wsstat <url>` and `wsstat measure <url>` produce identical output.
- `wsstat stream <url>` streams until interrupted; `--once` exits after first event.
- `-o json` emits the **same** envelope fields regardless of `-v/-vv`; a parser sees a
  stable schema for both measure and stream.
- `-o raw` writes payload bytes verbatim via `os.Stdout.Write` — no label, no color, no
  timing, no added newline, in **both** measure and stream modes (stream frames are
  concatenated undelimited); `-o raw` under `measure` without `--text`/`--rpc-method`
  is rejected.
- `--body auto` pretty-prints the **measured response** as well as stream messages;
  `--body compact` one-lines both.
- `--clip` clips **per line** of `auto` (multi-line) output, not just `compact`.
- `--body`/`--clip`/`-q`/`-v`/`-vv` are **rejected with a clear error** under
  `json`/`raw` (not silently ignored); `--color` stays inert.
- Each subcommand's `-h` lists only flags valid for that command.
- Invalid combinations (`--text` + `--rpc-method`; `stream`-only flags under
  `measure`) are rejected with a clear error, not silently ignored.
- Removed v2 flags (`-subscribe`, `-subscribe-once`, `-format`/`-f`, `-no-tls`) emit a
  targeted "removed; use X" migration error, verified by test.
- `make lint && make test` pass; `flags_test.go`/`config_test.go` updated for the new
  parsing/dispatch.
- Docs updated: `README.md` (currently documents `-subscribe`/`-format`, README.md:128-177),
  `CHANGELOG.md`, `VERSION`, and any smoke/dev docs referencing the old flags. The
  close-timeout wording contradiction (CHANGELOG vs help) is reconciled against actual
  core behavior.
