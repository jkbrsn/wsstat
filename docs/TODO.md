# todo

## v3.0.0 work implementation order

The sequence we're clearing the blockers/polish in. Items are grouped so each batch touches a
coherent slice of the code once (freeze a struct once, review the dial path once, etc.). Hard
dependencies are noted; everything else inside a batch can be done in any order.

**Batch 0 — Freeze decisions (no code). DONE.** Four contract-freezing choices that gate the code
below; full text recorded inline on the blocker items.
1. Timing JSON key — float ms @ 3 decimals, keys unchanged.
2. Read-limit — 16 MiB default, `-1` unlimited, `0` = default.
3. Error sentinels — export `ErrConnectionNotEstablished` + `ErrClosed` only; timeouts via `context.DeadlineExceeded`.
4. Schema versioning — single whole-family monotonic version, emitted on every JSON object.

**Batch 1 — Dial-path rework** (one focused pass over `wsstat.go:351-394` + the `Result`/`DialOptions`
structs; freezes the `Result` struct once). `DialContext` is the prerequisite for Batch 2 and the
subscription migration.
- `DialContext(ctx, target, headers)` + per-call ctx/deadline on read/write (reshape step 1).
- Connection-leak fix: drop the redundant `net.LookupIP`, populate `Result.IPs` from the dialed
  address (folds in the "record actually-dialed IP" polish item).
- `WithSubprotocols` + `Result.Subprotocol` field.
- `WithReadLimit` (needs decision #2).
- `WithHeaders` option (reshape step 6).
- Optional while in `DialOptions`: `WithCompression` (default off).

**Batch 2 — One-shots + error contract** (depends on Batch 1's `DialContext`).
- `MeasureText`/`MeasureJSON`/`MeasurePing`; each returns a deep-copied `Result` (reshape step 2,
  subsumes the wrappers-return-deep-copy polish item).
- Full `%v -> %w` sweep + exported sentinels (needs decision #3; reshape step 3).
- Delete the nine `MeasureLatency*` wrappers + goroutine shuttle; unexport `OneHit*`; decide the
  `OneHitMessage` keep/remove polish item (reshape step 4).
- Remove `Dial`; migrate in-tree callers (reshape step 5).
- Tighten `handleConnectionError` to `errors.As`.

**Batch 3 — Race + concurrency** (slot after Batch 2; the one-shots call `calculateResult`).
- `resultMu`/atomic-swap race fix + `-race` test.
- Concurrency-safety contract godoc.
- `nextSubscriptionID` 32-bit-safe.

**Parallel track — Timing precision + TLS integration test** (needs decision #1; disjoint from the
dial-path core, lives in `internal/app/formatting.go`/`output.go` + a new test). Can run alongside
Batches 1-3.

**Batch 4 — CLI error contract** (after sentinels exist in Batch 2).
- JSON error envelope under `-o json`.
- Exit-code normalization + table.

**Batch 5 — Test harness + tests.**
- Swap fixed `localhost:8080` echo server for `httptest` (do before writing new tests).
- Error-path tests for the new `Measure*` funcs.

**Batch 6 — Docs/release hygiene (last).** CHANGELOG cut, README fixes, multi-platform assets,
toolchain + govulncheck, `-insecure` doc fix, schema-versioning doc, pre-tag ephemeral-doc cleanup.

## v3.0.0 release blockers

Full rationale + evidence in [v3-readiness-review.md](./v3-readiness-review.md). Ordered by
the suggested sequencing (contract-freezers first, docs/release hygiene last).

Contract-freezing (breaking if deferred past the tag):

- [ ] **Timing precision**: stop truncating phase durations to whole ms. Touch `formatDuration`
      (`internal/app/formatting.go:25`, integer `d/time.Millisecond`), `msPtr` (`:48`,
      `d.Milliseconds()`), the right-padded text variant (`:18`), and the text RTT/Total call sites
      (`internal/app/output.go:309,315`, `.Milliseconds()`).
  - **Decided**: `durations_ms`/`timeline_ms` values become float ms, rounded to 3 decimals (µs
    resolution). Key names unchanged (unit stays ms, parity with text output). `msPtr` nil-for-zero
    semantics stay, so a real sub-ms phase now renders non-zero. Baked into the initial
    `schema_version "1.0"` (no bump — 1.0 hasn't shipped).
  - Done when: a sub-ms `ws://localhost` measure renders non-zero in both text and JSON.
- [ ] **Measurement API reshape** — decided ([ADR 0002](./decisions/0002-measurement-api-shape.md);
      rationale in [design/measure-wrapper-api.md](./design/measure-wrapper-api.md)). Ordered:
  1. Add `DialContext(ctx, target, headers) error`; make read/write honor a per-call ctx/deadline
     (not only `ws.ctx`). Keep `Dial` until step 5.
  2. Write the three free one-shots `MeasureText`/`MeasureJSON`/`MeasurePing`
     (`(ctx, target, payload, ...Option)`, headers via `WithHeaders`), each owning
     New->DialContext->loop->Close with cleanup on every error path (subsumes the connection-leak
     fix for the one-shot path).
  3. Fold the error contract in here: `%w` wrapping + exported sentinels as one error style across
     the three funcs (the broader `%w` sweep + JSON error envelope stay in the error-contract item).
  4. Delete the nine `MeasureLatency*` wrappers and the ~120-line goroutine/channel shuttle
     (`wrappers.go:193-350`); unexport `OneHitMessage`/`OneHitMessageJSON`.
  5. Remove `Dial`; migrate in-tree callers: `internal/app/measurement.go:23,54,77` -> the three
     free funcs, `_example/main.go:28` -> `MeasureText`, `internal/app/subscription.go:34` + tests
     -> `DialContext`.
  6. Add `WithHeaders(http.Header)` option.
  - Done when: `go build ./...` + tests green, no `MeasureLatency*`/`Dial` symbols remain, and the
    race + error-path tests (below) cover the new funcs.
- [x] **`WithSubprotocols([]string)` + `--subprotocol`** (Batch 1): threaded into
      `DialOptions.Subprotocols`; `Result.Subprotocol` populated from `Conn.Subprotocol()`. Covered by
      `TestWithSubprotocols`.
- [x] **`WithReadLimit(int64)` + `--max-message-size`** (Batch 1): replace the unconditional
      `conn.SetReadLimit(-1)` (`wsstat.go:366`, removes coder's 32 KiB OOM guard) with a bounded
      default that callers can raise.
  - **Decided**: default 16 MiB. `WithReadLimit(n)`: `n > 0` sets the cap, `n < 0` (`-1`) disables
    it (unlimited opt-in), `n == 0` means "unset → default" (a 0-byte limit is never useful). CLI
    `--max-message-size` takes a byte count (optional `K`/`M` suffix), `-1` for unlimited.
- [ ] **Error contract — one coordinated change** (don't split across releases):
  - `%v` -> `%w` at `wsstat.go:361,363,396,576` and `wrappers.go:24,29,61,86,118` (the
    `*WithContext` variants at `:213,275` already use `%w`).
  - Export sentinels for the failure classes library consumers must branch on.
    **Decided**: export exactly `ErrConnectionNotEstablished` (op before successful dial) and
    `ErrClosed` (op on a closed conn; messages prefixed `wsstat:`). Defer `ErrReadTimeout` — once
    read/write honor a per-call ctx, timeouts surface as `context.DeadlineExceeded` /
    `os.ErrDeadlineExceeded`, which consumers check with `errors.Is`. Adding a sentinel later is
    non-breaking; removing one isn't, so freeze the minimal set.
  - Tighten `handleConnectionError` (`internal/app/formatting.go:36-39`) from
    `strings.Contains(errMsg,"tls:")` to `errors.As` once type info survives the `%w` boundary.
  - The JSON error envelope (below) is the CLI-facing half of this same contract.
- [ ] **JSON schema versioning decision** — one `schema_version="1.0"` is stamped on four
      structurally-distinct payloads (timing/response/subscription_summary/subscription_message,
      `internal/app/types.go:15`). One knob can't evolve them independently.
  - **Decided**: single monotonic `schema_version` covering the whole output family as a unit — any
    breaking change to any payload bumps it (`1.0`->`2.0`); additive fields don't. It stays emitted
    on **every** JSON object (NDJSON records are self-describing per line, incl. streamed
    `subscription_message` lines). Publishing a formal JSON Schema in `docs/` is additive doc, can
    land post-freeze (tracked under cross-cutting polish), and does not change this paradigm.

Correctness:

- [ ] **Data race on `ws.result`** (confirmed, 34 race reports reproduced). `calculateResult`
      (`wsstat.go:181-252`) writes every `ws.result.*` field with no lock; reachable concurrently
      from `ExtractResult` (`:620`, called by the app's `handleSubscriptionTick`) and `Close`
      (`:681`, via the pumps' `defer ws.Close()`). `closeDone` (`:672`) and the phase-timing
      callbacks (`:769,802,816,843`) are also unsynchronized writers.
  - Approach: a `resultMu` guarding the `calculateResult` body, the `*ws.result` copy in
    `ExtractResult`, the `closeDone` write, and the timing-callback writes — **or** build a local
    `Result` and atomic-swap a `*Result` pointer.
  - Done when: a new `-race` test calling `ExtractResult` in a loop concurrently with `Close` passes
    (the existing suite doesn't cover this, which is why the race is latent).
- [x] **Connection leak** in `Dial` (Batch 1): dropped the redundant post-handshake `net.LookupIP`;
      `Result.IPs` is now set from the connected address inside the transport's `dialWithAddresses`.
      Removes the leak window *and* the second DNS round-trip, and subsumes the "record actually-dialed
      IP" polish item below.

Contract completeness:

- [ ] **JSON error envelope** under `-o json`: failures currently print plain `Error: %v` text to
      stderr (`cmd/wsstat/main.go:101`, `fail()`), so `... -o json | jq` breaks on the failure path.
      Add a `type:"error"` envelope to `internal/app/types.go` and thread the resolved `Output` into
      the top-level error handler so `fail()` can format it.
  - Decision: emit to stdout (consistent with the NDJSON data stream) or stderr — document whichever.
  - This is the CLI half of the error-contract change above.
- [ ] **Document/normalize exit codes**. Today: 0 success/help/version, 1 runtime error *and*
      post-parse validation, 2 flag-parse error, 130 second-SIGINT — so "bad usage" is split across
      1 and 2 with no rule for scripts.
  - Decision: route post-parse argument/validation errors (`resolveCommon`/`buildMeasure`) to exit 2
    to match flag-parse, reserving 1 for genuine runtime/network failure. Add an exit-code table to
    README/usage.

Test the headline feature:

- [ ] **TLS integration test** (`httptest.NewTLSServer` + `WithTLSConfig(InsecureSkipVerify:true)`):
      assert `TLSHandshake`/`TLSHandshakeDone` are populated and correctly ordered, exercise
      `CertificateDetails` (`result.go:209`, currently 0%) and the TLS `Format` section
      (`DialTLSContext` 20.8%, `printTLSSectionIfPresent` 10.5%). No real `wss://` dial exists in any
      test today. A real handshake also surfaces the timing-truncation bug above — wire these together.
- [ ] **Error-path tests** for the `MeasureLatency*` wrappers + `Dial`: refused port (`127.0.0.1:1`),
      plain-HTTP endpoint (handshake failure), and a timeout via a slow `httptest` server. Assert the
      error is `%w`-wrapped (or matches the new sentinels) and that the returned `Result` is nil.
      `wrappers_test.go` is currently all happy-path.

Docs/release hygiene (low-risk, do last):

- [ ] **Cut the CHANGELOG `[3.0.0]` section**: rename `[Unreleased]` (`CHANGELOG.md:8`) to
      `## [3.0.0] - <date>`, add a fresh empty `[Unreleased]` above it, and add footer links
      `[Unreleased]: .../compare/v3.0.0...HEAD` and `[3.0.0]: .../compare/v2.2.2...v3.0.0`.
- [ ] **Fix README**: `_example/` path (`:220,225` — `go run examples/main.go` fails today),
      `go install` module path missing `/v3` and the stray `@latest` (`:61`), Go-version claim
      (`:48` says 1.21, go.mod needs 1.26), download asset URL (`:77` -> asset is `wsstat-<tag>`).
  - Decision: rename `_example/` -> `examples/` (also makes it visible to go tooling / pkg.go.dev)
    and keep the README text, vs. fix the README refs to `_example/`. Renaming is preferred.
- [ ] **Multi-platform release assets**: `release.yml:208-216` runs host-only `make build` and
      uploads just linux/amd64, while `build-all` (Makefile) covers 8 targets and the README
      advertises downloads. Switch to `make build-all`, upload all `wsstat-<os>-<arch>` (tag-renamed)
      + a `SHA256SUMS` file.
- [ ] **Toolchain + vuln gate**: build/release with go1.26.4 (fixes GO-2026-5039 / GO-2026-5037,
      both std-lib only) and add a `govulncheck` step to CI so future dep/std-lib vulns are caught.
- [ ] **Fix the `-insecure` docs**: CLAUDE.md/AGENTS.md say it "switches to ws://", but it keeps TLS
      and sets `InsecureSkipVerify` (`internal/app/client.go:225-229`). `usage.go:58` is already
      correct; align the agent docs to match.

## v3.0.0 polish (lift the B/C grades to A)

Not release-blocking, but this is the work that takes the review's B/C verticals to A. Low-risk
and mostly mechanical; grouped by the vertical it lifts. Evidence/grades in
[v3-readiness-review.md](./v3-readiness-review.md). The wrapper-family redesign that gates the
Features & API grade is its own discussion: [design/measure-wrapper-api.md](./design/measure-wrapper-api.md).

### Features & API (C+ -> A)

- [ ] Measurement API reshape — **decided** ([ADR 0002](./decisions/0002-measurement-api-shape.md));
      the single biggest lever on this grade. Execution is tracked as a release blocker above.
      Batch 1 landed reshape steps 1 (`DialContext`) and 6 (`WithHeaders`); steps 2-5 (one-shots,
      wrapper deletion, `Dial` removal) are Batch 2.
- [x] `DialContext(ctx, ...)` public method on `WSStat` (Batch 1): caller ctx is installed as the
      connection context, so the pumps and read/write paths honor caller cancellation/deadline.
- [x] `WithCompression` / permessage-deflate toggle (Batch 1): maps to `DialOptions.CompressionMode`,
      disabled by default; negotiated extension reflected in `Result.Compression`. Covered by
      `TestWithCompression`.
- [x] Record the actually-dialed remote IP in `Result.IPs` (Batch 1; done via the connection-leak
      fix). Still open: a `WithIPVersion`/family-preference option (deferred, not yet needed).
- [ ] `WithProxy(*url.URL)` / `--proxy` instead of env-only `http.ProxyFromEnvironment`
      (`wsstat.go:735`), or document proxying as env-driven.
- [ ] Decide on `OneHitMessage` (non-JSON, `wsstat.go:505`): zero in-tree callers — keep as
      supported surface or remove before the freeze.

### Concurrency (C -> A; A2 race fix is the blocker, these finish the job)

- [ ] Document the concurrency-safety contract: which `WSStat` methods are safe to call
      concurrently, and the rule that `ExtractResult` must not race `Dial`/`Close`. Goes in godoc.
- [ ] Wrappers should return `ExtractResult()` (deep copy) instead of the live `ws.result` pointer
      (`wrappers.go:33,67,90,124,...`) — harden against future post-return mutation.
- [ ] Make `nextSubscriptionID` 32-bit-safe: use `atomic.Uint64` (typed) or move it to the first
      struct word (`wsstat.go:85`, `subscription.go:218`).

### Documentation (C+ -> A; README/CHANGELOG fixes are blockers, these reach A)

- [ ] Runnable `Example*` test functions (`ExampleMeasureLatency`, `ExampleWSStat_Subscribe`) so
      pkg.go.dev renders usage and CI compile-checks the snippets. None exist today.
- [ ] Library-side v2->v3 upgrade guide in the README (import path now `/v3`, `ReadPong()` removed,
      gorilla->coder, message-type API stays int-based). Currently only in the CHANGELOG.
- [ ] Per-field godoc on `SubscriptionStats` and `SubscriptionMessage` (`subscription.go:59-76`) to
      match the `Result` convention (esp. `MeanInterArrival`, `Decoded`, `Size` vs `len(Data)`).
- [ ] De-duplicate the package doc (identical block in `wsstat.go:1` and `wrappers.go:1`); keep one
      copy and expand the overview to mention subscriptions/streaming.
- [ ] Document the standards posture (no UTF-8 validation on text frames, compression off by
      default, read-limit behavior) so the deliberate RFC deviations are explicit.
- [ ] Verify `README.md:247` `./TODO.md` link resolves (file is at `docs/TODO.md`).

### Code quality (B+ -> A)

- [ ] Remove dead `clipLines` (`internal/app/output.go:201-208`; only its own test references it).
- [ ] Share a close-status helper between `ReadMessage` (`wsstat.go:571-579`) and `ReadMessageJSON`
      (`:600-602`) so close handling is identical regardless of decode path.
- [ ] Fix doc/impl mismatches: `result.go:113` says `%#v` but verbose is `%+v`; `WithCloseGrace`
      zero-case doc (`wsstat.go:903`) overstates "immediate teardown" vs the timer race.
- [ ] Drop the redundant `extractFirstString` double-flatten (`measurement.go:99,120`).

### Security (B -> A; read-limit/govulncheck/toolchain are blockers, these reach A)

- [ ] URL-scheme allowlist at parse time (`cmd/wsstat/config.go:191-198`): accept only `{ws, wss}`,
      reject `http://`/`https://` (today silently dialed as plaintext via coder's lenient handling).
- [ ] Wrap the dial-error response-body read in `io.LimitReader` (~4 KiB) (`wsstat.go:357`); a
      hostile server can currently reflect an unbounded body into the returned error.
- [ ] Mask sensitive request headers (`Authorization`, `Cookie`, `Proxy-Authorization`) in `-vv`
      output (`internal/app/output.go:383,422`) with an explicit `--show-secrets` opt-out.
- [ ] Track a decision on the transitive `bytedance/sonic` JIT/asm surface pulled via
      `jkbrsn/jsonrpc` (sonic-free build tag in jsonrpc, or drop the dep for the JSON-RPC subset used).

### Testing (B -> A; TLS + error-path tests are blockers, these reach A)

- [ ] Replace the fixed `localhost:8080` echo server (`wsstat_test.go:22,902`) with
      `httptest.NewServer` (random port) + readiness poll; removes the documented port-conflict
      flakiness, the `time.Sleep(250ms)` startup, and unblocks `t.Parallel()`.
- [ ] CI-runnable end-to-end test of the `cmd/wsstat` binary (measure/stream dispatch + exit codes);
      `main.go` dispatch is 0% and the only e2e coverage is the Docker-gated `dev/` scripts.
- [ ] Table-driven test for the `color` package (`internal/app/color/color.go`, currently 0%),
      enabled vs disabled modes.
- [ ] Replace the global `os.Stdout` swap in `captureStdoutFrom` (`testing_helpers.go:22`) with an
      injected `io.Writer` (the app already has `WithOutput`).

### WebSocket standards (B -> A; read-limit + subprotocols are blockers, these reach A)

- [ ] Optional UTF-8 validation on `TextMessage` reads with a surfaced warning (coder skips it), or
      at minimum the documented note above.
- [ ] Allow closing with a chosen status/reason (`CloseWith(code, reason)` or a `Close` arg);
      `gracefulClose` hardcodes 1000 (`wsstat.go:640`). (Also tracked under "Further ahead".)
- [ ] Guard against `documentedDefaultHeaders` drift (`wsstat.go:56-67`): a test that fails if the
      assumed `Sec-WebSocket-Version` changes, or label the map as "library defaults (not captured)".

### CLI UX (B -> A; JSON error envelope is a blocker, these reach A)

- [ ] Payload from stdin/file (`-t @file` / `-t @-`); stdin is currently ignored entirely.
- [ ] Shell completion (`completion` subcommand, bash/zsh/fish).
- [ ] `help <subcommand>` dispatches to that subcommand's usage (`main.go:82-84` ignores the arg).
- [ ] Document NO_COLOR in help text (it works at `output.go:63-65` and is tested; just undocumented).
- [ ] Fix `"Messages sent::"` double colon (`internal/app/output.go:382`; `-vv` path at `:404` is OK).
- [ ] `--close-timeout` above the 5s cap emits a stderr notice instead of silently clamping
      (`config.go:116`, `usage.go:60`).
- [ ] Decide the `-o raw` trailing-newline contract (no terminator today, diverges from `-q`/curl).
- [ ] Align `--version` ("Version: unknown") and the help header ("wsstat unknown"); consider build
      metadata.

### Repo structure & release (B -> A; multi-platform assets + govulncheck are blockers)

- [ ] macOS (and ideally Windows) CI job — the tool's TTY/term/signal code is platform-sensitive
      (`go-isatty`, `x/term`, `signal.Notify(SIGTERM)` is a no-op on Windows).
- [ ] Commit a `cliff.toml` aligned with the Conventional Commit scopes (or switch release notes to
      extract the CHANGELOG `[Unreleased]` section); `release.yml:243` uses git-cliff with no config.
- [ ] `make clean` target / build-into-clean-`bin/` to stop stale per-arch binaries accumulating.
- [ ] SHA256SUMS (and optionally cosign/SLSA provenance) for release assets.
- [ ] Lower the go directive to `go 1.26` + add explicit `toolchain go1.26.4` for reproducibility clarity.

### Cross-cutting (B- -> A)

- [ ] API-diff/gorelease CI gate vs the prior tag, so the v3 freeze is mechanically enforced across
      future 3.x releases. This is what makes all the "decide before freeze" advice durable.
- [ ] `--debug` flag wiring the core's existing zerolog `Debug` logs to stderr, decoupled from
      `-v`/`-vv` output verbosity (the app never calls `WithLogger`).
- [ ] Publish a versioned JSON output schema (docs/ JSON Schema + per-type version semantics) once
      the schema-versioning blocker decision is made.

## Pre-tag / merge cleanup

Ephemeral working docs to delete once their content has fully landed in the durable trail (this
TODO + the ADRs + the architecture overview). Check for dangling references before deleting each.

- [ ] Remove `docs/design/measure-wrapper-api.md` (ephemeral rationale record;
      [ADR 0002](./decisions/0002-measurement-api-shape.md) is the durable decision). Before deleting:
  - Update the `Sources` link in ADR 0002 that points at it (link edits to an accepted ADR are
    allowed by the ADR convention), or drop that bullet.
  - Fix the references in this file (the measurement-API reshape blocker and the Features & API
    polish item both link it).
  - `rg 'measure-wrapper-api'` to catch anything else.
- [ ] Remove `docs/v3-readiness-review.md` (working review report; its findings now live in this
      TODO, the ADRs, and the architecture overview). Before deleting:
  - Fix the "Evidence/grades in ..." links in this file (the blockers intro and the polish intro).
  - `rg 'v3-readiness-review'` to catch anything else.

## Upcoming minor

- Option to log metadata when messages are received

## Further ahead

- CLI: deferred past v3.0.0 (add only when a concrete need appears, YAGNI)
  - `--clip-width N` override (ship `--clip` boolean first)
  - `-vvv` level-3 verbosity (current ladder is content-bounded; needs a custom counter `flag.Value`)
  - raw stream framing opt-in: `--delimiter` / `--print0`
  - JSON output enrichment: `--include headers,certs` / `--detail full` (keep `-o json` schema-stable)
- Homebrew tap
  - Initially self-maintained, e.g. new repo `github.com/jkbrsn/homebrew-wsstat` + `brew tap-new jkbrsn/wsstat` etc.
- Support setting a custom close error/close code
