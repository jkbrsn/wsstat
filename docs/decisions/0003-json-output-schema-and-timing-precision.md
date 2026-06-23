# 0003. JSON output schema: single monotonic version, float-ms timing precision

Date: 2026-06-22
Commit: a5281c9
Status: accepted

## Context

`-o json` emits newline-delimited JSON (one self-describing object per line) across five
record types: `timing`, `response`, `subscription_summary`, `subscription_message`, and the
runtime-failure `error` envelope. v3 freezes this as a public contract, which forces two
questions that are breaking to defer past the tag:

1. **Versioning.** How is `schema_version` scoped, and what counts as a bump? Options ranged
   from per-record versions to a single family version.
2. **Timing precision.** The timing builders truncated every phase to whole milliseconds
   (`d/time.Millisecond`, `d.Milliseconds()`, typed `*int64`), so a sub-millisecond phase
   rendered `0` in both text and JSON. Fixing this changes the *value shape* of the
   `durations_ms` / `timeline_ms` fields (integer-looking to fractional). If integer ms ships
   in `1.0`, adding decimals later is a breaking change for typed consumers and would force
   `1.0 -> 2.0` on a brand-new format.

The two are coupled: the precision decision must land *before* `1.0` ships, or it cannot be
folded into `1.0`.

## Decision

- **Single monotonic `schema_version`** covering the whole output family as a unit. A breaking
  change to *any* record (removed/renamed field, changed value shape) bumps it for *all*
  records (`1.0 -> 2.0`); additive optional fields do not bump it. It is stamped on every
  record, including each streamed `subscription_message` line, so records are self-describing.
- **Timing values are float milliseconds at microsecond resolution** (rounded to 3 decimals),
  in both text and JSON. The JSON `*_ms` fields are typed `*float64` and the published schema
  declares them as `number` (not `integer`). Key names and the nil-for-zero (`omitempty`)
  semantics are unchanged. This is baked into `1.0`.
- The contract is published as a JSON Schema (draft 2020-12) at
  `docs/schema/wsstat-output-v1.schema.json`, with the version semantics in
  `docs/schema/README.md`. The schema is intentionally **open** (no `additionalProperties:
  false`) so additive fields, which keep the same version, still validate against the `1.x`
  document.

## Consequences

- Adding an optional field anywhere in the output is a non-event for consumers and validators:
  same version, still valid. Renaming or re-shaping a field is a whole-family `2.0`, which
  would warrant a new schema document and a follow-up ADR.
- Consumers must treat `*_ms` values as floating point; assuming integers is now wrong. Whole
  millisecond values still serialize without a fractional part (`50`, not `50.0`), so the
  common case reads naturally.
- The open schema cannot catch typo'd or stray fields — it validates the fields a consumer
  depends on, not a closed set. That is the deliberate cost of the additive-no-bump rule.
- `TestSchemaDocDrift` (`internal/app/schema_doc_test.go`) pins the schema's declared version
  and record-type set to the code, so a new record type or a version change cannot land
  without updating the published schema. Full payload validation is left to external
  validators (no validator dependency is added to the module).

## Sources

- `internal/app/types.go` — record structs, `JSONSchemaVersion`
- `internal/app/formatting.go` — `msFloat` / `msString` / `msPtr` (3-decimal rounding)
- `internal/app/output.go`, `internal/app/subscription.go` — record builders
- `docs/schema/wsstat-output-v1.schema.json`, `docs/schema/README.md`
