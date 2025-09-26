# Rename Burst To Count Plan

## Overview
The goal is to replace the existing `-burst` option with a more general `-count` flag while aligning subscription workflows so the flag represents "number of interactions" across all modes. Subscription runs should default to streaming indefinitely (count=0) while one-shot flows retain a default count of 1. The existing `-subscribe-once` flag will be deprecated, but left functional for now.

## Detailed Steps

1. **Introduce `-count` flag and maintain temporary aliasing for `-burst`**
   - Add a new `-count` flag to `cmd/wsstat/main.go` (defaulting to 1) and parse `-burst` as a deprecated alias that forwards its value to `count`.
   - Update `app.Client` and downstream structs to replace `Burst` with `Count`, preserving backwards-compatible field names via TODOs if necessary.
   - Ensure every reference in core code (`wsstat`, wrappers, client, tests) switches to the new name.
   - Inject TODO comments referencing the eventual removal of the alias.

2. **Default `-count` behavior based on mode**
   - In CLI parsing, detect when `-subscribe` (or `-subscribe-once`) is active and override a zero-value `count` to 0, otherwise keep the default at 1.
   - Propagate these defaults into `app.Client` so validation and downstream logic don’t rely on CLI-specific behavior.
   - Document the new defaults in README and usage text.

3. **Align subscription behavior with `-count`**
   - Modify validation so subscription modes accept `count >= 0`, with 0 representing unlimited streaming.
   - Update the subscription loop to stop after emitting exactly `count` messages when `count > 0`.
   - Ensure `StreamSubscriptionOnce` delegates to the common logic with `count == 1`.
   - Verify `SubscribeOnce` in the library honors the same semantics.

4. **Deprecate `-subscribe-once`**
   - Leave the flag wired but mark it as deprecated in CLI usage and help text.
   - Add TODO comments near the flag definition and client logic to remove it in a future breaking release (reference issue/project name if available).
   - Update validation to treat `-subscribe-once` as shorthand for `-subscribe -count 1`, ensuring the combination produces identical behavior.

5. **Documentation updates**
   - Replace all mentions of `-burst` with `-count` in README, comments, usage output, and other docs.
   - Clearly explain the new semantics: `count` controls send-and-wait cycles for standard flows and limits received events in subscription mode, with `0` meaning unlimited.
   - Note the deprecation of `-subscribe-once` and recommend `-subscribe -count 1` instead.

6. **Testing and validation adjustments**
   - Update unit tests in `internal/app` and `wsstat` to reflect the renamed flag and new count semantics.
   - Add coverage ensuring `-subscribe` with `count=0` streams indefinitely (mock via limited loop) and `count>0` stops after the specified number of messages.
   - Extend CLI-level tests (if present) or add integration checks verifying the alias `-burst` still works but emits a warning or TODO reference.

7. **Additional considerations**
   - Decide whether to emit a runtime warning when users supply `-burst` so they notice the upcoming rename.
   - Evaluate whether exported APIs or wrappers should be renamed (e.g., `MeasureLatencyBurst`) or if they stay untouched until a broader API version bump.
   - Plan follow-up work to remove deprecated flags and aliases once users have transitioned.

