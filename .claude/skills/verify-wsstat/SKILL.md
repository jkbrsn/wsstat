---
name: verify-wsstat
description: Build and drive the wsstat CLI end-to-end against the dev-stack mock server to verify changes at the CLI surface.
---

# Verifying wsstat changes

wsstat is a CLI; its surface is the terminal. Verify by running the built
binary against the repo's own mock server and reading stdout/stderr + exit
codes. Do NOT write a throwaway WebSocket server — `dev/` already provides one
with per-feature endpoints (see the endpoint table in `dev/README.md`).

## Build + server

```bash
make build            # -> ./bin/wsstat
./dev/run.sh up       # Dockerized mock on ws://localhost:17080 and wss://localhost:17443
```

No Docker? Run the mock on the host instead:

```bash
cd dev/mock-server && PORT=17080 TLS_PORT=17443 go run . &
```

## Drive

Pick the endpoint that isolates the changed feature (`/echo`, `/jsonrpc`,
`/stream?rate=N`, `/subscriptions` for stateful multi-frame conversations,
`/slow`, `/headers`, `/close-abrupt`, `/push`, …):

```bash
./bin/wsstat measure -t ping ws://localhost:17080/echo
./bin/wsstat stream -c 2 -o json -t '{"method":"subscribe","subscription":{}}' ws://localhost:17080/subscriptions
```

## Make it stick

A one-off invocation proves the change today; the suites keep proving it:

- `dev/smoke-test.sh` (`make smoke`) — one assertion per feature. A new feature
  gets one `check` line.
- `dev/soak-test.sh` (`make soak`) — the combination matrix. A new flag gets a
  POSITIVE row per alias, a REJECT row per validation rule (a rule that exits 0
  is a silent accept), and an EFFECT check if its only failure mode is being
  ignored.
- New server behavior needed? Add an endpoint to `dev/mock-server/main.go` and
  document it in the `dev/README.md` table.

Both suites also run standalone against a host-run mock:
`WSSTAT=./bin/wsstat ./dev/smoke-test.sh`.

## Gotchas

- Flags must come before the URL; the subcommand must come first.
- `-q`/`-v`/`--body`/`--clip` are rejected under `-o json|raw` (axis purity).
- Usage errors exit 2, runtime errors exit 1; under `-o json` runtime errors
  emit a `{"type":"error"}` envelope on stdout.
- Use `ws://` (17080) to skip TLS; `wss://` (17443) is self-signed — verify via
  `SSL_CERT_FILE=<(curl http://localhost:17080/ca.pem)` or `-k`.
- Bare hosts default to `wss://`.
