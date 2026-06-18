# 0001. `-o raw` is verbatim for text/binary, compact re-encode for JSON-RPC

Date: 2026-06-18
Commit: acacaac
Status: accepted

## Context

The v3 output rehaul defines `-o raw` as "verbatim payload bytes, nothing added"
(no label, color, timing, or trailing newline) in both measure and stream modes.
The flag-layout design doc states this verbatim guarantee holds for *all* message
kinds.

Implementation exposed a constraint the design did not account for. By the time a
response reaches the output layer (`Client.PrintResponse`, `internal/app/output.go`),
a JSON-RPC response has already been decoded into a `map[string]any` upstream in
`measureJSON` (`internal/app/measurement.go`) — the original frame bytes are gone.
Text and binary responses keep their raw form, because `processTextResponse` returns
the unparsed string for `OutputRaw`. So only the `--rpc-method` path lacks the
original bytes.

Plumbing the raw frame bytes through `measureJSON`, the burst wrappers, and
`MeasurementResult` purely to serve `-o raw --rpc-method` is real cross-layer work
for a corner case: the parseable, structured path for JSON-RPC is `-o json`, which
exists for exactly that. Callers who want byte-fidelity over rpc would use `-o json`,
not `-o raw`.

## Decision

`-o raw` writes payload bytes verbatim for text and binary messages. For
`--rpc-method` responses, where the original frame is no longer available, it emits
the decoded value re-marshaled as **compact JSON** (`rawResponseBytes` in
`internal/app/output.go`). This is documented in the CLI help (`cmd/wsstat/usage.go`)
and the `PrintResponse` doc comment. We do not thread raw frame bytes through the
core to make rpc truly verbatim.

## Consequences

- `-o raw` is byte-exact for text/binary payloads but **not** for JSON-RPC: the
  rpc path re-serializes, so key order and insignificant whitespace are not
  preserved from the wire. A consumer needing wire-faithful rpc bytes must not rely
  on `-o raw`.
- The parseable/structured rpc path is `-o json` (one NDJSON envelope per frame),
  which is unaffected and remains the recommended machine-readable option.
- Making rpc raw truly verbatim later is an additive change (carry original bytes in
  `MeasurementResult`, hand them to `writeRaw`) and would warrant a follow-up ADR.

## Sources

- `internal/app/output.go` — `PrintResponse`, `rawResponseBytes`
- `internal/app/measurement.go` — `measureJSON`, `processTextResponse`
- `cmd/wsstat/usage.go` — `-o raw` help note
