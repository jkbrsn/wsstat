#!/usr/bin/env bash
# Verify the Go version is aligned across the repo. The toolchain line in
# go.mod is canonical; CI workflows and the snap tarball must match it.
set -euo pipefail

cd "$(dirname "$0")/.."

canon=$(sed -nE 's/^toolchain go([0-9.]+)$/\1/p' go.mod)
[[ -n "$canon" ]] || { echo "::error::no 'toolchain goX.Y.Z' line in go.mod"; exit 1; }

# go.mod's 'go' directive minor must match the toolchain minor
goline=$(sed -nE 's/^go ([0-9.]+)$/\1/p' go.mod)
[[ "${canon%.*}" == "$goline" ]] || \
  { echo "::error::go.mod: 'go $goline' minor != toolchain '$canon'"; exit 1; }

fail=0
check() { # <label> <found>
  if [[ "$2" != "$canon" ]]; then
    echo "::error::$1 = '$2', expected '$canon' (from go.mod toolchain)"
    fail=1
  fi
}

for f in .github/workflows/release.yml .github/workflows/ci-tests.yml; do
  check "$f GO_VERSION" "$(sed -nE 's/^[[:space:]]*GO_VERSION:[[:space:]]*([0-9.]+).*$/\1/p' "$f")"
done

check "snapcraft.yaml go tarball" \
  "$(sed -nE 's#.*go\.dev/dl/go([0-9.]+)\.linux-amd64.*#\1#p' snap/snapcraft.yaml)"

[[ "$fail" == 0 ]] && echo "Go version aligned across repo: $canon"
exit "$fail"
