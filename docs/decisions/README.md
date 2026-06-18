# Architecture Decision Records (ADRs)

Decisions that survive plan churn live here. Plan docs and design docs may rewrite freely; ADRs are the durable trail.

## Scope

ADRs capture architectural choices: CLI contracts, output schemas, API seams, scoping decisions that constrain future work. Skip the small stuff — file renames, comment style, local variable shapes, transient cleanups. If reversing the choice would require follow-up ADRs and migrations, write it. If reversing it is a one-line edit, don't.

## Format

Lean MADR. Each ADR has three sections:

- **Context** — what was true before the decision; the problem or constraint.
- **Decision** — what we chose, in one or two sentences.
- **Consequences** — what follows: what becomes easier, what becomes harder, what's now load-bearing.

Optional: a **Sources** line linking the docs or code that originally captured the decision (so the ADR is auditable, not just asserted).

## Conventions

- **Numbering.** Sequential, zero-padded to four digits: `0001-...`, `0002-...`. Numbers never reused. Gaps are fine.
- **Filename.** `NNNN-kebab-case-title.md`.
- **Status.** Implicit `accepted` unless stated otherwise. Use `proposed` for ADRs drafted ahead of implementation; flip to `accepted` after review against the shipped code. Use `superseded by NNNN` in a top-line note when replaced; do not edit the original decision.
- **Dates.** Inside the doc, not the filename. ISO format.
- **Commit.** Optional `Commit: <short-sha>` line under `Date:`. Names the last commit before the ADR was created — anchors any "current state" or "today's behavior" prose to a specific snapshot of the tree, so future readers can `git show <sha>:path` to read the code the ADR was written against.
- **Immutability.** Once accepted, the body of an ADR is not rewritten — corrections happen in a follow-up ADR. Typo fixes and link updates are fine.

## Template

```markdown
# NNNN. Title

Date: YYYY-MM-DD
Commit: <short-sha>   # optional; anchor for "current state" prose
Status: accepted

## Context

What was true before; the problem or constraint that forced the decision.

## Decision

What we chose. One or two sentences.

## Consequences

What follows. What becomes easier, what becomes harder, what's now load-bearing.

## Sources

- `path/to/source.go` — relevant symbol
```

## Index

| # | Title | Status |
|---|---|---|
| 0001 | [`-o raw` is verbatim for text/binary, compact re-encode for JSON-RPC](0001-raw-output-not-verbatim-for-rpc.md) | accepted |
