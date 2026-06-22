# wsstat v3.0.0 Readiness Review

Multi-vertical pre-release review of `github.com/jkbrsn/wsstat/v3` at branch `stage-v3.0.0`
(VERSION=3.0.0, post coder/websocket migration, not yet tagged). Produced by a 10-agent
review sweep; the highest-impact claims were independently spot-checked against the code.

## Scorecard

| Vertical | Grade | One-line |
| --- | --- | --- |
| Code quality & Go idioms | B+ | Idiomatic, lint-clean, 80-line discipline holds; `%v`-not-`%w` is the main wart |
| CLI UX | B | Thoughtful dispatch/output model; JSON error path + a couple of bugs let it down |
| Feature set & API design | C+ | Solid core, but asymmetric `MeasureLatency*` matrix and real transport gaps freeze badly |
| WebSocket standards | B | RFC-correct via coder; deliberate deviations OK; unbounded read limit is the risk |
| Concurrency & correctness | C | Subscription/pump lifecycle is excellent; one real `result` data race + a leak |
| Testing | B | Good migration/race coverage; TLS path and error paths almost untested |
| Repo structure / release | B | Clean layering, good release guards; single-platform asset, thin CI |
| Documentation | C+ | Godoc strong; README has factual errors and CHANGELOG isn't cut for 3.0.0 |
| Security & deps | B | Verifies by default, no MITM hole; unbounded reads + stale toolchain |
| Cross-cutting | B- | **Sub-ms timing truncated to whole ms** — headline defect nobody else caught |

Overall: **the project is in good shape and close to release-ready.** The architecture,
concurrency primitives, and release tooling are more mature than typical for a tool this
size. What stands between it and a clean v3 tag is a short list of correctness/contract
issues that are cheap to fix but expensive to fix *after* the API and JSON schema freeze.

---

## A. Release blockers (fix before tagging v3.0.0)

These either corrupt the headline feature, break the machine-facing contract that v3 is
about to freeze, or are trivially-correct doc fixes a user hits in the first five minutes.

### A1. Timings truncated to whole milliseconds — destroys sub-ms fidelity
`internal/app/formatting.go:25` (`formatDuration` → `%dms` via integer `d/time.Millisecond`)
and `:48` (`msPtr` → `d.Milliseconds()`, `nil` when `d<=0`). A 900µs phase renders `0ms`/`-`
in text and as integer ms (or omitted) in JSON. For LAN/localhost targets — including the
project's own `ws://localhost:8080` test server — DNS/TCP/WS phases are routinely sub-ms, so
the core timing output collapses to `0`. **This is the tool's headline feature emitting
zeros.** Fix: float64 ms (`%.3fms`) for text and either `durations_us` (integer µs) or
float ms in the JSON envelope. Must land before `durations_ms`/`schema_version "1.0"` freezes.
Verified directly.

### A2. Data race on `ws.result` via concurrent `calculateResult()`
`wsstat.go:181-252` reads/writes every `ws.result.*` field with no lock (only `timings.mu`
guards the message slices). It's reachable concurrently from `ExtractResult()` (`wsstat.go:620`,
called by the app's `handleSubscriptionTick`, `internal/app/subscription.go:17`) **and** from
`Close()` (`wsstat.go:681`, run via the pumps' `defer ws.Close()`). The concurrency agent
reproduced 34 race reports under `-race`. This is the exact pattern the streaming path uses,
so it's not theoretical. Fix: guard `calculateResult` + the `*ws.result` copy in
`ExtractResult` (and the `closeDone`/phase-timing writes) with a mutex, or build a local
`Result` and atomic-swap a `*Result`. Add a `-race` regression test (`ExtractResult` vs
`Close`); the current suite doesn't cover it, which is why it's latent.

### A3. JSON output contract is violated on the error path
Under `-o json`, a failure prints plain `Error: ...` text to stderr (`cmd/wsstat/main.go:101`,
`fail()`); there is no `type:"error"` envelope in `internal/app/types.go`. `wsstat measure -o
json ... | jq` gets valid NDJSON on success and unparseable text on failure — the one place
machine consumers most need structure. Fix: emit a final
`{"schema_version":"1.0","type":"error","message":"..."}` envelope before the non-zero exit.

### A4. CHANGELOG has no dated `[3.0.0]` entry
`CHANGELOG.md:8` — all v3 content still lives under `## [Unreleased]`; no `## [3.0.0] - <date>`
header and no `[3.0.0]` compare link, while `VERSION` already reads `3.0.0`. Cut the section
and add footer links before tagging.

### A5. README factual errors a v3 user hits immediately
- `README.md:220,225` reference `examples/main.go`; the dir is `_example/` — the documented
  `go run examples/main.go` fails. (Renaming `_example/` → `examples/` also makes it visible to
  pkg.go.dev.)
- `README.md:61` `go install ... github.com/jkbrsn/wsstat@latest` is missing `/v3` (resolves to
  v2) and appends `@latest` after a local clone, discarding the checkout + ldflags.
- `README.md:48` claims "Go 1.21 or later"; `go.mod` requires `go 1.26.3`.
- `README.md:77` documents `wget .../wsstat`; the release asset is `wsstat-<tag>` → 404.

### A6 (decide, don't necessarily build). API freeze decisions that are breaking if deferred
These don't block the *binary* but DO block the *frozen library surface* — adding them after
3.0.0 is a breaking signature/behavior change, so the decision must be made now:
- **Context/Options on non-burst wrappers** (`wrappers.go:15,73,131`): only `*BurstWithContext`
  variants take `ctx`/`opts`. `MeasureLatency`/`...JSON`/`...Ping` can't be canceled or
  configured at all. Add `...Option` (additive) + `*WithContext` forms, or collapse the family.
- **`WithSubprotocols`** (`wsstat.go:351` sets only HTTPClient/HTTPHeader; coder supports
  `DialOptions.Subprotocols`): without it wsstat cannot even connect to graphql-ws / mqtt /
  ocpp / wamp endpoints. Highest-value missing capability.

---

## B. High-value gaps to add before the freeze (feature completeness)

Ranked by value. All are API/CLI-surface decisions best settled pre-3.0.0.

1. **`WithSubprotocols` + `--subprotocol`** (see A6) and surface the negotiated
   `Sec-WebSocket-Protocol` in `Result`.
2. **`WithReadLimit(int64)` + `--max-message-size`**, with a bounded default instead of the
   current unconditional `SetReadLimit(-1)` (`wsstat.go:366`), which removes coder's 32 KiB
   OOM guard. Keep it configurable; just don't default to unlimited.
3. **Sub-ms timing precision in the schema** (A1) — listed here too because it's a contract
   decision, not only a bug.
4. **Exported error sentinels** (`ErrConnectionNotEstablished`, `ErrClosed`, …) plus the
   `%v`→`%w` conversion (see C1) so library consumers can `errors.Is/As` instead of string-matching.
5. **`DialContext(ctx, ...)`** public method (`Dial` at `wsstat.go:347` only uses the internal
   background context).
6. **Payload from stdin/file** (`-t @file` / `-t @-`): a large JSON-RPC params body must
   currently fit on the command line; stdin is ignored entirely.
7. **`WithCompression` / permessage-deflate toggle** (RFC 7692) — keep off by default but let
   users reproduce deflate-negotiating clients; reflect negotiated extensions in `Result`.
8. **`--debug` flag** wiring the core's existing zerolog `Debug` logs to stderr (the app never
   calls `WithLogger`); today a failed dial/handshake gives only a one-line error.
9. **Record the actually-dialed IP** in `Result.IPs` (`wsstat.go:394` dumps the full
   `net.LookupIP` set, not the connected address) and add an IPv4/IPv6 preference option.
10. **Shell completion** (`completion` subcommand) and **multi-platform release assets** (B/§G).
11. **Programmatic proxy** (`WithProxy`/`--proxy`) instead of env-only `http.ProxyFromEnvironment`.
12. **Dropped-message counter** in `SubscriptionStats` (delivery silently drops on full buffer,
    `subscription.go:322-326`) — add before the stats struct freezes.

---

## C. Per-vertical findings

### C1. Code quality — B+
Strengths: clean functional-options across all three layers; no function over the 80-line cap
(`calculateResult` 71L is the longest); golangci-lint clean; careful concurrency primitives
(`atomic.Pointer`, `closeOnce`, scoped `RWMutex`); intent-focused comments.
- **[high]** Core wraps errors with `%v` not `%w` (`wsstat.go:361,363,396,576`,
  `wrappers.go:24,29,61,86,118`), violating the project's own convention and forcing
  `handleConnectionError` (`internal/app/formatting.go:37`) to fall back to
  `strings.Contains(errMsg,"tls:")`. Convert to `%w`; pair with exported sentinels (B4).
- **[medium]** `ReadMessage` filters close status (`wsstat.go:571-579`) but `ReadMessageJSON`
  (`:600-602`) doesn't — divergent error semantics for the same socket condition. Share a helper.
- **[low]** `clipLines` (`internal/app/output.go:201-208`) is dead (only its own test refs it).
- **[low]** `result.go:113` doc says `%#v` triggers verbose, but `Format` only branches on
  `s.Flag('+')` — verbose is `%+v`. Fix the comment (or add the `#` branch).
- **[low]** `WithCloseGrace` zero-case doc overstates "immediate teardown" vs the timer race
  (`wsstat.go:645,903`); `extractFirstString` double-flattens (`measurement.go:99,120`).

### C2. CLI UX — B
Strengths: documented `measure`/`stream` dispatch; excellent v2→v3 removed-flag hints
(`main.go:106-133`); strong flag-combo validation; three orthogonal output axes with
`schema_version` on every envelope; graceful double-SIGINT (first prints summary, second exits
130); correct stdout/exit-0 for `--help`/`--version`.
- **[high]** JSON error path (A3).
- **[medium]** `"Messages sent::"` double colon — `output.go:382` label ends in `:` and the
  format adds another. (`-vv` path at `:404` is correct.)
- **[medium]** Exit codes undocumented and split: post-parse validation errors exit 1 via
  `fail()`, flag-parse errors exit 2 — no rule for scripts. Document, and consider routing
  user-error validation to 2.
- **[medium]** Typos/misordered globals surface as `expected exactly one URL argument`
  (`config.go:179-189`); list the offending args and hint near-miss subcommands.
- **[low]** `help <subcmd>` ignores its argument (`main.go:82-84`); `-o raw` emits no trailing
  newline (diverges from `-q`); `--version` ("Version: unknown") vs help header ("wsstat
  unknown") disagree; `--close-timeout` silently capped at 5s with no feedback.
- Correction (from cross-check): **NO_COLOR is implemented and tested** (`output.go:63-65`,
  `client_output_test.go:41`) — the only gap is that it's undocumented in help.

### C3. Feature set & API — C+
Strengths: the subscription API is the strongest part of the surface
(`Updates()`/`Done()`/`Cancel()`/counts, `SubscribeOnce`, archived stats);
`ExtractResult` deep-copies; `WithResolves` is a nice curl-like touch.
- **[high]** Context/Options only on `*Burst*` wrappers (A6); **[high]** no subprotocol
  negotiation (A6); **[medium]** `MeasureLatency*` matrix is asymmetric and hard to learn —
  decide the final shape now; **[medium]** no `DialContext`; **[medium]** no compression
  control; **[low]** auth/redirect/IPv6/proxy surfaces are thin (see §B).

### C4. WebSocket standards — B
RFC 6455 conformance is solid because coder/websocket (Autobahn-tested) owns masking,
control frames, fragmentation, ping/pong matching, and the close handshake. wsstat's own
deviations (3s close grace under coder's 5s; handshake-before-cancel ordering) are deliberate
and documented (`wsstat.go:636-669`). TLS verifies by default.
- **[high]** `SetReadLimit(-1)` (`wsstat.go:366`) removes all max-message-size protection (B2).
- **[medium]** No subprotocol negotiation (dup of A6).
- **[low]** No UTF-8 validation on text frames (inherited from coder — document it); close
  always sends 1000 (no way to test other close codes); `documentedDefaultHeaders`
  (`wsstat.go:56-67`) is a hand-maintained mirror that can drift from the library.

### C5. Concurrency — C
Strengths: subscription buffer send/close/finalize all correctly guarded by `state.mu` + a
`closed` flag (send-on-closed impossible, double-close impossible); pumps call `wgPumps.Done()`
before `Close()` so no self-deadlock; documented load-bearing close ordering. The existing race
suite passes.
- **[high]** `result` data race (A2).
- **[medium]** phase-timing writes in transport callbacks (`wsstat.go:769,802,816,843`) race a
  concurrent `ExtractResult` during `Dial` — same root cause, same mutex fix.
- **[medium]** **Connection leak**: in `Dial`, `conn` is stored at `wsstat.go:367` but a
  subsequent `net.LookupIP` failure (`:394`) returns without closing it; pumps haven't started,
  so the socket leaks until the caller calls `Close()`. The CLI is shielded (app calls `Close`
  on dial error) but documented library use isn't. Fix: close on the error path, or drop the
  redundant second DNS lookup entirely.
- **[low]** wrappers return the live `ws.result` pointer, not `ExtractResult()`'s copy
  (`wrappers.go:232`) — safe today, fragile; `nextSubscriptionID` `uint64` may misalign on
  32-bit (`wsstat.go:85`).

### C6. Testing — B
Coverage: core 78.0%, app 73.1%, CLI 54.9%. Migration-critical behaviors (clean 1000 vs
abrupt 1006 close, close-grace bound, concurrent close/write races) have high-quality
httptest regression tests passing under `-race`; CLI flag validation is exceptionally
well-tested.
- **[high]** **TLS path — the headline feature — is effectively untested**: `DialTLSContext`
  20.8%, `CertificateDetails` 0%, `printTLSSectionIfPresent` 10.5%; no `httptest.NewTLSServer`
  anywhere. Add a real `wss://` test asserting handshake timings + cert details. (A real TLS
  test would also have caught A1.)
- **[high]** Error paths nearly untested: `wrappers_test.go` is all happy-path; nothing dials a
  refused port / non-WS endpoint / timeout. Add these and assert `%w` wrapping.
- **[medium]** Fixed `localhost:8080` + `DefaultServeMux` + `time.Sleep(250ms)` startup =
  flakiness and no parallelism; switch the echo server to `httptest.NewServer`.
- **[medium]** End-to-end binary/dispatch coverage is Docker-gated (`dev/smoke-test.sh`,
  `soak-test.sh`) and not in CI — `main.go` dispatch is 0%.
- **[low]** `captureStdoutFrom` swaps global `os.Stdout` unsynchronized; `color` package 0%.

### C7. Repo structure & release — B
Strengths: clean layer boundaries enforced by `internal/`; correct `/v3` module; consistent
1.26.3 pin across go.mod/CI/snap; **artifacts are correctly gitignored** (the brief's premise
that `bin/`, the `.snap`, and `coverage.out` are committed is false — `git ls-files` confirms
they're untracked, tree clean); well-guarded release workflow; strong Dockerized dev tooling;
strict golangci-lint v2 config.
- **[high]** Release publishes only host linux/amd64 (`release.yml:208-216` runs `make build`)
  despite an 8-platform `build-all` target and a README advertising downloads. Ship
  darwin/linux/windows × amd64/arm64.
- **[medium]** Thin CI: single OS, single Go version, no `go vet`, no cross-compile smoke, no
  coverage gate. Add `make build-all` + a macOS job (TTY/term code is platform-sensitive).
- **[low]** `release.yml` uses git-cliff with no committed `cliff.toml`; no `toolchain`
  directive in go.mod; stale 2025 per-arch binaries linger in local `bin/` (add `make clean`).

### C8. Documentation — C+
Strengths: **every exported symbol has a godoc comment**; CHANGELOG BREAKING entries +
v2→v3 CLI migration table are excellent; mature ADR setup; good ops docs.
- **[high]** CHANGELOG not cut for 3.0.0 (A4); README example path broken (A5); README
  `go install` path wrong (A5).
- **[medium]** README Go-version claim wrong (A5); **no library-side migration guide** (v2→/v3
  import path, `ReadPong()` removal, gorilla→coder) outside the CHANGELOG.
- **[low]** `SubscriptionStats`/`SubscriptionMessage` lack per-field godoc (unlike `Result`);
  package doc duplicated across `wsstat.go`/`wrappers.go` and omits subscriptions; **no runnable
  `Example*` functions** for pkg.go.dev; `git clone` command missing scheme.

### C9. Security & deps — B
Strengths: TLS verifies by default; `-insecure` is scoped opt-in (`InsecureSkipVerify` only,
keeps wss — note CLAUDE.md/AGENTS.md wrongly say it "switches to ws://"); DNS/IP override keeps
TLS `ServerName` (no MITM hole); resolve values IP-validated; `go vet` clean; `go mod verify`
passes.
- **[medium]** Unbounded read limit (B2 / C4).
- **[low]** No URL-scheme allowlist (`config.go:191-198` accepts `http://`/`https://` →
  plaintext, silently); two stdlib vulns (GO-2026-5039, GO-2026-5037) fixed by building with
  go1.26.4 — bump toolchain + add a `govulncheck` CI gate; dial-error path embeds the full
  unbounded response body in the error (`wsstat.go:357`) — wrap in `io.LimitReader`; sensitive
  headers (Authorization) printed unredacted in `-vv`; `bytedance/sonic` JIT/asm surface pulled
  in transitively via `jkbrsn/jsonrpc` — heavier than a timing tool needs.

### C10. Cross-cutting — B-
The critic confirmed all three load-bearing blockers (A1/A2/A3) with independent code reads and
found no false positives or contradictions among the nine reviews.
- **[high]** Millisecond truncation (A1).
- **[medium]** No API-diff/gorelease CI gate to mechanically enforce the v3 freeze — the
  mechanism that makes all the "decide before freeze" advice durable.
- **[medium]** One `schema_version="1.0"` spans four structurally-distinct JSON payload types
  (`types.go:15`) with no published schema; can't evolve independently. Version per-type or
  publish a JSON Schema.
- **[low]** No debug-log path to the core's zerolog (B8); the `%w`/sentinels/`handleConnectionError`/
  error-envelope/error-tests items across four reviews are **one coordinated error-contract
  workstream**, not four backlog items.

---

## D. Suggested sequencing

1. **Contract-freezing fixes first** (breaking if deferred): A1 (timing precision), A6
   (wrapper ctx/opts + subprotocols), B2 (read limit), B4 (`%w` + sentinels as one change),
   schema versioning decision (C10).
2. **Correctness**: A2 (result race + regression test), C5 connection leak.
3. **Contract completeness**: A3 (JSON error envelope), exit-code policy.
4. **Test the headline feature**: C6 TLS integration test + error-path tests (also catches A1).
5. **Docs/release hygiene** (low-risk, do last): A4 (CHANGELOG), A5 (README), multi-platform
   release assets, govulncheck + toolchain bump, CI matrix.

Nothing here suggests the architecture is wrong — these are polish and contract items on a
fundamentally sound v3. The cluster worth the most attention is **the things that freeze at the
tag**: timing precision, the wrapper/subprotocol API shape, the JSON schema, and the read limit.
