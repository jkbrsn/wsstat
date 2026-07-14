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

`run.sh up` runs in the **foreground** and blocks until Ctrl+C, which fires its
teardown trap (`docker compose down`). To drive the binary from the same session
you must background it — and then **you** own the teardown (see below):

```bash
./dev/run.sh up &     # background so you can drive; you must tear it down when done
until curl -fsS http://localhost:17080/healthz >/dev/null 2>&1; do sleep 0.5; done
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

## Tear down

Always stop the stack you started — a backgrounded `run.sh up` keeps blocking and
holds ports 17080/17443 otherwise. Ctrl+C (SIGINT) is the clean teardown: it fires
run.sh's trap, which runs `docker compose down`. From a non-interactive session,
send it that signal — do **not** just `docker compose down`, which leaves the
`run.sh` process orphaned:

```bash
pkill -INT -f 'dev/run.sh up'    # Ctrl+C the mock; its trap tears the stack down
# host-run mock instead? stop the go process:
pkill -f 'dev/mock-server'
```

Or skip the manual lifecycle entirely: `make smoke` / `make soak` build, run, and
tear the stack down in one shot — prefer these unless you need ad-hoc drive commands.

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
