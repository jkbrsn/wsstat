# CI & Release Hardening

**Date:** 2026-07-10
**Commit:** 68c92e0 (various-cleanup)

## Problem Statement

The release workflow holds the repo's most sensitive capabilities — `contents: write` and the snap store credential — while every third-party action it runs is pinned to a mutable major tag. A compromised tag on any of those actions can push tags/releases or exfiltrate `SNAPCRAFT_STORE_CREDENTIALS`. Around that core risk sit four smaller gaps, all of the same "duplicated fact without a drift guard" class this repo otherwise defends against:

- Four quality-gate jobs are duplicated verbatim between `ci-tests.yml` and `release.yml` (~120 lines of drift surface).
- `scripts/check-go-version.sh` runs only at release time, so Go-version drift merges green on PRs and fails weeks later when a release is cut.
- The VERSION validation regex accepts `-rc.1`/`+build` suffixes its own comment says it rejects, which would mint malformed tags.
- The smoke/soak harness — which `dev/README.md` calls part of a flag's definition of done — never runs in CI; the exit-code contract, JSON error envelope, and SIGINT behavior are unverified on every PR.

**Constraints:**

- The release flow's rerun-resilience (tag reconciliation, idempotent re-tagging) must be preserved untouched.
- CI runtime budget: the smoke suite must stay a fast gate (< ~1 min including mock-server startup); soak stays manual/nightly.
- No new external services or secrets.

## Non-Goals

- Release artifact strategy changes (multi-platform binaries, checksums) — separate discussion.
- Soak test in CI — too slow for the PR gate; revisit as a nightly if flakes appear.
- OIDC/`id-token` hardening for the snap upload — snapcraft auth is credential-based today.

## Proposed Solution

Six independent changes, each committable on its own:

1. **Pin third-party actions to commit SHAs** in `release.yml`; pin `govulncheck` to a version in both workflows.
2. **Scope `permissions` per job** — workflow-level `contents: read`, `write` only where tagging/publishing happens.
3. **Fix the VERSION regex** to match its comment (bare `X.Y.Z` only).
4. **Run `check-go-version.sh` on PRs** as a step in the existing lint job.
5. **Extract the quality gates** into a reusable `workflow_call` workflow consumed by both `ci-tests.yml` and `release.yml`.
6. **Run the smoke suite in CI without Docker** via a native mode in `dev/run.sh` (the mock server is a self-contained Go binary that generates its own TLS cert).

## Detailed Design

### 1) Pin actions and tools

**Goal:** No mutable tag resolves inside the workflow that holds write permissions and store credentials.

As of 68c92e0 the third-party pins in `.github/workflows/release.yml` are:

- `orhun/git-cliff-action@v4` — lines 341, 351
- `softprops/action-gh-release@v2` — line 361
- `snapcore/action-build@v1` — line 388
- `snapcore/action-publish@v1` — line 391

Replace each with the full commit SHA of the current release, keeping the tag as a comment for readability:

```yaml
uses: softprops/action-gh-release@<40-char-sha>  # v2.x.y
```

First-party `actions/checkout@v6`, `actions/setup-go@v6`, and `golangci/golangci-lint-action@v9` may stay on tags (lower risk, and SHA-pinning them adds update friction for little gain) — but pinning them too is fine if consistency is preferred.

Also pin the vulnerability scanner in **both** workflows (`release.yml:158`, `ci-tests.yml:60`):

```yaml
- run: go install golang.org/x/vuln/cmd/govulncheck@v1.1.4   # was @latest
```

Optionally add Dependabot config (`.github/dependabot.yml`, `package-ecosystem: github-actions`) so SHA pins get PR-driven updates instead of rotting.

### 2) Per-job permissions

**Goal:** Only the jobs that tag/publish hold `contents: write`.

`release.yml:13-14` currently sets workflow-level `permissions: contents: write`, inherited by every job including lint and tests. Change to:

```yaml
permissions:
  contents: read        # workflow-level default

jobs:
  release:              # tags via gh api (release.yml:267-326) + gh-release publish
    permissions:
      contents: write
  snap:                 # needs only the checkout + the store secret
    permissions:
      contents: read
```

The `snap` job (release.yml:375-396) needs no `write` — `SNAPCRAFT_STORE_CREDENTIALS` (line 393) is a secret, not a token permission. Verify with a full dry run (workflow_dispatch with prerelease) that `gh api` tag reconciliation still works under the job-scoped token.

### 3) VERSION regex

**Goal:** The regex enforces what the comment at `release.yml:50` states ("we only accept bare X.Y.Z for base version").

Line 51, in the `version-bump-check` job:

```bash
# was: ^[0-9]+(\.[0-9]+){2}([\-+][0-9A-Za-z\.-]+)?$
[[ "$version" =~ ^[0-9]+(\.[0-9]+){2}$ ]]
```

Without this, a suffixed VERSION file (e.g. `3.2.0-rc.1`) passes validation, behaves oddly in the `sort -V` bump comparison, and flows into `compute_tag` as the base version — minting `v3.2.0-rc.1` as a "final" tag or `v3.2.0-rc.1-rc.1` with the prerelease box ticked. RC numbering is the workflow input's job, not the file's.

### 4) Go-version alignment on PRs

**Goal:** Go-version drift fails the PR that introduces it, not the release weeks later.

`scripts/check-go-version.sh` verifies go.mod's `toolchain` line against both workflows' `GO_VERSION` and the snapcraft Go tarball. It is wired only into `release.yml:85-91` (`go-version-align` job). Add one step to the existing `golangci` job in `ci-tests.yml` (lines 16-28):

```yaml
      - name: Go version alignment
        run: bash scripts/check-go-version.sh
```

Keep the release-side job as belt-and-braces (it costs seconds), or drop it once the reusable workflow (component 5) carries the check.

### 5) Reusable quality-gate workflow

**Goal:** One definition of the quality gates, consumed by both CI and release.

The duplicated pairs as of 68c92e0:

| ci-tests.yml | release.yml |
|---|---|
| `golangci` (16-28) | `golangci-lint` (127-143) |
| `unit-tests` (30-39) | `test-race` (93-108) |
| `unit-tests-flaky` (41-50) | `test-flaky` (110-125) |
| `govulncheck` (52-61) | `govulncheck` (145-159) |

Create `.github/workflows/quality-gates.yml`:

```yaml
name: Quality gates
on:
  workflow_call:
    inputs:
      fetch-depth:
        type: number
        default: 1
jobs:
  golangci: ...      # moved verbatim, plus the check-go-version step
  unit-tests: ...
  unit-tests-flaky: ...
  govulncheck: ...
```

- `ci-tests.yml` becomes: trigger block + `uses: ./.github/workflows/quality-gates.yml` + the `summary` job (`needs` the called workflow's job via `needs: [quality]`).
- `release.yml` replaces its four duplicated jobs with the same `uses:` and points the downstream `needs:` at it.
- `env` values (`GO_VERSION`, `GOLANGCI_LINT_VERSION`) move into the reusable workflow; `check-go-version.sh` keeps them honest against go.mod.

Gotcha: workflow-level `env` is not inherited across `workflow_call` — define the env inside quality-gates.yml, not in the callers.

### 6) Smoke tests in CI (Docker-free)

**Goal:** The exit-code contract, JSON envelope, TLS verify path, and validation-rejection behavior run on every PR.

The mock server (`dev/mock-server/main.go`) is a standalone Go module that generates its own self-signed cert in-process (`selfSignedCert()`, lines 96-130), serves it at `/ca.pem`, and takes `PORT`/`TLS_PORT` env overrides. `smoke-test.sh` already accepts `WS_URL`/`WSS_URL`/`WSSTAT` env overrides (lines 11-13) and bootstraps TLS trust from `/ca.pem` itself. Docker adds nothing but the healthcheck wait.

Add a `native` mode to `dev/run.sh` (which currently hard-wires `docker compose`):

```bash
# dev/run.sh native [soak]
start_native() {
  (cd dev/mock-server && go build -o "$BIN_DIR/mock-ws" .)
  PORT=17080 TLS_PORT=17443 "$BIN_DIR/mock-ws" &
  MOCK_PID=$!
  trap 'kill "$MOCK_PID" 2>/dev/null' EXIT
  for _ in $(seq 1 50); do
    curl -fsS http://localhost:17080/healthz >/dev/null 2>&1 && return
    sleep 0.1
  done
  echo "mock server failed to become healthy" >&2; exit 1
}
```

Then a Makefile target `smoke-native` and a CI job in quality-gates.yml (or ci-tests.yml directly):

```yaml
  smoke:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
      - uses: actions/setup-go@v6
      - run: make build
      - run: make smoke-native
```

PTY-dependent checks (`--clip`/`--color` via `dev/pty-run.py`) need python3, present on ubuntu runners; if they prove flaky in CI, gate them behind an env var (`SMOKE_SKIP_PTY=1`) rather than dropping the job.

## Affected Files

- `.github/workflows/release.yml` (modify) — SHA pins, per-job permissions, regex, jobs replaced by `workflow_call`
- `.github/workflows/ci-tests.yml` (modify) — govulncheck pin, check-go-version step, jobs replaced by `workflow_call`, smoke job
- `.github/workflows/quality-gates.yml` (new) — reusable gates
- `.github/dependabot.yml` (new, optional) — github-actions ecosystem updates
- `scripts/check-go-version.sh` (unchanged; new call site)
- `dev/run.sh` (modify) — `native` mode
- `Makefile` (modify) — `smoke-native` target
- `CHANGELOG.md` — `(ci)` entries under Unreleased

## Implementation Phases

1. **Quick wins, zero behavior risk** — regex fix (3), govulncheck pins, check-go-version on PRs (4). One commit.
2. **Supply chain** — SHA pins + dependabot (1), per-job permissions (2). Verify with a prerelease dry run of the release workflow.
3. **Structure** — reusable quality-gates workflow (5). Verify both callers go green on a PR and a dry-run dispatch.
4. **Smoke in CI** — native mode + CI job (6). Land last; it has the only real flake risk.

## Acceptance Criteria

- `grep -E 'uses:.*@(v[0-9]|main|master)' .github/workflows/release.yml` matches only `actions/*` and `golangci/*` (or nothing, if pinning those too).
- No workflow-level `contents: write` remains; `release` is the only job with it.
- A PR that bumps `GO_VERSION` in one place fails CI.
- `echo "3.2.0-rc.1" > VERSION` fails `version-bump-check` on a dry run.
- The four gate jobs exist in exactly one file.
- A PR run shows the smoke job passing in under ~90s.
- A full prerelease dispatch of release.yml succeeds end to end after all changes.

## Open Questions

- Keep release re-running the full gates via the reusable workflow (current behavior, belt-and-braces) or switch to asserting the CI run for `$GITHUB_SHA` succeeded? Plan assumes the former.
- Pin first-party `actions/*` to SHAs as well? Cheap with Dependabot in place; default here is tags-only.
