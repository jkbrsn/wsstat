# Check Mode Tier 2: Adversarial Conformance Probes

- **Date**: 2026-07-18
- **Status**: Speculative — a possible future direction, not scheduled or committed

## Idea

`wsstat check <url>` runs observational RFC 6455 checks: every exchange is well-formed, so
it can only observe how a server handles a correct client. Autobahn-grade conformance
requires the opposite — sending malformed frames and asserting the server rejects them
correctly (close 1002/1007): RSV bits without a negotiated extension, reserved opcodes,
fragmented/oversized control frames, invalid and reserved close codes, unmasked client
frames, invalid UTF-8 (fail-fast) in text frames, interleaved fragmentation violations,
unsolicited pongs.

A Tier 2 probe set would close that gap while keeping wsstat's point-at-a-URL,
seconds-not-minutes character.

## Why it is not straightforward

`coder/websocket` cannot send malformed frames: frame construction (`writeFrame`, opcodes,
RSV, masking) is unexported and there is no client-side hijack. Tier 2 therefore needs a
raw transport path — manual HTTP/1.1 upgrade plus a minimal hand-rolled RFC 6455 frame
codec over the raw `net.Conn`. wsstat already captures that conn post-TLS
(`captureNetConn`, `wsstat.go`), so the dial/TLS/timing instrumentation is reusable, but
the codec and a parallel read path are a substantial new layer — roughly the size of the
entire Tier 1 change. That cost is the main reason this may never be built.

## Design sketch (if built)

- `wsstat check --probe <url>`: opt-in, because firing malformed frames at production
  endpoints is borderline hostile. Without the flag, `check` stays purely observational.
- One fresh connection per probe; sequential, same politeness posture as Tier 1.
- Probe results appear as a fourth report group (`probe.*`) in the existing `check_report`
  schema — additive, no schema version bump. Tier 1's report schema and exit-code contract
  (fail → exit 3) were designed so this slots in without breaking changes.
- Letter grades (SSL-Labs style) stay out of scope until the check set stabilizes.

## Open questions

- Is the hand-rolled codec worth owning, or does a maintained library with a raw-frame
  write path appear (or a `coder/websocket` upstream change land) first?
- How much of Autobahn's case matrix is worth replicating before the tool stops being
  "fast and polite" and starts being a fuzzer?
