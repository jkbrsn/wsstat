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

**Batch 1 — Dial-path rework. DONE.** (one focused pass over `wsstat.go:351-394` + the
`Result`/`DialOptions` structs; freezes the `Result` struct once). `DialContext` is the prerequisite
for Batch 2 and the subscription migration.
- `DialContext(ctx, target, headers)` + per-call ctx/deadline on read/write (reshape step 1).
- Connection-leak fix: drop the redundant `net.LookupIP`, populate `Result.IPs` from the dialed
  address (folds in the "record actually-dialed IP" polish item).
- `WithSubprotocols` + `Result.Subprotocol` field.
- `WithReadLimit` (needs decision #2).
- `WithHeaders` option (reshape step 6).
- Optional while in `DialOptions`: `WithCompression` (default off).

**Batch 2 — One-shots + error contract. DONE.** (depended on Batch 1's `DialContext`).
- `MeasureText`/`MeasureJSON`/`MeasurePing`; each returns a deep-copied `Result` (reshape step 2,
  subsumes the wrappers-return-deep-copy polish item).
- Full `%v -> %w` sweep + exported sentinels (needs decision #3; reshape step 3).
- Delete the nine `MeasureLatency*` wrappers + goroutine shuttle; unexport `OneHit*`; decide the
  `OneHitMessage` keep/remove polish item (reshape step 4).
- Remove `Dial`; migrate in-tree callers (reshape step 5).
- Tighten `handleConnectionError` to `errors.As`.

**Batch 3 — Race + concurrency. DONE.** (slot after Batch 2; the one-shots call
`calculateResult`).
- `resultMu` race fix + `-race` test. `calculateResult` -> `calculateResultLocked` (caller holds
  `resultMu`); guarded the `ExtractResult` snapshot copy and `Close`'s `closeDone` write +
  finalize. New `TestRaceExtractResultDuringClose` fires `DATA RACE` without the lock, passes with.
- Concurrency-safety contract godoc on `WSStat`.
- `nextSubscriptionID` 32-bit-safe via typed `atomic.Uint64`.

**Parallel track — Timing precision + TLS integration test** (needs decision #1; disjoint from the
dial-path core, lives in `internal/app/formatting.go`/`output.go` + a new test). Can run alongside
Batches 1-3.

**Batch 4 — CLI error contract. DONE.** (after sentinels exist in Batch 2).
- JSON error envelope under `-o json`: `errorOutputJSON` + exported `EmitJSONError(w, err)` in
  `internal/app/types.go`; emitted to stdout on the runtime-failure path.
- Exit-code normalization + table: `cliError`/`usageErr`/`runtimeErr` in `main.go` route validation
  to exit 2, runtime to exit 1; table in `usage.go` (`EXIT CODES`) + README.

**Batch 5 — Test harness + tests. DONE.**
- Swapped the fixed `localhost:8080` echo server for a shared `httptest.NewServer` on a random
  port (no readiness sleep; `echoHostPort`/`echoHostURL` helpers carry the dynamic port into the
  resolve-override tests).
- Error-path tests for the `Measure*` funcs: `TestMeasureDialErrors` covers refused port,
  plain-HTTP handshake failure, and dial timeout across MeasureText/JSON/Ping (asserts wrapped
  error + nil Result).

**Batch 6 — Docs/release hygiene (last). PARTIAL.** Landed: README fixes (incl. `_example/` ->
`examples/` rename), `-insecure` doc fix, and the pre-tag ephemeral-doc cleanup. Deferred to a
dedicated release-CI pass at tag time: CHANGELOG `[3.0.0]` cut, multi-platform assets + `SHA256SUMS`,
and the go1.26.4 toolchain + `govulncheck` gate. (The formal JSON-Schema doc remains a separate
cross-cutting polish item.)

## v3.0.0 release blockers

Ordered by the suggested sequencing (contract-freezers first, docs/release hygiene last).

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
- [x] **Measurement API reshape** (Batches 1-2; [ADR 0002](./decisions/0002-measurement-api-shape.md)).
      All six steps landed: `DialContext` + ctx-honoring read/write and `WithHeaders` (Batch 1); the
      three free one-shots `MeasureText`/`MeasureJSON`/`MeasurePing` in `measure.go`, deletion of the
      nine wrappers + goroutine shuttle (`wrappers.go` removed), removal of `OneHitMessage*` and
      `Dial`, and migration of all in-tree callers + ~33 test sites to `DialContext` (Batch 2).
      `MeasureLatency*`/`Dial`/`OneHit*` symbols are gone; error-path tests landed in Batch 5.
- [x] **`WithSubprotocols([]string)` + `--subprotocol`** (Batch 1): threaded into
      `DialOptions.Subprotocols`; `Result.Subprotocol` populated from `Conn.Subprotocol()`. Covered by
      `TestWithSubprotocols`.
- [x] **`WithReadLimit(int64)` + `--max-message-size`** (Batch 1): replace the unconditional
      `conn.SetReadLimit(-1)` (`wsstat.go:366`, removes coder's 32 KiB OOM guard) with a bounded
      default that callers can raise.
  - **Decided**: default 16 MiB. `WithReadLimit(n)`: `n > 0` sets the cap, `n < 0` (`-1`) disables
    it (unlimited opt-in), `n == 0` means "unset → default" (a 0-byte limit is never useful). CLI
    `--max-message-size` takes a byte count (optional `K`/`M` suffix), `-1` for unlimited.
- [x] **Error contract — core half** (Batch 2): `%v`->`%w` on the `DialContext` dial errors and the
      `ReadMessage` "unexpected close error"; exported `ErrConnectionNotEstablished` + `ErrClosed`
      (returned by `ReadMessage`/`ReadMessageJSON`/`PingPong` via a `closed atomic.Bool` + conn-nil
      guards); `handleConnectionError` tightened to `errors.As` over the tls/x509 cert types with a
      string fallback. `ErrReadTimeout` deferred (timeouts surface as `context.DeadlineExceeded`).
      Remaining CLI half — the JSON error envelope — is its own item below (Batch 4).
- [x] **JSON schema versioning decision** — decision frozen and implementation matches: a single
      `JSONSchemaVersion = "1.0"` (`internal/app/types.go:16`) is stamped on every envelope —
      timing/response/subscription_summary/subscription_message and the `error` envelope (Batch 4).
  - **Decided**: single monotonic `schema_version` covering the whole output family as a unit — any
    breaking change to any payload bumps it (`1.0`->`2.0`); additive fields don't. It stays emitted
    on **every** JSON object (NDJSON records are self-describing per line, incl. streamed
    `subscription_message` lines). Publishing a formal JSON Schema in `docs/` is additive doc, can
    land post-freeze (tracked under the cross-cutting polish item below), and does not change this
    paradigm.

Correctness:

- [x] **Data race on `ws.result`** (Batch 3). Fixed with a `resultMu` guarding the (renamed)
      `calculateResultLocked` body, the `*ws.result` snapshot copy in `ExtractResult`, and `Close`'s
      `closeDone` write + finalize. The dial-time `TLSState`/`IPs`/phase-timing writes happen-before
      the pumps start (single-threaded dial), so they don't race and stay lock-free; the message
      read/write slices keep their existing `timings.mu`. `nextSubscriptionID` is now `atomic.Uint64`.
      Lock order is `resultMu` -> `subscriptionMu` (via `snapshotSubscriptionStats`); no inverse path.
  - Done: `TestRaceExtractResultDuringClose` loops `ExtractResult` concurrently with `Close` and
    passes under `-race` (verified it reports `DATA RACE` with the lock removed).
- [x] **Connection leak** in `Dial` (Batch 1): dropped the redundant post-handshake `net.LookupIP`;
      `Result.IPs` is now set from the connected address inside the transport's `dialWithAddresses`.
      Removes the leak window *and* the second DNS round-trip, and subsumes the "record actually-dialed
      IP" polish item below.

Contract completeness:

- [x] **JSON error envelope** under `-o json` (Batch 4): `errorOutputJSON` + exported
      `EmitJSONError(w, err)` in `internal/app/types.go`; `fail()` (`cmd/wsstat/main.go`) threads the
      resolved `Output` via `cliError` and emits the `type:"error"` record on the runtime-failure
      path. Decision: emit to **stdout** (consistent with the NDJSON data stream); usage errors keep
      plain stderr text. Covered by `TestEmitJSONError` + `TestErrorClassification`.
- [x] **Document/normalize exit codes** (Batch 4): post-parse argument/validation errors now exit 2
      (matching flag-parse) via `usageErr`, reserving 1 for runtime/network failure via `runtimeErr`;
      `130` stays second-SIGINT. Exit-code table added to `usage.go` (`EXIT CODES`) and the README.
      Covered by `TestErrorClassification`.

Test the headline feature:

- [x] **TLS integration test**: `TestDialTLS` (`wsstat_test.go`) dials a real `wss://` against an
      in-test HTTPS echo server using a generated self-signed ECDSA cert with known fields
      (`selfSignedCert`/`newTLSEchoServer` helpers) and `WithTLSConfig(InsecureSkipVerify:true)`.
      Asserts `TLSState`/`HandshakeComplete`, `TLSHandshake`/`TLSHandshakeDone` populated and ordered,
      `CertificateDetails` parsing (CommonName/DNSNames/NotAfter), and the `%+v` TLS `Format` section.
      Lifted `CertificateDetails` 0%->83%, `printTLSSectionIfPresent` 10.5%->95%, `DialTLSContext`
      (via `newHTTPClient`) ~21%->71%. Passes under `-race`. The timing-truncation bug stays separate
      (asserts on raw `time.Duration` ordering, not ms rendering).
- [x] **Error-path tests** for the one-shot `Measure*` funcs (Batch 5): `TestMeasureDialErrors`
      (`measure_test.go`) covers refused port (`127.0.0.1:1`), a plain-HTTP endpoint (handshake
      failure), and a dial timeout via a context-blocking `httptest` server, across
      MeasureText/MeasureJSON/MeasurePing. Asserts the error is `%w`-wrapped (`dial: ...`) and the
      returned `Result` is nil. (The `MeasureLatency*` wrappers + `Dial` no longer exist as of Batch 2.)

Docs/release hygiene (low-risk, do last):

- [ ] **Cut the CHANGELOG `[3.0.0]` section**: rename `[Unreleased]` (`CHANGELOG.md:8`) to
      `## [3.0.0] - <date>`, add a fresh empty `[Unreleased]` above it, and add footer links
      `[Unreleased]: .../compare/v3.0.0...HEAD` and `[3.0.0]: .../compare/v2.2.2...v3.0.0`.
  - Deferred to actual tag time (so the release date isn't fixed prematurely).
- [x] **Fix README** (Batch 6): renamed `_example/` -> `examples/` (git mv; dropped a stray 9 MB
      tracked binary), so `go run examples/main.go` works and the dir is visible to go tooling.
      Fixed the `go install` line to `./cmd/wsstat` (was a `/v3`-less module path + contradictory
      `@latest`), the Go-version claim (1.21 -> 1.26), the download asset URL (`wsstat-<tag>`), and
      the `./TODO.md` link (-> `./docs/TODO.md`).
- [ ] **Multi-platform release assets**: `release.yml:208-216` runs host-only `make build` and
      uploads just linux/amd64, while `build-all` (Makefile) covers 8 targets and the README
      advertises downloads. Switch to `make build-all`, upload all `wsstat-<os>-<arch>` (tag-renamed)
      + a `SHA256SUMS` file.
  - Deferred (Batch 6): release-CI work, to land in a dedicated pass with the govulncheck/toolchain item.
- [ ] **Toolchain + vuln gate**: build/release with go1.26.4 (fixes GO-2026-5039 / GO-2026-5037,
      both std-lib only) and add a `govulncheck` step to CI so future dep/std-lib vulns are caught.
  - Deferred (Batch 6): local Go is 1.26.3; pinning the toolchain triggers a local auto-download, so
    this is held for the release-CI pass (bump CI `GO_VERSION` + go.mod together then).
- [x] **Fix the `-insecure` docs** (Batch 6): `AGENTS.md:32` (CLAUDE.md symlinks to it) now says
      `-insecure`/`-k` keeps TLS but sets `InsecureSkipVerify` (use `ws://` for plaintext), matching
      `usage.go`.

## v3.0.0 polish (lift the B/C grades to A)

Not release-blocking, but this is the work that takes the review's B/C verticals to A. Low-risk
and mostly mechanical; grouped by the vertical it lifts. The wrapper-family redesign that gated the
Features & API grade is recorded in [ADR 0002](./decisions/0002-measurement-api-shape.md).

### Features & API (C+ -> A)

- [x] Measurement API reshape (Batches 1-2; [ADR 0002](./decisions/0002-measurement-api-shape.md),
      now accepted) — the single biggest lever on this grade. Execution tracked as a release blocker
      above; all six steps landed.
- [x] `DialContext(ctx, ...)` public method on `WSStat` (Batch 1): caller ctx is installed as the
      connection context, so the pumps and read/write paths honor caller cancellation/deadline.
- [x] `WithCompression` / permessage-deflate toggle (Batch 1): maps to `DialOptions.CompressionMode`,
      disabled by default; negotiated extension reflected in `Result.Compression`. Covered by
      `TestWithCompression`.
- [x] Record the actually-dialed remote IP in `Result.IPs` (Batch 1; done via the connection-leak
      fix). Still open: a `WithIPVersion`/family-preference option (deferred, not yet needed).
- [ ] `WithProxy(*url.URL)` / `--proxy` instead of env-only `http.ProxyFromEnvironment`
      (`wsstat.go:735`), or document proxying as env-driven.
- [x] Decide on `OneHitMessage` (Batch 2): removed both `OneHitMessage`/`OneHitMessageJSON` (the
      one-shots use the Write/Read loop directly).

### Concurrency (C -> A; A2 race fix is the blocker, these finish the job)

- [x] Document the concurrency-safety contract (Batch 3): `WSStat` godoc now states the dial-first
      rule and that `ExtractResult`/`Close`/read/write/subscription methods are safe concurrently.
- [x] Wrappers should return `ExtractResult()` (deep copy) (Batch 2): the new one-shots return
      `ExtractResult()` after Close; the live-pointer-returning wrappers are gone.
- [x] Make `nextSubscriptionID` 32-bit-safe (Batch 3): typed `atomic.Uint64`
      (`wsstat.go:98`, `subscription.go:218`).

### Documentation (C+ -> A; README/CHANGELOG fixes are blockers, these reach A)

- [ ] Runnable `Example*` test functions (`ExampleMeasureLatency`, `ExampleWSStat_Subscribe`) so
      pkg.go.dev renders usage and CI compile-checks the snippets. None exist today.
- [ ] Per-field godoc on `SubscriptionStats` and `SubscriptionMessage` (`subscription.go:59-76`) to
      match the `Result` convention (esp. `MeanInterArrival`, `Decoded`, `Size` vs `len(Data)`).
- [~] De-duplicate the package doc (Batch 2): the duplicate block went away with `wrappers.go`; the
      one in `wsstat.go:1` remains. Still TODO: expand that overview to mention subscriptions/streaming.
- [ ] Document the standards posture (no UTF-8 validation on text frames, compression off by
      default, read-limit behavior) so the deliberate RFC deviations are explicit.
- [ ] Verify `README.md:247` `./TODO.md` link resolves (file is at `docs/TODO.md`).

### Code quality (B+ -> A)

- [x] Remove dead `clipLines` (`internal/app/output.go`): deleted the function and its only caller,
      `TestClipLinesMultiLine`; `clipBodyWithPrefix` keeps its own inline per-line clipping.
- [x] Share a close-status helper between `ReadMessage` and `ReadMessageJSON`: extracted
      `classifyReadErr` (`wsstat.go`), called by both, so close handling is identical regardless of
      decode path (`ReadMessageJSON` now wraps abnormal closes too; noted in CHANGELOG).
- [x] Fix doc/impl mismatches: `result.go` `formatVerbosePlus` comment now says `%+v` (was `%#v`);
      `WithCloseGrace` zero-case doc reworded to reflect the timer race rather than "immediate teardown".
- [x] Drop the redundant `extractFirstString` double-flatten — moot: current code has a single
      clean flatten (`measurement.go:120`, called once at `:99`); no redundancy remains to drop.

### Security (B -> A; read-limit/govulncheck/toolchain are blockers, these reach A)

- [x] URL-scheme allowlist at parse time (`cmd/wsstat/config.go` `parseWSURI`): accept only
      `{ws, wss}`, reject `http://`/`https://` (and any other scheme) with a directed error instead
      of silently dialing plaintext. Scheme-less input still defaults to `wss://`. Covered by
      `TestParseWSURI` rejection rows.
- [x] Bounded the dial-error response-body read with `io.LimitReader(resp.Body, 4 KiB)`
      (`wsstat.go`, `maxErrBodyBytes`); a hostile server can no longer reflect an unbounded body
      into the returned error.
- [x] Mask sensitive headers (`Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`) in
      `-vv` output (`internal/app/output.go` `headerValue`/`sensitiveHeaders`) as `[redacted]`, with
      an explicit `--show-secrets` opt-out (text-only flag). Covered by
      `TestPrintRequestDetailsMasksSecrets`. (Library `%+v` `printHeadersSection` left unmasked by
      design — out of CLI scope.)
- [x] Resolved the transitive `bytedance/sonic` JIT/asm surface (pulled via `jkbrsn/jsonrpc`) by
      dropping the dependency entirely (option C). The two call sites (`measureJSON`/subscription
      request build, text-response decode) now use an inline JSON-RPC 2.0 subset in
      `internal/app/jsonrpc.go` over the stdlib `encoding/json`; `go.mod`/`go.sum` no longer reference
      `jsonrpc` or `sonic`. Covered by `TestDecodeJSONRPCResponse`/`TestNewJSONRPCRequest`.

### Testing (B -> A; TLS + error-path tests are blockers, these reach A)

- [x] Replaced the fixed `localhost:8080` echo server with a shared `httptest.NewServer` (random
      port) in `TestMain` (Batch 5). httptest is listening before it returns, so the `time.Sleep(250ms)`
      startup and port-conflict flakiness are gone. Existing tests were not mass-converted to
      `t.Parallel()` (left for a follow-up), but the shared server is now read-only so it no longer
      blocks them.
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

- [x] Removed `docs/design/measure-wrapper-api.md` (Batch 6; [ADR 0002](./decisions/0002-measurement-api-shape.md)
      is the durable decision). Dropped its `Sources` bullet in ADR 0002 and the two intro links in
      this file; `rg 'measure-wrapper-api'` is clean.
- [x] Removed `docs/v3-readiness-review.md` (Batch 6; findings now live in this TODO, the ADRs, and
      the architecture overview). Dropped the blockers-intro and polish-intro links; `rg
      'v3-readiness-review'` is clean.

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
