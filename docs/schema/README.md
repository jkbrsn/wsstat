# wsstat JSON output schema

`wsstat ... -o json` emits **newline-delimited JSON (NDJSON)**: one self-describing JSON
object per line. Every record carries a `schema_version` and a `type`, so a consumer can
parse each line independently — including streamed `subscription_message` lines and the
final `error` record on the failure path.

[`wsstat-output-v1.schema.json`](./wsstat-output-v1.schema.json) is a JSON Schema
(draft 2020-12) that validates a single record. It is a `oneOf` over the record types,
discriminated by `type`.

## Record types

| `type`                 | Emitted by                          | Notes |
|------------------------|-------------------------------------|-------|
| `timing`               | `measure` (and `stream` startup)    | per-phase `durations_ms` + cumulative `timeline_ms` |
| `response`             | `measure` with a payload            | decoded response body |
| `subscription_summary` | `stream` (periodic + final)         | per-subscription counts and timing |
| `subscription_message` | `stream`, one per inbound frame     | streamed live |
| `error`                | any mode, runtime failure           | written to stdout so the stream stays parseable |

## Versioning

`schema_version` is a **single monotonic version covering the whole output family as a
unit**:

- A breaking change to *any* record (a removed/renamed field, or a changed value shape)
  bumps the version for *all* records: `1.0` -> `2.0`.
- Additive fields (new optional keys) do **not** bump the version.

Because additive growth keeps the same version, this schema is intentionally **open**:
it does not set `additionalProperties: false`, so output that has gained new optional
fields still validates against the published `1.x` document. Validate the fields you
depend on; do not assume the set is closed.

### Timing precision

`durations_ms` / `timeline_ms` values are **float milliseconds with microsecond
resolution** (rounded to 3 decimals), so a sub-millisecond phase renders non-zero rather
than truncating to `0`. They are declared as `number` (not `integer`); consumers must not
assume integer values. This precision is part of `1.0`.

## Validating

```sh
wsstat measure -o json wss://example.test/ws \
  | while read -r line; do echo "$line" | check-jsonschema --schemafile docs/schema/wsstat-output-v1.schema.json /dev/stdin; done
```

(any draft 2020-12 validator works; the example uses `check-jsonschema`).
